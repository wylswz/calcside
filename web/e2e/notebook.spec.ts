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

async function runAll(id: string) {
  const r = await rt.client.post(`/api/v1/instances/${id}/notebook/run`, { data: { sections: 'all' } })
  expect(r.status()).toBe(200)
  return r.json()
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

  // Add section creates an "Untitled Section" immediately — no
  // dialog; the name is auto-generated as untitled_section.
  await page.locator('.nb-toolbar').getByRole('button', { name: 'Add section' }).click()
  const c = card(page, 'untitled_section')
  await expect(c).toBeVisible()
  await expect(page.getByRole('dialog')).toHaveCount(0)
  // 'Untitled Section' lands in the description input; code is typed
  // into the new cell afterwards.
  await expect(c.locator('input[aria-label="untitled_section description"]')).toHaveValue('Untitled Section')
  // The new cell's editor takes focus; seed it with code.
  await c.locator('.cm-content').click()
  await page.keyboard.insertText('print("V1")')
  // Wait until the autosave queue is live (Saved, not loading…).
  await expect(c.getByText('Saved', { exact: true })).toBeVisible({ timeout: 15_000 })

  // Edit and run IMMEDIATELY — inside the 800ms debounce the draft is
  // still pending; the run must flush it first (P3: runs execute saved
  // code, and the saved code must be what is on screen).
  await c.locator('.cm-content').click()
  await page.keyboard.press('Meta+A')
  await page.keyboard.insertText('print("RUNS-LATEST")')
  await c.getByRole('button', { name: 'Run section' }).click()

  // Single-section feedback is one line in the sticky status slot (S3).
  await expect(page.locator('.nb-status')).toContainText('untitled_section · ok', { timeout: 20_000 })
  // The PUT landed before the run request — never the other way round.
  expect(order.indexOf('PUT untitled_section')).toBeGreaterThanOrEqual(0)
  expect(order.indexOf('PUT untitled_section')).toBeLessThan(order.indexOf('RUN'))
  // And the executed code is the latest draft, verified server-side.
  await expect.poll(async () => {
    const r = await rt.client.get(`/api/v1/instances/${id}/sections/untitled_section?include_output=true`)
    return (await r.json()).section.output
  }, { timeout: 15_000 }).toContain('RUNS-LATEST')
})

