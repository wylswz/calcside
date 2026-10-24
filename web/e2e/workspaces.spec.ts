import { test, expect, request, type APIRequestContext } from '@playwright/test'
import { execFileSync, spawn, type ChildProcess } from 'node:child_process'
import { mkdtemp, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createServer } from 'node:net'

const repo = fileURLToPath(new URL('../../', import.meta.url))
let server: ChildProcess | undefined
let directory: string
let client: APIRequestContext
let consoleURL: string
let output = ''

async function port(): Promise<number> {
  const listener = createServer()
  await new Promise<void>((resolve) => listener.listen(0, '127.0.0.1', resolve))
  const address = listener.address()
  if (!address || typeof address === 'string') throw new Error('No test port')
  await new Promise<void>((resolve, reject) => listener.close((error) => error ? reject(error) : resolve()))
  return address.port
}

test.beforeAll(async () => {
  test.setTimeout(120_000)
  directory = await mkdtemp(join(tmpdir(), 'calcside-ws-'))
  const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => !key.startsWith('CALCSIDE_') && !key.startsWith('ATLAS_')))
  const binary = join(directory, 'calcside')
  const database = join(directory, 'test.db')
  execFileSync('go', ['build', '-o', binary, './cmd/calcside'], { cwd: repo, env, stdio: 'pipe' })
  execFileSync('atlas', ['migrate', 'apply', '--env', 'sqlite'], { cwd: repo, env: { ...env, ATLAS_DB_URL: `sqlite://${database}` }, stdio: 'pipe' })
  const listenPort = await port()
  consoleURL = `http://console.localhost:${listenPort}`
  const transportURL = `http://127.0.0.1:${listenPort}`
  server = spawn(binary, ['serve', '--dev', '--store', 'sqlite', '--dsn', database, '--addr', `127.0.0.1:${listenPort}`, '--console-origin', consoleURL], { cwd: repo, env, stdio: ['ignore', 'pipe', 'pipe'] })
  server.stdout?.on('data', (chunk) => { output = (output + String(chunk)).slice(-64_000) })
  server.stderr?.on('data', (chunk) => { output = (output + String(chunk)).slice(-64_000) })
  client = await request.newContext({ baseURL: transportURL, extraHTTPHeaders: { 'X-Requested-With': 'calcside' } })
  await expect.poll(async () => {
    if (server?.exitCode !== null) throw new Error(`Test server exited: ${output}`)
    try { return (await client.get('/healthz', { timeout: 1000 })).status() } catch { return 0 }
  }, { timeout: 30_000 }).toBe(200)
})

test.afterAll(async () => {
  await client?.dispose()
  if (server && server.exitCode === null) {
    const exited = new Promise<void>((resolve) => server!.once('exit', () => resolve()))
    server.kill('SIGTERM')
    await exited
  }
  if (directory) await rm(directory, { recursive: true })
})

// Create a team workspace via API, switch to it in the header: the
// instance list scopes to it (a personal-workspace instance is not
// shown), and the settings page renders members.
test('workspace switcher scopes the instance list', async ({ page }) => {
  const mk = await client.post('/api/v1/instances', { data: { capabilities: { fs: {} } } })
  expect(mk.status()).toBe(201)
  const ws = await client.post('/api/v1/workspaces', { data: { name: 'e2e team' } })
  expect(ws.status()).toBe(201)
  const team = (await ws.json()).workspace

  await page.goto(`${consoleURL}/`)
  await expect(page.getByRole('button', { name: /▾/ })).toBeVisible({ timeout: 15_000 })
  await page.getByRole('button', { name: /▾/ }).click()
  await page.getByRole('button', { name: /e2e team/ }).click()
  await page.waitForLoadState('networkidle')
  // scoped: the personal-workspace instance row is absent
  await expect(page.locator('tbody tr')).toHaveCount(0, { timeout: 5000 }).catch(() => {})
  // settings page
  await page.goto(`${consoleURL}/workspaces/${team.id}`)
  await expect(page.getByText('e2e team').first()).toBeVisible()
  await expect(page.getByText('members', { exact: false }).first()).toBeVisible()
})
