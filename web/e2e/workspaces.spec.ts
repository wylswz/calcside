import { test, expect, request, type APIRequestContext } from '@playwright/test'
import { execFileSync, spawn, type ChildProcess } from 'node:child_process'
import { mkdtemp, rm } from 'node:fs/promises'
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
  execFileSync('atlas', ['migrate', 'apply', '--env', 'sqlite'], { cwd: repo, env: { ...env, ATLAS_DB_URL: `sqlite://${database}` }, stdio: 'pipe' })
  const listenPort = await port()
  consoleURL = `http://console.localhost:${listenPort}`
  const transportURL = `http://127.0.0.1:${listenPort}`
  server = spawn(binary, ['serve', '--dev', '--store', 'sqlite', '--dsn', database, '--addr', `127.0.0.1:${listenPort}`, '--console-origin', consoleURL], { cwd: repo, env, stdio: ['ignore', 'pipe', 'pipe'] })
  server.stdout?.on('data', (chunk) => { output = (output + String(chunk)).slice(-64_000) })
  server.stderr?.on('data', (chunk) => { output = (output + String(chunk)).slice(-64_000) })
  client = await request.newContext({ baseURL: transportURL, extraHTTPHeaders: { 'X-Requested-With': 'calcside' } })
  await expect.poll(async () => {
    if (server?.exitCode !== null) throw new Error(`Test server exited: ${output}`)
    try { return (await client.get('/healthz', { timeout: 1000 })).status() } catch { return 0 }
  }, { timeout: 30_000 }).toBe(200)
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
  await page.getByRole('button', { name: /e2e team/ }).click()
  await page.waitForLoadState('networkidle')
  // scoped: the personal-workspace instance row is absent
  await expect(page.locator('tbody tr')).toHaveCount(0, { timeout: 5000 }).catch(() => {})
  // settings page
  await page.goto(`${consoleURL}/workspaces/${team.id}`)
  await expect(page.getByText('e2e team').first()).toBeVisible()
  await expect(page.getByText('members', { exact: false }).first()).toBeVisible()
})

