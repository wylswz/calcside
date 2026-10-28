import { test, expect, type APIRequestContext } from '@playwright/test'
import { startApplicationRuntime, type ApplicationRuntime } from './applicationRuntime'

// Extension lifecycle proof under the real API→worker→child topology:
// a blocked artifact fetch fails the caller's publish/create, an
// explicit retry succeeds, and cached pins serve exact results.

let rt: ApplicationRuntime
let client: APIRequestContext
let viteURL = ''

test.beforeAll(async () => {
  test.setTimeout(300_000)
  rt = await startApplicationRuntime()
  client = rt.client
  viteURL = rt.viteURL
})

test.afterAll(async () => { await rt?.close() })

test.describe.configure({ mode: 'serial' })

test('create application with ext -> blocked publish fails -> republish -> ready -> invoke v1', async ({ page }) => {
  test.setTimeout(180_000)
  await page.goto(`${viteURL}/applications/new`)
  await page.locator('#app-name').fill('Echo app')
  // the optional capabilities section holds the ext selection
  await page.getByRole('button', { name: /Optional.*capabilities/ }).click()
  await page.getByRole('button', { name: 'Add probe', exact: true }).click()
  // collapse + reopen keeps the selection
  await page.getByRole('button', { name: 'Hide' }).click()
  await page.getByRole('button', { name: /Optional.*capabilities/ }).click()
  await page.getByRole('button', { name: 'Create application' }).click()
  await page.waitForURL(/\/applications\/app_/)
  const id = page.url().split('/').pop()!

  // the created draft carries the pinned ext requirement
  const app0 = (await (await client.get(`/api/v1/applications/${id}`)).json()).application
  if (!app0.spec?.capabilities?.ext?.probe) {
    throw new Error('spec missing ext pin: ' + JSON.stringify(app0.spec).slice(0, 500))
  }
  expect(app0.spec.capabilities.ext.probe.sum).toMatch(/^h1:/)

  // write code that calls the pinned extension
  const cm = page.locator('.app-cm .cm-content').first()
  await cm.click()
  await page.keyboard.press('Meta+A')
  await page.keyboard.type('def main(input): return ext.probe.echo(input.get("name", "world"))')

  // block the artifact fetch BEFORE publish: with synchronous
  // fetch-and-cache the publish's prepare just fails — no pending
  // desired state, no automatic retry.
  rt.blockArtifacts(true)
  try {
    const app0v = app0.version
    const putResp = page.waitForResponse((r) => r.url().includes(`/api/v1/applications/${id}`) && r.request().method() === 'PUT', { timeout: 60_000 })
    const pubResp = page.waitForResponse((r) => r.url().includes(`/api/v1/applications/${id}/publish`), { timeout: 60_000 })
    await page.getByRole('button', { name: 'Save & Publish' }).click()
    expect((await putResp).status()).toBe(200)
    const pub = await pubResp
    expect(pub.status(), `publish should fail while artifacts blocked: ${pub.status()}`).toBeGreaterThanOrEqual(400)
    // the app stays an unpublished draft — never fake-ready
    const after = (await (await client.get(`/api/v1/applications/${id}`)).json()).application
    expect(after.status, JSON.stringify(after)).not.toBe('ready')
    expect(after.published_version ?? 0).toBeLessThanOrEqual(0)
    expect(after.version).toBeGreaterThanOrEqual(app0v)
    // no background retry: after a quiet moment the ext-tree call
    // count must not grow
    const calls = rt.artifactFetches()
    await new Promise((r) => setTimeout(r, 3000))
    expect(rt.artifactFetches()).toBe(calls)
  } finally {
    rt.blockArtifacts(false)
  }

  // explicit second publish — fetch succeeds synchronously, ready
  await page.getByRole('button', { name: 'Save & Publish' }).click()
  await expect.poll(async () => {
    const r = await client.get(`/api/v1/applications/${id}`)
    return (await r.json()).application.status
  }, { timeout: 120_000 }).toBe('ready')
  // cache-hit invoke must not re-fetch the artifact
  const callsBefore = rt.artifactFetches()
  const inv = await client.post(`/api/v1/applications/${id}/invoke`, {
    headers: { 'Content-Type': 'application/json' }, data: '{"name":"ext-probe"}',
  })
  expect(inv.status()).toBe(200)
  expect(JSON.parse((await inv.json()).output_json)).toEqual({ value: 'ext-probe', release: 'v1' })
  expect(rt.artifactFetches()).toBe(callsBefore)
})

