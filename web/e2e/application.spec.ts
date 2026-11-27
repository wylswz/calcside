import { test, expect, type APIRequestContext, type Page } from '@playwright/test'
import { startApplicationRuntime, type ApplicationRuntime } from './applicationRuntime'

// Application lifecycle under real Vite (StrictMode) + scratch
// API/SQLite: create → scaffold → test → publish → ready → live invoke;
// a draft edit does not change the published response until republish.

let rt: ApplicationRuntime
let client: APIRequestContext
let viteURL = ''

test.beforeAll(async () => {
  test.setTimeout(300_000)
  rt = await startApplicationRuntime()
  client = rt.client
  viteURL = rt.viteURL
})

test.afterAll(async () => {
  await rt?.close()
})

test.describe.configure({ mode: 'serial' })

// helper: wait until the app's status flips ready (real scheduler —
// never assert ready from the 202 alone)
async function waitReady(id: string, timeoutMs = 90_000) {
  await expect.poll(async () => {
    const r = await client.get(`/api/v1/applications/${id}`)
    return (await r.json()).application.status
  }, { timeout: timeoutMs }).toBe('ready')
}

// Basics is a separate step now; tests that need a named draft start here.
async function startApplication(page: Page, name?: string) {
  await page.goto(`${viteURL}/applications/new`)
  if (name) await page.locator('#basics-name').fill(name)
  await page.getByRole('button', { name: 'Continue to configuration' }).click()
}


// Publishing now routes through the review dialog: header Publish…
// saves the draft then opens it; the dialog's own Publish commits.
// The notebook workbench runs the draft through its Test panel: the
// the toolbar Test button opens the panel, then Run executes.
async function openTest(page: Page) {
  const panel = page.getByRole('region', { name: 'Test notebook' })
  if (!(await panel.isVisible())) {
    await page.getByRole('button', { name: 'Test', exact: true }).click()
  }
}

async function runMain(page: Page) {
  await openTest(page)
  await page.getByTestId('app-run').click()
}

async function publishViaDialog(page: Page) {
  await page.getByRole('button', { name: 'Publish…' }).click()
  await page.getByTestId('review-publish').click()
}

test('a) create -> scaffold -> test -> publish -> ready -> live invoke', async ({ page }) => {
  await startApplication(page, 'Greeter')
  await page.getByRole('button', { name: 'Create application' }).click()
  await page.waitForURL(/\/applications\/app_/)
  const id = page.url().split('/').pop()!
  await expect(page.locator('[aria-label="Application section main"]')).toContainText('def main(input)')

  await runMain(page)
  await expect(page.locator('[aria-label="Test notebook"] .app-out-pre')).toContainText('Hello, world', { timeout: 30_000 })

  await publishViaDialog(page)
  await waitReady(id)
  // the Invoke button is always rendered; it enables once ready
  await page.getByRole('tablist', { name: 'Application views' }).getByRole('tab', { name: 'Invoke' }).click()
  const invokeCard = page.getByRole('tabpanel', { name: 'Invoke', exact: true })
  await expect(invokeCard.getByRole('button', { name: 'Send' })).toBeEnabled({ timeout: 10_000 })
  await invokeCard.getByRole('button', { name: 'Send' }).click()
  await expect(invokeCard.locator('.app-invoke-output > .wb-pane-body > .app-out-pre')).toContainText('Hello, world', { timeout: 30_000 })
  await expect(invokeCard.locator('.app-invoke-output .wb-status-cell')).toHaveText(/^succeeded · \d+ms · 1 step$/)
})

