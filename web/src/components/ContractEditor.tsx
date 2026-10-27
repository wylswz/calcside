import { useEffect, useMemo, useRef, useState } from 'react'
import { Button, SwapLabel, Notice, Confirm, inputCls } from './ui'
import SchemaFields from './SchemaFields'
import { jsonPrecise, MAX_TEXT_BYTES, ContractSide } from '../buildDraft'
import { api } from '../api'

// ContractEditor edits one side (input or output) of the build draft:
// raw sample textarea + Generate schema + schema text. Field editing
// stays in SchemaFields when the schema is precision-safe AND has been
// backend-validated. Async guards compare against a latest-ref updated
// every render — never a stale closure.
export default function ContractEditor({
  label, side, onChange, scopeAlive,
}: {
  label: string
  side: ContractSide
  onChange: (s: ContractSide) => void
  scopeAlive: () => boolean
}) {
  const [busy, setBusy] = useState<'idle' | 'gen' | 'val'>('idle')
  const [msg, setMsg] = useState('')
  const [confirmGen, setConfirmGen] = useState(false)
  const [mode, setMode] = useState<'fields' | 'json'>('json')
  // trusted: schema was generated or backend-validated this session —
  // safe to parse for the fields view.
  const [trusted, setTrusted] = useState(false)

  const latest = useRef(side)
  latest.current = side
  const mounted = useRef(true)
  useEffect(() => { mounted.current = true; return () => { mounted.current = false } }, [])
  const reqID = useRef(0)

  const over = new TextEncoder().encode(side.sample).length > MAX_TEXT_BYTES
  const precise = useMemo(() => jsonPrecise(side.schema), [side.schema])
  const canFields = trusted && precise

  // Entering fields mode on an untrusted schema must validate first —
  // canonicalization also rejects duplicate keys JSON.parse would hide.
  async function enterFields() {
    if (trusted) { setMode('fields'); return }
    const schema = latest.current.schema
    const id = ++reqID.current
    setBusy('val')
    try {
      const res = await api.post<{ schema: string }>('/api/v1/editor/contracts/validate', { schema })
      setBusy('idle')
      if (mounted.current && reqID.current === id && latest.current.schema === schema) {
        // Apply the canonical form to the schema only — never touch sample.
        onChange({ ...latest.current, schema: res.schema })
        setTrusted(true)
        setMode('fields')
      }
    } catch {
      setBusy('idle')
      setMsg('Schema invalid; fix the JSON first.')
    }
  }

  async function generate() {
    const sample = latest.current.sample
    const prior = latest.current.schema
    const id = ++reqID.current
    setBusy('gen')
    try {
      const res = await api.post<{ schema: string }>('/api/v1/editor/contracts/infer', { sample })
      setBusy('idle')
      if (mounted.current && scopeAlive() && reqID.current === id &&
        latest.current.sample === sample && latest.current.schema === prior) {
        onChange({ ...latest.current, schema: res.schema })
        setTrusted(true)
        setMode('fields')
        setMsg('')
      } else {
        setMsg('Changed while generating; generate again.')
      }
    } catch {
      setBusy('idle')
      setMsg('Generation failed; check the sample is valid JSON.')
    }
  }

  async function validate() {
    const schema = latest.current.schema
    const id = ++reqID.current
    setBusy('val')
    try {
      const res = await api.post<{ schema: string }>('/api/v1/editor/contracts/validate', { schema })
      setBusy('idle')
      if (mounted.current && scopeAlive() && reqID.current === id && latest.current.schema === schema) {
        onChange({ ...latest.current, schema: res.schema })
        setTrusted(true)
        setMsg('')
      } else {
        setMsg('Changed while validating; validate again.')
      }
    } catch {
      setBusy('idle')
      setMsg('Schema invalid.')
    }
  }

  return (
    <div className="contract-side">
      <label className="build-label">{label} sample <span className="dim">(raw JSON, stays in this tab)</span>
        <textarea
          aria-label={`${label} sample`}
          className={inputCls + ' mono sample-area'}
          rows={5}
          placeholder='{"id":1,"name":"x"}'
          value={side.sample}
          onChange={(e) => onChange({ ...latest.current, sample: e.target.value })}
        />
      </label>
      {over && <Notice tone="warning">Sample over 64 KiB.</Notice>}
      <div className="build-actions">
        <Button size="sm" disabled={busy !== 'idle' || over || side.sample.trim() === ''}
          onClick={() => (side.schema !== '{}' && side.schema !== '' ? setConfirmGen(true) : generate())}>
          <SwapLabel show={busy === 'gen' ? 'Generating' : 'Generate schema'}
            options={['Generate schema', 'Generating']} />
        </Button>
        <Button size="sm" disabled={busy !== 'idle'} onClick={validate}>
          <SwapLabel show={busy === 'val' ? 'Validating' : 'Validate'}
            options={['Validate', 'Validating']} />
        </Button>
      </div>
      {msg && <Notice tone="warning">{msg}</Notice>}
      <div className="build-label">{label} schema</div>
      <div className="build-actions mode-row">
        <button className={'link-btn' + (mode === 'fields' ? ' sel' : '')}
          onClick={enterFields}>Fields</button>
        <button className={'link-btn' + (mode === 'json' ? ' sel' : '')}
          onClick={() => setMode('json')}>JSON</button>
      </div>
      {mode === 'fields' && canFields ? (
        <SchemaFields schema={side.schema}
          onChange={(s) => onChange({ ...latest.current, schema: s })} />
      ) : (
        <label className="build-label">{label} schema JSON
          <textarea
            aria-label={`${label} schema`}
            className={inputCls + ' mono schema-area'}
            rows={8}
            value={side.schema}
            onChange={(e) => {
              onChange({ ...latest.current, schema: e.target.value })
              setTrusted(false)
            }}
          />
        </label>
      )}
      {mode === 'fields' && !precise && (
        <Notice tone="warning">Schema contains numbers JS can't represent exactly; edit JSON directly.</Notice>
      )}
      {mode === 'fields' && !trusted && precise && (
        <Notice tone="warning">Validate the schema before field editing.</Notice>
      )}
      {confirmGen && (
        <Confirm
          title="Replace schema?"
          confirm="Replace"
          pending="Replacing"
          action={async () => { await generate() }}
          onClose={() => setConfirmGen(false)}
        >
          The existing schema will be replaced by the inferred draft.
        </Confirm>
      )}
    </div>
  )
}
