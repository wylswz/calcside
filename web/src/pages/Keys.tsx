import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, APIKey } from '../api'
import { Button, Field, Modal, fmtTime, inputCls } from '../components/ui'

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
      <div className="flex items-center justify-between mb-3">
        <h1 className="text-base font-semibold">API Keys</h1>
        <Button variant="primary" onClick={() => { setShowNew(true); setSecret(''); setName('') }}>New key</Button>
      </div>
      <div className="overflow-x-auto rounded border border-gray-200 dark:border-gray-800">
        <table className="w-full text-sm">
          <thead className="bg-gray-100 dark:bg-gray-900 text-left text-xs text-gray-600 dark:text-gray-400">
            <tr><th className="px-3 py-2">Name</th><th className="px-3 py-2">Prefix</th><th className="px-3 py-2">Created</th><th className="px-3 py-2">Expires</th><th className="px-3 py-2">Status</th><th className="px-3 py-2" /></tr>
          </thead>
          <tbody className="divide-y divide-gray-200 dark:divide-gray-800">
            {(data?.keys ?? []).map((k) => (
              <tr key={k.id}>
                <td className="px-3 py-2">{k.name}</td>
                <td className="px-3 py-2 font-mono text-xs">{k.prefix}…</td>
                <td className="px-3 py-2 text-xs">{fmtTime(k.created_at)}</td>
                <td className="px-3 py-2 text-xs">{k.expires_at ? fmtTime(k.expires_at) : 'never'}</td>
                <td className="px-3 py-2 text-xs">{k.revoked_at ? 'revoked' : 'active'}</td>
                <td className="px-3 py-2 text-right">
                  {!k.revoked_at && <Button variant="danger" className="!px-2 !py-1 text-xs" onClick={() => revoke.mutate(k.id)}>Revoke</Button>}
                </td>
              </tr>
            ))}
            {data && data.keys.length === 0 && (
              <tr><td colSpan={6} className="px-3 py-6 text-center text-sm text-gray-500">no keys</td></tr>
            )}
          </tbody>
        </table>
      </div>

      {showNew && (
        <Modal title="New API key" onClose={() => setShowNew(false)}>
          {!secret ? (
            <div className="space-y-3">
              <Field label="Name"><input className={inputCls} value={name} onChange={(e) => setName(e.target.value)} /></Field>
              <Field label="Expires in (days; 0 = never)"><input type="number" className={inputCls} value={days} onChange={(e) => setDays(Number(e.target.value))} /></Field>
              <div className="flex justify-end gap-2">
                <Button onClick={() => setShowNew(false)}>Cancel</Button>
                <Button variant="primary" disabled={!name || create.isPending} onClick={() => create.mutate()}>Create</Button>
              </div>
            </div>
          ) : (
            <div className="space-y-3">
              <p className="text-xs text-red-600 dark:text-red-400 font-medium">
                Copy this secret now — it will never be shown again.
              </p>
              <div className="flex gap-2">
                <code className="flex-1 rounded bg-gray-100 dark:bg-gray-800 px-2 py-2 text-xs font-mono break-all">{secret}</code>
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
