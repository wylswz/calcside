import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '../api'
import { Button, Confirm, Field, Modal, inputCls } from './ui'

interface Share { user_id: string; email?: string; name?: string; role: string }
interface Member { user_id: string; email: string; name?: string; role: string }

export default function ShareDialog({ instanceId, workspaceId, onClose }: { instanceId: string; workspaceId: string; onClose: () => void }) {
  const qc = useQueryClient()
  const [email, setEmail] = useState('')
  const [role, setRole] = useState('viewer')
  const [err, setErr] = useState('')
  const [removing, setRemoving] = useState<Share | null>(null)
  const sharesQ = useQuery({
    queryKey: ['shares', instanceId],
    queryFn: () => api.get<{ shares: Share[] }>(`/api/v1/instances/${instanceId}/shares`),
  })
  const membersQ = useQuery({
    queryKey: ['members', workspaceId],
    queryFn: () => api.get<{ members: Member[] }>(`/api/v1/workspaces/${workspaceId}/members`),
  })
  const put = useMutation({
    mutationFn: () => api.put(`/api/v1/instances/${instanceId}/shares`, { email, role }),
    onSuccess: () => { setEmail(''); setErr(''); qc.invalidateQueries({ queryKey: ['shares', instanceId] }) },
    onError: (e: Error) => setErr(e.message),
  })
  const del = useMutation({
    mutationFn: (uid: string) => api.del(`/api/v1/instances/${instanceId}/shares/${uid}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['shares', instanceId] }),
  })

  return (
    <Modal title="Share instance" onClose={onClose} dirty={email.trim() !== ''} footer={
      <Button variant="primary" onClick={onClose}>Done</Button>
    }>
      <div className="space-y-4 text-sm">
        <div>
          <p className="mb-1 text-xs uppercase tracking-label text-mute">workspace members</p>
          <p className="text-sec">{(membersQ.data?.members ?? []).map((m) => m.name || m.email).join(', ') || '—'}</p>
        </div>
        <div>
          <p className="mb-1 text-xs uppercase tracking-label text-mute">shared with</p>
          {(sharesQ.data?.shares ?? []).length === 0 && <p className="text-mute">nobody outside the workspace</p>}
          {(sharesQ.data?.shares ?? []).map((s) => (
            <div key={s.user_id} className="flex items-center justify-between border-t border-line py-1.5">
              <span className="text-ink">{s.name || s.email} <span className="text-mute">· {s.role}</span></span>
              <Button size="sm" onClick={() => setRemoving(s)}>remove</Button>
            </div>
          ))}
        </div>
        <Field label="Share with (email)">
          <div className="flex items-center gap-2">
            <input className={inputCls + ' min-w-0 flex-1'} value={email} onChange={(e) => setEmail(e.target.value)} placeholder="user@example.com" />
            <select aria-label="Role" className={inputCls + ' w-24 shrink-0'} value={role} onChange={(e) => setRole(e.target.value)}>
              <option value="viewer">viewer</option><option value="editor">editor</option>
            </select>
            <Button size="sm" onClick={() => put.mutate()} disabled={!email.trim() || put.isPending}>Share</Button>
          </div>
        </Field>
        {err && <p className="text-xs text-warn-text">{err}</p>}
      </div>
      {removing && (
        <Confirm title={`Stop sharing with ${removing.name || removing.email}?`} confirm="Remove" pending="Removing…"
          action={() => del.mutateAsync(removing.user_id)} onClose={() => setRemoving(null)}>
          <p className="text-sm text-body">They lose access to this instance.</p>
        </Confirm>
      )}
    </Modal>
  )
}
