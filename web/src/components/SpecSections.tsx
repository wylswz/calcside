import type { Secret } from '../api'
import type { InstanceDraft } from '../instanceDraft'
import { Field, inputCls } from './ui'

type Patch = (patch: Partial<InstanceDraft>) => void

// LimitsSection edits run limits only; a blank field keeps the server
// default. Policies moved to the capabilities palette — they are
// add-ons, not settings.
export function LimitsSection({ draft, set }: {
  draft: InstanceDraft
  set: Patch
}) {
  return (
    <div className="grid gap-3 sm:grid-cols-3">
      {(['exec_timeout_ms', 'max_steps', 'max_output_bytes'] as const).map((key) => <Field key={key} label={key}>
        <input className={inputCls} type="number" min="1" value={draft.limits[key] ?? ''} placeholder="Server default" onChange={(e) => {
          const limits = { ...draft.limits }; if (e.target.value === '') delete limits[key]; else limits[key] = Number(e.target.value); set({ limits })
        }} />
      </Field>)}
    </div>
  )
}

// KeyValueRows edits a list of name/value pairs; idPrefix namespaces
// the aria-labels so the same editor can appear in two surfaces (the
// create flow's basics step uses 'basics label').
export function KeyValueRows({ title, addText, idPrefix, rows, onChange }: {
  title: string
  addText: string
  idPrefix: string
  rows: { k: string; v: string }[]
  onChange: (rows: { k: string; v: string }[]) => void
}) {
  return (
    <div>
      <div className="mb-3 flex justify-between"><h4 className="font-semibold text-ink">{title}</h4><button type="button" className="text-btn" onClick={() => onChange([...rows, { k: '', v: '' }])}>{addText}</button></div>
      {rows.map((row, i) => <div key={i} className="mb-3 grid gap-2 sm:grid-cols-[1fr_1fr_auto]">
        <input aria-label={`${idPrefix} name ${i + 1}`} className={inputCls} value={row.k} onChange={(e) => onChange(rows.map((r, j) => j === i ? { ...r, k: e.target.value } : r))} />
        <input aria-label={`${idPrefix} value ${i + 1}`} className={inputCls} value={row.v} onChange={(e) => onChange(rows.map((r, j) => j === i ? { ...r, v: e.target.value } : r))} />
        <button type="button" className="text-btn" aria-label={`Remove ${idPrefix} ${i + 1}`} onClick={() => onChange(rows.filter((_, j) => j !== i))}>Remove</button>
      </div>)}
    </div>
  )
}

// InputsSection edits variables, labels and secret bindings. Secret
// values are write-only; vault entries are referenced by name. When the
// create flow owns labels in its basics step, they are hidden here.
export function InputsSection({ draft, set, keyMode, hideLabels = false, vault, vaultError, onRetryVault, listId }: {
  draft: InstanceDraft
  set: Patch
  keyMode: boolean
  hideLabels?: boolean
  vault: Secret[]
  vaultError: boolean
  onRetryVault: () => void
  listId: string
}) {
  return (
    <div className="space-y-5">
      <KeyValueRows title="Variables" addText="Add variable" idPrefix="env" rows={draft.env} onChange={(env) => set({ env })} />
      {!hideLabels && <KeyValueRows title="Labels" addText="Add label" idPrefix="labels" rows={draft.labels} onChange={(labels) => set({ labels })} />}
      <div className="flex justify-between"><h4 className="font-semibold text-ink">Secrets</h4><button type="button" className="text-btn" onClick={() => set({ secrets: [...draft.secrets, { name: '', kind: 'vault', ref: '', value: '', domains: [], extra: {} }] })}>Add secret</button></div>
      {vaultError && <p className="text-xs text-sec">Vault unavailable. Existing references are preserved; enter a reference by name or retry. <button className="underline" onClick={onRetryVault}>Retry vault</button></p>}
      <datalist id={listId}>{vault.map((s) => <option key={s.id} value={s.name} />)}</datalist>
      {draft.secrets.map((secret, index) => {
        const patch = (value: Partial<typeof secret>) => set({ secrets: draft.secrets.map((s, i) => i === index ? { ...s, ...value } : s) })
        return <div key={index} className="configuration-card space-y-3">
          <div className="flex justify-between gap-3"><h5 className="font-semibold text-ink">{secret.name || 'New secret'}</h5><button type="button" className="text-btn" aria-label={`Remove secret ${index + 1}`} onClick={() => set({ secrets: draft.secrets.filter((_, i) => i !== index) })}>Remove</button></div>
          <Field label={`Secret name ${index + 1}`}><input className={inputCls} value={secret.name} onChange={(e) => patch({ name: e.target.value })} /></Field>
          <Field label={`Secret source ${index + 1}`}><select className={inputCls} value={secret.kind} onChange={(e) => patch({ kind: e.target.value === 'vault' ? 'vault' : 'inline', value: '', needsReplacement: false })}><option value="vault">Vault reference</option>{!keyMode && <option value="inline">Inline value</option>}</select></Field>
          {secret.kind === 'vault'
            ? <Field label={`Vault reference ${index + 1}`}><input className={inputCls} list={listId} value={secret.ref} onChange={(e) => patch({ ref: e.target.value })} /></Field>
            : <Field label={`Secret value ${index + 1}`}><input type="password" className={inputCls} autoComplete="new-password" value={secret.value} onChange={(e) => patch({ value: e.target.value, needsReplacement: false })} /></Field>}
          {secret.needsReplacement && <p className="text-xs text-warn-text">The original inline value is not stored. Replace or remove this secret.</p>}
          <Field label={`Allowed secret domains ${index + 1}`}><input className={inputCls} value={secret.domains.join(',')} onChange={(e) => patch({ domains: e.target.value.split(',') })} /></Field>
        </div>
      })}
    </div>
  )
}
