import { Fragment, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useSearchParams } from 'react-router-dom'
import { api, ApiError, Instance, Notebook as NotebookData, NotebookSectionResponse, ResourceUsages, RunSectionsResponse, SaveNotebookSectionResponse } from '../api'
import { AutosaveQueue } from '../autosave'
import { fmtBytes } from '../format'
import { Button, Confirm, Icon, Loading, Notice, RevealNew, SignalMark, SwapLabel } from './ui'
import { SectionCard, SectionValue, sectionFingerprint, sectionPath, sectionStateLabel } from './SectionCard'
import { NotebookGraph, type GraphStructure } from './NotebookGraph'
import { NotebookAddSection, NotebookGraphPane, NotebookInsert, NotebookViewToggle, nextNotebookSectionName } from './NotebookChrome'
import { useHeldOrder } from '../stableList'
import './notebook.css'

// isRestored/isExpired read the typed error codes the API returns for
// the two lifecycle edges a run can hit.
function errCode(e: unknown): string {
  return e instanceof ApiError ? e.code : ''
}

const nbPath = (id: string) => `/api/v1/instances/${id}/notebook`

// MemoryStatus is the workbench footer readout — current/limit inline,
// peak and limit in the tooltip. It renders from the shared inspect
// snapshot the page passes down (undefined while not running or when
// memory reporting is unavailable).
export function MemoryStatus({ resources }: { resources?: ResourceUsages }) {
  const current = resources ? fmtBytes(resources.memory_usage) : '—'
  const limit = resources && resources.memory_max > 0 ? fmtBytes(resources.memory_max) : ''
  const peak = resources && resources.memory_peak > 0 ? fmtBytes(resources.memory_peak) : ''
  return (
    <span className="nb-memory" title={resources
      ? `Memory: ${current}${peak ? `; peak ${peak}` : ''}; ${limit ? `limit ${limit}` : 'no limit'}`
      : 'Memory reporting unavailable'}>
      <span>Memory</span> <strong>{current}</strong>
      {limit && <span className="nb-memory-limit"> / {limit}</span>}
      {!resources && <span className="sr-only"> reporting unavailable</span>}
    </span>
  )
}

