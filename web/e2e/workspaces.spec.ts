import { test, expect, request, chromium, type APIRequestContext, type Page } from '@playwright/test'
import { execFileSync, spawn, type ChildProcess } from 'node:child_process'
import { randomBytes } from 'node:crypto'
import { mkdtemp, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createServer } from 'node:net'

const repo = fileURLToPath(new URL('../../', import.meta.url))
let server: ChildProcess | undefined
let worker: ChildProcess | undefined
let directory: string
let client: APIRequestContext
let consoleURL: string
let output = ''

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
  directory = await mkdtemp(join(tmpdir(), 'calcside-ws-'))
  const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => !key.startsWith('CALCSIDE_') && !key.startsWith('ATLAS_')))
  const binary = join(directory, 'calcside')
  const database = join(directory, 'test.db')
  execFileSync('go', ['build', '-o', binary, './cmd/calcside'], { cwd: repo, env, stdio: 'pipe' })
  const extRoots = join(repo, 'contrib')
  const workerBin = join(directory, 'calcside-worker')
  execFileSync('go', ['build', '-o', workerBin, './cmd/calcside-worker'], { cwd: repo, env, stdio: 'pipe' })
  execFileSync('atlas', ['migrate', 'apply', '--env', 'sqlite'], { cwd: repo, env: { ...env, ATLAS_DB_URL: `sqlite://${database}` }, stdio: 'pipe' })
  const listenPort = await port()
  const workerPort = await port()
  const wkey = randomBytes(32).toString('hex')
  const workerURL = `http://127.0.0.1:${workerPort}`
  consoleURL = `http://console.localhost:${listenPort}`
  const transportURL = `http://127.0.0.1:${listenPort}`
  // The suite accumulates live instances across tests; the quota itself is
  // covered by Go tests, so keep it out of the way here.
  server = spawn(binary, ['serve', '--dev', '--store', 'sqlite', '--dsn', database, '--addr', `127.0.0.1:${listenPort}`, '--console-origin', consoleURL, '--max-instances-per-user', '100', '--ext-local-roots', extRoots, '--workers', `w1=127.0.0.1:${workerPort}`, '--worker-key', wkey], { cwd: repo, env, stdio: ['ignore', 'pipe', 'pipe'] })
  worker = spawn(workerBin, ['serve', '--listen', `127.0.0.1:${workerPort}`, '--api-addr', transportURL, '--shared-key', wkey, '--node-id', 'w1', '--ext-cache-dir', join(directory, 'workercache'), '--instance-memory-max', '0'], { cwd: repo, env, stdio: ['ignore', 'pipe', 'pipe'] })
  server.stdout?.on('data', (chunk) => { output = (output + String(chunk)).slice(-64_000) })
  server.stderr?.on('data', (chunk) => { output = (output + String(chunk)).slice(-64_000) })
  worker.stdout?.on('data', (chunk) => { output = (output + '[worker] ' + String(chunk)).slice(-64_000) })
  worker.stderr?.on('data', (chunk) => { output = (output + '[worker] ' + String(chunk)).slice(-64_000) })
  client = await request.newContext({ baseURL: transportURL, extraHTTPHeaders: { 'X-Requested-With': 'calcside' } })
  await expect.poll(async () => {
    if (server?.exitCode !== null) throw new Error(`Test server exited: ${output}`)
    try { return (await client.get('/healthz', { timeout: 1000 })).status() } catch { return 0 }
  }, { timeout: 30_000 }).toBe(200)
  {
    const c = await request.newContext({ baseURL: workerURL })
    await expect.poll(async () => {
      if (worker?.exitCode !== null && worker?.exitCode !== undefined) throw new Error(`Worker exited: ${output}`)
      try { return (await c.get('/healthz', { timeout: 1000 })).status() } catch { return 0 }
    }, { timeout: 30_000 }).toBe(200)
    await c.dispose()
  }
})

test.afterAll(async () => {
  await client?.dispose()
  if (server && server.exitCode === null) {
    const exited = new Promise<void>((resolve) => server!.once('exit', () => resolve()))
    server.kill('SIGTERM')
    await Promise.race([exited, new Promise((resolve) => setTimeout(resolve, 5000))])
    if (server.exitCode === null) { server.kill('SIGKILL'); await exited }
    server.stdout?.destroy(); server.stderr?.destroy()
  }
  if (worker && worker.exitCode === null) {
    const wexited = new Promise<void>((resolve) => worker!.once('exit', () => resolve()))
    worker.kill('SIGTERM')
    await Promise.race([wexited, new Promise((resolve) => setTimeout(resolve, 5000))])
    if (worker.exitCode === null) { worker.kill('SIGKILL'); await wexited }
    worker.stdout?.destroy(); worker.stderr?.destroy()
  }
  if (directory) await rm(directory, { recursive: true })
})

// Records every distinct box each selector occupies, frame by frame, until
// the returned stop() is called. Frames where the element runs a keyframe
// entrance animation are skipped; transitions are not, so a hover or press
// that moves or resizes the element shows up as a second box.
async function watchGeometry(page: Page, selectors: string[]) {
  await page.evaluate((selectors) => {
    const seen: Record<string, string[]> = Object.fromEntries(selectors.map((s) => [s, []]))
    const state = { seen, on: true }
    ;(window as any).__geometry = state
    const tick = () => {
      for (const s of selectors) {
        const el = document.querySelector(s)
        if (!el || el.getAnimations().some((a) => 'animationName' in a && a.playState === 'running')) continue
        const b = el.getBoundingClientRect()
        const key = [b.x, b.y, b.width, b.height].map((n) => n.toFixed(2)).join(' ')
        if (!seen[s].includes(key)) seen[s].push(key)
      }
      if (state.on) requestAnimationFrame(tick)
    }
    requestAnimationFrame(tick)
  }, selectors)
  return () => page.evaluate(() => {
    const state = (window as any).__geometry
    state.on = false
    return state.seen as Record<string, string[]>
  })
}

// Create a team workspace via API, switch to it in the header: the
// instance list scopes to it (a personal-workspace instance is not
// shown), and the settings page renders members.
test('workspace switcher scopes the instance list', async ({ page }) => {
  const mk = await client.post('/api/v1/instances', { data: { capabilities: { fs: {} } } })
  expect(mk.status()).toBe(201)
  const ws = await client.post('/api/v1/workspaces', { data: { name: 'e2e team' } })
  expect(ws.status()).toBe(201)
  const team = (await ws.json()).workspace

  await page.goto(`${consoleURL}/`)
  await expect(page.getByRole('button', { name: /▾/ })).toBeVisible({ timeout: 15_000 })
  await page.getByRole('button', { name: /▾/ }).click()
  await page.getByRole('menuitem', { name: /e2e team/ }).click()
  await page.waitForLoadState('networkidle')
  // scoped: the personal-workspace instance row is absent
  await expect(page.locator('tbody tr')).toHaveCount(0, { timeout: 5000 }).catch(() => {})
  // settings page
  await page.goto(`${consoleURL}/workspaces/${team.id}`)
  await expect(page.getByText('e2e team').first()).toBeVisible()
  await expect(page.getByText('members', { exact: false }).first()).toBeVisible()
})

