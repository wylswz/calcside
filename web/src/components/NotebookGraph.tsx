import { useMemo } from 'react'
import type { Notebook, NotebookSectionMeta } from '../api'
import { ancestors, descendants, layoutGraph, NODE_H, NODE_W, type GraphEdge } from '../notebookGraph'
import { sectionStateLabel } from './SectionCard'
import { Button, SwapLabel } from './ui'
import './notebook.css'

type GraphEdgeT = Notebook['graph']['edges'][number]
type GraphDiag = Notebook['graph']['diagnostics'][number]

export interface GraphStructure {
  /** section names in notebook order — the held snapshot */
  names: string[]
  edges: GraphEdgeT[]
  diagnostics: GraphDiag[]
}

// Diagnostics that refuse the sections they touch — parse_error only
// marks the section itself, which still runs and fails honestly.
const BLOCKING_KINDS = new Set(['duplicate_define', 'missing_provider', 'cycle'])

interface Props {
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
  /** a newer structure is waiting behind "Graph changed — Refresh" */
  changed: boolean
  onRefresh: () => void
  /** parent-owned scroll container ref for refresh scroll restore */
  scrollRef: React.RefObject<HTMLDivElement>
}

// linkLabel renders the midpoint caption: first two names plus a +N
// overflow count; the full list lives in the link's <title>.
function linkLabel(names: string[]): string {
  const head = names.slice(0, 2).join(', ')
  return names.length > 2 ? `${head} +${names.length - 2}` : head
}

