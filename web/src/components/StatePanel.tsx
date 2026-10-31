import { useEffect, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { api, InstanceInspect, ResourceUsages } from '../api'
import { EmptyRow, Notice, SectionTitle, SwapLabel } from './ui'
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
      <details className="state-resource-note">
        <summary>Memory reporting unavailable</summary>
        <p className="mt-1 text-xs text-mute">Memory reporting requires process isolation with a configured memory limit.</p>
      </details>
    )
  }
  const { memory_usage: usage, memory_peak: peak, memory_max: max } = resources
  const pct = max > 0 ? Math.min(100, (usage / max) * 100) : 0
  const peakPct = max > 0 ? Math.min(100, (peak / max) * 100) : 0
  const stat = (label: string, value: string) => (
    <div>
      <div className="text-xs font-medium text-sec">{label}</div>
      <div className="mt-1 text-sm tabular-nums text-ink">{value}</div>
    </div>
  )
  return (
    <div className="space-y-3 text-xs">
      <div className="flex items-center justify-between">
        <span className="font-medium text-ink">Memory</span>
        {max > 0 && <span className="text-sm tabular-nums text-ink">{pct.toFixed(1)}%</span>}
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
        {stat('Current', fmtBytes(usage))}
        {stat('Peak', peak > 0 ? fmtBytes(peak) : '—')}
        {stat('Limit', max > 0 ? fmtBytes(max) : 'unlimited')}
      </div>
      <MemorySparkline samples={history} max={max} />
    </div>
  )
}

// StatePanel shows the instance's live state: variables (reprs) and
// memory accounting. It never polls: the parent passes a revision that
// changes with each execution, and only then does state refresh.
export function StatePanel({ id, running, revision, capNames = [] }: { id: string; running: boolean; revision?: string; capNames?: string[] }) {
  const [filter, setFilter] = useState('')
  const [showBuiltins, setShowBuiltins] = useState(false)
  const [history, setHistory] = useState<number[]>([])
  const { data, error, refetch } = useQuery({
    queryKey: ['inspect', id],
    queryFn: () => api.get<InstanceInspect>(`/api/v1/instances/${id}/inspect`),
    enabled: running,
    retry: false,
  })
  // A changed revision marks a new execution. Refetching keeps the old
  // data on screen, so rows never disappear while the new snapshot arrives.
  const seen = useRef(revision)
  useEffect(() => {
    if (!running || revision === undefined || seen.current === revision) return
    seen.current = revision
    refetch()
  }, [revision, running, refetch])
  useEffect(() => {
    const usage = data?.resources?.memory_usage
    if (usage !== undefined) setHistory((h) => [...h, usage].slice(-HISTORY_LEN))
  }, [data])
  const q = filter.trim().toLowerCase()
  const allNames = Object.keys(data?.variables ?? {})
  const builtinCount = allNames.filter((n) => isBuiltinGlobal(n, data?.variables?.[n] ?? '', capNames)).length
  const names = allNames
    .filter((n) => showBuiltins || !isBuiltinGlobal(n, data?.variables?.[n] ?? '', capNames))
    .filter((n) => !q || n.toLowerCase().includes(q))
    .sort()
  return (
    <section aria-label="State" className="space-y-5">
      <SectionTitle>State</SectionTitle>
      {!running && <p className="text-xs text-mute">instance is not running — nothing to inspect</p>}
      {error && <Notice tone="warning">{(error as Error).message}</Notice>}
      {data && (
        <>
          <MemoryBlock resources={data.resources} history={history} />
          <div>
            <div className="state-variable-tools mb-2 flex items-center gap-3">
              <span className="text-sm font-medium text-ink">Variables</span>
              <span className="text-xs tabular-nums text-sec" title="Values are representations with known secrets redacted.">{names.length}</span>
              {builtinCount > 0 && (
                <button className="text-btn" onClick={() => setShowBuiltins(!showBuiltins)}>
                  <SwapLabel show={`${showBuiltins ? 'Hide' : 'Show'} built-ins (${builtinCount})`} options={[`Show built-ins (${builtinCount})`, `Hide built-ins (${builtinCount})`]} />
                </button>
              )}
              <input className="input w-40 ml-auto" placeholder="Filter variables" aria-label="Filter variables" value={filter} onChange={(e) => setFilter(e.target.value)} />
            </div>
            <div className="tbl-wrap">
              <table className="tbl tbl-compact">
                <tbody>
                  {names.map((name) => (
                    <tr key={name} className="row-hover">
                      <td className="w-px whitespace-nowrap align-top text-ink"><code>{name}</code></td>
                      <td>
                        <pre className="m-0 max-h-32 overflow-auto whitespace-pre-wrap break-all text-sec">{data.variables[name]}</pre>
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
