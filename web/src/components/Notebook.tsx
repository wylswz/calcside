import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useSearchParams } from 'react-router-dom'
import { api, ApiError, Instance, Notebook as NotebookData, NotebookSectionResponse, RunSectionsResponse, SaveNotebookSectionResponse } from '../api'
import { AutosaveQueue } from '../autosave'
import { Button, Confirm, Loading, Modal, Notice, RevealNew, SectionTitle, SwapLabel } from './ui'
import { SectionCard, SectionStateBadge, SectionValue, sectionFingerprint, sectionPath } from './SectionCard'
import { RunCode } from './RunCode'
import { useHeldOrder } from '../stableList'
import './notebook.css'

// isRestored/isExpired read the typed error codes the API returns for
// the two lifecycle edges a run can hit.
function errCode(e: unknown): string {
  return e instanceof ApiError ? e.code : ''
}

const nbPath = (id: string) => `/api/v1/instances/${id}/notebook`
const EMPTY_FORM = { name: '', description: '', code: '', after: '' }
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
  const [form, setForm] = useState<SectionForm | null>(null)
  const [formErr, setFormErr] = useState('')
  const [formBusy, setFormBusy] = useState(false)
  const [scratchExec, setScratchExec] = useState<string | null>(null) // exec_id of last good scratch run
  const [scratchForm, setScratchForm] = useState<SectionForm | null>(null)
  const [scratchRunning, setScratchRunningState] = useState(false)

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
    // Busy state goes up BEFORE the first await so every run control —
    // section, batch and scratch — disables in the same tick (P1).
    setRunning(label)
    setRunBlock([])
    setBatch(null)
    try {
      // Single-section runs only need their own save flushed; bulk runs
      // need every draft persisted so saved code matches the screen.
      const blocked = await flushSave(isList ? sel : undefined)
      if (blocked.length) {
        setRunBlock(blocked.map((n) => `section ${n} has an unresolved save — resolve it before running`))
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
          setRunBlock([`unknown section(s): ${missing.join(', ')} — reload the notebook`])
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
        setRunBlock([...new Set(problems)])
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
        refresh()
      } else if (errCode(e) === 'instance_expired') {
        setRunBlock(['instance expired — the notebook is read-only'])
        refresh()
      } else if (e instanceof ApiError && e.status === 409) {
        setRunBlock(['notebook changed while preparing the run — reload and retry; nothing executed'])
        refresh()
      } else {
        setRunBlock([(e as Error).message])
      }
    } finally {
      setRunning(null)
      runLock.current = false
    }
  }, [id, flushSave, refresh])

  const runOne = useCallback((name: string, onceConfirmed = false) => { void runSections([name], onceConfirmed) }, [runSections])

  const freshRevision = async () =>
    notebook?.revision ?? (await api.get<{ notebook?: NotebookData }>(nbPath(id))).notebook?.revision ?? 0

  const createSection = async () => {
    if (!form) return
    setFormBusy(true)
    setFormErr('')
    try {
      const rev = await freshRevision()
      await api.put<SaveNotebookSectionResponse>(
        `/api/v1/instances/${id}/sections/${encodeURIComponent(form.name)}`,
        {
          code: form.code, description: form.description, create_only: true,
          ...(form.after ? { after: form.after } : {}), revision: rev,
        })
      const created = form.name
      setForm(null)
      onStructural(created)
    } catch (e: any) {
      setFormErr(e.message)
    } finally {
      setFormBusy(false)
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

  if (nb.isLoading) return <Loading />
  if (nb.isError) return <Notice tone="warning">Notebook unavailable: {(nb.error as Error).message}</Notice>
  if (!notebook) return null

  const incarnation = notebook.incarnation
  const empty = sections.length === 0

  return (
    <div className="nb">
      {lost && (
        <Notice tone="warning">
          <strong>Runtime lost · code is retained.</strong> The process is gone but the notebook is intact —
          running a section or batch restores an <em>empty</em> process under the same instance; nothing re-runs automatically.
        </Notice>
      )}
      {expired && (
        <Notice tone="warning">
          <strong>Expired.</strong> This notebook is read-only history — code, sections and run records are preserved,
          but nothing can run or be edited.
        </Notice>
      )}
      {restoredNotice && (
        <Notice tone="warning">
          <div className="flex items-start gap-3">
            <p className="flex-1">
              <strong>Runtime restored.</strong>{' '}
              {restoredNotice === 'partial' ? (
                <>Variables and files were cleared when the process was lost — but earlier sections in this batch did execute in the previous runtime. Review the run report before repeating side effects.</>
              ) : (
                <>Variables and files were cleared when the process was lost. Your request did not run — choose sections to execute.</>
              )}
            </p>
            <button className="text-btn" aria-label="Dismiss restored notice" onClick={() => setRestoredNotice(null)}>×</button>
          </div>
        </Notice>
      )}
      {runBlock.length > 0 && (
        <Notice tone="warning">
          {runBlock.map((b) => <div key={b}>{b}</div>)}
        </Notice>
      )}
      {batch && (
        <div className="nb-batch" role="status">
          <div className="mb-2 flex items-center gap-3">
            <span className="text-sm font-semibold text-ink">Run report</span>
            <button className="text-btn ml-auto text-xs text-sec" onClick={() => setBatch(null)}>dismiss</button>
          </div>
          <ul className="nb-batch-list">
            {batch.results.map((r) => (
              <li key={r.name} className="nb-batch-row">
                <code className="nb-batch-name">{r.name}</code>
                <span className={`nb-batch-status nb-batch-${r.status}`}>{r.status}</span>
                {r.skipped_reason && <span className="text-xs text-sec">{r.skipped_reason}</span>}
                {r.error && <span className="min-w-0 flex-1 truncate text-xs text-warn-text" title={r.error}>{r.error}</span>}
              </li>
            ))}
          </ul>
          {batch.warnings && batch.warnings.length > 0 && (
            <ul className="mt-2 text-xs text-sec">
              {batch.warnings.map((w) => <li key={w}>⚠ {w}</li>)}
            </ul>
          )}
        </div>
      )}

      <div className="nb-toolbar">
        <SectionTitle className="!mb-0 flex-1">Notebook</SectionTitle>
        <RevealNew count={held.arrived} moved={held.moved} onClick={held.reveal} />
        {canExec && !empty && (
          <div className="flex items-center gap-2">
            <Button size="sm" disabled={running !== null || scratchRunning} onClick={() => void runSections('stale')}
              title="Run every stale or never-run section in order; once-sections that already succeeded are skipped">
              <SwapLabel show={running === 'stale' ? 'Running…' : 'Run stale'} options={['Run stale', 'Running…']} />
            </Button>
            <Button size="sm" variant="primary" disabled={running !== null || scratchRunning} onClick={() => void runSections('all')}
              title="Run every section in notebook order; once-sections that already succeeded are skipped">
              <SwapLabel show={running === 'all' ? 'Running…' : 'Run all'} options={['Run all', 'Running…']} />
            </Button>
          </div>
        )}
        {canEdit && (
          <Button size="sm" onClick={() => { setFormErr(''); setForm({ ...EMPTY_FORM }) }}>Add section</Button>
        )}
      </div>

      {notebook.next.length > 0 && (
        <ul className="nb-next">
          {notebook.next.map((n) => <li key={n}>{n}</li>)}
        </ul>
      )}

      {empty ? (
        <div className="nb-empty">
          <p className="text-sm text-sec">No sections yet — the notebook is the durable record of this instance's work.</p>
          <div className="mt-3 flex flex-wrap gap-2">
            {canEdit && <Button size="sm" variant="primary" onClick={() => { setFormErr(''); setForm({ ...EMPTY_FORM }) }}>Add section</Button>}
            {canExec && (
              <Button size="sm" onClick={() => {
                setParams((p) => { const n = new URLSearchParams(p); n.set('view', 'notebook'); return n })
                document.getElementById('run-code')?.scrollIntoView({ block: 'center' })
              }}>Use scratch below</Button>
            )}
          </div>
        </div>
      ) : (
        <>
          {/* Compact section directory (C4): held shown order, click
              deep-links and scrolls to the card. */}
          <nav className="nb-directory" aria-label="Section directory">
            {shown.map((m, i) => (
              <button
                key={m.name}
                type="button"
                className="nb-dir-row"
                onClick={() => {
                  setParams((p) => { const n = new URLSearchParams(p); n.set('section', m.name); return n })
                  document.getElementById(`section-${m.name}`)?.scrollIntoView({ block: 'start' })
                }}
              >
                <span className="nb-dir-ord">{i + 1}.</span>
                <span className="nb-dir-name">{m.name}</span>
                {m.description && <span className="nb-dir-desc">{m.description}</span>}
                <SectionStateBadge state={m.state} />
              </button>
            ))}
          </nav>
          <div className="nb-cards">
            {shown.map((meta) => (
              <SectionCard
                key={meta.name}
                id={id}
                inst={inst}
                meta={meta}
                order={order}
                incarnation={incarnation}
                canRun={canRun}
                busy={running === meta.name}
                blocked={scratchRunning}
                register={register}
                onStructural={onStructural}
                onRun={runOne}
                onInsertAfter={(n) => { setFormErr(''); setForm({ ...EMPTY_FORM, after: n }) }}
              />
            ))}
          </div>
        </>
      )}

      <section aria-label="Scratch" className="nb-scratch" id="run-code">
        <SectionTitle>Scratch</SectionTitle>
        <p className="-mt-2 mb-3 text-xs text-sec">scratch execs never become sections — promote a successful run explicitly</p>
        {canExec && (live || lost) && (
          <>
            <RunCode
              inst={inst}
              running={scratchRunning}
              disabled={running !== null}
              setRunning={setScratchRunningGuarded}
              onRan={(r) => { if (!r.error) setScratchExec(r.exec_id) }}
              onRestored={() => setRestoredNotice('trigger')}
            />
            {lost && (
              <p className="mt-2 text-xs text-sec">On a lost instance the first run restores an empty process — this code does not execute on that call.</p>
            )}
          </>
        )}
        {!canExec && expired && (
          <p className="text-sm text-sec">Scratch is unavailable — the instance expired.</p>
        )}
        {scratchExec && (
          <div className="nb-promote">
            <Button size="sm" onClick={() => { setFormErr(''); setScratchForm({ ...EMPTY_FORM }) }}>
              Save last scratch run as section…
            </Button>
          </div>
        )}
      </section>

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

      {form && (
        <Modal title={form.after ? `New section after ${form.after}` : 'New section'} onClose={() => setForm(null)} footer={
          <>
            <Button onClick={() => setForm(null)}>Cancel</Button>
            <Button variant="primary" disabled={formBusy || !form.name || !form.description} onClick={() => void createSection()}>
              <SwapLabel show={formBusy ? 'Creating…' : 'Create section'} options={['Create section', 'Creating…']} />
            </Button>
          </>
        }>
          <div className="grid gap-3">
            {formErr && <Notice tone="warning">{formErr}</Notice>}
            <label className="block">
              <span className="mb-1 block text-xs font-medium text-sec">Name</span>
              <input className="input w-full font-mono" value={form.name} placeholder="load_data"
                onChange={(e) => setForm({ ...form, name: e.target.value })} />
              <span className="mt-1 block text-xs text-mute">[a-z][a-z0-9_] up to 48 chars</span>
            </label>
            <label className="block">
              <span className="mb-1 block text-xs font-medium text-sec">Description (required)</span>
              <input className="input w-full" value={form.description} placeholder="one line — what this section does"
                maxLength={200} onChange={(e) => setForm({ ...form, description: e.target.value })} />
            </label>
            <label className="block">
              <span className="mb-1 block text-xs font-medium text-sec">Code</span>
              <textarea className="input w-full font-mono" rows={8} value={form.code} placeholder="# Starlark"
                onChange={(e) => setForm({ ...form, code: e.target.value })} />
            </label>
            <p className="text-xs text-sec">Created but not run — use Run section or Run all/stale to execute it.</p>
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
