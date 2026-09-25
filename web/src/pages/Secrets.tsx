import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, ApiError, Secret } from '../api'
import { Button, Field, Modal, fmtTime, inputCls } from '../components/ui'

function SecretForm({ onClose }: { onClose: () => void }) {
  const qc = useQueryClient()
  const [name, setName] = useState('')
  const [value, setValue] = useState('')
  const [domains, setDomains] = useState('')
  const [err, setErr] = useState('')
  const create = useMutation({
    mutationFn: () =>
      api.post<{ secret: Secret }>('/api/v1/secrets', {
        name, value,
        allowed_domains: domains.split('\n').map((s) => s.trim()).filter(Boolean),
      }),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['secrets'] }); onClose() },
    onError: (e: any) => setErr(e.message),
  })
  return (
    <Modal title="New secret" onClose={onClose}>
      <div className="space-y-3">
        <Field label="Name (SCREAMING_SNAKE)">
          <input className={inputCls + ' font-mono'} value={name} onChange={(e) => setName(e.target.value)} placeholder="GH_TOKEN" />
        </Field>
        <Field label="Value (write-only — never shown again)">
          <textarea className={inputCls + ' font-mono text-xs'} rows={2} value={value}
            onChange={(e) => setValue(e.target.value)} autoComplete="off" spellCheck={false} />
        </Field>
        <Field label="Allowed domains (one per line; *.suffix or host:port)">
          <textarea className={inputCls + ' font-mono text-xs'} rows={3} value={domains}
            onChange={(e) => setDomains(e.target.value)} placeholder="api.github.com" />
        </Field>
        {err && <p className="text-xs text-red-600">{err}</p>}
        <div className="flex justify-end gap-2">
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" disabled={!name || !value || create.isPending} onClick={() => create.mutate()}>Create</Button>
        </div>
      </div>
    </Modal>
  )
}

function EditDomains({ s, onClose }: { s: Secret; onClose: () => void }) {
  const qc = useQueryClient()
  const [domains, setDomains] = useState(s.allowed_domains.join('\n'))
  const [rotate, setRotate] = useState('')
  const [err, setErr] = useState('')
  const save = useMutation({
    mutationFn: () =>
      api.put<{ secret: Secret }>(`/api/v1/secrets/${s.id}`, {
        ...(rotate ? { value: rotate } : {}),
        allowed_domains: domains.split('\n').map((x) => x.trim()).filter(Boolean),
      }),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['secrets'] }); onClose() },
    onError: (e: any) => setErr(e.message),
  })
  return (
    <Modal title={`Edit ${s.name}`} onClose={onClose}>
      <div className="space-y-3">
        <Field label="Allowed domains">
          <textarea className={inputCls + ' font-mono text-xs'} rows={3} value={domains} onChange={(e) => setDomains(e.target.value)} />
        </Field>
        <Field label="Rotate value (optional — write-only)">
          <input type="password" className={inputCls + ' font-mono'} value={rotate} onChange={(e) => setRotate(e.target.value)} placeholder="leave blank to keep current value" autoComplete="new-password" />
        </Field>
        {err && <p className="text-xs text-red-600">{err}</p>}
        <div className="flex justify-end gap-2">
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" disabled={save.isPending} onClick={() => save.mutate()}>Save</Button>
        </div>
      </div>
    </Modal>
  )
}

export default function Secrets() {
  const qc = useQueryClient()
  const [showNew, setShowNew] = useState(false)
  const [editing, setEditing] = useState<Secret | null>(null)
  const { data, error } = useQuery({
    queryKey: ['secrets'],
    queryFn: () => api.get<{ secrets: Secret[] }>('/api/v1/secrets'),
    retry: false,
  })
  const del = useMutation({
    mutationFn: (id: string) => api.del(`/api/v1/secrets/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['secrets'] }),
  })

  const disabled = error instanceof ApiError && error.code === 'secrets_disabled'

  return (
    <div>
      <div className="flex items-center justify-between mb-1">
        <h1 className="text-base font-semibold">Secrets</h1>
        {!disabled && <Button variant="primary" onClick={() => setShowNew(true)}>New secret</Button>}
      </div>
      <p className="text-xs text-gray-500 mb-3">
        Values are write-only. Use <code className="font-mono">{'{{secrets.NAME}}'}</code> in net.get/post URL, headers or body — the server injects the value at send time and scrubs it from responses.
      </p>
      {disabled && (
        <p className="rounded border border-amber-300 dark:border-amber-800 bg-amber-50 dark:bg-amber-950/40 px-3 py-2 text-sm text-amber-800 dark:text-amber-300">
          Secrets vault is disabled on this server (no <code>--secret-key</code>). Inline per-instance secrets still work.
        </p>
      )}
      {!disabled && (
        <div className="overflow-x-auto rounded border border-gray-200 dark:border-gray-800">
          <table className="w-full text-sm">
            <thead className="bg-gray-100 dark:bg-gray-900 text-left text-xs text-gray-600 dark:text-gray-400">
              <tr><th className="px-3 py-2">Name</th><th className="px-3 py-2">Allowed domains</th><th className="px-3 py-2">Updated</th><th className="px-3 py-2" /></tr>
            </thead>
            <tbody className="divide-y divide-gray-200 dark:divide-gray-800">
              {(data?.secrets ?? []).map((s) => (
                <tr key={s.id}>
                  <td className="px-3 py-2 font-mono text-xs">{s.name}</td>
                  <td className="px-3 py-2 font-mono text-xs">{s.allowed_domains.join(', ')}</td>
                  <td className="px-3 py-2 text-xs">{fmtTime(s.updated_at)}</td>
                  <td className="px-3 py-2 text-right space-x-2">
                    <Button className="!px-2 !py-1 text-xs" onClick={() => setEditing(s)}>Edit</Button>
                    <Button variant="danger" className="!px-2 !py-1 text-xs" onClick={() => del.mutate(s.id)}>Delete</Button>
                  </td>
                </tr>
              ))}
              {data && data.secrets.length === 0 && (
                <tr><td colSpan={4} className="px-3 py-6 text-center text-sm text-gray-500">no secrets</td></tr>
              )}
            </tbody>
          </table>
        </div>
      )}
      {showNew && <SecretForm onClose={() => setShowNew(false)} />}
      {editing && <EditDomains s={editing} onClose={() => setEditing(null)} />}
    </div>
  )
}