test('b) draft change stays unpublished until republish', async ({ page }) => {
  await startApplication(page, 'Counter')
  await page.getByRole('button', { name: 'Create application' }).click()
  await page.waitForURL(/\/applications\/app_/)
  const id = page.url().split('/').pop()!

  // publish the scaffold, wait ready, note its output
  await publishViaDialog(page)
  await waitReady(id)
  const inv0 = await client.post(`/api/v1/applications/${id}/invoke`, {
    headers: { 'Content-Type': 'application/json' },
    data: `{"name":"a"}`,
  })
  expect(inv0.status()).toBe(200)
  const out0 = (await inv0.json()).output_json
  expect(String(out0)).toContain('Hello, a')

  // change the draft — published output must not move yet
  const code = await page.locator('[aria-label="Application section main"] .cm-content').first()
  await code.click()
  await page.keyboard.press('ControlOrMeta+a')
  await page.keyboard.type('def main(input): return {"message": "v2"}')
  await page.keyboard.press('ControlOrMeta+s')
  await expect.poll(async () => {
    const r0 = await client.get(`/api/v1/applications/${id}`)
    return (await r0.json()).application.sections.find((s: any) => s.id === 'main')?.code
  }, { timeout: 15_000 }).toContain('"v2"')
  const inv1 = await client.post(`/api/v1/applications/${id}/invoke`, {
    headers: { 'Content-Type': 'application/json' },
    data: `{"name":"a"}`,
  })
  expect((await inv1.json()).output_json).toContain('Hello, a')

  // republish — the live output flips
  await publishViaDialog(page)
  await waitReady(id)
  await expect.poll(async () => {
    const r = await client.post(`/api/v1/applications/${id}/invoke`, {
      headers: { 'Content-Type': 'application/json' },
      data: `{"name":"a"}`,
    })
    return (await r.json()).output_json ?? ''
  }, { timeout: 90_000 }).toContain('"v2"')
})

test('c) mid-save edits abort the chained publish', async ({ page }) => {
  await startApplication(page, 'Delay')
  await page.getByRole('button', { name: 'Create application' }).click()
  await page.waitForURL(/\/applications\/app_/)
  const id = page.url().split('/').pop()!

  // delay the PUT so an edit lands mid-flight
  await page.route('**/api/v1/applications/*', async (route) => {
    if (route.request().method() === 'PUT') {
      await new Promise((r) => setTimeout(r, 400))
    }
    await route.continue()
  })
  const cm = page.locator('[aria-label="Application section main"] .cm-content').first()
  await cm.click()
  await page.keyboard.press('ControlOrMeta+a')
  await page.keyboard.type('def main(input): return {"first":1}')
  const publish = page.getByRole('button', { name: 'Publish…' }).click()
  await page.waitForTimeout(80)
  await cm.click()
  await page.keyboard.press('ControlOrMeta+a')
  await page.keyboard.type('def main(input): return {"second":2}')
  await publish
  // the in-flight edit aborted the chain — the dialog must NOT open.
  await expect(page.getByTestId('review-publish')).toHaveCount(0)
  // the in-flight edit must abort the publish chain — the PUT saved the
  // FIRST code (v2); nothing was published.
  await page.waitForTimeout(500)
  const r = await client.get(`/api/v1/applications/${id}`)
  const app = (await r.json()).application
  expect(app.published_version).toBe(0)
  // the latest code is preserved in the editor, and autosave persists it
  await expect(page.locator('[aria-label="Application section main"]')).toContainText('"second":2')
  await expect.poll(async () => {
    const rr = await client.get(`/api/v1/applications/${id}`)
    return (await rr.json()).application.sections.find((s: any) => s.id === 'main')?.code
  }, { timeout: 15_000 }).toContain('"second":2')
  // then publish + invoke actually serves the SECOND code
  await publishViaDialog(page)
  await waitReady(id)
  const iv = await client.post(`/api/v1/applications/${id}/invoke`, {
    headers: { 'Content-Type': 'application/json' }, data: '{}',
  })
  expect((await iv.json()).output_json).toContain('"second":2')
})

test('c2) input-only mid-save change aborts and keeps the input', async ({ page }) => {
  const r = await client.post('/api/v1/applications', { data: { name: 'InOnly' } })
  const id = (await r.json()).application.id
  await page.goto(`${viteURL}/applications/${id}`)
  await expect(page.locator('.detail-header-status .wb-status-cell')).toBeVisible({ timeout: 10_000 })
  await page.route('**/api/v1/applications/*', async (route) => {
    if (route.request().method() === 'PUT') {
      await new Promise((rr) => setTimeout(rr, 400))
    }
    await route.continue()
  })
  const cm = page.locator('[aria-label="Application section main"] .cm-content').first()
  await cm.click()
  await page.keyboard.press('ControlOrMeta+a')
  await page.keyboard.type('def main(input): return {"c":1}')
  const saveP = runMain(page)
  await page.waitForTimeout(80)
  await page.locator('#app-input').fill('{"changed":true}')
  await saveP
  await page.waitForTimeout(1500)
  const app = (await (await client.get(`/api/v1/applications/${id}`)).json()).application
  expect(app.published_version).toBe(0)
  await expect(page.locator('#app-input')).toHaveValue('{"changed":true}')
  // a follow-up Run works on the saved baseline
  await runMain(page)
  await expect(page.locator('[aria-label="Test notebook"] .app-out-pre')).toContainText('"c":1', { timeout: 30_000 })
})

