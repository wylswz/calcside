import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api, Instance, Execution, APIKey, SharedInstance, workspaces } from '../api'
import { instanceName } from '../activity'
import { nextStep } from '../nextStep'
import { useRole } from '../workspace'
import { Button, Loading, SectionTitle, InstanceStatus, Modal, Notice } from '../components/ui'
import ShareDialog from '../components/ShareDialog'
import ConnectAgent from '../components/ConnectAgent'
import { Allowed } from '../components/Allowed'
import { Activity } from '../components/Activity'
import { RunCode } from '../components/RunCode'
import { StatePanel } from '../components/StatePanel'
import { PromptPanel } from '../components/PromptPanel'
import { ArtifactBrowser } from '../components/Artifacts'
import './instance-detail.css'

// NextSignal renders the small semantic marker for each next-step state —
// hollow circle ended, clock expiring, X denied, link connect/waiting.
function NextSignal({ kind }: { kind: 'neutral' | 'expiring' | 'denied' | 'connect' }) {
  const path = {
    neutral: 'M12 4a8 8 0 1 0 0 16 8 8 0 0 0 0-16z',
    expiring: 'M12 4a8 8 0 1 0 0 16 8 8 0 0 0 0-16z M12 8v4l3 2',
    denied: 'M12 4a8 8 0 1 0 0 16 8 8 0 0 0 0-16z M9.5 9.5l5 5 M14.5 9.5l-5 5',
    connect: 'M9 15l6-6 M11 7h3a3 3 0 0 1 0 6h-2 M13 17h-3a3 3 0 0 1 0-6h2',
  }[kind]
  return (
    <svg aria-hidden="true" width="20" height="20" viewBox="0 0 24 24" fill="none"
      stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round">
      {path.split(' M').map((d, i) => <path key={i} d={(i ? 'M' : '') + d} />)}
    </svg>
  )
}

