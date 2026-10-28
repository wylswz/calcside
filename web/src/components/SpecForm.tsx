import { forwardRef, useEffect, useImperativeHandle, useMemo, useRef, useState, type ReactNode } from 'react'
import { useQuery } from '@tanstack/react-query'
import * as Tabs from '@radix-ui/react-tabs'
import { api, currentWorkspace, type ExtensionCatalog, type ExtensionInfo, type Policy, type Secret } from '../api'
import {
  buildInstanceSpec, draftFromSpec, uniqueExtensionAlias, validateRawSpec,
  type BuildResult, type DraftError, type ExtDraft, type InstanceDraft, type InstanceSpec,
} from '../instanceDraft'
import { fmtBytes } from '../format'
import { Field, inputCls } from './ui'
import { InstanceExtensionEditor } from './InstanceExtensionEditor'
import { ExtensionPicker } from './ExtensionPicker'
import { PolicyPicker } from './PolicyPicker'
import { ExtensionIcon } from './ExtensionIcon'
import { InputsSection, LimitsSection } from './SpecSections'

export interface SpecFormHandle { buildSpec(): { spec?: InstanceSpec; specJSON?: string; err?: string } }

interface Props {
  keyMode?: boolean
  application?: boolean
  initial?: InstanceSpec
  workspaceId?: string
  onSpecChange?: (draft: InstanceDraft, errors?: DraftError[], built?: { spec?: InstanceSpec; specJSON?: string }) => void
  onRawDirtyChange?: (dirty: boolean) => void
  identity?: ReactNode
  footer?: ReactNode
}

// Item keys order the instance summary: 'fs', 'net' or 'ext:<id>'. The id
// is UI-only and never serialized.
type ItemKey = string
type View = 'capabilities' | 'settings'
type Removed = { at: number; title: string } & (
  | { key: 'fs'; data: NonNullable<InstanceDraft['fs']> }
  | { key: 'net'; data: NonNullable<InstanceDraft['net']> }
  | { key: ItemKey; data: ExtDraft; id: number }
)
// summary describes the item in Settings; brief fits the one-line row.
interface Item { key: ItemKey; title: string; icon: ReactNode; summary: string; brief: string; issues: string[]; missing: string[]; body: ReactNode }

const FilesIcon = () => <svg aria-hidden="true" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"><path d="M14 3H6a1 1 0 0 0-1 1v16a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V8z"/><path d="M14 3v5h5"/></svg>
const NetworkIcon = () => <svg aria-hidden="true" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"><circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3a15 15 0 0 1 0 18M12 3a15 15 0 0 0 0 18"/></svg>
const CheckIcon = () => <svg aria-hidden="true" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><path d="m5 12 5 5L20 7"/></svg>

const capabilityLabel = (name: string) => name === 'net' ? 'Network' : name === 'fs' ? 'Files' : name
const reducedMotion = () => window.matchMedia('(prefers-reduced-motion: reduce)').matches

