import { useState } from 'react'
import { Route, Routes, Link, NavLink, Navigate, useNavigate, useMatch } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api, loadAuthConfig, User, AuthConfig, AuthKind } from './api'
import WorkspaceSwitcher from './components/WorkspaceSwitcher'
import WorkspaceTabs, { WorkspaceRedirect } from './pages/WorkspaceTabs'
import Login from './pages/Login'
import Instances from './pages/Instances'
import NewInstance from './pages/NewInstance'
import InstanceDetail from './pages/InstanceDetail'
import ArtifactPreviewPage from './pages/ArtifactPreview'
import Audit from './pages/Audit'
import Profile from './pages/Profile'
import { Button, Loading, Notice, SignalMark } from './components/ui'

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
const navWork = [
  { to: '/', label: 'Instances', end: true },
  { to: '/audit', label: 'Audit' },
  { to: '/workspace', label: 'Workspace' },
]

const navCls = ({ isActive }: { isActive: boolean }) =>
  `relative flex items-center gap-1.5 whitespace-nowrap text-sm transition-colors ${isActive ? 'font-medium text-ink after:absolute after:inset-x-0 after:-bottom-px after:h-[3px] after:bg-accent' : 'text-sec hover:text-ink'}`

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
    <div className="min-h-screen">
      <header className="console-header sticky top-0 z-40 border-b border-line bg-paper/95 backdrop-blur">
        <div className="console-header-inner mx-auto flex min-h-14 max-w-7xl flex-wrap items-center gap-x-6 gap-y-1 px-6 py-1">
          <Link to="/" className="flex items-center">
            <span className="text-[15px] font-semibold tracking-tight text-ink">calcside</span>
          </Link>
          <nav className="console-nav flex flex-1 items-stretch gap-5 overflow-x-auto sm:gap-7">
            {navWork.map((n) => (
              <NavLink key={n.to} to={n.to} end={n.end} className={navCls}>
                {n.label}
              </NavLink>
            ))}
          </nav>
          <div className="ml-auto flex items-center gap-3 sm:gap-4">
            {dev && (
              <span className="console-dev inline-flex items-center gap-1.5 rounded-md bg-warn/10 px-2 py-1 text-xs font-medium text-warn-text">
                <SignalMark signal="failed" />dev · anonymous
              </span>
            )}
            <WorkspaceSwitcher />
            <Link to="/profile" className="console-profile text-xs text-sec hover:text-ink" title="Profile">{user?.username || user?.email}</Link>
            {!dev && <Button size="sm" onClick={logout}>Logout</Button>}
          </div>
        </div>
      </header>
      <main className="console-main mx-auto max-w-7xl px-6 py-10">
        <Routes>
          <Route path="/" element={<Instances />} />
          <Route path="/instances/new" element={<NewInstance />} />
          <Route path="/instances/:id" element={<InstanceDetail />} />
          <Route path="/workspace" element={<WorkspaceTabs />} />
          <Route path="/workspace/:tab" element={<WorkspaceTabs />} />
          <Route path="/keys" element={<Navigate to="/workspace/keys" replace />} />
          <Route path="/policies" element={<Navigate to="/workspace/policies" replace />} />
          <Route path="/secrets" element={<Navigate to="/workspace/secrets" replace />} />
          <Route path="/profile" element={user && <Profile user={user} />} />
          <Route path="/audit" element={<Audit />} />
          <Route path="/workspaces/:id" element={<WorkspaceRedirect />} />
          <Route path="/login" element={<Navigate to="/" replace />} />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </main>
    </div>
  )
}
