import { test, expect, type APIRequestContext, type Locator, type Page } from '@playwright/test'
import { cp, mkdir } from 'node:fs/promises'
import { dirname, join } from 'node:path'
import { startApplicationRuntime, type ApplicationRuntime } from './applicationRuntime'

let rt: ApplicationRuntime
let client: APIRequestContext
let viteURL = ''

test.beforeAll(async () => {
  test.setTimeout(300_000)
  rt = await startApplicationRuntime()
  client = rt.client
  viteURL = rt.viteURL
  const extRoot = dirname(dirname(rt.extensionDir))
  await mkdir(join(extRoot, 'charts'), { recursive: true })
  await cp(join(import.meta.dirname, '..', '..', 'contrib', 'charts', '0.1.1'), join(extRoot, 'charts', '0.1.1'), { recursive: true })
  await rt.restartAPI()
})

test.afterAll(async () => { await rt?.close() })

const selectAll = process.platform === 'darwin' ? 'Meta+A' : 'Control+A'

async function createChartsInstance(): Promise<string> {
  const r = await client.post('/api/v1/instances', {
    data: { capabilities: { ext: { charts: { source: 'fixture/charts@0.1.1' } } }, ttl_seconds: 600 },
  })
  expect(r.status()).toBe(201)
  return (await r.json()).instance.id as string
}

async function saveChartDemo(id: string): Promise<void> {
  const nb = await client.get(`/api/v1/instances/${id}/notebook`)
  expect(nb.status()).toBe(200)
  const rev = (await nb.json()).notebook.revision as number
  const r = await client.put(`/api/v1/instances/${id}/sections/chart_demo`, {
    data: { code: '', description: 'Chart demo', revision: rev },
  })
  expect(r.status()).toBe(200)
}

async function completionsReady(id: string): Promise<void> {
  await expect.poll(async () => {
    const r = await client.get(`/api/v1/instances/${id}/completions`)
    if (r.status() !== 200) return false
    const body = await r.json()
    const line = (body.symbols ?? []).find((s: { name?: string }) => s.name === 'ext.charts.line')
    return (line?.param_docs ?? []).some((p: { name?: string; doc?: string }) => p.name === 'x' && String(p.doc ?? '').includes('Horizontal-axis'))
  }, { timeout: 60_000 }).toBe(true)
}

function paramRow(page: Page, name: string): Locator {
  return page.locator('.cm-tooltip-autocomplete > ul > li').filter({
    has: page.locator('.cm-completionLabel').filter({ hasText: new RegExp(`^${name}=$`) }),
  })
}

async function assertUsageRow(page: Page, name: string, axis: string, example: string): Promise<Locator> {
  const row = paramRow(page, name)
  await expect(row).toBeVisible()
  const detail = row.locator('.cm-completionDetail')
  await expect(detail).toContainText(axis)
  await expect(detail).toContainText(example)
  await expect(detail).toContainText('key as a string')
  const info = page.locator('.cm-tooltip.cm-completionInfo')
  await expect(info).toContainText('string, required')
  await expect(info).toContainText(axis)
  await expect(info).toContainText(example)
  return row
}

test('S1/C2: instance chart parameter completion shows usage docs', async ({ page }, testInfo) => {
  test.setTimeout(120_000)
  const id = await createChartsInstance()
  await saveChartDemo(id)
  await completionsReady(id)

  const compReady = page.waitForResponse(
    (r) => r.url().includes(`/api/v1/instances/${id}/completions`) && r.status() === 200,
    { timeout: 60_000 },
  )
  await page.goto(`${viteURL}/instances/${id}`)
  await compReady
  const editor = page.locator('.nb-cards [data-section="chart_demo"] .cm-content')
  await expect(editor).toBeVisible()
  await page.waitForTimeout(400)

  await editor.click()
  await page.keyboard.insertText('ext.charts.line(data=[], ')
  await page.keyboard.type('x')
  await assertUsageRow(page, 'x', 'Horizontal-axis', 'x="day"')

  await page.keyboard.press('Escape')
  await expect(page.locator('.cm-tooltip-autocomplete')).toBeHidden()
  await expect(editor).toContainText('ext.charts.line(data=[], x')

  await page.keyboard.press(selectAll)
  await page.keyboard.insertText('ext.charts.line(data=[], ')
  await page.keyboard.type('y')
  await assertUsageRow(page, 'y', 'Vertical-axis', 'y="sales"')
  await page.keyboard.press('Escape')

  await page.keyboard.press(selectAll)
  await page.keyboard.insertText('ext.charts.line(data=[], ')
  await page.keyboard.press('Control+Space')
  const xRow = paramRow(page, 'x')
  const yRow = paramRow(page, 'y')
  await expect(xRow).toBeVisible()
  await expect(yRow).toBeVisible()
  await expect(xRow.locator('.cm-completionDetail')).toContainText('Horizontal-axis')
  await expect(xRow.locator('.cm-completionDetail')).toContainText('x="day"')
  await expect(yRow.locator('.cm-completionDetail')).toContainText('Vertical-axis')
  await expect(yRow.locator('.cm-completionDetail')).toContainText('y="sales"')

  const before = await xRow.boundingBox()
  await page.waitForTimeout(200)
  await page.keyboard.press('ArrowDown')
  const after = await xRow.boundingBox()
  expect(before).not.toBeNull()
  expect(after).not.toBeNull()
  for (const key of ['x', 'y', 'width', 'height'] as const) {
    expect(Math.abs(after![key] - before![key])).toBeLessThanOrEqual(1)
  }
  const info = page.locator('.cm-tooltip.cm-completionInfo')
  await expect(info).toContainText('string, required')
  await expect(info).toContainText('Horizontal-axis')
  await expect(info).toContainText('x="day"')

  await page.screenshot({ path: testInfo.outputPath('chart-parameter-completion.png') })
})

