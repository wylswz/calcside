import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import CodeMirror from '@uiw/react-codemirror'
import { api, Policy } from '../api'
import { Button, EmptyRow, Field, Modal, PageHeader, fmtTime, inputCls, Tag } from '../components/ui'
import { cmTheme } from '../components/codemirror'
import { useRegoEditor } from '../components/editor'

const POLICY_DOC = `deny is a partial set of strings. Any non-empty set denies the call.

Example — deny fs reads under /work/secrets:

  package calcside.hooks

  deny contains msg if {
      input.phase == "before"
      input.capability == "fs"
      input.op in {"read", "write", "append"}
      startswith(input.args.path, "/work/secrets")
      msg := sprintf("path %q is forbidden", [input.args.path])
  }`

const nameRe = /^(?!builtin\.)[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$/

function PolicyEditor({ policy, onClose }: { policy: Policy | null; onClose: () => void }) {
  const qc = useQueryClient()
  const [name, setName] = useState(policy?.name ?? '')
  const [rego, setRego] = useState(policy?.rego ?? 'package calcside.hooks\n\n')
  const [validation, setValidation] = useState<{ valid: boolean; error?: string } | null>(null)
  const [err, setErr] = useState('')
  const [capability, setCapability] = useState('')
  const [op, setOp] = useState('')
  const editor = useRegoEditor(capability, op)
  const operations = editor.metadata?.capabilities.find((c) => c.name === capability)?.ops ?? []

  const save = useMutation({
    mutationFn: async () => {
      if (policy) {
        return api.put<{ policy: Policy }>(`/api/v1/policies/${policy.id}`, { name, rego })
      }
      return api.post<{ policy: Policy }>('/api/v1/policies', { name, rego })
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
          <Button variant="primary" disabled={!nameRe.test(name) || save.isPending} onClick={() => save.mutate()}>Save</Button>
        </div>
      </>
    }>
      <div className="grid gap-6 lg:grid-cols-[minmax(0,1.2fr)_minmax(0,1fr)]">
        <div className="min-w-0 space-y-4">
          <Field label="Name (referenced by spec.policies)">
            <input className={inputCls + ' font-mono'} value={name} onChange={(e) => setName(e.target.value)} placeholder="deny_net_hosts" />
          </Field>
          {name && !nameRe.test(name) && <p className="text-xs text-danger">letters, digits, _ . - (max 64), starting with a letter or digit; builtin. is reserved</p>}
          <div className="border border-ink">
            <div className="flex items-center gap-3 border-b border-ink px-3 py-1.5">
              <span className="eyebrow">Rego</span>
              {validation && (
                <span className={`ml-auto truncate text-xs ${validation.valid ? 'text-accent' : 'text-danger'}`}>
                  {validation.valid ? '● valid' : validation.error}
                </span>
              )}
            </div>
            <CodeMirror value={rego} height="340px" extensions={editor.extensions} onChange={setRego} theme={cmTheme} />
            <div className="border-t border-line px-3 py-1.5 text-[11px] text-mute">
              Ctrl+Space to complete{editor.isFetching ? ' · Loading metadata…' : ''}
              {editor.error && <span className="ml-3 text-warn-text">Completion metadata unavailable</span>}
            </div>
          </div>
          {err && <p className="text-xs text-danger">{err}</p>}
        </div>
        <div className="min-w-0">
          <div className="eyebrow mb-2">Completion context</div>
          <div className="mb-2 grid grid-cols-2 gap-2">
            <Field label="Capability">
              <select className={inputCls} value={capability} onChange={(e) => { setCapability(e.target.value); setOp('') }}>
                <option value="">Select capability</option>
                {(editor.metadata?.capabilities ?? []).map((c) => <option key={c.name} value={c.name}>{c.name}</option>)}
              </select>
            </Field>
            <Field label="Operation">
              <select className={inputCls} value={op} onChange={(e) => setOp(e.target.value)} disabled={!operations.length}>
                <option value="">Select operation</option>
                {operations.map((o) => <option key={o.name} value={o.name}>{o.name}</option>)}
              </select>
            </Field>
          </div>
          <p className="mb-4 text-xs text-mute">Select an operation for args / result field suggestions. This only changes completion hints; Validate compiles the policy, it does not execute it.</p>
          <div className="eyebrow mb-2">Input schema</div>
          <pre className="pre mb-4 max-h-64 text-sec">{editor.fields.length ? editor.fields.map((s) => `${s.name}  ${s.detail ?? ''}`).join('\n') : editor.error ? 'Schema unavailable' : 'Loading schema…'}</pre>
          <pre className="pre max-h-64 text-sec">{POLICY_DOC}</pre>
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
  const del = useMutation({
    mutationFn: (id: string) => api.del(`/api/v1/policies/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['policies'] }),
  })

  return (
    <div>
      <PageHeader index="02" section="Governance" title="Policies"
        actions={<Button variant="primary" onClick={() => setShowNew(true)}>New policy</Button>}>
        Built-in and Rego policies share one selection list at instance creation (spec.policies). Built-in policies run native runtime checks and are read-only; default policies are preselected, not mandatory. Rego policies are snapshotted at creation. Server policies (--policy-dir) always apply.
      </PageHeader>
      <div className="tbl-wrap">
        <table className="tbl">
          <thead>
            <tr><th>Name</th><th>Type</th><th>Updated</th><th /></tr>
          </thead>
          <tbody>
            {(data?.policies ?? []).map((p) => (
              <tr key={p.id} className="row-hover">
                <td>
                  <span className="font-mono font-medium text-ink">{p.name}</span>
                  {p.default && <span className="ml-2"><Tag>Default</Tag></span>}
                  {p.description && <p className="mt-1 max-w-xl text-xs text-mute">{p.description}</p>}
                </td>
                <td><Tag>{p.kind === 'builtin' ? 'Built-in' : 'Rego'}</Tag></td>
                <td className="whitespace-nowrap text-xs text-sec">{p.updated_at ? fmtTime(p.updated_at) : '—'}</td>
                <td className="space-x-2 whitespace-nowrap text-right">
                  {p.kind === 'builtin' ? <span className="text-xs text-mute">Read-only</span> : <>
                    <Button size="sm" onClick={() => setEditing(p)}>Edit</Button>
                    <Button variant="danger" size="sm" onClick={() => del.mutate(p.id)}>Delete</Button>
                  </>}
                </td>
              </tr>
            ))}
            {data && data.policies.length === 0 && <EmptyRow cols={4}>no policies</EmptyRow>}
          </tbody>
        </table>
      </div>
      {(editing || showNew) && <PolicyEditor policy={editing} onClose={() => { setEditing(null); setShowNew(false) }} />}
    </div>
  )
}
