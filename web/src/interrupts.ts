// Pure helpers for the EP-0016 interrupts console — unit-tested, no UI.
import type { Interrupt, RunDetail, RunStep } from './api'

// resumeDraftKey scopes an in-progress resume event draft to the
// editing user, workspace and interrupt so polling, refetches and
// navigation never lose or leak it (C2).
export function resumeDraftKey(userId: string, wsId: string, interruptId: string): string {
	return JSON.stringify(['intrv1', userId, wsId, interruptId])
}

// eventParse validates free-form event input: it must be a JSON object
// (P5 — errors are rendered next to the control, never thrown).
export function eventParse(text: string): { ok: true; json: string } | { ok: false; message: string } {
	const t = text.trim()
	if (t === '') return { ok: false, message: 'required' }
	try {
		const v = JSON.parse(t)
		if (v === null || typeof v !== 'object' || Array.isArray(v)) {
			return { ok: false, message: 'must be a JSON object' }
		}
		return { ok: true, json: t }
	} catch {
		return { ok: false, message: 'invalid JSON' }
	}
}

// deadlineOf is the interrupt's expiry instant for TimeLeft.
export function deadlineOf(i: Interrupt): string | undefined {
	return i.expires_at ?? undefined
}

// interruptTitle is the row label: the handler the resume will call —
// interrupts carry no presentation fields.
export function interruptTitle(i: Interrupt): string {
	return i.handler ? `→ ${i.handler}` : 'interrupt'
}

// RunPhase is the derived UI phase of one GET /api/v1/runs/{id}
// response: a run waiting on a pending interrupt, a run whose handler
// is being dispatched/executed, or a terminal outcome.
export type RunPhase =
	| { phase: 'waiting'; interrupt: Interrupt }
	| { phase: 'running'; handler?: string }
	| { phase: 'succeeded'; output?: string }
	| { phase: 'failed' | 'expired' | 'cancelled' | 'unknown'; errorCode?: string }

const terminalRun = new Set(['succeeded', 'failed', 'expired', 'cancelled', 'unknown'])

// runPhase derives the phase: a pending interrupt means waiting (a
// chained second interrupt flips a running run back to waiting); a
// resolved/expired-but-not-yet-finished run means running.
export function runPhase(detail: { run?: RunDetail; steps?: RunStep[]; interrupts?: Interrupt[] }): RunPhase {
	const run = detail.run
	const intrs = detail.interrupts ?? []
	const pending = intrs.find((i) => i.status === 'pending')
	if (pending) return { phase: 'waiting', interrupt: pending }
	const st = run?.status ?? ''
	if (st === 'succeeded') return { phase: 'succeeded', output: run?.output_json ?? undefined }
	if (terminalRun.has(st)) return { phase: st as 'failed' | 'expired' | 'cancelled' | 'unknown', errorCode: run?.error_code ?? undefined }
	const latest = [...intrs].sort((a, b) => (a.created_at ?? '').localeCompare(b.created_at ?? '')).at(-1)
	const handler = latest ? (latest.status === 'expired' ? (latest.on_expire ?? latest.handler) : latest.handler) : undefined
	const entry = detail.steps?.filter((s) => s.status === 'running').at(-1)?.entry
	return { phase: 'running', handler: handler ?? entry ?? undefined }
}

// resolverLabel names whoever resolved — a user label or the
// machine resolvers the API reports verbatim.
export function resolverLabel(i: Interrupt): string {
	return i.resolved_by_label ?? i.resolved_by ?? ''
}

// fmtDateTime renders timestamps like the rest of the console.
export function fmtDateTime(iso?: string): string {
	if (!iso) return ''
	const d = new Date(iso)
	return d.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })
}
