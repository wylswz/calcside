import { test, expect, request, type APIRequestContext, type Page } from '@playwright/test'
import { execFileSync, spawn, type ChildProcess } from 'node:child_process'
import { mkdtemp, readFile, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createServer } from 'node:net'

const repo = fileURLToPath(new URL('../../', import.meta.url))
let server: ChildProcess | undefined
let directory: string
let client: APIRequestContext
let consoleURL: string
let output = ''
const instances: string[] = []

async function port(): Promise<number> {
  const listener = createServer()
  await new Promise<void>((resolve) => listener.listen(0, '127.0.0.1', resolve))
  const address = listener.address()
  if (!address || typeof address === 'string') throw new Error('No test port')
  await new Promise<void>((resolve, reject) => listener.close((error) => error ? reject(error) : resolve()))
  return address.port
}

test.beforeAll(async () => {
  test.setTimeout(120_000)
  directory = await mkdtemp(join(tmpdir(), 'calcside-browser-'))
  const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => !key.startsWith('CALCSIDE_') && !key.startsWith('ATLAS_')))
  const binary = join(directory, 'calcside')
  const database = join(directory, 'test.db')
  execFileSync('go', ['build', '-o', binary, './cmd/calcside'], { cwd: repo, env, stdio: 'pipe' })
  execFileSync('atlas', ['migrate', 'apply', '--env', 'sqlite'], { cwd: repo, env: { ...env, ATLAS_DB_URL: `sqlite://${database}` }, stdio: 'pipe' })
  const listenPort = await port()
  consoleURL = `http://console.localhost:${listenPort}`
  const transportURL = `http://127.0.0.1:${listenPort}`
  server = spawn(binary, ['serve', '--dev', '--store', 'sqlite', '--dsn', database, '--addr', `127.0.0.1:${listenPort}`, '--console-origin', consoleURL, '--artifact-preview-base-url', `http://preview.localhost:${listenPort}`, '--artifact-allow-scripts'], { cwd: repo, env, stdio: ['ignore', 'pipe', 'pipe'] })
  server.stdout?.on('data', (chunk) => { output = (output + String(chunk)).slice(-64_000) })
  server.stderr?.on('data', (chunk) => { output = (output + String(chunk)).slice(-64_000) })
  client = await request.newContext({ baseURL: transportURL, extraHTTPHeaders: { 'X-Requested-With': 'calcside' } })
  await expect.poll(async () => {
    if (server?.exitCode !== null) throw new Error(`Test server exited: ${output}`)
    try { return (await client.get('/healthz', { timeout: 1000 })).status() } catch { return 0 }
  }, { timeout: 30_000 }).toBe(200)
})

test.afterEach(async () => {
  for (const id of instances.splice(0)) await client.delete(`/api/v1/instances/${id}`)
})

test.afterAll(async () => {
  await client?.dispose()
  if (server && server.exitCode === null) {
    const exited = new Promise<void>((resolve) => server!.once('exit', () => resolve()))
    server.kill('SIGTERM')
    await exited
  }
  if (directory) await rm(directory, { recursive: true })
})

async function instance(code: string): Promise<string> {
  const created = await client.post('/api/v1/instances', { data: { capabilities: { fs: {} } } })
  expect(created.status()).toBe(201)
  const id = (await created.json()).instance.id as string
  instances.push(id)
  const executed = await client.post(`/api/v1/instances/${id}/exec`, { data: { code } })
  expect(executed.status()).toBe(200)
  expect((await executed.json()).error).toBeNull()
  return id
}

async function openPreview(page: Page, name: string): Promise<Page> {
  const opened = page.waitForEvent('popup')
  await page.getByRole('link', { name: `Preview ${name} (new tab)`, exact: true }).click()
  const viewer = await opened
  await viewer.bringToFront()
  await expect(viewer.getByRole('heading', { name, exact: true })).toBeVisible()
  return viewer
}

