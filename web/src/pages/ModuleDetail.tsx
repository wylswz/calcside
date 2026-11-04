import { useEffect, useRef, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import CodeMirror from '@uiw/react-codemirror'
import { api, wsPath, workspaces, ApiError } from '../api'
import { Button, Confirm, Loading, Notice, PageHeader, SectionTitle, SwapLabel } from '../components/ui'
import { PublishReviewDialog } from '../components/PublishReviewDialog'
import { SpecForm, type SpecFormHandle } from '../components/SpecForm'
import { cmTheme } from '../components/codemirror'
import { useApplicationEditor } from '../components/editor'
import type { InstanceSpec } from '../instanceDraft'
import './module.css'

type ModuleFull = {
  id: string; workspace_id: string; name: string; description?: string
  draft_version: number; latest_version: number; latest_sum?: string
  files: Record<string, string>
}
type DebugView = { version: number; script: string; spec?: InstanceSpec; spec_json?: string; updated_at?: string }
type ExecResult = { output?: string; error?: { message?: string } | null }
type RunView = { result?: ExecResult & { duration_ms?: number; steps?: number }; instance_id?: string; module_sum?: string; draft_version?: number }

// Outer component keyed by module id: navigation between modules
// discards all editor state (no cross-module leakage).
export default function ModuleDetail() {
  const { id = '' } = useParams()
  return <ModuleEditor key={id} id={id} />
}

function ModuleEditor({ id }: { id: string }) {
  const qc = useQueryClient()
  const wsq = useQuery({ queryKey: ['workspaces'], queryFn: workspaces })
  const q = useQuery({
    queryKey: ['module', id],
    queryFn: () => api.get<{ module: ModuleFull }>(wsPath(`/api/v1/modules/${id}`)),
  })
  const dq = useQuery({
    queryKey: ['module-debug', id],
    queryFn: () => api.get<{ debug: DebugView }>(wsPath(`/api/v1/modules/${id}/debug`)),
    retry: false,
  })
  const [files, setFiles] = useState<Record<string, string>>({})
  const [active, setActive] = useState('')
  const [dirty, setDirty] = useState(false)
  const filesRef = useRef<Record<string, string>>({})
  const [saving, setSaving] = useState(false)
  const [saveErr, setSaveErr] = useState('')
  const [reviewOpen, setReviewOpen] = useState(false)
  const runtimeRef = useRef<SpecFormHandle | null>(null)
  const [debugBusy, setDebugBusy] = useState(false)
  const [specText, setSpecText] = useState('')
  const [configRevision, setConfigRevision] = useState(0)
  const [debugEpoch, setDebugEpoch] = useState(0)
  const m = q.data?.module

  useEffect(() => {
    if (m && !dirty) {
      setFiles({ ...m.files })
      setActive((a) => (a && a in m.files ? a : Object.keys(m.files)[0] ?? ''))
    }
  }, [m, dirty])

  const save = async () => {
    if (!m || saving) return
    setSaving(true)
    setSaveErr('')
    const snapshot = { ...files }
    try {
      const r = await api.patch<{ module: ModuleFull }>(wsPath(`/api/v1/modules/${id}`), {
        draft_version: m.draft_version, files: snapshot,
      })
      qc.setQueryData(['module', id], r)
      if (JSON.stringify(filesRef.current) === JSON.stringify(snapshot)) {
        filesRef.current = { ...r.module.files }
        setFiles({ ...r.module.files })
        setDirty(false)
      }
    } catch (e) {
      setSaveErr((e as Error).message)
    } finally {
      setSaving(false)
    }
  }

  if (q.isLoading || dq.isLoading) return <Loading />
  if (q.isError) return <Notice tone="warning">Module unavailable: {(q.error as Error).message}</Notice>
  if (!m) return null
  const wsRole = wsq.data?.find((w) => w.id === m.workspace_id)?.role ?? 'viewer'
  const canEdit = wsRole === 'owner' || wsRole === 'editor'

  return (
    <div className="app-page">
      <div className="module-head"><PageHeader title={m.name}
        actions={<div className="flex flex-wrap items-center gap-2">
          <span className="text-xs text-mute">draft v{m.draft_version}{m.latest_version ? ` · published v${m.latest_version}` : ''}</span>
          <Link to="/modules" className="text-xs text-sec hover:underline">← Modules</Link>
          {canEdit && (
            <Button size="sm" variant="primary" disabled={saving || !dirty}
              onClick={() => void save()} data-testid="module-save">
              <SwapLabel show={saving ? 'Saving…' : 'Save draft'} options={['Save draft', 'Saving…'] as const} />
            </Button>
          )}
          {canEdit && (
            <Button size="sm" variant="primary" disabled={dirty}
              title={dirty ? 'Save the draft first' : undefined}
              onClick={() => setReviewOpen(true)} data-testid="module-publish">
              Publish…
            </Button>
          )}
        </div>} /></div>
      {dirty && <Notice tone="neutral">Unsaved draft changes — save before publishing or running.</Notice>}
      {saveErr && <Notice tone="warning">{saveErr}</Notice>}
      <div className="module-workbench grid gap-3 lg:grid-cols-[minmax(0,1.1fr)_minmax(0,1fr)]">
        <section className="min-w-0 rounded border border-line bg-panel p-3">
          <SectionTitle>Module source</SectionTitle>
          <div className="mb-2 flex flex-wrap gap-1" data-testid="module-files">
            {Object.keys(files).map((f) => (
              <button key={f} onClick={() => setActive(f)}
                className={`rounded border px-2 py-0.5 text-xs ${f === active ? 'border-accent bg-accent-10' : 'border-line text-sec hover:bg-hover'}`}>
                {f}
              </button>
            ))}
          </div>
          <div data-testid="module-editor" aria-label="Module source editor" className="module-cm">
            <ModuleCodeMirror value={files[active] ?? ''} readOnly={!canEdit} height="var(--module-source-h, 500px)"
              specText={specText} workspaceId={m.workspace_id}
              onChange={(v) => {
                setFiles((f) => { const next = { ...f, [active]: v }; filesRef.current = next; return next })
                setDirty(true)
              }} />
          </div>
        </section>
        <DebugWorkspace key={`dw-${debugEpoch}`} id={id} draftVersion={m.draft_version} workspaceId={m.workspace_id}
          debug={dq.data?.debug} sourceDirty={dirty} canEdit={canEdit} specRef={runtimeRef}
          specText={specText} configRevision={configRevision} onBusyChange={setDebugBusy}
          onEpochChange={() => setDebugEpoch((e) => e + 1)} />
      </div>
      <DebugRuntime key={`dr-${debugEpoch}`} id={id} workspaceId={m.workspace_id} debug={dq.data?.debug}
        canEdit={canEdit && !debugBusy} specRef={runtimeRef}
        onSpecText={setSpecText} onConfigChange={() => setConfigRevision((r) => r + 1)} />
      {reviewOpen && m && !dirty && (
        <PublishReviewDialog resource="modules" id={id} version={m.draft_version} onClose={() => setReviewOpen(false)}
          onPublished={() => { setReviewOpen(false); void qc.invalidateQueries({ queryKey: ['module', id] }); void qc.invalidateQueries({ queryKey: ['modules'] }) }} />
      )}
    </div>
  )
}

function ModuleCodeMirror({ value, readOnly, height, workspaceId, specText, onChange }: {
  value: string; readOnly: boolean; height: string; workspaceId: string
  /** current debug spec JSON — feeds ext/completion context */
  specText: string
  onChange: (v: string) => void
}) {
  const editor = useApplicationEditor(specText, workspaceId)
  return <CodeMirror value={value} height={height} extensions={editor.extensions}
    theme={cmTheme} readOnly={readOnly} onChange={onChange} />
}

function DebugWorkspace({ id, draftVersion, workspaceId, debug, sourceDirty, canEdit, specRef, specText, configRevision, onBusyChange, onEpochChange }: {
  id: string; draftVersion: number; workspaceId: string
  debug?: DebugView; sourceDirty: boolean; canEdit: boolean
  specRef: React.MutableRefObject<SpecFormHandle | null>
  specText: string
  configRevision: number
  onBusyChange: (busy: boolean) => void
  onEpochChange?: () => void
}) {
  const qc = useQueryClient()
  const [resultRev, setResultRev] = useState<number | null>(null)
  const [script, setScript] = useState<string | null>(null)
  const [version, setVersion] = useState(0)
  const [saving, setSaving] = useState(false)
  const [running, setRunning] = useState(false)
  const [result, setResult] = useState<RunView | null>(null)
  const [outdated, setOutdated] = useState(false)
  const [err, setErr] = useState('')
  const [conflict, setConflict] = useState(false)
  const [confirmReload, setConfirmReload] = useState(false)

  // Initialize once per identity — empty script is a valid config.
  useEffect(() => {
    if (debug && script === null) {
      setVersion(debug.version)
      setScript(debug.script)
    }
  }, [debug, script])

  const busy = saving || running
  useEffect(() => { onBusyChange(busy) }, [busy, onBusyChange])

  const saveSettings = async (): Promise<number | null> => {
    if (!specRef.current) { setErr('Configuration still loading — try again.'); return null }
    const built = specRef.current.buildSpec()
    if (built?.err) { setErr(built.err); return null }
    setSaving(true)
    setErr('')
    try {
      const rawSpec = built?.specJSON ?? JSON.stringify(built?.spec ?? {})
      const body = `{"version":${version},"script":${JSON.stringify(script ?? '')},"spec":${rawSpec}}`
      let r: { debug: DebugView }
      try {
        r = await api.putRawJson<{ debug: DebugView }>(wsPath(`/api/v1/modules/${id}/debug`), body)
      } catch (e) {
        // CAS conflict: the settings changed elsewhere. Never
        // silently overwrite — block until explicit reload.
        if (e instanceof ApiError && e.status === 409) {
          setConflict(true)
          return null
        }
        throw e
      }
      qc.setQueryData(['module-debug', id], r)
      setVersion(r.debug.version)
      return r.debug.version
    } catch (e) {
      setErr((e as Error).message)
      return null
    } finally {
      setSaving(false)
    }
  }

  const doReload = async () => {
    const r = await api.get<{ debug: DebugView }>(wsPath(`/api/v1/modules/${id}/debug`))
    qc.setQueryData(['module-debug', id], r)
    setVersion(r.debug.version)
    setScript(r.debug.script)
    setConflict(false)
    setConfirmReload(false)
    onEpochChange?.()
  }

  const run = async () => {
    if (running || saving || conflict) return
    setRunning(true)
    setErr('')
    try {
      const v = canEdit ? await saveSettings() : version
      if (v === null) { setRunning(false); return }
      const r = await api.post<RunView>(wsPath(`/api/v1/modules/${id}/debug/run`), {
        draft_version: draftVersion, debug_version: v,
      })
      setResult(r)
      setResultRev(configRevision)
      setOutdated(false)
    } catch (e) {
      setErr((e as Error).message)
    } finally {
      setRunning(false)
    }
  }

  if (!debug && script === null) return <Notice tone="warning">Debug settings unavailable.</Notice>
  const scriptText = script ?? debug?.script ?? ''
  return (
    <section className="flex min-w-0 flex-col gap-3">
      <div className="rounded border border-line bg-panel p-3">
        <SectionTitle aside={canEdit && <div className="flex gap-2">
          <Button size="sm" disabled={saving || running || conflict} onClick={() => void saveSettings()}>
            <SwapLabel show={saving ? 'Saving…' : 'Save settings'} options={['Save settings', 'Saving…'] as const} />
          </Button>
          <Button size="sm" variant="primary" disabled={running || saving || sourceDirty || conflict}
            title={sourceDirty ? 'Save the draft first' : undefined}
            onClick={() => void run()} data-testid="debug-run">
            <SwapLabel show={running ? 'Running…' : 'Run'} options={['Run', 'Running…'] as const} />
          </Button>
        </div>}>
          Caller script
        </SectionTitle>
        {sourceDirty && <Notice tone="warning">Save the draft first — run always uses the saved source.</Notice>}
        {conflict && (
          <Notice tone="warning">
            Debug settings changed elsewhere.{' '}
            <button type="button" className="link" data-testid="debug-reload" onClick={() => setConfirmReload(true)}>Reload settings</button>
          </Notice>
        )}
        {confirmReload && (
          <Confirm title="Reload debug settings" tone="danger" confirm="Reload" pending="Reloading…"
            action={async () => { await doReload() }}
            onClose={() => setConfirmReload(false)}>
            Discard local debug script/config changes and load the latest saved settings? Your module source draft is unaffected.
          </Confirm>
        )}
        {err && <Notice tone="warning">{err}</Notice>}
        <div className="mt-2" data-testid="debug-script" aria-label="Caller script editor">
          <ModuleCodeMirror value={scriptText} readOnly={!canEdit || saving || running}
            height="260px" specText={specText} workspaceId={workspaceId}
            onChange={(v) => { setScript(v); if (result) setOutdated(true) }} />
        </div>
      </div>
      <div className="rounded border border-line bg-panel p-3" data-testid="debug-result" style={{ minHeight: 200 }}>
        <SectionTitle aside={canEdit && result && (
          <Button size="sm" disabled={saving || running} onClick={() => { setResult(null); setErr(''); setOutdated(false) }}>Clear output</Button>
        )}>stdout</SectionTitle>
        {!result && !err && <p className="text-xs text-mute">Run the script to see output.</p>}
        {result && (
          <>
            {(outdated || sourceDirty || (resultRev !== null && resultRev !== configRevision) ||
              (result.draft_version !== undefined && result.draft_version !== draftVersion)) && (
              <Notice tone="neutral">Edits made after this run — result is outdated.</Notice>
            )}
            {result.result?.error?.message && <Notice tone="warning">{result.result.error.message}</Notice>}
            {result.result?.output && <pre className="mt-2 whitespace-pre-wrap break-words font-mono text-xs">{result.result.output}</pre>}
            <div className="mt-2 flex flex-wrap items-center gap-2 text-xs text-mute">
              {result.instance_id && <Link to={`/audit?instance_id=${result.instance_id}`} className="link">audit {result.instance_id}</Link>}
              {result.result?.duration_ms !== undefined && <>· {result.result.duration_ms}ms</>}
              {result.result?.steps !== undefined && <> · {result.result.steps} steps</>}
              {result.module_sum && <> · {result.module_sum.slice(0, 24)}…</>}
            </div>
          </>
        )}
      </div>
    </section>
  )
}

// DebugRuntime renders the full-width extensions/runtime form below
// the source+script grid; saving builds through SpecForm.buildSpec.
function DebugRuntime({ workspaceId, debug, canEdit, specRef, onSpecText, onConfigChange }: {
  id: string; workspaceId: string; debug?: DebugView; canEdit: boolean
  specRef: React.MutableRefObject<SpecFormHandle | null>
  onSpecText: (text: string) => void
  onConfigChange: () => void
}) {
  // Fingerprint stable drafts — only real config edits bump the
  // revision (which marks any displayed result outdated).
  const lastFp = useRef('')
  const first = useRef(true)
  useEffect(() => {
    if (debug) onSpecText(debug.spec_json ?? JSON.stringify(debug.spec ?? {}, null, 2))
    // once per settings load
  }, [debug?.version]) // eslint-disable-line react-hooks/exhaustive-deps
  if (!debug) return null
  return (
    <section className="mt-3 rounded border border-line bg-panel p-3">
      <SectionTitle>Extensions & runtime</SectionTitle>
      <p className="mb-2 text-xs text-mute">Debug settings are not published with the module.</p>
      <fieldset disabled={!canEdit}>
        <SpecForm ref={specRef} debug hideBasics workspaceId={workspaceId}
          initial={debug.spec ?? {}}
          onSpecChange={(draft, _errors, built) => {
            const text = built?.specJSON ?? JSON.stringify(built?.spec ?? {}, null, 2)
            onSpecText(text)
            const fp = JSON.stringify(draft)
            if (first.current) { first.current = false; lastFp.current = fp; return }
            if (fp !== lastFp.current) { lastFp.current = fp; onConfigChange() }
          }} />
      </fieldset>
    </section>
  )
}
