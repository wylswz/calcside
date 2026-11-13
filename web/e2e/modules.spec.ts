import { test, expect } from '@playwright/test'
import { startApplicationRuntime } from './applicationRuntime'

// Module V1: create → edit source → save → debug run @self → preview +
// publish → instance loads the published module.
let viteURL = ''
let rt: Awaited<ReturnType<typeof startApplicationRuntime>>

test.beforeAll(async () => {
  rt = await startApplicationRuntime()
  viteURL = rt.viteURL
})
test.afterAll(async () => { await rt.close() })

test('module create → source edit → debug run → publish → instance use', async ({ page }) => {
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(String(e)))
  page.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()) })

  await page.goto(`${viteURL}/modules`)
  await page.getByRole('button', { name: 'New module' }).click()
  await page.getByPlaceholder('strkit').fill('strkit')
  await page.getByRole('button', { name: 'Create' }).click()
  await page.waitForURL(/\/modules\/mod_/)
  const id = page.url().split('/').pop()!

  // Workbench: source, caller script, stdout and runtime config are
  // all visible on one page (no tabs).
  await expect(page.getByTestId('module-editor')).toBeVisible()
  await expect(page.getByTestId('debug-script')).toBeVisible()
  await expect(page.getByTestId('debug-result')).toBeVisible()
  await expect(page.getByText('Extensions & runtime')).toBeVisible()

  // Edit source via CodeMirror to a recognisable marker, save.
  const fillCM = async (testid: string, text: string) => {
    const cm = page.getByTestId(testid).locator('.cm-content')
    await cm.click()
    await page.keyboard.press('Meta+A')
    await page.keyboard.press('Delete')
    await page.keyboard.insertText(text)
  }
  await fillCM('module-editor', 'def hello(name):\n    return "V1 hello " + name\n')
  // autosave: wait for the edit to persist
  await expect.poll(async () => {
    const g = await rt.client.get(`/api/v1/modules/${id}`)
    return (await g.json()).module.files['main.star']
  }, { timeout: 15_000 }).toContain('V1 hello')
  await expect(page.getByText(/Unsaved draft changes/)).toHaveCount(0, { timeout: 10_000 })

  // Caller script: print via @self and run.
  await fillCM('debug-script', 'load("@self/main.star", "hello")\nprint(hello("world"))')
  await page.getByTestId('debug-run').click()
  await expect(page.getByTestId('debug-result')).toContainText('V1 hello world', { timeout: 60_000 })

  // Modify script output, re-run.
  await fillCM('debug-script', 'load("@self/main.star", "hello")\nprint("again: " + hello("you"))')
  await page.getByTestId('debug-run').click()
  await expect(page.getByTestId('debug-result')).toContainText('again: V1 hello you', { timeout: 60_000 })

  // Publish through the review dialog.
  await page.getByTestId('module-publish').click()
  await expect(page.getByText(/candidate h1:/)).toBeVisible({ timeout: 10_000 })
  await page.getByTestId('review-note').fill('v1')
  await page.getByTestId('review-publish').click()
  await expect(page.getByRole('button', { name: 'Publish…' })).toBeVisible({ timeout: 15_000 })

  // API: create an instance loading the published module, exec it.
  const create = await rt.client.post('/api/v1/instances', {
    data: { modules: { m: { name: 'strkit', version: 1 } } },
  })
  expect(create.ok()).toBeTruthy()
  const inst = (await create.json()).instance
  expect(inst.spec?.modules?.m?.sum).toMatch(/^h1:/)
  const exec = await rt.client.post(`/api/v1/instances/${inst.id}/exec`, {
    data: { code: 'load("@m/main.star", "hello")\nprint(hello("live"))' },
  })
  expect(exec.ok()).toBeTruthy()
  const body = await exec.json()
  expect(JSON.stringify(body)).toContain('V1 hello live')

  // Snapshot retention: modify the draft WITHOUT republish — the
  // instance still runs V1; publish v2 → new instance sees V2; delete
  // the library module → existing instance still V1.
  await rt.client.patch(`/api/v1/modules/${id}`, {
    data: { draft_version: (await (await rt.client.get(`/api/v1/modules/${id}`)).json()).module.draft_version, files: { 'main.star': 'def hello(name):\n    return "V2 hello " + name\n' } },
  })
  const exec1 = await rt.client.post(`/api/v1/instances/${inst.id}/exec`, {
    data: { code: 'load("@m/main.star", "hello")\nprint(hello("live"))' },
  })
  expect(JSON.stringify(await exec1.json())).toContain('V1 hello live')
  // Publish v2 via API (UI publish already proven above).
  const modNow = await rt.client.get(`/api/v1/modules/${id}`)
  const dv2 = (await modNow.json()).module.draft_version
  const prev = await rt.client.post(`/api/v1/modules/${id}/publish/preview`, { data: { draft_version: dv2 } })
  expect(prev.ok(), JSON.stringify(await prev.json())).toBeTruthy()
  const prevBody = await prev.json()
  const candidate = prevBody.candidate.digest
  const pub = await rt.client.post(`/api/v1/modules/${id}/publish`, {
    data: { draft_version: dv2, expected_base: inst.spec?.modules?.m?.sum ?? '', candidate },
  })
  expect(pub.ok()).toBeTruthy()
  const create2 = await rt.client.post('/api/v1/instances', {
    data: { modules: { m: { name: 'strkit', version: 2 } } },
  })
  const inst2 = (await create2.json()).instance
  const exec2 = await rt.client.post(`/api/v1/instances/${inst2.id}/exec`, {
    data: { code: 'load("@m/main.star", "hello")\nprint(hello("v2"))' },
  })
  expect(JSON.stringify(await exec2.json())).toContain('V2 hello v2')
  // Delete the library module — the captured snapshot still serves V1.
  const del = await rt.client.delete(`/api/v1/modules/${id}`)
  expect(del.status()).toBe(204)
  const gone = await rt.client.get(`/api/v1/modules/${id}`)
  expect(gone.status()).toBe(404)
  const exec3 = await rt.client.post(`/api/v1/instances/${inst.id}/exec`, {
    data: { code: 'load("@m/main.star", "hello")\nprint(hello("live"))' },
  })
  expect(JSON.stringify(await exec3.json())).toContain('V1 hello live')

  await page.screenshot({ path: 'e2e/screenshots/module-detail.png' })
  await page.setViewportSize({ width: 390, height: 844 })
  await page.screenshot({ path: 'e2e/screenshots/module-detail-mobile.png' })
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth)
  expect(overflow).toBe(false)
  expect(errors.filter((e) => !/favicon|DevTools|ResizeObserver|Failed to load resource/.test(e))).toEqual([])
})

test('create modal validates the unique name and keeps input on conflict', async ({ page }) => {
  // Seed a module with the colliding name via API.
  const cr = await rt.client.post('/api/v1/modules', {
    data: { name: 'dupmod', files: { 'main.star': 'x = 1\n' } },
  })
  expect(cr.status()).toBe(200)

  await page.goto(`${viteURL}/modules`)
  await page.getByRole('button', { name: 'New module' }).click()
  const input = page.getByLabel('Unique name')
  // Grammar violation → the error notice carries the name help; no request fires.
  await input.fill('bad name!')
  await page.getByRole('button', { name: 'Create' }).click()
  await expect(page.getByText(/Workspace-unique name/)).toHaveCount(1)
  // Duplicate → the server conflict message; the modal keeps the
  // entered name so a rename retry is one edit (C2/P5).
  await input.fill('dupmod')
  await page.getByRole('button', { name: 'Create' }).click()
  await expect(page.getByText(/already exists in this workspace/)).toBeVisible({ timeout: 10_000 })
  await expect(input).toHaveValue('dupmod')
})
