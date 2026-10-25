import { useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { api, AuthConfig } from '../api'
import { Button, Field, inputCls, Loading, Notice, SwapLabel } from '../components/ui'

export default function Login() {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const { data: cfg, isLoading, error: configError } = useQuery({
    queryKey: ['auth-config'],
    queryFn: () => api.get<AuthConfig>('/api/v1/auth/config'),
    retry: false,
  })
  const login = useMutation({
    mutationFn: () => api.basicLogin(username, password),
    onSuccess: () => {
      setPassword('')
      window.location.replace('/')
    },
  })

  return (
    <div className="grid min-h-screen md:grid-cols-[1.1fr_1fr]">
      <section className="relative hidden flex-col justify-between overflow-hidden bg-ink p-12 text-paper md:flex">
        <div className="text-[11px] font-medium uppercase tracking-label text-paper/60">calcside · console</div>
        <div>
          <h1 className="text-5xl font-semibold leading-[1.05] tracking-tight">
            Sandbox the effects.
          </h1>
          <div className="mt-8 h-0.5 w-full bg-paper/25" />
          <p className="mt-5 text-[15px] text-paper/70">A capability-gated runtime for agent-written code.</p>
        </div>
      </section>

      <section className="flex items-center justify-center p-8">
        <div className="w-full max-w-sm">
          <span className="text-[15px] font-semibold tracking-tight text-ink">calcside</span>
          <h2 className="mt-12 text-[32px] font-semibold leading-tight tracking-tight text-ink">Sign in</h2>
          <div className="mt-10 border-t-2 border-ink pt-6">
            {new URLSearchParams(window.location.search).get('password_changed') === '1' && (
              <div className="mb-6"><Notice tone="neutral">Password changed. Sign in with your new password.</Notice></div>
            )}
            {isLoading ? <Loading label="loading sign-in methods…" /> : configError ? (
              <Notice tone="warning">Unable to load sign-in methods. Please refresh to try again.</Notice>
            ) : cfg?.dev_mode ? (
              <p className="text-sm text-sec">The server is running in dev mode — you are signed in anonymously.</p>
            ) : (
              <>
                {cfg?.basic && (
                  <form className="space-y-4" onSubmit={(e) => { e.preventDefault(); login.mutate() }}>
                    <Field label="Username or email">
                      <input
                        className={inputCls}
                        name="username"
                        autoComplete="username"
                        autoCapitalize="none"
                        spellCheck={false}
                        required
                        maxLength={254}
                        value={username}
                        onChange={(e) => setUsername(e.target.value)}
                        disabled={login.isPending}
                      />
                    </Field>
                    <Field label="Password">
                      <input
                        className={inputCls}
                        type="password"
                        name="password"
                        autoComplete="current-password"
                        required
                        value={password}
                        onChange={(e) => setPassword(e.target.value)}
                        disabled={login.isPending}
                      />
                    </Field>
                    {login.error && <div role="alert"><Notice tone="warning">{login.error.message}</Notice></div>}
                    <Button type="submit" variant="primary" className="h-11 w-full" disabled={login.isPending}>
                      <SwapLabel show={login.isPending ? 'Signing in…' : 'Sign in'} options={['Sign in', 'Signing in…']} />
                    </Button>
                  </form>
                )}
                {cfg?.basic && cfg.google && <div className="my-6 text-center text-xs text-mute">or</div>}
                {cfg?.google && (
                  <a
                    href="/auth/google/login"
                    className="flex h-11 items-center justify-between border border-accent bg-accent px-4 text-sm font-medium text-white transition-colors hover:border-ink hover:bg-ink hover:text-paper"
                  >
                    Sign in with Google
                    <span aria-hidden>→</span>
                  </a>
                )}
                {cfg && !cfg.google && !cfg.basic && <p className="text-sm text-mute">No sign-in methods are configured. Contact your administrator.</p>}
              </>
            )}
          </div>
        </div>
      </section>
    </div>
  )
}
