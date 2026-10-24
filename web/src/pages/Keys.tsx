import { useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, APIKey, CreateKeyRequest } from '../api'
import { Button, EmptyRow, Field, Modal, Notice, PageHeader, inputCls } from '../components/ui'
import { SpecForm, type SpecFormHandle } from '../components/SpecForm'
import { fmtTime } from '../format'
import { allowedSummary } from '../activity'
import { claudeCodeSnippet, cursorSnippet, headerSnippet, mcpURL } from '../mcpSnippets'

type Access = 'restricted' | 'full'

// CopyBlock renders copyable text (secret or client snippet) with a
// copy button — one component for every "copy this once" surface.
function CopyBlock({ text, mono }: { text: string; mono?: boolean }) {
  const [copied, setCopied] = useState(false)
  return (
    <div className="flex gap-2">
      <pre className={`pre flex-1 overflow-auto break-all ${mono ? 'border-ink' : ''}`}>{text}</pre>
      <Button onClick={async () => { await navigator.clipboard.writeText(text); setCopied(true) }}>{copied ? 'Copied' : 'Copy'}</Button>
    </div>
  )
}

// SecretBlock shows the one-time secret with a copy button.
// MCPGuide shows the connect instructions once, next to the secret.
function MCPGuide({ secret }: { secret: string }) {
  const url = mcpURL(window.location.origin)
  const tabs = [
    ['Claude Code', claudeCodeSnippet(url, secret)],
    ['Cursor / VS Code', cursorSnippet(url, secret)],
    ['Any client', `URL: ${url}\n${headerSnippet(secret)}`],
  ] as const
  const [tab, setTab] = useState(0)
  return (
    <div className="space-y-2">
      <p className="text-xs text-sec">
        MCP endpoint: <code className="font-mono text-ink">{url}</code> — this restricted key is all an MCP client needs.
      </p>
      <div className="flex gap-1 border-b border-line">
        {tabs.map(([label], i) => (
          <button key={label}
            className={`px-3 py-1.5 text-xs ${i === tab ? 'border-b-2 border-ink font-medium text-ink' : 'text-mute'}`}
            onClick={() => setTab(i)}>{label}</button>
        ))}
      </div>
      <CopyBlock text={tabs[tab][1]} />
    </div>
  )
}

