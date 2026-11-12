// notebookGraph.ts — pure layout + reachability helpers for the
// read-only dependency graph view. No DOM, no React: the component
// feeds it the held structural snapshot; output is fully deterministic.

export interface GraphEdge {
  from: string
  to: string
  name: string
}

export interface GraphNodeBox {
  name: string
  /** longest-path layer over kept edges, roots at 0 */
  layer: number
  /** position inside its layer, ordered by notebook position */
  row: number
  x: number
  y: number
}

export interface GraphLink {
  from: string
  to: string
  /** all variable names carried by this provider→consumer pair */
  names: string[]
  /** cubic path anchors: right edge of `from` to left edge of `to` */
  x1: number
  y1: number
  x2: number
  y2: number
  /** horizontal control offset for the cubic path */
  cx: number
}

export interface GraphLayout {
  nodes: GraphNodeBox[]
  links: GraphLink[]
  width: number
  height: number
}

// Fixed geometry: nodes are absolutely positioned HTML buttons over an
// SVG edge layer. Same inputs always produce the same boxes. Sized so
// a three-layer chain fits the canvas beside the aside at desktop
// width without horizontal scrolling.
export const NODE_W = 128
export const NODE_H = 52
const GAP_X = 40
const GAP_Y = 16
const PAD = 12
// Section names are [a-z][a-z0-9_]* — a space safely delimits pair keys.
const SEP = ' '

interface Pair {
  from: string
  to: string
  names: string[]
}