test('CSV cells stay literal, leading zeros survive, and downloads complete', async ({ page }) => {
  const id = await instance(`fs.write("report.csv", csv.format([["id", "value"], ["A", "001"], ["B", "<img src=x onerror=alert(1)>"]]))
fs.write("empty.csv", "")`)
  await page.goto(`${consoleURL}/instances/${id}`)
  const viewer = await openPreview(page, 'report.csv')
  await expect(viewer.getByRole('cell', { name: '001', exact: true })).toBeVisible()
  await expect(viewer.getByRole('cell', { name: '<img src=x onerror=alert(1)>', exact: true })).toBeVisible()
  expect(await viewer.locator('td img').count()).toBe(0)
  await viewer.getByRole('checkbox', { name: 'First row is header' }).check()
  await expect(viewer.getByRole('columnheader', { name: 'id', exact: true })).toBeVisible()
  const downloaded = viewer.waitForEvent('download')
  await viewer.getByRole('button', { name: 'Download file', exact: true }).click()
  const download = await downloaded
  expect(download.suggestedFilename()).toBe('report.csv')
  expect(await download.failure()).toBeNull()
  const filename = await download.path()
  expect(filename).not.toBeNull()
  expect(await readFile(filename!, 'utf8')).toBe('id,value\nA,001\nB,<img src=x onerror=alert(1)>\n')
  await viewer.close()
  await page.getByRole('checkbox', { name: 'Select report.csv' }).check()
  const archive = page.waitForEvent('download')
  await page.getByRole('button', { name: /Download selected ZIP/ }).click()
  expect((await archive).suggestedFilename()).toBe('artifacts.zip')
  const empty = await openPreview(page, 'empty.csv')
  await expect(empty.getByText('CSV has no records.', { exact: true })).toBeVisible()
  await empty.close()
})

test('static HTML is sanitized; explicit JavaScript cannot read parent, storage, or fetch', async ({ page, context }) => {
  const report = `<h1>Report</h1><p id="state">not executed</p><canvas id="chart" width="100" height="40"></canvas><script>
    window.probes = {};
    window.violations = [];
    document.addEventListener('securitypolicyviolation', (event) => window.violations.push(event.violatedDirective));
    try { parent.document.body.dataset.broken = 'yes'; window.probes.parent = true; } catch { window.probes.parent = false; }
    try { window.probes.cookies = document.cookie; } catch { window.probes.cookies = 'blocked'; }
    try { localStorage.setItem('probe', 'bad'); window.probes.storage = true; } catch { window.probes.storage = false; }
    fetch(${JSON.stringify(consoleURL + '/api/v1/me')}, {credentials:'include'}).then(() => window.probes.fetch = true).catch(() => window.probes.fetch = false);
    const canvas = document.getElementById('chart').getContext('2d');
    canvas.fillStyle = '#0000ff'; canvas.fillRect(0, 0, 50, 40);
    document.getElementById('state').textContent = 'script ran';
  </script>`
  const id = await instance(`fs.write("report.html", ${JSON.stringify(report)})`)
  await context.addCookies([{ name: 'console_probe', value: 'console-only', domain: 'console.localhost', path: '/', sameSite: 'Lax' }])
  await page.goto(`${consoleURL}/instances/${id}`)
  const viewer = await openPreview(page, 'report.html')
  const iframe = viewer.locator('iframe[title="Artifact preview"]')
  await expect(iframe).toHaveAttribute('sandbox', '')
  await expect(viewer.frameLocator('iframe[title="Artifact preview"]').getByText('not executed', { exact: true })).toBeVisible()
  await viewer.getByRole('button', { name: 'Enable JavaScript', exact: true }).click()
  await expect(viewer.getByRole('heading', { name: 'Run this report’s JavaScript?' })).toBeVisible()
  await viewer.keyboard.press('Escape')
  await expect(viewer.getByRole('dialog')).not.toBeVisible()
  await expect(viewer.getByRole('button', { name: 'Enable JavaScript', exact: true })).toBeFocused()
  await viewer.getByRole('button', { name: 'Enable JavaScript', exact: true }).click()
  await expect(viewer.getByRole('dialog')).toBeVisible()
  await expect(iframe).toHaveCSS('pointer-events', 'none')
  await expect(iframe).toHaveAttribute('sandbox', '')
  await viewer.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))))
  const interactive = viewer.waitForResponse((response) => response.url().endsWith('/artifacts/preview') && response.request().postDataJSON()?.mode === 'interactive', { timeout: 5000 })
  await viewer.getByRole('button', { name: 'Run interactive preview', exact: true }).click()
  expect((await (await interactive).json()).interactive).toBe(true)
  await expect(viewer.getByRole('button', { name: 'Stop JavaScript', exact: true })).toBeVisible()
  await expect(iframe).toHaveAttribute('sandbox', 'allow-scripts')
  await expect(viewer.frameLocator('iframe[title="Artifact preview"]').getByText('script ran', { exact: true })).toBeVisible()
  const frame = viewer.frames().find((candidate) => candidate.url().includes('.preview.localhost'))!
  expect(frame).toBeDefined()
  expect(new URL(frame.url()).hostname).not.toBe(new URL(consoleURL).hostname)
  await expect.poll(() => frame.evaluate('window.probes')).toEqual({ parent: false, cookies: 'blocked', storage: false, fetch: false })
  expect(await frame.evaluate('window.violations')).toContain('connect-src')
  expect(await frame.evaluate('document.getElementById("chart").getContext("2d").getImageData(5, 5, 1, 1).data[2]')).toBe(255)
  expect(await viewer.locator('body').getAttribute('data-broken')).toBeNull()
  const forbidden = new URL(frame.url())
  forbidden.pathname = '/api/v1/me'
  const response = await client.get(forbidden.pathname + forbidden.search, { headers: { Host: forbidden.host } })
  expect(response.status()).toBe(404)
  await viewer.getByRole('button', { name: 'Source', exact: true }).click()
  await expect(iframe).toHaveCount(0)
  await expect(viewer.getByLabel('File source')).toContainText('window.probes')
  await viewer.getByRole('button', { name: 'Preview', exact: true }).click()
  await expect(iframe).toHaveAttribute('sandbox', '')
  await viewer.close()
})

