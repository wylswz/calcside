import { test, expect, type APIRequestContext, type Page } from '@playwright/test'
import { startApplicationRuntime, type ApplicationRuntime } from './applicationRuntime'

// EP-0025 notebook applications under real Vite + scratch API/SQLite:
// UI create -> section editing with entry
// labels -> saved dependency graph -> Run -> publish -> invoke,
// plus a pinned-release handler resume across republish.

let rt: ApplicationRuntime
let client: APIRequestContext
let viteURL = ''

test.beforeAll(async () => {
	test.setTimeout(300_000)
	rt = await startApplicationRuntime({ secretKey: 'MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=' })
	client = rt.client
	viteURL = rt.viteURL
})

test.afterAll(async () => { await rt?.close() })

test.describe.configure({ mode: 'serial' })

async function waitReady(id: string, timeoutMs = 90_000) {
	await expect.poll(async () => {
		const r = await client.get(`/api/v1/applications/${id}`)
		return (await r.json()).application.status
	}, { timeout: timeoutMs }).toBe('ready')
}

async function publishViaDialog(page: Page) {
	await page.getByRole('button', { name: 'Publish…' }).click()
	await page.getByTestId('review-publish').click()
}

// Reviewed publish over the API (preview -> publish with digests).
async function publish(id: string, version: number) {
	const prev = await client.post(`/api/v1/applications/${id}/publish/preview`, { data: { version } })
	expect(prev.status(), `preview: ${await prev.text()}`).toBe(200)
	const rev = await prev.json()
	const pub = await client.post(`/api/v1/applications/${id}/publish`, {
		data: { version, expected_base: rev.base?.digest ?? '', candidate: rev.candidate.digest },
	})
	expect(pub.status(), `publish: ${await pub.text()}`).toBe(202)
}