export default function Keys() {
  const qc = useQueryClient()
  const form = useRef<SpecFormHandle>(null)
  const [showNew, setShowNew] = useState(false)
  const [name, setName] = useState('')
  const [days, setDays] = useState(90)
  const [access, setAccess] = useState<Access>('restricted')
  const [maxInstances, setMaxInstances] = useState(2)
  const [secret, setSecret] = useState('')
  const [secretRestricted, setSecretRestricted] = useState(false)
  const [err, setErr] = useState('')

  const { data } = useQuery({
    queryKey: ['keys'],
    queryFn: () => api.get<{ keys: APIKey[] }>('/api/v1/keys'),
  })
  const create = useMutation({
    mutationFn: (body: CreateKeyRequest) => api.post<{ key: APIKey; secret: string }>('/api/v1/keys', body),
    onSuccess: (r) => {
      setSecret(r.secret)
      setSecretRestricted(!!r.key.restricted)
      qc.invalidateQueries({ queryKey: ['keys'] })
    },
    onError: (e: Error) => setErr(e.message),
  })
  const revoke = useMutation({
    mutationFn: (id: string) => api.del(`/api/v1/keys/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['keys'] }),
  })

  const submit = () => {
    setErr('')
    const body: CreateKeyRequest = { name, expires_in_seconds: days * 86400 }
    if (access === 'restricted') {
      const { spec, err: e } = form.current?.buildSpec() ?? {}
      if (e) { setErr(e); return }
      body.spec = spec
      body.max_instances = maxInstances
    }
    create.mutate(body)
  }

  return (
    <div>
      <PageHeader title="API keys"
        actions={<Button variant="primary" onClick={() => { setShowNew(true); setSecret(''); setName(''); setErr(''); setAccess('restricted') }}>New key</Button>}>
        Bearer tokens (<code className="font-mono text-ink">cs_…</code>) for the SDK and agents. A restricted key may only create and use instances from its instance spec; the secret is shown once at creation.
      </PageHeader>
      <div className="tbl-wrap">
        <table className="tbl">
          <thead>
            <tr><th>Name</th><th>Prefix</th><th>Access</th><th>Created</th><th>Expires</th><th>Status</th><th /></tr>
          </thead>
          <tbody>
            {(data?.keys ?? []).map((k) => (
              <tr key={k.id} className={`row-hover ${k.revoked_at ? 'text-mute' : ''}`}>
                <td className={k.revoked_at ? 'line-through' : 'font-medium text-ink'}>{k.name}</td>
                <td className="font-mono text-xs">{k.prefix}…</td>
                <td className="max-w-[320px]">
                  {k.restricted
                    ? <span className="block truncate font-mono text-xs text-sec" title={allowedSummary(k.spec)}>restricted · {allowedSummary(k.spec)}</span>
                    : <span className="font-mono text-xs text-sec">full access</span>}
                </td>
                <td className="whitespace-nowrap text-xs text-sec">{fmtTime(k.created_at)}</td>
                <td className="whitespace-nowrap text-xs text-sec">{k.expires_at ? fmtTime(k.expires_at) : 'never'}</td>
                <td className="text-xs">{k.revoked_at ? <span className="text-mute">revoked</span> : <span className="text-sec">active</span>}</td>
                <td className="text-right">
                  {!k.revoked_at && <Button size="sm" onClick={() => revoke.mutate(k.id)}>Revoke</Button>}
                </td>
              </tr>
            ))}
            {data && data.keys.length === 0 && <EmptyRow cols={7}>no keys</EmptyRow>}
          </tbody>
        </table>
      </div>

      {showNew && (
        <Modal title="New API key" onClose={() => setShowNew(false)} wide={!secret && access === 'restricted'}>
          {!secret ? (
            <div className="space-y-4">
              <div className="grid grid-cols-2 gap-3">
                <Field label="Name"><input className={inputCls} value={name} onChange={(e) => setName(e.target.value)} /></Field>
                <Field label="Expires in (days; 0 = never)"><input type="number" className={inputCls} value={days} onChange={(e) => setDays(Number(e.target.value))} /></Field>
              </div>
              <Field label="Access">
                <div className="flex gap-3">
                  {(['restricted', 'full'] as const).map((a) => (
                    <label key={a} className={`flex flex-1 items-start gap-2 border px-3 py-2 text-xs ${access === a ? 'border-ink' : 'border-line'}`}>
                      <input type="radio" className="mt-0.5" checked={access === a} onChange={() => setAccess(a)} />
                      <span>
                        <span className="block font-medium text-ink">{a === 'restricted' ? 'Restricted' : 'Full access'}</span>
                        <span className="block text-mute">
                          {a === 'restricted'
                            ? 'creates and uses instances from the spec below — for agents'
                            : 'the key can create any instance and use every instance and vault secret reference'}
                        </span>
                      </span>
                    </label>
                  ))}
                </div>
              </Field>
              {access === 'full' && (
                <Notice tone="warning">Full access keys are for trusted automation only.</Notice>
              )}
              {access === 'restricted' && (
                <>
                  <Field label="Max instances">
                    <input type="number" min={1} className={inputCls + ' !w-32'} value={maxInstances} onChange={(e) => setMaxInstances(Number(e.target.value))} />
                  </Field>
                  <SpecForm ref={form} keyMode />
                </>
              )}
              {err && <p className="text-xs text-warn-text">{err}</p>}
              <div className="flex justify-end gap-2 pt-2">
                <Button onClick={() => setShowNew(false)}>Cancel</Button>
                <Button variant="primary" disabled={!name || create.isPending} onClick={submit}>Create</Button>
              </div>
            </div>
          ) : (
            <div className="space-y-4">
              <Notice tone="warning">
                <span className="font-medium text-ink">Copy this secret now — it will never be shown again.</span>
              </Notice>
              <CopyBlock text={secret} mono />
              {secretRestricted
                ? <MCPGuide secret={secret} />
                : <p className="text-xs text-sec">This is a full access key. The MCP endpoint (<code className="font-mono text-ink">{mcpURL(window.location.origin)}</code>) requires a restricted key.</p>}
              <div className="flex justify-end">
                <Button onClick={() => setShowNew(false)}>Done</Button>
              </div>
            </div>
          )}
        </Modal>
      )}
    </div>
  )
}
