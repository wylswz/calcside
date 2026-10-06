import { useMemo, useState, type ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, Instance, Policy, Secret } from '../api'
import type { SecretSource, ExtensionCatalog, ExtensionInfo, ExtConfigField } from '../api'
import { Button, EmptyRow, Field, Loading, Mark, Modal, PageHeader, SectionTitle, StatusBadge, Tag, fmtCountdown, fmtTime, inputCls } from '../components/ui'

interface SpecDraft {
  ttl_seconds: number
  labels: Record<string, string>
  capabilities: Record<string, any>
  limits: { exec_timeout_ms?: number; max_steps?: number; max_output_bytes?: number }
  env?: Record<string, string>
  secrets?: Record<string, any>
  policies?: string[]
}

interface EnvRow { k: string; v: string }
interface SecretRow {
  name: string
  kind: SecretSource
  value: string      // inline only
  domains: string    // comma/space separated; optional for vault (narrows)
  auto?: boolean     // added via an ext secret config field
}
interface ExtRow {
  source: string                  // catalog entry source or custom text
  custom: boolean                 // free-form source/sum inputs
  sum: string                     // remote h1: sum (custom only)
  alias: string
  config: Record<string, string>  // only keys the user set; '' = default
}

const capBar = { blue: 'border-accent', red: 'border-danger', yellow: 'border-warn' }
const capShape = { blue: 'circle', red: 'square', yellow: 'triangle' } as const

const aliasRe = /^[A-Za-z_][A-Za-z0-9_]*$/
const secretRefRe = /\{\{\s*secrets\.([A-Z_][A-Z0-9_]{0,63})\s*\}\}/

// extCfgValue converts a config form string to its JSON value per the
// manifest field type.
function extCfgValue(type: string, v: string): any {
  switch (type) {
    case 'int':
      return Number(v)
    case 'bool':
      return v === 'true'
    case 'string_list':
      return v.split('\n').map((s) => s.trim()).filter(Boolean)
    case 'string_map': {
      const m: Record<string, string> = {}
      for (const line of v.split('\n')) {
        const i = line.indexOf('=')
        if (i > 0) m[line.slice(0, i).trim()] = line.slice(i + 1).trim()
      }
      return m
    }
    default:
      return v
  }
}

