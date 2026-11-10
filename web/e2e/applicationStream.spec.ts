import { test, expect, type APIRequestContext, type Page } from '@playwright/test'
import { startApplicationRuntime, type ApplicationRuntime } from './applicationRuntime'
import { createServer, type Server, type ServerResponse } from 'node:http'

let rt: ApplicationRuntime
let client: APIRequestContext
let viteURL = ''
let gatePort = 0
let gatePending: ServerResponse[] = []
let gate: Server

test.beforeAll(async () => {
  test.setTimeout(300_000)
  gate = createServer((_req, res) => {
    gatePending.push(res)
    res.on('close', () => { gatePending = gatePending.filter((x) => x !== res) })
  })
  await new Promise<void>((r) => gate.listen(0, '127.0.0.1', r))
  gatePort = (gate.address() as { port: number }).port
  rt = await startApplicationRuntime()
  client = rt.client
  viteURL = rt.viteURL
})

test.afterAll(async () => {
  for (const r of gatePending) { try { r.writeHead(200); r.end('ok') } catch { /* closed */ } }
  gate?.close()
  await rt?.close()
})

test.describe.configure({ mode: 'serial' })

const releaseGate = () => {
  for (const r of gatePending) { try { r.writeHead(200, { 'Content-Type': 'text/plain' }); r.end('ok') } catch { /* closed */ } }
  gatePending = []
}

const streamCode = (gate: string) => `def main(input):
    state = {"i": 0}
    def nxt():
        state["i"] += 1
        i = state["i"]
        if i == 1:
            return (True, {"n": 1})
        if i == 2:
            r = net.stream("GET", "${gate}")
            r["body"].close()
            return (True, {"n": 2})
        return (False, None)
    return stream.new(nxt)
`

const failCode = `def main(input):
    return stream.from_list([{"ok": True}, 7])
`

async function createApp(code: string, opts: { name: string; outputSchema?: string; spec?: object }): Promise<string> {
  const spec = opts.spec ?? { capabilities: { net: { allow_hosts: [`127.0.0.1:${gatePort}`] } } }
  const r = await client.post('/api/v1/applications', {
    data: {
      name: opts.name, code,
      input_schema: '{"type":"object"}',
      output_schema: opts.outputSchema,
      spec,
    },
  })
  const body = await r.json()
  expect(r.status(), JSON.stringify(body)).toBe(200)
  return body.application.id
}

const wbBounds = async (page: Page) => {
  const res = await box(page, '.app-resources')
  const frame = await box(page, '.app-editor-frame')
  if (!res || !frame) return { aligned: false, res, frame }
  return {
    aligned: Math.abs(res.y - frame.y) <= 1 && Math.abs(res.y + res.h - frame.y - frame.h) <= 1,
    res, frame,
  }
}

const box = async (page: Page, sel: string) => {
  const b = await page.locator(sel).boundingBox()
  return b && { x: Math.round(b.x), y: Math.round(b.y), w: Math.round(b.width), h: Math.round(b.height) }
}

async function publishViaDialog(page: Page) {
  await page.getByTestId('app-publish').click()
  await page.getByTestId('review-publish').click()
}

async function waitReady(id: string) {
  await expect.poll(async () => (await (await client.get(`/api/v1/applications/${id}`)).json()).application.status, { timeout: 90_000 }).toBe('ready')
}

