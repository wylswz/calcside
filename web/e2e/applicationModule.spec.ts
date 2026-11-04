import { test, expect, type APIRequestContext } from '@playwright/test'
import { startApplicationRuntime, type ApplicationRuntime } from './applicationRuntime'

// Application module mounts end to end: a published workspace module is
// added through the Settings library, pinned {name,version,sum} in the
// draft spec, loaded via @alias in Run and the published release — a
// later library publish never moves the pinned reference, and removal
// drops only the named mount (F2).
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

async function publishModule(name: string, files: Record<string, string>) {
  const cr = await client.post('/api/v1/modules', { data: { name, files } })
  expect(cr.status(), JSON.stringify(await cr.json())).toBe(200)
  const mod = (await cr.json()).module
  const prev = await client.post(`/api/v1/modules/${mod.id}/publish/preview`, { data: { draft_version: mod.draft_version } })
  expect(prev.ok(), JSON.stringify(await prev.json())).toBeTruthy()
  const candidate = (await prev.json()).candidate.digest
  const pub = await client.post(`/api/v1/modules/${mod.id}/publish`, {
    data: { draft_version: mod.draft_version, expected_base: '', candidate },
  })
  expect(pub.ok(), JSON.stringify(await pub.json())).toBeTruthy()
  const fresh = await client.get(`/api/v1/modules/${mod.id}`)
  return (await fresh.json()).module
}

async function appSpec(id: string) {
  const r = await client.get(`/api/v1/applications/${id}`)
  const body = await r.json()
  return JSON.parse(body.application.spec_json || '{}')
}

test('application mounts a published module through Settings', async ({ page }) => {
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(String(e)))

  const hello = await publishModule('apphello', { 'main.star': 'def greet(name):\n    return "hello " + name\n' })
  await publishModule('apphelp2', { 'util.star': 'def twice(x):\n    return x + x\n' })
  // Unpublished modules are listed but cannot be mounted.
  const dr = await client.post('/api/v1/modules', { data: { name: 'appdraft', files: { 'main.star': 'x = 1\n' } } })
  expect(dr.status()).toBe(200)

  await page.goto(`${viteURL}/applications/new`)
  await page.getByRole('button', { name: 'Continue to configuration' }).click()
  await expect(page.getByRole('button', { name: 'Add module appdraft' })).toBeDisabled()
  await page.getByRole('button', { name: 'Add module apphello' }).click()
  await page.getByRole('button', { name: 'Add module apphelp2' }).click()
  // Adding the same module again only points at the existing row.
  await page.getByRole('button', { name: 'Added apphello, show settings' }).click()
  await expect(page.locator('[data-row="mod:apphello"]')).toHaveCount(1)
  await expect(page.locator('[data-row="mod:apphelp2"]')).toHaveCount(1)

  // Removal asks by name, leaves a placeholder, and Undo restores it.
  await page.getByRole('button', { name: 'Remove module apphelp2' }).click()
  await expect(page.getByRole('dialog')).toContainText('apphelp2')
  await page.getByRole('dialog').getByRole('button', { name: 'Remove' }).click()
  await expect(page.locator('[data-row="mod:apphelp2"]')).toHaveCount(0)
  await page.getByRole('button', { name: 'Undo' }).click()
  await expect(page.locator('[data-row="mod:apphelp2"]')).toHaveCount(1)

  await page.getByRole('button', { name: 'Create application' }).click()
  await page.waitForURL(/\/applications\/app_/)
  const id = page.url().split('/').pop()!

  // The saved draft pins name/version/sum — never an upgrade hook.
  await expect.poll(async () => JSON.stringify((await appSpec(id)).modules ?? {}), { timeout: 15_000 }).toContain('apphello')
  const spec0 = await appSpec(id)
  expect(spec0.modules.apphello.name).toBe('apphello')
  expect(spec0.modules.apphello.version).toBe(hello.latest_version)
  expect(spec0.modules.apphello.sum).toMatch(/^h1:/)

  // Reload keeps the visual mount.
  await page.reload()
  await expect(page.locator('[data-row="mod:apphello"]')).toHaveCount(1)

  // The draft run resolves @apphello.
  const cm = page.locator('.app-cm .cm-content').first()
  await cm.click()
  await page.keyboard.press('Meta+A')
  await page.keyboard.insertText('load("@apphello/main.star", "greet")\ndef main(input):\n    return {"message": greet(input["name"])}\n')
  await page.getByRole('button', { name: 'Run', exact: true }).click()
  await expect(page.locator('.app-out-pre:not(.app-call-out)')).toContainText('hello world', { timeout: 60_000 })

  // Publish + invoke serve the pinned module too.
  await page.getByRole('button', { name: 'Publish…' }).click()
  await page.getByTestId('review-publish').click()
  await expect.poll(async () => {
    const r = await client.get(`/api/v1/applications/${id}`)
    return (await r.json()).application.status
  }, { timeout: 90_000 }).toBe('ready')
  const inv = await client.post(`/api/v1/applications/${id}/invoke`, { data: { name: 'live' } })
  expect(String((await inv.json()).output_json)).toContain('hello live')

  // A later library publish must not move the app reference.
  const modNow = await client.get(`/api/v1/modules/${hello.id}`)
  const dv = (await modNow.json()).module.draft_version
  await client.patch(`/api/v1/modules/${hello.id}`, {
    data: { draft_version: dv, files: { 'main.star': 'def greet(name):\n    return "hello2 " + name\n' } },
  })
  const prev2 = await client.post(`/api/v1/modules/${hello.id}/publish/preview`, { data: { draft_version: dv + 1 } })
  const cand2 = (await prev2.json()).candidate.digest
  const pub2 = await client.post(`/api/v1/modules/${hello.id}/publish`, {
    data: { draft_version: dv + 1, expected_base: spec0.modules.apphello.sum, candidate: cand2 },
  })
  expect(pub2.ok(), JSON.stringify(await pub2.json())).toBeTruthy()
  const spec1 = await appSpec(id)
  expect(spec1.modules.apphello.version).toBe(hello.latest_version)
  expect(spec1.modules.apphello.sum).toBe(spec0.modules.apphello.sum)
  const inv2 = await client.post(`/api/v1/applications/${id}/invoke`, { data: { name: 'still' } })
  expect(String((await inv2.json()).output_json)).toContain('hello still')

  // A confirmed removal drops only the named mount.
  await page.getByRole('button', { name: 'Remove module apphelp2' }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Remove' }).click()
  await expect.poll(async () => JSON.stringify((await appSpec(id)).modules ?? {}), { timeout: 15_000 })
    .not.toContain('apphelp2')
  expect((await appSpec(id)).modules.apphello).toBeTruthy()

  await page.screenshot({ path: 'e2e/screenshots/application-module.png' })
  await page.setViewportSize({ width: 390, height: 844 })
  await page.screenshot({ path: 'e2e/screenshots/application-module-mobile.png' })
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth)
  expect(overflow).toBe(false)
  expect(errors.filter((e) => !/favicon|DevTools|ResizeObserver|Failed to load resource/.test(e))).toEqual([])
})
