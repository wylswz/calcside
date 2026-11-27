import { test, expect } from '@playwright/test'
import { startApplicationRuntime, type ApplicationRuntime } from './applicationRuntime'

let rt: ApplicationRuntime
test.beforeAll(async () => {
  test.setTimeout(300_000)
  rt = await startApplicationRuntime()
})
test.afterAll(async () => { await rt?.close() })

test('notebook workbench retains section editor, test input and call input across views (C2/C3/C4)', async ({ page }) => {
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(e.message))
  const ar = await rt.client.post('/api/v1/applications', { data: { name: 'Notebook workbench' } })
  expect(ar.ok(), await ar.text()).toBeTruthy()
  const id = (await ar.json()).application.id
  await page.goto(`${rt.viteURL}/applications/${id}`)
  const tabs = page.getByRole('tablist', { name: 'Application views' })
  const editor = tabs.getByRole('tab', { name: 'Editor', exact: true })

  const main = page.locator('[aria-label="Application section main"] .cm-content')
  await expect(main).toContainText('def main(input)')
  await page.evaluate(() => { (window as unknown as { workbenchEditor: Element | null }).workbenchEditor = document.querySelector('[aria-label="Application section main"] .cm-content') })
  await main.click()
  await page.keyboard.press('ControlOrMeta+End')
  await page.keyboard.insertText('\n# keep this edit')

  await page.getByRole('button', { name: 'Test', exact: true }).click()
  await page.locator('#app-input').fill('{"name":"keep draft input"}')
  await page.getByTestId('app-run').click()
  await expect(page.locator('[aria-label="Test notebook"] .app-out-pre')).toContainText('keep draft input')

  await tabs.getByRole('tab', { name: 'Invoke', exact: true }).click()
  await expect(page).toHaveURL(/view=invoke/)
  await expect(page.getByRole('button', { name: 'Send', exact: true })).toBeDisabled()
  await page.locator('#app-call-input').fill('{"name":"keep call input"}')
  await expect(page.getByRole('button', { name: 'Run', exact: true })).toHaveCount(0)
  const requestTabs = page.getByRole('tablist', { name: 'Request' })
  const auth = requestTabs.getByRole('tab', { name: 'Auth', exact: true })
  await auth.click()
  await expect(editor).toBeInViewport()
  await editor.click()
  await expect(main).toBeVisible()
  await expect(page.locator('#app-input')).toHaveValue('{"name":"keep draft input"}')

  await page.getByRole('button', { name: 'Settings', exact: true }).click()
  await expect(page.getByRole('region', { name: 'Notebook settings' })).toBeVisible()
  await editor.click()
  await expect(main).toBeVisible()
  await page.getByRole('button', { name: 'Graph', exact: true }).click()
  await expect(page).toHaveURL(/nbview=graph/)

  await tabs.getByRole('tab', { name: 'Invoke', exact: true }).click()
  await expect(auth).toHaveAttribute('aria-selected', 'true')
  await requestTabs.getByRole('tab', { name: 'Body', exact: true }).click()
  await tabs.getByRole('tab', { name: 'Activity', exact: true }).click()
  await expect(page.getByRole('region', { name: 'Runs table' })).toBeVisible()
  await page.goBack()
  await expect(page.locator('#app-call-input')).toHaveValue('{"name":"keep call input"}')
  await editor.click()
  await expect(main).toBeVisible()
  await expect(page).not.toHaveURL(/nbview=graph/)
  expect(await page.evaluate(() => (window as unknown as { workbenchEditor: Element }).workbenchEditor === document.querySelector('[aria-label="Application section main"] .cm-content'))).toBe(true)
  await expect(main).toContainText('keep this edit')
  await expect(page.locator('#app-input')).toHaveValue('{"name":"keep draft input"}')
  await page.screenshot({ path: '/tmp/calcside-workbench-desktop.png', fullPage: true })
  await page.setViewportSize({ width: 390, height: 844 })
  await expect(main).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth)).toBe(false)
  await page.screenshot({ path: '/tmp/calcside-workbench-mobile.png', fullPage: true })
  await page.setViewportSize({ width: 1440, height: 1000 })
  expect(errors).toEqual([])
})

test('view routing: unknown view falls back to the notebook tab and params survive (C4)', async ({ page }) => {
  const ar = await rt.client.post('/api/v1/applications', { data: { name: 'Routes' } })
  expect(ar.ok()).toBeTruthy()
  const id = (await ar.json()).application.id
  const tabs = page.getByRole('tablist', { name: 'Application views' })
  await page.goto(`${rt.viteURL}/applications/${id}?view=invoke`)
  await expect(tabs.getByRole('tab', { name: 'Invoke', exact: true })).toHaveAttribute('aria-selected', 'true')
  await page.goto(`${rt.viteURL}/applications/${id}?view=unknown&keep=1`)
  await expect(tabs.getByRole('tab', { name: 'Editor', exact: true })).toHaveAttribute('aria-selected', 'true')
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
  await expect.poll(async () => (await (await rt.client.get(`/api/v1/applications/${app.id}`)).json()).application.status,
    { timeout: 30_000 }).toBe('ready')
  await page.goto(`${rt.viteURL}/applications/${app.id}?view=invoke`)
  const tabs = page.getByRole('tablist', { name: 'Application views' })
  await page.getByRole('button', { name: 'Send', exact: true }).click()
  await expect(page.getByRole('tabpanel', { name: 'Invoke', exact: true }).locator('.app-invoke-output > .wb-pane-body > .app-out-pre')).toContainText('Hello, world')
  await tabs.getByRole('tab', { name: 'Editor', exact: true }).click()
  await tabs.getByRole('tab', { name: 'Invoke', exact: true }).click()
  await expect(page.getByRole('tabpanel', { name: 'Invoke', exact: true }).locator('.app-invoke-output > .wb-pane-body > .app-out-pre')).toContainText('Hello, world')
  await page.route(`**/api/v1/applications/${app.id}/invoke`, (route) => route.fulfill({
    status: 400, contentType: 'application/json',
    body: '{"error":{"code":"bad_request","message":"Invalid invocation test input"}}',
  }))
  await page.getByRole('button', { name: 'Send', exact: true }).click()
  await expect(page.getByText('Invalid invocation test input', { exact: true })).toBeVisible()
})
