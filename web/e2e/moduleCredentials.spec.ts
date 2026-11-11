import { test, expect, type APIRequestContext } from '@playwright/test'
import { randomBytes } from 'node:crypto'
import { startApplicationRuntime, type ApplicationRuntime } from './applicationRuntime'
import http from 'node:http'

// REAL credential flow: API decrypts vault values and the trusted
// worker injects them into ext config over its internal channel; the
// echo server compares privately and returns booleans — values never
// appear in responses, DOM, or logs.
let rt: ApplicationRuntime
let client: APIRequestContext
let viteURL = ''
let echoPort = 0
let echo: http.Server
const sawAuth: Record<string, string> = {}
let expectedKey = 'TEST_API_KEY_VALUE'

test.beforeAll(async () => {
  test.setTimeout(300_000)
  echo = http.createServer((req, res) => {
    sawAuth.authorization = String(req.headers.authorization ?? '')
    sawAuth['x-client-secret'] = String(req.headers['x-client-secret'] ?? '')
    const ok = sawAuth.authorization === 'Bearer ' + expectedKey &&
      sawAuth['x-client-secret'] === 'TEST_CLIENT_SECRET_VALUE'
    res.writeHead(200, { 'Content-Type': 'application/json' })
    res.end(JSON.stringify({ key_ok: sawAuth.authorization === 'Bearer ' + expectedKey, client_ok: ok }))
  })
  await new Promise<void>((r) => echo.listen(0, '127.0.0.1', r))
  echoPort = (echo.address() as { port: number }).port
  rt = await startApplicationRuntime({ secretKey: randomBytes(32).toString('base64'), secretsAllowHTTP: true })
  client = rt.client
  viteURL = rt.viteURL
})
test.afterAll(async () => { echo.close(); await rt?.close() })

