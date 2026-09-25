import { useQuery } from '@tanstack/react-query'
import { api, AuthConfig } from '../api'

export default function Login() {
  const { data: cfg } = useQuery({
    queryKey: ['auth-config'],
    queryFn: () => api.get<AuthConfig>('/api/v1/auth/config'),
    retry: false,
  })

  return (
    <div className="min-h-screen flex items-center justify-center">
      <div className="w-80 space-y-4 rounded-lg border border-gray-200 dark:border-gray-800 bg-white dark:bg-gray-900 p-6">
        <h1 className="text-lg font-semibold text-center">calcside</h1>
        {cfg?.dev_mode ? (
          <p className="text-xs text-center text-gray-500">
            The server is running in dev mode — you are signed in anonymously.
          </p>
        ) : (
          <>
            <a
              href="/auth/google/login"
              className={`block text-center rounded px-3 py-2 text-sm font-medium bg-blue-600 text-white hover:bg-blue-700 ${cfg && !cfg.google ? 'opacity-40 pointer-events-none' : ''}`}
            >
              Sign in with Google
            </a>
            {cfg && !cfg.google && (
              <p className="text-xs text-gray-500 text-center">Google sign-in is not configured</p>
            )}
          </>
        )}
      </div>
    </div>
  )
}
