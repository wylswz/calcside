import { test, expect, type APIRequestContext } from '@playwright/test'
import { startApplicationRuntime, type ApplicationRuntime } from './applicationRuntime'

// EP-0016 interrupt console flows under real Vite + scratch API/SQLite.
// Invariants: S3/S4 (waiting/count boxes keep geometry), S5/C3 (held
// order), F2 (named confirms), P5/C2 (already_resolved in place with
// input kept), C4 (deep links).

const WAITS = `def on_ok(h):
    if h["event"].get("chain", False):
        i = interrupt.new()
        interrupt.wait(i["id"], handler="on_ok", context={"x": 2})
    return {"ok": h["event"]["approved"]}
def main(input):
    i = interrupt.new()
    interrupt.wait(i["id"], handler="on_ok", context={"x": 1})
    return {"unreachable": True}
`

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

async function makeApp(name: string): Promise<string> {
	const r = await client.post('/api/v1/applications', {
		data: { name, sections: [{ id: 'main', code: WAITS }] },
	})
	const { application } = await r.json()
	return application.id as string
}

async function testRun(appID: string): Promise<{ run_id: string; int_id: string }> {
	const r = await client.post(`/api/v1/applications/${appID}/test`, {
		data: { version: 1, input: {} },
	})
	const b = await r.json()
	return { run_id: b.run_id, int_id: b.waiting.interrupt_id }
}

test('a) develop waiting -> resume -> handler output on run page', async ({ page }) => {
	const appID = await makeApp('int-e2e')
	for (const viewport of [{ width: 1280, height: 800 }, { width: 390, height: 844 }]) {
		const { run_id: runID, int_id: intID } = await testRun(appID)
		await page.setViewportSize(viewport)
		await page.goto(`${viteURL}/applications/${appID}`)
		await page.getByRole('tablist', { name: 'Application views' }).getByRole('tab', { name: 'Activity' }).click()
		await expect(page.getByRole('region', { name: 'Runs table' })).toBeVisible()
		await page.screenshot({ path: `/tmp/calcside-ep16-logs/app-detail-${viewport.width}.png` })

		await page.goto(`${viteURL}/applications/${appID}/runs/${runID}`)
		const progress = page.getByTestId('run-progress')
		await expect(progress).toBeVisible()
		await expect(progress.locator('.wb-pane-title')).toHaveText('Waiting · on_ok')
		await page.screenshot({ path: `/tmp/calcside-ep16-logs/run-page-${viewport.width}.png` })

		const waiting = await (await client.get(`/api/v1/runs/${runID}`)).json()
		let resumeAccepted = false
		page.on('response', (r) => {
			if (r.request().method() === 'POST' && r.url().includes(`/api/v1/interrupts/${intID}/resume`)) resumeAccepted = true
		})
		await page.route(`**/api/v1/runs/${runID}`, (route) => {
			if (route.request().method() !== 'GET') return route.fallback()
			const body = resumeAccepted
				? { run: { ...waiting.run, status: 'waiting' }, interrupts: (waiting.interrupts ?? []).map((i: any) => ({ ...i, status: 'resolved', can_resume: false })) }
				: waiting
			return route.fulfill({ json: body })
		})

		// Resume through the named confirm (F2); the box keeps geometry (S3).
		await progress.locator(`#event-${intID}`).fill('{"approved":true}')
		const before = await progress.boundingBox()
		expect(before).not.toBeNull()
		await progress.getByRole('button', { name: 'Resume' }).click()
		await expect(page.getByText('Calls on_ok with this event:')).toBeVisible()
		const resumeResp = page.waitForResponse((r) => r.request().method() === 'POST' && r.url().includes(`/api/v1/interrupts/${intID}/resume`))
		await page.getByRole('button', { name: 'Resume', exact: true }).last().click()
		expect((await resumeResp).status()).toBe(202)
		// The submit control reports a waiting state until the phase flips.
		await expect(progress.locator('.wb-pane-title')).toHaveText('Running · on_ok')
		const after = await progress.boundingBox()
		expect(after).not.toBeNull()
		for (const dim of ['x', 'y', 'width', 'height'] as const) {
			expect(Math.abs(after![dim] - before![dim])).toBeLessThanOrEqual(1)
		}

		// The dispatcher runs the handler; the run completes and the
		// handler's return value is the application output.
		await page.unroute(`**/api/v1/runs/${runID}`)
		await expect.poll(async () => {
			const r = await client.get(`/api/v1/runs/${runID}`)
			return (await r.json()).run.status
		}, { timeout: 30_000 }).toBe('succeeded')
		await expect(page.locator('pre.wb-code').filter({ hasText: '"ok":true' })).toBeVisible()

		// Deep link still works off the resumable page (C4).
		await page.goto(`${viteURL}/inbox?interrupt=${intID}`)
		await expect(page.getByText(/Resolved by|View run/).first()).toBeVisible()
		await page.screenshot({ path: `/tmp/calcside-ep16-logs/inbox-${viewport.width}.png` })
	}
})

