import { test, expect, type APIRequestContext } from '@playwright/test'
import { startApplicationRuntime, type ApplicationRuntime } from './applicationRuntime'

// EP-0016 interrupt console flows under real Vite + scratch API/SQLite.
// Invariants: S3/S4 (waiting/count boxes keep geometry), S5/C3 (held
// order), F2 (named confirms), P5/C2 (already_resolved in place with
// input kept), C4 (deep links).

const WAITS = `def on_ok(h):
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
		data: { name, code: WAITS },
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
	const { run_id: runID, int_id: intID } = await testRun(appID)

	await page.goto(`${viteURL}/applications/${appID}`)
	await expect(page.locator('section.app-card').filter({ hasText: 'Runs' })).toBeVisible()
	await page.screenshot({ path: '/tmp/calcside-ep16-logs/app-detail.png' })

	await page.goto(`${viteURL}/applications/${appID}/runs/${runID}`)
	await expect(page.getByText('Waiting on — → on_ok')).toBeVisible()
	await page.screenshot({ path: '/tmp/calcside-ep16-logs/run-page.png' })

	// Resume through the named confirm (F2); the box keeps geometry (S3).
	const card = page.locator('section.app-card').filter({ hasText: 'Waiting on' })
	const box = await card.boundingBox()
	await page.locator(`#event-${intID}`).fill('{"approved":true}')
	await card.getByRole('button', { name: 'Resume' }).click()
	await expect(page.getByText('Calls on_ok with this event:')).toBeVisible()
	await page.getByRole('button', { name: 'Resume', exact: true }).last().click()
	const box2 = await card.boundingBox()
	expect(Math.abs((box2?.height ?? 0) - (box?.height ?? 0))).toBeLessThan(80)
	// The submit control reports a waiting state until the phase flips.
	await expect(page.getByText(/Resumed — running handler…|Running — on_ok|Running on_ok/).first()).toBeVisible()
	// The dispatcher runs the handler; the run completes and the
	// handler's return value is the application output.
	await expect.poll(async () => {
		const r = await client.get(`/api/v1/runs/${runID}`)
		return (await r.json()).run.status
	}, { timeout: 30_000 }).toBe('succeeded')
	await expect(page.locator('pre.app-out-pre').filter({ hasText: '"ok":true' })).toBeVisible()

	// Deep link still works off the resumable page (C4).
	await page.goto(`${viteURL}/inbox?interrupt=${intID}`)
	await expect(page.getByText(/Resolved by|View run/).first()).toBeVisible()
	await page.screenshot({ path: '/tmp/calcside-ep16-logs/inbox.png' })
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
	await expect(page.locator('section.app-card').filter({ hasText: 'Runs' })).toBeVisible()
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
	await expect(page.locator('tbody tr')).toHaveCount(2, { timeout: 15_000 })

	// Resolve the newest via the API in the background; its row must
	// keep its box as a placeholder while the other row stays put (S3).
	const before1 = (await page.locator('tbody tr').nth(0).boundingBox())!
	const before2 = (await page.locator('tbody tr').nth(1).boundingBox())!
	const rr = await client.post(`/api/v1/interrupts/${b.int_id}/resume`, { data: { approved: true } })
	expect(rr.status(), `resume: ${await rr.text()}`).toBe(202)
	await expect(page.locator('tbody tr.row-gone')).toContainText('Resolved by', { timeout: 15_000 })
	const after1 = (await page.locator('tbody tr').nth(0).boundingBox())!
	const after2 = (await page.locator('tbody tr').nth(1).boundingBox())!
	for (const dim of ['x', 'y', 'width', 'height'] as const) {
		expect(Math.abs(after1[dim] - before1[dim])).toBeLessThan(2)
		expect(Math.abs(after2[dim] - before2[dim])).toBeLessThan(2)
	}
	await expect(page.locator('tbody tr.row-gone')).toHaveCount(1)
	await expect(page.locator('tbody tr.row-hover')).toHaveCount(1)
	await expect(page.getByRole('link', { name: 'View run' })).toBeVisible()

	// The reveal affordance clears the placeholder.
	await page.getByRole('button', { name: 'Show latest' }).click()
	await expect(page.locator('tbody tr')).toHaveCount(1)
})