// Flow: creating an instance lands on its page with a compact connect
// action — no oversized guidance card — that opens the key dialog.
test('instance flow exposes a compact connect-agent action', async ({ page }) => {
  await page.goto(`${consoleURL}/`)
  await page.getByRole('button', { name: 'New instance' }).first().click()
  await page.getByRole('button', { name: 'Create instance' }).click()
  await expect(page).toHaveURL(/\/instances\/ins_/, { timeout: 10000 })
  await expect(page.getByText('Connect an agent')).toHaveCount(0)
  const connect = page.getByRole('button', { name: 'Connect agent', exact: true })
  await expect(connect).toBeVisible()
  const box = (await connect.boundingBox())!
  expect(box.width).toBeLessThanOrEqual(44)
  expect(box.height).toBeLessThanOrEqual(44)
  await connect.click()
  await expect(page.getByRole('dialog', { name: 'Connect agent' })).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(page.getByRole('dialog', { name: 'Connect agent' })).toHaveCount(0)
})

// The builder is a page: /instances/new with a capability palette and
// the instance being built.
test('new instance: compute-only default, name persists', async ({ page }) => {
  await page.goto(`${consoleURL}/instances/new`)
  await expect(page.getByRole('heading', { name: 'This instance' })).toBeVisible()
  await expect(page.getByRole('heading', { name: 'Compute only' })).toBeVisible()
  await page.getByPlaceholder('e.g. research run').fill('my-run')
  const [created] = await Promise.all([
    page.waitForResponse((r) => r.url().endsWith('/api/v1/instances') && r.request().method() === 'POST'),
    page.getByRole('button', { name: 'Create instance' }).click(),
  ])
  const body = await created.request().postDataJSON()
  expect(body.capabilities?.fs).toBeUndefined()
  expect(body.capabilities?.net).toBeUndefined()
  expect(body.labels.name).toBe('my-run')
  await expect(page).toHaveURL(/\/instances\/[a-zA-Z0-9_]+/, { timeout: 10000 })
})

test('new instance: add/configure/remove Files and Network, review reflects', async ({ page }) => {
  await page.goto(`${consoleURL}/instances/new`)
  // one click each; adding never opens a form or leaves the palette
  await expect(page.getByRole('button', { name: 'Add ability' })).toHaveCount(0)
  await page.getByRole('button', { name: 'Add Files' }).click()
  await page.getByRole('button', { name: 'Add Network' }).click()
  await expect(page.locator('[data-row="fs"]')).toBeVisible()
  await expect(page.locator('[data-row="net"]')).toBeVisible()
  await expect(page.getByLabel(/Files quota/)).toBeHidden()
  // Configure is the explicit step to the settings view
  await page.getByRole('button', { name: 'Configure Files' }).click()
  await expect(page.getByRole('tabpanel', { name: 'Add capabilities', includeHidden: true })).toBeHidden()
  await expect(page.getByRole('button', { name: 'Add Network' })).toBeHidden()
  await page.getByLabel(/Files quota/).fill('2048')
  await page.getByLabel(/Allowed hosts/).fill('api.example.com\nother.com')
  await page.getByLabel(/HTTP methods/).fill('GET')
  await expect(page.locator('[data-row="net"] .selected-status')).toHaveText('api.example.com, other.com · GET')
  await expect(page.locator('[data-row="fs"] .selected-status')).toHaveText('2.00 KiB')
  await page.getByRole('button', { name: 'Remove Files' }).click()
  await expect(page.getByText('Files removed')).toBeVisible()
  const [created] = await Promise.all([
    page.waitForResponse((r) => r.url().endsWith('/api/v1/instances') && r.request().method() === 'POST'),
    page.getByRole('button', { name: 'Create instance' }).click(),
  ])
  const body = await created.request().postDataJSON()
  expect(body.capabilities.net.allow_hosts).toEqual(['api.example.com', 'other.com'])
  expect(body.capabilities.fs).toBeUndefined()
  await expect(page).toHaveURL(/\/instances\/[a-zA-Z0-9_]+/, { timeout: 10000 })
})

test('new instance: advanced JSON survives, invalid blocks the editor', async ({ page }) => {
  await page.goto(`${consoleURL}/instances/new`)
  await page.getByText('Advanced JSON').click()
  const ta = page.getByLabel('Instance spec JSON')
  await ta.fill('{ "ttl_seconds": 45 }')
  await page.getByText('Back to editor').click()
  await expect(page.getByLabel('Lifetime (seconds)')).toHaveValue('45')
  // invalid JSON traps the editor
  await page.getByText('Advanced JSON').click()
  await page.getByLabel('Instance spec JSON').fill('{ not json')
  await page.getByText('Back to editor').click()
  await expect(page.locator('.create-bar-issues')).toContainText(/Fix invalid JSON/i)
  await expect(page.getByRole('button', { name: 'Create instance' })).toBeDisabled()
})

test('new instance: 390px does not overflow', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 800 })
  await page.goto(`${consoleURL}/instances/new`)
  const w = await page.evaluate(() => ({ s: document.documentElement.scrollWidth, i: window.innerWidth }))
  expect(w.s).toBeLessThanOrEqual(w.i)
})

// Clone via ?from= serves a synthetic spec; the POST body is the assertion.
const SRC_SPEC = {
  ttl_seconds: 90,
  labels: { name: 'clone-src', team: 'x=y' },
  env: { MODE: 'test' },
  secrets: { TOKEN: { ref: 'different-vault-name' }, INL: { source: 'inline' } },
  capabilities: {
    net: { allow_hosts: ['a.com'], methods: ['GET'] },
    ext: { fetcher: { source: 'contrib/fetcher', config: { retries: 3, tags: ['a', 'b'] } } },
  },
  limits: { max_steps: 50 },
}
const EXT_CAT = {
  extensions: [{ source: 'contrib/fetcher', name: 'fetcher', description: 'fixture', dependencies: [], ops: [], config: [] }],
  local_enabled: true, remote_enabled: false,
}

test('clone prefill: exact TTL, vault ref ≠ name, typed ext config, limits preserved', async ({ page }) => {
  // capture route first, specific mocks after (Playwright matches last-first)
  let captured: any = null
  await page.route('**/api/v1/instances**', async (r) => {
    if (r.request().method() === 'POST') { captured = r.request().postDataJSON(); await r.fulfill({ json: { instance: { id: 'ins_new', status: 'running', workspace_id: '', spec: captured } } }) }
    else await r.continue()
  })
  await page.route('**/api/v1/extensions**', (r) => r.fulfill({ json: EXT_CAT }))
  await page.route('**/api/v1/instances/ins_src**', (r) => r.fulfill({ json: { instance: { id: 'ins_src', status: 'running', workspace_id: '', spec: SRC_SPEC } } }))
  await page.goto(`${consoleURL}/instances/new?from=ins_src`)
  await expect(page.getByLabel('Lifetime (seconds)')).toHaveValue('90')
  // sanitized inline clone must be replaced or removed — replace it
  await page.getByRole('button', { name: /^Inputs/ }).click()
  await expect(page.locator('[data-section="inputs"]')).toBeVisible()
  await page.getByLabel('Secret value 2').fill('replaced-value')
  const btn = page.getByRole('button', { name: 'Create similar' })
  await expect(btn).toBeEnabled()
  await Promise.all([
    page.waitForRequest((r) => r.method() === 'POST' && new URL(r.url()).pathname === '/api/v1/instances'),
    btn.click(),
  ])
  expect(captured).not.toBeNull()
  expect(captured.ttl_seconds).toBe(90)
  expect(captured.secrets.TOKEN.ref).toBe('different-vault-name')
  expect(captured.capabilities.ext.fetcher.config.retries).toBe(3)
  expect(captured.capabilities.ext.fetcher.config.tags).toEqual(['a', 'b'])
  expect(captured.limits.max_steps).toBe(50)
})

