import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { ApiError, Interrupt, resumeInterrupt } from '../api'
import { eventParse, resolverLabel, fmtDateTime, resumeDraftKey } from '../interrupts'
import { Button, Confirm, Notice, SwapLabel } from './ui'

// ResumeForm is the console's debug entry for a pending interrupt
// (EP-0016): it posts one JSON object event to the same resume route
// external systems call with the interrupt's token. It never navigates
// (P3): the surrounding view updates in place via onResumed. The
// Confirm dialog shows the exact event before firing (F2/P1); a lost
// race surfaces already_resolved in place with the typed input kept
// (P5/C2/F3).
export function ResumeForm({ interrupt: i, scope, onResumed }: {
	interrupt: Interrupt
	scope: { userId: string; workspaceId: string }
	onResumed?: () => void
}) {
	const qc = useQueryClient()
	const draftKey = resumeDraftKey(scope.userId, scope.workspaceId, i.id)
	const [event, setEvent] = useState(() => {
		try { return sessionStorage.getItem(draftKey + '.e') ?? '{}' } catch { return '{}' }
	})
	const [busy, setBusy] = useState(false)
	// A successful resume disables the form immediately — the run query
	// flips to running next poll; no pending-form flash (S4/P1).
	const [resumed, setResumed] = useState(false)
	const [err, setErr] = useState('')
	const [alreadyBy, setAlreadyBy] = useState<{ label: string; at?: string } | null>(null)
	const [confirmEv, setConfirmEv] = useState<{ json: string } | null>(null)

	const persist = (v: string) => {
		try { if (v === '' || v === '{}') sessionStorage.removeItem(draftKey + '.e'); else sessionStorage.setItem(draftKey + '.e', v) } catch { /* storage unavailable */ }
	}
	const readOnly = busy || resumed || alreadyBy !== null || i.status !== 'pending'

	const submit = (json: string) => {
		setConfirmEv(null)
		setBusy(true)
		setErr('')
		return resumeInterrupt(i.id, json)
			.then(() => {
				persist('')
				setResumed(true)
				// Mark this interrupt resolved in the cached run detail so
				// dependent views flip to running before the next poll.
				qc.setQueryData(['run', i.run_id], (old: { interrupts?: Interrupt[] } | undefined) =>
					old?.interrupts ? { ...old, interrupts: old.interrupts.map((x) => (x.id === i.id ? { ...x, status: 'resolved', can_resume: false } : x)) } : old)
				qc.invalidateQueries({ queryKey: ['run', i.run_id] })
				onResumed?.()
			})
			.catch((e) => {
				if (e instanceof ApiError && e.code === 'already_resolved') {
					setAlreadyBy({ label: resolverLabel(i) || 'someone', at: i.resolved_at })
					return
				}
				setErr(e instanceof Error ? e.message : 'resume failed')
			})
			.finally(() => setBusy(false))
	}
	const submitLabel = busy ? 'Resuming…' : resumed ? 'Resumed — running handler…' : 'Resume'

	return (
		<div>
			{alreadyBy !== null && (
				<Notice tone="neutral">Already resumed by {alreadyBy.label}{alreadyBy.at ? ` at ${fmtDateTime(alreadyBy.at)}` : ''}</Notice>
			)}
			{err !== '' && <Notice tone="warning">{err}</Notice>}
			<div className="mt-2">
				<label className="app-label" htmlFor={`event-${i.id}`}>Event — a JSON object passed to {i.handler ?? 'the handler'} as ctx["event"]</label>
				<textarea id={`event-${i.id}`} className="app-area" rows={4} spellCheck={false}
					value={event} disabled={readOnly}
					onChange={(e) => { setEvent(e.target.value); persist(e.target.value) }} />
				<div className="mt-2 flex items-center gap-2">
					<Button variant="primary" disabled={readOnly}
						onClick={() => {
							const p = eventParse(event)
							if (!p.ok) { setErr(p.message); return }
							setConfirmEv({ json: p.json })
						}}>
						<SwapLabel show={submitLabel} options={['Resume', 'Resuming…', 'Resumed — running handler…']} />
					</Button>
				</div>
			</div>
			{confirmEv && (
				<Confirm title={`Resume ${i.id}?`} confirm="Resume" pending="Resuming…"
					action={() => submit(confirmEv.json)}
					onClose={() => setConfirmEv(null)}>
					<p className="text-sm text-body">Calls {i.handler ?? 'the handler'} with this event:</p>
					<pre className="app-out-pre mt-1">{confirmEv.json}</pre>
				</Confirm>
			)}
		</div>
	)
}
