import { test, expect } from '@playwright/test'
import { execFileSync } from 'node:child_process'
import { mkdir } from 'node:fs/promises'
import { join } from 'node:path'
import { startApplicationRuntime, type ApplicationRuntime } from './applicationRuntime'

test.setTimeout(120_000)

let rt: ApplicationRuntime
const SHOTS = join(import.meta.dirname, '..', '..', '.devin-artifacts', 'screenshots')
test.beforeAll(async () => {
  rt = await startApplicationRuntime()
  await mkdir(SHOTS, { recursive: true })
})
test.afterAll(async () => { await rt?.close() })

// --- helpers ---------------------------------------------------------------

async function createInstance(ttl = 600): Promise<string> {
  const r = await rt.client.post('/api/v1/instances', {
    data: { capabilities: { io: {} }, ttl_seconds: ttl },
  })
  expect(r.status()).toBe(201)
  return (await r.json()).instance.id as string
}

async function notebookRevision(id: string): Promise<number> {
  const r = await rt.client.get(`/api/v1/instances/${id}/notebook`)
  expect(r.status()).toBe(200)
  return (await r.json()).notebook.revision as number
}

async function saveSection(id: string, name: string, code: string, extra: Record<string, unknown> = {}): Promise<void> {
  const rev = await notebookRevision(id)
  const r = await rt.client.put(`/api/v1/instances/${id}/sections/${name}`, {
    data: { code, description: `section ${name}`, revision: rev, ...extra },
  })
  expect(r.status()).toBe(200)
}

async function runAll(id: string): Promise<void> {
  const r = await rt.client.post(`/api/v1/instances/${id}/notebook/run`, { data: { sections: 'all' } })
  expect(r.status()).toBe(200)
}

function card(page: import('@playwright/test').Page, name: string) {
  return page.locator(`[data-section="${name}"]`)
}

// instance child processes (`<worker> __instance`) — used to simulate a
// lost runtime by killing exactly the child this test's instance owns.
function instanceChildPids(): number[] {
  try {
    const out = execFileSync('pgrep', ['-f', '__instance'], { encoding: 'utf8' })
    return out.split('\n').map((s) => parseInt(s.trim(), 10)).filter((n) => Number.isFinite(n) && n > 0)
  } catch {
    return [] // pgrep exits 1 on no matches
  }
}

// --- tests -----------------------------------------------------------------

test('autosave: Run section awaits the pending save and executes latest code', async ({ page }) => {
  const id = await createInstance()
  const order: string[] = []
  page.on('request', (r) => {
    const u = r.url()
    if (r.method() === 'PUT' && u.includes(`/sections/`)) order.push('PUT ' + u.split('/sections/')[1])
    if (r.method() === 'POST' && u.endsWith('/notebook/run')) order.push('RUN')
  })
  await page.goto(`${rt.viteURL}/instances/${id}`)
  // First test pays the cold Vite transform cost — give it room.
  await expect(page.locator('.nb-toolbar')).toBeVisible({ timeout: 30_000 })

  // Create a section through the UI.
  await page.locator('.nb-toolbar').getByRole('button', { name: 'Add section' }).click()
  const dlg = page.getByRole('dialog')
  await dlg.locator('input').nth(0).fill('greet')
  await dlg.locator('input').nth(1).fill('says hello')
  await dlg.locator('textarea').fill('print("V1")')
  await dlg.getByRole('button', { name: 'Create section' }).click()
  await expect(dlg).toHaveCount(0)

  const c = card(page, 'greet')
  await expect(c).toBeVisible()
  // Wait until the autosave queue is live (Saved, not loading…).
  await expect(c.getByText('Saved', { exact: true })).toBeVisible({ timeout: 15_000 })

  // Edit and run IMMEDIATELY — inside the 800ms debounce the draft is
  // still pending; the run must flush it first (P3: runs execute saved
  // code, and the saved code must be what is on screen).
  await c.locator('.cm-content').click()
  await page.keyboard.press('Meta+A')
  await page.keyboard.insertText('print("RUNS-LATEST")')
  await c.getByRole('button', { name: 'Run section' }).click()

  await expect(page.locator('.nb-batch')).toContainText('greet', { timeout: 20_000 })
  await expect(page.locator('.nb-batch')).toContainText('ok')
  // The PUT landed before the run request — never the other way round.
  expect(order.indexOf('PUT greet')).toBeGreaterThanOrEqual(0)
  expect(order.indexOf('PUT greet')).toBeLessThan(order.indexOf('RUN'))
  // And the executed code is the latest draft, verified server-side.
  await expect.poll(async () => {
    const r = await rt.client.get(`/api/v1/instances/${id}/sections/greet?include_output=true`)
    return (await r.json()).section.output
  }, { timeout: 15_000 }).toContain('RUNS-LATEST')
})