test('preview truncation is explicit and unsupported files fail ZIP without a download', async ({ page }) => {
  const id = await instance(`rows = [["n", "value"]]
for n in range(220):
    rows.append([str(n), "text"])
fs.write("many.csv", csv.format(rows))
fs.write("unsupported.png", "not an image")`)
  await page.goto(`${consoleURL}/instances/${id}`)
  const viewer = await openPreview(page, 'many.csv')
  await expect(viewer.getByText(/Table preview is limited to 200 rows/)).toBeVisible()
  await expect(viewer.getByText(/221 records parsed/)).toBeVisible()
  await viewer.close()
  let downloads = 0
  page.on('download', () => { downloads++ })
  await page.getByRole('button', { name: 'Download folder ZIP', exact: true }).click()
  await expect(page.getByText(/unsupported artifact file type/)).toBeVisible()
  expect(downloads).toBe(0)
})

test('HTML preview opens a dedicated page without changing the workspace', async ({ page }, testInfo) => {
  const report = `<div style="font-family:Arial,sans-serif;max-width:920px;margin:0 auto;padding:36px;color:#111">
    <p style="font-size:12px;color:#666">OPERATIONS / SAMPLE REPORT</p><h1 style="font-size:36px">Quarterly report</h1>
    <p style="color:#666">A bounded snapshot of reconciliation results.</p>
    <table style="width:100%;border-collapse:collapse;margin:32px 0"><tr><td><p>RECORDS</p><h2>1,284</h2></td><td><p>MATCHED</p><h2>1,260</h2></td><td><p>REVIEW REQUIRED</p><h2 style="color:#e1251b">24</h2></td></tr></table>
    <h2>Exceptions by source</h2><svg width="100%" viewBox="0 0 660 150"><rect x="0" y="10" width="396" height="28" fill="#0033ff"/><rect x="0" y="60" width="220" height="28" fill="#0033ff"/><rect x="0" y="110" width="132" height="28" fill="#ffc700"/></svg>
    <p style="color:#666;font-size:12px">Orders: 13 / Payments: 7 / Refunds: 4</p>
    <h2>Next steps</h2><p>Review the exception list before closing the reporting period. Source files remain available in this instance.</p>
  </div>`
  const id = await instance(`fs.mkdir("output")
fs.write("report.html", ${JSON.stringify(report)})
fs.write("reconciliation.csv", "id,status\\n001,matched\\n")
fs.write("notes.txt", "Review the exception list before closing the reporting period.")`)
  await page.goto(`${consoleURL}/instances/${id}`)
  const previewLink = page.getByRole('link', { name: 'Preview report.html (new tab)', exact: true })
  await expect(previewLink).toBeVisible()
  await expect(previewLink).toHaveAttribute('target', '_blank')
  const originalCode = await page.locator('.cm-content').innerText()
  const opened = page.waitForEvent('popup')
  await previewLink.focus()
  await page.keyboard.press('Enter')
  const viewer = await opened
  await viewer.bringToFront()
  await expect(viewer).toHaveURL(new RegExp(`/instances/${id}/preview\\?path=`))
  await expect(viewer.getByRole('heading', { name: 'report.html', exact: true })).toBeVisible()
  await expect(viewer.frameLocator('iframe[title="Artifact preview"]').getByRole('heading', { name: 'Quarterly report' })).toBeVisible()
  expect(await viewer.evaluate(() => window.opener === null)).toBe(true)
  expect(new URL(viewer.url()).searchParams.has('ticket')).toBe(false)
  await expect(page).toHaveURL(`${consoleURL}/instances/${id}`)
  await expect(page.locator('iframe')).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Expand preview' })).toHaveCount(0)
  expect(await page.locator('.cm-content').innerText()).toBe(originalCode)
  const iframe = viewer.locator('iframe[title="Artifact preview"]')
  const bounds = await iframe.boundingBox()
  expect(bounds!.width).toBeGreaterThan(1000)
  expect(bounds!.height).toBeGreaterThan(650)
  await page.screenshot({ path: testInfo.outputPath('files-desktop.png'), fullPage: true })
  await viewer.screenshot({ path: testInfo.outputPath('preview-desktop.png') })
  await viewer.getByRole('button', { name: 'Mobile width', exact: true }).click()
  await expect.poll(async () => (await iframe.boundingBox())!.width).toBeLessThan(400)
  await viewer.getByRole('button', { name: 'Fit to window', exact: true }).click()
  await expect.poll(async () => (await iframe.boundingBox())!.width).toBeGreaterThan(1000)
  await viewer.emulateMedia({ colorScheme: 'dark', reducedMotion: 'reduce' })
  await expect(viewer.getByRole('button', { name: 'Enable JavaScript', exact: true })).toHaveCSS('color', 'rgb(245, 245, 245)')
  await viewer.screenshot({ path: testInfo.outputPath('preview-dark.png') })
  await viewer.emulateMedia({ colorScheme: 'light' })
  await viewer.setViewportSize({ width: 390, height: 844 })
  expect(await viewer.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  await expect(viewer.getByRole('button', { name: 'Download file' })).toBeVisible()
  await expect(viewer.getByRole('contentinfo')).toBeInViewport({ ratio: 1 })
  await expect(viewer.getByRole('banner')).toHaveCount(1)
  await viewer.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))))
  await viewer.screenshot({ path: testInfo.outputPath('preview-mobile.png') })
  await viewer.close()
})


