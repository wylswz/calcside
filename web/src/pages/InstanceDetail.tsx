import { Fragment, useCallback, useEffect, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import CodeMirror from '@uiw/react-codemirror'
import { useStarlarkEditor } from '../components/editor'
import { api, AuditEvent, ExecResult, Execution, Instance, InstanceInspect, ResourceUsages } from '../api'
import type { SecretSource } from '../api'

interface SpecSecret { ref?: string; source?: SecretSource; allowed_domains?: string[] }
import { Badge, Button, EmptyRow, Loading, Notice, SectionTitle, StatusBadge, Tag, fmtBytes, fmtCountdown, fmtTime } from '../components/ui'
import { cmTheme } from '../components/codemirror'
import { AuditTable, ExecCode } from '../components/AuditTable'
import { ArtifactBrowser } from '../components/Artifacts'

const DEFAULT_CODE = `# Starlark. Capabilities appear as globals when granted.
# Utilities: json, math, url, csv, base64, hashlib, regex, datetime.
print("hello from calcside")
`


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
    <svg viewBox="0 0 100 30" preserveAspectRatio="none" className="h-10 w-full text-accent">
      <polyline points={pts} fill="none" stroke="currentColor" strokeWidth="1.5" vectorEffect="non-scaling-stroke" />
    </svg>
  )
}

