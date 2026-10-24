import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '../api'
import { Button, Field, Modal, inputCls } from './ui'

interface Share { user_id: string; email?: string; name?: string; role: string }
interface Member { user_id: string; email: string; name?: string; role: string }

export default function ShareDialog({ instanceId, workspaceId, onClose }: { instanceId: string; workspaceId: string; onClose: () => void }) {
  const qc = useQueryClient()
  const [email, setEmail] = useState('')
  const [role, setRole] = useState('viewer')
  const [err, setErr] = useState('')
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
    onError: (e: Error) => setErr(e.message),
  })

  return (
    <Modal title="Share instance" onClose={onClose} footer={
      <Button variant="primary" onClick={onClose}>Done</Button>
    }>
      <div className="space-y-4 text-sm">
        <div>
          <p className="mb-1 text-xs uppercase tracking-label text-faint">workspace members</p>
          <p className="text-sec">{(membersQ.data?.members ?? []).map((m) => m.name || m.email).join(', ') || '—'}</p>
        </div>
        <div>
          <p className="mb-1 text-xs uppercase tracking-label text-faint">shared with</p>
          {(sharesQ.data?.shares ?? []).length === 0 && <p className="text-mute">nobody outside the workspace</p>}
          {(sharesQ.data?.shares ?? []).map((s) => (
            <div key={s.user_id} className="flex items-center justify-between border-t border-line py-1.5">
              <span className="text-ink">{s.name || s.email} <span className="text-faint">· {s.role}</span></span>
              <Button size="sm" onClick={() => del.mutate(s.user_id)}>remove</Button>
            </div>
          ))}
        </div>
        <Field label="Share with (email)">
          <div className="flex gap-2">
            <input className={inputCls} value={email} onChange={(e) => setEmail(e.target.value)} placeholder="user@example.com" />
            <select className="border border-line bg-paper px-1 py-1 text-xs text-ink" value={role} onChange={(e) => setRole(e.target.value)}>
              <option value="viewer">viewer</option><option value="editor">editor</option>
            </select>
            <Button size="sm" onClick={() => put.mutate()} disabled={!email.trim() || put.isPending}>Share</Button>
          </div>
        </Field>
        {err && <p className="text-xs text-warn-text">{err}</p>}
      </div>
    </Modal>
  )
}