test('notebook: create -> sections -> test main -> graph -> publish -> invoke', async ({ page }) => {
	await page.goto(`${viteURL}/applications/new`)
	await page.locator('#basics-name').fill('NbGreeter')
	await page.getByRole('button', { name: 'Continue to configuration' }).click()
	await page.getByRole('button', { name: 'Create application' }).click()
	await page.waitForURL(/\/applications\/app_/)
	const id = page.url().split('/').pop()!

	// The workbench renders the scaffold's main cell and the directory.
	const main = page.locator('[aria-label="Application section main"] .cm-content')
	await expect(main).toContainText('def main(input)', { timeout: 15_000 })
	const directory = page.getByRole('navigation', { name: 'Section directory' })
	await expect(directory.getByRole('button', { name: /main/ })).toBeVisible()

	// Toolbar Add section creates an auto-named cell above pinned main —
	// the same interaction as the instance notebook, no prompt.
	await page.getByRole('button', { name: 'Add section', exact: true }).click()
	const auto = page.locator('[aria-label="Application section untitled_section"] .cm-content')
	await expect(auto).toBeVisible()
	await expect(auto).toBeFocused()
	await expect(main).toBeVisible()
	const cardIds = () => page.locator('.nb-card').evaluateAll((els) => els.map((e) => e.id))
	expect(await cardIds()).toEqual(['section-untitled_section', 'section-main'])

	// Rename the new cell to format via its card menu.
	await page.getByRole('button', { name: 'Section untitled_section actions' }).click()
	await page.getByRole('menuitem', { name: 'Rename…' }).click()
	await page.getByLabel('Rename section untitled_section').fill('format')
	await page.keyboard.press('Enter')
	const format = page.locator('[aria-label="Application section format"] .cm-content')
	await expect(format).toBeVisible()
	await format.fill('def greet(name):\n    return "Notebook " + name')
	await directory.getByRole('button', { name: /main/ }).click()
	await expect(page).toHaveURL(/section=main/)
	await main.fill('def main(input):\n    return {"message": greet(input["name"])}')

	// The shared between-cell inserter lands another auto-named cell
	// right after its own cell — before pinned main — and focuses it.
	await page.getByRole('button', { name: 'Insert section after format', exact: true }).click()
	const auto2 = page.locator('[aria-label="Application section untitled_section"] .cm-content')
	await expect(auto2).toBeFocused()
	await page.keyboard.type('tmp = 1')
	expect(await cardIds()).toEqual(['section-format', 'section-untitled_section', 'section-main'])
	// Nothing inserts after the pinned main cell.
	await expect(page.getByRole('button', { name: 'Insert section after main', exact: true })).toHaveCount(0)

	const savedOrder = async () => {
		const r = await client.get(`/api/v1/applications/${id}`)
		return (await r.json()).application.sections?.map((s: any) => s.id) ?? []
	}
	// main is pinned last — no drag handle.
	await expect(page.getByRole('button', { name: 'Drag section main' })).toHaveCount(0)
	const handle = page.getByRole('button', { name: 'Drag section untitled_section' })
	const hb = (await handle.boundingBox())!
	const fb = (await page.locator('#section-format').boundingBox())!
	// A press without movement is a click, not a reorder.
	await page.mouse.move(hb.x + hb.width / 2, hb.y + hb.height / 2)
	await page.mouse.down()
	await page.mouse.up()
	expect(await cardIds()).toEqual(['section-format', 'section-untitled_section', 'section-main'])
	// Drag the handle above format — commit happens on release.
	await page.mouse.down()
	await page.mouse.move(fb.x + fb.width / 2, fb.y + 4, { steps: 8 })
	await page.mouse.up()
	await expect.poll(savedOrder, { timeout: 15_000 }).toEqual(['untitled_section', 'format', 'main'])

	// Delete the temporary cell via menu + confirm.
	await page.getByRole('button', { name: 'Collapse untitled_section code' }).click()
	await expect(page.locator('[aria-label="Application section untitled_section"] .cm-content')).toBeHidden()
	await page.getByRole('button', { name: 'Section untitled_section actions' }).click()
	await page.getByRole('menuitem', { name: 'Delete…' }).click()
	await page.getByRole('button', { name: 'Remove', exact: true }).click()
	await expect.poll(savedOrder, { timeout: 15_000 }).toEqual(['format', 'main'])

	// The toolbar Test button opens the Test panel —
	// the output placeholder stays until Run is clicked.
	await page.getByRole('button', { name: 'Test', exact: true }).click()
	const testPanel = page.getByRole('region', { name: 'Test notebook' })
	await expect(testPanel).toBeVisible()
	await expect(testPanel.locator('.app-out-pre')).toContainText('Run to see output')
	await page.getByTestId('app-run').click()
	await expect(page.locator('[aria-label="Test notebook"] .app-out-pre')).toContainText('Notebook world', { timeout: 30_000 })

	// Saved graph carries the format -> main greet edge and entry labels.
	const app = (await (await client.get(`/api/v1/applications/${id}`)).json()).application
	expect(app.sections.map((s: any) => s.id).sort()).toEqual(['format', 'main'])
	expect(app.notebook_graph.edges).toContainEqual({ from: 'format', to: 'main', name: 'greet' })
	expect(app.notebook_entries.main).toContain('main')

	await page.getByRole('button', { name: 'Graph' }).click()
	await expect(page.locator('[data-testid="nb-graph-node"][data-section="main"]')).toBeVisible({ timeout: 15_000 })
	await expect(page.locator('[data-testid="nb-graph-node"][data-section="format"]')).toBeVisible()
	await expect(page.locator('[data-testid="nb-graph-node"][data-section="main"] .nb-graph-node-entry')).toHaveText('Main: main')
	// Clear deselects in the graph without touching anything else.
	await page.locator('[data-testid="nb-graph-node"][data-section="main"]').click()
	await page.getByRole('button', { name: 'Clear' }).click()
	await expect(page.locator('[data-testid="nb-graph-node"][data-section="main"]')).toHaveAttribute('aria-pressed', 'false')
	// Open code deep-links back to the cell without losing other cells.
	await page.locator('[data-testid="nb-graph-node"][data-section="main"]').click()
	await page.getByRole('button', { name: 'Open code' }).click()
	await expect(page).toHaveURL(/section=main/)
	await expect(page.locator('.nb-card')).toHaveCount(2)
	await expect(main).toBeVisible()
	await expect(format).toBeVisible()

	// Narrow view: no horizontal document overflow at 390px.
	await page.setViewportSize({ width: 390, height: 800 })
	expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
	await page.setViewportSize({ width: 1280, height: 800 })

	// Draft survives a refresh.
	await page.reload()
	await expect(page.locator('[aria-label="Application section main"] .cm-content')).toContainText('greet(input["name"])', { timeout: 15_000 })

	await publishViaDialog(page)
	await waitReady(id)
	await page.getByRole('tablist', { name: 'Application views' }).getByRole('tab', { name: 'Invoke' }).click()
	const invokeCard = page.getByRole('tabpanel', { name: 'Invoke', exact: true })
	await expect(invokeCard.getByRole('button', { name: 'Send' })).toBeEnabled({ timeout: 10_000 })
	await invokeCard.getByRole('button', { name: 'Send' }).click()
	await expect(invokeCard.locator('.app-invoke-output > .wb-pane-body > .app-out-pre')).toContainText('Notebook world', { timeout: 30_000 })

	// Back on the workbench, renaming the pinned main keeps its lock
	// and label through the pre-save window.
	await page.getByRole('tablist', { name: 'Application views' }).getByRole('tab', { name: 'Editor' }).click()
	await page.getByRole('button', { name: 'Section main actions' }).click()
	await page.getByRole('menuitem', { name: 'Rename…' }).click()
	await page.getByLabel('Rename section main').fill('entry')
	await page.keyboard.press('Enter')
	await expect(page.locator('[aria-label="Application section entry"]')).toBeVisible()
	await expect(page.getByRole('button', { name: 'Drag section entry' })).toHaveCount(0)
	expect((await cardIds()).at(-1)).toBe('section-entry')
	// After the save lands, the re-derived entries keep the pin and label.
	await expect.poll(savedOrder, { timeout: 15_000 }).toEqual(['format', 'entry'])
	await expect(page.locator('#section-entry .appnb-entry')).toHaveText('Main: main')
})

