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
  // Scoped to the code cards — graph nodes carry data-section too.
  return page.locator(`.nb-cards [data-section="${name}"]`)
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
    // The dry-run preview is not a run — count only the real POST.
    if (r.method() === 'POST' && r.url().endsWith('/notebook/run') && !r.postDataJSON()?.dry_run) runPosts++
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
  // The scoped status text must not collide with other page copy.
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

test('S3: section directory stays visible while a long notebook scrolls', async ({ page }) => {
  const id = await createInstance()
  for (let i = 1; i <= 8; i++) await saveSection(id, `sec${i}`, `v${i} = ${i}`)
  await page.goto(`${rt.viteURL}/instances/${id}`)
  const dir = page.locator('.nb-directory')
  const head = page.locator('.nb-head')
  await expect(dir).toBeVisible()
  // 8 section chips — there is no scratch chip anymore.
  await expect(dir.locator('.nb-dir-row')).toHaveCount(8)
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

test('no scratch affordance: every run belongs to a section', async ({ page }) => {
  const id = await createInstance()
  await saveSection(id, 's1', 'x = 1')
  await page.goto(`${rt.viteURL}/instances/${id}`)
  await expect(card(page, 's1')).toBeVisible()
  // The scratch runner, its directory chip and its promote flow are gone.
  await expect(page.locator('#run-code')).toHaveCount(0)
  await expect(page.locator('.nb-dir-scratch')).toHaveCount(0)
  await expect(page.locator('.nb-directory .nb-dir-row')).toHaveCount(1)
  await expect(page.getByRole('button', { name: 'Save as section…' })).toHaveCount(0)
})

test('S1/S3: a chip click highlights immediately; a user scroll hands back to the spy', async ({ page }) => {
  const id = await createInstance()
  // Long enough to scroll; the LAST card cannot reach the top of the
  // viewport, so the scroll-spy alone could never mark its chip.
  for (let i = 1; i <= 8; i++) await saveSection(id, `sec${i}`, `v${i} = ${i}`)
  await page.goto(`${rt.viteURL}/instances/${id}`)
  const dir = page.locator('.nb-directory')
  const last = dir.locator('.nb-dir-row', { hasText: 'sec8' })
  await expect(last).toBeVisible()
  await last.click()
  await expect(page).toHaveURL(/section=sec8/)
  // The clicked chip is active at once — the spy does not get to
  // override it while the programmatic scroll settles.
  await expect(last).toHaveAttribute('aria-current', 'true')
  await expect(dir.locator('.nb-dir-row[aria-current="true"]')).toHaveCount(1)
  // The programmatic scroll settles inside the hold window; once the
  // user scrolls, the spy marks the topmost card again.
  await page.waitForTimeout(700)
  await page.mouse.move(720, 500)
  await page.mouse.wheel(0, -400)
  await expect.poll(async () => {
    const t = await dir.locator('.nb-dir-row[aria-current="true"]').textContent()
    return t ?? ''
  }, { timeout: 10_000 }).not.toContain('sec8')
})

test('S1: view-toggle segment boxes are identical active/inactive', async ({ page }) => {
  const id = await createInstance()
  await saveSection(id, 's1', 'x = 1')
  await page.goto(`${rt.viteURL}/instances/${id}`)
  const toggle = page.locator('.nb-view-toggle')
  const code = toggle.getByRole('button', { name: 'Code' })
  const graph = toggle.getByRole('button', { name: 'Graph' })
  // Segment boxes are measured RELATIVE to the toggle: the Code→Graph
  // switch legitimately scrolls the page to reveal the graph (S6), and
  // viewport-relative boxes would report that scroll, not a state
  // change. Inside the toggle, paint-only state can never move them.
  const rel = async (loc: import('@playwright/test').Locator) => {
    const [l, t] = await Promise.all([loc.boundingBox(), toggle.boundingBox()])
    return { x: l!.x - t!.x, y: l!.y - t!.y, width: l!.width, height: l!.height }
  }
  const codeBefore = await rel(code)
  const graphBefore = await rel(graph)
  await graph.click()
  await expect(page.locator('.nb-graph')).toBeVisible()
  // State is paint-only: both segment boxes are unchanged (S1).
  const codeAfter = await rel(code)
  const graphAfter = await rel(graph)
  for (const [a, b] of [[codeBefore, codeAfter], [graphBefore, graphAfter]] as const) {
    expect(a).toEqual(b)
  }
  await expect(graph).toHaveAttribute('aria-pressed', 'true')
  await expect(code).toHaveAttribute('aria-pressed', 'false')
})

// Computed border-*-radius returns "rx ry" for elliptical corners.
async function cornerRadius(loc: import('@playwright/test').Locator, corner: string) {
  return loc.evaluate((el, c) => {
    const v = getComputedStyle(el)[`border${c}Radius` as 'borderTopLeftRadius']
    const [rx, ry] = v.split(' ').map(parseFloat)
    return { rx, ry: Number.isFinite(ry) ? ry : rx }
  }, corner)
}

test('S3: toolbar spacing holds and nested radii are concentric', async ({ page }) => {
  const id = await createInstance()
  await saveSection(id, 's1', 'x = 1')
  await page.goto(`${rt.viteURL}/instances/${id}`)
  await expect(card(page, 's1')).toBeVisible()
  const head = page.locator('.nb-head')
  const toggle = page.locator('.nb-view-toggle')
  // Activate the first chip first — its click scrolls the page and the
  // sticky head moves, so every box below is measured in ONE state.
  const chip = page.locator('.nb-dir-row').first()
  await chip.click()
  await expect(chip).toHaveAttribute('aria-current', 'true')
  await page.waitForTimeout(150)
  const headBox = (await head.boundingBox())!
  const toggleBox = (await toggle.boundingBox())!
  const chipBox = (await chip.boundingBox())!
  const thumb = toggle.getByRole('button', { name: 'Code' })
  const thumbBox = (await thumb.boundingBox())!
  const firstBtn = page.locator('.nb-toolbar-actions [data-ui="button"]').first()
  const btnBox = (await firstBtn.boundingBox())!

  const near = (actual: number, expected: number, what: string) =>
    expect(Math.abs(actual - expected), what).toBeLessThanOrEqual(0.5)
  // Corner-near controls sit border+pad = 7px inside the panel edge on
  // BOTH axes; the toggle keeps ≥12px clear of the "Show N new" slot.
  near(chipBox.x - headBox.x, 7, 'chip left inset')
  near(headBox.y + headBox.height - (chipBox.y + chipBox.height), 7, 'chip bottom inset')
  near(headBox.x + headBox.width - (toggleBox.x + toggleBox.width), 7, 'toggle right inset')
  near(toggleBox.y - headBox.y, 7, 'toggle top inset')
  near(btnBox.x - headBox.x, 7, 'button left inset')
  near(btnBox.y - headBox.y, 7, 'button top inset')
  const revealBox = (await page.locator('.nb-toolbar-side > button').boundingBox())!
  expect(toggleBox.x - (revealBox.x + revealBox.width)).toBeGreaterThanOrEqual(12)

  // Concentric AND circular (AGENTS.md): the gap must be equal on both
  // axes, the radius circular, and inner = outer − gap.
  const assertConcentric = async (
    inner: import('@playwright/test').Locator, outer: import('@playwright/test').Locator,
    corner: string, gapX: number, gapY: number, label: string,
  ) => {
    near(gapX, gapY, `${label}: gap equal on both axes`)
    const o = await cornerRadius(outer, corner)
    const i = await cornerRadius(inner, corner)
    near(i.rx, i.ry, `${label}: radius is circular`)
    near(i.rx, Math.max(0, o.rx - (gapX + gapY) / 2), `${label}: inner = outer − gap`)
  }
  await assertConcentric(chip, head, 'BottomLeft',
    chipBox.x - headBox.x, headBox.y + headBox.height - (chipBox.y + chipBox.height), 'head→chip')
  await assertConcentric(toggle, head, 'TopRight',
    headBox.x + headBox.width - (toggleBox.x + toggleBox.width), toggleBox.y - headBox.y, 'head→track')
  await assertConcentric(firstBtn, head, 'TopLeft',
    btnBox.x - headBox.x, btnBox.y - headBox.y, 'head→button')
  await assertConcentric(thumb, toggle, 'TopLeft',
    thumbBox.x - toggleBox.x, thumbBox.y - toggleBox.y, 'track→thumb')
})

test('S3: the floating head box is constant across scroll, view switch and a run', async ({ page }) => {
  const id = await createInstance()
  for (let i = 1; i <= 8; i++) await saveSection(id, `sec${i}`, `v${i} = ${i}`)
  await page.goto(`${rt.viteURL}/instances/${id}`)
  const head = page.locator('.nb-head')
  // Wait for the notebook to lay out — scrolling earlier clamps to a
  // still-short page and the sticky head never engages.
  await expect(head).toBeVisible()
  await expect(card(page, 'sec8')).toBeVisible()
  const box = async () => {
    const b = (await head.boundingBox())!
    return [Math.round(b.x), Math.round(b.y), Math.round(b.width), Math.round(b.height)]
  }
  const scrollTop = () => page.evaluate(() => window.scrollTo({ top: 0, behavior: 'instant' }))
  await scrollTop()
  await page.waitForTimeout(200)
  const top = await box()

  // Stuck state is what must hold still — scroll near the bottom, then
  // to the very bottom (instant: the app's smooth scroll-behavior would
  // otherwise animate the head mid-measurement).
  const maxScroll = await page.evaluate(() => document.documentElement.scrollHeight - window.innerHeight)
  await page.evaluate((y) => window.scrollTo({ top: y, behavior: 'instant' }), maxScroll - 120)
  await page.waitForTimeout(300)
  const stuck = await box()
  expect(stuck[1]).toBe(65) // actually pinned, not still in flow
  await page.evaluate(() => window.scrollTo({ top: document.documentElement.scrollHeight, behavior: 'instant' }))
  await page.waitForTimeout(300)
  expect(await box()).toEqual(stuck)
  // A run repaints statuses inside cells — the stuck head never moves.
  await page.locator('.nb-toolbar').getByRole('button', { name: 'Run all' }).click()
  await expect(page.locator('.nb-status')).toContainText('ran', { timeout: 30_000 })
  expect(await box()).toEqual(stuck)
  // View switch at the same scroll offset: the panel's box is identical
  // in Code and Graph (the graph page is shorter, so re-pin scroll top).
  await scrollTop()
  await page.waitForTimeout(200)
  expect(await box()).toEqual(top)
  await openGraph(page)
  await scrollTop()
  await page.waitForTimeout(200)
  expect(await box()).toEqual(top)
  await page.locator('.nb-view-toggle').getByRole('button', { name: 'Code' }).click()
  await expect(page.locator('.nb-code')).toBeVisible()
  await page.waitForTimeout(200)
  expect(await box()).toEqual(top)
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
  // execute nothing. The dry-run preview passes through untouched —
  // the edit lands only on the real run request.
  await page.route('**/notebook/run', async (route) => {
    if (route.request().postDataJSON()?.dry_run) {
      await route.continue()
      return
    }
    await saveSection(id, 's1', 'x = 999')
    await route.continue()
  })
  await c.getByRole('button', { name: 'Run section' }).click()
  // No report — the status line carries the block AND the s1 cell note
  // repeats it (P5); both are the same runBlock message.
  await expect(page.locator('.nb-status')).toContainText('notebook changed while preparing the run', { timeout: 20_000 })
  await expect(c.locator('.nb-note')).toContainText('notebook changed while preparing the run')
  await page.unroute('**/notebook/run')
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
    if (r.method() === 'POST' && r.url().endsWith('/notebook/run') && !r.postDataJSON()?.dry_run) runPosts++
  })
  await c.getByRole('button', { name: 'Run section' }).click()
  await expect(page.locator('.nb-status')).toContainText('changed on the server', { timeout: 20_000 })
  // The dry-run preview went out but no real run request was ever sent.
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
  // lost after earlier sections already executed. The dry-run preview
  // gets the plan shape; the real POST gets the restore report.
  await page.route('**/notebook/run', async (route) => {
    const dry = route.request().postDataJSON()?.dry_run === true
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        report: {
          instance_id: id, incarnation: 2, revision: 3, drift: 0,
          restored: !dry, stopped_reason: dry ? undefined : 'instance_restored',
          results: dry
            ? [
                { name: 's1', status: 'planned', reason: 'target' },
              ]
            : [
                { name: 's1', status: 'ok', exec_id: 'exe_1', duration_ms: 4 },
                { name: 's2', status: 'not_run' },
              ],
          warnings: dry ? [] : ['instance was restored during the run: earlier sections executed in the previous runtime; do not blindly retry side effects'],
        },
      }),
    })
  })
  await page.getByRole('button', { name: 'Run all' }).click()
  // The status line carries the restore warning AND every per-section
  // row in its title — there is no Details modal any more.
  await expect(page.locator('.nb-status')).toContainText('earlier sections in this batch did execute', { timeout: 20_000 })
  const title = await page.locator('.nb-status').getAttribute('title')
  expect(title).toContain('s1 · ok')
  expect(title).toContain('s2 · not_run')
  expect(title).toContain('do not blindly retry')
  await page.unroute('**/notebook/run')
})

