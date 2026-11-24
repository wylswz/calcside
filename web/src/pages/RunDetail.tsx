import { useLayoutEffect, useRef, useState } from 'react'
import { useParams } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { getRun, cancelRun, retryRun, Interrupt, currentWorkspace } from '../api'
import { Button, Confirm, Loading, Notice, TimeLeft } from '../components/ui'
import { DetailWorkbenchHeader } from '../components/DetailWorkbench'
import { ApplicationWorkView, ApplicationWorkSection } from '../components/ApplicationWorkView'
import { ResumeForm } from '../components/ResumeForm'
import { deadlineOf, interruptTitle, resolverLabel, runPhase } from '../interrupts'

// One durable run (EP-0016): status header with deadline, Cancel/Retry
// actions behind named Confirms (F2), the pending interrupt with a
// ResumeForm, and the step list. Polling updates rows in place;
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
		// 1s while a handler is running, 5s otherwise (S4 stable slot)
		refetchInterval: (q) => (q.state.data && runPhase(q.state.data).phase === 'running' ? 1000 : 5000),
		enabled: runId != null,
	})
	const run = q.data?.run
	const steps = q.data?.steps ?? []
	const intrs = q.data?.interrupts ?? []
	const phase = q.data ? runPhase(q.data) : undefined
	const pending = phase?.phase === 'waiting' ? phase.interrupt : undefined
	const runningHandler = phase?.phase === 'running' ? phase.handler : undefined
	const deadline = pending ? deadlineOf(pending) : undefined

	const progressRef = useRef<HTMLElement | null>(null)
	const waitingHeight = useRef<{ runId: string; height: number } | null>(null)
	useLayoutEffect(() => {
		if (!pending || runId == null) return
		const el = progressRef.current
		if (!el) return
		const measure = () => {
			const height = el.getBoundingClientRect().height
			const cur = waitingHeight.current
			if (!cur || cur.runId !== runId || cur.height !== height) waitingHeight.current = { runId, height }
		}
		measure()
		const ro = new ResizeObserver(measure)
		ro.observe(el)
		return () => ro.disconnect()
	}, [pending, runId])

	if (q.isLoading) return <Loading />
	if (q.isError) return <div className="app-page"><Notice tone="warning">Could not load run: {(q.error as Error).message}</Notice></div>
	if (!run) return <div className="app-page"><Notice tone="warning">Run not found.</Notice></div>

	const cancellable = run.status === 'waiting' || run.status === 'unknown'
	const retryable = run.status === 'unknown'
	const remembered = waitingHeight.current
	const reserved = phase?.phase === 'running' && remembered != null && remembered.runId === runId ? remembered.height : undefined

	return (
		// inner .detail-workbench is not .console-content's direct child, so
		// the shell keeps its normal outer scroll — the frame supplies the
		// shared header and typography only
		<div className="app-page app-run-page">
		<div className="detail-workbench">
			<DetailWorkbenchHeader
				backTo={`/applications/${appId}?view=activity`}
				backLabel="← Application activity"
				title="Run"
				id={run.id}
				status={<>
					<span className={`app-status app-status-${run.status}`}>{run.status}</span>
					{deadline && <TimeLeft expiresAt={deadline} />}
				</>}
				actions={<>
					{cancellable && <Button variant="danger" size="sm" onClick={() => setConfirm('cancel')}>Cancel run</Button>}
					{retryable && <Button size="sm" onClick={() => setConfirm('retry')}>Retry step</Button>}
				</>} />
			{err !== '' && <div className="detail-error"><Notice tone="warning">{err}</Notice></div>}

			<ApplicationWorkView>
			{run.streaming && (
				<ApplicationWorkSection title="Output">
					<p className="app-dim">Stream not retained · {run.stream_items ?? 0} item{(run.stream_items ?? 0) === 1 ? '' : 's'} · {run.stream_bytes ?? 0} bytes</p>
				</ApplicationWorkSection>
			)}
			{run.status === 'succeeded' && run.output_json && (
				<ApplicationWorkSection title="Output">
					<pre className="app-out-pre">{run.output_json}</pre>
				</ApplicationWorkSection>
			)}
			{run.status === 'failed' && run.error_code && (
				<Notice tone="warning">Run failed — {run.error_code}</Notice>
			)}

			{(pending || phase?.phase === 'running') && (
				<ApplicationWorkSection data-testid="run-progress" ref={progressRef}
					style={reserved === undefined ? undefined : { minHeight: reserved }}
					title={pending ? `Waiting on — ${interruptTitle(pending)}` : `Running — ${runningHandler ?? 'handler'}`}>
					{pending?.context_json && <pre className="app-out-pre">{pending.context_json}</pre>}
					{pending?.resume_url && <p className="text-xs text-sec"><span className="app-mono">POST {pending.resume_url}</span></p>}
					{pending ? (
						pending.can_resume ? (
							<ResumeForm interrupt={pending}
								scope={{ userId: me?.user.id ?? '', workspaceId: ws }}
								onResumed={() => qc.invalidateQueries({ queryKey: ['run', runId] })} />
						) : (
							<p className="text-sm text-body">Waiting to be resumed.</p>
						)
					) : (
						// S3: the card keeps its box while the handler runs.
						<p className="text-sm text-body" aria-live="polite">Running {runningHandler ?? 'handler'}…</p>
					)}
				</ApplicationWorkSection>
			)}

			<ApplicationWorkSection title="Steps">
				<div className="app-work-table" role="region" aria-label="Steps table" tabIndex={0}>
					<table className="tbl app-step-table" role="table">
						<thead role="rowgroup">
							<tr role="row"><th role="columnheader">Seq</th><th role="columnheader">Attempt</th><th role="columnheader">Entry</th><th role="columnheader">Status</th><th role="columnheader">Duration</th><th role="columnheader">Resolver</th></tr>
						</thead>
						<tbody role="rowgroup">
							{steps.map((s) => (
								<tr role="row" key={`${s.seq}/${s.attempt}`}>
									<td role="cell" className="text-xs" data-label="Seq">{s.seq}</td>
									<td role="cell" className="text-xs" data-label="Attempt">{s.attempt}</td>
									<td role="cell" className="text-xs" data-label="Entry">{s.entry}</td>
									<td role="cell" data-label="Status"><span><span className={`app-status app-status-${s.status}`}>{s.status}</span>{s.error_code ? ` · ${s.error_code}` : ''}</span></td>
									<td role="cell" className="text-xs text-sec" data-label="Duration">{s.duration_ms}ms</td>
									<td role="cell" className="text-xs text-sec" data-label="Resolver">{s.interrupt_id ? (intrs.find((i: Interrupt) => i.id === s.interrupt_id)?.resolved_by ? resolverLabel(intrs.find((i) => i.id === s.interrupt_id)!) : s.interrupt_id) : '—'}</td>
								</tr>
							))}
						</tbody>
					</table>
				</div>
			</ApplicationWorkSection>
			</ApplicationWorkView>
		</div>

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
