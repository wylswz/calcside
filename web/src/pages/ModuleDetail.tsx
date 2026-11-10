import { useEffect, useRef, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import CodeMirror from '@uiw/react-codemirror'
import { api, wsPath, workspaces, type ExtensionInfo } from '../api'
import { AutosaveQueue, type AutosaveState } from '../autosave'
import { readPendingDraft, writePendingDraft, type PendingDraft } from '../pendingDraft'
import { CopyBlock } from '../components/MCP'
import { Button, Confirm, Loading, Notice, PageHeader, SectionTitle, SwapLabel } from '../components/ui'
import { PublishReviewDialog } from '../components/PublishReviewDialog'
import { SpecForm, type SpecFormHandle } from '../components/SpecForm'
import { cmTheme } from '../components/codemirror'
import { useApplicationEditor } from '../components/editor'
import { uniqueExtensionAlias, validateRawSpec, type InstanceSpec } from '../instanceDraft'
import { uniqueMountAlias } from '../moduleMounts'
import './module.css'

const EMPTY_DEPS: Record<string, { source: string; sum?: string }> = {}
type ModuleRequirements = {
  capabilities?: string[]
  ext?: Record<string, { source: string; sum?: string }>
}
type ModuleDraft = { files: Record<string, string>; requirements: ModuleRequirements | undefined }
type DebugDraft = { script: string; specText: string }
function isModuleDraft(value: unknown): value is ModuleDraft {
  if (!value || typeof value !== 'object' || !('files' in value)) return false
  const files = value.files
  return !!files && typeof files === 'object' && !Array.isArray(files) && Object.values(files).every((v) => typeof v === 'string')
}
function isDebugDraft(value: unknown): value is DebugDraft {
  if (!value || typeof value !== 'object' || !('script' in value) || typeof value.script !== 'string' ||
    !('specText' in value) || typeof value.specText !== 'string') return false
  try { return validateRawSpec(JSON.parse(value.specText), true).length === 0 } catch { return false }
}
const pendingKey = (workspace: string, id: string, kind: 'source' | 'debug') => `calcside:module:${workspace}:${id}:${kind}`
type ModuleFull = {
  requirements?: ModuleRequirements
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
  const filesRef = useRef<Record<string, string>>({})
  const [reqs, setReqs] = useState<ModuleRequirements | undefined>(undefined)
  const reqsRef = useRef<ModuleRequirements | undefined>(undefined)
  const modQ = useRef<AutosaveQueue<{ files: Record<string, string>; requirements: ModuleRequirements | undefined }> | null>(null)
  const [modState, setModState] = useState<AutosaveState<{ files: Record<string, string>; requirements: ModuleRequirements | undefined }> | null>(null)
  const [reviewOpen, setReviewOpen] = useState(false)
  const [reviewVer, setReviewVer] = useState<number | null>(null)
  const [pubBusy, setPubBusy] = useState(false)
  const [pubErr, setPubErr] = useState('')
  const [depRemove, setDepRemove] = useState<string | null>(null)
  const [confirmModReload, setConfirmModReload] = useState(false)
  const [recoveredModule, setRecoveredModule] = useState<PendingDraft<ModuleDraft> | null>(null)
  const [recoverModuleConfirm, setRecoverModuleConfirm] = useState<'restore' | 'discard' | null>(null)
  const runtimeRef = useRef<SpecFormHandle | null>(null)
  const [debugBusy, setDebugBusy] = useState(false)
  const [specText, setSpecText] = useState('{}')
  const [configError, setConfigError] = useState('')
  const [configRevision, setConfigRevision] = useState(0)
  const [debugEpoch, setDebugEpoch] = useState(0)
  const dbgPending = useRef<() => boolean>(() => false)
  const m = q.data?.module

  // Module autosave queue — serial CAS writes of {files, requirements}.
  // Ack files carry the server-pinned manifest; reconcile keeps newer
  // .star edits while adopting the pinned module.yaml.
  useEffect(() => {
    if (!m || wsq.isLoading || modQ.current) return
    setFiles({ ...m.files })
    setActive((a) => (a && a in m.files ? a : Object.keys(m.files).filter((f) => f !== 'module.yaml')[0] ?? ''))
    reqsRef.current = m.requirements
    setReqs(m.requirements)
    filesRef.current = { ...m.files }
    modQ.current = new AutosaveQueue({
      initial: { value: { files: m.files, requirements: m.requirements }, version: m.draft_version },
      fingerprint: (v) => JSON.stringify({ files: v.files, requirements: v.requirements }),
      save: async (v, ver) => {
        const r = await api.patch<{ module: ModuleFull }>(wsPath(`/api/v1/modules/${id}`), {
          draft_version: ver, files: v.files, requirements: v.requirements,
        })
        qc.setQueryData(['module', id], r)
        return { value: { files: r.module.files, requirements: r.module.requirements }, version: r.module.draft_version }
      },
      reconcile: (cur, sent, ack) => ({
        // Keep the user's current .star edits; adopt the server
        // manifest only when requirements are still what we sent.
        files: { ...cur.files, 'module.yaml': ack.files['module.yaml'] },
        requirements: JSON.stringify(cur.requirements) === JSON.stringify(sent.requirements)
          ? ack.requirements : cur.requirements,
      }),
      onChange: (st) => {
        writePendingDraft(pendingKey(m.workspace_id, id, 'source'),
          st.dirty ? { version: st.saved.version, value: st.latest } : null)
        setModState(st)
        const l = st.latest
        if (JSON.stringify(l.files) !== JSON.stringify(filesRef.current)) {
          filesRef.current = { ...l.files }
          setFiles({ ...l.files })
        }
        if (JSON.stringify(l.requirements) !== JSON.stringify(reqsRef.current)) {
          reqsRef.current = l.requirements
          setReqs(l.requirements)
        }
      },
    })
    setModState(modQ.current.snapshot)
    const pending = readPendingDraft(pendingKey(m.workspace_id, id, 'source'), isModuleDraft)
    const role = wsq.data?.find((w) => w.id === m.workspace_id)?.role
    if (pending && (role === 'owner' || role === 'editor')) {
      if (pending.version === m.draft_version) modQ.current.update(pending.value)
      else setRecoveredModule(pending)
    }
  }, [m, id, qc, wsq.data, wsq.isLoading])
  // Dispose only on unmount — never on query ack/refresh.
  useEffect(() => () => { modQ.current?.dispose(); modQ.current = null }, [])

  const pushModule = (nextFiles: Record<string, string>, nextReqs = reqsRef.current) => {
    modQ.current?.update({ files: nextFiles, requirements: nextReqs })
  }
  const addDep = (entry: ExtensionInfo) => {
    const ext = { ...(reqsRef.current?.ext ?? {}) }
    const alias = uniqueExtensionAlias(entry.name, new Set(Object.keys(ext)))
    const next: ModuleRequirements = { ...reqsRef.current, ext: { ...ext, [alias]: { source: entry.source, sum: entry.sum } } }
    reqsRef.current = next
    setReqs(next)
    pushModule(filesRef.current, next)
  }
  const switchDep = (from: ExtensionInfo | undefined, to: ExtensionInfo) => {
    const ext = { ...(reqsRef.current?.ext ?? {}) }
    const fam = (src: string) => src.split('@')[0]
    const alias = Object.keys(ext).find((a) =>
      ext[a].source === from?.source || ext[a].source === from?.family ||
      (from === undefined && ext[a].source === to.family) || fam(ext[a].source) === to.family)
    if (!alias) return
    const next: ModuleRequirements = { ...reqsRef.current, ext: { ...ext, [alias]: { source: to.source, sum: to.sum } } }
    reqsRef.current = next
    setReqs(next)
    pushModule(filesRef.current, next)
  }
  const removeDep = (alias: string) => {
    const ext = { ...(reqsRef.current?.ext ?? {}) }
    delete ext[alias]
    const next: ModuleRequirements = { ...reqsRef.current, ext }
    reqsRef.current = next
    setReqs(next)
    pushModule(filesRef.current, next)
  }
  const doModuleReload = async () => {
    const r = await api.get<{ module: ModuleFull }>(wsPath(`/api/v1/modules/${id}`))
    qc.setQueryData(['module', id], r)
    modQ.current?.reset({ value: { files: r.module.files, requirements: r.module.requirements }, version: r.module.draft_version })
    filesRef.current = { ...r.module.files }
    setFiles({ ...r.module.files })
    reqsRef.current = r.module.requirements
    setReqs(r.module.requirements)
    setConfirmModReload(false)
    writePendingDraft(pendingKey(r.module.workspace_id, id, 'source'), null)
  }

  useEffect(() => {
    const h = (e: BeforeUnloadEvent) => {
      if (modState?.dirty || modState?.saving || dbgPending.current() || configError || recoveredModule) e.preventDefault()
    }
    window.addEventListener('beforeunload', h)
    return () => window.removeEventListener('beforeunload', h)
  }, [modState?.dirty, modState?.saving, configError, recoveredModule])
  if (q.isLoading || dq.isLoading || wsq.isLoading) return <Loading />
  if (q.isError) return <Notice tone="warning">Module unavailable: {(q.error as Error).message}</Notice>
  if (!m) return null
  const wsRole = wsq.data?.find((w) => w.id === m.workspace_id)?.role ?? 'viewer'
  const canEdit = wsRole === 'owner' || wsRole === 'editor'
  const modConflict = modState?.conflict ?? false
  // Reference example: an actual .star file from the current draft
  // (main.star when present), under the same alias the pickers would
  // generate — the mount grammar is narrower than names and 'self' is
  // the debug mount, so dotted/reserved names differ here.
  const refFile = Object.keys(m.files).filter((f) => f.endsWith('.star'))
    .sort((a, b) => (a === 'main.star' ? -1 : b === 'main.star' ? 1 : a.localeCompare(b)))[0] ?? 'main.star'
  const refAlias = uniqueMountAlias(m.name, new Set())
  const refExample = `load("@${refAlias}/${refFile}", "func")`
  const saveStatus = modConflict ? "Couldn't save"
    : modState?.error ? "Couldn't save"
    : modState?.saving ? 'Saving…'
    : modState?.dirty ? 'Unsaved'
    : modState ? 'Saved' : ''

  return (
    <div className="app-page">
      <div className="module-head"><PageHeader title={m.name}
        actions={<div className="flex flex-wrap items-center gap-2">
          <span className="text-xs text-mute">draft v{m.draft_version}{m.latest_version ? ` · published v${m.latest_version}` : ''}</span>
          <Link to="/modules" className="text-xs text-sec hover:underline">← Modules</Link>
          {canEdit && saveStatus && (
            <span className="text-xs text-mute" data-testid="module-save-status">
              <SwapLabel show={saveStatus} options={['Saved', 'Saving…', 'Unsaved', "Couldn't save"] as const} />
            </span>
          )}
          {canEdit && modState?.error && (
            <Button size="sm" onClick={() => { void modQ.current?.retry().catch(() => undefined) }}>Retry</Button>
          )}
          {canEdit && (
            <Button size="sm" variant="primary" disabled={modConflict || pubBusy || debugBusy || !!recoveredModule}
              onClick={async () => {
                setPubBusy(true); setPubErr('')
                try {
                  const v = await modQ.current?.flush()
                  if (v) { setReviewVer(v.version); setReviewOpen(true) }
                } catch (e) { setPubErr((e as Error).message) }
                finally { setPubBusy(false) }
              }} data-testid="module-publish">
              <SwapLabel show={pubBusy ? 'Preparing…' : 'Publish…'} options={['Publish…', 'Preparing…'] as const} />
            </Button>
          )}
        </div>} /></div>
      {modConflict && (
        <Notice tone="warning">
          Module draft changed elsewhere.{' '}
          <button type="button" className="link" data-testid="module-reload" onClick={() => setConfirmModReload(true)}>Reload draft</button>
        </Notice>
      )}
      {recoveredModule && <Notice tone="warning">
        An unsaved local draft belongs to an older server version.{' '}
        <button type="button" className="link" onClick={() => setRecoverModuleConfirm('restore')}>Restore local edits</button>{' · '}
        <button type="button" className="link" onClick={() => setRecoverModuleConfirm('discard')}>Use server draft</button>
      </Notice>}
      {recoverModuleConfirm && <Confirm title={recoverModuleConfirm === 'restore' ? 'Restore local draft' : 'Discard local backup'}
        confirm={recoverModuleConfirm === 'restore' ? 'Restore' : 'Discard'} pending="Updating…" tone="danger"
        onClose={() => setRecoverModuleConfirm(null)} action={async () => {
          if (recoverModuleConfirm === 'restore' && recoveredModule) modQ.current?.update(recoveredModule.value)
          else writePendingDraft(pendingKey(m.workspace_id, id, 'source'), null)
          setRecoveredModule(null)
          setRecoverModuleConfirm(null)
        }}>
        {recoverModuleConfirm === 'restore'
          ? 'Replace the current server draft with your recovered local edits? Publishing still requires review.'
          : 'Discard the recovered local edits and keep the current server draft?'}
      </Confirm>}
      {confirmModReload && (
        <Confirm title="Reload module draft" tone="danger" confirm="Reload" pending="Reloading…"
          action={async () => { await doModuleReload() }}
          onClose={() => setConfirmModReload(false)}>
          Discard local edits and load the latest draft?
        </Confirm>
      )}
      {modState?.error && !modConflict && <Notice tone="warning">{modState.error.message}</Notice>}
      {pubErr && <Notice tone="warning">{pubErr}</Notice>}
      <section className="module-identity rounded border border-line bg-panel p-3">
        <p className="mb-2 text-xs text-sec">
          Name <code className="font-mono text-ink">{m.name}</code> — unique in this workspace.
          The debug run mounts this draft as <code>@self</code>; applications and instances mount a published
          version by name under a mount alias. Example with this draft's files:
        </p>
        <CopyBlock text={refExample} mono />
      </section>
      <div className="module-workbench grid gap-3 lg:grid-cols-[minmax(0,1.1fr)_minmax(0,1fr)]">
        <section className="min-w-0 rounded border border-line bg-panel p-3">
          <SectionTitle>Module source</SectionTitle>
          <div className="mb-2 flex flex-wrap gap-1" data-testid="module-files">
            {Object.keys(files).filter((f) => f !== 'module.yaml').map((f) => (
              <button key={f} onClick={() => setActive(f)}
                className={`rounded border px-2 py-0.5 text-xs ${f === active ? 'border-accent bg-accent-10' : 'border-line text-sec hover:bg-hover'}`}>
                {f}
              </button>
            ))}
          </div>
          <div data-testid="module-editor" aria-label="Module source editor" className="module-cm">
            <ModuleCodeMirror value={files[active] ?? ''} readOnly={!canEdit || modConflict || pubBusy || reviewOpen || debugBusy || !!recoveredModule} height="var(--module-source-h, 500px)"
              specText={specText} workspaceId={m.workspace_id}
              onChange={(v) => {
                const next = { ...filesRef.current, [active]: v }
                filesRef.current = next
                setFiles(next)
                pushModule(next)
              }} />
          </div>
        </section>
        <DebugWorkspace key={`dw-${debugEpoch}`} id={id} draftVersion={m.draft_version} workspaceId={m.workspace_id}
          debug={dq.data?.debug} modDirty={modState?.dirty ?? false} modConflict={modConflict}
          flushModule={() => modQ.current?.flush() ?? null}
          locked={pubBusy || reviewOpen}
          configurationError={configError}
          canEdit={canEdit} specRef={runtimeRef}
          specText={specText} configRevision={configRevision} onBusyChange={setDebugBusy}
          onEpochChange={() => setDebugEpoch((e) => e + 1)}
          onPendingChange={(f) => { dbgPending.current = f }} />
      </div>
      {depRemove && (
        <Confirm title={`Remove ${depRemove}?`} tone="danger" confirm="Remove" pending="Removing…"
          action={async () => { removeDep(depRemove); setDepRemove(null) }}
          onClose={() => setDepRemove(null)}>
          Remove the static dependency {depRemove}? Caller scripts referencing it will fail until re-added.
        </Confirm>
      )}
      <DebugRuntime key={`dr-${debugEpoch}`} id={id} workspaceId={m.workspace_id} debug={dq.data?.debug}
        canEdit={canEdit && !debugBusy && !pubBusy && !reviewOpen} specRef={runtimeRef} fixedExtensions={reqs?.ext ?? EMPTY_DEPS}
        depsDisabled={!canEdit || modConflict || pubBusy || reviewOpen || debugBusy || !!recoveredModule}
        onAddDep={addDep} onSwitchDep={switchDep} onRemoveDep={(alias) => setDepRemove(alias)}
        onSpecText={setSpecText} onValidationChange={setConfigError} onConfigChange={() => setConfigRevision((r) => r + 1)} />
      {reviewOpen && m && reviewVer !== null && (
        <PublishReviewDialog resource="modules" id={id} version={reviewVer} onClose={() => setReviewOpen(false)}
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

function DebugWorkspace({ id, draftVersion, workspaceId, debug, modDirty, modConflict, flushModule, canEdit, locked, configurationError, specRef, specText, configRevision, onBusyChange, onEpochChange, onPendingChange }: {
  id: string; draftVersion: number; workspaceId: string
  debug?: DebugView; modDirty: boolean; modConflict: boolean
  flushModule: () => { value: { files: Record<string, string> }; version: number } | null | Promise<{ value: { files: Record<string, string> }; version: number } | null>
  canEdit: boolean
  locked: boolean
  configurationError: string
  specRef: React.MutableRefObject<SpecFormHandle | null>
  specText: string
  configRevision: number
  onBusyChange: (busy: boolean) => void
  onEpochChange?: () => void
  onPendingChange?: (f: () => boolean) => void
}) {
  const qc = useQueryClient()
  const [resultRev, setResultRev] = useState<number | null>(null)
  const [script, setScript] = useState<string | null>(null)
  const [version, setVersion] = useState(0)
  const dbgQ = useRef<AutosaveQueue<{ script: string; specText: string }> | null>(null)
  const [dbgState, setDbgState] = useState<AutosaveState<{ script: string; specText: string }> | null>(null)
  const latestSpecRef = useRef('')
  const [running, setRunning] = useState(false)
  const [result, setResult] = useState<RunView | null>(null)
  const [outdated, setOutdated] = useState(false)
  const [err, setErr] = useState('')
  const [confirmReload, setConfirmReload] = useState(false)
  const [recovered, setRecovered] = useState<PendingDraft<DebugDraft> | null>(null)
  const [recoverConfirm, setRecoverConfirm] = useState<'restore' | 'discard' | null>(null)
  const recovering = useRef(false)
  const configErrorRef = useRef(configurationError)
  configErrorRef.current = configurationError
  const conflict = dbgState?.conflict ?? false

  // Initialize once per identity — empty script is a valid config.
  useEffect(() => {
    if (!debug) return
    setVersion(debug.version)
    if (!dbgQ.current) {
      const pending = readPendingDraft(pendingKey(workspaceId, id, 'debug'), isDebugDraft)
      const restore = canEdit && pending?.version === debug.version ? pending : null
      setScript(restore?.value.script ?? debug.script)
      if (pending && !restore && canEdit) setRecovered(pending)
      dbgQ.current = new AutosaveQueue({
        initial: { value: { script: debug.script, specText: debug.spec_json ?? JSON.stringify(debug.spec ?? {}) }, version: debug.version },
        initialDirty: debug.version === 0,
        fingerprint: (v) => JSON.stringify(v),
        save: async (v, ver) => {
          if (configErrorRef.current) throw new Error(configErrorRef.current)
          // Deps must exist server-side before the debug PUT.
          await flushModule()
          // Persist the SNAPSHOT specText — never the live editor state.
          const body = `{"version":${ver},"script":${JSON.stringify(v.script ?? '')},"spec":${v.specText}}`
          const r = await api.putRawJson<{ debug: DebugView }>(wsPath(`/api/v1/modules/${id}/debug`), body)
          qc.setQueryData(['module-debug', id], r)
          setVersion(r.debug.version)
          return { value: v, version: r.debug.version }
        },
        onChange: (st) => {
          setDbgState(st)
          writePendingDraft(pendingKey(workspaceId, id, 'debug'),
            st.dirty ? { version: st.saved.version, value: st.latest } : null)
        },
      })
      if (restore) dbgQ.current.update(restore.value)
    }
  }, [debug, script, id, qc, specRef, canEdit, workspaceId])
  useEffect(() => () => { dbgQ.current?.dispose(); dbgQ.current = null }, [])
  useEffect(() => {
    latestSpecRef.current = specText
    // Spec edits autosave too — queue no-ops when unchanged.
    if (script !== null && !recovering.current) dbgQ.current?.update({ script, specText })
  }, [specText, script])

  const busy = running
  useEffect(() => { onBusyChange(busy) }, [busy, onBusyChange])
  useEffect(() => { onPendingChange?.(() => (dbgState?.dirty ?? false) || (dbgState?.saving ?? false) || !!recovered) }, [dbgState, onPendingChange, recovered])

  const doReload = async () => {
    const r = await api.get<{ debug: DebugView }>(wsPath(`/api/v1/modules/${id}/debug`))
    qc.setQueryData(['module-debug', id], r)
    dbgQ.current?.reset({ value: { script: r.debug.script, specText: latestSpecRef.current }, version: r.debug.version })
    setVersion(r.debug.version)
    setScript(r.debug.script)
    setConfirmReload(false)
    writePendingDraft(pendingKey(workspaceId, id, 'debug'), null)
    onEpochChange?.()
  }

  const run = async () => {
    if (running || locked || conflict || modConflict) return
    setRunning(true)
    setErr('')
    try {
      let dv = version
      let mvVer = draftVersion
      if (canEdit) {
        const mv = await flushModule()
        if (mv === null) { setErr('Module save conflict — reload the draft first.'); setRunning(false); return }
        mvVer = mv.version
        if (dbgQ.current) {
          const built = specRef.current?.buildSpec()
          if (built?.err) { setErr(built.err); setRunning(false); return }
          const res = await dbgQ.current.flush()
          dv = res.version
        }
      }
      const r = await api.post<RunView>(wsPath(`/api/v1/modules/${id}/debug/run`), {
        draft_version: mvVer, debug_version: dv,
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
        <SectionTitle aside={canEdit && <div className="flex items-center gap-2">
          {dbgState?.saving && <span className="text-xs text-mute">Saving…</span>}
          {dbgState?.error && !conflict && <span className="text-xs text-error">Couldn't save</span>}
          <Button size="sm" variant="primary" disabled={running || locked || conflict || modConflict}
            onClick={() => void run()} data-testid="debug-run">
            <SwapLabel show={running ? 'Running…' : 'Run'} options={['Run', 'Running…'] as const} />
          </Button>
        </div>}>
          Caller script
        </SectionTitle>
        {modDirty && <Notice tone="neutral">Module edits are autosaving. Run waits for the latest changes.</Notice>}
        {dbgState?.error && !conflict && <Notice tone="warning">
          {dbgState.error.message}{' '}
          <button type="button" className="link" onClick={() => { void dbgQ.current?.retry().catch(() => {}) }}>Retry</button>
        </Notice>}
        {conflict && (
          <Notice tone="warning">
            Debug settings changed elsewhere.{' '}
            <button type="button" className="link" data-testid="debug-reload" onClick={() => setConfirmReload(true)}>Reload settings</button>
          </Notice>
        )}
        {recovered && <Notice tone="warning">
          A local debug draft belongs to an older server version.{' '}
          <button type="button" className="link" onClick={() => setRecoverConfirm('restore')}>Restore local settings</button>{' · '}
          <button type="button" className="link" onClick={() => setRecoverConfirm('discard')}>Use server settings</button>
        </Notice>}
        {recoverConfirm && <Confirm title={recoverConfirm === 'restore' ? 'Restore local settings' : 'Discard local debug backup'}
          confirm={recoverConfirm === 'restore' ? 'Restore' : 'Discard'} pending="Updating…" tone="danger"
          onClose={() => setRecoverConfirm(null)} action={async () => {
            recovering.current = true
            try {
              if (recoverConfirm === 'restore' && recovered) {
                dbgQ.current?.update(recovered.value)
                await dbgQ.current?.flush()
              }
              writePendingDraft(pendingKey(workspaceId, id, 'debug'), null)
              setRecovered(null)
              setRecoverConfirm(null)
              if (recoverConfirm === 'restore') onEpochChange?.()
            } finally { recovering.current = false }
          }}>
          {recoverConfirm === 'restore' ? 'Replace the saved debug settings with your recovered local edits?' : 'Discard the recovered debug script and settings?'}
        </Confirm>}
        {confirmReload && (
          <Confirm title="Reload debug settings" tone="danger" confirm="Reload" pending="Reloading…"
            action={async () => { await doReload() }}
            onClose={() => setConfirmReload(false)}>
            Discard local debug script/config changes and load the latest saved settings? Your module source draft is unaffected.
          </Confirm>
        )}
        {err && <Notice tone="warning">{err}</Notice>}
        <div className="mt-2" data-testid="debug-script" aria-label="Caller script editor">
          <ModuleCodeMirror value={scriptText} readOnly={!canEdit || running || locked || conflict || !!recovered}
            height="260px" specText={specText} workspaceId={workspaceId}
            onChange={(v) => { setScript(v); if (result) setOutdated(true); dbgQ.current?.update({ script: v, specText: latestSpecRef.current }) }} />
        </div>
      </div>
      <div className="rounded border border-line bg-panel p-3" data-testid="debug-result" style={{ minHeight: 200 }}>
        <SectionTitle aside={canEdit && result && (
          <Button size="sm" disabled={running} onClick={() => { setResult(null); setErr(''); setOutdated(false) }}>Clear output</Button>
        )}>stdout</SectionTitle>
        {!result && !err && <p className="text-xs text-mute">Run the script to see output.</p>}
        {result && (
          <>
            {(outdated || modDirty || (resultRev !== null && resultRev !== configRevision) ||
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
// It also owns the static dependency picker: adds/removes go through
// onAddDep/onRemoveDep (module requirements CAS), while the selected
// summary's Configure rows edit the debug-only environment.
function DebugRuntime({ id, workspaceId, debug, canEdit, specRef, fixedExtensions, depsDisabled, onAddDep, onSwitchDep, onRemoveDep, onSpecText, onValidationChange, onConfigChange }: {
  id: string; workspaceId: string; debug?: DebugView; canEdit: boolean
  specRef: React.MutableRefObject<SpecFormHandle | null>
  fixedExtensions?: Record<string, { source: string; sum?: string }>
  /** locks dep add/remove only — runtime config stays editable (e.g. while a source save conflicts) */
  depsDisabled?: boolean
  onAddDep: (entry: ExtensionInfo) => void
  onSwitchDep: (from: ExtensionInfo | undefined, to: ExtensionInfo) => void
  onRemoveDep: (alias: string) => void
  onSpecText: (text: string) => void
  onValidationChange: (error: string) => void
  onConfigChange: () => void
}) {
  // Fingerprint stable drafts — only real config edits bump the
  // revision (which marks any displayed result outdated).
  const lastFp = useRef('')
  const first = useRef(true)
  const edited = useRef(false)
  const wasInvalid = useRef(false)
  const hydrated = useRef(false)
  const [incomplete, setIncomplete] = useState(false)
  const [seed] = useState(() => {
    const pending = readPendingDraft(pendingKey(workspaceId, id, 'debug'), isDebugDraft)
    const raw = pending && pending.version === debug?.version ? pending.value.specText
      : debug?.spec_json ?? JSON.stringify(debug?.spec ?? {})
    return { raw, spec: JSON.parse(raw) as InstanceSpec }
  })
  useEffect(() => {
    if (debug && !hydrated.current) {
      hydrated.current = true
      onSpecText(seed.raw)
    }
  }, [debug, onSpecText, seed])
  if (!debug) return null
  return (
    <section className="mt-3 rounded border border-line bg-panel p-3" data-testid="module-deps">
      <SectionTitle>Extensions & runtime</SectionTitle>
      <p className="mb-2 text-xs text-mute">Dependencies publish with the module (source and sum only); settings and credentials below are debug-only.</p>
      {incomplete && <Notice tone="neutral">Debug configuration incomplete. Fix the indicated fields before running; module publishing is unaffected.</Notice>}
      <fieldset disabled={!canEdit} className="m-0 min-w-0 border-0 p-0">
        <SpecForm ref={specRef} debug hideBasics workspaceId={workspaceId} fixedExtensions={fixedExtensions}
          onAddFixedExtension={onAddDep} onSwitchFixedExtension={onSwitchDep} onRemoveFixedExtension={onRemoveDep} fixedDepsDisabled={depsDisabled}
          initial={seed.spec}
          onSpecChange={(draft, errors, built) => {
            const invalid = (errors?.length ?? 0) > 0 || !built?.spec
            const becameValid = wasInvalid.current && !invalid
            wasInvalid.current = invalid
            setIncomplete(invalid)
            onValidationChange(invalid ? 'Debug configuration incomplete' : '')
            const fp = JSON.stringify([draft, built?.specJSON])
            if (first.current) { first.current = false; lastFp.current = fp; return }
            const changed = fp !== lastFp.current
            if (changed) { edited.current = true; lastFp.current = fp; onConfigChange() }
            // Keep the original raw JSON until configuration actually changes;
            // caller-only edits must not normalize or round unrelated fields.
            if (!invalid && edited.current && (changed || becameValid)) {
              onSpecText(built.specJSON ?? JSON.stringify(built.spec))
            }
          }} />
      </fieldset>
    </section>
  )
}
