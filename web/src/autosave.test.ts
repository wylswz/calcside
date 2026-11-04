import { test } from 'node:test'
import assert from 'node:assert/strict'
import { AutosaveQueue } from './autosave.ts'

const deferred = <T>() => {
  let resolve!: (v: T) => void, reject!: (e: Error) => void
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej })
  return { promise, resolve, reject }
}
const mk = (save: (v: string, ver: number) => Promise<{ value: string; version: number }>) =>
  new AutosaveQueue<string>({ initial: { value: 'a', version: 1 }, fingerprint: (v) => v, save, delayMs: 10 })

test('an unpersisted initial draft still saves after edits are reverted', async () => {
  const calls: [string, number][] = []
  const q = new AutosaveQueue({
    initial: { value: 'scaffold', version: 0 }, initialDirty: true, fingerprint: (v) => v,
    save: async (value, version) => { calls.push([value, version]); return { value, version: version + 1 } },
  })
  q.update('changed')
  q.update('scaffold')
  await q.flush()
  assert.deepEqual(calls, [['scaffold', 0]])
  assert.equal(q.snapshot.dirty, false)
  q.dispose()
})

test('debounce coalesces edits into one write', async () => {
  const calls: string[] = []
  const q = mk(async (v, ver) => { calls.push(v); return { value: v, version: ver + 1 } })
  q.update('b'); q.update('c')
  await q.flush()
  assert.deepEqual(calls, ['c'])
  assert.equal(q.snapshot.saved.version, 2)
})

test('update to saved value produces no write', async () => {
  let n = 0
  const q = mk(async (v, ver) => { n++; return { value: v, version: ver + 1 } })
  q.update('a')
  await q.flush()
  assert.equal(n, 0)
})

test('first flush already includes mid-flight edits via CAS chain', async () => {
  const calls: [string, number][] = []
  const gate = deferred<{ value: string; version: number }>()
  const q = mk(async (v, ver) => {
    calls.push([v, ver])
    if (calls.length === 1) return gate.promise
    return { value: v, version: ver + 1 }
  })
  q.update('b')
  const f = q.flush()
  q.update('x') // mid-flight — must use returned version 2
  gate.resolve({ value: 'b', version: 2 })
  const r = await f
  assert.deepEqual(calls, [['b', 1], ['x', 2]])
  assert.equal(r.version, 3)
  assert.equal(q.snapshot.latest, 'x')
  assert.equal(q.snapshot.dirty, false)
})

test('parallel flushes share one flight', async () => {
  let n = 0
  const q = mk(async (v, ver) => { n++; await new Promise((r) => setTimeout(r, 5)); return { value: v, version: ver + 1 } })
  q.update('b')
  await Promise.all([q.flush(), q.flush(), q.flush()])
  assert.equal(n, 1)
})

test('revert during flight writes back to the saved baseline', async () => {
  const calls: [string, number][] = []
  const gate = deferred<{ value: string; version: number }>()
  const q = mk(async (v, ver) => { calls.push([v, ver]); if (calls.length === 1) return gate.promise; return { value: v, version: ver + 1 } })
  q.update('b')
  const f = q.flush()
  q.update('a') // reverted to baseline
  gate.resolve({ value: 'b', version: 2 })
  await f
  assert.deepEqual(calls, [['b', 1], ['a', 2]])
  assert.equal(q.snapshot.dirty, false)
})

test('409 is sticky — no auto-retry until reset', async () => {
  const err = Object.assign(new Error('conflict'), { status: 409 })
  const q = mk(async () => { throw err })
  q.update('b')
  await assert.rejects(q.flush())
  assert.equal(q.snapshot.conflict, true)
  await assert.rejects(q.retry())
  q.reset({ value: 'a', version: 2 })
  assert.equal(q.snapshot.conflict, false)
  q.dispose()
})

test('transient error retained; explicit retry works', async () => {
  let fail = true
  const q = mk(async (v, ver) => { if (fail) throw new Error('net'); return { value: v, version: ver + 1 } })
  q.update('b')
  await assert.rejects(q.flush())
  assert.equal(q.snapshot.latest, 'b')
  fail = false
  await q.retry()
  assert.equal(q.snapshot.saved.version, 2)
  q.dispose()
})

test('scheduled flush rejection is swallowed and never loops', async () => {
  let n = 0
  const q = mk(async () => { n++; throw new Error('net') })
  q.update('b')
  await new Promise((r) => setTimeout(r, 60)) // 3+ delays — must NOT retry
  assert.equal(q.snapshot.error?.message, 'net')
  assert.equal(n, 1)
  q.dispose()
})

test('dispose stops timers; in-flight ack cannot write', async () => {
  const gate = deferred<{ value: string; version: number }>()
  let n = 0
  const q = mk(async (v, ver) => { n++; if (n === 1) return gate.promise; return { value: v, version: ver + 1 } })
  q.update('b')
  const f = q.flush()
  q.update('c')
  q.dispose()
  gate.resolve({ value: 'b', version: 2 })
  await f
  await new Promise((r) => setTimeout(r, 20))
  assert.equal(n, 1) // no follow-up write after dispose
})
