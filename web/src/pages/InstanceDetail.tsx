import { useEffect, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api, Instance } from '../api'
import { instanceName } from '../activity'
import { Button, Loading, SectionTitle, StatusText, TimeLeft } from '../components/ui'
import ShareDialog from '../components/ShareDialog'
import { Allowed } from '../components/Allowed'
import { Activity } from '../components/Activity'
import { RunCode } from '../components/RunCode'
import { StatePanel } from '../components/StatePanel'
import { PromptPanel } from '../components/PromptPanel'
import { ArtifactBrowser } from '../components/Artifacts'

// The instance page follows the supervisor's sequence: the boundary
// (Allowed), what happened (Activity), the results (Files + State),
// and the intervention tools (Run code, Agent prompt) behind the header.
export default function InstanceDetail() {
  const { id = '' } = useParams()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const [running, setRunning] = useState(false)
  const [share, setShare] = useState(false)
  const [panel, setPanel] = useState<'code' | 'prompt' | null>(null)

  const { data: inst } = useQuery({
    queryKey: ['instance', id],
    queryFn: () => api.get<{ instance: Instance }>(`/api/v1/instances/${id}`).then((r) => r.instance),
    refetchInterval: 15000,
  })

  // Esc closes whichever tool panel is open.
  useEffect(() => {
    if (!panel) return
    const h = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setPanel(null)
    }
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  }, [panel])

  const keepalive = async () => {
    await api.post(`/api/v1/instances/${id}/keepalive`)
    qc.invalidateQueries({ queryKey: ['instance', id] })
  }

  const del = async () => {
    if (!window.confirm(`Delete instance ${instanceName(inst!)}? This cannot be undone.`)) return
    await api.del(`/api/v1/instances/${id}`)
    qc.invalidateQueries({ queryKey: ['instances'] })
    navigate('/')
  }

  if (!inst) return <Loading />
  const name = instanceName(inst)
  const live = inst.status === 'running'

  const toggle = (p: 'code' | 'prompt') => setPanel(panel === p ? null : p)

  return (
    <div>
      <div className="mb-8 flex flex-wrap items-end gap-x-8 gap-y-4">
        <div className="min-w-0 flex-1">
          <div className="mb-3 text-xs text-sec">
            <Link to="/" className="transition-colors hover:text-accent">← Instances</Link>
          </div>
          <h1 className="break-all text-[32px] font-semibold leading-[1.1] tracking-tight text-ink">{name}</h1>
          {name !== inst.id && <div className="mt-1 font-mono text-xs text-mute">{inst.id}</div>}
          <div className="mt-3 flex flex-wrap items-center gap-x-5 gap-y-2">
            <StatusText status={inst.status} />
            {live && <TimeLeft expiresAt={inst.expires_at} />}
          </div>
        </div>
        <div className="flex items-center gap-2">
          <Button onClick={() => toggle('code')} aria-expanded={panel === 'code'}>Run code</Button>
          <Button onClick={() => toggle('prompt')} aria-expanded={panel === 'prompt'}>Agent prompt</Button>
          {live && <Button onClick={keepalive}>Keep alive</Button>}
          <Button onClick={() => setShare(true)}>Share</Button>
          {inst.status !== 'deleted' && <Button onClick={del}>Delete</Button>}
        </div>
      </div>

      {panel === 'code' && (
        <div className="mb-10">
          <RunCode inst={inst} running={running} setRunning={setRunning} />
        </div>
      )}
      {share && <ShareDialog instanceId={id!} workspaceId={inst.workspace_id ?? ''} onClose={() => setShare(false)} />}
      {panel === 'prompt' && (
        <div className="mb-10">
          <PromptPanel id={id} running={live} />
        </div>
      )}

      <div className="grid grid-cols-1 gap-10 lg:grid-cols-[minmax(0,1fr)_360px]">
        <div className="min-w-0 space-y-10">
          <Allowed inst={inst} />
          <Activity id={id} />
        </div>
        <aside className="space-y-10">
          <section aria-label="Files">
            <SectionTitle>Files</SectionTitle>
            <ArtifactBrowser key={id} id={id} running={live} busy={running} />
          </section>
          <StatePanel key={`state-${id}`} id={id} running={live} busy={running} capNames={Object.keys(inst.spec?.capabilities ?? {})} />
        </aside>
      </div>
    </div>
  )
}