test('notebook: docked extension catalog installs and configures into the draft', async ({ page }) => {
	await page.goto(`${viteURL}/applications/new`)
	await page.locator('#basics-name').fill('NbExt')
	await page.getByRole('button', { name: 'Continue to configuration' }).click()
	await page.getByRole('button', { name: 'Create application' }).click()
	await page.waitForURL(/\/applications\/app_/)
	const id = page.url().split('/').pop()!

	await expect(page.locator('[aria-label="Application section main"] .cm-content')).toContainText('def main(input)', { timeout: 15_000 })

	// The catalog docks in the side rail; adding a pin mutates the draft.
	await page.getByRole('button', { name: 'Extensions', exact: true }).click()
	await page.getByRole('button', { name: 'Add probe 0.1.0', exact: true }).click({ timeout: 15_000 })
	await expect.poll(async () => {
		const r = await client.get(`/api/v1/applications/${id}`)
		return (await r.json()).application.spec?.capabilities?.ext?.probe?.source ?? ''
	}, { timeout: 15_000 }).toBe('fixture/probe@0.1.0')

	// The "Added" action opens the full Settings workspace on Runtime,
	// revealing the installed extension's editor — Code stays mounted
	// behind hidden.
	await page.getByRole('button', { name: 'Added probe, show settings' }).click()
	const settings = page.getByRole('region', { name: 'Notebook settings' })
	await expect(settings).toBeVisible()
	await expect(settings.getByRole('tab', { name: 'Runtime' })).toHaveAttribute('aria-selected', 'true')
	await expect(settings.locator('[data-section^="ext:"]')).toBeVisible()
	await expect(page.locator('.appnb-code')).toBeHidden()
	await expect(page.locator('[aria-label="Application section main"] .cm-content')).toBeAttached()

	// Contract holds the JSON Schema toggles; General holds Name.
	await settings.getByRole('tab', { name: 'Contract' }).click()
	await settings.getByLabel('Custom input schema').check()
	await settings.getByLabel('in schema JSON').fill('{"type":"object"}')
	await expect.poll(async () => {
		const r = await client.get(`/api/v1/applications/${id}`)
		return (await r.json()).application.input_schema ?? ''
	}, { timeout: 15_000 }).toContain('"object"')
	await settings.getByRole('tab', { name: 'General' }).click()
	await expect(settings.getByLabel('Name')).toBeVisible()

	// Narrow view: no horizontal overflow with Settings open.
	await page.setViewportSize({ width: 390, height: 800 })
	expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
	await page.setViewportSize({ width: 1280, height: 800 })

	// Modules and Policies dock as parallel rails next to Extensions —
	// their real pickers mount into each pane.
	await page.getByRole('button', { name: 'Modules', exact: true }).click()
	await expect(page.getByLabel('Search modules')).toBeVisible()
	await page.getByRole('button', { name: 'Policies', exact: true }).click()
	await expect(page.getByLabel('Search policies')).toBeVisible()

	// Closing Settings returns to the mounted canvas and hands focus
	// back to the toolbar Settings toggle.
	await page.getByRole('button', { name: 'Close settings', exact: true }).click()
	await expect(page.getByRole('button', { name: 'Settings', exact: true })).toBeFocused()
	await expect(page.locator('[aria-label="Application section main"] .cm-content')).toBeVisible()

	// Draft main calls the pinned extension; Test runs it pre-publish.
	await page.locator('[aria-label="Application section main"] .cm-content')
		.fill('def main(input):\n    return ext.probe.echo(input["name"])')
	await page.getByRole('button', { name: 'Test', exact: true }).click()
	await page.getByTestId('app-run').click()
	await expect(page.locator('[aria-label="Test notebook"] .app-out-pre')).toContainText('"release":"v1"', { timeout: 30_000 })
	const app = (await (await client.get(`/api/v1/applications/${id}`)).json()).application
	expect(app.published_version ?? 0).toBe(0)

	// --- Settings workspace layout ---
	await page.getByRole('button', { name: 'Settings', exact: true }).click()
	const nav = page.getByRole('tablist', { name: 'Settings categories' })
	const tabs = nav.getByRole('tab')
	await expect(tabs).toHaveCount(4)
	const tabBoxes = await tabs.evaluateAll((els) => els.map((e) => e.getBoundingClientRect()))
	for (let i = 1; i < tabBoxes.length; i++) {
		expect(tabBoxes[i].x - (tabBoxes[i - 1].x + tabBoxes[i - 1].width)).toBeGreaterThanOrEqual(4)
	}
	const navPad = await nav.evaluate((el) => getComputedStyle(el).paddingLeft)
	expect(parseFloat(navPad)).toBeGreaterThanOrEqual(4)

	// Form | JSON is one fixed header control — geometry never moves (S3/S4).
	const modeBox = async () => {
		const [f, j] = await Promise.all([
			page.getByRole('button', { name: 'Form', exact: true }).boundingBox(),
			page.getByRole('button', { name: 'JSON', exact: true }).boundingBox(),
		])
		return [f, j].map((b) => [Math.round(b!.x), Math.round(b!.y), Math.round(b!.width), Math.round(b!.height)])
	}
	const before = await modeBox()
	const activeTab = await nav.locator('[aria-selected="true"]').textContent()
	await page.getByRole('button', { name: 'JSON', exact: true }).click()
	const jsonArea = page.getByLabel('Application spec JSON')
	await expect(jsonArea).toBeVisible()
	expect(await modeBox()).toEqual(before)
	// Invalid raw JSON keeps JSON visible — no silent mode swap.
	const validJSON = await jsonArea.inputValue()
	await jsonArea.fill('{')
	await page.getByRole('button', { name: 'Form', exact: true }).click()
	await expect(jsonArea).toBeVisible()
	await jsonArea.fill(validJSON)
	await page.getByRole('button', { name: 'Form', exact: true }).click()
	await expect(jsonArea).toBeHidden()
	// Selected category is restored, headers unchanged.
	await expect(nav.locator('[aria-selected="true"]')).toHaveText(activeTab!)
	expect(await modeBox()).toEqual(before)
	// Groups render as headed bands in order on Runtime.
	await settings.getByRole('tab', { name: 'Runtime' }).click()
	const runtimeGroups = page.locator(".appnb-settings-content[data-state='active'] .appnb-setting-group > .appnb-setting-heading h3")
	await expect(runtimeGroups.first()).toHaveText('Base capabilities')
	await expect(runtimeGroups.nth(1)).toHaveText('Capability configuration')

	// --- Configure extension jump ---
	// Adding needsnet without Network makes the draft invalid; the page
	// notice offers a direct jump even with the catalog closed.
	await page.getByRole('button', { name: 'Settings', exact: true }).click()
	const extRail = page.getByRole('button', { name: 'Extensions', exact: true })
	if (await extRail.getAttribute('aria-pressed') !== 'true') await extRail.click()
	await page.getByRole('button', { name: 'Add needsnet 0.1.0', exact: true }).click()
	const configureExt = page.getByRole('button', { name: 'Configure extension', exact: true })
	await expect(configureExt).toBeVisible({ timeout: 15_000 })
	await expect(page.locator('.appnb-code')).toBeVisible()
	await configureExt.click()
	await expect(settings.getByRole('tab', { name: 'Runtime' })).toHaveAttribute('aria-selected', 'true')
	const needsnetCard = settings.locator('[data-section^="ext:"]', { hasText: 'needsnet' })
	await expect(needsnetCard).toBeVisible()
	await expect(needsnetCard).toBeFocused()
	// Fix it in place: the card's own "Add Network" satisfies the dependency.
	await needsnetCard.getByRole('button', { name: 'Add Network' }).click()
	await page.getByRole('button', { name: 'Settings', exact: true }).click()
	await expect(configureExt).toBeHidden()
})

