import { switchExtSource } from '../instanceDraft.ts'
import { findExtension } from '../builderCatalog.ts' 
import { forwardRef, useEffect, useImperativeHandle, useMemo, useRef, useState, type ReactNode } from 'react'
import { useQuery } from '@tanstack/react-query'
import * as Tabs from '@radix-ui/react-tabs'
import { api, currentWorkspace, type ExtensionCatalog, type ExtensionInfo, type Policy, type Secret } from '../api'
import {
  buildInstanceSpec, draftFromSpec, uniqueExtensionAlias, validateRawSpec,
  type BuildResult, type DraftError, type ExtDraft, type InstanceDraft, type InstanceSpec, type JsonValue,
} from '../instanceDraft'
import { fmtBytes } from '../format'
import { Confirm, Field, inputCls } from './ui'
import { InstanceExtensionEditor } from './InstanceExtensionEditor'
import { setBindingField, removeBindingField } from '../environment'
import { ExtensionPicker } from './ExtensionPicker'
import { ModulePicker, type ModuleEntry } from './ModulePicker'
import { PolicyPicker } from './PolicyPicker'
import {
  addMount, canAddModule, mountName, parseModuleMounts, removeMount, uniqueMountAlias,
  type ModuleMount,
} from '../moduleMounts'
import { ExtensionIcon } from './ExtensionIcon'
import { InputsSection, LimitsSection } from './SpecSections'

export interface SpecFormHandle {
  buildSpec(): { spec?: InstanceSpec; specJSON?: string; err?: string }
  applyBasics(basics: { name?: string; description?: string; labels?: { k: string; v: string }[] }): void
}

