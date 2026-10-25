import { useEffect, Fragment, useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import CodeMirror from '@uiw/react-codemirror'
import { python } from '@codemirror/lang-python'
import { api, AuditEvent, Execution, ExecutionDetail } from '../api'
import { callOutcome, groupCalls, runBy, runOutcome } from '../activity'
import { fmtMs, fmtTime, relTime } from '../format'
import { CallRows } from './CallTable'
import { EmptyRow, Loading, Notice, OutcomeBadge, RunByBadge, SectionTitle } from './ui'
import { cmTheme } from './codemirror'

// firstCodeLine picks the first meaningful line of a run's code — the
// first non-empty line that is not a comment.
function firstCodeLine(snippet: string): string {
  for (const line of snippet.split('\n')) {
    const t = line.trim()
    if (t && !t.startsWith('#')) return t.length > 80 ? t.slice(0, 80) + '…' : t
  }
  return ''
}

function RunDetail({ execId }: { execId: string }) {
  const detail = useQuery({
    queryKey: ['execution', execId],
    queryFn: () => api.get<ExecutionDetail>(`/api/v1/executions/${execId}`),
    retry: false,
  })
  const calls = useQuery({
    queryKey: ['audit', 'exec', execId],
    queryFn: () => api.get<{ events: AuditEvent[] }>(`/api/v1/audit?exec_id=${execId}&limit=500`),
    retry: false,
  })
  if (detail.isLoading) return <Loading />
  if (detail.error) return <Notice tone="warning">{(detail.error as Error).message}</Notice>
  const d = detail.data!
  const denied = runOutcome(d.execution) === 'denied'
  return (
    <div className="run-detail space-y-4">
      {d.code && (
        <div className="detail-code-shell">
          <CodeMirror
            value={d.code}
            readOnly
            editable={false}
            maxHeight="20rem"
            extensions={[python()]}
            theme={cmTheme}
            basicSetup={{ highlightActiveLine: false, highlightActiveLineGutter: false, foldGutter: false }}
          />
        </div>
      )}
      <div>
        <div className="mb-1 text-xs font-medium text-sec">
          Output{d.output_truncated && <span className="ml-2 text-warn-text">truncated at 64 KiB</span>}
        </div>
        {d.output
          ? <pre className="pre max-h-64">{d.output}</pre>
          : <p className="text-sm text-sec">No output.</p>}
      </div>
      {d.error && (
        <div>
          <div className={`error-head mb-1 text-xs font-medium ${denied ? 'text-danger' : 'text-warn-text'}`}>Error · {d.error.type}</div>
          <pre className="pre">{d.error.message}</pre>
          {d.error.backtrace && (
            <details className="run-backtrace mt-3 text-xs text-sec">
              <summary className="cursor-pointer">Stack trace</summary>
              <pre className="pre mt-2">{d.error.backtrace}</pre>
            </details>
          )}
        </div>
      )}
      <div>
        <div className="mb-2 text-xs font-medium text-sec">Calls</div>
        {calls.isLoading && <Loading />}
        {calls.error && <Notice tone="warning">{(calls.error as Error).message}</Notice>}
        {calls.data && <CallRows events={calls.data.events} />}
      </div>
    </div>
  )
}

// Activity lists runs newest first with their outcomes and the calls
// each made; console (file-browsing) calls sit in their own muted group.
// editor renders directly under the heading, so the control that opens it
// stays where it was clicked.
export function Activity({ id, aside, editor, focusId }: { id: string; aside?: React.ReactNode; editor?: React.ReactNode; focusId?: string }) {
  const [open, setOpen] = useState<string | null>(focusId ?? null)
  const [attentionOnly, setAttentionOnly] = useState(false)
  useEffect(() => {
    if (focusId) { setOpen(focusId); document.getElementById(`run-${focusId}`)?.scrollIntoView({ behavior: 'smooth', block: 'center' }) }
  }, [focusId])
  const [showConsole, setShowConsole] = useState(false)

  const { data: execs } = useQuery({
    queryKey: ['executions', id],
    queryFn: () => api.get<{ executions: Execution[] }>(`/api/v1/instances/${id}/executions?limit=50`),
    refetchInterval: 5000,
  })
  const { data: audit } = useQuery({
    queryKey: ['audit', id],
    queryFn: () => api.get<{ events: AuditEvent[] }>(`/api/v1/audit?instance_id=${id}&limit=500`),
    refetchInterval: 5000,
  })

  const callsByExec = useMemo(() => groupCalls(audit?.events ?? []), [audit])
  const consoleCalls = callsByExec.get('console') ?? []

  const runs = useMemo(() => (execs?.executions ?? []).map((x) => {
    const calls = callsByExec.get(x.id) ?? []
    let denied = 0, failed = 0
    for (const c of calls) {
      const o = callOutcome(c)
      if (o === 'denied') denied++
      else if (o === 'failed') failed++
    }
    const outcome = runOutcome(x)
    return { x, denied, failed, outcome, attention: outcome !== 'ok' || denied > 0 || failed > 0 }
  }), [execs, callsByExec])

  const shown = attentionOnly ? runs.filter((r) => r.attention) : runs

  return (
    <section aria-label="Activity">
      <SectionTitle className="activity-heading" aside={
        <>{aside}<div className="seg-filters flex gap-1 text-xs">
          {(['all', 'needs attention'] as const).map((f) => {
            const on = (f === 'needs attention') === attentionOnly
            return (
              <button key={f} aria-pressed={on} onClick={() => setAttentionOnly(f === 'needs attention')}
                className={`font-medium ${on ? 'text-ink' : 'text-sec hover:text-ink'}`}>
                {f}
              </button>
            )
          })}
        </div></>
      }>Activity</SectionTitle>
      {editor}

      <div className="tbl-wrap">
        <table className="tbl tbl-compact">
          <thead>
            <tr>
              <th>Run by</th>
              <th>When</th>
              <th>Outcome</th>
              <th>Summary</th>
              <th className="!text-right">Calls</th>
              <th className="!text-right">Time</th>
            </tr>
          </thead>
          <tbody className="font-mono">
            {shown.map(({ x, denied, failed, outcome }) => (
              <Fragment key={x.id}>
                <tr id={`run-${x.id}`} className={`row-click ${open === x.id ? 'row-open' : ''}`} onClick={() => setOpen(open === x.id ? null : x.id)}>
                  <td className="whitespace-nowrap font-sans">
                    <span className="mr-1.5 inline-block w-2 text-mute">{open === x.id ? '▾' : '▸'}</span>
                    <RunByBadge runBy={runBy(x)} />
                  </td>
                  <td className="whitespace-nowrap font-sans text-sec" title={fmtTime(x.created_at)}>{relTime(x.created_at)}</td>
                  <td>
                    <OutcomeBadge outcome={outcome} label={outcome === 'ok' ? 'ok' : outcome === 'denied' ? 'denied' : x.error_type || 'failed'} />
                  </td>
                  <td className="max-w-[260px] truncate text-sec" title={x.code_snippet}><code className="detail-code">{firstCodeLine(x.code_snippet)}</code></td>
                  <td className="num whitespace-nowrap text-right font-sans">
                    {denied > 0 && <OutcomeBadge outcome="denied" label={`${denied} denied`} />}
                    {failed > 0 && <span className={denied > 0 ? 'ml-2' : ''}><OutcomeBadge outcome="failed" label={`${failed} failed`} /></span>}
                    {denied === 0 && failed === 0 && <span className="text-xs text-mute">—</span>}
                  </td>
                  <td className="num text-right text-sec">{fmtMs(x.duration_ms)}</td>
                </tr>
                {open === x.id && (
                  <tr className="bg-surface">
                    <td colSpan={6} className="!px-4 !py-4 font-sans"><RunDetail execId={x.id} /></td>
                  </tr>
                )}
              </Fragment>
            ))}
            {shown.length === 0 && <EmptyRow cols={6}>{attentionOnly ? 'nothing needs attention' : 'no runs yet'}</EmptyRow>}
          </tbody>
        </table>
      </div>

      {consoleCalls.length > 0 && (
        <div className="mt-4">
          <button onClick={() => setShowConsole(!showConsole)} aria-expanded={showConsole}
            className="flex items-center gap-2 text-xs text-mute transition-colors hover:text-sec">
            <span className="inline-block w-2">{showConsole ? '▾' : '▸'}</span>
            Your file browsing · {consoleCalls.length} call{consoleCalls.length === 1 ? '' : 's'}
          </button>
          {showConsole && (
            <div className="mt-2 opacity-70"><CallRows events={consoleCalls} /></div>
          )}
        </div>
      )}
    </section>
  )
}