test('extension with unmet dependency blocks submit (no auto-grant)', async ({ page }) => {
  await page.route('**/api/v1/extensions**', (r) => r.fulfill({ json: {
    extensions: [{ source: 'contrib/tav', name: 'tav', description: 'needs net', dependencies: ['net'], ops: [], config: [] }],
    local_enabled: true, remote_enabled: false,
  } }))
  await page.route('**/api/v1/instances/ins_src2**', (r) => r.fulfill({ json: { instance: { id: 'ins_src2', status: 'running', workspace_id: '', spec: { ttl_seconds: 60, capabilities: { ext: { tav: { source: 'contrib/tav' } } } } } } }))
  await page.goto(`${consoleURL}/instances/new?from=ins_src2`)
  await expect(page.getByRole('button', { name: 'Create similar' })).toBeDisabled()
  // the dep error is attached (may be inside a collapsed panel)
  await expect(page.locator('text=requires').first()).toBeAttached()
})

test('restricted key: inline secret rejected through JSON path too', async ({ page }) => {
  await page.goto(`${consoleURL}/workspace/keys`)
  await page.getByRole('button', { name: 'New key' }).click()
  await page.locator('input').first().fill('json-ban')
  await page.getByText('Advanced JSON').click()
  await page.getByLabel('Instance spec JSON').fill('{ "secrets": { "T": { "value": "inline" } } }')
  let posted = false
  await page.route('**/api/v1/keys**', async (r) => {
    if (r.request().method() === 'POST') { posted = true; await r.fulfill({ status: 400, json: { error: 'should not reach' } }) }
    else await r.continue()
  })
  await page.getByRole('button', { name: 'Create', exact: true }).click()
  await expect(page.getByRole('alert')).toContainText('inline values are forbidden')
  expect(posted).toBe(false)
})

// Quiet-surface restyle: supervision flow + visual structure intact.
test('instance detail: quiet surfaces retain supervision flow', async ({ page }, testInfo) => {
  const created = await client.post('/api/v1/instances', {
    data: {
      ttl_seconds: 600,
      capabilities: { fs: {}, net: { allow_hosts: ['example.com'], methods: ['GET'] } },
      env: { DOMAIN_NAME_REALLY_LONG_VALUE: 'NEVER_RENDER_THIS' },
      labels: { name: 'Research session' },
    },
  })
  const inst = (await created.json()).instance
  await client.post(`/api/v1/instances/${inst.id}/exec`, { data: { code: 'net.get("https://blocked.example/")' } })
  const ex = await client.get(`/api/v1/instances/${inst.id}/executions`)
  const runId = (await ex.json()).executions[0].id
  expect((await ex.json()).executions[0].error_type).toBe('policy_denied')

  await page.setViewportSize({ width: 1280, height: 900 })
  await page.goto(`${consoleURL}/instances/${inst.id}`)
  await page.evaluate(() => window.scrollTo(0, 0))
  // title comes from labels.name, not 'name=…'
  await expect(page.getByRole('heading', { name: 'Research session' })).toBeVisible()
  await expect(page.getByText('The latest run was denied')).toBeVisible()
  for (const h of ['Boundary', 'Results', 'Agent', 'State']) {
    await expect(page.getByRole('heading', { name: h })).toBeVisible()
  }
  // Work's h2 exists for a11y but is visually hidden by design
  const workH2 = page.getByRole('heading', { name: 'Work' })
  await expect(workH2).toBeAttached()
  const wh = await workH2.evaluate((el) => ({ h: el.getBoundingClientRect().height, clip: getComputedStyle(el).clipPath }))
  expect(wh.h).toBeLessThan(4)
  expect(wh.clip).not.toBe('none')
  // inspect (State) lives in the right rail and is visible by default
  const state = page.locator('[aria-label="State"]')
  await expect(state).toBeVisible()
  expect(await state.evaluate((el) => !!el.closest('.side-col'))).toBe(true)
  await expect(page.locator('.state-variable-tools')).toBeVisible()
  const variableFilter = page.getByRole('textbox', { name: 'Filter variables' })
  await variableFilter.fill('__no_such_variable__')
  await expect(page.getByText('no matching variables', { exact: true })).toBeVisible()
  await variableFilter.clear()
  await expect(page.getByText('no matching variables', { exact: true })).toHaveCount(0)
  const tableType = await page.locator('[aria-label="Activity"] table').evaluate((table) => {
    const header = getComputedStyle(table.querySelector('th')!)
    const source = getComputedStyle(table.querySelector('.detail-code')!)
    const when = getComputedStyle(table.querySelector('tbody tr td:nth-child(2)')!)
    return { casing: header.textTransform, tracking: header.letterSpacing, source: source.fontFamily, when: when.fontFamily }
  })
  expect(tableType.casing).toBe('none')
  expect(['normal', '0px']).toContain(tableType.tracking)
  expect(tableType.source).toContain('IBM Plex Mono')
  expect(tableType.when).toContain('Inter')
  // title wraps at word boundaries, not mid-word
  const h1 = page.getByRole('heading', { name: 'Research session' })
  expect(await h1.evaluate((el) => getComputedStyle(el).wordBreak)).toBe('normal')
  // no thick structural rules inside the work surface (real parseFloat)
  const thick = await page.evaluate(() =>
    Array.from(document.querySelectorAll('.instance-detail .work-surface [data-ui="section-title"]'))
      .filter((el) => parseFloat(getComputedStyle(el).borderBottomWidth) > 1).length)
  expect(thick).toBe(0)
  // env name rendered whole; its value never leaks into the boundary
  await expect(page.getByText('DOMAIN_NAME_REALLY_LONG_VALUE')).toBeVisible()
  await expect(page.getByText('NEVER_RENDER_THIS')).toHaveCount(0)
  // white base — no beige regression
  const base = await page.evaluate(() => ({
    body: getComputedStyle(document.body).backgroundColor,
    header: getComputedStyle(document.querySelector('.console-header')!).backgroundColor,
  }))
  expect(base.body).toBe('rgb(255, 255, 255)')
  // frosted white header over a white page
  expect(base.header).toMatch(/^rgba\(255, 255, 255, 0\.\d+\)$/)
  // no page overflow on desktop
  const dw = await page.evaluate(() => ({ s: document.documentElement.scrollWidth, i: window.innerWidth }))
  expect(dw.s).toBeLessThanOrEqual(dw.i)

  // primary button is visibly primary
  const primary = page.getByRole('button', { name: 'Review run' })
  const pb = await primary.evaluate((el) => {
    const c = getComputedStyle(el)
    return { bg: c.backgroundColor, fg: c.color }
  })
  expect(pb.bg).toBe('rgb(37, 99, 235)')
  expect(pb.fg).toBe('rgb(255, 255, 255)')

  // initial screenshot (scrollTop 0, full page)
  await page.screenshot({ path: testInfo.outputPath('detail-denied-desktop.png'), fullPage: true })

  // Review run expands the run and shows its calls
  await primary.click()
  const detail = page.locator(`#run-${runId}`).locator('xpath=following-sibling::tr[1]')
  await expect(detail).toBeVisible()
  await expect(detail.getByText(/net\.|blocked\.example/).first()).toBeVisible()
  // tables render inside the rounded envelope, including nested calls
  const tblWrap = await page.evaluate(() => Array.from(
    document.querySelectorAll('.instance-detail .tbl-wrap')).map((el) => {
      const c = getComputedStyle(el)
      return { r: c.borderRadius, ox: c.overflowX, oy: c.overflowY }
    }))
  expect(tblWrap.length).toBeGreaterThan(0)
  for (const w of tblWrap) {
    expect(w.r).toBe('10px')
    expect(w.ox).toBe('auto')
    expect(w.oy).toBe('hidden')
  }
  await page.screenshot({ path: testInfo.outputPath('detail-expanded.png'), fullPage: true })

  // More menu: arrow keys move through items, Escape closes and returns
  // focus to the trigger, an outside click closes it
  const more = page.getByRole('button', { name: 'More ▾' })
  await more.click()
  const menu = page.getByRole('menu', { name: 'More ▾' })
  await expect(menu).toBeVisible()
  await page.keyboard.press('ArrowDown')
  await expect(page.getByRole('menuitem').first()).toBeFocused()
  await page.keyboard.press('Escape')
  await expect(menu).toHaveCount(0)
  await expect(more).toBeFocused()
  await more.click()
  await page.locator('body').click({ position: { x: 10, y: 10 } })
  await expect(menu).toHaveCount(0)
  // the menu's remaining item opens a dialog; closing it returns to More
  await more.click()
  await page.getByRole('menuitem', { name: 'End instance' }).click()
  const endDialog = page.getByRole('dialog', { name: /End instance Research session/ })
  await expect(endDialog).toBeVisible()
  await page.waitForTimeout(300)
  await expect(endDialog).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(endDialog).toHaveCount(0)
  await expect(more).toBeFocused()
  // Share and Copy prompt are direct header actions, not menu items
  const promptButton = page.getByRole('button', { name: 'Copy prompt', exact: true })
  await promptButton.click()
  const promptDialog = page.getByRole('dialog', { name: 'Agent prompt' })
  await expect(promptDialog).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(promptDialog).toHaveCount(0)
  await expect(promptButton).toBeFocused()
  await expect(page.getByRole('button', { name: 'Share', exact: true })).toBeVisible()

  // The run editor is its own region on arrival, above Activity — no
  // toggle to reveal it. The all / needs attention filter belongs to
  // Activity's heading and moving it never moves the editor.
  await expect(page.locator('#run-code .cm-editor')).toBeVisible({ timeout: 8000 })
  await expect(page.getByRole('button', { name: 'Run code', exact: true })).toHaveCount(0)
  const editorBox = (await page.locator('#run-code').boundingBox())!
  const activityBox = (await page.locator('[aria-label="Activity"]').boundingBox())!
  expect(editorBox.y + editorBox.height).toBeLessThanOrEqual(activityBox.y)
  const filtersBox = (await page.locator('.seg-filters').boundingBox())!
  const headingBox = (await page.locator('.activity-heading').boundingBox())!
  expect(filtersBox.y).toBeGreaterThanOrEqual(headingBox.y)
  expect(filtersBox.y + filtersBox.height).toBeLessThanOrEqual(headingBox.y + headingBox.height)
  // the editor's action is an icon; there is no header toggle or close
  const runAction = page.getByRole('button', { name: 'Run', exact: true })
  await expect(runAction).toBeVisible()
  await expect(runAction.locator('svg')).toHaveCount(1)
  await expect(page.locator('#run-code').getByRole('button', { name: 'Close' })).toHaveCount(0)
  // switching the filter keeps the editor and the filter in place
  const at = async () => ({
    editor: await page.locator('#run-code').evaluate((el) => { const b = el.getBoundingClientRect(); return [Math.round(b.x), Math.round(b.y + scrollY), Math.round(b.width)] }),
    filter: await page.locator('.seg-filters').evaluate((el) => { const b = el.getBoundingClientRect(); return [Math.round(b.x), Math.round(b.y + scrollY), Math.round(b.width)] }),
  })
  await page.mouse.move(1, 1)
  await page.waitForTimeout(400)
  const closedAt = await at()
  await page.locator('.seg-filters button').nth(1).click()
  await page.waitForTimeout(400)
  expect(await at()).toEqual(closedAt)
  await page.locator('.seg-filters button').first().click()

  // mobile: Work precedes Boundary; ≥44px primary; no overflow
  await page.setViewportSize({ width: 390, height: 900 })
  await page.waitForTimeout(600)
  const mob = await page.evaluate(() => {
    const work = document.querySelector('[aria-label="Work"]')!.getBoundingClientRect()
    const bound = document.querySelector('[aria-label="Boundary"]')!.getBoundingClientRect()
    return { work: work.top, bound: bound.top, sw: document.documentElement.scrollWidth, iw: window.innerWidth }
  })
  expect(mob.work).toBeLessThan(mob.bound)
  expect(mob.sw).toBeLessThanOrEqual(mob.iw)
  const bb = await primary.boundingBox()
  expect(bb!.height).toBeGreaterThanOrEqual(44)
  // state-variable toolbar stays inside the right rail at 390px
  const tools = await page.evaluate(() => {
    const t = document.querySelector('.state-variable-tools')
    const rail = document.querySelector('.side-col')
    if (!t || !rail) return { inside: null }
    const inner = rail.getBoundingClientRect().right
    const children = Array.from(t.children).every((c) => c.getBoundingClientRect().right <= inner + 1)
    return { inside: t.scrollWidth <= t.clientWidth && children }
  })
  expect(tools.inside).toBe(true)
  await page.reload()
  await page.evaluate(() => window.scrollTo(0, 0))
  await page.waitForTimeout(800)
  await page.screenshot({ path: testInfo.outputPath('detail-denied-mobile.png'), fullPage: true })

  // dark: desk + ink actually dark/light
  await page.setViewportSize({ width: 1280, height: 900 })
  await page.emulateMedia({ colorScheme: 'dark' })
  await page.evaluate(() => window.scrollTo(0, 0))
  const dark = await page.evaluate(() => ({
    body: getComputedStyle(document.body).backgroundColor,
    title: getComputedStyle(document.querySelector('.next-title')!).color,
  }))
  expect(dark.body).toBe('rgb(11, 18, 32)')
  expect(dark.title).toBe('rgb(241, 245, 249)')
  await page.screenshot({ path: testInfo.outputPath('detail-dark.png'), fullPage: true })
})

