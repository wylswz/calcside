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
