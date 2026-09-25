import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, Instance } from '../api'
import { Badge, Button, Field, Modal, StatusBadge, fmtCountdown, fmtTime, inputCls } from '../components/ui'

interface SpecDraft {
  ttl_seconds: number
  labels: Record<string, string>
  capabilities: Record<string, any>
  limits: { exec_timeout_ms?: number; max_steps?: number; max_output_bytes?: number }
}

function NewInstanceDialog({ onClose }: { onClose: () => void }) {
  const qc = useQueryClient()
  const [ttlMin, setTtlMin] = useState(15)
  const [fsOn, setFsOn] = useState(true)
  const [fsQuota, setFsQuota] = useState('67108864')
  const [netOn, setNetOn] = useState(false)
  const [netHosts, setNetHosts] = useState('')
  const [netMethods, setNetMethods] = useState('GET,POST')
  const [labelsText, setLabelsText] = useState('')
  const [err, setErr] = useState('')
  const [jsonText, setJsonText] = useState('')
  const [jsonDirty, setJsonDirty] = useState(false)

  const specFromForm = (): SpecDraft => {
    const labels: Record<string, string> = {}
    for (const part of labelsText.split(',')) {
      const [k, v] = part.split('=').map((s) => s.trim())
      if (k) labels[k] = v ?? ''
    }
    const caps: Record<string, any> = {}
    if (fsOn) caps.fs = fsQuota ? { quota_bytes: Number(fsQuota) } : {}
    if (netOn) {
      caps.net = {
        allow_hosts: netHosts.split('\n').map((s) => s.trim()).filter(Boolean),
        methods: netMethods.split(',').map((s) => s.trim()).filter(Boolean),
      }
    }
    return { ttl_seconds: ttlMin * 60, labels, capabilities: caps, limits: {} }
  }

  const jsonShown = useMemo(() => {
    if (jsonDirty) return jsonText
    return JSON.stringify(specFromForm(), null, 2)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [jsonDirty, ttlMin, fsOn, fsQuota, netOn, netHosts, netMethods, labelsText])

  const create = useMutation({
    mutationFn: (spec: any) => api.post<{ instance: Instance }>('/api/v1/instances', spec),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['instances'] })
      onClose()
    },
    onError: (e: any) => setErr(e.message),
  })

  const submit = () => {
    setErr('')
    let spec: any
    if (jsonDirty) {
      try {
        spec = JSON.parse(jsonText)
      } catch {
        setErr('advanced JSON is invalid')
        return
      }
    } else {
      spec = specFromForm()
    }
    create.mutate(spec)
  }

  return (
    <Modal title="New instance" onClose={onClose}>
      <div className="grid grid-cols-2 gap-4">
        <div className="space-y-3">
          <Field label="TTL (minutes)">
            <input type="number" min={1} className={inputCls} value={ttlMin} onChange={(e) => { setJsonDirty(false); setTtlMin(Number(e.target.value)) }} />
          </Field>
          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={fsOn} onChange={(e) => { setJsonDirty(false); setFsOn(e.target.checked) }} />
            fs capability
          </label>
          {fsOn && (
            <Field label="fs quota (bytes)">
              <input className={inputCls} value={fsQuota} onChange={(e) => { setJsonDirty(false); setFsQuota(e.target.value) }} />
            </Field>
          )}
          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={netOn} onChange={(e) => { setJsonDirty(false); setNetOn(e.target.checked) }} />
            net capability
          </label>
          {netOn && (
            <>
              <Field label="allow_hosts (one per line; *.suffix or host:port)">
                <textarea className={inputCls + ' font-mono text-xs'} rows={3} value={netHosts} onChange={(e) => { setJsonDirty(false); setNetHosts(e.target.value) }} />
              </Field>
              <Field label="methods">
                <input className={inputCls} value={netMethods} onChange={(e) => { setJsonDirty(false); setNetMethods(e.target.value) }} />
              </Field>
            </>
          )}
          <Field label="labels (k=v, comma separated)">
            <input className={inputCls} value={labelsText} onChange={(e) => { setJsonDirty(false); setLabelsText(e.target.value) }} placeholder="team=agents, env=dev" />
          </Field>
        </div>
        <div>
          <Field label="spec (advanced JSON — editable)">
            <textarea
              className={inputCls + ' font-mono text-xs h-[340px]'}
              value={jsonShown}
              onChange={(e) => { setJsonText(e.target.value); setJsonDirty(true) }}
            />
          </Field>
        </div>
      </div>
      {err && <p className="mt-2 text-xs text-red-600">{err}</p>}
      <div className="mt-4 flex justify-end gap-2">
        <Button onClick={onClose}>Cancel</Button>
        <Button variant="primary" disabled={create.isPending} onClick={submit}>
          Create
        </Button>
      </div>
    </Modal>
  )
}