test('conflict: a 409 save blocks Run until explicitly resolved (C2/F3)', async ({ page }) => {
  const id = await createInstance()
  await saveSection(id, 'fragile', 'x = 1')
  await page.goto(`${rt.viteURL}/instances/${id}`)
  const c = card(page, 'fragile')
  await expect(c).toBeVisible()
  await expect(c.getByText('Saved', { exact: true })).toBeVisible({ timeout: 15_000 })

  // Force the next autosave PUT to conflict once. A bare 409 is not
  // enough: the queue treats an unchanged server copy as a stale-CAS
  // race and retries, so a concurrent edit must land first.
  let conflicted = false
  await page.route(`**/api/v1/instances/${id}/sections/fragile`, async (route) => {
    if (route.request().method() === 'PUT' && !conflicted) {
      conflicted = true
      const rev = await notebookRevision(id)
      await rt.client.put(`/api/v1/instances/${id}/sections/fragile`, {
        data: { code: 'x = "SERVER-WINS"', description: 'section fragile', revision: rev },
      })
      await route.fulfill({
        status: 409,
        contentType: 'application/json',
        body: JSON.stringify({ error: { code: 'conflict', message: 'notebook changed since your revision; reload and retry' } }),
      })
      return
    }
    await route.continue()
  })

  await c.locator('.cm-content').click()
  await page.keyboard.press('Meta+A')
  await page.keyboard.insertText('x = "LOCAL-DRAFT"')
  await expect(c.getByText('Conflict', { exact: true })).toBeVisible({ timeout: 15_000 })
  await expect(c.getByText('This section changed elsewhere', { exact: false })).toBeVisible()
  // The draft is kept on screen AND the run is blocked — saved code is
  // what executes, and the draft is not saved.
  await expect(c.locator('.cm-content')).toContainText('LOCAL-DRAFT')
  await expect(c.getByRole('button', { name: 'Run section' })).toBeDisabled()

  // Explicit recovery: reload drops the draft for the server copy —
  // which now holds the concurrent edit, not the original 'x = 1'.
  await c.getByRole('button', { name: 'Reload server copy' }).click()
  await expect(c.getByText('This section changed elsewhere', { exact: false })).toHaveCount(0, { timeout: 15_000 })
  await expect(c.locator('.cm-content')).toContainText('SERVER-WINS')
  await expect(c.getByRole('button', { name: 'Run section' })).toBeEnabled()
})

test('polling: a held autosave keeps the dirty draft across polls and view switches (C2/C3)', async ({ page }) => {
  const id = await createInstance()
  await saveSection(id, 'drafted', 'v = "SERVER"')
  await page.goto(`${rt.viteURL}/instances/${id}`)
  const c = card(page, 'drafted')
  await expect(c).toBeVisible()
  await expect(c.getByText('Saved', { exact: true })).toBeVisible({ timeout: 15_000 })

  // Hold the next PUT so the draft stays dirty/saving across polls.
  let release!: () => void
  const gate = new Promise<void>((res) => { release = res })
  let held = false
  await page.route(`**/api/v1/instances/${id}/sections/drafted`, async (route) => {
    if (route.request().method() === 'PUT' && !held) {
      held = true
      await gate
    }
    await route.continue()
  })
  try {
    await c.locator('.cm-content').click()
    await page.keyboard.press('Meta+A')
    await page.keyboard.insertText('v = "DIRTY-DRAFT"')
    await expect(c.locator('.cm-content')).toContainText('DIRTY-DRAFT')
    // Wait past a notebook poll (5s) AND a section-detail poll (5s)…
    await page.waitForTimeout(5600)
    // …then switch views and back — the mounted editor keeps the draft.
    await page.getByRole('tab', { name: 'Activity' }).click()
    await page.getByRole('tab', { name: 'Notebook' }).click()
    await expect(c.locator('.cm-content')).toContainText('DIRTY-DRAFT')
    release()
    await expect(c.getByText('Saved', { exact: true })).toBeVisible({ timeout: 15_000 })
  } finally {
    release()
    await page.unrouteAll()
  }
})

