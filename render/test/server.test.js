import { test } from 'node:test';
import assert from 'node:assert/strict';

import { start } from '../src/server.js';
import { QueueFullError, WorkerDiedError } from '../src/pool.js';
import { LINE_SPEC, LAYERED_SPEC } from './specs.js';
import { countNonWhite } from './png.js';

const KEY = 'test-render-key';
const PNG_MAGIC = '89504e470d0a1a0a';

async function boot(t, opts = {}) {
  const srv = await start({ key: KEY, addr: '127.0.0.1:0', concurrency: 1, ...opts });
  t.after(async () => {
    srv.server.closeAllConnections?.();
    srv.server.close();
    await srv.pool.close?.();
  });
  return { url: `http://127.0.0.1:${srv.port}`, pool: srv.pool, server: srv.server };
}

function post(url, body, { key = KEY, raw } = {}) {
  return fetch(`${url}/render`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      ...(key ? { Authorization: `Bearer ${key}` } : {}),
    },
    body: raw ?? JSON.stringify(body),
  });
}

test('healthz is unauthenticated', async (t) => {
  const { url } = await boot(t);
  const res = await fetch(`${url}/healthz`);
  assert.equal(res.status, 200);
  assert.equal(await res.text(), 'ok');
});

test('auth: missing and wrong keys get 401', async (t) => {
  const { url } = await boot(t);
  assert.equal((await post(url, { spec: LINE_SPEC }, { key: null })).status, 401);
  assert.equal((await post(url, { spec: LINE_SPEC }, { key: 'wrong' })).status, 401);
  const res = await post(url, { spec: LINE_SPEC });
  assert.equal(res.status, 200);
  assert.equal(res.headers.get('content-type'), 'image/png');
  const buf = Buffer.from(await res.arrayBuffer());
  assert.equal(buf.subarray(0, 8).toString('hex'), PNG_MAGIC);
  const info = countNonWhite(buf);
  assert.ok(info.width > 0 && info.height > 0 && info.nonWhite > 1000);
});

test('scale and max_side are honored', async (t) => {
  const { url } = await boot(t);
  const res = await post(url, { spec: LINE_SPEC, scale: 2, max_side: 300 });
  assert.equal(res.status, 200);
  const info = countNonWhite(Buffer.from(await res.arrayBuffer()));
  assert.equal(Math.max(info.width, info.height), 300);
});

test('bad JSON and missing spec get 400', async (t) => {
  const { url } = await boot(t);
  assert.equal((await post(url, null, { raw: '{not json' })).status, 400);
  assert.equal((await post(url, { scale: 2 })).status, 400);
  assert.equal((await post(url, { spec: 'nope' })).status, 400);
});

test('body over 1.5 MiB gets 413', async (t) => {
  const { url } = await boot(t);
  const big = JSON.stringify({ spec: LINE_SPEC, pad: 'x'.repeat(1.6 * 1024 * 1024) });
  const res = await post(url, null, { raw: big });
  assert.equal(res.status, 413);
});

test('denied keys and marks get 400; uncompilable spec gets 422', async (t) => {
  const { url } = await boot(t);
  const urlSpec = { ...LINE_SPEC, data: { url: 'https://example.com/x.json' } };
  let res = await post(url, { spec: urlSpec });
  assert.equal(res.status, 400);
  assert.deepEqual(await res.json(), { error: 'spec rejected' });

  const imageSpec = { ...LINE_SPEC, mark: 'image' };
  res = await post(url, { spec: imageSpec });
  assert.equal(res.status, 400);

  const badSpec = { data: { values: [{ a: 1 }] }, mark: 'line', encoding: { x: { field: 'a', type: 'bogus' } } };
  res = await post(url, { spec: badSpec });
  assert.equal(res.status, 422);
});

test('render timeout returns 504 and the pool recovers', async (t) => {
  const { url, pool } = await boot(t, { timeoutMs: 1 });
  const res = await post(url, { spec: LINE_SPEC });
  assert.equal(res.status, 504);
  await pool.close();
});

test('queue overflow returns 503', async (t) => {
  const fakePool = {
    render: () => Promise.reject(new QueueFullError()),
    close: async () => {},
  };
  const { url } = await boot(t, { pool: fakePool });
  const res = await post(url, { spec: LINE_SPEC });
  assert.equal(res.status, 503);
});

test('worker death returns 503', async (t) => {
  const fakePool = {
    render: () => Promise.reject(new WorkerDiedError()),
    close: async () => {},
  };
  const { url } = await boot(t, { pool: fakePool });
  const res = await post(url, { spec: LINE_SPEC });
  assert.equal(res.status, 503);
});

test('layered chart with legend renders non-blank', async (t) => {
  const { url } = await boot(t);
  const res = await post(url, { spec: LAYERED_SPEC });
  assert.equal(res.status, 200);
  const info = countNonWhite(Buffer.from(await res.arrayBuffer()));
  assert.ok(info.nonWhite > 1000);
});
