import { useMemo, useState, type CSSProperties } from 'react'
import { paginateExtensions } from '../builderCatalog'
import { Button, inputCls } from './ui'

const PAGE_SIZE = 12

export interface ModuleEntry {
  id: string
  name: string
  description?: string
  latest_version: number
  latest_sum?: string
}

// ModulePicker is the always-visible workspace module library for
// application specs: same fixed geometry as ExtensionPicker — client
// search, fixed paging, a reserved result area (S1/S3). Modules without
// a published version are visible but cannot be mounted.
export function ModulePicker({ entries, selectedNames, loading, error, onRetry, onAdd, onShow, canAdd }: {
  entries: ModuleEntry[]
  selectedNames: ReadonlySet<string>
  loading: boolean
  error: boolean
  onRetry: () => void
  onAdd: (entry: ModuleEntry) => void
  onShow: (entry: ModuleEntry) => void
  /** per-entry add eligibility (mount bound, pin shape); default requires a published version+sum */
  canAdd?: (entry: ModuleEntry) => boolean
}) {
  const [query, setQuery] = useState('')
  const [page, setPage] = useState(0)
  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase()
    if (!q) return entries
    return entries.filter((e) =>
      e.name.toLowerCase().includes(q) || (e.description ?? '').toLowerCase().includes(q))
  }, [entries, query])
  const result = useMemo(() => paginateExtensions(filtered, page, PAGE_SIZE), [filtered, page])
  const search = (v: string) => { setQuery(v); setPage(0) }
  const paged = entries.length > PAGE_SIZE
  const rows = { '--ext-rows': Math.max(1, Math.ceil(Math.min(PAGE_SIZE, entries.length) / 2)) } as CSSProperties

  return (
    <div className="ext-library">
      <div className="ext-toolbar">
        <input className={inputCls + ' ext-search'} type="search" aria-label="Search modules"
          placeholder="Search modules" value={query} onChange={(e) => search(e.target.value)} />
        <span className="ext-count" aria-live="polite">{result.total === 0 ? 'No results' : `${result.start}–${result.end} of ${result.total.toLocaleString('en-US')}`}</span>
        {paged && (
          <div className="ext-pager">
            <Button aria-label="Previous modules page" disabled={result.page === 0} onClick={() => setPage(result.page - 1)}>Prev</Button>
            <Button aria-label="Next modules page" disabled={result.page >= result.pages - 1} onClick={() => setPage(result.page + 1)}>Next</Button>
          </div>
        )}
      </div>
      <div className="ext-results" style={rows}>
        {loading && <p className="ext-library-status">Loading modules…</p>}
        {error && <p className="ext-library-status" role="alert">Could not load modules. <button type="button" className="underline" onClick={onRetry}>Retry</button></p>}
        {!loading && !error && result.total === 0 && (
          <p className="ext-library-status">
            {query.trim()
              ? <>No modules match “{query.trim()}”. <button type="button" className="underline" onClick={() => search('')}>Clear search</button></>
              : 'No published modules in this workspace yet.'}
          </p>
        )}
        {!error && result.items.length > 0 && (
          <ul className="ext-grid">
            {result.items.map((entry) => {
              const added = selectedNames.has(entry.name)
              const addable = canAdd ? canAdd(entry) : entry.latest_version > 0 && !!entry.latest_sum
              return (
                <li key={entry.id} className={'ext-card' + (added ? ' ext-card-added' : '')}>
                  <div className="min-w-0 flex-1">
                    <p className="ext-card-name">{entry.name}</p>
                    <p className="ext-card-desc" title={entry.description}>{entry.description || entry.name}</p>
                    <p className="ext-card-source">{entry.latest_version > 0 && entry.latest_sum ? `v${entry.latest_version} · ${entry.latest_sum.slice(0, 16)}…` : 'unpublished'}</p>
                  </div>
                  <Button className="ext-card-action" disabled={!addable && !added}
                    onClick={() => added ? onShow(entry) : onAdd(entry)}
                    aria-label={added ? `Added ${entry.name}, show settings` : `Add module ${entry.name}`}>
                    {added ? 'Added' : 'Add'}
                  </Button>
                </li>
              )
            })}
          </ul>
        )}
      </div>
    </div>
  )
}
