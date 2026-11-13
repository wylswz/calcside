import { test, expect } from '@playwright/test'
import { startApplicationRuntime, type ApplicationRuntime } from './applicationRuntime'

let rt: ApplicationRuntime
test.beforeAll(async () => {
  test.setTimeout(300_000)
  rt = await startApplicationRuntime()
})
test.afterAll(async () => { await rt?.close() })

test('workbench resources use pinned files; views retain editor and input (C2/C3/C4/F3)', async ({ page }) => {
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(e.message))
  const cr = await rt.client.post('/api/v1/modules', {
    data: { name: 'worklib', files: { 'lib/util.star': 'def greet(name):\n    return name\n' } },
  })
  expect(cr.ok()).toBeTruthy()
  const mod = (await cr.json()).module
  const preview = await rt.client.post(`/api/v1/modules/${mod.id}/publish/preview`, { data: { draft_version: 1 } })
  const review = await preview.json()
  const pub = await rt.client.post(`/api/v1/modules/${mod.id}/publish`, {
    data: { draft_version: 1, expected_base: '', candidate: review.candidate.digest },
  })
  expect(pub.ok()).toBeTruthy()
  const published = (await (await rt.client.get(`/api/v1/modules/${mod.id}`)).json()).module
  await rt.client.patch(`/api/v1/modules/${mod.id}`, {
    data: { draft_version: 1, files: { 'draft_only.star': 'x = 2\n' } },
  })
  const ar = await rt.client.post('/api/v1/applications', {
    data: {
      name: 'Resource workbench',
      spec: {
        capabilities: { ext: { helper: { source: 'fixture/probe@0.1.0' } } },
        modules: { toolkit: { name: 'worklib', version: 1, sum: published.latest_sum } },
      },
    },
  })
  expect(ar.ok(), await ar.text()).toBeTruthy()
  const id = (await ar.json()).application.id
  await page.goto(`${rt.viteURL}/applications/${id}`)
  const tabs = page.getByRole('tablist', { name: 'Application views' })
  const resources = page.getByTestId('application-resources')
  await expect(resources).toBeVisible()
  await resources.locator('summary').filter({ hasText: 'ext.helper' }).click()
  await resources.getByRole('button', { name: 'echo', exact: true }).click()
  await expect(page.getByTestId('resource-document').getByText('ext.helper.echo(value)', { exact: true })).toBeVisible()
  await expect(resources.getByText('ext.helper.echo(value)', { exact: true })).toHaveCount(0)
  await resources.locator('summary').filter({ hasText: '@toolkit' }).click()
  await resources.locator('summary').filter({ hasText: /^lib$/ }).click()
  await resources.getByRole('button', { name: 'util.star', exact: true }).click()
  const resourceDocument = page.getByTestId('resource-document')
  await expect(resourceDocument.getByText('load("@toolkit/lib/util.star", "<symbol>")', { exact: true })).toBeVisible()
  await expect(resourceDocument.locator('.cm-content')).toContainText('def greet(name)')
  await expect(resourceDocument.locator('.cm-content')).toHaveAttribute('contenteditable', 'false')
  await expect(resources.getByText(/load\("/)).toHaveCount(0)
  await expect(resources.getByText('draft_only.star', { exact: true })).toHaveCount(0)
  const left = await resources.boundingBox()
  const code = await page.getByTestId('app-editor-frame').boundingBox()
  expect(left!.x + left!.width).toBeLessThanOrEqual(code!.x)
  expect(Math.abs(left!.y - code!.y)).toBeLessThanOrEqual(1)
  expect(Math.abs(left!.y + left!.height - code!.y - code!.height)).toBeLessThanOrEqual(1)
  await page.getByRole('button', { name: 'main.star', exact: true }).click()

  await page.locator('#app-input').fill('{"name":"keep draft input"}')
  const cm = page.locator('.app-cm .cm-content')
  await cm.click()
  await page.keyboard.press('ControlOrMeta+End')
  await page.keyboard.insertText('\n# keep this edit')
  await page.evaluate(() => { (window as unknown as { workbenchEditor: Element | null }).workbenchEditor = document.querySelector('.app-cm .cm-content') })
  await resources.getByRole('button', { name: 'util.star', exact: true }).click()
  await page.getByRole('button', { name: 'Run', exact: true }).click()
  await expect(page.locator('.app-out-pre:not(.app-call-out)')).toContainText('Hello, keep draft input')
  await expect(resourceDocument.locator('.cm-content')).toContainText('def greet(name)')
  await tabs.getByRole('tab', { name: 'Invoke', exact: true }).click()
  await expect(page).toHaveURL(/view=invoke/)
  await expect(page.getByRole('button', { name: 'Invoke', exact: true })).toBeDisabled()
  await page.locator('#app-call-input').fill('{"name":"keep call input"}')
  await expect(page.getByRole('button', { name: 'Run', exact: true })).toHaveCount(0)
  await tabs.getByRole('tab', { name: 'Activity', exact: true }).click()
  await expect(page.getByText('Runs', { exact: true })).toBeVisible()
  await page.goBack()
  await expect(page.locator('#app-call-input')).toHaveValue('{"name":"keep call input"}')
  await tabs.getByRole('tab', { name: 'Develop', exact: true }).click()
  await expect(resourceDocument.locator('.cm-content')).toContainText('def greet(name)')
  expect(await page.evaluate(() => (window as unknown as { workbenchEditor: Element }).workbenchEditor === document.querySelector('.app-cm .cm-content'))).toBe(true)
  await expect(page.locator('#app-input')).toHaveValue('{"name":"keep draft input"}')
  await expect(resourceDocument.getByText('load("@toolkit/lib/util.star", "<symbol>")', { exact: true })).toBeVisible()
  await page.screenshot({ path: '/tmp/calcside-workbench-desktop.png', fullPage: true })
  await page.setViewportSize({ width: 390, height: 844 })
  await resources.scrollIntoViewIfNeeded()
  expect(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth)).toBe(false)
  await page.screenshot({ path: '/tmp/calcside-workbench-mobile-resources.png', fullPage: true })
  await page.setViewportSize({ width: 1440, height: 1000 })

  await resources.getByRole('button', { name: 'Configure resources' }).click()
  await page.getByRole('button', { name: 'Advanced JSON', exact: true }).click()
  await page.getByLabel('Application spec JSON').fill('{"unfinished":')
  await expect(resourceDocument.getByText(/resource changed or was removed/)).toBeVisible()
  await expect(resourceDocument.locator('.cm-content')).toHaveCount(0)
  await tabs.getByRole('tab', { name: 'Invoke', exact: true }).click()
  await tabs.getByRole('tab', { name: 'Develop', exact: true }).click()
  await expect(page.getByLabel('Application spec JSON')).toHaveValue('{"unfinished":')
  await page.getByRole('button', { name: 'main.star', exact: true }).click()
  await expect(cm).toContainText('keep this edit')
  await page.setViewportSize({ width: 390, height: 844 })
  await resources.scrollIntoViewIfNeeded()
  await resources.locator(':scope > summary').click()
  await expect(resources.locator('.app-resource-body')).toBeHidden()
  await resources.locator(':scope > summary').press('Enter')
  await expect(resources.locator('.app-resource-body')).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth)).toBe(false)
  await page.screenshot({ path: '/tmp/calcside-workbench-mobile.png', fullPage: true })
  expect(errors).toEqual([])
})

