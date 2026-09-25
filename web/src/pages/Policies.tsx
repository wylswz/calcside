import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import CodeMirror from '@uiw/react-codemirror'
import { api, Policy } from '../api'
import { Badge, Button, Field, Modal, inputCls } from '../components/ui'

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
    <Modal title={policy ? `Edit ${policy.name}` : 'New policy'} onClose={onClose}>
      <div className="grid grid-cols-2 gap-4">
        <div className="space-y-3">
          <Field label="Name"><input className={inputCls} value={name} onChange={(e) => setName(e.target.value)} /></Field>
          <div className="rounded border border-gray-300 dark:border-gray-700 overflow-hidden">
            <CodeMirror value={rego} height="300px" onChange={setRego} theme={window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'} />
          </div>
          <div className="flex gap-2 items-center">
            <Button onClick={validate}>Validate</Button>
            {validation && (
              <span className={`text-xs ${validation.valid ? 'text-green-600' : 'text-red-600'}`}>
                {validation.valid ? 'valid' : validation.error}
              </span>
            )}
          </div>
          {err && <p className="text-xs text-red-600">{err}</p>}
          <div className="flex justify-end gap-2">
            <Button onClick={onClose}>Cancel</Button>
            <Button variant="primary" disabled={!name || save.isPending} onClick={() => save.mutate()}>Save</Button>
          </div>
        </div>
        <pre className="text-xs font-mono text-gray-600 dark:text-gray-400 whitespace-pre-wrap rounded bg-gray-50 dark:bg-gray-900 p-3 max-h-[420px] overflow-y-auto">{SCHEMA_DOC}</pre>
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
      <div className="flex items-center justify-between mb-1">
        <h1 className="text-base font-semibold">Policies</h1>
        <Button variant="primary" onClick={() => setShowNew(true)}>New policy</Button>
      </div>
      <p className="text-xs text-gray-500 mb-3">
        Policies are snapshotted when an instance is created; changes apply to instances created afterwards.
      </p>
      <div className="overflow-x-auto rounded border border-gray-200 dark:border-gray-800">
        <table className="w-full text-sm">
          <thead className="bg-gray-100 dark:bg-gray-900 text-left text-xs text-gray-600 dark:text-gray-400">
            <tr><th className="px-3 py-2">Name</th><th className="px-3 py-2">Enabled</th><th className="px-3 py-2" /></tr>
          </thead>
          <tbody className="divide-y divide-gray-200 dark:divide-gray-800">
            {(data?.policies ?? []).map((p) => (
              <tr key={p.id}>
                <td className="px-3 py-2">{p.name}</td>
                <td className="px-3 py-2">
                  <button onClick={() => toggle.mutate(p)}>
                    {p.enabled ? <Badge tone="green">enabled</Badge> : <Badge tone="gray">disabled</Badge>}
                  </button>
                </td>
                <td className="px-3 py-2 text-right space-x-2">
                  <Button className="!px-2 !py-1 text-xs" onClick={() => setEditing(p)}>Edit</Button>
                  <Button variant="danger" className="!px-2 !py-1 text-xs" onClick={() => del.mutate(p.id)}>Delete</Button>
                </td>
              </tr>
            ))}
            {data && data.policies.length === 0 && (
              <tr><td colSpan={3} className="px-3 py-6 text-center text-sm text-gray-500">no policies</td></tr>
            )}
          </tbody>
        </table>
      </div>
      {(editing || showNew) && <PolicyEditor policy={editing} onClose={() => { setEditing(null); setShowNew(false) }} />}
    </div>
  )
}
