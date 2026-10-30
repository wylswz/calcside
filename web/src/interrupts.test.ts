import { test } from 'node:test'
import assert from 'node:assert/strict'
import { buildApprovalEvent, eventParse, resolveDraftKey, hasCustomSchema, deadlineOf, interruptTitle, resolverLabel } from './interrupts'
import type { Interrupt } from './api'

const intr = (o: Partial<Interrupt>): Interrupt => o as Interrupt

test('resolveDraftKey scopes by user + workspace + interrupt (C2)', () => {
	assert.equal(resolveDraftKey('u1', 'w1', 'int_a'), '["intrv1","u1","w1","int_a"]')
	assert.notEqual(resolveDraftKey('u1', 'w1', 'int_a'), resolveDraftKey('u2', 'w1', 'int_a'))
	assert.notEqual(resolveDraftKey('u1', 'w1', 'int_a'), resolveDraftKey('u1', 'w2', 'int_a'))
	assert.notEqual(resolveDraftKey('u1', 'w1', 'int_a'), resolveDraftKey('u1', 'w1', 'int_b'))
})

test('buildApprovalEvent emits the exact event body, note only when set', () => {
	assert.equal(buildApprovalEvent(true, ''), '{"approved":true}')
	assert.equal(buildApprovalEvent(false, ''), '{"approved":false}')
	assert.equal(buildApprovalEvent(true, 'go "yes"'), '{"approved":true,"note":"go \\"yes\\""}')
	assert.equal(buildApprovalEvent(false, 'no'), '{"approved":false,"note":"no"}')
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

test('hasCustomSchema / deadlineOf / titles', () => {
	assert.equal(hasCustomSchema(intr({})), false)
	assert.equal(hasCustomSchema(intr({ event_schema: '{"type":"object"}' })), true)
	assert.equal(deadlineOf(intr({ expires_at: 'x', fire_at: 'y' })), 'y')
	assert.equal(deadlineOf(intr({ expires_at: 'x' })), 'x')
	assert.equal(interruptTitle(intr({ kind: 'approval' })), 'approval')
	assert.equal(interruptTitle(intr({ kind: 'approval', title: 'ship' })), 'ship')
	assert.equal(resolverLabel(intr({ resolved_by_label: 'Ann' })), 'Ann')
	assert.equal(resolverLabel(intr({ resolved_by: 'timer' })), 'timer')
})

import { serializeHandlers, parseHandlers, handlerNameErr, schemaErr } from './interrupts'
import type { HandlerRow } from './interrupts'

test('serializeHandlers embeds schema verbatim (big ints)', () => {
	const rows: HandlerRow[] = [
		{ name: 'on_ok', on: true, text: '{"enum":[9007199254740993]}' },
		{ name: 'on_x', on: false, text: '' },
		{ name: 'bad', on: true, text: '{oops' },
	]
	assert.equal(serializeHandlers(rows),
		'{"on_ok":{"context_schema":{"enum":[9007199254740993]}},"on_x":{},"bad":{}}')
})

test('parseHandlers round-trips and flags invalid names', () => {
	const doc = '{"on_ok":{"context_schema":{"enum":[9007199254740993]}},"on_x":{}}'
	const rows = parseHandlers(doc)
	assert.equal(rows.length, 2)
	assert.equal(rows[0].on, true)
	assert.equal(rows[0].text, '{"enum":[9007199254740993]}')
	assert.equal(serializeHandlers(rows), doc)
	assert.equal(parseHandlers('').length, 0)
	assert.equal(handlerNameErr('main', rows, 0), '"main" is reserved')
	assert.equal(handlerNameErr('on_ok', rows, 0), 'duplicate name')
	assert.notEqual(handlerNameErr('good_name', rows, 0), '')
	assert.equal(schemaErr('{"type":"object"}'), '')
	assert.notEqual(schemaErr('[1]'), '')
})