test('notebook: pinned release resumes the published handler after republish', async () => {
	const create = await client.post('/api/v1/applications', {
		data: { name: 'NbWait' },
	})
	expect(create.status(), `create: ${await create.text()}`).toBe(200)
	// CreateRequest seeds a scaffold main section — replace via update.
	let app = (await create.json()).application
	const put = await client.put(`/api/v1/applications/${app.id}`, {
		data: {
			version: app.version, name: app.name, spec: app.spec,
			sections: [
				{ id: 'main', code: 'def main(input):\n    i = interrupt.new()\n    interrupt.wait(i["id"], handler="done", context={})\n    return {"unreachable": True}\n' },
				{ id: 'handlers', code: 'def done(ctx):\n    return {"marker": "old"}\n' },
			],
		},
	})
	expect(put.status(), `put: ${await put.text()}`).toBe(200)
	app = (await put.json()).application
	const id = app.id
	await publish(id, app.version)
	await waitReady(id)

	const inv = await client.post(`/api/v1/applications/${id}/invoke`, { data: { name: 'a' } })
	expect(inv.status(), `invoke: ${await inv.text()}`).toBe(200)
	const body = await inv.json()
	const runID = body.run_id as string
	const intID = body.waiting.interrupt_id as string

	// Republish a changed handler — the waiting run stays pinned to v1.
	const put2 = await client.put(`/api/v1/applications/${id}`, {
		data: {
			version: app.version, name: app.name, spec: app.spec,
			sections: [
				{ id: 'main', code: 'def main(input):\n    i = interrupt.new()\n    interrupt.wait(i["id"], handler="done", context={})\n    return {"unreachable": True}\n' },
				{ id: 'handlers', code: 'def done(ctx):\n    return {"marker": "new"}\n' },
			],
		},
	})
	expect(put2.status(), `put2: ${await put2.text()}`).toBe(200)
	const app2 = (await put2.json()).application
	await publish(id, app2.version)

	const resume = await client.post(`/api/v1/interrupts/${intID}/resume`, { data: { approved: true } })
	expect(resume.status(), `resume: ${await resume.text()}`).toBe(202)

	await expect.poll(async () => {
		const r = await client.get(`/api/v1/runs/${runID}`)
		return (await r.json()).run?.status
	}, { timeout: 60_000 }).toBe('succeeded')
	const run = await (await client.get(`/api/v1/runs/${runID}`)).json()
	expect(String(run.run.output_json)).toContain('old')
	expect(run.steps.map((s: any) => s.entry)).toContain('done')
})

