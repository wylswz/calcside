import { Fragment, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useSearchParams } from 'react-router-dom'
import { api, ApiError, Instance, Notebook as NotebookData, NotebookSectionResponse, RunSectionsResponse, SaveNotebookSectionResponse } from '../api'
import { AutosaveQueue } from '../autosave'
import { Button, Confirm, Icon, Loading, Modal, Notice, RevealNew, SignalMark, SwapLabel } from './ui'
import { SectionCard, SectionValue, sectionFingerprint, sectionPath, sectionStateLabel } from './SectionCard'
import { RunCode } from './RunCode'
import { useHeldOrder } from '../stableList'
import './notebook.css'

// isRestored/isExpired read the typed error codes the API returns for
// the two lifecycle edges a run can hit.
function errCode(e: unknown): string {
  return e instanceof ApiError ? e.code : ''
}

const nbPath = (id: string) => `/api/v1/instances/${id}/notebook`
const EMPTY_FORM = { name: '', description: '', after: '' }
type SectionForm = typeof EMPTY_FORM

// Notebook is the instance's working document: an ordered directory of
// durable sections, each an editable card with autosave, plus the
// scratch runner. Polling never overwrites a dirty draft (C2) and the
// held order keeps polled arrivals behind "Show N new" (S5).
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
  const scratchRunningRef = useRef(false)
  // Same synchronous-guard pattern as runLock for section creation:
  // set before the first await so repeat clicks cannot interleave
  // GET/PUT pairs; `adding` mirrors it to disable the controls (P1).
  const addLock = useRef(false)
  const [adding, setAdding] = useState(false)
  const [addErr, setAddErr] = useState('')
  const [formErr, setFormErr] = useState('')
  const [formBusy, setFormBusy] = useState(false)
  const [scratchExec, setScratchExec] = useState<string | null>(null) // exec_id of last good scratch run
  const [scratchForm, setScratchForm] = useState<SectionForm | null>(null)
  const [scratchRunning, setScratchRunningState] = useState(false)
  // P5: a single-section run's block/error/restore feedback also lands
  // on that cell's alerts slot — the toolbar status line only carries
  // the one-line summary (S3: nothing is inserted above the cells).
  const [cellNote, setCellNote] = useState<{ name: string; tone: 'warning' | 'neutral'; text: string } | null>(null)
  const [focusName, setFocusName] = useState('') // section whose editor takes focus once mounted
  const [detailsOpen, setDetailsOpen] = useState(false)

  // Scratch shares the notebook run lock through this setter: a scratch
  // start acquires runLock (refusing while a section/batch run holds it)
  // and a section run refuses while scratch holds it — same-tick clicks
  // across every run control serialize on one ref (P1). Returns false
  // when the lock was refused so RunCode can abort before posting.
  const setScratchRunningGuarded = useCallback((v: boolean): boolean => {
    if (v) {
      if (runLock.current) return false
      runLock.current = true
    } else if (scratchRunningRef.current) {
      runLock.current = false
    }
    scratchRunningRef.current = v
    setScratchRunningState(v)
    return true
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
    if (runLock.current || scratchRunningRef.current) return
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
    // section, batch and scratch — disables in the same tick (P1).
    setRunning(label)
    setRunBlock([])
    setBatch(null)
    setCellNote(null)
    try {
      // Single-section runs only need their own save flushed; bulk runs
      // need every draft persisted so saved code matches the screen.
      const blocked = await flushSave(isList ? sel : undefined)
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
        // a stale outline must not skip the confirmation (F3).
        if (sel.length === 1 && !onceConfirmed) {
          const m = freshSecs.find((s) => s.name === sel[0])
          if (m && m.rerun === 'once' && m.once_ran) {
            setPendingOnce(sel[0])
            return
          }
        }
      }
      // The run executes SAVED code, so every selected section must be
      // proven identical to what this page displays and last saved —
      // an agent edit landing before the revision capture above would
      // otherwise be executed unseen. Sections without a registered
      // autosave queue (held behind "Show N new" or still loading)
      // cannot be verified and block the run.
      const selected = freshSecs.filter((s) =>
        isList ? sel.includes(s.name) : sel === 'all' || ['stale', 'not_run', 'failed'].includes(s.state))
      type Check = {
        name: string
        q?: AutosaveQueue<SectionValue>
        srv?: NotebookSectionResponse['section'] | null
        problem?: string
      }
      const checks: Check[] = await Promise.all(selected.map(async (s): Promise<Check> => {
        const q = queues.current.get(s.name)
        if (!q) return { name: s.name, problem: 'not loaded — reveal it if held behind "Show N new"' }
        try {
          const d = await api.get<NotebookSectionResponse>(`${sectionPath(id, s.name)}?include_output=false`)
          return { name: s.name, q, srv: d.section ?? null }
        } catch {
          return { name: s.name, q, srv: null }
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

  const freshRevision = async () =>
    notebook?.revision ?? (await api.get<{ notebook?: NotebookData }>(nbPath(id))).notebook?.revision ?? 0

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

  const promoteScratch = async () => {
    if (!scratchForm || !scratchExec) return
    setFormBusy(true)
    setFormErr('')
    try {
      const rev = await freshRevision()
      await api.put<SaveNotebookSectionResponse>(
        `/api/v1/instances/${id}/sections/${encodeURIComponent(scratchForm.name)}`,
        {
          from_exec: scratchExec, description: scratchForm.description,
          create_only: true, revision: rev,
        })
      const created = scratchForm.name
      setScratchForm(null)
      setScratchExec(null)
      onStructural(created)
    } catch (e: any) {
      setFormErr(e.message)
    } finally {
      setFormBusy(false)
    }
  }

  // C4: ?section=name deep-links scroll the card into view; the outline
  // sets it so a shared URL lands on the same section.
  const sectionParam = params.get('section') ?? ''
  useEffect(() => {
    if (!sectionParam) return
    const t = window.setTimeout(() => {
      document.getElementById(`section-${sectionParam}`)?.scrollIntoView({ block: 'start' })
    }, 300)
    return () => window.clearTimeout(t)
  }, [sectionParam, shown.length])

  // Scroll-spy for the sticky directory (S3/S1): one IntersectionObserver
  // over the shown cards plus scratch; its top edge sits under the head
  // (--nb-bar-h is the same reservation the cards' scroll-margin uses),
  // and the first intersecting chip in shown order — scratch last — is
  // highlighted paint-only via aria-current.
  const [activeNav, setActiveNav] = useState('')
  const headRef = useRef<HTMLDivElement | null>(null)
  const dirRef = useRef<HTMLElement | null>(null)
  const scratchNav = canExec && (live || lost)
  const navNames = shown.map((s) => s.name)
  const navKey = JSON.stringify([...navNames, scratchNav])
  useEffect(() => {
    const barH = headRef.current
      ? parseFloat(getComputedStyle(headRef.current).getPropertyValue('--nb-bar-h')) || 0
      : 0
    const targets = [
      ...navNames.map((n) => ({ key: n, id: `section-${n}` })),
      ...(scratchNav ? [{ key: 'scratch', id: 'run-code' }] : []),
    ]
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
      if (next !== undefined) setActiveNav((prev) => (prev === next ? prev : next))
    }, { rootMargin: `-${57 + barH + 8}px 0px 0px 0px` })
    for (const t of els) io.observe(t.el)
    return () => io.disconnect()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [navKey])

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
  const status = useMemo(() => {
    if (running === 'all') return { text: 'Running all sections…', warn: false }
    if (running === 'stale') return { text: 'Running stale sections…', warn: false }
    if (running) return { text: `Running ${running}…`, warn: false }
    if (addErr) return { text: `Couldn't add section — ${addErr}`, warn: true }
    if (restoredNotice === 'partial') {
      return { text: 'Runtime restored mid-run — earlier sections in this batch did execute; review the report', warn: true }
    }
    if (restoredNotice === 'trigger') return { text: 'Runtime restored — your request did not run', warn: true }
    if (runBlock.length > 0) {
      return { text: `Run blocked — ${runBlock[0]}${runBlock.length > 1 ? ` (+${runBlock.length - 1} more)` : ''}`, warn: true }
    }
    if (batch) {
      const res = batch.results
      if (res.length === 1) {
        const r = res[0]
        return { text: `${r.name} · ${r.status}`, warn: r.status === 'error' || r.status === 'not_run' }
      }
      const ok = res.filter((r) => r.status === 'ok').length
      const err = res.filter((r) => r.status === 'error').length
      const skipped = res.filter((r) => r.status === 'skipped').length
      const notRun = res.filter((r) => r.status === 'not_run').length
      let text = `${ok + err} ran · ${ok} ok`
      if (err > 0) text += ` · ${err} failed`
      if (skipped > 0) text += ` · ${skipped} skipped`
      if (notRun > 0) text += ` · ${notRun} not run`
      return { text, warn: err > 0 || notRun > 0 || (batch.warnings?.length ?? 0) > 0 }
    }
    if (lost) return { text: 'Runtime lost · code is retained — running restores an empty process', warn: true }
    if (expired) return { text: 'Expired. Read-only history — nothing can run or be edited', warn: true }
    if (nextItems.length > 0) {
      return { text: `${nextItems[0]}${nextItems.length > 1 ? ` (+${nextItems.length - 1} more)` : ''}`, warn: false }
    }
    return { text: `${sections.length} section${sections.length === 1 ? '' : 's'}`, warn: false }
  }, [running, addErr, restoredNotice, runBlock, batch, lost, expired, nextItems, sections.length])

  // Details stays mounted (visibility, never unmounted — S3/S4) and
  // opens whenever the status line is only a summary of more content.
  const hasDetails = !!restoredNotice || runBlock.length > 0 || !!batch || lost || expired || nextItems.length > 0

  if (nb.isLoading) return <Loading />
  if (nb.isError) return <Notice tone="warning">Notebook unavailable: {(nb.error as Error).message}</Notice>
  if (!notebook) return null

  const incarnation = notebook.incarnation
  const empty = sections.length === 0

  return (
    <div className="nb">
      {/* Sticky two-row head (S3): the toolbar row plus the section
          directory ride together as one fixed-height overlay — cards
          scroll beneath it with scroll-margin clearance, so the run
          actions and the outline stay reachable on a long notebook. */}
      <div className="nb-head" ref={headRef}>
        {/* Sticky status toolbar: run actions | one-line status | details.
            Every notice that used to be a banner above the cells now lives
            in this fixed-height row or behind Details — nothing inserts
            content above the cells after a click or a poll (S3/S5). */}
        <div className="nb-toolbar">
          <div className="nb-toolbar-actions">
            {canExec && !empty && (
              <>
                <Button variant="ghost" size="xs" disabled={running !== null || scratchRunning}
                  onClick={() => void runSections('all')}
                  title="Run every section in notebook order; once-sections that already succeeded are skipped">
                  <Icon name="run-all" />
                  <SwapLabel show={running === 'all' ? 'Running…' : 'Run all'} options={['Run all', 'Running…']} />
                </Button>
                <Button variant="ghost" size="xs" disabled={running !== null || scratchRunning}
                  onClick={() => void runSections('stale')}
                  title="Run every stale or never-run section in order; once-sections that already succeeded are skipped">
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
          <span className={`nb-status${status.warn ? ' nb-status-warn' : ''}`} role="status">
            {status.warn && <SignalMark signal="failed" />}
            <span className="nb-status-text">{status.text}</span>
          </span>
          <div className="nb-toolbar-side">
            <RevealNew count={held.arrived} moved={held.moved} onClick={held.reveal} />
            <Button
              variant="ghost" size="xs"
              style={{ visibility: hasDetails ? 'visible' : 'hidden' }}
              onClick={() => setDetailsOpen(true)}
            >
              Details
            </Button>
          </div>
        </div>

        {/* Compact section directory (C4): held shown order, click
            deep-links and scrolls to the card. The trailing scratch
            chip reaches the runner from anywhere in a long notebook. */}
        {navNames.length > 0 && (
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
                  document.getElementById(`section-${m.name}`)?.scrollIntoView({ block: 'start' })
                }}
              >
                <span className="nb-dir-ord">{i + 1}.</span>
                <span className="nb-dir-name">{m.name}</span>
                <span className="nb-dir-dot" data-state={m.state} />
                <span className="sr-only">{sectionStateLabel(m.state)}</span>
              </button>
            ))}
            {scratchNav && (
              <button
                type="button"
                className="nb-dir-row nb-dir-scratch"
                aria-current={activeNav === 'scratch' ? 'true' : undefined}
                onClick={() => {
                  setParams((p) => { const n = new URLSearchParams(p); n.delete('section'); return n })
                  document.getElementById('run-code')?.scrollIntoView({ block: 'start' })
                }}
              >
                scratch
              </button>
            )}
          </nav>
        )}
      </div>

      {empty ? (
        <div className="nb-empty">
          <p>No sections yet — the notebook is the durable record of this instance's work.</p>
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
                blocked={scratchRunning}
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

      {(canExec && (live || lost)) || expired ? (
        <section aria-label="Scratch" className="nb-scratch" id="run-code">
          {canExec && (live || lost) && (
            <RunCode
              inst={inst}
              running={scratchRunning}
              disabled={running !== null}
              setRunning={setScratchRunningGuarded}
              onRan={(r) => { if (!r.error) setScratchExec(r.exec_id) }}
              onRestored={() => setRestoredNotice('trigger')}
              promotable={!!scratchExec}
              onPromote={() => { setFormErr(''); setScratchForm({ ...EMPTY_FORM }) }}
            />
          )}
          {expired && (
            <p className="nb-scratch-note">Scratch is unavailable — the instance expired.</p>
          )}
        </section>
      ) : null}

      {pendingOnce && (
        <Confirm
          title={`Run ${pendingOnce} again?`}
          confirm="Run again"
          pending="Running…"
          action={async () => { const n = pendingOnce; setPendingOnce(null); await runSections([n], true) }}
          onClose={() => setPendingOnce(null)}
        >
          <p className="text-sm text-body">This section is marked “once” — it already succeeded. Bulk runs skip it; running it explicitly repeats it in the current process.</p>
        </Confirm>
      )}

      {detailsOpen && (
        <Modal title="Notebook status" dirty={false} onClose={() => setDetailsOpen(false)}>
          <div className="nb-details">
            {restoredNotice && (
              <p className="nb-details-para">
                <strong>Runtime restored.</strong>{' '}
                {restoredNotice === 'partial' ? (
                  <>Variables and files were cleared when the process was lost — but earlier sections in this batch did execute in the previous runtime. Review the run report before repeating side effects.</>
                ) : (
                  <>Variables and files were cleared when the process was lost. Your request did not run — choose sections to execute.</>
                )}
              </p>
            )}
            {lost && (
              <p className="nb-details-para">
                <strong>Runtime lost · code is retained.</strong> The process is gone but the notebook is intact —
                running a section or batch restores an <em>empty</em> process under the same instance; nothing re-runs automatically.
              </p>
            )}
            {expired && (
              <p className="nb-details-para">
                <strong>Expired.</strong> This notebook is read-only history — code, sections and run records are preserved,
                but nothing can run or be edited.
              </p>
            )}
            {runBlock.length > 0 && (
              <div className="nb-details-block">
                <h4 className="nb-details-h">Blocked</h4>
                <ul className="nb-details-list">
                  {runBlock.map((b) => <li key={b}>{b}</li>)}
                </ul>
              </div>
            )}
            {batch && (
              <div className="nb-details-block">
                <h4 className="nb-details-h">Run report</h4>
                <div className="nb-batch">
                  <ul className="nb-batch-list">
                    {batch.results.map((r) => (
                      <li key={r.name} className="nb-batch-row">
                        <code className="nb-batch-name">{r.name}</code>
                        <span className={`nb-batch-status nb-batch-${r.status}`}>{r.status}</span>
                        {r.skipped_reason && <span className="nb-batch-skip">{r.skipped_reason}</span>}
                        {r.error && <span className="nb-batch-error-text" title={r.error}>{r.error}</span>}
                      </li>
                    ))}
                  </ul>
                  {batch.stopped_reason && <p className="nb-batch-warn-line">stopped: {batch.stopped_reason}</p>}
                  {batch.warnings && batch.warnings.length > 0 && (
                    <ul className="nb-batch-warnings">
                      {batch.warnings.map((w) => <li key={w}>⚠ {w}</li>)}
                    </ul>
                  )}
                </div>
              </div>
            )}
            {nextItems.length > 0 && (
              <div className="nb-details-block">
                <h4 className="nb-details-h">Next</h4>
                <ul className="nb-details-list">
                  {nextItems.map((n) => <li key={n}>{n}</li>)}
                </ul>
              </div>
            )}
          </div>
        </Modal>
      )}

      {scratchForm && scratchExec && (
        <Modal title="Save scratch run as section" onClose={() => { setScratchForm(null); setScratchExec(null) }} footer={
          <>
            <Button onClick={() => { setScratchForm(null); setScratchExec(null) }}>Cancel</Button>
            <Button variant="primary" disabled={formBusy || !scratchForm.name || !scratchForm.description} onClick={() => void promoteScratch()}>
              <SwapLabel show={formBusy ? 'Saving…' : 'Save section'} options={['Save section', 'Saving…']} />
            </Button>
          </>
        }>
          <div className="grid gap-3">
            {formErr && <Notice tone="warning">{formErr}</Notice>}
            <p className="text-xs text-sec">Promotes the code from the last successful scratch run (exec {scratchExec}).</p>
            <label className="block">
              <span className="mb-1 block text-xs font-medium text-sec">Name</span>
              <input className="input w-full font-mono" value={scratchForm.name} placeholder="load_data"
                onChange={(e) => setScratchForm({ ...scratchForm, name: e.target.value })} />
            </label>
            <label className="block">
              <span className="mb-1 block text-xs font-medium text-sec">Description (required)</span>
              <input className="input w-full" value={scratchForm.description} placeholder="one line — what this section does"
                maxLength={200} onChange={(e) => setScratchForm({ ...scratchForm, description: e.target.value })} />
            </label>
          </div>
        </Modal>
      )}
    </div>
  )
}
