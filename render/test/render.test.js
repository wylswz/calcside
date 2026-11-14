import { test } from 'node:test';
import assert from 'node:assert/strict';

import { renderPng, RenderError, PIXEL_CAP } from '../src/render.js';
import { SpecError } from '../src/spec-check.js';
import { countNonWhite } from './png.js';
import { LINE_SPEC, NO_TITLE_SPEC, LAYERED_SPEC } from './specs.js';

const PNG_MAGIC = '89504e470d0a1a0a';

async function dims(spec, opts) {
  const out = await renderPng(spec, opts);
  assert.equal(out.png.subarray(0, 8).toString('hex'), PNG_MAGIC);
  return countNonWhite(out.png);
}

test('renders a PNG with expected dimensions', async () => {
  const info = await dims(LINE_SPEC, { scale: 1 });
  assert.ok(info.width > 0 && info.height > 0);
  assert.ok(info.nonWhite > 1000, `expected non-blank render, got ${info.nonWhite}`);
});

test('scale and max_side size the output', async () => {
  const base = await dims(LINE_SPEC, { scale: 1 });
  const big = await dims(LINE_SPEC, { scale: 2 });
  assert.ok(Math.abs(big.width - base.width * 2) <= 1, `${big.width} vs ${base.width * 2}`);
  assert.ok(Math.abs(big.height - base.height * 2) <= 1);

  const capped = await dims(LINE_SPEC, { scale: 2, maxSide: 400 });
  assert.equal(Math.max(capped.width, capped.height), 400);
});

test('scale clamps to [0.25, 4]', async () => {
  const base = await dims(LINE_SPEC, { scale: 1 });
  const lo = await dims(LINE_SPEC, { scale: 0.01 });
  assert.ok(Math.abs(lo.width - base.width * 0.25) <= 1, `${lo.width} vs ${base.width * 0.25}`);
  const hi = await dims(LINE_SPEC, { scale: 99 });
  assert.ok(Math.abs(hi.width - base.width * 4) <= 1, `${hi.width} vs ${base.width * 4}`);
});

test('text actually rasterizes: a titled chart has more ink than untitled', async () => {
  const titled = countNonWhite((await renderPng(LINE_SPEC)).png);
  const untitled = countNonWhite((await renderPng(NO_TITLE_SPEC)).png);
  assert.ok(
    titled.nonWhite > untitled.nonWhite + 100,
    `title should add pixels: titled=${titled.nonWhite} untitled=${untitled.nonWhite}`,
  );
});

test('layered multi-series spec renders', async () => {
  const info = await dims(LAYERED_SPEC);
  assert.ok(info.nonWhite > 1000, `expected non-blank render, got ${info.nonWhite}`);
});

test('denies url/href keys anywhere in the spec', async () => {
  const cases = [
    { ...LINE_SPEC, data: { url: 'https://example.com/data.json' } },
    { ...LINE_SPEC, data: { values: [{ m: 'a', v: 1, url: 'x' }] } },
    { ...LINE_SPEC, encoding: { ...LINE_SPEC.encoding, href: { field: 'v', type: 'nominal' } } },
    { spec: { href: 'http://x' } },
    { ...LINE_SPEC, transform: [{ lookup: 'v', from: { data: 't', key: 'k', fields: ['f'], url: 'http://x' } }] },
  ];
  for (const spec of cases) {
    await assert.rejects(renderPng(spec), SpecError, JSON.stringify(spec).slice(0, 80));
  }
});

test('denies image and geoshape marks', async () => {
  for (const mark of ['image', 'geoshape', { type: 'image' }, [{ type: 'line' }, 'image']]) {
    const spec = { ...LINE_SPEC, mark };
    await assert.rejects(renderPng(spec), SpecError, `mark ${JSON.stringify(mark)}`);
  }
});

test('non-object spec is rejected', async () => {
  await assert.rejects(renderPng('nope'), SpecError);
  await assert.rejects(renderPng([1, 2]), SpecError);
  await assert.rejects(renderPng(null), SpecError);
});

test('uncompilable spec is a render error, not a spec error', async () => {
  const bad = { data: { values: [{ a: 1 }] }, mark: 'line', encoding: { x: { field: 'a', type: 'not-a-type' } } };
  await assert.rejects(renderPng(bad), RenderError);
});

test('pixel cap rejects oversized output and max_side rescues it', async () => {
  const huge = { ...LINE_SPEC, width: PIXEL_CAP + 1, height: PIXEL_CAP + 1 };
  await assert.rejects(renderPng(huge), RenderError);
  const rescued = await dims(huge, { maxSide: 1024 });
  assert.equal(Math.max(rescued.width, rescued.height), 1024);
});
