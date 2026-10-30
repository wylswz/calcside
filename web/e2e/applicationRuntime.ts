import { request, type APIRequestContext } from '@playwright/test'
import { execFileSync, spawn, type ChildProcess } from 'node:child_process'
import { mkdtemp, rm, mkdir, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { createServer as tcpServer } from 'node:net'
import { createServer as httpServer, request as httpRequest, type Server as HTTPServer } from 'node:http'
import { randomBytes } from 'node:crypto'

// Real API + worker + Vite fixture for application browser tests.
// The worker resolves extensions through a deterministic HTTP proxy
// so tests can gate artifact fetches without touching source policy.

export interface ApplicationRuntime {
  apiURL: string
  viteURL: string
  client: APIRequestContext
  extensionDir: string
  blockArtifacts(block: boolean): void
  /** count of /internal/v1/ext/tree calls seen by the gate proxy */
  artifactFetches(): number
  restartWorker(emptyCache?: boolean): Promise<void>
  restartAPI(): Promise<void>
  close(): Promise<void>
  logs(): string
}

const repo = join(import.meta.dirname, '..', '..')

async function freePort(): Promise<number> {
  const l = tcpServer()
  await new Promise<void>((r) => l.listen(0, '127.0.0.1', r))
  const a = l.address()
  if (!a || typeof a === 'string') throw new Error('no port')
  await new Promise<void>((r, j) => l.close((e) => (e ? j(e) : r())))
  return a.port
}

export async function startApplicationRuntime(opts: { secretKey?: string } = {}): Promise<ApplicationRuntime> {
  const dir = await mkdtemp(join(tmpdir(), 'calcside-apprt-'))
  const env = Object.fromEntries(Object.entries(process.env).filter(([k]) => !k.startsWith('CALCSIDE_') && !k.startsWith('ATLAS_')))
  const apiBin = join(dir, 'calcside')
  const workerBin = join(dir, 'calcside-worker')
  const db = join(dir, 'app.db')
  const key = randomBytes(32).toString('hex')
  execFileSync('go', ['build', '-o', apiBin, './cmd/calcside'], { cwd: repo, env, stdio: 'pipe' })
  execFileSync('go', ['build', '-o', workerBin, './cmd/calcside-worker'], { cwd: repo, env, stdio: 'pipe' })
  execFileSync('atlas', ['migrate', 'apply', '--env', 'sqlite'], {
    cwd: repo, env: { ...env, ATLAS_DB_URL: `sqlite://${db}` }, stdio: 'pipe',
  })

  const extRoot = join(dir, 'fixture')
  const probe = join(extRoot, 'probe')
  await mkdir(probe, { recursive: true })
  await writeFile(join(probe, 'capability.yaml'),
    'name: probe\nversion: "0.1.0"\ndescription: Fixture extension\nops:\n  - name: echo\n    params: [value]\n')
  await writeFile(join(probe, 'main.star'),
    'def echo(value):\n    return {"value": value, "release": "v1"}\n')

  const apiPort = await freePort()
  const vitePort = await freePort()
  const workerPort = await freePort()
  const proxyPort = await freePort()
  const apiURL = `http://127.0.0.1:${apiPort}`
  const viteURL = `http://127.0.0.1:${vitePort}`
  const workerAddr = `127.0.0.1:${workerPort}`
  const proxyURL = `http://127.0.0.1:${proxyPort}`

  let out = ''
  const procs = new Set<ChildProcess>()
  const start = (bin: string, args: string[], tag: string, cwd = repo): ChildProcess => {
    const p = spawn(bin, args, { cwd, env: tag === 'vite' ? { ...env, VITE_API_TARGET: apiURL } : env, stdio: 'pipe' })
    procs.add(p)
    p.on('exit', () => procs.delete(p))
    for (const s of [p.stdout, p.stderr]) {
      s?.on('data', (c) => { out = (out + `[${tag}] ` + String(c)).slice(-64_000) })
    }
    return p
  }
  const stop = (p: ChildProcess | undefined | null) => new Promise<void>((r) => {
    if (!p || p.exitCode !== null || p.signalCode !== null) return r()
    let done = false
    const fin = () => { if (!done) { done = true; r() } }
    const t = setTimeout(() => { try { p.kill('SIGKILL') } catch { /* gone */ } setTimeout(fin, 500) }, 4000)
    p.on('exit', () => { clearTimeout(t); p.stdout?.destroy(); p.stderr?.destroy(); fin() })
    try { p.kill('SIGTERM') } catch { /* already dead */ }
  })

  // deterministic ext-tree gate: 503s /internal/v1/ext/tree while
  // blocked, forwards everything else (HMAC covers path, not host)
  let block = false
  let extTreeCalls = 0
  const proxy: HTTPServer = httpServer((req, res) => {
    if (req.url?.startsWith('/internal/v1/ext/tree')) extTreeCalls++
    if (block && req.url?.startsWith('/internal/v1/ext/tree')) {
      res.writeHead(503, { 'Content-Type': 'application/json' })
      res.end('{"error":{"kind":"pending","message":"blocked"}}')
      return
    }
    const chunks: Buffer[] = []
    req.on('data', (c) => chunks.push(c))
    req.on('end', () => {
      const r = httpRequest({
        hostname: '127.0.0.1', port: apiPort, path: req.url, method: req.method,
        headers: { ...req.headers, host: `127.0.0.1:${apiPort}` },
      }, (up) => { res.writeHead(up.statusCode ?? 200, up.headers); up.pipe(res) })
      r.on('error', () => { res.writeHead(502); res.end() })
      r.end(Buffer.concat(chunks))
    })
  })
  await new Promise<void>((resolve, reject) => {
    proxy.once('error', reject)
    proxy.listen(proxyPort, '127.0.0.1', () => resolve())
  })

  const apiArgs = ['serve', '--dev', '--store', 'sqlite', '--dsn', db,
    '--addr', `127.0.0.1:${apiPort}`, '--console-origin', viteURL,
    '--workers', `w1=${workerAddr}`, '--worker-key', key,
    '--ext-local-roots', extRoot, '--ext-cache-dir', join(dir, 'apicache')]
  if (opts.secretKey) apiArgs.push('--secret-key', opts.secretKey)
  const workerArgs = ['serve', '--listen', workerAddr, '--api-addr', proxyURL,
    '--shared-key', key, '--node-id', 'w1',
    '--ext-cache-dir', join(dir, 'workercache'),
    '--instance-memory-max', '0']

  let api = start(apiBin, apiArgs, 'api')
  let worker = start(workerBin, workerArgs, 'worker')
  const vite = start('node', [join(repo, 'web', 'node_modules', 'vite', 'bin', 'vite.js'),
    '--host', '127.0.0.1', '--port', String(vitePort)], 'vite', join(repo, 'web'))

  const client = await request.newContext({ baseURL: apiURL, extraHTTPHeaders: { 'X-Requested-With': 'calcside' } })
  const wait200 = async (url: string, label: string, ms = 30_000) => {
    const t0 = Date.now()
    for (;;) {
      try {
        const r = await client.get(url, { timeout: 1000 })
        if (r.status() === 200) return
      } catch { /* not up yet */ }
      if (api.exitCode !== null) throw new Error(`api exited: ${out}`)
      if (worker.exitCode !== null) throw new Error(`worker exited: ${out}`)
      if (Date.now() - t0 > ms) throw new Error(`${label} timeout: ${out}`)
      await new Promise((r) => setTimeout(r, 200))
    }
  }
  await wait200(`${apiURL}/healthz`, 'api')
  await wait200(`http://${workerAddr}/healthz`, 'worker')
  {
    const c = await request.newContext({ baseURL: viteURL })
    const t0 = Date.now()
    for (;;) {
      try { if ((await c.get('/', { timeout: 1000 })).status() === 200) break } catch { /* retry */ }
      if (vite.exitCode !== null) { await c.dispose(); throw new Error('vite exited: ' + out.slice(-2000)) }
      if (Date.now() - t0 > 60_000) { await c.dispose(); throw new Error('vite timeout: ' + out.slice(-2000)) }
      await new Promise((r) => setTimeout(r, 200))
    }
    await c.dispose()
  }

  return {
    apiURL, viteURL, client,
    extensionDir: probe,
    blockArtifacts: (b) => { block = b },
    artifactFetches: () => extTreeCalls,
    async restartWorker(emptyCache = true) {
      await stop(worker)
      if (emptyCache) await rm(join(dir, 'workercache'), { recursive: true, force: true })
      worker = start(workerBin, workerArgs, 'worker2')
      await wait200(`http://${workerAddr}/healthz`, 'worker-restart')
    },
    async restartAPI() {
      await stop(api)
      api = start(apiBin, apiArgs, 'api2')
      await wait200(`${apiURL}/healthz`, 'api-restart')
    },
    async close() {
      proxy.closeAllConnections?.()
      proxy.close()
      await Promise.all([...procs].map(stop))
      proxy.close()
      await Promise.race([client.dispose(), new Promise((r) => setTimeout(r, 3000))])
      await rm(dir, { recursive: true, force: true })
    },
    logs: () => out,
  }
}
