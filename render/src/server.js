import http from 'node:http';
import { createHash, timingSafeEqual } from 'node:crypto';
import { pathToFileURL } from 'node:url';

import {
  ClosedError,
  QueueFullError,
  RenderJobError,
  RenderPool,
  TimeoutError,
  WorkerDiedError,
} from './pool.js';

const BODY_LIMIT = 1536 * 1024; // 1.5 MiB

class HttpError extends Error {
  constructor(status, msg) {
    super(msg);
    this.status = status;
  }
}

function authorized(req, keyHash) {
  const h = req.headers.authorization;
  if (typeof h !== 'string' || !h.startsWith('Bearer ')) return false;
  const got = createHash('sha256').update(h.slice('Bearer '.length)).digest();
  return timingSafeEqual(got, keyHash);
}

async function readBody(req) {
  const chunks = [];
  let size = 0;
  for await (const chunk of req) {
    size += chunk.length;
    if (size > BODY_LIMIT) throw new HttpError(413, 'body too large');
    chunks.push(chunk);
  }
  return Buffer.concat(chunks);
}

function reply(res, req, log, status, body, headers = {}) {
  const buf = Buffer.isBuffer(body) ? body : Buffer.from(body);
  res.writeHead(status, { 'Content-Length': buf.length, ...headers });
  res.end(buf);
  log(`${req.method} ${req.url} ${status} ${Date.now() - req._t0}ms ${buf.length}B`);
}

function jsonError(res, req, log, status, msg) {
  reply(res, req, log, status, JSON.stringify({ error: msg }), { 'Content-Type': 'application/json' });
}

// createHandler returns the request handler. `key` is the shared
// bearer secret; `pool` renders specs. Every response logs one line —
// method, path, status, elapsed ms and byte count; never spec data.
export function createHandler({ key, pool, log = (line) => console.log(line) }) {
  const keyHash = createHash('sha256').update(key).digest();
  return async function handle(req, res) {
    req._t0 = Date.now();
    const path = new URL(req.url, 'http://render').pathname;
    try {
      if (req.method === 'GET' && path === '/healthz') {
        reply(res, req, log, 200, 'ok', { 'Content-Type': 'text/plain' });
        return;
      }
      if (req.method !== 'POST' || path !== '/render') {
        jsonError(res, req, log, 404, 'not found');
        return;
      }
      if (!authorized(req, keyHash)) {
        jsonError(res, req, log, 401, 'unauthorized');
        return;
      }
      let body;
      try {
        body = await readBody(req);
      } catch (e) {
        if (e instanceof HttpError) {
          res.shouldKeepAlive = false;
          jsonError(res, req, log, e.status, e.message);
          return;
        }
        throw e;
      }
      let parsed;
      try {
        parsed = JSON.parse(body.toString('utf8'));
      } catch {
        jsonError(res, req, log, 400, 'invalid JSON');
        return;
      }
      if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed) || parsed.spec === undefined) {
        jsonError(res, req, log, 400, 'expected {spec, scale?, max_side?}');
        return;
      }
      const out = await pool.render({
        spec: parsed.spec,
        scale: parsed.scale,
        maxSide: parsed.max_side,
      });
      reply(res, req, log, 200, out.png, { 'Content-Type': 'image/png' });
    } catch (e) {
      if (e instanceof QueueFullError || e instanceof ClosedError || e instanceof WorkerDiedError) {
        jsonError(res, req, log, 503, 'render unavailable');
      } else if (e instanceof TimeoutError) {
        jsonError(res, req, log, 504, 'render timeout');
      } else if (e instanceof RenderJobError) {
        jsonError(res, req, log, e.code === 'spec' ? 400 : 422, e.code === 'spec' ? 'spec rejected' : 'render failed');
      } else {
        jsonError(res, req, log, 500, 'internal error');
      }
    }
  };
}

// parseAddr splits "host:port" listen addresses — net.Server.listen
// treats a bare string as a unix socket path, so the host and port
// must be passed separately. Accepts ":8091", "127.0.0.1:8091",
// "[::1]:8091" and a bare port number.
export function parseAddr(addr) {
  if (/^\d+$/.test(addr)) return { port: Number(addr) };
  const m = /^(.*):(\d+)$/.exec(addr);
  if (!m) throw new Error(`bad listen address ${addr}`);
  let host = m[1];
  if (host.startsWith('[') && host.endsWith(']')) host = host.slice(1, -1);
  const port = Number(m[2]);
  // 0 is legal — the OS picks an ephemeral port (tests rely on it).
  if (!Number.isInteger(port) || port < 0 || port > 65535) {
    throw new Error(`bad listen address ${addr}`);
  }
  return host ? { host, port } : { port };
}

// start boots the HTTP server and worker pool. Resolves once listening;
// the returned {server, pool, port} supports graceful shutdown.
export function start({ key, addr = ':8091', concurrency = 2, queueSize = 16, timeoutMs = 5000, pool, log } = {}) {
  if (!key || !key.trim()) throw new Error('CALCSIDE_RENDER_KEY is required');
  // Parse before spawning workers so a bad address fails fast without
  // leaking a pool.
  const listenOpts = parseAddr(addr);
  const p = pool || new RenderPool({ concurrency, queueSize, timeoutMs });
  const handler = createHandler({ key, pool: p, log });
  const server = http.createServer((req, res) => {
    Promise.resolve(handler(req, res)).catch((e) => {
      try {
        jsonError(res, req, log || console.log, 500, 'internal error');
      } catch {
        // socket already gone — nothing to report to
      }
    });
  });
  return new Promise((resolve, reject) => {
    server.once('error', (e) => {
      p.close().catch(() => {});
      reject(e);
    });
    server.listen(listenOpts, () => {
      const address = server.address();
      const port = address && typeof address === 'object' ? address.port : null;
      resolve({ server, pool: p, port });
    });
  });
}

function intEnv(name, dflt) {
  const raw = process.env[name];
  if (raw == null || raw === '') return dflt;
  const n = Number(raw);
  if (!Number.isInteger(n) || n <= 0) throw new Error(`${name} must be a positive integer`);
  return n;
}

async function main() {
  const key = process.env.CALCSIDE_RENDER_KEY;
  if (!key || !key.trim()) {
    console.error('calcside-render: CALCSIDE_RENDER_KEY is required');
    process.exit(1);
  }
  const addr = process.env.RENDER_ADDR || ':8091';
  const concurrency = intEnv('RENDER_CONCURRENCY', 2);
  const timeoutMs = intEnv('RENDER_TIMEOUT_MS', 5000);
  const { server, pool } = await start({ key, addr, concurrency, timeoutMs });
  const bound = server.address();
  console.log(`calcside-render listening on ${typeof bound === 'object' ? bound.address + ':' + bound.port : addr}`);
  let shuttingDown = false;
  const shutdown = () => {
    if (shuttingDown) return;
    shuttingDown = true;
    server.close(() => process.exit(0));
    pool.close().catch(() => {});
    setTimeout(() => process.exit(0), 3000).unref();
  };
  process.on('SIGTERM', shutdown);
  process.on('SIGINT', shutdown);
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main().catch((e) => {
    console.error(`calcside-render: ${e.message}`);
    process.exit(1);
  });
}
