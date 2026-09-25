import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { api, AuditEvent } from '../api'
import { Button, DecisionBadge, Field, inputCls } from '../components/ui'

const PAGE = 50

export default function Audit() {
  const [instFilter, setInstFilter] = useState('')
  const [execFilter, setExecFilter] = useState('')
  const [page, setPage] = useState(0)
  const [cursors, setCursors] = useState<(string | undefined)[]>([undefined])

  const before = cursors[page]
  const q = new URLSearchParams()
  if (instFilter) q.set('instance_id', instFilter)
  if (execFilter) q.set('exec_id', execFilter)
  q.set('limit', String(PAGE + 1)) // fetch one extra to know if there's a next page
  if (before) q.set('before', before)

  const { data } = useQuery({
    queryKey: ['audit', instFilter, execFilter, before],
    queryFn: () => api.get<{ events: AuditEvent[] }>(`/api/v1/audit?${q}`),
  })
  const events = (data?.events ?? []).slice(0, PAGE)
  const hasNext = (data?.events ?? []).length > PAGE

  const next = () => {
    const last = events[events.length - 1]
    if (!last) return
    const nextCursors = cursors.slice(0, page + 1)
    nextCursors.push(last.ts)
    setCursors(nextCursors)
    setPage(page + 1)
  }
  const prev = () => setPage(Math.max(0, page - 1))

  return (
    <div>
      <h1 className="text-base font-semibold mb-3">Audit</h1>
      <div className="flex gap-3 mb-3 items-end">
        <Field label="Instance ID"><input className={inputCls + ' !w-64'} value={instFilter} onChange={(e) => { setInstFilter(e.target.value); setPage(0); setCursors([undefined]) }} placeholder="ins_…" /></Field>
        <Field label="Exec ID"><input className={inputCls + ' !w-64'} value={execFilter} onChange={(e) => { setExecFilter(e.target.value); setPage(0); setCursors([undefined]) }} placeholder="exe_…" /></Field>
      </div>
      <div className="overflow-x-auto rounded border border-gray-200 dark:border-gray-800">
        <table className="w-full text-xs">
          <thead className="bg-gray-100 dark:bg-gray-900 text-left text-gray-600 dark:text-gray-400">
            <tr>
              <th className="px-2 py-1.5">Time</th><th className="px-2 py-1.5">Instance</th><th className="px-2 py-1.5">Exec</th>
              <th className="px-2 py-1.5">Cap</th><th className="px-2 py-1.5">Op</th><th className="px-2 py-1.5">Args</th>
              <th className="px-2 py-1.5">Decision</th><th className="px-2 py-1.5">Reason</th><th className="px-2 py-1.5">ms</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-200 dark:divide-gray-800 font-mono">
            {events.map((e) => (
              <tr key={e.id}>
                <td className="px-2 py-1">{new Date(e.ts).toLocaleString()}</td>
                <td className="px-2 py-1">{e.instance_id}</td>
                <td className="px-2 py-1">{e.exec_id}</td>
                <td className="px-2 py-1">{e.capability}</td>
                <td className="px-2 py-1">{e.op}</td>
                <td className="px-2 py-1 max-w-[200px] truncate" title={e.args}>{e.args}</td>
                <td className="px-2 py-1"><DecisionBadge decision={e.decision} /></td>
                <td className="px-2 py-1 max-w-[180px] truncate" title={e.reason}>{e.reason}</td>
                <td className="px-2 py-1">{e.duration_ms}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <div className="mt-2 flex gap-2 items-center text-xs">
        <Button onClick={prev} disabled={page === 0}>Prev</Button>
        <span>page {page + 1}</span>
        <Button onClick={next} disabled={!hasNext}>Next</Button>
      </div>
    </div>
  )
}