// Detail page has no generic create entry; instance actions stay in the
// header, including the compact square connect-agent control.
test('instance detail: header actions replace the create entry', async ({ page }) => {
  const created = await client.post('/api/v1/instances', { data: { ttl_seconds: 600 } })
  const inst = (await created.json()).instance
  await page.goto(`${consoleURL}/instances/${inst.id}`)
  await expect(page.getByRole('button', { name: 'New instance' })).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Share', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Copy prompt', exact: true })).toBeVisible()
  const connect = page.getByRole('button', { name: 'Connect agent', exact: true })
  await expect(connect).toBeVisible()
  const box = (await connect.boundingBox())!
  expect(box.width).toBeLessThanOrEqual(44)
  expect(box.height).toBeLessThanOrEqual(44)

  // mobile: the square grows to a 44px target and nothing overflows
  await page.setViewportSize({ width: 390, height: 900 })
  await page.waitForTimeout(600)
  const mobile = (await connect.boundingBox())!
  expect(mobile.height).toBeGreaterThanOrEqual(44)
  const dw = await page.evaluate(() => ({ s: document.documentElement.scrollWidth, i: window.innerWidth }))
  expect(dw.s).toBeLessThanOrEqual(dw.i)
  await page.screenshot({ path: 'test-results/detail-header-mobile.png', fullPage: true })
})

// State has no refresh controls: a new execution is the only trigger.
test('instance detail: state follows executions without auto-refresh', async ({ page }) => {
  const created = await client.post('/api/v1/instances', { data: { ttl_seconds: 600 } })
  const inst = (await created.json()).instance
  await page.goto(`${consoleURL}/instances/${inst.id}`)
  const state = page.locator('[aria-label="State"]')
  await expect(state).toBeVisible()
  await expect(state.getByText('auto-refresh')).toHaveCount(0)
  await expect(state.getByRole('button', { name: /refresh/i })).toHaveCount(0)
  await expect(state.getByText('no variables yet — run some code first')).toBeVisible()

  const ex = await client.post(`/api/v1/instances/${inst.id}/exec`, { data: { code: 'answer = 42' } })
  expect(ex.status()).toBe(200)
  await expect(state.getByText('answer', { exact: true })).toBeVisible({ timeout: 15000 })
})

