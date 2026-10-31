import { useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api, saveArtifact, ApiError, type ArtifactConfig, type FileEntry } from '../api'
import { Button, Confirm, Icon, Loading, Notice, SwapLabel } from './ui'
import { fmtBytes } from '../format'

const MAX_UPLOAD_BYTES = 1 << 20

type UploadState = 'queued' | 'uploading' | 'finalizing' | 'done' | 'conflict' | 'error'

interface UploadTask {
  key: number
  file: File
  path: string
  state: UploadState
  loaded: number
  message: string
}

function taskLabel(t: UploadTask): string {
  switch (t.state) {
    case 'queued': return 'Queued'
    case 'uploading': return t.file.size > 0 && t.loaded > 0 ? `Uploading ${Math.round((t.loaded / t.file.size) * 100)}%` : 'Uploading…'
    case 'finalizing': return 'Finalizing…'
    case 'done': return 'Uploaded'
    case 'conflict': return 'Already exists'
    case 'error': return 'Failed'
  }
}

export function ArtifactBrowser({ id, running, busy }: { id: string; running: boolean; busy: boolean }) {
  const [dir, setDir] = useState('/work')
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [downloading, setDownloading] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [tasks, setTasks] = useState<UploadTask[]>([])
  const tasksRef = useRef<UploadTask[]>([])
  const [dragging, setDragging] = useState(false)
  const [replacing, setReplacing] = useState<UploadTask | null>(null)
  const uploading = useRef(false)
  const fileInput = useRef<HTMLInputElement>(null)
  const seq = useRef(0)
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

  // tasksRef mirrors the queue synchronously — the pump drains it
  // across awaits, where functional setState reads would go stale.
  const applyTasks = (updater: (previous: UploadTask[]) => UploadTask[]) => {
    tasksRef.current = updater(tasksRef.current)
    setTasks(tasksRef.current)
  }
  const patchTask = (key: number, patch: Partial<UploadTask>) =>
    applyTasks((previous) => previous.map((t) => (t.key === key ? { ...t, ...patch } : t)))

  const runTask = async (task: UploadTask, overwrite: boolean) => {
    patchTask(task.key, { state: 'uploading', loaded: 0, message: '' })
    try {
      await api.uploadFile(id, task.path, task.file, {
        overwrite,
        onProgress: (loaded) => patchTask(task.key, { loaded, state: loaded >= task.file.size ? 'finalizing' : 'uploading' }),
      })
      patchTask(task.key, { state: 'done', loaded: task.file.size })
      void refetch()
    } catch (e) {
      const err = e as Error
      if (err instanceof ApiError && err.status === 409 && err.code === 'conflict') {
        patchTask(task.key, { state: 'conflict', message: err.message })
      } else {
        patchTask(task.key, { state: 'error', message: err.message })
      }
    }
  }

  // Files upload one at a time per instance — the queue drains
  // serially so concurrent picks never interleave writes.
  const pump = async () => {
    if (uploading.current) return
    uploading.current = true
    try {
      for (;;) {
        const next = tasksRef.current.find((t) => t.state === 'queued')
        if (!next) break
        await runTask(next, false)
      }
    } finally { uploading.current = false }
  }

  const enqueue = (files: Iterable<File>) => {
    setError(''); setNotice('')
    const added: UploadTask[] = []
    for (const file of files) {
      const task: UploadTask = {
        key: ++seq.current, file,
        path: `${dir.replace(/\/$/, '')}/${file.name}`,
        state: 'queued', loaded: 0, message: '',
      }
      if (file.size > MAX_UPLOAD_BYTES) {
        task.state = 'error'
        task.message = `File exceeds the ${fmtBytes(MAX_UPLOAD_BYTES)} upload limit.`
      }
      added.push(task)
    }
    if (!added.length) return
    applyTasks((previous) => [...previous, ...added])
    void pump()
  }

  const finished = tasks.some((t) => t.state === 'done' || t.state === 'error' || t.state === 'conflict')

  return <div className="artifact-browser space-y-3">
    <div className="flex items-center gap-2">
      <nav aria-label="File directory" className="flex min-w-0 flex-1 items-center gap-1 overflow-x-auto font-mono text-xs">
        {parts.map((part, index) => <span key={index} className="inline-flex shrink-0 items-center gap-1">
          {index > 0 && <Icon name="chevron" className="!h-3 !w-3 text-mute" />}
          <button className={index === parts.length - 1 ? 'text-ink' : 'text-sec hover:text-accent'} aria-current={index === parts.length - 1 ? 'location' : undefined} disabled={disabled} onClick={() => navigate('/' + parts.slice(0, index + 1).join('/'))}>{index === 0 ? '/' : ''}{part}</button>
        </span>)}
      </nav>
      <input ref={fileInput} type="file" multiple className="hidden" aria-hidden="true" tabIndex={-1} onChange={(e) => { enqueue(e.target.files ?? []); e.target.value = '' }} />
      <button className="artifact-action" title="Upload files" aria-label="Upload files" disabled={disabled} onClick={() => fileInput.current?.click()}><Icon name="upload" /></button>
      <button className="artifact-action" title="Refresh files" aria-label="Refresh files" disabled={disabled || isFetching} onClick={() => void refetch()}><Icon name="refresh" className={isFetching ? 'motion-safe:animate-spin' : ''} /></button>
    </div>
    {!running && <Notice>Instance is not running. Files are only available while its filesystem is live.</Notice>}
    {listingError && <Notice tone="warning">{(listingError as Error).message}</Notice>}
    {error && <div role="alert"><Notice tone="warning">{error}</Notice></div>}
    {tasks.length > 0 && <ul aria-label="Uploads" className="divide-y divide-line border-y border-line">
      {tasks.map((t) => <li key={t.key} className="flex items-center gap-2 px-1 py-2">
        <Icon name="file" className={t.state === 'error' || t.state === 'conflict' ? 'text-warn-text' : 'text-mute'} />
        <div className="min-w-0 flex-1">
          <span className="block truncate font-mono text-xs text-ink" title={t.path}>{t.path}</span>
          <span className="text-xs text-mute">{fmtBytes(t.file.size)} · {taskLabel(t)}</span>
          {t.message && <span className="block truncate text-xs text-warn-text" title={t.message}>{t.message}</span>}
        </div>
        {(t.state === 'uploading' || t.state === 'finalizing') &&
          <progress aria-label={`Upload progress for ${t.file.name}`} max={t.file.size} value={t.state === 'finalizing' ? t.file.size : t.loaded} className="h-1.5 w-20 accent-current text-accent" />}
        {t.state === 'conflict' &&
          <Button size="sm" disabled={disabled} onClick={() => setReplacing(t)}>Replace…</Button>}
        {t.state === 'error' &&
          <Button size="sm" disabled={disabled} onClick={() => { patchTask(t.key, { state: 'queued', message: '' }); void pump() }}>Retry</Button>}
      </li>)}
    </ul>}
    <ul aria-label="Artifact files"
      onDragOver={(e) => { if (!disabled && e.dataTransfer.types.includes('Files')) { e.preventDefault(); setDragging(true) } }}
      onDragLeave={() => setDragging(false)}
      onDrop={(e) => { setDragging(false); if (disabled) return; e.preventDefault(); enqueue(e.dataTransfer.files) }}
      className={`max-h-96 overflow-y-auto border-y border-line ${dragging ? 'bg-accent/5' : ''}`}>
      {entries.map((entry) => {
        const href = `/instances/${encodeURIComponent(id)}/preview?path=${encodeURIComponent(entry.path)}`
        const kind = entry.is_dir ? 'Folder' : entry.name.includes('.') ? entry.name.split('.').pop()!.toUpperCase() : 'Text'
        return <li key={entry.path} className={`group flex items-center gap-2 border-b border-line px-1 py-2 last:border-b-0 hover:bg-surface ${selected.has(entry.path) ? 'bg-accent/5' : ''}`}>
          <label className="inline-flex h-8 w-5 shrink-0 items-center justify-center"><input type="checkbox" aria-label={`Select ${entry.name}`} checked={selected.has(entry.path)} className="h-3.5 w-3.5 shrink-0" disabled={disabled || !selected.has(entry.path) && selected.size >= (config?.max_files ?? 100)} onChange={() => toggle(entry.path)} /></label>
          <Icon name={entry.is_dir ? 'folder' : 'file'} className={entry.is_dir ? 'text-accent' : 'text-mute'} />
          <div className="min-w-0 flex-1">
            {entry.is_dir ? <button className="block w-full truncate text-left font-mono text-xs text-ink hover:text-accent" title={entry.name} disabled={disabled} onClick={() => navigate(entry.path)}>{entry.name}/</button> :
              <Link to={href} target="_blank" rel="noopener noreferrer" title={`${entry.name} — open in a new tab`} aria-label={`Open ${entry.name} (new tab)`} aria-disabled={disabled} tabIndex={disabled ? -1 : undefined} onClick={(event) => { if (disabled) event.preventDefault() }} className="block truncate artifact-name text-xs text-ink hover:text-accent">{entry.name}</Link>}
            <div className="mt-1 flex items-center gap-2 text-xs text-mute"><span className="truncate">{kind}</span>{!entry.is_dir && <span className="shrink-0 font-mono">{fmtBytes(entry.size)}</span>}</div>
          </div>
          {entry.is_dir ? <button className="artifact-action" aria-label={`Open folder ${entry.name}`} title="Open folder" disabled={disabled} onClick={() => navigate(entry.path)}><Icon name="chevron" /></button> : <>
            <Link to={href} target="_blank" rel="noopener noreferrer" className="artifact-action !text-accent" aria-label={`Preview ${entry.name} (new tab)`} title="Preview in a new tab" aria-disabled={disabled} tabIndex={disabled ? -1 : undefined} onClick={(event) => { if (disabled) event.preventDefault() }}><Icon name="preview" /></Link>
            <button className="artifact-action" aria-label={`Download ${entry.name}`} title="Download file" disabled={disabled} onClick={() => void download([entry.path], 'file')}><Icon name="download" /></button>
          </>}
        </li>
      })}
      {isLoading && <li className="py-6"><Loading label="Loading files…" /></li>}
      {!isLoading && data && entries.length === 0 && <li className="artifact-empty"><Icon name="file" className="artifact-empty-icon" /><p className="text-sm text-sec">No files yet.</p><p className="mt-1 text-xs text-mute">Drop files here to upload them.</p></li>}
    </ul>
    <div className="flex items-center justify-between gap-2">
      <span className="text-xs text-sec">{selected.size ? `${selected.size} selected` : `${entries.length} item${entries.length === 1 ? '' : 's'}`}</span>
      <div className="flex items-center gap-2">
        {finished && <button className="text-btn" onClick={() => applyTasks((previous) => previous.filter((t) => t.state === 'queued' || t.state === 'uploading' || t.state === 'finalizing'))}>Clear uploads</button>}
        {selected.size > 0 && <button className="text-btn" disabled={disabled} onClick={() => setSelected(new Set())}>Clear</button>}
        <Button size="sm" disabled={disabled || isLoading || !entries.length && !selected.size} aria-label={selected.size ? `Download selected ZIP (${selected.size})` : 'Download folder ZIP'} onClick={() => void download(selected.size ? [...selected] : [dir], 'zip')}><Icon name="download" className="!h-3.5 !w-3.5" /><SwapLabel show={downloading ? 'Preparing…' : 'Export ZIP'} options={['Export ZIP', 'Preparing…']} /></Button>
      </div>
    </div>
    {notice && <p role="status" className="text-xs text-sec">{notice}</p>}
    {config && <details className="text-xs text-sec"><summary className="w-fit cursor-pointer hover:text-ink">Export limits</summary><p className="mt-2 leading-relaxed">Text files only. {fmtBytes(config.max_file_bytes)} per file; {fmtBytes(config.max_total_bytes)} / {config.max_files} files per export. ZIP export fails as a whole if any file is denied or unsupported.</p></details>}
    {replacing && <Confirm
      title="Replace existing file?"
      confirm="Replace"
      pending="Replacing…"
      action={async () => { await runTask(replacing, true) }}
      onClose={() => setReplacing(null)}
    >
      <p className="text-sm text-sec"><code className="font-mono">{replacing.path}</code> already exists. Uploading will replace its contents; this cannot be undone.</p>
    </Confirm>}
  </div>
}