test('b) already_resolved surfaces in place with input kept (P5/C2)', async ({ page }) => {
	const appID = await makeApp('int-race')
	const { int_id: intID } = await testRun(appID)
	await page.goto(`${viteURL}/inbox?interrupt=${intID}`)
	await page.locator(`#event-${intID}`).fill('{"keep":"me"}')
	// Lose the race via the API.
	await client.post(`/api/v1/interrupts/${intID}/resume`, { data: { approved: true } })
	await page.getByRole('button', { name: 'Resume' }).click()
	await page.getByRole('button', { name: 'Resume', exact: true }).last().click()
	await expect(page.getByText(/Already resumed/).first()).toBeVisible()
	await expect(page.locator(`#event-${intID}`)).toHaveValue('{"keep":"me"}')
})

test('c) cancel run; retry absent while waiting; count slot stable (S4)', async ({ page }) => {
	const appID = await makeApp('int-cancel')
	const { run_id: runID } = await testRun(appID)
	await page.goto(`${viteURL}/applications/${appID}/runs/${runID}`)
	await expect(page.getByRole('button', { name: 'Retry step' })).toHaveCount(0)
	const badge = page.locator('.inbox-count').first()
	const w1 = await badge.boundingBox()
	await page.getByRole('button', { name: 'Cancel run' }).click()
	await page.getByRole('button', { name: 'Cancel run', exact: true }).last().click()
	await expect(page.getByText(/cancelled/).first()).toBeVisible()
	const w2 = await badge.boundingBox()
	expect(Math.abs((w2?.width ?? 0) - (w1?.width ?? 0))).toBeLessThan(2)
})

test('d) settings have no handler declarations', async ({ page }) => {
	const appID = await makeApp('int-no-decl')
	await page.goto(`${viteURL}/applications/${appID}`)
	await page.getByRole('tablist', { name: 'Application views' }).getByRole('tab', { name: 'Activity' }).click()
	await expect(page.getByRole('region', { name: 'Runs table' })).toBeVisible()
	await expect(page.getByRole('button', { name: 'Add handler' })).toHaveCount(0)
	await expect(page.getByText('Handlers', { exact: true })).toHaveCount(0)
})

test('e) resolved row keeps its box until reveal (S3/F1)', async ({ page }) => {
	const appID = await makeApp('int-gone')
	// Two pending interrupts so a vanished row has a neighbour to hold.
	await testRun(appID)
	const b = await testRun(appID)

	page.on('pageerror', (e) => console.log('PAGEERR', e.message))
	await page.goto(`${viteURL}/inbox`)
	await page.bringToFront() // keep the 5s poll ticking while the page sits open
	await expect(page.locator('.inbox-item')).toHaveCount(2, { timeout: 15_000 })

	// Resolve the newest via the API in the background; its row must
	// keep its box as a placeholder while the other row stays put (S3).
	const before1 = (await page.locator('.inbox-item').nth(0).boundingBox())!
	const before2 = (await page.locator('.inbox-item').nth(1).boundingBox())!
	const rr = await client.post(`/api/v1/interrupts/${b.int_id}/resume`, { data: { approved: true } })
	expect(rr.status(), `resume: ${await rr.text()}`).toBe(202)
	await expect(page.locator('.inbox-item-gone')).toContainText('Resolved by', { timeout: 15_000 })
	const after1 = (await page.locator('.inbox-item').nth(0).boundingBox())!
	const after2 = (await page.locator('.inbox-item').nth(1).boundingBox())!
	for (const dim of ['x', 'y', 'width', 'height'] as const) {
		expect(Math.abs(after1[dim] - before1[dim])).toBeLessThan(2)
		expect(Math.abs(after2[dim] - before2[dim])).toBeLessThan(2)
	}
	await expect(page.locator('.inbox-item-gone')).toHaveCount(1)
	await expect(page.locator('.inbox-item:not(.inbox-item-gone)')).toHaveCount(1)
	await expect(page.getByRole('link', { name: 'View run' })).toBeVisible()

	// The reveal affordance clears the placeholder.
	await page.getByRole('button', { name: 'Show latest' }).click()
	await expect(page.locator('.inbox-item')).toHaveCount(1)
})

