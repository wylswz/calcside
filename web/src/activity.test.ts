import { test } from 'node:test'
import assert from 'node:assert/strict'
import { allowedSummary, callOutcome, denialReason, groupCalls, instanceName, isBuiltinGlobal, isConsoleCall, runBy, runOutcome } from './activity.ts'
import { relTime } from './format.ts'
import type { AuditEvent, Execution, Instance } from './api.ts'

const ev = (patch: Partial<AuditEvent>): AuditEvent => ({
  id: 'a1', ts: '2026-01-01T00:00:00Z', user_id: 'u', instance_id: 'i', exec_id: 'e',
  capability: 'fs', op: 'read', args: '{}', phase: '', decision: 'allow', reason: '',
  error: '', duration_ms: 1, ...patch,
} as AuditEvent)

const exec = (patch: Partial<Execution>): Execution => ({
  id: 'e', instance_id: 'i', user_id: 'u', code_sha256: '', code_snippet: '',
  status: 'ok', duration_ms: 1, steps: 1, output_bytes: 0, created_at: '2026-01-01T00:00:00Z',
  ...patch,
} as Execution)

test('callOutcome', () => {
  assert.equal(callOutcome(ev({ decision: 'deny' })), 'denied')
  assert.equal(callOutcome(ev({ error: 'boom' })), 'failed')
  assert.equal(callOutcome(ev({ decision: 'deny', error: 'x' })), 'denied')
  assert.equal(callOutcome(ev({})), 'ok')
})

test('runOutcome', () => {
  assert.equal(runOutcome(exec({ status: 'ok' })), 'ok')
  assert.equal(runOutcome(exec({ status: 'error', error_type: 'runtime' })), 'failed')
  assert.equal(runOutcome(exec({ status: 'error', error_type: 'policy_denied' })), 'denied')
})

test('denialReason', () => {
  assert.equal(
    denialReason('denied by net (before): net: host "x.com" (port 443) not in allow_hosts'),
    'net: host "x.com" (port 443) not in allow_hosts',
  )
  assert.equal(
    denialReason('denied by policy (before): policy builtin.block_private_network: net: host "h" has disallowed address'),
    'policy builtin.block_private_network: net: host "h" has disallowed address',
  )
  assert.equal(denialReason('plain reason'), 'plain reason')
})

test('isBuiltinGlobal', () => {
  const caps = ['fs', 'net']
  assert.equal(isBuiltinGlobal('fs', '<module "fs">', caps), true)
  assert.equal(isBuiltinGlobal('env', 'whatever', caps), true)
  assert.equal(isBuiltinGlobal('secrets', 'whatever', caps), true)
  assert.equal(isBuiltinGlobal('helper', '<built-in function helper>', caps), true)
  assert.equal(isBuiltinGlobal('answer', '42', caps), false)
  assert.equal(isBuiltinGlobal('fs', '42', caps), true)
})

test('runBy', () => {
  assert.equal(runBy(exec({ via: 'api_key' })), 'agent')
  assert.equal(runBy(exec({ via: 'session' })), 'you')
  assert.equal(runBy(exec({ via: 'anonymous' })), 'you')
  assert.equal(runBy(exec({})), 'unknown')
})

test('isConsoleCall', () => {
  assert.equal(isConsoleCall(ev({ exec_id: 'console' })), true)
  assert.equal(isConsoleCall(ev({ exec_id: 'exe_1' })), false)
})

test('instanceName', () => {
  const inst = { id: 'ins_1', labels: { task: 'recon', env: 'dev' } } as unknown as Instance
  assert.equal(instanceName(inst), 'env=dev · task=recon')
  assert.equal(instanceName({ id: 'ins_2', labels: {} } as unknown as Instance), 'ins_2')
  assert.equal(instanceName({ id: 'ins_3' } as unknown as Instance), 'ins_3')
})

test('allowedSummary', () => {
  assert.equal(allowedSummary({ capabilities: {} } as any), 'nothing granted')
  assert.equal(allowedSummary(undefined), 'nothing granted')
  const spec = {
    capabilities: {
      fs: { quota_bytes: 64 * 1024 * 1024 },
      net: { allow_hosts: ['a.com', 'b.com'] },
      ext: { tavily: { source: 'contrib/tavily' } },
    },
  } as any
  assert.equal(allowedSummary(spec), 'ext tavily · fs 64.0 MiB · net 2 hosts')
  assert.equal(
    allowedSummary({ capabilities: { net: { allow_hosts: ['a.com'] }, io: {} } } as any),
    'io · net 1 host',
  )
})

test('groupCalls', () => {
  const a = ev({ id: '1', exec_id: 'e1', ts: '2026-01-01T00:00:02Z' })
  const b = ev({ id: '2', exec_id: 'e1', ts: '2026-01-01T00:00:01Z' })
  const c = ev({ id: '3', exec_id: 'e2', ts: '2026-01-01T00:00:03Z' })
  const g = groupCalls([a, b, c])
  assert.deepEqual([...(g.get('e1') ?? [])].map((e) => e.id), ['2', '1'])
  assert.equal(g.get('e2')?.length, 1)
})

test('relTime', () => {
  const now = new Date('2026-01-01T00:10:00Z').getTime()
  assert.equal(relTime('2026-01-01T00:09:00Z', now), '1m ago')
  assert.equal(relTime('2026-01-01T00:00:00Z', now), '10m ago')
  assert.equal(relTime('2025-12-31T22:00:00Z', now), '2h ago')
})