test('shortcut: ⌘/Ctrl+Enter in a section editor runs that section', async ({ page }) => {
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
    if (r.url().endsWith('/notebook/run') && !r.postDataJSON()?.dry_run) runPosts++
  })
  // Inside a SECTION editor the chord runs THAT section — notebook/run
  // POSTs only, never a scratch /exec (P1).
  await c.locator('.cm-content').click()
  await page.keyboard.press('Control+Enter')
  await expect.poll(() => runPosts, { timeout: 20_000 }).toBe(1)
  await expect(page.locator('.nb-status')).toContainText('s1 · ok', { timeout: 20_000 })
  await page.waitForTimeout(400)
  expect(execPosts).toBe(0)
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
  await expect(page.locator('.nb-directory .nb-dir-row')).toHaveCount(6)
  for (const scheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme: scheme })
    // The previous iteration's mobile shots leave the page scrolled.
    await page.evaluate(() => window.scrollTo(0, 0))
    await page.waitForTimeout(150)
    await page.screenshot({ path: join(SHOTS, `notebook-desktop-${scheme}.png`), fullPage: false })
    // Sticky toolbar pinned under the console header, overlapping cells.
    await card(page, 'notes').scrollIntoViewIfNeeded()
    await page.waitForTimeout(300)
    // The floating head is pinned 8px under the 57px console header —
    // the toolbar sits inside it behind the panel's padding (S3).
    const headBox = (await page.locator('.nb-head').boundingBox())!
    const bar = await page.locator('.nb-toolbar').boundingBox()
    expect(bar).not.toBeNull()
    expect(Math.round(bar!.y - headBox.y)).toBe(7) // 1px border + 6px padding
    // The panel paints above the cells scrolling beneath it and is
    // opaque — including the ::before mask over the header gap.
    const topmost = await page.evaluate(
      ([x, y]) => document.elementFromPoint(x, y)?.closest('.nb-head') != null,
      [bar!.x + bar!.width / 2, bar!.y + bar!.height / 2],
    )
    expect(topmost).toBe(true)
    const headBg = await page.locator('.nb-head').evaluate((el) => getComputedStyle(el).backgroundColor)
    expect(headBg).not.toBe('rgba(0, 0, 0, 0)')
    const maskBg = await page.locator('.nb-head').evaluate((el) => getComputedStyle(el, '::before').backgroundColor)
    expect(maskBg).not.toBe('rgba(0, 0, 0, 0)')
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
    // Run feedback lives on the one-line status; its title carries the
    // full per-section report — no Details modal exists.
    await page.locator('.nb-toolbar').getByRole('button', { name: 'Run all' }).click()
    await expect(page.locator('.nb-status')).toContainText('ran', { timeout: 20_000 })
    const runTitle = await page.locator('.nb-status').getAttribute('title')
    expect(runTitle).toContain('setup')
    expect(runTitle).toContain('ok')
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
    // A scrolled section card as the second shot.
    await card(page, 'report').scrollIntoViewIfNeeded()
    await page.screenshot({ path: join(SHOTS, `notebook-mobile-${scheme}-section.png`), fullPage: false })
    await card(page, 'notes').scrollIntoViewIfNeeded()
    await page.screenshot({ path: join(SHOTS, `notebook-mobile-${scheme}-notes.png`), fullPage: false })
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

// --- dependency graph view (EP-0023) ----------------------------------------

const graphNode = (page: import('@playwright/test').Page, name: string) =>
  page.locator(`[data-testid="nb-graph-node"][data-section="${name}"]`)

const graphEdge = (page: import('@playwright/test').Page, from: string, to: string) =>
  page.locator(`.react-flow__edge[data-id="${from}→${to}"]`)

// The React Flow viewport transform — translate + scale as one string.
const viewportTransform = (page: import('@playwright/test').Page) =>
  page.locator('.react-flow__viewport').evaluate((el) => (el as HTMLElement).style.transform)