test('Add section appends an Untitled Section without a dialog', async ({ page }) => {
  const id = await createInstance()
  await saveSection(id, 's1', 'a = 1')
  await saveSection(id, 's2', 'b = 2')
  await page.goto(`${rt.viteURL}/instances/${id}`)
  await expect(card(page, 's1')).toBeVisible()
  await expect(card(page, 's2')).toBeVisible()

  // Toolbar adds append at the end, auto-named, with no dialog.
  const add = page.locator('.nb-toolbar').getByRole('button', { name: 'Add section' })
  await add.click()
  await expect(card(page, 'untitled_section')).toBeVisible()
  await add.click()
  await expect(card(page, 'untitled_section_2')).toBeVisible()
  await expect(page.getByRole('dialog')).toHaveCount(0)

  const names = () => page.locator('.nb-cards [data-section]').evaluateAll(
    (els) => els.map((e) => e.getAttribute('data-section')))
  expect(await names()).toEqual(['s1', 's2', 'untitled_section', 'untitled_section_2'])
  const nb = await (await rt.client.get(`/api/v1/instances/${id}/notebook`)).json()
  expect(nb.notebook.sections.map((s: { name: string }) => s.name))
    .toEqual(['s1', 's2', 'untitled_section', 'untitled_section_2'])

  // The per-cell inserter creates one directly after its cell.
  await page.getByRole('button', { name: 'Insert section after s1' }).click()
  await expect(card(page, 'untitled_section_3')).toBeVisible()
  await expect(page.getByRole('dialog')).toHaveCount(0)
  expect(await names()).toEqual(['s1', 'untitled_section_3', 's2', 'untitled_section', 'untitled_section_2'])
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
  // The once marker is a quiet tag in the cell header (it lives in the
  // cell menu now, not a permanent checkbox row).
  await expect(c.locator('.nb-once')).toHaveText('once')

  await c.getByRole('button', { name: 'Run section' }).click()
  await expect(page.locator('.nb-status')).toContainText('charge · ok', { timeout: 20_000 })

  // Second explicit run → Confirm, not silent repeat.
  await c.getByRole('button', { name: 'Run section' }).click()
  const dlg = page.getByRole('dialog')
  await expect(dlg).toContainText('Run charge again?')
  await expect(dlg.getByRole('button', { name: 'Run again' })).toBeVisible()
  await dlg.getByRole('button', { name: 'Run again' }).click()
  await expect(page.locator('.nb-status')).toContainText('charge · ok', { timeout: 20_000 })
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
  // The status line and the cell note both carry the restore warning —
  // the request did NOT run (P5: single-section feedback lands on the
  // cell too, so scope assertions to avoid strict-mode duplicates).
  await expect(page.locator('.nb-status')).toContainText('Runtime restored', { timeout: 30_000 })
  await expect(page.locator('.nb-status')).toContainText('did not run')
  await expect(c.locator('.nb-note')).toContainText('did not run')
  // Exactly one run request — never an automatic retry of the trigger.
  expect(runPosts).toBe(1)
  // Sections are not_run on the empty incarnation; old output is kept
  // and marked as previous-runtime (always-visible output block now).
  await expect(c.getByText('not run', { exact: true })).toBeVisible({ timeout: 20_000 })
  await expect(c.locator('.nb-output-tag', { hasText: 'previous runtime' })).toBeVisible()
  // State is verifiably empty: re-running loader alone fails (mark was
  // never defined), while the notebook itself is intact.
  await c.getByRole('button', { name: 'Run section' }).click()
  await expect(page.locator('.nb-status')).toContainText('loader', { timeout: 20_000 })
})

