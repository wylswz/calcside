import { test } from 'node:test'
import assert from 'node:assert/strict'
import { draftFromSpec, buildInstanceSpec, validateRawSpec } from './instanceDraft.ts'
import type { ExtensionInfo } from './api'

const errs = (r: any): string[] => r.ok ? [] : r.errors.map((e: any) => `${e.section}: ${e.message}`)

test('defaults: pure compute, no fs/net', () => {
  const r = buildInstanceSpec(draftFromSpec())
  assert.ok(r.ok)
  assert.equal(r.spec.capabilities?.fs, undefined)
  assert.equal(r.spec.capabilities?.net, undefined)
  assert.equal(r.spec.ttl_seconds, 900)
})

test('policies: absent vs [] vs list', () => {
  assert.equal(buildInstanceSpec(draftFromSpec()).ok && (buildInstanceSpec(draftFromSpec()) as any).spec.policies, undefined)
  const d = draftFromSpec(); d.policies = []
  assert.deepEqual((buildInstanceSpec(d) as any).spec.policies, [])
  d.policies = ['builtin.block_private_network']
  assert.deepEqual((buildInstanceSpec(d) as any).spec.policies, ['builtin.block_private_network'])
})

test('round-trip: populated spec preserves net fields, ext config, limits, labels', () => {
  const src: any = {
    ttl_seconds: 90, labels: { team: 'x=y' },
    env: { FOO: 'bar' },
    secrets: { API_KEY: { ref: 'vault-key', allowed_domains: ['api.example.com'] } },
    capabilities: {
      fs: { quota_bytes: 1000 },
      net: { allow_hosts: ['a.com', 'b.com'], methods: ['GET'] },
      ext: { search: { source: 'contrib/tav', version: 'v1', sum: 'h1:abc', config: { retries: 3, tags: ['x', 'y'], opt: { a: '1' } } } },
      unknown_cap: { whatever: true },
    },
    limits: { max_steps: 50 },
    policies: [],
  }
  const d = draftFromSpec(src)
  assert.equal(d.ttlSeconds, 90) // exact, no rounding
  assert.deepEqual(d.labels, [{ k: 'team', v: 'x=y' }])
  assert.deepEqual(d.net!.allowHosts, ['a.com', 'b.com'])
  assert.deepEqual(d.net!.methods, ['GET'])
  assert.equal(d.exts[0].alias, 'search')
  assert.equal(d.exts[0].sum, 'h1:abc')
  assert.equal(d.exts[0].config.retries, 3) // typed, not '3'
  const r = buildInstanceSpec(d)
  assert.ok(r.ok, JSON.stringify(errs(r)))
  const out: any = r.spec
  assert.deepEqual(out.capabilities.net, { allow_hosts: ['a.com', 'b.com'], methods: ['GET'] })
  assert.equal(out.capabilities.ext.search.source, 'contrib/tav')
  assert.equal(out.capabilities.unknown_cap.whatever, true)
  assert.equal(out.limits.max_steps, 50)
  assert.deepEqual(out.policies, [])
})

test('duplicates rejected: aliases, env, secrets', () => {
  const d = draftFromSpec()
  d.exts = [
    { alias: 'a', source: 'x/ext', config: {}, extra: {} },
    { alias: 'a', source: 'x/ext2', config: {}, extra: {} },
  ]
  d.env = [{ k: 'A', v: '1' }, { k: 'A', v: '2' }]
  d.secrets = [{ name: 'S', kind: 'vault', ref: 'r', value: '', domains: [], extra: {} }, { name: 'S', kind: 'vault', ref: 'r2', value: '', domains: [], extra: {} }]
  const r = buildInstanceSpec(d)
  assert.ok(!r.ok)
  assert.ok(errs(r).some(e => e.includes('duplicate alias')))
  assert.ok(errs(r).some(e => e.includes('duplicate variable')))
  assert.ok(errs(r).some(e => e.includes('duplicate secret')))
})

test('invalid numbers rejected', () => {
  const d = draftFromSpec(); d.ttlSeconds = -5
  assert.ok(errs(buildInstanceSpec(d)).some(e => e.includes('TTL')))
  const d2 = draftFromSpec(); d2.fs = { quotaBytes: -1, extra: {} }
  assert.ok(errs(buildInstanceSpec(d2)).some(e => e.includes('quota')))
})

test('cloned inline secret blocks until replaced or removed', () => {
  const d = draftFromSpec({ secrets: { TOK: { value: '' } } } as any)
  assert.ok(d.secrets[0].needsReplacement)
  assert.ok(errs(buildInstanceSpec(d)).some(e => e.includes('replaced or removed')))
  d.secrets[0].value = 'new-value'
  assert.ok(buildInstanceSpec(d).ok)
})

test('keyMode rejects inline secrets', () => {
  const d = draftFromSpec()
  d.secrets = [{ name: 'S', kind: 'inline', ref: '', value: 'x', domains: [], extra: {} }]
  assert.ok(errs(buildInstanceSpec(d, { keyMode: true })).some(e => e.includes('vault')))
  assert.ok(buildInstanceSpec(d).ok)
})

const manifest: ExtensionInfo = {
  source: 'contrib/search', name: 'search', dependencies: ['net'], ops: [],
  config: [
    { name: 'token', type: 'secret', default: '{{secrets.TOKEN}}' },
    { name: 'retries', type: 'int', default: 3 },
  ],
}