const viewportScale = async (page: import('@playwright/test').Page) => {
  const t = await viewportTransform(page)
  const m = /scale\(([-\d.]+)\)/.exec(t)
  return m ? +m[1] : NaN
}

async function openGraph(page: import('@playwright/test').Page) {
  await page.locator('.nb-view-toggle').getByRole('button', { name: 'Graph' }).click()
  await expect(page.locator('.nb-graph')).toBeVisible()
}

// Node positions in FLOW coordinates, parsed from each React Flow
// node's translate() — viewport pans/zooms can never masquerade as a
// node move, and paint-only updates keep these identical (S1/S5).
async function nodeBoxes(page: import('@playwright/test').Page) {
  return page.locator('.react-flow__node').evaluateAll((els) =>
    els.map((el) => {
      const id = el.getAttribute('data-id') ?? ''
      const m = /translate\(\s*(-?[\d.]+)px\s*,\s*(-?[\d.]+)px/.exec((el as HTMLElement).style.transform)
      return { id, x: m ? +m[1] : NaN, y: m ? +m[2] : NaN }
    }).sort((a, b) => a.id.localeCompare(b.id))
  )
}

// Every node's SCREEN box inside the canvas box — the first-reveal fit
// must already have landed (fitView is the only viewport move allowed
// without a user gesture).
async function allNodesInsideCanvas(page: import('@playwright/test').Page) {
  const cb = (await page.locator('.nb-graph-canvas').boundingBox())!
  const boxes = await page.locator('.react-flow__node').evaluateAll((els) =>
    els.map((el) => {
      const r = el.getBoundingClientRect()
      return { x: r.x, y: r.y, r: r.right, b: r.bottom }
    })
  )
  return boxes.every((b) =>
    b.x >= cb.x - 1 && b.y >= cb.y - 1 &&
    b.r <= cb.x + cb.width + 1 && b.b <= cb.y + cb.height + 1
  )
}

// Wait until the canvas reports a completed fitView — viewport
// assertions must not race the first-reveal fit.
const graphFitted = (page: import('@playwright/test').Page) =>
  expect(page.locator('.nb-graph-canvas')).toHaveAttribute('data-fitted', '1')

test('graph: provider→consumer edge in reverse page order + duplicate issue', async ({ page }) => {
  const id = await createInstance()
  // The consumer is declared BEFORE its provider — page order is not
  // dependency order; the edge still points maker → reader.
  await saveSection(id, 'reader', 'total = price * 2')
  await saveSection(id, 'maker', 'price = 10')
  // The duplicated name must be READ to be a diagnostic — unread
  // duplicates (loop vars, temporaries) are allowed.
  await saveSection(id, 'dup_a', 'dup = 1')
  await saveSection(id, 'dup_b', 'dup = 2')
  await saveSection(id, 'dup_user', 'show = dup + 1')
  await page.goto(`${rt.viteURL}/instances/${id}`)
  await expect(card(page, 'reader')).toBeVisible()
  await openGraph(page)

  await expect(page.locator('.react-flow__node')).toHaveCount(5)
  // One resolved provider→consumer link — the edge id carries the
  // direction (from→to) and its label names the variable.
  await expect(page.locator('.react-flow__edge')).toHaveCount(1)
  const link = graphEdge(page, 'maker', 'reader')
  await expect(link).toBeVisible()
  await expect(link).toContainText('price')

  // The aside lists the duplicate definition with its sections, tags it
  // as run-blocking, and the header slot carries the execution note.
  await expect(page.locator('.nb-graph-aside')).toContainText('Graph issues (1)')
  await expect(page.locator('.nb-graph-aside')).toContainText('dup is defined by dup_a, dup_b')
  await expect(page.locator('.nb-graph-aside')).toContainText('blocks runs')
  await expect(page.locator('.nb-graph-note')).toContainText('Running a section first runs its upstream sections that are not fresh.')
})

test('graph C2: a dirty draft survives Code→Graph→Code', async ({ page }) => {
  const id = await createInstance()
  await saveSection(id, 'draft', 'x = 1')
  await page.goto(`${rt.viteURL}/instances/${id}`)
  const c = card(page, 'draft')
  await expect(c.locator('.cm-content')).toBeVisible()
  await c.locator('.cm-content').click()
  await page.keyboard.press('Meta+A')
  await page.keyboard.insertText('x = 999')
  // The draft is genuinely unsaved when the view flips — the round trip
  // completes well inside the autosave debounce.
  await expect(c.getByText('Unsaved', { exact: true })).toBeVisible({ timeout: 15_000 })

  await openGraph(page)
  await expect(page.locator('.nb-code')).toBeHidden()
  await page.locator('.nb-view-toggle').getByRole('button', { name: 'Code' }).click()
  await expect(page.locator('.nb-code')).toBeVisible()
  // Same mounted editor — the unsaved text is still there.
  await expect(c.locator('.cm-content')).toContainText('x = 999')
})

test('graph: a polled structure re-lays out live; the viewport never moves', async ({ page }) => {
  const id = await createInstance()
  await saveSection(id, 'aaa', 'a = 1')
  await saveSection(id, 'bbb', 'b = a + 1')
  await page.goto(`${rt.viteURL}/instances/${id}`)
  await openGraph(page)
  await expect(page.locator('.nb-graph-node')).toHaveCount(2)
  await graphFitted(page)
  const before = await nodeBoxes(page)
  const vpBefore = await viewportTransform(page)

  // A server-side write lands on the next poll — no user action: the
  // graph follows it live. ccc joins bbb's rank, so the deterministic
  // re-layout really does move an existing node.
  await saveSection(id, 'ccc', 'c = a + 1')
  await expect(page.locator('.nb-graph-node')).toHaveCount(3, { timeout: 20_000 })
  await expect(graphEdge(page, 'aaa', 'ccc')).toBeVisible({ timeout: 10_000 })
  // Let the ~250ms tween finish, then the boxes are the new layout and
  // stable — and at least one existing node actually moved.
  await page.waitForTimeout(500)
  const settled = await nodeBoxes(page)
  await page.waitForTimeout(300)
  expect(await nodeBoxes(page)).toEqual(settled)
  // The pre-existing nodes really did re-lay out (ccc joined bbb's
  // rank), not just appended.
  const shared = settled.filter((n) => before.some((b) => b.id === n.id))
  expect(shared).not.toEqual(before)
  const pos = Object.fromEntries(settled.map((n) => [n.id, n]))
  // aaa stays left of the rank-1 pair; bbb and ccc share that rank.
  expect(pos['aaa'].x).toBeLessThan(pos['bbb'].x)
  expect(pos['aaa'].x).toBeLessThan(pos['ccc'].x)
  expect(Math.abs(pos['bbb'].x - pos['ccc'].x)).toBeLessThan(1)
  expect(pos['bbb'].y).not.toBe(pos['ccc'].y)
  // Polls never touch the viewport (S5).
  expect(await viewportTransform(page)).toBe(vpBefore)
})

test('graph: deleting a section removes its node and clears the selection', async ({ page }) => {
  const id = await createInstance()
  await saveSection(id, 'del_a', 'a = 1')
  await saveSection(id, 'del_b', 'b = a + 1')
  await saveSection(id, 'keep', 'k = 1')
  await page.goto(`${rt.viteURL}/instances/${id}`)
  await openGraph(page)
  await expect(page.locator('.nb-graph-node')).toHaveCount(3)
  await graphNode(page, 'del_b').click()
  await expect(page).toHaveURL(/section=del_b/)
  await expect(graphNode(page, 'del_b')).toHaveAttribute('aria-pressed', 'true')

  const rev = await notebookRevision(id)
  const dr = await rt.client.delete(`/api/v1/instances/${id}/sections/del_b?revision=${rev}`)
  expect(dr.status()).toBe(200)
  // The node fades out and unmounts on the next poll; the vanished
  // selection clears ?section= so the aside falls back to issues.
  await expect(page.locator('.nb-graph-node')).toHaveCount(2, { timeout: 20_000 })
  await expect(graphNode(page, 'del_b')).toHaveCount(0)
  await expect(page).not.toHaveURL(/section=/, { timeout: 10_000 })
  await expect(graphNode(page, 'keep')).toBeVisible()
})

test('graph: a state-only poll repaints without moving nodes or viewport', async ({ page }) => {
  const id = await createInstance()
  await saveSection(id, 'st_a', 'a = 1')
  await saveSection(id, 'st_b', 'b = a + 1')
  await page.goto(`${rt.viteURL}/instances/${id}`)
  await openGraph(page)
  await expect(graphNode(page, 'st_a')).toHaveAttribute('data-state', 'not_run')
  await graphFitted(page)
  const before = await nodeBoxes(page)
  const vpBefore = await viewportTransform(page)

  // Run via the API — the next poll only changes state metadata: the
  // deterministic layout is identical, so no tween runs and nothing
  // geometric changes (S5).
  const rr = await rt.client.post(`/api/v1/instances/${id}/notebook/run`, { data: { sections: 'all' } })
  expect(rr.status()).toBe(200)
  await expect(graphNode(page, 'st_a')).toHaveAttribute('data-state', 'fresh', { timeout: 20_000 })
  await expect(graphNode(page, 'st_b')).toHaveAttribute('data-state', 'fresh', { timeout: 20_000 })
  expect(await nodeBoxes(page)).toEqual(before)
  expect(await viewportTransform(page)).toBe(vpBefore)
})

test('graph: reduced motion applies a re-layout with no intermediate frames', async ({ browser }) => {
  const ctx = await browser.newContext({ reducedMotion: 'reduce', viewport: { width: 1280, height: 720 } })
  const page = await ctx.newPage()
  const id = await createInstance()
  await saveSection(id, 'rm_a', 'a = 1')
  await saveSection(id, 'rm_b', 'b = a + 1')
  await page.goto(`${rt.viteURL}/instances/${id}`)
  await openGraph(page)
  await expect(page.locator('.nb-graph-node')).toHaveCount(2)
  await graphFitted(page)

  await saveSection(id, 'rm_c', 'c = b + 1')
  await expect(page.locator('.nb-graph-node')).toHaveCount(3, { timeout: 20_000 })
  // No tween: the first observable pose is already the final layout —
  // a sample now and after the tween window must be identical.
  const immediate = await nodeBoxes(page)
  await page.waitForTimeout(350)
  expect(await nodeBoxes(page)).toEqual(immediate)
  await ctx.close()
})

test('graph: ?nbview=graph deep link and reload render nodes fitted — never a false empty state', async ({ page }) => {
  const id = await createInstance()
  await saveSection(id, 'deep_a', 'a = 1')
  await saveSection(id, 'deep_b', 'b = a + 1')
  // Direct deep link into graph view (regression: the held snapshot
  // used to freeze the pre-load EMPTY structure here and the graph
  // stayed "No sections yet" forever).
  await page.goto(`${rt.viteURL}/instances/${id}?nbview=graph`)
  await expect(page.locator('.nb-graph')).toBeVisible()
  await expect(page.locator('.nb-graph-node')).toHaveCount(2)
  await graphFitted(page)
  await expect(page.locator('.nb-graph-empty')).toHaveCount(0)
  await expect.poll(() => allNodesInsideCanvas(page), { timeout: 10_000 }).toBe(true)

  // Reload on the same URL — same expectation.
  await page.reload()
  await expect(page.locator('.nb-graph-node')).toHaveCount(2, { timeout: 20_000 })
  await graphFitted(page)
  await expect(page.locator('.nb-graph-empty')).toHaveCount(0)
})

test('graph S1: selecting a node never moves a node', async ({ page }) => {
  const id = await createInstance()
  await saveSection(id, 'prov', 'v = 1')
  await saveSection(id, 'cons', 'w = v + 1')
  await page.goto(`${rt.viteURL}/instances/${id}`)
  await openGraph(page)
  await graphFitted(page)
  const before = await nodeBoxes(page)
  const vpBefore = await viewportTransform(page)

  await graphNode(page, 'cons').click()
  await expect(graphNode(page, 'cons')).toHaveAttribute('aria-pressed', 'true')
  // Give any (incorrect) selection pan a chance to run before asserting.
  await page.waitForTimeout(300)
  // Selection is paint-only: every node keeps its exact box and the
  // viewport never moves for a direct node click (S1).
  expect(await nodeBoxes(page)).toEqual(before)
  expect(await viewportTransform(page)).toBe(vpBefore)
  // Ancestor/descendant highlight applied without geometry changes.
  await expect(graphNode(page, 'prov')).toHaveClass(/is-hi/)
  await expect(page.locator('.nb-graph-aside')).toContainText('Reads')
})

test('graph: chip and node selection stay in sync', async ({ page }) => {
  const id = await createInstance()
  await saveSection(id, 'prov', 'v = 1')
  await saveSection(id, 'cons', 'w = v + 1')
  await page.goto(`${rt.viteURL}/instances/${id}`)
  await openGraph(page)
  const dir = page.locator('.nb-directory')
  const provChip = dir.locator('.nb-dir-row', { hasText: 'prov' })
  const consChip = dir.locator('.nb-dir-row', { hasText: 'cons' })

  // Chip → node: the click selects the node (pressed + aside) and the
  // chip itself carries the active marker — one active-section concept.
  await provChip.click()
  await expect(page).toHaveURL(/section=prov/)
  await expect(graphNode(page, 'prov')).toHaveAttribute('aria-pressed', 'true')
  await expect(page.locator('.nb-graph-aside')).toContainText('prov')
  await expect(provChip).toHaveAttribute('aria-current', 'true')
  await expect(consChip).not.toHaveAttribute('aria-current', 'true')

  // Node → chip: clicking another node moves the highlight.
  await graphNode(page, 'cons').click()
  await expect(consChip).toHaveAttribute('aria-current', 'true')
  await expect(provChip).not.toHaveAttribute('aria-current', 'true')

  // Clearing the selection clears the active chip.
  await page.locator('.nb-graph-aside').getByRole('button', { name: 'Clear' }).click()
  await expect(page).not.toHaveURL(/section=/)
  await expect(graphNode(page, 'cons')).toHaveAttribute('aria-pressed', 'false')
  await expect(dir.locator('.nb-dir-row[aria-current="true"]')).toHaveCount(0)
})

test('graph: Open code lands on the section card', async ({ page }) => {
  const id = await createInstance()
  for (let i = 1; i <= 8; i++) await saveSection(id, `s${i}`, `v${i} = ${i}`)
  await page.goto(`${rt.viteURL}/instances/${id}`)
  await openGraph(page)
  await graphNode(page, 's8').click()
  await page.locator('.nb-graph-aside').getByRole('button', { name: 'Open code' }).click()

  await expect(page.locator('.nb-code')).toBeVisible()
  expect(page.url()).not.toContain('nbview')
  expect(page.url()).toContain('section=s8')
  // The deep-link scroll fires on return from the graph panel.
  await expect(card(page, 's8')).toBeInViewport({ timeout: 10_000 })
})

test('graph: running sections repaints state without moving nodes', async ({ page }) => {
  const id = await createInstance()
  await saveSection(id, 'r1', 'v = 1')
  await saveSection(id, 'r2', 'w = v + 1')
  await page.goto(`${rt.viteURL}/instances/${id}`)
  await openGraph(page)
  await expect(graphNode(page, 'r1')).toHaveAttribute('data-state', 'not_run')
  const before = await nodeBoxes(page)

  // Run controls stay in the sticky toolbar in graph view; the state
  // repaint is paint-only — no node may move (S1).
  await page.locator('.nb-toolbar').getByRole('button', { name: 'Run all' }).click()
  await expect(page.locator('.nb-status')).toContainText('ran', { timeout: 30_000 })
  await expect(graphNode(page, 'r1')).toHaveAttribute('data-state', 'fresh', { timeout: 20_000 })
  await expect(graphNode(page, 'r2')).toHaveAttribute('data-state', 'fresh', { timeout: 20_000 })
  expect(await nodeBoxes(page)).toEqual(before)
})

// --- dependency-driven runs (EP-0023) ---------------------------------------

test('run: a consumer runs its not-fresh upstream first — dependency order, not page order', async ({ page }) => {
  const id = await createInstance()
  // The consumer sits BEFORE its provider in page order — the plan must
  // still execute provider → consumer.
  await saveSection(id, 'consumer', 'total = price * 2')
  await saveSection(id, 'provider', 'price = 10')
  await page.goto(`${rt.viteURL}/instances/${id}`)
  const c = card(page, 'consumer')
  await expect(c).toBeVisible()
  await expect(c.getByText('Saved', { exact: true })).toBeVisible({ timeout: 15_000 })

  let dryPosts = 0
  let realPosts = 0
  page.on('request', (r) => {
    if (r.method() !== 'POST' || !r.url().endsWith('/notebook/run')) return
    if (r.postDataJSON()?.dry_run) dryPosts++
    else realPosts++
  })
  await c.getByRole('button', { name: 'Run section' }).click()
  // The single-target run pulled in upstream — summarized as a batch.
  await expect(page.locator('.nb-status')).toContainText('2 ran', { timeout: 30_000 })
  await expect(page.locator('.nb-status')).toContainText('2 ok')
  // Exactly one dry-run preview, then one real run.
  await expect.poll(() => realPosts, { timeout: 20_000 }).toBe(1)
  expect(dryPosts).toBe(1)
  // Plan order is provider → consumer — the status title lists the
  // per-section rows in execution order (no Details modal).
  const reportTitle = await page.locator('.nb-status').getAttribute('title')
  const providerAt = reportTitle!.indexOf('provider · ok')
  const consumerAt = reportTitle!.indexOf('consumer · ok')
  expect(providerAt).toBeGreaterThanOrEqual(0)
  expect(consumerAt).toBeGreaterThan(providerAt)
  // Both sections end fresh.
  await expect.poll(async () => {
    const nb = await (await rt.client.get(`/api/v1/instances/${id}/notebook`)).json()
    return nb.notebook.sections.map((s: { name: string; state: string }) => `${s.name}:${s.state}`).sort().join(',')
  }, { timeout: 20_000 }).toBe('consumer:fresh,provider:fresh')
})

test('run: a fresh upstream is reused, not re-executed', async ({ page }) => {
  const id = await createInstance()
  await saveSection(id, 'provider', 'price = 10\nprint("provider ran")')
  await saveSection(id, 'consumer', 'total = price * 2')
  // Run everything once so both are fresh.
  const first = await runAll(id)
  expect(first.report.results.every((r: { status: string }) => r.status === 'ok')).toBe(true)
  const execsBefore = (await (await rt.client.get(`/api/v1/instances/${id}/executions`)).json()).executions.length

  await page.goto(`${rt.viteURL}/instances/${id}`)
  const c = card(page, 'consumer')
  await expect(c).toBeVisible()
  await expect(c.getByText('Saved', { exact: true })).toBeVisible({ timeout: 15_000 })

  await c.getByRole('button', { name: 'Run section' }).click()
  // provider is fresh → reused; consumer is the target → runs.
  await expect(page.locator('.nb-status')).toContainText('1 ran', { timeout: 30_000 })
  await expect(page.locator('.nb-status')).toContainText('1 reused')
  const execsAfter = (await (await rt.client.get(`/api/v1/instances/${id}/executions`)).json()).executions.length
  // Only the consumer executed — the fresh upstream cost no exec.
  expect(execsAfter).toBe(execsBefore + 1)
})

test('run: an agent edit to an upstream section blocks the run (F3)', async ({ page }) => {
  const id = await createInstance()
  await saveSection(id, 'provider', 'price = 10')
  await saveSection(id, 'consumer', 'total = price * 2')
  await page.goto(`${rt.viteURL}/instances/${id}`)
  const c = card(page, 'consumer')
  await expect(c).toBeVisible()
  await expect(c.getByText('Saved', { exact: true })).toBeVisible({ timeout: 15_000 })

  // Pin the upstream queue's saved value: abort its detail polls so the
  // agent's edit below can never be adopted into saved/latest.
  await page.route(/sections\/provider\?.*include_output=true/, (route) => route.abort())

  // The agent rewrites the UPSTREAM section before the run — the
  // dependency plan pulls it in, so its unseen edit must block.
  const rev = await notebookRevision(id)
  const wr = await rt.client.put(`/api/v1/instances/${id}/sections/provider`, {
    data: { code: 'price = 999', description: 'section provider', revision: rev },
  })
  expect(wr.status()).toBe(200)

  let realPosts = 0
  page.on('request', (r) => {
    if (r.method() === 'POST' && r.url().endsWith('/notebook/run') && !r.postDataJSON()?.dry_run) realPosts++
  })
  await c.getByRole('button', { name: 'Run section' }).click()
  await expect(page.locator('.nb-status')).toContainText('changed on the server', { timeout: 20_000 })
  // The dry-run preview went out but no real run request was ever sent.
  expect(realPosts).toBe(0)
  // The displayed draft is preserved.
  await expect(c.locator('.cm-content')).toContainText('total = price * 2')
  await page.unroute(/sections\/provider\?.*include_output=true/)
})

test('run: graph_blocked shows the server diagnostic and executes nothing', async ({ page }) => {
  const id = await createInstance()
  // `missing_value` has no provider — the plan refuses before executing.
  await saveSection(id, 'broken', 'show = missing_value + 1')
  await page.goto(`${rt.viteURL}/instances/${id}`)
  const c = card(page, 'broken')
  await expect(c).toBeVisible()
  await expect(c.getByText('Saved', { exact: true })).toBeVisible({ timeout: 15_000 })

  let realPosts = 0
  page.on('request', (r) => {
    if (r.method() === 'POST' && r.url().endsWith('/notebook/run') && !r.postDataJSON()?.dry_run) realPosts++
  })
  await c.getByRole('button', { name: 'Run section' }).click()
  // The server's diagnostic reaches the status line AND the cell note —
  // it is not the generic revision-conflict text.
  await expect(page.locator('.nb-status')).toContainText('no section defines it', { timeout: 20_000 })
  await expect(c.locator('.nb-note')).toContainText('no section defines it')
  expect(realPosts).toBe(0)
  // No run report exists — the block is only the status line + title.
  const title = await page.locator('.nb-status').getAttribute('title')
  expect(title).toContain('no section defines it')
})

test('run: a failed section shows its error on its card after a batch run', async ({ page }) => {
  const id = await createInstance()
  await saveSection(id, 'good', 'x = 1')
  // A runtime failure — not a graph diagnostic (those block the run
  // before anything executes): fail() raises inside the exec.
  await saveSection(id, 'bad', 'fail("boom-marker")')
  await page.goto(`${rt.viteURL}/instances/${id}`)
  const bad = card(page, 'bad')
  await expect(bad).toBeVisible()
  await expect(bad.getByText('Saved', { exact: true })).toBeVisible({ timeout: 15_000 })
  await page.locator('.nb-toolbar').getByRole('button', { name: 'Run all' }).click()
  await expect(page.locator('.nb-status')).toContainText('failed', { timeout: 30_000 })
  // The failure is on the card itself — the error output block, no
  // Details modal involved.
  await expect(bad.locator('.nb-output-err .nb-output-pre')).toContainText('boom-marker', { timeout: 15_000 })
})

// --- DAG execution coverage (EP-0023) — real API, no mocks ------------------

test('run: a shared diamond upstream executes exactly once', async () => {
  const id = await createInstance()
  // src feeds BOTH mid_a and mid_b, which feed sink — the plan must
  // dedupe the shared upstream, not run it per consumer.
  await saveSection(id, 'src', 'base = 10')
  await saveSection(id, 'mid_a', 'a = base + 1')
  await saveSection(id, 'mid_b', 'b = base + 2')
  await saveSection(id, 'sink', 'total = a + b')
  const execsBefore = (await (await rt.client.get(`/api/v1/instances/${id}/executions`)).json()).executions.length

  const r = await rt.client.post(`/api/v1/instances/${id}/notebook/run`, { data: { sections: 'sink' } })
  expect(r.status()).toBe(200)
  const report = (await r.json()).report
  const srcRows = report.results.filter((x: { name: string }) => x.name === 'src')
  expect(srcRows).toHaveLength(1)
  expect(srcRows[0].status).toBe('ok')
  // src, mid_a, mid_b, sink — four execs, not five.
  const execsAfter = (await (await rt.client.get(`/api/v1/instances/${id}/executions`)).json()).executions.length
  expect(execsAfter).toBe(execsBefore + 4)
  // Plan order stays topological: src before its consumers.
  const names = report.results.map((x: { name: string }) => x.name)
  expect(names.indexOf('src')).toBeLessThan(names.indexOf('mid_a'))
  expect(names.indexOf('src')).toBeLessThan(names.indexOf('mid_b'))
})

test('run: duplicate/cycle diagnostics block their sections but not an unrelated target', async () => {
  const id = await createInstance()
  // duplicate_define: `dup` defined twice, read by dup_user.
  await saveSection(id, 'dup_a', 'dup = 1')
  await saveSection(id, 'dup_b', 'dup = 2')
  await saveSection(id, 'dup_user', 'show = dup + 1')
  // cycle: cyc_a uses what cyc_b defines and vice versa.
  await saveSection(id, 'cyc_a', 'va = vb + 1')
  await saveSection(id, 'cyc_b', 'vb = va + 1')
  // unrelated: no shared names, no diagnostics on its closure.
  await saveSection(id, 'ok_sec', 'x = 1')

  const execsBefore = (await (await rt.client.get(`/api/v1/instances/${id}/executions`)).json()).executions.length

  // The unrelated section runs clean — diagnostics elsewhere don't leak.
  const ok = await rt.client.post(`/api/v1/instances/${id}/notebook/run`, { data: { sections: 'ok_sec' } })
  expect(ok.status()).toBe(200)

  // dup_user's closure touches the duplicate → graph_blocked, nothing runs.
  const dup = await rt.client.post(`/api/v1/instances/${id}/notebook/run`, { data: { sections: 'dup_user' } })
  expect(dup.status()).toBe(400)
  const dupBody = await dup.json()
  expect(dupBody.error.code).toBe('graph_blocked')
  expect(dupBody.error.message).toContain('dup')

  // cyc_a's closure contains the cycle → graph_blocked too.
  const cyc = await rt.client.post(`/api/v1/instances/${id}/notebook/run`, { data: { sections: 'cyc_a' } })
  expect(cyc.status()).toBe(400)
  const cycBody = await cyc.json()
  expect(cycBody.error.code).toBe('graph_blocked')

  // Only ok_sec ever executed — the blocked runs sent nothing.
  const execsAfter = (await (await rt.client.get(`/api/v1/instances/${id}/executions`)).json()).executions.length
  expect(execsAfter).toBe(execsBefore + 1)
})

test('run: a failed upstream stops the downstream section', async () => {
  const id = await createInstance()
  // prov_fail defines v statically but raises at runtime; cons_after
  // reads v, so the plan pulls the provider in first and fail-fast
  // must leave the consumer unreached.
  await saveSection(id, 'prov_fail', 'v = 1\nfail("upstream-boom")')
  await saveSection(id, 'cons_after', 'w = v + 1')
  const execsBefore = (await (await rt.client.get(`/api/v1/instances/${id}/executions`)).json()).executions.length

  const r = await rt.client.post(`/api/v1/instances/${id}/notebook/run`, { data: { sections: 'cons_after' } })
  expect(r.status()).toBe(200)
  const report = (await r.json()).report
  const prov = report.results.find((x: { name: string }) => x.name === 'prov_fail')
  const cons = report.results.find((x: { name: string }) => x.name === 'cons_after')
  expect(prov.status).toBe('error')
  expect(cons.status).toBe('not_run')
  // Only the provider ever executed — the consumer never ran.
  const execsAfter = (await (await rt.client.get(`/api/v1/instances/${id}/executions`)).json()).executions.length
  expect(execsAfter).toBe(execsBefore + 1)
})

test('run: dry_run reports the plan without executing anything', async () => {
  const id = await createInstance()
  await saveSection(id, 'dry_a', 'a = 1')
  await saveSection(id, 'dry_b', 'b = a + 1')
  const execsBefore = (await (await rt.client.get(`/api/v1/instances/${id}/executions`)).json()).executions.length

  const r = await rt.client.post(`/api/v1/instances/${id}/notebook/run`, { data: { sections: 'all', dry_run: true } })
  expect(r.status()).toBe(200)
  const report = (await r.json()).report
  expect(report.results.every((x: { status: string }) => x.status === 'planned')).toBe(true)
  // Nothing executed, nothing claimed: executions unchanged and every
  // section still not_run on the next read.
  const execsAfter = (await (await rt.client.get(`/api/v1/instances/${id}/executions`)).json()).executions.length
  expect(execsAfter).toBe(execsBefore)
  const nb = await (await rt.client.get(`/api/v1/instances/${id}/notebook`)).json()
  expect(nb.notebook.sections.every((s: { state: string }) => s.state === 'not_run')).toBe(true)
})

test('graph: Run section from the aside runs it and repaints without moving nodes', async ({ page }) => {
  const id = await createInstance()
  await saveSection(id, 'g1', 'v = 1')
  await saveSection(id, 'g2', 'w = v + 1')
  await page.goto(`${rt.viteURL}/instances/${id}`)
  await openGraph(page)
  await expect(graphNode(page, 'g1')).toHaveAttribute('data-state', 'not_run')
  await graphFitted(page)
  const before = await nodeBoxes(page)
  const vpBefore = await viewportTransform(page)

  await graphNode(page, 'g2').click()
  await page.locator('.nb-graph-aside').getByRole('button', { name: 'Run section' }).click()
  // g1 is g2's not-fresh upstream — it runs first, then g2; both repaint.
  await expect(page.locator('.nb-status')).toContainText('ran', { timeout: 30_000 })
  await expect(graphNode(page, 'g2')).toHaveAttribute('data-state', 'fresh', { timeout: 20_000 })
  await expect(graphNode(page, 'g1')).toHaveAttribute('data-state', 'fresh', { timeout: 20_000 })
  // Feedback lives in the toolbar status line; node boxes and the
  // viewport never moved (S1).
  expect(await nodeBoxes(page)).toEqual(before)
  expect(await viewportTransform(page)).toBe(vpBefore)
})

test('graph: first reveal fits a three-layer chain inside the canvas', async ({ page }) => {
  const id = await createInstance()
  await saveSection(id, 'src', 'price = 10')
  await saveSection(id, 'mid', 'total = price * 2')
  await saveSection(id, 'out', 'print(total)')
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto(`${rt.viteURL}/instances/${id}`)
  await openGraph(page)
  await expect(page.locator('.react-flow__node')).toHaveCount(3)
  // fitView ran on first reveal — every node inside the canvas, zoom
  // capped at 100% (no upscaling).
  await expect.poll(() => allNodesInsideCanvas(page), { timeout: 10_000 }).toBe(true)
  expect(await viewportScale(page)).toBeLessThanOrEqual(1.001)
})

test('graph: zoom persists across polls; a chip click pans a node into view', async ({ page }) => {
  const id = await createInstance()
  // Six ranks — wider than the canvas at fit zoom, so zooming in pushes
  // the tail nodes off-screen.
  await saveSection(id, 'n1', 'v1 = 1')
  for (let i = 2; i <= 6; i++) await saveSection(id, `n${i}`, `v${i} = v${i - 1} + 1`)
  await page.goto(`${rt.viteURL}/instances/${id}`)
  await openGraph(page)
  await expect(page.locator('.react-flow__node')).toHaveCount(6)
  await expect.poll(() => allNodesInsideCanvas(page), { timeout: 10_000 }).toBe(true)
  await graphFitted(page)

  // Zoom in through the Controls button — an explicit user action.
  const z0 = await viewportScale(page)
  for (let i = 0; i < 3; i++) await page.locator('.react-flow__controls-zoomin').click()
  await expect.poll(() => viewportScale(page)).toBeGreaterThan(z0 * 1.5)
  const vpZoomed = await viewportTransform(page)
  const zZoomed = await viewportScale(page)

  // A poll lands a new section: the graph re-lays out live (tail
  // fades in at the end of the chain) and the user's zoom must not
  // reset — polls never touch the viewport (S5).
  await saveSection(id, 'tail', 'v7 = v6 + 1')
  await expect(page.locator('.react-flow__node')).toHaveCount(7, { timeout: 20_000 })
  await expect(graphNode(page, 'tail')).toBeVisible()
  await page.waitForTimeout(400) // let the re-layout tween settle
  expect(await viewportTransform(page)).toBe(vpZoomed)

  // n6 sits off-screen at this zoom; its chip click pans it into view
  // at the SAME zoom — only the translate may change.
  await page.locator('.nb-directory .nb-dir-row', { hasText: 'n6' }).click()
  await expect(graphNode(page, 'n6')).toHaveAttribute('aria-pressed', 'true')
  await expect.poll(async () => {
    const b = (await graphNode(page, 'n6').boundingBox())!
    const cb = (await page.locator('.nb-graph-canvas').boundingBox())!
    return b.x >= cb.x - 1 && b.y >= cb.y - 1 &&
      b.x + b.width <= cb.x + cb.width + 1 && b.y + b.height <= cb.y + cb.height + 1
  }, { timeout: 5_000 }).toBe(true)
  // The 200ms pan interpolates zoom mid-flight (d3-zoom); wait for it to settle.
  await expect.poll(async () => Math.abs(await viewportScale(page) - zZoomed) < 0.001,
    { timeout: 5_000 }).toBe(true)
  expect(await viewportTransform(page)).not.toBe(vpZoomed)
})

test('graph: the wheel over the canvas still scrolls the page', async ({ page }) => {
  const id = await createInstance()
  await saveSection(id, 'w1', 'a = 1')
  await saveSection(id, 'w2', 'b = a + 1')
  await page.goto(`${rt.viteURL}/instances/${id}`)
  // A short viewport makes the fixed-height graph box taller than the
  // remaining page — the window must still scroll with the pointer
  // over the canvas (panOnScroll stays off; wheel is not captured).
  await page.setViewportSize({ width: 1440, height: 460 })
  await openGraph(page)
  await expect(page.locator('.react-flow__node')).toHaveCount(2)
  await graphFitted(page)

  const vpBefore = await viewportTransform(page)
  await page.evaluate(() => window.scrollTo(0, 10))
  const cb = (await page.locator('.nb-graph-canvas').boundingBox())!
  await page.mouse.move(cb.x + cb.width / 2, cb.y + cb.height / 2)
  const y0 = await page.evaluate(() => window.scrollY)
  await page.mouse.wheel(0, 300)
  await expect.poll(() => page.evaluate(() => window.scrollY), { timeout: 5_000 }).toBeGreaterThan(y0)
  // The canvas viewport itself did not pan — the wheel belonged to
  // the page.
  expect(await viewportTransform(page)).toBe(vpBefore)
})

test('graph: a structural poll tweens movers and fades entrants in', async ({ page }) => {
  const id = await createInstance()
  await saveSection(id, 'tw_a', 'a = 1')
  await saveSection(id, 'tw_b', 'b = a + 1')
  await saveSection(id, 'tw_c', 'c = b + 1')
  await page.goto(`${rt.viteURL}/instances/${id}`)
  await openGraph(page)
  await expect(page.locator('.nb-graph-node')).toHaveCount(3)
  await graphFitted(page)

  // Per-frame sampler: every animation frame records each node's
  // translate and the wrapper's inline opacity (the fade channel).
  // It keeps running in the page until cancelled below.
  await page.evaluate(() => {
    const w = window as unknown as {
      __gs: { t: number; n: Record<string, { x: number; y: number; o: number }> }[]
      __gsa: number
    }
    w.__gs = []
    const loop = () => {
      const nodes: Record<string, { x: number; y: number; o: number }> = {}
      document.querySelectorAll('.react-flow__node').forEach((el) => {
        const nid = el.getAttribute('data-id') ?? ''
        const m = /translate\(\s*(-?[\d.]+)px\s*,\s*(-?[\d.]+)px/.exec((el as HTMLElement).style.transform)
        nodes[nid] = {
          x: m ? +m[1] : NaN,
          y: m ? +m[2] : NaN,
          o: parseFloat((el as HTMLElement).style.opacity || '1'),
        }
      })
      w.__gs.push({ t: performance.now(), n: nodes })
      w.__gsa = requestAnimationFrame(loop)
    }
    w.__gsa = requestAnimationFrame(loop)
  })

  // Two structural changes inside one sampling window: deleting tw_b
  // drops its edge so tw_c loses its rank and tweens left while tw_b
  // fades out; the new isolated tw_d fades in at its final position.
  const rev = await notebookRevision(id)
  const dr = await rt.client.delete(`/api/v1/instances/${id}/sections/tw_b?revision=${rev}`)
  expect(dr.status()).toBe(200)
  await saveSection(id, 'tw_d', 'd = 1')
  await expect(page.locator('.react-flow__node[data-id="tw_d"]')).toHaveCount(1, { timeout: 20_000 })
  await page.waitForTimeout(500) // let tweens and fades settle
  await page.evaluate(() => {
    const w = window as unknown as { __gsa?: number }
    if (w.__gsa) cancelAnimationFrame(w.__gsa)
  })
  const samples = await page.evaluate(() =>
    (window as unknown as { __gs: { t: number; n: Record<string, { x: number; y: number; o: number }> }[] }).__gs
  )
  expect(samples.length).toBeGreaterThan(30)

  // tw_c moved ranks: some sampled frame sits strictly between its
  // first and last observed x — proof of a tween, not a jump cut.
  const cXs = samples.map((s) => s.n['tw_c']?.x).filter((x): x is number => typeof x === 'number' && !Number.isNaN(x))
  expect(cXs.length).toBeGreaterThan(10)
  const cMin = Math.min(...cXs)
  const cMax = Math.max(...cXs)
  expect(cMax - cMin).toBeGreaterThan(50) // it really moved
  expect(cXs.some((x) => x > cMin + 5 && x < cMax - 5)).toBe(true)

  // tw_d fades IN: its first sampled opacity is well below 1 (the
  // layout-effect reconcile means it is never painted at 1 first) and
  // it ends fully opaque.
  const dSamples = samples.filter((s) => s.n['tw_d'])
  expect(dSamples.length).toBeGreaterThan(5)
  expect(dSamples[0].n['tw_d'].o).toBeLessThan(0.9)
  expect(dSamples[dSamples.length - 1].n['tw_d'].o).toBeGreaterThan(0.99)

  // tw_b fades OUT: opacity decreases across samples before unmount.
  const bSamples = samples.filter((s) => s.n['tw_b'])
  expect(bSamples.length).toBeGreaterThan(3)
  expect(Math.min(...bSamples.map((s) => s.n['tw_b'].o))).toBeLessThan(0.6)
})

test('graph: rapid Code↔Graph toggles before first fit still land fitted', async ({ page }) => {
  const id = await createInstance()
  await saveSection(id, 'rq_a', 'a = 1')
  await saveSection(id, 'rq_b', 'b = a + 1')
  await page.goto(`${rt.viteURL}/instances/${id}`)
  const toggle = page.locator('.nb-view-toggle')
  const graphBtn = toggle.getByRole('button', { name: 'Graph' })
  const codeBtn = toggle.getByRole('button', { name: 'Code' })
  await graphBtn.click()
  // Hammer the switch while the first fit is still pending — every
  // hide cancels the scheduled RAFs; each reveal must retry and the
  // fit must still complete exactly once, never while hidden.
  for (let i = 0; i < 3; i++) {
    await codeBtn.click()
    await graphBtn.click()
  }
  await expect(page.locator('.nb-graph')).toBeVisible()
  await graphFitted(page)
  await expect.poll(() => allNodesInsideCanvas(page), { timeout: 10_000 }).toBe(true)

  // Once fitted, a structural poll re-lays out without refitting —
  // the viewport the user sees is the one the fit chose.
  const vp = await viewportTransform(page)
  await saveSection(id, 'rq_c', 'c = b + 1')
  await expect(page.locator('.react-flow__node')).toHaveCount(3, { timeout: 20_000 })
  await page.waitForTimeout(400)
  expect(await viewportTransform(page)).toBe(vp)

  // A user zoom is explicit state: hiding and re-revealing the panel
  // must not refit or reset it (the first fit already ran once).
  const z0 = await viewportScale(page)
  for (let i = 0; i < 2; i++) await page.locator('.react-flow__controls-zoomin').click()
  await expect.poll(() => viewportScale(page)).toBeGreaterThan(z0 * 1.2)
  const vpZoomed = await viewportTransform(page)
  await codeBtn.click()
  await graphBtn.click()
  await expect(page.locator('.nb-graph')).toBeVisible()
  await page.waitForTimeout(400)
  expect(await viewportTransform(page)).toBe(vpZoomed)
})

test('view switch: Code→Graph reveals the graph; polls never scroll; Code restores scroll', async ({ page }) => {
  const id = await createInstance()
  for (let i = 1; i <= 10; i++) await saveSection(id, `sec${i}`, `v${i} = ${i}`)
  await page.goto(`${rt.viteURL}/instances/${id}`)
  await expect(card(page, 'sec1')).toBeVisible()
  // Deep scroll in Code view — the page (window) is the scroller.
  await page.evaluate(() => window.scrollTo(0, document.documentElement.scrollHeight))
  const yCode = await page.evaluate(() => window.scrollY)
  expect(yCode).toBeGreaterThan(0)

  // Explicit switch: the graph box lands just under the sticky head,
  // not at the bottom of the long card list.
  await page.locator('.nb-view-toggle').getByRole('button', { name: 'Graph' }).click()
  await expect(page.locator('.nb-graph')).toBeVisible()
  await graphFitted(page)
  await expect.poll(async () => {
    const g = await page.locator('.nb-graph').boundingBox()
    return g ? g.y : -1
  }, { timeout: 5_000 }).toBeGreaterThan(60)
  await page.waitForTimeout(600) // let the smooth scrollIntoView settle
  const gTop = (await page.locator('.nb-graph').boundingBox())!.y
  expect(gTop).toBeLessThan(320) // revealed near the viewport top
  expect(gTop).toBeGreaterThan(60) // and clear of the sticky head
  const yGraph = await page.evaluate(() => window.scrollY)

  // A structural poll re-lays the graph out but must NOT scroll the
  // page — viewport pan/zoom and window scroll both stay put.
  await saveSection(id, 'tail', 'v11 = v10 + 1')
  await expect(page.locator('.react-flow__node')).toHaveCount(11, { timeout: 20_000 })
  await page.waitForTimeout(500)
  expect(await page.evaluate(() => window.scrollY)).toBe(yGraph)

  // Back to Code: the previous card-list scroll position is restored
  // (no ?section= navigation is involved, so nothing fights it).
  await page.locator('.nb-view-toggle').getByRole('button', { name: 'Code' }).click()
  await expect(page.locator('.nb-code')).toBeVisible()
  await expect.poll(() => page.evaluate(() => window.scrollY), { timeout: 5_000 })
    .toBeGreaterThan(yCode - 120)
})

test('graph: nodes are keyboard-focusable and Enter/Space select them', async ({ page }) => {
  const id = await createInstance()
  await saveSection(id, 'ka', 'a = 1')
  await saveSection(id, 'kb', 'b = a + 1')
  await page.goto(`${rt.viteURL}/instances/${id}`)
  await openGraph(page)
  const ka = graphNode(page, 'ka')
  const kb = graphNode(page, 'kb')
  // RF keeps nodes visibility:hidden until measured — wait for the
  // real box before focusing, or focus() lands nowhere.
  await expect(ka).toBeVisible()
  await ka.focus()
  await page.keyboard.press('Enter')
  await expect(ka).toHaveAttribute('aria-pressed', 'true')
  await kb.focus()
  await page.keyboard.press('Space')
  await expect(kb).toHaveAttribute('aria-pressed', 'true')
  await expect(ka).toHaveAttribute('aria-pressed', 'false')
})

test('screenshots: notebook graph desktop/mobile × light/dark', async ({ page }) => {
  const id = await createInstance()
  await saveSection(id, 'src', 'price = 10')
  await saveSection(id, 'mid', 'total = price * 2')
  await saveSection(id, 'out', 'print(total)', { rerun: 'once' })
  // A read duplicate keeps the issues list populated; the src→mid→out
  // chain is the three-layer chain the desktop shot must show whole.
  await saveSection(id, 'dup_a', 'dup = 1')
  await saveSection(id, 'dup_b', 'dup = 2')
  await saveSection(id, 'dup_user', 'show = dup + 1')
  await page.goto(`${rt.viteURL}/instances/${id}`)
  await openGraph(page)
  await expect(page.locator('.react-flow__node')).toHaveCount(6)
  // First reveal fit: every node box sits inside the canvas — no pan
  // needed at desktop width (fitView is the only allowed viewport move).
  await expect.poll(() => allNodesInsideCanvas(page), { timeout: 10_000 }).toBe(true)
  // A chip click is the explicit selection in graph view: the node is
  // pressed, the aside opens and the chip carries aria-current — the
  // shots below show the floating head with that active chip.
  await page.locator('.nb-directory .nb-dir-row', { hasText: 'mid' }).click()
  await expect(graphNode(page, 'mid')).toHaveAttribute('aria-pressed', 'true')
  await expect(page.locator('.nb-directory .nb-dir-row', { hasText: 'mid' })).toHaveAttribute('aria-current', 'true')
  for (const scheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme: scheme })
    await page.waitForTimeout(200)
    await page.screenshot({ path: join(SHOTS, `notebook-graph-desktop-${scheme}.png`), fullPage: false })
  }
  // One shot with a different selection (ancestors/descendants move).
  await page.emulateMedia({ colorScheme: 'light' })
  await graphNode(page, 'out').click()
  await expect(page.locator('.nb-directory .nb-dir-row', { hasText: 'out' })).toHaveAttribute('aria-current', 'true')
  await page.waitForTimeout(200)
  await page.screenshot({ path: join(SHOTS, `notebook-graph-desktop-light-selected.png`), fullPage: false })
  // Mobile: the aside stacks below the canvas.
  await page.setViewportSize({ width: 390, height: 844 })
  await page.waitForTimeout(200)
  await page.screenshot({ path: join(SHOTS, `notebook-graph-mobile-light.png`), fullPage: false })
  await page.setViewportSize({ width: 1440, height: 1000 })
})