test('d) SPA navigation resets scope; B never receives A draft', async ({ page }) => {
  const a1 = await client.post('/api/v1/applications', { data: { name: 'One' } })
  const id1 = (await a1.json()).application.id
  const a2 = await client.post('/api/v1/applications', { data: { name: 'Two' } })
  const id2 = (await a2.json()).application.id

  const spa = (pid: string) =>
    page.evaluate((p) => {
      history.pushState(null, '', `/applications/${p}`)
      dispatchEvent(new PopStateEvent('popstate'))
    }, pid)

  await page.goto(`${viteURL}/applications/${id1}`)
  const cm = page.locator('[aria-label="Application section main"] .cm-content').first()
  await cm.click()
  await page.keyboard.press('ControlOrMeta+a')
  await page.keyboard.type('def main(input): return {"from_a":1}')
  await openTest(page)
  await page.locator('#app-input').fill('{"name":"scoped-one"}')
  // same-component navigation — the keyed remount must reset the draft
  await spa(id2)
  await openTest(page)
  await expect(page.locator('#app-input')).toHaveValue(/\{\s*"name": "world"/, { timeout: 10_000 })
  await expect(page.locator('[aria-label="Application section main"]')).toContainText('def main(input)')
  await expect(page.locator('[aria-label="Application section main"]')).not.toContainText('from_a')
  // saving B writes B's own code, never A's
  await page.keyboard.press('ControlOrMeta+s')
  const b = (await (await client.get(`/api/v1/applications/${id2}`)).json()).application
  expect(b.sections.find((s: any) => s.id === 'main')?.code).not.toContain('from_a')
  // back to A — its edits were scoped and restored
  await spa(id1)
  await expect(page.locator('[aria-label="Application section main"]')).toContainText('from_a', { timeout: 10_000 })
  await openTest(page)
  await expect(page.locator('#app-input')).toHaveValue('{"name":"scoped-one"}')
})

test('e) external write + my save -> 409, edits retained, reload offered', async ({ page }) => {
  const r = await client.post('/api/v1/applications', { data: { name: 'Race' } })
  const id = (await r.json()).application.id
  await page.goto(`${viteURL}/applications/${id}`)
  // wait until the page has actually loaded the app (version 1 base)
  await expect(page.locator('.detail-header-status .wb-status-cell')).toBeVisible({ timeout: 10_000 })
  // another session bumps the draft server-side
  const app0 = (await (await client.get(`/api/v1/applications/${id}`)).json()).application
  await client.put(`/api/v1/applications/${id}`, {
    data: { version: app0.version, name: 'Race', sections: [{ id: 'main', code: 'def main(i): return {"remote":true}' }], spec: app0.spec },
  })
  // my editor still holds version 1 — its save must conflict
  const cm = page.locator('[aria-label="Application section main"] .cm-content').first()
  await cm.click()
  await page.keyboard.press('ControlOrMeta+a')
  await page.keyboard.type('def main(input): return {"mine":1}')
  await page.keyboard.press('ControlOrMeta+s')
  await expect(page.getByText('Saved elsewhere')).toBeVisible({ timeout: 15_000 })
  // edits must not be silently replaced; Cancel keeps them
  await expect(page.locator('[aria-label="Application section main"]')).toContainText('"mine":1')
  await page.getByRole('button', { name: 'Cancel' }).click()
  await expect(page.locator('[aria-label="Application section main"]')).toContainText('"mine":1')
  // save again -> conflict again -> Confirm reload replaces explicitly
  await page.keyboard.press('ControlOrMeta+s')
  await expect(page.getByText('Saved elsewhere')).toBeVisible({ timeout: 15_000 })
  await page.getByRole('button', { name: 'Reload' }).click()
  await expect(page.locator('[aria-label="Application section main"]')).toContainText('remote', { timeout: 10_000 })
})

test('f) instance creation still works via the UI', async ({ page }) => {
  await page.goto(`${viteURL}/instances/new?step=config`)
  await page.getByRole('button', { name: 'Create instance' }).click()
  await page.waitForURL(/\/instances\/ins_/, { timeout: 20_000 })
})

test('g) mobile populated layout, no horizontal overflow', async ({ page }) => {
  const r = await client.post('/api/v1/applications', { data: { name: 'Mobile' } })
  const id = (await r.json()).application.id
  // populate output via the page itself, then resize — a viewport
  // change does not remount, so the populated state is preserved
  await page.goto(`${viteURL}/applications/${id}`)
  await runMain(page)
  await expect(page.locator('[aria-label="Test notebook"] .app-out-pre')).toContainText('Hello, world', { timeout: 30_000 })
  await page.setViewportSize({ width: 390, height: 844 })
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth)
  expect(overflow).toBe(false)
  await page.screenshot({ path: '/tmp/calcside-ep14-logs/application-ui-mobile.png', fullPage: true })
  await page.setViewportSize({ width: 1440, height: 1000 })
  await expect(page.locator('[aria-label="Test notebook"] .app-out-pre')).toContainText('Hello, world')
  await page.screenshot({ path: '/tmp/calcside-ep14-logs/application-ui-desktop.png', fullPage: true })
})

