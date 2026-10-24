import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { api, AuditEvent, Instance } from '../api'
import { callOutcome } from '../activity'
import { Button, Field, PageHeader, inputCls } from '../components/ui'
import { CallRows } from '../components/CallTable'

const PAGE = 50

type OutcomeFilter = 'all' | 'denied' | 'failed'

export default function Audit() {
  const [instFilter, setInstFilter] = useState('')
  const [execFilter, setExecFilter] = useState('')
  const [outcome, setOutcome] = useState<OutcomeFilter>('all')
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
  const { data: instData } = useQuery({
    queryKey: ['instances'],
    queryFn: () => api.get<{ instances: Instance[] }>('/api/v1/instances'),
  })
  const instances = useMemo(
    () => new Map((instData?.instances ?? []).map((i) => [i.id, i])),
    [instData],
  )
  const all = (data?.events ?? []).slice(0, PAGE)
  const events = outcome === 'all' ? all : all.filter((e) => callOutcome(e) === outcome)
  const hasNext = (data?.events ?? []).length > PAGE

  const next = () => {
    const last = all[all.length - 1]
    if (!last) return
    const nextCursors = cursors.slice(0, page + 1)
    nextCursors.push(last.ts)
    setCursors(nextCursors)
    setPage(page + 1)
  }
  const prev = () => setPage(Math.max(0, page - 1))

  return (
    <div>
      <PageHeader title="Audit">
        Every capability call — allowed or denied — with its outcome and reason. Click a row for full args.
      </PageHeader>
      <div className="mb-6 flex flex-wrap items-end gap-3">
        <Field label="Instance ID"><input className={inputCls + ' !w-64 font-mono'} value={instFilter} onChange={(e) => { setInstFilter(e.target.value); setPage(0); setCursors([undefined]) }} placeholder="ins_…" /></Field>
        <Field label="Exec ID"><input className={inputCls + ' !w-64 font-mono'} value={execFilter} onChange={(e) => { setExecFilter(e.target.value); setPage(0); setCursors([undefined]) }} placeholder="exe_…" /></Field>
        <Field label="Outcome">
          <select className={inputCls + ' !w-40'} value={outcome} onChange={(e) => setOutcome(e.target.value as OutcomeFilter)}>
            <option value="all">All</option>
            <option value="denied">Denied</option>
            <option value="failed">Failed</option>
          </select>
        </Field>
      </div>
      <CallRows events={events} instances={instances} showInstance />
      <div className="mt-4 flex items-center gap-3 text-xs">
        <Button size="sm" onClick={prev} disabled={page === 0}>← Prev</Button>
        <span className="font-mono text-sec">page {page + 1}</span>
        <Button size="sm" onClick={next} disabled={!hasNext}>Next →</Button>
      </div>
    </div>
  )
}