// Notebook is the instance's working document: an ordered directory of
// durable sections, each an editable card with autosave — every run is
// a section run in dependency order. Polling never overwrites a dirty
// draft (C2) and the held order keeps polled arrivals behind "Show N
// new" (S5). The parent owns the section outline's open state so it
// can coordinate with the instance inspector on narrow viewports.
export function Notebook({ id, inst, canRun, visible, resources, outlineOpen, onToggleOutline, onOutlineNavigate }: {
  id: string
  inst: Instance
  canRun: boolean
  /** this view is the one on screen — the code scroller can't be targeted while hidden */
  visible: boolean
  /** shared inspect snapshot's memory accounting — undefined when not running */
  resources?: ResourceUsages
  outlineOpen: boolean
  onToggleOutline: () => void
  onOutlineNavigate: () => void
}) {
  const qc = useQueryClient()
  const [params, setParams] = useSearchParams()
  const live = inst.status === 'running'
  const lost = inst.status === 'lost'
  const expired = inst.status === 'expired'
  const canExec = canRun && (live || lost) && inst.status !== 'deleted'
  const canEdit = canRun && live

  const nb = useQuery({
    queryKey: ['notebook', id],
    queryFn: () => api.get<{ notebook?: NotebookData }>(nbPath(id)),
    refetchInterval: live || lost ? 5000 : false,
    retry: false,
  })
  const notebook = nb.data?.notebook
  // codeRef mounts only once the first payload lands (`!notebook` returns
  // null below) — pane-root effects must key off this, not mount order.
  const hasNotebook = notebook !== undefined
  const sections = useMemo(() => notebook?.sections ?? [], [notebook])
  const order = useMemo(() => sections.map((s) => s.name), [sections])

  // Code|Graph view lives in the URL (?nbview=graph; absent = code) so
  // it deep-links and survives reload (C4). BOTH panels stay mounted —
  // editors, drafts, autosave queues and scroll survive toggling (C2/C3).
  const view = params.get('nbview') === 'graph' ? 'graph' : 'code'
  const setView = useCallback((v: 'code' | 'graph') => {
    setParams((p) => {
      const n = new URLSearchParams(p)
      if (v === 'graph') n.set('nbview', 'graph')
      else n.delete('nbview')
      return n
    })
  }, [setParams])

  // The graph renders the LIVE structure: every poll re-lays it out —
  // NotebookGraph tweens node positions (~250ms) and fades entries/
  // exits while the viewport never moves (S5). State, once,
  // description and effects paint from the same payload.
  const graphStructure = useMemo<GraphStructure>(() => ({
    names: sections.map((s) => s.name),
    edges: notebook?.graph?.edges ?? [],
    diagnostics: notebook?.graph?.diagnostics ?? [],
  }), [sections, notebook])
  const graphMetas = useMemo(() => new Map(sections.map((s) => [s.name, s])), [sections])
  const openGraphCode = useCallback((name: string) => {
    // Clear the deep-link guard so returning to this same section still
    // jumps after the code pane scrolled away from it.
    scrolledTo.current = ''
    setParams((p) => {
      const n = new URLSearchParams(p)
      n.delete('nbview') // keep ?section=name — the scroll effect lands on the card
      n.set('section', name)
      return n
    })
  }, [setParams])

  // S5: polled arrivals/reorders wait behind "Show N new"; edits the
  // user just caused call reveal() through onStructural. undefined
  // until the first notebook payload lands — an empty default would
  // seed the held order and count the initial sections as arrivals.
  const held = useHeldOrder(notebook ? sections : undefined, (s) => ({ key: s.name, slot: '' }), { reset: inst.status })
  const shown = held.rows.map((r) => r.item)

  // Registry of live section autosave queues (name → queue) so a run can
  // wait for pending saves before executing saved code.
  const queues = useRef(new Map<string, AutosaveQueue<SectionValue>>())
  const register = useCallback((name: string, q: AutosaveQueue<SectionValue> | null) => {
    if (q) queues.current.set(name, q)
    else queues.current.delete(name)
  }, [])

  const [running, setRunning] = useState<string | null>(null) // 'all' | 'stale' | section name
  const [batch, setBatch] = useState<RunSectionsResponse['report'] | null>(null)
  const [runBlock, setRunBlock] = useState<string[]>([])
  // 'trigger': the run request itself hit a lost instance and did NOT
  // execute. 'partial': a mid-batch loss — earlier sections DID run in
  // the previous runtime, so side effects must be reviewed first.
  const [restoredNotice, setRestoredNotice] = useState<'trigger' | 'partial' | null>(null)
  const [pendingOnce, setPendingOnce] = useState<string | null>(null)
  // Synchronous guard: set before any await so repeated clicks across
  // run controls cannot interleave flushes/POSTs (P1); the server op
  // lease stays the authoritative backstop.
  const runLock = useRef(false)
  // Same synchronous-guard pattern as runLock for section creation:
  // set before the first await so repeat clicks cannot interleave
  // GET/PUT pairs; `adding` mirrors it to disable the controls (P1).
  const addLock = useRef(false)
  const [adding, setAdding] = useState(false)
  const [addErr, setAddErr] = useState('')
  // P5: a single-section run's block/error/restore feedback also lands
  // on that cell's alerts slot — the toolbar status line only carries
  // the one-line summary (S3: nothing is inserted above the cells).
  const [cellNote, setCellNote] = useState<{ name: string; tone: 'warning' | 'neutral'; text: string } | null>(null)
  const [focusName, setFocusName] = useState('') // section whose editor takes focus once mounted
  const [dirFilter, setDirFilter] = useState('')

  // The code pane is its own scroller — the window never moves. Its
  // ref and the programmatic-scroll marker must exist before the
  // structural handler that scrolls after a user-caused reveal.
  const codeRef = useRef<HTMLDivElement | null>(null)
  const dirRef = useRef<HTMLElement | null>(null)
  const sectionsBtnRef = useRef<HTMLButtonElement | null>(null)
  const spyHold = useRef(0)
  const selfScrollAt = useRef(0)
  // One "active section" concept (S1/S3): an explicit pick — a
  // directory row, a graph node, a deep link — marks the row at once
  // and holds the scroll-spy until the programmatic scroll settles or
  // the user scrolls; otherwise the spy marks the topmost card in
  // view. In graph view the selected node IS the active section.
  const [activeNav, setActiveNav] = useState('')
  // scrolledTo records the ?section= navigation this mount already
  // performed so polls and unrelated arrivals never re-scroll (S5).
  const scrolledTo = useRef('')
  // Returns true once the pane was scrolled — false when the card or a
  // measurable root is missing (still loading, or a hidden pane).
  const scrollToCard = useCallback((name: string) => {
    const root = codeRef.current
    const card = document.getElementById(`section-${name}`)
    if (!root || !card || !root.contains(card) || !root.clientHeight) return false
    selfScrollAt.current = performance.now()
    root.scrollTo({
      top: root.scrollTop + card.getBoundingClientRect().top - root.getBoundingClientRect().top,
      behavior: 'instant',
    })
    return true
  }, [])
  const pickSection = useCallback((name: string) => {
    spyHold.current = performance.now() + 600
    setActiveNav(name)
  }, [])

  // Prefix invalidation covers every section detail — including ones
  // whose cards are not mounted yet — and stays correct regardless of
  // which render's `sections` snapshot this callback captured (F3).
  const refresh = useCallback(() => {
    qc.invalidateQueries({ queryKey: ['notebook', id] })
    qc.invalidateQueries({ queryKey: ['executions', id] })
    qc.invalidateQueries({ queryKey: ['execs', id] })
    qc.invalidateQueries({ queryKey: ['instance', id] })
    qc.invalidateQueries({ queryKey: ['inspect', id] })
    qc.invalidateQueries({ queryKey: ['files', id] })
    qc.invalidateQueries({ queryKey: ['audit', id] })
    qc.invalidateQueries({ queryKey: ['section', id] })
  }, [id, qc])

  // A user-caused structural change (create/move/delete) must not hide
  // behind "Show N new": flag it, and once the refetched notebook lands
  // adopt the fresh order wholesale. reveal() called synchronously would
  // freeze the pre-change placements and hold the user's own edit.
  const pendingReveal = useRef(false)
  const pendingScroll = useRef('')
  const onStructural = useCallback((scrollTo = '') => {
    pendingReveal.current = true
    pendingScroll.current = scrollTo
    refresh()
  }, [refresh])
  useEffect(() => {
    if (!pendingReveal.current || !notebook) return
    pendingReveal.current = false
    held.reveal()
    const target = pendingScroll.current
    pendingScroll.current = ''
    if (target) {
      const t = window.setTimeout(() => scrollToCard(target), 50)
      return () => window.clearTimeout(t)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [notebook])

  // flushSave waits for pending autosaves, then reports the names that
  // still could not be persisted (conflict, error, or still dirty). A
  // run executes SAVED code, so an unpersisted draft blocks it (P3).
  const flushSave = useCallback(async (names?: string[]): Promise<string[]> => {
    const targets = names
      ? names.map((n) => queues.current.get(n)).filter((q): q is AutosaveQueue<SectionValue> => !!q)
      : [...queues.current.values()]
    await Promise.all(targets.map((q) => q.flush().catch(() => undefined)))
    const scope = names ? new Set(names) : null
    const bad: string[] = []
    for (const [n, q] of queues.current) {
      if (scope && !scope.has(n)) continue
      const st = q.snapshot
      if (st.conflict || st.error || st.dirty) bad.push(n)
    }
    return bad
  }, [])

  const runSections = useCallback(async (sel: string | string[], onceConfirmed = false) => {
    if (runLock.current) return
    runLock.current = true
    const isList = Array.isArray(sel)
    const label = isList ? sel[0] ?? 'section' : sel
    // A run aimed at exactly one section also reports its block/errors
    // on that cell (P5); bulk runs only use the toolbar status line.
    const single = isList && sel.length === 1 ? sel[0] : null
    const blockWith = (msgs: string[]) => {
      setRunBlock(msgs)
      if (single) setCellNote({ name: single, tone: 'warning', text: msgs.join(' ') })
    }
    // Busy state goes up BEFORE the first await so every run control —
    // section and batch — disables in the same tick (P1).
    setRunning(label)
    setRunBlock([])
    setBatch(null)
    setCellNote(null)
    try {
      // The dependency closure can pull in ANY section, so every pending
      // save flushes before a run — blocked saves stop it (P3).
      const blocked = await flushSave()
      if (blocked.length) {
        blockWith(blocked.map((n) => `section ${n} has an unresolved save — resolve it before running`))
        return
      }
      // Capture the notebook revision AFTER pending saves landed, then
      // validate against that snapshot: any write racing the POST
      // conflicts (409) instead of executing code nobody saw (F3).
      const fresh = await api.get<{ notebook?: NotebookData }>(nbPath(id))
      const rev = fresh.notebook?.revision ?? 0
      const freshSecs = fresh.notebook?.sections ?? []
      if (isList) {
        const names = new Set(freshSecs.map((s) => s.name))
        const missing = sel.filter((n) => !names.has(n))
        if (missing.length) {
          blockWith([`unknown section(s): ${missing.join(', ')} — reload the notebook`])
          return
        }
        // A single explicit run re-checks `once` against FRESH meta —
        // a stale outline must not skip the confirmation (F3). Upstream
        // once-sections re-run without a prompt — that is the plan's call.
        if (sel.length === 1 && !onceConfirmed) {
          const m = freshSecs.find((s) => s.name === sel[0])
          if (m && m.rerun === 'once' && m.once_ran) {
            setPendingOnce(sel[0])
            return
          }
        }
      }
      // The dry run returns the dependency plan without executing: only
      // its `planned` sections will actually run, so only those need the
      // displayed-vs-saved proof below.
      const plan = await api.post<RunSectionsResponse>(`${nbPath(id)}/run`, { sections: sel, revision: rev, dry_run: true })
      const planned = (plan.report?.results ?? []).filter((r) => r.status === 'planned').map((r) => r.name)
      // The run executes SAVED code, so every planned section must be
      // proven identical to what this page displays and last saved —
      // an agent edit landing before the revision capture above would
      // otherwise be executed unseen. Sections without a registered
      // autosave queue (held behind "Show N new" or still loading)
      // cannot be verified and block the run.
      type Check = {
        name: string
        q?: AutosaveQueue<SectionValue>
        srv?: NotebookSectionResponse['section'] | null
        problem?: string
      }
      const checks: Check[] = await Promise.all(planned.map(async (name): Promise<Check> => {
        const q = queues.current.get(name)
        if (!q) return { name, problem: 'not loaded — reveal it if held behind "Show N new"' }
        try {
          const d = await api.get<NotebookSectionResponse>(`${sectionPath(id, name)}?include_output=false`)
          return { name, q, srv: d.section ?? null }
        } catch {
          return { name, q, srv: null }
        }
      }))
      const problems: string[] = []
      for (const c of checks) {
        if (c.problem) {
          problems.push(`section ${c.name} is ${c.problem}`)
          continue
        }
        const q = c.q!
        const srv = c.srv
        const st = q.snapshot
        const srvVal: SectionValue | null = srv ? {
          code: srv.code,
          description: srv.section.description,
          rerun: srv.section.rerun,
        } : null
        if (!srvVal) {
          problems.push(`section ${c.name} could not be verified — reload the notebook`)
          continue
        }
        if (st.dirty || st.conflict || st.error) {
          problems.push(`section ${c.name} has an unresolved save — resolve it before running`)
          continue
        }
        // Server copy must equal BOTH the displayed draft and the last
        // persisted value: a difference means someone else wrote since
        // this page last saw the section.
        if (sectionFingerprint(srvVal) !== sectionFingerprint(st.latest)
          || sectionFingerprint(srvVal) !== sectionFingerprint(st.saved.value)) {
          problems.push(`section ${c.name} changed on the server — reload and retry; nothing executed`)
        }
      }
      // Re-check after the awaited reads: a local edit or a save landing
      // during validation must not slip a stale comparison into the POST.
      if (!problems.length) {
        for (const c of checks) {
          if (!c.q) continue
          const st = c.q.snapshot
          if (st.dirty || st.conflict || st.error) {
            problems.push(`section ${c.name} changed while validating — retry the run`)
          }
        }
      }
      if (problems.length) {
        blockWith([...new Set(problems)])
        refresh()
        return
      }
      const r = await api.post<RunSectionsResponse>(`${nbPath(id)}/run`, { sections: sel, revision: rev })
      setBatch(r.report ?? null)
      // A mid-batch restore comes back as a report, not an error: keep
      // the persistent notice semantics — 'partial' warns that earlier
      // sections DID execute before the loss.
      setRestoredNotice(r.report?.restored ? 'partial' : null)
      refresh()
    } catch (e) {
      if (errCode(e) === 'instance_restored') {
        // The restore already happened server-side; the request did NOT
        // run. Show the persistent notice and refresh — never retry.
        setRestoredNotice('trigger')
        if (single) setCellNote({ name: single, tone: 'warning', text: 'Runtime restored — this request did not run.' })
        refresh()
      } else if (errCode(e) === 'graph_blocked') {
        // The dependency plan refused the run — the server message names
        // the diagnostics; it is NOT a revision conflict.
        blockWith([(e as Error).message])
        refresh()
      } else if (errCode(e) === 'instance_expired') {
        blockWith(['instance expired — the notebook is read-only'])
        refresh()
      } else if (e instanceof ApiError && e.status === 409) {
        blockWith(['notebook changed while preparing the run — reload and retry; nothing executed'])
        refresh()
      } else {
        blockWith([(e as Error).message])
      }
    } finally {
      setRunning(null)
      runLock.current = false
    }
  }, [id, flushSave, refresh])

  const runOne = useCallback((name: string, onceConfirmed = false) => { void runSections([name], onceConfirmed) }, [runSections])

  // Adding a section is immediate — no dialog: the first free
  // untitled_section[_N] name is picked from a fresh notebook read so
  // a raced name or moved revision 409s and retries (≤3 attempts)
  // instead of colliding. `after` inserts positionally for the
  // per-cell inserters; omitted appends at the end.
  const addSection = async (after?: string) => {
    if (addLock.current) return
    addLock.current = true
    setAdding(true)
    setAddErr('')
    try {
      for (let attempt = 0; attempt < 3; attempt++) {
        const fresh = await api.get<{ notebook?: NotebookData }>(nbPath(id))
        const rev = fresh.notebook?.revision ?? 0
        const taken = new Set((fresh.notebook?.sections ?? []).map((s) => s.name))
        const name = nextNotebookSectionName(taken)
        try {
          // `code` is omitted: the API stores an empty section and the new
          // cell's editor takes the code (autosave persists it).
          await api.put<SaveNotebookSectionResponse>(
            `/api/v1/instances/${id}/sections/${encodeURIComponent(name)}`,
            {
              description: 'Untitled Section', create_only: true,
              ...(after ? { after } : {}), revision: rev,
            })
          setFocusName(name)
          onStructural(name)
          return
        } catch (e) {
          if (!(e instanceof ApiError) || e.status !== 409 || attempt === 2) throw e
        }
      }
    } catch (e) {
      setAddErr((e as Error).message)
    } finally {
      addLock.current = false
      setAdding(false)
    }
  }

  const navNames = shown.map((s) => s.name)
  const navKey = JSON.stringify(navNames)
  const sectionParam = params.get('section') ?? ''

  // ?section= is the explicit selection — a row click, a graph node,
  // a deep link: it highlights its chip at once and holds the spy
  // until the scroll settles. Clearing it clears the chip (graph
  // deselect); in code view the spy resumes on the next scroll.
  useEffect(() => {
    if (sectionParam) pickSection(sectionParam)
    else { spyHold.current = 0; setActiveNav('') }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sectionParam])

  // A ?section= pick whose section disappeared (deleted server-side,
  // stale deep link) clears itself — the node/card it pointed at is
  // gone, and the graph aside falls back to the issues list.
  useEffect(() => {
    if (!sectionParam || !notebook) return
    if (!sections.some((s) => s.name === sectionParam)) {
      setParams((p) => {
        const n = new URLSearchParams(p)
        n.delete('section')
        return n
      })
    }
  }, [sectionParam, sections, notebook, setParams])

  // View-switch scroll continuity (S6): Graph→Code restores the code
  // pane's saved scrollTop unless a pending ?section= navigation owns
  // the scroll (a graph "Open code" pick, a deep link or Back — never a
  // stale URL selection after the user scrolled on). The capture runs
  // in the click handler BEFORE the navigation; the restore runs in a
  // layout effect once the code pane is unhidden again. Scrolls are
  // instant — a view switch is already a hard cut.
  const codeScrollY = useRef<number | null>(null)
  const prevView = useRef(view)
  const goView = useCallback((v: 'code' | 'graph') => {
    if (v === 'graph' && view === 'code') codeScrollY.current = codeRef.current?.scrollTop ?? null
    if (v === 'code' && sectionParam && codeScrollY.current !== null) {
      // Explicit Code click with a saved position: a lingering
      // ?section= is a stale pick — mark it already-scrolled so it
      // cannot jump. Without a saved position (a direct Graph deep
      // link) the URL selection still owns the landing scroll.
      scrolledTo.current = sectionParam
    }
    setView(v)
  }, [view, setView, sectionParam])
  useLayoutEffect(() => {
    const prev = prevView.current
    prevView.current = view
    if (prev === view || view !== 'code') return
    const y = codeScrollY.current
    codeScrollY.current = null
    const pendingNav = !!sectionParam && scrolledTo.current !== sectionParam
    if (y !== null && !pendingNav && codeRef.current) {
      codeRef.current.scrollTop = y
    }
  }, [view, sectionParam])

  // C4: ?section=name deep-links scroll the target into view once its
  // card is actually mounted and the pane is on screen — an eventual
  // arrival (or opening the notebook view after landing on Activity)
  // is picked up when the held order admits it or `visible` flips, but
  // unrelated additions/polls never re-scroll (scrolledTo covers them).
  // In graph view the canvas reveals the node itself through ?section=.
  useEffect(() => {
    if (!sectionParam) scrolledTo.current = ''
    if (!visible || !sectionParam || view === 'graph' || scrolledTo.current === sectionParam) return
    if (scrollToCard(sectionParam)) scrolledTo.current = sectionParam
  }, [sectionParam, view, navKey, visible, scrollToCard])

  // Scroll-spy (S3/S1): one IntersectionObserver rooted at the code
  // pane; the first intersecting card in shown order highlights its
  // row, paint-only via aria-current. A hidden pane reports nothing
  // intersecting, so graph view can never clobber the pick.
  const spyEls = useRef<{ key: string; el: HTMLElement }[]>([])
  useEffect(() => {
    const root = codeRef.current
    if (!root) return
    const targets = navNames.map((n) => ({ key: n, id: `section-${n}` }))
    const els = targets
      .map((t) => ({ key: t.key, el: document.getElementById(t.id) }))
      .filter((t): t is { key: string; el: HTMLElement } => t.el !== null)
    spyEls.current = els
    if (!els.length) {
      setActiveNav('')
      return
    }
    const hit = new Set<string>()
    const byEl = new Map(els.map((t) => [t.el, t.key]))
    const io = new IntersectionObserver((entries) => {
      for (const en of entries) {
        const k = byEl.get(en.target as HTMLElement)
        if (!k) continue
        if (en.isIntersecting) hit.add(k)
        else hit.delete(k)
      }
      const next = els.map((t) => t.key).find((k) => hit.has(k))
      // An explicit pick holds the spy until its programmatic scroll
      // settles — the clicked row keeps the highlight (S1/S3).
      if (next !== undefined && performance.now() >= spyHold.current) {
        setActiveNav((prev) => (prev === next ? prev : next))
      }
    }, { root })
    for (const t of els) io.observe(t.el)
    return () => io.disconnect()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [navKey])

  // The hold ends on the next user-initiated scroll of the code pane:
  // wheel/touch, a scroll key, and any scroll event that is not our own
  // programmatic scroll (scrollbar drags emit only scroll). A user
  // scroll also recomputes the spy at once — intersection entries may
  // not have changed even though the topmost card did.
  useEffect(() => {
    const root = codeRef.current
    if (!root) return
    const release = () => { spyHold.current = 0 }
    const onScroll = () => {
      if (performance.now() - selfScrollAt.current <= 150) return
      release()
      const rTop = root.getBoundingClientRect().top
      const next = spyEls.current.find(({ el }) => {
        const r = el.getBoundingClientRect()
        return r.bottom > rTop + 8 && r.top < rTop + root.clientHeight
      })?.key
      if (next !== undefined) setActiveNav((prev) => (prev === next ? prev : next))
    }
    const scrollKeys = new Set(['ArrowUp', 'ArrowDown', 'PageUp', 'PageDown', 'Home', 'End', ' '])
    const onKey = (e: KeyboardEvent) => { if (scrollKeys.has(e.key)) release() }
    root.addEventListener('wheel', release, { passive: true })
    root.addEventListener('touchstart', release, { passive: true })
    root.addEventListener('keydown', onKey)
    root.addEventListener('scroll', onScroll, { passive: true })
    return () => {
      root.removeEventListener('wheel', release)
      root.removeEventListener('touchstart', release)
      root.removeEventListener('keydown', onKey)
      root.removeEventListener('scroll', onScroll)
    }
  }, [hasNotebook])

  // Keep the active row inside the directory by moving the nav's own
  // scrollTop — never scrollIntoView, which could move other panes (S6).
  useEffect(() => {
    const nav = dirRef.current
    const row = nav?.querySelector<HTMLElement>('.nb-dir-row[aria-current="true"]')
    if (!nav || !row) return
    const n = nav.getBoundingClientRect()
    const c = row.getBoundingClientRect()
    const d = c.top < n.top ? c.top - n.top : c.bottom > n.bottom ? c.bottom - n.bottom : 0
    if (d) nav.scrollTop += d
  }, [activeNav, dirFilter, outlineOpen])

  // One-line status, first match wins (S3/S4: it replaces every banner
  // that used to push the cells down). Warning tone carries the failed
  // signal mark; everything else stays quiet secondary text.
  const nextItems = useMemo(() => notebook?.next ?? [], [notebook])
  // One-line status (S3): `text` is the truncated line; `title` carries
  // the FULL content — every blocked message, next item, per-section
  // batch row, warning — one per line, for the native tooltip.
  const status = useMemo(() => {
    const one = (text: string, warn: boolean, title?: string) => ({ text, warn, title: title ?? text })
    // Every per-section row of the last report plus its stop/warning
    // lines — lifecycle notices that reference the report append it.
    const batchReport = (b: NonNullable<typeof batch>): string => {
      const detail = (r: (typeof b.results)[number]) =>
        `${r.name} · ${r.status}${r.reason ? ` — ${r.reason}` : ''}${r.skipped_reason ? ` — ${r.skipped_reason}` : ''}${r.error ? ` — ${r.error}` : ''}`
      const lines = b.results.map(detail)
      if (b.stopped_reason) lines.push(`stopped: ${b.stopped_reason}`)
      for (const w of b.warnings ?? []) lines.push(`⚠ ${w}`)
      return lines.join('\n')
    }
    const withReport = (para: string) => (batch ? `${para}\n${batchReport(batch)}` : para)
    if (running === 'all') return one('Running all sections…', false)
    if (running === 'stale') return one('Running stale sections…', false)
    if (running) return one(`Running ${running}…`, false)
    if (addErr) return one(`Couldn't add section — ${addErr}`, true)
    if (restoredNotice === 'partial') {
      return one(
        'Runtime restored mid-run — earlier sections in this batch did execute; review the run report',
        true,
        withReport('Variables and files were cleared when the process was lost — but earlier sections in this batch did execute in the previous runtime. Review the run report before repeating side effects.'),
      )
    }
    if (restoredNotice === 'trigger') {
      return one(
        'Runtime restored — your request did not run',
        true,
        withReport('Variables and files were cleared when the process was lost. Your request did not run — choose sections to execute.'),
      )
    }
    if (runBlock.length > 0) {
      return one(
        `Run blocked — ${runBlock[0]}${runBlock.length > 1 ? ` (+${runBlock.length - 1} more)` : ''}`,
        true,
        runBlock.join('\n'),
      )
    }
    if (batch) {
      const res = batch.results
      if (res.length === 1) {
        const r = res[0]
        return one(
          `${r.name} · ${r.status}`,
          r.status === 'error' || r.status === 'not_run',
          `${r.name} · ${r.status}${r.reason ? ` — ${r.reason}` : ''}${r.skipped_reason ? ` — ${r.skipped_reason}` : ''}${r.error ? ` — ${r.error}` : ''}`,
        )
      }
      const ok = res.filter((r) => r.status === 'ok').length
      const err = res.filter((r) => r.status === 'error').length
      const reused = res.filter((r) => r.status === 'reused').length
      const skipped = res.filter((r) => r.status === 'skipped').length
      const notRun = res.filter((r) => r.status === 'not_run').length
      let text = `${ok + err} ran · ${ok} ok`
      if (err > 0) text += ` · ${err} failed`
      if (reused > 0) text += ` · ${reused} reused`
      if (skipped > 0) text += ` · ${skipped} skipped`
      if (notRun > 0) text += ` · ${notRun} not run`
      return one(text, err > 0 || notRun > 0 || (batch.warnings?.length ?? 0) > 0, `${text}\n${batchReport(batch)}`)
    }
    if (lost) {
      return one(
        'Runtime lost · code is retained — running restores an empty process',
        true,
        withReport('Runtime lost · code is retained. The process is gone but the notebook is intact — running a section or batch restores an empty process under the same instance; nothing re-runs automatically.'),
      )
    }
    if (expired) {
      return one(
        'Expired. Read-only history — nothing can run or be edited',
        true,
        withReport('Expired. This notebook is read-only history — code, sections and run records are preserved, but nothing can run or be edited.'),
      )
    }
    if (nextItems.length > 0) {
      return one(
        `${nextItems[0]}${nextItems.length > 1 ? ` (+${nextItems.length - 1} more)` : ''}`,
        false,
        nextItems.join('\n'),
      )
    }
    return one(`${sections.length} section${sections.length === 1 ? '' : 's'}`, false)
  }, [running, addErr, restoredNotice, runBlock, batch, lost, expired, nextItems, sections.length])

  if (nb.isLoading) return <Loading />
  if (nb.isError) return <Notice tone="warning">Notebook unavailable: {(nb.error as Error).message}</Notice>
  if (!notebook) return null

  const incarnation = notebook.incarnation
  const empty = sections.length === 0
  const dirQuery = dirFilter.trim().toLowerCase()
  const dirMatch = (name: string) => !dirQuery || name.toLowerCase().includes(dirQuery)
  const dirEmpty = shown.length > 0 && !shown.some((m) => dirMatch(m.name))

  return (
    <div className="nb">
      {/* Toolbar row: run actions | reveal + view switch + Sections
          toggle. Every notice that used to be a banner above the cells
          lives in the reserved status bar at the bottom — nothing
          inserts content above the cells after a click or a poll
          (S3/S5). */}
      <div className="nb-head">
        <div className="nb-toolbar">
          <div className="nb-toolbar-actions">
            {canExec && (
              <>
                <Button variant="ghost" size="xs" disabled={empty || running !== null}
                  onClick={() => void runSections('all')}>
                  <Icon name="run-all" />
                  <SwapLabel show={running === 'all' ? 'Running…' : 'Run all'} options={['Run all', 'Running…']} />
                </Button>
                <Button variant="ghost" size="xs" disabled={empty || running !== null}
                  onClick={() => void runSections('stale')}>
                  <Icon name="play" />
                  <SwapLabel show={running === 'stale' ? 'Running…' : 'Run stale'} options={['Run stale', 'Running…']} />
                </Button>
              </>
            )}
            {canEdit && <NotebookAddSection disabled={adding} onAdd={() => void addSection()} />}
          </div>
          <div className="nb-toolbar-side">
            <RevealNew count={held.arrived} moved={held.moved} onClick={held.reveal} />
            <NotebookViewToggle view={view} onChange={goView} />
            <Button variant="ghost" size="xs" className="nb-icon-btn nb-sections-btn"
              ref={sectionsBtnRef}
              aria-label="Sections" title="Sections"
              aria-expanded={outlineOpen} aria-controls="notebook-outline"
              onClick={onToggleOutline}>
              <svg aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor"
                strokeWidth="1.6" strokeLinecap="round">
                <path d="M9 4h11M9 12h11M9 20h11" />
                <path d="M4 4h.01M4 12h.01M4 20h.01" strokeWidth="3" />
              </svg>
            </Button>
          </div>
        </div>
      </div>

      {/* Section outline (C4): held shown order, name filter keeps the
          original ordinals, click deep-links and scrolls to the card.
          Stays mounted behind `hidden` so the filter and scroll survive
          toggles (C2/C3); overlays the code pane under 740px. */}
      <aside id="notebook-outline" className="nb-outline" hidden={!outlineOpen}>
        <div className="nb-outline-head">
          <h2 className="nb-outline-title">Sections</h2>
          <label className="nb-outline-filter">
            <span className="sr-only">Filter sections</span>
            <input type="search" value={dirFilter} placeholder="Filter"
              aria-label="Filter sections"
              onChange={(e) => setDirFilter(e.target.value)} />
          </label>
        </div>
        <nav className="nb-directory" aria-label="Section directory" ref={dirRef}>
          {shown.map((m, i) => dirMatch(m.name) ? (
            <button
              key={m.name}
              type="button"
              className="nb-dir-row"
              title={m.description || sectionStateLabel(m.state)}
              aria-current={activeNav === m.name ? 'true' : undefined}
              data-running={running === m.name ? 'true' : undefined}
              onClick={() => {
                pickSection(m.name)
                setParams((p) => { const n = new URLSearchParams(p); n.set('section', m.name); return n })
                // Code view scrolls the card — the pick marks the nav
                // done. In graph view the ?section= change makes the
                // canvas reveal the node; scrolledTo stays untouched so
                // a later return to Code still honours the selection.
                if (view !== 'graph') {
                  scrolledTo.current = m.name
                  scrollToCard(m.name)
                }
                onOutlineNavigate()
                // The overlay just closed — drop focus back on its
                // toggle rather than losing it to the pane (C1).
                if (window.matchMedia('(max-width: 739px)').matches) {
                  sectionsBtnRef.current?.focus()
                }
              }}
            >
              <span className="nb-dir-ord">{i + 1}.</span>
              <span className="nb-dir-name">{m.name}</span>
              <span className="nb-dir-dot" data-state={m.state} />
              <span className="sr-only">{sectionStateLabel(m.state)}</span>
            </button>
          ) : null)}
          {dirEmpty && <p className="nb-dir-empty">No sections match “{dirFilter.trim()}”.</p>}
        </nav>
      </aside>

      {/* Code panel stays mounted while the graph view is shown —
          hidden, never unmounted, so editors/drafts/autosave survive
          toggling and polling (C2/C3). It is the scroll container —
          the window never moves. */}
      <div className="nb-code" hidden={view === 'graph'} ref={codeRef}>
      {empty ? (
        <div className="nb-empty">
          <p>No sections yet.</p>
          {canEdit && <NotebookAddSection disabled={adding} onAdd={() => void addSection()} />}
        </div>
      ) : (
        <div className="nb-cards">
          {shown.map((meta) => (
            <Fragment key={meta.name}>
              <SectionCard
                id={id}
                inst={inst}
                meta={meta}
                order={order}
                incarnation={incarnation}
                canRun={canRun}
                busy={running === meta.name}
                adding={adding}
                register={register}
                onStructural={onStructural}
                onRun={runOne}
                onInsertAfter={(n) => void addSection(n)}
                note={cellNote && cellNote.name === meta.name ? cellNote : null}
                focusOnMount={focusName === meta.name}
              />
              {/* Inserter after every cell — the row keeps its height
                  even without edit rights so cells never move (S3). */}
              <NotebookInsert name={meta.name} disabled={adding}
                onInsert={canEdit ? () => void addSection(meta.name) : undefined} />
            </Fragment>
          ))}
        </div>
      )}

      </div>

      {/* Read-only dependency graph — same fixed box in both themes;
          follows polls live with an animated re-layout. */}
      <NotebookGraphPane hidden={view !== 'graph'}>
        <NotebookGraph
          structure={graphStructure}
          metas={graphMetas}
          selected={sectionParam}
          onSelect={(n) => setParams((p) => { const x = new URLSearchParams(p); x.set('section', n); return x })}
          onClear={() => setParams((p) => { const x = new URLSearchParams(p); x.delete('section'); return x })}
          onOpenCode={openGraphCode}
          canRunSection={canExec}
          runBusy={running !== null}
          runLabel={running === sectionParam ? 'Running…' : 'Run section'}
          onRunSection={runOne}
          running={running}
        />
      </NotebookGraphPane>

      {/* Reserved status bar: the one-line summary (title carries the
          full text — screen readers get the same via an sr-only twin so
          there is no Details dialog). Memory sits at the far end from
          the shared inspect snapshot. */}
      <div className="nb-statusbar">
        <span className={`nb-status${status.warn ? ' nb-status-warn' : ''}`} role="status" title={status.title}>
          {status.warn && <SignalMark signal="failed" />}
          <span className="nb-status-text" aria-hidden="true">{status.text}</span>
          <span className="sr-only">{status.title}</span>
        </span>
        <MemoryStatus resources={resources} />
      </div>

      {pendingOnce && (
        <Confirm
          title={`Run ${pendingOnce} again?`}
          confirm="Run again"
          pending="Running…"
          action={async () => { const n = pendingOnce; setPendingOnce(null); await runSections([n], true) }}
          onClose={() => setPendingOnce(null)}
        >
          <p className="text-sm text-body">It already succeeded once.</p>
        </Confirm>
      )}

    </div>
  )
}
