import { test } from 'node:test';
import assert from 'node:assert/strict';

import { RenderPool, QueueFullError, TimeoutError, RenderJobError, ClosedError } from '../src/pool.js';
import { LINE_SPEC } from './specs.js';

const PNG_MAGIC = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);

test('renders a spec to PNG bytes', async (t) => {
  const pool = new RenderPool({ concurrency: 1 });
  t.after(() => pool.close());
  const out = await pool.render({ spec: LINE_SPEC });
  assert.ok(out.png.subarray(0, 8).equals(PNG_MAGIC));
  assert.ok(out.width > 0 && out.height > 0);
});

test('runs concurrent renders across the pool', async (t) => {
  const pool = new RenderPool({ concurrency: 2 });
  t.after(() => pool.close());
  const [a, b, c] = await Promise.all([
    pool.render({ spec: LINE_SPEC }),
    pool.render({ spec: LINE_SPEC, scale: 0.5 }),
    pool.render({ spec: LINE_SPEC, scale: 1.5 }),
  ]);
  for (const out of [a, b, c]) assert.ok(out.png.subarray(0, 8).equals(PNG_MAGIC));
});

test('bounded queue rejects overflow with QueueFullError', async (t) => {
  const pool = new RenderPool({ concurrency: 1, queueSize: 1, timeoutMs: 10_000 });
  t.after(() => pool.close());
  const slow = { spec: LINE_SPEC, delayMs: 400 };
  const first = pool.render(slow); // occupies the only worker
  const second = pool.render(slow); // waits in the queue
  await assert.rejects(pool.render(slow), QueueFullError);
  await first;
  await second;
});

test('timed-out worker is killed, respawned, and the next render succeeds', async (t) => {
  const pool = new RenderPool({ concurrency: 1, queueSize: 4, timeoutMs: 600 });
  t.after(() => pool.close());
  await assert.rejects(pool.render({ spec: LINE_SPEC, delayMs: 10_000 }), TimeoutError);
  // The respawned worker pays module+font load on its first render —
  // retry briefly to prove the pool recovered rather than racing it.
  let out;
  for (let i = 0; i < 10 && !out; i += 1) {
    try {
      out = await pool.render({ spec: LINE_SPEC });
    } catch (e) {
      if (!(e instanceof TimeoutError)) throw e;
    }
  }
  assert.ok(out, 'pool did not recover after worker timeout');
  assert.ok(out.png.subarray(0, 8).equals(PNG_MAGIC));
});

test('worker-reported spec errors carry code spec', async (t) => {
  const pool = new RenderPool({ concurrency: 1 });
  t.after(() => pool.close());
  await assert.rejects(
    pool.render({ spec: { data: { url: 'https://x' } } }),
    (e) => e instanceof RenderJobError && e.code === 'spec',
  );
  const bad = { data: { values: [{ a: 1 }] }, mark: 'line', encoding: { x: { field: 'a', type: 'bogus' } } };
  await assert.rejects(
    pool.render({ spec: bad }),
    (e) => e instanceof RenderJobError && e.code === 'render',
  );
});

test('close rejects pending work', async (t) => {
  const pool = new RenderPool({ concurrency: 1, queueSize: 4, timeoutMs: 10_000 });
  const slow = pool.render({ spec: LINE_SPEC, delayMs: 2_000 });
  const queued = pool.render({ spec: LINE_SPEC, delayMs: 2_000 });
  const p = pool.close();
  await assert.rejects(slow, ClosedError);
  await assert.rejects(queued, ClosedError);
  await p;
  await assert.rejects(pool.render({ spec: LINE_SPEC }), ClosedError);
});
