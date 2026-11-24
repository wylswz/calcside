import { useState } from 'react'
import type { InstanceInspect } from '../api'
import { EmptyRow, Notice, SectionTitle, SwapLabel } from './ui'
import { isBuiltinGlobal } from '../activity'

// StatePanel shows the instance's live variables (reprs) — memory
// accounting moved to the workbench status bar (`MemoryStatus`), which
// shares the same inspect snapshot (see useInstanceInspect in
// InstanceDetail).
export function StatePanel({ data, error, running, capNames = [], heading = true }: {
  data?: InstanceInspect
  error: Error | null
  running: boolean
  capNames?: string[]
  heading?: boolean
}) {
  const [filter, setFilter] = useState('')
  const [showBuiltins, setShowBuiltins] = useState(false)
  const q = filter.trim().toLowerCase()
  const allNames = Object.keys(data?.variables ?? {})
  const builtinCount = allNames.filter((n) => isBuiltinGlobal(n, data?.variables?.[n] ?? '', capNames)).length
  const names = allNames
    .filter((n) => showBuiltins || !isBuiltinGlobal(n, data?.variables?.[n] ?? '', capNames))
    .filter((n) => !q || n.toLowerCase().includes(q))
    .sort()
  return (
    <section aria-label="State" className="space-y-5">
      {heading && <SectionTitle>State</SectionTitle>}
      {!running && <p className="text-xs text-mute">Instance is not running.</p>}
      {error && <Notice tone="warning">{error.message}</Notice>}
      {data && (
        <div>
          <div className="state-variable-tools mb-2 flex items-center gap-3">
            <span className="text-sm font-medium text-ink">Variables</span>
            <span className="text-xs tabular-nums text-sec">{names.length}</span>
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
      )}
    </section>
  )
}
