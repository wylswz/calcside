import { useEffect, useMemo, useRef, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api, Instance, User } from '../api'
import { instanceName } from '../activity'
import { Button, Notice, PageHeader, SectionTitle, SwapLabel, Icon, Confirm, inputCls } from '../components/ui'
import ContractEditor from '../components/ContractEditor'
import {
  BuildDraft, FileEntry, FileRole, draftKey, emptyDraft, applyPick,
  checkLimits, portablePath, exportDraft, parseSavedDraft,
} from '../buildDraft'
import './build-instance.css'

// sessionMirror keeps drafts for the tab's session even when
// sessionStorage is unavailable — one entry per scope key.
const sessionMirror = new Map<string, BuildDraft>()

export default function BuildInstance() {
  const { id = '' } = useParams()
  const qc = useQueryClient()
  // Same queryFn + data shape as InstanceDetail — a cold visit or
  // navigation from detail must resolve identically.
  const inst = useQuery({
    queryKey: ['instance', id],
    queryFn: () => api.get<{ instance: Instance }>(
      `/api/v1/instances/${encodeURIComponent(id)}`).then((r) => r.instance),
    retry: false,
  })
  const user = qc.getQueryData<{ user: User }>(['me'])?.user

  if (inst.isLoading) return <div className="page build-page">Loading…</div>
  if (inst.isError || !inst.data) {
    return <div className="page build-page"><Notice tone="warning">Source instance not found.</Notice></div>
  }
  // Anonymous/dev principals have no user row — scope the draft under
  // an empty user id rather than blocking.
  const key = draftKey(user?.id ?? '', inst.data.workspace_id ?? '', id)
  return <BuildDraftEditor key={key} scopeKey={key} inst={inst.data} />
}