test('source fallback works when rendered HTML is disabled', async ({ page, context }) => {
  await context.route('**/api/v1/artifacts/config', async (route) => {
    const response = await route.fetch()
    await route.fulfill({ response, json: { ...await response.json(), html_enabled: false, interactive_enabled: false } })
  })
  const id = await instance(`fs.write("report.html", "<h1>Source only</h1><script>document.title='bad'</script>")`)
  await page.goto(`${consoleURL}/instances/${id}`)
  const viewer = await openPreview(page, 'report.html')
  await expect(viewer.getByText(/HTML rendering is not configured/)).toBeVisible()
  await expect(viewer.getByLabel('File source')).toContainText('<h1>Source only</h1>')
  await expect(viewer.getByRole('button', { name: 'Preview', exact: true })).toBeDisabled()
  await expect(viewer.locator('iframe')).toHaveCount(0)
  await expect(viewer).toHaveTitle('report.html · calcside')
  await viewer.close()
})

test('preview errors are recoverable and expired snapshots are removed', async ({ page, context }) => {
  let fail = true
  await context.route('**/api/v1/instances/*/artifacts/preview', async (route) => {
    if (fail) {
      fail = false
      await route.fulfill({ status: 429, json: { error: { code: 'too_many', message: 'Preview capacity reached.' } } })
    } else await route.continue()
  })
  const id = await instance(`fs.write("report.html", "<h1>Recovery report</h1>")`)
  await page.goto(`${consoleURL}/instances/${id}`)
  const viewer = await openPreview(page, 'report.html')
  await expect(viewer.getByRole('heading', { name: 'Preview unavailable' })).toBeVisible()
  await expect(viewer.getByRole('alert')).toHaveText('Preview capacity reached.')
  await viewer.getByRole('button', { name: 'Try again' }).click()
  await expect(viewer.frameLocator('iframe').getByRole('heading', { name: 'Recovery report' })).toBeVisible()
  await viewer.clock.install()
  await viewer.reload()
  await expect(viewer.frameLocator('iframe').getByRole('heading', { name: 'Recovery report' })).toBeVisible()
  await viewer.clock.fastForward(121_000)
  await expect(viewer.getByRole('heading', { name: 'Preview expired' })).toBeVisible()
  await expect(viewer.locator('iframe')).toHaveCount(0)
  await viewer.getByRole('button', { name: 'Source', exact: true }).click()
  await expect(viewer.getByLabel('File source')).toContainText('Recovery report')
  await viewer.close()
})

