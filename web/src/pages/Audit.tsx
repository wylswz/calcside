import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { api, AuditEvent } from '../api'
import { Button, Field, PageHeader, inputCls } from '../components/ui'
import { AuditTable } from '../components/AuditTable'

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
      <PageHeader index="05" section="Record" title="Audit">
        Every capability call — allowed or denied — with its decision, reason and duration. Click a row for full args and the executed code.
      </PageHeader>
      <div className="mb-6 flex items-end gap-3">
        <Field label="Instance ID"><input className={inputCls + ' !w-64 font-mono'} value={instFilter} onChange={(e) => { setInstFilter(e.target.value); setPage(0); setCursors([undefined]) }} placeholder="ins_…" /></Field>
        <Field label="Exec ID"><input className={inputCls + ' !w-64 font-mono'} value={execFilter} onChange={(e) => { setExecFilter(e.target.value); setPage(0); setCursors([undefined]) }} placeholder="exe_…" /></Field>
      </div>
      <AuditTable events={events} showScope />
      <div className="mt-4 flex items-center gap-3 text-xs">
        <Button size="sm" onClick={prev} disabled={page === 0}>← Prev</Button>
        <span className="font-mono text-sec">page {String(page + 1).padStart(2, '0')}</span>
        <Button size="sm" onClick={next} disabled={!hasNext}>Next →</Button>
      </div>
    </div>
  )
}
