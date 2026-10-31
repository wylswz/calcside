import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, ExtensionInfo, LLMProvider, Secret, wsPath } from '../api'
import { Button, Confirm, EmptyRow, Field, Modal, Notice, PageHeader, SwapLabel, Tag, inputCls } from '../components/ui'
import { fmtTime } from '../format'
import { useRole } from '../workspace'

const NAME_RE = /^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$/
const SECRET_RE = /^\{\{\s*secrets\.([A-Z_][A-Z0-9_]{0,63})\s*\}\}$/

function secretName(p?: LLMProvider): string {
  const v = p?.config?.api_key
  if (typeof v !== 'string') return ''
  return v.match(SECRET_RE)?.[1] ?? ''
}

function defaults(entry?: ExtensionInfo): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  for (const f of entry?.config ?? []) {
    if (f.default !== undefined && f.default !== null) out[f.name] = f.default
  }
  return out
}

function ProviderForm({ provider, entries, secrets, onClose }: {
  provider: LLMProvider | null
  entries: ExtensionInfo[]
  secrets: Secret[]
  onClose: () => void
}) {
  const qc = useQueryClient()
  const initialSource = provider?.source ?? entries[0]?.source ?? ''
  const initialEntry = entries.find((e) => e.source === initialSource)
  const [name, setName] = useState(provider?.name ?? '')
  const [source, setSource] = useState(initialSource)
  const [models, setModels] = useState((provider?.models ?? initialEntry?.models ?? []).join('\n'))
  const [config, setConfig] = useState<Record<string, unknown>>(() => provider ? { ...(provider.config ?? {}) } : defaults(initialEntry))
  const [err, setErr] = useState('')
  const entry = entries.find((e) => e.source === source)
  const save = useMutation({
    mutationFn: () => {
      const body = { name, source, models: models.split(/[\n,]/).map((m) => m.trim()).filter(Boolean), config }
      return provider
        ? api.put<{ provider: LLMProvider }>(`/api/v1/llm-providers/${provider.id}`, body)
        : api.post<{ provider: LLMProvider }>(wsPath('/api/v1/llm-providers'), body)
    },
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['llm-providers'] }); onClose() },
    onError: (e: Error) => setErr(e.message),
  })
  const pickSource = (next: string) => {
    const e = entries.find((x) => x.source === next)
    setSource(next)
    setModels((e?.models ?? []).join('\n'))
    setConfig(defaults(e))
  }
  const setField = (key: string, v: unknown) => setConfig((c) => {
    const next = { ...c }
    if (v === undefined || v === '') delete next[key]
    else next[key] = v
    return next
  })

  const modelList = models.split(/[\n,]/).map((m) => m.trim()).filter(Boolean)
  const baseURL = typeof config.base_url === 'string' ? config.base_url : ''
  const secretFields = (entry?.config ?? []).filter((f) => f.type === 'secret')
  const missingSecret = secretFields.some((f) => {
    const v = config[f.name]
    if (typeof v !== 'string' || !v) return f.default != null && f.default !== ''
    const n = v.match(SECRET_RE)?.[1]
    return !n || !secrets.some((s) => s.name === n)
  })
  const valid = NAME_RE.test(name) && !!entry && modelList.length > 0 && baseURL.trim() !== '' && !missingSecret

  return (
    <Modal title={provider ? `Edit ${provider.name}` : 'New LLM provider'} onClose={onClose} wide footer={
      <>
        <p role="status" aria-live="polite" className={`min-w-0 flex-1 truncate text-xs ${err || !valid ? 'text-warn-text' : 'text-sec'}`}>
          {err ? `Save failed: ${err}` : !NAME_RE.test(name) ? 'Enter a name (letters, digits, _ . -).' : !entry ? 'Choose a provider extension.' : !modelList.length ? 'Add at least one model.' : !baseURL.trim() ? 'Enter a base URL.' : missingSecret ? 'Choose a workspace secret for the API key.' : ''}
        </p>
        <div className="ml-auto flex gap-2">
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" disabled={!valid || save.isPending} onClick={() => save.mutate()}>
            <SwapLabel show={save.isPending ? 'Saving…' : 'Save'} options={['Save', 'Saving…']} />
          </Button>
        </div>
      </>
    }>
      <div className="space-y-4">
        {!entries.length && <Notice tone="warning">No provider extensions are available on this server. Enable a local extension root with the bundled <code>contrib/llm-*</code> providers.</Notice>}
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Name (selected by the llm extension)">
            <input className={inputCls + ' font-mono'} value={name} onChange={(e) => setName(e.target.value)} placeholder="my-openai" />
          </Field>
          <Field label="Provider extension">
            <select className={inputCls} value={source} onChange={(e) => pickSource(e.target.value)}>
              <option value="">Choose a provider</option>
              {entries.map((e) => <option key={e.source} value={e.source}>{e.name} · {e.source}</option>)}
            </select>
          </Field>
        </div>
        <Field label="Models (one per line or comma separated; defaults come from provider metadata)">
          <textarea className={inputCls + ' font-mono text-xs'} rows={4} value={models} onChange={(e) => setModels(e.target.value)} placeholder={'model-id-a\nmodel-id-b'} />
        </Field>
        {(entry?.config ?? []).map((f) => {
          const v = config[f.name]
          if (f.type === 'secret') {
            const picked = typeof v === 'string' ? v.match(SECRET_RE)?.[1] ?? '' : ''
            return (
              <Field key={f.name} label={`${f.name}${f.doc ? ` — ${f.doc}` : ''}`}>
                <select className={inputCls} value={picked} onChange={(e) => setField(f.name, e.target.value ? `{{secrets.${e.target.value}}}` : '')}>
                  <option value="">{f.default ? 'Choose a secret' : 'None'}</option>
                  {secrets.map((s) => <option key={s.id} value={s.name}>{s.name}</option>)}
                </select>
              </Field>
            )
          }
          if (f.type === 'bool') {
            return (
              <Field key={f.name} label={`${f.name}${f.doc ? ` — ${f.doc}` : ''}`}>
                <select className={inputCls} value={v === undefined ? '' : String(v)} onChange={(e) => setField(f.name, e.target.value === '' ? undefined : e.target.value === 'true')}>
                  <option value="">Default</option><option value="true">true</option><option value="false">false</option>
                </select>
              </Field>
            )
          }
          return (
            <Field key={f.name} label={`${f.name}${f.doc ? ` — ${f.doc}` : ''}`}>
              <input className={inputCls + ' font-mono text-xs'} type={f.type === 'int' ? 'number' : 'text'}
                value={typeof v === 'string' || typeof v === 'number' ? v : ''}
                placeholder={f.default == null ? '' : String(f.default)}
                onChange={(e) => setField(f.name, e.target.value === '' ? undefined : f.type === 'int' ? Number(e.target.value) : e.target.value)} />
            </Field>
          )
        })}
        {secretFields.length > 0 && <p className="text-xs text-sec">Secrets are workspace references; add one under <strong>Secrets</strong>.</p>}
        {name && !NAME_RE.test(name) && <p className="text-xs text-warn-text">Name allows letters, digits, _ . - (max 64), starting with a letter or digit.</p>}
      </div>
    </Modal>
  )
}

