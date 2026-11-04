import test from 'node:test'
import assert from 'node:assert/strict'
import { draftFromSpec, buildInstanceSpec } from './instanceDraft.ts'

// environment survives draft->spec roundtrip; unknown top-level
// fields preserved via draft.extra.
test('environment bindings roundtrip through instanceDraft', () => {
  const spec = {
    environment: {
      extensions: {
        tavily: {
          bindings: {
            default: { secrets: { api_key: { ref: 'KEY' } } },
            stage: {
              secrets: {
                api_key: { ref: 'STAGE', allowed_domains: ['s.example.com'] },
                token: { ref: 'TOK' },
              },
            },
          },
        },
      },
    },
    unknownTop: { keep: true },
    capabilities: { net: { allow_hosts: ['h'], methods: ['GET'] } },
  }
  const draft = draftFromSpec(spec as never)
  const built = buildInstanceSpec(draft)
  assert.ok(built.ok)
  const env = (built as { spec: { environment?: any } }).spec.environment
  assert.equal(env.extensions.tavily.bindings.default.secrets.api_key.ref, 'KEY')
  assert.deepEqual(env.extensions.tavily.bindings.stage.secrets.api_key.allowed_domains, ['s.example.com'])
  assert.equal(env.extensions.tavily.bindings.stage.secrets.token.ref, 'TOK')
  assert.deepEqual((built as { spec: Record<string, unknown> }).spec.unknownTop, { keep: true })
})

import { setBindingField, removeBindingField, validIdent } from './environment.ts'

test('setBindingField merges without nesting or dropping siblings', () => {
  let env = setBindingField({}, 'credentials', 'default', 'api_key', { ref: 'K1' })
  env = setBindingField(env, 'credentials', 'default', 'client_secret', { ref: 'K2' })
  env = setBindingField(env, 'credentials', 'stage', 'api_key', { ref: 'K3' })
  const e = (env.extensions as Record<string, { bindings: Record<string, { secrets: Record<string, { ref: string }> }> }>)['credentials']
  assert.equal(e.bindings.default.secrets.api_key.ref, 'K1')
  assert.equal(e.bindings.default.secrets.client_secret.ref, 'K2')
  assert.equal(e.bindings.stage.secrets.api_key.ref, 'K3')
  assert.equal((env as { environment?: unknown }).environment, undefined, 'no nested .extensions')
  env = removeBindingField(env, 'credentials', 'default', 'api_key')
  const e2 = (env.extensions as Record<string, { bindings: Record<string, { secrets: Record<string, { ref: string }> }> }>)['credentials']
  assert.equal(e2.bindings.default.secrets.api_key, undefined)
  assert.equal(e2.bindings.default.secrets.client_secret.ref, 'K2')
  assert.equal(e2.bindings.stage.secrets.api_key.ref, 'K3')
  env = removeBindingField(env, 'credentials', 'stage', 'api_key')
  assert.equal((env.extensions as Record<string, { bindings: Record<string, unknown> }>)['credentials'].bindings.stage, undefined)
})

test('validIdent matches runtime identifier rules', () => {
  assert.equal(validIdent('api_key'), true)
  assert.equal(validIdent('1bad'), false)
  assert.equal(validIdent('a/b'), false)
  assert.equal(validIdent('a b'), false)
})

test('buildInstanceSpec: env default binding satisfies secret field, stage preserved', () => {
  const draft = draftFromSpec()
  draft.exts = [{ alias: 'credentials', source: 'fixture/credentials', config: {} } as never]
  draft.extra = {
    environment: {
      extensions: {
        credentials: {
          bindings: {
            default: { secrets: { api_key: { ref: 'K1' }, client_secret: { ref: 'K2' } } },
            stage: { secrets: { api_key: { ref: 'S1' } } },
          },
        },
      },
    },
  }
  const catalog = [{ source: 'fixture/credentials', name: 'credentials', config: [
    { name: 'api_key', type: 'secret' }, { name: 'client_secret', type: 'secret' },
  ] }] as never
  const built = buildInstanceSpec(draft, { catalog })
  assert.ok(built.ok, JSON.stringify((built as { errors?: unknown }).errors))
  const env = (built as { spec: { environment: any } }).spec.environment
  assert.equal(env.extensions.credentials.bindings.default.secrets.api_key.ref, 'K1')
  assert.equal(env.extensions.credentials.bindings.default.secrets.client_secret.ref, 'K2')
  assert.equal(env.extensions.credentials.bindings.stage.secrets.api_key.ref, 'S1')
})

test('buildInstanceSpec: env covers only its field — other fields still validated', () => {
  const draft = draftFromSpec()
  // api_key comes from env default; client_secret carries an INVALID
  // legacy config value — only the client_secret error may surface.
  draft.exts = [{ alias: 'credentials', source: 'fixture/credentials', config: { client_secret: 'not-a-secret-ref' } } as never]
  draft.extra = { environment: { extensions: { credentials: { bindings: { default: { secrets: { api_key: { ref: 'K1' } } } } } } } }
  const catalog = [{ source: 'fixture/credentials', name: 'credentials', config: [
    { name: 'api_key', type: 'secret' }, { name: 'client_secret', type: 'secret' },
  ] }] as never
  const built = buildInstanceSpec(draft, { catalog })
  assert.equal(built.ok, false)
  const errs = (built as { errors: { message: string }[] }).errors.map((e) => e.message)
  assert.ok(errs.some((e) => e.includes('client_secret')))
  assert.ok(!errs.some((e) => e.includes('api_key')), JSON.stringify(errs))
})

test('legacy config placeholder path still validates without env', () => {
  const draft = draftFromSpec()
  draft.exts = [{ alias: 'credentials', source: 'fixture/credentials', config: { api_key: '{{secrets.MYKEY}}' } } as never]
  draft.secrets = [{ name: 'MYKEY', kind: 'vault', ref: 'MYKEY', value: '', domains: [], extra: {} }]
  const catalog = [{ source: 'fixture/credentials', name: 'credentials', config: [{ name: 'api_key', type: 'secret' }] }] as never
  const built = buildInstanceSpec(draft, { catalog })
  assert.ok(built.ok, JSON.stringify((built as { errors?: unknown }).errors))
})
