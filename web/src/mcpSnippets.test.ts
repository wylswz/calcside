import { test } from 'node:test'
import assert from 'node:assert/strict'
import { claudeCodeSnippet, cursorSnippet, headerSnippet, mcpURL } from './mcpSnippets.ts'

test('mcpURL appends /mcp', () => {
  assert.equal(mcpURL('http://localhost:5173'), 'http://localhost:5173/mcp')
  assert.equal(mcpURL('https://x.example/'), 'https://x.example/mcp')
})

test('claude code snippet carries url and bearer header', () => {
  const s = claudeCodeSnippet('https://x.example/mcp', 'cs_abc')
  assert.equal(s, 'claude mcp add --transport http calcside https://x.example/mcp --header "Authorization: Bearer cs_abc"')
})

test('cursor snippet is valid JSON with the key', () => {
  const s = cursorSnippet('https://x.example/mcp', 'cs_abc')
  const o = JSON.parse(s)
  assert.equal(o.mcpServers.calcside.url, 'https://x.example/mcp')
  assert.equal(o.mcpServers.calcside.headers.Authorization, 'Bearer cs_abc')
})

test('generic header snippet', () => {
  assert.equal(headerSnippet('cs_k'), 'Authorization: Bearer cs_k')
})