test('module debug executes ext with vault-injected credentials', async ({ page }) => {
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(String(e)))
  page.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()) })

  const { writeFile, mkdir } = await import('node:fs/promises')
  const { dirname, join } = await import('node:path')
  const ext = join(dirname(dirname(rt.extensionDir)), 'credentials', '0.1.0')
  await mkdir(ext, { recursive: true })
  await writeFile(join(ext, 'capability.yaml'),
    `name: credentials\nversion: "0.1.0"\nops: [{name: check, params: []}]\ndependencies: [net]\nconfig:\n  - {name: endpoint, type: string}\n  - {name: api_key, type: secret}\n  - {name: client_secret, type: secret}\n`)
  await writeFile(join(ext, 'main.star'),
    `def check():
    r = net.get(config['endpoint'], headers={"Authorization": "Bearer " + config['api_key'], "X-Client-Secret": config['client_secret']})
    return json.decode(r['body'])`)

  for (const [name, value] of [['TEST_API_KEY', 'TEST_API_KEY_VALUE'], ['TEST_CLIENT_SECRET', 'TEST_CLIENT_SECRET_VALUE'], ['TEST_STAGE_KEY', 'STAGE_VALUE'], ['TEST_ROTATED_KEY', 'ROTATED_VALUE']]) {
    const r = await client.post('/api/v1/secrets', { data: { name, value, allowed_domains: [`127.0.0.1:${echoPort}`] } })
    expect([200, 201]).toContain(r.status())
  }

  const cr = await client.post('/api/v1/modules', {
    data: { name: 'credmod', files: { 'main.star': 'def check(): return ext.credentials.check()' } },
  })
  expect(cr.status()).toBe(200)
  const mod = (await cr.json()).module

  // Seed debug baseline via API: net allowlist + script only — the
  // extension and all credentials are added through the UI.
  const dbg0 = await client.get(`/api/v1/modules/${mod.id}/debug`)
  const spec0 = {
    capabilities: { net: { allow_hosts: [`127.0.0.1:${echoPort}`], methods: ['GET'] } },
    policies: [],
  }
  const put0 = await client.put(`/api/v1/modules/${mod.id}/debug`, {
    data: { version: (await dbg0.json()).debug.version, script: 'load("@self/main.star", "check")\nprint(check())', spec: spec0 },
  })
  expect(put0.status(), await put0.text()).toBe(200)

  await page.goto(`${viteURL}/modules/${mod.id}`)
  // Workbench: all surfaces present at once.
  await expect(page.getByTestId('module-editor')).toBeVisible()
  await expect(page.getByTestId('debug-script')).toBeVisible()
  await expect(page.getByText('Extensions & runtime')).toBeVisible()
  // No binding/default terminology and no manual identity fields.
  await expect(page.getByText(/binding/i)).toHaveCount(0)

  // Add the credentials extension via the catalog card, then configure.
  await page.getByTestId('module-deps').getByRole('button', { name: 'Add credentials 0.1.0' }).click()
  await page.getByRole('button', { name: 'Configure credentials' }).click()
  await page.getByLabel('credentials endpoint').fill(`http://127.0.0.1:${echoPort}/echo`)
  const pick = async (field: string, ref: string) => {
    await page.getByLabel(`credentials ${field}`).selectOption(ref)
  }
  await pick('api_key', 'TEST_API_KEY')
  await pick('client_secret', 'TEST_CLIENT_SECRET')
  // Autosave persists the picks — poll until the env is stored.
  await expect.poll(async () => {
    const g = await client.get(`/api/v1/modules/${mod.id}/debug`)
    const sp = (await g.json()).debug.spec
    return JSON.stringify(sp?.environment ?? {})
  }, { timeout: 15_000 }).toContain('TEST_API_KEY')
  const dbgSt = await client.get(`/api/v1/modules/${mod.id}/debug`)
  const stBody = await dbgSt.json()
  // Prove the UI picks persisted as the env default before stage inject.
  expect(JSON.stringify(stBody.debug.spec?.environment ?? {}), 'env after UI save').toContain('TEST_API_KEY')
  const stSpec = stBody.debug.spec
  stSpec.environment = stSpec.environment ?? { extensions: {} }
  stSpec.environment.extensions.credentials = stSpec.environment.extensions.credentials ?? { bindings: {} }
  stSpec.environment.extensions.credentials.bindings.stage = { secrets: { api_key: { ref: 'TEST_STAGE_KEY' } } }
  const putSt = await client.put(`/api/v1/modules/${mod.id}/debug`, {
    data: { version: stBody.debug.version, script: stBody.debug.script, spec: stSpec },
  })
  expect(putSt.status(), await putSt.text()).toBe(200)
  await page.reload()
  await page.getByRole('button', { name: 'Configure credentials' }).click()

  await page.getByTestId('debug-run').click()
  const result = page.getByTestId('debug-result')
  await expect(result).toContainText('key_ok', { timeout: 90_000 })

  expect(sawAuth.authorization).toBe('Bearer TEST_API_KEY_VALUE')
  expect(sawAuth['x-client-secret']).toBe('TEST_CLIENT_SECRET_VALUE')
  expect(sawAuth['x-client-secret']).not.toBe('STAGE_VALUE')

  const out = await result.textContent()
  expect(out).not.toContain('TEST_API_KEY_VALUE')
  expect(out).not.toContain('TEST_CLIENT_SECRET_VALUE')

  // Selects show the stored refs; rotate api_key via select.
  await expect(page.getByLabel('credentials api_key')).toHaveValue('TEST_API_KEY')
  const dbg1 = await client.get(`/api/v1/modules/${mod.id}/debug`)
  const dbgSpec = JSON.stringify(await dbg1.json())
  expect(dbgSpec).not.toContain('EXT_BIND_')
  expect(dbgSpec).toContain('TEST_API_KEY')
  expect(dbgSpec).toContain('TEST_STAGE_KEY')
  expect(dbgSpec).not.toContain('TEST_API_KEY_VALUE')

  await page.getByLabel('credentials api_key').selectOption('TEST_ROTATED_KEY')
  await expect(page.getByLabel('credentials api_key')).toHaveValue('TEST_ROTATED_KEY')
  expectedKey = 'ROTATED_VALUE'
  const runResp = page.waitForResponse((r) => r.url().includes('/debug/run') && r.request().method() === 'POST', { timeout: 90_000 })
  await page.getByTestId('debug-run').click()
  await runResp
  await expect(result).toContainText('key_ok', { timeout: 90_000 })
  const dbgRot = await client.get(`/api/v1/modules/${mod.id}/debug`)
  const rotSpec = JSON.stringify((await dbgRot.json()).debug.spec)
  expect(rotSpec, 'saved spec after rotate').toContain('TEST_ROTATED_KEY')
  expect(sawAuth.authorization).toBe('Bearer ROTATED_VALUE')
  expect(sawAuth['x-client-secret']).toBe('TEST_CLIENT_SECRET_VALUE')

  const dbg2 = await client.get(`/api/v1/modules/${mod.id}/debug`)
  const dbg2Spec = JSON.stringify(await dbg2.json())
  expect(dbg2Spec).toContain('TEST_ROTATED_KEY')
  expect(dbg2Spec).toContain('TEST_STAGE_KEY')
  expect(dbg2Spec).not.toContain('ROTATED_VALUE')

  // Editing config marks the displayed result outdated.
  await page.getByLabel('credentials api_key').selectOption('TEST_API_KEY')
  await expect(page.getByText(/outdated/)).toBeVisible()
  expectedKey = 'TEST_API_KEY_VALUE'

  // Advanced JSON escape: a raw spec edit round-trips as an OBJECT
  // (proves the raw-fragment save path, never quote-wrapped JSON).
  await page.getByRole('button', { name: 'Advanced JSON' }).click()
  const raw = page.getByLabel('Debug spec JSON')
  const rawText = await raw.inputValue()
  const edited = rawText.replace('/echo', '/echo?m=1')
  await raw.fill(edited)
  await page.getByLabel('Debug spec JSON').press('ControlOrMeta+a')
  await expect.poll(async () => {
    const g = await client.get(`/api/v1/modules/${mod.id}/debug`)
    return JSON.stringify((await g.json()).debug.spec ?? {})
  }, { timeout: 15_000 }).toContain('echo?m=1')
  const dbgRaw = await client.get(`/api/v1/modules/${mod.id}/debug`)
  const rawSpec = (await dbgRaw.json()).debug.spec
  expect(typeof rawSpec).toBe('object')
  expect(JSON.stringify(rawSpec)).toContain('echo?m=1')
  // back to the structured editor keeps the fields
  await page.getByRole('button', { name: 'Back to editor' }).click()

  // Integrated layout: the dependency catalog sits left of the Debug
  // environment summary while a dep is selected (S3) — capture it
  // before the removal below leaves the row empty.
  const deps = page.getByTestId('module-deps')
  await deps.getByRole('tab', { name: 'Add capabilities' }).click()
  const libBox = await deps.locator('.extensions-block .ext-library').boundingBox()
  const asideBox = await deps.locator('aside').boundingBox()
  expect(libBox && asideBox && libBox.x < asideBox.x).toBeTruthy()
  await page.screenshot({ path: 'e2e/screenshots/module-credentials-deps.png' })
  await page.setViewportSize({ width: 390, height: 844 })
  expect(await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth)).toBe(false)
  await page.setViewportSize({ width: 1280, height: 900 })

  // A confirmed dependency removal prunes the dep's config and env.
  await page.getByTestId('module-deps').getByRole('button', { name: 'Remove credentials' }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Remove' }).click()
  await expect.poll(async () => {
    const g = await client.get(`/api/v1/modules/${mod.id}`)
    return JSON.stringify((await g.json()).module.requirements ?? {})
  }, { timeout: 15_000 }).not.toContain('fixture/credentials@0.1.0')
  await expect.poll(async () => {
    const g = await client.get(`/api/v1/modules/${mod.id}/debug`)
    const sp = (await g.json()).debug.spec
    return JSON.stringify(sp?.environment?.extensions ?? {})
  }, { timeout: 15_000 }).not.toContain('credentials')
  await expect.poll(async () => {
    const g = await client.get(`/api/v1/modules/${mod.id}/debug`)
    const sp = (await g.json()).debug.spec
    return JSON.stringify(sp?.capabilities?.ext ?? {})
  }, { timeout: 15_000 }).not.toContain('credentials')

  await page.screenshot({ path: 'e2e/screenshots/module-credentials.png' })
  await page.setViewportSize({ width: 390, height: 844 })
  await page.screenshot({ path: 'e2e/screenshots/module-credentials-mobile.png' })
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth)
  expect(overflow).toBe(false)
  expect(errors.filter((e) => !/favicon|DevTools|ResizeObserver|Failed to load resource/.test(e))).toEqual([])
})