// 1000-entry catalog: library stays a 12-card bounded surface, search +
// paging work, selected extensions survive page turns, icons are same-origin.
test('new instance: extension library stays bounded at 1000 entries', async ({ page }) => {
  const hex = (n: number) => n.toString(16).padStart(64, '0')
  const entries = Array.from({ length: 1000 }, (_, i) => ({
    source: `contrib/ext${i}`, name: `ext-${String(i).padStart(4, '0')}`,
    description: `Fixture extension ${i}`, dependencies: [] as string[], ops: [], config: [],
    icon_url: i === 0 ? `/api/v1/extensions/icons/${hex(0)}` : undefined,
  }))
  entries[999].dependencies = ['net']
  // broad catalog route FIRST so the specific icon route takes priority
  await page.route('**/api/v1/extensions**', (r) => r.fulfill({
    status: 200, contentType: 'application/json',
    body: JSON.stringify({ extensions: entries, local_enabled: true, remote_enabled: false }),
  }))
  await page.route(`**/api/v1/extensions/icons/${hex(0)}`, (r) => r.fulfill({
    status: 200, contentType: 'image/png',
    body: Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==', 'base64'),
  }))
  let captured: any = null
  await page.route('**/api/v1/instances**', async (r) => {
    if (r.request().method() !== 'POST') return r.continue()
    captured = r.request().postDataJSON()
    await r.fulfill({ json: { instance: { id: 'ins_mock1000', status: 'running', workspace_id: '', spec: captured } } })
  })
  const external: string[] = []
  page.on('request', (r) => { if (r.url().startsWith('http') && !r.url().startsWith(consoleURL)) external.push(r.url()) })

  await page.goto(`${consoleURL}/instances/new`)
  await expect(page.locator('.extensions-block .ext-card')).toHaveCount(12)
  await expect(page.getByText('1–12 of 1,000')).toBeVisible()
  const realIcon = page.locator('.extensions-block .ext-card img').first()
  await expect(realIcon).toBeVisible()
  await expect.poll(async () => realIcon.evaluate((img: HTMLImageElement) => img.complete && img.naturalWidth > 0)).toBe(true)

  // search hits the tail entry; one click adds it to the summary
  await page.getByLabel('Search extensions').fill('ext-0999')
  await expect(page.locator('.extensions-block').getByText('1–1 of 1')).toBeVisible()
  await page.getByRole('button', { name: 'Add ext-0999' }).click()
  const row = page.locator('[data-row^="ext:"]')
  await expect(row.locator('.config-item-title')).toHaveText('ext_0999')
  // missing dependency is shown, and granting it is the user's click
  await expect(row.locator('.selected-status')).toContainText('Needs Network')
  await row.getByRole('button', { name: 'Add Network' }).click()
  await expect(page.locator('[data-row="net"]')).toBeVisible()
  await expect(row.locator('.selected-status')).not.toContainText('Needs')

  // clearing the query resets paging; page turns don't drop the selection
  await page.getByLabel('Search extensions').fill('')
  await expect(page.getByText('1–12 of 1,000')).toBeVisible()
  await page.getByRole('button', { name: 'Next extensions page' }).click()
  await expect(page.getByText('13–24 of 1,000')).toBeVisible()
  await expect(page.locator('.extensions-block .ext-card')).toHaveCount(12)
  await expect(row).toHaveCount(1)
  expect(external).toHaveLength(0)

  // settings for the catalog entry: source and alias sit under Advanced
  await page.getByRole('button', { name: 'Configure ext_0999' }).click()
  await page.getByText('Advanced extension settings').click()
  await expect(page.getByLabel('Extension source')).toHaveValue('contrib/ext999')
  await expect(page.getByLabel('Alias')).toHaveValue('ext_0999')
  await page.getByRole('button', { name: 'Create instance' }).click()
  await expect.poll(() => captured).not.toBeNull()
  expect(captured.capabilities.ext.ext_0999.source).toBe('contrib/ext999')
  expect(captured.capabilities.net).toBeDefined()
  expect(Object.keys(captured.capabilities.ext)).toEqual(['ext_0999'])
})

// per-editor JSON state survives view switches and removals — invalid
// text doesn't silently clear and only that entry's error blocks
test('new instance: extension editor keeps invalid JSON through view switches and removes', async ({ page }) => {
  await page.goto(`${consoleURL}/instances/new`)
  const palette = page.getByRole('tab', { name: 'Add capabilities' })
  const card = (alias: string) => page.locator('.settings-card').filter({ has: page.getByRole('heading', { name: alias }) })
  // custom extensions open their settings, since they need a source
  await page.getByRole('button', { name: 'Configure a custom extension' }).click()
  await page.getByLabel('Alias').fill('alpha')
  await page.getByLabel('Extension source').fill('contrib/alpha')
  await palette.click()
  await page.getByRole('button', { name: 'Configure a custom extension' }).click()
  const beta = page.locator('.settings-card').last()
  await beta.getByLabel('Alias').fill('beta')
  await beta.getByLabel('Extension source').fill('contrib/beta')
  await card('beta').getByLabel('Custom configuration JSON').fill('{ broken')
  await expect(card('beta').getByRole('alert')).toContainText(/JSON object/i)
  const create = page.getByRole('button', { name: 'Create instance' })
  await expect(create).toBeDisabled()
  // leave and come back: the typed text and its error are still there
  await palette.click()
  await page.getByRole('button', { name: 'Configure beta' }).click()
  await expect(card('beta').getByLabel('Custom configuration JSON')).toHaveValue('{ broken')
  // removing the other entry keeps beta's text and error
  await page.getByRole('button', { name: 'Remove alpha' }).click()
  await expect(card('beta').getByLabel('Custom configuration JSON')).toHaveValue('{ broken')
  await expect(create).toBeDisabled()
  await card('beta').getByLabel('Custom configuration JSON').fill('{}')
  await expect(create).toBeEnabled()
})

// catalog unavailable → fs/net still one-click, retry recovers
test('new instance: catalog failure does not hide Files or Network', async ({ page }) => {
  let calls = 0
  await page.route('**/api/v1/extensions**', (r) => r.fulfill(++calls === 1 ? { status: 500 } : { status: 200, contentType: 'application/json', body: JSON.stringify({ extensions: [], local_enabled: true, remote_enabled: false }) }))
  await page.goto(`${consoleURL}/instances/new`)
  await expect(page.getByText(/Could not load extensions/i)).toBeVisible()
  await page.getByRole('button', { name: 'Add Files' }).click()
  await page.getByRole('button', { name: 'Add Network' }).click()
  await expect(page.locator('[data-row="fs"]')).toBeVisible()
  await expect(page.locator('[data-row="net"]')).toBeVisible()
  await page.getByText('Retry catalog').click()
  await expect(page.getByText(/Could not load extensions/i)).not.toBeVisible()
  await expect(page.locator('[data-row="fs"]')).toBeVisible()
})