// ExtRowEditor edits one extension alias row: catalog pick or custom
// source, alias, per-manifest config form, ops + dependency hints.
function ExtRowEditor({ row, catalog, specNames, vaultNames, netOn, fsOn, onChange, onEnsureSecret, onRemove }: {
  row: ExtRow
  catalog: ExtensionInfo[]
  specNames: string[]   // names already present in the spec's secrets
  vaultNames: string[]  // names in the user's vault
  netOn: boolean
  fsOn: boolean
  onChange: (r: ExtRow) => void
  onEnsureSecret: (prevName: string, s: SecretRow) => void
  onRemove: () => void
}) {
  const set = (patch: Partial<ExtRow>) => onChange({ ...row, ...patch })
  const entry = catalog.find((x) => x.source === row.source)
  const setCfg = (k: string, v: string) => set({ config: { ...row.config, [k]: v } })
  return (
    <div className="panel space-y-1.5 p-2.5">
      <div className="flex items-center gap-1.5">
        <span className="relative flex h-8 w-8 shrink-0 items-center justify-center rounded bg-surface font-mono text-accent" aria-hidden="true">
          {(entry?.name || row.alias || '?').slice(0, 1).toUpperCase()}
          {entry?.icon_url?.startsWith('/api/v1/extensions/icons/') && <img key={entry.icon_url} src={entry.icon_url} alt="" className="absolute inset-0 h-8 w-8 rounded" onError={(e) => { e.currentTarget.style.display = 'none' }} />}
        </span>
        <select className={inputCls + ' font-mono text-xs'} value={row.custom ? '__custom__' : row.source}
          onChange={(e) => {
            if (e.target.value === '__custom__') {
              set({ custom: true, source: '', alias: row.alias, config: {} })
            } else {
              const en = catalog.find((x) => x.source === e.target.value)
              set({ custom: false, source: e.target.value, alias: row.alias || en?.name || '', config: {} })
            }
          }}>
          <option value="">pick…</option>
          {catalog.map((x) => (
            <option key={x.source} value={x.source}>{x.name} {x.version} — {x.description}</option>
          ))}
          <option value="__custom__">custom…</option>
        </select>
        <input className={inputCls + ' !w-28 font-mono text-xs'} placeholder="alias" value={row.alias}
          onChange={(e) => set({ alias: e.target.value })} />
        <button className="ml-auto px-1 text-base leading-none text-mute transition-colors hover:text-danger" aria-label="Remove" onClick={onRemove}>×</button>
      </div>
      {row.alias && !aliasRe.test(row.alias) && (
        <p className="text-xs text-danger">alias must be a starlark identifier</p>
      )}
      {row.custom && (
        <>
          <input className={inputCls + ' font-mono text-xs'} placeholder="source (contrib/tavily or domain/group/name@version)" value={row.source}
            onChange={(e) => set({ source: e.target.value })} />
          <input className={inputCls + ' font-mono text-xs'} placeholder="h1:… (required for remote)" value={row.sum}
            onChange={(e) => set({ sum: e.target.value })} />
        </>
      )}
      {entry?.description && <p className="text-xs text-sec">{entry.description}</p>}
      {entry && (entry.ops ?? []).length > 0 && (
        <p className="font-mono text-xs text-sec">
          {(entry.ops ?? []).map((o) => `ext.${row.alias || '?'}.${o.name}(${(o.params ?? []).join(', ')})`).join('  ')}
        </p>
      )}
      {entry && (entry.dependencies ?? []).map((d) => {
        const granted = (d === 'net' && netOn) || (d === 'fs' && fsOn) || d === 'io'
        return granted ? null : (
          <p key={d} className="text-xs text-warn-text">requires {d} — enable it below/above</p>
        )
      })}
      {entry && (entry.config ?? []).map((f) => (
        <ExtCfgInput key={f.name} f={f} specNames={specNames} vaultNames={vaultNames}
          value={row.config[f.name] ?? ''} onChange={(v) => setCfg(f.name, v)} onEnsureSecret={onEnsureSecret} />
      ))}
      {entry && (entry.config ?? []).map((f) => {
        if (f.type !== 'secret') return null
        const eff = row.config[f.name] || String(f.default ?? '')
        const need = eff.match(secretRefRe)?.[1]
        if (!need || specNames.includes(need)) return null
        if (vaultNames.includes(need)) {
          return <p key={'s' + f.name} className="text-xs text-sec">vault secret {need} will be added to the instance</p>
        }
        return <p key={'s' + f.name} className="text-xs text-warn-text">secret {need} not defined — pick it above or create it inline</p>
      })}
    </div>
  )
}

