import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  portablePath, applyPick, checkLimits, jsonPrecise, normalizeNumber,
  exportDraft, parseSavedDraft, draftKey, emptyDraft,
  patchSchema, toggleRequired, toggleNullable,
} from './buildDraft.ts'
import type { SchemaNode } from './buildDraft.ts'

test('portablePath requires exact /work/ prefix and clean segments', () => {
  assert.equal(portablePath('/work/main.star'), 'main.star')
  assert.equal(portablePath('/work/a/foo..bar.star'), 'a/foo..bar.star')
  assert.equal(portablePath('/etc/passwd'), null)
  assert.equal(portablePath('/workx/main.star'), null)
  assert.equal(portablePath('/work/../x'), null)
  assert.equal(portablePath('/work//x'), null)
  assert.equal(portablePath('/work/./x'), null)
  assert.equal(portablePath('/work/'), null)
  assert.equal(portablePath('work/main.star'), null)
  assert.equal(portablePath('/work/a\\b'), null)
  assert.equal(portablePath('/work/a\x00b'), null)
})

test('applyPick enforces one role per file and .star-only sources', () => {
  let d = emptyDraft()
  d = applyPick(d, 'main.star', 10, 'source')
  d = applyPick(d, 'data.bin', 20, 'asset')
  d = applyPick(d, 'data.bin', 20, 'source') // non-.star: removed, not added
  assert.deepEqual(d.assets, [])
  assert.deepEqual(d.sources, [{ path: 'main.star', size: 10 }])
  d = applyPick(d, 'data.star', 0, 'source') // size 0 is a real size
  assert.deepEqual(d.sources[1], { path: 'data.star', size: 0 })
})

test('checkLimits flags bad paths, non-star sources, dupes, sizes', () => {
  const d = emptyDraft()
  d.sources = [
    { path: '../x.star', size: 1 },
    { path: 'a.txt', size: 5 },
    { path: 'a.star', size: -1 },
    { path: 'a.star', size: 2 }, // duplicate
  ]
  const errs = checkLimits(d).map((e) => e.field)
  assert.ok(errs.includes('../x.star'))
  assert.ok(errs.includes('a.txt'))
  assert.ok(errs.includes('a.star'))
  assert.ok(checkLimits(d).some((e) => e.message.includes('duplicate')))
})

test('jsonPrecise rejects non-roundtrippable numbers', () => {
  assert.equal(jsonPrecise('{"a":9007199254740993}'), false)
  assert.equal(jsonPrecise('{"a":1e999}'), false)
  assert.equal(jsonPrecise('{"a":1e-999}'), false)
  assert.equal(jsonPrecise('{"a":0.12345678901234567890}'), false)
  assert.equal(jsonPrecise('{"a":1.5}'), true)
  assert.equal(jsonPrecise('{"a":1e3}'), true)
  assert.equal(jsonPrecise('{"a":"9007199254740993"}'), true)
})

test('normalizeNumber canonical form', () => {
  assert.equal(normalizeNumber('9007199254740993'), '9007199254740993e0')
  assert.equal(normalizeNumber('1.5'), '15e-1')
  assert.equal(normalizeNumber('1e3'), '1e3')
  assert.equal(normalizeNumber('0.5e2'), '5e1')
})

test('exportDraft keeps raw schema text and drops samples', () => {
  const d = emptyDraft()
  d.name = 'svc'
  d.input.schema = '{"minimum":9007199254740993}'
  d.output.schema = '{"x":0.12345678901234567890}'
  d.input.sample = 'SECRET_SAMPLE'
  const out = exportDraft(d, 'ins_1', 'ws_1')
  assert.ok(out.includes('9007199254740993'))
  assert.ok(out.includes('0.12345678901234567890'))
  assert.ok(!out.includes('SECRET_SAMPLE'))
  const doc = JSON.parse(out)
  assert.equal(doc.format, 'calcside.build-draft.v1')
})

test('parseSavedDraft rejects malformed and foreign shapes', () => {
  assert.equal(parseSavedDraft('not json'), null)
  assert.equal(parseSavedDraft('{"name":1}'), null)
  const d = emptyDraft()
  assert.deepEqual(parseSavedDraft(JSON.stringify(d)), d)
})

test('draftKey scopes per user/workspace/source unambiguously', () => {
  const a = draftKey('u1', 'ws1', 'i1')
  const b = draftKey('u1', 'ws1', 'i2')
  const c = draftKey('u2', 'ws1', 'i1')
  assert.notEqual(a, b); assert.notEqual(a, c)
})

test('patchSchema owns-key copies handle __proto__/constructor/a.b', () => {
  const root: SchemaNode = { type: 'object', properties: {} }
  const next = patchSchema(root, ['properties', '__proto__'], () => ({ type: 'string' }))
  assert.deepEqual((next.properties as any)['__proto__'], { type: 'string' })
  const dotted = patchSchema(root, ['properties', 'a.b'], () => ({ type: 'integer' }))
  assert.deepEqual((dotted.properties as any)['a.b'], { type: 'integer' })
  const ctor = patchSchema(root, ['properties', 'constructor'], () => ({ type: 'boolean' }))
  assert.deepEqual((ctor.properties as any)['constructor'], { type: 'boolean' })
})

test('toggleRequired touches only parent required; nullable only type', () => {
  const root: SchemaNode = {
    type: 'object',
    required: ['a'],
    properties: {
      a: { type: 'string', enum: ['x'], minimum: 3 },
      b: { type: ['object', 'null'], properties: { c: { type: 'integer' } }, required: ['c'] },
    },
  }
  const unreq = toggleRequired(root, [], 'a', false)
  assert.deepEqual(unreq.required, [])
  assert.deepEqual((unreq.properties as any).a, { type: 'string', enum: ['x'], minimum: 3 })
  const nullable = toggleNullable(root, ['properties', 'a'], true)
  assert.deepEqual((nullable.properties as any).a.type, ['string', 'null'])
  const unnull = toggleNullable(nullable, ['properties', 'a'], false)
  assert.deepEqual((unnull.properties as any).a.type, 'string')
  // required untouched by nullable toggles
  const b = toggleNullable(root, ['properties', 'b', 'properties', 'c'], true)
  assert.deepEqual((b.properties as any).b.required, ['c'])
  assert.deepEqual((b.properties as any).b.properties.c.type, ['integer', 'null'])
})

test('nullable on null-only and union types', () => {
  const root: SchemaNode = { type: 'object', required: ['x'], properties: {
    x: { type: 'null' }, y: { type: ['string', 'null'], enum: ['a'] } } }
  // nullable on a null-only node is a no-op shape (stays 'null')
  const n = toggleNullable(root, ['properties', 'x'], true)
  assert.deepEqual((n.properties as any).x.type, 'null')
  // removing null from a union keeps the base + unknown keywords
  const u = toggleNullable(root, ['properties', 'y'], false)
  assert.deepEqual((u.properties as any).y, { type: 'string', enum: ['a'] })
})

test('patchSchema __proto__ key does not pollute prototype', () => {
  const next = patchSchema({}, ['__proto__'], () => ({ type: 'string' }))
  assert.ok(Object.prototype.hasOwnProperty.call(next, '__proto__'))
  assert.equal(({} as any).type, undefined)
})

test('normalizeNumber strips the double-minus bug', () => {
  assert.equal(normalizeNumber('-1.5'), '-15e-1')
  assert.equal(normalizeNumber('-0e3'), '0')
})