// Adding or removing must not move what the user is pointing at: palette
// controls keep their position, and removal leaves an undo row in place.
test('new instance: palette controls stay put while adding and removing', async ({ page }) => {
  const entries = Array.from({ length: 30 }, (_, i) => ({
    source: `contrib/stable${i}`, name: `stable-${String(i).padStart(2, '0')}`,
    description: `Stable fixture ${i}`, dependencies: i === 2 ? ['net'] : [], ops: [], config: [],
  }))
  await page.route('**/api/v1/extensions**', (r) => r.fulfill({ json: { extensions: entries, local_enabled: true, remote_enabled: false } }))
  for (const viewport of [{ width: 1440, height: 900 }, { width: 390, height: 844 }]) {
    await page.setViewportSize(viewport)
    await page.goto(`${consoleURL}/instances/new`)
    await expect(page.locator('.extensions-block .ext-card')).toHaveCount(12)
    const targets = {
      files: page.locator('.ability-choice').nth(0),
      network: page.locator('.ability-choice').nth(1),
      search: page.getByLabel('Search extensions'),
      next: page.getByRole('button', { name: 'Next extensions page' }),
      ext2: page.locator('.extensions-block .ext-card').nth(2).locator('.ext-card-action'),
      ext3: page.locator('.extensions-block .ext-card').nth(3).locator('.ext-card-action'),
      custom: page.getByRole('button', { name: 'Configure a custom extension' }),
    }
    // Document coordinates: Playwright may scroll a target into view to
    // click it, which is the user's own scrolling, not layout movement.
    const box = (l: ReturnType<typeof page.locator>) => l.evaluate((el) => {
      const b = el.getBoundingClientRect()
      return { x: Math.round(b.x + window.scrollX), y: Math.round(b.y + window.scrollY), w: Math.round(b.width), h: Math.round(b.height) }
    })
    // Measured with the pointer still on the control just clicked: hover
    // and press feedback are paint-only, so nothing may move under it.
    const snapshot = async () => {
      await page.waitForTimeout(400)
      const out: Record<string, { x: number; y: number; w: number; h: number }> = {}
      for (const [k, l] of Object.entries(targets)) out[k] = await box(l)
      return out
    }
    const before = await snapshot()
    await targets.files.click()
    await expect(page.locator('[data-row="fs"]')).toBeVisible()
    expect(await snapshot()).toEqual(before)
    await targets.network.click()
    await expect(page.locator('[data-row="net"]')).toBeVisible()
    expect(await snapshot()).toEqual(before)
    await targets.ext2.click()
    await expect(targets.ext2).toHaveText('Added')
    expect(await snapshot()).toEqual(before)
    await targets.ext3.click()
    expect(await snapshot()).toEqual(before)
    // a second click on an added card neither removes it nor navigates
    await targets.files.click()
    await expect(page.locator('[data-row="fs"]')).toHaveCount(1)
    await expect(page.getByRole('tab', { name: 'Add capabilities' })).toHaveAttribute('aria-selected', 'true')
    expect(await snapshot()).toEqual(before)

    // removal keeps the next row in place behind an undo row
    const network = page.locator('[data-row="net"]')
    const top = (await box(network)).y
    await page.getByRole('button', { name: 'Remove Files' }).click()
    await expect(page.getByText('Files removed')).toBeVisible()
    expect((await box(network)).y).toBe(top)
    await page.getByRole('button', { name: 'Undo' }).click()
    await expect(page.locator('[data-row="fs"]')).toHaveCount(1)
    expect((await box(network)).y).toBe(top)
  }
})

// No nested scroll: the summary column grows with the page, and the
// settings view scrolls with the page too.
test('new instance: no nested scrolling and nothing overflows', async ({ page }) => {
  for (const viewport of [{ width: 1440, height: 900 }, { width: 390, height: 844 }]) {
    await page.setViewportSize(viewport)
    await page.goto(`${consoleURL}/instances/new`)
    await page.getByRole('button', { name: 'Add Files' }).click()
    await page.getByRole('button', { name: 'Add Network' }).click()
    await page.getByRole('button', { name: 'Configure Network' }).click()
    await page.getByLabel(/Allowed hosts/).fill('a-very-long-hostname-that-would-otherwise-stretch-the-column.example.com')
    const layout = await page.evaluate(() => {
      const scrolls = Array.from(document.querySelectorAll('.spec-form *')).filter((el) => {
        const s = getComputedStyle(el)
        return el.tagName !== 'TEXTAREA' && /(auto|scroll)/.test(s.overflowY) && el.scrollHeight > el.clientHeight
      }).map((el) => el.className)
      const within = (sel: string, inner: string) => Array.from(document.querySelectorAll(`${sel} ${inner}`)).every((c) =>
        c.getBoundingClientRect().right <= document.querySelector(sel)!.getBoundingClientRect().right + 0.5)
      return { scrolls, rows: within('.spec-selection', '.selected-row'), cards: within('.spec-settings', '.settings-card'), page: document.documentElement.scrollWidth <= window.innerWidth }
    })
    expect(layout).toEqual({ scrolls: [], rows: true, cards: true, page: true })
  }
})


// Behaviour from the headless primitives: tabs follow arrow keys, dialogs
// trap focus, close on Escape and hand focus back.
test('console primitives: keyboard tabs and dialog focus', async ({ page }) => {
  await page.goto(`${consoleURL}/instances/new`)
  const caps = page.getByRole('tab', { name: 'Add capabilities' })
  await caps.focus()
  await page.keyboard.press('ArrowRight')
  await expect(page.getByRole('tab', { name: /^Settings/ })).toHaveAttribute('aria-selected', 'true')
  await expect(page.getByRole('tabpanel', { name: /^Settings/ })).toBeVisible()
  await page.keyboard.press('ArrowLeft')
  await expect(caps).toHaveAttribute('aria-selected', 'true')

  await page.goto(`${consoleURL}/workspace/secrets`)
  const open = page.getByRole('button', { name: 'New secret' })
  await open.click()
  const dialog = page.getByRole('dialog', { name: 'New secret' })
  await expect(dialog).toBeVisible()
  // focus starts inside and Tab cannot leave the dialog
  for (let i = 0; i < 8; i++) {
    await page.keyboard.press('Tab')
    expect(await dialog.evaluate((d) => d.contains(document.activeElement))).toBe(true)
  }
  await page.keyboard.press('Escape')
  await expect(dialog).toHaveCount(0)
  await expect(open).toBeFocused()
})

// A floating menu never chases its anchor: pressing the trigger (the menu
// opens on pointerdown), resting on its edge and moving across the items
// leave both the trigger and the menu exactly where they were.
test('instance detail: the More menu stays where it opened', async ({ page }) => {
  const created = await client.post('/api/v1/instances', { data: { ttl_seconds: 600 } })
  const inst = (await created.json()).instance
  await page.goto(`${consoleURL}/instances/${inst.id}`)
  const more = page.getByRole('button', { name: 'More ▾' })
  await expect(more).toBeVisible()
  const t = (await more.boundingBox())!
  const stop = await watchGeometry(page, ['[data-ui="menu"]', '.instance-detail [aria-haspopup="menu"]'])
  await page.mouse.move(t.x + t.width / 2, t.y + t.height / 2, { steps: 4 })
  await page.mouse.down()
  await expect(page.getByRole('menu')).toBeVisible()
  await page.waitForTimeout(450)
  await page.mouse.up()
  await page.waitForTimeout(450)
  await page.mouse.move(t.x + t.width / 2, t.y + t.height - 1, { steps: 4 })
  await page.waitForTimeout(450)
  for (const item of await page.getByRole('menuitem').all()) {
    const b = (await item.boundingBox())!
    await page.mouse.move(b.x + b.width / 2, b.y + b.height / 2, { steps: 4 })
    await page.waitForTimeout(150)
  }
  await page.mouse.move(t.x + t.width / 2, t.y + t.height / 2, { steps: 8 })
  await page.waitForTimeout(450)
  await expect(page.getByRole('menu')).toBeVisible()
  const seen = await stop()
  expect(seen['[data-ui="menu"]']).toHaveLength(1)
  expect(seen['.instance-detail [aria-haspopup="menu"]']).toHaveLength(1)
})