test('screenshots: 2x corner close-ups — head chip, toggle, graph node', async ({ browser }) => {
  const id = await createInstance()
  await saveSection(id, 'alpha', 'a = 1')
  await saveSection(id, 'beta', 'b = a + 1')
  for (const scheme of ['light', 'dark'] as const) {
    const ctx = await browser.newContext({
      viewport: { width: 1440, height: 900 },
      deviceScaleFactor: 2,
      colorScheme: scheme,
    })
    const page = await ctx.newPage()
    await page.goto(`${rt.viteURL}/instances/${id}`)
    await expect(card(page, 'alpha')).toBeVisible()
    // (a) the head's left end with the first chip active.
    const firstChip = page.locator('.nb-dir-row').first()
    await firstChip.click()
    await expect(firstChip).toHaveAttribute('aria-current', 'true')
    await page.waitForTimeout(250)
    const head = (await page.locator('.nb-head').boundingBox())!
    await page.screenshot({
      path: join(SHOTS, `nb-corner-head-chip-${scheme}-2x.png`),
      clip: { x: head.x, y: head.y, width: Math.min(460, head.width), height: head.height },
    })
    // (b) the segmented toggle — Code active, then Graph active.
    const tog = (await page.locator('.nb-view-toggle').boundingBox())!
    const togClip = {
      x: Math.max(0, tog.x - 40), y: Math.max(0, tog.y - 16),
      width: tog.width + 80, height: tog.height + 32,
    }
    await page.screenshot({ path: join(SHOTS, `nb-corner-toggle-code-${scheme}-2x.png`), clip: togClip })
    await openGraph(page)
    await page.waitForTimeout(250)
    await page.screenshot({ path: join(SHOTS, `nb-corner-toggle-graph-${scheme}-2x.png`), clip: togClip })
    // (c) a selected node inside the graph box — node corners sit
    // concentric inside the box's rounded edge.
    await graphNode(page, 'beta').click()
    await expect(graphNode(page, 'beta')).toHaveAttribute('aria-pressed', 'true')
    await page.waitForTimeout(250)
    const node = (await graphNode(page, 'beta').boundingBox())!
    const graphBox = (await page.locator('.nb-graph').boundingBox())!
    const x0 = Math.max(graphBox.x, node.x - 60)
    const y0 = Math.max(graphBox.y, node.y - 60)
    const x1 = Math.min(graphBox.x + graphBox.width, node.x + node.width + 60)
    const y1 = Math.min(graphBox.y + graphBox.height, node.y + node.height + 60)
    await page.screenshot({
      path: join(SHOTS, `nb-corner-graph-node-${scheme}-2x.png`),
      clip: { x: x0, y: y0, width: x1 - x0, height: y1 - y0 },
    })
    await ctx.close()
  }
})

