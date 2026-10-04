import { Fragment, useCallback, useEffect, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import CodeMirror from '@uiw/react-codemirror'
import { python } from '@codemirror/lang-python'
import { api, AuditEvent, ExecResult, Execution, FileEntry, Instance, InstanceInspect, ResourceUsages } from '../api'
import type { SecretSource } from '../api'

interface SpecSecret { ref?: string; source?: SecretSource; allowed_domains?: string[] }
import { Badge, Button, StatusBadge, fmtBytes, fmtCountdown, fmtTime } from '../components/ui'
import { AuditTable, ExecCode } from '../components/AuditTable'

const DEFAULT_CODE = `# Starlark. Capabilities appear as globals when granted.
# fs.read/write/list, net.get/post, print/json/math are always available.
print("hello from calcside")
`

function FileBrowser({ id }: { id: string }) {
  const [dir, setDir] = useState('/work')
  const [file, setFile] = useState<{ path: string; content: string } | null>(null)
  const { data, refetch } = useQuery({
    queryKey: ['files', id, dir],
    queryFn: () => api.get<{ entries: FileEntry[] }>(`/api/v1/instances/${id}/files?path=${encodeURIComponent(dir)}`),
  })
  const open = async (e: FileEntry) => {
    if (e.is_dir) {
      setFile(null)
      setDir(e.path)
    } else {
      const r = await api.get<{ path: string; content: string }>(`/api/v1/instances/${id}/files?path=${encodeURIComponent(e.path)}`)
      setFile({ path: r.path, content: r.content })
    }
  }
  return (
    <div className="space-y-2">
      <div className="flex items-center gap-2 text-xs">
        <button className="text-blue-600 dark:text-blue-400" onClick={() => { setDir('/work'); setFile(null) }}>/work</button>
        {dir !== '/work' && (
          <>
            <span className="text-gray-400">/</span>
            <span className="font-mono">{dir.replace('/work/', '')}</span>
          </>
        )}
        <button className="ml-auto text-gray-500" onClick={() => refetch()}>refresh</button>
      </div>
      <ul className="rounded border border-gray-200 dark:border-gray-800 divide-y divide-gray-200 dark:divide-gray-800 text-xs font-mono max-h-48 overflow-y-auto">
        {(data?.entries ?? []).map((e) => (
          <li key={e.path}>
            <button className="w-full text-left px-2 py-1 hover:bg-gray-100 dark:hover:bg-gray-800" onClick={() => open(e)}>
              {e.is_dir ? '▸ ' : '  '}{e.name}{e.is_dir ? '/' : ` (${e.size}B)`}
            </button>
          </li>
        ))}
        {data && data.entries.length === 0 && <li className="px-2 py-1 text-gray-500">empty</li>}
      </ul>
      {file && (
        <pre className="rounded border border-gray-200 dark:border-gray-800 bg-gray-50 dark:bg-gray-900 p-2 text-xs max-h-64 overflow-auto whitespace-pre-wrap">
          {file.path}{'\n'}{file.content}
        </pre>
      )}
    </div>
  )
}

function AuditTab({ id }: { id: string }) {
  const { data } = useQuery({
    queryKey: ['audit', id],
    queryFn: () => api.get<{ events: AuditEvent[] }>(`/api/v1/audit?instance_id=${id}&limit=200`),
    refetchInterval: 5000,
  })
  return <AuditTable events={data?.events ?? []} />
}

const HISTORY_LEN = 60

function MemorySparkline({ samples, max }: { samples: number[]; max: number }) {
  if (samples.length < 2) return null
  const top = Math.max(max, ...samples) || 1
  const pts = samples.map((v, i) => `${(i / (HISTORY_LEN - 1)) * 100},${30 - (v / top) * 28}`).join(' ')
  return (
    <svg viewBox="0 0 100 30" preserveAspectRatio="none" className="w-full h-10 text-blue-500">
      <polyline points={pts} fill="none" stroke="currentColor" strokeWidth="1.5" vectorEffect="non-scaling-stroke" />
    </svg>
  )
}

function ResourcesCard({ resources, history }: { resources?: ResourceUsages; history: number[] }) {
  if (!resources) {
    return (
      <p className="rounded border border-gray-200 dark:border-gray-800 p-3 text-xs text-gray-500">
        resource usage unavailable — this instance has no cgroup of its own (inproc isolation, or process isolation
        without <span className="font-mono">--instance-memory-max</span>)
      </p>
    )
  }
  const { memory_usage: usage, memory_peak: peak, memory_max: max } = resources
  const pct = max > 0 ? Math.min(100, (usage / max) * 100) : 0
  const peakPct = max > 0 ? Math.min(100, (peak / max) * 100) : 0
  const bar = pct >= 90 ? 'bg-red-500' : pct >= 70 ? 'bg-yellow-500' : 'bg-green-500'
  const stat = (label: string, value: string) => (
    <div>
      <div className="text-gray-500">{label}</div>
      <div className="font-mono text-sm">{value}</div>
    </div>
  )
  return (
    <div className="rounded border border-gray-200 dark:border-gray-800 p-3 space-y-2 text-xs">
      <div className="flex items-baseline">
        <span className="font-semibold text-gray-600 dark:text-gray-400">Memory</span>
        {max > 0 && <span className="ml-auto font-mono">{pct.toFixed(1)}%</span>}
      </div>
      {max > 0 && (
        <div className="relative h-2 rounded bg-gray-200 dark:bg-gray-800 overflow-hidden">
          <div className={`h-full ${bar}`} style={{ width: `${pct}%` }} />
          {peak > 0 && (
            <div className="absolute top-0 h-full w-0.5 bg-gray-700 dark:bg-gray-300" style={{ left: `calc(${peakPct}% - 1px)` }} title={`peak ${fmtBytes(peak)}`} />
          )}
        </div>
      )}
      <div className="grid grid-cols-3 gap-2">
        {stat('current', fmtBytes(usage))}
        {stat('peak', peak > 0 ? fmtBytes(peak) : '—')}
        {stat('limit', max > 0 ? fmtBytes(max) : 'unlimited')}
      </div>
      <MemorySparkline samples={history} max={max} />
    </div>
  )
}

function InspectTab({ id, running, busy }: { id: string; running: boolean; busy: boolean }) {
  const [auto, setAuto] = useState(true)
  const [filter, setFilter] = useState('')
  const [history, setHistory] = useState<number[]>([])
  // Inspect waits on the session's exec lock, so polling pauses while
  // code runs; run() invalidates the query once the exec returns.
  const { data, error, refetch, isFetching, dataUpdatedAt } = useQuery({
    queryKey: ['inspect', id],
    queryFn: () => api.get<InstanceInspect>(`/api/v1/instances/${id}/inspect`),
    enabled: running,
    retry: false,
    refetchInterval: auto && !busy ? 3000 : false,
  })
  useEffect(() => {
    const usage = data?.resources?.memory_usage
    if (usage !== undefined) setHistory((h) => [...h, usage].slice(-HISTORY_LEN))
  }, [data, dataUpdatedAt])
  const q = filter.trim().toLowerCase()
  const names = Object.keys(data?.variables ?? {})
    .filter((n) => !q || n.toLowerCase().includes(q))
    .sort()
  return (
    <div className="space-y-3">
      <div className="flex items-center gap-3 text-xs">
        <span className="text-gray-500">live snapshot of this instance</span>
        {dataUpdatedAt > 0 && <span className="text-gray-400">updated {new Date(dataUpdatedAt).toLocaleTimeString()}</span>}
        {busy && auto && <span className="text-gray-400">paused while code runs</span>}
        <label className="ml-auto flex items-center gap-1 text-gray-500">
          <input type="checkbox" checked={auto} onChange={(e) => setAuto(e.target.checked)} disabled={!running} />
          auto-refresh
        </label>
        <button className="text-gray-500" onClick={() => refetch()} disabled={!running}>
          {isFetching ? 'refreshing…' : 'refresh'}
        </button>
      </div>
      {!running && <p className="text-xs text-gray-500">instance is not running — nothing to inspect</p>}
      {error && <p className="text-xs text-red-600">{(error as Error).message}</p>}
      {data && (
        <>
          <ResourcesCard resources={data.resources} history={history} />
          <div className="space-y-1">
            <div className="flex items-center gap-2 text-xs">
              <span className="font-semibold text-gray-600 dark:text-gray-400">Variables</span>
              <span className="text-gray-400">{Object.keys(data.variables).length} globals · reprs, secrets scrubbed</span>
              <input
                className="ml-auto rounded border border-gray-300 dark:border-gray-700 bg-transparent px-2 py-0.5 font-mono w-40"
                placeholder="filter"
                value={filter}
                onChange={(e) => setFilter(e.target.value)}
              />
            </div>
            <div className="overflow-x-auto rounded border border-gray-200 dark:border-gray-800">
              <table className="w-full text-xs">
                <tbody className="divide-y divide-gray-200 dark:divide-gray-800 font-mono">
                  {names.map((name) => (
                    <tr key={name}>
                      <td className="px-2 py-1 align-top whitespace-nowrap text-gray-600 dark:text-gray-400">{name}</td>
                      <td className="px-2 py-1">
                        <div className="max-h-32 overflow-auto whitespace-pre-wrap break-all">{data.variables[name]}</div>
                      </td>
                    </tr>
                  ))}
                  {names.length === 0 && (
                    <tr><td className="px-2 py-2 text-gray-500">{q ? 'no matching variables' : 'no variables yet — run some code first'}</td></tr>
                  )}
                </tbody>
              </table>
            </div>
          </div>
        </>
      )}
    </div>
  )
}

interface PromptResponse {
  instance_id: string
  prompt: string
  capabilities: string[]
  tools: Record<string, string>
}

function PromptTab({ id, running }: { id: string; running: boolean }) {
  const [prefix, setPrefix] = useState('calcside_')
  const [copied, setCopied] = useState(false)
  const { data, error } = useQuery({
    queryKey: ['instance-prompt', id, prefix],
    queryFn: () => api.get<PromptResponse>(`/api/v1/instances/${id}/prompt?tool_prefix=${encodeURIComponent(prefix)}`),
    enabled: running && /^[A-Za-z0-9_]{0,32}$/.test(prefix),
    retry: false,
  })
  const copy = async () => {
    if (!data) return
    await navigator.clipboard.writeText(data.prompt)
    setCopied(true)
    setTimeout(() => setCopied(false), 1500)
  }
  return (
    <div className="space-y-2">
      <div className="flex items-center gap-2 text-xs">
        <label className="text-gray-500">tool prefix</label>
        <input
          className="rounded border border-gray-300 dark:border-gray-700 bg-transparent px-2 py-1 font-mono w-40"
          value={prefix}
          onChange={(e) => setPrefix(e.target.value)}
        />
        <Button onClick={copy} disabled={!data} className="!px-2 !py-1 text-xs ml-auto">
          {copied ? 'Copied' : 'Copy'}
        </Button>
      </div>
      {!running && <p className="text-xs text-gray-500">instance is not running — no prompt available</p>}
      {error && <p className="text-xs text-red-600">{(error as Error).message}</p>}
      {data && (
        <pre className="rounded border border-gray-200 dark:border-gray-800 bg-gray-50 dark:bg-gray-900 p-3 text-xs font-mono max-h-[32rem] overflow-auto whitespace-pre-wrap">
          {data.prompt}
        </pre>
      )}
    </div>
  )
}

export default function InstanceDetail() {
  const { id = '' } = useParams()
  const qc = useQueryClient()
  const [code, setCode] = useState(DEFAULT_CODE)
  const [result, setResult] = useState<ExecResult | null>(null)
  const [running, setRunning] = useState(false)
  const [tab, setTab] = useState<'execs' | 'audit' | 'inspect' | 'prompt'>('execs')
  const [openExec, setOpenExec] = useState<string | null>(null)

  const { data: inst } = useQuery({
    queryKey: ['instance', id],
    queryFn: () => api.get<{ instance: Instance }>(`/api/v1/instances/${id}`).then((r) => r.instance),
    refetchInterval: 15000,
  })
  const { data: execs, refetch: refetchExecs } = useQuery({
    queryKey: ['executions', id],
    queryFn: () => api.get<{ executions: Execution[] }>(`/api/v1/instances/${id}/executions?limit=50`),
  })

  const run = useCallback(async () => {
    if (running) return
    setRunning(true)
    try {
      const r = await api.post<ExecResult>(`/api/v1/instances/${id}/exec`, { code })
      setResult(r)
      refetchExecs()
      qc.invalidateQueries({ queryKey: ['audit', id] })
      qc.invalidateQueries({ queryKey: ['files', id] })
      qc.invalidateQueries({ queryKey: ['inspect', id] })
    } catch (e: any) {
      setResult({ exec_id: '', output: '', error: { type: 'runtime', message: e.message }, duration_ms: 0, steps: 0 })
    } finally {
      setRunning(false)
    }
  }, [code, id, running, refetchExecs, qc])

  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') {
        e.preventDefault()
        run()
      }
    }
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  }, [run])

  const keepalive = async () => {
    await api.post(`/api/v1/instances/${id}/keepalive`)
    qc.invalidateQueries({ queryKey: ['instance', id] })
  }

  if (!inst) return <p className="text-sm text-gray-500">loading…</p>

  return (
    <div className="space-y-4">
      <div className="flex items-center gap-3">
        <Link to="/" className="text-xs text-gray-500 hover:underline">← instances</Link>
        <span className="font-mono text-sm">{inst.id}</span>
        <StatusBadge status={inst.status} />
        {inst.status === 'running' && (
          <>
            <span className="text-xs text-gray-500">expires in {fmtCountdown(inst.expires_at)}</span>
            <Button onClick={keepalive} className="!px-2 !py-1 text-xs">Keepalive</Button>
          </>
        )}
      </div>

      <div className="grid grid-cols-1 lg:grid-cols-3 gap-4">
        <div className="lg:col-span-2 space-y-3">
          <div className="rounded border border-gray-300 dark:border-gray-700 overflow-hidden">
            <CodeMirror
              value={code}
              height="280px"
              extensions={[python()]}
              onChange={setCode}
              theme={window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'}
            />
          </div>
          <div className="flex items-center gap-2">
            <Button variant="primary" disabled={running || inst.status !== 'running'} onClick={run}>
              {running ? 'Running…' : 'Run (⌘/Ctrl+Enter)'}
            </Button>
            {result && (
              <span className="text-xs text-gray-500">
                {result.duration_ms}ms · {result.steps} steps
                {result.error && <> · <Badge tone="red">{result.error.type}</Badge></>}
              </span>
            )}
          </div>
          {result && (
            <div className="space-y-2">
              <pre className="rounded border border-gray-200 dark:border-gray-800 bg-gray-50 dark:bg-gray-900 p-3 text-xs font-mono max-h-64 overflow-auto whitespace-pre-wrap">
                {result.output || '(no output)'}
              </pre>
              {result.error && (
                <pre className="rounded border border-red-300 dark:border-red-900 bg-red-50 dark:bg-red-950/40 p-3 text-xs font-mono max-h-64 overflow-auto whitespace-pre-wrap text-red-700 dark:text-red-300">
                  error[{result.error.type}]: {result.error.message}
                  {result.error.backtrace ? '\n' + result.error.backtrace : ''}
                </pre>
              )}
            </div>
          )}

          <div className="flex gap-2 border-b border-gray-200 dark:border-gray-800 text-sm">
            <button className={`px-3 py-1.5 ${tab === 'execs' ? 'border-b-2 border-blue-600 font-medium' : 'text-gray-500'}`} onClick={() => setTab('execs')}>Executions</button>
            <button className={`px-3 py-1.5 ${tab === 'audit' ? 'border-b-2 border-blue-600 font-medium' : 'text-gray-500'}`} onClick={() => setTab('audit')}>Audit</button>
            <button className={`px-3 py-1.5 ${tab === 'inspect' ? 'border-b-2 border-blue-600 font-medium' : 'text-gray-500'}`} onClick={() => setTab('inspect')}>Inspect</button>
            <button className={`px-3 py-1.5 ${tab === 'prompt' ? 'border-b-2 border-blue-600 font-medium' : 'text-gray-500'}`} onClick={() => setTab('prompt')}>Agent prompt</button>
          </div>
          {tab === 'execs' && (
            <div className="overflow-x-auto rounded border border-gray-200 dark:border-gray-800">
              <table className="w-full text-xs">
                <thead className="bg-gray-100 dark:bg-gray-900 text-left text-gray-600 dark:text-gray-400">
                  <tr><th className="px-2 py-1.5">Exec</th><th className="px-2 py-1.5">Status</th><th className="px-2 py-1.5">Snippet</th><th className="px-2 py-1.5">ms</th><th className="px-2 py-1.5">Steps</th><th className="px-2 py-1.5">Time</th></tr>
                </thead>
                <tbody className="divide-y divide-gray-200 dark:divide-gray-800 font-mono">
                  {(execs?.executions ?? []).map((x) => (
                    <Fragment key={x.id}>
                      <tr
                        className="cursor-pointer hover:bg-gray-50 dark:hover:bg-gray-900"
                        onClick={() => setOpenExec(openExec === x.id ? null : x.id)}
                      >
                        <td className="px-2 py-1">{openExec === x.id ? '▾ ' : '▸ '}{x.id}</td>
                        <td className="px-2 py-1">{x.status === 'ok' ? <Badge tone="green">ok</Badge> : <Badge tone="red">{x.error_type || 'error'}</Badge>}</td>
                        <td className="px-2 py-1 max-w-[240px] truncate" title={x.code_snippet}>{x.code_snippet}</td>
                        <td className="px-2 py-1">{x.duration_ms}</td>
                        <td className="px-2 py-1">{x.steps}</td>
                        <td className="px-2 py-1">{fmtTime(x.created_at)}</td>
                      </tr>
                      {openExec === x.id && (
                        <tr>
                          <td colSpan={6} className="px-3 py-3 bg-gray-50/60 dark:bg-gray-900/40">
                            <ExecCode execId={x.id} />
                          </td>
                        </tr>
                      )}
                    </Fragment>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          {tab === 'audit' && <AuditTab id={id} />}
          {tab === 'inspect' && <InspectTab key={id} id={id} running={inst.status === 'running'} busy={running} />}
          {tab === 'prompt' && <PromptTab id={id} running={inst.status === 'running'} />}
        </div>

        <div className="space-y-4">
          <div>
            <h3 className="text-xs font-semibold text-gray-600 dark:text-gray-400 mb-2">Files</h3>
            <FileBrowser id={id} />
          </div>
          {Object.keys(inst.spec?.capabilities?.ext ?? {}).length > 0 && (
            <div>
              <h3 className="text-xs font-semibold text-gray-600 dark:text-gray-400 mb-2">Extensions</h3>
              <div className="rounded border border-gray-200 dark:border-gray-800 p-2 text-xs font-mono space-y-1">
                {(Object.entries(inst.spec.capabilities?.ext ?? {}) as [string, { source?: string; sum?: string }][]).map(([alias, e]) => (
                  <div key={alias}>
                    <span className="text-gray-500">{alias}</span> → {e.source}
                    {e.sum && <span className="text-gray-400"> ({e.sum})</span>}
                  </div>
                ))}
              </div>
            </div>
          )}
          {(Object.keys(inst.spec?.env ?? {}).length > 0 || Object.keys(inst.spec?.secrets ?? {}).length > 0) && (
            <div>
              <h3 className="text-xs font-semibold text-gray-600 dark:text-gray-400 mb-2">Env & Secrets</h3>
              <div className="rounded border border-gray-200 dark:border-gray-800 p-2 text-xs font-mono space-y-1">
                {Object.entries(inst.spec.env ?? {}).map(([k, v]) => (
                  <div key={k}><span className="text-gray-500">{k}</span>={v as string}</div>
                ))}
                {(Object.entries(inst.spec.secrets ?? {}) as [string, SpecSecret][]).map(([k, v]) => (
                  <div key={k}>
                    <span className="text-gray-500">{k}</span>
                    <span className="text-gray-400"> {v.source === 'vault' ? `→vault:${v.ref}` : '(inline)'} {(v.allowed_domains ?? []).join(', ') || 'any'}</span>
                  </div>
                ))}
              </div>
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
