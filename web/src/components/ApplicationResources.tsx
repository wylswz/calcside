import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { api, type ExtensionCatalog } from '../api'
import { mountName, parseModuleMounts, type ModuleMount } from '../moduleMounts'
import type { JsonValue } from '../instanceDraft'
import { Button, Icon } from './ui'

type LibraryModule = { id: string; name: string; latest_version: number }
type PublishedModule = { version: number; sum: string; files: Record<string, string> }

export type ModuleDocument = {
  kind: 'module'
  moduleId: string
  moduleName: string
  alias: string
  path: string
  version: number
  sum: string
  source: string
  mountFingerprint: string
}
export type ResourceDocument = ModuleDocument | {
  kind: 'extension'; alias: string; operation: string; source: string
  signature: string; documentation: string; extensionFingerprint: string
}

function record(value: unknown): Record<string, unknown> | undefined {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
    ? value as Record<string, unknown> : undefined
}

export function ApplicationResources({ specText, workspaceId, onConfigure, onOpenDocument, selectedDocument }: {
  specText: string; workspaceId: string; onConfigure: () => void
  onOpenDocument: (document: ResourceDocument) => void
  selectedDocument: ResourceDocument | null
}) {
  let spec: Record<string, unknown> | undefined
  try { spec = record(JSON.parse(specText || '{}')) } catch { /* Keep malformed JSON in the editor. */ }
  const extValue = record(spec?.capabilities)?.ext
  const extensions = record(extValue)
  const mounts = parseModuleMounts(spec?.modules as JsonValue | undefined)
  const catalog = useQuery({
    queryKey: ['extensions', workspaceId],
    queryFn: () => api.get<ExtensionCatalog>(`/api/v1/extensions?workspace=${encodeURIComponent(workspaceId)}`),
    staleTime: 60_000,
    retry: false,
    enabled: !!extensions && Object.keys(extensions).length > 0,
  })
  const library = useQuery({
    queryKey: ['modules', workspaceId],
    queryFn: () => api.get<{ modules: LibraryModule[] }>(`/api/v1/modules?workspace=${encodeURIComponent(workspaceId)}`),
    enabled: mounts.ok && mounts.mounts.length > 0,
    retry: false,
  })

  return (
    <details className="app-resources" open data-testid="application-resources">
      <summary><Icon name="folder" /> Resources</summary>
      <div className="app-resource-body">
        <p className="app-resource-hint">Available to this draft</p>
        {!spec && <p role="alert">Fix the spec JSON in Settings to browse resources.</p>}
        <h3>Extensions</h3>
        {extValue != null && !extensions && <p role="alert">Extension declarations are invalid.</p>}
        {(!extensions || Object.keys(extensions).length === 0) && <p className="app-resource-hint">No configured extensions.</p>}
        {catalog.isError && <p role="alert">Extension documentation unavailable. <button type="button" className="link" onClick={() => void catalog.refetch()}>Retry</button></p>}
        {Object.entries(extensions ?? {}).map(([alias, raw]) => {
          const source = record(raw)?.source
          const entry = catalog.data?.extensions.find((e) => e.source === source)
          return (
            <details key={alias} className="app-resource-node">
              <summary><Icon name="code" /><span>ext.{alias}</span></summary>
              <div className="app-resource-children">
                <p className="app-resource-hint">{typeof source === 'string' ? source : 'Invalid extension source'}</p>
                {catalog.isPending && <p className="app-resource-hint">Loading operations...</p>}
                {!catalog.isPending && !catalog.isError && !entry && <p>Documentation unavailable for this source.</p>}
                {entry?.ops.filter((op) => op.name).map((op) => (
                  <button type="button" key={op.name} className="app-resource-file"
                    aria-current={selectedDocument?.kind === 'extension' && selectedDocument.alias === alias &&
                      selectedDocument.operation === op.name ? 'true' : undefined}
                    onClick={() => onOpenDocument({
                      kind: 'extension', alias, operation: op.name!, source: String(source),
                      signature: `ext.${alias}.${op.name}(${(op.params ?? []).join(', ')})`,
                      documentation: op.doc ?? '', extensionFingerprint: JSON.stringify(raw),
                    })}><Icon name="code" /><span>{op.name}</span></button>
                ))}
              </div>
            </details>
          )
        })}
        <h3>Modules</h3>
        {!mounts.ok && <p role="alert">Module declarations are invalid. Edit them in Settings.</p>}
        {mounts.ok && mounts.mounts.length === 0 && <p className="app-resource-hint">No mounted modules.</p>}
        {library.isError && <p role="alert">Module library unavailable. <button type="button" className="link" onClick={() => void library.refetch()}>Retry</button></p>}
        {mounts.ok && mounts.mounts.map((mount) => (
          <ModuleResource key={mount.alias} mount={mount} workspaceId={workspaceId}
            module={library.data?.modules.find((m) => m.name === mountName(mount))}
            loading={library.isFetching} onOpenDocument={onOpenDocument} selectedDocument={selectedDocument} />
        ))}
        <Button size="sm" onClick={onConfigure}>Configure resources</Button>
      </div>
    </details>
  )
}

