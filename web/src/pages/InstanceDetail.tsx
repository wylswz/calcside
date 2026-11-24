import { useCallback, useEffect, useRef, useState } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api, Instance, Execution, APIKey, SharedInstance, workspaces, InstanceInspect } from '../api'
import { instanceDescription, instanceTitle } from '../activity'
import { nextStep } from '../nextStep'
import { Button, Confirm, Loading, Menu, MenuItem, InstanceStatus, Modal, Notice, SwapLabel } from '../components/ui'
import ShareDialog from '../components/ShareDialog'
import ConnectAgent from '../components/ConnectAgent'
import { Allowed } from '../components/Allowed'
import { Activity } from '../components/Activity'
import { Notebook, MemoryStatus } from '../components/Notebook'
import { StatePanel } from '../components/StatePanel'
import { PromptPanel } from '../components/PromptPanel'
import { ArtifactBrowser } from '../components/Artifacts'
import './instance-detail.css'

// Panel breakpoints: the section outline docks inside the notebook at
// 740px+, the inspector docks beside the work surface at 1240+; below
// those they open as overlays, and under 740 only one may be open.
const OUTLINE_MQ = '(min-width: 740px)'
const INSPECTOR_MQ = '(min-width: 1240px)'
const NARROW_MQ = '(max-width: 739px)'
const media = (q: string) => window.matchMedia(q).matches

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

// The instance page is a workbench: a compact identity row, a one-line
// next-action slot, the view tabs, then a work surface beside a dockable
// inspector. Both view panels and the inspector stay mounted.
// useInstanceInspect owns the inspect snapshot: it never polls — the
// revision changes with each execution, and only then does state
// refresh. One snapshot feeds the inspector's State tab and the
// workbench status bar's memory readout.
function useInstanceInspect(id: string, running: boolean, revision?: string) {
  const query = useQuery({
    queryKey: ['inspect', id],
    queryFn: () => api.get<InstanceInspect>(`/api/v1/instances/${id}/inspect`),
    enabled: running,
    retry: false,
  })
  const { refetch } = query
  // A changed revision marks a new execution. Refetching keeps the old
  // data on screen, so rows never disappear while the new snapshot arrives.
  const seen = useRef(revision)
  useEffect(() => {
    if (!running || revision === undefined || seen.current === revision) return
    seen.current = revision
    void refetch()
  }, [revision, running, refetch])
  return query
}