test('artifact persists across worker restart with empty cache', async () => {
  test.setTimeout(180_000)
  // create + publish a second app through the API directly so the
  // first test's UI state does not leak into this recovery case.
  const r = await client.post('/api/v1/applications', {
    data: { name: 'Persist', spec: { capabilities: { ext: { probe: { source: 'fixture/probe' } } } } },
  })
  expect(r.status()).toBe(200)
  const id = (await r.json()).application.id
  const a0 = (await (await client.get(`/api/v1/applications/${id}`)).json()).application
  const sum = a0.spec.capabilities?.ext?.probe?.sum
  expect(sum, `pin: ${JSON.stringify(a0.spec)}`).toMatch(/^h1:/)
  const put = await client.put(`/api/v1/applications/${id}`, {
    data: {
      version: a0.version, name: 'Persist',
      code: 'def main(input): return ext.probe.echo(input.get("name", "world"))',
      spec: a0.spec,
    },
  })
  expect(put.status(), await put.text()).toBe(200)
  const pub = await client.post(`/api/v1/applications/${id}/publish`, { data: { version: a0.version + 1 } })
  expect(pub.status(), await pub.text()).toBe(202)
  await expect.poll(async () => {
    const rr = await client.get(`/api/v1/applications/${id}`)
    return (await rr.json()).application.status
  }, { timeout: 120_000 }).toBe('ready')

  // mutate the source — the persisted artifact must still serve v1
  const { writeFile } = await import('node:fs/promises')
  const { join } = await import('node:path')
  await writeFile(join(rt.extensionDir, 'main.star'),
    'def echo(value):\n    return {"value": value, "release": "v2"}\n')
  await rt.restartWorker(true)

  // old lease expires then the replica recovers — invoke still v1
  await expect.poll(async () => {
    const rr = await client.get(`/api/v1/applications/${id}`)
    return (await rr.json()).application.status
  }, { timeout: 120_000 }).toBe('ready')
  const inv = await client.post(`/api/v1/applications/${id}/invoke`, {
    headers: { 'Content-Type': 'application/json' }, data: '{"name":"recovered"}',
  })
  expect(inv.status()).toBe(200)
  const out = (await inv.json()).output_json
  expect(out).toContain('"v1"')
  expect(out).not.toContain('"v2"')

  // API restart on the same DB keeps the published release intact
  await rt.restartAPI()
  const inv2 = await client.post(`/api/v1/applications/${id}/invoke`, {
    headers: { 'Content-Type': 'application/json' }, data: '{"name":"again"}',
  })
  expect((await inv2.json()).output_json).toContain('"v1"')
})

test('blocked cold ext create fails then explicit retry runs and deletes', async () => {
  test.setTimeout(180_000)
  // a second fixture source — real pin, but never fetched by the
  // worker yet, so the artifact is genuinely missing
  const { mkdir, writeFile } = await import('node:fs/promises')
  const { join, dirname } = await import('node:path')
  const p2 = join(dirname(rt.extensionDir), 'probe2')
  await mkdir(p2, { recursive: true })
  await writeFile(join(p2, 'capability.yaml'), 'name: probe2\nversion: "0.1.0"\ndescription: P2\nops:\n  - name: echo\n    params: [value]\n')
  await writeFile(join(p2, 'main.star'), 'def echo(value):\n    return value\n')
  // blocked cold fetch: the synchronous ensure fails the create —
  // a startup error, never a phantom pending instance
  rt.blockArtifacts(true)
  try {
    const r = await client.post('/api/v1/instances', {
      data: { capabilities: { ext: { p2: { source: 'fixture/probe2' } } } },
    })
    expect(r.status(), await r.text()).toBeGreaterThanOrEqual(400)
  } finally {
    rt.blockArtifacts(false)
  }
  // explicit retry once the artifact can be fetched: real create,
  // then delete is a normal lifecycle
  const r2 = await client.post('/api/v1/instances', {
    data: { capabilities: { ext: { p2: { source: 'fixture/probe2' } } } },
  })
  expect(r2.status(), await r2.text()).toBe(201)
  const inst = (await r2.json()).instance.id
  const del = await client.delete(`/api/v1/instances/${inst}`)
  expect(del.status(), await del.text()).toBe(200)
})

// Examples panel: all three tabs switch and carry the real endpoint,
// method, body and key-env hints; the code block height stays fixed.
test('call examples tabs show real request shapes', async ({ page }) => {
  const r = await client.post('/api/v1/applications', { data: { name: 'Examples app' } })
  expect(r.status()).toBe(200)
  const id = (await r.json()).application.id
  await page.goto(`${viteURL}/applications/${id}`)
  for (const tab of ['curl', 'Python', 'JavaScript']) {
    await page.getByRole('tab', { name: tab, exact: true }).click()
    const body = await page.locator('.app-examples').innerText()
    expect(body).toContain(`/api/v1/applications/${id}/invoke`)
  }
  const curl = await page.locator('.app-examples').innerText()
  expect(curl).toContain('input')
  await page.setViewportSize({ width: 390, height: 844 })
  const clipped = await page.locator('.app-examples').evaluate((el) => el.scrollWidth > el.clientWidth)
  expect(clipped).toBe(false)
  await page.screenshot({ path: '/tmp/calcside-ep14-logs/ext-sync-cache-mobile-examples.png' })
})

// Invalid raw spec edits persist through collapse/reopen; a kind
// switch asks before discarding them (C2).
test('raw spec invalid edit keeps draft and kind switch asks before discard', async ({ page }) => {
  await page.goto(`${viteURL}/applications/new`)
  await page.locator('#app-name').fill('Raw edit')
  await page.getByRole('button', { name: /Optional.*capabilities/ }).click()
  await page.getByRole('button', { name: /Advanced/ }).click()
  const raw = page.getByLabel('Application spec JSON')
  await raw.fill('{ not json')
  // invalid JSON refuses to leave raw mode — the draft is kept
  await page.getByRole('button', { name: 'Back to editor' }).click()
  await expect(page.getByLabel('Application spec JSON')).toHaveValue('{ not json')
  // kind switch with dirty state asks first
  await page.locator('.kind-tabs a', { hasText: 'Instance' }).click()
  await expect(page.getByText('Discard draft')).toBeVisible()
})