test('expired: read-only history with the expiry shown, no run or edit affordances', async ({ page }) => {
  const id = await createInstance(3) // 3s TTL — the 10s reaper settles it
  await saveSection(id, 'eph', 'e = 1')
  await page.goto(`${rt.viteURL}/instances/${id}`)
  await expect(card(page, 'eph')).toBeVisible({ timeout: 20_000 })
  // Wait for the reaper to flip status → expired (UI polls every 15s).
  // The one-line status slot carries the lifecycle text — scoped so the
  // scratch footer note and next-step heading cannot collide.
  await expect(page.locator('.nb-status')).toContainText('Expired.', { timeout: 60_000 })
  await expect(page.locator('.nb-status')).toContainText('Read-only history')
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
  await expect(dir.locator('.nb-dir-row:not(.nb-dir-scratch)')).toHaveCount(2)
  await expect(dir.locator('.nb-dir-row:not(.nb-dir-scratch)').first()).toContainText('alpha')
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

test('S3: section directory stays visible while a long notebook scrolls', async ({ page }) => {
  const id = await createInstance()
  for (let i = 1; i <= 8; i++) await saveSection(id, `sec${i}`, `v${i} = ${i}`)
  await page.goto(`${rt.viteURL}/instances/${id}`)
  const dir = page.locator('.nb-directory')
  const head = page.locator('.nb-head')
  await expect(dir).toBeVisible()
  // 8 section chips + the trailing scratch chip.
  await expect(dir.locator('.nb-dir-row')).toHaveCount(9)
  const headBefore = (await head.boundingBox())!.height
  await page.evaluate(() => window.scrollTo(0, document.documentElement.scrollHeight))
  await page.waitForTimeout(300)
  // The strip is pinned as row 2 of the sticky head: its top is the
  // toolbar's bottom edge and the head keeps one height (S3).
  await expect(dir).toBeInViewport()
  const dirBox = (await dir.boundingBox())!
  const barBox = (await page.locator('.nb-toolbar').boundingBox())!
  expect(Math.abs(dirBox.y - (barBox.y + barBox.height))).toBeLessThanOrEqual(1)
  expect((await head.boundingBox())!.height).toBe(headBefore)
})

test('S3: jumping to a section lands the card below the sticky head', async ({ page }) => {
  const id = await createInstance()
  for (let i = 1; i <= 8; i++) await saveSection(id, `sec${i}`, `v${i} = ${i}`)
  await page.goto(`${rt.viteURL}/instances/${id}`)
  const dir = page.locator('.nb-directory')
  await dir.locator('.nb-dir-row', { hasText: 'sec8' }).click()
  await expect(page).toHaveURL(/section=sec8/)
  const c = card(page, 'sec8')
  await expect(c).toBeInViewport()
  const cBox = (await c.boundingBox())!
  const dBox = (await dir.boundingBox())!
  expect(cBox.y).toBeGreaterThanOrEqual(dBox.y + dBox.height)
})

test('S1: scroll-spy highlight is paint-only', async ({ page }) => {
  const id = await createInstance()
  for (let i = 1; i <= 8; i++) await saveSection(id, `sec${i}`, `v${i} = ${i}`)
  await page.goto(`${rt.viteURL}/instances/${id}`)
  const dir = page.locator('.nb-directory')
  const chip = dir.locator('.nb-dir-row', { hasText: 'sec5' })
  await expect(chip).toBeVisible()
  const before = (await chip.boundingBox())!
  await chip.click()
  // The jumped-to chip carries the marker and it is the only one (S1:
  // aria-current repaints background/name colour, never the box).
  await expect(chip).toHaveAttribute('aria-current', 'true')
  await expect(dir.locator('.nb-dir-row[aria-current="true"]')).toHaveCount(1)
  const after = (await chip.boundingBox())!
  expect(after.width).toBe(before.width)
  expect(after.height).toBe(before.height)
})

test('scratch chip reaches the scratch runner', async ({ page }) => {
  const id = await createInstance()
  for (let i = 1; i <= 8; i++) await saveSection(id, `sec${i}`, `v${i} = ${i}`)
  await page.goto(`${rt.viteURL}/instances/${id}`)
  const dir = page.locator('.nb-directory')
  // Set ?section= first so the chip's param cleanup is observable.
  await dir.locator('.nb-dir-row', { hasText: 'sec3' }).click()
  await expect(page).toHaveURL(/section=sec3/)
  await page.evaluate(() => window.scrollTo(0, 0))
  const chip = dir.locator('.nb-dir-scratch')
  await expect(chip).toBeVisible()
  await chip.click()
  await expect(page).not.toHaveURL(/section=/)
  const scratch = page.locator('#run-code')
  await expect(scratch).toBeInViewport()
  const sBox = (await scratch.boundingBox())!
  const dBox = (await dir.boundingBox())!
  expect(sBox.y).toBeGreaterThanOrEqual(dBox.y + dBox.height)
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
  // No report — the status line carries the block AND the s1 cell note
  // repeats it (P5); both are the same runBlock message.
  await expect(page.locator('.nb-status')).toContainText('notebook changed while preparing the run', { timeout: 20_000 })
  await expect(c.locator('.nb-note')).toContainText('notebook changed while preparing the run')
  await page.unroute('**/notebook/run')
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
  await expect(page.locator('.nb-status')).toContainText('changed on the server', { timeout: 20_000 })
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
  // The status line summarizes the restore; the full report — every
  // selected row's honest status — lives behind Details.
  await expect(page.locator('.nb-status')).toContainText('earlier sections in this batch did execute', { timeout: 20_000 })
  await page.locator('.nb-toolbar').getByRole('button', { name: 'Details' }).click()
  const dlg = page.getByRole('dialog', { name: 'Notebook status' })
  const report = dlg.locator('.nb-batch')
  await expect(report).toContainText('s1')
  await expect(report).toContainText('ok')
  await expect(report).toContainText('not_run')
  await expect(report).toContainText('do not blindly retry')
  // Closing the modal leaves the warning on the status line — the old
  // dismiss buttons are gone; the next run replaces the state instead.
  await dlg.getByRole('button', { name: 'Close' }).click()
  await expect(dlg).toHaveCount(0)
  await expect(page.locator('.nb-status')).toContainText('earlier sections in this batch did execute')
  await page.unroute('**/notebook/run')
})

test('shortcut: ⌘/Ctrl+Enter in a section editor runs that section, never scratch', async ({ page }) => {
  const id = await createInstance()
  await saveSection(id, 's1', 'x = 1')
  await page.goto(`${rt.viteURL}/instances/${id}`)
  const c = card(page, 's1')
  await expect(c).toBeVisible()
  await expect(c.getByText('Saved', { exact: true })).toBeVisible({ timeout: 15_000 })

  let execPosts = 0
  let runPosts = 0
  page.on('request', (r) => {
    if (r.method() !== 'POST') return
    if (/\/instances\/[^/]+\/exec$/.test(r.url())) execPosts++
    if (r.url().endsWith('/notebook/run')) runPosts++
  })
  // Inside a SECTION editor the chord runs THAT section — one
  // /notebook/run POST and never a scratch /exec (P1).
  await c.locator('.cm-content').click()
  await page.keyboard.press('Control+Enter')
  await expect.poll(() => runPosts, { timeout: 20_000 }).toBe(1)
  await expect(page.locator('.nb-status')).toContainText('s1 · ok', { timeout: 20_000 })
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

test('S3: run feedback never moves the cell', async ({ page }) => {
  const id = await createInstance()
  await saveSection(id, 'first', 'a = 1')
  await saveSection(id, 'second', 'b = 2')
  await page.goto(`${rt.viteURL}/instances/${id}`)
  const c2 = card(page, 'second')
  await expect(c2).toBeVisible()
  await expect(c2.getByText('Saved', { exact: true })).toBeVisible({ timeout: 15_000 })

  const runBtn = c2.getByRole('button', { name: 'Run section' })
  const toolbar = page.locator('.nb-toolbar')
  const before = {
    btn: await runBtn.boundingBox(),
    bar: await toolbar.boundingBox(),
    scroll: await page.evaluate(() => window.scrollY),
  }
  expect(before.btn).not.toBeNull()
  expect(before.bar).not.toBeNull()

  await runBtn.click()
  // The status line updates in place; the second cell's run control and
  // the toolbar keep their exact boxes (S3 — no banner insertion).
  await expect(page.locator('.nb-status')).toContainText('second · ok', { timeout: 20_000 })
  expect(await runBtn.boundingBox()).toEqual(before.btn)
  expect(await toolbar.boundingBox()).toEqual(before.bar)
  expect(await page.evaluate(() => window.scrollY)).toBe(before.scroll)
})

test('S1: cell controls keep their box under hover', async ({ page }) => {
  const id = await createInstance()
  await saveSection(id, 's1', 'x = 1')
  await page.goto(`${rt.viteURL}/instances/${id}`)
  const c = card(page, 's1')
  await expect(c).toBeVisible()
  await expect(c.getByText('Saved', { exact: true })).toBeVisible({ timeout: 15_000 })

  // Gutter run, the ⋯ menu trigger and the inserter after the cell —
  // hover states repaint only, geometry is identical (S1).
  const targets = [
    c.getByRole('button', { name: 'Run section' }),
    c.getByRole('button', { name: 'Section s1 actions' }),
    page.getByRole('button', { name: 'Insert section after s1' }),
  ]
  for (const t of targets) {
    const box = await t.boundingBox()
    expect(box).not.toBeNull()
    await t.hover()
    await page.waitForTimeout(250)
    expect(await t.boundingBox()).toEqual(box)
  }
})

test('once toggle lives in the cell menu', async ({ page }) => {
  const id = await createInstance()
  await saveSection(id, 'hook', 'h = 1')
  await page.goto(`${rt.viteURL}/instances/${id}`)
  const c = card(page, 'hook')
  await expect(c).toBeVisible()
  await expect(c.getByText('Saved', { exact: true })).toBeVisible({ timeout: 15_000 })
  await expect(c.locator('.nb-once')).toHaveCount(0)

  await c.getByRole('button', { name: 'Section hook actions' }).click()
  await page.getByRole('menuitem', { name: 'Mark as run-once' }).click()
  // The once tag appears in the header and the server meta flips.
  await expect(c.locator('.nb-once')).toHaveText('once', { timeout: 15_000 })
  await expect.poll(async () => {
    const r = await rt.client.get(`/api/v1/instances/${id}/notebook`)
    const nb = await r.json()
    return nb.notebook.sections.find((s: { name: string }) => s.name === 'hook')?.rerun
  }, { timeout: 15_000 }).toBe('once')
})

test('screenshots: notebook desktop/mobile × light/dark', async ({ page }) => {
  const beforePids = instanceChildPids()
  const id = await createInstance()
  await saveSection(id, 'setup', 'x = 40 + 2')
  await saveSection(id, 'load', 'rows = [i * i for i in range(12)]\nlabels = {"a": 1, "b": 2}')
  await saveSection(id, 'transform', 'def twice(v):\n    return v * 2\nvals = [twice(i) for i in range(8)]')
  await saveSection(id, 'analyze', 'total = 0\nfor v in vals:\n    total += v\nprint("total", total)')
  await saveSection(id, 'report', 'print("answer", x)', { rerun: 'once' })
  await saveSection(id, 'notes', '# closing remarks\n# the notebook is the durable record')
  const run = await runAll(id)
  const results = run.report?.results ?? []
  expect(results).toHaveLength(6)
  expect(results.every((s: { status: string }) => s.status === 'ok')).toBe(true)
  await page.goto(`${rt.viteURL}/instances/${id}`)
  await expect(card(page, 'setup')).toBeVisible()
  await expect(card(page, 'report')).toBeVisible()
  await expect(page.locator('.nb-directory .nb-dir-row:not(.nb-dir-scratch)')).toHaveCount(6)
  for (const scheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme: scheme })
    // The previous iteration's mobile shots leave the page scrolled.
    await page.evaluate(() => window.scrollTo(0, 0))
    await page.waitForTimeout(150)
    await page.screenshot({ path: join(SHOTS, `notebook-desktop-${scheme}.png`), fullPage: false })
    // Sticky toolbar pinned under the console header, overlapping cells.
    await card(page, 'notes').scrollIntoViewIfNeeded()
    await page.waitForTimeout(300)
    // Pinned exactly under the 56px header + 1px border — never hidden.
    const bar = await page.locator('.nb-toolbar').boundingBox()
    expect(bar).not.toBeNull()
    expect(Math.round(bar!.y)).toBe(57)
    // The two-row head paints above the cells scrolling beneath it and
    // is opaque; the toolbar keeps its 1px bottom edge as row 1.
    const barBox = bar!
    const topmost = await page.evaluate(
      ([x, y]) => document.elementFromPoint(x, y)?.closest('.nb-head') != null,
      [barBox.x + barBox.width / 2, barBox.y + barBox.height / 2],
    )
    expect(topmost).toBe(true)
    const headBg = await page.locator('.nb-head').evaluate((el) => getComputedStyle(el).backgroundColor)
    expect(headBg).not.toBe('rgba(0, 0, 0, 0)')
    const barBw = await page.locator('.nb-toolbar').evaluate((el) => getComputedStyle(el).borderBottomWidth)
    expect(barBw).toBe('1px')
    // --nb-bar-h equals the measured two-row head so the cards'
    // scroll-margin clears it exactly (S3).
    const deskHead = (await page.locator('.nb-head').boundingBox())!
    const deskBarH = await page.locator('.nb').evaluate(
      (el) => parseFloat(getComputedStyle(el).getPropertyValue('--nb-bar-h')),
    )
    expect(Math.round(deskHead.height)).toBe(Math.round(deskBarH))
    // No horizontal overflow at desktop width either.
    const deskOverflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
    expect(deskOverflow).toBeLessThanOrEqual(1)
    await page.screenshot({ path: join(SHOTS, `notebook-desktop-${scheme}-sticky.png`), fullPage: false })
    await page.evaluate(() => window.scrollTo(0, 0))
    // Details modal open over the run report.
    await page.locator('.nb-toolbar').getByRole('button', { name: 'Run all' }).click()
    await expect(page.locator('.nb-status')).toContainText('ran', { timeout: 20_000 })
    await page.locator('.nb-toolbar').getByRole('button', { name: 'Details' }).click()
    const dialog = page.getByRole('dialog', { name: 'Notebook status' })
    await expect(dialog).toBeVisible()
    // The dialog animates in (material-rise) — wait for it to settle so
    // the screenshot is not a mid-flight frame.
    await expect(dialog).toHaveCSS('opacity', '1')
    await page.screenshot({ path: join(SHOTS, `notebook-desktop-${scheme}-details.png`), fullPage: false })
    await page.keyboard.press('Escape')
    await page.setViewportSize({ width: 390, height: 844 })
    await expect(page.locator('.nb')).toBeVisible()
    // --nb-bar-h must equal the measured sticky head so the cards'
    // scroll-margin clears both rows at mobile width too (S3).
    const mobHead = (await page.locator('.nb-head').boundingBox())!
    const mobBarH = await page.locator('.nb').evaluate(
      (el) => parseFloat(getComputedStyle(el).getPropertyValue('--nb-bar-h')),
    )
    expect(Math.round(mobHead.height)).toBe(Math.round(mobBarH))
    // No horizontal overflow at mobile width.
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
    expect(overflow).toBeLessThanOrEqual(1)
    // The gutter run control is top-aligned with its cell, not stretched
    // to mid-cell: its top sits within 16px of the card's top edge.
    const setupCard = await card(page, 'setup').boundingBox()
    const setupRun = await card(page, 'setup').getByRole('button', { name: 'Run section' }).boundingBox()
    expect(setupCard).not.toBeNull()
    expect(setupRun).not.toBeNull()
    const gutterOffset = setupRun!.y - setupCard!.y
    expect(gutterOffset).toBeGreaterThanOrEqual(0)
    expect(gutterOffset).toBeLessThanOrEqual(16)
    // Viewport top — not a tiled fullPage capture.
    await page.screenshot({ path: join(SHOTS, `notebook-mobile-${scheme}.png`), fullPage: false })
    // A scrolled section card and the scratch area as separate shots.
    await card(page, 'report').scrollIntoViewIfNeeded()
    await page.screenshot({ path: join(SHOTS, `notebook-mobile-${scheme}-section.png`), fullPage: false })
    await page.locator('#run-code').scrollIntoViewIfNeeded()
    await page.screenshot({ path: join(SHOTS, `notebook-mobile-${scheme}-scratch.png`), fullPage: false })
    await page.setViewportSize({ width: 1440, height: 1000 })
  }

  // S4: the wrapped mobile toolbar keeps one height whether the status
  // line is short or clamped at two lines — the status box is reserved.
  await page.setViewportSize({ width: 390, height: 844 })
  await expect(page.locator('.nb')).toBeVisible()
  const barShort = (await page.locator('.nb-toolbar').boundingBox())!.height
  expect((await page.locator('.nb-status').boundingBox())!.height).toBe(36)
  // Killing the child makes the next run hit the restore path — the
  // request does not execute and the status line carries the longest
  // warning text it ever shows (a lost-status wait would not work: the
  // batch report outranks `lost` in the status priority, and the API
  // only marks the instance lost when a request probes the dead child).
  const mine = instanceChildPids().filter((p) => !beforePids.includes(p))
  expect(mine.length).toBe(1)
  process.kill(mine[0], 'SIGKILL')
  await page.waitForTimeout(1200)
  await page.locator('.nb-toolbar').getByRole('button', { name: 'Run all' }).click()
  await expect(page.locator('.nb-status')).toContainText('Runtime restored', { timeout: 30_000 })
  const barLong = (await page.locator('.nb-toolbar').boundingBox())!.height
  expect(Math.abs(barLong - barShort)).toBeLessThanOrEqual(1)
})
