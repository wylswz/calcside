import { test } from 'node:test'
import assert from 'node:assert/strict'
import { eventParse, resumeDraftKey, deadlineOf, interruptTitle, resolverLabel, runPhase } from './interrupts.ts'
import type { RunDetail, RunStep } from './api.ts'
import type { Interrupt } from './api.ts'

const intr = (o: Partial<Interrupt>): Interrupt => o as Interrupt

test('resumeDraftKey scopes by user + workspace + interrupt (C2)', () => {
	assert.equal(resumeDraftKey('u1', 'w1', 'int_a'), '["intrv1","u1","w1","int_a"]')
	assert.notEqual(resumeDraftKey('u1', 'w1', 'int_a'), resumeDraftKey('u2', 'w1', 'int_a'))
	assert.notEqual(resumeDraftKey('u1', 'w1', 'int_a'), resumeDraftKey('u1', 'w2', 'int_a'))
	assert.notEqual(resumeDraftKey('u1', 'w1', 'int_a'), resumeDraftKey('u1', 'w1', 'int_b'))
})

test('eventParse accepts only JSON objects', () => {
	assert.deepEqual(eventParse('  '), { ok: false, message: 'required' })
	assert.deepEqual(eventParse('{bad'), { ok: false, message: 'invalid JSON' })
	assert.deepEqual(eventParse('[1]'), { ok: false, message: 'must be a JSON object' })
	assert.deepEqual(eventParse('5'), { ok: false, message: 'must be a JSON object' })
	const p = eventParse(' {"a":9007199254740993} ')
	assert.equal(p.ok, true)
	if (p.ok) assert.equal(p.json, '{"a":9007199254740993}') // verbatim
})

test('deadlineOf / titles / resolver labels', () => {
	assert.equal(deadlineOf(intr({ expires_at: 'x' })), 'x')
	assert.equal(deadlineOf(intr({})), undefined)
	assert.equal(interruptTitle(intr({})), 'interrupt')
	assert.equal(interruptTitle(intr({ handler: 'on_ok' })), '→ on_ok')
	assert.equal(resolverLabel(intr({ resolved_by_label: 'Ann' })), 'Ann')
	assert.equal(resolverLabel(intr({ resolved_by: 'token' })), 'token')
})

const run = (o: Partial<RunDetail>): RunDetail => o as RunDetail
const step = (o: Partial<RunStep>): RunStep => o as RunStep

test('runPhase derives waiting/running/terminal phases', () => {
	const pending = intr({ id: 'int_1', status: 'pending', handler: 'on_ok' })
	assert.deepEqual(runPhase({ run: run({ status: 'waiting' }), interrupts: [pending] }).phase, 'waiting')
	// A resolved-but-undispatched interrupt means the handler is running.
	const resolved = intr({ id: 'int_1', status: 'resolved', handler: 'on_ok', created_at: 'a' })
	const p = runPhase({ run: run({ status: 'waiting' }), interrupts: [resolved] })
	assert.equal(p.phase, 'running')
	assert.equal(p.phase === 'running' && p.handler, 'on_ok')
	// Expired dispatch uses on_expire.
	const expired = intr({ id: 'int_1', status: 'expired', handler: 'on_ok', on_expire: 'on_exp' })
	const pe = runPhase({ run: run({ status: 'waiting' }), interrupts: [expired] })
	assert.equal(pe.phase === 'running' && pe.handler, 'on_exp')
	// Chained second pending interrupt flips back to waiting.
	const chained = [resolved, intr({ id: 'int_2', status: 'pending', handler: 'on_next', created_at: 'b' })]
	assert.equal(runPhase({ run: run({ status: 'waiting' }), interrupts: chained }).phase, 'waiting')
	// Running step entry is the fallback handler label.
	const ps = runPhase({ run: run({ status: 'running' }), steps: [step({ status: 'running', entry: 'main' })] })
	assert.equal(ps.phase === 'running' && ps.handler, 'main')
	// Terminal outcomes.
	assert.deepEqual(runPhase({ run: run({ status: 'succeeded', output_json: '{"ok":true}' }) }), { phase: 'succeeded', output: '{"ok":true}' })
	assert.deepEqual(runPhase({ run: run({ status: 'failed', error_code: 'chain_limit' }) }), { phase: 'failed', errorCode: 'chain_limit' })
})
