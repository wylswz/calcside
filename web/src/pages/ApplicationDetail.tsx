import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import CodeMirror from '@uiw/react-codemirror'
import { python } from '@codemirror/lang-python'
import { api, ApiError, Application, workspaces } from '../api'
import { Button, Confirm, Loading, Notice, PageHeader, SwapLabel } from '../components/ui'
import { cmTheme } from '../components/codemirror'
import './application.css'
import { ApplicationCallExamples } from '../components/ApplicationCallExamples'

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
    refetchInterval: 5000,
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
  const [warn, setWarn] = useState('')
  const [storeFailed, setStoreFailed] = useState(false)
  const [run, setRun] = useState<RunView | null>(null)
  const [outdated, setOutdated] = useState(false)
  const [busy, setBusy] = useState('')
  const [delOpen, setDelOpen] = useState(false)
  const [reloadOpen, setReloadOpen] = useState<Application | null>(null)
  const [adv, setAdv] = useState(false)
  const mounted = useRef(false)
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
  type Result = { id: string; output?: unknown; error?: { code: string; message: string }; duration_ms: number; steps: number }
  type RunView = { result: Result; output_json?: string }

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

  const onSave = () => guarded('save', async () => { await save() })

  const onTest = () => guarded('test', async () => {
    const s = await save()
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
    const s = await save()
    if (!s) return
    await api.post<{ application: Application }>(`/api/v1/applications/${s.app2.id}/publish`, { version: s.app2.version })
    if (mounted.current) qc.invalidateQueries({ queryKey: ['application', app.id] })
  })

  const onInvoke = () => guarded('invoke', async () => {
    const snapInput = latest.current.input
    const r = await api.postRawJson<RunView>(`/api/v1/applications/${app.id}/invoke`, snapInput)
    if (!mounted.current) return
    if (fp(latest.current) !== latestBase.current.fp || latest.current.input !== snapInput) setOutdated(true)
    else setOutdated(false)
    setRun(r)
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
  }

  const ready = app.status === 'ready'
  return (
    <div className="app-page app-editor">
      <PageHeader title={draft.name || 'Application'}>
        <span className={`app-status app-status-${app.status}`}>{app.status}</span>
        {app.has_unpublished_changes && <span className="app-dim">unpublished changes</span>}
        {dirty && <span className="app-dim">unsaved</span>}
      </PageHeader>
      <p className="app-dim"><Link to="/applications" className="link">← Applications</Link> · <span className="app-mono">{app.id}</span></p>

      <ol className="app-steps">
        <li><strong>1 · Write</strong> — edit <code>main(input)</code> below.</li>
        <li><strong>2 · Test</strong> — saves the draft and runs it isolated; does not change the deployment.</li>
        <li><strong>3 · Publish</strong> — deploys the saved draft; callers invoke the published version.</li>
      </ol>

      <div className="app-grid">
        <div>
          <label className="app-label" htmlFor="app-name">Name</label>
          <input id="app-name" className="app-input" value={draft.name} disabled={view === 'view'} onChange={(e) => edit({ name: e.target.value })} />
          <label className="app-label" id="app-code-label">Code — main.star</label>
          <div className="app-cm" aria-labelledby="app-code-label">
            <CodeMirror value={draft.code} height="360px" extensions={[python()]} theme={cmTheme}
              readOnly={view === 'view'} onChange={(v) => edit({ code: v })} />
          </div>
        </div>
        <div>
          <label className="app-label" htmlFor="app-input">Input — a JSON object</label>
          <textarea id="app-input" className="app-area" rows={5} spellCheck={false}
            value={draft.input} onChange={(e) => edit({ input: e.target.value })} />
          <div className="app-out">
            <span className="app-label">Output</span>
            <pre className="app-out-pre" aria-live="polite">
              {run ? (run.output_json ?? (run.result.error ? `error: ${run.result.error.code} — ${run.result.error.message}` : '')) : 'Test to see output'}
            </pre>
            {outdated && <p className="app-dim">Result is from before your latest edits.</p>}
          </div>
        </div>
      </div>

      <div className="app-actions">
        <Button onClick={onSave} disabled={busy !== '' || view === 'view'}><SwapLabel show={busy === 'save' ? 'Saving…' : 'Save draft'} options={['Save draft', 'Saving…']} /></Button>
        <Button variant="primary" onClick={onTest} disabled={busy !== '' || view === 'view' || corrupt}><SwapLabel show={busy === 'test' ? 'Testing…' : 'Save & Test'} options={['Save & Test', 'Testing…']} /></Button>
        <Button variant="primary" onClick={onPublish} disabled={busy !== '' || view === 'view' || corrupt}><SwapLabel show={busy === 'publish' ? 'Publishing…' : 'Save & Publish'} options={['Save & Publish', 'Publishing…']} /></Button>
        <Button onClick={onInvoke} disabled={busy !== '' || !ready || view === 'view' || corrupt}><SwapLabel show={busy === 'invoke' ? 'Invoking…' : 'Invoke published'} options={['Invoke published', 'Invoking…']} /></Button>
        {view === 'edit' && <button className="link-btn" onClick={() => setDelOpen(true)}>Delete</button>}
      </div>
      <ApplicationCallExamples appId={app.id} input={draft.input} status={app.status} />
      <div className="app-result" role="status" aria-live="polite">
        {warn && <Notice tone="warning">{warn}</Notice>}
        {storeFailed && <Notice tone="warning">Not saved to this tab.</Notice>}
        {corrupt && (
          <Notice tone="warning">
            Saved editor state for this application is unreadable.
            <button className="link-btn" onClick={() => setResetOpen(true)}>Reset it</button>
          </Notice>
        )}
      </div>

      <details className="app-adv" open={adv} onToggle={(e) => setAdv((e.target as HTMLDetailsElement).open)}>
        <summary>Advanced — JSON schemas and runtime settings</summary>
        <p className="app-dim">By default the contract accepts any JSON object on input and returns any JSON object on output. Opt in per side only when you want a pinned schema.</p>
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
        <label className="app-label" htmlFor="app-spec">Runtime spec (JSON, secrets by vault ref only)</label>
        <textarea id="app-spec" className="app-area" rows={4} spellCheck={false} disabled={view === 'view'}
          value={draft.spec} onChange={(e) => edit({ spec: e.target.value })} />
      </details>

      {delOpen && (
        <Confirm title="Delete application" confirm="Delete" pending="Deleting…" onClose={() => setDelOpen(false)}
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
      {busy === 'delete' && <Loading />}
    </div>
  )
}
