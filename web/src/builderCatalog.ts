import type { ExtensionInfo } from './api'

// Pure search/pagination for the extension library — the API returns whole
// metadata; a thousand entries must never become a thousand DOM cards.

export function filterExtensions(entries: ExtensionInfo[], query: string): ExtensionInfo[] {
  const q = query.trim().toLowerCase()
  if (!q) return entries
  return entries.filter((e) =>
    e.name.toLowerCase().includes(q) ||
    e.source.toLowerCase().includes(q) ||
    (e.description ?? '').toLowerCase().includes(q))
}

export interface Page<T> { items: T[]; total: number; start: number; end: number; page: number; pages: number }

export function paginateExtensions<T>(entries: T[], page: number, pageSize = 12): Page<T> {
  const total = entries.length
  const pages = Math.max(1, Math.ceil(total / pageSize))
  const clamped = Math.min(Math.max(0, page), pages - 1)
  const start = clamped * pageSize
  const items = entries.slice(start, start + pageSize)
  return { items, total, start: items.length ? start + 1 : 0, end: start + items.length, page: clamped, pages }
}

// compareVersions is a natural-order compare used only for console
// recommendation/sorting — the server never resolves "latest".
export function compareVersions(a: string, b: string): number {
  const core = (v: string): [string, string] => {
    const s = v.replace(/^[vV]/, '')
    const i = s.search(/[-+]/)
    return i < 0 ? [s, ''] : [s.slice(0, i), s.slice(i)]
  }
  const [ca, sa] = core(a)
  const [cb, sb] = core(b)
  const pa = ca.split('.')
  const pb = cb.split('.')
  for (let i = 0; i < Math.max(pa.length, pb.length); i++) {
    const x = pa[i]
    const y = pb[i]
    if (x === undefined) return -1
    if (y === undefined) return 1
    const nx = /^\d+$/.test(x) ? Number(x) : NaN
    const ny = /^\d+$/.test(y) ? Number(y) : NaN
    const c = (Number.isNaN(nx) || Number.isNaN(ny)) ? (x < y ? -1 : x > y ? 1 : 0) : nx - ny
    if (c !== 0) return c < 0 ? -1 : 1
  }
  if (sa !== sb) {
    if (sa === '') return 1
    if (sb === '') return -1
    return sa < sb ? -1 : 1
  }
  return a < b ? -1 : a > b ? 1 : 0
}

export interface ExtensionFamily {
  family: string
  name: string
  versions: ExtensionInfo[]
  latest: ExtensionInfo
}

// groupExtensions folds the flat catalog into one family per source
// family, preserving first-appearance order; versions sort newest first.
export function groupExtensions(entries: ExtensionInfo[]): ExtensionFamily[] {
  const out: ExtensionFamily[] = []
  const by = new Map<string, ExtensionFamily>()
  for (const e of entries) {
    let f = by.get(e.family)
    if (!f) {
      f = { family: e.family, name: e.name, versions: [], latest: e }
      by.set(e.family, f)
      out.push(f)
    }
    f.versions.push(e)
  }
  for (const f of out) {
    f.versions.sort((x, y) => compareVersions(y.version, x.version))
    f.latest = f.versions[0]
    f.name = f.latest.name
  }
  return out
}

const extMatches = (e: ExtensionInfo, q: string) =>
  e.name.toLowerCase().includes(q) ||
  e.source.toLowerCase().includes(q) ||
  (e.description ?? '').toLowerCase().includes(q)

// filterFamilies keeps a family when any version (or the family string)
// matches the search predicate.
export function filterFamilies(families: ExtensionFamily[], query: string): ExtensionFamily[] {
  const q = query.trim().toLowerCase()
  if (!q) return families
  return families.filter((f) => f.family.toLowerCase().includes(q) || f.versions.some((e) => extMatches(e, q)))
}

// findExtension resolves a spec/draft source to its catalog entry: exact
// source match, else a legacy unversioned local pin matches only a
// sole-candidate family (never guesses among several versions).
export function findExtension(catalog: ExtensionInfo[] | undefined, source: string): ExtensionInfo | undefined {
  if (!catalog || !source) return undefined
  const exact = catalog.find((e) => e.source === source)
  if (exact) return exact
  if (source.includes('@')) return undefined
  const fam = catalog.filter((e) => e.family === source)
  return fam.length === 1 ? fam[0] : undefined
}

// addedVersion resolves a family's added catalog entry: the newest
// added version, else a sole-version family pinned by bare name
// (legacy). One card shows at most one added version (EP-0020).
export function addedVersion(fam: ExtensionFamily, selected: ReadonlySet<string>): ExtensionInfo | undefined {
  const added = fam.versions.filter((v) => selected.has(v.source))
  if (added.length) return added[0]
  if (fam.versions.length === 1 && selected.has(fam.family)) return fam.versions[0]
  return undefined
}
