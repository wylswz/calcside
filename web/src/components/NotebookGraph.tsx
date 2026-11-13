import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import {
  Background,
  BackgroundVariant,
  Controls,
  Handle,
  MarkerType,
  Position,
  ReactFlow,
  ReactFlowProvider,
  useNodesInitialized,
  useReactFlow,
  type Edge,
  type Node,
  type NodeProps,
} from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import type { Notebook, NotebookSectionMeta } from '../api'
import {
  ancestors,
  descendants,
  interpolatePositions,
  layoutDelta,
  layoutGraph,
  NODE_H,
  NODE_W,
  type GraphEdge,
  type GraphLayout,
  type GraphLink,
  type NodePose,
} from '../notebookGraph'
import { sectionStateLabel } from './SectionCard'
import { Button, SwapLabel } from './ui'
import './notebook.css'

type GraphEdgeT = Notebook['graph']['edges'][number]
type GraphDiag = Notebook['graph']['diagnostics'][number]

export interface GraphStructure {
  /** section names in notebook order */
  names: string[]
  edges: GraphEdgeT[]
  diagnostics: GraphDiag[]
}

// Diagnostics that refuse the sections they touch — parse_error only
// marks the section itself, which still runs and fails honestly.
const BLOCKING_KINDS = new Set(['duplicate_define', 'missing_provider', 'cycle'])

interface Props {
  /** LIVE structure — every poll re-lays the graph out with motion */
  structure: GraphStructure
  /** LIVE section meta by name — state/once/description/effects paint */
  metas: Map<string, NotebookSectionMeta>
  selected: string
  onSelect: (name: string) => void
  onClear: () => void
  /** switch to code view and land on the section's card */
  onOpenCode: (name: string) => void
  /** run rights + in-flight state for the aside's Run section action */
  canRunSection?: boolean
  runBusy?: boolean
  runLabel?: string
  onRunSection?: (name: string) => void
  /** section currently running — its node pulses like the head chip */
  running?: string | null
}

// linkLabel renders the edge caption: first two names plus a +N
// overflow count; the full list lives in the label's <title>.
function linkLabel(names: string[]): string {
  const head = names.slice(0, 2).join(', ')
  return names.length > 2 ? `${head} +${names.length - 2}` : head
}

const reducedMotion = () => window.matchMedia('(prefers-reduced-motion: reduce)').matches

// Motion budget (P4): node positions tween over TWEEN_MS ease-out,
// entering nodes/edges fade in over the same window, removed nodes and
// their links fade out over EXIT_MS then unmount. All JS-driven —
// edges follow node positions because React Flow recomputes edge
// geometry from the nodes array every frame.
const TWEEN_MS = 250
const EXIT_MS = 200
const easeOut = (t: number) => 1 - Math.pow(1 - t, 3)

interface Pose {
  x: number
  y: number
  /** 1 = fully shown; entering nodes ramp 0→1, exits 1→0 */
  o: number
}

interface ExitAnim {
  /** frozen node snapshot — data does not update during the fade */
  tmpl: NbFlowNode
  startO: number
  startT: number
  o: number
}

interface Tween {
  start: number
  fromPos: Map<string, NodePose>
  toPos: Map<string, NodePose>
  fromO: Map<string, number>
}

interface NbNodeData extends Record<string, unknown> {
  name: string
  state?: string
  once: boolean
  running: boolean
  description?: string
  diagCount: number
  diagTitle: string
  selected: boolean
  hi: boolean
  dim: boolean
  onFocusNode: (name: string) => void
}

type NbFlowNode = Node<NbNodeData, 'nb'>

