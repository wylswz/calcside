import { useEffect } from 'react'
import { useParams, useNavigate, Navigate, NavLink } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { workspaces, currentWorkspace, setWorkspace, Workspace } from '../api'
import { PageHeader, Loading, Notice } from '../components/ui'
import Members from './Members'
import Policies from './Policies'
import Secrets from './Secrets'
import Keys from './Keys'

const tabs = [
  { to: 'members', label: 'Members' },
  { to: 'policies', label: 'Policies' },
  { to: 'secrets', label: 'Secrets' },
  { to: 'keys', label: 'API keys' },
] as const

// Workspace settings for the current workspace: members, policies,
// secrets, keys.
export default function WorkspaceTabs() {
  const { tab = 'members' } = useParams()
  const { data } = useQuery({ queryKey: ['workspaces'], queryFn: workspaces })
  const cur = currentWorkspace()
  const ws: Workspace | undefined = (data ?? []).find((w) => w.id === cur) ?? (data ?? []).find((w) => w.personal)
  if (!ws) return <Loading />
  return (
    <div>
      <PageHeader title={ws.name}>
        Workspace settings for <code className="font-mono text-ink">{ws.id}</code>.
      </PageHeader>
      <nav className="workspace-tabs" aria-label="Workspace settings">
        {tabs.map((t) => (
          <NavLink key={t.to} to={`/workspace/${t.to}`}>
            {t.label}
          </NavLink>
        ))}
      </nav>
      {tab === 'members' && <Members ws={ws} />}
      {tab === 'policies' && <Policies bare />}
      {tab === 'secrets' && <Secrets bare />}
      {tab === 'keys' && <Keys bare />}
      {!tabs.find((t) => t.to === tab) && <Navigate to="/workspace/members" replace />}
    </div>
  )
}

// WorkspaceRedirect selects /workspaces/:id then opens its settings.
export function WorkspaceRedirect() {
  const { id } = useParams()
  const nav = useNavigate()
  const { data } = useQuery({ queryKey: ['workspaces'], queryFn: workspaces })
  const qc = useQueryClient()
  useEffect(() => {
    if (!data) return // wait for memberships before switching
    if (id && data.find((w) => w.id === id)) {
      setWorkspace(id)
      // scoped lists cached under the previous workspace are stale
      for (const k of ['instances', 'members', 'changes', 'keys', 'policies', 'secrets']) {
        qc.invalidateQueries({ queryKey: [k] })
      }
    }
    nav('/workspace/members', { replace: true })
  }, [id, data, nav, qc])
  if (data && id && !data.find((w) => w.id === id)) {
    return <Notice tone="warning">That workspace doesn't exist or you don't have access — showing your workspace.</Notice>
  }
  return <Loading />
}
