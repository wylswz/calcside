import { test, expect } from '@playwright/test'
import { startApplicationRuntime, type ApplicationRuntime } from './applicationRuntime'

let rt: ApplicationRuntime
test.beforeAll(async () => {
  test.setTimeout(300_000)
  rt = await startApplicationRuntime()
})
test.afterAll(async () => { await rt?.close() })

test('expired instance next-action bar is flush and icon-aligned', async ({ page }) => {
  const cr = await rt.client.post('/api/v1/instances', {
    data: { labels: { name: 'Expired check' }, capabilities: { fs: {} } },
  })
  expect(cr.status()).toBe(201)
  const inst = (await cr.json()).instance

  // Render the exact expired state so we can assert both geometry and pixels.
  await page.route(`**/api/v1/instances/${inst.id}`, async (route) => {
    const resp = await route.fetch()
    const body = await resp.json()
    body.instance.status = 'expired'
    await route.fulfill({ response: resp, json: body })
  })

  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto(`${rt.viteURL}/instances/${inst.id}`)
  const strip = page.locator('.next-action')
  await expect(strip.locator('.next-title')).toHaveText('This instance expired.')

  const metrics = await page.evaluate(() => {
    const bar = document.querySelector('.next-action')!
    const sig = document.querySelector('.next-action .next-signal svg')!
    const title = document.querySelector('.next-action .next-title')!
    const bs = getComputedStyle(bar)
    const br = bar.getBoundingClientRect()
    const sr = sig.getBoundingClientRect()
    const tr = title.getBoundingClientRect()
    return {
      borderRadius: bs.borderRadius,
      borderTopWidth: bs.borderTopWidth,
      sigCenterY: sr.top + sr.height / 2,
      titleCenterY: tr.top + tr.height / 2,
      barCenterY: br.top + br.height / 2,
    }
  })
  expect(metrics.borderRadius).toBe('0px')
  expect(metrics.borderTopWidth).toBe('0px')
  expect(Math.abs(metrics.sigCenterY - metrics.titleCenterY)).toBeLessThanOrEqual(1.5)
  expect(Math.abs(metrics.sigCenterY - metrics.barCenterY)).toBeLessThanOrEqual(1.5)

  await page.screenshot({ path: '/tmp/calcside-ep26-expired-instance.png' })
})

test('EP-0026: readiness check -> publish -> history -> diff -> rollback', async ({ page }) => {
  const cr = await rt.client.post('/api/v1/applications', {
    data: {
      name: 'ReleaseFlow',
      sections: [{ id: 'main', code: 'def main(input):\n    return {"msg": "v1 " + input.get("name", "world")}\n' }],
    },
  })
  expect(cr.status()).toBe(200)
  const app = (await cr.json()).application

  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto(`${rt.viteURL}/applications/${app.id}`)
  await expect(page.locator('[aria-label="Application section main"]')).toContainText('def main(input)')
  await page.screenshot({ path: '/tmp/calcside-ep26-app-editor.png' })

  // 1. Open Review & publish -> Readiness + Diff -> Publish v1
  await page.getByTestId('app-publish').click()
  await expect(page.getByText(/candidate rel_/)).toBeVisible({ timeout: 15_000 })
  await page.screenshot({ path: '/tmp/calcside-ep26-review-dialog.png' })

  await page.getByTestId('review-note').fill('initial release')
  await page.getByTestId('review-publish').click()
  await expect(page.getByRole('button', { name: 'Open release history' })).toBeVisible({ timeout: 15_000 })

  // 2. Update draft via API and publish v2
  const cur = await (await rt.client.get(`/api/v1/applications/${app.id}`)).json()
  const curVer = cur.application.version as number
  const put = await rt.client.put(`/api/v1/applications/${app.id}`, {
    data: {
      version: curVer,
      name: 'ReleaseFlow',
      sections: [{ id: 'main', code: 'def main(input):\n    return {"msg": "v2 " + input.get("name", "world")}\n' }],
    },
  })
  expect(put.status()).toBe(200)
  const v2Ver = (await put.json()).application.version as number
  const prev2 = await rt.client.post(`/api/v1/applications/${app.id}/publish/preview`, { data: { version: v2Ver } })
  const p2Body = await prev2.json()
  const pub2 = await rt.client.post(`/api/v1/applications/${app.id}/publish`, {
    data: { version: v2Ver, expected_base: p2Body.base.digest, candidate: p2Body.candidate.digest, note: 'second release' },
  })
  expect(pub2.status()).toBe(202)

  // 3. Open History tab -> inspect v2 & v1 -> Diff -> Rollback to v1
  await page.getByRole('tab', { name: 'History', exact: true }).click()
  const table = page.getByRole('region', { name: 'Releases table' })
  await expect(table).toContainText('v2', { timeout: 15_000 })
  await expect(table).toContainText('initial release')
  await page.screenshot({ path: '/tmp/calcside-ep26-history.png' })

  // Rollback to v1
  const v1Row = table.locator('tbody tr', { hasText: 'initial release' })
  await v1Row.getByRole('button', { name: 'Rollback…' }).click()
  const confirm = page.getByRole('dialog', { name: /Rollback live pointer to v1/ })
  await expect(confirm).toBeVisible()
  await confirm.getByRole('button', { name: 'Rollback live' }).click()
  await expect(table).toContainText('v3', { timeout: 15_000 })
  await expect(table).toContainText('rollback → v1')
})
