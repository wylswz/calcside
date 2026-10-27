import { useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api, wsPath, Application, workspaces, currentWorkspace } from '../api'
import { Button, Confirm, Loading, Notice, PageHeader, SwapLabel } from '../components/ui'

// NewApplication: the application half of the create flow — a name
// plus a one-line mental model. The server seeds the main(input)
// scaffold; code is edited on the application's page.
export default function NewApplication() {
  const nav = useNavigate()
  const qc = useQueryClient()
  const [name, setName] = useState('')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [discard, setDiscard] = useState(false)
  const wsq = useQuery({ queryKey: ['workspaces'], queryFn: workspaces })
  const dest = wsq.data?.find((w) => w.id === (currentWorkspace() ?? wsq.data?.find((x) => x.personal)?.id))
  const canCreate = dest != null && (dest.role === 'owner' || dest.role === 'editor')
  if (wsq.isLoading) return <Loading />
  if (!dest || !canCreate) return <Notice tone="warning">You need editor access to create applications in the current workspace.</Notice>

  const create = async () => {
    setErr('')
    setBusy(true)
    try {
      const r = await api.post<{ application: Application }>(wsPath('/api/v1/applications'), { name })
      qc.invalidateQueries({ queryKey: ['applications'] })
      nav(`/applications/${r.application.id}`)
    } catch (x: any) { setErr(x.message); setBusy(false) }
  }

  return (
    <div className="app-page">
      <PageHeader title="New application">
        <KindSelect current="application" onDirty={() => (name !== '' ? setDiscard(true) : nav('/instances/new'))} />
      </PageHeader>
      <div className="app-new-card">
        <label className="app-label" htmlFor="app-name">Name</label>
        <input id="app-name" className="app-input" value={name} placeholder="Untitled application" onChange={(e) => setName(e.target.value)} />
        <p className="app-dim">An application is a durable JSON function: you write <code>main(input)</code> in Starlark, callers POST a JSON object and get a JSON object back.</p>
        <pre className="app-example">{`{"name": "world"}  ->  main(input)  ->  {"message": "Hello, world"}`}</pre>
        <div className="app-result" role="status" aria-live="polite">{err && <Notice tone="warning">{err}</Notice>}</div>
        <Button variant="primary" disabled={busy} onClick={create}>
          <SwapLabel show={busy ? 'Creating…' : 'Create application'} options={['Create application', 'Creating…']} />
        </Button>
        <p className="app-dim">A server-side draft is created with a scaffold you can test and publish.</p>
        {discard && (
          <Confirm title="Discard draft" confirm="Discard" pending="Discarding…"
            onClose={() => setDiscard(false)}
            action={async () => { nav('/instances/new') }}>
            A name is already typed — leave without creating?
          </Confirm>
        )}
      </div>
    </div>
  )
}

// KindSelect distinguishes the two things you can create. Dirty pages
// gate the switch behind the blocker Confirm (F2), not a bare link.
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
