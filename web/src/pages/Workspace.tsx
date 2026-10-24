import { useState } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api, workspaces, ApiError } from '../api'
import { PageHeader, SectionTitle, Button, inputCls, Loading, EmptyRow, Notice } from '../components/ui'

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

  const bump = () => {
    qc.invalidateQueries({ queryKey: ['members', id] })
    qc.invalidateQueries({ queryKey: ['changes', id] })
    qc.invalidateQueries({ queryKey: ['workspaces'] })
  }
  const useMut = <T,>(fn: (x: T) => Promise<unknown>) => useMutation({ mutationFn: fn, onSuccess: bump })
  const rename = useMut<void>(() => api.patch(`/api/v1/workspaces/${id}`, { name }))
  const add = useMut<void>(() => api.post(`/api/v1/workspaces/${id}/members`, { email, role }))
  const setRoleM = useMut<{ uid: string; role: string }>((p: { uid: string; role: string }) => api.put(`/api/v1/workspaces/${id}/members/${p.uid}`, { role: p.role }))
  const remove = useMut<string>((uid: string) => api.del(`/api/v1/workspaces/${id}/members/${uid}`))

  if (wsQ.isLoading) return <Loading />
  if (!ws) return <Notice tone="warning">workspace not found</Notice>
  if (ws.personal) return (
    <div>
      <PageHeader title={ws.name} />
      <Notice>Your personal workspace has exactly one member — you. Use a shared workspace to work with other people.</Notice>
    </div>
  )
  const err = (rename.error || add.error || setRoleM.error || remove.error) as ApiError | null

  return (
    <div>
      <PageHeader title={ws.name} actions={<span className="text-xs text-faint">you are {isOwner ? 'an' : 'a'} {ws.role}</span>} />
      {err && <Notice tone="warning">{err.message}</Notice>}

      <SectionTitle>name</SectionTitle>
      {isOwner ? (
        <div className="mb-6 flex max-w-md gap-2">
          <input className={inputCls} defaultValue={ws.name} onChange={(e) => setName(e.target.value)} />
          <Button size="sm" onClick={() => rename.mutate()} disabled={!name.trim() || rename.isPending}>Rename</Button>
        </div>
      ) : <p className="mb-6 text-sm text-ink">{ws.name}</p>}

      <SectionTitle>members</SectionTitle>
      <table className="mb-4 w-full text-left text-sm">
        <thead><tr className="text-xs text-faint"><th className="pb-2">member</th><th className="pb-2">role</th><th /></tr></thead>
        <tbody>
          {(membersQ.data?.members ?? []).map((m) => (
            <tr key={m.user_id} className="border-t border-line">
              <td className="py-2 text-ink">{m.name || m.email}{m.name ? ` · ${m.email}` : ''}</td>
              <td className="py-2">
                {isOwner && m.user_id !== myID ? (
                  <select className="border border-line bg-paper px-1 py-0.5 text-xs text-ink" value={m.role}
                    onChange={(e) => setRoleM.mutate({ uid: m.user_id, role: e.target.value })}>
                    {['owner', 'editor', 'viewer'].map((r) => <option key={r} value={r}>{r}</option>)}
                  </select>
                ) : <span className="text-sec">{m.role}</span>}
              </td>
              <td className="py-2 text-right">
                {(isOwner || m.user_id === myID) && (
                  <Button size="sm" onClick={() => remove.mutate(m.user_id)}>{m.user_id === myID ? 'leave' : 'remove'}</Button>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      {(membersQ.data?.members ?? []).length === 0 && <EmptyRow cols={3}>no members</EmptyRow>}
      {isOwner && (
        <div className="mb-6 flex max-w-lg gap-2">
          <input className={inputCls} placeholder="member email (must have signed in once)"
            value={email} onChange={(e) => setEmail(e.target.value)} />
          <select className="border border-line bg-paper px-1 py-1 text-xs text-ink" value={role} onChange={(e) => setRole(e.target.value)}>
            <option value="viewer">viewer</option><option value="editor">editor</option><option value="owner">owner</option>
          </select>
          <Button size="sm" onClick={() => add.mutate()} disabled={!email.trim() || add.isPending}>Add</Button>
        </div>
      )}

      {isOwner && (changesQ.data?.changes.length ?? 0) > 0 && (
        <>
          <SectionTitle>changes</SectionTitle>
          <div className="mb-6 max-h-64 max-w-2xl space-y-1 overflow-y-auto text-xs text-sec">
            {changesQ.data!.changes.map((c) => (
              <div key={c.id} className="flex justify-between">
                <span>{CHANGE_LABEL[c.change] ?? c.change}{c.role ? ` · ${c.role}` : ''}</span>
                <span className="text-faint">{new Date(c.created_at).toLocaleString()}</span>
              </div>
            ))}
          </div>
        </>
      )}

      {isOwner && (
        <>
          <SectionTitle>delete workspace</SectionTitle>
          {!confirmDel
            ? <Button size="sm" onClick={() => setConfirmDel(true)}>Delete workspace</Button>
            : <div className="flex items-center gap-3">
                <span className="text-sm text-warn-text">Delete {ws.name} and its policies, secrets and keys? Stop running instances first.</span>
                <Button size="sm" onClick={async () => { try { await api.del(`/api/v1/workspaces/${id}`); nav('/') } catch (e: any) { alert(e.message) } }}>Confirm</Button>
                <Button size="sm" onClick={() => setConfirmDel(false)}>Cancel</Button>
              </div>}
        </>
      )}
    </div>
  )
}