test('manifest dependency and missing secret block without auto-granting Network', () => {
  const d = draftFromSpec({ capabilities: { ext: { search: { source: manifest.source, config: {} } } } })
  const result = buildInstanceSpec(d, { catalog: [manifest] })
  assert.ok(!result.ok)
  assert.ok(result.errors.some((e) => e.message.includes('requires Network')))
  assert.ok(result.errors.some((e) => e.message.includes('secret reference')))
  assert.equal(d.net, null)
  d.net = { allowHosts: ['api.example.com'], methods: ['GET'], extra: {} }
  d.secrets = [{ name: 'TOKEN', kind: 'vault', ref: 'OTHER_TOKEN', value: '', domains: [], extra: {} }]
  assert.ok(buildInstanceSpec(d, { catalog: [manifest] }).ok)
})

test('complete round-trip preserves typed extension values, inner extras and secret aliases', () => {
  const src = {
    ttl_seconds: 90, labels: { name: 'report', team: 'x=y,z' },
    env: { REGION: 'test' },
    secrets: { TOKEN: { ref: 'OTHER_TOKEN', source: 'vault' as const, allowed_domains: ['api.example.com'] } },
    capabilities: {
      fs: { quota_bytes: 2000, future: true },
      net: { allow_hosts: ['api.example.com'], methods: ['POST'], timeout_ms: 1000 },
      ext: { search: { source: 'contrib/search', sum: 'h1:test', future: 'keep', config: { token: '{{secrets.TOKEN}}', retries: 3, flags: ['a', 'b'], map: { x: 'y' }, enabled: false } } },
      io: { future: 'keep' },
    },
    limits: { max_steps: 999, exec_timeout_ms: 777, max_output_bytes: 555 },
    policies: [],
    future: { keep: true },
  }
  const r = buildInstanceSpec(draftFromSpec(src))
  assert.ok(r.ok)
  // Null-prototype dictionaries deliberately survive hostile keys; JSON
  // comparison checks the actual wire representation rather than prototypes.
  assert.deepEqual(JSON.parse(JSON.stringify(r.spec)), src)
})

test('empty Files configuration does not inject a quota', () => {
  const r = buildInstanceSpec(draftFromSpec({ capabilities: { fs: {} } }))
  assert.ok(r.ok)
  assert.deepEqual(r.spec.capabilities?.fs, {})
})

test('advanced mode rejects malformed nested shapes before hydration', () => {
  for (const raw of [null, [], { capabilities: [] }, { capabilities: { ext: [] } }, { capabilities: { ext: { a: { source: 12 } } } },
    { capabilities: { net: { allow_hosts: 'host' } } }, { secrets: { TOKEN: null } }, { limits: { max_steps: '5' } }, { env: { REGION: {} } }, { policies: [12] }]) {
    assert.ok(validateRawSpec(raw).length, JSON.stringify(raw))
  }
})

test('key mode rejects mixed ref/value descriptors through JSON', () => {
  const raw = { secrets: { TOKEN: { ref: 'TOKEN', value: 'do-not-store' } } }
  assert.ok(validateRawSpec(raw, true).some((e) => e.message.includes('inline')))
  assert.ok(validateRawSpec(raw).some((e) => e.message.includes('mutually exclusive')))
})

test('variables and secrets use runtime identifier grammar and cannot collide', () => {
  const d = draftFromSpec()
  d.env = [{ k: 'lower.case', v: 'x' }, { k: 'TOKEN', v: 'x' }]
  d.secrets = [{ name: 'TOKEN', ref: 'TOKEN', kind: 'vault', value: '', domains: [], extra: {} }]
  const r = buildInstanceSpec(d)
  assert.ok(!r.ok)
  assert.ok(r.errors.some((e) => e.message.includes('uppercase')))
  assert.ok(r.errors.some((e) => e.message.includes('collides')))
})

test('numeric settings require safe positive integers', () => {
  for (const value of [0, -1, 1.1, Number.NaN, Number.POSITIVE_INFINITY, Number.MAX_SAFE_INTEGER + 1]) {
    const d = draftFromSpec()
    d.ttlSeconds = value
    d.limits.max_steps = value
    const r = buildInstanceSpec(d)
    assert.ok(!r.ok)
    assert.ok(r.errors.some((e) => e.section === 'Lifetime'))
    assert.ok(r.errors.some((e) => e.section === 'Limits'))
  }
})

test('remote sources require version in source and h1 SHA256 sum', () => {
  const d = draftFromSpec({ capabilities: { ext: { search: { source: 'github.com/team/search' } } } })
  assert.ok(!buildInstanceSpec(d).ok)
  d.exts[0].source = 'github.com/team/search@v1'
  d.exts[0].sum = 'h1:' + 'A'.repeat(43) + '='
  assert.ok(buildInstanceSpec(d).ok)
})

test('object prototype property names remain data, not mutations or false duplicates', () => {
  const d = draftFromSpec({ labels: JSON.parse('{"__proto__":"kept","toString":"kept"}') })
  const r = buildInstanceSpec(d)
  assert.ok(r.ok)
  assert.equal(r.spec.labels?.__proto__, 'kept')
  assert.equal(r.spec.labels?.toString, 'kept')
  assert.equal(({} as Record<string, string>).kept, undefined)
})