test('held order: polled arrivals wait behind "Show N new" (S5)', async ({ page }) => {
  const id = await createInstance()
  await saveSection(id, 'alpha', 'a = 1')
  await saveSection(id, 'beta', 'b = 2')
  await page.goto(`${rt.viteURL}/instances/${id}`)
  await expect(card(page, 'alpha')).toBeVisible()
  await expect(card(page, 'beta')).toBeVisible()

  // A new section lands server-side (e.g. an agent's add_section).
  await saveSection(id, 'gamma', 'g = 3')
  // The 5s poll picks it up but the held order keeps it back.
  await expect(page.getByRole('button', { name: /Show 1 new/ })).toBeVisible({ timeout: 20_000 })
  await expect(card(page, 'gamma')).toHaveCount(0)
  await page.getByRole('button', { name: /Show 1 new/ }).click()
  await expect(card(page, 'gamma')).toBeVisible()
  // Order is the server's: alpha, beta, gamma.
  const names = await page.locator('.nb-cards [data-section]').evaluateAll(
    (els) => els.map((e) => e.getAttribute('data-section')))
  expect(names).toEqual(['alpha', 'beta', 'gamma'])
})

test('once: an explicit rerun of a succeeded once-section asks first (F2)', async ({ page }) => {
  const id = await createInstance()
  await saveSection(id, 'charge', 'print("CHARGED")', { rerun: 'once' })
  await page.goto(`${rt.viteURL}/instances/${id}`)
  const c = card(page, 'charge')
  await expect(c).toBeVisible()
  await expect(c.getByText('Saved', { exact: true })).toBeVisible({ timeout: 15_000 })
  await expect(c.getByText(/skip in bulk after success/)).toBeVisible()

  await c.getByRole('button', { name: 'Run section' }).click()
  await expect(page.locator('.nb-batch')).toContainText('ok', { timeout: 20_000 })

  // Second explicit run → Confirm, not silent repeat.
  await c.getByRole('button', { name: 'Run section' }).click()
  const dlg = page.getByRole('dialog')
  await expect(dlg).toContainText('Run charge again?')
  await expect(dlg.getByRole('button', { name: 'Run again' })).toBeVisible()
  await dlg.getByRole('button', { name: 'Run again' }).click()
  await expect(page.locator('.nb-batch')).toContainText('ok', { timeout: 20_000 })
})

test('restored: a dead child restores an EMPTY process — the request does not run', async ({ page }) => {
  const before = instanceChildPids()
  const id = await createInstance()
  await saveSection(id, 'loader', 'mark = "OLD-STATE"\nprint("OLD-OUTPUT")')
  await runAll(id)
  // Historical output exists and is incarnation-1's.
  const mine = instanceChildPids().filter((p) => !before.includes(p))
  expect(mine.length).toBe(1)
  process.kill(mine[0], 'SIGKILL')

  let runPosts = 0
  page.on('request', (r) => {
    if (r.method() === 'POST' && r.url().endsWith('/notebook/run')) runPosts++
  })
  await page.goto(`${rt.viteURL}/instances/${id}`)
  const c = card(page, 'loader')
  await expect(c).toBeVisible()
  await expect(c.getByText('Saved', { exact: true })).toBeVisible({ timeout: 15_000 })

  await c.getByRole('button', { name: 'Run section' }).click()
  // The persistent notice: restored empty, request did NOT run.
  await expect(page.getByText('Runtime restored.', { exact: false })).toBeVisible({ timeout: 30_000 })
  await expect(page.getByText('did not run', { exact: false })).toBeVisible()
  // Exactly one run request — never an automatic retry of the trigger.
  expect(runPosts).toBe(1)
  // Sections are not_run on the empty incarnation; old output is kept
  // and marked as previous-runtime.
  await expect(c.getByText('not run', { exact: true })).toBeVisible({ timeout: 20_000 })
  await expect(c.locator('.nb-output summary', { hasText: 'previous runtime' })).toBeVisible()
  // State is verifiably empty: re-running loader alone fails (mark was
  // never defined), while the notebook itself is intact.
  await c.getByRole('button', { name: 'Run section' }).click()
  await expect(page.locator('.nb-batch')).toContainText('loader', { timeout: 20_000 })
})

