import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, APIKey } from '../api'
import { Badge, Button, EmptyRow, Field, Modal, Notice, PageHeader, fmtTime, inputCls } from '../components/ui'

export default function Keys() {
  const qc = useQueryClient()
  const [showNew, setShowNew] = useState(false)
  const [name, setName] = useState('')
  const [days, setDays] = useState(90)
  const [secret, setSecret] = useState('')
  const [copied, setCopied] = useState(false)

  const { data } = useQuery({
    queryKey: ['keys'],
    queryFn: () => api.get<{ keys: APIKey[] }>('/api/v1/keys'),
  })
  const create = useMutation({
    mutationFn: () => api.post<{ key: APIKey; secret: string }>('/api/v1/keys', { name, expires_in_seconds: days * 86400 }),
    onSuccess: (r) => {
      setSecret(r.secret)
      setCopied(false)
      qc.invalidateQueries({ queryKey: ['keys'] })
    },
  })
  const revoke = useMutation({
    mutationFn: (id: string) => api.del(`/api/v1/keys/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['keys'] }),
  })

  const copy = async () => {
    await navigator.clipboard.writeText(secret)
    setCopied(true)
  }

  return (
    <div>
      <PageHeader index="03" section="Access" title="API Keys"
        actions={<Button variant="primary" onClick={() => { setShowNew(true); setSecret(''); setName('') }}>New key</Button>}>
        Bearer tokens (<code className="font-mono text-ink">cs_…</code>) for the SDK and agents. The secret is shown once at creation.
      </PageHeader>
      <div className="tbl-wrap">
        <table className="tbl">
          <thead>
            <tr><th>Name</th><th>Prefix</th><th>Created</th><th>Expires</th><th>Status</th><th /></tr>
          </thead>
          <tbody>
            {(data?.keys ?? []).map((k) => (
              <tr key={k.id} className={`row-hover ${k.revoked_at ? 'text-mute' : ''}`}>
                <td className={k.revoked_at ? 'line-through' : 'font-medium text-ink'}>{k.name}</td>
                <td className="font-mono text-xs">{k.prefix}…</td>
                <td className="whitespace-nowrap text-xs text-sec">{fmtTime(k.created_at)}</td>
                <td className="whitespace-nowrap text-xs text-sec">{k.expires_at ? fmtTime(k.expires_at) : 'never'}</td>
                <td>{k.revoked_at ? <Badge tone="gray">revoked</Badge> : <Badge tone="blue">active</Badge>}</td>
                <td className="text-right">
                  {!k.revoked_at && <Button variant="danger" size="sm" onClick={() => revoke.mutate(k.id)}>Revoke</Button>}
                </td>
              </tr>
            ))}
            {data && data.keys.length === 0 && <EmptyRow cols={6}>no keys</EmptyRow>}
          </tbody>
        </table>
      </div>

      {showNew && (
        <Modal title="New API key" onClose={() => setShowNew(false)}>
          {!secret ? (
            <div className="space-y-4">
              <Field label="Name"><input className={inputCls} value={name} onChange={(e) => setName(e.target.value)} /></Field>
              <Field label="Expires in (days; 0 = never)"><input type="number" className={inputCls} value={days} onChange={(e) => setDays(Number(e.target.value))} /></Field>
              <div className="flex justify-end gap-2 pt-2">
                <Button onClick={() => setShowNew(false)}>Cancel</Button>
                <Button variant="primary" disabled={!name || create.isPending} onClick={() => create.mutate()}>Create</Button>
              </div>
            </div>
          ) : (
            <div className="space-y-4">
              <Notice tone="yellow">
                <span className="font-medium text-ink">Copy this secret now — it will never be shown again.</span>
              </Notice>
              <div className="flex gap-2">
                <code className="flex-1 break-all border border-ink bg-code px-3 py-2 font-mono text-xs text-ink">{secret}</code>
                <Button onClick={copy}>{copied ? 'Copied' : 'Copy'}</Button>
              </div>
              <div className="flex justify-end">
                <Button onClick={() => setShowNew(false)}>Done</Button>
              </div>
            </div>
          )}
        </Modal>
      )}
    </div>
  )
}
