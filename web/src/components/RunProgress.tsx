import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { getRun, Interrupt, RunDetail, RunStep } from '../api'
import { deadlineOf, interruptTitle, runPhase, RunPhase } from '../interrupts'
import { SwapLabel, TimeLeft } from './ui'

// useRunProgress polls one run and derives its phase. Poll cadence
// follows the phase (S4): fast while a handler is running, slower while
// waiting, stopped once terminal.
export function useRunProgress(runId?: string): { phase?: RunPhase; run?: RunDetail; steps?: RunStep[] } {
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
	return { phase: q.data ? runPhase(q.data) : undefined, run: q.data?.run, steps: q.data?.steps }
}

// WaitHead renders only the 28px strip head (tone dot · label · Open
// run) for a live run phase. Output panes place it directly under
// their head, then the (bounded) output pre, then the resume form —
// the form owns the remaining height instead of being pushed to the
// pane bottom.
export function WaitHead({ phase, runId, appId }: { phase: RunPhase; runId: string; appId: string }) {
	const link = <Link className="app-waiting-link" to={`/applications/${appId}/runs/${runId}`}>Open run</Link>
	if (phase.phase === 'succeeded') return null
	if (phase.phase === 'waiting') {
		const i = phase.interrupt as Interrupt
		const dl = deadlineOf(i)
		return (
			<div className="app-waiting" aria-live="polite">
				<div className="app-waiting-head">
					<span className="app-waiting-dot" data-tone="warn" />
					<span className="app-waiting-text">
						waiting · {interruptTitle(i)}
						{dl && <> · <TimeLeft expiresAt={dl} /></>}
					</span>
					{link}
				</div>
			</div>
		)
	}
	if (phase.phase === 'running') {
		const label = `running · ${phase.handler ?? 'handler'}`
		return (
			<div className="app-waiting" aria-live="polite">
				<div className="app-waiting-head">
					<span className="app-waiting-dot" data-tone="ok" />
					<span className="app-waiting-text">
						<SwapLabel show={label} options={[label, 'resumed · running handler']} />
					</span>
					{link}
				</div>
			</div>
		)
	}
	return (
		<div className="app-waiting" aria-live="polite">
			<div className="app-waiting-head">
				<span className="app-waiting-dot" data-tone="err" />
				<span className="app-waiting-text">{phase.phase}{phase.errorCode ? ` · ${phase.errorCode}` : ''}</span>
				{link}
			</div>
		</div>
	)
}
