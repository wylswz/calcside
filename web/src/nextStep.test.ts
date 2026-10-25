import { test } from 'node:test'
import assert from 'node:assert/strict'
import { nextStep, attentionText } from './nextStep.ts'
import type { AuditEvent, Execution } from './api'

const now = Date.parse('2026-10-24T12:00:00Z')
const inst = { status: 'running', created_at: '2026-10-24T11:00:00Z', expires_at: '2026-10-24T13:00:00Z' }

test('nextStep priorities', () => {
  assert.equal(nextStep({ ...inst, status: 'ended' }, null, false, now).kind, 'ended')
  const short = { status: 'running', created_at: inst.created_at, expires_at: '2026-10-24T12:02:00Z' } // ttl 62m → expiring at <5m
  assert.equal(nextStep(short, null, false, now).kind, 'expiring')
  const denied: Pick<Execution, 'status' | 'error_type'> & { calls?: Pick<AuditEvent, 'op' | 'args' | 'decision'>[] } = { status: 'error', error_type: 'policy_denied', calls: [{ op: 'net.get', args: 'example.com', decision: 'deny' }] }
  const r = nextStep(inst, denied, false, now)
  assert.equal(r.kind, 'denied')
  assert.equal((r as any).call, 'net.get example.com')
  assert.equal(nextStep(inst, null, true, now).kind, 'waiting')
  assert.equal(nextStep(inst, null, false, now).kind, 'connect')
  assert.equal(nextStep(inst, { status: 'ok' }, false, now).kind, 'none')
})

test('attentionText', () => {
  assert.deepEqual(attentionText({ status: 'running' }), { text: 'no runs yet', tone: 'mute', link: true })
  assert.deepEqual(attentionText({ status: 'ended' }), { text: '—', tone: 'mute', link: false })
  assert.deepEqual(attentionText({ status: 'ended', activity: { runs: 3 } }), { text: '3 runs', tone: 'sec', link: false })
  assert.equal(attentionText({ status: 'ended', activity: { denied_runs: 2 } }).text, '2 denied')
  assert.equal(attentionText({ status: 'running', activity: { failed_runs: 1 } }).text, '1 failed')
})
test('expiring rule: short TTLs use the half mark', () => {
  const tiny = { status: 'running', created_at: '2026-10-24T11:59:40Z', expires_at: '2026-10-24T12:00:15Z' }
  assert.equal(nextStep(tiny, null, false, now).kind, 'expiring') // left 15s < half of 35s
  const tinyFresh = { status: 'running', created_at: '2026-10-24T11:59:55Z', expires_at: '2026-10-24T12:00:20Z' }
  assert.equal(nextStep(tinyFresh, null, false, now).kind, 'connect') // left 20s > half of 25s
})
