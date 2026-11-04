import { test, expect, type Page } from '@playwright/test'
import { startApplicationRuntime } from './applicationRuntime'

test.setTimeout(120_000)

const noOverflow = async (page: Page) =>
  page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)

test('console shell: desktop sidebar, groups, active state, account menu', async ({ page }) => {
  const rt = await startApplicationRuntime()
  try {
    await page.setViewportSize({ width: 1440, height: 900 })
    const cr = await rt.client.post('/api/v1/modules', { data: { name: 'm1', files: { 'main.star': 'x = 1' } } })
    const mid = (await cr.json()).module.id
    await page.goto(`${rt.viteURL}/modules/${mid}`)
    // sidebar landmark
    const nav = page.locator('aside').getByRole('navigation', { name: 'Primary' })
    await expect(nav).toBeVisible()
    // header has no top nav links
    const header = page.locator('header.console-header')
    for (const l of ['Instances', 'Applications', 'Modules', 'Inbox', 'Audit']) {
      await expect(header.getByRole('link', { name: l, exact: true })).toHaveCount(0)
    }
    // all six destinations in sidebar
    for (const l of ['Instances', 'Applications', 'Modules', 'Inbox', 'Audit', 'Workspace']) {
      await expect(nav.getByRole('link', { name: l })).toBeVisible()
    }
    // groups
    for (const g of ['Build & run', 'Observe', 'Manage']) {
      await expect(page.locator('.shell-group-label', { hasText: g })).toBeVisible()
    }
    // active state on nested module detail
    await expect(nav.getByRole('link', { name: 'Modules' })).toHaveAttribute('aria-current', 'page')
    // S1: nav link box unchanged under hover
    const mod = nav.getByRole('link', { name: 'Modules' })
    const before = await mod.boundingBox()
    await mod.hover()
    await page.waitForTimeout(100)
    expect(await mod.boundingBox()).toEqual(before)
    // six real hrefs
    const hrefs = await nav.getByRole('link').evaluateAll((els) => els.map((e) => e.getAttribute('href')))
    for (const h of ['/', '/applications', '/modules', '/inbox', '/audit', '/workspace']) {
      expect(hrefs, `missing ${h}`).toContain(h)
    }
    // real colors — rgb() computed, so invalid token CSS can't slip
    const bg = await page.locator('aside.shell-sidebar').evaluate((el) => getComputedStyle(el).backgroundColor)
    expect(bg).toMatch(/^rgba?\(/)
    const activeBg = await mod.evaluate((el) => getComputedStyle(el).backgroundColor)
    expect(activeBg).toMatch(/^rgba?\(/)
    await page.screenshot({ path: '/tmp/calcside-shell-desktop.png' })
    // account menu → Profile
    await page.getByLabel(/^Account /).click()
    await page.getByRole('menuitem', { name: 'Profile' }).click()
    await expect(page).toHaveURL(/profile/)
    expect(await noOverflow(page)).toBe(true)
  } finally { await rt.close() }
})

test('console shell: mobile drawer opens/closes, nav works', async ({ page }) => {
  const rt = await startApplicationRuntime()
  try {
    await page.setViewportSize({ width: 390, height: 844 })
    await page.goto(`${rt.viteURL}/modules`)
    // sidebar hidden; hamburger opens drawer
    await expect(page.locator('aside.shell-sidebar')).toBeHidden()
    const open = page.getByLabel('Open navigation')
    await open.click()
    const dlg = page.getByRole('dialog')
    await expect(dlg).toBeVisible()
    const nav = dlg.getByRole('navigation', { name: 'Primary' })
    await expect(nav.getByRole('link', { name: 'Modules' })).toBeVisible()
    // drawer is actually left-bound, not centered
    await page.waitForTimeout(250) // entrance animation settles
    const geo = await dlg.evaluate((el) => {
      const r = el.getBoundingClientRect()
      return { x: r.x, top: r.top, h: r.height, w: r.width, vh: window.innerHeight }
    })
    expect(geo.x).toBe(0)
    expect(geo.top).toBe(0)
    expect(geo.h).toBe(geo.vh)
    expect(geo.w).toBeLessThanOrEqual(280)
    await page.screenshot({ path: '/tmp/calcside-shell-mobile.png' })
    // Escape closes; focus returns to the trigger
    await page.keyboard.press('Escape')
    await expect(dlg).toHaveCount(0)
    await expect(open).toBeFocused()
    // reopen → click Modules → drawer closes + navigates
    await open.click()
    await page.getByRole('dialog').getByRole('link', { name: 'Modules' }).click()
    await expect(page.getByRole('dialog')).toHaveCount(0)
    await expect(page).toHaveURL(/modules/)
    
    expect(await noOverflow(page)).toBe(true)
  } finally { await rt.close() }
})

test('console shell: drawer closes on widen; one nav landmark; edits survive', async ({ page }) => {
  const rt = await startApplicationRuntime()
  try {
    await page.setViewportSize({ width: 390, height: 844 })
    const cr = await rt.client.post('/api/v1/modules', { data: { name: 'm2', files: { 'main.star': 'x = 1' } } })
    const mid = (await cr.json()).module.id
    await page.goto(`${rt.viteURL}/modules/${mid}`)
    const cm = page.getByTestId('module-editor').locator('.cm-content')
    await cm.click(); await page.keyboard.press('Meta+A'); await page.keyboard.type('y = 2')
    const caller = page.getByTestId('debug-script').locator('.cm-content')
    await caller.click(); await page.keyboard.press('Meta+A'); await page.keyboard.type('print("caller-marker")')
    // open drawer, Escape — both buffers preserved
    await page.getByLabel('Open navigation').click()
    await page.keyboard.press('Escape')
    await expect(cm).toHaveText(/y = 2/)
    await expect(caller).toHaveText(/caller-marker/)
    // reopen, then WIDEN — drawer must close on its own
    await page.getByLabel('Open navigation').click()
    await expect(page.getByRole('dialog')).toBeVisible()
    await page.setViewportSize({ width: 1440, height: 900 })
    await expect(page.getByRole('dialog')).toHaveCount(0)
    const landmarks = page.getByRole('navigation', { name: 'Primary' })
    await expect(landmarks).toHaveCount(1)
    // shrink back — markers still there
    await page.setViewportSize({ width: 390, height: 844 })
    await expect(cm).toHaveText(/y = 2/)
    await expect(caller).toHaveText(/caller-marker/)
    expect(await noOverflow(page)).toBe(true)
  } finally { await rt.close() }
})

test('console shell: long names do not overflow header', async ({ page }) => {
  const rt = await startApplicationRuntime()
  try {
    const long = 'verylongworkspacename-that-never-breaks-and-keeps-going-abcdef0123456789'
    const ws = await rt.client.post('/api/v1/workspaces', { data: { name: long } })
    expect(ws.status()).toBe(201)
    await page.goto(`${rt.viteURL}/modules`)
    // mock a long account display name (same IDs/permissions)
    await page.route('**/api/v1/me', async (r) => {
      const res = await r.fetch()
      const j = await res.json()
      if (j.user) j.user.username = 'averylongusernamethat-never-breaks-0123456789abcdef@example.com'
      await r.fulfill({ json: j })
    })
    // switcher opens its menu, SELECT the long workspace, header shows it
    await page.locator('.ws-switcher button').first().click()
    await page.getByRole('menuitem', { name: new RegExp(long) }).click()
    await expect(page.locator('.ws-switcher .ws-name').first()).toContainText(long, { timeout: 15_000 })
    for (const vp of [{ width: 390, height: 844 }, { width: 768, height: 900 }, { width: 1024, height: 900 }, { width: 1440, height: 900 }]) {
      await page.setViewportSize(vp)
      await expect(page.locator('header.console-header')).toBeVisible()
      expect(await noOverflow(page), `overflow at ${vp.width}px`).toBe(true)
    }
  } finally { await rt.close() }
})