test('h) guide -> configuration carries basics, Back keeps them', async ({ page }) => {
  await page.goto(`${viteURL}/applications/new`)
  await expect(page.getByRole('heading', { name: 'Application', exact: true })).toBeVisible({ timeout: 10_000 })
  // one kind per create flow: the instance guide is not offered here
  await expect(page.getByRole('heading', { name: 'Instance', exact: true })).toHaveCount(0)
  await page.locator('#basics-name').fill('GuideGreeter')
  await page.locator('#basics-description').fill('Says hello')
  await page.getByRole('button', { name: 'Continue to configuration' }).click()
  // Basics are read-only in configuration: no duplicate name/description form.
  await expect(page.getByText('GuideGreeter — Says hello')).toBeVisible()
  await expect(page.locator('#app-name')).toHaveCount(0)
  await expect(page.locator('#app-description')).toHaveCount(0)
  // Back is a paint-only step switch; details stay mounted (C2).
  await page.getByRole('button', { name: /^1 Basics$/ }).click()
  await expect(page.locator('#basics-name')).toHaveValue('GuideGreeter')
  await expect(page.locator('#basics-description')).toHaveValue('Says hello')
  await page.getByRole('button', { name: 'Continue to configuration' }).click()
  await expect(page.getByText('GuideGreeter — Says hello')).toBeVisible()
  await page.getByRole('button', { name: 'Create application' }).click()
  await page.waitForURL(/\/applications\/app_/)
  const id = page.url().split('/').pop()!
  const created = (await (await client.get(`/api/v1/applications/${id}`)).json()).application
  expect(created.spec.labels.description).toBe('Says hello')
  await page.goto(`${viteURL}/applications`)
  await expect(page.locator('tr').filter({ hasText: 'GuideGreeter' })).toContainText('Says hello')
})

test('h2) the application guide is static with or without reduced motion (P4)', async ({ page }) => {
  await page.goto(`${viteURL}/applications/new`)
  const guide = page.getByRole('complementary', { name: 'About applications' })
  for (const reducedMotion of ['no-preference', 'reduce'] as const) {
    await page.emulateMedia({ reducedMotion })
    await expect(guide).toBeVisible()
    await expect(guide).toContainText('Publish a version when the code is ready to serve.')
    expect(await guide.evaluate((el) => el.getAnimations({ subtree: true }).length)).toBe(0)
  }
})

test('i) applications list Delete removes the row after confirm', async ({ page }) => {
  const r = await client.post('/api/v1/applications', { data: { name: 'ToDelete' } })
  const id = (await r.json()).application.id
  await page.goto(`${viteURL}/applications`)
  const row = page.locator('tr').filter({ hasText: 'ToDelete' })
  await expect(row).toBeVisible({ timeout: 10_000 })
  await row.getByRole('button', { name: 'Delete' }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog).toContainText('Delete ToDelete?')
  await dialog.getByRole('button', { name: 'Delete' }).click()
  await expect(row).toHaveCount(0)
  const gone = await client.get(`/api/v1/applications/${id}`)
  expect(gone.status()).toBe(404)
})