test('a) Develop Run streams prefix before gate release; S3/S4 boxes stable; publish+invoke stream too', async ({ page }) => {
  const id = await createApp(streamCode(`http://127.0.0.1:${gatePort}/`), { name: 'Streamer' })
  await page.goto(`${viteURL}/applications/${id}`)
  await expect(page.locator('.app-cm')).toContainText('stream.new')

  const head0 = await box(page, '.app-out-head >> nth=0')
  const pre0 = await box(page, '.app-out-pre:not(.app-call-out)')
  const stop0 = await box(page, '[data-testid="app-stream-stop-slot"]')
  const meta0 = await box(page, '[data-testid="app-stream-meta"]')

  await page.getByRole('button', { name: 'Run' }).click()
  await expect(page.locator('.app-out-pre:not(.app-call-out)')).toContainText('{"n":1}', { timeout: 30_000 })
  await expect(page.locator('[data-testid="app-stream-meta"]')).toContainText('Streaming')
  await expect(page.locator('[data-testid="app-stream-meta"]')).not.toContainText('Completed')
  await expect(page.locator('.app-out-pre:not(.app-call-out)')).not.toContainText('{"n":2}')
  await expect.poll(() => gatePending.length, { timeout: 15_000 }).toBe(1)
  expect((await wbBounds(page)).aligned).toBe(true)

  expect(await box(page, '.app-out-head >> nth=0')).toEqual(head0)
  expect(await box(page, '.app-out-pre:not(.app-call-out)')).toEqual(pre0)
  expect(await box(page, '[data-testid="app-stream-stop-slot"]')).toEqual(stop0)
  expect(await box(page, '[data-testid="app-stream-meta"]')).toEqual(meta0)
  await page.locator('.app-out >> nth=0').screenshot({ path: 'test-results/stream-prefix.png' })
  await page.evaluate(() => window.scrollTo(0, 0))

  releaseGate()
  await expect(page.locator('[data-testid="app-stream-meta"]')).toContainText('2 items', { timeout: 30_000 })
  await expect(page.locator('[data-testid="app-stream-meta"]')).toContainText('Completed')
  await expect(page.locator('.app-out-pre:not(.app-call-out)')).toContainText('{"n":2}')

  expect(await box(page, '.app-out-head >> nth=0')).toEqual(head0)
  expect(await box(page, '.app-out-pre:not(.app-call-out)')).toEqual(pre0)
  await page.locator('.app-out >> nth=0').screenshot({ path: 'test-results/stream-completed.png' })
  await page.evaluate(() => window.scrollTo(0, 0))

  await publishViaDialog(page)
  await waitReady(id)
  await page.getByRole('tablist', { name: 'Application views' }).getByRole('tab', { name: 'Invoke' }).click()
  const invokeCard = page.getByRole('tabpanel', { name: 'Invoke', exact: true })
  await expect(invokeCard.getByRole('button', { name: 'Invoke' })).toBeEnabled({ timeout: 15_000 })
  await invokeCard.getByRole('button', { name: 'Invoke' }).click()
  await expect(invokeCard.locator('.app-call-out')).toContainText('{"n":1}', { timeout: 30_000 })
  await expect(page.locator('[data-testid="app-call-stream-meta"]')).not.toContainText('Completed')
  await expect.poll(() => gatePending.length, { timeout: 15_000 }).toBe(1)
  releaseGate()
  await expect(page.locator('[data-testid="app-call-stream-meta"]')).toContainText('Completed', { timeout: 30_000 })
  await expect(invokeCard.locator('.app-call-out')).toContainText('{"n":2}')
})

test('b) schema failure mid-stream: prefix retained, Failed status, error frame without done', async ({ page }) => {
  const id = await createApp(failCode, { name: 'Breaker', outputSchema: '{"type":"object"}', spec: {} })

  const r = await client.post(`/api/v1/applications/${id}/test`, {
    data: { version: 1, input: {} },
    headers: { 'Content-Type': 'application/json' },
  })
  expect(r.status()).toBe(200)
  expect(r.headers()['content-type']).toContain('text/event-stream')
  const body = await r.text()
  expect(body).toContain('event: item\ndata: {"ok":true}')
  expect(body).toContain('event: error')
  expect(body).not.toContain('event: done')

  await page.goto(`${viteURL}/applications/${id}`)
  await page.getByRole('button', { name: 'Run' }).click()
  await expect(page.locator('.app-out-pre:not(.app-call-out)')).toContainText('{"ok":true}', { timeout: 30_000 })
  await expect(page.locator('[data-testid="app-stream-meta"]')).toContainText('Failed')
  expect((await wbBounds(page)).aligned).toBe(true)
  await page.locator('.app-out >> nth=0').screenshot({ path: 'test-results/stream-failed.png' })
})

