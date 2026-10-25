import { test } from 'node:test'
import assert from 'node:assert/strict'
import { wsPath, setWorkspace } from './api.ts'

test('wsPath appends workspace param', () => {
  setWorkspace('ws_team1')
  assert.equal(wsPath('/api/v1/instances'), '/api/v1/instances?workspace=ws_team1')
  assert.equal(wsPath('/api/v1/audit?limit=50'), '/api/v1/audit?limit=50&workspace=ws_team1')
  setWorkspace(null)
  assert.equal(wsPath('/api/v1/instances'), '/api/v1/instances')
})
