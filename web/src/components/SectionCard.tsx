import { Fragment, lazy, Suspense, useEffect, useRef, useState, type KeyboardEvent, type ReactNode } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import CodeMirror from '@uiw/react-codemirror'
import { api, ApiError, Instance, Notebook, NotebookSectionMeta, NotebookSectionResponse, SaveNotebookSectionResponse } from '../api'
import { AutosaveQueue, type AutosaveState } from '../autosave'
import { readPendingDraft, writePendingDraft, type PendingDraft } from '../pendingDraft'
import { Button, Confirm, Icon, Menu, MenuItem, MenuSeparator, SwapLabel } from './ui'
import { useStarlarkEditor } from './editor'
import { cmTheme } from './codemirror'
import { fmtTime, relTime } from '../format'
import { chartReservedBox } from '../chartBox'

// ChartView is lazy so the vega stack lands in its own chunk; the
// Suspense fallback reserves the same box the chart will occupy (S).
const ChartView = lazy(() => import('./ChartView'))

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
export const sectionStateLabel = (state: NotebookSectionMeta['state']): string =>
  ({ not_run: 'not run', fresh: 'fresh', stale: 'stale', failed: 'failed' } as const)[state] ?? 'not run'

const stateTone: Record<string, string> = {
  not_run: 'text-mute',
  fresh: 'text-sec',
  stale: 'text-warn-text',
  failed: 'text-danger',
}
export function SectionStateBadge({ state }: { state: NotebookSectionMeta['state'] }) {
  return <span className={`whitespace-nowrap text-xs font-medium ${stateTone[state] ?? stateTone.not_run}`}>{sectionStateLabel(state)}</span>
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
  /** a section creation is in flight — disables the insert-after item */
  adding?: boolean
  register: (name: string, q: AutosaveQueue<SectionValue> | null) => void
  onStructural: () => void
  /** onceConfirmed: the card's own once-warning was already accepted */
  onRun: (name: string, onceConfirmed?: boolean) => void
  onInsertAfter: (name: string) => void
  /** per-cell run feedback from the parent's last single-section run (P5) */
  note?: { tone: 'warning' | 'neutral'; text: string } | null
  /** focus the CodeMirror once when this card's editor is created */
  focusOnMount?: boolean
}