test('c) Stop aborts stream; history stats-only; rerun and JSON regression', async ({ page }) => {
  const id = await createApp(streamCode(`http://127.0.0.1:${gatePort}/`), { name: 'Stoppable' })
  await page.goto(`${viteURL}/applications/${id}`)

  await page.getByRole('button', { name: 'Run' }).click()
  await expect(page.locator('.app-out-pre:not(.app-call-out)')).toContainText('{"n":1}', { timeout: 30_000 })
  await expect.poll(() => gatePending.length, { timeout: 15_000 }).toBe(1)

  await page.getByTestId('app-stream-stop').click()
  await page.getByRole('button', { name: 'Stop', exact: true }).last().click()
  await expect(page.locator('[data-testid="app-stream-meta"]')).toContainText('Stopped', { timeout: 30_000 })
  expect((await wbBounds(page)).aligned).toBe(true)
  await expect(page.locator('.app-out-pre:not(.app-call-out)')).toContainText('{"n":1}')
  await page.locator('.app-out >> nth=0').screenshot({ path: 'test-results/stream-stopped.png' })

  await expect.poll(() => gatePending.length, { timeout: 20_000 }).toBe(0)

  const runs = await client.get(`/api/v1/applications/${id}/runs`)
  const rows = (await runs.json()).runs ?? []
  const run = rows.find((x: any) => x.streaming)
  expect(run).toBeTruthy()
  expect(['unknown', 'failed', 'cancelled']).toContain(run.status)
  const detail = await client.get(`/api/v1/runs/${run.id}`)
  const rd = (await detail.json()).run
  expect(rd.streaming).toBe(true)
  expect(rd.output_json ?? null).toBeNull()

  await page.goto(`${viteURL}/applications/${id}/runs/${run.id}`)
  await expect(page.getByText(/not retained/)).toBeVisible({ timeout: 15_000 })

  await page.goto(`${viteURL}/applications/${id}`)
  await page.getByRole('button', { name: 'Run' }).click()
  await expect(page.locator('.app-out-pre:not(.app-call-out)')).toContainText('{"n":1}', { timeout: 30_000 })
  await expect.poll(() => gatePending.length, { timeout: 15_000 }).toBe(1)
  releaseGate()
  await expect(page.locator('[data-testid="app-stream-meta"]')).toContainText('Completed', { timeout: 30_000 })

  const code = page.locator('.app-cm .cm-content').first()
  await code.click()
  await page.keyboard.press('Meta+A')
  await page.keyboard.type('def main(input): return {"ok": True, "n": 42}')
  await page.keyboard.press('ControlOrMeta+s')
  await expect.poll(async () => (await (await client.get(`/api/v1/applications/${id}`)).json()).application.code, { timeout: 15_000 }).toContain('"n": 42')
  await page.getByRole('button', { name: 'Run' }).click()
  await expect(page.locator('.app-out-pre:not(.app-call-out)')).toContainText('"n":42', { timeout: 30_000 })
  await expect(page.locator('[data-testid="app-stream-meta"]')).toHaveText('')
})

