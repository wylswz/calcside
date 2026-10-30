import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { getRun, Interrupt } from '../api'
import { interruptTitle, runPhase, RunPhase } from '../interrupts'
import { ResumeForm } from './ResumeForm'
import { SwapLabel } from './ui'

// useRunProgress polls one run and derives its phase. Poll cadence
// follows the phase (S4): fast while a handler is running, slower while
// waiting, stopped once terminal.
export function useRunProgress(runId?: string): { phase?: RunPhase } {
	const q = useQuery({
		queryKey: ['run', runId],
		queryFn: () => getRun(runId!),
		enabled: runId != null,
		refetchInterval: (q) => {
			const d = q.state.data
			if (!d) return 1000
			const p = runPhase(d).phase
			return p === 'running' ? 1000 : p === 'waiting' ? 3000 : false
		},
	})
	return { phase: q.data ? runPhase(q.data) : undefined }
}

// RunProgress renders one run's live phase in a fixed region (S3/S4):
// waiting shows the interrupt plus an optional inline resume form,
// running shows a state label, terminal shows the outcome. The parent
// reads the same phase via useRunProgress for its Output area.
export function RunProgress({ runId, appId, scope, inline, fallbackWaiting }: {
	runId: string
	appId: string
	scope: { userId: string; workspaceId: string }
	inline?: boolean
	fallbackWaiting?: { interrupt_id: string }
}) {
	const { phase } = useRunProgress(runId)
	const link = <Link className="link text-xs" to={`/applications/${appId}/runs/${runId}`}>Open run</Link>
	if (phase?.phase === 'waiting') {
		const i = phase.interrupt as Interrupt
		return (
			<div className="app-waiting" aria-live="polite">
				<p className="text-sm font-medium text-body">Waiting — {interruptTitle(i)}</p>
				{inline && i.can_resume && <ResumeForm interrupt={i} scope={scope} />}
				{link}
			</div>
		)
	}
	if (phase?.phase === 'running') {
		const label = `Running ${phase.handler ?? 'handler'}…`
		return (
			<div className="app-waiting" aria-live="polite">
				<p className="text-sm font-medium text-body"><SwapLabel show={label} options={[label, 'Resumed — running handler…']} /></p>
				{link}
			</div>
		)
	}
	if (phase && phase.phase !== 'succeeded') {
		return (
			<div className="app-waiting" aria-live="polite">
				<p className="text-sm font-medium text-body">{phase.phase}{phase.errorCode ? `: ${phase.errorCode}` : ''}</p>
				{link}
			</div>
		)
	}
	if (fallbackWaiting) {
		return (
			<div className="app-waiting" aria-live="polite">
				<p className="text-sm font-medium text-body">Waiting — {fallbackWaiting.interrupt_id}</p>
				{link}
			</div>
		)
	}
	return null
}
