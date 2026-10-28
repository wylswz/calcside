import type { CSSProperties } from 'react'
import type { Policy, Secret } from '../api'
import type { InstanceDraft } from '../instanceDraft'
import { Button, Field, inputCls } from './ui'

type Patch = (patch: Partial<InstanceDraft>) => void

// RulesSection edits policies and run limits. Rules constrain what the
// capabilities may do; they never grant access.
export function RulesSection({ draft, set, policies, policiesError, onRetry, selected }: {
  draft: InstanceDraft
  set: Patch
  policies: Policy[]
  policiesError: boolean
  onRetry: () => void
  selected: string[]
}) {
  return (
    <div className="space-y-4">
      <p className="text-sm text-sec">Policies constrain capabilities; they do not grant access. Server policies always apply.</p>
      {policiesError && <p className="text-sm text-danger">Could not load policies. <button className="underline" onClick={onRetry}>Retry policies</button></p>}
      <div className="mb-2 flex items-center justify-between gap-3">
        <p className="text-xs text-sec">{draft.policies === null ? 'Default selection' : 'Custom selection'}</p>
        {draft.policies !== null && <button type="button" className="text-btn underline" onClick={() => set({ policies: null })}>Use default policies</button>}
      </div>
      <div className="ext-results" style={{ '--ext-rows': Math.max(1, Math.ceil(policies.length / 2)) } as CSSProperties}>
        <ul className="ext-grid">
          {policies.map((p) => {
            const added = selected.includes(p.name)
            return (
              <li key={p.name} className={'ext-card' + (added ? ' ext-card-added' : '')}>
                <div className="min-w-0 flex-1">
                  <p className="ext-card-name">{p.name}</p>
                  <p className="ext-card-desc" title={p.description}>{p.description || 'No description'}</p>
                  <p className="ext-card-source">{p.kind === 'builtin' ? 'built-in' : 'workspace policy'}{p.default ? ' · default' : ''}</p>
                </div>
                <Button className="ext-card-action" aria-pressed={added}
                  onClick={() => set({ policies: added ? selected.filter((name) => name !== p.name) : [...selected, p.name] })}>
                  {added ? 'Added' : 'Add'}
                </Button>
              </li>
            )
          })}
          {selected.filter((name) => !policies.some((p) => p.name === name)).map((name) => (
            <li key={name} className="ext-card ext-card-added">
              <div className="min-w-0 flex-1">
                <p className="ext-card-name">{name}</p>
                <p className="ext-card-desc">Not in the current policy catalog.</p>
                <p className="ext-card-source">unknown</p>
              </div>
              <Button className="ext-card-action" onClick={() => set({ policies: selected.filter((p) => p !== name) })}>Remove</Button>
            </li>
          ))}
        </ul>
      </div>
      {policies.length === 0 && !policiesError && <p className="text-xs text-sec">No policies are available in this workspace.</p>}
      <div className="grid gap-3 sm:grid-cols-3">
        {(['exec_timeout_ms', 'max_steps', 'max_output_bytes'] as const).map((key) => <Field key={key} label={key}>
          <input className={inputCls} type="number" min="1" value={draft.limits[key] ?? ''} placeholder="Server default" onChange={(e) => {
            const limits = { ...draft.limits }; if (e.target.value === '') delete limits[key]; else limits[key] = Number(e.target.value); set({ limits })
          }} />
        </Field>)}
      </div>
    </div>
  )
}

// InputsSection edits variables, labels and secret bindings. Secret
// values are write-only; vault entries are referenced by name.
export function InputsSection({ draft, set, keyMode, application = false, vault, vaultError, onRetryVault, listId }: {
  draft: InstanceDraft
  set: Patch
  keyMode: boolean
  application?: boolean
  vault: Secret[]
  vaultError: boolean
  onRetryVault: () => void
  listId: string
}) {
  return (
    <div className="space-y-5">
      {(['env', 'labels'] as const).map((kind) => <div key={kind}>
        <div className="mb-3 flex justify-between"><h4 className="font-semibold text-ink">{kind === 'env' ? 'Variables' : 'Labels'}</h4><button type="button" className="text-btn" onClick={() => set({ [kind]: [...draft[kind], { k: '', v: '' }] })}>Add {kind === 'env' ? 'variable' : 'label'}</button></div>
        {draft[kind].map((row, i) => <div key={i} className="mb-3 grid gap-2 sm:grid-cols-[1fr_1fr_auto]">
          <input aria-label={`${kind} name ${i + 1}`} className={inputCls} value={row.k} onChange={(e) => set({ [kind]: draft[kind].map((r, j) => j === i ? { ...r, k: e.target.value } : r) })} />
          <input aria-label={`${kind} value ${i + 1}`} className={inputCls} value={row.v} onChange={(e) => set({ [kind]: draft[kind].map((r, j) => j === i ? { ...r, v: e.target.value } : r) })} />
          <button type="button" className="text-btn" aria-label={`Remove ${kind} ${i + 1}`} onClick={() => set({ [kind]: draft[kind].filter((_, j) => j !== i) })}>Remove</button>
        </div>)}
      </div>)}
      <div className="flex justify-between"><h4 className="font-semibold text-ink">Secrets</h4><button type="button" className="text-btn" onClick={() => set({ secrets: [...draft.secrets, { name: '', kind: 'vault', ref: '', value: '', domains: [], extra: {} }] })}>Add secret</button></div>
      {keyMode && <p className="text-xs text-sec">{application ? 'Applications' : 'Restricted keys'} store vault references only. Inline values are not allowed.</p>}
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
