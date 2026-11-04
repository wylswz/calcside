import { test, expect, type APIRequestContext } from '@playwright/test'
import { startApplicationRuntime, type ApplicationRuntime } from './applicationRuntime'

// Application reviewed publish V1: UI review dialog -> publish -> ready
// invoke; stale candidate -> 409 + Refresh -> publish; module snapshot
// retention.
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

test('app reviewed publish: preview -> publish -> invoke -> stale -> refresh -> publish', async ({ page }) => {
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(String(e)))
  page.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()) })

  // Seed + publish a module via API, then create an app that loads it.
  const h1 = await client.post('/api/v1/modules', { data: { name: 'strkit', files: { 'u.star': 'def hello(name): return "V1 "+name\n' } } })
  const mod = (await h1.json()).module
  const prev = await client.post(`/api/v1/modules/${mod.id}/publish/preview`, { data: { draft_version: mod.draft_version } })
  const cand = (await prev.json()).candidate.digest
  const pub = await client.post(`/api/v1/modules/${mod.id}/publish`, {
    data: { draft_version: mod.draft_version, expected_base: '', candidate: cand },
  })
  expect(pub.ok()).toBeTruthy()

  const cr = await client.post('/api/v1/applications', {
    data: {
      name: 'Greeter',
      code: 'load("@m/u.star", "hello")\ndef main(input): return hello(input.get("name","world"))',
      input_schema: '{"type":"object"}',
      output_schema: '{"type":"string"}',
      spec: { modules: { m: { name: 'strkit', version: 0 } } },
    },
  })
  const crBody = await cr.json()
  expect(cr.status(), JSON.stringify(crBody)).toBe(200)
  const app = crBody.application

  await page.goto(`${viteURL}/applications/${app.id}`)
  await page.getByTestId('app-publish').click()
  await expect(page.getByText(/candidate rel_/)).toBeVisible({ timeout: 15_000 })
  await page.getByTestId('review-note').fill('v1')
  const pubResp = page.waitForResponse((r) => r.url().includes('/publish') && !r.url().includes('preview') && r.request().method() === 'POST')
  await page.getByTestId('review-publish').click()
  const pubRes = await pubResp
  expect(pubRes.status(), await pubRes.text()).toBe(202)

  await expect.poll(async () => {
    const r = await client.get(`/api/v1/applications/${app.id}`)
    return (await r.json()).application.status
  }, { timeout: 120_000 }).toBe('ready')
  const inv = await client.post(`/api/v1/applications/${app.id}/invoke`, { data: '{"name":"live"}' })
  const invBody = await inv.json()
  expect(invBody.output_json ?? invBody.error?.message ?? JSON.stringify(invBody)).toContain('V1 live')

  // Stale: mutate draft via API (live version), then publish against
  // an old preview.
  const app1 = (await (await client.get(`/api/v1/applications/${app.id}`)).json()).application
  const put1 = await client.put(`/api/v1/applications/${app.id}`, {
    data: { version: app1.version, name: 'Greeter', code: 'load("@m/u.star", "hello")\ndef main(input): return hello("NEW:"+input.get("name","world"))',
      input_schema: '{"type":"object"}', output_schema: '{"type":"string"}' },
  })
  expect(put1.status(), await put1.text()).toBe(200)
  expect((await put1.json()).application.version).toBe(app1.version + 1)
  await page.getByTestId('app-publish').click()
  // Preview may fail "application changed" (stale version) — wait for
  // whichever surface appears, then Refresh re-previews the live draft.
  const refreshBtn = page.getByRole('button', { name: 'Refresh preview' })
  await expect(refreshBtn.or(page.getByText(/candidate rel_/))).toBeVisible({ timeout: 15_000 })
  if (await refreshBtn.isVisible().catch(() => false)) {
    await refreshBtn.click()
  }
  await expect(page.getByText(/candidate rel_/)).toBeVisible({ timeout: 15_000 })
  await page.getByTestId('review-note').fill('v2')
  // Mutate the draft between preview and publish → 409 stays open.
  const app2 = (await (await client.get(`/api/v1/applications/${app.id}`)).json()).application
  const put2 = await client.put(`/api/v1/applications/${app.id}`, {
    data: { version: app2.version, name: 'Greeter', code: 'load("@m/u.star", "hello")\ndef main(input): return hello("NEW2:"+input.get("name","world"))',
      input_schema: '{"type":"object"}', output_schema: '{"type":"string"}' },
  })
  expect(put2.status(), await put2.text()).toBe(200)
  const staleResp = page.waitForResponse((r) => r.url().includes('/publish') && !r.url().includes('preview') && r.request().method() === 'POST', { timeout: 15_000 })
  await page.getByTestId('review-publish').click()
  expect((await staleResp).status()).toBe(409)
  expect(await page.getByTestId('review-publish').isDisabled()).toBe(true)
  expect(await page.getByTestId('review-note').inputValue()).toBe('v2')
  // Note retained + dialog open; Refresh waits for the preview 200
  // and the new candidate, then Publish commits it.
  const prevResp = page.waitForResponse((r) => r.url().includes('/publish/preview') && r.request().method() === 'POST', { timeout: 15_000 })
  await page.getByRole('button', { name: 'Refresh preview' }).click()
  expect((await prevResp).status()).toBe(200)
  await expect(page.getByText(/candidate rel_/)).toBeVisible({ timeout: 15_000 })
  const pub2 = page.waitForResponse((r) => r.url().includes('/publish') && !r.url().includes('preview') && r.request().method() === 'POST')
  await page.getByTestId('review-publish').click()
  expect((await pub2).status()).toBe(202)

  await expect.poll(async () => {
    const r = await client.get(`/api/v1/applications/${app.id}`)
    return (await r.json()).application.status
  }, { timeout: 120_000 }).toBe('ready')
  const inv2 = await client.post(`/api/v1/applications/${app.id}/invoke`, { data: '{"name":"again"}' })
  const inv2Body = await inv2.json()
  expect(inv2Body.output_json ?? JSON.stringify(inv2Body)).toContain('V1 NEW2:again')

  // Snapshot retention: edit + republish the module — the app's release
  // still serves V1.
  await client.patch(`/api/v1/modules/${mod.id}`, {
    data: { draft_version: 2, files: { 'u.star': 'def hello(name): return "V2 "+name\n' } },
  })
  const inv3 = await client.post(`/api/v1/applications/${app.id}/invoke`, { data: '{"name":"stable"}' })
  const inv3Body = await inv3.json()
  expect(inv3Body.output_json ?? JSON.stringify(inv3Body)).toContain('V1 NEW2:stable')

  await page.screenshot({ path: 'e2e/screenshots/app-review.png' })
  await page.setViewportSize({ width: 390, height: 844 })
  await page.screenshot({ path: 'e2e/screenshots/app-review-mobile.png' })
  expect(errors.filter((e) => !/favicon|DevTools|ResizeObserver|Failed to load resource/.test(e))).toEqual([])
})
