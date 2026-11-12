import { test } from 'node:test'
import assert from 'node:assert/strict'
import { ancestors, descendants, layoutGraph, NODE_H, NODE_W, type GraphEdge } from './notebookGraph.ts'

const e = (from: string, to: string, name: string): GraphEdge => ({ from, to, name })

test('layoutGraph: chain layers left to right', () => {
  const g = layoutGraph(['a', 'b', 'c'], [e('a', 'b', 'x'), e('b', 'c', 'y')])
  assert.equal(g.nodes.length, 3)
  assert.deepEqual(g.nodes.map((n) => n.name), ['a', 'b', 'c'])
  assert.deepEqual(g.nodes.map((n) => n.layer), [0, 1, 2])
  assert.ok(g.nodes[1].x > g.nodes[0].x && g.nodes[2].x > g.nodes[1].x)
  assert.equal(g.links.length, 2)
  assert.equal(g.width, 12 * 2 + 3 * NODE_W + 2 * 40)
  assert.ok(g.height > 0)
})

test('layoutGraph: diamond shares the root layer', () => {
  const g = layoutGraph(
    ['src', 'l', 'r', 'sink'],
    [e('src', 'l', 'x'), e('src', 'r', 'x'), e('l', 'sink', 'y'), e('r', 'sink', 'z')],
  )
  const layer = Object.fromEntries(g.nodes.map((n) => [n.name, n.layer]))
  assert.deepEqual(layer, { src: 0, l: 1, r: 1, sink: 2 })
  // l and r share layer 1, ordered by notebook position.
  const mid = g.nodes.filter((n) => n.layer === 1)
  assert.deepEqual(mid.map((n) => n.name), ['l', 'r'])
  assert.notEqual(mid[0].y, mid[1].y)
})

test('layoutGraph: cycle terminates and renders both directions', () => {
  const g = layoutGraph(['a', 'b'], [e('a', 'b', 'x'), e('b', 'a', 'y')])
  assert.equal(g.nodes.length, 2)
  assert.equal(g.links.length, 2)
  // One direction is a back edge: it does not inflate the layering.
  const layers = g.nodes.map((n) => n.layer).sort()
  assert.deepEqual(layers, [0, 1])
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

test('layoutGraph: isolated nodes keep input order in one layer', () => {
  const g = layoutGraph(['z', 'a', 'm'], [])
  assert.deepEqual(g.nodes.map((n) => n.name), ['z', 'a', 'm'])
  assert.ok(g.nodes.every((n) => n.layer === 0))
  assert.equal(g.nodes[0].y, g.nodes[1].y - (NODE_H + 16))
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
