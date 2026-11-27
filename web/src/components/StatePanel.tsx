import { useState } from 'react'
import { Check, Copy, Maximize2 } from 'lucide-react'
import type { InstanceInspect } from '../api'
import { Button, EmptyRow, Modal, Notice, SectionTitle, SwapLabel } from './ui'
import { Tooltip } from '../ui/core'
import { isBuiltinGlobal } from '../activity'

function inferVarMeta(repr: string): { type: string; hint?: string } {
  const s = repr.trim()
  if (s === 'None') return { type: 'None' }
  if (s === 'True' || s === 'False') return { type: 'bool' }
  if (/^-?\d+$/.test(s)) return { type: 'int' }
  if (/^-?\d+\.\d+([eE][+-]?\d+)?$/.test(s)) return { type: 'float' }
  if ((s.startsWith('"') && s.endsWith('"')) || (s.startsWith("'") && s.endsWith("'"))) {
    return { type: 'str', hint: `${Math.max(0, s.length - 2)} chars` }
  }
  if (s.startsWith('{') && s.endsWith('}')) return { type: 'dict' }
  if (s.startsWith('[') && s.endsWith(']')) return { type: 'list' }
  if (s.startsWith('(') && s.endsWith(')')) return { type: 'tuple' }
  if (s.startsWith('<function ') || s.startsWith('<built-in function ')) return { type: 'fn' }
  if (s.startsWith('<module ') || s.startsWith('<struct ')) return { type: 'struct' }
  return { type: 'val' }
}

// StatePanel (VariableInspector): shows the instance's live variables
// with inferred Starlark type badges, quick copy, and full-value
// inspection modal. Memory lives in the workbench status bar.
export function StatePanel({ data, error, running, capNames = [], heading = true }: {
  data?: InstanceInspect
  error: Error | null
  running: boolean
  capNames?: string[]
  heading?: boolean
}) {
  const [filter, setFilter] = useState('')
  const [showBuiltins, setShowBuiltins] = useState(false)
  const [copiedName, setCopiedName] = useState<string | null>(null)
  const [expanded, setExpanded] = useState<{ name: string; value: string } | null>(null)
  const q = filter.trim().toLowerCase()
  const allNames = Object.keys(data?.variables ?? {})
  const builtinCount = allNames.filter((n) => isBuiltinGlobal(n, data?.variables?.[n] ?? '', capNames)).length
  const names = allNames
    .filter((n) => showBuiltins || !isBuiltinGlobal(n, data?.variables?.[n] ?? '', capNames))
    .filter((n) => !q || n.toLowerCase().includes(q))
    .sort()

  const copyValue = async (name: string, value: string) => {
    try {
      await navigator.clipboard.writeText(value)
      setCopiedName(name)
      window.setTimeout(() => setCopiedName((cur) => (cur === name ? null : cur)), 1400)
    } catch { /* clipboard unavailable */ }
  }

  return (
    <section aria-label="State" className="space-y-4">
      {heading && <SectionTitle>State</SectionTitle>}
      {!running && <p className="text-xs text-mute">Instance is not running.</p>}
      {error && <Notice tone="warning">{error.message}</Notice>}
      {data && (
        <div>
          <div className="state-variable-tools mb-2 flex items-center gap-2">
            <span className="text-xs font-semibold uppercase tracking-wider text-mute">Variables</span>
            <span className="text-xs tabular-nums text-sec">{names.length}</span>
            {builtinCount > 0 && (
              <button className="text-btn" onClick={() => setShowBuiltins(!showBuiltins)}>
                <SwapLabel show={`${showBuiltins ? 'Hide' : 'Show'} built-ins (${builtinCount})`} options={[`Show built-ins (${builtinCount})`, `Hide built-ins (${builtinCount})`]} />
              </button>
            )}
            <input className="input ml-auto w-36" placeholder="Filter…" aria-label="Filter variables" value={filter} onChange={(e) => setFilter(e.target.value)} />
          </div>
          <div className="tbl-wrap">
            <table className="tbl tbl-compact">
              <tbody>
                {names.map((name) => {
                  const val = data.variables[name]
                  const meta = inferVarMeta(val)
                  const isLong = val.length > 100 || val.includes('\n')
                  return (
                    <tr key={name} className="row-hover">
                      <td className="w-px whitespace-nowrap align-top">
                        <div className="flex items-center gap-1.5">
                          <code className="font-medium text-ink">{name}</code>
                          <span className="rounded-[3px] border border-line bg-surface px-1 py-px font-mono text-[10px] leading-3 text-mute">
                            {meta.type}
                          </span>
                        </div>
                        {meta.hint && <div className="mt-0.5 font-mono text-[10px] text-mute">{meta.hint}</div>}
                      </td>
                      <td className="align-top">
                        <div className="flex items-start gap-1">
                          <pre className="m-0 max-h-24 min-w-0 flex-1 overflow-auto whitespace-pre-wrap break-all font-mono text-[11px] leading-4 text-sec">{val}</pre>
                          <div className="flex shrink-0 items-center gap-0.5">
                            <Tooltip content="Copy value">
                              <Button variant="ghost" size="xs" className="w-5 px-0" aria-label={`Copy ${name}`} onClick={() => void copyValue(name, val)}>
                                {copiedName === name
                                  ? <Check className="h-3 w-3 text-live" strokeWidth={1.75} aria-hidden="true" />
                                  : <Copy className="h-3 w-3 text-mute" strokeWidth={1.5} aria-hidden="true" />}
                              </Button>
                            </Tooltip>
                            {isLong && (
                              <Tooltip content="Expand value">
                                <Button variant="ghost" size="xs" className="w-5 px-0" aria-label={`Expand ${name}`} onClick={() => setExpanded({ name, value: val })}>
                                  <Maximize2 className="h-3 w-3 text-mute" strokeWidth={1.5} aria-hidden="true" />
                                </Button>
                              </Tooltip>
                            )}
                          </div>
                        </div>
                      </td>
                    </tr>
                  )
                })}
                {names.length === 0 && <EmptyRow cols={2}>{q ? 'no matching variables' : 'no variables yet — run some code first'}</EmptyRow>}
              </tbody>
            </table>
          </div>
        </div>
      )}
      {expanded && (
        <Modal title={expanded.name} wide dirty={false} onClose={() => setExpanded(null)}
          footer={
            <Button onClick={() => void copyValue(expanded.name, expanded.value)}>
              <SwapLabel show={copiedName === expanded.name ? 'Copied' : 'Copy value'} options={['Copy value', 'Copied']} />
            </Button>
          }>
          <pre className="pre max-h-[60vh] overflow-auto">{expanded.value}</pre>
        </Modal>
      )}
    </section>
  )
}
