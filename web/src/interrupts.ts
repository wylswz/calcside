// Pure helpers for the EP-0016 interrupts console — unit-tested, no UI.
import type { Interrupt } from './api'

// resolveDraftKey scopes an in-progress resolution draft (note/event
// text) to the editing user, workspace and interrupt so polling,
// refetches and navigation never lose or leak it (C2).
export function resolveDraftKey(userId: string, wsId: string, interruptId: string): string {
	return JSON.stringify(['intrv1', userId, wsId, interruptId])
}

// buildApprovalEvent composes the resolution event for the default
// approval form as a raw JSON string — the body is posted verbatim, so
// nothing passes through a float64 round trip.
export function buildApprovalEvent(approved: boolean, note: string): string {
	if (note === '') return `{"approved":${approved}}`
	return `{"approved":${approved},"note":${JSON.stringify(note)}}`
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

// hasCustomSchema reports whether the interrupt declares its own event
// schema (default approvals get the approve/reject form instead).
export function hasCustomSchema(i: Interrupt): boolean {
	return i.event_schema != null && i.event_schema !== ''
}

// deadlineOf is the interrupt's next due instant for TimeLeft.
export function deadlineOf(i: Interrupt): string | undefined {
	return i.fire_at ?? i.expires_at ?? undefined
}

// interruptTitle is the row title: the program's title or the kind.
export function interruptTitle(i: Interrupt): string {
	return i.title != null && i.title !== '' ? i.title : (i.kind ?? 'interrupt')
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

// --- structured handlers editor -------------------------------------

// HandlerRow is one declared handler in the application draft. `text`
// holds the context_schema verbatim (raw JSON) — big integers inside
// schemas never pass through JSON.parse→stringify.
export type HandlerRow = { name: string; on: boolean; text: string }

const handlerNameRe = /^[A-Za-z][A-Za-z0-9_]*$/

// handlerNameErr reports why a handler name is invalid: not an
// identifier, the forbidden "main", a leading underscore, or a
// duplicate (checked against the full row list).
export function handlerNameErr(name: string, rows: readonly HandlerRow[], self: number): string {
	if (name === 'main') return '"main" is reserved'
	if (!handlerNameRe.test(name)) return 'must be a valid name (letters, digits, _, not leading _)'
	for (let k = 0; k < rows.length; k++) {
		if (k !== self && rows[k].name === name) return 'duplicate name'
	}
	return ''
}

// schemaErr reports why an enabled context schema is not a JSON object.
export function schemaErr(text: string): string {
	const t = text.trim()
	if (t === '') return 'required'
	try {
		const v = JSON.parse(t)
		if (v === null || typeof v !== 'object' || Array.isArray(v)) return 'must be a JSON object'
		return ''
	} catch { return 'invalid JSON' }
}

// serializeHandlers builds the declarations document by concatenating
// the pieces — schema text is embedded verbatim, so >2^53 literals
// survive intact.
export function serializeHandlers(rows: readonly HandlerRow[]): string {
	const parts = rows
		.filter((r) => handlerNameErr(r.name, rows, rows.indexOf(r)) === '')
		.map((r) => {
			const t = r.text.trim()
			if (r.on && t !== '' && schemaErr(t) === '') {
				return `${JSON.stringify(r.name)}:{"context_schema":${t}}`
			}
			return `${JSON.stringify(r.name)}:{}`
		})
	return `{${parts.join(',')}}`
}

// parseHandlers splits a stored declarations document into rows without
// parsing schema bodies — member names are read via JSON.parse on the
// key only; each context_schema value is extracted by brace matching so
// its text stays byte-exact.
export function parseHandlers(text: string): HandlerRow[] {
	const t = (text || '').trim()
	if (t === '') return []
	let doc: Record<string, unknown>
	try { doc = JSON.parse(t) } catch { return [{ name: '', on: true, text: t }] }
	if (doc === null || typeof doc !== 'object' || Array.isArray(doc)) {
		return [{ name: '', on: true, text: t }]
	}
	const rows: HandlerRow[] = []
	for (const name of Object.keys(doc)) {
		const schema = extractContextSchema(t, name)
		if (schema === null) rows.push({ name, on: false, text: '' })
		else rows.push({ name, on: true, text: schema })
	}
	return rows
}

// extractContextSchema finds `"name"` at top level in the document
// text, then `"context_schema"` inside that member's object, and
// returns the balanced-brace JSON text that follows, or null.
function extractContextSchema(doc: string, name: string): string | null {
	// Locate the member by scanning for its JSON-encoded name followed
	// by ':' at document depth 1.
	const key = JSON.stringify(name)
	let i = doc.indexOf(key)
	let memberStart = -1
	while (i !== -1) {
		let j = i + key.length
		while (doc[j] === ' ' || doc[j] === '\n' || doc[j] === '\t' || doc[j] === '\r') j++
		if (doc[j] === ':') { memberStart = j + 1; break }
		i = doc.indexOf(key, j)
	}
	if (memberStart === -1) return null
	// The member value is an object; find "context_schema" inside it.
	const cs = doc.indexOf('"context_schema"', memberStart)
	if (cs === -1) return null
	let j = cs + '"context_schema"'.length
	while (doc[j] === ' ' || doc[j] === '\n' || doc[j] === '\t' || doc[j] === '\r') j++
	if (doc[j] !== ':') return null
	j++
	while (doc[j] === ' ' || doc[j] === '\n' || doc[j] === '\t' || doc[j] === '\r') j++
	// Brace-match the value (objects; strings/brackets inside skipped).
	const start = j
	let depth = 0, instr = false, esc = false
	for (; j < doc.length; j++) {
		const c = doc[j]
		if (esc) { esc = false; continue }
		if (c === '\\' && instr) { esc = true; continue }
		if (c === '"') { instr = !instr; continue }
		if (instr) continue
		if (c === '{' || c === '[') depth++
		else if (c === '}' || c === ']') {
			depth--
			if (depth === 0) return doc.slice(start, j + 1)
			if (depth < 0) return null
		}
	}
	return null
}
