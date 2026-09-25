import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { api, AuthConfig } from '../api'

export default function Login() {
  const { data: cfg } = useQuery({
    queryKey: ['auth-config'],
    queryFn: () => api.get<AuthConfig>('/api/v1/auth/config'),
    retry: false,
  })
  const [email, setEmail] = useState('')
  const [err, setErr] = useState('')

  const devLogin = async (e: React.FormEvent) => {
    e.preventDefault()
    setErr('')
    try {
      await api.post('/auth/dev/login', { email })
      window.location.assign('/')
    } catch (ex: any) {
      setErr(ex.message ?? 'login failed')
    }
  }

  return (
    <div className="min-h-screen flex items-center justify-center">
      <div className="w-80 space-y-4 rounded-lg border border-gray-200 dark:border-gray-800 bg-white dark:bg-gray-900 p-6">
        <h1 className="text-lg font-semibold text-center">calcside</h1>
        <a
          href="/auth/google/login"
          className={`block text-center rounded px-3 py-2 text-sm font-medium bg-blue-600 text-white hover:bg-blue-700 ${cfg && !cfg.google ? 'opacity-40 pointer-events-none' : ''}`}
        >
          Sign in with Google
        </a>
        {cfg && !cfg.google && (
          <p className="text-xs text-gray-500 text-center">Google sign-in is not configured</p>
        )}
        {cfg?.dev_login && (
          <>
            <div className="border-t border-gray-200 dark:border-gray-700 pt-3 text-center text-xs text-gray-500">
              dev login
            </div>
            <form onSubmit={devLogin} className="space-y-2">
              <input
                type="email"
                required
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                placeholder="you@example.com"
                className="w-full rounded border border-gray-300 dark:border-gray-700 bg-transparent px-2 py-1.5 text-sm"
              />
              <button className="w-full rounded px-3 py-2 text-sm bg-gray-800 dark:bg-gray-200 text-white dark:text-gray-900">
                Sign in (dev)
              </button>
            </form>
          </>
        )}
        {err && <p className="text-xs text-red-600">{err}</p>}
      </div>
    </div>
  )
}
