
import { Link, useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api, SharedInstance, Instance, wsPath } from '../api'
import { useRole } from '../workspace'
import { Button, Loading, PageHeader, InstanceStatus, RevealNew } from '../components/ui'
import { attentionText, expiringSoon } from '../nextStep'
import { allowedSummary, instanceName } from '../activity'
import { fmtTime, relTime } from '../format'
import { useHeldOrder } from '../stableList'


const toneCls = { danger: 'text-danger', warn: 'text-warn-text', mute: 'text-mute', sec: 'text-sec' } as const
function attention(i: Instance) {
  const a = attentionText(i)
  return <span className={`text-xs ${toneCls[a.tone]}`}>{a.text}{a.link ? <> · <Link className="link" to={`/instances/${i.id}`}>connect agent →</Link></> : null}</span>
}

function rank(i: Instance): number {
  if (i.status !== 'running') return 2
  return expiringSoon(i, Date.now()) ? 0 : 1
}

export default function Instances() {
  const nav = useNavigate()
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
  const all = [...(data?.instances ?? [])].sort((a, b) =>
    new Date(b.last_active_at).getTime() - new Date(a.last_active_at).getTime())
  const attn = (i: Instance) => i.status === 'running' && (
    (i.activity?.denied_runs ?? 0) > 0 || (i.activity?.failed_runs ?? 0) > 0 || rank(i) === 0)
  const needsAttention = all.filter(attn)
  const live = all.filter((i) => (i.status === 'running') && !attn(i))
  const ended = all.filter((i) => i.status !== 'running')
  const fresh: [string, Instance[]][] = [['Needs attention', needsAttention], ['Live', live], ['Ended', ended]]
  // Polling updates rows in place but never moves them between groups or
  // inserts new ones under the user (S5); "Show latest" re-sorts.
  const held = useHeldOrder(data ? fresh.flatMap(([slot, g]) => g.map((i) => ({ i, slot }))) : undefined, ({ i, slot }) => ({ key: i.id, slot }))
  const groups: [string, Instance[]][] = fresh.map(([label]) => [label, held.rows.filter((r) => r.slot === label).map((r) => r.item.i)])

  return (
    <div>
      <PageHeader title="Instances"
        actions={<>
          <RevealNew count={held.arrived} moved={held.moved} onClick={held.reveal} />
          {role != null && role !== 'viewer' && <Button variant="primary" onClick={() => nav('/instances/new')}>New instance</Button>}
        </>}>
        Set the boundary, connect an agent, and follow its work.
      </PageHeader>
      {isLoading && <Loading />}
      {data && all.length === 0 && (
        <div className="rounded-xl border border-line bg-surface px-6 py-10 text-center">
          <p className="mb-4 text-sm text-sec">1 Decide what an agent may do · 2 Connect the agent · 3 Review its runs</p>
          {role != null && role !== 'viewer' && <Button variant="primary" onClick={() => nav('/instances/new')}>New instance</Button>}
        </div>
      )}
      {all.length > 0 && groups.filter(([, g]) => g.length).map(([label, g]) => (
        <div key={label} className="mb-8">
          <h2 className="mb-3 text-base font-semibold text-ink">{label}</h2>
          <div className="tbl-wrap">
            <table className="tbl">
              <thead>
                <tr><th>Name</th><th>Status</th><th>Needs attention</th><th>Allowed</th><th>Last active</th></tr>
              </thead>
              <tbody>
                {g.map((i) => {
                const name = instanceName(i)
                return (
                  <tr key={i.id} className="row-hover" title={`created ${fmtTime(i.created_at)}`}>
                    <td>
                      <Link to={`/instances/${i.id}`} className="link font-medium">{name}</Link>
                      {name !== i.id && <div className="mt-0.5 font-mono text-[11px] text-mute">{i.id}</div>}
                    </td>
                    <td><InstanceStatus inst={i} /></td>
                    <td>{attention(i)}</td>
                    <td className="max-w-[280px]">
                      <span className="block truncate font-mono text-xs text-sec" title={allowedSummary(i.spec)}>{allowedSummary(i.spec)}</span>
                    </td>
                    <td className="whitespace-nowrap text-xs text-sec" title={fmtTime(i.last_active_at)}>{relTime(i.last_active_at)}</td>
                  </tr>
                )
                  })}
              </tbody>
            </table>
          </div>
        </div>
      ))}
            {(shared?.instances.length ?? 0) > 0 && (
        <div className="mt-8">
          <h2 className="mb-3 text-base font-semibold text-ink">Shared with you</h2>
          <div className="tbl-wrap">
            <table className="tbl">
              <thead><tr><th>Name</th><th>Workspace</th><th>Role</th><th>Status</th></tr></thead>
              <tbody>
                {shared!.instances.map((si) => (
                  <tr key={si.instance.id} className="row-hover">
                    <td><Link to={`/instances/${si.instance.id}`} className="link font-medium">{instanceName(si.instance)}</Link></td>
                    <td className="text-xs text-sec">{si.workspace_name}</td>
                    <td className="text-xs text-sec">{si.role}</td>
                    <td><InstanceStatus inst={si.instance} /></td>
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
