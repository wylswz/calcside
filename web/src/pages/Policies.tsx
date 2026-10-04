import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import CodeMirror from '@uiw/react-codemirror'
import { api, Policy } from '../api'
import { Button, EmptyRow, Field, Modal, PageHeader, inputCls } from '../components/ui'
import { cmTheme } from '../components/codemirror'

const SCHEMA_DOC = `Input document available to every policy:

  phase      "before" | "after"
  user       {id, email}
  instance   {id, labels}
  exec_id    string
  capability "fs" | "net" | "io"
  op         e.g. "read", "write", "get"
  args       normalized op args (e.g. {"path": "/work/a"})
  result     only in "after": {error: str|null, meta: {...}}

deny is a partial set of strings. Any non-empty set denies the call.

Example — deny fs reads under /work/secrets:

  package calcside.hooks

  deny contains msg if {
      input.phase == "before"
      input.capability == "fs"
      input.op in {"read", "write", "append"}
      startswith(input.args.path, "/work/secrets")
      msg := sprintf("path %q is forbidden", [input.args.path])
  }`

function PolicyEditor({ policy, onClose }: { policy: Policy | null; onClose: () => void }) {
  const qc = useQueryClient()
  const [name, setName] = useState(policy?.name ?? '')
  const [rego, setRego] = useState(policy?.rego ?? 'package calcside.hooks\n\n')
  const [validation, setValidation] = useState<{ valid: boolean; error?: string } | null>(null)
  const [err, setErr] = useState('')

  const save = useMutation({
    mutationFn: async () => {
      if (policy) {
        return api.put<{ policy: Policy }>(`/api/v1/policies/${policy.id}`, { name, rego })
      }
      return api.post<{ policy: Policy }>('/api/v1/policies', { name, rego, enabled: true })
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['policies'] })
      onClose()
    },
    onError: (e: any) => setErr(e.message),
  })

  const validate = async () => {
    const r = await api.post<{ valid: boolean; error?: string }>('/api/v1/policies/validate', { rego })
    setValidation(r)
  }

  return (
    <Modal title={policy ? `Edit ${policy.name}` : 'New policy'} onClose={onClose} wide footer={
      <>
        <Button onClick={validate}>Validate</Button>
        <div className="ml-auto flex gap-2">
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" disabled={!name || save.isPending} onClick={() => save.mutate()}>Save</Button>
        </div>
      </>
    }>
      <div className="grid gap-6 lg:grid-cols-[minmax(0,1.2fr)_minmax(0,1fr)]">
        <div className="min-w-0 space-y-4">
          <Field label="Name"><input className={inputCls} value={name} onChange={(e) => setName(e.target.value)} /></Field>
          <div className="border border-ink">
            <div className="flex items-center gap-3 border-b border-ink px-3 py-1.5">
              <span className="eyebrow">Rego</span>
              {validation && (
                <span className={`ml-auto truncate text-xs ${validation.valid ? 'text-accent' : 'text-danger'}`}>
                  {validation.valid ? '● valid' : validation.error}
                </span>
              )}
            </div>
            <CodeMirror value={rego} height="340px" onChange={setRego} theme={cmTheme} />
          </div>
          {err && <p className="text-xs text-danger">{err}</p>}
        </div>
        <div className="min-w-0">
          <div className="eyebrow mb-2">Input schema</div>
          <pre className="pre max-h-[440px] text-sec">{SCHEMA_DOC}</pre>
        </div>
      </div>
    </Modal>
  )
}

export default function Policies() {
  const qc = useQueryClient()
  const [editing, setEditing] = useState<Policy | null>(null)
  const [showNew, setShowNew] = useState(false)
  const { data } = useQuery({
    queryKey: ['policies'],
    queryFn: () => api.get<{ policies: Policy[] }>('/api/v1/policies'),
  })
  const toggle = useMutation({
    mutationFn: (p: Policy) => api.put(`/api/v1/policies/${p.id}`, { enabled: !p.enabled }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['policies'] }),
  })
  const del = useMutation({
    mutationFn: (id: string) => api.del(`/api/v1/policies/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['policies'] }),
  })

  return (
    <div>
      <PageHeader index="02" section="Governance" title="Policies"
        actions={<Button variant="primary" onClick={() => setShowNew(true)}>New policy</Button>}>
        Rego hooks evaluated before and after every capability call; any deny vetoes the op. Policies are snapshotted when an instance is created; changes apply to instances created afterwards.
      </PageHeader>
      <div className="tbl-wrap">
        <table className="tbl">
          <thead>
            <tr><th>Name</th><th>Enabled</th><th /></tr>
          </thead>
          <tbody>
            {(data?.policies ?? []).map((p) => (
              <tr key={p.id} className="row-hover">
                <td className="font-medium text-ink">{p.name}</td>
                <td>
                  <button onClick={() => toggle.mutate(p)} title="Toggle" className="inline-flex items-center gap-2">
                    <span className={`relative h-4 w-7 border transition-colors ${p.enabled ? 'border-accent bg-accent' : 'border-line-strong bg-surface'}`}>
                      <span className={`absolute top-0.5 h-2.5 w-2.5 transition-all ${p.enabled ? 'left-[14px] bg-white' : 'left-0.5 bg-mute'}`} />
                    </span>
                    {p.enabled ? <span className="text-xs font-medium text-ink">enabled</span> : <span className="text-xs text-mute">disabled</span>}
                  </button>
                </td>
                <td className="space-x-2 whitespace-nowrap text-right">
                  <Button size="sm" onClick={() => setEditing(p)}>Edit</Button>
                  <Button variant="danger" size="sm" onClick={() => del.mutate(p.id)}>Delete</Button>
                </td>
              </tr>
            ))}
            {data && data.policies.length === 0 && <EmptyRow cols={3}>no policies</EmptyRow>}
          </tbody>
        </table>
      </div>
      {(editing || showNew) && <PolicyEditor policy={editing} onClose={() => { setEditing(null); setShowNew(false) }} />}
    </div>
  )
}
