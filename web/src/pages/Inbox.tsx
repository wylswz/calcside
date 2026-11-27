import { Link, useSearchParams } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Interrupt, listInterrupts, getInterrupt, currentWorkspace } from '../api'
import { useHeldOrder } from '../stableList'
import { Loading, Notice, RevealNew, TimeLeft } from '../components/ui'
import { ResumeForm } from '../components/ResumeForm'
import { InterruptContext } from '../components/InterruptContext'
import { useRunProgress } from '../components/RunProgress'
import { deadlineOf, interruptTitle, resolverLabel, fmtDateTime } from '../interrupts'
import './inbox.css'

// The interrupt inbox: a mail-client split — a dense held-order list on
// the left, the selected interrupt's context + resume form on the
// right. Selection lives in ?interrupt= for deep links (C4); a row
// resolved since the last reveal keeps its box until the user reveals
// fresh data (S3/F1).
export default function Inbox() {
	const [params, setParams] = useSearchParams()
	const sel = params.get('interrupt')
	const qc = useQueryClient()
	const me = qc.getQueryData<{ user: { id: string } }>(['me'])
	const ws = currentWorkspace() ?? ''

	const q = useQuery({
		queryKey: ['inbox', ws],
		queryFn: () => listInterrupts({ status: 'pending', resumable: true }),
		refetchInterval: 5000,
	})
	const held = useHeldOrder(q.data?.interrupts?.map((i) => ({ i })) ?? undefined, ({ i }) => ({ key: i.id, slot: 'inbox' }), { keepGone: true })

	// Deep link: a selected interrupt may sit outside the resumable
	// page (resolved elsewhere or out of scope) — fetch it anyway (C4).
	const selQ = useQuery({
		queryKey: ['interrupt', sel],
		queryFn: () => getInterrupt(sel!),
		enabled: sel != null,
	})
	// The list row is fresher for status; the single-interrupt fetch
	// supplies context_json / resume_url the list omits. A gone row
	// never wins, and a fetch that already shows the interrupt resolved
	// beats a stale pending list row.
	const listedRow = held.rows.find((r) => r.item.i.id === sel)
	const listed = listedRow && !listedRow.gone ? listedRow.item.i : undefined
	const fetched = selQ.data?.interrupt
	const base = fetched && fetched.status !== 'pending' ? fetched : listed ?? fetched
	const selected: Interrupt | undefined = base ? {
		...base,
		context_json: base.context_json ?? fetched?.context_json,
		resume_url: base.resume_url ?? fetched?.resume_url,
	} : undefined
	// A resolved selection keeps following its run so the detail can
	// show the live phase (a resumed run may wait again or finish).
	const selRun = useRunProgress(selected && selected.status !== 'pending' ? selected.run_id : undefined)

	return (
		<div className={`inbox-split${sel ? ' has-sel' : ''}`}>
			<div className="inbox-list">
				<div className="inbox-list-bar">
					<span className="inbox-list-title">Inbox</span>
					<span className="inbox-list-count">{held.rows.length || ''}</span>
					<RevealNew count={held.arrived} moved={held.moved || held.gone > 0} onClick={held.reveal} />
				</div>
				{q.isLoading && <Loading />}
				{q.isError && <Notice tone="warning">Could not load interrupts: {(q.error as Error).message}</Notice>}
				<div className="inbox-items">
					{held.rows.length === 0 && held.arrived === 0 && !q.isLoading && (
						<p className="inbox-empty">Nothing waiting for you.</p>
					)}
					{held.rows.map(({ item: { i }, gone }) => gone ? (
						<GoneRow key={i.id} interrupt={i} />
					) : (
						<button key={i.id} type="button" className="inbox-item"
							aria-current={sel === i.id ? 'true' : undefined}
							onClick={() => setParams({ interrupt: i.id })}>
							<span className="inbox-item-top">
								<span className="inbox-item-title">{interruptTitle(i)}</span>
								{deadlineOf(i) && <span className="inbox-item-time"><TimeLeft expiresAt={deadlineOf(i)!} /></span>}
							</span>
							<span className="inbox-item-sub">
								{i.application_name} · <code className="inbox-item-run">{i.run_id}</code>
							</span>
						</button>
					))}
				</div>
			</div>
			<div className="inbox-detail">
				{sel && <button type="button" className="inbox-back" onClick={() => setParams({})}>‹ Inbox</button>}
				{!selected && <p className="inbox-empty inbox-detail-empty">Select an interrupt</p>}
				{selected && (
					<>
						<header className="inbox-detail-head">
							<h1 className="inbox-detail-title">{interruptTitle(selected)}</h1>
							<code className="inbox-detail-id">{selected.id}</code>
						</header>
						<dl className="kv-strip">
							<div><dt>Application</dt><dd><Link className="link" to={`/applications/${selected.application_id}`}>{selected.application_name}</Link></dd></div>
							<div><dt>Run</dt><dd><Link className="link" to={`/applications/${selected.application_id}/runs/${selected.run_id}`}><code>{selected.run_id}</code></Link></dd></div>
							<div><dt>Created</dt><dd>{fmtDateTime(selected.created_at)}</dd></div>
							<div><dt>Expires</dt><dd>{deadlineOf(selected) ? fmtDateTime(deadlineOf(selected)!) : '—'}</dd></div>
						</dl>
						<InterruptContext interrupt={selected} />
						{selected.status === 'pending' && selected.can_resume ? (
							<section className="inbox-resume">
								<ResumeForm key={selected.id} interrupt={selected}
									scope={{ userId: me?.user.id ?? '', workspaceId: ws }}
									onResumed={() => {
										qc.invalidateQueries({ queryKey: ['inbox'] })
										qc.invalidateQueries({ queryKey: ['interrupt', selected.id] })
									}} />
							</section>
						) : (
							<p className="inbox-resolved">
								{selected.status !== 'pending'
									? `Resolved by ${resolverLabel(selected)}${selected.resolved_at ? ` at ${fmtDateTime(selected.resolved_at)}` : ''} · `
									: ''}
								{selRun.phase && <>Run {selRun.phase.phase} · </>}
								<Link className="link" to={`/applications/${selected.application_id}/runs/${selected.run_id}`}>View run</Link>
							</p>
						)}
					</>
				)}
			</div>
		</div>
	)
}

// GoneRow is the inert placeholder for a row that left the resumable
// page — same box (S3/F1), showing who resolved it once the
// single-interrupt fetch lands. It vanishes only on reveal or
// navigation.
function GoneRow({ interrupt: i }: { interrupt: Interrupt }) {
	const q = useQuery({
		queryKey: ['interrupt', i.id],
		queryFn: () => getInterrupt(i.id),
	})
	const resolved = q.data?.interrupt
	const text = resolved?.resolved_at
		? `Resolved by ${resolverLabel(resolved)}`
		: 'No longer waiting'
	return (
		<div className="inbox-item inbox-item-gone" aria-disabled="true">
			<span className="inbox-item-top">
				<span className="inbox-item-title">{interruptTitle(i)} — {text}</span>
			</span>
			<span className="inbox-item-sub">
				{i.application_name} · <code className="inbox-item-run">{i.run_id}</code>
				{' · '}<Link className="link" to={`/applications/${i.application_id}/runs/${i.run_id}`}>View run</Link>
			</span>
		</div>
	)
}