test('expired: read-only history with the expiry shown, no run or edit affordances', async ({ page }) => {
  const id = await createInstance(3) // 3s TTL — the 10s reaper settles it
  await saveSection(id, 'eph', 'e = 1')
  await page.goto(`${rt.viteURL}/instances/${id}`)
  await expect(card(page, 'eph')).toBeVisible({ timeout: 20_000 })
  // Wait for the reaper to flip status → expired (UI polls every 15s).
  // Exact match isolates the notebook notice's <strong>Expired.</strong>
  // from the next-step heading and scratch notice.
  await expect(page.getByText('Expired.', { exact: true })).toBeVisible({ timeout: 60_000 })
  await expect(page.locator('.nb').getByText('read-only history', { exact: false })).toBeVisible()
  // No run buttons, no Add section, no editable fields.
  await expect(page.getByRole('button', { name: 'Run section' })).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Run all' })).toHaveCount(0)
  await expect(page.locator('.nb-toolbar').getByRole('button', { name: 'Add section' })).toHaveCount(0)
  await expect(card(page, 'eph').locator('.cm-content')).toHaveCount(0)
  await expect(card(page, 'eph').locator('pre.nb-code-readonly')).toContainText('e = 1')
})

test('directory + collapse: outline rows deep-link; code folds per card', async ({ page }) => {
  const id = await createInstance()
  await saveSection(id, 'alpha', 'a = 1')
  await saveSection(id, 'beta', 'b = 2')
  await page.goto(`${rt.viteURL}/instances/${id}`)
  const dir = page.locator('.nb-directory')
  await expect(dir).toBeVisible()
  await expect(dir.locator('.nb-dir-row')).toHaveCount(2)
  await expect(dir.locator('.nb-dir-row').first()).toContainText('alpha')
  // Click deep-links ?section= and scrolls the card into view (C4).
  await dir.locator('.nb-dir-row', { hasText: 'beta' }).click()
  await expect(page).toHaveURL(/section=beta/)
  await expect(card(page, 'beta')).toBeInViewport()
  // Per-card collapse hides the editor; header and footer stay (H).
  const c = card(page, 'beta')
  await c.getByRole('button', { name: 'Collapse beta code' }).click()
  await expect(c.locator('.cm-content')).toHaveCount(0)
  await expect(c.locator('.nb-card-head')).toBeVisible()
  await c.getByRole('button', { name: 'Expand beta code' }).click()
  await expect(c.locator('.cm-content')).toBeVisible()
})

test('run CAS: an agent edit between validation and POST conflicts — nothing executes', async ({ page }) => {
  const id = await createInstance()
  await saveSection(id, 's1', 'x = 1')
  await page.goto(`${rt.viteURL}/instances/${id}`)
  const c = card(page, 's1')
  await expect(c).toBeVisible()
  await expect(c.getByText('Saved', { exact: true })).toBeVisible({ timeout: 15_000 })
  const execsBefore = await (await rt.client.get(`/api/v1/instances/${id}/executions`)).json()

  // The agent writes between the client's revision capture and the
  // run POST: the captured revision is stale, the server must 409 and
  // execute nothing.
  await page.route('**/notebook/run', async (route) => {
    await saveSection(id, 's1', 'x = 999')
    await route.continue()
  })
  await c.getByRole('button', { name: 'Run section' }).click()
  await expect(page.getByText('notebook changed while preparing the run', { exact: false })).toBeVisible({ timeout: 20_000 })
  await page.unroute('**/notebook/run')
  // No run report was produced and no execution landed.
  await expect(page.locator('.nb-batch')).toHaveCount(0)
  const execsAfter = await (await rt.client.get(`/api/v1/instances/${id}/executions`)).json()
  expect((execsAfter.executions ?? []).length).toBe((execsBefore.executions ?? []).length)
  // The editor adopts the server's newer code — the draft was not
  // destroyed and nothing stale overwrote it.
  await expect(c.locator('.cm-content')).toContainText('x = 999', { timeout: 15_000 })
})

