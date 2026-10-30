import { useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { getRun, cancelRun, retryRun, Interrupt, currentWorkspace } from '../api'
import { Button, Confirm, Loading, Notice, PageHeader, TimeLeft } from '../components/ui'
import { ResolveForm } from '../components/ResolveForm'
import { deadlineOf, interruptTitle, resolverLabel } from '../interrupts'

// One durable run (EP-0016): status header with deadline, Cancel/Retry
// actions behind named Confirms (F2), the pending interrupt with a
// ResolveForm, and the step list. Polling updates rows in place;
// selections and the waiting card keep their boxes (C3/S3).
export default function RunDetail() {
	const { id: appId, runId } = useParams<{ id: string; runId: string }>()
	const qc = useQueryClient()
	const me = qc.getQueryData<{ user: { id: string } }>(['me'])
	const ws = currentWorkspace() ?? ''
	const [confirm, setConfirm] = useState<'' | 'cancel' | 'retry'>('')
	const [err, setErr] = useState('')

	const q = useQuery({
		queryKey: ['run', runId],
		queryFn: () => getRun(runId!),
		refetchInterval: 5000,
		enabled: runId != null,
	})
	const run = q.data?.run
	const steps = q.data?.steps ?? []
	const intrs = q.data?.interrupts ?? []
	const pending = intrs.find((i) => i.status === 'pending')
	const deadline = pending ? deadlineOf(pending) : undefined

	if (q.isLoading) return <Loading />
	if (q.isError) return <Notice tone="warning">Could not load run: {(q.error as Error).message}</Notice>
	if (!run) return <Notice tone="warning">Run not found.</Notice>

	const cancellable = run.status === 'waiting' || run.status === 'unknown'
	const retryable = run.status === 'unknown'

	return (
		<div className="app-page">
			<PageHeader title={
				<span>Run <span className={`app-status app-status-${run.status}`}>{run.status}</span></span>
			} actions={<>
				{cancellable && <Button variant="danger" size="sm" onClick={() => setConfirm('cancel')}>Cancel run</Button>}
				{retryable && <Button size="sm" onClick={() => setConfirm('retry')}>Retry step</Button>}
			</>} />
			<p className="app-dim">
				<Link to={`/applications/${appId}`} className="link">← {appId}</Link> ·{' '}
				<span className="app-mono">{run.id}</span>
				{deadline && <> · <TimeLeft expiresAt={deadline} /></>}
			</p>
			{err !== '' && <Notice tone="warning">{err}</Notice>}

			{run.status === 'succeeded' && run.output_json && (
				<section className="app-card">
					<div className="app-card-head"><span className="app-card-title">Output</span></div>
					<div className="app-card-body"><pre className="app-out-pre">{run.output_json}</pre></div>
				</section>
			)}
			{run.status === 'failed' && run.error_code && (
				<Notice tone="warning">Run failed — {run.error_code}</Notice>
			)}

			{pending && (
				<section className="app-card">
					<div className="app-card-head"><span className="app-card-title">Waiting on — {interruptTitle(pending)}</span></div>
					<div className="app-card-body">
						{pending.assignees && pending.assignees.length > 0 && (
							<dl className="app-kv">
								<dt className="app-dim">Assignees</dt>
								<dd>{(pending.assignee_labels ?? pending.assignees).join(', ')}</dd>
							</dl>
						)}
						{pending.context_json && <pre className="app-out-pre mt-2">{pending.context_json}</pre>}
						{pending.can_resolve ? (
							<ResolveForm interrupt={pending}
								scope={{ userId: me?.user.id ?? '', workspaceId: ws }}
								onResolved={() => qc.invalidateQueries({ queryKey: ['run', runId] })} />
						) : (
							<p className="text-sm text-body">Waiting for a decision{pending.assignees?.length ? ` from ${(pending.assignee_labels ?? pending.assignees).join(', ')}` : ''}.</p>
						)}
					</div>
				</section>
			)}

			<section className="app-card">
				<div className="app-card-head"><span className="app-card-title">Steps</span></div>
				<div className="app-card-body">
					<table className="tbl">
						<thead>
							<tr><th>Seq</th><th>Attempt</th><th>Entry</th><th>Status</th><th>Duration</th><th>Resolver</th></tr>
						</thead>
						<tbody>
							{steps.map((s) => (
								<tr key={`${s.seq}/${s.attempt}`}>
									<td className="text-xs">{s.seq}</td>
									<td className="text-xs">{s.attempt}</td>
									<td className="text-xs">{s.entry}</td>
									<td><span className={`app-status app-status-${s.status}`}>{s.status}</span>{s.error_code ? ` · ${s.error_code}` : ''}</td>
									<td className="text-xs text-sec">{s.duration_ms}ms</td>
									<td className="text-xs text-sec">{s.interrupt_id ? (intrs.find((i: Interrupt) => i.id === s.interrupt_id)?.resolved_by ? resolverLabel(intrs.find((i) => i.id === s.interrupt_id)!) : s.interrupt_id) : '—'}</td>
								</tr>
							))}
						</tbody>
					</table>
				</div>
			</section>

			{confirm === 'cancel' && (
				<Confirm tone="danger" title={`Cancel run ${run.id}?`} confirm="Cancel run" pending="Cancelling…"
					action={async () => {
						try { await cancelRun(run.id); setErr('') }
						catch (e) { setErr(e instanceof Error ? e.message : 'cancel failed') }
						setConfirm('')
						await qc.invalidateQueries({ queryKey: ['run', runId] })
					}}
					onClose={() => setConfirm('')}>
					<p className="text-sm text-body">Pending interrupts are cancelled and their callback keys revoked.</p>
				</Confirm>
			)}
			{confirm === 'retry' && (
				<Confirm title={`Retry the failed step of ${run.id}?`} confirm="Retry" pending="Retrying…"
					action={async () => {
						try { await retryRun(run.id); setErr('') }
						catch (e) { setErr(e instanceof Error ? e.message : 'retry failed') }
						setConfirm('')
						await qc.invalidateQueries({ queryKey: ['run', runId] })
					}}
					onClose={() => setConfirm('')}>
					<p className="text-sm text-body">The handler may repeat side effects; it runs again with the same interrupt id.</p>
				</Confirm>
			)}
		</div>
	)
}