test('debug settings CAS conflict blocks save until explicit reload', async ({ page }) => {
  const cr = await client.post('/api/v1/modules', {
    data: { name: 'casmod', files: { 'main.star': 'def x(): return 1' } },
  })
  expect(cr.status()).toBe(200)
  const mod = (await cr.json()).module
  const dbg0 = await client.get(`/api/v1/modules/${mod.id}/debug`)
  const v0 = (await dbg0.json()).debug.version
  await page.goto(`${viteURL}/modules/${mod.id}`)
  await expect(page.getByTestId('debug-script')).toBeVisible()

  // External edit bumps the server version while the page holds v0.
  const ext = await client.put(`/api/v1/modules/${mod.id}/debug`, {
    data: { version: v0, script: 'print("external")', spec: {} },
  })
  expect(ext.status()).toBe(200)
  const v1 = (await ext.json()).debug.version

  // An edit triggers autosave on the stale version: exactly one PUT, a
  // 409, no auto-retry, remote unchanged.
  const puts: string[] = []
  page.on('response', (r) => {
    if (r.url().includes('/debug') && r.request().method() === 'PUT') puts.push(String(r.status()))
  })
  await page.getByLabel('Caller script editor').getByRole('textbox').fill('print("local edit")')
  await expect(page.getByText(/changed elsewhere/)).toBeVisible({ timeout: 15_000 })
  await page.waitForTimeout(1500) // beyond the debounce — no retry must fire
  expect(puts).toEqual(['409'])
  const remote = await client.get(`/api/v1/modules/${mod.id}/debug`)
  expect((await remote.json()).debug.version).toBe(v1)
  expect((await remote.json()).debug.script).toContain('external')

  // Cancel keeps local edits + conflict state.
  await page.getByTestId('debug-reload').click()
  await page.getByRole('button', { name: 'Cancel' }).click().catch(() => {})
  await expect(page.getByText(/changed elsewhere/)).toBeVisible()

  // Confirm reload adopts the remote version; the next edit saves.
  await page.getByTestId('debug-reload').click()
  await page.getByRole('button', { name: 'Reload', exact: true }).click()
  await expect(page.getByText(/changed elsewhere/)).toHaveCount(0)
  const puts2: string[] = []
  page.on('response', (r) => {
    if (r.url().includes('/debug') && r.request().method() === 'PUT') puts2.push(String(r.status()))
  })
  await page.getByLabel('Caller script editor').getByRole('textbox').fill('print("after reload")')
  await expect.poll(() => puts2.length, { timeout: 15_000 }).toBeGreaterThanOrEqual(1)
  expect(puts2[0]).toBe('200')
})
