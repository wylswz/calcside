import { test, expect, request } from '@playwright/test'
import { execFileSync, spawn, type ChildProcess } from 'node:child_process'
import { mkdtemp, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { createServer } from 'node:net'

// Application lifecycle under real Vite (StrictMode) + scratch
// API/SQLite: create → scaffold → test → publish → ready → live invoke;
// a draft edit does not change the published response until republish.

const repo = join(import.meta.dirname, '..', '..')
let directory = ''
let api: ChildProcess | null = null
let vite: ChildProcess | null = null
let client: Awaited<ReturnType<typeof request.newContext>> | null = null
let viteURL = ''

async function port(): Promise<number> {
  const listener = createServer()
  await new Promise<void>((resolve) => listener.listen(0, '127.0.0.1', resolve))
  const address = listener.address()
  if (!address || typeof address === 'string') throw new Error('No test port')
  await new Promise<void>((resolve, reject) => listener.close((e) => e ? reject(e) : resolve()))
  return address.port
}

test.beforeAll(async () => {
  test.setTimeout(240_000)
  directory = await mkdtemp(join(tmpdir(), 'calcside-app-'))
  const env = Object.fromEntries(Object.entries(process.env).filter(([k]) => !k.startsWith('CALCSIDE_') && !k.startsWith('ATLAS_')))
  const binary = join(directory, 'calcside')
  const database = join(directory, 'test.db')
  execFileSync('go', ['build', '-o', binary, './cmd/calcside'], { cwd: repo, env, stdio: 'pipe' })
  execFileSync('atlas', ['migrate', 'apply', '--env', 'sqlite'], {
    cwd: repo, env: { ...env, ATLAS_DB_URL: `sqlite://${database}` }, stdio: 'pipe',
  })
  const apiPort = await port()
  const vitePort = await port()
  const apiURL = `http://127.0.0.1:${apiPort}`
  viteURL = `http://127.0.0.1:${vitePort}`
  api = spawn(binary, ['serve', '--dev', '--store', 'sqlite', '--dsn', database,
    '--addr', `127.0.0.1:${apiPort}`, '--console-origin', viteURL],
    { cwd: repo, env, stdio: 'pipe' })
  vite = spawn('node', [join(repo, 'web', 'node_modules', 'vite', 'bin', 'vite.js'),
    '--host', '127.0.0.1', '--port', String(vitePort)],
    { cwd: join(repo, 'web'), env: { ...env, VITE_API_TARGET: apiURL }, stdio: 'pipe' })
  let out = ''
  for (const p of [api, vite]) {
    p?.stdout?.on('data', (c) => { out = (out + String(c)).slice(-32_000) })
    p?.stderr?.on('data', (c) => { out = (out + String(c)).slice(-32_000) })
  }
  client = await request.newContext({ baseURL: apiURL, extraHTTPHeaders: { 'X-Requested-With': 'calcside' } })
  await expect.poll(async () => {
    if (api?.exitCode !== null) throw new Error(`api exited: ${out}`)
    try { return (await client!.get('/healthz', { timeout: 1000 })).status() } catch { return 0 }
  }, { timeout: 30_000 }).toBe(200)
  await expect.poll(async () => {
    if (vite?.exitCode !== null) throw new Error(`vite exited: ${out}`)
    try {
      const c = await request.newContext({ baseURL: viteURL })
      const st = await c.get('/', { timeout: 1000 }).then((r) => r.status()).catch(() => 0)
      await c.dispose()
      return st
    } catch { return 0 }
  }, { timeout: 30_000 }).toBe(200)
})

test.afterAll(async () => {
  const stop = (p: ChildProcess | null) => new Promise<void>((resolve) => {
    if (!p) return resolve()
    const t = setTimeout(() => { try { p.kill('SIGKILL') } catch { /* gone */ } }, 5000)
    p.on('exit', () => { clearTimeout(t); resolve() })
    p.kill('SIGTERM')
  })
  await Promise.all([stop(vite), stop(api)])
  if (client) await client.dispose()
  if (directory) await rm(directory, { recursive: true, force: true })
})

test.describe.configure({ mode: 'serial' })

// helper: wait until the app's status flips ready (real scheduler —
// never assert ready from the 202 alone)
async function waitReady(id: string, timeoutMs = 90_000) {
  await expect.poll(async () => {
    const r = await client!.get(`/api/v1/applications/${id}`)
    return (await r.json()).application.status
  }, { timeout: timeoutMs }).toBe('ready')
}

test('a) create -> scaffold -> test -> publish -> ready -> live invoke', async ({ page }) => {
  await page.goto(`${viteURL}/applications/new`)
  await page.locator('#app-name').fill('Greeter')
  await page.getByRole('button', { name: 'Create application' }).click()
  await page.waitForURL(/\/applications\/app_/)
  const id = page.url().split('/').pop()!
  await expect(page.locator('.app-cm')).toContainText('def main(input)')

  await page.getByRole('button', { name: 'Save & Test' }).click()
  await expect(page.locator('.app-out-pre')).toContainText('Hello, world', { timeout: 30_000 })

  await page.getByRole('button', { name: 'Save & Publish' }).click()
  await waitReady(id)
  // the Invoke button is always rendered; it enables once ready
  await expect(page.getByRole('button', { name: 'Invoke published' })).toBeEnabled({ timeout: 10_000 })
  await page.getByRole('button', { name: 'Invoke published' }).click()
  await expect(page.locator('.app-out-pre')).toContainText('Hello, world', { timeout: 30_000 })
})

test('b) draft change stays unpublished until republish', async ({ page }) => {
  await page.goto(`${viteURL}/applications/new`)
  await page.locator('#app-name').fill('Counter')
  await page.getByRole('button', { name: 'Create application' }).click()
  await page.waitForURL(/\/applications\/app_/)
  const id = page.url().split('/').pop()!

  // publish the scaffold, wait ready, note its output
  await page.getByRole('button', { name: 'Save & Publish' }).click()
  await waitReady(id)
  const inv0 = await client!.post(`/api/v1/applications/${id}/invoke`, {
    headers: { 'Content-Type': 'application/json' },
    data: `{"name":"a"}`,
  })
  expect(inv0.status()).toBe(200)
  const out0 = (await inv0.json()).output_json
  expect(String(out0)).toContain('Hello, a')

  // change the draft — published output must not move yet
  const code = await page.locator('.app-cm .cm-content').first()
  await code.click()
  await page.keyboard.press('Meta+A')
  await page.keyboard.type('def main(input): return {"message": "v2"}')
  await page.getByRole('button', { name: 'Save draft' }).click()
  await page.waitForTimeout(500)
  const inv1 = await client!.post(`/api/v1/applications/${id}/invoke`, {
    headers: { 'Content-Type': 'application/json' },
    data: `{"name":"a"}`,
  })
  expect((await inv1.json()).output_json).toContain('Hello, a')

  // republish — the live output flips
  await page.getByRole('button', { name: 'Save & Publish' }).click()
  await waitReady(id)
  await expect.poll(async () => {
    const r = await client!.post(`/api/v1/applications/${id}/invoke`, {
      headers: { 'Content-Type': 'application/json' },
      data: `{"name":"a"}`,
    })
    return (await r.json()).output_json ?? ''
  }, { timeout: 90_000 }).toContain('"v2"')
})

test('c) mid-save edits abort the chained publish', async ({ page }) => {
  await page.goto(`${viteURL}/applications/new`)
  await page.locator('#app-name').fill('Delay')
  await page.getByRole('button', { name: 'Create application' }).click()
  await page.waitForURL(/\/applications\/app_/)
  const id = page.url().split('/').pop()!

  // delay the PUT so an edit lands mid-flight
  await page.route('**/api/v1/applications/*', async (route) => {
    if (route.request().method() === 'PUT') {
      await new Promise((r) => setTimeout(r, 400))
    }
    await route.continue()
  })
  const cm = page.locator('.app-cm .cm-content').first()
  await cm.click()
  await page.keyboard.press('Meta+A')
  await page.keyboard.type('def main(input): return {"first":1}')
  const publish = page.getByRole('button', { name: 'Save & Publish' }).click()
  await page.waitForTimeout(80)
  await cm.click()
  await page.keyboard.press('Meta+A')
  await page.keyboard.type('def main(input): return {"second":2}')
  await publish
  // the in-flight edit must abort the publish chain — the PUT saved the
  // FIRST code (v2); nothing was published.
  await page.waitForTimeout(1500)
  const r = await client!.get(`/api/v1/applications/${id}`)
  const app = (await r.json()).application
  expect(app.published_version).toBe(0)
  expect(app.version).toBe(2)
  expect(app.code).toContain('"first":1')
  // the latest code is preserved in the editor
  await expect(page.locator('.app-cm')).toContainText('"second":2')
  // a follow-up save must NOT self-conflict — baseVersion advanced to 2
  await page.getByRole('button', { name: 'Save draft' }).click()
  await expect.poll(async () => {
    const rr = await client!.get(`/api/v1/applications/${id}`)
    return (await rr.json()).application.code
  }, { timeout: 15_000 }).toContain('"second":2')
  // then publish + invoke actually serves the SECOND code
  await page.getByRole('button', { name: 'Save & Publish' }).click()
  await waitReady(id)
  const iv = await client!.post(`/api/v1/applications/${id}/invoke`, {
    headers: { 'Content-Type': 'application/json' }, data: '{}',
  })
  expect((await iv.json()).output_json).toContain('"second":2')
})

test('c2) input-only mid-save change aborts and keeps the input', async ({ page }) => {
  const r = await client!.post('/api/v1/applications', { data: { name: 'InOnly' } })
  const id = (await r.json()).application.id
  await page.goto(`${viteURL}/applications/${id}`)
  await expect(page.locator('.app-status')).toBeVisible({ timeout: 10_000 })
  await page.route('**/api/v1/applications/*', async (route) => {
    if (route.request().method() === 'PUT') {
      await new Promise((rr) => setTimeout(rr, 400))
    }
    await route.continue()
  })
  const cm = page.locator('.app-cm .cm-content').first()
  await cm.click()
  await page.keyboard.press('Meta+A')
  await page.keyboard.type('def main(input): return {"c":1}')
  const saveP = page.getByRole('button', { name: 'Save & Test' }).click()
  await page.waitForTimeout(80)
  await page.locator('#app-input').fill('{"changed":true}')
  await saveP
  await page.waitForTimeout(1500)
  const app = (await (await client!.get(`/api/v1/applications/${id}`)).json()).application
  expect(app.published_version).toBe(0)
  await expect(page.locator('#app-input')).toHaveValue('{"changed":true}')
  // a follow-up Save & Test works on the saved baseline
  await page.getByRole('button', { name: 'Save & Test' }).click()
  await expect(page.locator('.app-out-pre')).toContainText('"c":1', { timeout: 30_000 })
})

test('d) SPA navigation resets scope; B never receives A draft', async ({ page }) => {
  const a1 = await client!.post('/api/v1/applications', { data: { name: 'One' } })
  const id1 = (await a1.json()).application.id
  const a2 = await client!.post('/api/v1/applications', { data: { name: 'Two' } })
  const id2 = (await a2.json()).application.id

  const spa = (pid: string) =>
    page.evaluate((p) => {
      history.pushState(null, '', `/applications/${p}`)
      dispatchEvent(new PopStateEvent('popstate'))
    }, pid)

  await page.goto(`${viteURL}/applications/${id1}`)
  const cm = page.locator('.app-cm .cm-content').first()
  await cm.click()
  await page.keyboard.press('Meta+A')
  await page.keyboard.type('def main(input): return {"from_a":1}')
  await page.locator('#app-input').fill('{"name":"scoped-one"}')
  // same-component navigation — the keyed remount must reset the draft
  await spa(id2)
  await expect(page.locator('#app-input')).toHaveValue(/\{\s*"name": "world"/, { timeout: 10_000 })
  await expect(page.locator('.app-cm')).toContainText('def main(input)')
  await expect(page.locator('.app-cm')).not.toContainText('from_a')
  // saving B writes B's own code, never A's
  await page.getByRole('button', { name: 'Save draft' }).click()
  const b = (await (await client!.get(`/api/v1/applications/${id2}`)).json()).application
  expect(b.code).not.toContain('from_a')
  // back to A — its edits were scoped and restored
  await spa(id1)
  await expect(page.locator('.app-cm')).toContainText('from_a', { timeout: 10_000 })
  await expect(page.locator('#app-input')).toHaveValue('{"name":"scoped-one"}')
})

test('e) external write + my save -> 409, edits retained, reload offered', async ({ page }) => {
  const r = await client!.post('/api/v1/applications', { data: { name: 'Race' } })
  const id = (await r.json()).application.id
  await page.goto(`${viteURL}/applications/${id}`)
  // wait until the page has actually loaded the app (version 1 base)
  await expect(page.locator('.app-status')).toBeVisible({ timeout: 10_000 })
  // another session bumps the draft server-side
  const app0 = (await (await client!.get(`/api/v1/applications/${id}`)).json()).application
  await client!.put(`/api/v1/applications/${id}`, {
    data: { version: app0.version, name: 'Race', code: 'def main(i): return {"remote":true}', spec: app0.spec },
  })
  // my editor still holds version 1 — its save must conflict
  const cm = page.locator('.app-cm .cm-content').first()
  await cm.click()
  await page.keyboard.press('Meta+A')
  await page.keyboard.type('def main(input): return {"mine":1}')
  await page.getByRole('button', { name: 'Save draft' }).click()
  await expect(page.getByText('Saved elsewhere')).toBeVisible({ timeout: 15_000 })
  // edits must not be silently replaced; Cancel keeps them
  await expect(page.locator('.app-cm')).toContainText('"mine":1')
  await page.getByRole('button', { name: 'Cancel' }).click()
  await expect(page.locator('.app-cm')).toContainText('"mine":1')
  // save again -> conflict again -> Confirm reload replaces explicitly
  await page.getByRole('button', { name: 'Save draft' }).click()
  await expect(page.getByText('Saved elsewhere')).toBeVisible({ timeout: 15_000 })
  await page.getByRole('button', { name: 'Reload' }).click()
  await expect(page.locator('.app-cm')).toContainText('remote', { timeout: 10_000 })
})

test('f) instance creation still works via the UI', async ({ page }) => {
  await page.goto(`${viteURL}/instances/new`)
  await page.getByRole('button', { name: 'Create instance' }).click()
  await page.waitForURL(/\/instances\/ins_/, { timeout: 20_000 })
})

test('g) mobile populated layout, no horizontal overflow', async ({ page }) => {
  const r = await client!.post('/api/v1/applications', { data: { name: 'Mobile' } })
  const id = (await r.json()).application.id
  // populate output via the page itself, then resize — a viewport
  // change does not remount, so the populated state is preserved
  await page.goto(`${viteURL}/applications/${id}`)
  await page.getByRole('button', { name: 'Save & Test' }).click()
  await expect(page.locator('.app-out-pre')).toContainText('Hello, world', { timeout: 30_000 })
  await page.setViewportSize({ width: 390, height: 844 })
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth)
  expect(overflow).toBe(false)
  await page.screenshot({ path: '/tmp/calcside-ep14-logs/application-ui-mobile.png', fullPage: true })
  await page.setViewportSize({ width: 1440, height: 1000 })
  await expect(page.locator('.app-out-pre')).toContainText('Hello, world')
  await page.screenshot({ path: '/tmp/calcside-ep14-logs/application-ui-desktop.png', fullPage: true })
})

test('h) kind switch: pristine navigates, edited name asks', async ({ page }) => {
  await page.goto(`${viteURL}/applications/new`)
  await expect(page.locator('#app-name')).toBeVisible({ timeout: 10_000 })
  // pristine -> switch to Instance needs no confirm
  await page.locator('.kind-tabs a', { hasText: 'Instance' }).first().click()
  await page.waitForURL(/\/instances\/new/)
  // edited name -> switching back asks
  await page.goto(`${viteURL}/applications/new`)
  await page.locator('#app-name').fill('half-done')
  await page.locator('.kind-tabs a', { hasText: 'Instance' }).first().click()
  await expect(page.getByText('Discard draft')).toBeVisible()
  await page.getByRole('button', { name: 'Cancel' }).click()
  await expect(page.locator('#app-name')).toHaveValue('half-done')
})
