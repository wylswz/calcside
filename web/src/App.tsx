import { useState } from 'react'
import { Route, Routes, Link, NavLink, Navigate, useNavigate, useMatch } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api, loadAuthConfig, User, AuthConfig, AuthKind } from './api'
import Login from './pages/Login'
import Instances from './pages/Instances'
import InstanceDetail from './pages/InstanceDetail'
import ArtifactPreviewPage from './pages/ArtifactPreview'
import Keys from './pages/Keys'
import Policies from './pages/Policies'
import Audit from './pages/Audit'
import Secrets from './pages/Secrets'
import Profile from './pages/Profile'
import { Button, Loading, Logo, Mark, Notice } from './components/ui'

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

const nav = [
  { to: '/', label: 'Instances', end: true },
  { to: '/policies', label: 'Policies' },
  { to: '/keys', label: 'API Keys' },
  { to: '/secrets', label: 'Secrets' },
  { to: '/audit', label: 'Audit' },
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
          <Notice tone="red">
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
      <header className="sticky top-0 z-40 border-b border-ink bg-paper/95 backdrop-blur">
        <div className="mx-auto flex h-14 max-w-7xl items-stretch gap-10 px-6">
          <Link to="/" className="flex items-center gap-2.5">
            <Logo />
            <span className="text-[15px] font-semibold tracking-tight text-ink">calcside</span>
          </Link>
          <nav className="flex flex-1 items-stretch gap-7 overflow-x-auto">
            {nav.map((n, i) => (
              <NavLink key={n.to} to={n.to} end={n.end} className={navCls}>
                <span className="font-mono text-[10px] text-mute">{String(i + 1).padStart(2, '0')}</span>
                {n.label}
              </NavLink>
            ))}
          </nav>
          <div className="flex items-center gap-4">
            {dev && (
              <span className="inline-flex items-center gap-1.5 border border-warn px-2 py-0.5 text-[11px] font-medium uppercase tracking-label text-warn-text">
                <Mark tone="yellow" />dev · anonymous
              </span>
            )}
            <Link to="/profile" className="text-xs text-sec hover:text-ink" title="Profile">{user?.username || user?.email}</Link>
            {!dev && <Button size="sm" onClick={logout}>Logout</Button>}
          </div>
        </div>
      </header>
      <main className="mx-auto max-w-7xl px-6 py-10">
        <Routes>
          <Route path="/" element={<Instances />} />
          <Route path="/instances/:id" element={<InstanceDetail />} />
          <Route path="/keys" element={<Keys />} />
          <Route path="/policies" element={<Policies />} />
          <Route path="/secrets" element={<Secrets />} />
          <Route path="/profile" element={user && <Profile user={user} />} />
          <Route path="/audit" element={<Audit />} />
          <Route path="/login" element={<Navigate to="/" replace />} />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </main>
    </div>
  )
}