export default function Providers({ bare }: { bare?: boolean }) {
  const qc = useQueryClient()
  const role = useRole()
  const canEdit = role === 'owner' || role === 'editor'
  const [showNew, setShowNew] = useState(false)
  const [editing, setEditing] = useState<LLMProvider | null>(null)
  const [deleting, setDeleting] = useState<LLMProvider | null>(null)
  const { data } = useQuery({
    queryKey: ['llm-providers'],
    queryFn: () => api.get<{ providers: LLMProvider[] }>(wsPath('/api/v1/llm-providers')),
  })
  const catalog = useQuery({
    queryKey: ['extensions'],
    queryFn: () => api.get<{ extensions: ExtensionInfo[] }>(wsPath('/api/v1/extensions')),
  })
  const secrets = useQuery({
    queryKey: ['secrets'],
    queryFn: () => api.get<{ secrets: Secret[] }>(wsPath('/api/v1/secrets')),
    retry: false,
  })
  const del = useMutation({
    mutationFn: (id: string) => api.del(`/api/v1/llm-providers/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['llm-providers'] }),
  })
  const providers = data?.providers ?? []
  const entries = (catalog.data?.extensions ?? []).filter((e) => e.interface === 'llm')

  return (
    <div>
      {bare && <div className="mb-4 flex items-center justify-between"><p className="text-xs text-sec" /><Button variant="primary" disabled={!canEdit} onClick={() => setShowNew(true)}>New provider</Button></div>}
      {!bare && <PageHeader title="LLM providers" actions={canEdit && <Button variant="primary" onClick={() => setShowNew(true)}>New provider</Button>}>
        Named extensions for the <code className="font-mono text-ink">llm</code> capability; each supplies default models that instances can select.
      </PageHeader>}
      <div className="tbl-wrap">
        <table className="tbl">
          <thead>
            <tr><th>Name</th><th>Provider</th><th>Models</th><th>Config</th><th>API key</th><th>Updated</th><th /></tr>
          </thead>
          <tbody>
            {providers.map((p) => (
              <tr key={p.id} className="row-hover">
                <td className="font-mono text-xs font-medium text-ink">{p.name}</td>
                <td className="font-mono text-xs text-sec">{p.source}</td>
                <td><span className="flex flex-wrap gap-1">{(p.models ?? []).map((m) => <Tag key={m}>{m}</Tag>)}</span></td>
                <td className="max-w-[280px] truncate font-mono text-xs text-sec">{configSummary(p)}</td>
                <td className="font-mono text-xs text-sec">{secretName(p) || <span className="text-mute">none</span>}</td>
                <td className="whitespace-nowrap text-xs text-sec">{p.updated_at ? fmtTime(p.updated_at) : '—'}</td>
                <td className="space-x-2 whitespace-nowrap text-right">
                  {canEdit && <Button size="sm" onClick={() => setEditing(p)}>Edit</Button>}
                  {canEdit && <Button size="sm" variant="danger" onClick={() => setDeleting(p)}>Delete</Button>}
                </td>
              </tr>
            ))}
            {data && providers.length === 0 && <EmptyRow cols={7}>no LLM providers</EmptyRow>}
          </tbody>
        </table>
      </div>
      {showNew && <ProviderForm provider={null} entries={entries} secrets={secrets.data?.secrets ?? []} onClose={() => setShowNew(false)} />}
      {editing && <ProviderForm provider={editing} entries={entries} secrets={secrets.data?.secrets ?? []} onClose={() => setEditing(null)} />}
      {deleting && (
        <Confirm tone="danger" title={`Delete provider ${deleting.name}?`} confirm="Delete" pending="Deleting…"
          action={() => del.mutateAsync(deleting.id)} onClose={() => setDeleting(null)}>
          <p className="text-sm text-body">Instances that selected this provider will fail closed until they are recreated with another one.</p>
        </Confirm>
      )}
    </div>
  )
}

function configSummary(p: LLMProvider): string {
  const entries = Object.entries(p.config ?? {}).filter(([k]) => k !== 'api_key')
  if (!entries.length) return '—'
  return entries.map(([k, v]) => `${k}=${typeof v === 'object' ? JSON.stringify(v) : String(v)}`).join(' · ')
}