test('resource browser rejects mismatched sums without showing draft files (F3)', async ({ page }) => {
  const list = await rt.client.get('/api/v1/modules')
  const mod = (await list.json()).modules.find((m: { name: string }) => m.name === 'worklib')
  const ar = await rt.client.post('/api/v1/applications', {
    data: { name: 'Wrong pin', spec: { modules: { wrong: { name: mod.name, version: 1, sum: mod.latest_sum } } } },
  })
  expect(ar.ok()).toBeTruthy()
  const id = (await ar.json()).application.id
  // The API rejects invalid pins on writes; simulate stale/mismatched
  // documentation at the read boundary to exercise the browser's guard.
  await page.route(`**/api/v1/modules/${mod.id}/versions/1?*`, async (route) => {
    const response = await route.fetch()
    const body = await response.json()
    body.version.sum = 'h1:' + 'A'.repeat(43) + '='
    await route.fulfill({ response, json: body })
  })
  await page.goto(`${rt.viteURL}/applications/${id}?view=invoke`)
  const tabs = page.getByRole('tablist', { name: 'Application views' })
  await expect(tabs.getByRole('tab', { name: 'Invoke', exact: true })).toHaveAttribute('aria-selected', 'true')
  await tabs.getByRole('tab', { name: 'Develop', exact: true }).click()
  const resources = page.getByTestId('application-resources')
  await resources.locator('summary').filter({ hasText: '@wrong' }).click()
  await expect(resources.getByText('Module pin does not match this version.')).toBeVisible()
  await expect(resources.getByText('util.star', { exact: true })).toHaveCount(0)
  await page.goto(`${rt.viteURL}/applications/${id}?view=unknown&keep=1`)
  await expect(tabs.getByRole('tab', { name: 'Develop', exact: true })).toHaveAttribute('aria-selected', 'true')
  await tabs.getByRole('tab', { name: 'Invoke', exact: true }).click()
  await expect(page).toHaveURL(/keep=1/)
})

test('published invoke retains output across views and shows failures in place (C2/P5/F3)', async ({ page }) => {
  const cr = await rt.client.post('/api/v1/applications', { data: { name: 'Invoke workbench' } })
  const app = (await cr.json()).application
  const preview = await rt.client.post(`/api/v1/applications/${app.id}/publish/preview`, { data: { version: app.version } })
  const review = await preview.json()
  const pub = await rt.client.post(`/api/v1/applications/${app.id}/publish`, {
    data: { version: app.version, expected_base: '', candidate: review.candidate.digest },
  })
  expect(pub.ok()).toBeTruthy()
  await expect.poll(async () => (await (await rt.client.get(`/api/v1/applications/${app.id}`)).json()).application.status).toBe('ready')
  await page.goto(`${rt.viteURL}/applications/${app.id}?view=invoke`)
  const tabs = page.getByRole('tablist', { name: 'Application views' })
  await page.getByRole('button', { name: 'Invoke', exact: true }).click()
  await expect(page.locator('.app-call-out')).toContainText('Hello, world')
  await tabs.getByRole('tab', { name: 'Develop', exact: true }).click()
  await tabs.getByRole('tab', { name: 'Invoke', exact: true }).click()
  await expect(page.locator('.app-call-out')).toContainText('Hello, world')
  await page.route(`**/api/v1/applications/${app.id}/invoke`, (route) => route.fulfill({
    status: 400, contentType: 'application/json',
    body: '{"error":{"code":"bad_request","message":"Invalid invocation test input"}}',
  }))
  await page.getByRole('button', { name: 'Invoke', exact: true }).click()
  await expect(page.getByText('Invalid invocation test input', { exact: true })).toBeVisible()
})
