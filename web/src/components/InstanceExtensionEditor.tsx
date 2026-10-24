import { useState } from 'react'
import type { ExtensionInfo } from '../api'
import type { ExtDraft, JsonValue } from '../instanceDraft'
import { Field, inputCls } from './ui'

export function InstanceExtensionEditor({ value, catalog, secretNames, onChange, onValidityChange }: {
  value: ExtDraft
  catalog: ExtensionInfo[]
  secretNames: string[]
  onChange: (value: ExtDraft) => void
  onValidityChange: (error: string) => void
}) {
  const entry = catalog.find((e) => e.source === value.source)
  const [customJSON, setCustomJSON] = useState(JSON.stringify(value.config, null, 2))
  const [jsonError, setJsonError] = useState('')
  const config = (name: string, v: JsonValue | undefined) => {
    const next = { ...value.config }
    if (v === undefined) delete next[name]
    else next[name] = v
    onChange({ ...value, config: next })
  }
  return (
    <div className="space-y-4">
      <Field label="Extension source">
        <input className={inputCls} value={value.source} placeholder="contrib/tavily or domain/group/name@version"
          onChange={(e) => onChange({ ...value, source: e.target.value })} />
      </Field>
      <Field label="Alias">
        <input className={inputCls} value={value.alias} onChange={(e) => onChange({ ...value, alias: e.target.value })} />
      </Field>
      <Field label="Integrity sum (required for remote sources)">
        <input className={inputCls + ' font-mono text-xs'} value={value.sum ?? ''} placeholder="h1:..."
          onChange={(e) => onChange({ ...value, sum: e.target.value || undefined })} />
      </Field>
      {entry && (
        <>
          <p className="text-sm text-sec">{entry.description}</p>
          {entry.dependencies.length > 0 && <p className="text-xs text-sec">Requires: {entry.dependencies.join(', ')}. Add these abilities separately.</p>}
          {entry.ops.length > 0 && <p className="break-words font-mono text-xs text-sec">{entry.ops.map((op) => `ext.${value.alias || '?'}.${op.name}()`).join(' · ')}</p>}
          {entry.config.map((field) => {
            const v = value.config[field.name]
            const fallback = field.default == null ? '' : `Default: ${JSON.stringify(field.default)}`
            return (
              <Field key={field.name} label={`${field.name}${field.doc ? ` - ${field.doc}` : ''}`}>
                {field.type === 'bool' ? (
                  <select className={inputCls} value={v === undefined ? '' : String(v)}
                    onChange={(e) => config(field.name, e.target.value === '' ? undefined : e.target.value === 'true')}>
                    <option value="">{fallback || 'Default'}</option><option value="true">true</option><option value="false">false</option>
                  </select>
                ) : field.type === 'secret' ? (
                  <select className={inputCls} value={typeof v === 'string' ? v : ''}
                    onChange={(e) => config(field.name, e.target.value || undefined)}>
                    <option value="">{fallback || 'Choose a secret from Inputs'}</option>
                    {secretNames.map((name) => <option key={name} value={`{{secrets.${name}}}`}>{name}</option>)}
                  </select>
                ) : field.type === 'string_list' ? (
                  <textarea className={inputCls} rows={3} placeholder={fallback || 'One value per line'}
                    value={Array.isArray(v) ? v.join('\n') : ''}
                    onChange={(e) => config(field.name, e.target.value === '' ? undefined : e.target.value.split('\n'))} />
                ) : field.type === 'string_map' ? (
                  <JSONConfigField value={v} onChange={(v) => config(field.name, v)} onValidityChange={onValidityChange} />
                ) : (
                  <input className={inputCls} type={field.type === 'int' ? 'number' : 'text'} placeholder={fallback}
                    value={typeof v === 'string' || typeof v === 'number' ? v : ''}
                    onChange={(e) => config(field.name, e.target.value === '' ? undefined : field.type === 'int' ? Number(e.target.value) : e.target.value)} />
                )}
              </Field>
            )
          })}
          <p className="text-xs text-sec">Secret fields reference names only. Add or replace their values under Inputs.</p>
        </>
      )}
      {!entry && (
        <Field label="Custom configuration JSON">
          <textarea className={inputCls + ' font-mono text-xs'} rows={5} value={customJSON} onChange={(e) => {
            setCustomJSON(e.target.value)
            try {
              const parsed: unknown = JSON.parse(e.target.value)
              if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) throw new Error('Expected an object')
              onChange({ ...value, config: parsed as Record<string, JsonValue> })
              setJsonError('')
              onValidityChange('')
            } catch {
              const message = 'Configuration must be a JSON object; fix it before creating.'
              setJsonError(message)
              onValidityChange(message)
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
      <textarea className={inputCls + ' font-mono text-xs'} rows={3} value={text} onChange={(e) => {
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