// EP-0024: a section that io.display()s a chart.spec renders an SVG in
// its card; the same SVG DOM node must survive the 5s detail poll —
// re-embedding on every fetch would reset zoom/brush state (C3).
test('chart: io.display renders an SVG that survives section polls', async ({ page }) => {
  const id = await createInstance()
  await saveSection(id, 'chartsec', `data = [{"x": 1, "y": 2}, {"x": 2, "y": 4}, {"x": 3, "y": 9}]
spec = chart.spec("line", data=data, encoding={"x": chart.field("x", "quantitative"), "y": chart.field("y", "quantitative")}, params=[{"name": "zoom", "select": "interval", "bind": "scales"}])
io.display(spec, title="pts")`)
  await runAll(id)

  await page.goto(`${rt.viteURL}/instances/${id}`)
  await expect(page.locator('.nb-toolbar')).toBeVisible({ timeout: 30_000 })
  const c = card(page, 'chartsec')
  // svg.marks is the chart itself — the embed actions menu has its own svg.
  const svg = c.locator('.nb-chart svg.marks')
  // First render pays the lazy vega chunk + embed cost.
  await expect(svg).toBeVisible({ timeout: 30_000 })
  // Mark THIS svg node; a re-embed would replace it with an unmarked one.
  await svg.evaluate((el) => el.setAttribute('data-chart-node', '1'))
  // The section detail refetches every 5s while live — wait past a
  // full poll cycle, then require the marked node to still be there.
  await page.waitForTimeout(6500)
  await expect(c.locator('.nb-chart svg.marks[data-chart-node="1"]')).toHaveCount(1)
})