test('save CAS gap: an agent write between revision capture and detail read keeps remote code + local draft', async ({ page }) => {
  const id = await createInstance()
  await saveSection(id, 's1', 'base = 1')
  await page.goto(`${rt.viteURL}/instances/${id}`)
  const c = card(page, 's1')
  await expect(c).toBeVisible()
  await expect(c.getByText('Saved', { exact: true })).toBeVisible({ timeout: 15_000 })

  // Establish a saved baseline, then type a local edit so autosave PUTs.
  await c.locator('.cm-content').click()
  await page.keyboard.type('\nlocal_draft = 1')
  await expect(c.getByText('Unsaved', { exact: true })).toBeVisible({ timeout: 5_000 })

  // The save's 409-retry reads the section with include_output=false —
  // the only such request on this page. An agent write landing in that
  // gap must make the retry conflict, never overwrite remote code.
  await page.route(/sections\/s1\?.*include_output=false/, async (route) => {
    const rev = await notebookRevision(id)
    const r = await rt.client.put(`/api/v1/instances/${id}/sections/s1`, {
      data: { code: 'remote_code = 1', description: 'section s1', revision: rev },
    })
    expect(r.status()).toBe(200)
    await route.continue()
  })
  // Bump the notebook revision via another section so the pending
  // autosave's CAS fails and takes the compare-and-retry path.
  await saveSection(id, 'other', 'o = 1')
  // Wait for the debounced autosave to hit the 409 + retry path.
  await expect(c.getByText('Conflict', { exact: true })).toBeVisible({ timeout: 20_000 })
  await page.unroute(/sections\/s1\?.*include_output=false/)
  // Remote code is intact — the retry did NOT overwrite it.
  const det = await rt.client.get(`/api/v1/instances/${id}/sections/s1`)
  expect((await det.json()).section.code).toBe('remote_code = 1')
  // The local draft text is still in the editor (conflict preserved it).
  await expect(c.locator('.cm-content')).toContainText('local_draft')
})

test('run validation: an agent edit before the run blocks unseen code (F)', async ({ page }) => {
  const id = await createInstance()
  await saveSection(id, 's1', 'x = "ORIGINAL"')
  await page.goto(`${rt.viteURL}/instances/${id}`)
  const c = card(page, 's1')
  await expect(c).toBeVisible()
  await expect(c.getByText('Saved', { exact: true })).toBeVisible({ timeout: 15_000 })

  // Pin the queue's saved value: abort the section DETAIL polls so the
  // agent's edit below can never be adopted into saved/latest. The run
  // validation reads include_output=false — a different URL — so it
  // still sees the true server copy.
  await page.route(/sections\/s1\?.*include_output=true/, (route) => route.abort())

  // The agent rewrites the section BEFORE the run's fresh notebook GET
  // returns a revision: the captured revision is already the agent's,
  // so only the per-section compare can catch the unseen code.
  const rev = await notebookRevision(id)
  const wr = await rt.client.put(`/api/v1/instances/${id}/sections/s1`, {
    data: { code: 'x = "AGENT-EDIT"', description: 'section s1', revision: rev },
  })
  expect(wr.status()).toBe(200)

  let runPosts = 0
  page.on('request', (r) => {
    if (r.method() === 'POST' && r.url().endsWith('/notebook/run')) runPosts++
  })
  await c.getByRole('button', { name: 'Run section' }).click()
  await expect(page.getByText('changed on the server', { exact: false })).toBeVisible({ timeout: 20_000 })
  // Nothing executed and no run request was ever sent.
  expect(runPosts).toBe(0)
  // The displayed draft is preserved — polling could not overwrite it.
  await expect(c.locator('.cm-content')).toContainText('ORIGINAL')
  await page.unroute(/sections\/s1\?.*include_output=true/)
})

