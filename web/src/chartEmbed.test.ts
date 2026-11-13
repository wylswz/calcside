import { test } from 'node:test'
import assert from 'node:assert/strict'
import { chartEmbedOptions, denyAllLoader } from './chartEmbed.ts'

test('denyAllLoader rejects every resource method', async () => {
  const loader = denyAllLoader()
  await assert.rejects(loader.load('https://evil.example/data.json'), /disabled/)
  await assert.rejects(loader.sanitize('https://evil.example/x', {} as never), /disabled/)
  await assert.rejects(loader.http('https://evil.example/x', {}), /disabled/)
  await assert.rejects(loader.file('/etc/passwd'), /disabled/)
})

test('chartEmbedOptions locks down the embed', () => {
  for (const dark of [false, true]) {
    const opts = chartEmbedOptions(dark)
    assert.equal(opts.ast, true)
    assert.ok(opts.expr && typeof opts.expr === 'object')
    assert.equal(opts.renderer, 'svg')
    assert.deepEqual(opts.actions, { export: true, source: true, compiled: false, editor: false })
    assert.ok(opts.loader && typeof opts.loader === 'object')
    assert.deepEqual(opts.config, { background: 'transparent' })
    assert.equal(opts.theme, dark ? 'dark' : undefined)
  }
})
