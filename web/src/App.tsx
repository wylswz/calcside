import { Route, Routes, NavLink, Navigate, useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api, User } from './api'
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
    queryFn: () => api.get<{ user: User }>('/api/v1/me').then((r) => r.user),
    retry: false,
  })
}

const navCls = ({ isActive }: { isActive: boolean }) =>
  `px-3 py-1.5 rounded text-sm ${isActive ? 'bg-gray-200 dark:bg-gray-800 font-medium' : 'text-gray-600 dark:text-gray-400 hover:text-gray-900 dark:hover:text-gray-100'}`

export default function App() {
  const { data: user, isLoading, isError } = useMe()
  const navigate = useNavigate()

  if (isLoading) {
    return <div className="p-8 text-sm text-gray-500">loading…</div>
  }
  const onLoginPage = window.location.pathname === '/login'
  if (isError || !user) {
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
          <span className="text-xs text-gray-500 dark:text-gray-400">{user.email}</span>
          <button onClick={logout} className="text-xs px-2 py-1 rounded border border-gray-300 dark:border-gray-700 hover:bg-gray-100 dark:hover:bg-gray-800">
            Logout
          </button>
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