function ModuleResource({ mount, module, workspaceId, loading, onOpenDocument, selectedDocument }: {
  mount: ModuleMount; module?: LibraryModule; workspaceId: string; loading: boolean
  onOpenDocument: (document: ResourceDocument) => void
  selectedDocument: ResourceDocument | null
}) {
  const [open, setOpen] = useState(false)
  const pinned = typeof mount.ref.version === 'number' && Number.isSafeInteger(mount.ref.version) && mount.ref.version > 0
  const version = pinned ? mount.ref.version as number : module?.latest_version
  const invalidVersion = mount.ref.version != null && mount.ref.version !== 0 && !pinned
  const q = useQuery({
    queryKey: ['resource-module-version', workspaceId, module?.id, version],
    queryFn: () => api.get<{ version: PublishedModule }>(
      `/api/v1/modules/${encodeURIComponent(module!.id)}/versions/${version}?workspace=${encodeURIComponent(workspaceId)}`),
    enabled: open && !!module && !!version && !invalidVersion,
    staleTime: Infinity,
    retry: false,
  })
  // F3: never substitute a draft/latest tree for an unavailable or mismatched pin.
  const mismatch = q.data && (q.data.version.version !== version ||
    (mount.ref.sum != null && mount.ref.sum !== '' && mount.ref.sum !== q.data.version.sum))
  return (
    <details className="app-resource-node" open={open} onToggle={(e) => setOpen(e.currentTarget.open)}>
      <summary><Icon name="folder" /><span>@{mount.alias}</span></summary>
      <div className="app-resource-children">
        <p className="app-resource-hint">{mountName(mount)} {pinned ? `v${version}` : '(latest published, not pinned)'}</p>
        {loading && !module ? <p>Loading module...</p>
          : !module || !version || invalidVersion ? <p role="alert">Published module unavailable.</p>
          : q.isError ? <p role="alert">Could not load this version. <button type="button" className="link" onClick={() => void q.refetch()}>Retry</button></p>
          : mismatch ? <p role="alert">Module pin does not match this version.</p>
          : q.data ? <ModuleFiles files={Object.keys(q.data.version.files).filter((f) => f.endsWith('.star'))}
            contents={q.data.version.files} documentBase={{
              kind: 'module', moduleId: module.id, moduleName: mountName(mount), alias: mount.alias,
              version: q.data.version.version, sum: q.data.version.sum, mountFingerprint: JSON.stringify(mount.ref),
            }} onOpenDocument={onOpenDocument} selectedDocument={selectedDocument} />
          : <p className="app-resource-hint">Loading files...</p>}
      </div>
    </details>
  )
}

function ModuleFiles({ files, contents, documentBase, onOpenDocument, selectedDocument, prefix = '' }: {
  files: string[]
  contents: Record<string, string>
  documentBase: Omit<ModuleDocument, 'path' | 'source'>
  onOpenDocument: (document: ResourceDocument) => void
  selectedDocument: ResourceDocument | null
  prefix?: string
}) {
  const folders = [...new Set(files.filter((f) => f.startsWith(prefix) && f.slice(prefix.length).includes('/'))
    .map((f) => f.slice(prefix.length).split('/')[0]))].sort()
  const leaves = files.filter((f) => f.startsWith(prefix) && !f.slice(prefix.length).includes('/')).sort()
  return (
    <>
      {folders.map((folder) => (
        <details key={folder} className="app-resource-node">
          <summary><Icon name="folder" /><span>{folder}</span></summary>
          <div className="app-resource-children"><ModuleFiles files={files} contents={contents} documentBase={documentBase}
            onOpenDocument={onOpenDocument} selectedDocument={selectedDocument} prefix={`${prefix}${folder}/`} /></div>
        </details>
      ))}
      {leaves.map((file) => (
        <button key={file} type="button" className="app-resource-file"
          aria-current={selectedDocument?.kind === 'module' && selectedDocument.alias === documentBase.alias && selectedDocument.path === file &&
            selectedDocument.sum === documentBase.sum ? 'true' : undefined}
          title={`Open @${documentBase.alias}/${file}`}
          onClick={() => onOpenDocument({ ...documentBase, path: file, source: contents[file] })}>
          <Icon name="file" /><span>{file.slice(prefix.length)}</span>
        </button>
      ))}
    </>
  )
}