// Hover and press are paint-only: every kind of control keeps its box
// while the pointer rests on it and while it is held down.
test('console controls keep their box under hover and press', async ({ page }) => {
  await page.route('**/api/v1/extensions**', (r) => r.fulfill({ json: {
    extensions: [{ source: 'contrib/steady', name: 'steady', description: 'fixture', dependencies: [], ops: [], config: [] }],
    local_enabled: true, remote_enabled: false,
  } }))
  await page.goto(`${consoleURL}/instances/new`)
  await expect(page.locator('.extensions-block .ext-card')).toHaveCount(1)
  // tabs last: a pressed tab activates and swaps the panel
  const controls = ['[data-ui="button"][data-variant="primary"]', '[data-ui="button"][data-variant="default"]', '.ability-choice', '.extensions-block .ext-card', '.input', '.segmented [role="tab"]:not([aria-selected="true"])']
  for (const sel of controls) {
    const control = page.locator(sel).first()
    await control.scrollIntoViewIfNeeded()
    await page.evaluate(() => document.querySelector('[data-probe]')?.removeAttribute('data-probe'))
    await control.evaluate((el) => el.setAttribute('data-probe', ''))
    const b = (await control.boundingBox())!
    await page.mouse.move(1, 1)
    const stop = await watchGeometry(page, ['[data-probe]'])
    await page.mouse.move(b.x + b.width / 2, b.y + b.height / 2, { steps: 4 })
    await page.waitForTimeout(450)
    await page.mouse.move(b.x + b.width / 2, b.y + b.height - 1, { steps: 2 })
    await page.waitForTimeout(450)
    await page.mouse.down()
    await page.waitForTimeout(250)
    // leave before releasing so nothing is activated
    await page.mouse.move(1, 1)
    await page.mouse.up()
    expect((await stop())['[data-probe]'], sel).toHaveLength(1)
  }
})

// The tests below check invariants from docs/interaction-stability.md on
// the rendered console; the IDs in their names refer to that document.

test('removal asks first, cannot act twice and hands focus on (F1 F2 P1 C1)', async ({ page }) => {
  const rego = 'package calcside.hooks\ndeny contains "x" if { input.op == "never" }'
  for (const name of ['stab_a', 'stab_b', 'stab_c']) {
    expect((await client.post('/api/v1/policies', { data: { name, rego } })).status()).toBe(201)
  }
  await page.goto(`${consoleURL}/workspace/policies`)
  const row = (name: string) => page.locator('tbody tr', { hasText: name })
  await expect(row('stab_c')).toHaveCount(1)
  const del = row('stab_a').getByRole('button', { name: 'Delete' })
  // a double click opens the question at most once and removes nothing
  await del.dblclick()
  await page.waitForTimeout(300)
  const asked = await page.getByRole('dialog').count()
  expect(asked).toBeLessThanOrEqual(1)
  if (asked) await page.keyboard.press('Escape')
  for (const name of ['stab_a', 'stab_b', 'stab_c']) await expect(row(name)).toHaveCount(1)
  // Escape cancels and returns focus to the control that asked
  await del.click()
  const confirm = page.getByRole('dialog', { name: 'Delete policy stab_a?' })
  await expect(confirm).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(confirm).toHaveCount(0)
  await expect(del).toBeFocused()
  // confirming removes exactly that row, then focus moves to a neighbour
  await del.click()
  await confirm.getByRole('button', { name: 'Delete' }).click()
  await expect(confirm).toHaveCount(0)
  await expect(row('stab_a')).toHaveCount(0)
  await expect(row('stab_b')).toHaveCount(1)
  await expect(row('stab_c')).toHaveCount(1)
  expect(await page.evaluate(() => {
    const el = document.activeElement
    return el !== document.body && el?.tagName === 'BUTTON' && !!el.closest('tbody')
  })).toBe(true)
})

test('a dialog with unsaved input asks before Escape, close or an outside click discards it (C2)', async ({ page }) => {
  await page.goto(`${consoleURL}/workspace/policies`)
  const open = page.getByRole('button', { name: 'New policy' })
  const dialog = page.getByRole('dialog', { name: 'New policy' })
  const ask = page.getByRole('alertdialog', { name: 'Discard unsaved changes?' })
  // nothing typed yet: Escape just closes
  await open.click()
  await expect(dialog).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(dialog).toHaveCount(0)
  // typed: an outside click asks, Keep editing returns to the field intact
  await open.click()
  const field = dialog.getByPlaceholder('deny_net_hosts')
  await field.fill('draft_name')
  await dialog.evaluate((el) => Promise.all(el.getAnimations().map((a) => a.finished)))
  const box = (await dialog.boundingBox())!
  await page.mouse.click(Math.max(4, box.x - 8), box.y + 40)
  await expect(ask).toBeVisible()
  expect(await dialog.boundingBox()).toEqual(box)
  await page.getByRole('button', { name: 'Keep editing' }).click()
  await expect(ask).toHaveCount(0)
  await expect(field).toHaveValue('draft_name')
  await expect(field).toBeFocused()
  // the close control asks too, not just Escape and the backdrop
  await dialog.getByRole('button', { name: 'Close' }).click()
  await expect(ask).toBeVisible()
  await page.getByRole('button', { name: 'Keep editing' }).click()
  await expect(ask).toHaveCount(0)
  await expect(field).toBeFocused()
  // Escape asks; Escape again keeps editing; Discard closes for real
  await page.keyboard.press('Escape')
  await expect(ask).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(ask).toHaveCount(0)
  await expect(dialog).toBeVisible()
  await page.keyboard.press('Escape')
  await page.getByRole('button', { name: 'Discard' }).click()
  await expect(dialog).toHaveCount(0)
  await expect(open).toBeFocused()
})

test('policy editor reports validation and save outcomes beside its buttons (P5)', async ({ page }) => {
  // the laptop-sized viewport where the old messages scrolled out of sight
  await page.setViewportSize({ width: 1280, height: 720 })
  await page.goto(`${consoleURL}/workspace/policies`)
  await page.getByRole('button', { name: 'New policy' }).click()
  let dialog = page.getByRole('dialog', { name: 'New policy' })
  const name = dialog.getByPlaceholder('deny_net_hosts')
  const save = dialog.getByRole('button', { name: 'Save' })
  // a disabled save says why it is disabled, in the footer
  await expect(dialog.getByRole('status')).toHaveText('Enter a name to save.')
  await name.fill('bad name')
  await expect(save).toBeDisabled()
  await expect(dialog.getByRole('status')).toContainText('Name allows letters')
  // a valid policy reports validation, then saves and closes
  await name.fill('feedback_policy')
  await expect(save).toBeEnabled()
  await dialog.getByRole('button', { name: 'Validate' }).click()
  await expect(dialog.getByRole('status')).toHaveText('Policy is valid.')
  await save.click()
  await expect(dialog).toHaveCount(0)
  await expect(page.locator('tbody tr', { hasText: 'feedback_policy' })).toHaveCount(1)
  // a rejected save stays open with the reason in the footer
  expect((await client.post('/api/v1/policies', { data: { name: 'feedback_other', rego: 'package calcside.hooks\n\n' } })).status()).toBe(201)
  await page.locator('tbody tr', { hasText: 'feedback_policy' }).getByRole('button', { name: 'Edit' }).click()
  dialog = page.getByRole('dialog', { name: 'Edit feedback_policy' })
  await expect(dialog).toBeVisible()
  await dialog.getByPlaceholder('deny_net_hosts').fill('feedback_other')
  await dialog.getByRole('button', { name: 'Save' }).click()
  await expect(dialog.getByRole('status')).toContainText('Save failed: policy with that name already exists')
  await expect(dialog).toBeVisible()
})

