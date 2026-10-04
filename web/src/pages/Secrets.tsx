import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, ApiError, Secret } from '../api'
import { Button, EmptyRow, Field, Modal, Notice, PageHeader, Tag, fmtTime, inputCls } from '../components/ui'

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
      <div className="space-y-4">
        <Field label="Name (SCREAMING_SNAKE)">
          <input className={inputCls + ' font-mono'} value={name} onChange={(e) => setName(e.target.value)} placeholder="GH_TOKEN" />
        </Field>
        <Field label="Value (write-only — never shown again)">
          <textarea className={inputCls + ' font-mono text-xs'} rows={2} value={value}
            onChange={(e) => setValue(e.target.value)} autoComplete="off" spellCheck={false} />
        </Field>
        <Field label="Allowed domains (optional; one per line; empty = any host allowed by net)">
          <textarea className={inputCls + ' font-mono text-xs'} rows={3} value={domains}
            onChange={(e) => setDomains(e.target.value)} placeholder="api.github.com" />
        </Field>
        {err && <p className="text-xs text-danger">{err}</p>}
        <div className="flex justify-end gap-2 pt-2">
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
      <div className="space-y-4">
        <Field label="Allowed domains (optional; one per line; empty = any host allowed by net)">
          <textarea className={inputCls + ' font-mono text-xs'} rows={3} value={domains} onChange={(e) => setDomains(e.target.value)} />
        </Field>
        <Field label="Rotate value (optional — write-only)">
          <input type="password" className={inputCls + ' font-mono'} value={rotate} onChange={(e) => setRotate(e.target.value)} placeholder="leave blank to keep current value" autoComplete="new-password" />
        </Field>
        {err && <p className="text-xs text-danger">{err}</p>}
        <div className="flex justify-end gap-2 pt-2">
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
      <PageHeader index="04" section="Vault" title="Secrets"
        actions={!disabled && <Button variant="primary" onClick={() => setShowNew(true)}>New secret</Button>}>
        Values are write-only. Use <code className="font-mono text-ink">{'{{secrets.NAME}}'}</code> in net.get/post URL, headers or body — the server injects the value at send time and scrubs it from responses.
      </PageHeader>
      {disabled && (
        <Notice tone="yellow">
          Secrets vault is disabled on this server (no <code>--secret-key</code>). Inline per-instance secrets still work.
        </Notice>
      )}
      {!disabled && (
        <div className="tbl-wrap">
          <table className="tbl">
            <thead>
              <tr><th>Name</th><th>Allowed domains</th><th>Updated</th><th /></tr>
            </thead>
            <tbody>
              {(data?.secrets ?? []).map((s) => (
                <tr key={s.id} className="row-hover">
                  <td className="font-mono text-xs font-medium text-ink">{s.name}</td>
                  <td>
                    <span className="flex flex-wrap gap-1">
                      {s.allowed_domains.length ? s.allowed_domains.map((d) => <Tag key={d}>{d}</Tag>) : <span className="text-xs text-mute">any</span>}
                    </span>
                  </td>
                  <td className="whitespace-nowrap text-xs text-sec">{fmtTime(s.updated_at)}</td>
                  <td className="space-x-2 whitespace-nowrap text-right">
                    <Button size="sm" onClick={() => setEditing(s)}>Edit</Button>
                    <Button variant="danger" size="sm" onClick={() => del.mutate(s.id)}>Delete</Button>
                  </td>
                </tr>
              ))}
              {data && data.secrets.length === 0 && <EmptyRow cols={4}>no secrets</EmptyRow>}
            </tbody>
          </table>
        </div>
      )}
      {showNew && <SecretForm onClose={() => setShowNew(false)} />}
      {editing && <EditDomains s={editing} onClose={() => setEditing(null)} />}
    </div>
  )
}
