import { Fragment, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import CodeMirror from '@uiw/react-codemirror'
import { python } from '@codemirror/lang-python'
import { api, AuditEvent } from '../api'
import type { Execution } from '../api'
import { DecisionBadge } from './ui'

interface ExecutionDetail {
  execution: Execution
  code: string
}

function dark(): boolean {
  return window.matchMedia('(prefers-color-scheme: dark)').matches
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
      <div className="flex items-center gap-2 mb-1">
        <span className="font-medium">exec {execId || '—'}</span>
        {data?.code && (
          <button className="text-blue-600 dark:text-blue-400" onClick={copy}>
            {copied ? 'copied' : 'copy'}
          </button>
        )}
      </div>
      {execId === '' && <p className="text-gray-500">no execution linked to this event</p>}
      {isLoading && <p className="text-gray-500">loading…</p>}
      {error && <p className="text-red-600">{(error as Error).message}</p>}
      {data && data.code === '' && (
        <p className="text-gray-500">code was not recorded for this execution (pre-dates code retention)</p>
      )}
      {data && data.code !== '' && (
        <div className="rounded border border-gray-300 dark:border-gray-700 overflow-hidden">
          <CodeMirror
            value={data.code}
            readOnly
            editable={false}
            maxHeight="24rem"
            extensions={[python()]}
            theme={dark() ? 'dark' : 'light'}
            basicSetup={{ highlightActiveLine: false, highlightActiveLineGutter: false, foldGutter: false }}
          />
        </div>
      )}
    </div>
  )
}

function EventDetail({ e }: { e: AuditEvent }) {
  return (
    <div className="space-y-3 font-mono text-xs">
      <div className="grid grid-cols-2 md:grid-cols-4 gap-x-6 gap-y-1">
        <div><span className="text-gray-500">id:</span> {e.id}</div>
        <div><span className="text-gray-500">user:</span> {e.user_id}</div>
        <div><span className="text-gray-500">instance:</span> {e.instance_id}</div>
        <div><span className="text-gray-500">exec:</span> {e.exec_id || '—'}</div>
        <div><span className="text-gray-500">phase:</span> {e.phase || '—'}</div>
        <div><span className="text-gray-500">duration:</span> {e.duration_ms}ms</div>
      </div>
      {e.reason && (
        <div>
          <div className="text-gray-500 mb-0.5">reason</div>
          <pre className="rounded bg-gray-50 dark:bg-gray-900 p-2 whitespace-pre-wrap">{e.reason}</pre>
        </div>
      )}
      {e.error && (
        <div>
          <div className="text-gray-500 mb-0.5">error</div>
          <pre className="rounded bg-red-50 dark:bg-red-950/40 text-red-700 dark:text-red-300 p-2 whitespace-pre-wrap">{e.error}</pre>
        </div>
      )}
      <div>
        <div className="text-gray-500 mb-0.5">args</div>
        <pre className="rounded bg-gray-50 dark:bg-gray-900 p-2 whitespace-pre-wrap break-all max-h-64 overflow-auto">{prettyArgs(e.args)}</pre>
      </div>
      <div>
        <div className="text-gray-500 mb-0.5">executed code</div>
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
    <div className="overflow-x-auto rounded border border-gray-200 dark:border-gray-800">
      <table className="w-full text-xs">
        <thead className="bg-gray-100 dark:bg-gray-900 text-left text-gray-600 dark:text-gray-400">
          <tr>
            <th className="px-2 py-1.5">Time</th>
            {showScope && <th className="px-2 py-1.5">Instance</th>}
            <th className="px-2 py-1.5">Exec</th>
            <th className="px-2 py-1.5">Cap</th>
            <th className="px-2 py-1.5">Op</th>
            <th className="px-2 py-1.5">Args</th>
            <th className="px-2 py-1.5">Decision</th>
            <th className="px-2 py-1.5">Reason</th>
            <th className="px-2 py-1.5">ms</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-gray-200 dark:divide-gray-800 font-mono">
          {events.map((e) => (
            <Fragment key={e.id}>
              <tr
                className="cursor-pointer hover:bg-gray-50 dark:hover:bg-gray-900"
                onClick={() => setOpen(open === e.id ? null : e.id)}
              >
                <td className="px-2 py-1 whitespace-nowrap">{open === e.id ? '▾ ' : '▸ '}{new Date(e.ts).toLocaleString()}</td>
                {showScope && <td className="px-2 py-1">{e.instance_id}</td>}
                <td className="px-2 py-1">{e.exec_id}</td>
                <td className="px-2 py-1">{e.capability}</td>
                <td className="px-2 py-1">{e.op}</td>
                <td className="px-2 py-1 max-w-[200px] truncate">{e.args}</td>
                <td className="px-2 py-1"><DecisionBadge decision={e.decision} /></td>
                <td className="px-2 py-1 max-w-[180px] truncate">{e.reason}</td>
                <td className="px-2 py-1">{e.duration_ms}</td>
              </tr>
              {open === e.id && (
                <tr>
                  <td colSpan={cols} className="px-3 py-3 bg-gray-50/60 dark:bg-gray-900/40">
                    <EventDetail e={e} />
                  </td>
                </tr>
              )}
            </Fragment>
          ))}
          {events.length === 0 && (
            <tr><td colSpan={cols} className="px-2 py-3 text-center text-gray-500">no events</td></tr>
          )}
        </tbody>
      </table>
    </div>
  )
}
