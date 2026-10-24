import { useCallback, useRef, useState } from 'react'
import { useNavigate, useSearchParams, Link } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api, wsPath, Instance, workspaces, currentWorkspace } from '../api'
import { Button, Loading, Notice, PageHeader } from '../components/ui'

import { SpecForm, type SpecFormHandle } from '../components/SpecForm'
import type { InstanceDraft } from '../instanceDraft'

// NewInstance is a page, not a modal: name + lifetime first, available
// capabilities on one side, the instance being built (with Create) on
// the other, and Advanced JSON as an explicit alternate mode.
export default function NewInstance() {
  const nav = useNavigate()
  const qc = useQueryClient()
  const [params] = useSearchParams()
  const from = params.get('from')
  const form = useRef<SpecFormHandle>(null)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [errors, setErrors] = useState<{ section: string; message: string }[]>([])

  // Create similar: fetch the source spec; an auth/404 error must not
  // silently open an empty draft.
  const src = useQuery({
    queryKey: ['instance', from],
    enabled: !!from,
    queryFn: () => api.get<{ instance: Instance }>(`/api/v1/instances/${from}`).then((r) => r.instance),
    retry: false,
  })

  const create = async () => {
    setErr('')
    const { spec, err: e } = form.current?.buildSpec() ?? {}
    if (e) { setErr(e); return }
    setBusy(true)
    try {
      const r = await api.post<{ instance: Instance }>(wsPath('/api/v1/instances'), spec)
      qc.invalidateQueries({ queryKey: ['instances'] })
      nav(`/instances/${r.instance.id}`)
    } catch (x: any) { setErr(x.message) } finally { setBusy(false) }
  }

  const wsq = useQuery({ queryKey: ['workspaces'], queryFn: workspaces })
  const dest = wsq.data?.find((w) => w.id === (currentWorkspace() ?? wsq.data?.find((x) => x.personal)?.id))
  const canCreate = dest != null && (dest.role === 'owner' || dest.role === 'editor')
  const reviewCb = useCallback((_: InstanceDraft, errs?: { section: string; message: string }[]) => {
    setErrors(errs ?? [])
  }, [])
  if (wsq.isLoading || (from && src.isLoading)) return <Loading />
  if (wsq.isError) return <Notice tone="warning">Could not load workspaces — retry shortly.</Notice>
  if (!dest || !canCreate) return <Notice tone="warning">You need editor access to create instances in the current workspace.</Notice>
  if (from && src.isError) return <Notice tone="warning">Could not load the source instance's spec: {(src.error as Error).message}</Notice>

  return (
    <div className="instance-builder">
      <PageHeader title={from ? 'Create similar' : 'New instance'}>
        <Link to="/" className="link">← Instances</Link> · {from ? 'Copy configuration only. Files and state stay in the original instance.' : 'Choose what the agent can access. Connect it after creation.'}
      </PageHeader>
      <div className="builder-work-panel">
        <SpecForm key={from ?? 'new'} ref={form} initial={from ? src.data?.spec : undefined} onSpecChange={reviewCb} workspaceId={dest.id}
          footer={
            <div className="create-bar">
              {errors.length > 0 && <ul className="create-bar-issues" aria-live="polite">{errors.map((e, i) => <li key={i}>{e.section === 'Extensions' ? e.message : `${e.section}: ${e.message}`}</li>)}</ul>}
              {err && <p className="create-bar-issues" role="alert">{err}</p>}
              <Button variant="primary" className="w-full" disabled={busy || errors.length > 0} onClick={create}>
                {from ? 'Create similar' : 'Create instance'}
              </Button>
              <p className="create-bar-note">Next: connect an agent or run code yourself.</p>
            </div>
          } />
      </div>
    </div>
  )
}
