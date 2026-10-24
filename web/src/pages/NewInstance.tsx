import { useCallback, useRef, useState } from 'react'
import { useNavigate, useSearchParams, Link } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api, wsPath, Instance, workspaces, currentWorkspace } from '../api'
import { Button, Loading, Notice, PageHeader } from '../components/ui'

import { SpecForm, type SpecFormHandle } from '../components/SpecForm'
import { draftFromSpec, type InstanceDraft } from '../instanceDraft'

// NewInstance is a page, not a modal: name + lifetime first, then only
// the add-ons the task needs, a persistent review of what is being
// granted, and Advanced JSON as an explicit alternate mode.
export default function NewInstance() {
  const nav = useNavigate()
  const qc = useQueryClient()
  const [params] = useSearchParams()
  const from = params.get('from')
  const form = useRef<SpecFormHandle>(null)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [review, setReview] = useState<InstanceDraft | null>(null)
  const [errors, setErrors] = useState<{ section: string; message: string }[]>([])

  // Create similar: fetch the source spec; an auth/404 error must not
  // silently open an empty draft.
  const src = useQuery({
    queryKey: ['instance', from],
    enabled: !!from,
    queryFn: () => api.get<{ instance: Instance }>(`/api/v1/instances/${from}`).then((r) => r.instance),
    retry: false,
  })

  const d = review ?? draftFromSpec()
  const grants: string[] = []
  if (d.fs) grants.push(d.fs.quotaBytes === '' ? 'Files' : `Files ${d.fs.quotaBytes}B`)
  if (d.net) grants.push(`Network ${d.net.allowHosts.length ? d.net.allowHosts.join(',') : 'any host'} ${d.net.methods.join('/')}`)
  if (d.exts.length) grants.push(...d.exts.map((e) => e.alias || 'extension'))
  const rules: string[] = []
  if (d.policies === null) rules.push('default rules')
  else if (d.policies.length === 0) rules.push('no policies')
  else rules.push(...d.policies.map((p) => p))
  const inputs = [...d.secrets.map((s) => s.name), ...d.env.map((e) => e.k)].filter(Boolean)
  const name = d.name

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
  const reviewCb = useCallback((d: InstanceDraft, errs?: { section: string; message: string }[]) => {
    setReview(d); setErrors(errs ?? [])
  }, [])
  if (wsq.isLoading || (from && src.isLoading)) return <Loading />
  if (wsq.isError) return <Notice tone="warning">Could not load workspaces — retry shortly.</Notice>
  if (!dest || !canCreate) return <Notice tone="warning">You need editor access to create instances in the current workspace.</Notice>
  if (from && src.isError) return <Notice tone="warning">Could not load the source instance's spec: {(src.error as Error).message}</Notice>

  return (
    <div>
      <PageHeader title={from ? 'Create similar' : 'New instance'}>
        <Link to="/" className="link">← Instances</Link> · a new instance starts empty — no files or state are copied.
      </PageHeader>
      <div className="grid gap-10 lg:grid-cols-[1fr_320px]">
        <div className="min-w-0 space-y-6">
          <SpecForm key={from ?? 'new'} ref={form} initial={from ? src.data?.spec : undefined} onSpecChange={reviewCb} workspaceId={dest.id} />

        </div>
        <aside className="space-y-4">
          <div className="sticky top-6 border border-line bg-surface p-4">
            <h2 className="mb-3 text-lg font-semibold text-ink">Review</h2>
            <dl className="space-y-3 text-xs">
              <div><dt className="text-mute">Lifetime</dt><dd className="font-mono text-ink">{typeof d.ttlSeconds === 'number' ? `${d.ttlSeconds}s` : '—'}</dd></div>
            {Object.keys(d.limits).length > 0 && <div><dt className="text-mute">Limits</dt><dd className="font-mono text-ink">{Object.entries(d.limits).map(([k, v]) => `${k}=${v}`).join(' ')}</dd></div>}
              {name && <div><dt className="text-mute">Name</dt><dd className="text-ink">{name}</dd></div>}
              <div><dt className="text-mute">Abilities</dt><dd className="text-ink">{grants.length ? grants.join(' · ') : 'compute only'}</dd></div>
              <div><dt className="text-mute">Rules</dt><dd className="text-ink">{rules.join(' · ') || '—'}</dd></div>
              <div><dt className="text-mute">Inputs</dt><dd className="font-mono text-ink">{inputs.join(' · ') || '—'}</dd></div>
            </dl>
            <div className="mt-4 border-t border-line pt-4">
              {errors.length > 0 && <ul className="mb-2 space-y-0.5 text-xs text-danger">{errors.map((e, i) => <li key={i}>{e.section}: {e.message}</li>)}</ul>}
              <Button variant="primary" className="w-full" disabled={busy || errors.length > 0} onClick={create}>
                {from ? 'Create similar' : 'Create instance'}
              </Button>
              {err && <p className="mt-2 text-xs text-warn-text">{err}</p>}
            </div>
          </div>
        </aside>
      </div>
    </div>
  )
}
