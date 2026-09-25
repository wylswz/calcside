import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, Instance, Secret } from '../api'
import { Badge, Button, Field, Modal, StatusBadge, fmtCountdown, fmtTime, inputCls } from '../components/ui'

interface SpecDraft {
  ttl_seconds: number
  labels: Record<string, string>
  capabilities: Record<string, any>
  limits: { exec_timeout_ms?: number; max_steps?: number; max_output_bytes?: number }
  env?: Record<string, string>
  secrets?: Record<string, any>
}

interface EnvRow { k: string; v: string }
interface SecretRow {
  name: string
  kind: 'vault' | 'inline'
  value: string      // inline only
  domains: string    // comma/space separated; optional for vault (narrows)
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
  const [envRows, setEnvRows] = useState<EnvRow[]>([])
  const [secretRows, setSecretRows] = useState<SecretRow[]>([])
  const [err, setErr] = useState('')
  const [jsonText, setJsonText] = useState('')
  const [jsonDirty, setJsonDirty] = useState(false)
  const { data: vault } = useQuery({
    queryKey: ['secrets'],
    queryFn: () => api.get<{ secrets: Secret[] }>('/api/v1/secrets'),
    retry: false,
  })

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
    const draft: SpecDraft = { ttl_seconds: ttlMin * 60, labels, capabilities: caps, limits: {} }
    const env: Record<string, string> = {}
    for (const r of envRows) if (r.k) env[r.k] = r.v
    if (Object.keys(env).length) draft.env = env
    const secs: Record<string, any> = {}
    for (const r of secretRows) {
      if (!r.name) continue
      const doms = r.domains.split(/[,\s]+/).map((s) => s.trim()).filter(Boolean)
      if (r.kind === 'vault') {
        secs[r.name] = doms.length ? { ref: r.name, allowed_domains: doms } : { ref: r.name }
      } else {
        secs[r.name] = { value: r.value, allowed_domains: doms }
      }
    }
    if (Object.keys(secs).length) draft.secrets = secs
    return draft
  }

  const jsonShown = useMemo(() => {
    if (jsonDirty) return jsonText
    return JSON.stringify(specFromForm(), null, 2)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [jsonDirty, ttlMin, fsOn, fsQuota, netOn, netHosts, netMethods, labelsText, envRows, secretRows])

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
          <div>
            <div className="flex items-center justify-between mb-1">
              <span className="text-xs font-medium text-gray-600 dark:text-gray-400">env</span>
              <button type="button" className="text-xs text-blue-600 dark:text-blue-400"
                onClick={() => { setJsonDirty(false); setEnvRows([...envRows, { k: '', v: '' }]) }}>+ add</button>
            </div>
            {envRows.map((r, i) => (
              <div key={i} className="flex gap-1 mb-1">
                <input className={inputCls + ' !w-28 font-mono text-xs'} placeholder="NAME" value={r.k}
                  onChange={(e) => { setJsonDirty(false); const rs = [...envRows]; rs[i] = { ...r, k: e.target.value }; setEnvRows(rs) }} />
                <input className={inputCls + ' font-mono text-xs'} placeholder="value" value={r.v}
                  onChange={(e) => { setJsonDirty(false); const rs = [...envRows]; rs[i] = { ...r, v: e.target.value }; setEnvRows(rs) }} />
                <button className="text-xs text-gray-400" onClick={() => { setJsonDirty(false); setEnvRows(envRows.filter((_, j) => j !== i)) }}>×</button>
              </div>
            ))}
          </div>
          <div>
            <div className="flex items-center justify-between mb-1">
              <span className="text-xs font-medium text-gray-600 dark:text-gray-400">secrets</span>
              <button type="button" className="text-xs text-blue-600 dark:text-blue-400"
                onClick={() => { setJsonDirty(false); setSecretRows([...secretRows, { name: '', kind: 'vault', value: '', domains: '' }]) }}>+ add</button>
            </div>
            {secretRows.map((r, i) => {
              const set = (patch: Partial<SecretRow>) => {
                setJsonDirty(false)
                const rs = [...secretRows]
                rs[i] = { ...r, ...patch }
                setSecretRows(rs)
              }
              return (
                <div key={i} className="mb-2 rounded border border-gray-200 dark:border-gray-800 p-2 space-y-1">
                  <div className="flex gap-1 items-center">
                    <select className={inputCls + ' !w-20 text-xs'} value={r.kind} onChange={(e) => set({ kind: e.target.value as 'vault' | 'inline' })}>
                      <option value="vault">vault</option>
                      <option value="inline">inline</option>
                    </select>
                    {r.kind === 'vault' ? (
                      <select className={inputCls + ' font-mono text-xs'} value={r.name} onChange={(e) => set({ name: e.target.value })}>
                        <option value="">pick…</option>
                        {(vault?.secrets ?? []).map((s) => <option key={s.id} value={s.name}>{s.name}</option>)}
                      </select>
                    ) : (
                      <input className={inputCls + ' font-mono text-xs'} placeholder="NAME" value={r.name} onChange={(e) => set({ name: e.target.value })} />
                    )}
                    <button className="ml-auto text-xs text-gray-400" onClick={() => { setJsonDirty(false); setSecretRows(secretRows.filter((_, j) => j !== i)) }}>×</button>
                  </div>
                  {r.kind === 'inline' && (
                    <input type="password" className={inputCls + ' font-mono text-xs'} placeholder="value (never persisted)" value={r.value} onChange={(e) => set({ value: e.target.value })} autoComplete="new-password" />
                  )}
                  <input className={inputCls + ' font-mono text-xs'} value={r.domains} onChange={(e) => set({ domains: e.target.value })}
                    placeholder={r.kind === 'vault' ? 'narrow domains (optional)' : 'allowed domains (required)'} />
                </div>
              )
            })}
          </div>
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
