import { AlertTriangle, Info, OctagonAlert } from 'lucide-react'
import { Button } from '../components/ui'

export interface Finding {
  code?: string
  severity: 'blocker' | 'warning' | 'info'
  category?: string
  section?: string
  message: string
  fix?: string
}

const rank: Record<Finding['severity'], number> = { blocker: 0, warning: 1, info: 2 }

// FindingList: structured readiness & diagnostic list (blockers first,
// warnings second, info last) with optional jump actions. Shared by
// DAG diagnostics, spec validation, and EP-0026 release readiness.
export function FindingList({ findings, onJump }: {
  findings: Finding[]
  onJump?: (finding: Finding) => void
}) {
  if (findings.length === 0) return null
  const ordered = [...findings].sort((a, b) => rank[a.severity] - rank[b.severity])
  return (
    <ul className="divide-y divide-line rounded-[3px] border border-line bg-paper text-xs">
      {ordered.map((f, idx) => {
        const isBlocker = f.severity === 'blocker'
        const isWarn = f.severity === 'warning'
        return (
          <li key={`${f.code ?? ''}:${f.section ?? ''}:${idx}`} className="flex items-start gap-2.5 px-3 py-2">
            <span className={`mt-0.5 shrink-0 ${isBlocker ? 'text-danger' : isWarn ? 'text-warn-text' : 'text-accent'}`} aria-hidden="true">
              {isBlocker
                ? <OctagonAlert className="h-3.5 w-3.5" strokeWidth={1.75} />
                : isWarn
                  ? <AlertTriangle className="h-3.5 w-3.5" strokeWidth={1.75} />
                  : <Info className="h-3.5 w-3.5" strokeWidth={1.75} />}
            </span>
            <div className="min-w-0 flex-1">
              <div className="flex flex-wrap items-center gap-1.5">
                <span className={`font-medium ${isBlocker ? 'text-danger' : isWarn ? 'text-warn-text' : 'text-ink'}`}>
                  {isBlocker ? 'Blocker' : isWarn ? 'Warning' : 'Info'}
                </span>
                {f.category && (
                  <span className="rounded-[3px] border border-line bg-surface px-1 py-px font-mono text-[10px] text-sec">
                    {f.category}
                  </span>
                )}
                {f.section && (
                  <span className="font-mono text-[11px] text-mute">{f.section}</span>
                )}
              </div>
              <p className="mt-0.5 text-body">{f.message}</p>
              {f.fix && <p className="mt-0.5 text-mute">{f.fix}</p>}
            </div>
            {onJump && f.section && (
              <Button variant="ghost" size="xs" onClick={() => onJump(f)}>
                Open
              </Button>
            )}
          </li>
        )
      })}
    </ul>
  )
}
