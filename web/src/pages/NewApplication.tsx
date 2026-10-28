import { useCallback, useRef, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api, Application, workspaces, currentWorkspace } from '../api'
import { SpecForm, type SpecFormHandle } from '../components/SpecForm'
import type { InstanceDraft, DraftError } from '../instanceDraft'
import { Button, Confirm, Field, inputCls, Loading, Notice, PageHeader, SwapLabel } from '../components/ui'

// NewApplication mirrors NewInstance: the spec builder is the page,
// the name lives in the sidebar identity slot, and the footer create
// bar carries issues and the primary action. The server seeds the
// main(input) scaffold; code is edited on the application's page.
export default function NewApplication() {
  const nav = useNavigate()
  const qc = useQueryClient()
  const [name, setName] = useState('')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [discard, setDiscard] = useState(false)
  const [specDirty, setSpecDirty] = useState(false)
  const [rawDirty, setRawDirty] = useState(false)
  const [specErrs, setSpecErrs] = useState<{ section: string; message: string }[]>([])
  const form = useRef<SpecFormHandle>(null)
  // stable callback: an inline fn in a child's effect deps would
  // re-fire on every render and falsely mark the form dirty
  const baseline = useRef<string | null>(null)
  const onSpec = useCallback((d: InstanceDraft, errs?: DraftError[]) => {
    setSpecErrs((cur) => (JSON.stringify(cur) === JSON.stringify(errs ?? []) ? cur : (errs ?? [])))
    const fp = JSON.stringify(d)
    if (baseline.current === null) baseline.current = fp // first call = pristine baseline
    else setSpecDirty(fp !== baseline.current)
  }, [])
  const wsq = useQuery({ queryKey: ['workspaces'], queryFn: workspaces })
  const dest = wsq.data?.find((w) => w.id === currentWorkspace()) ?? wsq.data?.find((w) => w.personal)
  const canCreate = dest != null && (dest.role === 'owner' || dest.role === 'editor')
  if (wsq.isLoading) return <Loading />
  if (!dest || !canCreate) return <Notice tone="warning">You need editor access to create applications in the current workspace.</Notice>

  const create = async () => {
    setErr('')
    const built = form.current?.buildSpec()
    if (!built) { setErr('Configuration is not ready yet. Try again.'); return }
    const { spec, specJSON, err: se } = built
    if (se) { setErr(se); return }
    setBusy(true)
    try {
      const r = await api.postRawJson<{ application: Application }>(`/api/v1/applications?workspace=${encodeURIComponent(dest.id)}`,
        `{"name":${JSON.stringify(name)},"spec":${specJSON ?? JSON.stringify(spec ?? {})}}`)
      qc.invalidateQueries({ queryKey: ['applications'] })
      nav(`/applications/${r.application.id}`)
    } catch (x: any) { setErr(x.message); setBusy(false) }
  }

  return (
    <div className="instance-builder">
      <PageHeader title="New application">
        <KindSelect current="application" onDirty={() => ((name !== '' || specDirty || rawDirty) ? setDiscard(true) : nav('/instances/new'))} />
        <span className="app-dim">A durable JSON function: callers POST a JSON object to main(input) and get one back. Edit code after creating.</span>
      </PageHeader>
      <div className="builder-work-panel">
        <SpecForm ref={form} application workspaceId={dest.id} onSpecChange={onSpec} onRawDirtyChange={setRawDirty}
          identity={
            <Field label="Name"><input id="app-name" className={inputCls} value={name} placeholder="Untitled application" onChange={(e) => setName(e.target.value)} /></Field>
          }
          footer={
            <div className="create-bar">
              {specErrs.length > 0 && <ul className="create-bar-issues" aria-live="polite">{specErrs.map((e, i) => <li key={i}>{e.section}: {e.message}</li>)}</ul>}
              {err && <p className="create-bar-issues" role="alert">{err}</p>}
              <Button variant="primary" className="w-full" disabled={busy || specErrs.length > 0} onClick={create}>
                <SwapLabel show={busy ? 'Creating…' : 'Create application'} options={['Create application', 'Creating…']} />
              </Button>
              <p className="create-bar-note">Next: write main(input), run it, then publish.</p>
            </div>
          } />
      </div>
      {discard && (
        <Confirm title="Discard draft" confirm="Discard" pending="Discarding…"
          onClose={() => setDiscard(false)}
          action={async () => { nav('/instances/new') }}>
          Leave without creating this application? Your name and configuration edits will be discarded.
        </Confirm>
      )}
    </div>
  )
}

// KindSelect distinguishes the two things you can create. When the
// page carries an onDirty handler the non-active tab routes through it
// instead of navigating raw — a typed draft gets a Confirm (F2).
export function KindSelect({ current, onDirty }: {
  current: 'instance' | 'application'
  onDirty?: () => void
}) {
  return (
    <span className="kind-tabs" role="navigation" aria-label="create kind">
      {current === 'instance'
        ? <Link to="/instances/new" className="kind-on">Instance</Link>
        : <Link to="/instances/new" onClick={(e) => { e.preventDefault(); onDirty?.() }}>Instance</Link>}
      {current === 'application'
        ? <Link to="/applications/new" className="kind-on">Application</Link>
        : <Link to="/applications/new" onClick={(e) => { e.preventDefault(); onDirty?.() }}>Application</Link>}
    </span>
  )
}
