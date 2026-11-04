import type { components } from './api/schema'

type Env = NonNullable<components['schemas']['SpecEnvironment']>
type Exts = NonNullable<Env['extensions']>
type BindingMap = NonNullable<Exts[string]['bindings']>
type FieldMap = NonNullable<BindingMap[string]['secrets']>
type SecretField = FieldMap[string]

// Identifier: non-empty, no whitespace/slash (matches runtime env
// alias/binding/field path rules).
export function validIdent(v: string): boolean {
  return /^[A-Za-z_][A-Za-z0-9_]{0,63}$/.test(v)
}

// setBindingField merges one field into alias/binding — siblings and
// unknown properties on every level are preserved.
export function setBindingField(env: Env, alias: string, binding: string, field: string, sf: SecretField): Env {
  const next: Env = { ...env, extensions: { ...(env.extensions ?? {}) } }
  const exts = next.extensions as Exts
  const e = { ...(exts[alias] ?? {}) }
  const bs = { ...(e.bindings ?? {}) }
  const b = { ...(bs[binding] ?? {}) }
  b.secrets = { ...(b.secrets ?? {}), [field]: sf }
  bs[binding] = b
  e.bindings = bs
  exts[alias] = e
  return next
}

// removeBindingField drops one field; empty binding/alias maps are
// pruned only when they carry no other properties.
export function removeBindingField(env: Env, alias: string, binding: string, field: string): Env {
  const next: Env = { ...env, extensions: { ...(env.extensions ?? {}) } }
  const exts = next.extensions as Exts
  const e = { ...(exts[alias] ?? {}) }
  const bs = { ...(e.bindings ?? {}) }
  const b = { ...(bs[binding] ?? {}) }
  const secrets = { ...(b.secrets ?? {}) }
  delete secrets[field]
  if (Object.keys(secrets).length) b.secrets = secrets as FieldMap
  else delete b.secrets
  if (Object.keys(b).length === 0) delete bs[binding]
  else bs[binding] = b
  if (Object.keys(bs).length === 0) delete e.bindings
  else e.bindings = bs
  if (Object.keys(e).length === 0) delete exts[alias]
  else exts[alias] = e
  if (Object.keys(exts).length === 0) delete next.extensions
  return next
}
