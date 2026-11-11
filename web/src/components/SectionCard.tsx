import { useEffect, useRef, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import CodeMirror from '@uiw/react-codemirror'
import { api, ApiError, Instance, Notebook, NotebookSectionMeta, NotebookSectionResponse, SaveNotebookSectionResponse } from '../api'
import { AutosaveQueue, type AutosaveState } from '../autosave'
import { readPendingDraft, writePendingDraft, type PendingDraft } from '../pendingDraft'
import { Button, Confirm, Menu, MenuItem, SwapLabel } from './ui'
import { useStarlarkEditor } from './editor'
import { cmTheme } from './codemirror'
import { fmtTime, relTime } from '../format'

// SectionValue is everything one autosave PUT carries: code, the
// one-line description and the bulk-run mode move together so a single
// CAS write never splits them.
export interface SectionValue {
  code: string
  description: string
  rerun: 'safe' | 'once'
}

// sectionFingerprint/sectionPath are shared with Notebook's run
// validation: a run may only execute code identical to what the card
// displays and last saved (F3).
export const sectionFingerprint = (v: SectionValue) => JSON.stringify(v)

export const sectionPath = (id: string, name: string) =>
  `/api/v1/instances/${id}/sections/${encodeURIComponent(name)}`

const isSectionValue = (v: unknown): v is SectionValue =>
  !!v && typeof v === 'object' &&
  typeof (v as SectionValue).code === 'string' &&
  typeof (v as SectionValue).description === 'string' &&
  ((v as SectionValue).rerun === 'safe' || (v as SectionValue).rerun === 'once')

// Drafts back up to tab-local storage so SPA navigation cannot silently
// discard them (C2); in-app navigation itself stays unguarded, matching
// the console-wide convention — recovery is explicit on return.
const pendingKey = (ws: string, instance: string, name: string) =>
  `calcside:instance-section:${ws}:${instance}:${name}`

// SectionStateBadge renders the derived run state; colors follow the
// shared signals — failed is the warn tone, stale a warning tone.
export function SectionStateBadge({ state }: { state: NotebookSectionMeta['state'] }) {
  const map: Record<string, { text: string; cls: string }> = {
    not_run: { text: 'not run', cls: 'text-mute' },
    fresh: { text: 'fresh', cls: 'text-sec' },
    stale: { text: 'stale', cls: 'text-warn-text' },
    failed: { text: 'failed', cls: 'text-danger' },
  }
  const s = map[state] ?? map.not_run
  return <span className={`whitespace-nowrap text-xs font-medium ${s.cls}`}>{s.text}</span>
}

// SaveStatus is the persistent autosave indicator (C2): it always shows
// one of Saved / Unsaved / Saving… / Couldn't save / Conflict and never
// claims Saved for a write that did not land (F3). SwapLabel keeps the
// width stable (S4).
const SAVE_OPTIONS = ['Saved', 'Unsaved', 'Saving…', "Couldn't save", 'Conflict'] as const
function SaveStatus({ st, onRetry }: { st: AutosaveState<SectionValue> | null; onRetry: () => void }) {
  if (!st) return <span className="text-xs text-mute">loading…</span>
  const show = st.conflict ? 'Conflict' : st.error ? "Couldn't save" : st.saving ? 'Saving…' : st.dirty ? 'Unsaved' : 'Saved'
  return (
    <span className="inline-flex items-center gap-2 text-xs">
      <SwapLabel show={show} options={SAVE_OPTIONS} />
      {st.error && !st.conflict && (
        <button className="text-btn text-accent" onClick={onRetry}>Retry</button>
      )}
      {st.conflict && <span className="text-warn-text">resolve below</span>}
    </span>
  )
}

export interface SectionCardProps {
  id: string
  inst: Instance
  meta: NotebookSectionMeta
  /** server-ordered section names — needed to compute move neighbours */
  order: string[]
  incarnation: number
  canRun: boolean
  /** this section's run is in flight — disables its Run button (P1) */
  busy?: boolean
  /** another run (scratch or batch) is in flight — disables without relabeling */
  blocked?: boolean
  register: (name: string, q: AutosaveQueue<SectionValue> | null) => void
  onStructural: () => void
  /** onceConfirmed: the card's own once-warning was already accepted */
  onRun: (name: string, onceConfirmed?: boolean) => void
  onInsertAfter: (name: string) => void
}

// SectionCard is one notebook section: ordinal + name + description,
// the derived state badge, a CodeMirror editor autosaving through a
// serial CAS queue, last-run output, and the Run action. Polling never
// overwrites a dirty draft (C2) and never reorders the card (S5 is
// handled by the parent's held order).
export function SectionCard({ id, inst, meta, order, incarnation, canRun, busy, blocked, register, onStructural, onRun, onInsertAfter }: SectionCardProps) {
  const name = meta.name
  const qc = useQueryClient()
  const live = inst.status === 'running'
  const lost = inst.status === 'lost'
  // Writes need a running process; runs additionally work on a lost
  // instance because the first one restores an empty process.
  const canEdit = canRun && live
  const canExec = canRun && (live || lost)
  const qRef = useRef<AutosaveQueue<SectionValue> | null>(null)
  const [as, setAs] = useState<AutosaveState<SectionValue> | null>(null)
  const [value, setValue] = useState<SectionValue | null>(null)
  const valueRef = useRef<SectionValue | null>(null)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [confirmOnce, setConfirmOnce] = useState(false)
  const [codeOpen, setCodeOpen] = useState(true)
  const [structErr, setStructErr] = useState('')
  const [recovered, setRecovered] = useState<PendingDraft<SectionValue> | null>(null)
  const pKey = pendingKey(inst.workspace_id ?? '', id, name)

  const det = useQuery({
    queryKey: ['section', id, name],
    queryFn: () => api.get<NotebookSectionResponse>(`${sectionPath(id, name)}?include_output=true`),
    refetchInterval: live ? 5000 : false,
    retry: false,
  })
  const detail = det.data?.section

  // Structural actions (move/delete) need the freshest notebook CAS —
  // the outline poll may lag a just-landed agent write.
  const freshRevision = async () => {
    const nb = await api.get<{ notebook?: Notebook }>(`/api/v1/instances/${id}/notebook`)
    return nb.notebook?.revision ?? 0
  }

  // save runs inside the queue's serial drain: PUT with the queued CAS
  // version; on a notebook-wide 409, compare OUR section against the
  // server — unchanged means the write raced an unrelated section, so
  // adopt the fresh revision and retry once; changed means a real
  // conflict and the draft stays put behind the conflict UI.
  const save = async (v: SectionValue, ver: number) => {
    const body = { code: v.code, description: v.description, rerun: v.rerun, revision: ver }
    try {
      const r = await api.put<SaveNotebookSectionResponse>(sectionPath(id, name), body)
      return { value: v, version: r.revision }
    } catch (e) {
      if (!(e instanceof ApiError) || e.status !== 409) throw e
      // Capture the notebook revision FIRST, then read the section:
      // a mutation landing between the two reads makes the retry 409
      // instead of overwriting code this client never saw (F3).
      const rev = await freshRevision()
      const fresh = await api.get<NotebookSectionResponse>(`${sectionPath(id, name)}?include_output=false`)
      const fs = fresh.section
      if (!fs) throw e
      const srvVal: SectionValue = { code: fs.code, description: fs.section.description, rerun: fs.section.rerun }
      const base = qRef.current?.snapshot.saved.value
      if (base && sectionFingerprint(base) === sectionFingerprint(srvVal)) {
        const r2 = await api.put<SaveNotebookSectionResponse>(sectionPath(id, name), { ...body, revision: rev })
        return { value: v, version: r2.revision }
      }
      throw e
    }
  }

  // Queue lifecycle: created once the section detail arrives; polling
  // adopts server values only while the draft is clean (C2).
  useEffect(() => {
    if (!detail || qRef.current) return
    const v: SectionValue = { code: detail.code, description: detail.section.description, rerun: detail.section.rerun }
    const q = new AutosaveQueue<SectionValue>({
      initial: { value: v, version: 0 },
      fingerprint: sectionFingerprint,
      save,
      onChange: (st) => {
        writePendingDraft(pKey,
          st.dirty ? { version: st.saved.version, value: st.latest } : null)
        setAs(st)
        valueRef.current = st.latest
        setValue(st.latest)
      },
      delayMs: 800,
    })
    qRef.current = q
    register(name, q)
    setAs(q.snapshot)
    valueRef.current = v
    setValue(v)
    // A draft backed up before an SPA navigation round-trip is offered
    // back explicitly — never silently applied over a newer server copy.
    const pending = readPendingDraft(pKey, isSectionValue)
    if (pending) {
      if (sectionFingerprint(pending.value) !== sectionFingerprint(v)) setRecovered(pending)
      else writePendingDraft(pKey, null)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [detail])

  // Adopt polled server state while clean; a dirty/conflicted draft is
  // never touched by polling.
  useEffect(() => {
    const q = qRef.current
    if (!q || !detail) return
    const st = q.snapshot
    if (st.dirty || st.conflict || st.saving) return
    const v: SectionValue = { code: detail.code, description: detail.section.description, rerun: detail.section.rerun }
    if (sectionFingerprint(v) !== sectionFingerprint(st.saved.value)) {
      q.reset({ value: v, version: st.saved.version })
    }
  }, [detail])

  // Unmount: drop the queue and its registration.
  useEffect(() => () => {
    qRef.current?.dispose()
    qRef.current = null
    register(name, null)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [name])

  // C2: an unsaved draft — or a backed-up draft awaiting a decision —
  // guards tab close. The queue notifies through `as`, so this stays
  // correct while autosave timers fire.
  useEffect(() => {
    if (!as?.dirty && !as?.saving && !recovered) return
    const h = (e: BeforeUnloadEvent) => e.preventDefault()
    window.addEventListener('beforeunload', h)
    return () => window.removeEventListener('beforeunload', h)
  }, [as?.dirty, as?.saving, recovered])

  const restorePending = () => {
    if (!recovered) return
    qRef.current?.update(recovered.value)
    setRecovered(null)
  }
  const discardPending = () => {
    writePendingDraft(pKey, null)
    setRecovered(null)
  }

  const update = (patch: Partial<SectionValue>) => {
    const cur = valueRef.current
    if (!cur || !canEdit) return
    qRef.current?.update({ ...cur, ...patch })
  }

  const retrySave = () => { void qRef.current?.retry().catch(() => undefined) }

  // Conflict recovery fetches the authoritative section + notebook
  // revision, then resolves the queue explicitly (C2/F2): reload drops
  // the draft for the server copy; keep-draft re-bases the CAS and lets
  // autosave overwrite on the user's explicit say-so.
  const resolveConflict = async (mode: 'reload' | 'draft') => {
    // Sequential, revision first: the CAS base must be captured before
    // the detail read so a racing write conflicts on the next save
    // rather than being silently re-based over (F3).
    const nb = await api.get<{ notebook?: Notebook }>(`/api/v1/instances/${id}/notebook`)
    const fresh = await api.get<NotebookSectionResponse>(`${sectionPath(id, name)}?include_output=false`)
    const fs = fresh.section
    if (!fs) throw new Error('section no longer exists')
    const v: SectionValue = { code: fs.code, description: fs.section.description, rerun: fs.section.rerun }
    qRef.current?.resolveConflict(mode, { value: v, version: nb.notebook?.revision ?? 0 })
    qc.invalidateQueries({ queryKey: ['section', id, name] })
    qc.invalidateQueries({ queryKey: ['notebook', id] })
  }

  // Move is a position-only PUT: `after` names the new predecessor.
  // Moving up re-seats the previous section after this one; moving down
  // re-seats this section after the next. Content is never touched.
  const idx = order.indexOf(name)
  const move = async (dir: -1 | 1) => {
    setStructErr('')
    try {
      const rev = await freshRevision()
      if (dir === 1 && idx >= 0 && idx < order.length - 1) {
        await api.put(sectionPath(id, name), { after: order[idx + 1], revision: rev })
      } else if (dir === -1 && idx > 0) {
        await api.put(sectionPath(id, order[idx - 1]), { after: name, revision: rev })
      } else {
        return
      }
      onStructural()
    } catch (e: any) {
      setStructErr(e.message)
    }
  }

  const doDelete = async () => {
    const rev = await freshRevision()
    await api.del(`${sectionPath(id, name)}?revision=${rev}`)
    writePendingDraft(pKey, null)
    qc.invalidateQueries({ queryKey: ['notebook', id] })
    onStructural()
  }

  const runOnce = meta.rerun === 'once' && meta.once_ran
  // Output provenance comes from the section DETAIL (fresher than the
  // outline row): a previous incarnation marks it previous-runtime,
  // and a stale state means the code changed since that run.
  const dmeta = detail?.section ?? meta
  const prevRuntime = dmeta.last_incarnation > 0 && dmeta.last_incarnation !== incarnation
  const outStale = dmeta.state === 'stale'
  const outTag = prevRuntime ? 'previous runtime' : outStale ? 'code changed since run' : ''
  const editor = useStarlarkEditor(id, live, as?.saving ?? false)

  return (
    <section
      id={`section-${name}`}
      aria-label={`Section ${name}`}
      className="nb-card"
      data-section={name}
    >
      <header className="nb-card-head">
        <span className="nb-ordinal">{meta.position + 1}.</span>
        <div className="nb-card-title min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
            <h3 className="nb-name">{name}</h3>
            <SectionStateBadge state={meta.state} />
            {meta.rerun === 'once' && (
              <span className="text-xs text-sec" title="Skipped by bulk runs after a prior success">once · skip in bulk after success</span>
            )}
          </div>
          {meta.description && <p className="nb-desc">{meta.description}</p>}
          {(meta.defines.length > 0 || meta.uses.length > 0 || meta.effects.length > 0 || meta.depends_on.length > 0) && (
            <p className="nb-facts">
              {meta.defines.length > 0 && <>defines <code>{meta.defines.join(', ')}</code>{' · '}</>}
              {meta.uses.length > 0 && <>uses <code>{meta.uses.join(', ')}</code>{' · '}</>}
              {meta.effects.length > 0 && <>calls <code>{meta.effects.join(', ')}</code>{' · '}</>}
              {meta.depends_on.length > 0 && <>needs <code>{meta.depends_on.join(', ')}</code></>}
            </p>
          )}
        </div>
        <div className="nb-card-actions">
          <SaveStatus st={as} onRetry={retrySave} />
          <button
            type="button"
            className="nb-collapse text-btn text-xs text-sec"
            aria-expanded={codeOpen}
            aria-label={`${codeOpen ? 'Collapse' : 'Expand'} ${name} code`}
            onClick={() => setCodeOpen((o) => !o)}
          >
            <SwapLabel show={codeOpen ? '▾ code' : '▸ code'} options={['▾ code', '▸ code']} />
          </button>
          {canExec && (
            <Button size="sm" variant="primary" disabled={busy || blocked || as?.conflict || !!as?.error || det.isLoading}
              title={as?.conflict ? 'Resolve the save conflict first' : undefined}
              onClick={() => (runOnce ? setConfirmOnce(true) : onRun(name, false))}>
              <SwapLabel show={busy ? 'Running…' : 'Run section'} options={['Run section', 'Running…']} />
            </Button>
          )}
          {canEdit && (
            <Menu trigger={<Button size="sm" aria-label={`Section ${name} actions`}>⋯</Button>}>
              <MenuItem disabled={idx <= 0} onSelect={() => void move(-1)}>Move up</MenuItem>
              <MenuItem disabled={idx < 0 || idx >= order.length - 1} onSelect={() => void move(1)}>Move down</MenuItem>
              <MenuItem onSelect={() => onInsertAfter(name)}>Insert section after…</MenuItem>
              <MenuItem tone="danger" onSelect={() => setConfirmDelete(true)}>Delete…</MenuItem>
            </Menu>
          )}
        </div>
      </header>

      {as?.conflict && (
        <div className="nb-conflict" role="alert">
          <p className="text-sm text-ink">This section changed elsewhere — your draft is kept.</p>
          <div className="mt-2 flex gap-2">
            <Button size="sm" onClick={() => void resolveConflict('reload').catch((e) => setStructErr(e.message))}>Reload server copy</Button>
            <Button size="sm" variant="primary" onClick={() => void resolveConflict('draft').catch((e) => setStructErr(e.message))}>Keep my draft</Button>
          </div>
        </div>
      )}
      {recovered && (
        <div className="nb-conflict" role="alert">
          <p className="text-sm text-ink">Unsaved local edits from a previous visit are still here.</p>
          <div className="mt-2 flex gap-2">
            <Button size="sm" onClick={discardPending}>Discard backup</Button>
            <Button size="sm" variant="primary" onClick={restorePending}>Restore edits</Button>
          </div>
        </div>
      )}
      {structErr && <p className="nb-err">{structErr}</p>}

      {codeOpen && (
      <div className="nb-editor">
        {canEdit ? (
          <>
            <input
              className="nb-desc-input"
              value={value?.description ?? ''}
              placeholder="one-line description (required)"
              maxLength={200}
              aria-label={`${name} description`}
              onChange={(e) => update({ description: e.target.value })}
            />
            <CodeMirror
              value={value?.code ?? ''}
              height="180px"
              extensions={editor.extensions}
              onChange={(code) => update({ code })}
              theme={cmTheme}
            />
            <label className="nb-once">
              <input
                type="checkbox"
                checked={value?.rerun === 'once'}
                onChange={(e) => update({ rerun: e.target.checked ? 'once' : 'safe' })}
              />
              Skip in bulk runs after success
            </label>
          </>
        ) : (
          <>
            <pre className="nb-code-readonly">{value?.code || '(empty)'}</pre>
            <p className="nb-readonly-note">
              {inst.status === 'expired' ? 'Expired — read-only.' : lost ? 'Runtime lost — code is retained; running restores an empty process.' : 'Read-only.'}
            </p>
          </>
        )}
      </div>
      )}

      <footer className="nb-card-foot">
        <span className="text-xs text-sec">
          {meta.last_run_at
            ? <>last run <span title={fmtTime(meta.last_run_at)}>{relTime(meta.last_run_at)}</span>{meta.last_status ? ` · ${meta.last_status}` : ''}</>
            : 'never run'}
        </span>
        {detail?.output ? (
          <details className="nb-output">
            <summary className="cursor-pointer text-xs text-sec">
              output{outTag && <span className="ml-2 text-warn-text">{outTag}</span>}
              {detail.output_truncated && <span className="ml-2 text-warn-text">truncated</span>}
            </summary>
            <pre className="pre mt-2 max-h-48">{detail.output}</pre>
          </details>
        ) : null}
        {detail?.error && (
          <details className="nb-output" open={meta.state === 'failed'}>
            <summary className="cursor-pointer text-xs text-warn-text">
              error{outTag && <span className="ml-2">· {outTag}</span>}
            </summary>
            <pre className="pre mt-2 max-h-48 border-l-4 border-l-warn text-warn-text">{detail.error}</pre>
          </details>
        )}
      </footer>

      {confirmOnce && (
        <Confirm
          title={`Run ${name} again?`}
          confirm="Run again"
          pending="Running…"
          action={async () => { setConfirmOnce(false); onRun(name, true) }}
          onClose={() => setConfirmOnce(false)}
        >
          <p className="text-sm text-body">This section is marked “once” — it already succeeded. Bulk runs skip it; running it explicitly repeats it in the current process.</p>
        </Confirm>
      )}
      {confirmDelete && (
        <Confirm
          title={`Delete section ${name}?`}
          confirm="Delete"
          pending="Deleting…"
          tone="danger"
          action={doDelete}
          onClose={() => setConfirmDelete(false)}
        >
          <p className="text-sm text-body">The code is removed from the notebook; run history stays. Sections that read its definitions are marked stale.</p>
        </Confirm>
      )}
    </section>
  )
}
