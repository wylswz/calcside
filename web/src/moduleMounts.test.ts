import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  MaxModuleMounts, addMount, canAddModule, mountName, parseModuleMounts, removeMount,
  uniqueMountAlias, validMountAlias,
} from './moduleMounts.ts'

test('mount alias grammar matches internal/module MountRe', () => {
  for (const ok of ['m', 'strkit', 'kit-2', 'kit_2', '9lives', 'A']) {
    assert.ok(validMountAlias(ok), ok)
  }
  for (const bad of ['', 'self', '-m', '_m', 'a.b', 'a b', 'a/b', 'a'.repeat(65)]) {
    assert.ok(!validMountAlias(bad), bad)
  }
})

test('uniqueMountAlias sanitizes, collides and stays in grammar', () => {
  assert.equal(uniqueMountAlias('strkit', new Set()), 'strkit')
  assert.equal(uniqueMountAlias('str.kit', new Set()), 'str_kit')
  assert.equal(uniqueMountAlias('strkit', new Set(['strkit'])), 'strkit_2')
  assert.equal(uniqueMountAlias('strkit', new Set(['strkit', 'strkit_2'])), 'strkit_3')
  assert.equal(uniqueMountAlias('self', new Set()), 'self_mod')
  assert.equal(uniqueMountAlias('...', new Set()), 'mod')
  assert.equal(uniqueMountAlias('9lives', new Set()), '9lives')
  const long = 'a'.repeat(70)
  const alias = uniqueMountAlias(long, new Set([long.slice(0, 64)]))
  assert.ok(alias.length <= 64)
  assert.ok(validMountAlias(alias))
  const colliding = uniqueMountAlias('a'.repeat(63) + 'x', new Set(['a'.repeat(63) + 'x']))
  assert.ok(colliding.length <= 64 && colliding !== 'a'.repeat(63) + 'x')
  assert.ok(validMountAlias(colliding))
})

test('parseModuleMounts preserves order and unknown fields', () => {
  const parsed = parseModuleMounts({
    m: { name: 'strkit', version: 1, sum: 'h1:x', ext: ['a'], custom_field: { nested: true } },
    m2: {},
  })
  assert.ok(parsed.ok)
  assert.deepEqual(parsed.mounts.map((m) => m.alias), ['m', 'm2'])
  assert.equal(parsed.mounts[0].ref.custom_field && (parsed.mounts[0].ref.custom_field as Record<string, unknown>).nested, true)
  assert.equal(mountName(parsed.mounts[0]), 'strkit')
  assert.equal(mountName(parsed.mounts[1]), 'm2')
})

test('parseModuleMounts reports malformed shapes', () => {
  for (const bad of ['str', 42, ['a'], { m: 'str' }, { m: [1] }]) {
    assert.equal(parseModuleMounts(bad as never).ok, false, JSON.stringify(bad))
  }
  assert.deepEqual(parseModuleMounts(undefined), { ok: true, mounts: [] })
  assert.deepEqual(parseModuleMounts(null), { ok: true, mounts: [] })
})

test('addMount appends without touching existing entries', () => {
  const cur = { a: { name: 'x' } }
  const next = addMount(cur, 'b', { name: 'y', version: 2 })
  assert.deepEqual(next, { a: { name: 'x' }, b: { name: 'y', version: 2 } })
  assert.equal(addMount(cur, 'a', { name: 'z' }), cur)
  assert.deepEqual(addMount(undefined, 'b', { name: 'y' }), { b: { name: 'y' } })
})

test('removeMount prunes only the target and empties to undefined', () => {
  const cur = { a: { name: 'x' }, b: { name: 'y' } }
  assert.deepEqual(removeMount(cur, 'a'), { b: { name: 'y' } })
  assert.equal(removeMount({ a: { name: 'x' } }, 'a'), undefined)
  assert.equal(removeMount(cur, 'missing') && Object.keys(removeMount(cur, 'missing') as object).length, 2)
})

test('canAddModule enforces publish pin, count and uniqueness', () => {
  const published = { name: 'strkit', latest_version: 1, latest_sum: 'h1:' + 'A'.repeat(43) + '=' }
  const empty = parseModuleMounts(undefined)
  assert.ok(empty.ok)
  assert.ok(canAddModule(empty, published))

  // Unpublished / malformed pins cannot be mounted.
  for (const bad of [
    { name: 'x', latest_version: 0, latest_sum: 'h1:' + 'A'.repeat(43) + '=' },
    { name: 'x', latest_version: 1 },
    { name: 'x', latest_version: 1, latest_sum: 'h1:short' },
    { name: 'x', latest_version: 1.5, latest_sum: 'h1:' + 'A'.repeat(43) + '=' },
  ]) {
    assert.ok(!canAddModule(empty, bad as never), JSON.stringify(bad))
  }

  // Duplicate resolved name (mount or explicit ref name) cannot add.
  const dup = parseModuleMounts({ m: { name: 'strkit' }, other: {} })
  assert.ok(dup.ok)
  assert.ok(!canAddModule(dup, published))
  assert.ok(!canAddModule(dup, { ...published, name: 'other' }))

  // The mount bound rejects further adds.
  const full = { ok: true as const, mounts: Array.from({ length: MaxModuleMounts }, (_, i) => ({ alias: `m${i}`, ref: {} })) }
  assert.ok(!canAddModule(full, published))

  // Malformed declarations cannot add or be overwritten.
  const malformed = parseModuleMounts('nonsense' as never)
  assert.equal(malformed.ok, false)
  assert.ok(!canAddModule(malformed, published))

  // A valid add pins exactly name/version/sum.
  const parsed = parseModuleMounts({ keep: { name: 'other', custom_field: 1 } })
  assert.ok(parsed.ok)
  assert.ok(canAddModule(parsed, published))
  const alias = uniqueMountAlias('strkit', new Set(parsed.mounts.map((m) => m.alias)))
  const next = addMount(undefined, alias, { name: published.name, version: published.latest_version, sum: published.latest_sum })
  assert.deepEqual(next, { strkit: { name: 'strkit', version: 1, sum: published.latest_sum } })
  assert.deepEqual(parsed.mounts[0].ref, { name: 'other', custom_field: 1 })
})
