import { useCallback, useEffect, useRef, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import CodeMirror from '@uiw/react-codemirror'
import { api, ApiError, Application, Run, workspaces } from '../api'
import { Button, Confirm, Loading, Notice, PageHeader, RevealNew, SwapLabel } from '../components/ui'
import { cmTheme } from '../components/codemirror'
import { useApplicationEditor } from '../components/editor'
import { RunProgress, useRunProgress } from '../components/RunProgress'
import { SpecForm } from '../components/SpecForm'
import type { DraftError, InstanceDraft, InstanceSpec } from '../instanceDraft'
import './application.css'
import { ApplicationCallExamples } from '../components/ApplicationCallExamples'
import { AppKeyModal } from '../components/AppKeyModal'
import { useHeldOrder } from '../stableList'
import { fmtDateTime, type RunPhase } from '../interrupts'

type Draft = {
  name: string
  code: string
  input: string
  spec: string
  inOn: boolean
  inText: string
  outOn: boolean
  outText: string
}
type Saved = { draft: Draft; version: number; fp: string }

const DEFAULT_INPUT = '{\n  "name": "world"\n}'

const statusHint: Record<string, string> = {
  draft: 'Not published.',
  preparing: 'Deploying… Invoke will be enabled when ready.',
  degraded: 'Deployment unhealthy.',
  disabled: 'Deployment disabled.',
}

function draftOf(a: Application): Draft {
  // spec_json is the verbatim stored text — re-serializing the decoded
  // object would round precision-limited numbers in limits.
  return {
    name: a.name, code: a.code, input: DEFAULT_INPUT,
    spec: a.spec_json ?? '{}',
    inOn: a.input_schema != null, inText: a.input_schema ?? '',
    outOn: a.output_schema != null, outText: a.output_schema ?? '',
  }
}

// The SpecForm edits a parsed copy of the stored spec text; an
// unparseable stored spec simply starts empty rather than breaking
// the editor.
function parseSpec(text: string): InstanceSpec | undefined {
  try {
    const v = JSON.parse(text.trim() || '{}')
    return v && typeof v === 'object' && !Array.isArray(v) ? (v as InstanceSpec) : undefined
  } catch { return undefined }
}

// sameJSON deep-compares ignoring object key order — key order differs
// between a stored spec_json and the builder's serialization.
function sameJSON(a: unknown, b: unknown): boolean {
  const canon = (v: unknown): string => {
    if (Array.isArray(v)) return `[${v.map(canon).join(',')}]`
    if (v && typeof v === 'object') return `{${Object.keys(v).sort().map((k) => `${JSON.stringify(k)}:${canon((v as Record<string, unknown>)[k])}`).join(',')}}`
    return JSON.stringify(v)
  }
  return canon(a) === canon(b)
}

function fp(d: Draft): string {
  return JSON.stringify([d.name, d.code, d.spec, d.inOn, d.inText, d.outOn, d.outText])
}

function validSaved(x: unknown): x is Saved {
  const s = x as Saved
  if (!s || typeof s !== 'object' || !Number.isSafeInteger(s.version) || s.version < 1 || typeof s.fp !== 'string') return false
  const d = s.draft as Draft
  return !!d && typeof d === 'object' &&
    ['name', 'code', 'input', 'spec', 'inText', 'outText'].every((k) => typeof (d as any)[k] === 'string') &&
    ['inOn', 'outOn'].every((k) => typeof (d as any)[k] === 'boolean')
}

function scopeKey(user: string, ws: string, id: string) { return JSON.stringify(['appv1', user, ws, id]) }

// autosavable: the spec text parses as JSON (or is empty) and every
// enabled schema is non-empty JSON — the shapes the PUT accepts.
function autosavable(d: Draft): boolean {
  try {
    if (d.spec.trim()) JSON.parse(d.spec)
    for (const [on, t] of [[d.inOn, d.inText], [d.outOn, d.outText]] as const) {
      if (on && !t.trim()) return false
      if (on) JSON.parse(t)
    }
    return true
  } catch { return false }
}
const mirror = new Map<string, Saved>()

function loadSaved(key: string, app: Application): { s: Saved; corrupt: boolean } {
  const fresh: Saved = { draft: draftOf(app), version: app.version, fp: fp(draftOf(app)) }
  const local = mirror.get(key)
  if (local) return { s: local, corrupt: false }
  try {
    const raw = sessionStorage.getItem(key)
    if (raw) {
      let x: unknown
      try { x = JSON.parse(raw) } catch { return { s: fresh, corrupt: true } }
      if (validSaved(x)) return { s: x, corrupt: false }
      return { s: fresh, corrupt: true } // keep the blob for the user to reset
    }
  } catch { /* storage broken */ }
  return { s: mirror.get(key) ?? fresh, corrupt: false }
}

function persist(key: string, s: Saved): boolean {
  mirror.set(key, s)
  try { sessionStorage.setItem(key, JSON.stringify(s)); return true } catch { return false }
}

// Outer loader: resolves the app + caller scope, then mounts a keyed
// inner editor so navigating A->B can never carry A's draft into B.
export default function ApplicationDetail() {
  const { id } = useParams<{ id: string }>()
  const qc = useQueryClient()
  const me = qc.getQueryData<{ user: { id: string } }>(['me'])
  const wsq = useQuery({ queryKey: ['workspaces'], queryFn: workspaces })
  const q = useQuery({
    queryKey: ['application', id],
    queryFn: () => api.get<{ application: Application }>(`/api/v1/applications/${id}`).then((r) => r.application),
    // poll faster while a publish is deploying so Invoke unblocks sooner
    refetchInterval: (q) => (q.state.data?.status === 'preparing' ? 1000 : 5000),
    retry: false,
  })
  if (q.isLoading || !me?.user) return <Loading />
  if (q.isError) return <Notice tone="warning">Could not load application: {(q.error as Error).message}</Notice>
  const app = q.data!
  const role = wsq.data?.find((w) => w.id === app.workspace_id)?.role ?? 'viewer'
  return <Editor key={scopeKey(me.user.id, app.workspace_id, app.id)} app={app} role={role}
    userID={me.user.id} qc={qc} />
}

type Props = { app: Application; role: string; userID: string; qc: ReturnType<typeof useQueryClient> }

function Editor({ app, role, userID, qc }: Props) {
  const nav = useNavigate()
  const scope = scopeKey(userID, app.workspace_id, app.id)
  const [init] = useState(() => loadSaved(scope, app))
  const [draft, setDraft] = useState<Draft>(init.s.draft)
  const [base, setBase] = useState<Saved>(init.s)
  const [corrupt, setCorrupt] = useState(init.corrupt)
  const [resetOpen, setResetOpen] = useState(false)
  // fp of the draft whose save last failed; null = no outstanding failure.
  const [failedFp, setFailedFp] = useState<string | null>(null)
  const saveFailed = failedFp !== null
  const [warn, setWarn] = useState('')
  const [storeFailed, setStoreFailed] = useState(false)
  const [run, setRun] = useState<RunView | null>(null)
  const [outdated, setOutdated] = useState(false)
  const [busy, setBusy] = useState('')
  const [delOpen, setDelOpen] = useState(false)
  const [keyOpen, setKeyOpen] = useState(false)
  const [published, setPublished] = useState(false)
  const [reloadOpen, setReloadOpen] = useState<Application | null>(null)
  const [adv, setAdv] = useState(true)
  // SpecForm remount key: bumped on reset so a corrupt stored spec is
  // re-parsed, never remounted while the user is editing.
  const [specKey, setSpecKey] = useState(0)
  const [specErrs, setSpecErrs] = useState<DraftError[]>([])
  const [initialSpec, setInitialSpec] = useState<InstanceSpec | undefined>(() => parseSpec(init.s.draft.spec))
  // The first onSpecChange is the pristine baseline — ignore it so an
  // untouched stored spec keeps its verbatim text (see draftOf).
  const specSeen = useRef(false)
  const mounted = useRef(false)
  // Completion is built from the draft spec — no live instance needed.
  const editor = useApplicationEditor(draft.spec, app.workspace_id)
  const req = useRef(0)
  const latest = useRef(draft)
  latest.current = draft
  const latestBase = useRef(base)
  latestBase.current = base
  useEffect(() => { mounted.current = true; return () => { mounted.current = false } }, [])

  const view = role === 'owner' || role === 'editor' ? 'edit' : 'view'
  const dirty = fp(draft) !== base.fp || draft.input !== base.draft.input
  useEffect(() => {
    if (!dirty) return
    const h = (e: BeforeUnloadEvent) => { e.preventDefault() }
    window.addEventListener('beforeunload', h)
    return () => window.removeEventListener('beforeunload', h)
  }, [dirty])

  const edit = (patch: Partial<Draft>) => {
    if (corrupt) return
    setDraft((cur) => {
      const next = { ...cur, ...patch }
      setStoreFailed(!persist(scope, { draft: next, version: latestBase.current.version, fp: latestBase.current.fp }))
      return next
    })
  }
  // Only a real user edit rewrites draft.spec: the baseline call is
  // skipped and no-op serializations are dropped, so a stored spec is
  // never re-serialized without a change (see draftOf for why its
  // verbatim text matters).
  const onSpec = useCallback((_d: InstanceDraft, errs?: DraftError[], built?: { spec?: InstanceSpec; specJSON?: string }) => {
    setSpecErrs(errs ?? [])
    if (!specSeen.current) { specSeen.current = true; return }
    if (!built) return
    const cur = latest.current.spec
    if (built.specJSON !== undefined) {
      // Advanced JSON mode carries verbatim text; only a real edit lands.
      if (built.specJSON !== cur) edit({ spec: built.specJSON })
      return
    }
    // Errors leave built.spec undefined — never clobber the stored
    // text with an empty serialization.
    if (!built.spec) return
    // An order-insensitive equality keeps the stored formatting (and
    // precision) when the form merely re-describes the same spec; an
    // unparseable stored spec is left alone too.
    let curParsed: unknown
    try { curParsed = JSON.parse(cur.trim() || 'null') } catch { return } // keep unparseable stored text
    if (!sameJSON(curParsed, built.spec)) edit({ spec: JSON.stringify(built.spec) })
  }, [])
  type Result = { id: string; output?: unknown; error?: { code: string; message: string }; duration_ms: number; steps: number }
  type RunView = { result: Result; output_json?: string; run_id?: string; run_status?: string; waiting?: { interrupt_id: string; console_url: string } }
  // The Invoke card owns its own input/result; neither touches the draft.
  const [callInput, setCallInput] = useState(DEFAULT_INPUT)
  const [callRun, setCallRun] = useState<RunView | null>(null)

  // Save pipeline: snapshot everything BEFORE the flight; a draft that
  // changed during the save aborts the chained test/publish instead of
  // publishing code the user already moved past.
  const put = async (fromVersion: number, d: Draft): Promise<Application | null> => {
    if ((d.inOn && !d.inText.trim()) || (d.outOn && !d.outText.trim())) {
      throw new Error('An enabled custom schema cannot be empty.')
    }
    const spec = d.spec.trim() || 'null'
    const ins = d.inOn && d.inText.trim() ? JSON.stringify(d.inText) : 'null'
    const outs = d.outOn && d.outText.trim() ? JSON.stringify(d.outText) : 'null'
    const body = `{"version":${fromVersion},"name":${JSON.stringify(d.name)},"code":${JSON.stringify(d.code)},"spec":${spec},"input_schema":${ins},"output_schema":${outs}}`
    const r = await api.putRawJson<{ application: Application }>(`/api/v1/applications/${app.id}`, body)
    return r.application ?? null
  }

  // save runs the PUT only when the draft actually changed; returns the
  // advanced app or null (conflict / error already surfaced in warn).
  const save = async (): Promise<{ app2: Application; draftFp: string } | null> => {
    const snap = { ...latest.current }
    const snapFp = fp(snap)
    const base0 = latestBase.current
    if (snapFp === base0.fp) {
      if (app.version !== base0.version) throw new ApiError(409, 'conflict', 'Saved elsewhere.')
      return { app2: app, draftFp: snapFp } // nothing to save; chain proceeds
    }
    const rid = ++req.current
    const a2 = await put(base0.version, snap)
    if (!mounted.current || rid !== req.current) return null
    if (!a2) throw new Error('Application save returned no draft.')
    const advanced = { draft: snap, version: a2.version, fp: snapFp }
    latestBase.current = advanced
    setBase(advanced)
    persist(scope, { ...advanced, draft: latest.current })
    qc.setQueryData(['application', app.id], a2)
    qc.invalidateQueries({ queryKey: ['applications'] })
    if (fp(latest.current) !== snapFp || latest.current.input !== snap.input) {
      // user kept typing during the save — keep their edits, do not
      // chain into test/publish with stale code.
      setWarn('Changed while saving; try again.')
      return null
    }
    return { app2: a2, draftFp: snapFp }
  }

  // attemptSave records the outcome: a non-conflict failure leaves the
  // "Couldn't save" indicator up until a save lands; a conflict keeps the
  // Reload Confirm flow instead. A null result (edit mid-flight) is
  // neither success nor failure.
  const attemptSave = async (): Promise<{ app2: Application; draftFp: string } | null> => {
    try {
      const r = await save()
      if (r) setFailedFp(null)
      return r
    } catch (x: any) {
      if (x?.code !== 'conflict') setFailedFp(fp(latest.current))
      throw x
    }
  }

  const guarded = async (kind: string, fn: () => Promise<void>) => {
    if (corrupt || busy !== '') return
    setWarn(''); setBusy(kind)
    try { await fn() }
    catch (x: any) {
      if (!mounted.current) return
      if (x.code === 'conflict') {
        try {
          const fresh = (await api.get<{ application: Application }>(`/api/v1/applications/${app.id}`)).application
          setReloadOpen(fresh)
        } catch { setWarn('Saved elsewhere.') }
      } else setWarn(x.message)
    }
    finally { if (mounted.current) setBusy('') }
  }

  // fp covers everything the server persists; the test input is local.
  const fpDirty = fp(draft) !== base.fp
  const saveNow = () => guarded('save', async () => { await attemptSave() })
  const saveRef = useRef(saveNow)
  saveRef.current = saveNow
  // Autosave: fires only on shapes that would survive the PUT; anything
  // else stays 'Unsaved' silently — Run surfaces the real error.
  useEffect(() => {
    // A failed draft is retried only after a new edit or an explicit Retry.
    if (view !== 'edit' || !fpDirty || corrupt || busy !== '' || specErrs.length > 0 || !autosavable(draft) || fp(draft) === failedFp) return
    const t = window.setTimeout(() => saveRef.current(), 1200)
    return () => window.clearTimeout(t)
  }, [draft, fpDirty, corrupt, busy, view, failedFp, specErrs])
  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 's') {
        e.preventDefault()
        if (view === 'edit' && !corrupt && busy === '' && fp(latest.current) !== latestBase.current.fp) saveRef.current()
      }
    }
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  }, [view, corrupt, busy])

  const onRun = () => guarded('run', async () => {
    const s = await attemptSave()
    if (!s) return
    const snapInput = latest.current.input
    const r = await api.postRawJson<RunView>(`/api/v1/applications/${s.app2.id}/test`,
      `{"version":${s.app2.version},"input":${snapInput}}`)
    if (!mounted.current) return
    if (fp(latest.current) !== s.draftFp || latest.current.input !== snapInput) {
      setOutdated(true) // edits made mid-flight: keep them, mark the result
      setRun(r)
      return
    }
    setOutdated(false)
    setRun(r)
  })

  const onPublish = () => guarded('publish', async () => {
    const s = await attemptSave()
    if (!s) return
    await api.post<{ application: Application }>(`/api/v1/applications/${s.app2.id}/publish`, { version: s.app2.version })
    if (mounted.current) {
      setPublished(true)
      qc.invalidateQueries({ queryKey: ['application', app.id] })
    }
  })

  const onInvoke = () => guarded('invoke', async () => {
    const snapInput = callInput
    const r = await api.postRawJson<RunView>(`/api/v1/applications/${app.id}/invoke`, snapInput)
    if (!mounted.current) return
    setCallRun(r)
  })

  const doDelete = async () => {
    // baseVersion is ours; a poll can never move it
    await api.del(`/api/v1/applications/${app.id}?version=${latestBase.current.version}`)
    qc.invalidateQueries({ queryKey: ['applications'] })
    nav('/applications')
  }

  const resetCorrupt = () => {
    try { sessionStorage.removeItem(scope) } catch { /* ignored */ }
    mirror.delete(scope)
    const fresh: Saved = { draft: draftOf(app), version: app.version, fp: fp(draftOf(app)) }
    setDraft(fresh.draft); setBase(fresh); setCorrupt(false); setWarn('')
    specSeen.current = false
    setSpecErrs([])
    setInitialSpec(parseSpec(fresh.draft.spec))
    setSpecKey((k) => k + 1)
  }

  // Follow each unfinished run to its real outcome: the handler's
  // return value is the application's output (S4 — the Output pre
  // keeps its box while the phase label changes).
  const devPhase = useRunProgress(run?.run_id && !run.output_json && !run.result.error ? run.run_id : undefined).phase
  const callPhase = useRunProgress(callRun?.run_id && !callRun.output_json && !callRun.result.error ? callRun.run_id : undefined).phase
  const preText = (r: RunView | null, p: RunPhase | undefined, placeholder: string): string => {
    if (!r) return placeholder
    if (r.output_json) return r.output_json
    if (r.result.error) return `error: ${r.result.error.code} — ${r.result.error.message}`
    if (!p) return r.waiting ? 'Waiting to be resumed…' : ''
    if (p.phase === 'succeeded') return p.output ?? ''
    if (p.phase === 'waiting') return `Waiting to resume ${p.interrupt.handler ?? 'handler'}…`
    if (p.phase === 'running') return `Running ${p.handler ?? 'handler'}…`
    return `error: ${p.phase}${p.errorCode ? ` — ${p.errorCode}` : ''}`
  }
  const ready = app.status === 'ready'
  const saveState = busy === 'save' ? 'Saving…' : saveFailed ? "Couldn't save" : fpDirty ? 'Unsaved' : 'Saved'
  return (
    <div className="app-page app-editor">
      <PageHeader title={draft.name || 'Application'}
        actions={view === 'edit' ? <Button variant="danger" onClick={() => setDelOpen(true)}>Delete</Button> : undefined}>
        <span className={`app-status app-status-${app.status}`}>{app.status}</span>{' '}
        {app.has_unpublished_changes && <span className="app-dim">unpublished changes</span>}{' '}
        <span className="app-dim"><SwapLabel show={saveState} options={['Saved', 'Saving…', 'Unsaved', "Couldn't save"]} /></span>{' '}
        {saveFailed && view === 'edit' && <Button size="sm" onClick={saveNow} disabled={busy !== ''}>Retry</Button>}
      </PageHeader>
      <p className="app-dim"><Link to="/applications" className="link">← Applications</Link> · <span className="app-mono">{app.id}</span></p>

      <section className="app-card">
        <div className="app-card-head">
          <span className="app-card-title">Develop</span>
          <div className="flex items-center gap-2">
            <Button onClick={onRun} disabled={busy !== '' || view === 'view' || corrupt || specErrs.length > 0}><SwapLabel show={busy === 'run' ? 'Running…' : 'Run'} options={['Run', 'Running…']} /></Button>
            <Button variant="primary" onClick={onPublish} disabled={busy !== '' || view === 'view' || corrupt || specErrs.length > 0}><SwapLabel show={busy === 'publish' ? 'Publishing…' : 'Publish'} options={['Publish', 'Publishing…']} /></Button>
          </div>
        </div>
        <label className="app-label" htmlFor="app-name">Name</label>
        <input id="app-name" className="app-input" value={draft.name} disabled={view === 'view'} onChange={(e) => edit({ name: e.target.value })} />
        <div className="app-grid mt-3">
          <div>
            <label className="app-label" id="app-code-label">Code — main.star</label>
            <div className="app-cm" aria-labelledby="app-code-label">
              <CodeMirror value={draft.code} height="360px" extensions={editor.extensions} theme={cmTheme}
                basicSetup={{ autocompletion: false }}
                readOnly={view === 'view'} onChange={(v) => edit({ code: v })} />
              <div className="editor-footer flex flex-wrap gap-x-4 border-t border-line px-3 py-2 text-xs text-sec">
                <span>Ctrl+Space to complete</span>
                {editor.error
                  ? <span className="text-warn-text">Completion metadata unavailable</span>
                  : <span>{editor.isFetching ? 'Refreshing symbols…' : 'Suggestions use this application’s spec'}</span>}
              </div>
            </div>
          </div>
          <div>
            <label className="app-label" htmlFor="app-input">Input — a JSON object</label>
            <textarea id="app-input" className="app-area" rows={5} spellCheck={false}
              value={draft.input} onChange={(e) => edit({ input: e.target.value })} />
            <div className="app-out">
              <span className="app-label">Output</span>
              <pre className="app-out-pre" aria-live="polite">
                {preText(run, devPhase, 'Run to see output')}
              </pre>
              {outdated && <p className="app-dim">Result is from before your latest edits.</p>}
              {run?.run_id && !run.output_json && !run.result.error && (
                <RunProgress runId={run.run_id} appId={app.id} inline
                  scope={{ userId: userID, workspaceId: app.workspace_id }}
                  fallbackWaiting={run.waiting ?? undefined} />
              )}
            </div>
          </div>
        </div>
        <div className="app-result" role="status" aria-live="polite">
          {specErrs.length > 0 && <Notice tone="warning">{specErrs[0].section}: {specErrs[0].message}</Notice>}
          {warn && <Notice tone="warning">{warn}</Notice>}
          {storeFailed && <Notice tone="warning">Not saved to this tab.</Notice>}
          {published && (
            <Notice><SwapLabel
              show={app.status === 'preparing' ? 'Published — deploying. Invoke becomes available when status is ready.' : 'Published.'}
              options={['Published — deploying. Invoke becomes available when status is ready.', 'Published.']} /></Notice>
          )}
          {corrupt && (
            <Notice tone="warning">
              Saved editor state for this application is unreadable.
              <button className="link-btn" onClick={() => setResetOpen(true)}>Reset it</button>
            </Notice>
          )}
        </div>
      </section>

      <details className="app-card" open={adv} onToggle={(e) => setAdv((e.target as HTMLDetailsElement).open)}>
        <summary><span className="app-card-title">Settings</span></summary>
        <div className="app-card-body">
        <fieldset disabled={view === 'view'} className="app-fieldset">
          <SpecForm key={specKey} application workspaceId={app.workspace_id} initial={initialSpec} onSpecChange={onSpec} />
        </fieldset>
        {(['in', 'out'] as const).map((side) => (
          <div key={side} className="app-schema">
            <label className="app-check">
              <input type="checkbox" checked={side === 'in' ? draft.inOn : draft.outOn} disabled={view === 'view'}
                onChange={(e) => {
                  const on = e.target.checked
                  // an enabled-but-empty schema must not silently fall
                  // back to the default: seed the open-object contract
                  const seed = '{"type":"object","additionalProperties":true}'
                  edit(side === 'in' ? { inOn: on, inText: on && !draft.inText.trim() ? seed : draft.inText }
                                    : { outOn: on, outText: on && !draft.outText.trim() ? seed : draft.outText })
                }} />
              Custom {side === 'in' ? 'input' : 'output'} schema
            </label>
            {(side === 'in' ? draft.inOn : draft.outOn) && (
              <textarea className="app-area" rows={6} spellCheck={false} disabled={view === 'view'}
                aria-label={`${side} schema JSON`}
                value={side === 'in' ? draft.inText : draft.outText}
                onChange={(e) => edit(side === 'in' ? { inText: e.target.value } : { outText: e.target.value })} />
            )}
          </div>
        ))}
        </div>
      </details>

      <section className="app-card">
        <div className="app-card-head">
          <span className="app-card-title">Invoke</span>
          {view === 'edit' && <Button onClick={() => setKeyOpen(true)}>Create API key</Button>}
        </div>
        {!ready && <p className="app-dim">{statusHint[app.status] ?? 'Not ready.'}</p>}
        <div className="app-grid">
          <div>
            <label className="app-label" htmlFor="app-call-input">Input — a JSON object</label>
            <textarea id="app-call-input" className="app-area" rows={5} spellCheck={false}
              value={callInput} onChange={(e) => setCallInput(e.target.value)} />
            <div className="mt-2">
              <Button variant="primary" onClick={onInvoke} disabled={busy !== '' || !ready || view === 'view' || corrupt}><SwapLabel show={busy === 'invoke' ? 'Invoking…' : 'Invoke'} options={['Invoke', 'Invoking…']} /></Button>
            </div>
            <div className="app-out">
              <span className="app-label">Output</span>
              <pre className="app-out-pre app-call-out" aria-live="polite">
                {preText(callRun, callPhase, 'Invoke to see output')}
              </pre>
              {callRun?.run_id && !callRun.output_json && !callRun.result.error && (
                <RunProgress runId={callRun.run_id} appId={app.id} inline
                  scope={{ userId: userID, workspaceId: app.workspace_id }}
                  fallbackWaiting={callRun.waiting ?? undefined} />
              )}
            </div>
          </div>
          <div>
            <span className="app-label">Request examples</span>
            <ApplicationCallExamples appId={app.id} input={callInput} />
          </div>
        </div>
      </section>


      {delOpen && (
        <Confirm tone="danger" title="Delete application" confirm="Delete" pending="Deleting…" onClose={() => setDelOpen(false)}
          action={doDelete}>
          Delete “{draft.name}” and disable its deployment?
        </Confirm>
      )}
      {resetOpen && (
        <Confirm title="Reset unreadable draft" confirm="Reset" pending="Resetting…"
          onClose={() => setResetOpen(false)} action={async () => { resetCorrupt(); setResetOpen(false) }}>
          Discard the unreadable local editor state and load the saved server draft?
        </Confirm>
      )}
      {reloadOpen && (
        <Confirm title="Saved elsewhere" confirm="Reload" pending="Loading…" onClose={() => setReloadOpen(null)}
          action={async () => {
            const a2 = reloadOpen
            const fresh: Saved = { draft: draftOf(a2), version: a2.version, fp: fp(draftOf(a2)) }
            setDraft(fresh.draft); setBase(fresh); persist(scope, fresh); setReloadOpen(null); setWarn('')
          }}>
          This draft was changed by another session. Reload the newer version? Your current edits are kept in this tab until you confirm.
        </Confirm>
      )}
      <section className="app-card">
        <div className="app-card-head"><span className="app-card-title">Runs</span></div>
        <div className="app-card-body"><RunsTable appId={app.id} /></div>
      </section>
      {keyOpen && <AppKeyModal appId={app.id} appName={app.name} workspaceId={app.workspace_id} onClose={() => setKeyOpen(false)} />}
      {busy === 'delete' && <Loading />}
    </div>
  )
}