test('chart parameter completion wraps on mobile', async ({ page }, testInfo) => {
  test.setTimeout(120_000)
  await page.setViewportSize({ width: 390, height: 844 })
  const id = await createChartsInstance()
  await saveChartDemo(id)
  await completionsReady(id)

  const compReady = page.waitForResponse(
    (r) => r.url().includes(`/api/v1/instances/${id}/completions`) && r.status() === 200,
    { timeout: 60_000 },
  )
  await page.goto(`${viteURL}/instances/${id}`)
  await compReady
  const editor = page.locator('.nb-cards [data-section="chart_demo"] .cm-content')
  await expect(editor).toBeVisible()
  await page.waitForTimeout(400)

  await editor.click()
  await page.keyboard.insertText('ext.charts.line(data=[], ')
  await page.keyboard.type('x')
  const row = paramRow(page, 'x')
  await expect(row).toBeVisible()
  const detail = row.locator('.cm-completionDetail')
  await expect(detail).toContainText('Horizontal-axis')
  const metrics = await detail.evaluate((el) => {
    const r = el.getBoundingClientRect()
    return { scrollWidth: el.scrollWidth, clientWidth: el.clientWidth, left: r.left, right: r.right }
  })
  expect(metrics.scrollWidth).toBeLessThanOrEqual(metrics.clientWidth + 1)
  expect(metrics.left).toBeGreaterThanOrEqual(-1)
  expect(metrics.right).toBeLessThanOrEqual(391)
  const box = await row.boundingBox()
  expect(box).not.toBeNull()
  expect(box!.x).toBeGreaterThanOrEqual(-1)
  expect(box!.x + box!.width).toBeLessThanOrEqual(391)
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1)).toBe(true)
  await page.screenshot({ path: testInfo.outputPath('chart-parameter-completion-mobile.png') })
})

test('chart parameter completion in the application editor', async ({ page }) => {
  test.setTimeout(120_000)
  const r = await client.post('/api/v1/applications', {
    data: {
      name: 'Chart params',
      spec: { capabilities: { ext: { charts: { source: 'fixture/charts@0.1.1' } } } },
    },
  })
  expect(r.status()).toBe(200)
  const app = (await r.json()).application

  const catReady = page.waitForResponse(
    (res) => res.url().includes('/api/v1/extensions') && res.status() === 200,
    { timeout: 60_000 },
  )
  await page.goto(`${viteURL}/applications/${app.id}`)
  await catReady
  const editor = page.locator('.app-cm .cm-content').first()
  await expect(editor).toBeVisible()
  await page.waitForTimeout(400)

  await editor.click()
  await page.keyboard.press(selectAll)
  await page.keyboard.insertText('ext.charts.line(data=[], ')
  await page.keyboard.press('Control+Space')

  const xRow = paramRow(page, 'x')
  const yRow = paramRow(page, 'y')
  await expect(xRow).toBeVisible()
  await expect(yRow).toBeVisible()
  await expect(xRow.locator('.cm-completionDetail')).toContainText('Horizontal-axis')
  await expect(xRow.locator('.cm-completionDetail')).toContainText('x="day"')
  await expect(yRow.locator('.cm-completionDetail')).toContainText('Vertical-axis')
  await expect(yRow.locator('.cm-completionDetail')).toContainText('y="sales"')
  await page.waitForTimeout(200)
  await page.keyboard.press('ArrowDown')
  const info = page.locator('.cm-tooltip.cm-completionInfo')
  await expect(info).toContainText('string, required')
  await expect(info).toContainText('Horizontal-axis')
})
