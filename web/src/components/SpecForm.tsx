import { forwardRef, useEffect, useImperativeHandle, useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { api, currentWorkspace, type ExtensionCatalog, type Policy, type Secret } from '../api'
import {
  buildInstanceSpec, draftFromSpec, validateRawSpec,
  type InstanceDraft, type InstanceSpec, type DraftError, type BuildResult,
} from '../instanceDraft'
import { Button, Field, inputCls } from './ui'
import { InstanceExtensionEditor } from './InstanceExtensionEditor'

export interface SpecFormHandle { buildSpec(): { spec?: InstanceSpec; err?: string } }

interface Props {
  keyMode?: boolean
  initial?: InstanceSpec
  workspaceId?: string
  onSpecChange?: (draft: InstanceDraft, errors?: DraftError[]) => void
}

// The draft is the only editable model. Cards, JSON mode and submission
// all pass through the same validation; mode switches never rebuild from
// a partial projection of the spec.
export const SpecForm = forwardRef<SpecFormHandle, Props>(function SpecForm({
  keyMode = false, initial, workspaceId, onSpecChange,
}, ref) {
  const [draft, setDraft] = useState(() => draftFromSpec(initial))
  const [advanced, setAdvanced] = useState(false)
  const [json, setJSON] = useState('')
  const [picker, setPicker] = useState(false)
  const [editing, setEditing] = useState<string | null>(null)
  const [editorError, setEditorError] = useState('')
  const [submitted, setSubmitted] = useState(false)
  const ws = workspaceId ?? currentWorkspace() ?? ''
  const scope = (path: string) => ws ? `${path}?workspace=${encodeURIComponent(ws)}` : path
  const vault = useQuery({
    queryKey: ['secrets', ws],
    queryFn: () => api.get<{ secrets: Secret[] }>(scope('/api/v1/secrets')),
    retry: false,
  })
  const policies = useQuery({
    queryKey: ['policies', ws],
    queryFn: () => api.get<{ policies: Policy[] }>(scope('/api/v1/policies')),
    retry: false,
  })
  const catalog = useQuery({
    queryKey: ['extensions', ws],
    queryFn: () => api.get<ExtensionCatalog>(scope('/api/v1/extensions')),
    enabled: picker || draft.exts.length > 0 || advanced,
    retry: false,
  })
  const set = (patch: Partial<InstanceDraft>) => setDraft((d) => ({ ...d, ...patch }))
  const extensions = useMemo(() => catalog.data?.extensions ?? [], [catalog.data])
  const activeDraft = useMemo(() => {
    if (!advanced) return { draft, errors: [] as DraftError[] }
    try {
      const raw: unknown = JSON.parse(json)
      const errors = validateRawSpec(raw, keyMode)
      return errors.length ? { draft, errors } : { draft: draftFromSpec(raw as InstanceSpec), errors }
    } catch {
      return { draft, errors: [{ section: 'JSON', message: 'Fix invalid JSON before returning to the editor or creating.' }] }
    }
  }, [advanced, json, draft, keyMode])
  const result = useMemo<BuildResult>(() => {
    const built = buildInstanceSpec(activeDraft.draft, { keyMode, catalog: extensions })
    const errors = [...activeDraft.errors, ...(built.ok ? [] : built.errors)]
    if (activeDraft.draft.exts.length && (catalog.isPending || catalog.isError)) {
      errors.push({ section: 'Extensions', message: catalog.isError ? 'Retry loading the extension catalog before creating.' : 'Wait for the extension catalog.' })
    }
    if (!advanced && editorError) errors.push({ section: 'Extensions', message: editorError })
    return errors.length ? { ok: false, errors } : built
  }, [activeDraft, keyMode, extensions, catalog.isPending, catalog.isError, editorError, advanced])
  useEffect(() => {
    onSpecChange?.(activeDraft.draft, result.ok ? [] : result.errors)
  }, [activeDraft.draft, result, onSpecChange])
  useImperativeHandle(ref, () => ({
    buildSpec() {
      setSubmitted(true)
      return result.ok ? { spec: result.spec } : { err: result.errors.map((e) => `${e.section}: ${e.message}`).join('; ') }
    },
  }), [result])
  const toggleAdvanced = () => {
    if (!advanced) {
      if (editorError) { setSubmitted(true); return }
      // A structurally valid but incomplete draft can still be fixed in JSON.
      const built = buildInstanceSpec(draft)
      if (!built.spec) { setSubmitted(true); return }
      setJSON(JSON.stringify(built.spec, null, 2))
      setAdvanced(true)
    } else if (activeDraft.errors.length === 0) {
      setDraft(activeDraft.draft)
      setAdvanced(false)
    } else setSubmitted(true)
  }
  const startFiles = () => {
    set({ fs: { quotaBytes: 67108864, extra: {} } })
    setEditing('fs')
    setPicker(false)
  }
  const removeExtension = (index: number) => {
    set({ exts: draft.exts.filter((_, i) => i !== index) })
    setEditing(null)
    setEditorError('')
  }
  const defaults = policies.data?.policies.filter((p) => p.default).map((p) => p.name) ?? ['builtin.block_private_network']
  const selectedPolicies = draft.policies ?? defaults
  const cardClass = 'border border-line-strong bg-paper p-4'
  const sectionClass = 'border-t-2 border-ink pt-4'
  const itemHeader = (title: string, summary: string, id: string, remove: () => void) => (
    <div className="flex flex-wrap items-start gap-3">
      <div className="min-w-0 flex-1"><h4 className="text-base font-semibold text-ink">{title}</h4><p className="mt-1 break-words text-xs text-sec">{summary}</p></div>
      <button type="button" className="text-btn" aria-expanded={editing === id} onClick={() => { setEditing(editing === id ? null : id); setEditorError('') }}>Configure {title}</button>
      <button type="button" className="text-btn" onClick={remove} aria-label={`Remove ${title}`}>Remove</button>
    </div>
  )

  return (
    <div className="min-w-0 space-y-7">
      <div className="grid gap-4 sm:grid-cols-[minmax(0,1fr)_160px]">
        <Field label="Name"><input className={inputCls} value={draft.name} placeholder="e.g. research run" onChange={(e) => set({ name: e.target.value })} disabled={advanced} /></Field>
        <Field label="Lifetime (seconds)"><input className={inputCls} type="number" min="1" value={draft.ttlSeconds} disabled={advanced} onChange={(e) => set({ ttlSeconds: e.target.value === '' ? '' : Number(e.target.value) })} /></Field>
      </div>
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-sm text-sec">Configure the boundary. Create the instance. Then connect an agent.</p>
        <button type="button" className="text-btn underline" onClick={toggleAdvanced}>{advanced ? 'Back to editor' : 'Advanced JSON'}</button>
      </div>
      {advanced ? (
        <div className={sectionClass}>
          <p className="mb-3 text-sm text-sec">Advanced editing includes any inline secret values. Review lists names only. Invalid edits cannot be submitted.</p>
          <textarea aria-label="Instance spec JSON" className={inputCls + ' min-h-[420px] font-mono text-xs'} value={json} spellCheck={false} onChange={(e) => setJSON(e.target.value)} />
        </div>
      ) : (
        <>
          <section className={sectionClass} aria-label="Abilities">
            <div className="mb-4 flex items-center justify-between gap-3">
              <h3 className="text-xl font-semibold text-ink">Abilities</h3>
              <Button onClick={() => setPicker(!picker)} aria-expanded={picker}>Add ability</Button>
            </div>
            {!draft.fs && !draft.net && !draft.exts.length && (
              <div className="mb-4 bg-surface p-5">
                <h4 className="text-base font-semibold text-ink">Compute only</h4>
                <p className="mt-2 text-sm text-sec">Starlark and the standard library. No filesystem or network access.</p>
                <button type="button" className="mt-3 text-btn underline" onClick={startFiles}>Start with Files</button>
              </div>
            )}
            {picker && (
              <div className="mb-4 grid gap-3 border border-line-strong bg-surface p-4 sm:grid-cols-2" aria-label="Available abilities">
                {!draft.fs && <Button className="!h-auto !whitespace-normal p-3" onClick={startFiles}>Add Files - bounded virtual storage</Button>}
                {!draft.net && <Button className="!h-auto !whitespace-normal p-3" onClick={() => { set({ net: { allowHosts: [], methods: ['GET', 'POST'], extra: {} } }); setEditing('net'); setPicker(false) }}>Add Network - outbound HTTP</Button>}
                {extensions.map((entry) => <Button key={entry.source} className="!h-auto !whitespace-normal p-3" onClick={() => {
                  set({ exts: [...draft.exts, { alias: entry.name, source: entry.source, config: {}, extra: {} }] }); setEditing(`ext:${draft.exts.length}`); setPicker(false)
                }}>Add {entry.name}</Button>)}
                <Button onClick={() => { set({ exts: [...draft.exts, { alias: '', source: '', config: {}, extra: {} }] }); setEditing(`ext:${draft.exts.length}`); setPicker(false) }}>Custom extension</Button>
              </div>
            )}
            {(picker || draft.exts.length > 0) && catalog.isError && <p className="mb-3 text-sm text-danger">Could not load extensions. <button type="button" className="underline" onClick={() => catalog.refetch()}>Retry catalog</button></p>}
            <div className="space-y-3">
              {draft.fs && <div className={cardClass}>
                {itemHeader('Files', 'Grants a virtual filesystem, not access to the host.', 'fs', () => { set({ fs: null }); setEditing(null) })}
                {editing === 'fs' && <div className="mt-4"><Field label="Files quota (bytes; blank uses server default)"><input className={inputCls} type="number" value={draft.fs.quotaBytes} onChange={(e) => set({ fs: { ...draft.fs!, quotaBytes: e.target.value === '' ? '' : Number(e.target.value) } })} /></Field></div>}
              </div>}
              {draft.net && <div className={cardClass}>
                {itemHeader('Network', 'Grants outbound HTTP. Host and method rules apply to every call.', 'net', () => { set({ net: null }); setEditing(null) })}
                {editing === 'net' && <div className="mt-4 space-y-3">
                  <Field label="Allowed hosts (one per line; blank permits any host subject to rules)"><textarea className={inputCls} rows={3} value={draft.net.allowHosts.join('\n')} onChange={(e) => set({ net: { ...draft.net!, allowHosts: e.target.value.split('\n') } })} /></Field>
                  <Field label="HTTP methods (comma separated)"><input className={inputCls} value={draft.net.methods.join(',')} onChange={(e) => set({ net: { ...draft.net!, methods: e.target.value.split(',') } })} /></Field>
                </div>}
              </div>}
              {draft.exts.map((ext, index) => <div className={cardClass} key={index}>
                {itemHeader(ext.alias || 'Extension', ext.source || 'Choose a source and an alias.', `ext:${index}`, () => removeExtension(index))}
                {editing === `ext:${index}` && <div className="mt-4"><InstanceExtensionEditor value={ext} catalog={extensions} secretNames={draft.secrets.map((s) => s.name).filter(Boolean)}
                  onValidityChange={setEditorError} onChange={(value) => set({ exts: draft.exts.map((e, i) => i === index ? value : e) })} /></div>}
              </div>)}
              {Object.keys(draft.otherCaps).map((name) => <div key={name} className={cardClass}><h4 className="font-semibold text-ink">{name}</h4><p className="text-xs text-sec">Existing configuration preserved. Use Advanced JSON to edit.</p></div>)}
            </div>
          </section>
          <details className={sectionClass}>
            <summary className="cursor-pointer text-lg font-semibold text-ink">Rules <span className="ml-2 text-sm font-normal text-sec">Policies and run limits</span></summary>
            <div className="mt-4 space-y-4">
              <p className="text-sm text-sec">Rules constrain abilities; they do not grant access. Server policies always apply.</p>
              {policies.isError && <p className="text-sm text-danger">Could not load policies. <button className="underline" onClick={() => policies.refetch()}>Retry policies</button></p>}
              {(policies.data?.policies ?? []).map((p) => <label key={p.name} className="flex items-start gap-3 text-sm">
                <input type="checkbox" className="mt-1" checked={selectedPolicies.includes(p.name)} onChange={(e) => set({ policies: e.target.checked ? [...selectedPolicies, p.name] : selectedPolicies.filter((name) => name !== p.name) })} />
                <span className="break-all text-ink">{p.name}<span className="mt-1 block break-normal text-xs text-sec">{p.description}</span></span>
              </label>)}
              {selectedPolicies.filter((name) => !policies.data?.policies.some((p) => p.name === name)).map((name) => <label key={name} className="flex gap-3 text-sm"><input type="checkbox" checked onChange={() => set({ policies: selectedPolicies.filter((p) => p !== name) })} />{name}</label>)}
              <button type="button" className="text-btn underline" onClick={() => set({ policies: null })}>Use default policies</button>
              <div className="grid gap-3 sm:grid-cols-3">
                {(['exec_timeout_ms', 'max_steps', 'max_output_bytes'] as const).map((key) => <Field key={key} label={key}>
                  <input className={inputCls} type="number" min="1" value={draft.limits[key] ?? ''} placeholder="Server default" onChange={(e) => {
                    const limits = { ...draft.limits }; if (e.target.value === '') delete limits[key]; else limits[key] = Number(e.target.value); set({ limits })
                  }} />
                </Field>)}
              </div>
            </div>
          </details>
          <details className={sectionClass}>
            <summary className="cursor-pointer text-lg font-semibold text-ink">Inputs <span className="ml-2 text-sm font-normal text-sec">Variables, secrets and labels</span></summary>
            <div className="mt-4 space-y-5">
              {(['env', 'labels'] as const).map((kind) => <div key={kind}>
                <div className="mb-3 flex justify-between"><h4 className="font-semibold text-ink">{kind === 'env' ? 'Variables' : 'Labels'}</h4><button type="button" className="text-btn" onClick={() => set({ [kind]: [...draft[kind], { k: '', v: '' }] })}>Add {kind === 'env' ? 'variable' : 'label'}</button></div>
                {draft[kind].map((row, i) => <div key={i} className="mb-3 grid gap-2 sm:grid-cols-[1fr_1fr_auto]">
                  <input aria-label={`${kind} name ${i + 1}`} className={inputCls} value={row.k} onChange={(e) => set({ [kind]: draft[kind].map((r, j) => j === i ? { ...r, k: e.target.value } : r) })} />
                  <input aria-label={`${kind} value ${i + 1}`} className={inputCls} value={row.v} onChange={(e) => set({ [kind]: draft[kind].map((r, j) => j === i ? { ...r, v: e.target.value } : r) })} />
                  <button type="button" className="text-btn" aria-label={`Remove ${kind} ${i + 1}`} onClick={() => set({ [kind]: draft[kind].filter((_, j) => j !== i) })}>Remove</button>
                </div>)}
              </div>)}
              <div className="flex justify-between"><h4 className="font-semibold text-ink">Secrets</h4><button type="button" className="text-btn" onClick={() => set({ secrets: [...draft.secrets, { name: '', kind: 'vault', ref: '', value: '', domains: [], extra: {} }] })}>Add secret</button></div>
              {keyMode && <p className="text-xs text-sec">Restricted keys store vault references only. Inline values are not allowed.</p>}
              {vault.isError && <p className="text-xs text-sec">Vault unavailable. Existing references are preserved; enter a reference by name or retry. <button className="underline" onClick={() => vault.refetch()}>Retry vault</button></p>}
              <datalist id={`vault-names-${ws}`} >{vault.data?.secrets.map((s) => <option key={s.id} value={s.name} />)}</datalist>
              {draft.secrets.map((secret, index) => {
                const patch = (value: Partial<typeof secret>) => set({ secrets: draft.secrets.map((s, i) => i === index ? { ...s, ...value } : s) })
                return <div key={index} className={cardClass + ' space-y-3'}>
                  <div className="flex justify-between gap-3"><h5 className="font-semibold text-ink">{secret.name || 'New secret'}</h5><button type="button" className="text-btn" aria-label={`Remove secret ${index + 1}`} onClick={() => set({ secrets: draft.secrets.filter((_, i) => i !== index) })}>Remove</button></div>
                  <Field label={`Secret name ${index + 1}`}><input className={inputCls} value={secret.name} onChange={(e) => patch({ name: e.target.value })} /></Field>
                  <Field label={`Secret source ${index + 1}`}><select className={inputCls} value={secret.kind} onChange={(e) => patch({ kind: e.target.value === 'vault' ? 'vault' : 'inline', value: '', needsReplacement: false })}><option value="vault">Vault reference</option>{!keyMode && <option value="inline">Inline value</option>}</select></Field>
                  {secret.kind === 'vault'
                    ? <Field label={`Vault reference ${index + 1}`}><input className={inputCls} list={`vault-names-${ws}`} value={secret.ref} onChange={(e) => patch({ ref: e.target.value })} /></Field>
                    : <Field label={`Secret value ${index + 1}`}><input type="password" className={inputCls} autoComplete="new-password" value={secret.value} onChange={(e) => patch({ value: e.target.value, needsReplacement: false })} /></Field>}
                  {secret.needsReplacement && <p className="text-xs text-warn-text">The original inline value is not stored. Replace or remove this secret.</p>}
                  <Field label={`Allowed secret domains ${index + 1}`}><input className={inputCls} value={secret.domains.join(',')} onChange={(e) => patch({ domains: e.target.value.split(',') })} /></Field>
                </div>
              })}
            </div>
          </details>
        </>
      )}
      {!result.ok && (submitted || advanced) && <div className="border-l-4 border-danger bg-danger/5 p-4" role="alert"><h3 className="font-semibold text-danger">Check the configuration</h3><ul className="mt-2 space-y-1 text-sm">{result.errors.map((e, i) => <li key={i}>{e.section}: {e.message}</li>)}</ul></div>}
    </div>
  )
})