test('notebook graph: Code / Graph interaction parity on desktop and narrow screens', async ({ page }) => {
	const create = await client.post('/api/v1/applications', {
		data: { name: 'NbGraph' },
	})
	expect(create.status(), `create: ${await create.text()}`).toBe(200)
	const app = (await create.json()).application
	// Enough content per cell for a real code-pane scrollbar.
	const filler = (n: number) => Array.from({ length: 20 }, (_, i) => `pad${n}_${i} = ${i}`).join('\n')
	const put = await client.put(`/api/v1/applications/${app.id}`, {
		data: {
			version: app.version, name: app.name, spec: app.spec,
			sections: [
				{ id: 'first', code: `x = 1\n${filler(1)}\n` },
				{ id: 'middle', code: `y = x + 1\n${filler(2)}\n` },
				{ id: 'main', code: `def main(input):\n    return {"r": y}\n${filler(3)}\n` },
			],
		},
	})
	expect(put.status(), `put: ${await put.text()}`).toBe(200)

	const transform = () =>
		page.locator('.react-flow__viewport').evaluate((el) => (el as HTMLElement).style.transform)
	const headBox = () => page.locator('.nb-head').boundingBox()
	const statusBox = () => page.locator('.nb-statusbar:visible').boundingBox()

	await page.setViewportSize({ width: 1440, height: 1000 })
	await page.goto(`${viteURL}/applications/${app.id}`)
	await expect(page.locator('[aria-label="Application section main"] .cm-content')).toContainText('def main(input)', { timeout: 15_000 })
	const code = page.locator('.appnb-code')

	// Explicit pick, then a real user scroll to the bottom — the scroll
	// spy re-marks the directory and the snapshot survives the toggle.
	await page.getByRole('navigation', { name: 'Section directory' }).getByRole('button', { name: /first/ }).click()
	await expect(page).toHaveURL(/section=first/)
	await code.evaluate((el) => { el.scrollTop = el.scrollHeight })
	await expect.poll(() => code.evaluate((el) => el.scrollTop)).toBeGreaterThan(0)
	const scrolled = await code.evaluate((el) => el.scrollTop)

	const headBefore = await headBox()
	const statusBefore = await statusBox()
	await page.locator('.nb-view-toggle').getByRole('button', { name: 'Graph' }).click()
	const canvas = page.locator('.nb-graph-canvas')
	await expect(canvas).toHaveAttribute('data-fitted', '1')
	// Toolbar and status bar keep their boxes across the view cut (S3).
	expect(await headBox()).toEqual(headBefore)
	expect(await statusBox()).toEqual(statusBefore)

	// The shared toggle distinguishes the track and active segment with
	// flat fills and a hairline, not shadows or gradients.
	const toggleBox = (await page.locator('.nb-view-toggle').boundingBox())!
	const togglePaint = await page.locator('.nb-view-toggle').evaluate((el) => {
		// settle the paint transition so computed styles are final, not mid-flight
		for (const b of el.querySelectorAll('button')) (b as HTMLElement).style.transition = 'none'
		void el.offsetHeight
		const track = getComputedStyle(el)
		const active = getComputedStyle(el.querySelector("button[aria-pressed='true']")!)
		const idle = getComputedStyle(el.querySelector("button[aria-pressed='false']")!)
		return {
			trackBg: track.backgroundColor, trackShadow: track.boxShadow,
			activeShadow: active.boxShadow, activeBg: active.backgroundColor, activeBorder: active.borderTopColor,
			idleShadow: idle.boxShadow, idleBg: idle.backgroundColor, idleBorder: idle.borderTopColor,
			backgroundImages: [track.backgroundImage, active.backgroundImage, idle.backgroundImage],
		}
	})
	expect(togglePaint.trackBg).not.toBe(togglePaint.idleBg)
	expect(togglePaint.trackShadow).toBe('none')
	expect(togglePaint.activeShadow).toBe('none')
	expect(togglePaint.activeBg).not.toBe(togglePaint.trackBg)
	expect(togglePaint.activeBorder).not.toBe(togglePaint.idleBorder)
	expect(togglePaint.idleShadow).toBe('none')
	expect(togglePaint.backgroundImages).toEqual(['none', 'none', 'none'])
	// Selecting the other segment moves the thumb, never the track (S1).
	await page.locator('.nb-view-toggle').getByRole('button', { name: 'Code' }).click()
	expect(await page.locator('.nb-view-toggle').boundingBox()).toEqual(toggleBox)
	await page.locator('.nb-view-toggle').getByRole('button', { name: 'Graph' }).click()

	// Real zoom and pan change the React Flow viewport transform.
	const t0 = await transform()
	await page.locator('.react-flow__controls').getByRole('button', { name: /zoom in/i }).click()
	await expect.poll(transform).not.toBe(t0)
	const t1 = await transform()
	// Pan from blank canvas area (top-left, away from nodes/controls).
	const cb = (await canvas.boundingBox())!
	const px = cb.x + 24, py = cb.y + 24
	await page.mouse.move(px, py)
	await page.mouse.down()
	await page.mouse.move(px - 80, py - 40, { steps: 4 })
	await page.mouse.up()
	await expect.poll(transform).not.toBe(t1)

	// Node select → aside + directory highlight; Clear deselects.
	await page.locator('.react-flow__node', { hasText: 'main' }).click()
	const aside = page.locator('.nb-graph-aside')
	await expect(aside.locator('.nb-graph-detail-name')).toHaveText('main')
	await expect(aside.locator('.nb-graph-reads')).toBeVisible()
	const directory = page.getByRole('navigation', { name: 'Section directory' })
	await expect(directory.getByRole('button', { name: /main/ })).toHaveAttribute('aria-current', 'true')
	await page.locator('.react-flow__node', { hasText: 'first' }).click()
	await expect(directory.getByRole('button', { name: /first/ })).toHaveAttribute('aria-current', 'true')
	await aside.getByRole('button', { name: 'Clear' }).click()
	await expect(page).not.toHaveURL(/section=/)

	// Plain Code return restores the user's own scroll position.
	await page.locator('.nb-view-toggle').getByRole('button', { name: 'Code' }).click()
	await expect.poll(() => code.evaluate((el) => el.scrollTop)).toBe(scrolled)

	// Back to Graph: the mounted viewport keeps the user's pan/zoom.
	const t2 = await transform()
	await page.locator('.nb-view-toggle').getByRole('button', { name: 'Graph' }).click()
	await expect.poll(transform).toBe(t2)

	// Open code lands on the selected card at the top of the pane.
	await page.locator('.react-flow__node', { hasText: 'middle' }).click()
	await aside.getByRole('button', { name: 'Open code' }).click()
	await expect(page.locator('#section-middle')).toBeVisible()
	await expect.poll(async () => {
		const [p, c] = await Promise.all([code.boundingBox(), page.locator('#section-middle').boundingBox()])
		return c!.y - p!.y
	}).toBeLessThanOrEqual(24)

	// --- narrow: Test hides in Graph, the explorer is an overlay ---
	await page.setViewportSize({ width: 390, height: 844 })
	await page.getByRole('button', { name: 'Test', exact: true }).click()
	await expect(page.getByRole('region', { name: 'Test notebook' })).toBeVisible()
	await page.locator('.nb-view-toggle').getByRole('button', { name: 'Graph' }).click()
	await expect(page.locator('.nb-graph')).toBeVisible()
	await expect(page.getByRole('region', { name: 'Test notebook' })).toBeHidden()
	const gb = (await page.locator('.nb-graph-panel').boundingBox())!
	for (const box of [await canvas.boundingBox()!, await page.locator('.nb-graph-aside').boundingBox()!]) {
		expect(box.x).toBeGreaterThanOrEqual(gb.x - 1)
		expect(box.x + box.width).toBeLessThanOrEqual(gb.x + gb.width + 1)
		expect(box.y + box.height).toBeLessThanOrEqual(gb.y + gb.height + 1)
	}
	// Sections overlay opens/closes without resizing the graph canvas.
	const gbBefore = await canvas.boundingBox()
	const sectionsBtn = page.getByRole('button', { name: 'Sections', exact: true })
	if (await sectionsBtn.getAttribute('aria-pressed') !== 'true') await sectionsBtn.click()
	await expect(page.locator('.appnb-explorer')).toBeVisible()
	expect(await canvas.boundingBox()).toEqual(gbBefore)
	await sectionsBtn.click()
	// Node select + zoom still work at 390px.
	await page.locator('.react-flow__node', { hasText: 'main' }).click()
	await expect(aside.locator('.nb-graph-detail-name')).toHaveText('main')
	const tn = await transform()
	await page.locator('.react-flow__controls').getByRole('button', { name: /zoom in/i }).click()
	await expect.poll(transform).not.toBe(tn)
	expect(await page.evaluate(() => window.scrollY)).toBe(0)
	expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
})

