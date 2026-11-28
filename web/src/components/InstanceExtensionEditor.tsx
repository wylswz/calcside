import { findExtension } from '../builderCatalog.ts' 
import { useEffect, useRef, useState } from 'react'
import type { ExtensionInfo } from '../api'
import { formatOpDoc } from '../opDoc.ts'
import type { ExtDraft, JsonValue } from '../instanceDraft'
import { Field, inputCls } from './ui'
import { Disclosure, Select } from '../ui/core'

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
                  <Select
                    ariaLabel={`${value.alias} ${field.name}`}
                    className="w-full"
                    value={v === undefined ? '' : String(v)}
                    onChange={(val) => config(field.name, val === '' ? undefined : val === 'true')}
                    options={[
                      { value: '', label: fallback || 'Default' },
                      { value: 'true', label: 'true' },
                      { value: 'false', label: 'false' },
                    ]}
                  />
                ) : field.type === 'secret' ? (
                  onSecretBindingChange ? (() => {
                    const bound = secretBindings?.[field.name]?.ref
                    const m = typeof v === 'string' ? v.match(/^\{\{\s*secrets\.([A-Z_][A-Z0-9_]{0,63})\s*\}\}$/) : null
                    const real = m ? legacySecretRefs[m[1]] : undefined
                    const curVal = bound || (m ? (real ?? '__existing__') : (v === undefined ? '' : '__existing__'))
                    const opts: Array<{ value: string; label: string; disabled?: boolean }> = [
                      { value: '', label: 'Choose a vault secret' },
                      ...vaultNames.map((name) => ({ value: name, label: name })),
                    ]
                    if (bound && !vaultNames.includes(bound)) {
                      opts.push({ value: bound, label: `${bound} (unavailable)`, disabled: true })
                    } else if (!bound) {
                      if (m && real && !vaultNames.includes(real)) opts.push({ value: real, label: `${real} (unavailable)`, disabled: true })
                      else if (m && !real) opts.push({ value: '__existing__', label: '(existing input)', disabled: true })
                      else if (!m && v !== undefined) opts.push({ value: '__existing__', label: '(existing input)', disabled: true })
                    }
                    return (
                      <Select
                        ariaLabel={`${value.alias} ${field.name}`}
                        className="w-full"
                        value={curVal}
                        onChange={(picked) => {
                          if (picked === '__existing__') return
                          const prev = secretBindings?.[field.name]
                          onSecretBindingChange(field.name, picked ? { ...prev, ref: picked } : undefined)
                        }}
                        options={opts}
                      />
                    )
                  })() : (
                    <Select
                      ariaLabel={`${value.alias} ${field.name}`}
                      className="w-full"
                      value={typeof v === 'string' ? v : ''}
                      onChange={(picked) => {
                        if (picked.startsWith('__vault:')) { onUseVault?.(picked.slice(8)); config(field.name, `{{secrets.${picked.slice(8)}}}`) }
                        else config(field.name, picked || undefined)
                      }}
                      options={[{ value: '', label: 'Choose a secret' }]}
                      groups={[
                        ...(secretNames.length > 0 ? [{ label: 'Existing inputs', options: secretNames.map((name) => ({ value: `{{secrets.${name}}}`, label: name })) }] : []),
                        ...(vaultNames.filter((n) => !secretNames.includes(n)).length > 0
                          ? [{ label: 'Vault', options: vaultNames.filter((n) => !secretNames.includes(n)).map((name) => ({ value: `__vault:${name}`, label: name })) }]
                          : []),
                      ]}
                    />
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
        </>
      )}
      {entry && (
        <Disclosure variant="card" summary="Advanced extension settings" className="ext-advanced">
          <div className="space-y-4">{sourceFields}
            {(entry.ops ?? []).length > 0 && (
              <Disclosure summary={`Operations (${(entry.ops ?? []).length})`} className="ext-advanced">
                <div className="space-y-2">
                  {(entry.ops ?? []).map((op) => (
                    <pre key={op.name} className="m-0 whitespace-pre-wrap break-words font-mono text-xs text-sec">
                      {formatOpDoc(`ext.${value.alias || '?'}.${op.name}`, op)}
                    </pre>
                  ))}
                </div>
              </Disclosure>
            )}
          </div>
        </Disclosure>
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