// ExtCfgInput renders one manifest config field.
function ExtCfgInput({ f, value, specNames, vaultNames, onChange, onEnsureSecret }: {
  f: ExtConfigField
  value: string
  specNames: string[]
  vaultNames: string[]
  onChange: (v: string) => void
  onEnsureSecret: (prevName: string, s: SecretRow) => void
}) {
  const ph = f.default != null ? `default: ${typeof f.default === 'object' ? JSON.stringify(f.default) : f.default}` : ''
  const label = (
    <span className="mb-1 block text-xs text-sec" title={f.doc}>{f.name}{f.doc ? ' — ' + f.doc : ''}</span>
  )
  switch (f.type) {
    case 'int':
      return <div>{label}<input type="number" className={inputCls + ' text-xs'} placeholder={ph} value={value} onChange={(e) => onChange(e.target.value)} /></div>
    case 'bool':
      return (
        <label className="flex items-center gap-2 text-xs">
          <input type="checkbox" checked={value === 'true'} onChange={(e) => onChange(e.target.checked ? 'true' : 'false')} />
          {label}
        </label>
      )
    case 'string_list':
      return <div>{label}<textarea className={inputCls + ' font-mono text-xs'} rows={2} placeholder={ph + ' (one per line)'} value={value} onChange={(e) => onChange(e.target.value)} /></div>
    case 'string_map':
      return <div>{label}<textarea className={inputCls + ' font-mono text-xs'} rows={2} placeholder={ph + ' (k=v per line)'} value={value} onChange={(e) => onChange(e.target.value)} /></div>
    case 'secret':
      return <SecretCfgInput f={f} label={label} value={value} specNames={specNames} vaultNames={vaultNames} onChange={onChange} onEnsureSecret={onEnsureSecret} />
    default:
      return <div>{label}<input className={inputCls + ' font-mono text-xs'} placeholder={ph} value={value} onChange={(e) => onChange(e.target.value)} /></div>
  }
}

const secretNameRe = /^[A-Z_][A-Z0-9_]{0,63}$/

// SecretCfgInput edits a `type: secret` config field: pick any vault or
// already-defined instance secret, or create a new inline secret on the
// spot (upserted into the spec's secrets via onEnsureSecret).
function SecretCfgInput({ f, label, value, specNames, vaultNames, onChange, onEnsureSecret }: {
  f: ExtConfigField
  label: ReactNode
  value: string
  specNames: string[]
  vaultNames: string[]
  onChange: (v: string) => void
  onEnsureSecret: (prevName: string, s: SecretRow) => void
}) {
  const defName = String(f.default ?? '').match(secretRefRe)?.[1] ?? ''
  const [newMode, setNewMode] = useState(false)
  const [draft, setDraft] = useState({ name: defName, value: '', domains: '' })
  const [prevName, setPrevName] = useState('')
  const vaultSet = new Set(vaultNames)
  const names = [...new Set([...vaultNames, ...specNames])]

  const applyDraft = (d: typeof draft) => {
    onEnsureSecret(prevName, { name: d.name, kind: 'inline', value: d.value, domains: d.domains })
    setPrevName(d.name)
    onChange(d.name ? `{{secrets.${d.name}}}` : '')
  }

  return (
    <div>
      {label}
      <select className={inputCls + ' font-mono text-xs'} value={newMode ? '__new__' : value}
        onChange={(e) => {
          const v = e.target.value
          if (v === '__new__') {
            setNewMode(true)
            applyDraft(draft)
            return
          }
          if (newMode && prevName) {
            onEnsureSecret(prevName, { name: '', kind: 'inline', value: '', domains: '' })
          }
          setNewMode(false)
          onChange(v)
        }}>
        <option value="">{f.default != null ? `default (${String(f.default)})` : 'default'}</option>
        {names.map((n) => (
          <option key={n} value={`{{secrets.${n}}}`}>{`{{secrets.${n}}}`}{vaultSet.has(n) ? ' — vault' : ''}</option>
        ))}
        <option value="__new__">+ new inline secret…</option>
      </select>
      {newMode && (
        <div className="mt-1.5 space-y-1.5 border-l-2 border-line-strong pl-3">
          <input className={inputCls + ' font-mono text-xs'} placeholder="NAME" value={draft.name}
            onChange={(e) => { const d = { ...draft, name: e.target.value }; setDraft(d); applyDraft(d) }} />
          {draft.name && !secretNameRe.test(draft.name) && (
            <p className="text-xs text-danger">NAME must match [A-Z_][A-Z0-9_]*</p>
          )}
          <input type="password" className={inputCls + ' font-mono text-xs'} placeholder="value (never persisted)"
            value={draft.value} autoComplete="new-password"
            onChange={(e) => { const d = { ...draft, value: e.target.value }; setDraft(d); applyDraft(d) }} />
          <input className={inputCls + ' font-mono text-xs'} placeholder="allowed domains (optional; empty = any)"
            value={draft.domains}
            onChange={(e) => { const d = { ...draft, domains: e.target.value }; setDraft(d); applyDraft(d) }} />
        </div>
      )}
    </div>
  )
}

