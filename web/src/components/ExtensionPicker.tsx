import { useEffect, useMemo, useRef, useState, type CSSProperties } from 'react'
import type { ExtensionInfo } from '../api'
import { addedVersion, filterFamilies, groupExtensions, paginateExtensions } from '../builderCatalog.ts'
import { ExtensionIcon } from './ExtensionIcon'
import { Button, Menu, MenuItem, SwapLabel, inputCls } from './ui'

const PAGE_SIZE = 12

// ExtensionPicker is the always-visible extension library: client-side
// search + fixed paging so a 1000-entry catalog stays a 12-card surface.
// One card per extension family with a version dropdown; the displayed
// version defaults to the latest as a recommendation — the user's
// explicit choice is never overwritten by refetches. Geometry is fixed:
// cards keep their size across versions and states (S1).
export function ExtensionPicker({ entries, selectedSources, loading, error, onRetry, onAdd, onShow, onSwitch }: {
  entries: ExtensionInfo[]
  selectedSources: ReadonlySet<string>
  loading: boolean
  error: boolean
  onRetry: () => void
  onAdd: (entry: ExtensionInfo) => void
  onShow: (entry: ExtensionInfo) => void
  // Only one version per family is added through the picker; choosing a
  // different version switches the existing entry in place.
  onSwitch: (from: ExtensionInfo | undefined, to: ExtensionInfo) => void
}) {
  const [query, setQuery] = useState('')
  const [page, setPage] = useState(0)
  // Switch feedback: the main segment briefly shows the switched-to
  // version (~1.4s) plus a chip flash — in-place only, geometry never
  // changes (S1/S4). The aria-live line announces it to screen readers.
  const [flash, setFlash] = useState<{ family: string; version: string; name: string } | null>(null)
  const flashTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  useEffect(() => () => clearTimeout(flashTimer.current), [])
  const noteSwitch = (family: string, name: string, version: string) => {
    clearTimeout(flashTimer.current)
    setFlash({ family, name, version })
    flashTimer.current = setTimeout(() => setFlash(null), 1400)
  }
  const families = useMemo(() => groupExtensions(entries), [entries])
  const result = useMemo(() => paginateExtensions(filterFamilies(families, query), page, PAGE_SIZE), [families, query, page])
  const search = (v: string) => { setQuery(v); setPage(0) }
  const paged = families.length > PAGE_SIZE
  const rows = { '--ext-rows': Math.max(1, Math.ceil(Math.min(PAGE_SIZE, families.length) / 2)) } as CSSProperties

  return (
    <div className="ext-library">
      <div className="ext-toolbar">
        <input className={inputCls + ' ext-search'} type="search" aria-label="Search extensions"
          placeholder="Search extensions" value={query} onChange={(e) => search(e.target.value)} />
        <span className="ext-count" aria-live="polite">{result.total === 0 ? 'No results' : `${result.start}–${result.end} of ${result.total.toLocaleString('en-US')}`}</span>
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
        <span className="ext-live" aria-live="polite">{flash ? `Switched ${flash.name} to ${flash.version}` : ''}</span>
        {!error && result.items.length > 0 && (
          <ul className="ext-grid">
            {result.items.map((fam) => {
              const addedEntry = addedVersion(fam, selectedSources)
              const added = addedEntry !== undefined
              const single = fam.versions.length === 1
              const displayed = addedEntry ?? fam.latest
              return (
                <li key={fam.family} className={'ext-card' + (added ? ' ext-card-added' : '')}>
                  <ExtensionIcon iconUrl={displayed.icon_url} name={displayed.name} />
                  <div className="min-w-0 flex-1">
                    <p className="ext-card-name">{displayed.name}</p>
                    <p className="ext-card-desc" title={displayed.description}>{displayed.description || displayed.source}</p>
                    <p className="ext-card-source" title={displayed.source}>
                      <span className="ext-card-family">{fam.family}</span>
                      <span key={displayed.version}
                        className={'ext-card-version' + (flash?.family === fam.family ? ' ext-card-version-flash' : '')}>{displayed.version}</span>
                    </p>
                  </div>
                  <span className="ext-split">
                    <Button className="ext-card-action" data-flash={flash?.family === fam.family ? 'true' : 'false'}
                      onClick={() => added ? onShow(displayed) : onAdd(fam.latest)}
                      aria-label={added ? `Added ${displayed.name}, show settings` : `Add ${displayed.name}`}>
                      <SwapLabel show={added ? 'Added' : 'Add'} options={['Add', 'Added']} />
                      {flash?.family === fam.family && <span className="ext-split-version" aria-hidden="true">{flash.version}</span>}
                    </Button>
                    {!single && (
                      <>
                        <span className="ext-split-sep" aria-hidden="true" />
                        <Menu trigger={
                          <Button className="ext-card-caret" aria-label={`Choose version of ${fam.latest.name}`}>▾</Button>
                        }>
                          {fam.versions.map((v) => {
                            const isAdded = v === addedEntry
                            const act = isAdded ? () => onShow(v)
                              : added ? () => { onSwitch(addedEntry, v); noteSwitch(fam.family, displayed.name, v.version) }
                              : () => onAdd(v)
                            return (
                              <MenuItem key={v.source} onSelect={act}>
                                {isAdded ? '✓ ' : ''}{v.version}{v === fam.latest ? ' · latest' : ''}{isAdded ? ' · added' : ''}
                              </MenuItem>
                            )
                          })}
                        </Menu>
                      </>
                    )}
                  </span>
                </li>
              )
            })}
          </ul>
        )}
      </div>
    </div>
  )
}
