import { Fragment, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import CodeMirror from '@uiw/react-codemirror'
import { python } from '@codemirror/lang-python'
import { api, AuditEvent } from '../api'
import type { Execution } from '../api'
import { DecisionBadge, EmptyRow, Loading, Notice } from './ui'
import { cmTheme } from './codemirror'

interface ExecutionDetail {
  execution: Execution
  code: string
}

function prettyArgs(args: string): string {
  try {
    return JSON.stringify(JSON.parse(args), null, 2)
  } catch {
    return args // truncated JSON — show raw
  }
}

// ExecCode lazily fetches and displays the full code of one execution.
export function ExecCode({ execId }: { execId: string }) {
  const [copied, setCopied] = useState(false)
  const { data, error, isLoading } = useQuery({
    queryKey: ['execution', execId],
    queryFn: () => api.get<ExecutionDetail>(`/api/v1/executions/${execId}`),
    enabled: execId !== '',
    retry: false,
  })
  const copy = async () => {
    if (!data?.code) return
    await navigator.clipboard.writeText(data.code)
    setCopied(true)
    setTimeout(() => setCopied(false), 1500)
  }
  return (
    <div>
      <div className="mb-2 flex items-center gap-3 text-xs">
        <span className="eyebrow">exec</span>
        <span className="font-mono text-ink">{execId || '—'}</span>
        {data?.code && (
          <button className="text-btn" onClick={copy}>
            {copied ? 'copied' : 'copy'}
          </button>
        )}
      </div>
      {execId === '' && <p className="text-xs text-mute">no execution linked to this event</p>}
      {isLoading && <Loading />}
      {error && <Notice tone="red">{(error as Error).message}</Notice>}
      {data && data.code === '' && (
        <p className="text-xs text-mute">code was not recorded for this execution (pre-dates code retention)</p>
      )}
      {data && data.code !== '' && (
        <div className="border border-line">
          <CodeMirror
            value={data.code}
            readOnly
            editable={false}
            maxHeight="24rem"
            extensions={[python()]}
            theme={cmTheme}
            basicSetup={{ highlightActiveLine: false, highlightActiveLineGutter: false, foldGutter: false }}
          />
        </div>
      )}
    </div>
  )
}

function EventDetail({ e }: { e: AuditEvent }) {
  return (
    <div className="space-y-4 text-xs">
      <dl className="grid grid-cols-2 gap-px border border-line bg-line md:grid-cols-3 [&>div]:bg-paper [&>div]:px-3 [&>div]:py-2">
        {([['id', e.id], ['user', e.user_id], ['instance', e.instance_id], ['exec', e.exec_id || '—'], ['phase', e.phase || '—'], ['duration', `${e.duration_ms}ms`]] as const).map(([k, v]) => (
          <div key={k} className="min-w-0">
            <dt className="eyebrow">{k}</dt>
            <dd className="mt-0.5 truncate font-mono text-ink" title={v}>{v}</dd>
          </div>
        ))}
      </dl>
      {e.reason && (
        <div>
          <div className="eyebrow mb-1">reason</div>
          <pre className="pre">{e.reason}</pre>
        </div>
      )}
      {e.error && (
        <div>
          <div className="eyebrow mb-1">error</div>
          <pre className="pre border-l-4 border-danger !bg-danger/5 text-danger">{e.error}</pre>
        </div>
      )}
      <div>
        <div className="eyebrow mb-1">args</div>
        <pre className="pre max-h-64 break-all">{prettyArgs(e.args)}</pre>
      </div>
      <div>
        <ExecCode execId={e.exec_id} />
      </div>
    </div>
  )
}

// AuditTable renders audit events with expandable rows: click a row to
// see full args, reason/error, and the complete executed code.
export function AuditTable({ events, showScope }: { events: AuditEvent[]; showScope?: boolean }) {
  const [open, setOpen] = useState<string | null>(null)
  const cols = showScope ? 9 : 8
  return (
    <div className="tbl-wrap">
      <table className="tbl tbl-compact">
        <thead>
          <tr>
            <th>Time</th>
            {showScope && <th>Instance</th>}
            <th>Exec</th>
            <th>Cap</th>
            <th>Op</th>
            <th>Args</th>
            <th>Decision</th>
            <th>Reason</th>
            <th className="!text-right">ms</th>
          </tr>
        </thead>
        <tbody className="font-mono">
          {events.map((e) => (
            <Fragment key={e.id}>
              <tr className={`row-click ${open === e.id ? 'row-open' : ''}`} onClick={() => setOpen(open === e.id ? null : e.id)}>
                <td className="whitespace-nowrap font-sans text-sec"><span className="mr-1.5 inline-block w-2 text-accent">{open === e.id ? '▾' : '▸'}</span>{new Date(e.ts).toLocaleString()}</td>
                {showScope && <td className="max-w-[150px] truncate" title={e.instance_id}>{e.instance_id}</td>}
                <td className="max-w-[150px] truncate" title={e.exec_id}>{e.exec_id}</td>
                <td className="font-medium text-ink">{e.capability}</td>
                <td className="text-ink">{e.op}</td>
                <td className="max-w-[180px] truncate text-sec">{e.args}</td>
                <td><DecisionBadge decision={e.decision} /></td>
                <td className={`max-w-[160px] truncate ${e.decision === 'deny' ? 'text-danger' : 'text-sec'}`}>{e.reason}</td>
                <td className="text-right text-sec">{e.duration_ms}</td>
              </tr>
              {open === e.id && (
                <tr className="bg-surface">
                  <td colSpan={cols} className="!px-4 !py-4 font-sans">
                    <EventDetail e={e} />
                  </td>
                </tr>
              )}
            </Fragment>
          ))}
          {events.length === 0 && <EmptyRow cols={cols}>no events</EmptyRow>}
        </tbody>
      </table>
    </div>
  )
}
