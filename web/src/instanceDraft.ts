// instanceDraft is the pure typed layer between the console editors
// and the wire InstanceSpec. draftFromSpec lifts a spec (or nothing)
// into a draft; buildInstanceSpec validates and serializes without
// dropping unknown fields anywhere in the tree.
import type { components } from './api/schema'

export type InstanceSpec = components['schemas']['InstanceSpec']
export type SecretSpec = components['schemas']['SecretSpec']
type ExtensionInfo = components['schemas']['ExtensionInfo']

export type JsonValue = string | number | boolean | null | JsonValue[] | { [k: string]: JsonValue }

export interface ExtDraft {
  alias: string
  source: string
  version?: string
  sum?: string
  custom?: boolean          // free-form source/sum editor
  config: Record<string, JsonValue> // raw typed values, keyed by manifest field
  extra: Record<string, JsonValue>  // unknown fields on this ext entry
}

export interface SecretDraft {
  name: string
  kind: 'vault' | 'inline'
  ref: string
  value: string
  domains: string[]        // parsed allowed_domains
  needsReplacement?: boolean
  extra: Record<string, JsonValue>
}

export interface InstanceDraft {
  name: string
  ttlSeconds: number | ''
  fs: { quotaBytes: number | '' ; extra: Record<string, JsonValue> } | null
  net: { allowHosts: string[]; methods: string[]; extra: Record<string, JsonValue> } | null
  exts: ExtDraft[]
  policies: string[] | null   // null = omitted/defaults; [] = explicit none
  env: { k: string; v: string }[]
  secrets: SecretDraft[]
  labels: { k: string; v: string }[]
  limits: Record<string, number>
  otherCaps: Record<string, JsonValue>
  extra: Record<string, JsonValue>
}

const identRe = /^[A-Za-z_][A-Za-z0-9_]*$/
const inputNameRe = /^[A-Z_][A-Z0-9_]{0,63}$/

// reserved aliases an extension must not shadow
const RESERVED = new Set(['fs', 'net', 'ext', 'print', 'load', 'exec'])

export function draftFromSpec(spec?: InstanceSpec): InstanceDraft {
  const d: InstanceDraft = {
    name: '', ttlSeconds: 900, fs: null, net: null, exts: [],
    policies: null, env: [], secrets: [], labels: [], limits: {},
    otherCaps: Object.create(null), extra: Object.create(null),
  }
  if (!spec) return d
  const s = spec as Record<string, any>
  const { ttl_seconds, labels, env, secrets, capabilities, limits, policies, ...rest } = s
  if (ttl_seconds != null) d.ttlSeconds = ttl_seconds
  if (labels) {
    if (labels.name) d.name = String(labels.name)
    d.labels = Object.entries(labels).filter(([k]) => k !== 'name').map(([k, v]) => ({ k, v: String(v) }))
  }
  if (env) d.env = Object.entries(env).map(([k, v]) => ({ k, v: String(v) }))
  if (secrets) d.secrets = Object.entries(secrets).map(([name, v]: [string, any]) => {
    const { ref, value, allowed_domains, ...rest } = v ?? {}
    return {
      name,
      kind: ref ? 'vault' as const : 'inline' as const,
      ref: ref ?? '',
      value: value ?? '',
      needsReplacement: !ref && !value,
      domains: Array.isArray(allowed_domains) ? allowed_domains.map(String) : [],
      extra: rest,
    }
  })
  const caps = (capabilities ?? {}) as Record<string, any>
  for (const [k, v] of Object.entries(caps)) {
    if (k === 'fs') {
      const { quota_bytes, ...rest } = v ?? {}
      d.fs = { quotaBytes: quota_bytes ?? '', extra: rest }
    } else if (k === 'net') {
      const { allow_hosts, methods, ...rest } = v ?? {}
      d.net = { allowHosts: Array.isArray(allow_hosts) ? allow_hosts.map(String) : [],
        methods: Array.isArray(methods) ? methods.map(String) : ['GET', 'POST'], extra: rest }
    } else if (k === 'ext') {
      for (const [alias, e] of Object.entries(v as Record<string, any>)) {
        const { source, version, sum, config, ...rest } = e ?? {}
        d.exts.push({
          alias, source: source ?? '', version, sum,
          custom: true,
          config: { ...(config ?? {}) },
          extra: rest,
        })
      }
    } else d.otherCaps[k] = v
  }
  if (limits) d.limits = { ...limits }
  if (policies !== undefined) d.policies = policies === null ? null : [...policies]
  for (const [k, v] of Object.entries(rest)) d.extra[k] = v
  return d
}

