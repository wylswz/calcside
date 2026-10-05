import { useEffect, useRef, useState } from 'react'
import { Link, useParams, useSearchParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api, saveArtifact, type ArtifactConfig, type ArtifactPreview } from '../api'
import { Button, Icon, Loading, Logo, Mark, Notice, fmtBytes, fmtTime } from '../components/ui'

export default function ArtifactPreviewPage({ dev }: { dev: boolean }) {
  const { id = '' } = useParams()
  const [params] = useSearchParams()
  const path = params.get('path') ?? ''
  return <Preview key={`${id}:${path}`} id={id} path={path} dev={dev} />
}

function Preview({ id, path, dev }: { id: string; path: string; dev: boolean }) {
  const [view, setView] = useState<'preview' | 'source'>('preview')
  const [scripts, setScripts] = useState(false)
  const [confirmScripts, setConfirmScripts] = useState(false)
  const confirmation = useRef<HTMLDialogElement>(null)
  const [mobile, setMobile] = useState(false)
  const [firstRowHeader, setFirstRowHeader] = useState(false)
  const [revision, setRevision] = useState(0)
  const [expired, setExpired] = useState(false)
  const [downloading, setDownloading] = useState(false)
  const [downloadError, setDownloadError] = useState('')
  const [notice, setNotice] = useState('')
  const filename = path.split('/').pop() || 'Artifact preview'
  const html = /\.html?$/i.test(path)
  const csv = /\.(csv|tsv)$/i.test(path)
  const config = useQuery({ queryKey: ['artifact-config'], queryFn: () => api.get<ArtifactConfig>('/api/v1/artifacts/config'), retry: false })
  const showPreview = view === 'preview' && (csv || html && config.data?.html_enabled)
  const mode = html && showPreview ? scripts ? 'interactive' : 'static' : 'source'
  const snapshot = useQuery({
    queryKey: ['artifact-preview', id, path, mode, revision],
    queryFn: () => api.post<ArtifactPreview>(`/api/v1/instances/${encodeURIComponent(id)}/artifacts/preview`, { path, mode }),
    enabled: !!id && !!path && config.isSuccess,
    retry: false, staleTime: Infinity, gcTime: 0, refetchOnWindowFocus: false, refetchOnReconnect: false,
  })
  const preview = snapshot.data
  const error = !id || !path ? new Error('Choose a file from the instance to preview it.') : config.error || snapshot.error
  const loading = !error && (config.isPending || snapshot.isPending)
  const preparing = config.isFetching || snapshot.isFetching
  const status = loading ? 'Preparing preview' : error ? 'Unavailable' : expired && showPreview ? 'Expired snapshot' : preview?.preview_error && showPreview ? 'Source fallback' : html && showPreview ? scripts ? 'JavaScript · sandboxed' : 'Static · scripts removed' : csv && showPreview ? `${preview?.csv_total_rows ?? 0} records parsed` : 'Source'
  const back = `/instances/${encodeURIComponent(id)}`
  const refresh = () => { if (config.isError) void config.refetch(); else setRevision((value) => value + 1) }

  useEffect(() => {
    const dialog = confirmation.current
    if (confirmScripts) dialog?.showModal()
    else dialog?.close()
    return () => dialog?.close()
  }, [confirmScripts])
  useEffect(() => {
    const previous = document.title
    document.title = `${filename} · calcside`
    return () => { document.title = previous }
  }, [filename])
  useEffect(() => {
    setExpired(false)
    if (!preview?.expires_at) return
    const timer = window.setTimeout(() => setExpired(true), Math.max(0, new Date(preview.expires_at).getTime() - Date.now()))
    return () => window.clearTimeout(timer)
  }, [preview?.expires_at])

  const download = async () => {
    setDownloading(true); setDownloadError(''); setNotice('')
    try {
      const result = await api.exportFiles(id, [path], 'file')
      saveArtifact(result)
      setNotice(`Download sent to your browser${result.redacted ? ' · secrets redacted' : ''}.`)
    } catch (e) { setDownloadError((e as Error).message) }
    finally { setDownloading(false) }
  }
  let source = preview?.source ?? ''
  if (preview?.kind === 'json' && !preview.source_truncated) {
    try { source = JSON.stringify(JSON.parse(source), null, 2) } catch { source = preview.source }
  }

  return <div className="flex h-dvh min-h-0 flex-col overflow-hidden bg-paper">
    <header className="flex shrink-0 flex-wrap items-center gap-3 border-b-2 border-ink px-4 py-4 sm:gap-5 sm:px-6">
      <Link to={back} className="artifact-action !border-line-strong" title="Back to instance" aria-label="Back to instance"><Icon name="back" /></Link>
      <div className="min-w-0 flex-1">
        <div className="mb-1 flex items-center gap-2"><Logo className="!h-2" /><span className="eyebrow">Artifact</span></div>
        <h1 title={filename} className="truncate font-mono text-base font-medium tracking-tight text-ink sm:text-xl">{filename}</h1>
      </div>
      <button className="artifact-action" aria-label="Refresh preview" title="Refresh preview" disabled={preparing || !path} onClick={refresh}><Icon name="refresh" className={preparing ? 'motion-safe:animate-spin' : ''} /></button>
      <Button variant="primary" disabled={!path || downloading} onClick={() => void download()} aria-label="Download file"><Icon name="download" /><span>{downloading ? 'Preparing…' : 'Download'}</span></Button>
    </header>

    <div className="flex shrink-0 flex-wrap items-center justify-between gap-x-4 border-b border-line px-4 sm:px-6">
      <div className="flex items-center" role="group" aria-label="View mode">
        {(html || csv) && <button className="artifact-tab" aria-pressed={!!showPreview} disabled={preparing || html && !config.data?.html_enabled} onClick={() => { setView('preview'); setConfirmScripts(false) }}><Icon name="preview" />Preview</button>}
        <button className="artifact-tab" aria-pressed={!showPreview} disabled={preparing} onClick={() => { setView('source'); setScripts(false); setConfirmScripts(false) }}><Icon name="code" />Source</button>
      </div>
      <div className="flex items-center gap-3 py-2">
        {html && showPreview && <>
          <div className="flex border border-line-strong" role="group" aria-label="Preview width">
            <button className={`artifact-action ${!mobile ? '!bg-ink !text-paper' : ''}`} title="Fit to window" aria-label="Fit to window" aria-pressed={!mobile} onClick={() => setMobile(false)}><Icon name="desktop" /></button>
            <button className={`artifact-action ${mobile ? '!bg-ink !text-paper' : ''}`} title="Mobile width" aria-label="Mobile width" aria-pressed={mobile} onClick={() => setMobile(true)}><Icon name="mobile" /></button>
          </div>
          {config.data?.interactive_enabled && <Button size="sm" disabled={preparing} onClick={() => scripts ? setScripts(false) : setConfirmScripts(true)}><Icon name={scripts ? 'stop' : 'play'} className="!h-3 !w-3" />{scripts ? 'Stop JavaScript' : 'Enable JavaScript'}</Button>}
        </>}
        {csv && showPreview && <label className="flex items-center gap-2 text-xs text-sec"><input type="checkbox" checked={firstRowHeader} disabled={!preview?.csv_rows?.length} onChange={(event) => setFirstRowHeader(event.target.checked)} />First row is header</label>}
      </div>
    </div>

    <dialog ref={confirmation} aria-labelledby="script-confirmation-title" onClose={() => { if (!confirmation.current?.open) setConfirmScripts(false) }} className="m-auto w-[min(28rem,calc(100vw-2rem))] border border-ink bg-paper p-0 text-body backdrop:bg-black/40">
      <div className="flex items-center gap-3 border-b border-line bg-warn/10 px-5 py-4"><Mark tone="yellow" /><h2 id="script-confirmation-title" className="text-base font-semibold text-ink">Run this report’s JavaScript?</h2></div>
      <p className="px-5 py-5 text-sm leading-relaxed">The report runs on an isolated origin without console access. Network restrictions are not a complete firewall, and browser code is outside Starlark resource limits.</p>
      <div className="flex justify-end gap-2 border-t border-line px-5 py-4"><Button onClick={() => setConfirmScripts(false)}>Cancel</Button><Button variant="primary" onClick={() => { setScripts(true); setConfirmScripts(false) }}>Run interactive preview</Button></div>
    </dialog>
    {downloadError && <div role="alert" className="shrink-0"><Notice tone="red">{downloadError}</Notice></div>}
    {html && config.isSuccess && !config.data.html_enabled && <Notice>HTML rendering is not configured. Source and downloads are available; configure an isolated preview domain to render reports.</Notice>}
    {preview?.redacted && <Notice>Secrets were redacted. Delivered content may differ from the generated file.</Notice>}
    {preview?.source_truncated && !showPreview && <Notice>Source view is limited to 64 KiB. Download uses the complete redacted file within export limits.</Notice>}
    {preview?.csv_truncated && showPreview && <Notice>Table preview is limited to 200 rows, 32 columns, 512 bytes per cell, and 256 KiB total. Download is not truncated.</Notice>}

    <main aria-label="Artifact content" aria-busy={loading} className={`min-h-0 flex-1 overflow-auto bg-surface ${html && showPreview ? 'p-3 sm:p-5' : ''}`}>
      {loading ? <div className="flex h-full items-center justify-center"><Loading label="Preparing preview…" /></div> : error ? <div className="flex min-h-full items-center justify-center p-8"><div className="max-w-lg border-l-4 border-danger pl-5"><h2 className="text-xl font-semibold text-ink">Preview unavailable</h2><p role="alert" className="mt-2 break-words text-sm text-sec">{error.message}</p><div className="mt-5 flex items-center gap-4"><Button onClick={refresh} disabled={preparing || !path}>Try again</Button><Link className="link text-sm" to={back}>Back to files</Link></div></div></div> : expired && showPreview ? <div className="flex h-full items-center justify-center"><div className="max-w-sm text-center"><Icon name="refresh" className="mx-auto mb-4 !h-7 !w-7 text-mute" /><h2 className="text-xl font-semibold text-ink">Preview expired</h2><p className="mt-2 text-sm text-sec">Refresh to create a new snapshot while the instance is running.</p><Button className="mt-5" onClick={refresh}>Create new preview</Button></div></div> : preview && <>
        {html && showPreview && preview.preview_url ? <div className="mx-auto flex h-full min-h-0 max-w-full flex-col border border-line-strong bg-white motion-safe:transition-[width]" style={{ width: mobile ? 390 : '100%' }}>
          <iframe key={preview.preview_url} title="Artifact preview" src={preview.preview_url} sandbox={preview.interactive ? 'allow-scripts' : ''} referrerPolicy="no-referrer" className="min-h-0 w-full flex-1 border-0 bg-white" style={{ colorScheme: 'light', pointerEvents: confirmScripts ? 'none' : undefined }} />
        </div> : csv && showPreview && !preview.preview_error ? <div className="min-h-full bg-paper">
          {!preview.csv_rows?.length ? <div className="p-8 text-sm text-sec">CSV has no records.</div> : <table className="tbl font-mono text-xs">
            <thead className="sticky top-0 bg-paper"><tr><th scope="col" className="!w-16 !text-right">Row</th>{(firstRowHeader ? preview.csv_rows[0] : Array.from({ length: Math.max(...preview.csv_rows.map((row) => row.length)) }, (_, index) => String(index + 1))).map((cell, index) => <th scope="col" key={index}>{cell}</th>)}</tr></thead>
            <tbody>{preview.csv_rows.slice(firstRowHeader ? 1 : 0).map((row, index) => <tr key={index} className="hover:bg-surface"><th scope="row" className="!border-line !text-right !text-mute">{index + (firstRowHeader ? 2 : 1)}</th>{row.map((cell, column) => <td key={column} className="max-w-96 whitespace-pre-wrap break-words text-ink">{cell}</td>)}</tr>)}</tbody>
          </table>}
        </div> : <>{preview.preview_error && <Notice tone="red">Table preview unavailable: {preview.preview_error}</Notice>}<pre aria-label="File source" className="min-h-full whitespace-pre-wrap break-words bg-code p-4 font-mono text-xs leading-6 text-ink sm:p-6">{source || '(empty file)'}</pre></>}
      </>}
    </main>

    <footer className="flex shrink-0 flex-wrap items-center gap-x-5 gap-y-2 border-t border-line px-4 py-2 text-[11px] text-sec sm:px-6">
      <span className="inline-flex items-center gap-2"><Mark tone={expired || scripts && showPreview ? 'yellow' : error ? 'red' : 'blue'} />{status}</span>
      <span className="min-w-0 flex-1 truncate font-mono" title={path}>{path}</span>
      {preview && <span className="font-mono">{fmtBytes(preview.size)}</span>}
      <details className="relative"><summary className="cursor-pointer hover:text-ink">About this preview</summary><div className="absolute bottom-7 right-0 z-10 w-72 max-w-[85vw] border border-line-strong bg-paper p-4 text-xs leading-relaxed shadow-sm">
        {preview?.expires_at && <p className="mb-2">Snapshot access expires {fmtTime(preview.expires_at)}.</p>}
        <p>Refresh and download read current files, not a saved copy. Files are available only while the instance is running.</p>
        {html && <p className="mt-2">Static mode removes scripts and unsupported markup. Interactive mode is not a network firewall. Downloaded HTML runs outside these viewer controls.</p>}
        {csv && <p className="mt-2">Spreadsheet exports can contain formulas. Use <code>csv.format(..., spreadsheet_safe=True)</code> when needed.</p>}
      </div></details>
      {dev && <span className="inline-flex items-center gap-1.5 text-warn-text"><Mark tone="yellow" />dev · anonymous</span>}
      {notice && <span role="status" className="w-full">{notice}</span>}
    </footer>
  </div>
}
