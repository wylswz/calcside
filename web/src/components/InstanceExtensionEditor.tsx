import { findExtension } from '../builderCatalog.ts' 
import { useEffect, useRef, useState } from 'react'
import type { ExtensionInfo } from '../api'
import { formatOpDoc } from '../opDoc.ts'
import type { ExtDraft, JsonValue } from '../instanceDraft'
import { Field, inputCls } from './ui'

export function InstanceExtensionEditor({ value, catalog, secretNames, vaultNames = [], onUseVault, secretBindings, legacySecretRefs = {}, onSecretBindingChange, onChange, onValidityChange }: {
  value: ExtDraft
  catalog: ExtensionInfo[]
  secretNames: string[]
  vaultNames?: string[]
  onUseVault?: (name: string) => void
  // Default-binding env refs per manifest secret field; when
  // onSecretBindingChange is present the secret picker writes the
  // environment default binding instead of a config placeholder.
  secretBindings?: Record<string, { ref: string; allowed_domains?: string[] }>
  // installed vault-input name -> real vault ref (for displaying
  // legacy {{secrets.NAME}} placeholders as their actual ref).
  legacySecretRefs?: Record<string, string>
  onSecretBindingChange?: (field: string, sf: { ref: string; allowed_domains?: string[] } | undefined) => void
  onChange: (value: ExtDraft) => void
  onValidityChange: (error: string) => void
}) {
  const entry = findExtension(catalog, value.source)
  const [customJSON, setCustomJSON] = useState(JSON.stringify(value.config, null, 2))
  const [jsonError, setJsonError] = useState('')
  const fieldErrors = useRef(new Map<string, string>())
  const previousSource = useRef(value.source)
  const reportValidity = (field: string, message: string) => {
    if (message) fieldErrors.current.set(field, message)
    else fieldErrors.current.delete(field)
    onValidityChange([...fieldErrors.current.values()].join('; '))
  }
  useEffect(() => {
    if (previousSource.current === value.source) return
    previousSource.current = value.source
    fieldErrors.current.clear()
    setJsonError('')
    onValidityChange('')
  }, [value.source, onValidityChange])
  const config = (name: string, v: JsonValue | undefined) => {
    const next = { ...value.config }
    if (v === undefined) delete next[name]
    else next[name] = v
    onChange({ ...value, config: next })
  }
  // Identity is read-only metadata: extensions are added/replaced only
  // through the catalog picker (remove + Add), never by typing IDs.
  const sourceFields = (
    <>
      <Field label="Extension">
        <code className="rounded bg-hover px-1 py-0.5 text-xs">
          {entry?.name || value.source || 'unresolved'}{entry?.version ? ` · ${entry.version}` : value.source.includes('@') ? ` · ${value.source.split('@')[1]}` : ''}
        </code>
      </Field>
      <Field label="Call-site alias">
        <code className="rounded bg-hover px-1 py-0.5 font-mono text-xs">{value.alias || 'unset'}</code>
      </Field>
      {value.sum && (
        <Field label="Integrity sum">
          <code className="break-all font-mono text-xs text-sec">{value.sum}</code>
        </Field>
      )}
    </>
  )
  return (
    <div className="space-y-4">
      {entry && (
        <>
          {(entry.config ?? []).map((field) => {
            const v = value.config[field.name]
            const fallback = field.default == null ? '' : `Default: ${JSON.stringify(field.default)}`
            return (
              <Field key={field.name} label={`${field.name}${field.doc ? ` - ${field.doc}` : ''}`}>
                {field.type === 'bool' ? (
                  <select className={inputCls} aria-label={`${value.alias} ${field.name}`} value={v === undefined ? '' : String(v)}
                    onChange={(e) => config(field.name, e.target.value === '' ? undefined : e.target.value === 'true')}>
                    <option value="">{fallback || 'Default'}</option><option value="true">true</option><option value="false">false</option>
                  </select>
                ) : field.type === 'secret' ? (
                  onSecretBindingChange ? (
                    // Environment default binding: the picker writes a
                    // vault ref only — never a config placeholder.
                    <select className={inputCls} aria-label={`${value.alias} ${field.name}`}
                      value={(() => {
                        const bound = secretBindings?.[field.name]?.ref
                        if (bound) return bound
                        // Legacy placeholder still configured — show it.
                        const m = typeof v === 'string' ? v.match(/^\{\{\s*secrets\.([A-Z_][A-Z0-9_]{0,63})\s*\}\}$/) : null
                        if (m) return legacySecretRefs[m[1]] ?? '__existing__'
                        return v === undefined ? '' : '__existing__'
                      })()}
                      onChange={(e) => {
                        const picked = e.target.value
                        if (picked === '__existing__') return // never silently clear a legacy value
                        const prev = secretBindings?.[field.name]
                        onSecretBindingChange(field.name,
                          picked ? { ...prev, ref: picked } : undefined)
                      }}>
                      <option value="">Choose a vault secret</option>
                      {vaultNames.map((name) => <option key={name} value={name}>{name}</option>)}
                      {secretBindings?.[field.name]?.ref && !vaultNames.includes(secretBindings[field.name]!.ref) && (
                        <option value={secretBindings[field.name]!.ref} disabled>{secretBindings[field.name]!.ref} (unavailable)</option>
                      )}
                      {(() => {
                        const bound = secretBindings?.[field.name]?.ref
                        if (bound) return null
                        const m = typeof v === 'string' ? v.match(/^\{\{\s*secrets\.([A-Z_][A-Z0-9_]{0,63})\s*\}\}$/) : null
                        const real = m ? legacySecretRefs[m[1]] : undefined
                        if (m && real && !vaultNames.includes(real)) return <option value={real} disabled>{real} (unavailable)</option>
                        if (m && !real) return <option value="__existing__" disabled>(existing input)</option>
                        if (!m && v !== undefined) return <option value="__existing__" disabled>(existing input)</option>
                        return null
                      })()}
                    </select>
                  ) : (
                  <select className={inputCls} value={typeof v === 'string' ? v : ''}
                    onChange={(e) => {
                      const picked = e.target.value
                      if (picked.startsWith('__vault:')) { onUseVault?.(picked.slice(8)); config(field.name, `{{secrets.${picked.slice(8)}}}`) }
                      else config(field.name, picked || undefined)
                    }}>
                    <option value="">Choose a secret</option>
                    {secretNames.length > 0 && <optgroup label="Existing inputs">{secretNames.map((name) => <option key={name} value={`{{secrets.${name}}}`}>{name}</option>)}</optgroup>}
                    {vaultNames.length > 0 && <optgroup label="Vault">{vaultNames.filter((n) => !secretNames.includes(n)).map((name) => <option key={name} value={`__vault:${name}`}>{name}</option>)}</optgroup>}
                  </select>
                  )
                ) : field.type === 'string_list' ? (
                  <textarea className={inputCls} rows={3} placeholder={fallback || 'One value per line'}
                    value={Array.isArray(v) ? v.join('\n') : ''}
                    onChange={(e) => config(field.name, e.target.value === '' ? undefined : e.target.value.split('\n'))} />
                ) : field.type === 'string_map' ? (
                  <JSONConfigField value={v} onChange={(v) => config(field.name, v)} onValidityChange={(m) => reportValidity(field.name, m)} />
                ) : (
                  <input className={inputCls} aria-label={`${value.alias} ${field.name}`} type={field.type === 'int' ? 'number' : 'text'} placeholder={fallback}
                    value={typeof v === 'string' || typeof v === 'number' ? v : ''}
                    onChange={(e) => config(field.name, e.target.value === '' ? undefined : field.type === 'int' ? Number(e.target.value) : e.target.value)} />
                )}
              </Field>
            )
          })}
          {(entry.config ?? []).some((f) => f.type === 'secret') && <p className="text-xs text-sec">Choose an existing vault reference, or add a secret under Inputs. Values are never shown here.</p>}
        </>
      )}
      {entry && (
        <details className="ext-advanced">
          <summary>Advanced extension settings</summary>
          <div className="mt-3 space-y-4">{sourceFields}
            {(entry.ops ?? []).length > 0 && (
              <details className="ext-advanced">
                <summary>Operations ({(entry.ops ?? []).length})</summary>
                <div className="mt-2 space-y-2">
                  {(entry.ops ?? []).map((op) => (
                    <pre key={op.name} className="m-0 whitespace-pre-wrap break-words font-mono text-xs text-sec">
                      {formatOpDoc(`ext.${value.alias || '?'}.${op.name}`, op)}
                    </pre>
                  ))}
                </div>
              </details>
            )}
          </div>
        </details>
      )}
      {!entry && sourceFields}
      {!entry && (
        <Field label="Custom configuration JSON">
          <textarea className={inputCls + ' font-mono text-xs'} rows={5} value={customJSON} onChange={(e) => {
            setCustomJSON(e.target.value)
            try {
              const parsed: unknown = JSON.parse(e.target.value)
              if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) throw new Error('Expected an object')
              onChange({ ...value, config: parsed as Record<string, JsonValue> })
              setJsonError('')
              reportValidity('$config', '')
            } catch {
              const message = 'Configuration must be a JSON object; fix it before creating.'
              setJsonError(message)
              reportValidity('$config', message)
            }
          }} />
          {jsonError && <p className="text-xs text-danger" role="alert">{jsonError}</p>}
        </Field>
      )}
    </div>
  )
}

function JSONConfigField({ value, onChange, onValidityChange }: {
  value: JsonValue | undefined
  onChange: (value: JsonValue | undefined) => void
  onValidityChange: (error: string) => void
}) {
  const [text, setText] = useState(value === undefined ? '' : JSON.stringify(value, null, 2))
  const [error, setError] = useState(false)
  return (
    <>
      <textarea className={inputCls + ' font-mono text-xs'} aria-label="config JSON" rows={3} value={text} onChange={(e) => {
        setText(e.target.value)
        try {
          const parsed: JsonValue | undefined = e.target.value === '' ? undefined : JSON.parse(e.target.value)
          onChange(parsed)
          setError(false)
          onValidityChange('')
        } catch { setError(true); onValidityChange('Fix extension configuration JSON before creating.') }
      }} />
      {error && <p className="text-xs text-danger" role="alert">Fix this JSON before creating.</p>}
    </>
  )
}