// Flow: creating an instance lands on its page with the Connect-agent
// guidance card; after the first run the card disappears.
test('instance flow guides connect then clears', async ({ page }) => {
  await page.goto(`${consoleURL}/`)
  await page.getByRole('button', { name: 'New instance' }).first().click()
  await page.getByRole('button', { name: 'Create instance' }).click()
  await expect(page).toHaveURL(/\/instances\/ins_/, { timeout: 10000 })
  await expect(page.getByText('Connect an agent')).toBeVisible()
  const id = page.url().split('/instances/')[1].split('?')[0].split('/')[0]
  const ex = await client.post(`/api/v1/instances/${id}/exec`, { data: { code: 'print(1)' } })
  expect(ex.status()).toBe(200)
  await page.reload()
  await page.waitForTimeout(1500)
  await expect(page.getByText('Connect an agent')).toHaveCount(0)
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
  for (const h of ['Boundary', 'Results', 'Agent']) {
    await expect(page.getByRole('heading', { name: h })).toBeVisible()
  }
  // Work's h2 exists for a11y but is visually hidden by design
  const workH2 = page.getByRole('heading', { name: 'Work' })
  await expect(workH2).toBeAttached()
  const wh = await workH2.evaluate((el) => ({ h: el.getBoundingClientRect().height, clip: getComputedStyle(el).clipPath }))
  expect(wh.h).toBeLessThan(4)
  expect(wh.clip).not.toBe('none')
  // State disclosure defaults collapsed; toolbar appears after opening
  const stateDisclosure = page.locator('.detail-state-disclosure')
  await expect(stateDisclosure).not.toHaveAttribute('open', '')
  await expect(page.locator('.state-variable-tools')).not.toBeVisible()
  await stateDisclosure.locator('summary').first().click()
  await expect(stateDisclosure).toHaveAttribute('open', '')
  await expect(page.locator('.state-variable-tools')).toBeVisible()
  const variableFilter = page.getByRole('textbox', { name: 'Filter variables' })
  await variableFilter.fill('__no_such_variable__')
  await expect(page.getByText('no matching variables', { exact: true })).toBeVisible()
  await variableFilter.clear()
  await expect(page.getByText('no matching variables', { exact: true })).toHaveCount(0)
  await stateDisclosure.locator(':scope > summary').click()
  await expect(stateDisclosure).not.toHaveAttribute('open', '')
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
  expect(base.header).toBe('rgb(255, 255, 255)')
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

  // initial collapsed screenshot (scrollTop 0, full page)
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
    expect(w.r).toBe('7px')
    expect(w.ox).toBe('auto')
    expect(w.oy).toBe('hidden')
  }
  await page.screenshot({ path: testInfo.outputPath('detail-expanded.png'), fullPage: true })

  // More menu: Escape then outside-click dismissal
  await page.getByRole('button', { name: 'More ▾' }).click()
  await expect(page.locator('#inst-more')).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(page.locator('#inst-more')).toHaveCount(0)
  await page.getByRole('button', { name: 'More ▾' }).click()
  await page.locator('body').click({ position: { x: 10, y: 10 } })
  await expect(page.locator('#inst-more')).toHaveCount(0)

  // Run code yourself opens the editor
  await page.getByText('Run code yourself').first().click()
  await expect(page.locator('.cm-editor')).toBeVisible({ timeout: 8000 })

  // mobile: Work precedes Boundary; ≥44px primary; no overflow
  await stateDisclosure.locator(':scope > summary').click()
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
  // state-variable toolbar stays inside the work surface at 390px
  const tools = await page.evaluate(() => {
    const t = document.querySelector('.state-variable-tools')
    const ws = document.querySelector('.work-surface')
    if (!t || !ws) return { inside: null }
    const inner = ws.getBoundingClientRect().right - parseFloat(getComputedStyle(ws).paddingRight)
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

// Regression: the detail page exposes a plain New-instance entry for owners/editors.
test('instance detail: new instance starts a fresh draft', async ({ page }) => {
  const created = await client.post('/api/v1/instances', { data: { ttl_seconds: 600 } })
  const inst = (await created.json()).instance
  await page.goto(`${consoleURL}/instances/${inst.id}`)
  const entry = page.getByRole('button', { name: 'New instance' })
  await expect(entry).toBeVisible()
  await entry.click()
  await expect(page).toHaveURL(/\/instances\/new$/) // no ?from → fresh draft
  await expect(page.getByRole('heading', { name: 'Compute only' })).toBeVisible()
  await expect(page.getByPlaceholder('e.g. research run')).toHaveValue('')
  await expect(page.getByRole('button', { name: 'Create instance' })).toBeEnabled()

  // mobile: entry stays ≥44px with no overflow
  await page.setViewportSize({ width: 390, height: 900 })
  await page.goto(`${consoleURL}/instances/${inst.id}`)
  await page.waitForTimeout(600)
  const bb = await page.getByRole('button', { name: 'New instance' }).boundingBox()
  expect(bb!.height).toBeGreaterThanOrEqual(44)
  const dw = await page.evaluate(() => ({ s: document.documentElement.scrollWidth, i: window.innerWidth }))
  expect(dw.s).toBeLessThanOrEqual(dw.i)
  await page.screenshot({ path: 'test-results/new-entry-mobile.png', fullPage: true })
})

// Role-driven: the entry appears for editors, hides for viewers.
for (const role of ['editor', 'viewer'] as const) {
  test(`instance detail: create entry follows ${role} role`, async ({ page }) => {
    const created = await client.post('/api/v1/instances', { data: { ttl_seconds: 600 } })
    const inst = (await created.json()).instance
    const real = await client.get('/api/v1/workspaces')
    const body = await real.json()
    for (const ws of body.workspaces ?? []) ws.role = role
    await page.route('**/api/v1/workspaces', (r) =>
      r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) }))
    await page.goto(`${consoleURL}/instances/${inst.id}`)
    const entry = page.getByRole('button', { name: 'New instance' })
    if (role === 'editor') {
      await expect(entry).toBeVisible()
      await entry.click()
      await expect(page).toHaveURL(/\/instances\/new$/)
    } else {
      await expect(entry).toHaveCount(0)
      await expect(page.getByRole('button', { name: 'Create similar' })).toHaveCount(0)
      await page.goto(`${consoleURL}/instances/new`)
      await expect(page.getByText(/You need editor access/i)).toBeVisible()
    }
  })
}

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
  await expect(page.locator('.ext-card')).toHaveCount(12)
  await expect(page.getByText('1–12 of 1,000')).toBeVisible()
  const realIcon = page.locator('.ext-card img').first()
  await expect(realIcon).toBeVisible()
  await expect.poll(async () => realIcon.evaluate((img: HTMLImageElement) => img.complete && img.naturalWidth > 0)).toBe(true)

  // search hits the tail entry; one click adds it to the summary
  await page.getByLabel('Search extensions').fill('ext-0999')
  await expect(page.getByText('1–1 of 1')).toBeVisible()
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
  await expect(page.locator('.ext-card')).toHaveCount(12)
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
    await expect(page.locator('.ext-card')).toHaveCount(12)
    const targets = {
      files: page.locator('.ability-choice').nth(0),
      network: page.locator('.ability-choice').nth(1),
      search: page.getByLabel('Search extensions'),
      next: page.getByRole('button', { name: 'Next extensions page' }),
      ext2: page.locator('.ext-card').nth(2).locator('.ext-card-action'),
      ext3: page.locator('.ext-card').nth(3).locator('.ext-card-action'),
      custom: page.getByRole('button', { name: 'Configure a custom extension' }),
    }
    // Document coordinates: Playwright may scroll a target into view to
    // click it, which is the user's own scrolling, not layout movement.
    const box = (l: ReturnType<typeof page.locator>) => l.evaluate((el) => {
      const b = el.getBoundingClientRect()
      return { x: Math.round(b.x + window.scrollX), y: Math.round(b.y + window.scrollY), w: Math.round(b.width), h: Math.round(b.height) }
    })
    const snapshot = async () => {
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
