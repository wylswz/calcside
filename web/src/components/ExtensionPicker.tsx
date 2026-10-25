import { useMemo, useState, type CSSProperties } from 'react'
import type { ExtensionInfo } from '../api'
import { filterExtensions, paginateExtensions } from '../builderCatalog'
import { ExtensionIcon } from './ExtensionIcon'
import { Button, inputCls } from './ui'

const PAGE_SIZE = 12

// ExtensionPicker is the always-visible extension library: client-side
// search + fixed paging so a 1000-entry catalog stays a 12-card surface.
// Geometry is fixed: cards keep their size when added, the pager is
// always present for multi-page catalogs, and the result area reserves a
// full page so filtering does not move what follows.
export function ExtensionPicker({ entries, selectedSources, loading, error, onRetry, onAdd, onShow, onCustom }: {
  entries: ExtensionInfo[]
  selectedSources: ReadonlySet<string>
  loading: boolean
  error: boolean
  onRetry: () => void
  onAdd: (entry: ExtensionInfo) => void
  onShow: (entry: ExtensionInfo) => void
  onCustom: () => void
}) {
  const [query, setQuery] = useState('')
  const [page, setPage] = useState(0)
  const result = useMemo(() => paginateExtensions(filterExtensions(entries, query), page, PAGE_SIZE), [entries, query, page])
  const search = (v: string) => { setQuery(v); setPage(0) }
  const paged = entries.length > PAGE_SIZE
  const rows = { '--ext-rows': Math.max(1, Math.ceil(Math.min(PAGE_SIZE, entries.length) / 2)) } as CSSProperties

  return (
    <div className="ext-library">
      <div className="ext-toolbar">
        <input className={inputCls + ' ext-search'} type="search" aria-label="Search extensions"
          placeholder="Search extensions" value={query} onChange={(e) => search(e.target.value)} />
        <span className="ext-count" aria-live="polite">{result.total === 0 ? 'No results' : `${result.start}\u2013${result.end} of ${result.total.toLocaleString('en-US')}`}</span>
        {paged && (
          <div className="ext-pager">
            <Button aria-label="Previous extensions page" disabled={result.page === 0} onClick={() => setPage(result.page - 1)}>Prev</Button>
            <Button aria-label="Next extensions page" disabled={result.page >= result.pages - 1} onClick={() => setPage(result.page + 1)}>Next</Button>
          </div>
        )}
      </div>
      <div className="ext-results" style={rows}>
        {loading && <p className="ext-library-status">Loading extensions…</p>}
        {error && <p className="ext-library-status" role="alert">Could not load extensions. <button type="button" className="underline" onClick={onRetry}>Retry catalog</button></p>}
        {!loading && !error && result.total === 0 && (
          <p className="ext-library-status">
            {query.trim()
              ? <>No extensions match “{query.trim()}”. <button type="button" className="underline" onClick={() => search('')}>Clear search</button></>
              : 'No extensions are available on this server.'}
          </p>
        )}
        {!error && result.items.length > 0 && (
          <ul className="ext-grid">
            {result.items.map((entry) => {
              const added = selectedSources.has(entry.source)
              return (
                <li key={entry.source} className={'ext-card' + (added ? ' ext-card-added' : '')}>
                  <ExtensionIcon iconUrl={entry.icon_url} name={entry.name} />
                  <div className="min-w-0 flex-1">
                    <p className="ext-card-name">{entry.name}</p>
                    <p className="ext-card-desc" title={entry.description}>{entry.description || entry.source}</p>
                    <p className="ext-card-source" title={entry.source}>{entry.source}</p>
                  </div>
                  <Button className="ext-card-action" onClick={() => added ? onShow(entry) : onAdd(entry)}
                    aria-label={added ? `Added ${entry.name}, show settings` : `Add ${entry.name}`}>
                    {added ? 'Added' : 'Add'}
                  </Button>
                </li>
              )
            })}
          </ul>
        )}
      </div>
      <p className="mt-3 text-xs text-sec">Extension not listed? <button type="button" className="underline" onClick={onCustom}>Configure a custom extension</button></p>
    </div>
  )
}
