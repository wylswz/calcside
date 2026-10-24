import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api, Workspace } from '../api'
import { Button, inputCls, Notice, Loading } from '../components/ui'

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
  const bump = () => {
    qc.invalidateQueries({ queryKey: ['members', ws.id] })
    qc.invalidateQueries({ queryKey: ['changes', ws.id] })
  }
  const add = useMutation({ mutationFn: () => api.post(`/api/v1/workspaces/${ws.id}/members`, { email, role }), onSuccess: bump })
  const setRoleM = useMutation({ mutationFn: (p: { uid: string; role: string }) => api.put(`/api/v1/workspaces/${ws.id}/members/${p.uid}`, { role: p.role }), onSuccess: bump })
  const remove = useMutation({ mutationFn: (uid: string) => api.del(`/api/v1/workspaces/${ws.id}/members/${uid}`), onSuccess: bump })

  if (ws.personal) return <Notice>Personal workspaces have exactly one member — you.</Notice>
  const err = (add.error || setRoleM.error || remove.error) as Error | null

  return (
    <div>
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
                  ? <select className="border border-line bg-paper px-1 py-0.5 text-xs text-ink" value={m.role}
                      onChange={(e) => setRoleM.mutate({ uid: m.user_id, role: e.target.value })}>
                      {['owner', 'editor', 'viewer'].map((r) => <option key={r}>{r}</option>)}
                    </select>
                  : <span className="text-sec">{m.role}</span>}
              </td>
              <td className="py-2 text-right">
                {(isOwner || m.user_id === myID) && <Button size="sm" onClick={() => remove.mutate(m.user_id)}>{m.user_id === myID ? 'leave' : 'remove'}</Button>}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      {isOwner && (
        <div className="flex max-w-lg gap-2">
          <input className={inputCls} placeholder="member email (must have signed in once)" value={email} onChange={(e) => setEmail(e.target.value)} />
          <select className="border border-line bg-paper px-1 py-1 text-xs text-ink" value={role} onChange={(e) => setRole(e.target.value)}>
            <option value="viewer">viewer</option><option value="editor">editor</option><option value="owner">owner</option>
          </select>
          <Button size="sm" onClick={() => add.mutate()} disabled={!email.trim() || add.isPending}>Add</Button>
        </div>
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