test('notebook layout: workbench fills the viewport and panes own scrolling', async ({ page }) => {
	const create = await client.post('/api/v1/applications', {
		data: { name: 'NbLayout' },
	})
	expect(create.status(), `create: ${await create.text()}`).toBe(200)
	const id = (await create.json()).application.id
	await page.goto(`${viteURL}/applications/${id}`)
	await expect(page.locator('[aria-label="Application section main"] .cm-content'))
		.toContainText('def main(input)', { timeout: 15_000 })

	const checkFit = async () => {
		const main = page.locator('.console-main')
		const bench = page.locator('.app-notebook-workbench')
		const [mb, bb] = await Promise.all([main.boundingBox(), bench.boundingBox()])
		expect(Math.abs(mb!.x + mb!.width - (bb!.x + bb!.width))).toBeLessThanOrEqual(2)
		expect(Math.abs(mb!.y + mb!.height - (bb!.y + bb!.height))).toBeLessThanOrEqual(2)
		expect(await main.evaluate((el) => getComputedStyle(el).overflowY)).toBe('hidden')
		// The code pane is the inner scroller; the status bar stays put.
		await expect(page.locator('.nb-statusbar:visible')).toBeVisible()
		expect(await page.locator('.appnb-code').evaluate((el) => getComputedStyle(el).overflowY)).toBe('auto')
	}

	await page.setViewportSize({ width: 1440, height: 1000 })
	await checkFit()
	await page.setViewportSize({ width: 390, height: 844 })
	await checkFit()
	await page.setViewportSize({ width: 1440, height: 1000 })

	// Activity owns its own scroller inside the shared work-panel.
	await page.getByRole('tab', { name: 'Activity' }).click()
	const scroll = page.getByRole('region', { name: 'Runs table' })
	await expect(scroll).toBeVisible()
	expect((await scroll.boundingBox())!.height).toBeGreaterThan(0)
	expect(await scroll.evaluate((el) => getComputedStyle(el).overflowY)).toBe('auto')
	await page.getByRole('tab', { name: 'Editor' }).click()
	await expect(page.locator('[aria-label="Application section main"] .cm-content')).toBeVisible()
})