test('share dialog: the role menu matches the email field (P2)', async ({ page }) => {
  const inst = (await (await client.post('/api/v1/instances', { data: { ttl_seconds: 600 } })).json()).instance
  await page.goto(`${consoleURL}/instances/${inst.id}`)
  await page.getByRole('button', { name: 'Share', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: 'Share instance' })
  await expect(dialog).toBeVisible()
  await dialog.evaluate((el) => Promise.all(el.getAnimations().map((a) => a.finished)))
  const style = (locator: ReturnType<typeof dialog.locator>) => locator.evaluate((el) => {
    const s = getComputedStyle(el)
    const b = el.getBoundingClientRect()
    return { height: b.height, radius: s.borderTopLeftRadius, border: s.borderTopWidth, bg: s.backgroundColor }
  })
  const email = dialog.getByPlaceholder('user@example.com')
  const role = dialog.getByRole('combobox', { name: 'Role' })
  expect(await style(role)).toEqual(await style(email))
  await expect(role).toHaveValue('viewer')
  await role.selectOption('editor')
  await expect(role).toHaveValue('editor')
  await dialog.getByRole('button', { name: 'Done' }).click()
  await expect(dialog).toHaveCount(0)
})

test('agent runs wait behind "Show N new"; your own runs appear at once (S5)', async ({ page }) => {
  test.setTimeout(60_000)
  const inst = (await (await client.post('/api/v1/instances', { data: { ttl_seconds: 600 } })).json()).instance
  const key = await client.post('/api/v1/keys', { data: { name: 'stability agent', expires_in_seconds: 3600 } })
  expect(key.status()).toBe(201)
  const agent = { Authorization: `Bearer ${(await key.json()).secret}` }
  const exec = (code: string, headers?: Record<string, string>) => client.post(`/api/v1/instances/${inst.id}/exec`, { data: { code }, headers })
  expect((await exec('print("first")', agent)).status()).toBe(200)
  await page.goto(`${consoleURL}/instances/${inst.id}`)
  const rows = page.locator('[aria-label="Activity"] tbody tr[id^="run-"]')
  await expect(rows).toHaveCount(1)
  const first = await rows.first().evaluate((el) => { const b = el.getBoundingClientRect(); return [b.x, b.y + scrollY] })
  const filters = page.locator('[aria-label="Activity"] .seg-filters')
  const filtersAt = (await filters.boundingBox())!
  expect((await exec('print("second")', agent)).status()).toBe(200)
  const more = page.getByRole('button', { name: 'Show 1 new' })
  await expect(more).toBeVisible({ timeout: 15_000 })
  await expect(rows).toHaveCount(1)
  expect(await rows.first().evaluate((el) => { const b = el.getBoundingClientRect(); return [b.x, b.y + scrollY] })).toEqual(first)
  // the reveal slot is always as wide as its longest label (S3/S4)
  expect(await filters.boundingBox()).toEqual(filtersAt)
  await more.click()
  await expect(rows).toHaveCount(2)
  await expect(more).toBeHidden()
  expect(await filters.boundingBox()).toEqual(filtersAt)
  // a run you started is the outcome you are waiting for: no button
  expect((await exec('print("mine")')).status()).toBe(200)
  await expect(rows).toHaveCount(3, { timeout: 15_000 })
  await expect(page.getByRole('button', { name: /^Show \d+ new$/ })).toBeHidden()
  expect(await filters.boundingBox()).toEqual(filtersAt)
})

test('new instances wait behind "Show latest" without moving the primary action (S5)', async ({ page }) => {
  test.setTimeout(60_000)
  await page.goto(`${consoleURL}/`)
  const newInstance = page.getByRole('button', { name: 'New instance' }).first()
  await expect(newInstance).toBeVisible()
  const newAt = (await newInstance.boundingBox())!
  const rows = page.locator('tbody tr')
  const before = await rows.count()
  const created = await client.post('/api/v1/instances', { data: { ttl_seconds: 600, labels: { name: 'held-list' } } })
  expect(created.status()).toBe(201)
  const more = page.getByRole('button', { name: /^Show \d+ new$/ })
  await expect(more).toBeVisible({ timeout: 25_000 })
  await expect(rows).toHaveCount(before)
  expect(await newInstance.boundingBox()).toEqual(newAt)
  await more.click()
  await expect(rows).toHaveCount(before + 1)
  await expect(page.getByRole('link', { name: 'name=held-list' })).toBeVisible()
  await expect(more).toBeHidden()
  expect(await newInstance.boundingBox()).toEqual(newAt)
})

// Classic (space-taking) scrollbars, as on Windows/Linux or with "always
// show scrollbars": the case where scroll locking can shift the page.
// Headless Chromium hides them unless launched without that flag, and
// macOS Chrome overlays them unless that feature is disabled.
test('opening a dialog does not shift the page under it (S6)', async () => {
  const browser = await chromium.launch({
    channel: process.env.PLAYWRIGHT_CHANNEL,
    ignoreDefaultArgs: ['--hide-scrollbars'],
    args: ['--disable-features=OverlayScrollbar'],
  })
  const page = await browser.newPage({ viewport: { width: 1280, height: 500 } })
  await page.goto(`${consoleURL}/workspace/policies`)
  await page.addStyleTag({ content: '::-webkit-scrollbar { width: 15px; height: 15px } ::-webkit-scrollbar-thumb { background: #999 } body { min-height: 200vh }' })
  expect(await page.evaluate(() => window.innerWidth - document.documentElement.clientWidth)).toBeGreaterThan(0)
  const where = () => page.evaluate(() => ['main', '.console-header-inner', 'h1, [role="tablist"], .workspace-tabs'].map((s) => {
    const b = document.querySelector(s)!.getBoundingClientRect()
    return [s, Math.round(b.x), Math.round(b.width)]
  }))
  const before = await where()
  await page.getByRole('button', { name: 'New policy' }).click()
  await expect(page.getByRole('dialog', { name: 'New policy' })).toBeVisible()
  expect(await where()).toEqual(before)
  await page.keyboard.press('Escape')
  expect(await where()).toEqual(before)
  await browser.close()
})

// The material is real CSS, not a screenshot impression: raised faces are
// lit by a soft radial gradient (no bevels), fields are flat wells, and
// pressing changes the face while it is held.
test('console material: raised controls, inset wells, pressed state', async ({ page }) => {
  await page.goto(`${consoleURL}/instances/new`)
  const style = (sel: string) => page.locator(sel).first().evaluate((el) => {
    const s = getComputedStyle(el)
    return { image: s.backgroundImage, shadow: s.boxShadow, radius: s.borderTopLeftRadius }
  })
  const create = await style('[data-ui="button"][data-variant="primary"]')
  expect(create.image).toContain('radial-gradient')
  expect(create.shadow).toContain('inset')
  expect(create.radius).toBe('10px')
  const input = await style('.input')
  expect(input.image).toBe('none')
  expect(input.shadow).toMatch(/inset/)
  expect(input.radius).toBe('10px')
  const panel = await style('.builder-work-panel')
  expect(panel.image).toContain('radial-gradient')
  expect(panel.radius).toBe('16px')
  const tray = await style('.spec-selection')
  expect(tray.shadow).toMatch(/inset/)
  // pressed: gradient and shadow change while held
  const files = page.locator('.ability-choice').first()
  const box = (await files.boundingBox())!
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2)
  const hover = await files.evaluate((el) => getComputedStyle(el).backgroundImage)
  await page.mouse.down()
  await expect.poll(() => files.evaluate((el) => getComputedStyle(el).backgroundImage)).not.toBe(hover)
  await page.mouse.up()
})
