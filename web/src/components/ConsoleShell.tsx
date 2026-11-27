// ConsoleShell: a persistent 48px activity bar on desktop (≥900px) with
// Radix tooltips, a hamburger drawer below 900px. `.console-main` is the
// page scroller — workbench routes opt out via :has and scroll their own
// panes. One Primary nav landmark at every width.
import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { Link, useLocation, useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import {
  AppWindow, Blocks, Building2, Check, Inbox, Menu as MenuIcon,
  Monitor, Moon, ShieldCheck, Sun, Terminal, type LucideIcon,
} from 'lucide-react'
import WorkspaceSwitcher from './WorkspaceSwitcher'
import { Menu, MenuItem, MenuSeparator, Modal } from './ui'
import { Tooltip } from '../ui/core'
import { api, wsPath, currentWorkspace } from '../api'
import { useTheme, type ThemeMode } from '../theme'
import './consoleShell.css'

type User = { username?: string; email?: string }

const GROUPS: { label: string; items: { to: string; label: string; Icon: LucideIcon }[] }[] = [
  {
    label: 'Build & run',
    items: [
      { to: '/', label: 'Instances', Icon: Terminal },
      { to: '/applications', label: 'Applications', Icon: AppWindow },
      { to: '/modules', label: 'Modules', Icon: Blocks },
    ],
  },
  {
    label: 'Observe',
    items: [
      { to: '/inbox', label: 'Inbox', Icon: Inbox },
      { to: '/audit', label: 'Audit', Icon: ShieldCheck },
    ],
  },
  {
    label: 'Manage',
    items: [
      { to: '/workspace', label: 'Workspace', Icon: Building2 },
    ],
  },
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
          {g.items.map(({ to, label, Icon }) => (
            <Link key={to} to={to} className="shell-link"
              aria-current={isActive(pathname, to) ? 'page' : undefined}
              onClick={onNavigate}>
              <Icon strokeWidth={1.5} aria-hidden="true" />
              <span className="truncate">{label}</span>
              {to === '/inbox' && <InboxBadge />}
            </Link>
          ))}
        </div>
      ))}
    </nav>
  )
}

function ActivityBar() {
  const { pathname } = useLocation()
  return (
    <nav className="shell-activity" aria-label="Primary">
      {ALL_ITEMS.map(({ to, label, Icon }) => (
        <Tooltip key={to} content={label} side="right">
          <Link to={to} className="shell-activity-link"
            aria-label={label}
            aria-current={isActive(pathname, to) ? 'page' : undefined}>
            <Icon strokeWidth={1.5} aria-hidden="true" />
            {to === '/inbox' && <InboxBadge />}
          </Link>
        </Tooltip>
      ))}
    </nav>
  )
}

const THEME_OPTIONS: { mode: ThemeMode; label: string; Icon: typeof Sun }[] = [
  { mode: 'light', label: 'Theme: Light', Icon: Sun },
  { mode: 'dark', label: 'Theme: Dark', Icon: Moon },
  { mode: 'system', label: 'Theme: System', Icon: Monitor },
]

function AccountMenu({ user, dev, onLogout }: { user?: User; dev: boolean; onLogout: () => void }) {
  const navigate = useNavigate()
  const [theme, setTheme] = useTheme()
  const name = user?.username || user?.email || 'account'
  return (
    <Menu
      trigger={
        <button type="button" className="shell-account" aria-label={`Account ${name}`}>
          <span className="shell-avatar" aria-hidden="true">{(name[0] ?? '?').toUpperCase()}</span>
          <span className="shell-account-name truncate">{name}</span>
        </button>
      }>
      <MenuItem onSelect={() => navigate('/profile')}>Profile</MenuItem>
      <MenuSeparator />
      {THEME_OPTIONS.map(({ mode, label, Icon }) => (
        <MenuItem key={mode} onSelect={() => setTheme(mode)}>
          <Icon className="h-3.5 w-3.5 text-sec" strokeWidth={1.5} aria-hidden="true" />
          <span className="flex-1">{label}</span>
          {theme === mode && <Check className="h-3.5 w-3.5 text-accent" strokeWidth={2} aria-hidden="true" />}
        </MenuItem>
      ))}
      {!dev && (
        <>
          <MenuSeparator />
          <MenuItem onSelect={onLogout}>Logout</MenuItem>
        </>
      )}
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
  useLayoutEffect(() => {
    mainRef.current?.scrollTo({ top: 0, left: 0, behavior: 'instant' })
  }, [pathname])
  return (
    <div className="console-shell">
      <div className="shell-canvas">
        {!drawer && <ActivityBar />}
        <div className="shell-body">
          <header className="console-header z-40 border-b border-line">
            <div className="console-header-inner flex h-9 items-center gap-3 px-4">
              <button type="button" className="shell-hamburger" aria-label="Open navigation"
                aria-haspopup="dialog" aria-expanded={drawer} onClick={() => setDrawer(true)}>
                <MenuIcon className="h-5 w-5" strokeWidth={1.5} aria-hidden="true" />
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
