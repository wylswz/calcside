import { test } from '@playwright/test'
import { startApplicationRuntime, type ApplicationRuntime } from './applicationRuntime'

// Optional visual sweep: run with VISUAL_AUDIT=1 to capture the core
// pages in light/dark at desktop width. Screenshots land in Playwright's
// test-results directory for human comparison; CI skips this test.
let rt: ApplicationRuntime

test.describe('visual audit', () => {
  test.skip(process.env.VISUAL_AUDIT !== '1', 'set VISUAL_AUDIT=1 to capture the visual sweep')

  test.beforeAll(async () => {
    test.setTimeout(300_000)
    rt = await startApplicationRuntime()
  })

  test.afterAll(async () => {
    await rt?.close()
  })

  test('core pages', async ({ page }, testInfo) => {
    test.setTimeout(300_000)
    await rt.client.post('/api/v1/instances', {
      data: { ttl_seconds: 600, labels: { name: 'Research session' }, capabilities: { fs: {}, net: { allow_hosts: ['example.com'], methods: ['GET'] } } },
    })
    await rt.client.post('/api/v1/applications', { data: { name: 'Mobile' } })
    await page.setViewportSize({ width: 1280, height: 900 })

    const pages: [string, string][] = [
      ['instances', '/'],
      ['applications', '/applications'],
      ['new-instance', '/instances/new'],
      ['new-application', '/applications/new'],
      ['workspace-members', '/workspace/members'],
      ['workspace-policies', '/workspace/policies'],
      ['workspace-secrets', '/workspace/secrets'],
      ['workspace-providers', '/workspace/providers'],
      ['workspace-keys', '/workspace/keys'],
      ['audit', '/audit'],
      ['inbox', '/inbox'],
    ]
    for (const [name, path] of pages) {
      await page.emulateMedia({ colorScheme: 'light' })
      await page.goto(rt.viteURL + path)
      await page.waitForTimeout(900)
      await page.screenshot({ path: testInfo.outputPath(`${name}-light.png`), fullPage: true })
    }
    for (const [name, path] of [['instances', '/'], ['workspace-policies', '/workspace/policies']] as [string, string][]) {
      await page.emulateMedia({ colorScheme: 'dark' })
      await page.goto(rt.viteURL + path)
      await page.waitForTimeout(900)
      await page.screenshot({ path: testInfo.outputPath(`${name}-dark.png`), fullPage: true })
    }
  })
})
