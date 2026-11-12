import { test } from 'node:test'
import assert from 'node:assert/strict'
import { ancestors, descendants, layoutDelta, layoutGraph, interpolatePositions, NODE_H, NODE_W, type GraphEdge } from './notebookGraph.ts'

const e = (from: string, to: string, name: string): GraphEdge => ({ from, to, name })

test('layoutGraph: chain orders providers left of consumers', () => {
  const g = layoutGraph(['a', 'b', 'c'], [e('a', 'b', 'x'), e('b', 'c', 'y')])
  assert.equal(g.nodes.length, 3)
  assert.deepEqual(g.nodes.map((n) => n.name), ['a', 'b', 'c'])
  const x = Object.fromEntries(g.nodes.map((n) => [n.name, n.x]))
  assert.ok(x.b > x.a && x.c > x.b, `expected a<b<c by x, got ${JSON.stringify(x)}`)
  assert.equal(g.links.length, 2)
  assert.ok(g.width > 0 && g.height > 0)
})

test('layoutGraph: diamond keeps shared endpoints on outer ranks', () => {
  const g = layoutGraph(
    ['src', 'l', 'r', 'sink'],
    [e('src', 'l', 'x'), e('src', 'r', 'x'), e('l', 'sink', 'y'), e('r', 'sink', 'z')],
  )
  const p = Object.fromEntries(g.nodes.map((n) => [n.name, n]))
  // src is the only root → leftmost; sink the only sink → rightmost;
  // l and r share the middle rank (same x, stacked y).
  assert.ok(p.src.x < p.l.x && p.src.x < p.r.x)
  assert.ok(Math.abs(p.l.x - p.r.x) < 1, 'middle rank nodes share x')
  assert.ok(p.sink.x > p.l.x && p.sink.x > p.r.x)
  assert.notEqual(p.l.y, p.r.y)
})

test('layoutGraph: cycle terminates and places every node', () => {
  const g = layoutGraph(['a', 'b'], [e('a', 'b', 'x'), e('b', 'a', 'y')])
  assert.equal(g.nodes.length, 2)
  assert.equal(g.links.length, 2)
  assert.ok(g.nodes.every((n) => Number.isFinite(n.x) && Number.isFinite(n.y)))
})

test('layoutGraph: parallel names merge into one link', () => {
  const g = layoutGraph(['p', 'q'], [e('p', 'q', 'x'), e('p', 'q', 'y'), e('p', 'q', 'x')])
  assert.equal(g.links.length, 1)
  assert.deepEqual(g.links[0].names, ['x', 'y'])
})

test('layoutGraph: deterministic and drops unknown endpoints', () => {
  const edges = [e('b', 'a', 'x'), e('a', 'c', 'y'), e('ghost', 'a', 'z'), e('a', 'gone', 'w')]
  const g1 = layoutGraph(['a', 'b', 'c'], edges)
  const g2 = layoutGraph(['a', 'b', 'c'], edges)
  assert.deepEqual(g1, g2)
  assert.equal(g1.links.length, 2) // ghost→a and a→gone dropped
  assert.deepEqual(g1.links.map((l) => `${l.from}>${l.to}`), ['a>c', 'b>a'])
})

test('layoutGraph: empty input', () => {
  const g = layoutGraph([], [])
  assert.deepEqual(g, { nodes: [], links: [], width: 0, height: 0 })
})

test('layoutGraph: isolated nodes all placed in one rank', () => {
  const g = layoutGraph(['z', 'a', 'm'], [])
  assert.deepEqual(g.nodes.map((n) => n.name), ['z', 'a', 'm'])
  const xs = new Set(g.nodes.map((n) => n.x))
  assert.equal(xs.size, 1, 'isolated nodes share the single rank x')
  const ys = new Set(g.nodes.map((n) => n.y))
  assert.equal(ys.size, 3, 'isolated nodes stack vertically')
  // Node boxes keep the fixed RF size contract.
  assert.ok(g.height >= NODE_H && g.width >= NODE_W)
})

