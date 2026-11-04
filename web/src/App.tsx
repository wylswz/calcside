import { useState } from 'react'
import { Route, Routes, Navigate, useNavigate, useMatch } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api, loadAuthConfig, AuthConfig, User, AuthKind } from './api'
import { ConsoleShell } from './components/ConsoleShell'
import WorkspaceTabs, { WorkspaceRedirect } from './pages/WorkspaceTabs'
import Login from './pages/Login'
import Instances from './pages/Instances'
import Applications from './pages/Applications'
import Modules from './pages/Modules'
import ModuleDetail from './pages/ModuleDetail'
import NewApplication from './pages/NewApplication'
import ApplicationDetail from './pages/ApplicationDetail'
import Inbox from './pages/Inbox'
import RunDetail from './pages/RunDetail'
import NewInstance from './pages/NewInstance'
import InstanceDetail from './pages/InstanceDetail'
import ArtifactPreviewPage from './pages/ArtifactPreview'
import Audit from './pages/Audit'
import Profile from './pages/Profile'
import { Loading, Notice } from './components/ui'

function useMe(onBackendRetrying: (retrying: boolean) => void) {
  return useQuery({
    queryKey: ['me'],
    queryFn: async () => {
      const cfg = await loadAuthConfig(onBackendRetrying)
      const r = await api.get<{ user: User; kind: AuthKind }>('/api/v1/me')
      return { user: r.user, cfg }
    },
    retry: false,
  })
}

// Work pages first; setup pages visually separated — you supervise in
// the first group, you configure in the second.


export default function App() {
  const [backendRetrying, setBackendRetrying] = useState(false)
  const { data, isLoading, isError, error } = useMe(setBackendRetrying)
  const navigate = useNavigate()
  const previewRoute = useMatch('/instances/:id/preview')
  const user = data?.user
  const cfg: AuthConfig | undefined = data?.cfg
  const dev = cfg?.dev_mode === true

  if (isLoading || (backendRetrying && !data)) {
    return (
      <div className="flex min-h-screen items-center justify-center">
        <Loading label={backendRetrying ? 'backend unreachable — retrying…' : 'loading…'} />
      </div>
    )
  }
  const onLoginPage = window.location.pathname === '/login'
  if ((isError || !user) && dev) {
    return (
      <div className="flex min-h-screen items-center justify-center p-8">
        <div className="w-full max-w-md">
          <Notice tone="warning">
            <div className="mb-1 font-semibold">dev mode — failed to load anonymous session</div>
            {error instanceof Error ? error.message : 'unknown error'}
          </Notice>
        </div>
      </div>
    )
  }
  if ((isError || !user) && !dev) {
    if (!onLoginPage) return <Navigate to="/login" replace />
    return (
      <Routes>
        <Route path="/login" element={<Login />} />
      </Routes>
    )
  }

  if (previewRoute) return <Routes><Route path="/instances/:id/preview" element={<ArtifactPreviewPage dev={dev} />} /></Routes>

  const logout = async () => {
    await api.post('/auth/logout')
    navigate('/login')
    window.location.reload()
  }

  return (
    <ConsoleShell user={user} dev={dev} onLogout={logout}>
      <Routes>
        <Route path="/" element={<Instances />} />      <Route path="/instances/new" element={<NewInstance />} />
        <Route path="/instances/:id" element={<InstanceDetail />} />
        <Route path="/instances/:id/build" element={<Navigate to="/applications/new" replace />} />
        <Route path="/applications" element={<Applications />} />
        <Route path="/applications/new" element={<NewApplication />} />
        <Route path="/applications/:id" element={<ApplicationDetail />} />
        <Route path="/modules" element={<Modules />} />
        <Route path="/modules/:id" element={<ModuleDetail />} />
        <Route path="/applications/:id/runs/:runId" element={<RunDetail />} />
        <Route path="/inbox" element={<Inbox />} />
        <Route path="/workspace" element={<WorkspaceTabs />} />
        <Route path="/workspace/:tab" element={<WorkspaceTabs />} />
        <Route path="/keys" element={<Navigate to="/workspace/keys" replace />} />
        <Route path="/policies" element={<Navigate to="/workspace/policies" replace />} />
        <Route path="/secrets" element={<Navigate to="/workspace/secrets" replace />} />
        <Route path="/providers" element={<Navigate to="/workspace/providers" replace />} />
        <Route path="/profile" element={user && <Profile user={user} />} />
        <Route path="/audit" element={<Audit />} />
        <Route path="/workspaces/:id" element={<WorkspaceRedirect />} />
        <Route path="/login" element={<Navigate to="/" replace />} />
        <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>

    </ConsoleShell>
  )
}
