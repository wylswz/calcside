import { useMemo, useState } from 'react'
import { ChevronDown, ChevronRight, ShieldAlert, ShieldCheck } from 'lucide-react'
import type { components } from '../api/schema'
import { CodeDiffView } from './CodeDiffView'

type SemanticChange = NonNullable<components['schemas']['PublishReview']['changes']>[number]

function riskRank(c: SemanticChange): number {
  if (c.direction === 'widening') return 0
  if (c.category === 'grants' || c.category === 'rules') return 1
  if (c.category === 'contract') return 2
  if (c.category === 'inputs' || c.category === 'requirements') return 3
  if (c.category === 'sources') return 4
  if (c.direction === 'narrowing') return 5
  return 6
}

function asText(v: unknown): string {
  if (v === undefined || v === null) return ''
  if (typeof v === 'string') return v
  try { return JSON.stringify(v, null, 2) } catch { return String(v) }
}

function inferLang(c: SemanticChange): 'starlark' | 'json' | 'text' {
  const p = c.path ?? ''
  if (p.endsWith('.star') || c.category === 'sources') return 'starlark'
  if (c.category === 'contract' || p.endsWith('.json') || typeof c.before === 'object' || typeof c.after === 'object') return 'json'
  return 'text'
}

function ChangeRow({ change, defaultOpen }: { change: SemanticChange; defaultOpen: boolean }) {
  const hasDiff = change.before !== undefined || change.after !== undefined
  const [open, setOpen] = useState(defaultOpen)
  const widening = change.direction === 'widening'
  const narrowing = change.direction === 'narrowing'

  return (
    <div className="border-b border-line last:border-b-0">
      <button
        type="button"
        disabled={!hasDiff}
        onClick={() => hasDiff && setOpen((o) => !o)}
        className="flex w-full items-center gap-2 px-3 py-2 text-left text-xs hover:bg-surface disabled:cursor-default disabled:hover:bg-transparent"
      >
        {hasDiff ? (
          open
            ? <ChevronDown className="h-3.5 w-3.5 shrink-0 text-mute" strokeWidth={1.5} aria-hidden="true" />
            : <ChevronRight className="h-3.5 w-3.5 shrink-0 text-mute" strokeWidth={1.5} aria-hidden="true" />
        ) : <span className="w-3.5 shrink-0" />}
        <span className="rounded-[3px] border border-line bg-surface px-1.5 py-px font-mono text-[10px] uppercase text-sec">
          {change.category}
        </span>
        <span className="min-w-0 flex-1 truncate font-mono font-medium text-ink" title={change.path}>
          {change.path}
        </span>
        <span className="text-mute">{change.kind}</span>
        {widening && (
          <span className="inline-flex items-center gap-1 rounded-[3px] border border-danger/30 bg-danger/10 px-1.5 py-px font-medium text-danger">
            <ShieldAlert className="h-3 w-3" strokeWidth={1.75} aria-hidden="true" />
            widening
          </span>
        )}
        {narrowing && (
          <span className="inline-flex items-center gap-1 rounded-[3px] border border-live/30 bg-live/10 px-1.5 py-px font-medium text-live">
            <ShieldCheck className="h-3 w-3" strokeWidth={1.75} aria-hidden="true" />
            narrowing
          </span>
        )}
      </button>
      {hasDiff && open && (
        <div className="bg-surface/50 px-3 pb-3 pt-1">
          <CodeDiffView
            before={asText(change.before)}
            after={asText(change.after)}
            language={inferLang(change)}
            maxHeight="260px"
          />
        </div>
      )}
    </div>
  )
}

// GrantDiffCard: risk-ordered semantic change list (widening grants
// surfaced first) with inline unified CodeMirror diffs.
export function GrantDiffCard({ changes }: { changes: SemanticChange[] }) {
  const ordered = useMemo(() => [...changes].sort((a, b) => riskRank(a) - riskRank(b)), [changes])
  const wideningCount = useMemo(() => changes.filter((c) => c.direction === 'widening').length, [changes])

  if (ordered.length === 0) return null

  return (
    <div className="overflow-hidden rounded-[3px] border border-line bg-paper">
      <div className="flex items-center justify-between border-b border-line bg-surface px-3 py-1.5 text-xs">
        <span className="font-medium text-ink">
          Changes <span className="ml-1 font-mono text-sec tabular-nums">{ordered.length}</span>
        </span>
        {wideningCount > 0 && (
          <span className="inline-flex items-center gap-1 font-medium text-danger">
            <ShieldAlert className="h-3.5 w-3.5" strokeWidth={1.75} aria-hidden="true" />
            {wideningCount} grant widening{wideningCount === 1 ? '' : 's'}
          </span>
        )}
      </div>
      <div>
        {ordered.map((c, i) => (
          <ChangeRow key={`${c.category}:${c.path}:${i}`} change={c} defaultOpen={i < 2 || c.direction === 'widening'} />
        ))}
      </div>
    </div>
  )
}