// layoutGraph positions `names` (notebook order) left→right by
// longest-path layer. Edges closing a cycle — found by DFS over `names`
// order with successors in notebook order — are ignored for layering so
// cyclic graphs still terminate; they are still rendered as links.
// Edges naming an unknown endpoint are dropped entirely.
export function layoutGraph(names: string[], edges: GraphEdge[]): GraphLayout {
  const idx = new Map<string, number>()
  names.forEach((n, i) => { if (!idx.has(n)) idx.set(n, i) })
  const byIdx = (a: string, b: string) => (idx.get(a) ?? 0) - (idx.get(b) ?? 0)

  // Merge parallel edges into one link per ordered pair and build the
  // adjacency used for cycle detection and layering.
  const pairs = new Map<string, Pair>()
  const succ = new Map<string, Set<string>>()
  for (const e of edges) {
    if (!idx.has(e.from) || !idx.has(e.to)) continue
    const k = e.from + SEP + e.to
    const p = pairs.get(k)
    if (p) {
      if (!p.names.includes(e.name)) p.names.push(e.name)
      continue
    }
    pairs.set(k, { from: e.from, to: e.to, names: [e.name] })
    let s = succ.get(e.from)
    if (!s) { s = new Set(); succ.set(e.from, s) }
    s.add(e.to)
  }

  // DFS in names order; an edge to a node on the recursion stack is a
  // back edge (cycle-closing) and is excluded from layering. Removing
  // every back edge leaves a DAG.
  const back = new Set<string>()
  const color = new Map<string, number>() // 1 = on stack, 2 = done
  const visit = (u: string) => {
    color.set(u, 1)
    for (const v of [...(succ.get(u) ?? [])].sort(byIdx)) {
      const c = color.get(v)
      if (c === 1) back.add(u + SEP + v)
      else if (c === undefined) visit(v)
    }
    color.set(u, 2)
  }
  for (const n of names) if (!color.has(n)) visit(n)

  // Kahn topo over kept edges; ready nodes pop in notebook order.
  const keptSucc = new Map<string, string[]>()
  const indeg = new Map<string, number>()
  for (const p of pairs.values()) {
    if (back.has(p.from + SEP + p.to)) continue
    keptSucc.set(p.from, [...(keptSucc.get(p.from) ?? []), p.to])
    indeg.set(p.to, (indeg.get(p.to) ?? 0) + 1)
  }
  for (const list of keptSucc.values()) list.sort(byIdx)

  const layer = new Map<string, number>()
  const queue = names.filter((n) => (indeg.get(n) ?? 0) === 0).sort(byIdx)
  for (let head = 0; head < queue.length; head++) {
    const u = queue[head]
    const lu = layer.get(u) ?? 0
    for (const w of keptSucc.get(u) ?? []) {
      if (lu + 1 > (layer.get(w) ?? 0)) layer.set(w, lu + 1)
      const d = (indeg.get(w) ?? 0) - 1
      indeg.set(w, d)
      if (d === 0) {
        let p = queue.length
        while (p > head + 1 && byIdx(queue[p - 1], w) > 0) p--
        queue.splice(p, 0, w)
      }
    }
  }

  // Nodes in a layer are rows in notebook order; short layers centre
  // vertically inside the tallest one.
  const byLayer = new Map<number, string[]>()
  for (const n of names) {
    const l = layer.get(n) ?? 0
    byLayer.set(l, [...(byLayer.get(l) ?? []), n])
  }
  const maxRows = Math.max(0, ...[...byLayer.values()].map((r) => r.length))
  const nodes: GraphNodeBox[] = []
  const pos = new Map<string, GraphNodeBox>()
  const layerCount = byLayer.size
  for (const l of [...byLayer.keys()].sort((a, b) => a - b)) {
    const rowNames = byLayer.get(l)!
    const yOff = Math.floor(((maxRows - rowNames.length) * (NODE_H + GAP_Y)) / 2)
    rowNames.forEach((n, row) => {
      const box: GraphNodeBox = {
        name: n, layer: l, row,
        x: PAD + l * (NODE_W + GAP_X),
        y: PAD + yOff + row * (NODE_H + GAP_Y),
      }
      nodes.push(box)
      pos.set(n, box)
    })
  }
  const width = nodes.length
    ? PAD * 2 + layerCount * NODE_W + Math.max(0, layerCount - 1) * GAP_X
    : 0
  const height = nodes.length
    ? PAD * 2 + maxRows * NODE_H + Math.max(0, maxRows - 1) * GAP_Y
    : 0

  // Links carry cubic geometry from the provider's right edge to the
  // consumer's left edge; cycle-closing links route the same way.
  const links: GraphLink[] = [...pairs.values()]
    .sort((a, b) => byIdx(a.from, b.from) || byIdx(a.to, b.to))
    .map((p) => {
      const a = pos.get(p.from)!
      const b = pos.get(p.to)!
      const x1 = a.x + NODE_W
      const y1 = a.y + NODE_H / 2
      const x2 = b.x
      const y2 = b.y + NODE_H / 2
      return {
        from: p.from, to: p.to, names: p.names,
        x1, y1, x2, y2,
        cx: Math.max(28, Math.abs(x2 - x1) / 2),
      }
    })
  return { nodes, links, width, height }
}

// ancestors returns every node that can reach `name` following edge
// direction (providers upstream of it); descendants returns every node
// reachable FROM `name` (consumers downstream). Both are cycle-safe.
export function ancestors(name: string, edges: GraphEdge[]): Set<string> {
  const rev = new Map<string, string[]>()
  for (const e of edges) rev.set(e.to, [...(rev.get(e.to) ?? []), e.from])
  return reach(name, rev)
}

export function descendants(name: string, edges: GraphEdge[]): Set<string> {
  const fwd = new Map<string, string[]>()
  for (const e of edges) fwd.set(e.from, [...(fwd.get(e.from) ?? []), e.to])
  return reach(name, fwd)
}

function reach(start: string, adj: Map<string, string[]>): Set<string> {
  const out = new Set<string>()
  const seen = new Set<string>([start])
  const stack = [start]
  while (stack.length) {
    const u = stack.pop()!
    for (const v of adj.get(u) ?? []) {
      if (seen.has(v)) continue
      seen.add(v)
      out.add(v)
      stack.push(v)
    }
  }
  return out
}
