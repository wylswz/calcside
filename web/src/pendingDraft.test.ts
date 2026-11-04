import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readPendingDraft, writePendingDraft } from './pendingDraft.ts'

test('pending drafts stay resource-scoped and acknowledgements clear only their backup', () => {
  const values = new Map<string, string>()
  const storage = {
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => { values.set(key, value) },
    removeItem: (key: string) => { values.delete(key) },
  }
  const valid = (v: unknown): v is string => typeof v === 'string'
  writePendingDraft('workspace-a:module-a', { version: 2, value: 'local source' }, storage)
  writePendingDraft('workspace-a:module-b', { version: 1, value: 'other source' }, storage)
  assert.deepEqual(readPendingDraft('workspace-a:module-a', valid, storage), { version: 2, value: 'local source' })
  assert.equal(readPendingDraft('workspace-b:module-a', valid, storage), null)
  writePendingDraft('workspace-a:module-a', null, storage)
  assert.equal(readPendingDraft('workspace-a:module-a', valid, storage), null)
  assert.equal(readPendingDraft('workspace-a:module-b', valid, storage)?.value, 'other source')
  storage.setItem('broken', '{')
  assert.equal(readPendingDraft('broken', valid, storage), null)
  storage.setItem('wrong-shape', '{"version":1,"value":{}}')
  assert.equal(readPendingDraft('wrong-shape', valid, storage), null)
})

test('unavailable storage does not break server autosave', () => {
  const storage = {
    getItem: () => { throw new Error('blocked') },
    setItem: () => { throw new Error('full') },
    removeItem: () => { throw new Error('blocked') },
  }
  const valid = (v: unknown): v is string => typeof v === 'string'
  assert.doesNotThrow(() => writePendingDraft('a', { version: 1, value: 'source' }, storage))
  assert.equal(readPendingDraft('a', valid, storage), null)
})
