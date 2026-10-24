import { Fragment, useState } from 'react'
import { Link } from 'react-router-dom'
import type { AuditEvent, Instance } from '../api'
import { callOutcome, denialReason, instanceName, isConsoleCall } from '../activity'
import { fmtMs, relTime } from '../format'
import { EmptyRow, OutcomeBadge } from './ui'
import { fmtTime } from '../format'

// keyArgs renders the arguments a supervisor scans for: path for fs,
// url/method for net — compact, single line.
function keyArgs(e: AuditEvent): string {
  try {
    const a = JSON.parse(e.args || '{}') as Record<string, any>
    const parts: string[] = []
    for (const k of ['path', 'url', 'method', 'host', 'name', 'alias']) {
      if (a[k] !== undefined) parts.push(String(a[k]))
    }
    return parts.join(' ')
  } catch {
    return e.args
  }
}

function prettyArgs(args: string): string {
  try {
    return JSON.stringify(JSON.parse(args), null, 2)
  } catch {
    return args
  }
}

function CallDetail({ e }: { e: AuditEvent }) {
  return (
    <div className="call-detail space-y-3 text-xs">
      <dl className="grid grid-cols-2 gap-px border border-line bg-line md:grid-cols-4 [&>div]:bg-paper [&>div]:px-3 [&>div]:py-2">
        {([['id', e.id], ['exec', e.exec_id || '—'], ['phase', e.phase || '—'], ['duration', fmtMs(e.duration_ms)]] as const).map(([k, v]) => (
          <div key={k} className="min-w-0">
            <dt className="text-[11px] font-medium uppercase tracking-label text-mute">{k}</dt>
            <dd className="mt-0.5 truncate font-mono text-ink" title={v}>{v}</dd>
          </div>
        ))}
      </dl>
      {e.reason && <pre className="pre">{e.reason}</pre>}
      {e.error && <pre className="pre border-l-4 border-warn !bg-warn/5 text-warn-text">{e.error}</pre>}
      <pre className="pre max-h-64 break-all">{prettyArgs(e.args)}</pre>
    </div>
  )
}

// CallRows renders capability calls as compact rows: cap.op, key args,
// outcome, reason, duration. Click a row for the full args. Shared by
// the instance Activity block and the audit page.
export function CallRows({ events, instances, showInstance }: {
  events: AuditEvent[]
  instances?: Map<string, Instance>
  showInstance?: boolean
}) {
  const [open, setOpen] = useState<string | null>(null)
  const cols = showInstance ? 6 : 5
  return (
    <div className="tbl-wrap">
      <table className="tbl tbl-compact detail-meta">
        <thead>
          <tr>
            <th>When</th>
            {showInstance && <th>Instance</th>}
            <th>Call</th>
            <th>Outcome</th>
            <th>Reason</th>
            <th className="!text-right">Time</th>
          </tr>
        </thead>
        <tbody className="font-mono">
          {events.map((e) => {
            const outcome = callOutcome(e)
            const inst = instances?.get(e.instance_id)
            const consoleCall = isConsoleCall(e)
            return (
              <Fragment key={e.id}>
                <tr className={`row-click ${open === e.id ? 'row-open' : ''} ${consoleCall ? 'opacity-60' : ''}`} onClick={() => setOpen(open === e.id ? null : e.id)}>
                  <td className="whitespace-nowrap font-sans text-sec" title={fmtTime(e.ts)}>
                    <span className="mr-1.5 inline-block w-2 text-mute">{open === e.id ? '▾' : '▸'}</span>{relTime(e.ts)}
                  </td>
                  {showInstance && (
                    <td className="max-w-[160px] truncate font-sans">
                      {inst
                        ? <Link to={`/instances/${inst.id}`} className="link text-xs" onClick={(ev) => ev.stopPropagation()}>{instanceName(inst)}</Link>
                        : <span className="text-xs text-mute">{e.instance_id}</span>}
                    </td>
                  )}
                  <td className="max-w-[280px] truncate text-ink" title={`${e.capability}.${e.op} ${e.args}`}>
                    {consoleCall && <span className="text-mute">your file browsing · </span>}
                    <span className="font-medium">{e.capability}.{e.op}</span>
                    <span className="text-sec"> {keyArgs(e)}</span>
                  </td>
                  <td><OutcomeBadge outcome={outcome} /></td>
                  <td className={`max-w-[200px] truncate ${outcome !== 'ok' ? (outcome === 'denied' ? 'text-danger' : 'text-warn-text') : 'text-mute'}`}>
                    {outcome === 'ok' ? '' : outcome === 'denied' ? denialReason(e.reason) : e.error || e.reason}
                  </td>
                  <td className="text-right text-sec">{fmtMs(e.duration_ms)}</td>
                </tr>
                {open === e.id && (
                  <tr className="bg-surface">
                    <td colSpan={cols} className="!px-4 !py-4 font-sans"><CallDetail e={e} /></td>
                  </tr>
                )}
              </Fragment>
            )
          })}
          {events.length === 0 && <EmptyRow cols={cols}>no calls</EmptyRow>}
        </tbody>
      </table>
    </div>
  )
}