test('d) default output schema: mixed scalar stream + integrated IO layout (S3/S4)', async ({ page }) => {
  const id = await createApp(`def main(input):
    return stream.from_list([1, "x", None, True, {"n": 1}, [2, 3]])
`, { name: 'DefaultOut' })
  await page.goto(`${viteURL}/applications/${id}`)

  await expect(page.locator('.app-editor-frame #app-input')).toHaveCount(1)
  await expect(page.locator('.app-editor-frame [data-testid="app-run"]')).toHaveCount(1)
  await expect(page.locator('.app-editor-frame .app-out >> nth=0')).toHaveCount(1)
  await expect(page.locator('.editor-footer')).toHaveCount(0)
  await expect(page.getByText('Ctrl+Space')).toHaveCount(0)

  expect(await page.locator('.app-io-toolbar [data-testid="app-run"]').count()).toBe(0)
  const toolbarRun = page.locator('.app-editor-toolbar [data-testid="app-run"]')
  await expect(toolbarRun).toHaveCount(1)
  await expect(toolbarRun).toHaveAttribute('aria-label', 'Run')
  const runBox = await toolbarRun.boundingBox()
  const toolbarBox = await box(page, '.app-editor-toolbar')
  expect(runBox && toolbarBox).toBeTruthy()
  expect(Math.abs(runBox!.width - runBox!.height)).toBeLessThanOrEqual(2)
  expect(runBox!.x + runBox!.width).toBeGreaterThan(toolbarBox!.x + toolbarBox!.w - 40)
  expect((await wbBounds(page)).aligned).toBe(true)
  const inputTop = await box(page, '#app-input')
  const outTop = await box(page, '.app-out-pre:not(.app-call-out)')
  expect(inputTop && outTop && Math.abs(inputTop.y - outTop.y) <= 2).toBeTruthy()
  expect(await page.locator('.editor-footer').count()).toBe(0)

  const run0 = await box(page, '[data-testid="app-run"]')
  const head0 = await box(page, '.app-out-head >> nth=0')
  const pre0 = await box(page, '.app-out-pre:not(.app-call-out)')

  await page.getByTestId('app-run').click()
  const pre = page.locator('.app-out-pre:not(.app-call-out)')
  await expect(pre).toContainText('1', { timeout: 30_000 })
  await expect(pre).toContainText('"x"')
  await expect(pre).toContainText('null')
  await expect(pre).toContainText('true')
  await expect(pre).toContainText('{"n":1}')
  await expect(pre).toContainText('[2,3]')
  await expect(page.locator('[data-testid="app-stream-meta"]')).toContainText('6 items')
  await expect(page.locator('[data-testid="app-stream-meta"]')).toContainText('Completed')

  expect((await wbBounds(page)).aligned).toBe(true)
  expect(await box(page, '[data-testid="app-run"]')).toEqual(run0)
  expect(await box(page, '.app-out-head >> nth=0')).toEqual(head0)
  expect(await box(page, '.app-out-pre:not(.app-call-out)')).toEqual(pre0)

  await page.locator('.app-workbench').screenshot({ path: 'test-results/io-integrated-desktop.png' })

  // S3: the resource body must contribute height when the columns stack.
  await page.setViewportSize({ width: 820, height: 1000 })
  const resources = await box(page, '.app-resources')
  const frame = await box(page, '.app-editor-frame')
  expect(resources && frame && resources.y + resources.h <= frame.y).toBeTruthy()
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(820)
  await page.locator('.app-workbench').screenshot({ path: 'test-results/io-integrated-820.png' })

  await page.setViewportSize({ width: 390, height: 844 })
  await expect(page.locator('#app-input')).toBeVisible()
  await expect(page.getByTestId('app-run')).toBeVisible()
  await expect(pre).toBeVisible()
  const mRun = await box(page, '[data-testid="app-run"]')
  expect(mRun && Math.abs(mRun.w - mRun.h) <= 2).toBeTruthy()
  const mToolbar = await box(page, '.app-editor-toolbar')
  expect(mRun && mToolbar && mRun.y >= mToolbar.y && mRun.y + mRun.h <= mToolbar.y + mToolbar.h).toBeTruthy()
  const scrollW = await page.evaluate(() => document.documentElement.scrollWidth)
  expect(scrollW).toBeLessThanOrEqual(390)
  await page.evaluate(() => document.querySelector('.app-workbench')!.scrollIntoView({ block: 'start' }))
  await page.locator('.app-workbench').screenshot({ path: 'test-results/io-integrated-mobile.png' })
  await page.setViewportSize({ width: 1280, height: 900 })

  await publishViaDialog(page)
  await waitReady(id)
  await page.getByRole('tab', { name: 'Invoke' }).click()
  await page.getByRole('button', { name: 'Invoke', exact: true }).click()
  const callPre = page.locator('.app-call-out')
  await expect(callPre).toContainText('"x"', { timeout: 30_000 })
  await expect(callPre).toContainText('[2,3]')
  await expect(page.locator('[data-testid="app-call-stream-meta"]')).toContainText('Completed')
})
