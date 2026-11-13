import { Fragment, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useSearchParams } from 'react-router-dom'
import { api, ApiError, Instance, Notebook as NotebookData, NotebookSectionResponse, RunSectionsResponse, SaveNotebookSectionResponse } from '../api'
import { AutosaveQueue } from '../autosave'
import { Button, Confirm, Icon, Loading, Notice, RevealNew, SignalMark, SwapLabel } from './ui'
import { SectionCard, SectionValue, sectionFingerprint, sectionPath, sectionStateLabel } from './SectionCard'
import { NotebookGraph, type GraphStructure } from './NotebookGraph'
import { useHeldOrder } from '../stableList'
import './notebook.css'

// isRestored/isExpired read the typed error codes the API returns for
// the two lifecycle edges a run can hit.
function errCode(e: unknown): string {
  return e instanceof ApiError ? e.code : ''
}

const nbPath = (id: string) => `/api/v1/instances/${id}/notebook`

// Notebook is the instance's working document: an ordered directory of
// durable sections, each an editable card with autosave — every run is
// a section run in dependency order. Polling never overwrites a dirty
// draft (C2) and the held order keeps polled arrivals behind "Show N
// new" (S5).
export function Notebook({ id, inst, canRun }: { id: string; inst: Instance; canRun: boolean }) {
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
      const t = window.setTimeout(() => {
        document.getElementById(`section-${target}`)?.scrollIntoView({ block: 'start' })
      }, 50)
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
        let name = 'untitled_section'
        for (let n = 2; taken.has(name); n++) name = `untitled_section_${n}`
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

  // Scroll-spy for the sticky directory (S3/S1): one IntersectionObserver
  // over the shown cards; its top edge sits under the head
  // (--nb-bar-h is the same reservation the cards' scroll-margin uses),
  // and the first intersecting chip in shown order is highlighted
  // paint-only via aria-current.
  const [activeNav, setActiveNav] = useState('')
  const headRef = useRef<HTMLDivElement | null>(null)
  const dirRef = useRef<HTMLElement | null>(null)
  // One "active section" concept (S1/S3): an explicit pick — a chip
  // click, a graph node, a deep link — highlights its chip at once and
  // holds the scroll-spy until the programmatic scroll settles or the
  // user scrolls; otherwise the spy marks the topmost card in view.
  // In graph view the selected node IS the active section.
  const spyHold = useRef(0)
  const selfScrollAt = useRef(0)
  const scrollToCard = useCallback((name: string) => {
    selfScrollAt.current = performance.now()
    document.getElementById(`section-${name}`)?.scrollIntoView({ block: 'start' })
  }, [])
  const pickSection = useCallback((name: string) => {
    spyHold.current = performance.now() + 600
    setActiveNav(name)
  }, [])
  const navNames = shown.map((s) => s.name)
  const navKey = JSON.stringify(navNames)
  const sectionParam = params.get('section') ?? ''

  // ?section= is the explicit selection — a chip click, a graph node,
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

  // View-switch scroll continuity (S6): an explicit Code→Graph switch
  // reveals the graph just under the sticky head instead of inheriting
  // the cards' deep scroll position; Graph→Code restores the saved
  // position unless a ?section= navigation owns the scroll. This runs
  // only on view changes — polls never scroll the page. The scroller
  // is the window: the console shell has no inner scroll container.
  // codeScrollY is captured in goView BEFORE the navigation: by the
  // time any effect runs the browser has already re-laid out and
  // clamped scrollY against the shorter graph page, so the pre-switch
  // position only exists in the click handler. Scrolls are instant —
  // a view switch is already a hard cut, and animation would only race
  // the user's own scrolling.
  const graphPanelRef = useRef<HTMLDivElement | null>(null)
  const codeScrollY = useRef<number | null>(null)
  const prevView = useRef(view)
  const goView = useCallback((v: 'code' | 'graph') => {
    if (v === 'graph' && view === 'code') codeScrollY.current = window.scrollY
    setView(v)
  }, [view, setView])
  useLayoutEffect(() => {
    const prev = prevView.current
    prevView.current = view
    if (prev === view) return
    if (view === 'graph') {
      graphPanelRef.current
        ?.querySelector('.nb-graph')
        ?.scrollIntoView({ block: 'start' })
      return
    }
    const y = codeScrollY.current
    codeScrollY.current = null
    if (y !== null && !sectionParam) {
      // Force the reflow so scrollTo clamps against the restored card
      // list's scrollHeight, not the short graph page's.
      void document.documentElement.scrollHeight
      window.scrollTo({ top: y })
    }
  }, [view, sectionParam])

  // C4: ?section=name deep-links scroll the target into view; the
  // outline sets it so a shared URL lands on the same section. In graph
  // view the canvas reveals the node itself through ?section= — the
  // only scroll this effect owns is the code-view card.
  useEffect(() => {
    if (!sectionParam || view === 'graph') return
    const t = window.setTimeout(() => scrollToCard(sectionParam), 300)
    return () => window.clearTimeout(t)
  }, [sectionParam, shown.length, view, scrollToCard])

  useEffect(() => {
    const barH = headRef.current
      ? parseFloat(getComputedStyle(headRef.current).getPropertyValue('--nb-bar-h')) || 0
      : 0
    const targets = navNames.map((n) => ({ key: n, id: `section-${n}` }))
    const els = targets
      .map((t) => ({ key: t.key, el: document.getElementById(t.id) }))
      .filter((t): t is { key: string; el: HTMLElement } => t.el !== null)
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
      // settles — the clicked chip keeps the highlight (S1/S3).
      if (next !== undefined && performance.now() >= spyHold.current) {
        setActiveNav((prev) => (prev === next ? prev : next))
      }
    }, { rootMargin: `-${73 + barH}px 0px 0px 0px` })
    for (const t of els) io.observe(t.el)
    return () => io.disconnect()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [navKey])

  // The hold ends on the next user-initiated scroll: wheel/touch, a
  // scroll key, and any scroll event that is not our own scrollIntoView
  // (scrollbar drags emit only scroll). Nested scrollers releasing the
  // hold early is harmless — the spy simply resumes.
  useEffect(() => {
    const release = () => { spyHold.current = 0 }
    const onScroll = () => {
      if (performance.now() - selfScrollAt.current > 150) release()
    }
    const scrollKeys = new Set(['ArrowUp', 'ArrowDown', 'PageUp', 'PageDown', 'Home', 'End', ' '])
    const onKey = (e: KeyboardEvent) => { if (scrollKeys.has(e.key)) release() }
    window.addEventListener('wheel', release, { passive: true })
    window.addEventListener('touchstart', release, { passive: true })
    window.addEventListener('keydown', onKey)
    window.addEventListener('scroll', onScroll, { passive: true, capture: true })
    return () => {
      window.removeEventListener('wheel', release)
      window.removeEventListener('touchstart', release)
      window.removeEventListener('keydown', onKey)
      window.removeEventListener('scroll', onScroll, { capture: true })
    }
  }, [])

  // Keep the active chip inside the strip by moving the nav's own
  // scrollLeft — never scrollIntoView, which could move the page (S6).
  useEffect(() => {
    const nav = dirRef.current
    const chip = nav?.querySelector<HTMLElement>('.nb-dir-row[aria-current="true"]')
    if (!nav || !chip) return
    const n = nav.getBoundingClientRect()
    const c = chip.getBoundingClientRect()
    const d = c.left < n.left ? c.left - n.left : c.right > n.right ? c.right - n.right : 0
    if (d) nav.scrollLeft += d
  }, [activeNav])

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

  return (
    <div className="nb">
      {/* Floating head panel (S3): the toolbar row plus the section
          directory ride together as one fixed-height opaque surface
          pinned under the console header — cards scroll beneath it
          with scroll-margin clearance. */}
      <div className="nb-head" ref={headRef}>
        {/* Sticky status toolbar: run actions | one-line status | reveal
            + view switch. Every notice that used to be a banner above
            the cells now lives in this fixed-height row — the status
            line's title carries the full text — nothing inserts
            content above the cells after a click or a poll (S3/S5). */}
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
            {canEdit && (
              <Button variant="ghost" size="xs" disabled={adding} onClick={() => void addSection()}>
                <Icon name="plus" />Add section
              </Button>
            )}
          </div>
          <span className={`nb-status${status.warn ? ' nb-status-warn' : ''}`} role="status" title={status.title}>
            {status.warn && <SignalMark signal="failed" />}
            <span className="nb-status-text">{status.text}</span>
          </span>
          <div className="nb-toolbar-side">
            <RevealNew count={held.arrived} moved={held.moved} onClick={held.reveal} />
            {/* The view switch is the LAST toolbar item — flush with the
                panel's right inner padding, ≥12px clear of its left
                neighbour (S3 spacing). */}
            <div className="seg-filters nb-view-toggle" role="group" aria-label="Notebook view">
              <button type="button" aria-pressed={view === 'code'} onClick={() => goView('code')}>
                Code
              </button>
              <button type="button" aria-pressed={view === 'graph'} onClick={() => goView('graph')}>
                Graph
              </button>
            </div>
          </div>
        </div>

        {/* Compact section directory (C4): held shown order, click
            deep-links and scrolls to the card. It renders even with zero
            sections — the row's fixed height keeps the head box constant
            when the first section lands (S3/F1). */}
        <nav className="nb-directory" aria-label="Section directory" ref={dirRef}>
          {shown.map((m, i) => (
            <button
              key={m.name}
              type="button"
              className="nb-dir-row"
              title={m.description || sectionStateLabel(m.state)}
              aria-current={activeNav === m.name ? 'true' : undefined}
              data-running={running === m.name ? 'true' : undefined}
              onClick={() => {
                setParams((p) => { const n = new URLSearchParams(p); n.set('section', m.name); return n })
                // Code view scrolls the card; in graph view the
                // ?section= change makes the canvas reveal the node.
                if (view !== 'graph') scrollToCard(m.name)
              }}
            >
              <span className="nb-dir-ord">{i + 1}.</span>
              <span className="nb-dir-name">{m.name}</span>
              <span className="nb-dir-dot" data-state={m.state} />
              <span className="sr-only">{sectionStateLabel(m.state)}</span>
            </button>
          ))}
        </nav>
      </div>

      {/* Code panel stays mounted while the graph view is shown —
          hidden, never unmounted, so editors/drafts/autosave survive
          toggling and polling (C2/C3). */}
      <div className="nb-code" hidden={view === 'graph'}>
      {empty ? (
        <div className="nb-empty">
          <p>No sections yet.</p>
          {canEdit && (
            <Button variant="ghost" size="xs" disabled={adding} onClick={() => void addSection()}>
              <Icon name="plus" />Add section
            </Button>
          )}
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
              <div className="nb-insert">
                {canEdit && (
                  <Button
                    variant="ghost" size="xs"
                    className="nb-icon-btn nb-insert-btn"
                    aria-label={`Insert section after ${meta.name}`}
                    disabled={adding}
                    onClick={() => void addSection(meta.name)}
                  >
                    <Icon name="plus" />
                  </Button>
                )}
              </div>
            </Fragment>
          ))}
        </div>
      )}

      </div>

      {/* Read-only dependency graph — same fixed box in both themes;
          follows polls live with an animated re-layout. */}
      <div className="nb-graph-panel" hidden={view !== 'graph'} ref={graphPanelRef}>
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