// RunsTable lists the application's runs with held order (S5/C3); rows
// update in place and reordering waits for the reveal affordance.
function RunsTable({ appId }: { appId: string }) {
  const q = useQuery({
    queryKey: ['runs', appId],
    queryFn: () => api.get<{ runs?: Run[] }>(`/api/v1/applications/${encodeURIComponent(appId)}/runs`),
    refetchInterval: 5000,
  })
  const held = useHeldOrder(q.data?.runs?.map((r) => ({ r })) ?? undefined, ({ r }) => ({ key: r.id, slot: 'runs' }))
  if (q.isLoading) return <Loading />
  if (q.isError) return <Notice tone="warning">Could not load runs.</Notice>
  return (
    <>
      <RevealNew count={held.arrived} moved={held.moved} onClick={held.reveal} />
      {held.rows.length === 0 && held.arrived === 0 && <p className="app-dim">No runs yet.</p>}
      {held.rows.length > 0 && (
        <table className="tbl">
          <thead><tr><th>Status</th><th>Trigger</th><th>Steps</th><th>Started</th><th>Waiting on</th></tr></thead>
          <tbody>
            {held.rows.map(({ item: { r } }) => (
              <tr key={r.id} className="row-hover">
                <td><Link className="link" to={`/applications/${appId}/runs/${r.id}`}><span className={`app-status app-status-${r.status}`}>{r.status}</span></Link></td>
                <td className="text-xs text-sec">{r.trigger}</td>
                <td className="text-xs text-sec">{r.steps}</td>
                <td className="text-xs text-sec">{r.created_at ? fmtDateTime(r.created_at) : ''}</td>
                <td className="text-xs text-sec">{r.interrupt_id ?? '—'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </>
  )
}