test('f) invoke waiting phase; inbox context + per-interrupt drafts; in-place outcome', async ({ page }) => {
	const appID = await makeApp('int-qa')
	const a = await testRun(appID)
	const b = await testRun(appID)

	// Publish, then invoke through the UI: the response head reports the
	// run's waiting phase — step 0 returning is not the outcome.
	const app = await (await client.get(`/api/v1/applications/${appID}`)).json()
	const prev = await client.post(`/api/v1/applications/${appID}/publish/preview`, { data: { version: app.application.version } })
	expect(prev.status(), `preview: ${await prev.text()}`).toBe(200)
	const rev = await prev.json()
	const pub = await client.post(`/api/v1/applications/${appID}/publish`, {
		data: { version: app.application.version, expected_base: rev.base?.digest ?? '', candidate: rev.candidate.digest },
	})
	expect(pub.status(), `publish: ${await pub.text()}`).toBe(202)
	await expect.poll(async () => {
		const r = await client.get(`/api/v1/applications/${appID}`)
		return (await r.json()).application.status
	}, { timeout: 90_000 }).toBe('ready')
	await page.goto(`${viteURL}/applications/${appID}?view=invoke`)
	const invokePanel = page.getByRole('tabpanel', { name: 'Invoke', exact: true })
	await invokePanel.getByRole('button', { name: 'Send' }).click()
	await expect(invokePanel.locator('.app-waiting')).toContainText('waiting', { timeout: 30_000 })
	const status = invokePanel.locator('.wb-pane-head .wb-status-cell')
	const runHref = await invokePanel.getByRole('link', { name: 'Open run' }).getAttribute('href')
	const runId = runHref!.split('/').pop()
	const getRun = async () => (await (await client.get(`/api/v1/runs/${runId}`)).json())
	const expectMetrics = async (phase: string, steps: number) => {
		await expect.poll(async () => {
			const { run } = await getRun()
			return { status: run.status, steps: run.steps }
		}).toEqual({ status: phase, steps })
		const run = await getRun()
		const duration = run.steps.reduce((total: number, step: { status: string; duration_ms: number }) =>
			total + (step.status === 'succeeded' ? step.duration_ms : 0), 0)
		await expect(status).toHaveText(`${phase} · ${duration}ms · ${steps} step${steps === 1 ? '' : 's'}`)
	}
	await expectMetrics('waiting', 1)
	const resume = async (event: string) => {
		await invokePanel.getByRole('textbox', { name: /EVENT/ }).fill(event)
		await invokePanel.getByRole('button', { name: 'Resume', exact: true }).click()
		await page.getByRole('dialog').getByRole('button', { name: 'Resume', exact: true }).click()
	}
	const firstInterrupt = (await getRun()).interrupts.find((i: { status: string }) => i.status === 'pending')
	await resume('{"approved":true,"chain":true}')
	await expectMetrics('waiting', 2)
	const secondInterrupt = (await getRun()).interrupts.find((i: { status: string }) => i.status === 'pending')
	expect(secondInterrupt.id).not.toBe(firstInterrupt.id)
	await expect(invokePanel.locator(`#event-${secondInterrupt.id}`)).toHaveValue('{}')
	await resume('{"approved":false}')
	await expectMetrics('succeeded', 3)
	await expect(invokePanel.locator('.app-invoke-output > .wb-pane-body > .app-out-pre')).toContainText('"ok":false')

	// Inbox detail: labelled CONTEXT + RESUME URL blocks render.
	await page.goto(`${viteURL}/inbox?interrupt=${a.int_id}`)
	const detail = page.locator('.inbox-detail')
	await expect(detail.locator('.int-ctx-title', { hasText: 'Context' })).toBeVisible()
	await expect(detail.locator('.int-ctx-pre').first()).toContainText('"x"')
	await expect(detail.locator('.int-ctx-title', { hasText: 'Resume URL' })).toBeVisible()
	await expect(detail.locator('.int-ctx-pre').last()).toContainText('POST ')

	// Each interrupt keeps its own event draft — switching selection
	// remounts the form instead of leaking the previous event.
	await detail.locator(`#event-${a.int_id}`).fill('{"draft":"A"}')
	await page.getByRole('button', { name: new RegExp(b.run_id) }).click()
	await expect(page.locator(`#event-${b.int_id}`)).toHaveValue('{}')
	await page.locator(`#event-${b.int_id}`).fill('{"draft":"B"}')
	await page.getByRole('button', { name: new RegExp(a.run_id) }).click()
	await expect(page.locator(`#event-${a.int_id}`)).toHaveValue('{"draft":"A"}')

	// Resume in place: the detail flips to the resolved view with the
	// live run phase and View run — no navigation away from the inbox.
	await page.locator(`#event-${a.int_id}`).fill('{"approved":true}')
	await page.getByRole('button', { name: 'Resume' }).click()
	await page.getByRole('button', { name: 'Resume', exact: true }).last().click()
	const resolved = page.locator('.inbox-resolved')
	await expect(resolved).toContainText(/Resolved by/, { timeout: 15_000 })
	await expect(resolved).toContainText(/Run succeeded/, { timeout: 30_000 })
	await expect(resolved.getByRole('link', { name: 'View run' })).toBeVisible()
	await expect(page).toHaveURL(/\/inbox/)
})
