import { Route, Routes, NavLink, Navigate, useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api, loadAuthConfig, User, AuthConfig, AuthKind } from './api'
import Login from './pages/Login'
import Instances from './pages/Instances'
import InstanceDetail from './pages/InstanceDetail'
import Keys from './pages/Keys'
import Policies from './pages/Policies'
import Audit from './pages/Audit'
import Secrets from './pages/Secrets'

function useMe() {
  return useQuery({
    queryKey: ['me'],
    queryFn: async () => {
      const cfg = await loadAuthConfig()
      const r = await api.get<{ user: User; kind: AuthKind }>('/api/v1/me')
      return { user: r.user, cfg }
    },
    retry: false,
  })
}

const navCls = ({ isActive }: { isActive: boolean }) =>
  `px-3 py-1.5 rounded text-sm ${isActive ? 'bg-gray-200 dark:bg-gray-800 font-medium' : 'text-gray-600 dark:text-gray-400 hover:text-gray-900 dark:hover:text-gray-100'}`

export default function App() {
  const { data, isLoading, isError, error } = useMe()
  const navigate = useNavigate()
  const user = data?.user
  const cfg: AuthConfig | undefined = data?.cfg
  const dev = cfg?.dev_mode === true

  if (isLoading) {
    return <div className="p-8 text-sm text-gray-500">loading…</div>
  }
  const onLoginPage = window.location.pathname === '/login'
  if ((isError || !user) && dev) {
    return (
      <div className="p-8">
        <div className="max-w-md mx-auto rounded border border-red-300 bg-red-50 dark:border-red-900 dark:bg-red-950/40 px-4 py-3 text-sm text-red-800 dark:text-red-300">
          <div className="font-medium mb-1">dev mode — failed to load anonymous session</div>
          {error instanceof Error ? error.message : 'unknown error'}
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

  const logout = async () => {
    await api.post('/auth/logout')
    navigate('/login')
    window.location.reload()
  }

  return (
    <div className="min-h-screen">
      <header className="border-b border-gray-200 dark:border-gray-800">
        <div className="mx-auto max-w-7xl flex items-center gap-2 px-4 py-2">
          <span className="font-semibold text-sm mr-4">calcside</span>
          <nav className="flex gap-1 flex-1">
            <NavLink to="/" end className={navCls}>Instances</NavLink>
            <NavLink to="/policies" className={navCls}>Policies</NavLink>
            <NavLink to="/keys" className={navCls}>API Keys</NavLink>
            <NavLink to="/secrets" className={navCls}>Secrets</NavLink>
            <NavLink to="/audit" className={navCls}>Audit</NavLink>
          </nav>
          {dev && (
            <span className="text-xs px-2 py-0.5 rounded bg-amber-100 text-amber-800 dark:bg-amber-900/40 dark:text-amber-300">
              dev mode · anonymous
            </span>
          )}
          <span className="text-xs text-gray-500 dark:text-gray-400">{user?.email}</span>
          {!dev && (
            <button onClick={logout} className="text-xs px-2 py-1 rounded border border-gray-300 dark:border-gray-700 hover:bg-gray-100 dark:hover:bg-gray-800">
              Logout
            </button>
          )}
        </div>
      </header>
      <main className="mx-auto max-w-7xl px-4 py-4">
        <Routes>
          <Route path="/" element={<Instances />} />
          <Route path="/instances/:id" element={<InstanceDetail />} />
          <Route path="/keys" element={<Keys />} />
          <Route path="/policies" element={<Policies />} />
          <Route path="/secrets" element={<Secrets />} />
          <Route path="/audit" element={<Audit />} />
          <Route path="/login" element={<Navigate to="/" replace />} />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </main>
    </div>
  )
}
