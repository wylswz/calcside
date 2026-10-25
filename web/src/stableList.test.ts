import { test } from 'node:test'
import assert from 'node:assert/strict'
import { hold, reconcile, type Placement } from './stableList.ts'

const at = (spec: string): Placement[] => spec.split(' ').filter(Boolean).map((s) => {
  const [slot, key] = s.includes(':') ? s.split(':') : ['', s]
  return { key, slot }
})
const keys = (ps: Placement[]) => ps.map((p) => (p.slot ? `${p.slot}:${p.key}` : p.key)).join(' ')

test('arrivals wait instead of pushing rows down', () => {
  const r = reconcile(at('b a'), at('d c b a'))
  assert.equal(keys(r.shown), 'b a')
  assert.equal(r.arrived, 2)
  assert.equal(r.moved, false)
})

test('reordering and regrouping wait; rows keep the group they were seen in', () => {
  const r = reconcile(at('live:a live:b ended:c'), at('attn:b live:a ended:c'))
  assert.equal(keys(r.shown), 'live:a live:b ended:c')
  assert.equal(r.arrived, 0)
  assert.equal(r.moved, true)
})

test('rows that are gone drop out', () => {
  const r = reconcile(at('a b c'), at('a c'))
  assert.equal(keys(r.shown), 'a c')
  assert.equal(r.moved, false)
})

test('first data and a reset are adopted, rows the user caused show at once', () => {
  const first = hold(null, at('b a'), 'all', () => false)
  assert.equal(keys(first.placements), 'b a')
  assert.equal(hold(first, at('c b a'), 'all', () => false), first)
  const mine = hold(first, at('d c b a'), 'all', (k) => k === 'd')
  assert.equal(keys(mine.placements), 'd b a')
  assert.equal(keys(hold(mine, at('a'), 'attention', () => false).placements), 'a')
})
