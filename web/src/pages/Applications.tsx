import { useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api, wsPath, Application } from '../api'
import { applicationDescription } from '../activity'
import { useRole } from '../workspace'
import { Button, Confirm, ListPage, Loading, Notice, RevealNew } from '../components/ui'
import { useHeldOrder } from '../stableList'

// Applications are durable named functions — a different thing from
// development instances. The list holds its order behind the reveal
// affordance (S5) like the instances list.
export default function Applications() {
  const nav = useNavigate()
  const qc = useQueryClient()
  const role = useRole()
  const canEdit = role === 'owner' || role === 'editor'
  const [deleting, setDeleting] = useState<Application | null>(null)
  const q = useQuery({
    queryKey: ['applications'],
    queryFn: () => api.get<{ applications: Application[] }>(wsPath('/api/v1/applications')),
    refetchInterval: 5000,
  })
  const held = useHeldOrder(q.data?.applications.map((a) => ({ a })) ?? undefined, ({ a }) => ({ key: a.id, slot: 'apps' }))

  return (
    <ListPage title="Applications" count={held.rows.length}
      actions={<>
        {held.arrived > 0 && <RevealNew count={held.arrived} moved={held.moved} onClick={held.reveal} />}
        {canEdit && <Button variant="primary" onClick={() => nav('/applications/new')}>New application</Button>}
      </>}>
      {q.isLoading && <Loading />}
      {q.isError && <Notice tone="warning">Could not load applications: {(q.error as Error).message}</Notice>}
      {held.rows.length === 0 && held.arrived === 0 && !q.isLoading && (
        <div className="flex h-full flex-col items-center justify-center gap-3 py-16">
          <p className="text-sm text-sec">No applications yet. <Link to="/applications/new" className="text-accent">Create the first one.</Link></p>
        </div>
      )}
      {held.rows.length > 0 && (
        <div className="tbl-wrap">
          <table className="tbl">
            <thead>
              <tr><th>Name</th><th>Status</th><th>Version</th><th /></tr>
            </thead>
            <tbody>
              {held.rows.map(({ item: { a } }) => {
                const description = applicationDescription(a)
                return (
                  <tr key={a.id} className="row-hover">
                    <td>
                      <Link to={`/applications/${a.id}`} className="link font-medium">{a.name}</Link>
                      {description && <div className="mt-0.5 max-w-[420px] truncate text-xs text-sec" title={description}>{description}</div>}
                    </td>
                    <td><AppStatus status={a.status} /></td>
                    <td className="text-xs text-sec">
                      v{a.version}
                      {a.has_unpublished_changes && <span className="text-mute"> · unpublished</span>}
                    </td>
                    <td className="text-right">
                      {canEdit && <Button size="sm" variant="danger" onClick={() => setDeleting(a)}>Delete</Button>}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
      {deleting && (
        <Confirm tone="danger" title={`Delete ${deleting.name}?`} confirm="Delete" pending="Deleting…"
          action={async () => {
            await api.del(`/api/v1/applications/${deleting.id}?version=${deleting.version}`)
            await qc.invalidateQueries({ queryKey: ['applications'] })
          }}
          onClose={() => setDeleting(null)}>
          <p className="text-sm text-body">Its deployment and keys stop working.</p>
        </Confirm>
      )}
    </ListPage>
  )
}

// AppStatus: one status vocabulary — dot + text like StatusCell.
function AppStatus({ status }: { status: string }) {
  const dot = status === 'ready' ? 'bg-live' : status === 'degraded' ? 'bg-warn' : 'bg-mute'
  return (
    <span className="inline-flex items-center gap-1.5 whitespace-nowrap text-xs text-sec">
      <span className={`inline-block h-1.5 w-1.5 rounded-full ${dot}`} />
      {status}
    </span>
  )
}