// NbNode is the read-only section node: name, state dot, once tag and
// diagnostic marker inside a React Flow node with minimal
// non-connectable handles. Selection stays OURS — the inner button
// owns click/keyboard and drives ?section=; React Flow keeps no
// second selection state.
function NbNode({ data }: NodeProps<NbFlowNode>) {
  const d = data
  return (
    <>
      <Handle type="target" position={Position.Left} isConnectable={false} className="nb-graph-handle" />
      <button
        type="button"
        className={
          'nb-graph-node' +
          (d.selected ? ' is-sel' : '') +
          (d.hi ? ' is-hi' : '')
        }
        data-testid="nb-graph-node"
        data-section={d.name}
        data-state={d.state}
        data-running={d.running || undefined}
        aria-pressed={d.selected}
        title={d.description || d.name}
        onFocus={(e) => {
          // Keyboard focus (Tab) on an off-screen node pans it into
          // view; mouse clicks never match :focus-visible → no move.
          if (e.currentTarget.matches(':focus-visible')) d.onFocusNode(d.name)
        }}
      >
        <span className="nb-graph-node-name">{d.name}</span>
        <span className="nb-graph-node-meta">
          <span className="nb-dir-dot" data-state={d.state} />
          <span className="sr-only">{sectionStateLabel(d.state as NotebookSectionMeta['state'])}</span>
          {d.once && <span className="nb-once">once</span>}
          {d.diagCount > 0 && <span className="nb-graph-node-diag" title={d.diagTitle}>!</span>}
        </span>
      </button>
      <Handle type="source" position={Position.Right} isConnectable={false} className="nb-graph-handle" />
    </>
  )
}

const nodeTypes = { nb: NbNode }

type Hi = { all: Set<string>; up: Set<string>; down: Set<string> } | null

interface NodePaint {
  metas: Map<string, NotebookSectionMeta>
  selected: string
  hi: Hi
  diagsBySection: Map<string, GraphDiag[]>
  running: string | null
  onFocusNode: (name: string) => void
}

// makeNode builds one controlled RF node. `pose` carries the animated
// position and fade opacity (wrapper-level, so it never fights the
// dim class which now paints the inner elements only).
function makeNode(name: string, pose: Pose, d: NodePaint): NbFlowNode {
  const meta = d.metas.get(name)
  const isSel = d.selected === name
  const inHi = !!d.hi && d.hi.all.has(name) && !isSel
  const dim = !!d.hi && !d.hi.all.has(name)
  const diags = d.diagsBySection.get(name) ?? []
  return {
    id: name,
    type: 'nb',
    position: { x: pose.x, y: pose.y },
    width: NODE_W,
    height: NODE_H,
    draggable: false,
    connectable: false,
    selectable: false,
    focusable: false,
    style: { opacity: pose.o },
    className: 'nb-graph-rf' + (dim ? ' is-dim' : ''),
    data: {
      name,
      state: meta?.state,
      once: meta?.rerun === 'once',
      running: d.running === name,
      description: meta?.description,
      diagCount: diags.length,
      diagTitle: diags.map((x) => x.message).join('\n'),
      selected: isSel,
      hi: inHi,
      dim,
      onFocusNode: d.onFocusNode,
    },
  }
}