export default function Instances() {
  const qc = useQueryClient()
  const [showNew, setShowNew] = useState(false)
  const { data, isLoading } = useQuery({
    queryKey: ['instances'],
    queryFn: () => api.get<{ instances: Instance[] }>('/api/v1/instances'),
    refetchInterval: 15000,
  })
  const del = useMutation({
    mutationFn: (id: string) => api.del(`/api/v1/instances/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['instances'] }),
  })

  return (
    <div>
      <div className="flex items-center justify-between mb-3">
        <h1 className="text-base font-semibold">Instances</h1>
        <Button variant="primary" onClick={() => setShowNew(true)}>New instance</Button>
      </div>
      {isLoading && <p className="text-sm text-gray-500">loading…</p>}
      <div className="overflow-x-auto rounded border border-gray-200 dark:border-gray-800">
        <table className="w-full text-sm">
          <thead className="bg-gray-100 dark:bg-gray-900 text-left text-xs text-gray-600 dark:text-gray-400">
            <tr>
              <th className="px-3 py-2">ID</th>
              <th className="px-3 py-2">Status</th>
              <th className="px-3 py-2">Capabilities</th>
              <th className="px-3 py-2">Labels</th>
              <th className="px-3 py-2">Created</th>
              <th className="px-3 py-2">Expires</th>
              <th className="px-3 py-2" />
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-200 dark:divide-gray-800">
            {(data?.instances ?? []).map((i) => (
              <tr key={i.id} className="hover:bg-gray-50 dark:hover:bg-gray-900/60">
                <td className="px-3 py-2 font-mono text-xs">
                  <Link to={`/instances/${i.id}`} className="text-blue-600 dark:text-blue-400 hover:underline">{i.id}</Link>
                </td>
                <td className="px-3 py-2"><StatusBadge status={i.status} /></td>
                <td className="px-3 py-2 text-xs">{Object.keys(i.spec?.capabilities ?? {}).join(', ') || '—'}</td>
                <td className="px-3 py-2 text-xs">
                  {Object.entries(i.labels ?? {}).map(([k, v]) => (
                    <Badge key={k} tone="gray">{k}={v}</Badge>
                  ))}
                </td>
                <td className="px-3 py-2 text-xs">{fmtTime(i.created_at)}</td>
                <td className="px-3 py-2 text-xs">{i.status === 'running' ? fmtCountdown(i.expires_at) : '—'}</td>
                <td className="px-3 py-2 text-right">
                  {i.status === 'running' && (
                    <Button variant="danger" className="!px-2 !py-1 text-xs" onClick={() => del.mutate(i.id)}>Delete</Button>
                  )}
                </td>
              </tr>
            ))}
            {data && data.instances.length === 0 && (
              <tr><td colSpan={7} className="px-3 py-6 text-center text-sm text-gray-500">no instances</td></tr>
            )}
          </tbody>
        </table>
      </div>
      {showNew && <NewInstanceDialog onClose={() => setShowNew(false)} />}
    </div>
  )
}
