import { useEffect, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { api, InstanceInspect, ResourceUsages } from '../api'
import { EmptyRow, Notice, SectionTitle } from './ui'
import { fmtBytes } from '../format'
import { isBuiltinGlobal } from '../activity'

const HISTORY_LEN = 60

function MemorySparkline({ samples, max }: { samples: number[]; max: number }) {
  if (samples.length < 2) return null
  const top = Math.max(max, ...samples) || 1
  const pts = samples.map((v, i) => `${(i / (HISTORY_LEN - 1)) * 100},${30 - (v / top) * 28}`).join(' ')
  return (
    <svg viewBox="0 0 100 30" preserveAspectRatio="none" className="h-10 w-full text-ink">
      <polyline points={pts} fill="none" stroke="currentColor" strokeWidth="1.5" vectorEffect="non-scaling-stroke" />
    </svg>
  )
}

function MemoryBlock({ resources, history }: { resources?: ResourceUsages; history: number[] }) {
  if (!resources) {
    return (
      <p className="text-xs text-mute">
        memory unavailable — this instance has no cgroup of its own (inproc isolation, or process
        isolation without <span className="font-mono">--instance-memory-max</span>)
      </p>
    )
  }
  const { memory_usage: usage, memory_peak: peak, memory_max: max } = resources
  const pct = max > 0 ? Math.min(100, (usage / max) * 100) : 0
  const peakPct = max > 0 ? Math.min(100, (peak / max) * 100) : 0
  const stat = (label: string, value: string) => (
    <div>
      <div className="text-[11px] font-medium uppercase tracking-label text-mute">{label}</div>
      <div className="mt-1 font-mono text-sm text-ink">{value}</div>
    </div>
  )
  return (
    <div className="space-y-3 text-xs">
      <div className="flex items-center justify-between">
        <span className="font-medium text-ink">memory</span>
        {max > 0 && <span className="font-mono text-sm text-ink">{pct.toFixed(1)}%</span>}
      </div>
      {max > 0 && (
        <div className="relative h-3 bg-surface">
          <div className="h-full bg-ink transition-[width]" style={{ width: `${pct}%` }} />
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

// StatePanel shows the instance's live state: variables (reprs) and
// memory accounting.
export function StatePanel({ id, running, busy, capNames = [] }: { id: string; running: boolean; busy: boolean; capNames?: string[] }) {
  const [auto, setAuto] = useState(true)
  const [filter, setFilter] = useState('')
  const [showBuiltins, setShowBuiltins] = useState(false)
  const [history, setHistory] = useState<number[]>([])
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
  const allNames = Object.keys(data?.variables ?? {})
  const builtinCount = allNames.filter((n) => isBuiltinGlobal(n, data?.variables?.[n] ?? '', capNames)).length
  const names = allNames
    .filter((n) => showBuiltins || !isBuiltinGlobal(n, data?.variables?.[n] ?? '', capNames))
    .filter((n) => !q || n.toLowerCase().includes(q))
    .sort()
  return (
    <section aria-label="State" className="space-y-5">
      <SectionTitle aside={
        <>
          {dataUpdatedAt > 0 && <span className="text-mute">updated {new Date(dataUpdatedAt).toLocaleTimeString()}</span>}
          <label className="flex items-center gap-1.5 text-sec">
            <input type="checkbox" checked={auto} onChange={(e) => setAuto(e.target.checked)} disabled={!running} />
            auto-refresh
          </label>
          <button className="text-btn" onClick={() => refetch()} disabled={!running}>
            {isFetching ? 'refreshing…' : 'refresh'}
          </button>
        </>
      }>State</SectionTitle>
      {busy && auto && <p className="text-xs text-mute">paused while code runs</p>}
      {!running && <p className="text-xs text-mute">instance is not running — nothing to inspect</p>}
      {error && <Notice tone="warning">{(error as Error).message}</Notice>}
      {data && (
        <>
          <MemoryBlock resources={data.resources} history={history} />
          <div>
            <div className="mb-2 flex items-center gap-3">
              <span className="text-xs font-medium text-ink">variables</span>
              <span className="text-[11px] text-mute">{names.length} · reprs, secrets scrubbed</span>
              {builtinCount > 0 && (
                <button className="text-btn" onClick={() => setShowBuiltins(!showBuiltins)}>
                  {showBuiltins ? 'hide' : 'show'} built-ins ({builtinCount})
                </button>
              )}
              <input className="input !w-40 !py-1 font-mono !text-xs ml-auto" placeholder="filter" value={filter} onChange={(e) => setFilter(e.target.value)} />
            </div>
            <div className="overflow-x-auto">
              <table className="tbl tbl-compact">
                <tbody className="font-mono">
                  {names.map((name) => (
                    <tr key={name} className="row-hover">
                      <td className="w-px whitespace-nowrap align-top font-medium text-ink">{name}</td>
                      <td>
                        <div className="max-h-32 overflow-auto whitespace-pre-wrap break-all text-sec">{data.variables[name]}</div>
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
    </section>
  )
}