// GraphCanvas owns the React Flow instance and the animated re-layout:
// dagre positions become controlled nodes whose poses are interpolated
// in JS each frame (edges follow automatically), the viewport only
// moves on first reveal or an explicit user gesture (S3/S5/S6), and
// polls re-lay the graph out live instead of waiting behind a refresh.
function GraphCanvas({
  layout,
  metas,
  selected,
  onSelect,
  hi,
  diagsBySection,
  running,
}: {
  layout: GraphLayout
  metas: Map<string, NotebookSectionMeta>
  selected: string
  onSelect: (name: string) => void
  hi: Hi
  diagsBySection: Map<string, GraphDiag[]>
  running: string | null
}) {
  const rf = useReactFlow()
  const wrapRef = useRef<HTMLDivElement | null>(null)
  const [visible, setVisible] = useState(false)
  const [flowReady, setFlowReady] = useState(false)
  // Explicit width/height fields make every node "measured" for bounds
  // purposes immediately — fitView never races ResizeObserver.
  const nodesReady = useNodesInitialized({ includeHiddenNodes: true })

  // The panel is `hidden` while Code view is up (C2/C3) — React Flow
  // measures a hidden container as zero, so the first fit waits for an
  // IntersectionObserver to report the canvas actually on screen.
  useEffect(() => {
    const el = wrapRef.current
    if (!el) return
    const io = new IntersectionObserver(([entry]) => setVisible(entry.isIntersecting), { threshold: 0 })
    io.observe(el)
    return () => io.disconnect()
  }, [])

  // Node clicks (RF's onNodeClick — drag-threshold aware, so a pan that
  // starts on a node is never a selection) mark the pick so the
  // selection effect knows the node is already under the pointer — it
  // must never move the viewport.
  const nodePick = useRef('')
  const onSelectRef = useRef(onSelect)
  useEffect(() => { onSelectRef.current = onSelect })

  const panToNode = useCallback((name: string) => {
    const pose = poseRef.current.get(name)
    const box = pose ?? layout.nodes.find((b) => b.name === name)
    const el = wrapRef.current
    if (!box || !el) return
    const vp = rf.getViewport()
    const w = el.clientWidth
    const h = el.clientHeight
    // Already fully inside the viewport → no movement at all.
    const x0 = box.x * vp.zoom + vp.x
    const y0 = box.y * vp.zoom + vp.y
    const x1 = x0 + NODE_W * vp.zoom
    const y1 = y0 + NODE_H * vp.zoom
    if (x0 >= 0 && y0 >= 0 && x1 <= w && y1 <= h) return
    void rf.setCenter(box.x + NODE_W / 2, box.y + NODE_H / 2, {
      zoom: vp.zoom,
      duration: reducedMotion() ? 0 : 200,
    })
  }, [layout, rf])

  // --- animated re-layout -------------------------------------------------
  // poseMap is the authoritative DISPLAYED pose of every live node —
  // positions never derive straight from `layout`, so a structure
  // change can never snap nodes before the tween starts. exits holds
  // nodes still fading out; tweenRef drives the rAF interpolation.
  const [poseMap, setPoseMap] = useState<Map<string, Pose>>(new Map())
  const poseRef = useRef<Map<string, Pose>>(new Map())
  const [exits, setExits] = useState<Map<string, ExitAnim>>(new Map())
  const exitsRef = useRef<Map<string, ExitAnim>>(new Map())
  const tweenRef = useRef<Tween | null>(null)
  const rafRef = useRef(0)
  const hasPopulated = useRef(false)
  const prevLayout = useRef<GraphLayout | null>(null)

  // Latest paint inputs for reconcile — exit node snapshots freeze the
  // current data, so reconcile must read the committed render's values.
  const dataRef = useRef<NodePaint>({ metas, selected, hi, diagsBySection, running, onFocusNode: panToNode })
  useEffect(() => {
    dataRef.current = { metas, selected, hi, diagsBySection, running, onFocusNode: panToNode }
  })

  const tick = useCallback((now: number) => {
    const tw = tweenRef.current
    if (tw) {
      const t = easeOut(Math.min(1, (now - tw.start) / TWEEN_MS))
      const interp = interpolatePositions(tw.fromPos, tw.toPos, t)
      const next = new Map<string, Pose>()
      for (const [name, p] of interp) {
        const o0 = tw.fromO.get(name) ?? 1
        next.set(name, { x: p.x, y: p.y, o: o0 + (1 - o0) * t })
      }
      poseRef.current = next
      setPoseMap(next)
      if (t >= 1) tweenRef.current = null
    }
    const ex = exitsRef.current
    if (ex.size) {
      const nextEx = new Map<string, ExitAnim>()
      for (const [name, e] of ex) {
        const o = Math.max(0, e.startO * (1 - (now - e.startT) / EXIT_MS))
        if (o > 0) nextEx.set(name, { ...e, o })
      }
      exitsRef.current = nextEx
      setExits(nextEx)
    }
    if (tweenRef.current || exitsRef.current.size) {
      rafRef.current = requestAnimationFrame(tick)
    } else {
      rafRef.current = 0
    }
  }, [])

  const ensureLoop = useCallback(() => {
    if (!rafRef.current) rafRef.current = requestAnimationFrame(tick)
  }, [tick])

  // Edge fades are CSS animations guarded by prefers-reduced-motion —
  // reconcile only tags the ids; timeouts retire the tags.
  const [edgeFade, setEdgeFade] = useState<ReadonlySet<string>>(new Set())
  const edgeFadeRef = useRef(new Set<string>())
  const fadeEdgeIn = useCallback((id: string) => {
    if (edgeFadeRef.current.has(id)) return
    edgeFadeRef.current.add(id)
    setEdgeFade(new Set(edgeFadeRef.current))
    window.setTimeout(() => {
      edgeFadeRef.current.delete(id)
      setEdgeFade(new Set(edgeFadeRef.current))
    }, TWEEN_MS + 60)
  }, [])

  const [ghosts, setGhosts] = useState<Edge[]>([])
  const spawnGhost = useCallback((l: GraphLink) => {
    const id = `${l.from}→${l.to}`
    const ghost: Edge = {
      id,
      source: l.from,
      target: l.to,
      type: 'default',
      label: <><title>{l.names.join(', ')}</title>{linkLabel(l.names)}</>,
      labelShowBg: true,
      labelBgStyle: { fill: 'var(--detail-work)' },
      labelBgBorderRadius: 4,
      labelStyle: { fill: 'rgb(var(--mute))', fontSize: 10 },
      markerEnd: { type: MarkerType.ArrowClosed, width: 14, height: 14, color: 'rgb(var(--mute) / 0.55)' },
      style: { stroke: 'rgb(var(--mute) / 0.55)', strokeWidth: 1.5 },
      className: 'nb-edge-out',
      ariaLabel: `${l.from} → ${l.to}: ${l.names.join(', ')}`,
    }
    setGhosts((prev) => (prev.some((e) => e.id === id) ? prev : [...prev, ghost]))
    window.setTimeout(() => {
      setGhosts((prev) => prev.filter((e) => e.id !== id))
    }, EXIT_MS + 40)
  }, [])

  // reconcile applies a new dagre layout to the displayed poses: shared
  // names tween from their CURRENT interpolated positions (a superseding
  // poll retargets mid-flight, no snap, no queue), entering names fade
  // in at their final spot, vanished names freeze and fade out, and an
  // unchanged structure — identical deterministic positions — skips the
  // tween entirely so state-only polls move nothing.
  const reconcile = useCallback((to: GraphLayout) => {
    const now = performance.now()
    const first = !hasPopulated.current
    hasPopulated.current = true
    const reduced = reducedMotion()
    const prev = prevLayout.current
    prevLayout.current = to

    const targetPos = new Map<string, NodePose>()
    for (const n of to.nodes) targetPos.set(n.name, { x: n.x, y: n.y })

    // Live `from` poses: what's on screen right now.
    const from = new Map<string, Pose>()
    for (const [name, p] of poseRef.current) {
      if (targetPos.has(name)) from.set(name, { ...p })
    }
    // Exits that re-appeared are rescued — they rejoin live from their
    // frozen pose and current opacity instead of restarting anywhere.
    const nextExits = new Map(exitsRef.current)
    let rescued = 0
    for (const [name, ex] of exitsRef.current) {
      if (targetPos.has(name)) {
        nextExits.delete(name)
        from.set(name, { x: ex.tmpl.position.x, y: ex.tmpl.position.y, o: ex.o })
        rescued++
      }
    }
    // Live names that vanished start their exit fade at the current pose.
    const d = dataRef.current
    let newExits = 0
    for (const [name, p] of poseRef.current) {
      if (targetPos.has(name) || nextExits.has(name)) continue
      const pose: Pose = { x: p.x, y: p.y, o: p.o }
      nextExits.set(name, { tmpl: makeNode(name, pose, d), startO: p.o, startT: now, o: p.o })
      newExits++
    }
    // Entering names are already at their final position — only opacity
    // animates (from 0, or from the rescued exit's current opacity).
    for (const name of targetPos.keys()) {
      if (!from.has(name)) {
        const tp = targetPos.get(name)!
        from.set(name, { x: tp.x, y: tp.y, o: 0 })
      }
    }

    const instant = first || reduced
    let needsAnim = newExits > 0 || rescued > 0
    if (!needsAnim) {
      for (const [name, tp] of targetPos) {
        const f = from.get(name)!
        if (Math.abs(f.x - tp.x) > 0.01 || Math.abs(f.y - tp.y) > 0.01 || f.o < 0.999) {
          needsAnim = true
          break
        }
      }
    }

    if (instant) {
      tweenRef.current = null
      exitsRef.current = new Map()
      setExits(new Map())
      const final = new Map<string, Pose>()
      for (const [name, tp] of targetPos) final.set(name, { x: tp.x, y: tp.y, o: 1 })
      poseRef.current = final
      setPoseMap(final)
      if (rafRef.current) {
        cancelAnimationFrame(rafRef.current)
        rafRef.current = 0
      }
    } else {
      exitsRef.current = nextExits
      setExits(nextExits)
      if (needsAnim) {
        const fromPos = new Map<string, NodePose>()
        const fromO = new Map<string, number>()
        for (const [name, p] of from) {
          fromPos.set(name, { x: p.x, y: p.y })
          fromO.set(name, p.o)
        }
        tweenRef.current = { start: now, fromPos, toPos: targetPos, fromO }
        // Seed the displayed poses at t=0 — movers at their current
        // interpolated spot, entrants at opacity 0 — so the synchronous
        // re-render this layout effect triggers already carries the
        // fade-in start. Without it poseMap still lacks the new names
        // and rfNodes falls back to opacity 1, which React Flow's
        // passive prop→store sync can paint for a frame.
        poseRef.current = from
        setPoseMap(from)
        ensureLoop()
      } else {
        // Nothing moved — drop the tween (if a previous one was still
        // settling) and write the exact final poses; exits keep fading.
        tweenRef.current = null
        const final = new Map<string, Pose>()
        for (const [name, tp] of targetPos) final.set(name, { x: tp.x, y: tp.y, o: 1 })
        poseRef.current = final
        setPoseMap(final)
        if (exitsRef.current.size) ensureLoop()
      }
    }

    // Edge fades only on real structure changes — a first populate or a
    // state-only poll adds/removes no links anyway (delta is empty).
    if (prev && !instant) {
      const d = layoutDelta(prev, to)
      for (const l of d.linksAdded) fadeEdgeIn(`${l.from}→${l.to}`)
      for (const l of d.linksRemoved) spawnGhost(l)
    }
  }, [ensureLoop, fadeEdgeIn, spawnGhost])

  // The dagre layout is deterministic, so a state-only poll produces an
  // identical signature — reconcile only runs on real structure moves.
  // LAYOUT effect: its setPoseMap/setExits updates flush synchronously
  // before paint, so a structural poll can never paint one frame with
  // the new layout's fallback poses (entering nodes at opacity 1,
  // removed nodes already gone) ahead of the tween/exit snapshots.
  const sigRef = useRef('')
  useLayoutEffect(() => {
    const sig =
      layout.nodes.map((n) => `${n.name}@${n.x.toFixed(2)},${n.y.toFixed(2)}`).join('|') +
      '#' + layout.links.map((l) => `${l.from}>${l.to}`).join(',')
    if (sig === sigRef.current) return
    sigRef.current = sig
    reconcile(layout)
  }, [layout, reconcile])

  useEffect(() => () => {
    if (rafRef.current) cancelAnimationFrame(rafRef.current)
  }, [])

  // fitView is allowed ONLY here: the first reveal of the panel after
  // data is on screen. Later structure changes re-lay out with motion
  // but never move the viewport; the Controls fit button and selection
  // pans are the other explicit user actions (S5/S6). It waits for the
  // panel to be visible, the flow initialized and node bounds known —
  // a hidden container measures as zero. Both RAF ids are tracked so
  // cleanup cancels a pending fit entirely, and `done` flips only after
  // fitView resolves — a cancelled first fit retries on the next reveal
  // instead of being lost or landing while hidden. Once done, no
  // refit ever runs again.
  const fitRef = useRef({ raf1: 0, raf2: 0, done: false })
  const fitDone = useRef<Promise<unknown>>(Promise.resolve())
  useEffect(() => {
    const st = fitRef.current
    if (!visible || !flowReady || !nodesReady || st.done || layout.nodes.length === 0) return
    let alive = true
    // Two frames: ResizeObserver notifications are delivered after rAF
    // callbacks in a frame, so the first rAF only schedules — the
    // second runs after the unhidden container's real dimensions have
    // reached the store.
    st.raf1 = requestAnimationFrame(() => {
      st.raf2 = requestAnimationFrame(() => {
        st.raf2 = 0
        fitDone.current = rf.fitView({ padding: 0.15, maxZoom: 1, duration: 0, includeHiddenNodes: true })
          .then(() => {
            if (!alive) return
            st.done = true
            // e2e waits on this before trusting the viewport transform.
            if (wrapRef.current) wrapRef.current.dataset.fitted = '1'
          })
      })
    })
    return () => {
      alive = false
      if (st.raf1) { cancelAnimationFrame(st.raf1); st.raf1 = 0 }
      if (st.raf2) { cancelAnimationFrame(st.raf2); st.raf2 = 0 }
    }
  }, [visible, flowReady, nodesReady, layout, rf])

  // Selection pans: chip clicks, aside picks and deep links arrive as
  // ?section= changes; the node is brought into view at the CURRENT
  // zoom — never a refit. Returning to the panel also re-checks the
  // current selection (the viewport may have been preserved). Runs
  // after any pending fit so the visibility check reads the final
  // viewport.
  const prevSelected = useRef(selected)
  const wasVisible = useRef(false)
  useEffect(() => {
    const becameVisible = visible && !wasVisible.current
    wasVisible.current = visible
    if (!visible) {
      prevSelected.current = selected
      return
    }
    const selChanged = selected !== prevSelected.current
    prevSelected.current = selected
    if (!selected || (!selChanged && !becameVisible)) return
    if (nodePick.current === selected) {
      nodePick.current = ''
      return
    }
    let dead = false
    void fitDone.current.then(() => {
      if (!dead) panToNode(selected)
    })
    return () => { dead = true }
  }, [selected, visible, panToNode])

  // DOM order = visual order (column, then row) so Tab walks the graph
  // in layout order; positions themselves come from the animated poses.
  const rfNodes = useMemo<NbFlowNode[]>(() => {
    const out: NbFlowNode[] = [...layout.nodes]
      .sort((a, b) => a.x - b.x || a.y - b.y)
      .map((n) => {
        const pose = poseMap.get(n.name) ?? { x: n.x, y: n.y, o: 1 }
        return makeNode(n.name, pose, {
          metas, selected, hi, diagsBySection, running, onFocusNode: panToNode,
        })
      })
    for (const ex of exits.values()) {
      // Exiting nodes keep their frozen paint; only opacity animates,
      // and they stop taking pointer input while they fade.
      out.push({
        ...ex.tmpl,
        className: `${ex.tmpl.className ?? ''} is-exit`,
        style: { ...ex.tmpl.style, opacity: ex.o },
      })
    }
    return out
  }, [layout, poseMap, exits, metas, selected, hi, diagsBySection, running, panToNode])

  const rfEdges = useMemo<Edge[]>(() => {
    const live = layout.links.map((l) => {
      const hiLink = !!hi && hi.all.has(l.from) && hi.all.has(l.to)
      const dimLink = !!hi && !hiLink
      // All edge paint goes through RF's style props — inline styles
      // resolve rgb(var(--…)) so both themes stay token-driven. Arrowheads
      // live in shared <defs> and cannot be reached by className, so the
      // marker colour is set per edge state the same way.
      const stroke = hiLink ? 'rgb(var(--accent))' : dimLink ? 'rgb(var(--mute) / 0.25)' : 'rgb(var(--mute) / 0.55)'
      const fade = edgeFade.has(`${l.from}→${l.to}`) ? 'nb-edge-in' : ''
      const stateCls = hiLink ? 'nb-edge-hi' : dimLink ? 'nb-edge-dim' : ''
      const cls = [fade, stateCls].filter(Boolean).join(' ') || undefined
      return {
        id: `${l.from}→${l.to}`,
        source: l.from,
        target: l.to,
        type: 'default',
        label: <><title>{l.names.join(', ')}</title>{linkLabel(l.names)}</>,
        labelShowBg: true,
        labelBgStyle: { fill: 'var(--detail-work)' },
        labelBgBorderRadius: 4,
        labelStyle: { fill: hiLink ? 'rgb(var(--accent))' : 'rgb(var(--mute))', fontSize: 10 },
        markerEnd: { type: MarkerType.ArrowClosed, width: 14, height: 14, color: stroke },
        style: { stroke, strokeWidth: hiLink ? 2 : 1.5 },
        className: cls,
        animated: hiLink && !reducedMotion(),
        ariaLabel: `${l.from} → ${l.to}: ${l.names.join(', ')}`,
      }
    })
    // Ghost edges are removed links still fading out — drop any whose
    // pair came back (the live edge already covers it).
    const liveIds = new Set(live.map((e) => e.id))
    return [...live, ...ghosts.filter((g) => !liveIds.has(g.id))]
  }, [layout, hi, ghosts, edgeFade])

  return (
    <div className="nb-graph-canvas" ref={wrapRef} data-testid="nb-graph-canvas">
      <ReactFlow
        nodes={rfNodes}
        edges={rfEdges}
        nodeTypes={nodeTypes}
        colorMode="system"
        nodesDraggable={false}
        nodesConnectable={false}
        nodesFocusable={false}
        edgesFocusable={false}
        elementsSelectable={false}
        zoomOnScroll={false}
        zoomOnDoubleClick={false}
        minZoom={0.4}
        maxZoom={1.5}
        onInit={() => setFlowReady(true)}
        // RF's node click is drag-threshold aware — a pan that starts on
        // a node never becomes a selection. It also makes the node
        // wrapper pointer-interactive (hasPointerEvents).
        onNodeClick={(_, node) => {
          nodePick.current = node.id
          onSelectRef.current(node.id)
        }}
        proOptions={{ hideAttribution: false }}
      >
        <Background variant={BackgroundVariant.Dots} gap={22} size={1.5} />
        <Controls showInteractive={false} position="bottom-left" />
      </ReactFlow>
    </div>
  )
}