// Debug entries: every top-level one-argument function gets a gutter ▶
// that runs it verbatim (no input schema, no run record); main keeps
// the contract path.
test('notebook: gutter run on a non-main entry is a debug run', async ({ page }) => {
	const r = await client.post('/api/v1/applications', {
		data: {
			name: 'NbDebug',
			sections: [{ id: 'main', code: 'def double(x):\n    return x * 2\n\ndef main(input):\n    return {"echo": input}\n' }],
		},
	})
	expect(r.ok(), await r.text()).toBeTruthy()
	const id = (await r.json()).application.id
	await page.goto(`${viteURL}/applications/${id}`)
	const sec = page.locator('[aria-label="Application section main"] .cm-content')
	await expect(sec).toContainText('def double(x)')
	// The markers render inside .cm-gutters, which CodeMirror marks
	// aria-hidden — target the buttons by their label attribute.
	await expect(page.locator('button[aria-label="Run double"]')).toBeVisible()
	await expect(page.locator('button[aria-label="Run main"]')).toBeVisible()

	// The gutter marks both entries; clicking "Run double" fires a
	// debug run with the Test panel's current input.
	await page.getByRole('button', { name: 'Test', exact: true }).click()
	const testPanel = page.getByRole('region', { name: 'Test notebook' })
	await expect(testPanel).toBeVisible()
	await page.locator('#app-input').fill('21')
	await page.locator('button[aria-label="Run double"]').click()
	await expect(testPanel.locator('#app-entry')).toHaveValue('double')
	await expect(testPanel.locator('.app-out-pre')).toContainText('42', { timeout: 30_000 })
	await expect(testPanel).toContainText('debug run, not recorded')

	// No run was recorded for the debug call.
	const runs = await (await client.get(`/api/v1/applications/${id}/runs`)).json()
	expect(runs.runs ?? []).toHaveLength(0)

	// Selecting main and running uses the contract path as before.
	await testPanel.locator('#app-entry').selectOption('main')
	await page.locator('#app-input').fill('{"name":"ui"}')
	await page.getByTestId('app-run').click()
	await expect(testPanel.locator('.app-out-pre')).toContainText('"echo"', { timeout: 30_000 })
})