function ResourcesCard({ resources, history }: { resources?: ResourceUsages; history: number[] }) {
  if (!resources) {
    return (
      <Notice>
        <span className="text-xs text-sec">
          resource usage unavailable — this instance has no cgroup of its own (inproc isolation, or process isolation
          without <span className="font-mono">--instance-memory-max</span>)
        </span>
      </Notice>
    )
  }
  const { memory_usage: usage, memory_peak: peak, memory_max: max } = resources
  const pct = max > 0 ? Math.min(100, (usage / max) * 100) : 0
  const peakPct = max > 0 ? Math.min(100, (peak / max) * 100) : 0
  const bar = pct >= 90 ? 'bg-danger' : pct >= 70 ? 'bg-warn' : 'bg-accent'
  const stat = (label: string, value: string) => (
    <div>
      <div className="eyebrow">{label}</div>
      <div className="mt-1 font-mono text-sm text-ink">{value}</div>
    </div>
  )
  return (
    <div className="space-y-3 text-xs">
      <SectionTitle aside={max > 0 && <span className="font-mono text-sm text-ink">{pct.toFixed(1)}%</span>}>Memory</SectionTitle>
      {max > 0 && (
        <div className="relative h-3 bg-surface">
          <div className={`h-full transition-[width] ${bar}`} style={{ width: `${pct}%` }} />
          {peak > 0 && (
            <div className="absolute -top-1 h-5 w-0.5 bg-ink" style={{ left: `calc(${peakPct}% - 1px)` }} title={`peak ${fmtBytes(peak)}`} />
          )}
        </div>
      )}
      <div className="grid grid-cols-3 gap-px border border-line bg-line [&>div]:bg-paper [&>div]:px-3 [&>div]:py-2">
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
    <div className="space-y-6">
      <div className="flex items-center gap-3 text-xs">
        <span className="text-sec">live snapshot of this instance</span>
        {dataUpdatedAt > 0 && <span className="text-mute">updated {new Date(dataUpdatedAt).toLocaleTimeString()}</span>}
        {busy && auto && <span className="text-warn-text">paused while code runs</span>}
        <label className="ml-auto flex items-center gap-1.5 text-sec">
          <input type="checkbox" checked={auto} onChange={(e) => setAuto(e.target.checked)} disabled={!running} />
          auto-refresh
        </label>
        <button className="text-btn" onClick={() => refetch()} disabled={!running}>
          {isFetching ? 'refreshing…' : 'refresh'}
        </button>
      </div>
      {!running && <p className="text-xs text-mute">instance is not running — nothing to inspect</p>}
      {error && <Notice tone="red">{(error as Error).message}</Notice>}
      {data && (
        <>
          <ResourcesCard resources={data.resources} history={history} />
          <div>
            <SectionTitle aside={
              <>
                <span className="text-mute">{Object.keys(data.variables).length} globals · reprs, secrets scrubbed</span>
                <input className="input !w-40 !py-1 font-mono !text-xs" placeholder="filter" value={filter} onChange={(e) => setFilter(e.target.value)} />
              </>
            }>Variables</SectionTitle>
            <div className="overflow-x-auto">
              <table className="tbl tbl-compact">
                <tbody className="font-mono">
                  {names.map((name) => (
                    <tr key={name} className="row-hover">
                      <td className="w-px whitespace-nowrap align-top font-medium text-accent">{name}</td>
                      <td>
                        <div className="max-h-32 overflow-auto whitespace-pre-wrap break-all text-ink">{data.variables[name]}</div>
                      </td>
                    </tr>
                  ))}
                  {names.length === 0 && <EmptyRow cols={2}>{q ? 'no matching variables' : 'no variables yet — run some code first'}</EmptyRow>}
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
        <label className="text-sec">tool prefix</label>
        <input className="input !w-40 !py-1 font-mono !text-xs" value={prefix} onChange={(e) => setPrefix(e.target.value)} />
        <Button onClick={copy} disabled={!data} size="sm" className="ml-auto">
          {copied ? 'Copied' : 'Copy'}
        </Button>
      </div>
      {!running && <p className="text-xs text-mute">instance is not running — no prompt available</p>}
      {error && <Notice tone="red">{(error as Error).message}</Notice>}
      {data && <pre className="pre max-h-[32rem] p-4">{data.prompt}</pre>}
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
  const editor = useStarlarkEditor(id, inst?.status === 'running', running)
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
      qc.invalidateQueries({ queryKey: ['completions', id] })
      qc.invalidateQueries({ queryKey: ['files', id] })
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

  if (!inst) return <Loading />

  const tabs = [
    { k: 'execs', label: 'Executions' },
    { k: 'audit', label: 'Audit' },
    { k: 'inspect', label: 'Inspect' },
    { k: 'prompt', label: 'Agent prompt' },
  ] as const
  const ext = Object.entries(inst.spec?.capabilities?.ext ?? {}) as [string, { source?: string; sum?: string }][]
  const env = Object.entries(inst.spec?.env ?? {}) as [string, string][]
  const secrets = Object.entries(inst.spec?.secrets ?? {}) as [string, SpecSecret][]
  const policies = inst.spec?.policies ?? []

  return (
    <div>
      <div className="mb-8 flex flex-wrap items-end gap-x-8 gap-y-4">
        <div className="min-w-0 flex-1">
          <div className="eyebrow">
            <Link to="/" className="transition-colors hover:text-accent">← <span className="text-accent">01</span> · Instances</Link>
          </div>
          <h1 className="mt-3 truncate font-mono text-[28px] font-medium leading-tight tracking-tight text-ink">{inst.id}</h1>
          <div className="mt-3 flex flex-wrap items-center gap-x-5 gap-y-2 text-xs text-sec">
            <StatusBadge status={inst.status} />
            {inst.status === 'running' && <span>expires in <span className="font-mono text-ink">{fmtCountdown(inst.expires_at)}</span></span>}
            <span>created {fmtTime(inst.created_at)}</span>
            {Object.keys(inst.spec?.capabilities ?? {}).length > 0 && (
              <span className="font-mono">{Object.keys(inst.spec.capabilities ?? {}).join(' · ')}</span>
            )}
          </div>
        </div>
        {inst.status === 'running' && <Button onClick={keepalive}>Keepalive</Button>}
      </div>

      <div className="grid grid-cols-1 gap-10 lg:grid-cols-[minmax(0,1fr)_320px]">
        <div className="min-w-0 space-y-8">
          <div className="border border-ink">
            <div className="flex items-center gap-3 border-b border-ink px-3 py-2">
              <span className="eyebrow">Starlark</span>
              <span className="text-[11px] text-mute">⌘/Ctrl + Enter</span>
              <Button variant="primary" size="sm" className="ml-auto min-w-20" disabled={running || inst.status !== 'running'} onClick={run}>
                {running ? 'Running…' : 'Run ▸'}
              </Button>
            </div>
            <CodeMirror value={code} height="300px" extensions={editor.extensions} onChange={setCode} theme={cmTheme} />
            <div className="flex flex-wrap gap-x-4 border-t border-line px-3 py-1.5 text-[11px] text-mute">
              <span>Ctrl+Space to complete</span>
              {editor.error ? <span className="text-warn-text">Live completions unavailable</span> :
                <span>{running ? 'Using cached symbols while code runs' : editor.isFetching ? 'Refreshing symbols…' : inst.status !== 'running' ? 'Instance is not running' : 'Suggestions use this instance’s runtime'}</span>}
              {editor.truncated && <span className="text-warn-text">Symbol limit reached; some suggestions omitted</span>}
              <button className="text-btn ml-auto" onClick={() => editor.refetch()} disabled={running || inst.status !== 'running' || editor.isFetching}>refresh symbols</button>
            </div>
            {result && (
              <div className="border-t border-ink">
                <div className="flex items-center gap-4 border-b border-line bg-surface px-3 py-1.5 text-xs">
                  <span className="eyebrow">Output</span>
                  <span className="font-mono text-sec">{result.duration_ms}ms · {result.steps} steps</span>
                  {result.error ? <Badge tone="red">{result.error.type}</Badge> : <Badge tone="blue">ok</Badge>}
                </div>
                <pre className="max-h-64 overflow-auto whitespace-pre-wrap p-3 font-mono text-xs leading-relaxed text-ink">
                  {result.output || <span className="text-mute">(no output)</span>}
                </pre>
                {result.error && (
                  <pre className="max-h-64 overflow-auto whitespace-pre-wrap border-t border-danger/40 border-l-4 border-l-danger bg-danger/5 p-3 font-mono text-xs leading-relaxed text-danger">
                    error[{result.error.type}]: {result.error.message}
                    {result.error.backtrace ? '\n' + result.error.backtrace : ''}
                  </pre>
                )}
              </div>
            )}
          </div>

          <div>
            <div className="mb-4 flex gap-6 border-b border-line">
              {tabs.map((t) => (
                <button key={t.k} onClick={() => setTab(t.k)}
                  className={`-mb-px border-b-[3px] pb-2 text-sm transition-colors ${tab === t.k ? 'border-accent font-medium text-ink' : 'border-transparent text-sec hover:text-ink'}`}>
                  {t.label}
                </button>
              ))}
            </div>
            {tab === 'execs' && (
              <div className="tbl-wrap">
                <table className="tbl tbl-compact">
                  <thead>
                    <tr><th>Exec</th><th>Status</th><th>Snippet</th><th>ms</th><th>Steps</th><th>Time</th></tr>
                  </thead>
                  <tbody className="font-mono">
                    {(execs?.executions ?? []).map((x) => (
                      <Fragment key={x.id}>
                        <tr className={`row-click ${openExec === x.id ? 'row-open' : ''}`} onClick={() => setOpenExec(openExec === x.id ? null : x.id)}>
                          <td className="whitespace-nowrap"><span className="mr-1.5 inline-block w-2 text-accent">{openExec === x.id ? '▾' : '▸'}</span>{x.id}</td>
                          <td>{x.status === 'ok' ? <Badge tone="blue">ok</Badge> : <Badge tone="red">{x.error_type || 'error'}</Badge>}</td>
                          <td className="max-w-[240px] truncate text-sec" title={x.code_snippet}>{x.code_snippet}</td>
                          <td>{x.duration_ms}</td>
                          <td>{x.steps}</td>
                          <td className="whitespace-nowrap font-sans text-sec">{fmtTime(x.created_at)}</td>
                        </tr>
                        {openExec === x.id && (
                          <tr className="bg-surface">
                            <td colSpan={6} className="!px-4 !py-4 font-sans">
                              <ExecCode execId={x.id} />
                            </td>
                          </tr>
                        )}
                      </Fragment>
                    ))}
                    {execs && execs.executions.length === 0 && <EmptyRow cols={6}>no executions yet</EmptyRow>}
                  </tbody>
                </table>
              </div>
            )}
            {tab === 'audit' && <AuditTab id={id} />}
            {tab === 'inspect' && <InspectTab key={id} id={id} running={inst.status === 'running'} busy={running} />}
            {tab === 'prompt' && <PromptTab id={id} running={inst.status === 'running'} />}
          </div>
        </div>

        <aside className="space-y-8">
          <div>
            <SectionTitle>Files</SectionTitle>
            <ArtifactBrowser key={id} id={id} running={inst.status === 'running'} busy={running} />
          </div>
          {ext.length > 0 && (
            <div>
              <SectionTitle>Extensions</SectionTitle>
              <dl className="space-y-2 font-mono text-xs">
                {ext.map(([alias, e]) => (
                  <div key={alias}>
                    <dt className="font-medium text-accent">{alias}</dt>
                    <dd className="break-all text-ink">{e.source}</dd>
                    {e.sum && <dd className="break-all text-mute">{e.sum}</dd>}
                  </div>
                ))}
              </dl>
            </div>
          )}
          <div>
            <SectionTitle>Policies</SectionTitle>
            {policies.length === 0
              ? <p className="text-xs text-mute">server policies only</p>
              : (
                <ul className="space-y-1 font-mono text-xs text-ink">
                  {policies.map((n) => <li key={n}>{n} <Tag>{n.startsWith('builtin.') ? 'Built-in' : 'Rego'}</Tag></li>)}
                </ul>
              )}
            <p className="mt-2 text-xs text-mute">selection fixed at creation; Rego sources snapshotted; server policies also apply</p>
          </div>
          {(env.length > 0 || secrets.length > 0) && (
            <div>
              <SectionTitle>Env &amp; Secrets</SectionTitle>
              <dl className="space-y-1.5 font-mono text-xs">
                {env.map(([k, v]) => (
                  <div key={k} className="flex gap-2">
                    <dt className="text-sec">{k}</dt>
                    <dd className="break-all text-ink">{v}</dd>
                  </div>
                ))}
                {secrets.map(([k, v]) => (
                  <div key={k} className="flex flex-wrap gap-x-2">
                    <dt className="font-medium text-ink">{k}</dt>
                    <dd className="text-mute">{v.source === 'vault' ? `→vault:${v.ref}` : '(inline)'} {(v.allowed_domains ?? []).join(', ') || 'any'}</dd>
                  </div>
                ))}
              </dl>
            </div>
          )}
        </aside>
      </div>
    </div>
  )
}