test('file actions support nested and Unicode paths without navigating the editor', async ({ page }) => {
  const id = await instance(`fs.mkdir("output")
fs.write("output/报告 #1.HTML", "<h1>Nested report</h1>")
fs.write("notes.txt", "notes")`)
  await page.goto(`${consoleURL}/instances/${id}`)
  await page.getByRole('button', { name: 'Open folder output', exact: true }).click()
  const viewer = await openPreview(page, '报告 #1.HTML')
  expect(new URL(viewer.url()).searchParams.get('path')).toBe('/work/output/报告 #1.HTML')
  await expect(viewer.frameLocator('iframe').getByRole('heading', { name: 'Nested report' })).toBeVisible()
  const download = page.waitForEvent('download')
  await page.getByRole('button', { name: 'Download 报告 #1.HTML', exact: true }).click()
  const delivered = await download
  expect(delivered.suggestedFilename()).toBe('报告 #1.HTML')
  expect(await delivered.failure()).toBeNull()
  await page.getByRole('checkbox', { name: 'Select 报告 #1.HTML' }).check()
  await page.getByRole('navigation', { name: 'File directory' }).getByRole('button', { name: '/work', exact: true }).click()
  await page.getByRole('checkbox', { name: 'Select notes.txt', exact: true }).check()
  await expect(page.getByText('2 selected', { exact: true })).toBeVisible()
  const zipped = page.waitForEvent('download')
  await page.getByRole('button', { name: 'Download selected ZIP (2)', exact: true }).click()
  expect((await zipped).suggestedFilename()).toBe('artifacts.zip')
  await viewer.close()
})


test('the standalone viewer still requires console authentication', async ({ page, context }) => {
  await context.route('**/api/v1/auth/config', async (route) => {
    const response = await route.fetch()
    await route.fulfill({ response, json: { ...await response.json(), dev_mode: false } })
  })
  await context.route('**/api/v1/me', (route) => route.fulfill({ status: 401, json: { error: { code: 'unauthorized', message: 'Login required.' } } }))
  let previews = 0
  context.on('request', (request) => { if (request.url().endsWith('/artifacts/preview')) previews++ })
  const id = await instance(`fs.write("report.html", "<h1>Private report</h1>")`)
  await page.goto(`${consoleURL}/instances/${id}/preview?path=%2Fwork%2Freport.html`)
  await expect(page).toHaveURL(`${consoleURL}/login`)
  await expect(page.locator('iframe')).toHaveCount(0)
  expect(previews).toBe(0)
})

