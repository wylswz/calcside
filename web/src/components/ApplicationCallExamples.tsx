import { useState } from 'react'
import * as Tabs from '@radix-ui/react-tabs'
import { useQueryClient } from '@tanstack/react-query'
import { api } from '../api'
import { applicationSnippets } from '../applicationSnippets'
import { CopyBlock } from './MCP'
import { Button, Field, Modal, SwapLabel, inputCls } from './ui'

const examples = [
  { id: 'curl', label: 'curl' },
  { id: 'python', label: 'Python' },
  { id: 'javascript', label: 'JavaScript' },
] as const
type Example = typeof examples[number]['id']

const statusHint: Record<string, string> = {
  draft: 'Not published.',
  preparing: 'Deploying…',
  degraded: 'Deployment unhealthy.',
  disabled: 'Deployment disabled.',
}

// Fixed-height panels and shared copy feedback preserve S1/S4 while
// Radix handles tab keyboard navigation and ARIA relationships.
export function ApplicationCallExamples({ appId, input, status, canCreateKey, appName, workspaceId }: {
  appId: string
  input: string
  status: string
  canCreateKey: boolean
  appName: string
  workspaceId: string
}) {
  const [tab, setTab] = useState<Example>('curl')
  const [keyOpen, setKeyOpen] = useState(false)
  const snips = applicationSnippets(window.location.origin, appId, input)
  return (
    <section className="app-examples" aria-label="Request examples">
      <h3 className="app-label">Request examples</h3>
      <Tabs.Root value={tab} onValueChange={(value) => setTab(value as Example)}>
        <Tabs.List className="app-ex-tabs" aria-label="Example language">
          {examples.map(({ id, label }) => (
            <Tabs.Trigger key={id} value={id} className="app-ex-tab">{label}</Tabs.Trigger>
          ))}
        </Tabs.List>
        <Tabs.Content key={tab} value={tab} className="app-ex-panel">
          <CopyBlock text={snips[tab]} preClassName="app-ex-pre" />
        </Tabs.Content>
      </Tabs.Root>
      {status !== 'ready' && <p className="app-dim">{statusHint[status] ?? 'Not ready.'}</p>}
      {canCreateKey && <Button onClick={() => setKeyOpen(true)}>Create API key</Button>}
      {keyOpen && (
        <AppKeyModal appId={appId} appName={appName} workspaceId={workspaceId} onClose={() => setKeyOpen(false)} />
      )}
    </section>
  )
}

// AppKeyModal creates an application-bound API key (view + invoke this
// application only) and shows the secret once.
function AppKeyModal({ appId, appName, workspaceId, onClose }: {
  appId: string
  appName: string
  workspaceId: string
  onClose: () => void
}) {
  const [name, setName] = useState(`${appName} · invoke`)
  const [days, setDays] = useState(90)
  const qc = useQueryClient()
  const [secret, setSecret] = useState('')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const create = async () => {
    setErr(''); setBusy(true)
    try {
      const r = await api.post<{ secret: string }>(`/api/v1/keys?workspace=${workspaceId}`, {
        name, application_id: appId, expires_in_seconds: days * 86400,
      })
      setSecret(r.secret)
      qc.invalidateQueries({ queryKey: ['keys'] })
    } catch (e: any) { setErr(e.message) } finally { setBusy(false) }
  }
  return (
    <Modal title="Create API key" onClose={onClose} dirty={secret ? true : undefined} discardPrompt={secret ? 'Close? The key secret is not shown again.' : undefined} footer={
      secret ? <Button variant="primary" onClick={onClose}>Done</Button>
        : <><Button onClick={onClose}>Cancel</Button><Button variant="primary" onClick={create} disabled={!name.trim() || busy}><SwapLabel show={busy ? 'Creating…' : 'Create key'} options={['Create key', 'Creating…']} /></Button></>
    }>
      {!secret ? (
        <div className="space-y-3 text-sm">
          <Field label="Key name"><input className={inputCls} value={name} onChange={(e) => setName(e.target.value)} /></Field>
          <Field label="Expires in (days; 0 = never)"><input type="number" className={inputCls} value={days} onChange={(e) => setDays(Number(e.target.value))} /></Field>
          {err && <p className="text-xs text-warn-text">{err}</p>}
        </div>
      ) : (
        <div className="space-y-3 text-sm">
          <p className="text-sec">Shown once.</p>
          <CopyBlock text={secret} mono />
        </div>
      )}
    </Modal>
  )
}
