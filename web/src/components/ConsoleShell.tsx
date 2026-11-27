// ConsoleShell: a persistent 48px activity bar on desktop (≥900px), a
// hamburger drawer below that. `.console-main` is the page scroller —
// children fill the canvas; a workbench route (instance detail) opts out
// via :has and scrolls its own panes. One Primary nav landmark (the
// activity bar on desktop, the drawer on mobile).
import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { Link, useLocation, useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import WorkspaceSwitcher from './WorkspaceSwitcher'
import { Menu, MenuItem, Modal } from './ui'
import { api, wsPath, currentWorkspace } from '../api'
import './consoleShell.css'

type User = { username?: string; email?: string }

const GROUPS: { label: string; items: { to: string; label: string; icon: JSX.Element }[] }[] = [
  { label: 'Build & run', items: [
    { to: '/', label: 'Instances', icon: <><rect x="3" y="4" width="18" height="16" rx="2" /><path d="M7 9l3 3-3 3M12 15h5" /></> },
    { to: '/applications', label: 'Applications', icon: <><rect x="4" y="4" width="16" height="16" rx="3" /><path d="M10.5 9.5l4.5 2.5-4.5 2.5z" /></> },
    { to: '/modules', label: 'Modules', icon: <><path d="M12 3l8 4.5v9L12 21l-8-4.5v-9L12 3z" /><path d="M4 7.5l8 4.5 8-4.5M12 12v9" /></> },
  ] },
  { label: 'Observe', items: [
    { to: '/inbox', label: 'Inbox', icon: <><path d="M4 6h16v12H4z" /><path d="M4 13h5l2 2.5h2L15 13h5" /></> },
    { to: '/audit', label: 'Audit', icon: <><path d="M12 3l7 2.8v5.4c0 4.6-3.2 8.2-7 9.8-3.8-1.6-7-5.2-7-9.8V5.8L12 3z" /><path d="M9.5 12l1.8 1.8 3.4-3.6" /></> },
  ] },
  { label: 'Manage', items: [
    { to: '/workspace', label: 'Workspace', icon: <><path d="M4 21h16" /><path d="M6 21V10l6-6 6 6v11" /><path d="M10 21v-5h4v5" /></> },
  ] },
]

const ALL_ITEMS = GROUPS.flatMap((g) => g.items)

function InboxBadge() {
  const ws = currentWorkspace() ?? ''
  const q = useQuery({
    queryKey: ['inbox-count', ws],
    queryFn: () => api.get<{ interrupts?: unknown[] }>(wsPath('/api/v1/interrupts?status=pending&resumable=true')),
    refetchInterval: 5000,
  })
  const n = q.data?.interrupts?.length ?? 0
  // paint nothing at zero — the activity-bar badge is absolutely
  // positioned and the drawer badge is the row's last item, so an
  // empty badge can unmount without shifting anything (S4)
  if (n === 0) return null
  return <span className="inbox-count" aria-live="off">{n}</span>
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

// ActivityBar: the desktop Primary nav — icon-only 40px buttons with
// tooltips; the Inbox badge keeps its reserved box (S4).
function ActivityBar() {
  const { pathname } = useLocation()
  return (
    <nav className="shell-activity" aria-label="Primary">
      {ALL_ITEMS.map((it) => (
        <Link key={it.to} to={it.to} className="shell-activity-link"
          title={it.label} aria-label={it.label}
          aria-current={isActive(pathname, it.to) ? 'page' : undefined}>
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" aria-hidden>{it.icon}</svg>
          {it.to === '/inbox' && <InboxBadge />}
        </Link>
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
  const mainRef = useRef<HTMLElement>(null)
  // The main pane is the scroller: a real pathname change starts at the
  // top (view/query switches on the same route keep their position).
  useLayoutEffect(() => {
    mainRef.current?.scrollTo({ top: 0, left: 0, behavior: 'instant' })
  }, [pathname])
  return (
    <div className="console-shell">
      <div className="shell-canvas">
        {/* while the drawer is open it is the only Primary nav — the
            activity bar unmounts so the landmark stays singular */}
        {!drawer && <ActivityBar />}
        <div className="shell-body">
          <header className="console-header z-40 border-b border-line">
            <div className="console-header-inner flex h-9 items-center gap-3 px-4">
              <button type="button" className="shell-hamburger" aria-label="Open navigation"
                aria-haspopup="dialog" aria-expanded={drawer} onClick={() => setDrawer(true)}>
                <svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" strokeWidth="1.5"><path d="M4 7h16M4 12h16M4 17h16"/></svg>
              </button>
              <Link to="/" className="shell-wordmark text-[15px] font-semibold text-ink">calcside</Link>
              <div className="min-w-0 flex-1"><WorkspaceSwitcher /></div>
              <AccountMenu user={user} dev={dev} onLogout={onLogout} />
            </div>
          </header>
          <main ref={mainRef} tabIndex={-1} className="console-main focus:outline-none">
            <div className="console-content">{children}</div>
          </main>
        </div>
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