// NotebookGraph is the read-only dependency view (EP-0023): React Flow
// renders the dagre-laid-out sections and their provider→consumer
// edges; the aside carries issues or the selected node's reads. The
// structure follows polls live — a changed layout tweens node positions
// (~250ms ease-out, JS-driven so edges stay attached), new nodes/edges
// fade in, removed ones fade out; the viewport never moves on a poll
// and an unchanged structure moves nothing (S1/S5).
export function NotebookGraph({ structure, metas, selected, onSelect, onClear, onOpenCode, canRunSection, runBusy, runLabel, onRunSection, running }: Props) {
  const layout = useMemo(
    () => layoutGraph(structure.names, structure.edges as GraphEdge[]),
    [structure],
  )
  const nodeSet = useMemo(() => new Set(structure.names), [structure])

  const hi = useMemo(() => {
    if (!selected || !nodeSet.has(selected)) return null
    const up = ancestors(selected, structure.edges as GraphEdge[])
    const down = descendants(selected, structure.edges as GraphEdge[])
    return { all: new Set([selected, ...up, ...down]), up, down }
  }, [selected, nodeSet, structure])

  const diagsBySection = useMemo(() => {
    const m = new Map<string, GraphDiag[]>()
    for (const d of structure.diagnostics) {
      for (const s of d.sections) m.set(s, [...(m.get(s) ?? []), d])
    }
    return m
  }, [structure])

  const selMeta = selected ? metas.get(selected) : undefined
  const selExists = !!selected && nodeSet.has(selected)

  // Reads/Read-by are derived from LIVE meta uses plus the live edges —
  // a name with no edge is unresolved: ambiguous when a duplicate
  // diagnostic names it, otherwise no provider.
  const reads = useMemo(() => {
    if (!selExists || !selMeta) return [] as { name: string; providers: string[]; problem: '' | 'ambiguous' | 'missing' }[]
    const dup = new Set(structure.diagnostics.filter((d) => d.kind === 'duplicate_define').flatMap((d) => d.names))
    return (selMeta.uses ?? []).map((u) => {
      const providers = [...new Set(structure.edges.filter((e) => e.to === selected && e.name === u).map((e) => e.from))]
      const problem = providers.length ? '' : dup.has(u) ? 'ambiguous' : 'missing'
      return { name: u, providers, problem: problem as '' | 'ambiguous' | 'missing' }
    })
  }, [selExists, selMeta, selected, structure])

  const readBy = useMemo(() => {
    if (!selected) return [] as { consumer: string; names: string[] }[]
    const m = new Map<string, string[]>()
    for (const e of structure.edges) {
      if (e.from !== selected) continue
      m.set(e.to, [...(m.get(e.to) ?? []), e.name])
    }
    return [...m.entries()].map(([consumer, names]) => ({ consumer, names }))
  }, [selected, structure])

  const nodeDiags = selected ? diagsBySection.get(selected) ?? [] : []
  const issueCount = structure.diagnostics.length

  // Aside picks are explicit user selections — the canvas reveals the
  // target through the same ?section= → pan path as chip clicks.
  const pickFromAside = (name: string) => onSelect(name)

  return (
    <div className="nb-graph">
      {structure.names.length === 0 ? (
        <div className="nb-graph-empty">No sections yet</div>
      ) : (
        <div className="nb-graph-body">
          <ReactFlowProvider>
            <GraphCanvas
              layout={layout}
              metas={metas}
              selected={selected}
              onSelect={onSelect}
              hi={hi}
              diagsBySection={diagsBySection}
              running={running ?? null}
            />
          </ReactFlowProvider>

          <aside className="nb-graph-aside" aria-label="Graph details">
            {!selExists ? (
              <div className="nb-graph-aside-empty">
                {issueCount === 0 ? (
                  <h3 className="nb-graph-aside-h">No issues</h3>
                ) : (
                  <>
                    <h3 className="nb-graph-aside-h">Graph issues ({issueCount})</h3>
                    <ul className="nb-graph-issues">
                      {structure.diagnostics.map((d, i) => (
                        <li key={i} className="nb-graph-issue">
                          <span className="nb-graph-issue-msg">
                            {d.message}
                            {BLOCKING_KINDS.has(d.kind) && <span className="nb-graph-issue-tag">blocks runs</span>}
                          </span>
                          <span className="nb-graph-issue-secs">
                            {d.sections.map((s) => (
                              <button key={s} type="button" className="nb-graph-sec-btn" onClick={() => pickFromAside(s)}>
                                {s}
                              </button>
                            ))}
                          </span>
                        </li>
                      ))}
                    </ul>
                  </>
                )}
              </div>
            ) : (
              <div className="nb-graph-detail">
                <div className="nb-graph-detail-head">
                  <h3 className="nb-graph-detail-name">{selected}</h3>
                  <button type="button" className="nb-graph-clear" onClick={onClear}>Clear</button>
                </div>
                {selMeta && (
                  <>
                    {selMeta.description && <p className="nb-graph-detail-desc">{selMeta.description}</p>}
                    <p className="nb-graph-detail-line">
                      <span className="nb-dir-dot" data-state={selMeta.state} />
                      <span>{sectionStateLabel(selMeta.state)}</span>
                      {selMeta.rerun === 'once' && <span className="nb-once">once</span>}
                    </p>
                    <div className="nb-graph-detail-block">
                      <h4 className="nb-graph-detail-h">Effects</h4>
                      {selMeta.effects.length === 0 ? (
                        <p className="nb-graph-detail-note">no effects observed</p>
                      ) : (
                        <ul className="nb-graph-detail-list">
                          {selMeta.effects.map((fx) => <li key={fx}><code>{fx}</code></li>)}
                        </ul>
                      )}
                    </div>
                    {selMeta.defines.length > 0 && (
                      <div className="nb-graph-detail-block">
                        <h4 className="nb-graph-detail-h">Defines</h4>
                        <p className="nb-graph-detail-line-text"><code>{selMeta.defines.join(', ')}</code></p>
                      </div>
                    )}
                  </>
                )}

                {reads.length > 0 && (
                  <div className="nb-graph-detail-block">
                    <h4 className="nb-graph-detail-h">Reads</h4>
                    <ul className="nb-graph-reads">
                      {reads.map((r) => (
                        <li key={r.name}>
                          {r.providers.length > 0 ? (
                            <>
                              <code>{r.name}</code>
                              <span className="nb-graph-read-arrow">←</span>
                              {r.providers.map((p) => (
                                <button key={p} type="button" className="nb-graph-sec-btn" onClick={() => pickFromAside(p)}>{p}</button>
                              ))}
                            </>
                          ) : (
                            <span className="nb-graph-unresolved">
                              <code>{r.name}</code>
                              {r.problem === 'ambiguous' ? ' — ambiguous' : ' — no provider'}
                            </span>
                          )}
                        </li>
                      ))}
                    </ul>
                  </div>
                )}

                {readBy.length > 0 && (
                  <div className="nb-graph-detail-block">
                    <h4 className="nb-graph-detail-h">Read by</h4>
                    <ul className="nb-graph-reads">
                      {readBy.map((r) => (
                        <li key={r.consumer}>
                          <button type="button" className="nb-graph-sec-btn" onClick={() => pickFromAside(r.consumer)}>{r.consumer}</button>
                          <span className="nb-graph-read-names">({[...new Set(r.names)].join(', ')})</span>
                        </li>
                      ))}
                    </ul>
                  </div>
                )}

                {nodeDiags.length > 0 && (
                  <div className="nb-graph-detail-block">
                    <h4 className="nb-graph-detail-h">Issues</h4>
                    <ul className="nb-graph-issues">
                      {nodeDiags.map((d, i) => <li key={i} className="nb-graph-issue-msg">{d.message}</li>)}
                    </ul>
                  </div>
                )}

                <div className="nb-graph-detail-foot">
                  {canRunSection && selMeta && onRunSection && (
                    <Button
                      variant="ghost" size="xs"
                      disabled={runBusy}
                      onClick={() => onRunSection(selected)}
                    >
                      <SwapLabel show={runLabel === 'Running…' ? 'Running…' : 'Run section'} options={['Run section', 'Running…']} />
                    </Button>
                  )}
                  <button type="button" className="nb-graph-open" onClick={() => onOpenCode(selected)}>
                    Open code
                  </button>
                </div>
              </div>
            )}
          </aside>
        </div>
      )}
    </div>
  )
}