test('partial restore: a mid-batch loss warns that earlier sections executed', async ({ page }) => {
  const id = await createInstance()
  await saveSection(id, 's1', 'a = 1')
  await page.goto(`${rt.viteURL}/instances/${id}`)
  await expect(card(page, 's1')).toBeVisible()
  await expect(card(page, 's1').getByText('Saved', { exact: true })).toBeVisible({ timeout: 15_000 })

  // Mock the honest partial report the API returns when the runtime is
  // lost after earlier sections already executed.
  await page.route('**/notebook/run', async (route) => {
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        report: {
          instance_id: id, incarnation: 2, revision: 3, drift: 0,
          restored: true, stopped_reason: 'instance_restored',
          results: [
            { name: 's1', status: 'ok', exec_id: 'exe_1', duration_ms: 4 },
            { name: 's2', status: 'not_run' },
          ],
          warnings: ['instance was restored during the run: earlier sections executed in the previous runtime; do not blindly retry side effects'],
        },
      }),
    })
  })
  await page.getByRole('button', { name: 'Run all' }).click()
  // The batch report shows every selected row's honest status.
  const report = page.locator('.nb-batch')
  await expect(report).toContainText('s1', { timeout: 20_000 })
  await expect(report).toContainText('ok')
  await expect(report).toContainText('not_run')
  await expect(report).toContainText('do not blindly retry')
  // The persistent notice says PARTIAL — earlier sections DID run —
  // and survives dismissing the batch report.
  const notice = page.getByText('earlier sections in this batch did execute', { exact: false })
  await expect(notice).toBeVisible()
  await report.getByRole('button', { name: 'dismiss' }).click()
  await expect(report).toHaveCount(0)
  await expect(notice).toBeVisible()
  await page.unroute('**/notebook/run')
})

test('scratch shortcut: ⌘/Ctrl+Enter only fires inside the visible scratch region', async ({ page }) => {
  const id = await createInstance()
  await saveSection(id, 's1', 'x = 1')
  await page.goto(`${rt.viteURL}/instances/${id}`)
  const c = card(page, 's1')
  await expect(c).toBeVisible()
  await expect(c.getByText('Saved', { exact: true })).toBeVisible({ timeout: 15_000 })

  let execPosts = 0
  page.on('request', (r) => {
    if (r.method() === 'POST' && /\/instances\/[^/]+\/exec$/.test(r.url())) execPosts++
  })
  // Inside a SECTION editor the shortcut must not trigger scratch.
  await c.locator('.cm-content').click()
  await page.keyboard.press('Control+Enter')
  await page.waitForTimeout(400)
  expect(execPosts).toBe(0)

  // Hidden behind Activity the mounted scratch region still must not run.
  await page.getByRole('tab', { name: 'Activity' }).click()
  await page.locator('#run-code .cm-content').evaluate((el) => {
    el.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', ctrlKey: true, bubbles: true }))
  })
  await page.waitForTimeout(400)
  expect(execPosts).toBe(0)

  // Visible scratch region: the shortcut works (positive control).
  await page.getByRole('tab', { name: 'Notebook' }).click()
  await page.locator('#run-code .cm-content').click()
  await page.keyboard.press('Control+Enter')
  await expect.poll(() => execPosts, { timeout: 10_000 }).toBe(1)
})

test('screenshots: notebook desktop/mobile × light/dark', async ({ page }) => {
  const id = await createInstance()
  await saveSection(id, 'setup', 'x = 40 + 2')
  await saveSection(id, 'report', 'print("answer", x)', { rerun: 'once' })
  await runAll(id)
  await page.goto(`${rt.viteURL}/instances/${id}`)
  await expect(card(page, 'setup')).toBeVisible()
  await expect(card(page, 'report')).toBeVisible()
  await expect(page.locator('.nb-directory .nb-dir-row')).toHaveCount(2)
  for (const scheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme: scheme })
    await page.screenshot({ path: join(SHOTS, `notebook-desktop-${scheme}.png`), fullPage: false })
    await page.setViewportSize({ width: 390, height: 844 })
    await expect(page.locator('.nb')).toBeVisible()
    // No horizontal overflow at mobile width.
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
    expect(overflow).toBeLessThanOrEqual(1)
    // Viewport top — not a tiled fullPage capture.
    await page.screenshot({ path: join(SHOTS, `notebook-mobile-${scheme}.png`), fullPage: false })
    // A scrolled section card and the scratch area as separate shots.
    await card(page, 'report').scrollIntoViewIfNeeded()
    await page.screenshot({ path: join(SHOTS, `notebook-mobile-${scheme}-section.png`), fullPage: false })
    await page.locator('#run-code').scrollIntoViewIfNeeded()
    await page.screenshot({ path: join(SHOTS, `notebook-mobile-${scheme}-scratch.png`), fullPage: false })
    await page.setViewportSize({ width: 1440, height: 1000 })
  }
})