test('j) instances list End ends a running instance', async ({ page }) => {
  const r = await client.post('/api/v1/instances', { data: { ttl_seconds: 600 } })
  const inst = (await r.json()).instance
  await page.goto(`${viteURL}/instances`)
  const row = page.locator('tr').filter({ hasText: inst.id })
  await expect(row).toBeVisible({ timeout: 10_000 })
  await row.getByRole('button', { name: 'End' }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByRole('button', { name: 'End' }).click()
  await expect(row).toContainText('ended', { timeout: 15_000 })
})

test('k) failed save shows "Couldn\'t save" + Retry and keeps the draft', async ({ page }) => {
  const r = await client.post('/api/v1/applications', { data: { name: 'Flaky' } })
  const id = (await r.json()).application.id
  await page.goto(`${viteURL}/applications/${id}`)
  await expect(page.locator('[aria-label="Application section main"]')).toContainText('def main(input)', { timeout: 15_000 })
  // fail every PUT — the autosave after this edit surfaces the failure
  await page.route('**/api/v1/applications/*', async (route) => {
    if (route.request().method() === 'PUT') {
      return route.fulfill({ status: 500, contentType: 'application/json', body: '{"error":{"code":"internal","message":"boom"}}' })
    }
    return route.continue()
  })
  const cm = page.locator('[aria-label="Application section main"] .cm-content').first()
  await cm.click()
  await page.keyboard.press('ControlOrMeta+a')
  await page.keyboard.type('def main(input): return {"kept":1}')
  const saved = () => page.locator('.detail-head .swap-label > span[data-on="true"]', { hasText: 'Saved' })
  await expect(page.locator('.detail-head .swap-label > span[data-on="true"]', { hasText: "Couldn't save" })).toBeVisible({ timeout: 15_000 })
  await expect(page.getByRole('button', { name: 'Retry' })).toBeVisible()
  await expect(page.locator('[aria-label="Application section main"]')).toContainText('"kept":1')
  await page.unroute('**/api/v1/applications/*')
  await page.getByRole('button', { name: 'Retry' }).click()
  await expect(saved()).toBeVisible({ timeout: 10_000 })
  await expect.poll(async () => {
    const rr = await client.get(`/api/v1/applications/${id}`)
    return (await rr.json()).application.sections.find((s: any) => s.id === 'main')?.code
  }, { timeout: 10_000 }).toContain('"kept":1')
})

// Work views: Invoke lays out input/output beside request examples on a
// wide pane and stacks them on a narrow one; Activity links each run to
// the shared run detail, which stays usable at 390px.
test('l) work views: Invoke layout, Activity -> Run detail, narrow safe', async ({ page }, testInfo) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  const r = await client.post('/api/v1/applications', { data: { name: 'WorkViews' } })
  const id = (await r.json()).application.id
  // publish the scaffold via the preview -> publish API pair
  const app = (await (await client.get(`/api/v1/applications/${id}`)).json()).application
  const rev = await (await client.post(`/api/v1/applications/${id}/publish/preview`, { data: { version: app.version } })).json()
  const pub = await client.post(`/api/v1/applications/${id}/publish`, {
    data: { version: app.version, expected_base: rev.base?.digest ?? '', candidate: rev.candidate.digest },
  })
  expect(pub.status()).toBe(202)
  await waitReady(id)
  const inv = await client.post(`/api/v1/applications/${id}/invoke`, { data: {} })
  expect(inv.status()).toBe(200)

  // Invoke: Postman split — request pane (Body|Code|Auth tabs) left of
  // the response pane; the panes are side by side.
  await page.goto(`${viteURL}/applications/${id}?view=invoke`)
  const invokePanel = page.getByRole('tabpanel', { name: 'Invoke', exact: true })
  await expect(invokePanel.getByRole('button', { name: 'Send' })).toBeEnabled({ timeout: 15_000 })
  const io = await invokePanel.locator('.app-invoke-io').boundingBox()
  const req = await invokePanel.locator('.app-invoke-request').boundingBox()
  const output = await invokePanel.locator('.app-invoke-output').boundingBox()
  expect(io).not.toBeNull(); expect(req).not.toBeNull(); expect(output).not.toBeNull()
  expect(req!.x + req!.width).toBeLessThanOrEqual(output!.x + 2)
  expect(Math.abs(req!.y - output!.y)).toBeLessThanOrEqual(2)
  // Body tab carries the input; Code tab hosts the request examples.
  const bodyTab = invokePanel.getByRole('tab', { name: 'Body' })
  await expect(bodyTab).toHaveAttribute('aria-selected', 'true')
  await expect(invokePanel.locator('#app-call-input')).toBeVisible()
  await invokePanel.getByRole('tab', { name: 'Code' }).click()
  const examples = invokePanel.locator('.app-examples-embedded')
  await expect(examples).toBeVisible()
  await expect(examples.getByRole('tablist', { name: 'Example language' })).toBeVisible()
  await invokePanel.getByRole('tab', { name: 'Body' }).click()
  await page.screenshot({ path: testInfo.outputPath('invoke-desktop.png'), fullPage: true })

  // stacked at 390px, no horizontal document overflow
  await page.setViewportSize({ width: 390, height: 844 })
  const reqN = await invokePanel.locator('.app-invoke-request').boundingBox()
  const outputN = await invokePanel.locator('.app-invoke-output').boundingBox()
  const method = (await invokePanel.locator('.wb-method').boundingBox())!
  const send = (await invokePanel.getByRole('button', { name: 'Send' }).boundingBox())!
  expect(method.y).toBeGreaterThanOrEqual(send.y)
  expect(method.y + method.height).toBeLessThanOrEqual(send.y + send.height)
  expect(reqN!.y + reqN!.height).toBeLessThanOrEqual(outputN!.y + 2)
  expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth)).toBe(false)

  // Activity: the run row links to the shared run detail.
  await page.getByRole('tablist', { name: 'Application views' }).getByRole('tab', { name: 'Activity' }).click()
  const runsRegion = page.getByRole('region', { name: 'Runs table' })
  await expect(runsRegion).toBeVisible()
  await runsRegion.getByRole('link', { name: 'succeeded' }).first().click()
  await page.waitForURL(/\/applications\/app_[^/]+\/runs\/run_/)
  await expect(page.getByRole('heading', { name: 'Run', exact: true })).toBeVisible()
  const runLocation = page.getByRole('navigation', { name: 'Run location' })
  const activityCrumb = runLocation.getByRole('link', { name: 'Activity', exact: true })
  await expect(activityCrumb).toBeInViewport()
  const steps = page.getByRole('region', { name: 'Steps table' })
  await expect(steps).toBeVisible()
  // the steps table scrolls inside its own region, never the document
  expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth)).toBe(false)
  const stepGeom = await steps.evaluate((el) => ({
    region: el.getBoundingClientRect().width,
    table: el.querySelector('table')!.getBoundingClientRect().width,
    scrollW: el.scrollWidth, clientW: el.clientWidth,
  }))
  expect(stepGeom.region).toBeGreaterThan(0)
  // at 390px the steps restack into labelled cells — no horizontal
  // scroll needed inside the region, and the values stay in view
  expect(stepGeom.scrollW).toBeLessThanOrEqual(stepGeom.clientW + 1)
  await expect(steps.locator('td[data-label="Duration"]').first()).toBeInViewport()
  await expect(steps.locator('td[data-label="Resolver"]').first()).toBeInViewport()
  await page.screenshot({ path: testInfo.outputPath('run-detail-mobile.png'), fullPage: true })
  // the crumb returns to the application's Activity view
  await activityCrumb.click()
  await page.waitForURL(/\/applications\/app_[^/]+\?view=activity/)
  await expect(runsRegion).toBeVisible()
  await runsRegion.getByRole('link', { name: 'succeeded' }).first().click()
  await runLocation.getByRole('link', { name: app.name, exact: true }).click()
  await expect(page.getByRole('tab', { name: 'Editor', exact: true })).toHaveAttribute('aria-selected', 'true')
  await expect(page.locator('[aria-label="Application section main"] .cm-content')).toBeVisible()
})
