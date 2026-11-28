import { useState } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api, workspaces, ApiError } from '../api'
import { ListPage, SectionTitle, Button, Confirm, inputCls, Loading, EmptyRow, MenuItem, Notice } from '../components/ui'
import { RowActionsMenu, Select } from '../ui/core'

interface Member { user_id: string; email: string; name?: string; role: string }
interface Change { id: string; by_user_id: string; change: string; user_id?: string; instance_id?: string; role?: string; created_at: string }

const CHANGE_LABEL: Record<string, string> = {
  member_added: 'added member', role_changed: 'changed role', member_removed: 'removed member',
  share_added: 'shared instance', share_removed: 'unshared instance', renamed: 'renamed workspace',
}

export default function WorkspacePage() {
  const { id } = useParams()
  const nav = useNavigate()
  const qc = useQueryClient()
  const [name, setName] = useState('')
  const [email, setEmail] = useState('')
  const [role, setRole] = useState('viewer')
  const [confirmDel, setConfirmDel] = useState(false)
  const [removing, setRemoving] = useState<Member | null>(null)

  const wsQ = useQuery({ queryKey: ['workspaces'], queryFn: workspaces })
  const ws = (wsQ.data ?? []).find((w) => w.id === id)
  const meQ = useQuery({ queryKey: ['me'], queryFn: () => api.get<{ user: { id: string } }>('/api/v1/me') })
  const myID = meQ.data?.user.id
  const membersQ = useQuery({
    queryKey: ['members', id],
    queryFn: () => api.get<{ members: Member[] }>(`/api/v1/workspaces/${id}/members`),
    enabled: !!ws && !ws.personal,
  })
  const changesQ = useQuery({
    queryKey: ['changes', id],
    queryFn: () => api.get<{ changes: Change[] }>(`/api/v1/workspaces/${id}/changes`),
    enabled: ws?.role === 'owner' && !ws.personal,
  })
  const isOwner = ws?.role === 'owner'

  const bump = () => Promise.all([
    qc.invalidateQueries({ queryKey: ['members', id] }),
    qc.invalidateQueries({ queryKey: ['changes', id] }),
    qc.invalidateQueries({ queryKey: ['workspaces'] }),
  ])
  const useMut = <T,>(fn: (x: T) => Promise<unknown>) => useMutation({ mutationFn: fn, onSuccess: bump })
  const rename = useMut<void>(() => api.patch(`/api/v1/workspaces/${id}`, { name }))
  const add = useMut<void>(() => api.post(`/api/v1/workspaces/${id}/members`, { email, role }))
  const setRoleM = useMut<{ uid: string; role: string }>((p: { uid: string; role: string }) => api.put(`/api/v1/workspaces/${id}/members/${p.uid}`, { role: p.role }))
  const remove = useMut<string>((uid: string) => api.del(`/api/v1/workspaces/${id}/members/${uid}`))

  if (wsQ.isLoading) return <Loading />
  if (!ws) return <Notice tone="warning">Workspace not found.</Notice>
  if (ws.personal) return (
    <ListPage title={ws.name}>
      <div className="p-4"><Notice>Personal workspaces have one member.</Notice></div>
    </ListPage>
  )
  const err = (rename.error || add.error || setRoleM.error) as ApiError | null

  return (
    <ListPage title={ws.name}
      actions={<span className="text-xs text-mute">Role: {ws.role}</span>}>
      <div className="space-y-6 p-4">
        {err && <Notice tone="warning">{err.message}</Notice>}

        <section>
          <SectionTitle>Workspace name</SectionTitle>
          {isOwner ? (
            <div className="flex max-w-md gap-2">
              <input className={inputCls} defaultValue={ws.name} onChange={(e) => setName(e.target.value)} />
              <Button size="sm" onClick={() => rename.mutate()} disabled={!name.trim() || rename.isPending}>Rename</Button>
            </div>
          ) : <p className="text-sm text-ink">{ws.name}</p>}
        </section>

        <section>
          <SectionTitle>Members</SectionTitle>
          <div className="tbl-wrap mb-4">
            <table className="tbl">
              <thead><tr><th>Member</th><th>Role</th><th className="w-10" /></tr></thead>
              <tbody>
                {(membersQ.data?.members ?? []).map((m) => (
                  <tr key={m.user_id} className="row-hover">
                    <td className="text-ink">{m.name || m.email}{m.name ? ` · ${m.email}` : ''}</td>
                    <td>
                      {isOwner && m.user_id !== myID ? (
                        <Select
                          ariaLabel={`Role for ${m.name || m.email}`}
                          size="sm"
                          className="w-28"
                          value={m.role}
                          onChange={(r) => setRoleM.mutate({ uid: m.user_id, role: r })}
                          options={[
                            { value: 'owner', label: 'owner' },
                            { value: 'editor', label: 'editor' },
                            { value: 'viewer', label: 'viewer' },
                          ]}
                        />
                      ) : <span className="text-xs text-sec">{m.role}</span>}
                    </td>
                    <td className="text-right">
                      {(isOwner || m.user_id === myID) && (
                        <RowActionsMenu label={`Actions for ${m.name || m.email}`}>
                          <MenuItem tone="danger" onSelect={() => setRemoving(m)}>
                            {m.user_id === myID ? 'Leave workspace…' : 'Remove member…'}
                          </MenuItem>
                        </RowActionsMenu>
                      )}
                    </td>
                  </tr>
                ))}
                {(membersQ.data?.members ?? []).length === 0 && <EmptyRow cols={3}>No members</EmptyRow>}
              </tbody>
            </table>
          </div>
          {isOwner && (
            <div className="flex max-w-lg gap-2">
              <input className={inputCls} placeholder="Member email (must have signed in once)"
                value={email} onChange={(e) => setEmail(e.target.value)} />
              <Select
                ariaLabel="Role"
                className="w-28 shrink-0"
                value={role}
                onChange={setRole}
                options={[
                  { value: 'viewer', label: 'viewer' },
                  { value: 'editor', label: 'editor' },
                  { value: 'owner', label: 'owner' },
                ]}
              />
              <Button size="sm" variant="primary" onClick={() => add.mutate()} disabled={!email.trim() || add.isPending}>Add</Button>
            </div>
          )}
        </section>

        {removing && (
          <Confirm tone="danger" title={removing.user_id === myID ? `Leave ${ws.name}?` : `Remove ${removing.name || removing.email}?`}
            confirm={removing.user_id === myID ? 'Leave' : 'Remove'} pending={removing.user_id === myID ? 'Leaving…' : 'Removing…'}
            action={() => remove.mutateAsync(removing.user_id)} onClose={() => setRemoving(null)}>
            <p className="text-sm text-body">{removing.user_id === myID
              ? 'You lose access to its instances, policies, secrets and keys until an owner adds you again.'
              : 'They lose access to this workspace until an owner adds them again.'}</p>
          </Confirm>
        )}

        {isOwner && (changesQ.data?.changes.length ?? 0) > 0 && (
          <section>
            <SectionTitle>Recent changes</SectionTitle>
            <div className="max-h-64 max-w-2xl divide-y divide-line overflow-y-auto rounded-[3px] border border-line bg-paper text-xs text-sec">
              {changesQ.data!.changes.map((c) => (
                <div key={c.id} className="flex justify-between px-3 py-2">
                  <span>{CHANGE_LABEL[c.change] ?? c.change}{c.role ? ` · ${c.role}` : ''}</span>
                  <span className="tabular-nums text-mute">{new Date(c.created_at).toLocaleString()}</span>
                </div>
              ))}
            </div>
          </section>
        )}

        {isOwner && (
          <section>
            <SectionTitle>Danger zone</SectionTitle>
            <Button size="sm" variant="danger" onClick={() => setConfirmDel(true)}>Delete workspace…</Button>
            {confirmDel && (
              <Confirm tone="danger" title={`Delete ${ws.name}?`} confirm="Delete workspace" pending="Deleting…"
                action={async () => { await api.del(`/api/v1/workspaces/${id}`); nav('/') }} onClose={() => setConfirmDel(false)}>
                <p className="text-sm text-body">Its policies, secrets and keys are deleted; this cannot be undone.</p>
              </Confirm>
            )}
          </section>
        )}
      </div>
    </ListPage>
  )
}
