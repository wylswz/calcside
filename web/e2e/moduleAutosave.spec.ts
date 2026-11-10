import { test, expect } from '@playwright/test'
import { startApplicationRuntime, type ApplicationRuntime } from './applicationRuntime'
import { mkdir, writeFile } from 'node:fs/promises'
import { join } from 'node:path'

test.setTimeout(120_000)

let rt: ApplicationRuntime
test.beforeAll(async () => {
  rt = await startApplicationRuntime()
  const dir = join(rt.extensionDir, '..', '..', 'needkey', '0.1.0')
  await mkdir(dir, { recursive: true })
  await writeFile(join(dir, 'capability.yaml'), 'name: needkey\nversion: "0.1.0"\nops: [{name: check, params: []}]\nconfig:\n  - {name: api_key, type: secret}\n')
  await writeFile(join(dir, 'main.star'), 'def check():\n    return config["api_key"] != ""\n')
})
test.afterAll(async () => { await rt?.close() })

test('autosave: delayed PATCH + immediate publish reviews latest code', async ({ page }) => {
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(e.message))
  const cr = await rt.client.post('/api/v1/modules', {
    data: { name: 'amod', files: { 'main.star': 'def hello(name):\n    return "V1 " + name\n' } },
  })
  const id = (await cr.json()).module.id
  await page.goto(`${rt.viteURL}/modules/${id}`)
  await expect(page.getByRole('button', { name: 'Save draft' })).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Save settings' })).toHaveCount(0)

  // Hold the PATCH until we release it — no heuristic delay.
  let release!: () => void
  const gate = new Promise<void>((res) => { release = res })
  let received!: () => void
  const firstPatch = new Promise<void>((res) => { received = res })
  const writes: { draft_version: number; files: Record<string, string> }[] = []
  await page.route('**/api/v1/modules/*', async (r) => {
    if (r.request().method() === 'PATCH') {
      writes.push(r.request().postDataJSON())
      if (writes.length === 1) { received(); await gate }
    }
    await r.continue()
  })
  const previews: string[] = []
  page.on('request', (r) => { if (r.url().includes('/publish/preview')) previews.push(r.method()) })

  const cm = page.getByTestId('module-editor').locator('.cm-content')
  await cm.click(); await page.keyboard.press('Meta+A'); await page.keyboard.insertText('x = "LATEST"')
  await firstPatch
  // edit AGAIN while first PATCH pending — serial queue keeps order
  try {
    await cm.click(); await page.keyboard.press('Meta+A'); await page.keyboard.insertText('x = "LATEST2"')
    await page.getByTestId('module-publish').click()
    await expect(page.getByTestId('module-publish')).toContainText('Preparing')
    expect(previews).toHaveLength(0)
    release()
    const dlg = page.getByRole('dialog')
    await expect(dlg).toContainText('LATEST2', { timeout: 15_000 })
    expect(writes.map((w) => w.draft_version)).toEqual([1, 2])
    expect(writes[1].files['main.star']).toBe('x = "LATEST2"')
    await dlg.getByTestId('review-publish').click()
    await expect.poll(async () => (await (await rt.client.get(`/api/v1/modules/${id}`)).json()).module.latest_version).toBe(1)
    const published = await rt.client.get(`/api/v1/modules/${id}/versions/1`)
    expect((await published.json()).version.files['main.star']).toBe('x = "LATEST2"')
    expect(errors).toEqual([])
  } finally { release() }
})

test('autosave: dep-only add publishes without debug creds', async ({ page }) => {
  // REAL fixture extension — manifest lives on the API's ext root.
  const cr = await rt.client.post('/api/v1/modules', {
    data: { name: 'depmod', files: { 'main.star': 'x = 1' } },
  })
  const id = (await cr.json()).module.id
  await page.goto(`${rt.viteURL}/modules/${id}`)
  await expect(page.getByTestId('module-deps')).toBeVisible()
  await page.getByTestId('module-deps').getByRole('button', { name: 'Add needkey' }).click()
  await expect.poll(async () => {
    const g = await rt.client.get(`/api/v1/modules/${id}`)
    return JSON.stringify((await g.json()).module.requirements ?? {})
  }, { timeout: 15_000 }).toContain('fixture/needkey')
  const g = await rt.client.get(`/api/v1/modules/${id}`)
  const body = await g.json()
  expect(body.module.files['main.star']).toBe('x = 1')
  const dep = body.module.requirements.ext.needkey as { source: string; sum?: string }
  expect(dep, 'dep missing from requirements').toBeTruthy()
  expect(dep!.sum).toMatch(/^h1:/)
  // source+sum only — never config/env/refs
  expect(Object.keys(dep!).sort()).toEqual(['source', 'sum'])
  expect(JSON.stringify(body.module.files)).not.toMatch(/api_key|environment|secrets/)
  // Review shows the dependency and publishes without any debug creds.
  await page.getByTestId('module-publish').click()
  const dlg = page.getByRole('dialog')
  await expect(dlg).toBeVisible({ timeout: 15_000 })
  await expect(dlg).toContainText('requirements/ext/needkey')
  await expect(dlg).toContainText('fixture/needkey')
  await expect(dlg).toContainText(dep.sum!)
  await expect(dlg.getByTestId('review-publish')).toBeEnabled()
  await dlg.getByTestId('review-publish').click()
  await expect.poll(async () => {
    const g2 = await rt.client.get(`/api/v1/modules/${id}`)
    return (await g2.json()).module.latest_version
  }, { timeout: 15_000 }).toBe(1)
  const published = (await (await rt.client.get(`/api/v1/modules/${id}/versions/1`)).json()).version
  expect(published.requirements.ext.needkey).toEqual(dep)
  expect(JSON.stringify(published.files)).not.toMatch(/api_key|environment|secrets|config:/)
})