// The draft is the only editable model. Cards, JSON mode and submission
// all pass through the same validation; mode switches never rebuild from
// a partial projection of the spec.
//
// Two views share one draft: "Add capabilities" is a fixed palette where
// adding only changes a card's state in place, and "Settings" is a normal
// page section for the forms. The instance summary beside them is short
// (one fixed-height row per capability), so nothing needs its own scroll.
export const SpecForm = forwardRef<SpecFormHandle, Props>(function SpecForm({
  keyMode = false, application = false, initial, workspaceId, onSpecChange, onRawDirtyChange, identity, footer,
}, ref) {
  // Application mode: vault-ref secrets only (same ceiling as key
  // specs) — never inline values in a durable spec.
  const refsOnly = keyMode || application
  const [draft, setDraft] = useState(() => draftFromSpec(initial))
  const [advanced, setAdvanced] = useState(false)
  const [json, setJSON] = useState('')
  const jsonBaseline = useRef('')
  const [view, setView] = useState<View>('capabilities')
  const extIds = useRef<number[]>(draft.exts.map((_, i) => i))
  const extNext = useRef(draft.exts.length)
  const [order, setOrder] = useState<ItemKey[]>(() => [
    ...(draft.fs ? ['fs'] : []), ...(draft.net ? ['net'] : []), ...extIds.current.map((id) => `ext:${id}`),
  ])
  const [removed, setRemoved] = useState<Removed | null>(null)
  const [flash, setFlash] = useState<ItemKey | null>(null)
  const [reveal, setReveal] = useState<ItemKey | null>(null)
  const tabsRef = useRef<HTMLDivElement>(null)
  const settingsRef = useRef<HTMLDivElement>(null)
  const [extErrors, setExtErrors] = useState<Map<number, string>>(new Map())
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
    retry: false,
  })
  const set = (patch: Partial<InstanceDraft>) => setDraft((d) => ({ ...d, ...patch }))
  const extensions = useMemo(() => (catalog.data?.extensions ?? []).filter((e) => e.interface !== 'llm'), [catalog.data])
  const activeDraft = useMemo(() => {
    if (!advanced) return { draft, errors: [] as DraftError[] }
    try {
      const raw: unknown = JSON.parse(json)
      const errors = validateRawSpec(raw, refsOnly)
      return errors.length ? { draft, errors } : { draft: draftFromSpec(raw as InstanceSpec), errors }
    } catch {
      return { draft, errors: [{ section: 'JSON', message: 'Fix invalid JSON before returning to the editor or creating.' }] }
    }
  }, [advanced, json, draft, refsOnly])
  const result = useMemo<BuildResult>(() => {
    const built = buildInstanceSpec(activeDraft.draft, { keyMode: refsOnly, catalog: extensions })
    const errors = [...activeDraft.errors, ...(built.ok ? [] : built.errors)]
    if (activeDraft.draft.exts.length && (catalog.isPending || catalog.isError)) {
      errors.push({ section: 'Extensions', message: catalog.isError ? 'Retry loading the extension catalog before creating.' : 'Wait for the extension catalog.' })
    }
    if (!advanced) extErrors.forEach((m) => errors.push({ section: 'Extensions', message: m }))
    return errors.length ? { ok: false, errors } : built
  }, [activeDraft, refsOnly, extensions, catalog.isPending, catalog.isError, extErrors, advanced])
  useEffect(() => {
    onSpecChange?.(activeDraft.draft, result.ok ? [] : result.errors,
      { spec: result.spec, ...(advanced ? { specJSON: json } : {}) })
  }, [activeDraft.draft, result, onSpecChange, advanced, json])
  useEffect(() => {
    onRawDirtyChange?.(advanced && json !== jsonBaseline.current)
  }, [advanced, json, onRawDirtyChange])
  useImperativeHandle(ref, () => ({
    buildSpec() {
      setSubmitted(true)
      return result.ok
        ? { spec: result.spec, ...(advanced ? { specJSON: json } : {}) }
        : { err: result.errors.map((e) => `${e.section}: ${e.message}`).join('; ') }
    },
  }), [result, advanced, json])
  // "Configure" is navigation: the page scrolls to the chosen section.
  useEffect(() => {
    if (!reveal) return
    setReveal(null)
    settingsRef.current?.querySelector<HTMLElement>(`[data-section="${reveal}"]`)
      ?.scrollIntoView({ block: 'start', behavior: reducedMotion() ? 'auto' : 'smooth' })
  }, [reveal])
  useEffect(() => {
    if (!flash) return
    const t = window.setTimeout(() => setFlash(null), 900)
    return () => window.clearTimeout(t)
  }, [flash])

  const toggleAdvanced = () => {
    if (!advanced) {
      if (extErrors.size) { setSubmitted(true); return }
      // A structurally valid but incomplete draft can still be fixed in JSON.
      const built = buildInstanceSpec(draft)
      if (!built.spec) { setSubmitted(true); return }
      jsonBaseline.current = JSON.stringify(built.spec, null, 2)
      setJSON(jsonBaseline.current)
      setAdvanced(true)
    } else if (activeDraft.errors.length === 0) {
      const next = activeDraft.draft
      extIds.current = next.exts.map(() => extNext.current++)
      setOrder([...(next.fs ? ['fs'] : []), ...(next.net ? ['net'] : []), ...extIds.current.map((id) => `ext:${id}`)])
      setRemoved(null)
      setExtErrors(new Map())
      setDraft(next)
      setAdvanced(false)
    } else setSubmitted(true)
  }

  const switchView = (next: View) => {
    setView(next)
    const tabs = tabsRef.current
    if (tabs && tabs.getBoundingClientRect().top < 0) tabs.scrollIntoView({ block: 'start', behavior: reducedMotion() ? 'auto' : 'smooth' })
  }
  const openSettings = (section: string) => { setView('settings'); setReveal(section) }
  const append = (key: ItemKey) => {
    setOrder((o) => [...o.filter((k) => k !== key), key])
    setRemoved(null)
    setFlash(key)
  }
  // Adding twice never navigates or removes: a second click only points
  // at the row that is already in the summary.
  const addFiles = () => {
    if (draft.fs) return setFlash('fs')
    set({ fs: { quotaBytes: 67108864, extra: {} } })
    append('fs')
  }
  const addNetwork = () => {
    if (draft.net) return setFlash('net')
    set({ net: { allowHosts: [], methods: ['GET', 'POST'], extra: {} } })
    append('net')
  }
  const addExtension = (entry: { name: string; source: string }) => {
    const id = extNext.current++
    extIds.current = [...extIds.current, id]
    const alias = entry.source ? uniqueExtensionAlias(entry.name, new Set(draft.exts.map((e) => e.alias))) : ''
    set({ exts: [...draft.exts, { alias, source: entry.source, config: {}, extra: {} }] })
    append(`ext:${id}`)
    // A custom extension has nothing usable until it has a source.
    if (!entry.source) openSettings(`ext:${id}`)
  }
  const pointAtExtension = (entry: ExtensionInfo) => {
    const index = draft.exts.findIndex((e) => e.source === entry.source)
    if (index >= 0) setFlash(`ext:${extIds.current[index]}`)
  }
  const setExtError = (id: number, message: string) =>
    setExtErrors((m) => { const n = new Map(m); if (message) n.set(id, message); else n.delete(id); return n })

  // Removing leaves an undo row of the same height in the same slot, so
  // the next row does not slide under the pointer.
  const remove = (key: ItemKey, title: string) => {
    const at = order.indexOf(key)
    setOrder((o) => o.filter((k) => k !== key))
    if (key === 'fs' && draft.fs) { setRemoved({ key, at, title, data: draft.fs }); set({ fs: null }); return }
    if (key === 'net' && draft.net) { setRemoved({ key, at, title, data: draft.net }); set({ net: null }); return }
    const id = Number(key.slice(4))
    const index = extIds.current.indexOf(id)
    if (index < 0) return
    setRemoved({ key, at, title, data: draft.exts[index], id })
    extIds.current = extIds.current.filter((x) => x !== id)
    set({ exts: draft.exts.filter((_, i) => i !== index) })
    setExtErrors((m) => { const n = new Map(m); n.delete(id); return n })
  }
  const undo = () => {
    if (!removed) return
    if (removed.key === 'fs') set({ fs: removed.data as NonNullable<InstanceDraft['fs']> })
    else if (removed.key === 'net') set({ net: removed.data as NonNullable<InstanceDraft['net']> })
    else if ('id' in removed) {
      extIds.current = [...extIds.current, removed.id]
      set({ exts: [...draft.exts, removed.data] })
    }
    setOrder((o) => { const n = o.filter((k) => k !== removed.key); n.splice(Math.min(removed.at, n.length), 0, removed.key); return n })
    setRemoved(null)
  }

  const errors = result.ok ? [] : result.errors
  const has = (cap: string) => cap === 'fs' ? !!draft.fs : cap === 'net' ? !!draft.net : Object.hasOwn(draft.otherCaps, cap)
  const extKeys = extIds.current.map((id) => `ext:${id}`)
  const present = new Set([...(draft.fs ? ['fs'] : []), ...(draft.net ? ['net'] : []), ...extKeys])
  const keys = [...order.filter((k) => present.has(k)), ...[...present].filter((k) => !order.includes(k))]
  const selectedSources = new Set(draft.exts.map((e) => e.source))
  const defaults = policies.data?.policies.filter((p) => p.default).map((p) => p.name) ?? []
  const selectedPolicies = draft.policies ?? defaults
  const hasDefaults = (policies.data?.policies ?? []).some((p) => p.default)
  const togglePolicy = (name: string) =>
    set({ policies: selectedPolicies.includes(name) ? selectedPolicies.filter((n) => n !== name) : [...selectedPolicies, name] })
  const removePolicy = (name: string) => set({ policies: selectedPolicies.filter((n) => n !== name) })
  const policyKind = (name: string) => policies.data?.policies.find((p) => p.name === name)?.kind
  const inputCount = draft.env.length + draft.secrets.length + draft.labels.length
  const limitCount = Object.keys(draft.limits).length
  const limitsSummary = limitCount ? `${limitCount} ${limitCount === 1 ? 'limit' : 'limits'}` : 'Server defaults'
  const inputsSummary = inputCount ? `${inputCount} ${inputCount === 1 ? 'input' : 'inputs'}` : 'None'
  const sectionIssues = (sections: string[]) => errors.filter((e) => sections.includes(e.section)).length

  const describe = (key: ItemKey): Item | null => {
    if (key === 'fs' && draft.fs) {
      return {
        key, title: 'Files', icon: <FilesIcon />, missing: [],
        summary: draft.fs.quotaBytes === '' ? 'Server default quota' : `${fmtBytes(draft.fs.quotaBytes)} virtual storage`,
        brief: draft.fs.quotaBytes === '' ? 'Default quota' : fmtBytes(draft.fs.quotaBytes),
        issues: errors.filter((e) => e.section === 'Files').map((e) => e.message),
        body: <Field label="Files quota (bytes; blank uses server default)"><input className={inputCls} type="number" value={draft.fs.quotaBytes} onChange={(e) => set({ fs: { ...draft.fs!, quotaBytes: e.target.value === '' ? '' : Number(e.target.value) } })} /></Field>,
      }
    }
    if (key === 'net' && draft.net) {
      const hosts = draft.net.allowHosts.map((h) => h.trim()).filter(Boolean)
      const summary = `${hosts.length ? hosts.join(', ') : 'Any public host'} · ${draft.net.methods.map((m) => m.trim()).filter(Boolean).join('/') || 'no methods'}`
      return {
        key, title: 'Network', icon: <NetworkIcon />, missing: [], summary, brief: summary,
        issues: errors.filter((e) => e.section === 'Network').map((e) => e.message),
        body: <div className="space-y-3">
          <Field label="Allowed hosts (one per line; blank permits any host subject to rules)"><textarea className={inputCls} rows={3} value={draft.net.allowHosts.join('\n')} onChange={(e) => set({ net: { ...draft.net!, allowHosts: e.target.value.split('\n') } })} /></Field>
          <Field label="HTTP methods (comma separated)"><input className={inputCls} value={draft.net.methods.join(',')} onChange={(e) => set({ net: { ...draft.net!, methods: e.target.value.split(',') } })} /></Field>
        </div>,
      }
    }
    if (!key.startsWith('ext:')) return null
    const id = Number(key.slice(4))
    const index = extIds.current.indexOf(id)
    const ext = draft.exts[index]
    if (!ext) return null
    const entry = extensions.find((e) => e.source === ext.source)
    const own = (m: string) => ext.alias ? m.startsWith(`${ext.alias}:`) || m === `duplicate alias ${ext.alias}` : m === 'extension alias is required'
    return {
      key, title: ext.alias || 'extension',
      icon: <ExtensionIcon iconUrl={entry?.icon_url} name={entry?.name ?? ext.alias} />,
      summary: entry?.description || ext.source || 'Choose a source and an alias.',
      brief: Object.keys(ext.config).length ? 'Configured' : 'Default settings',
      missing: (entry?.dependencies ?? []).filter((d) => !has(d)),
      issues: [...errors.filter((e) => e.section === 'Extensions' && own(e.message) && !e.message.includes('— add it explicitly')).map((e) => e.message), ...(extErrors.has(id) ? [extErrors.get(id)!] : [])],
      body: <InstanceExtensionEditor value={ext} catalog={extensions}
        secretNames={draft.secrets.map((s) => s.name).filter(Boolean)}
        vaultNames={(vault.data?.secrets ?? []).map((s) => s.name)}
        onUseVault={(name) => set({ secrets: draft.secrets.some((s) => s.name === name) ? draft.secrets : [...draft.secrets, { name, kind: 'vault', ref: name, value: '', domains: [], extra: {} }] })}
        onValidityChange={(m) => setExtError(id, m)} onChange={(value) => set({ exts: draft.exts.map((e, i) => i === index ? value : e) })} />,
    }
  }
  const items = keys.map(describe).filter((x): x is Item => x !== null)

  // One line, fixed height: "Needs Network" becomes the summary in place.
  const status = (item: Item) => {
    if (item.missing.length) {
      return <span className="selected-status is-warn">Needs {item.missing.map(capabilityLabel).join(', ')}
        {item.missing.filter((d) => d === 'net' || d === 'fs').map((d) => (
          <button key={d} type="button" className="underline" aria-label={`Add ${capabilityLabel(d)}`} onClick={() => d === 'net' ? addNetwork() : addFiles()}>Add</button>
        ))}
      </span>
    }
    if (item.issues.length) return <span className="selected-status is-warn">Needs setup</span>
    return <span className="selected-status" title={item.summary}>{item.brief}</span>
  }
  const rows: ReactNode[] = items.map((item) => (
    <div key={item.key} data-row={item.key} className={'selected-row' + (flash === item.key ? ' is-flash' : '')}>
      <span className="config-item-icon">{item.icon}</span>
      <div className="min-w-0 flex-1">
        <p className="config-item-title">{item.title}</p>
        {status(item)}
      </div>
      <button type="button" className="text-btn" aria-label={`Configure ${item.title}`} onClick={() => openSettings(item.key)}>Configure</button>
      <button type="button" className="text-btn" aria-label={`Remove ${item.title}`} onClick={() => remove(item.key, item.title)}>Remove</button>
    </div>
  ))
  if (removed) {
    rows.splice(Math.min(removed.at, rows.length), 0, (
      <div key="removed" className="selected-row is-removed" role="status">
        <span>{removed.title} removed</span>
        <button type="button" className="text-btn" onClick={undo}>Undo</button>
      </div>
    ))
  }

  const alert = !result.ok && !footer && (submitted || advanced) && (
    <div className="rounded-md border-l-4 border-danger bg-danger/5 p-4" role="alert"><h3 className="font-semibold text-danger">Check the configuration</h3><ul className="mt-2 space-y-1 text-sm">{errors.map((e, i) => <li key={i}>{e.section}: {e.message}</li>)}</ul></div>
  )

  if (advanced) {
    return (
      <div className="spec-form">
        <div className="spec-json">
          <div className="mb-3 flex flex-wrap items-center justify-between gap-3">
            <p className="text-sm text-sec">{refsOnly ? 'Secrets must use vault references.' : 'Advanced editing includes any inline secret values.'} Invalid edits cannot be submitted.</p>
            <button type="button" className="text-btn underline" onClick={toggleAdvanced}>Back to editor</button>
          </div>
          <textarea aria-label={application ? 'Application spec JSON' : 'Instance spec JSON'} className={inputCls + ' min-h-[420px] font-mono text-xs'} value={json} spellCheck={false} onChange={(e) => setJSON(e.target.value)} />
          {alert}
          {footer && <div className="spec-json-footer">{footer}</div>}
        </div>
      </div>
    )
  }

  return (
    <div className="spec-form">
      <div className="spec-builder">
        <Tabs.Root className="spec-main" value={view} onValueChange={(v) => switchView(v as View)}>
          <div className="spec-tabs" ref={tabsRef}>
            <Tabs.List className="segmented" aria-label={application ? 'Application configuration' : 'Instance configuration'}>
              <Tabs.Trigger value="capabilities">Add capabilities</Tabs.Trigger>
              <Tabs.Trigger value="settings">
                Settings{errors.length > 0 && <span className="tab-badge" aria-label={`${errors.length} issues`}>{errors.length}</span>}
              </Tabs.Trigger>
            </Tabs.List>
            <button type="button" className="text-btn underline" onClick={toggleAdvanced}>Advanced JSON</button>
          </div>

          {/* forceMount keeps both views mounted so editor state survives switching */}
          <Tabs.Content value="capabilities" forceMount className="spec-palette">
            <p className="spec-pane-note">Optional. Add capabilities and policies here; adjust capability details under Settings. {application ? 'Applied when published.' : 'Settings are fixed after creation.'}</p>
            <div className="ability-choices">
              <button type="button" className="ability-choice" data-selected={!!draft.fs} aria-label={draft.fs ? 'Files added' : 'Add Files'} onClick={addFiles}>
                <FilesIcon />
                <span className="min-w-0 flex-1"><strong>Files</strong><small>bounded virtual storage</small></span>
                <span className="ability-add">{draft.fs ? <><CheckIcon /> Added</> : 'Add'}</span>
              </button>
              <button type="button" className="ability-choice" data-selected={!!draft.net} aria-label={draft.net ? 'Network added' : 'Add Network'} onClick={addNetwork}>
                <NetworkIcon />
                <span className="min-w-0 flex-1"><strong>Network</strong><small>outbound HTTP</small></span>
                <span className="ability-add">{draft.net ? <><CheckIcon /> Added</> : 'Add'}</span>
              </button>
            </div>
            <div className="extensions-block">
              <h4 className="mb-3 text-sm font-semibold text-ink">Extensions</h4>
              <ExtensionPicker entries={extensions} selectedSources={selectedSources} loading={catalog.isPending} error={catalog.isError}
                onRetry={() => catalog.refetch()} onAdd={addExtension} onShow={pointAtExtension} onCustom={() => addExtension({ name: '', source: '' })} />
            </div>
            <div className="policies-block">
              <h4 className="mb-3 text-sm font-semibold text-ink">Policies</h4>
              <p className="spec-pane-note">Pluggable rules that constrain what capabilities may do. Add any number; they never grant access.</p>
              <PolicyPicker entries={policies.data?.policies ?? []} selected={selectedPolicies}
                loading={policies.isPending} error={policies.isError} onRetry={() => policies.refetch()}
                onToggle={togglePolicy} onRemove={removePolicy}
                showUseDefaults={hasDefaults && draft.policies !== null} onUseDefaults={() => set({ policies: null })} />
            </div>
          </Tabs.Content>

          <Tabs.Content value="settings" forceMount className="spec-settings" ref={settingsRef}>
            <section className="settings-group" data-section="capabilities">
              <h3 className="spec-pane-title">Capabilities</h3>
              {items.length === 0 && (
                <p className="text-sm text-sec">Compute only — nothing to configure. <button type="button" className="link" onClick={() => switchView('capabilities')}>Add capabilities</button></p>
              )}
              {items.map((item) => (
                <div key={item.key} data-section={item.key} className="settings-card">
                  <div className="config-item-head">
                    <span className="config-item-icon">{item.icon}</span>
                    <div className="min-w-0 flex-1">
                      <h4 className="config-item-title">{item.title}</h4>
                      <p className="config-item-summary">{item.summary}</p>
                    </div>
                  </div>
                  {item.missing.length > 0 && (
                    <p className="settings-note is-warn">Needs {item.missing.map(capabilityLabel).join(', ')}.{' '}
                      {item.missing.filter((d) => d === 'net' || d === 'fs').map((d) => (
                        <button key={d} type="button" className="underline" onClick={() => d === 'net' ? addNetwork() : addFiles()}>Add {capabilityLabel(d)}</button>
                      ))}
                    </p>
                  )}
                  <div className="config-item-body">{item.body}</div>
                </div>
              ))}
              {Object.keys(draft.otherCaps).map((name) => <div key={name} className="settings-card"><h4 className="config-item-title">{name}</h4><p className="config-item-summary">Existing configuration preserved. Use Advanced JSON to edit.</p></div>)}
            </section>
            <section className="settings-group" data-section="limits">
              <h3 className="spec-pane-title">Limits <span className="spec-pane-meta">{limitsSummary}</span></h3>
              <LimitsSection draft={draft} set={set} />
            </section>
            <section className="settings-group" data-section="inputs">
              <h3 className="spec-pane-title">Inputs <span className="spec-pane-meta">{inputsSummary}</span></h3>
              <InputsSection draft={draft} set={set} keyMode={refsOnly} application={application} vault={vault.data?.secrets ?? []} vaultError={vault.isError} onRetryVault={() => vault.refetch()} listId={`vault-names-${ws}`} />
            </section>
          </Tabs.Content>
        </Tabs.Root>

        <aside className="spec-selection" aria-label={application ? 'Application settings' : 'This instance'}>
          <h3 className="spec-pane-title">{application ? 'This application' : 'This instance'}</h3>
          <div className="spec-identity">
            {application ? identity : (
              <>
                <Field label="Name"><input className={inputCls} value={draft.name} placeholder="e.g. research run" onChange={(e) => set({ name: e.target.value })} /></Field>
                <Field label="Lifetime (seconds)"><input className={inputCls} type="number" min="1" value={draft.ttlSeconds} onChange={(e) => set({ ttlSeconds: e.target.value === '' ? '' : Number(e.target.value) })} /></Field>
              </>
            )}
          </div>
          <div>
            <p className="sidebar-label">Capabilities</p>
            <div className="selected-list">
              {rows.length === 0 && (
                <div className="selected-row is-empty">
                  <div className="min-w-0 flex-1">
                    <h4 className="config-item-title">Compute only</h4>
                    <span className="selected-status">Starlark and the standard library</span>
                  </div>
                </div>
              )}
              {rows}
            </div>
          </div>
          <div>
            <p className="sidebar-label">Policies</p>
            <div className="selected-list">
              {selectedPolicies.length === 0 && (
                <div className="selected-row is-empty">
                  <div className="min-w-0 flex-1">
                    <h4 className="config-item-title">No policies</h4>
                    <span className="selected-status">Add one to restrict capabilities</span>
                  </div>
                </div>
              )}
              {selectedPolicies.map((name) => (
                <div key={name} data-row={`policy:${name}`} className="selected-row">
                  <div className="min-w-0 flex-1">
                    <p className="config-item-title">{name}</p>
                    <span className="selected-status">{policyKind(name) === 'builtin' ? 'Built-in' : policyKind(name) === 'rego' ? 'Workspace policy' : 'Unknown'}</span>
                  </div>
                  <button type="button" className="text-btn" aria-label={`Remove policy ${name}`} onClick={() => removePolicy(name)}>Remove</button>
                </div>
              ))}
            </div>
          </div>
          <div className="sidebar-links">
            <button type="button" onClick={() => openSettings('limits')}>
              Limits <span className={sectionIssues(['Limits']) ? 'is-warn' : ''}>{limitsSummary}</span>
            </button>
            <button type="button" onClick={() => openSettings('inputs')}>
              Inputs <span className={sectionIssues(['Variables', 'Secrets', 'Labels']) ? 'is-warn' : ''}>{inputsSummary}</span>
            </button>
          </div>
          {alert}
          {footer}
        </aside>
      </div>
    </div>
  )
})