function NewInstanceDialog({ onClose }: { onClose: () => void }) {
  const qc = useQueryClient()
  const [ttlMin, setTtlMin] = useState(15)
  const [fsOn, setFsOn] = useState(true)
  const [fsQuota, setFsQuota] = useState('67108864')
  const [netOn, setNetOn] = useState(false)
  const [netHosts, setNetHosts] = useState('')
  const [netMethods, setNetMethods] = useState('GET,POST')
  const [extOn, setExtOn] = useState(false)
  const [extRows, setExtRows] = useState<ExtRow[]>([])
  const [labelsText, setLabelsText] = useState('')
  const [envRows, setEnvRows] = useState<EnvRow[]>([])
  const [secretRows, setSecretRows] = useState<SecretRow[]>([])
  const [policyNames, setPolicyNames] = useState<string[] | null>(null)
  const [err, setErr] = useState('')
  const [jsonText, setJsonText] = useState('')
  const [jsonDirty, setJsonDirty] = useState(false)
  const { data: vault } = useQuery({
    queryKey: ['secrets'],
    queryFn: () => api.get<{ secrets: Secret[] }>('/api/v1/secrets'),
    retry: false,
  })
  const { data: library, error: policyError } = useQuery({
    queryKey: ['policies'],
    queryFn: () => api.get<{ policies: Policy[] }>('/api/v1/policies'),
  })
  const { data: extCat } = useQuery({
    queryKey: ['extensions'],
    queryFn: () => api.get<ExtensionCatalog>('/api/v1/extensions'),
    enabled: extOn,
    retry: false,
  })
  const selectedPolicies = useMemo(() => policyNames ?? library?.policies.filter((p) => p.default).map((p) => p.name), [policyNames, library])
  const vaultNames = useMemo(() => (vault?.secrets ?? []).map((s) => s.name), [vault])

  // upsertSecretRow adds or updates an auto-managed secret row (created
  // from an ext secret field); a manually added row with the same name
  // wins, and an empty name just removes the stale auto row.
  const upsertSecretRow = (prevName: string, row: SecretRow) => {
    setJsonDirty(false)
    setSecretRows((rows) => {
      const next = rows.filter((x) => !(x.auto && x.name === prevName && prevName !== row.name))
      if (!row.name) return next
      const i = next.findIndex((x) => x.name === row.name)
      if (i >= 0) {
        if (!next[i].auto) return next
        return next.map((x, j) => (j === i ? { ...row, auto: true } : x))
      }
      return [...next, { ...row, auto: true }]
    })
  }

  const specFromForm = (): SpecDraft => {
    const labels: Record<string, string> = {}
    for (const part of labelsText.split(',')) {
      const [k, v] = part.split('=').map((s) => s.trim())
      if (k) labels[k] = v ?? ''
    }
    const caps: Record<string, any> = {}
    if (fsOn) caps.fs = fsQuota ? { quota_bytes: Number(fsQuota) } : {}
    if (netOn) {
      caps.net = {
        allow_hosts: netHosts.split('\n').map((s) => s.trim()).filter(Boolean),
        methods: netMethods.split(',').map((s) => s.trim()).filter(Boolean),
      }
    }
    const draft: SpecDraft = { ttl_seconds: ttlMin * 60, labels, capabilities: caps, limits: {} }
    const env: Record<string, string> = {}
    for (const r of envRows) if (r.k) env[r.k] = r.v
    if (Object.keys(env).length) draft.env = env
    const secs: Record<string, any> = {}
    for (const r of secretRows) {
      if (!r.name) continue
      const doms = r.domains.split(/[,\s]+/).map((s) => s.trim()).filter(Boolean)
      if (r.kind === 'vault') {
        secs[r.name] = doms.length ? { ref: r.name, allowed_domains: doms } : { ref: r.name }
      } else {
        secs[r.name] = { value: r.value, allowed_domains: doms }
      }
    }
    if (extOn) {
      const exts: Record<string, any> = {}
      const needed = new Set<string>()
      for (const r of extRows) {
        if (!r.alias || !aliasRe.test(r.alias) || !r.source) continue
        const e: Record<string, any> = { source: r.source }
        if (r.sum) e.sum = r.sum
        const entry = extCat?.extensions.find((x) => x.source === r.source)
        const cfg: Record<string, any> = {}
        for (const [k, v] of Object.entries(r.config)) {
          if (v === '') continue
          const f = entry?.config.find((f) => f.name === k)
          cfg[k] = extCfgValue(f?.type ?? 'string', v)
        }
        if (Object.keys(cfg).length) e.config = cfg
        exts[r.alias] = e
        // every {{secrets.NAME}} referenced by config values or manifest
        // defaults must exist in the instance's secrets
        for (const f of entry?.config ?? []) {
          if (f.type !== 'secret') continue
          const m = (r.config[f.name] || String(f.default ?? '')).match(secretRefRe)
          if (m) needed.add(m[1])
        }
        for (const v of Object.values(r.config)) {
          const m = String(v).match(secretRefRe)
          if (m) needed.add(m[1])
        }
      }
      if (Object.keys(exts).length) caps.ext = exts
      // referenced vault secrets are added automatically as refs
      for (const n of needed) {
        if (!secs[n] && vaultNames.includes(n)) secs[n] = { ref: n }
      }
    }
    if (Object.keys(secs).length) draft.secrets = secs
    if (selectedPolicies !== undefined) draft.policies = selectedPolicies
    return draft
  }

  const jsonShown = useMemo(() => {
    if (jsonDirty) return jsonText
    return JSON.stringify(specFromForm(), null, 2)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [jsonDirty, ttlMin, fsOn, fsQuota, netOn, netHosts, netMethods, extOn, extRows, extCat, labelsText, envRows, secretRows, selectedPolicies, vault])

  const create = useMutation({
    mutationFn: (spec: any) => api.post<{ instance: Instance }>('/api/v1/instances', spec),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['instances'] })
      onClose()
    },
    onError: (e: any) => setErr(e.message),
  })

  const submit = () => {
    setErr('')
    let spec: any
    if (jsonDirty) {
      try {
        spec = JSON.parse(jsonText)
      } catch {
        setErr('advanced JSON is invalid')
        return
      }
    } else {
      spec = specFromForm()
    }
    create.mutate(spec)
  }

  const capTile = (name: string, desc: string, tone: 'blue' | 'red' | 'yellow', on: boolean, set: (v: boolean) => void) => (
    <button type="button" aria-pressed={on} onClick={() => { setJsonDirty(false); set(!on) }}
      className={`flex flex-col items-start gap-3 border-t-[3px] bg-paper px-4 py-3 text-left transition-colors hover:bg-surface ${on ? capBar[tone] : 'border-transparent'}`}>
      <Mark tone={on ? tone : 'gray'} className="!h-3 !w-3" shape={capShape[tone]} />
      <span>
        <span className={`block font-mono text-sm font-medium ${on ? 'text-ink' : 'text-sec'}`}>{name}</span>
        <span className="block text-xs text-mute">{desc}</span>
      </span>
    </button>
  )
  const removeBtn = 'ml-auto px-1 text-base leading-none text-mute transition-colors hover:text-danger'

  return (
    <Modal title="New instance" onClose={onClose} wide footer={
      <>
        {err && <p className="text-xs text-danger">{err}</p>}
        <div className="ml-auto flex gap-2">
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" disabled={create.isPending} onClick={submit}>
            Create instance
          </Button>
        </div>
      </>
    }>
      <div className="grid gap-8 lg:grid-cols-[1fr_360px]">
        <div className="space-y-7">
          <div className="grid grid-cols-[110px_1fr] gap-3">
            <Field label="TTL (minutes)">
              <input type="number" min={1} className={inputCls} value={ttlMin} onChange={(e) => { setJsonDirty(false); setTtlMin(Number(e.target.value)) }} />
            </Field>
            <Field label="Labels (k=v, comma separated)">
              <input className={inputCls} value={labelsText} onChange={(e) => { setJsonDirty(false); setLabelsText(e.target.value) }} placeholder="team=agents, env=dev" />
            </Field>
          </div>

          <div>
            <SectionTitle>Capabilities</SectionTitle>
            <div className="grid grid-cols-3 gap-px border border-line bg-line">
              {capTile('fs', 'virtual filesystem', 'blue', fsOn, setFsOn)}
              {capTile('net', 'outbound HTTP', 'red', netOn, setNetOn)}
              {capTile('ext', 'Starlark extensions', 'yellow', extOn, setExtOn)}
            </div>
            <div className="mt-4 space-y-4">
              {fsOn && (
                <div className="space-y-3 border-l-2 border-accent pl-4">
                  <Field label="fs quota (bytes)">
                    <input className={inputCls + ' font-mono'} value={fsQuota} onChange={(e) => { setJsonDirty(false); setFsQuota(e.target.value) }} />
                  </Field>
                </div>
              )}
              {netOn && (
                <div className="space-y-3 border-l-2 border-danger pl-4">
                  <Field label="allow_hosts (one per line; *.suffix or host:port; empty = any host, subject to selected policies)">
                    <textarea className={inputCls + ' font-mono text-xs'} rows={3} value={netHosts} onChange={(e) => { setJsonDirty(false); setNetHosts(e.target.value) }} />
                  </Field>
                  <Field label="methods">
                    <input className={inputCls + ' font-mono'} value={netMethods} onChange={(e) => { setJsonDirty(false); setNetMethods(e.target.value) }} />
                  </Field>
                </div>
              )}
              {extOn && (
                <div className="space-y-2 border-l-2 border-warn pl-4">
                  <div className="flex items-center justify-between">
                    <span className="text-xs font-medium text-sec">extensions</span>
                    <button type="button" className="text-btn"
                      onClick={() => { setJsonDirty(false); setExtRows([...extRows, { source: '', custom: false, sum: '', alias: '', config: {} }]) }}>+ Add</button>
                  </div>
                  {extCat && !extCat.remote_enabled && !extCat.local_enabled && (
                    <p className="text-xs text-mute">server has no ext sources enabled (--ext-local-roots / --ext-allow-sources)</p>
                  )}
                  {extRows.map((r, i) => (
                    <ExtRowEditor key={i} row={r} catalog={extCat?.extensions ?? []}
                      specNames={secretRows.map((s) => s.name).filter(Boolean)}
                      vaultNames={vaultNames}
                      netOn={netOn} fsOn={fsOn}
                      onChange={(nr) => { setJsonDirty(false); const rs = [...extRows]; rs[i] = nr; setExtRows(rs) }}
                      onEnsureSecret={upsertSecretRow}
                      onRemove={() => { setJsonDirty(false); setExtRows(extRows.filter((_, j) => j !== i)) }} />
                  ))}
                </div>
              )}
            </div>
          </div>

          <div>
            <SectionTitle aside={<button type="button" className="text-btn" onClick={() => { setJsonDirty(false); setEnvRows([...envRows, { k: '', v: '' }]) }}>+ Add</button>}>
              Env
            </SectionTitle>
            {envRows.length === 0 && <p className="text-xs text-mute">no env vars</p>}
            {envRows.map((r, i) => (
              <div key={i} className="mb-1.5 flex items-center gap-1.5">
                <input className={inputCls + ' !w-36 font-mono text-xs'} placeholder="NAME" value={r.k}
                  onChange={(e) => { setJsonDirty(false); const rs = [...envRows]; rs[i] = { ...r, k: e.target.value }; setEnvRows(rs) }} />
                <input className={inputCls + ' font-mono text-xs'} placeholder="value" value={r.v}
                  onChange={(e) => { setJsonDirty(false); const rs = [...envRows]; rs[i] = { ...r, v: e.target.value }; setEnvRows(rs) }} />
                <button className={removeBtn} aria-label="Remove" onClick={() => { setJsonDirty(false); setEnvRows(envRows.filter((_, j) => j !== i)) }}>×</button>
              </div>
            ))}
          </div>

          <div>
            <SectionTitle aside={<button type="button" className="text-btn" onClick={() => { setJsonDirty(false); setSecretRows([...secretRows, { name: '', kind: 'vault', value: '', domains: '' }]) }}>+ Add</button>}>
              Secrets
            </SectionTitle>
            {secretRows.length === 0 && <p className="text-xs text-mute">no secrets</p>}
            {secretRows.map((r, i) => {
              const set = (patch: Partial<SecretRow>) => {
                setJsonDirty(false)
                const rs = [...secretRows]
                rs[i] = { ...r, ...patch }
                setSecretRows(rs)
              }
              return (
                <div key={i} className="panel mb-2 space-y-1.5 p-2.5">
                  <div className="flex items-center gap-1.5">
                    <select className={inputCls + ' !w-24 text-xs'} value={r.kind} onChange={(e) => set({ kind: e.target.value as SecretSource })}>
                      <option value="vault">vault</option>
                      <option value="inline">inline</option>
                    </select>
                    {r.auto && <span className="eyebrow whitespace-nowrap">via ext</span>}
                    {r.kind === 'vault' ? (
                      <select className={inputCls + ' font-mono text-xs'} value={r.name} onChange={(e) => set({ name: e.target.value })}>
                        <option value="">pick…</option>
                        {(vault?.secrets ?? []).map((s) => <option key={s.id} value={s.name}>{s.name}</option>)}
                      </select>
                    ) : (
                      <input className={inputCls + ' font-mono text-xs'} placeholder="NAME" value={r.name} onChange={(e) => set({ name: e.target.value })} />
                    )}
                    <button className={removeBtn} aria-label="Remove" onClick={() => { setJsonDirty(false); setSecretRows(secretRows.filter((_, j) => j !== i)) }}>×</button>
                  </div>
                  {r.kind === 'inline' && (
                    <input type="password" className={inputCls + ' font-mono text-xs'} placeholder="value (never persisted)" value={r.value} onChange={(e) => set({ value: e.target.value })} autoComplete="new-password" />
                  )}
                  <input className={inputCls + ' font-mono text-xs'} value={r.domains} onChange={(e) => set({ domains: e.target.value })}
                    placeholder={r.kind === 'vault' ? 'narrow domains (optional)' : 'allowed domains (optional; empty = any)'} />
                </div>
              )
            })}
          </div>

          <div>
            <SectionTitle aside={<Link to="/policies" className="text-btn">Manage</Link>}>
              Policies
            </SectionTitle>
            <p className="mb-2 text-xs text-mute">Select built-in and Rego policies for this instance. Default policies are preselected; uncheck to opt out. Server policies always apply.</p>
            {!library && <p className="text-xs text-mute">{policyError ? 'Could not load policies; server defaults will apply unless you set policies in the JSON spec.' : 'Loading policies…'}</p>}
            {library && library.policies.length === 0 && <p className="text-xs text-mute">no policies available</p>}
            <div className="flex flex-wrap gap-x-4 gap-y-1.5">
              {(library?.policies ?? []).map((p) => (
                <label key={p.id} className="flex items-center gap-2 font-mono text-xs text-ink">
                  <input type="checkbox" checked={selectedPolicies?.includes(p.name) ?? p.default}
                    onChange={(e) => {
                      setJsonDirty(false)
                      setPolicyNames(e.target.checked ? [...(selectedPolicies ?? []), p.name] : (selectedPolicies ?? []).filter((n) => n !== p.name))
                    }} />
                  <span>
                    {p.name} <Tag>{p.kind === 'builtin' ? 'Built-in' : 'Rego'}</Tag>
                    {p.default && <span className="ml-2 font-sans text-mute">default</span>}
                    {p.description && <span className="mt-1 block font-sans text-mute">{p.description}</span>}
                  </span>
                </label>
              ))}
            </div>
          </div>
        </div>

        <div className="flex flex-col">
          <SectionTitle aside={jsonDirty ? <span className="text-warn-text">edited by hand</span> : <span className="text-mute">generated from form</span>}>
            Spec JSON
          </SectionTitle>
          <textarea
            className={inputCls + ' min-h-[420px] flex-1 resize-none bg-code font-mono text-xs leading-relaxed'}
            spellCheck={false}
            value={jsonShown}
            onChange={(e) => { setJsonText(e.target.value); setJsonDirty(true) }}
          />
        </div>
      </div>
    </Modal>
  )
}