interface Props {
  keyMode?: boolean
  application?: boolean
  initial?: InstanceSpec
  workspaceId?: string
  /** module-debug variant: refs-only like application, own labels */
  debug?: boolean
  onSpecChange?: (draft: InstanceDraft, errors?: DraftError[], built?: { spec?: InstanceSpec; specJSON?: string }) => void
  onRawDirtyChange?: (dirty: boolean) => void
  footer?: ReactNode
  /** Basics are already collected by the create flow; only keep lifetime. */
  hideBasics?: boolean
  /** Module mode: static deps fixed by the dependency picker — merged
      into the debug draft with config preserved; not addable/removable here. */
  fixedExtensions?: Record<string, { source: string; sum?: string }>
  /** Module mode: catalog Add — the parent pins the dep and pushes a new
      fixedExtensions; there is no transient local add. */
  onAddFixedExtension?: (entry: ExtensionInfo) => void
  onSwitchFixedExtension?: (from: ExtensionInfo | undefined, to: ExtensionInfo) => void
  /** Module mode: summary Remove — routed to the parent's named Confirm
      rather than the local remove/undo path (F2). */
  onRemoveFixedExtension?: (alias: string) => void
  /** Module mode: disables dependency add/remove while leaving the rest
      of the runtime configuration editable (e.g. source save conflict). */
  fixedDepsDisabled?: boolean
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
  keyMode = false, application = false, debug = false, initial, workspaceId, onSpecChange, onRawDirtyChange, footer, hideBasics = false, fixedExtensions,
  onAddFixedExtension, onSwitchFixedExtension, onRemoveFixedExtension, fixedDepsDisabled = false,
}, ref) {
  // Application mode: vault-ref secrets only (same ceiling as key
  // specs) — never inline values in a durable spec.
  const refsOnly = keyMode || application || debug
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
  // Module mounts (application mode): named Confirm, then a same-slot
  // placeholder like capability removals (S3/F1/F2).
  const [modRemove, setModRemove] = useState<ModuleMount | null>(null)
  const [modRemoved, setModRemoved] = useState<{ at: number; title: string; alias: string; ref: Record<string, JsonValue> } | null>(null)
  // Module mode: reconcile the debug draft against the statically
  // declared deps — same alias+source keeps config/env, a changed
  // source resets them, removed aliases prune env/errors.
  const fixedPrev = useRef<ReadonlySet<string>>(new Set())
  useEffect(() => {
    if (!fixedExtensions) return
    setDraft((d) => {
      const fixed = fixedExtensions
      // index-stable ids: extIds.current[i] pairs d.exts[i]
      let changed = false
      const keepIdx: number[] = []
      const exts = d.exts.filter((e, i) => {
        if (fixedPrev.current.has(e.alias) && !(e.alias in fixed)) { changed = true; return false }
        keepIdx.push(i)
        return true
      })
      const ids = keepIdx.map((i) => extIds.current[i])
      const extra = { ...(d.extra ?? {}) }
      const env = { ...((extra.environment as { extensions?: Record<string, unknown> } | undefined)?.extensions ?? {}) }
      let envChanged = false
      for (const [alias, dep] of Object.entries(fixed)) {
        const i = exts.findIndex((e) => e.alias === alias)
        if (i >= 0) {
          if (exts[i].source !== dep.source) {
            exts[i] = { alias, source: dep.source, config: {}, extra: dep.sum ? { sum: dep.sum } : {} }
            ids[i] = extNext.current++
            delete env[alias]; envChanged = true
            changed = true
          } else if (dep.sum && exts[i].extra?.sum !== dep.sum) {
            exts[i] = { ...exts[i], extra: { ...(exts[i].extra ?? {}), sum: dep.sum } }
            changed = true
          }
        } else {
          exts.push({ alias, source: dep.source, config: {}, extra: dep.sum ? { sum: dep.sum } : {} })
          ids.push(extNext.current++)
          changed = true
        }
      }
      extIds.current = ids
      if (changed) {
        // clone env map — never mutate the old draft's snapshot
        for (const a of fixedPrev.current) if (!(a in fixed)) { delete env[a]; envChanged = true }
        setOrder((o) => {
          const keys = ids.map((i) => `ext:${i}`)
          return [...o.filter((k) => !k.startsWith('ext:') || keys.includes(k)),
                  ...keys.filter((k) => !o.includes(k))]
        })
        setExtErrors((errs) => {
          const next = new Map(errs)
          next.forEach((_, k) => { if (!ids.includes(k)) next.delete(k) })
          return next
        })
      }
      fixedPrev.current = new Set(Object.keys(fixed))
      if (!changed && !envChanged) return d
      if (envChanged) extra.environment = { ...((extra.environment as Record<string, unknown>) ?? {}), extensions: env } as never
      return { ...d, exts, extra }
    })
  }, [fixedExtensions])
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
  // Application mode: workspace module library for spec.modules mounts.
  const modulesQ = useQuery({
    queryKey: ['modules', ws],
    queryFn: () => api.get<{ modules: ModuleEntry[] }>(scope('/api/v1/modules')),
    retry: false,
    enabled: application,
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
    // applyBasics pushes the create flow's basics step into the mounted
    // draft without remounting it, so detailed choices survive Back (C2).
    applyBasics({ name, description, labels }) {
      setDraft((d) => ({
        ...d,
        ...(name !== undefined ? { name } : {}),
        ...(description !== undefined ? { description } : {}),
        ...(labels !== undefined ? { labels } : {}),
      }))
      if (!advanced) return
      // Advanced JSON is the submitted text. An explicit basics edit is
      // the one case where the text itself must follow (P3/F3).
      setJSON((text) => {
        let raw: any
        try { raw = JSON.parse(text) } catch { return text }
        if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return text
        const next: Record<string, unknown> = {}
        if (labels === undefined) {
          const prev = raw.labels && typeof raw.labels === 'object' && !Array.isArray(raw.labels) ? raw.labels : {}
          for (const [k, v] of Object.entries(prev)) next[k] = v
        } else {
          for (const r of labels) { const k = r.k.trim(); if (k) next[k] = r.v ?? '' }
        }
        for (const [key, value] of [['name', name], ['description', description]] as const) {
          if (value === undefined) continue
          if (value.trim()) next[key] = value
          else delete next[key]
        }
        if (Object.keys(next).length) raw.labels = next
        else delete raw.labels
        return JSON.stringify(raw, null, 2)
      })
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
  const extFamily = (src: string) => src.split('@')[0]
  const addExtension = (entry: { name: string; source: string; sum?: string; family?: string }) => {
    // One version per family through the picker: a second add switches
    // (or simply shows) the existing row instead of creating an alias.
    const fam = entry.family ?? extFamily(entry.source)
    const existing = draft.exts.findIndex((e) => e.source === fam || extFamily(e.source) === fam)
    if (entry.source && existing >= 0) { switchExtension({ ...entry, family: fam } as ExtensionInfo, entry as ExtensionInfo); return }
    const id = extNext.current++
    extIds.current = [...extIds.current, id]
    const alias = entry.source ? uniqueExtensionAlias(entry.name, new Set(draft.exts.map((e) => e.alias))) : ''
    set({ exts: [...draft.exts, { alias, source: entry.source, sum: entry.sum, config: {}, extra: {} }] })
    append(`ext:${id}`)
    // A custom extension has nothing usable until it has a source.
    if (!entry.source) openSettings(`ext:${id}`)
  }
  // switchExtension swaps source+sum on the existing family row, keeping
  // alias, config and extras; nothing is remapped or dropped silently.
  const switchExtension = (from: ExtensionInfo | undefined, to: ExtensionInfo) => {
    const index = draft.exts.findIndex((e) =>
      e.source === from?.source || e.source === from?.family ||
      (from === undefined && e.source === to.family) ||
      (e.source !== to.source && extFamily(e.source) === to.family))
    const exts = index >= 0 ? switchExtSource(draft.exts, from, to) : undefined
    if (index < 0 || !exts) return addExtension(to)
    // Silent in-place switch: no flash, no panel, no focus move (C).
    set({ exts })
  }
  const pointAtExtension = (entry: ExtensionInfo) => {
    const index = draft.exts.findIndex((e) => e.source === entry.source || e.source === entry.family)
    if (index >= 0) setFlash(`ext:${extIds.current[index]}`)
  }

  // Application module mounts (spec.modules). The raw map stays
  // authoritative: malformed values are never rewritten by the visual
  // editor — the user fixes them in Advanced JSON (C2/F3).
  const modParsed = application ? parseModuleMounts(draft.extra?.modules) : { ok: true as const, mounts: [] as ModuleMount[] }
  const modMounts = modParsed.ok ? modParsed.mounts : []
  const modNames = new Set(modMounts.map((x) => mountName(x)))
  const addModule = (entry: ModuleEntry) => {
    if (!canAddModule(modParsed, entry)) return
    const alias = uniqueMountAlias(entry.name, new Set(modMounts.map((x) => x.alias)))
    const ref: Record<string, JsonValue> = { name: entry.name }
    if (entry.latest_version) ref.version = entry.latest_version
    if (entry.latest_sum) ref.sum = entry.latest_sum
    setDraft((d) => ({ ...d, extra: { ...d.extra, modules: addMount(d.extra?.modules, alias, ref) } }))
    setModRemoved(null)
    setFlash(`mod:${alias}`)
  }
  const pointAtModule = (entry: ModuleEntry) => {
    const mount = modMounts.find((x) => mountName(x) === entry.name)
    if (mount) setFlash(`mod:${mount.alias}`)
  }
  const doRemoveModule = (mount: ModuleMount) => {
    const at = modMounts.findIndex((x) => x.alias === mount.alias)
    setModRemoved({ at, title: mountName(mount), alias: mount.alias, ref: mount.ref })
    setDraft((d) => {
      const extra = { ...d.extra }
      const next = removeMount(extra.modules, mount.alias)
      if (next === undefined) delete extra.modules
      else extra.modules = next
      return { ...d, extra }
    })
  }
  const undoModule = () => {
    if (!modRemoved) return
    const { alias, ref } = modRemoved
    setDraft((d) => ({ ...d, extra: { ...d.extra, modules: addMount(d.extra?.modules, alias, ref) } }))
    setModRemoved(null)
  }
  const setExtError = (id: number, message: string) =>
    setExtErrors((m) => { const n = new Map(m); if (message) n.set(id, message); else n.delete(id); return n })

  // Removing leaves an undo row of the same height in the same slot, so
  // the next row does not slide under the pointer.
  const remove = (key: ItemKey, title: string) => {
    // Module mode: fixed deps route to the parent's named Confirm before
    // ANY local state changes — cancelling the dialog must not drop or
    // reorder the row (C2/C3/F2).
    if (fixedExtensions !== undefined && key.startsWith('ext:')) {
      const index = extIds.current.indexOf(Number(key.slice(4)))
      if (index >= 0 && draft.exts[index].alias in fixedExtensions) {
        if (!fixedDepsDisabled) onRemoveFixedExtension?.(draft.exts[index].alias)
        return
      }
    }
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
    const entry = findExtension(extensions, ext.source)
    const extVersion = entry?.version ?? ext.source.split('@')[1]
    const own = (m: string) => ext.alias ? m.startsWith(`${ext.alias}:`) || m === `duplicate alias ${ext.alias}` : m === 'extension alias is required'
    return {
      key, title: ext.alias || 'extension',
      icon: <ExtensionIcon iconUrl={entry?.icon_url} name={entry?.name ?? ext.alias} />,
      summary: `${entry?.description || ext.source || 'Choose a source and an alias.'}${extVersion ? ` · ${extVersion}` : ''}`,
      brief: Object.keys(ext.config).length ? 'Configured' : 'Default settings',
      missing: (entry?.dependencies ?? []).filter((d) => !has(d)),
      issues: [...errors.filter((e) => e.section === 'Extensions' && own(e.message) && !e.message.includes('— add it explicitly')).map((e) => e.message), ...(extErrors.has(id) ? [extErrors.get(id)!] : [])],
      body: <InstanceExtensionEditor value={ext} catalog={extensions}
        secretNames={draft.secrets.map((s) => s.name).filter(Boolean)}
        legacySecretRefs={Object.fromEntries(draft.secrets.filter((x) => x.name && x.kind === 'vault').map((x) => [x.name, x.ref || x.name]))}
        vaultNames={(vault.data?.secrets ?? []).map((s) => s.name)}
        onUseVault={(name) => set({ secrets: draft.secrets.some((s) => s.name === name) ? draft.secrets : [...draft.secrets, { name, kind: 'vault', ref: name, value: '', domains: [], extra: {} }] })}
        secretBindings={(((draft.extra?.environment as { extensions?: Record<string, { bindings?: Record<string, { secrets?: Record<string, { ref: string; allowed_domains?: string[] }> }> }> } | undefined)?.extensions?.[ext.alias]?.bindings?.default?.secrets) ?? {})}
        onSecretBindingChange={(field, sf) => setDraft((d) => {
          const env = (d.extra?.environment ?? {}) as never
          const nextEnv = sf === undefined
            ? removeBindingField(env, ext.alias, 'default', field)
            : setBindingField(env, ext.alias, 'default', field, sf)
          // A new env selection removes the legacy config placeholder
          // for that field only — siblings and secondary bindings stay.
          const exts = d.exts.map((e, i) => {
            if (i !== index || sf === undefined) return e
            const cfg = { ...e.config }
            delete cfg[field]
            return { ...e, config: cfg }
          })
          return { ...d, exts, extra: { ...d.extra, environment: nextEnv } }
        })}
        onValidityChange={(m) => setExtError(id, m)} onChange={(value) => set({ exts: draft.exts.map((e, i) => i === index ? value : e) })} />,
    }
  }
  const items = keys.map(describe).filter((x): x is Item => x !== null)
  // Module mode: a fixed dep's Remove routes to the parent's Confirm —
  // and is disabled while dependency mutations are locked.
  const isFixedRow = (key: ItemKey) => {
    if (!key.startsWith('ext:') || fixedExtensions === undefined) return false
    const ext = draft.exts[extIds.current.indexOf(Number(key.slice(4)))]
    return !!ext && ext.alias in fixedExtensions
  }

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
      <button type="button" className="text-btn" aria-label={`Remove ${item.title}`} disabled={isFixedRow(item.key) && fixedDepsDisabled} onClick={() => remove(item.key, item.title)}>Remove</button>
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
          <div className="mb-3 flex flex-wrap items-center justify-end gap-3">
            <button type="button" className="text-btn underline" onClick={toggleAdvanced}>Back to editor</button>
          </div>
          <textarea aria-label={application ? 'Application spec JSON' : debug ? 'Debug spec JSON' : 'Instance spec JSON'} className={inputCls + ' min-h-[420px] font-mono text-xs'} value={json} spellCheck={false} onChange={(e) => setJSON(e.target.value)} />
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
            <Tabs.List className="segmented" aria-label={application ? 'Application configuration' : debug ? 'Debug configuration' : 'Instance configuration'}>
              <Tabs.Trigger value="capabilities">Add capabilities</Tabs.Trigger>
              <Tabs.Trigger value="settings">
                Settings{errors.length > 0 && <span className="tab-badge" aria-label={`${errors.length} issues`}>{errors.length}</span>}
              </Tabs.Trigger>
            </Tabs.List>
            <button type="button" className="text-btn underline" onClick={toggleAdvanced}>Advanced JSON</button>
          </div>

          {/* forceMount keeps both views mounted so editor state survives switching */}
          <Tabs.Content value="capabilities" forceMount className="spec-palette">
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
              <fieldset className="m-0 min-w-0 border-0 p-0" disabled={fixedExtensions !== undefined && fixedDepsDisabled}>
                <ExtensionPicker entries={extensions} selectedSources={selectedSources} loading={catalog.isPending} error={catalog.isError}
                  onRetry={() => catalog.refetch()}
                  onAdd={fixedExtensions !== undefined ? (entry) => onAddFixedExtension?.(entry) : addExtension}
                  onShow={pointAtExtension}
                  onSwitch={fixedExtensions !== undefined ? (f, t) => onSwitchFixedExtension?.(f, t) : switchExtension} />
              </fieldset>
            </div>
            {application && (
              <div className="modules-block">
                <h4 className="mb-3 text-sm font-semibold text-ink">Modules</h4>
                {!modParsed.ok ? (
                  <p className="spec-pane-note">The spec declares modules this editor can't display — edit them in Advanced JSON.</p>
                ) : (
                  <ModulePicker entries={modulesQ.data?.modules ?? []} selectedNames={modNames}
                    loading={modulesQ.isPending} error={modulesQ.isError} onRetry={() => modulesQ.refetch()}
                    onAdd={addModule} onShow={pointAtModule} canAdd={(e) => canAddModule(modParsed, e)} />
                )}
              </div>
            )}
            <div className="policies-block">
              <h4 className="mb-3 text-sm font-semibold text-ink">Policies</h4>
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
              {Object.keys(draft.otherCaps).map((name) => <div key={name} className="settings-card"><h4 className="config-item-title">{name}</h4><p className="config-item-summary">Edit in Advanced JSON.</p></div>)}
            </section>
            <section className="settings-group" data-section="limits">
              <h3 className="spec-pane-title">Limits <span className="spec-pane-meta">{limitsSummary}</span></h3>
              <LimitsSection draft={draft} set={set} />
            </section>
            <section className="settings-group" data-section="inputs">
              <h3 className="spec-pane-title">Inputs <span className="spec-pane-meta">{inputsSummary}</span></h3>
              <InputsSection draft={draft} set={set} keyMode={refsOnly} hideLabels={hideBasics} vault={vault.data?.secrets ?? []} vaultError={vault.isError} onRetryVault={() => vault.refetch()} listId={`vault-names-${ws}`} />
            </section>
          </Tabs.Content>
        </Tabs.Root>

        <aside className="spec-selection" aria-label={application ? 'Application settings' : debug ? 'Debug environment' : 'This instance'}>
          <h3 className="spec-pane-title">{application ? 'This application' : debug ? 'Debug environment' : 'This instance'}</h3>
          {!application && (
            hideBasics ? (
              <Field label="Lifetime (seconds)"><input className={inputCls} type="number" min="1" value={draft.ttlSeconds} onChange={(e) => set({ ttlSeconds: e.target.value === '' ? '' : Number(e.target.value) })} /></Field>
            ) : (
              <div className="spec-identity">
                <Field label="Name"><input className={inputCls} value={draft.name} placeholder="e.g. research run" onChange={(e) => set({ name: e.target.value })} /></Field>
                <Field label="Lifetime (seconds)"><input className={inputCls} type="number" min="1" value={draft.ttlSeconds} onChange={(e) => set({ ttlSeconds: e.target.value === '' ? '' : Number(e.target.value) })} /></Field>
              </div>
            )
          )}
          <div>
            <p className="sidebar-label">Capabilities</p>
            <div className="selected-list">
              {rows.length === 0 && (
                <div className="selected-row is-empty">
                  <div className="min-w-0 flex-1">
                    <h4 className="config-item-title">Compute only</h4>
                  </div>
                </div>
              )}
              {rows}
            </div>
          </div>
          {application && (
            <div>
              <p className="sidebar-label">Modules</p>
              <div className="selected-list">
                {!modParsed.ok && (
                  <div className="selected-row is-empty">
                    <div className="min-w-0 flex-1">
                      <h4 className="config-item-title">Modules declared</h4>
                      <span className="selected-status">Edit them in Advanced JSON</span>
                    </div>
                  </div>
                )}
                {modParsed.ok && modMounts.length === 0 && !modRemoved && (
                  <div className="selected-row is-empty">
                    <div className="min-w-0 flex-1">
                      <h4 className="config-item-title">No modules</h4>
                    </div>
                  </div>
                )}
                {(() => {
                  const modRows: ReactNode[] = modMounts.map((mount) => {
                    const name = mountName(mount)
                    const v = mount.ref.version
                    const sum = mount.ref.sum
                    return (
                      <div key={mount.alias} data-row={`mod:${mount.alias}`} className={'selected-row' + (flash === `mod:${mount.alias}` ? ' is-flash' : '')}>
                        <div className="min-w-0 flex-1">
                          <p className="config-item-title">{name}</p>
                          <span className="selected-status">
                            @{mount.alias}
                            {typeof v === 'number' && v > 0 ? ` · v${v}` : ''}
                            {typeof sum === 'string' && sum ? ` · ${sum.slice(0, 20)}…` : ''}
                          </span>
                          <code className="block truncate text-xs text-mute">{`load("@${mount.alias}/<file>.star", "<symbol>")`}</code>
                        </div>
                        <button type="button" className="text-btn" aria-label={`Remove module ${name}`} onClick={() => setModRemove(mount)}>Remove</button>
                      </div>
                    )
                  })
                  if (modRemoved) {
                    modRows.splice(Math.min(modRemoved.at, modRows.length), 0, (
                      <div key="mod-removed" className="selected-row is-removed" role="status">
                        <span>{modRemoved.title} removed</span>
                        <button type="button" className="text-btn" onClick={undoModule}>Undo</button>
                      </div>
                    ))
                  }
                  return modRows
                })()}
              </div>
            </div>
          )}
          <div>
            <p className="sidebar-label">Policies</p>
            <div className="selected-list">
              {selectedPolicies.length === 0 && (
                <div className="selected-row is-empty">
                  <div className="min-w-0 flex-1">
                    <h4 className="config-item-title">No policies</h4>
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
        {modRemove && (
          <Confirm title={`Remove module ${mountName(modRemove)}?`} tone="danger" confirm="Remove" pending="Removing…"
            action={async () => { doRemoveModule(modRemove); setModRemove(null) }}
            onClose={() => setModRemove(null)}>
            Remove the {mountName(modRemove)} module mounted as @{modRemove.alias}?
          </Confirm>
        )}
      </div>
    </div>
  )
})