// NotebookGraph is the read-only dependency view (EP-0023): nodes are
// section names laid out by notebookGraph.layoutGraph, edges are SVG
// cubics, and the aside carries issues or the selected node's reads.
// Everything here is paint — selection, highlight and the refresh
// banner never resize or move a node (S1/S4).
export function NotebookGraph({ structure, metas, selected, onSelect, onClear, onOpenCode, canRunSection, runBusy, runLabel, onRunSection, changed, onRefresh, scrollRef }: Props) {
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

  const scrollNodeIntoView = (name: string) => {
    const el = scrollRef.current
    const box = layout.nodes.find((n) => n.name === name)
    if (!el || !box) return
    const cx = box.x + NODE_W / 2
    const cy = box.y + NODE_H / 2
    el.scrollLeft = Math.max(0, Math.min(cx - el.clientWidth / 2, el.scrollWidth - el.clientWidth))
    el.scrollTop = Math.max(0, Math.min(cy - el.clientHeight / 2, el.scrollHeight - el.clientHeight))
  }
  // Aside picks are explicit user selections — the canvas may scroll to
  // reveal the target; canvas clicks never scroll (the node is visible).
  const pickFromAside = (name: string) => {
    onSelect(name)
    requestAnimationFrame(() => scrollNodeIntoView(name))
  }

  const selMeta = selected ? metas.get(selected) : undefined
  const selExists = !!selected && nodeSet.has(selected)

  // Reads/Read-by are derived from LIVE meta uses plus the held edges —
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

  return (
    <div className="nb-graph">
      {/* Reserved header row (S4): the note and the change banner keep
          their slots mounted; only paint flips. */}
      <div className="nb-graph-head">
        <span className="nb-graph-note">Running a section first runs its upstream sections that are not fresh.</span>
        <span className="nb-graph-changed" hidden={!changed}>
          <button type="button" className="nb-graph-refresh" onClick={onRefresh}>
            Graph changed — Refresh
          </button>
        </span>
      </div>

      {structure.names.length === 0 ? (
        <div className="nb-graph-empty">No sections yet</div>
      ) : (
        <div className="nb-graph-body">
          <div className="nb-graph-scroll" ref={scrollRef}>
            <div className="nb-graph-layer" style={{ width: layout.width, height: layout.height }}>
              <svg className="nb-graph-edges" width={layout.width} height={layout.height} aria-hidden="true">
                <defs>
                  {(['', '-hi', '-dim'] as const).map((v) => (
                    <marker key={v} id={`nb-graph-arrow${v}`} viewBox="0 0 8 8" refX="7" refY="4" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
                      <path d="M0,0 L8,4 L0,8 z" className={`nb-graph-arrowhead${v}`} />
                    </marker>
                  ))}
                </defs>
                {layout.links.map((l) => {
                  // Cubic midpoint at t=0.5 for the name caption.
                  const c1x = l.x1 + l.cx, c2x = l.x2 - l.cx
                  const mx = (l.x1 + 3 * c1x + 3 * c2x + l.x2) / 8
                  const my = (l.y1 + 3 * l.y1 + 3 * l.y2 + l.y2) / 8
                  const hiLink = !!hi && hi.all.has(l.from) && hi.all.has(l.to)
                  const dimLink = !!hi && !hiLink
                  const variant = hiLink ? '-hi' : dimLink ? '-dim' : ''
                  const d = `M ${l.x1} ${l.y1} C ${c1x} ${l.y1}, ${c2x} ${l.y2}, ${l.x2} ${l.y2}`
                  return (
                    <g key={`${l.from} ${l.to}`} data-from={l.from} data-to={l.to}
                      className={hiLink ? 'nb-graph-link-hi' : dimLink ? 'nb-graph-link-dim' : undefined}>
                      <title>{l.names.join(', ')}</title>
                      <path d={d} className="nb-graph-edge" markerEnd={`url(#nb-graph-arrow${variant})`} />
                      <text x={mx} y={my - 4} className="nb-graph-edge-label" textAnchor="middle">
                        {linkLabel(l.names)}
                      </text>
                    </g>
                  )
                })}
              </svg>
              {layout.nodes.map((n) => {
                const meta = metas.get(n.name)
                const removed = !meta
                const isSel = selected === n.name
                const inHi = !!hi && hi.all.has(n.name) && !isSel
                const dim = !!hi && !hi.all.has(n.name)
                const diags = diagsBySection.get(n.name) ?? []
                return (
                  <button
                    key={n.name}
                    type="button"
                    className={
                      'nb-graph-node' +
                      (isSel ? ' is-sel' : '') +
                      (inHi ? ' is-hi' : '') +
                      (dim ? ' is-dim' : '') +
                      (removed ? ' is-removed' : '')
                    }
                    data-node={n.name}
                    data-state={meta?.state}
                    style={{ left: n.x, top: n.y, width: NODE_W, height: NODE_H }}
                    aria-pressed={isSel}
                    // preventDefault on mousedown stops focus-on-click —
                    // without it the browser pans the overflow canvas to
                    // reveal a clipped node, moving the view on select.
                    onMouseDown={(e) => e.preventDefault()}
                    title={meta?.description || n.name}
                    onClick={() => onSelect(n.name)}
                  >
                    <span className="nb-graph-node-name">{n.name}</span>
                    <span className="nb-graph-node-meta">
                      {removed ? (
                        <span className="nb-graph-node-removed-tag">removed</span>
                      ) : (
                        <>
                          <span className="nb-dir-dot" data-state={meta!.state} />
                          <span className="sr-only">{sectionStateLabel(meta!.state)}</span>
                          {meta!.rerun === 'once' && <span className="nb-once">once</span>}
                        </>
                      )}
                      {diags.length > 0 && <span className="nb-graph-node-diag" title={diags.map((d) => d.message).join('\n')}>!</span>}
                    </span>
                  </button>
                )
              })}
            </div>
          </div>

          <aside className="nb-graph-aside" aria-label="Graph details">
            {!selExists ? (
              <div className="nb-graph-aside-empty">
                {issueCount === 0 ? (
                  <>
                    <h3 className="nb-graph-aside-h">No issues</h3>
                    <p className="nb-graph-aside-note">Running a section first runs its upstream sections that are not fresh.</p>
                  </>
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
                {selMeta ? (
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
                        <>
                          <p className="nb-graph-detail-note">no effects observed</p>
                          <p className="nb-graph-detail-note">observed in last run — not a purity claim</p>
                        </>
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
                ) : (
                  <p className="nb-graph-detail-note">This section is no longer in the notebook — the graph snapshot is held until Refresh.</p>
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
                      title="Run this section — upstream sections that are not fresh run first"
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
