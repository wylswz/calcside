// ConsoleShell: desktop left sidebar + canvas topbar; mobile hamburger
// drawer (shared Modal, left placement). One Primary nav landmark.
import { useEffect, useState } from 'react'
import { Link, useLocation, useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import WorkspaceSwitcher from './WorkspaceSwitcher'
import { Menu, MenuItem, Modal } from './ui'
import { api, wsPath, currentWorkspace } from '../api'
import './consoleShell.css'

type User = { username?: string; email?: string }

const GROUPS: { label: string; items: { to: string; label: string; icon: JSX.Element }[] }[] = [
  { label: 'Build & run', items: [
    { to: '/', label: 'Instances', icon: <path d="M4 6h16M4 12h16M4 18h10" /> },
    { to: '/applications', label: 'Applications', icon: <rect x="4" y="4" width="16" height="16" rx="3" /> },
    { to: '/modules', label: 'Modules', icon: <path d="M8 4l8 8-8 8" /> },
  ] },
  { label: 'Observe', items: [
    { to: '/inbox', label: 'Inbox', icon: <path d="M4 6h16v12H4zM4 12h6l2 2h4l2-2h2" /> },
    { to: '/audit', label: 'Audit', icon: <path d="M6 4h12v16H6zM9 9h6M9 13h6" /> },
  ] },
  { label: 'Manage', items: [
    { to: '/workspace', label: 'Workspace', icon: <path d="M4 8h16M8 8v10M4 8l3-4h10l3 4M4 8v10h16V8" /> },
  ] },
]

function InboxBadge() {
  const ws = currentWorkspace() ?? ''
  const q = useQuery({
    queryKey: ['inbox-count', ws],
    queryFn: () => api.get<{ interrupts?: unknown[] }>(wsPath('/api/v1/interrupts?status=pending&resumable=true')),
    refetchInterval: 5000,
  })
  const n = q.data?.interrupts?.length ?? 0
  return <span className="inbox-count" aria-live="off">{n > 0 ? n : ''}</span>
}

const isActive = (pathname: string, to: string) =>
  to === '/' ? pathname === '/' || pathname.startsWith('/instances/')
    : pathname === to || pathname.startsWith(to + '/')

function NavLinks({ onNavigate }: { onNavigate?: () => void }) {
  const { pathname } = useLocation()
  return (
    <nav className="shell-nav" aria-label="Primary">
      {GROUPS.map((g) => (
        <div key={g.label} className="shell-group">
          <div className="shell-group-label">{g.label}</div>
          {g.items.map((it) => (
            <Link key={it.to} to={it.to} className="shell-link"
              aria-current={isActive(pathname, it.to) ? 'page' : undefined}
              onClick={onNavigate}>
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" aria-hidden>{it.icon}</svg>
              <span className="truncate">{it.label}</span>
              {it.to === '/inbox' && <InboxBadge />}
            </Link>
          ))}
        </div>
      ))}
    </nav>
  )
}

function AccountMenu({ user, dev, onLogout }: { user?: User; dev: boolean; onLogout: () => void }) {
  const navigate = useNavigate()
  const name = user?.username || user?.email || 'account'
  return (
    <Menu
      trigger={
        <button type="button" className="shell-account" aria-label={`Account ${name}`}>
          <span className="shell-avatar" aria-hidden>{(name[0] ?? '?').toUpperCase()}</span>
          <span className="shell-account-name truncate">{name}</span>
        </button>
      }>
      <MenuItem onSelect={() => navigate('/profile')}>Profile</MenuItem>
      {!dev && <MenuItem onSelect={onLogout}>Logout</MenuItem>}
    </Menu>
  )
}

export function ConsoleShell({ user, dev, onLogout, children }: {
  user?: User; dev: boolean; onLogout: () => void; children: React.ReactNode
}) {
  const { pathname } = useLocation()
  const [drawer, setDrawer] = useState(false)
  useEffect(() => { setDrawer(false) }, [pathname])
  useEffect(() => {
    const mq = window.matchMedia('(min-width: 1024px)')
    const h = () => { if (mq.matches) setDrawer(false) }
    mq.addEventListener('change', h)
    return () => mq.removeEventListener('change', h)
  }, [])
  return (
    <div className="console-shell">
      <aside className="shell-sidebar">
        <Link to="/" className="shell-brand">calcside</Link>
        <NavLinks />
        <div className="shell-footer">
          {dev && <span className="console-dev text-xs text-mute">dev · anonymous</span>}
        </div>
      </aside>
      <div className="shell-canvas flex min-h-screen min-w-0 flex-col">
        <header className="console-header sticky top-0 z-40 border-b border-line bg-paper/95 backdrop-blur">
          <div className="console-header-inner flex h-14 items-center gap-3 px-4">
            <button type="button" className="shell-hamburger" aria-label="Open navigation" onClick={() => setDrawer(true)}>
              <svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" strokeWidth="1.5"><path d="M4 7h16M4 12h16M4 17h16"/></svg>
            </button>
            <Link to="/" className="shell-wordmark lg:hidden text-[15px] font-semibold text-ink">calcside</Link>
            <div className="min-w-0 flex-1"><WorkspaceSwitcher /></div>
            <AccountMenu user={user} dev={dev} onLogout={onLogout} />
          </div>
        </header>
        <main tabIndex={-1} className="console-main mx-auto w-full max-w-7xl flex-1 px-4 py-10 focus:outline-none sm:px-6">
          {children}
        </main>
      </div>
      {drawer && (
        <Modal title="Navigation" placement="left" dirty={false} onClose={() => setDrawer(false)}>
          <div className="-mx-6 -my-5">
            <NavLinks onNavigate={() => setDrawer(false)} />
            {dev && <div className="shell-footer text-xs text-mute">dev · anonymous</div>}
          </div>
        </Modal>
      )}
    </div>
  )
}
