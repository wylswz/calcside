import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api, saveArtifact, type ArtifactConfig, type FileEntry } from '../api'
import { Button, Icon, Loading, Notice, fmtBytes } from './ui'

export function ArtifactBrowser({ id, running, busy }: { id: string; running: boolean; busy: boolean }) {
  const [dir, setDir] = useState('/work')
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [downloading, setDownloading] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const { data: config } = useQuery({ queryKey: ['artifact-config'], queryFn: () => api.get<ArtifactConfig>('/api/v1/artifacts/config') })
  const { data, error: listingError, isLoading, isFetching, refetch } = useQuery({
    queryKey: ['files', id, dir],
    queryFn: () => api.get<{ entries: FileEntry[] }>(`/api/v1/instances/${encodeURIComponent(id)}/files?list_only=true&path=${encodeURIComponent(dir)}`),
    enabled: running && !busy,
    retry: false,
  })
  const disabled = !running || busy || downloading
  const entries = data?.entries ?? []
  const parts = dir.split('/').filter(Boolean)
  const navigate = (path: string) => { setDir(path); setError(''); setNotice('') }
  const toggle = (path: string) => setSelected((previous) => {
    const next = new Set(previous)
    if (next.has(path)) next.delete(path)
    else if (next.size < (config?.max_files ?? 100)) next.add(path)
    return next
  })
  const download = async (paths: string[], format: 'file' | 'zip') => {
    setDownloading(true); setError(''); setNotice('')
    try {
      const result = await api.exportFiles(id, paths, format)
      saveArtifact(result)
      setNotice(`Download sent to your browser${result.redacted ? ' · secrets redacted' : ''}.`)
    } catch (e) { setError((e as Error).message) }
    finally { setDownloading(false) }
  }

  return <div className="space-y-3">
    <div className="flex items-center gap-2">
      <nav aria-label="File directory" className="flex min-w-0 flex-1 items-center gap-1 overflow-x-auto font-mono text-xs">
        {parts.map((part, index) => <span key={index} className="inline-flex shrink-0 items-center gap-1">
          {index > 0 && <Icon name="chevron" className="!h-3 !w-3 text-mute" />}
          <button className={index === parts.length - 1 ? 'text-ink' : 'text-sec hover:text-accent'} aria-current={index === parts.length - 1 ? 'location' : undefined} disabled={disabled} onClick={() => navigate('/' + parts.slice(0, index + 1).join('/'))}>{index === 0 ? '/' : ''}{part}</button>
        </span>)}
      </nav>
      <button className="artifact-action" title="Refresh files" aria-label="Refresh files" disabled={disabled || isFetching} onClick={() => void refetch()}><Icon name="refresh" className={isFetching ? 'motion-safe:animate-spin' : ''} /></button>
    </div>
    {!running && <Notice>Instance is not running. Files are only available while its filesystem is live.</Notice>}
    {listingError && <Notice tone="red">{(listingError as Error).message}</Notice>}
    {error && <div role="alert"><Notice tone="red">{error}</Notice></div>}
    <ul aria-label="Artifact files" className="max-h-96 overflow-y-auto border-y border-line">
      {entries.map((entry) => {
        const href = `/instances/${encodeURIComponent(id)}/preview?path=${encodeURIComponent(entry.path)}`
        const kind = entry.is_dir ? 'Folder' : entry.name.includes('.') ? entry.name.split('.').pop()!.toUpperCase() : 'Text'
        return <li key={entry.path} className={`group flex items-center gap-2 border-b border-line px-1 py-2 last:border-b-0 hover:bg-surface ${selected.has(entry.path) ? 'bg-accent/5' : ''}`}>
          <label className="inline-flex h-8 w-5 shrink-0 items-center justify-center"><input type="checkbox" aria-label={`Select ${entry.name}`} checked={selected.has(entry.path)} className="h-3.5 w-3.5 shrink-0" disabled={disabled || !selected.has(entry.path) && selected.size >= (config?.max_files ?? 100)} onChange={() => toggle(entry.path)} /></label>
          <Icon name={entry.is_dir ? 'folder' : 'file'} className={entry.is_dir ? 'text-accent' : 'text-mute'} />
          <div className="min-w-0 flex-1">
            {entry.is_dir ? <button className="block w-full truncate text-left font-mono text-xs text-ink hover:text-accent" title={entry.name} disabled={disabled} onClick={() => navigate(entry.path)}>{entry.name}/</button> :
              <Link to={href} target="_blank" rel="noopener noreferrer" title={`${entry.name} — open in a new tab`} aria-label={`Open ${entry.name} (new tab)`} aria-disabled={disabled} tabIndex={disabled ? -1 : undefined} onClick={(event) => { if (disabled) event.preventDefault() }} className="block truncate font-mono text-xs text-ink hover:text-accent">{entry.name}</Link>}
            <div className="mt-1 flex items-center gap-2 text-[10px] text-mute"><span className="truncate uppercase tracking-label">{kind}</span>{!entry.is_dir && <span className="shrink-0 font-mono">{fmtBytes(entry.size)}</span>}</div>
          </div>
          {entry.is_dir ? <button className="artifact-action" aria-label={`Open folder ${entry.name}`} title="Open folder" disabled={disabled} onClick={() => navigate(entry.path)}><Icon name="chevron" /></button> : <>
            <Link to={href} target="_blank" rel="noopener noreferrer" className="artifact-action !text-accent" aria-label={`Preview ${entry.name} (new tab)`} title="Preview in a new tab" aria-disabled={disabled} tabIndex={disabled ? -1 : undefined} onClick={(event) => { if (disabled) event.preventDefault() }}><Icon name="preview" /></Link>
            <button className="artifact-action" aria-label={`Download ${entry.name}`} title="Download file" disabled={disabled} onClick={() => void download([entry.path], 'file')}><Icon name="download" /></button>
          </>}
        </li>
      })}
      {isLoading && <li className="py-6"><Loading label="Loading files…" /></li>}
      {!isLoading && data && entries.length === 0 && <li className="py-7 text-center"><Icon name="file" className="mx-auto mb-3 !h-6 !w-6 text-mute" /><p className="text-xs text-sec">No files in this folder</p><p className="mt-1 text-[11px] text-mute">Create an output with <code>fs.write(...)</code>.</p></li>}
    </ul>
    <div className="flex items-center justify-between gap-2">
      <span className="text-[11px] text-sec">{selected.size ? `${selected.size} selected` : `${entries.length} item${entries.length === 1 ? '' : 's'}`}</span>
      <div className="flex items-center gap-2">
        {selected.size > 0 && <button className="text-btn" disabled={disabled} onClick={() => setSelected(new Set())}>Clear</button>}
        <Button size="sm" disabled={disabled || isLoading || !entries.length && !selected.size} aria-label={selected.size ? `Download selected ZIP (${selected.size})` : 'Download folder ZIP'} onClick={() => void download(selected.size ? [...selected] : [dir], 'zip')}><Icon name="download" className="!h-3.5 !w-3.5" />{downloading ? 'Preparing…' : 'Export ZIP'}</Button>
      </div>
    </div>
    {notice && <p role="status" className="text-xs text-sec">{notice}</p>}
    {config && <details className="text-[11px] text-mute"><summary className="w-fit cursor-pointer hover:text-sec">Export limits</summary><p className="mt-2 leading-relaxed">Text files only. {fmtBytes(config.max_file_bytes)} per file; {fmtBytes(config.max_total_bytes)} / {config.max_files} files per export. ZIP export fails as a whole if any file is denied or unsupported.</p></details>}
  </div>
}