export default function InstanceDetail() {
  const { id = '' } = useParams()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const [params, setParams] = useSearchParams()
  const view = params.get('view') === 'activity' ? 'activity' : 'notebook'
  const setView = (v: 'notebook' | 'activity') =>
    setParams((p) => { const n = new URLSearchParams(p); n.set('view', v); return n }, { replace: false })
  const [share, setShare] = useState(false)
  const [agent, setAgent] = useState(false)
  const [prompt, setPrompt] = useState(false)
  const [focusId, setFocusId] = useState<string | undefined>()
  const [stateTab, setStateTab] = useState<'state' | 'files'>('state')

  // Panel visibility defaults follow the breakpoints; crossing one
  // resets to that side's default, explicit toggles hold within a side.
  const [outlineOpen, setOutlineOpen] = useState(() => media(OUTLINE_MQ))
  const [inspectorOpen, setInspectorOpen] = useState(() => media(INSPECTOR_MQ))
  const inspectorBtnRef = useRef<HTMLButtonElement | null>(null)
  useEffect(() => {
    const mo = window.matchMedia(OUTLINE_MQ)
    const mi = window.matchMedia(INSPECTOR_MQ)
    const onOutline = () => setOutlineOpen(mo.matches)
    const onInspector = () => setInspectorOpen(mi.matches)
    mo.addEventListener('change', onOutline)
    mi.addEventListener('change', onInspector)
    return () => {
      mo.removeEventListener('change', onOutline)
      mi.removeEventListener('change', onInspector)
    }
  }, [])
  const toggleOutline = useCallback(() => {
    // Under 740 the panels overlay — opening one closes the other.
    if (!outlineOpen && media(NARROW_MQ)) setInspectorOpen(false)
    setOutlineOpen(!outlineOpen)
  }, [outlineOpen])
  const toggleInspector = useCallback(() => {
    if (!inspectorOpen && media(NARROW_MQ)) setOutlineOpen(false)
    setInspectorOpen(!inspectorOpen)
  }, [inspectorOpen])
  // Picking a section in the overlayed outline dismisses it (C1 focus
  // return is handled inside Notebook against the Sections toggle).
  const onOutlineNavigate = useCallback(() => {
    if (media(NARROW_MQ)) setOutlineOpen(false)
  }, [])

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
    refetchInterval: 5000,
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
  const [pending, setPending] = useState<'extend' | null>(null)
  const [ending, setEnding] = useState(false)
  const keepalive = async () => {
    setActErr(''); setPending('extend')
    try {
      await api.post(`/api/v1/instances/${id}/keepalive`)
      qc.invalidateQueries({ queryKey: ['instance', id] })
    } catch (e: any) { setActErr(e.message) } finally { setPending(null) }
  }
  const end = async () => {
    await api.del(`/api/v1/instances/${id}`)
    await qc.invalidateQueries({ queryKey: ['instances'] })
    navigate('/')
  }

  // One inspect snapshot feeds both the inspector's State tab and the
  // workbench status bar's memory readout — no second request, no
  // poll timer.
  const live = inst?.status === 'running'
  const latest = (execs?.executions ?? []).slice().sort((a, b) => +new Date(b.created_at) - +new Date(a.created_at))[0] ?? null
  const stateRevision = latest ? `${latest.id}:${latest.steps}` : undefined
  const inspect = useInstanceInspect(id ?? '', live === true, stateRevision)
  const liveResources = live ? inspect.data?.resources : undefined

  if (instErr) return <Notice tone="warning">Could not load this instance — it may be gone or you may not have access.</Notice>
  if (!inst) return <Loading />
  const name = instanceTitle(inst)
  const description = instanceDescription(inst)
  const hasBoundKey = (keys?.keys ?? []).some((k) => k.instance_id === id && !k.revoked_at && (!k.expires_at || new Date(k.expires_at).getTime() > Date.now()))
  const step = nextStep(inst, latest, hasBoundKey, Date.now())


  const expSecs = step.kind === 'expiring' ? Math.floor(step.seconds) : 0
  const mUrl = `${window.location.origin}/mcp/instances/${id}`

  return (
    <div className="instance-detail">
      <div className="detail-head">
        <div className="detail-id min-w-0 flex-1">
          <div className="detail-crumb text-xs text-sec">
            <Link to="/" className="transition-colors hover:text-accent">← Instances</Link>
          </div>
          <h1 className="break-words font-semibold text-ink" title={name}>{name}</h1>
          {name !== inst.id && <div className="mt-1 truncate font-mono text-xs text-mute" title={inst.id}>{inst.id}</div>}
          {description && <p className="detail-desc text-sm text-sec" title={description}>{description}</p>}
          <div className="mt-2 flex items-center gap-3">
            <InstanceStatus inst={inst} />
            {live && effRun && <button className="text-xs text-accent hover:underline disabled:opacity-50" onClick={keepalive} disabled={pending === 'extend'}><SwapLabel show={pending === 'extend' ? 'extending…' : 'extend'} options={['extend', 'extending…']} /></button>}
          </div>
        </div>
        <div className="detail-actions ml-auto flex items-center gap-2">
          {isOwner && <Button onClick={() => setShare(true)}>Share</Button>}
          <Button onClick={() => setPrompt(true)}>Copy prompt</Button>
          {canManage && live && (
            <Button className="!w-8 !px-0" aria-label="Connect agent" title="Connect agent" onClick={() => setAgent(true)}>
              <NextSignal kind="connect" />
            </Button>
          )}
          {effRun && inst.status !== 'deleted' && (
            <Menu trigger={<Button>More ▾</Button>}>
              <MenuItem tone="danger" onSelect={() => setEnding(true)}>End instance</MenuItem>
            </Menu>
          )}
        </div>
      </div>

      {actErr && <div className="detail-error text-xs text-warn-text">{actErr}</div>}
      {/* One-line next-action slot — a reserved band that renders even
          when no step applies so nothing below it ever moves (S3). */}
      <div className="next-action">
      {step.kind === 'lost' && (
        <>
          <div className="next-signal expiring" aria-hidden="true"><NextSignal kind="expiring" /></div>
          <div className="next-body">
            <h2 className="next-title">Runtime lost — code is retained.</h2>
          </div>
        </>
      )}
      {step.kind === 'expired' && (
        <>
          <div className="next-signal neutral" aria-hidden="true"><NextSignal kind="neutral" /></div>
          <div className="next-body">
            <h2 className="next-title">This instance expired.</h2>
          </div>
        </>
      )}
      {step.kind === 'ended' && (
        <>
          <div className="next-signal neutral" aria-hidden="true"><NextSignal kind="neutral" /></div>
          <div className="next-body">
            <h2 className="next-title">This instance has ended.</h2>
          </div>
        </>
      )}
      {step.kind === 'expiring' && (
        <>
          <div className="next-signal expiring" aria-hidden="true"><NextSignal kind="expiring" /></div>
          <div className="next-body">
            <h2 className="next-title">Expires in {Math.floor(expSecs / 60)}:{String(expSecs % 60).padStart(2, '0')}.</h2>
          </div>
          {effRun && <div className="next-actions"><Button size="sm" variant="primary" onClick={keepalive} disabled={pending === 'extend'}>Extend</Button></div>}
        </>
      )}
      {step.kind === 'denied' && (
        <>
          <div className="next-signal denied" aria-hidden="true"><NextSignal kind="denied" /></div>
          <div className="next-body">
            <h2 className="next-title">The latest run was denied.</h2>
          </div>
          <div className="next-actions"><Button variant="primary" onClick={() => { setFocusId(latest?.id); setView('activity') }}>Review run</Button></div>
        </>
      )}
      {step.kind === 'waiting' && (
        <>
          <div className="next-signal connect" aria-hidden="true"><NextSignal kind="connect" /></div>
          <div className="next-body">
            <h2 className="next-title">Waiting for the agent's first run…</h2>
            <p className="next-text"><span className="font-mono text-xs">{mUrl}</span></p>
          </div>
        </>
      )}
      {step.kind === 'connect' && (
        <>
          <div className="next-signal connect" aria-hidden="true"><NextSignal kind="connect" /></div>
          <div className="next-body">
            <h2 className="next-title">Connect an agent to start working.</h2>
          </div>
          {canManage && live && <div className="next-actions"><Button size="sm" variant="primary" onClick={() => setAgent(true)}>Connect agent</Button></div>}
        </>
      )}
      {step.kind === 'none' && live && <span className="next-ready">Runtime ready</span>}

      </div>

      {ending && (
        <Confirm tone="danger" title={`End instance ${name}?`} confirm="End instance" pending="Ending…" action={end} onClose={() => setEnding(false)}>
          <p className="text-sm text-body">Files and variables are discarded; this cannot be undone.</p>
        </Confirm>
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

      <div className="detail-views">
        <div className="view-tabs flex gap-1" role="tablist" aria-label="Instance views">
          {(['notebook', 'activity'] as const).map((v) => (
            <button key={v} role="tab" aria-selected={view === v} data-on={view === v}
              className="view-tab" onClick={() => setView(v)}>
              {v === 'notebook' ? 'Notebook' : 'Activity'}
            </button>
          ))}
        </div>
        <button type="button" className="panel-toggle" aria-label="Inspector"
          ref={inspectorBtnRef}
          aria-expanded={inspectorOpen} aria-controls="instance-inspector"
          onClick={toggleInspector}>
          <svg aria-hidden="true" width="16" height="16" viewBox="0 0 24 24" fill="none"
            stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round">
            <rect x="3" y="4" width="18" height="16" rx="2" />
            <path d="M15 4v16" />
          </svg>
        </button>
      </div>

      <div className="detail-grid">
        <div className="work-surface min-w-0">
          {/* Both panels stay mounted so drafts, editors and scroll
              survive view switches and polls (C2/C3). */}
          <div className="work-panel" hidden={view !== 'notebook'}>
            <Notebook key={id} id={id} inst={inst} canRun={effRun}
              resources={liveResources}
              visible={view === 'notebook'}
              outlineOpen={outlineOpen} onToggleOutline={toggleOutline}
              onOutlineNavigate={onOutlineNavigate} />
          </div>
          <div className="work-panel" hidden={view !== 'activity'}>
            {/* The status bar is a sibling of the scroller, not inside
                it — one shared footer per view, whichever is on screen. */}
            <div className="work-panel-scroll">
              <Activity id={id} focusId={focusId} />
            </div>
            <div className="nb-statusbar">
              <span className="nb-status"><span className="nb-status-text">Activity</span></span>
              <MemoryStatus resources={liveResources} />
            </div>
          </div>
        </div>
        <aside id="instance-inspector" className="side-col" hidden={!inspectorOpen}>
          <div className="detail-inspector-head">
            <h2>Inspector</h2>
            <button type="button" className="panel-toggle" aria-label="Close inspector"
              onClick={() => { toggleInspector(); inspectorBtnRef.current?.focus({ preventScroll: true }) }}>
              <svg aria-hidden="true" width="14" height="14" viewBox="0 0 24 24" fill="none"
                stroke="currentColor" strokeWidth="1.8" strokeLinecap="round">
                <path d="M6 6l12 12M18 6L6 18" />
              </svg>
            </button>
          </div>
          <div className="detail-inspector-body">
            {/* Files are runtime state like variables — they live beside
                State under one tabbed panel; both stay mounted so the
                browser's directory and selection survive switches (C2/C3). */}
            {inst.spec?.capabilities?.fs != null ? (
              <section aria-label="Live state">
                <div className="state-tabs" role="tablist" aria-label="Live state">
                  <button role="tab" aria-selected={stateTab === 'state'} data-on={stateTab === 'state'} className="view-tab" onClick={() => setStateTab('state')}>State</button>
                  <button role="tab" aria-selected={stateTab === 'files'} data-on={stateTab === 'files'} className="view-tab" onClick={() => setStateTab('files')}>Files</button>
                </div>
                <div hidden={stateTab !== 'state'}>
                  <StatePanel key={`state-${id}`} data={inspect.data} error={inspect.error} running={live} capNames={Object.keys(inst.spec?.capabilities ?? {})} heading={false} />
                </div>
                <div hidden={stateTab !== 'files'}>
                  <ArtifactBrowser key={id} id={id} running={live} busy={false} />
                </div>
              </section>
            ) : (
              <StatePanel key={`state-${id}`} data={inspect.data} error={inspect.error} running={live} capNames={Object.keys(inst.spec?.capabilities ?? {})} />
            )}
            <section aria-label="Boundary">
              <h2>Boundary</h2>
              <Allowed inst={inst} extra={
                <div className="side-kv-row">
                  <dt>Access</dt>
                  <dd>
                    {member ? 'Workspace members' : 'Members and shares'}
                    {isOwner && <> · <button className="text-accent hover:underline" onClick={() => setShare(true)}>manage</button></>}
                  </dd>
                </div>
              } />
            </section>
            <section aria-label="Agent">
              <h2>Agent</h2>
              <dl className="side-kv">
                <div className="side-kv-row">
                  <dt>Key</dt>
                  <dd>{canManage ? (hasBoundKey ? 'Ready' : 'None') : 'Unknown'}</dd>
                </div>
                {hasBoundKey && (
                  <div className="side-kv-row">
                    <dt>Endpoint</dt>
                    <dd className="font-mono break-all">{mUrl}</dd>
                  </div>
                )}
              </dl>
            </section>
          </div>
        </aside>
      </div>
    </div>
  )
}
