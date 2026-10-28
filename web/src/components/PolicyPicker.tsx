import { useMemo, useState, type CSSProperties } from 'react'
import { Link } from 'react-router-dom'
import type { Policy } from '../api'
import { paginateExtensions } from '../builderCatalog'
import { Button, inputCls } from './ui'

const PAGE_SIZE = 12

const matches = (p: Policy, q: string) =>
  p.name.toLowerCase().includes(q) || (p.description ?? '').toLowerCase().includes(q)

// PolicyPicker mirrors ExtensionPicker: a fixed-geometry library where
// toggling a card only flips its Added state in place (S1/S5). Policies
// are pluggable constraints — they never grant access.
export function PolicyPicker({ entries, selected, loading, error, onRetry, onToggle, onRemove, showUseDefaults, onUseDefaults }: {
  entries: Policy[]
  selected: readonly string[]
  loading: boolean
  error: boolean
  onRetry: () => void
  onToggle: (name: string) => void
  onRemove: (name: string) => void
  showUseDefaults?: boolean
  onUseDefaults?: () => void
}) {
  const [query, setQuery] = useState('')
  const [page, setPage] = useState(0)
  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase()
    return q ? entries.filter((p) => matches(p, q)) : entries
  }, [entries, query])
  const result = useMemo(() => paginateExtensions(filtered, page, PAGE_SIZE), [filtered, page])
  const search = (v: string) => { setQuery(v); setPage(0) }
  const paged = filtered.length > PAGE_SIZE
  const rows = { '--ext-rows': Math.max(1, Math.ceil(Math.min(PAGE_SIZE, entries.length) / 2)) } as CSSProperties
  const unknown = selected.filter((name) => !entries.some((p) => p.name === name))

  return (
    <div className="ext-library">
      <div className="ext-toolbar">
        <input className={inputCls + ' ext-search'} type="search" aria-label="Search policies"
          placeholder="Search policies" value={query} onChange={(e) => search(e.target.value)} />
        <span className="ext-count" aria-live="polite">{result.total === 0 ? 'No results' : `${result.start}–${result.end} of ${result.total.toLocaleString('en-US')}`}</span>
        {showUseDefaults && <button type="button" className="text-btn underline" onClick={onUseDefaults}>Use default policies</button>}
        {paged && (
          <div className="ext-pager">
            <Button aria-label="Previous policies page" disabled={result.page === 0} onClick={() => setPage(result.page - 1)}>Prev</Button>
            <Button aria-label="Next policies page" disabled={result.page >= result.pages - 1} onClick={() => setPage(result.page + 1)}>Next</Button>
          </div>
        )}
      </div>
      <div className="ext-results" style={rows}>
        {loading && <p className="ext-library-status">Loading policies…</p>}
        {error && <p className="ext-library-status" role="alert">Could not load policies. <button type="button" className="underline" onClick={onRetry}>Retry policies</button></p>}
        {!loading && !error && result.total === 0 && (
          <p className="ext-library-status">
            {query.trim()
              ? <>No policies match “{query.trim()}”. <button type="button" className="underline" onClick={() => search('')}>Clear search</button></>
              : 'No policies are available in this workspace.'}
          </p>
        )}
        {!error && (result.items.length > 0 || unknown.length > 0) && (
          <ul className="ext-grid">
            {result.items.map((p) => {
              const added = selected.includes(p.name)
              return (
                <li key={p.name} className={'ext-card' + (added ? ' ext-card-added' : '')}>
                  <div className="min-w-0 flex-1">
                    <p className="ext-card-name">{p.name}</p>
                    <p className="ext-card-desc" title={p.description}>{p.description || 'No description'}</p>
                    <p className="ext-card-source">{p.kind === 'builtin' ? 'built-in' : 'workspace policy'}{p.default ? ' · default' : ''}</p>
                  </div>
                  <Button className="ext-card-action" aria-pressed={added} onClick={() => onToggle(p.name)}
                    aria-label={added ? `Remove ${p.name}` : `Add ${p.name}`}>
                    {added ? 'Added' : 'Add'}
                  </Button>
                </li>
              )
            })}
            {unknown.map((name) => (
              <li key={name} className="ext-card ext-card-added">
                <div className="min-w-0 flex-1">
                  <p className="ext-card-name">{name}</p>
                  <p className="ext-card-desc">Not in the current policy catalog.</p>
                  <p className="ext-card-source">unknown</p>
                </div>
                <Button className="ext-card-action" aria-label={`Remove ${name}`} onClick={() => onRemove(name)}>Remove</Button>
              </li>
            ))}
          </ul>
        )}
      </div>
      <p className="mt-3 text-xs text-sec">Write your own Rego policy in <Link to="/workspace/policies" className="underline">Workspace → Policies</Link></p>
    </div>
  )
}
