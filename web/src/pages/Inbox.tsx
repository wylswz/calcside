import { Link, useSearchParams } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Interrupt, listInterrupts, getInterrupt, currentWorkspace } from '../api'
import { useHeldOrder } from '../stableList'
import { Loading, Notice, PageHeader, RevealNew, TimeLeft } from '../components/ui'
import { ResolveForm } from '../components/ResolveForm'
import { deadlineOf, interruptTitle, resolverLabel, fmtDateTime } from '../interrupts'

// The interrupt inbox: resolvable pending interrupts, held order with
// "Show N new" (S5/C3). Selection lives in ?interrupt= for deep links
// (C4); a row resolved since the last reveal keeps its box as
// "Resolved by … · View run" until the user reveals fresh data (S3/F1).
export default function Inbox() {
	const [params, setParams] = useSearchParams()
	const sel = params.get('interrupt')
	const qc = useQueryClient()
	const me = qc.getQueryData<{ user: { id: string } }>(['me'])
	const ws = currentWorkspace() ?? ''

	const q = useQuery({
		queryKey: ['inbox', ws],
		queryFn: () => listInterrupts({ status: 'pending', resolvable: true }),
		refetchInterval: 5000,
	})
	const held = useHeldOrder(q.data?.interrupts?.map((i) => ({ i })) ?? undefined, ({ i }) => ({ key: i.id, slot: 'inbox' }), { keepGone: true })

	// Deep link: a selected interrupt may sit outside the resolvable
	// page (resolved elsewhere or out of scope) — fetch it anyway (C4).
	const selQ = useQuery({
		queryKey: ['interrupt', sel],
		queryFn: () => getInterrupt(sel!),
		enabled: sel != null,
	})
	const selected: Interrupt | undefined =
		held.rows.find((r) => r.item.i.id === sel)?.item.i ?? selQ.data?.interrupt

	return (
		<div className="app-page">
			<PageHeader title="Inbox" />
			{q.isLoading && <Loading />}
			{q.isError && <Notice tone="warning">Could not load interrupts: {(q.error as Error).message}</Notice>}
			<RevealNew count={held.arrived} moved={held.moved || held.gone > 0} onClick={held.reveal} />
			{q.data && held.rows.length === 0 && held.arrived === 0 && (
				<p className="app-dim">Nothing waiting for you.</p>
			)}
			<div className="app-grid">
				<div>
					{held.rows.length > 0 && (
						<div className="tbl-wrap">
							<table className="tbl">
								<thead>
									<tr><th>Title</th><th>Kind</th><th>Application</th><th>Deadline</th></tr>
								</thead>
								<tbody>
									{held.rows.map(({ item: { i }, gone }) => gone ? (
										<GoneRow key={i.id} interrupt={i} />
									) : (
										<tr key={i.id} className={`row-hover${sel === i.id ? ' row-sel' : ''}`}
											onClick={() => setParams({ interrupt: i.id })}>
											<td><Link to={`/inbox?interrupt=${i.id}`} className="link font-medium">{interruptTitle(i)}</Link></td>
											<td className="text-xs text-sec">{i.kind}</td>
											<td className="text-xs"><Link className="link" to={`/applications/${i.application_id}`}>{i.application_name}</Link></td>
											<td className="text-xs text-sec">{deadlineOf(i) ? <TimeLeft expiresAt={deadlineOf(i)!} /> : '—'}</td>
										</tr>
									))}
								</tbody>
							</table>
						</div>
					)}
				</div>
				{selected && (
					<section className="app-card">
						<div className="app-card-head"><span className="app-card-title">{interruptTitle(selected)}</span></div>
						<div className="app-card-body">
							{selected.status === 'pending' && selected.can_resolve ? (
								<ResolveForm interrupt={selected}
									scope={{ userId: me?.user.id ?? '', workspaceId: ws }}
									onResolved={() => qc.invalidateQueries({ queryKey: ['inbox'] })} />
							) : (
								<p className="text-sm text-body">
									{selected.status !== 'pending'
										? `Resolved by ${resolverLabel(selected)}${selected.resolved_at ? ` at ${fmtDateTime(selected.resolved_at)}` : ''} · `
										: ''}
									<Link className="link" to={`/applications/${selected.application_id}/runs/${selected.run_id}`}>View run</Link>
								</p>
							)}
						</div>
					</section>
				)}
			</div>
		</div>
	)
}

// GoneRow is the inert placeholder for a row that left the resolvable
// page — same table row and box (S3/F1), showing who resolved it once
// the single-interrupt fetch lands. It vanishes only on reveal or
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
	// Same 4-cell structure as a live row so column widths and the box
	// stay identical (S3); only the title becomes the status text.
	return (
		<tr className="row-gone" aria-disabled="true">
			<td><span className="text-sm text-sec">{interruptTitle(i)} — {text}</span></td>
			<td className="text-xs text-sec">{i.kind}</td>
			<td className="text-xs"><Link className="link" to={`/applications/${i.application_id}`}>{i.application_name}</Link></td>
			<td className="text-xs"><Link className="link" to={`/applications/${i.application_id}/runs/${i.run_id}`}>View run</Link></td>
		</tr>
	)
}
