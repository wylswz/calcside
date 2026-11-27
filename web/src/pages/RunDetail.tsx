import { useLayoutEffect, useRef, useState } from 'react'
import { useParams } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api, getRun, cancelRun, retryRun, Interrupt, currentWorkspace, Application } from '../api'
import { Button, Confirm, Loading, Notice, TimeLeft } from '../components/ui'
import { DetailWorkbenchHeader } from '../components/DetailWorkbench'
import { StatusCell, WorkView, WorkPane, WorkStatusbar } from '../components/ApplicationWorkView'
import { ResumeForm } from '../components/ResumeForm'
import { InterruptContext } from '../components/InterruptContext'
import { deadlineOf, fmtDateTime, resolverLabel, runPhase } from '../interrupts'

// runDuration is wall time created→ended (or →last update while live).
function runDuration(run: { created_at?: string; updated_at?: string; ended_at?: string }): string {
	const a = run.created_at ? new Date(run.created_at).getTime() : NaN
	const b = run.ended_at ? new Date(run.ended_at).getTime() : run.updated_at ? new Date(run.updated_at).getTime() : NaN
	if (!Number.isFinite(a) || !Number.isFinite(b) || b < a) return '—'
	const ms = b - a
	return ms < 1000 ? `${ms}ms` : ms < 60000 ? `${(ms / 1000).toFixed(1)}s` : `${Math.floor(ms / 60000)}m ${Math.round((ms % 60000) / 1000)}s`
}

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
	const appQ = useQuery({
		queryKey: ['application', appId],
		queryFn: () => api.get<{ application: Application }>(`/api/v1/applications/${appId}`).then((r) => r.application),
		enabled: appId != null,
		retry: false,
	})
	const appName = appQ.data?.name ?? ''
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
		// a real workbench route: the frame fills the console canvas and
		// the pane column owns the scroll, like the application detail
		<div className="app-page detail-workbench">
			<DetailWorkbenchHeader
				backTo={`/applications/${appId}?view=activity`}
				backLabel={appName ? `${appName} · Activity` : 'Application activity'}
				title="Run"
				id={run.id}
				status={<>
					<StatusCell status={run.status} />
					{deadline && <TimeLeft expiresAt={deadline} />}
				</>}
				actions={<>
					{cancellable && <Button variant="danger" size="sm" onClick={() => setConfirm('cancel')}>Cancel run</Button>}
					{retryable && <Button size="sm" onClick={() => setConfirm('retry')}>Retry step</Button>}
				</>} />
			<dl className="kv-strip">
				<div><dt>Application</dt><dd>{appName || appId}</dd></div>
				<div><dt>Trigger</dt><dd>{run.trigger}</dd></div>
				<div><dt>Release</dt><dd><code>{run.release_id}</code></dd></div>
				<div><dt>Started</dt><dd>{run.created_at ? fmtDateTime(run.created_at) : '—'}</dd></div>
				<div><dt>Duration</dt><dd>{runDuration(run)}</dd></div>
				<div><dt>Steps</dt><dd>{steps.length || run.steps}</dd></div>
			</dl>
			{err !== '' && <div className="detail-error"><Notice tone="warning">{err}</Notice></div>}

			<WorkView scroll>
			{run.status === 'failed' && run.error_code && (
				<div className="wb-pane-notice"><Notice tone="warning">Run failed — {run.error_code}</Notice></div>
			)}
			{run.streaming && (
				<WorkPane title="Output">
					<p className="app-dim wb-note-pad">Stream not retained · {run.stream_items ?? 0} item{(run.stream_items ?? 0) === 1 ? '' : 's'} · {run.stream_bytes ?? 0} bytes</p>
				</WorkPane>
			)}
			{run.status === 'succeeded' && run.output_json && (
				<WorkPane title="Output">
					<pre className="wb-code wb-cap">{run.output_json}</pre>
				</WorkPane>
			)}

			{(pending || phase?.phase === 'running') && (
				<WorkPane data-testid="run-progress" ref={progressRef}
					style={reserved === undefined ? undefined : { minHeight: reserved }}
					title={pending
						? <>Waiting · <code className="wb-pane-mono">{pending.handler ?? 'interrupt'}</code></>
						: <>Running · <code className="wb-pane-mono">{runningHandler ?? 'handler'}</code></>}>
					<div className="wb-note-pad grid gap-2">
						{pending && <InterruptContext interrupt={pending} />}
						{pending ? (
							pending.can_resume ? (
								<ResumeForm key={pending.id} interrupt={pending}
									scope={{ userId: me?.user.id ?? '', workspaceId: ws }}
									onResumed={() => qc.invalidateQueries({ queryKey: ['run', runId] })} />
							) : (
								<p className="text-sm text-body">Waiting to be resumed.</p>
							)
						) : (
							// S3: the pane keeps its box while the handler runs.
							<p className="text-sm text-body" aria-live="polite">Running {runningHandler ?? 'handler'}…</p>
						)}
					</div>
				</WorkPane>
			)}

			<WorkPane title="Steps">
				<div role="region" aria-label="Steps table" tabIndex={0}>
					<table className="tbl wb-table app-step-table" role="table">
						<thead role="rowgroup">
							<tr role="row"><th role="columnheader" className="wb-col-num">Seq</th><th role="columnheader" className="wb-col-num">Attempt</th><th role="columnheader">Entry</th><th role="columnheader">Status</th><th role="columnheader" className="wb-col-num">Duration</th><th role="columnheader">Resolver</th><th role="columnheader">Error</th></tr>
						</thead>
						<tbody role="rowgroup">
							{steps.map((s) => (
								<tr role="row" key={`${s.seq}/${s.attempt}`}>
									<td role="cell" className="text-xs wb-col-num" data-label="Seq">{s.seq}</td>
									<td role="cell" className="text-xs wb-col-num" data-label="Attempt">{s.attempt}</td>
									<td role="cell" className="text-xs" data-label="Entry">{s.entry}</td>
									<td role="cell" data-label="Status"><StatusCell status={s.status} /></td>
									<td role="cell" className="text-xs text-sec wb-col-num" data-label="Duration">{s.duration_ms}ms</td>
									<td role="cell" className="text-xs text-sec wb-mono" data-label="Resolver">{s.interrupt_id ? (intrs.find((i: Interrupt) => i.id === s.interrupt_id)?.resolved_by ? resolverLabel(intrs.find((i) => i.id === s.interrupt_id)!) : s.interrupt_id) : '—'}</td>
									<td role="cell" className="text-xs text-sec wb-mono" data-label="Error">{s.error_code ?? '—'}</td>
								</tr>
							))}
						</tbody>
					</table>
				</div>
			</WorkPane>
			</WorkView>
			<WorkStatusbar>
				<StatusCell status={run.status} />
				<span className="wb-status-num">{steps.length} step{steps.length === 1 ? '' : 's'}</span>
				<span className="wb-grow" />
				<span className="text-sec">{run.created_at ? fmtDateTime(run.created_at) : ''}</span>
			</WorkStatusbar>

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