function BuildDraftEditor({ scopeKey, inst }: {
  scopeKey: string; inst: Instance
}) {


  // Draft init: mirror -> storage -> empty. A malformed stored blob
  // is preserved (never overwritten) and blocks editing until reset.
  const [init] = useState<{ draft: BuildDraft; corrupt: boolean }>(() => {
    const mirrored = sessionMirror.get(scopeKey)
    if (mirrored) return { draft: mirrored, corrupt: false }
    try {
      const raw = sessionStorage.getItem(scopeKey)
      if (raw != null) {
        const parsed = parseSavedDraft(raw)
        if (parsed) return { draft: parsed, corrupt: false }
        return { draft: emptyDraft(), corrupt: true } // preserved blob blocks editing
      }
    } catch { /* storage unavailable */ }
    return { draft: emptyDraft(), corrupt: false }
  })
  const [draft, setDraft] = useState<BuildDraft>(init.draft)
  const [path, setPath] = useState(init.draft.directory || '/work')
  function nav(p: string) { setPath(p); setDraft((d) => ({ ...d, directory: p })) }

  const files = useQuery({
    queryKey: ['files', inst.id, path],
    queryFn: () => api.get<{ entries: FileEntry[] }>(
      `/api/v1/instances/${encodeURIComponent(inst.id)}/files?list_only=true&path=${encodeURIComponent(path)}`),
    retry: false,
    enabled: inst.status === 'running',
  })

  const [corrupt, setCorrupt] = useState(init.corrupt)
  const [warn, setWarn] = useState('')
  const [storageFailed, setStorageFailed] = useState(false)
  const [validated, setValidated] = useState(false)
  const [resetConfirm, setResetConfirm] = useState(false)

  // latest is updated every render so async callbacks compare against
  // the real current draft, never a captured closure.
  const latest = useRef(draft)
  latest.current = draft
  const mounted = useRef(true)
  useEffect(() => { mounted.current = true; return () => { mounted.current = false } }, [])

  // Persist on every edit — mirror always, storage best-effort.
  useEffect(() => {
    if (corrupt) return
    sessionMirror.set(scopeKey, draft)
    try {
      sessionStorage.setItem(scopeKey, JSON.stringify(draft))
      setStorageFailed(false)
    } catch {
      setStorageFailed(true)
    }
  }, [scopeKey, draft, corrupt])
  // validated is invalidated by schema edits, tracked separately from
  // the persist effect so a successful validation stays visible.
  const schemasFingerprint = draft.input.schema + ' ' + draft.output.schema
  const lastValidated = useRef('')
  useEffect(() => {
    if (schemasFingerprint !== lastValidated.current) setValidated(false)
  }, [schemasFingerprint])

  useEffect(() => {
    const guard = (e: BeforeUnloadEvent) => {
      if (storageFailed) { e.preventDefault(); e.returnValue = '' }
    }
    window.addEventListener('beforeunload', guard)
    return () => window.removeEventListener('beforeunload', guard)
  }, [storageFailed])

  const limits = useMemo(() => checkLimits(draft), [draft])
  const unknownSize = draft.sources.concat(draft.assets).some((f) => f.size == null)

  function roleOf(p: string): FileRole {
    if (draft.sources.some((f) => f.path === p)) return 'source'
    if (draft.assets.some((f) => f.path === p)) return 'asset'
    return 'none'
  }
  function pick(f: FileEntry, role: FileRole) {
    const p = portablePath(f.path)
    if (p == null) { setWarn('Path is not portable.'); return }
    setDraft((d) => applyPick(d, p, f.size ?? null, role))
  }

  const [busy, setBusy] = useState<'idle' | 'val' | 'dl'>('idle')
  const reqRef = useRef(0)

  async function validateAll(): Promise<{ inSchema: string; outSchema: string } | null> {
    const snapshot = latest.current
    const fingerprint = JSON.stringify(snapshot)
    const id = ++reqRef.current
    setBusy('val')
    try {
      const [a, b] = await Promise.all([
        api.post<{ schema: string }>('/api/v1/editor/contracts/validate', { schema: snapshot.input.schema }),
        api.post<{ schema: string }>('/api/v1/editor/contracts/validate', { schema: snapshot.output.schema }),
      ])
      setBusy('idle')
      // ANY draft change (files, name, samples, schema) or superseded
      // request: no write, no success claim.
      if (mounted.current && reqRef.current === id &&
        JSON.stringify(latest.current) === fingerprint) {
        latest.current = { ...latest.current,
          input: { ...latest.current.input, schema: a.schema },
          output: { ...latest.current.output, schema: b.schema } }
        setDraft(latest.current)
        lastValidated.current = a.schema + ' ' + b.schema
        setValidated(true)
        setWarn('')
        return { inSchema: a.schema, outSchema: b.schema }
      }
      setWarn('Changed while validating; validate again.')
      return null
    } catch {
      setBusy('idle')
      setWarn('Contract validation failed.')
      return null
    }
  }

  async function download() {
    if (draft.sources.length === 0 || !entryInSources || limits.length > 0) return
    const ok = await validateAll()
    if (!ok) return
    // Constraints rechecked against the validated snapshot — latest is
    // guaranteed identical to snapshot here by the fingerprint guard.
    if (checkLimits(latest.current).length > 0 ||
      latest.current.sources.length === 0 ||
      !latest.current.sources.some((f) => f.path === latest.current.entrypoint)) return
    // Serialize the RETURNED canonical schemas, not the stale draft —
    // spliced raw so no numeric round-trip.
    const snap: BuildDraft = { ...latest.current,
      input: { ...latest.current.input, schema: ok.inSchema },
      output: { ...latest.current.output, schema: ok.outSchema } }
    const text = exportDraft(snap, inst.id, inst.workspace_id ?? '')
    const blob = new Blob([text], { type: 'application/json' })
    const a = document.createElement('a')
    a.href = URL.createObjectURL(blob)
    a.download = `${latest.current.name || 'build-draft'}.json`
    a.click()
    URL.revokeObjectURL(a.href)
  }

  const name = instanceName(inst)
  const entryInSources = draft.sources.some((f) => f.path === draft.entrypoint)

  if (corrupt) {
    return (
      <div className="page build-page">
        <div className="build-header"><PageHeader title="Build a persistent instance">
          <span className="build-eyebrow">BUILD / CONTRACT DRAFT</span>
          <span className="build-breadcrumb"><Link to={`/instances/${inst.id}`} className="link-btn">← {name}</Link></span>
        </PageHeader></div>
        <Notice tone="warning">
          A saved draft exists but could not be read. It is preserved.
          Reset to start over.
        </Notice>
        {resetConfirm ? (
          <Confirm title="Reset draft?"
            confirm="Reset" pending="Resetting"
            action={async () => {
              try { sessionStorage.removeItem(scopeKey) } catch { /* best effort */ }
              sessionMirror.delete(scopeKey)
              setDraft(emptyDraft()); setCorrupt(false)
            }}
            onClose={() => setResetConfirm(false)}>
            The unreadable saved draft will be discarded.
          </Confirm>
        ) : (
          <Button variant="primary" onClick={() => setResetConfirm(true)}>Reset draft</Button>
        )}
      </div>
    )
  }

  return (
    <div className="page build-page">
      <div className="build-header"><PageHeader title="Build a persistent instance">
        <span className="build-eyebrow">BUILD / CONTRACT DRAFT — <span className="build-src-id">{inst.id}</span></span>
        <span className="build-breadcrumb"><Link to={`/instances/${inst.id}`} className="link-btn">← {name}</Link></span>
      </PageHeader></div>
      <Notice tone="neutral">
        Not published. Publishing is not connected yet. This page prepares a build
        draft; it does not create a release.
      </Notice>


      <div className="build-grid">
        <aside className="build-source">
          <SectionTitle>01 Files</SectionTitle>
          <div className="build-crumbs">
            <button className="link-btn" onClick={() => nav('/work')}>/work</button>
            {path !== '/work' && (
              <button className="link-btn" onClick={() => nav(path.replace(/\/[^/]*$/, '') || '/work')}>..</button>
            )}
            <button className="link-btn" onClick={() => files.refetch()}>refresh</button>
          </div>
          {files.isError && <Notice tone="warning">Source files unavailable; the draft is kept.</Notice>}
          <ul className="build-file-list">
            {(files.data?.entries ?? []).map((f) => {
              const rel = portablePath(f.path)
              return (
                <li key={f.path}>
                  {f.is_dir ? (
                    <button className="link-btn" onClick={() => nav(f.path)}>
                      <Icon name="folder" /> {f.name}
                    </button>
                  ) : (
                    <label className="build-file-row">
                      <span className="font-mono" title={f.name}>{f.name}</span>
                      <select aria-label={`Include ${f.name} as`}
                        value={rel != null ? roleOf(rel) : 'none'}
                        onChange={(e) => pick(f, e.target.value as FileRole)}>
                        <option value="none">not included</option>
                        {f.name.endsWith('.star') && <option value="source">source</option>}
                        <option value="asset">asset</option>
                      </select>
                    </label>
                  )}
                </li>
              )
            })}
          </ul>
          {unknownSize && <Notice tone="warning">Some file sizes are unknown; checked at publish.</Notice>}
          <label className="build-label">Name
            <input className={inputCls} value={draft.name}
              onChange={(e) => setDraft({ ...draft, name: e.target.value })} />
          </label>
          <label className="build-label">Entrypoint
            <select className={inputCls} value={draft.entrypoint}
              onChange={(e) => setDraft({ ...draft, entrypoint: e.target.value })}>
              {draft.sources.map((f) => <option key={f.path} value={f.path}>{f.path}</option>)}
              {!entryInSources && draft.entrypoint && (
                <option value={draft.entrypoint} disabled>{draft.entrypoint} (not selected)</option>
              )}
              {!draft.entrypoint && <option value="">—</option>}
            </select>
          </label>
          {!entryInSources && draft.sources.length > 0 && (
            <Notice tone="warning">Entrypoint is not among the selected sources.</Notice>
          )}
          {limits.map((e) => <Notice key={e.field} tone="warning">{e.field}: {e.message}</Notice>)}
        </aside>

        <section className="build-contract">
          <SectionTitle>02 Contract</SectionTitle>
          <div className="build-contract-grid">
            <ContractEditor label="Input" side={draft.input}
              scopeAlive={() => mounted.current}
              onChange={(s) => setDraft((d) => ({ ...d, input: s }))} />
            <ContractEditor label="Output" side={draft.output}
              scopeAlive={() => mounted.current}
              onChange={(s) => setDraft((d) => ({ ...d, output: s }))} />
          </div>

          <SectionTitle>03 Review</SectionTitle>
          <dl className="build-meta">
            <dt>Sources</dt><dd>{draft.sources.length} / 64</dd>
            <dt>Assets</dt><dd>{draft.assets.length}</dd>
            <dt>Entrypoint</dt><dd className="font-mono">{draft.entrypoint || '—'}</dd>
          </dl>
          <div className="build-actions">
            <Button variant="primary"
              disabled={busy !== 'idle' || draft.sources.length === 0 || !entryInSources || limits.length > 0}
              onClick={download}>
              <SwapLabel show={busy === 'dl' ? 'Validating' : 'Download build draft'}
                options={['Download build draft', 'Validating']} />
            </Button>
            <Button disabled={busy !== 'idle'} onClick={validateAll}>
              <SwapLabel show={busy === 'val' ? 'Validating' : 'Validate contracts'}
                options={['Validate contracts', 'Validating']} />
            </Button>
          </div>
          <div className="build-result" role="status" aria-live="polite">
            {warn && <Notice tone="warning">{warn}</Notice>}
            {validated && <Notice tone="neutral">Contracts validated.</Notice>}
          </div>
          <p className="build-dim">{storageFailed ? 'Not saved to this tab. Not published.' : 'Saved in this tab only. Not published.'}</p>
        </section>
      </div>
    </div>
  )
}