// The instance page shows one next step at a time: connect → waiting →
// runs → denials → expiry → ended. Tools live behind More.
export default function InstanceDetail() {
  const { id = '' } = useParams()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const moreRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const key = (e: KeyboardEvent) => { if (e.key === 'Escape') { setMore(false); setCode(false) } }
    const down = (e: MouseEvent) => { if (moreRef.current && !moreRef.current.contains(e.target as Node)) setMore(false) }
    window.addEventListener('keydown', key); window.addEventListener('mousedown', down)
    return () => { window.removeEventListener('keydown', key); window.removeEventListener('mousedown', down) }
  }, [])
  const role = useRole() // current workspace — destination for create-similar
  const canCreate = role === 'owner' || role === 'editor'
  const [running, setRunning] = useState(false)
  const [share, setShare] = useState(false)
  const [agent, setAgent] = useState(false)
  const [prompt, setPrompt] = useState(false)

  const [more, setMore] = useState(false)
  const [code, setCode] = useState(false)
  const [focusId, setFocusId] = useState<string | undefined>()

  const { data: inst, isError: instErr } = useQuery({
    queryKey: ['instance', id],
    queryFn: () => api.get<{ instance: Instance }>(`/api/v1/instances/${id}`).then((r) => r.instance),
    refetchInterval: 15000,
    retry: 1,
  })
  const { data: wsList } = useQuery({ queryKey: ['workspaces'], queryFn: workspaces })
  // Role must resolve against the INSTANCE's workspace, not the selected one.
  const member = wsList?.find((w) => w.id === inst?.workspace_id)
  const memberReady = wsList !== undefined && inst !== undefined
  const { data: execs } = useQuery({
    queryKey: ['execs', id],
    queryFn: () => api.get<{ executions: Execution[] }>(`/api/v1/instances/${id}/executions`),
    refetchInterval: 10000,
  })
  const { data: shared } = useQuery({
    queryKey: ['shared'],
    queryFn: () => api.get<{ instances: SharedInstance[] }>('/api/v1/instances?shared=true'),
    enabled: memberReady && !member, // no membership → maybe a shared recipient
  })
  const shareRole = member ? undefined : shared?.instances.find((si) => si.instance.id === id)?.role
  const mRole = member?.role
  const canManage = mRole === 'owner' || mRole === 'editor'
  const isOwner = mRole === 'owner'
  const effRun = canManage || shareRole === 'editor'
  const { data: keys } = useQuery({
    queryKey: ['keys', inst?.workspace_id],
    queryFn: () => api.get<{ keys: APIKey[] }>(`/api/v1/keys?workspace=${encodeURIComponent(inst?.workspace_id ?? '')}`),
    enabled: canManage && !!inst?.workspace_id && memberReady,
  })

  const [actErr, setActErr] = useState('')
  const [pending, setPending] = useState<'extend' | 'end' | null>(null)
  const keepalive = async () => {
    setActErr(''); setPending('extend')
    try {
      await api.post(`/api/v1/instances/${id}/keepalive`)
      qc.invalidateQueries({ queryKey: ['instance', id] })
    } catch (e: any) { setActErr(e.message) } finally { setPending(null) }
  }
  const end = async () => {
    if (!window.confirm(`End instance ${instanceName(inst!)}?`)) return
    setActErr(''); setPending('end')
    try {
      await api.del(`/api/v1/instances/${id}`)
      qc.invalidateQueries({ queryKey: ['instances'] })
      navigate('/')
    } catch (e: any) { setActErr(e.message) } finally { setPending(null) }
  }

  if (instErr) return <Notice tone="warning">Could not load this instance — it may be gone or you may not have access.</Notice>
  if (!inst) return <Loading />
  const name = inst.labels?.name?.trim() || instanceName(inst)
  const live = inst.status === 'running'
  const latest = (execs?.executions ?? []).slice().sort((a, b) => +new Date(b.created_at) - +new Date(a.created_at))[0] ?? null
  const hasBoundKey = (keys?.keys ?? []).some((k) => k.instance_id === id && !k.revoked_at && (!k.expires_at || new Date(k.expires_at).getTime() > Date.now()))
  const step = nextStep(inst, latest, hasBoundKey, Date.now())


  const expSecs = step.kind === 'expiring' ? Math.floor(step.seconds) : 0
  const mUrl = `${window.location.origin}/mcp/instances/${id}`

  return (
    <div className="instance-detail">
      <div className="detail-head mb-8 flex flex-wrap items-end gap-x-8 gap-y-4">
        <div className="min-w-0 basis-full flex-1 sm:basis-0">
          <div className="mb-3 text-xs text-sec">
            <Link to="/" className="transition-colors hover:text-accent">← Instances</Link>
          </div>
          <h1 className="break-words text-[32px] font-semibold leading-[1.1] tracking-tight text-ink">{name}</h1>
          {name !== inst.id && <div className="mt-1 font-mono text-xs text-mute">{inst.id}</div>}
          <div className="mt-3 flex items-center gap-3">
            <InstanceStatus inst={inst} />
            {live && effRun && <button className="text-xs text-accent hover:underline disabled:opacity-50" onClick={keepalive} disabled={pending === 'extend'}>{pending === 'extend' ? 'extending…' : 'extend'}</button>}
          </div>
        </div>
        <div className="ml-auto flex items-center gap-2">
          {canCreate && <Button variant="primary" onClick={() => navigate('/instances/new')}>New instance</Button>}
          <div className="relative" ref={moreRef}>
            <Button onClick={() => setMore(!more)} aria-expanded={more} aria-controls="inst-more">More ▾</Button>
            {more && (
              <div id="inst-more" className="more-menu absolute right-0 top-full z-50 mt-1 w-48 bg-paper">
                {isOwner && <button className="block w-full px-3 py-2 text-left text-xs text-ink hover:bg-surface" onClick={() => { setMore(false); setShare(true) }}>Share…</button>}
                <button className="block w-full px-3 py-2 text-left text-xs text-ink hover:bg-surface" onClick={() => { setMore(false); setPrompt(true) }}>Copy agent prompt</button>
                {effRun && inst.status !== 'deleted' && <button className="block w-full px-3 py-2 text-left text-xs text-ink hover:bg-surface" onClick={() => { setMore(false); end() }} disabled={pending === 'end'}>End instance</button>}
              </div>
            )}
          </div>
        </div>
      </div>

      {actErr && <div className="mb-4 text-xs text-warn-text">{actErr}</div>}
      {step.kind === 'ended' && (
        <div className="next-action mb-8">
          <div className="next-signal neutral" aria-hidden="true"><NextSignal kind="neutral" /></div>
          <div className="next-body">
            <h2 className="next-title">This instance has ended.</h2>
            <p className="next-text">Runs and calls stay for review; files and state are gone.</p>
          </div>
          {canCreate && <div className="next-actions"><Button size="sm" variant="primary" onClick={() => navigate(`/instances/new?from=${id}`)}>Create similar</Button></div>}
        </div>
      )}
      {step.kind === 'expiring' && (
        <div className="next-action mb-8">
          <div className="next-signal expiring" aria-hidden="true"><NextSignal kind="expiring" /></div>
          <div className="next-body">
            <h2 className="next-title">Expires in {Math.floor(expSecs / 60)}:{String(expSecs % 60).padStart(2, '0')}.</h2>
          </div>
          {effRun && <div className="next-actions"><Button size="sm" variant="primary" onClick={keepalive} disabled={pending === 'extend'}>Extend</Button></div>}
        </div>
      )}
      {step.kind === 'denied' && (
        <div className="next-action mb-8">
          <div className="next-signal denied" aria-hidden="true"><NextSignal kind="denied" /></div>
          <div className="next-body">
            <h2 className="next-title">The latest run was denied.</h2>
            <p className="next-text">Review the blocked call before changing the boundary.</p>
          </div>
          <div className="next-actions"><Button variant="primary" onClick={() => setFocusId(latest?.id)}>Review run</Button></div>
        </div>
      )}
      {step.kind === 'connect' && (
        <div className="next-action mb-8">
          <div className="next-signal connect" aria-hidden="true"><NextSignal kind="connect" /></div>
          <div className="next-body">
            <h2 className="next-title">Connect an agent</h2>
            <p className="next-text">Give an agent access to exactly this instance — nothing else.</p>
            <div className="next-links">
              <button className="hover:text-ink" onClick={() => setPrompt(true)}>Copy agent prompt</button>
              {canManage && <button className="hover:text-ink" onClick={() => setCode(true)}>Run code yourself</button>}
            </div>
          </div>
          {canManage && <div className="next-actions"><Button variant="primary" onClick={() => setAgent(true)}>Connect agent</Button></div>}
        </div>
      )}
      {step.kind === 'waiting' && (
        <div className="next-action mb-8">
          <div className="next-signal connect" aria-hidden="true"><NextSignal kind="connect" /></div>
          <div className="next-body">
            <h2 className="next-title">Waiting for the agent's first run…</h2>
            <p className="next-text"><span className="font-mono text-xs">{mUrl}</span></p>
          </div>
        </div>
      )}

      {share && <ShareDialog instanceId={id!} workspaceId={inst.workspace_id ?? ''} onClose={() => setShare(false)} />}
      {agent && <ConnectAgent id={id!} workspaceId={inst?.workspace_id} onClose={() => setAgent(false)} />}

      {prompt && (
        <Modal title="Agent prompt" onClose={() => setPrompt(false)} wide footer={
          <Button variant="primary" onClick={async () => {
            const el = document.querySelector<HTMLElement>('#agent-prompt-panel pre, #agent-prompt-panel textarea')
            if (el) await navigator.clipboard.writeText(el.textContent ?? '')
          }}>Copy</Button>
        }>
          <div id="agent-prompt-panel"><PromptPanel id={id} running={live} /></div>
        </Modal>
      )}

      <div className="detail-grid">
        <div className="work-surface min-w-0">
          <section aria-label="Work">
            <h2 className="sr-only">Work</h2>
            {code && <div className="mb-6"><RunCode inst={inst} running={running} setRunning={setRunning} /></div>}
            <Activity id={id} focusId={focusId} aside={
              effRun && live ? <button className="text-xs text-sec hover:text-ink" onClick={() => setCode(!code)}>Run code yourself</button> : undefined
            } />
          </section>
          <section aria-label="Results" className="results-block">
            <h2 className="mb-4 text-lg font-semibold text-ink">Results</h2>
            <div className="results-content">
              {inst.spec?.capabilities?.fs != null && (
                <div>
                  <SectionTitle>Files</SectionTitle>
                  <ArtifactBrowser key={id} id={id} running={live} busy={running} />
                </div>
              )}
              <details className="detail-state-disclosure">
                <summary>State <span className="state-hint">Variables and resources</span></summary>
                <div className="state-body">
                  <StatePanel key={`state-${id}`} id={id} running={live} busy={running} capNames={Object.keys(inst.spec?.capabilities ?? {})} />
                </div>
              </details>
            </div>
          </section>
        </div>
        <aside className="side-col">
          <section aria-label="Boundary">
            <h2>Boundary</h2>
            <p className="side-text mb-3">Fixed at creation.</p>
            <Allowed inst={inst} />
            <p className="side-text mt-3">
              {member ? 'you · workspace members' : 'Access follows workspace membership and explicit shares'}
              {isOwner && <> · <button className="text-accent hover:underline" onClick={() => setShare(true)}>manage</button></>}
            </p>
            {canCreate && (
              <p className="side-text mt-3"><button className="text-accent hover:underline" title="New instance — no files or state copied" onClick={() => navigate(`/instances/new?from=${id}`)}>Create similar</button></p>
            )}
          </section>
          <section aria-label="Agent">
            <h2>Agent</h2>
            {canManage ? (
              hasBoundKey ? (
                <>
                  <p className="side-text">Key ready.</p>
                  <p className="side-name break-all mt-1">{mUrl}</p>
                  {live && <p className="mt-2"><button className="text-xs text-accent hover:underline" onClick={() => setAgent(true)}>New key</button></p>}
                </>
              ) : (
                <>
                  <p className="side-text">No instance key yet.</p>
                  {live && <p className="mt-2"><Button size="sm" onClick={() => setAgent(true)}>Connect agent</Button></p>}
                </>
              )
            ) : (
              <p className="side-text">key availability unknown — this workspace's keys are not visible to you</p>
            )}
          </section>
        </aside>
      </div>
    </div>
  )
}
