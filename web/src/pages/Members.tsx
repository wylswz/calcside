import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api, Workspace } from '../api'
import { Button, Confirm, inputCls, Notice, Loading } from '../components/ui'
import { Select } from '../ui/core'

interface Member { user_id: string; email: string; name?: string; role: string }
interface Change { id: string; by_user_id: string; change: string; user_id?: string; instance_id?: string; role?: string; created_at: string }

const CHANGE_LABEL: Record<string, string> = {
  member_added: 'added member', role_changed: 'changed role', member_removed: 'removed member',
  share_added: 'shared instance', share_removed: 'unshared instance', renamed: 'renamed workspace',
}

export default function Members({ ws }: { ws: Workspace }) {
  const qc = useQueryClient()
  const [email, setEmail] = useState('')
  const [role, setRole] = useState('viewer')
  const [removing, setRemoving] = useState<Member | null>(null)
  const isOwner = ws.role === 'owner'
  const meQ = useQuery({ queryKey: ['me'], queryFn: () => api.get<{ user: { id: string } }>('/api/v1/me') })
  const myID = meQ.data?.user.id
  const membersQ = useQuery({
    queryKey: ['members', ws.id], enabled: !ws.personal,
    queryFn: () => api.get<{ members: Member[] }>(`/api/v1/workspaces/${ws.id}/members`),
  })
  const changesQ = useQuery({
    queryKey: ['changes', ws.id], enabled: isOwner && !ws.personal,
    queryFn: () => api.get<{ changes: Change[] }>(`/api/v1/workspaces/${ws.id}/changes`),
  })
  const bump = () => Promise.all([
    qc.invalidateQueries({ queryKey: ['members', ws.id] }),
    qc.invalidateQueries({ queryKey: ['changes', ws.id] }),
  ])
  const add = useMutation({ mutationFn: () => api.post(`/api/v1/workspaces/${ws.id}/members`, { email, role }), onSuccess: bump })
  const setRoleM = useMutation({ mutationFn: (p: { uid: string; role: string }) => api.put(`/api/v1/workspaces/${ws.id}/members/${p.uid}`, { role: p.role }), onSuccess: bump })
  const remove = useMutation({ mutationFn: (uid: string) => api.del(`/api/v1/workspaces/${ws.id}/members/${uid}`), onSuccess: bump })

  if (ws.personal) return <Notice>Personal workspaces have one member.</Notice>
  const err = (add.error || setRoleM.error) as Error | null

  return (
    <div>
      <div className="ws-pane-bar"><span className="ws-pane-title">Members</span></div>
      {err && <Notice tone="warning">{err.message}</Notice>}
      {membersQ.isLoading && <Loading />}
      <table className="mb-4 w-full text-left text-sm">
        <thead><tr className="text-xs text-mute"><th className="pb-2">member</th><th className="pb-2">role</th><th /></tr></thead>
        <tbody>
          {(membersQ.data?.members ?? []).map((m) => (
            <tr key={m.user_id} className="border-t border-line">
              <td className="py-2 text-ink">{m.name || m.email}{m.name ? ` · ${m.email}` : ''}</td>
              <td className="py-2">
                {isOwner && m.user_id !== myID
                  ? <Select
                      ariaLabel={`Role for ${m.name || m.email}`}
                      size="xs"
                      className="w-24"
                      value={m.role}
                      onChange={(r) => setRoleM.mutate({ uid: m.user_id, role: r })}
                      options={[
                        { value: 'owner', label: 'owner' },
                        { value: 'editor', label: 'editor' },
                        { value: 'viewer', label: 'viewer' },
                      ]}
                    />
                  : <span className="text-sec">{m.role}</span>}
              </td>
              <td className="py-2 text-right">
                {(isOwner || m.user_id === myID) && <Button size="sm" onClick={() => setRemoving(m)}>{m.user_id === myID ? 'leave' : 'remove'}</Button>}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      {isOwner && (
        <div className="flex max-w-lg items-center gap-2">
          <input className={inputCls + ' min-w-0 flex-1'} placeholder="member email (must have signed in once)" value={email} onChange={(e) => setEmail(e.target.value)} />
          <Select
            ariaLabel="Role"
            className="w-24 shrink-0"
            value={role}
            onChange={setRole}
            options={[
              { value: 'viewer', label: 'viewer' },
              { value: 'editor', label: 'editor' },
              { value: 'owner', label: 'owner' },
            ]}
          />
          <Button size="sm" onClick={() => add.mutate()} disabled={!email.trim() || add.isPending}>Add</Button>
        </div>
      )}
      {removing && (
        <Confirm title={removing.user_id === myID ? `Leave ${ws.name}?` : `Remove ${removing.name || removing.email}?`}
          confirm={removing.user_id === myID ? 'Leave' : 'Remove'} pending={removing.user_id === myID ? 'Leaving…' : 'Removing…'}
          action={() => remove.mutateAsync(removing.user_id)} onClose={() => setRemoving(null)}>
          <p className="text-sm text-body">{removing.user_id === myID
            ? 'You lose access to its instances, policies, secrets and keys until an owner adds you again.'
            : 'They lose access to this workspace until an owner adds them again.'}</p>
        </Confirm>
      )}
      {isOwner && (changesQ.data?.changes.length ?? 0) > 0 && (
        <div className="mt-6 max-h-64 max-w-2xl space-y-1 overflow-y-auto text-xs text-sec">
          {changesQ.data!.changes.map((c) => (
            <div key={c.id} className="flex justify-between">
              <span>{CHANGE_LABEL[c.change] ?? c.change}{c.role ? ` · ${c.role}` : ''}</span>
              <span className="text-mute">{new Date(c.created_at).toLocaleString()}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