test('autosave: dep remove publishes the deletion', async ({ page }) => {
  const cr = await rt.client.post('/api/v1/modules', {
    data: { name: 'rmmod', files: { 'main.star': 'x = 1' } },
  })
  const id = (await cr.json()).module.id
  await page.goto(`${rt.viteURL}/modules/${id}`)
  await page.getByTestId('module-deps').getByRole('button', { name: 'Add probe' }).click()
  await expect.poll(async () => {
    const g = await rt.client.get(`/api/v1/modules/${id}`)
    return JSON.stringify((await g.json()).module.requirements ?? {})
  }, { timeout: 15_000 }).toContain('fixture/probe')
  // publish v1 WITH the dep
  await page.getByTestId('module-publish').click()
  const dlg0 = page.getByRole('dialog')
  await expect(dlg0).toBeVisible()
  await dlg0.getByRole('button', { name: /^Publish/ }).click()
  await expect.poll(async () => {
    const g = await rt.client.get(`/api/v1/modules/${id}`)
    return (await g.json()).module.latest_version
  }).toBe(1)
  // remove via UI + Confirm, review shows removal, publish v2
  await page.getByTestId('module-deps').getByRole('button', { name: 'Remove probe' }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Remove' }).click()
  await expect.poll(async () => {
    const g = await rt.client.get(`/api/v1/modules/${id}`)
    return JSON.stringify((await g.json()).module.requirements ?? {})
  }, { timeout: 15_000 }).not.toContain('fixture/probe')
  await page.getByTestId('module-publish').click()
  const dlg = page.getByRole('dialog')
  await expect(dlg).toBeVisible()
  await expect(dlg).toContainText('requirements/ext/probe')
  await expect(dlg).toContainText('removed')
  await dlg.getByRole('button', { name: /^Publish/ }).click()
  await expect.poll(async () => {
    const g = await rt.client.get(`/api/v1/modules/${id}`)
    return (await g.json()).module.latest_version
  }, { timeout: 15_000 }).toBe(2)
  const v1 = (await (await rt.client.get(`/api/v1/modules/${id}/versions/1`)).json()).version
  const v2 = (await (await rt.client.get(`/api/v1/modules/${id}/versions/2`)).json()).version
  expect(v1.requirements.ext.probe.source).toBe('fixture/probe@0.1.0')
  expect(v2.requirements?.ext?.probe).toBeUndefined()
})

test('dep remove: cancel keeps row order, confirm drops only the dep (C2/C3/F2)', async ({ page }) => {
  const cr = await rt.client.post('/api/v1/modules', {
    data: { name: 'ordmod', files: { 'main.star': 'x = 1' } },
  })
  const id = (await cr.json()).module.id
  await page.goto(`${rt.viteURL}/modules/${id}`)
  const deps = page.getByTestId('module-deps')
  // Two fixed deps plus a non-dep capability (Files) so the summary
  // mixes managed and unmanaged rows.
  await deps.getByRole('button', { name: 'Add needkey' }).click()
  await deps.getByRole('button', { name: 'Add probe' }).click()
  await deps.getByRole('button', { name: 'Add Files' }).click()
  const rowOrder = () => deps.locator('aside .selected-list').first().locator('[data-row]').evaluateAll(
    (rows) => rows.map((r) => r.getAttribute('data-row')))
  await expect.poll(rowOrder).toEqual(['ext:0', 'ext:1', 'fs'])

  // Cancelling the named Confirm must not drop or reorder the row.
  await deps.getByRole('button', { name: 'Remove needkey' }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Cancel' }).click()
  await expect(deps.locator('aside .selected-list').first().locator('[data-row]')).toHaveCount(3)
  expect(await rowOrder()).toEqual(['ext:0', 'ext:1', 'fs'])

  // Confirming drops only that dep; the remaining rows keep their slots.
  await deps.getByRole('button', { name: 'Remove needkey' }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Remove' }).click()
  await expect.poll(rowOrder).toEqual(['ext:1', 'fs'])
  await expect.poll(async () => {
    const g = await rt.client.get(`/api/v1/modules/${id}`)
    return JSON.stringify((await g.json()).module.requirements ?? {})
  }, { timeout: 15_000 }).not.toContain('fixture/needkey')
})

test('C2: pending source and caller edits survive SPA navigation', async ({ page }) => {
  const cr = await rt.client.post('/api/v1/modules', { data: { name: 'recovermod', files: { 'main.star': 'x = 1' } } })
  const id = (await cr.json()).module.id
  const url = `${rt.viteURL}/modules/${id}`
  const fill = async (testId: string, value: string) => {
    await page.getByTestId(testId).locator('.cm-content').click()
    await page.keyboard.press('Meta+A')
    await page.keyboard.insertText(value)
  }
  await page.goto(url)
  await fill('module-editor', 'x = "RECOVERED_SOURCE"')
  await page.getByRole('link', { name: 'Modules', exact: true }).click()
  await page.goto(url)
  await expect(page.getByTestId('module-editor')).toContainText('RECOVERED_SOURCE')
  await expect.poll(async () => (await (await rt.client.get(`/api/v1/modules/${id}`)).json()).module.files['main.star']).toContain('RECOVERED_SOURCE')
  await fill('debug-script', 'print("RECOVERED_CALLER")')
  await page.getByRole('link', { name: 'Modules', exact: true }).click()
  await page.goto(url)
  await expect(page.getByTestId('debug-script')).toContainText('RECOVERED_CALLER')
  await expect.poll(async () => (await (await rt.client.get(`/api/v1/modules/${id}/debug`)).json()).debug.script).toContain('RECOVERED_CALLER')
})
