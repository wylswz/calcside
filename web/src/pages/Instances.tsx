import { useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, SharedInstance, Instance, wsPath } from '../api'
import { useRole } from '../workspace'
import { Button, EmptyRow, Loading, Modal, PageHeader, StatusText, TimeLeft } from '../components/ui'
import { SpecForm, type SpecFormHandle } from '../components/SpecForm'
import { fmtTime } from '../format'
import { allowedSummary, instanceName } from '../activity'
import { relTime } from '../format'

function NewInstanceDialog({ onClose }: { onClose: () => void }) {
  const qc = useQueryClient()
  const form = useRef<SpecFormHandle>(null)
  const [err, setErr] = useState('')
  const create = useMutation({
    mutationFn: (spec: any) => api.post<{ instance: Instance }>(wsPath('/api/v1/instances'), spec),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['instances'] })
      onClose()
    },
    onError: (e: any) => setErr(e.message),
  })
  const submit = () => {
    setErr('')
    const { spec, err: e } = form.current?.buildSpec() ?? {}
    if (e) { setErr(e); return }
    create.mutate(spec)
  }
  return (
    <Modal title="New instance" onClose={onClose} wide footer={
      <>
        {err && <p className="text-xs text-warn-text">{err}</p>}
        <div className="ml-auto flex gap-2">
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" disabled={create.isPending} onClick={submit}>
            Create instance
          </Button>
        </div>
      </>
    }>
      <SpecForm ref={form} />
    </Modal>
  )
}

export default function Instances() {
  const [showNew, setShowNew] = useState(false)
  const role = useRole()
  const { data, isLoading } = useQuery({
    queryKey: ['instances'],
    queryFn: () => api.get<{ instances: Instance[] }>(wsPath('/api/v1/instances')),
    refetchInterval: 15000,
  })
  const { data: shared } = useQuery({
    queryKey: ['shared'],
    queryFn: () => api.get<{ instances: SharedInstance[] }>('/api/v1/instances?shared=true'),
  })

  return (
    <div>
      <PageHeader title="Instances"
        actions={role !== 'viewer' ? <Button variant="primary" onClick={() => setShowNew(true)}>New instance</Button> : undefined}>
        Each instance is a Starlark session with its own globals, VFS and policy snapshot. Only the capabilities granted in its spec exist.
      </PageHeader>
      {isLoading && <Loading />}
      <div className="tbl-wrap">
        <table className="tbl">
          <thead>
            <tr>
              <th>Name</th>
              <th>Status</th>
              <th>Allowed</th>
              <th>Last active</th>
              <th>Created</th>
            </tr>
          </thead>
          <tbody>
            {(data?.instances ?? []).map((i) => {
              const name = instanceName(i)
              return (
                <tr key={i.id} className="row-hover">
                  <td>
                    <Link to={`/instances/${i.id}`} className="link font-medium">{name}</Link>
                    {name !== i.id && <div className="mt-0.5 font-mono text-[11px] text-mute">{i.id}</div>}
                  </td>
                  <td>
                    <div className="flex flex-col gap-1">
                      <StatusText status={i.status} />
                      {i.status === 'running' && <TimeLeft expiresAt={i.expires_at} />}
                    </div>
                  </td>
                  <td className="max-w-[320px]">
                    <span className="block truncate font-mono text-xs text-sec" title={allowedSummary(i.spec)}>{allowedSummary(i.spec)}</span>
                  </td>
                  <td className="whitespace-nowrap text-xs text-sec" title={fmtTime(i.last_active_at)}>{relTime(i.last_active_at)}</td>
                  <td className="whitespace-nowrap text-xs text-sec">{fmtTime(i.created_at)}</td>
                </tr>
              )
            })}
            {data && data.instances.length === 0 && <EmptyRow cols={5}>no instances</EmptyRow>}
          </tbody>
        </table>
      </div>
      {showNew && <NewInstanceDialog onClose={() => setShowNew(false)} />}
      {(shared?.instances.length ?? 0) > 0 && (
        <div className="mt-8">
          <h2 className="mb-3 text-xs font-medium uppercase tracking-label text-faint">Shared with you</h2>
          <div className="tbl-wrap">
            <table className="tbl">
              <thead><tr><th>Name</th><th>Workspace</th><th>Role</th><th>Status</th></tr></thead>
              <tbody>
                {shared!.instances.map((si) => (
                  <tr key={si.instance.id} className="row-hover">
                    <td><Link to={`/instances/${si.instance.id}`} className="link font-medium">{instanceName(si.instance)}</Link></td>
                    <td className="text-xs text-sec">{si.workspace_name}</td>
                    <td className="text-xs text-sec">{si.role}</td>
                    <td><StatusText status={si.instance.status} /></td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}
    </div>
  )
}
