export interface PendingDraft<T> { version: number; value: T }

// Backups are tab-local and resource-scoped. Callers only pass source or
// validated, references-only debug specs, never invalid raw secret inputs.
type PendingStorage = Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>

export function readPendingDraft<T>(key: string, valid: (value: unknown) => value is T, storage?: PendingStorage): PendingDraft<T> | null {
  try {
    const text = (storage ?? sessionStorage).getItem(key)
    if (!text) return null
    const row = JSON.parse(text) as PendingDraft<unknown>
    return Number.isSafeInteger(row.version) && row.version >= 0 && valid(row.value)
      ? { version: row.version, value: row.value } : null
  } catch { return null }
}

export function writePendingDraft<T>(key: string, draft: PendingDraft<T> | null, storage?: PendingStorage): void {
  try {
    const target = storage ?? sessionStorage
    if (draft) target.setItem(key, JSON.stringify(draft))
    else target.removeItem(key)
  } catch {
    // Server autosave and the beforeunload guard still work when storage
    // is unavailable or full.
  }
}
