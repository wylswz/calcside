import { useState } from 'react'
import { ApiError, Interrupt, resolveInterrupt } from '../api'
import { buildApprovalEvent, eventParse, hasCustomSchema, resolverLabel, fmtDateTime, resolveDraftKey } from '../interrupts'
import { Button, Confirm, Notice, SwapLabel, TimeLeft } from './ui'

// ResolveForm is the shared human-decision control for a pending
// interrupt (EP-0016). It never navigates (P3): the surrounding view
// updates in place via onResolved. Confirm dialogs show the exact
// event JSON before firing (F2/P1); a lost race surfaces
// already_resolved in place with the typed input kept (P5/C2/F3).
export function ResolveForm({ interrupt: i, scope, onResolved }: {
	interrupt: Interrupt
	scope: { userId: string; workspaceId: string }
	onResolved?: (i: Interrupt) => void
}) {
	const isApproval = i.kind === 'approval'
	const custom = !isApproval || hasCustomSchema(i)
	const draftKey = resolveDraftKey(scope.userId, scope.workspaceId, i.id)
	const [note, setNote] = useState(() => {
		try { return sessionStorage.getItem(draftKey) ?? '' } catch { return '' }
	})
	const [event, setEvent] = useState(() => {
		try { return sessionStorage.getItem(draftKey + '.e') ?? '' } catch { return '' }
	})
	const [busy, setBusy] = useState<'' | 'approve' | 'reject' | 'resolve'>('')
	const [err, setErr] = useState('')
	const [alreadyBy, setAlreadyBy] = useState<{ label: string; at?: string } | null>(null)
	const [confirmEv, setConfirmEv] = useState<{ action: 'approve' | 'reject' | 'resolve'; json: string; title: string } | null>(null)

	const persist = (k: string, v: string) => {
		try { if (v === '') sessionStorage.removeItem(k); else sessionStorage.setItem(k, v) } catch { /* storage unavailable */ }
	}
	const clearDraft = () => { persist(draftKey, ''); persist(draftKey + '.e', '') }
	const readOnly = busy !== '' || alreadyBy !== null || i.status !== 'pending'

	const submit = (action: 'approve' | 'reject' | 'resolve', json: string) => {
		setConfirmEv(null)
		setBusy(action)
		setErr('')
		return resolveInterrupt(i.id, json)
			.then((r) => {
				clearDraft()
				if (r.interrupt) onResolved?.(r.interrupt)
			})
			.catch((e) => {
				if (e instanceof ApiError && e.code === 'already_resolved') {
					setAlreadyBy({ label: resolverLabel(i) || 'someone', at: i.resolved_at })
					return
				}
				setErr(e instanceof Error ? e.message : 'resolve failed')
			})
			.finally(() => setBusy(''))
	}

	if (i.kind === 'timer') {
		// Timers resolve themselves; no human action exists (P6).
		return <p className="text-sm text-body">Timer fires {i.fire_at ? <TimeLeft expiresAt={i.fire_at} /> : 'as scheduled'}.</p>
	}

	return (
		<div>
			{alreadyBy !== null && (
				<Notice tone="neutral">Already resolved by {alreadyBy.label}{alreadyBy.at ? ` at ${fmtDateTime(alreadyBy.at)}` : ''}</Notice>
			)}
			{err !== '' && <Notice tone="warning">{err}</Notice>}
			{!custom ? (
				<div className="mt-2">
					<label className="app-label" htmlFor={`note-${i.id}`}>Note</label>
					<textarea id={`note-${i.id}`} className="app-area" rows={2} spellCheck={false}
						value={note} disabled={readOnly}
						onChange={(e) => { setNote(e.target.value); persist(draftKey, e.target.value) }} />
					<div className="mt-2 flex items-center gap-2">
						<Button variant="primary" disabled={readOnly}
							onClick={() => setConfirmEv({ action: 'approve', json: buildApprovalEvent(true, note.trim()), title: 'Approve' })}>
							<SwapLabel show={busy === 'approve' ? 'Approving…' : 'Approve'} options={['Approve', 'Approving…']} />
						</Button>
						<Button variant="danger" disabled={readOnly}
							onClick={() => setConfirmEv({ action: 'reject', json: buildApprovalEvent(false, note.trim()), title: 'Reject' })}>
							<SwapLabel show={busy === 'reject' ? 'Rejecting…' : 'Reject'} options={['Reject', 'Rejecting…']} />
						</Button>
					</div>
				</div>
			) : (
				<div className="mt-2">
					<label className="app-label" htmlFor={`event-${i.id}`}>
						Event — a JSON object{i.kind === 'callback' ? ' · resolve manually' : ''}
					</label>
					<textarea id={`event-${i.id}`} className="app-area" rows={4} spellCheck={false}
						value={event} disabled={readOnly}
						onChange={(e) => { setEvent(e.target.value); persist(draftKey + '.e', e.target.value) }} />
					<div className="mt-2 flex items-center gap-2">
						<Button variant="primary" disabled={readOnly}
							onClick={() => {
								const p = eventParse(event)
								if (!p.ok) { setErr(p.message); return }
								setConfirmEv({ action: 'resolve', json: p.json, title: 'Resolve' })
							}}>
							<SwapLabel show={busy === 'resolve' ? 'Resolving…' : 'Resolve'} options={['Resolve', 'Resolving…']} />
						</Button>
					</div>
				</div>
			)}
			{confirmEv && (
				<Confirm title={`${confirmEv.title} ${i.title ?? i.id}?`} confirm={confirmEv.title}
					pending={confirmEv.action === 'approve' ? 'Approving…' : confirmEv.action === 'reject' ? 'Rejecting…' : 'Resolving…'}
					action={() => submit(confirmEv.action, confirmEv.json)}
					onClose={() => setConfirmEv(null)}>
					<p className="text-sm text-body">Sends this event:</p>
					<pre className="app-out-pre mt-1">{confirmEv.json}</pre>
				</Confirm>
			)}
		</div>
	)
}