test('layoutDelta: identical layouts diff to nothing', () => {
  const names = ['a', 'b', 'c']
  const edges = [e('a', 'b', 'x'), e('b', 'c', 'y')]
  const g = layoutGraph(names, edges)
  const d = layoutDelta(g, layoutGraph(names, edges))
  assert.deepEqual(d, { added: [], removed: [], moved: [], linksAdded: [], linksRemoved: [] })
})

test('layoutDelta: added/removed/moved nodes and links', () => {
  const before = layoutGraph(['a', 'b'], [e('a', 'b', 'x')])
  const after = layoutGraph(['a', 'b', 'c'], [e('b', 'c', 'y'), e('a', 'c', 'z')])
  const d = layoutDelta(before, after)
  assert.deepEqual(d.added, ['c'])
  assert.deepEqual(d.removed, [])
  // c enters rank 2 — a and b may shift ranks; only names whose
  // position actually changed are reported.
  const movedSet = new Set(d.moved)
  for (const n of after.nodes.filter((n) => n.name !== 'c')) {
    const b = before.nodes.find((x) => x.name === n.name)!
    const changed = Math.abs(b.x - n.x) > 0.01 || Math.abs(b.y - n.y) > 0.01
    assert.equal(movedSet.has(n.name), changed, `${n.name} moved flag`)
  }
  assert.deepEqual(d.linksAdded.map((l) => `${l.from}>${l.to}`), ['a>c', 'b>c'])
  assert.deepEqual(d.linksRemoved.map((l) => `${l.from}>${l.to}`), ['a>b'])
})

test('layoutDelta: removed node reported, surviving nodes unmoved when layout stable', () => {
  const before = layoutGraph(['a', 'b', 'c'], [e('a', 'b', 'x'), e('b', 'c', 'y')])
  const after = layoutGraph(['a', 'c'], [e('a', 'c', 'y')])
  const d = layoutDelta(before, after)
  assert.deepEqual(d.removed, ['b'])
  assert.deepEqual(d.added, [])
  // a and c keep their relative order; positions may shift — the delta
  // must agree with the actual coordinate diff either way.
  for (const n of after.nodes) {
    const b = before.nodes.find((x) => x.name === n.name)!
    const changed = Math.abs(b.x - n.x) > 0.01 || Math.abs(b.y - n.y) > 0.01
    assert.equal(d.moved.includes(n.name), changed)
  }
})

test('interpolatePositions: lerps shared names, pins missing ones at target', () => {
  const from = new Map([['a', { x: 0, y: 0 }], ['b', { x: 100, y: 50 }]])
  const to = new Map([['a', { x: 10, y: 20 }], ['b', { x: 200, y: 100 }], ['c', { x: 300, y: 0 }]])
  const mid = interpolatePositions(from, to, 0.5)
  assert.deepEqual(mid.get('a'), { x: 5, y: 10 })
  assert.deepEqual(mid.get('b'), { x: 150, y: 75 })
  // 'c' has no `from` — it appears already at its final position.
  assert.deepEqual(mid.get('c'), { x: 300, y: 0 })
  const end = interpolatePositions(from, to, 1)
  assert.deepEqual(end.get('a'), { x: 10, y: 20 })
  const start = interpolatePositions(from, to, 0)
  assert.deepEqual(start.get('a'), { x: 0, y: 0 })
})

test('ancestors/descendants follow edge direction and survive cycles', () => {
  const edges = [e('a', 'b', 'x'), e('b', 'c', 'y'), e('c', 'a', 'z'), e('c', 'd', 'w')]
  assert.deepEqual([...ancestors('c', edges)].sort(), ['a', 'b'])
  assert.deepEqual([...descendants('a', edges)].sort(), ['b', 'c', 'd'])
  // In the a→b→c→a cycle both b and c reach a — no infinite loop.
  assert.deepEqual([...ancestors('a', edges)].sort(), ['b', 'c'])
  // A cycle does not loop forever and still reports reachability.
  assert.deepEqual([...descendants('b', edges)].sort(), ['a', 'c', 'd'])
})