test('local CSS, deferred scripts and module dependencies render from one HTML snapshot', async ({ page }) => {
  const html = `<!doctype html><html><head>
    <link rel="stylesheet" href="assets/report.css">
    <script>window.events=[]; document.addEventListener('DOMContentLoaded', () => window.events.push('ready'));</script>
    <script src="assets/base.js"></script>
    <script defer src="assets/first.js"></script><script defer src="assets/second.js"></script>
    <script type="module" src="assets/main.mjs"></script><script async src="assets/async.js"></script>
    </head><body><h1 id="state" class="report">not executed</h1><script>window.events.push('body');</script></body></html>`
  const files: Record<string, string> = {
    'report/index.html': html,
    'report/assets/report.css': '@import "./nested/base.css"; .report { color: rgb(1, 2, 3) }',
    'report/assets/nested/base.css': '.report { font-size: 25px }',
    'report/assets/base.js': 'var reportBase = 10; window.events.push("base");',
    'report/assets/first.js': 'window.events.push(document.getElementById("state") ? "defer1" : "too-early"); var reportValue = reportBase + 2;',
    'report/assets/second.js': 'window.events.push(reportValue === 12 ? "defer2" : "no-global");',
    'report/assets/main.mjs': 'import {answer} from "./nested/helper.js"; import data from "./data.json"; import "./module.css"; window.events.push("module"); document.getElementById("state").textContent = `value ${answer + data.value}`; import("./late.js").then(m => window.lateValue = m.value);',
    'report/assets/nested/helper.js': 'export const answer = 40;',
    'report/assets/data.json': '{"value":2}',
    'report/assets/module.css': '.report {letter-spacing: 1px} .report::after {content:"</style><script>window.escapeRan=true</script>"}',
    'report/assets/late.js': 'export const value = 99;',
    'report/assets/async.js': 'window.asyncRan = true; window.unicodeText = "你好，图表";',
    'report/unrelated.png': 'unreferenced binary extensions do not block preview',
  }
  const id = await instance(Object.entries(files).map(([name, content]) => `fs.write(${JSON.stringify(name)}, ${JSON.stringify(content)})`).join('\n'))
  await page.goto(`${consoleURL}/instances/${id}/preview?path=${encodeURIComponent('/work/report/index.html')}`)
  const content = page.frameLocator('iframe[title="Artifact preview"]')
  await expect(content.getByText('not executed', { exact: true })).toHaveCSS('color', 'rgb(1, 2, 3)')
  await expect(content.getByText('not executed', { exact: true })).toHaveCSS('font-size', '25px')
  await page.getByRole('button', { name: 'Enable JavaScript', exact: true }).click()
  await expect(page.getByRole('dialog')).toBeVisible()
  await expect(page.locator('iframe')).toHaveCSS('pointer-events', 'none')
  await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))))
  const interactive = page.waitForResponse((response) => response.url().endsWith('/artifacts/preview') && response.request().postDataJSON()?.mode === 'interactive')
  await page.getByRole('button', { name: 'Run interactive preview', exact: true }).click()
  expect((await (await interactive).json()).interactive).toBe(true)
  await expect(page.getByRole('dialog')).not.toBeVisible()
  await expect(content.getByText('value 42', { exact: true })).toHaveCSS('letter-spacing', '1px')
  const frame = page.frames().find((candidate) => candidate.url().includes('.preview.localhost'))!
  await expect.poll(() => frame.evaluate('window.events')).toEqual(['base', 'body', 'defer1', 'defer2', 'module', 'ready'])
  await expect.poll(() => frame.evaluate('window.lateValue')).toBe(99)
  await expect.poll(() => frame.evaluate('window.asyncRan')).toBe(true)
  expect(await frame.evaluate('window.unicodeText')).toBe('你好，图表')
  expect(await frame.evaluate('window.escapeRan')).toBeUndefined()
  expect(await frame.evaluate('Array.from(document.scripts).filter(s => s.src).every(s => s.src.startsWith("data:"))')).toBe(true)
  expect(await frame.locator('link[rel="stylesheet"]').count()).toBe(0)
  await page.getByRole('button', { name: 'Source', exact: true }).click()
  await expect(page.locator('iframe')).toHaveCount(0)
  await expect(page.getByLabel('File source')).toContainText('src="assets/main.mjs"')
})