export interface DraftError { section: string; message: string }
export type BuildResult = { ok: true; spec: InstanceSpec } | { ok: false; errors: DraftError[]; spec?: InstanceSpec }

const num = (v: number | '', section: string, label: string, errors: DraftError[]): number | null => {
  if (v === '') { errors.push({ section, message: `${label} is required` }); return null }
  if (!Number.isSafeInteger(v) || v <= 0) { errors.push({ section, message: `${label} must be a positive integer` }); return null }
  return v
}

// buildInstanceSpec serializes a draft; keyMode applies restricted-key
// rules (no inline secret values anywhere). catalog (ext manifest list)
// is used for manifest-level required-field/dependency checks.
export function buildInstanceSpec(draft: InstanceDraft, opts: { keyMode?: boolean; catalog?: ExtensionInfo[] } = {}): BuildResult {
  const errors: DraftError[] = []
  const spec: Record<string, any> = { ...draft.extra }

  const ttl = num(draft.ttlSeconds, 'Lifetime', 'TTL', errors)
  if (ttl) spec.ttl_seconds = ttl

  const labels: Record<string, string> = Object.create(null)
  for (const r of draft.labels) {
    if (!r.k.trim()) continue
    if (Object.hasOwn(labels, r.k)) errors.push({ section: 'Labels', message: `duplicate label ${r.k}` })
    labels[r.k] = r.v ?? ''
  }
  if (draft.name) labels.name = draft.name
  spec.labels = labels

  const env: Record<string, string> = Object.create(null)
  for (const r of draft.env) {
    if (!r.k.trim()) continue
    if (Object.hasOwn(env, r.k)) errors.push({ section: 'Variables', message: `duplicate variable ${r.k}` })
    else if (!inputNameRe.test(r.k)) errors.push({ section: 'Variables', message: `${r.k}: use an uppercase variable name` })
    env[r.k] = r.v
  }
  spec.env = env

  const secrets: Record<string, any> = Object.create(null)
  for (const r of draft.secrets) {
    if (!r.name.trim()) continue
    if (Object.hasOwn(secrets, r.name)) errors.push({ section: 'Secrets', message: `duplicate secret ${r.name}` })
    else if (!inputNameRe.test(r.name)) errors.push({ section: 'Secrets', message: `${r.name}: use an uppercase secret name` })
    if (r.kind === 'inline') {
      if (opts.keyMode) errors.push({ section: 'Secrets', message: `${r.name}: restricted keys only allow vault references` })
      else if (r.needsReplacement && !r.value) errors.push({ section: 'Secrets', message: `${r.name}: cloned inline value must be replaced or removed` })
      else if (!r.value) errors.push({ section: 'Secrets', message: `${r.name}: empty value` })
      secrets[r.name] = { ...r.extra, value: r.value, ...(r.extra.source ? { source: 'inline' } : {}) }
    } else {
      if (!r.ref) errors.push({ section: 'Secrets', message: `${r.name}: missing vault reference` })
      secrets[r.name] = { ...r.extra, ref: r.ref, ...(r.extra.source ? { source: 'vault' } : {}) }
    }
    if (Object.hasOwn(env, r.name)) errors.push({ section: 'Secrets', message: `${r.name}: collides with a variable name` })
    const domains = r.domains.map((d) => d.trim()).filter(Boolean)
    if (domains.length) secrets[r.name].allowed_domains = domains
  }
  spec.secrets = secrets

  const caps: Record<string, any> = { ...draft.otherCaps }
  if (draft.fs) {
    const e: Record<string, any> = { ...draft.fs.extra }
    const q = draft.fs.quotaBytes === '' ? null : num(draft.fs.quotaBytes, 'Files', 'quota', errors)
    if (q) e.quota_bytes = q   // fs:{} stays quota-free
    caps.fs = e
  }
  if (draft.net) caps.net = {
    ...draft.net.extra,
    allow_hosts: draft.net.allowHosts.map((h) => h.trim()).filter(Boolean),
    methods: draft.net.methods.map((m) => m.trim()).filter(Boolean),
  }

  const seen = new Set<string>()
  const manifestOf = (src: string) => opts.catalog?.find((x) => x.source === src)
  for (const ex of draft.exts) {
    if (!ex.alias) { errors.push({ section: 'Extensions', message: 'extension alias is required' }); continue }
    if (!identRe.test(ex.alias)) errors.push({ section: 'Extensions', message: `${ex.alias}: invalid alias` })
    if (RESERVED.has(ex.alias)) errors.push({ section: 'Extensions', message: `${ex.alias}: reserved name` })
    if (seen.has(ex.alias)) errors.push({ section: 'Extensions', message: `duplicate alias ${ex.alias}` })
    seen.add(ex.alias)
    if (!ex.source) { errors.push({ section: 'Extensions', message: `${ex.alias}: missing source` }); continue }
    const ent: Record<string, any> = { source: ex.source, ...ex.extra }
    if (ex.version) ent.version = ex.version
    if (ex.sum) ent.sum = ex.sum
    const remote = ex.source.includes('@') || ex.source.split('/').length >= 3
    if (remote) {
      if (!/^[A-Za-z0-9][A-Za-z0-9.-]*\/[A-Za-z0-9_][A-Za-z0-9._-]*\/[A-Za-z0-9_][A-Za-z0-9._-]*@[A-Za-z0-9_][A-Za-z0-9._+-]*$/.test(ex.source) || ex.source.includes('..')) {
        errors.push({ section: 'Extensions', message: `${ex.alias}: use domain/group/name@version for a remote source` })
      }
      if (!ex.sum || !/^h1:[A-Za-z0-9+/]{43}=$/.test(ex.sum)) errors.push({ section: 'Extensions', message: `${ex.alias}: remote sources require an h1: SHA-256 sum` })
    } else if (!/^[A-Za-z0-9_-][A-Za-z0-9._-]*\/[A-Za-z0-9_-][A-Za-z0-9._-]*$/.test(ex.source)) {
      errors.push({ section: 'Extensions', message: `${ex.alias}: use root-name/extension for a local source` })
    }
    const cfg: Record<string, JsonValue> = Object.create(null)
    for (const [k, v] of Object.entries(ex.config)) {
      cfg[k] = v
    }
    if (Object.keys(cfg).length) ent.config = cfg
    const man = manifestOf(ex.source)
    if (man?.config) {
      for (const f of man.config) {
        const value: unknown = ex.config[f.name] ?? f.default
        if (value === undefined || value === null) continue // The runtime supplies type zero values.
        const valid = f.type === 'int' ? Number.isSafeInteger(value)
          : f.type === 'bool' ? typeof value === 'boolean'
          : f.type === 'string_list' ? Array.isArray(value) && value.every((v) => typeof v === 'string')
          : f.type === 'string_map' ? isRecord(value) && Object.values(value).every((v) => typeof v === 'string')
          : typeof value === 'string'
        if (!valid) errors.push({ section: 'Extensions', message: `${ex.alias}: ${f.name} must have type ${f.type}` })
        if (f.type === 'secret') {
          const match = typeof value === 'string' ? value.match(/^\{\{\s*secrets\.([A-Z_][A-Z0-9_]{0,63})\s*\}\}$/) : null
          if (!match || !Object.hasOwn(secrets, match[1])) {
            errors.push({ section: 'Extensions', message: `${ex.alias}: ${f.name} requires an installed secret reference` })
          }
        }
      }
    }
    for (const dependency of man?.dependencies ?? []) {
      const installed = dependency === 'net' ? !!draft.net : dependency === 'fs' ? !!draft.fs : Object.hasOwn(draft.otherCaps, dependency)
      if (!installed) errors.push({ section: 'Extensions', message: `${ex.alias}: requires ${dependency === 'net' ? 'Network' : dependency === 'fs' ? 'Files' : dependency} — add it explicitly` })
    }
    caps.ext = caps.ext ?? Object.create(null)
    caps.ext[ex.alias] = ent
  }
  spec.capabilities = caps
  spec.limits = { ...draft.limits }
  for (const [k, v] of Object.entries(draft.limits)) {
    if (!Number.isSafeInteger(v) || v <= 0) errors.push({ section: 'Limits', message: `${k} must be a positive integer` })
  }
  if (draft.policies !== null) spec.policies = draft.policies

  // keyMode: nothing may carry an inline value anywhere in the spec.
  if (opts.keyMode) {
    for (const [n, v] of Object.entries(spec.secrets)) {
      if (v && typeof v === 'object' && 'value' in v) errors.push({ section: 'Secrets', message: `${n}: inline values are forbidden for restricted keys` })
    }
  }
  if (errors.length) return { ok: false, errors, spec: spec as InstanceSpec }
  return { ok: true, spec: spec as InstanceSpec }
}

