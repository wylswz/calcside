import { useQuery } from '@tanstack/react-query'
import { api, AuthConfig } from '../api'
import { Logo } from '../components/ui'

// Composition is a static Bauhaus arrangement of the three primitives.
function Composition() {
  return (
    <svg viewBox="0 0 400 400" className="h-full w-full" aria-hidden>
      <circle cx="250" cy="170" r="130" fill="#fff" />
      <rect x="60" y="210" width="140" height="140" className="fill-danger" />
      <polygon points="300,250 380,390 220,390" className="fill-warn" />
      <rect x="40" y="40" width="200" height="10" fill="#000" />
      <rect x="40" y="40" width="10" height="120" fill="#000" />
    </svg>
  )
}

export default function Login() {
  const { data: cfg } = useQuery({
    queryKey: ['auth-config'],
    queryFn: () => api.get<AuthConfig>('/api/v1/auth/config'),
    retry: false,
  })

  return (
    <div className="grid min-h-screen md:grid-cols-[1.1fr_1fr]">
      <section className="relative hidden flex-col justify-between overflow-hidden bg-[#0033FF] p-12 text-white md:flex">
        <div className="text-[11px] font-medium uppercase tracking-label text-white/60">calcside · console</div>
        <div className="mx-auto w-full max-w-md py-10">
          <Composition />
        </div>
        <div>
          <h1 className="text-5xl font-semibold leading-[1.05] tracking-tight">
            Sandbox the effects,
            <br />
            not the kernel.
          </h1>
          <div className="mt-8 h-0.5 w-full bg-white/25" />
          <p className="mt-5 text-[15px] text-white/70">A capability-gated runtime for agent-written code.</p>
        </div>
      </section>

      <section className="flex items-center justify-center p-8">
        <div className="w-full max-w-sm">
          <div className="flex items-center gap-2.5">
            <Logo />
            <span className="text-[15px] font-semibold tracking-tight text-ink">calcside</span>
          </div>
          <h2 className="mt-12 text-[32px] font-semibold leading-tight tracking-tight text-ink">Sign in</h2>
          <p className="mt-2 text-sm text-sec">Every side effect is gated, policy-checked and audited.</p>
          <div className="mt-10 border-t-2 border-ink pt-6">
            {cfg?.dev_mode ? (
              <p className="text-sm text-sec">The server is running in dev mode — you are signed in anonymously.</p>
            ) : (
              <>
                <a
                  href="/auth/google/login"
                  className={`flex h-11 items-center justify-between border border-accent bg-accent px-4 text-sm font-medium text-white transition-colors hover:border-ink hover:bg-ink hover:text-paper ${cfg && !cfg.google ? 'pointer-events-none opacity-40' : ''}`}
                >
                  Sign in with Google
                  <span aria-hidden>→</span>
                </a>
                {cfg && !cfg.google && <p className="mt-3 text-xs text-mute">Google sign-in is not configured</p>}
              </>
            )}
          </div>
        </div>
      </section>
    </div>
  )
}
