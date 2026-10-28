import { Link, useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api, wsPath, Application } from '../api'
import { Button, Loading, Notice, PageHeader, RevealNew } from '../components/ui'
import { useHeldOrder } from '../stableList'

// Applications are durable named functions — a different thing from
// development instances. The list holds its order behind the reveal
// affordance (S5) like the instances list.
export default function Applications() {
  const nav = useNavigate()
  const q = useQuery({
    queryKey: ['applications'],
    queryFn: () => api.get<{ applications: Application[] }>(wsPath('/api/v1/applications')),
    refetchInterval: 5000,
  })
  const held = useHeldOrder(q.data?.applications.map((a) => ({ a })) ?? undefined, ({ a }) => ({ key: a.id, slot: 'apps' }))

  return (
    <div className="app-page">
      <PageHeader title="Applications">
        <Button variant="primary" onClick={() => nav('/applications/new')}>New application</Button>
      </PageHeader>
      <p className="app-dim">Durable JSON functions: <code>main(input)</code> in → JSON out. Drafts live on the server; publish to deploy.</p>
      {q.isLoading && <Loading />}
      {q.isError && <Notice tone="warning">Could not load applications: {(q.error as Error).message}</Notice>}
      {held.arrived > 0 && <RevealNew count={held.arrived} moved={held.moved} onClick={held.reveal} />}
      {q.data && held.rows.length === 0 && held.arrived === 0 && (
        <p className="app-dim">No applications yet. <Link to="/applications/new" className="text-accent">Create the first one.</Link></p>
      )}
      <ul className="app-list">
        {held.rows.map(({ item: { a } }) => (
          <li key={a.id} className="app-row">
            <Link to={`/applications/${a.id}`} className="app-name">{a.name}</Link>
            <span className={`app-status app-status-${a.status}`}>{a.status}</span>
            <span className="app-dim">v{a.version}</span>
            {a.has_unpublished_changes && <span className="app-dim">unpublished changes</span>}
          </li>
        ))}
      </ul>
    </div>
  )
}
