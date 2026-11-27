import { test, expect } from '@playwright/test'
import { mkdir } from 'node:fs/promises'
import { join } from 'node:path'
import { startApplicationRuntime, type ApplicationRuntime } from './applicationRuntime'

// Optional visual sweep: run with VISUAL_AUDIT=1 to capture every
// console route. Screenshots land in /tmp/calcside-ui-audit/before so a
// later "after" sweep can diff against stable paths; CI skips this test.
const OUT = process.env.VISUAL_AUDIT_OUT ?? '/tmp/calcside-ui-audit/before'
const DESKTOP = { width: 1440, height: 1000 }
const MOBILE = { width: 390, height: 844 }

const WAITS = `def on_ok(h):
    return {"ok": h["event"]["approved"]}
def main(input):
    i = interrupt.new()
    interrupt.wait(i["id"], handler="on_ok", context={"x": 1})
    return {"unreachable": True}
`

let rt: ApplicationRuntime

const shot = async (page: import('@playwright/test').Page, name: string, fullPage = false) => {
  await page.screenshot({ path: join(OUT, `${name}.png`), fullPage })
}

test.describe('visual audit', () => {
  test.skip(process.env.VISUAL_AUDIT !== '1', 'set VISUAL_AUDIT=1 to capture the visual sweep')
  test.describe.configure({ mode: 'serial' })

  test.beforeAll(async () => {
    test.setTimeout(300_000)
    await mkdir(OUT, { recursive: true })
    rt = await startApplicationRuntime({ secretKey: 'MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=' })
  })

  test.afterAll(async () => {
    await rt?.close()
  })

  // List/settings surfaces: desktop light, full page. The inbox is
  // captured empty here before the interrupt test seeds pending rows.
  test('list pages', async ({ page }) => {
    test.setTimeout(300_000)
    await rt.client.post('/api/v1/instances', {
      data: { ttl_seconds: 600, labels: { name: 'Research session' }, capabilities: { fs: {}, net: { allow_hosts: ['example.com'], methods: ['GET'] } } },
    })
    await rt.client.post('/api/v1/applications', { data: { name: 'Mobile' } })
    await rt.client.post('/api/v1/modules', {
      data: { name: 'auditmod', files: { 'main.star': 'def shout(word):\n    return word.upper() + "!"\n' } },
    })
    await page.setViewportSize(DESKTOP)
    await page.emulateMedia({ colorScheme: 'light' })

    // Inbox empty state first — later tests add pending interrupts.
    await page.goto(rt.viteURL + '/inbox')
    await page.waitForTimeout(900)
    await shot(page, 'inbox-empty-light', true)

    const pages: [string, string][] = [
      ['instances', '/'],
      ['applications', '/applications'],
      ['modules', '/modules'],
      ['audit', '/audit'],
      ['profile', '/profile'],
      ['workspace-members', '/workspace/members'],
      ['workspace-policies', '/workspace/policies'],
      ['workspace-secrets', '/workspace/secrets'],
      ['workspace-providers', '/workspace/providers'],
      ['workspace-keys', '/workspace/keys'],
      ['new-instance-basics', '/instances/new'],
      ['new-instance-config', '/instances/new?step=config'],
      ['new-application-basics', '/applications/new'],
      ['new-application-config', '/applications/new?step=config'],
    ]
    for (const [name, path] of pages) {
      await page.goto(rt.viteURL + path)
      await page.waitForTimeout(900)
      await shot(page, `${name}-light`, true)
    }
    // Login page is unreachable: dev mode redirects /login -> /.
  })

  // Interrupt application: pending inbox rows, deep-linked selection,
  // waiting run detail, and the published Invoke view with the inline
  // resume form (the "resume area bottom-right").
  test('interrupt surfaces', async ({ page }) => {
    test.setTimeout(300_000)
    const client = rt.client
    const cr = await client.post('/api/v1/applications', {
      data: { name: 'Approval gate', sections: [{ id: 'main', code: WAITS }] },
    })
    expect(cr.ok(), `create waits app: ${await cr.text()}`).toBeTruthy()
    const app = (await cr.json()).application
    const appId = app.id as string

    const testRun = async () => {
      const r = await client.post(`/api/v1/applications/${appId}/test`, { data: { version: 1, input: {} } })
      const b = await r.json()
      return { run_id: b.run_id as string, int_id: b.waiting.interrupt_id as string }
    }
    const runs = [await testRun(), await testRun(), await testRun()]
    const waitingRun = runs[0]

    // Inbox with several pending rows + a selected interrupt.
    await page.setViewportSize(DESKTOP)
    await page.emulateMedia({ colorScheme: 'light' })
    await page.goto(rt.viteURL + '/inbox')
    await expect(page.locator('.inbox-item')).toHaveCount(3, { timeout: 15_000 })
    await shot(page, 'inbox-pending-light', true)
    await page.goto(`${rt.viteURL}/inbox?interrupt=${waitingRun.int_id}`)
    await expect(page.locator(`#event-${waitingRun.int_id}`)).toBeVisible({ timeout: 15_000 })
    await shot(page, 'inbox-selected-light', true)
    await page.emulateMedia({ colorScheme: 'dark' })
    await page.waitForTimeout(500)
    await shot(page, 'inbox-selected-dark', true)
    await page.emulateMedia({ colorScheme: 'light' })
    // mobile inbox with selection
    await page.setViewportSize(MOBILE)
    await page.reload()
    await page.waitForTimeout(700)
    await shot(page, 'inbox-selected-mobile-light', true)
    await page.setViewportSize(DESKTOP)

    // Waiting run detail.
    await page.goto(`${rt.viteURL}/applications/${appId}/runs/${waitingRun.run_id}`)
    await expect(page.getByTestId('run-progress')).toBeVisible({ timeout: 15_000 })
    await page.waitForTimeout(500)
    await shot(page, 'app-int-run-waiting-light')
    await page.emulateMedia({ colorScheme: 'dark' })
    await page.waitForTimeout(400)
    await shot(page, 'app-int-run-waiting-dark')
    await page.emulateMedia({ colorScheme: 'light' })

    // Publish, then invoke through the UI so the output pane shows the
    // waiting state with the inline resume form.
    await publishApp(appId, app.version)
    await expect.poll(async () => {
      const r = await client.get(`/api/v1/applications/${appId}`)
      return (await r.json()).application.status
    }, { timeout: 90_000 }).toBe('ready')
    await page.goto(`${rt.viteURL}/applications/${appId}?view=invoke`)
    const invokePanel = page.getByRole('tabpanel', { name: 'Invoke', exact: true })
    await invokePanel.getByRole('button', { name: 'Send' }).click()
    await expect(invokePanel.locator('.app-waiting')).toContainText('waiting', { timeout: 30_000 })
    await page.waitForTimeout(600)
    await shot(page, 'app-int-invoke-waiting-light')
    await page.emulateMedia({ colorScheme: 'dark' })
    await page.waitForTimeout(400)
    await shot(page, 'app-int-invoke-waiting-dark')
    await page.emulateMedia({ colorScheme: 'light' })
    // No reload: the inline resume form lives in component state.
    await page.setViewportSize(MOBILE)
    await page.waitForTimeout(700)
    await shot(page, 'app-int-invoke-waiting-mobile-light')
    await page.setViewportSize(DESKTOP)

    // App notebook + activity views for the interrupt app.
    await page.goto(`${rt.viteURL}/applications/${appId}`)
    await page.waitForTimeout(900)
    await shot(page, 'app-int-notebook-light')
    await page.goto(`${rt.viteURL}/applications/${appId}?view=activity`)
    await expect(page.getByRole('region', { name: 'Runs table' })).toBeVisible({ timeout: 15_000 })
    await page.waitForTimeout(500)
    await shot(page, 'app-int-activity-light')
  })

  // Detail/work surfaces: instance notebook + activity + graph, a
  // published notebook application across all views (Test panel, run,
  // Settings, graph, invoke idle/success, activity, run detail) and a
  // module detail — desktop light and dark.
  test('detail pages', async ({ page }) => {
    test.setTimeout(300_000)
    const client = rt.client

    // --- instance with notebook content -------------------------------
    const instRes = await client.post('/api/v1/instances', {
      data: { ttl_seconds: 600, labels: { name: 'Audit instance' }, capabilities: { fs: {}, net: { allow_hosts: ['example.com'], methods: ['GET'] } } },
    })
    expect(instRes.status(), `create instance: ${await instRes.text()}`).toBe(201)
    const instId = (await instRes.json()).instance.id as string
    const nbRevision = async () => (await (await client.get(`/api/v1/instances/${instId}/notebook`)).json()).notebook.revision as number
    for (const [name, code] of [
      ['setup', 'total = 21\nprint("setup done, total =", total)'],
      ['report', 'print("report: double is", total * 2)\n{"total": total}'],
    ] as [string, string][]) {
      const r = await client.put(`/api/v1/instances/${instId}/sections/${name}`, {
        data: { code, description: `section ${name}`, revision: await nbRevision() },
      })
      expect(r.status(), `section ${name}: ${await r.text()}`).toBe(200)
    }
    const runAll = await client.post(`/api/v1/instances/${instId}/notebook/run`, { data: { sections: 'all' } })
    expect(runAll.status(), `notebook run: ${await runAll.text()}`).toBe(200)

    // --- published notebook application with one run ------------------
    const nbCreate = await client.post('/api/v1/applications', { data: { name: 'NbAudit' } })
    expect(nbCreate.status(), `create nb app: ${await nbCreate.text()}`).toBe(200)
    let app = (await nbCreate.json()).application
    const nbPut = await client.put(`/api/v1/applications/${app.id}`, {
      data: {
        version: app.version, name: app.name, spec: app.spec,
        sections: [
          { id: 'greet', code: 'def greet(name):\n    return "Hello, " + name + "!"\n' },
          { id: 'main', code: 'def main(input):\n    return {"message": greet(input["name"])}\n' },
        ],
      },
    })
    expect(nbPut.status(), `nb put: ${await nbPut.text()}`).toBe(200)
    app = (await nbPut.json()).application
    const nbAppId = app.id as string
    await publishApp(nbAppId, app.version)
    await expect.poll(async () => {
      const r = await client.get(`/api/v1/applications/${nbAppId}`)
      return (await r.json()).application.status
    }, { timeout: 90_000 }).toBe('ready')
    const inv = await client.post(`/api/v1/applications/${nbAppId}/invoke`, { data: { name: 'Audit' } })
    expect(inv.status(), `invoke: ${await inv.text()}`).toBe(200)
    const runId = (await inv.json()).run_id as string

    // --- module ---------------------------------------------------------
    const mods = await (await client.get('/api/v1/modules')).json()
    const modId = mods.modules?.[0]?.id as string | undefined

    // --- static capture sweep (desktop light + dark) --------------------
    const routes: [string, string][] = [
      ['instance-notebook', `/instances/${instId}`],
      ['instance-graph', `/instances/${instId}?nbview=graph`],
      ['instance-activity', `/instances/${instId}?view=activity`],
      ['app-nb-notebook', `/applications/${nbAppId}`],
      ['app-nb-graph', `/applications/${nbAppId}?nbview=graph`],
      ['app-nb-invoke-idle', `/applications/${nbAppId}?view=invoke`],
      ['app-nb-activity', `/applications/${nbAppId}?view=activity`],
      ['app-nb-run', `/applications/${nbAppId}/runs/${runId}`],
    ]
    if (modId) routes.push(['module-detail', `/modules/${modId}`])

    for (const [name, path] of routes) {
      await page.setViewportSize(DESKTOP)
      await page.emulateMedia({ colorScheme: 'light' })
      await page.goto(rt.viteURL + path)
      await page.waitForTimeout(1200)
      await shot(page, `${name}-light`)
      await page.emulateMedia({ colorScheme: 'dark' })
      await page.reload()
      await page.waitForTimeout(1200)
      await shot(page, `${name}-dark`)
    }

    // Notebook with the Test panel open and a test run executed.
    await page.setViewportSize(DESKTOP)
    await page.emulateMedia({ colorScheme: 'light' })
    await page.goto(`${rt.viteURL}/applications/${nbAppId}`)
    await page.getByRole('button', { name: 'Test', exact: true }).click()
    await page.locator('#app-input').fill('{"name":"Playwright"}')
    await page.getByTestId('app-run').click()
    const testPanel = page.getByRole('region', { name: 'Test notebook' })
    await expect(testPanel.locator('.app-out-pre')).toContainText('Playwright', { timeout: 30_000 })
    await page.waitForTimeout(400)
    await shot(page, 'app-nb-testpanel-light')
    await page.emulateMedia({ colorScheme: 'dark' })
    await page.waitForTimeout(400)
    await shot(page, 'app-nb-testpanel-dark')
    await page.emulateMedia({ colorScheme: 'light' })

    // Settings pane open over the notebook.
    await page.getByRole('button', { name: 'Settings', exact: true }).click()
    await page.waitForTimeout(700)
    await shot(page, 'app-nb-settings-light')
    await page.emulateMedia({ colorScheme: 'dark' })
    await page.waitForTimeout(400)
    await shot(page, 'app-nb-settings-dark')
    await page.emulateMedia({ colorScheme: 'light' })
    await page.getByRole('button', { name: 'Settings', exact: true }).click()
    await page.waitForTimeout(400)

    // Invoke view: real UI invoke -> output pane populated.
    await page.goto(`${rt.viteURL}/applications/${nbAppId}?view=invoke`)
    const invokePanel = page.getByRole('tabpanel', { name: 'Invoke', exact: true })
    await invokePanel.locator('#app-call-input').fill('{"name": "UI"}')
    await invokePanel.getByRole('button', { name: 'Send' }).click()
    await expect(invokePanel.locator('.app-invoke-output > .wb-pane-body > .app-out-pre')).toContainText('Hello, UI!', { timeout: 30_000 })
    await page.waitForTimeout(400)
    await shot(page, 'app-nb-invoke-done-light')
    await page.emulateMedia({ colorScheme: 'dark' })
    await page.waitForTimeout(400)
    await shot(page, 'app-nb-invoke-done-dark')
    await page.emulateMedia({ colorScheme: 'light' })

    // Mobile light: app notebook + invoke.
    await page.setViewportSize(MOBILE)
    await page.goto(`${rt.viteURL}/applications/${nbAppId}`)
    await page.waitForTimeout(900)
    await shot(page, 'app-nb-notebook-mobile-light')
  })

  // Debug entries: the gutter ▶ runs a non-main function with verbatim
  // input; the Test panel shows the Entry select and the debug note.
  test('debug run capture', async ({ page }) => {
    test.setTimeout(300_000)
    const dbg = join(OUT, 'debug')
    await mkdir(dbg, { recursive: true })
    const r = await rt.client.post('/api/v1/applications', {
      data: {
        name: 'NbDebug',
        sections: [{ id: 'main', code: 'def double(x):\n    return x * 2\n\ndef main(input):\n    return {"echo": input}\n' }],
      },
    })
    expect(r.ok(), `create debug app: ${await r.text()}`).toBeTruthy()
    const id = (await r.json()).application.id as string

    await page.setViewportSize(DESKTOP)
    await page.emulateMedia({ colorScheme: 'light' })
    await page.goto(`${rt.viteURL}/applications/${id}`)
    await expect(page.locator('[aria-label="Application section main"] .cm-content'))
      .toContainText('def double(x)', { timeout: 15_000 })
    await page.waitForTimeout(600)
    // Zoomed crop of the gutter markers on the def lines.
    await page.locator('[aria-label="Application section main"]')
      .screenshot({ path: join(dbg, 'gutter-markers-light.png') })

    await page.getByRole('button', { name: 'Test', exact: true }).click()
    await page.locator('#app-input').fill('21')
    await page.locator('button[aria-label="Run double"]').click()
    const panel = page.getByRole('region', { name: 'Test notebook' })
    await expect(panel.locator('.app-out-pre')).toContainText('42', { timeout: 30_000 })
    await page.waitForTimeout(400)
    await page.screenshot({ path: join(dbg, 'app-testpanel-debug-light.png') })
    await page.emulateMedia({ colorScheme: 'dark' })
    await page.waitForTimeout(400)
    await page.screenshot({ path: join(dbg, 'app-testpanel-debug-dark.png') })
    await page.locator('[aria-label="Application section main"]')
      .screenshot({ path: join(dbg, 'gutter-markers-dark.png') })
  })
})

// Reviewed publish over the API (preview -> publish with digests).
async function publishApp(id: string, version: number) {
  const prev = await rt.client.post(`/api/v1/applications/${id}/publish/preview`, { data: { version } })
  expect(prev.status(), `preview: ${await prev.text()}`).toBe(200)
  const rev = await prev.json()
  const pub = await rt.client.post(`/api/v1/applications/${id}/publish`, {
    data: { version, expected_base: rev.base?.digest ?? '', candidate: rev.candidate.digest },
  })
  expect(pub.status(), `publish: ${await pub.text()}`).toBe(202)
}