// validateRawSpec checks a hand-edited JSON spec without flattening it:
// shape errors map back to sections; keyMode's inline ban applies.
export function validateRawSpec(raw: unknown, keyMode?: boolean): DraftError[] {
  const errors: DraftError[] = []
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) {
    return [{ section: 'JSON', message: 'spec must be an object' }]
  }
  const r = raw as Record<string, any>
  if (r.ttl_seconds !== undefined && (!Number.isSafeInteger(r.ttl_seconds) || r.ttl_seconds <= 0)) {
    errors.push({ section: 'Lifetime', message: 'ttl_seconds must be a positive integer' })
  }
  for (const k of ['labels', 'env', 'secrets', 'capabilities', 'limits']) {
    if (r[k] !== undefined && (r[k] === null || typeof r[k] !== 'object' || Array.isArray(r[k]))) {
      errors.push({ section: k, message: `${k} must be an object` })
    }
  }
  if (errors.length) return errors
  for (const key of ['labels', 'env']) {
    if (r[key] && !Object.values(r[key]).every((v) => typeof v === 'string')) errors.push({ section: key, message: `${key} values must be strings` })
  }
  for (const [name, secret] of Object.entries(r.secrets ?? {})) {
    if (!isRecord(secret)) {
      errors.push({ section: 'Secrets', message: `${name} must be an object` })
      continue
    }
    if (Object.hasOwn(secret, 'ref') && Object.hasOwn(secret, 'value')) errors.push({ section: 'Secrets', message: `${name}: ref and value are mutually exclusive` })
    if (secret.ref !== undefined && typeof secret.ref !== 'string') errors.push({ section: 'Secrets', message: `${name}: ref must be a string` })
    if (secret.value !== undefined && typeof secret.value !== 'string') errors.push({ section: 'Secrets', message: `${name}: value must be a string` })
    if (secret.allowed_domains !== undefined && !stringList(secret.allowed_domains)) errors.push({ section: 'Secrets', message: `${name}: allowed_domains must be a string list` })
  }
  const caps = r.capabilities ?? {}
  for (const name of ['fs', 'net', 'ext']) {
    if (caps[name] !== undefined && !isRecord(caps[name])) errors.push({ section: 'Abilities', message: `${name} must be an object` })
  }
  if (isRecord(caps.fs) && caps.fs.quota_bytes !== undefined && (!Number.isSafeInteger(caps.fs.quota_bytes) || Number(caps.fs.quota_bytes) <= 0)) errors.push({ section: 'Files', message: 'quota_bytes must be a positive integer' })
  if (isRecord(caps.net)) {
    for (const name of ['allow_hosts', 'methods']) {
      if (caps.net[name] !== undefined && !stringList(caps.net[name])) errors.push({ section: 'Network', message: `${name} must be a string list` })
    }
  }
  if (isRecord(caps.ext)) {
    for (const [alias, ext] of Object.entries(caps.ext)) {
      if (!isRecord(ext) || typeof ext.source !== 'string' || (ext.config !== undefined && !isRecord(ext.config)) || (ext.sum !== undefined && typeof ext.sum !== 'string')) {
        errors.push({ section: 'Extensions', message: `${alias}: expected source, optional sum and object config` })
      }
    }
  }
  if (r.limits && !Object.values(r.limits).every((v) => typeof v === 'number' && Number.isSafeInteger(v) && v > 0)) errors.push({ section: 'Limits', message: 'limits must be positive integers' })
  if (r.policies !== undefined && r.policies !== null && !Array.isArray(r.policies)) {
    errors.push({ section: 'Rules', message: 'policies must be a list' })
  }
  if (Array.isArray(r.policies) && !r.policies.every((v: unknown) => typeof v === 'string')) errors.push({ section: 'Rules', message: 'policy names must be strings' })
  if (keyMode && r.secrets && typeof r.secrets === 'object') {
    for (const [n, v] of Object.entries<any>(r.secrets)) {
      if (v && typeof v === 'object' && 'value' in v) errors.push({ section: 'Secrets', message: `${n}: inline values are forbidden for restricted keys` })
    }
  }
  return errors
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
}

function stringList(value: unknown): value is string[] {
  return Array.isArray(value) && value.every((v) => typeof v === 'string')
}