// SectionCard is one notebook section: ordinal + name + description,
// the derived state badge, a CodeMirror editor autosaving through a
// serial CAS queue, last-run output, and the Run action. Polling never
// overwrites a dirty draft (C2) and never reorders the card (S5 is
// handled by the parent's held order).
export function SectionCard({ id, inst, meta, order, incarnation, canRun, busy, adding, register, onStructural, onRun, onInsertAfter, note, focusOnMount }: SectionCardProps) {
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
  // A just-created section focuses its editor once; collapsing and
  // reopening must not steal focus again (C1).
  const didFocus = useRef(false)
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
  // The once menu item reads the DRAFT rerun so the label flips as soon
  // as it is picked, before the poll returns the saved meta; the header
  // `once` tag stays on saved meta only.
  const draftRerun = value?.rerun ?? meta.rerun
  // Output provenance comes from the section DETAIL (fresher than the
  // outline row): a previous incarnation marks it previous-runtime,
  // and a stale state means the code changed since that run.
  const dmeta = detail?.section ?? meta
  const prevRuntime = dmeta.last_incarnation > 0 && dmeta.last_incarnation !== incarnation
  const outStale = dmeta.state === 'stale'
  const outTag = prevRuntime ? 'previous runtime' : outStale ? 'code changed since run' : ''
  const editor = useStarlarkEditor(id, live, as?.saving ?? false)

  // The gutter Run control and ⌘/Ctrl+Enter share one path: same
  // disabled conditions, same once-confirm detour (P1/P2).
  const runDisabled = busy || as?.conflict || !!as?.error || det.isLoading
  const startRun = () => {
    if (runDisabled) return
    if (runOnce) setConfirmOnce(true)
    else onRun(name, false)
  }
  // ⌘/Ctrl+Enter inside THIS section's editor runs this section.
  // The chord is always claimed so a disabled run cannot fall through
  // to an editor binding; startRun honours the disabled conditions.
  const onEditorKeyDown = (e: KeyboardEvent<HTMLElement>) => {
    if (e.key !== 'Enter' || !(e.metaKey || e.ctrlKey)) return
    e.preventDefault()
    e.stopPropagation()
    startRun()
  }

  const factParts: ReactNode[] = []
  if (meta.defines.length > 0) factParts.push(<>defines <code>{meta.defines.join(', ')}</code></>)
  if (meta.uses.length > 0) factParts.push(<>uses <code>{meta.uses.join(', ')}</code></>)
  if (meta.effects.length > 0) factParts.push(<>calls <code>{meta.effects.join(', ')}</code></>)
  if (meta.depends_on.length > 0) factParts.push(<>needs <code>{meta.depends_on.join(', ')}</code></>)
  const readonlyNote = inst.status === 'expired'
    ? 'Expired — read-only.'
    : lost
      ? 'Runtime lost — code is retained; running restores an empty process.'
      : 'Read-only.'

  return (
    <section
      id={`section-${name}`}
      aria-label={`Section ${name}`}
      className="nb-card"
      data-section={name}
    >
      <div className="nb-gutter">
        {canExec && (
          <Button
            variant="ghost" size="xs" className="nb-icon-btn"
            aria-label="Run section" aria-busy={busy}
            title={as?.conflict ? 'Resolve the save conflict first' : 'Run this section — upstream sections that are not fresh run first'}
            disabled={runDisabled}
            onClick={startRun}
          >
            {busy
              ? <span className="inline-block h-3.5 w-3.5 motion-safe:animate-spin rounded-full border-2 border-current border-t-transparent" aria-hidden />
              : <Icon name="play" />}
          </Button>
        )}
      </div>
      <div className="nb-body">
        <header className="nb-card-head">
          <span className="nb-ordinal">{meta.position + 1}.</span>
          <h3 className="nb-name">{name}</h3>
          <SectionStateBadge state={meta.state} />
          {meta.rerun === 'once' && (
            <span className="nb-once" title="Skipped by bulk runs after a prior success">once</span>
          )}
          {canEdit ? (
            <input
              className="nb-desc-input"
              value={value?.description ?? ''}
              placeholder="Describe this section (required)"
              maxLength={200}
              aria-label={`${name} description`}
              onChange={(e) => update({ description: e.target.value })}
            />
          ) : meta.description ? (
            <span className="nb-desc-text" title={meta.description}>{meta.description}</span>
          ) : null}
          <div className="nb-card-actions">
            <SaveStatus st={as} onRetry={retrySave} />
            <Button
              variant="ghost" size="xs" className="nb-icon-btn nb-collapse"
              aria-expanded={codeOpen}
              aria-label={`${codeOpen ? 'Collapse' : 'Expand'} ${name} code`}
              onClick={() => setCodeOpen((o) => !o)}
            >
              <span className="nb-chevs" aria-hidden>
                <Icon name="chevron" className="nb-chev nb-chev-down" />
                <Icon name="chevron" className="nb-chev nb-chev-right" />
              </span>
            </Button>
            {canEdit && (
              <Menu trigger={
                <Button variant="ghost" size="xs" className="nb-icon-btn" aria-label={`Section ${name} actions`}>
                  <Icon name="more" />
                </Button>
              }>
                <MenuItem disabled={idx <= 0} onSelect={() => void move(-1)}>Move up</MenuItem>
                <MenuItem disabled={idx < 0 || idx >= order.length - 1} onSelect={() => void move(1)}>Move down</MenuItem>
                <MenuItem disabled={adding} onSelect={() => onInsertAfter(name)}>Insert section after</MenuItem>
                <MenuItem onSelect={() => update({ rerun: draftRerun === 'once' ? 'safe' : 'once' })}>
                  {draftRerun === 'once' ? 'Unmark run-once' : 'Mark as run-once'}
                </MenuItem>
                <MenuSeparator />
                <MenuItem tone="danger" onSelect={() => setConfirmDelete(true)}>Delete…</MenuItem>
              </Menu>
            )}
          </div>
        </header>

        {codeOpen && (
          <div className="nb-editor" onKeyDownCapture={onEditorKeyDown}>
            {canEdit ? (
              <CodeMirror
                value={value?.code ?? ''}
                minHeight="44px"
                maxHeight="420px"
                extensions={editor.extensions}
                onChange={(code) => update({ code })}
                onCreateEditor={(view) => {
                  if (focusOnMount && !didFocus.current) {
                    didFocus.current = true
                    view.focus()
                  }
                }}
                theme={cmTheme}
              />
            ) : (
              <pre className="nb-code-readonly">{value?.code || '(empty)'}</pre>
            )}
          </div>
        )}

        {(as?.conflict || recovered || structErr || note) && (
          <div className="nb-alerts">
            {as?.conflict && (
              <div className="nb-conflict" role="alert">
                <p className="nb-conflict-text">This section changed elsewhere — your draft is kept.</p>
                <div className="nb-conflict-actions">
                  <Button size="xs" onClick={() => void resolveConflict('reload').catch((e) => setStructErr(e.message))}>Reload server copy</Button>
                  <Button size="xs" onClick={() => void resolveConflict('draft').catch((e) => setStructErr(e.message))}>Keep my draft</Button>
                </div>
              </div>
            )}
            {recovered && (
              <div className="nb-conflict" role="alert">
                <p className="nb-conflict-text">Unsaved local edits from a previous visit are still here.</p>
                <div className="nb-conflict-actions">
                  <Button size="xs" onClick={discardPending}>Discard backup</Button>
                  <Button size="xs" onClick={restorePending}>Restore edits</Button>
                </div>
              </div>
            )}
            {structErr && <p className="nb-err">{structErr}</p>}
            {note && <p className={`nb-note${note.tone === 'warning' ? ' nb-note-warn' : ''}`} role="status">{note.text}</p>}
          </div>
        )}

        {detail?.output ? (
          <div className="nb-output">
            <div className="nb-output-head">
              <span className="nb-output-kind">output</span>
              {outTag && <span className="nb-output-tag">{outTag}</span>}
              {detail.output_truncated && <span className="nb-output-tag">truncated</span>}
            </div>
            <pre className="nb-output-pre">{detail.output}</pre>
          </div>
        ) : null}
        {detail?.displays?.map((d, i) => (
          <div className="nb-output" key={i}>
            <div className="nb-output-head">
              <span className="nb-output-kind">chart</span>
              {d.title && <span className="nb-output-meta">{d.title}</span>}
              {d.redacted && <span className="nb-output-tag">redacted</span>}
            </div>
            <Suspense fallback={<div className="nb-chart" style={chartReservedBox(d.spec)} />}>
              <ChartView display={d} />
            </Suspense>
          </div>
        ))}
        {detail?.error && (
          <div className="nb-output nb-output-err">
            <div className="nb-output-head">
              <span className="nb-output-kind">error</span>
              {outTag && <span className="nb-output-tag">{outTag}</span>}
            </div>
            <pre className="nb-output-pre">{detail.error}</pre>
          </div>
        )}

        <footer className="nb-card-foot">
          <span>
            {meta.last_run_at
              ? <>last run <span title={fmtTime(meta.last_run_at)}>{relTime(meta.last_run_at)}</span>{meta.last_status ? ` · ${meta.last_status}` : ''}</>
              : 'never run'}
          </span>
          {factParts.length > 0 && (
            <span className="nb-facts">
              {factParts.map((p, i) => <Fragment key={i}>{i > 0 && ' · '}{p}</Fragment>)}
            </span>
          )}
          {!canEdit && <span className="nb-readonly-note">{readonlyNote}</span>}
        </footer>
      </div>

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
