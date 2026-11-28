import { test, expect } from '@playwright/test'
import { startApplicationRuntime, type ApplicationRuntime } from './applicationRuntime'

let rt: ApplicationRuntime
test.beforeAll(async () => {
  test.setTimeout(300_000)
  rt = await startApplicationRuntime()
})
test.afterAll(async () => { await rt?.close() })

test('application test cases (multi-turn interrupts + strong output) and GitHub CI publish gate', async ({ page }) => {
  const appCode = `def main(inp):
    amt = inp.get("amount", 0)
    if amt <= 0:
        return {"status": "rejected"}
    s = interrupt.new(expires_in_s=60)
    interrupt.wait(s["id"], handler="on_approve", context={"stage": "manager", "amount": amt})

def on_approve(ctx):
    return {
        "status": "approved" if ctx["event"].get("approved") else "denied",
        "amount": ctx["context"]["amount"],
    }
`
  const cr = await rt.client.post('/api/v1/applications', {
    data: { name: 'ExpenseFlow', sections: [{ id: 'main', code: appCode }] },
  })
  expect(cr.status()).toBe(200)
  const app = (await cr.json()).application

  // Seed one passing multi-turn interrupt test case and one failing case via API
  // so we can inspect both the Tests workbench and the GitHub CI publish gate.
  const tc1 = await rt.client.post(`/api/v1/applications/${app.id}/test-cases`, {
    data: {
      name: 'Manager approves expense',
      description: 'Verifies interrupt context and approved final output',
      input: { amount: 420 },
      interrupts: [
        {
          expect_handler: 'on_approve',
          expect_context: { stage: 'manager', amount: 420 },
          context_match_mode: 'exact',
          resolution: 'resolved',
          event: { approved: true },
        },
      ],
      expect_status: 'succeeded',
      expected_output: { status: 'approved', amount: 420 },
      output_match_mode: 'exact',
    },
  })
  expect(tc1.status()).toBe(201)

  const tc2 = await rt.client.post(`/api/v1/applications/${app.id}/test-cases`, {
    data: {
      name: 'Reject zero amount',
      input: { amount: 0 },
      expect_status: 'succeeded',
      expected_output: { status: 'approved' }, // intentionally wrong first
      output_match_mode: 'exact',
    },
  })
  expect(tc2.status()).toBe(201)
  const tc2Id = (await tc2.json()).test_case.id as string

  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto(`${rt.viteURL}/applications/${app.id}?view=tests`)

  // 1. Run all in Tests tab -> 1 passed, 1 failed -> inspect Tests workbench.
  await expect(page.getByText('Manager approves expense')).toBeVisible({ timeout: 15_000 })
  await page.getByTestId('tc-run-all').click()
  await expect(page.getByText('1 failing · 1 passed')).toBeVisible({ timeout: 30_000 })
  await page.screenshot({ path: '/tmp/calcside-tests-tab.png' })

  // 2. Open Review & publish -> GitHub CI checks panel shows 1 failing, Publish disabled.
  await page.getByTestId('app-publish').click()
  const checks = page.getByRole('region', { name: 'Test checks' })
  await expect(checks).toContainText('1 failing, 1 successful checks', { timeout: 30_000 })
  await expect(page.getByTestId('review-publish')).toBeDisabled()
  await page.screenshot({ path: '/tmp/calcside-publish-ci-failed.png' })

  // Close dialog, fix tc2, re-open Publish -> GitHub CI auto-runs -> All 2 checks passed -> Publish!
  await page.keyboard.press('Escape')
  const fix = await rt.client.put(`/api/v1/applications/${app.id}/test-cases/${tc2Id}`, {
    data: {
      name: 'Reject zero amount',
      input: { amount: 0 },
      expect_status: 'succeeded',
      expected_output: { status: 'rejected' },
      output_match_mode: 'exact',
    },
  })
  expect(fix.status()).toBe(200)

  await page.getByTestId('app-publish').click()
  await expect(checks).toContainText('All 2 checks have passed', { timeout: 30_000 })
  await expect(page.getByTestId('review-publish')).toBeEnabled()
  await page.screenshot({ path: '/tmp/calcside-publish-ci-passed.png' })

  await page.getByTestId('review-note').fill('all CI checks passed')
  await page.getByTestId('review-publish').click()
  await expect(page.getByRole('button', { name: 'Open release history' })).toBeVisible({ timeout: 15_000 })
})