export default function Instances() {
  const qc = useQueryClient()
  const [showNew, setShowNew] = useState(false)
  const { data, isLoading } = useQuery({
    queryKey: ['instances'],
    queryFn: () => api.get<{ instances: Instance[] }>('/api/v1/instances'),
    refetchInterval: 15000,
  })
  const del = useMutation({
    mutationFn: (id: string) => api.del(`/api/v1/instances/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['instances'] }),
  })

  return (
    <div>
      <PageHeader index="01" section="Runtime" title="Instances"
        actions={<Button variant="primary" onClick={() => setShowNew(true)}>New instance</Button>}>
        Each instance is a Starlark session with its own globals, VFS and policy snapshot. Only the capabilities granted in its spec exist.
      </PageHeader>
      {isLoading && <Loading />}
      <div className="tbl-wrap">
        <table className="tbl">
          <thead>
            <tr>
              <th>ID</th>
              <th>Status</th>
              <th>Capabilities</th>
              <th>Labels</th>
              <th>Created</th>
              <th>Expires</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {(data?.instances ?? []).map((i) => (
              <tr key={i.id} className="row-hover">
                <td className="font-mono text-xs">
                  <Link to={`/instances/${i.id}`} className="link font-medium">{i.id}</Link>
                </td>
                <td><StatusBadge status={i.status} /></td>
                <td>
                  <span className="flex flex-wrap gap-1">
                    {Object.entries(i.spec?.capabilities ?? {}).map(([k, v]) => (
                      <Tag key={k}>{k === 'ext' ? `ext(${Object.keys(v ?? {}).join(', ')})` : k}</Tag>
                    ))}
                    {Object.keys(i.spec?.capabilities ?? {}).length === 0 && <span className="text-mute">—</span>}
                  </span>
                </td>
                <td>
                  <span className="flex flex-wrap gap-1">
                    {Object.entries(i.labels ?? {}).map(([k, v]) => (
                      <Tag key={k}>{k}={v}</Tag>
                    ))}
                  </span>
                </td>
                <td className="whitespace-nowrap text-xs text-sec">{fmtTime(i.created_at)}</td>
                <td className="font-mono text-xs">{i.status === 'running' ? fmtCountdown(i.expires_at) : <span className="text-mute">—</span>}</td>
                <td className="text-right">
                  {i.status === 'running' && (
                    <Button variant="danger" size="sm" onClick={() => del.mutate(i.id)}>Delete</Button>
                  )}
                </td>
              </tr>
            ))}
            {data && data.instances.length === 0 && <EmptyRow cols={7}>no instances</EmptyRow>}
          </tbody>
        </table>
      </div>
      {showNew && <NewInstanceDialog onClose={() => setShowNew(false)} />}
    </div>
  )
}
