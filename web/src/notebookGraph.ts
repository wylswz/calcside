// notebookGraph.ts — pure layout + reachability helpers for the
// read-only dependency graph view. No DOM, no React: the component
// feeds it the live polled structure; output is fully deterministic.
// Layout is delegated to dagre (LR ranks); React Flow owns edge
// geometry and the viewport.

import dagre from '@dagrejs/dagre'

export interface GraphEdge {
  from: string
  to: string
  name: string
}

export interface GraphNodeBox {
  name: string
  /** top-left corner in flow coordinates */
  x: number
  y: number
}

export interface GraphLink {
  from: string
  to: string
  /** all variable names carried by this provider→consumer pair */
  names: string[]
}

export interface GraphLayout {
  nodes: GraphNodeBox[]
  links: GraphLink[]
  width: number
  height: number
}

// LayoutDelta is the diff between two layouts of the same graph. The
// dagre layout is deterministic, so an unchanged structure yields an
// all-empty delta — the view then knows nothing moved and can skip
// animating entirely (a state-only poll repaints data, never geometry).
export interface LayoutDelta {
  /** node names entering the layout */
  added: string[]
  /** node names leaving the layout */
  removed: string[]
  /** shared names whose position changed */
  moved: string[]
  /** provider→consumer pairs entering the layout */
  linksAdded: GraphLink[]
  /** provider→consumer pairs leaving the layout */
  linksRemoved: GraphLink[]
}

const POS_EPS = 0.01

// layoutDelta compares node sets by name and link sets by ordered pair.
// Positions are compared with a sub-pixel epsilon — identical structure
// produces bit-identical dagre output, so any real move clears it.
export function layoutDelta(from: GraphLayout, to: GraphLayout): LayoutDelta {
  const fp = new Map(from.nodes.map((n) => [n.name, n]))
  const tp = new Map(to.nodes.map((n) => [n.name, n]))
  const delta: LayoutDelta = { added: [], removed: [], moved: [], linksAdded: [], linksRemoved: [] }
  for (const n of to.nodes) {
    const f = fp.get(n.name)
    if (!f) delta.added.push(n.name)
    else if (Math.abs(f.x - n.x) > POS_EPS || Math.abs(f.y - n.y) > POS_EPS) delta.moved.push(n.name)
  }
  for (const n of from.nodes) if (!tp.has(n.name)) delta.removed.push(n.name)
  const fkeys = new Set(from.links.map((l) => l.from + SEP + l.to))
  const tkeys = new Set(to.links.map((l) => l.from + SEP + l.to))
  delta.linksAdded = to.links.filter((l) => !fkeys.has(l.from + SEP + l.to))
  delta.linksRemoved = from.links.filter((l) => !tkeys.has(l.from + SEP + l.to))
  return delta
}

export interface NodePose {
  x: number
  y: number
}

// interpolatePositions lerps every target pose from its `from` pose.
// A name missing from `from` (a node fading in) stays at its target —
// it appears at the final position while its opacity ramps separately.
export function interpolatePositions(
  from: ReadonlyMap<string, NodePose>,
  to: ReadonlyMap<string, NodePose>,
  t: number,
): Map<string, NodePose> {
  const out = new Map<string, NodePose>()
  for (const [name, tp] of to) {
    const fp = from.get(name) ?? tp
    out.set(name, { x: fp.x + (tp.x - fp.x) * t, y: fp.y + (tp.y - fp.y) * t })
  }
  return out
}

// Fixed node size — the React Flow node renders exactly this box.
// ranksep/nodesep are chosen so a three-layer chain fits the desktop
// canvas beside the aside without panning.
export const NODE_W = 148
export const NODE_H = 54
// ranksep is tight on purpose: the graph canvas beside the aside is
// ~500px at desktop width and minZoom is 0.4, so a six-rank chain must
// stay under ~1260 flow units to remain fittable; a three-layer chain
// lands near 560 and fits comfortably.
const RANK_SEP = 64
const NODE_SEP = 32
// Section names are [a-z][a-z0-9_]* — a space safely delimits pair keys.
const SEP = ' '

interface Pair {
  from: string
  to: string
  names: string[]
}

// layoutGraph delegates positioning to dagre: rankdir LR, one rank per
// dependency layer. Nodes are inserted in `names` order and links in
// (from, to) notebook order so repeated calls produce identical output.
// Parallel edges merge into one link carrying every name; edges naming
// an unknown endpoint are dropped; dagre's acyclicer keeps cycles
// terminating with every node still placed.
export function layoutGraph(names: string[], edges: GraphEdge[]): GraphLayout {
  const idx = new Map<string, number>()
  names.forEach((n, i) => { if (!idx.has(n)) idx.set(n, i) })
  const byIdx = (a: string, b: string) => (idx.get(a) ?? 0) - (idx.get(b) ?? 0)

  // Merge parallel edges into one link per ordered pair.
  const pairs = new Map<string, Pair>()
  for (const e of edges) {
    if (!idx.has(e.from) || !idx.has(e.to)) continue
    const k = e.from + SEP + e.to
    const p = pairs.get(k)
    if (p) {
      if (!p.names.includes(e.name)) p.names.push(e.name)
      continue
    }
    pairs.set(k, { from: e.from, to: e.to, names: [e.name] })
  }
  const links = [...pairs.values()].sort((a, b) => byIdx(a.from, b.from) || byIdx(a.to, b.to))
  if (names.length === 0) return { nodes: [], links: [], width: 0, height: 0 }

  const g = new dagre.graphlib.Graph()
  g.setGraph({ rankdir: 'LR', nodesep: NODE_SEP, ranksep: RANK_SEP, marginx: 8, marginy: 8 })
  g.setDefaultEdgeLabel(() => ({}))
  for (const n of names) g.setNode(n, { width: NODE_W, height: NODE_H })
  for (const l of links) g.setEdge(l.from, l.to)
  dagre.layout(g)

  const nodes: GraphNodeBox[] = names.map((name) => {
    const c = g.node(name) // dagre reports the node CENTER
    return { name, x: c.x - NODE_W / 2, y: c.y - NODE_H / 2 }
  })
  const gw = g.graph()
  return { nodes, links, width: gw.width ?? 0, height: gw.height ?? 0 }
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
