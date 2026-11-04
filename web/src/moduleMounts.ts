// moduleMounts is the pure layer between the spec editor and the
// spec.modules mount map {alias: {name, version, sum, …}}. It mirrors
// internal/module: mount aliases use the @-load grammar (no dots),
// a spec carries at most MaxMounts mounts, and 'self' is reserved for
// the debug mount the server injects.
import type { JsonValue } from './instanceDraft'

export const MaxModuleMounts = 32

// internal/module MountRe — deliberately narrower than the module-name
// grammar (names may contain '.', mounts may not).
const mountRe = /^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/
const RESERVED_MOUNTS = new Set(['self'])

export function validMountAlias(alias: string): boolean {
  return mountRe.test(alias) && !RESERVED_MOUNTS.has(alias)
}

export interface ModuleMount {
  alias: string
  ref: Record<string, JsonValue>
}

// parseModuleMounts reads the raw spec.modules value. It returns the
// mounts in declaration order — never upgrading, re-pinning or dropping
// unknown fields — and reports ok=false for anything that is not a map
// of objects so callers can defer to raw JSON editing (C2/F3).
export function parseModuleMounts(value: JsonValue | undefined): { ok: true; mounts: ModuleMount[] } | { ok: false } {
  if (value === undefined || value === null) return { ok: true, mounts: [] }
  if (typeof value !== 'object' || Array.isArray(value)) return { ok: false }
  const mounts: ModuleMount[] = []
  for (const [alias, ref] of Object.entries(value)) {
    if (!ref || typeof ref !== 'object' || Array.isArray(ref)) return { ok: false }
    mounts.push({ alias, ref: ref as Record<string, JsonValue> })
  }
  return { ok: true, mounts }
}

// mountName resolves a reference's module name; an empty name defaults
// to the mount alias, matching the server-side resolver.
export function mountName(mount: ModuleMount): string {
  const n = mount.ref.name
  return typeof n === 'string' && n ? n : mount.alias
}

// canAddModule decides whether a catalog entry may be mounted right
// now: the raw map is well-formed, under MaxModuleMounts, the resolved
// name isn't mounted already, and the entry is published — a positive
// integer version plus a valid h1: sum.
export interface ModuleCandidate { name: string; latest_version: number; latest_sum?: string }
const h1Re = /^h1:[A-Za-z0-9+/]{43}=$/
export function canAddModule(parsed: { ok: boolean; mounts?: ModuleMount[] }, entry: ModuleCandidate): boolean {
  if (!parsed.ok) return false
  const mounts = parsed.mounts ?? []
  if (mounts.length >= MaxModuleMounts) return false
  if (mounts.some((m) => mountName(m) === entry.name)) return false
  return Number.isSafeInteger(entry.latest_version) && entry.latest_version > 0 &&
    typeof entry.latest_sum === 'string' && h1Re.test(entry.latest_sum)
}

// uniqueMountAlias derives a collision-free mount alias from a module
// name: fit the mount grammar, never 'self', never an existing alias.
export function uniqueMountAlias(name: string, existing: ReadonlySet<string>): string {
  let base = name.replace(/[^A-Za-z0-9_-]/g, '_').replace(/^[-_]+|[-_]+$/g, '')
  if (!base) base = 'mod'
  if (!/^[A-Za-z0-9]/.test(base)) base = 'm' + base
  if (base.length > 64) base = base.slice(0, 64)
  if (RESERVED_MOUNTS.has(base)) base = base + '_mod'
  let candidate = base
  for (let i = 2; existing.has(candidate) || RESERVED_MOUNTS.has(candidate); i++) {
    const suffix = `_${i}`
    candidate = base.slice(0, 64 - suffix.length) + suffix
  }
  return candidate
}

// addMount returns a new modules map with the entry appended; the
// caller guarantees the alias is unused and the count is under
// MaxModuleMounts. Returns the input when the alias already exists.
export function addMount(value: JsonValue | undefined, alias: string, ref: Record<string, JsonValue>): JsonValue {
  const map: Record<string, JsonValue> =
    value && typeof value === 'object' && !Array.isArray(value) ? { ...value } : {}
  if (Object.hasOwn(map, alias)) return value ?? {}
  map[alias] = ref
  return map
}

// removeMount returns the map without alias; undefined when the result
// is empty so spec.modules can be dropped entirely.
export function removeMount(value: JsonValue | undefined, alias: string): JsonValue | undefined {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return value
  const map = { ...value }
  delete map[alias]
  return Object.keys(map).length ? map : undefined
}
