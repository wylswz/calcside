import React from 'react'
import * as Dialog from '@radix-ui/react-dialog'
import * as DropdownMenu from '@radix-ui/react-dropdown-menu'
import type { InstanceStatus } from '../api'
import type { Outcome, RunBy } from '../activity'
import { fmtCountdown } from '../format'

import { expiringSoon } from '../nextStep'
// Three signals, each with one meaning and a redundant shape:
//   denied  — red square:   the boundary refused something
//   failed  — yellow triangle: a run or call errored, or time runs out
//   you     — blue circle:  things you did, things you can act on
// Normal outcomes (ok, running, allow) are never coloured.
export type Signal = 'denied' | 'failed' | 'you' | 'live'

const signalFill: Record<Signal, string> = { denied: 'fill-danger', failed: 'fill-warn', you: 'fill-accent', live: 'fill-live' }
const signalText: Record<Signal, string> = { denied: 'text-danger', failed: 'text-warn-text', you: 'text-accent', live: 'text-live' }

export function SignalMark({ signal, className = '' }: { signal: Signal; className?: string }) {
  return (
    <svg viewBox="0 0 10 10" className={`h-2 w-2 shrink-0 ${className}`} aria-hidden>
      {signal === 'you' && <circle cx="5" cy="5" r="5" className={signalFill[signal]} />}
      {signal === 'denied' && <rect x="0.75" y="0.75" width="8.5" height="8.5" className={signalFill[signal]} />}
      {signal === 'failed' && <polygon points="5,0 10,10 0,10" className={signalFill[signal]} />}
      {signal === 'live' && <circle cx="5" cy="5" r="4" className={signalFill[signal]} />}
    </svg>
  )
}

// OutcomeBadge renders a call or run outcome; ok is neutral text, the
// others carry their signal.
export function OutcomeBadge({ outcome, label }: { outcome: Outcome; label?: string }) {
  if (outcome === 'ok') {
    return <span className="whitespace-nowrap text-xs text-sec">{label ?? 'ok'}</span>
  }
  return (
    <span className={`inline-flex items-center gap-1.5 whitespace-nowrap text-xs font-medium ${signalText[outcome]}`}>
      <SignalMark signal={outcome} />
      {label ?? outcome}
    </span>
  )
}

// RunByBadge marks who started a run; only "you" is a signal.
export function RunByBadge({ runBy }: { runBy: RunBy }) {
  if (runBy === 'you') {
    return (
      <span className="inline-flex items-center gap-1.5 whitespace-nowrap text-xs font-medium text-accent">
        <SignalMark signal="you" />
        you
      </span>
    )
  }
  return <span className="whitespace-nowrap text-xs text-sec">{runBy === 'agent' ? 'agent' : '—'}</span>
}

const statusText: Record<InstanceStatus, string> = {
  running: 'running',
  expired: 'expired',
  lost: 'lost',
  deleted: 'deleted',
}

// StatusText renders the instance status plainly — running is the
// common case and stays uncoloured.
export function StatusText({ status }: { status: InstanceStatus }) {
  return <span className="whitespace-nowrap text-xs text-sec">{statusText[status]}</span>
}

// TimeLeft renders the countdown to expiry; under two minutes it becomes
// a failed/expiring signal.
export function TimeLeft({ expiresAt }: { expiresAt: string }) {
  const ms = new Date(expiresAt).getTime() - Date.now()
  if (ms <= 0) return <span className="whitespace-nowrap text-xs text-mute">expired</span>
  const text = fmtCountdown(expiresAt)
  if (ms < 2 * 60 * 1000) {
    return (
      <span className="inline-flex items-center gap-1.5 whitespace-nowrap text-xs font-medium text-warn-text">
        <SignalMark signal="failed" />
        {text} left
      </span>
    )
  }
  return <span className="whitespace-nowrap text-xs text-sec">{text} left</span>
}

export function Icon({ name, className = '' }: { name: 'preview' | 'download' | 'refresh' | 'back' | 'folder' | 'file' | 'code' | 'play' | 'stop' | 'desktop' | 'mobile' | 'chevron'; className?: string }) {
  const paths = {
    preview: <><path d="M2 10s3-6 8-6 8 6 8 6-3 6-8 6-8-6-8-6Z" /><circle cx="10" cy="10" r="2.5" /></>,
    download: <><path d="M10 2v10m-4-4 4 4 4-4M3 13v4h14v-4" /></>,
    refresh: <><path d="M17 8a7 7 0 1 0-1 7M17 3v5h-5" /></>,
    back: <path d="m9 4-6 6 6 6M3 10h14" />,
    folder: <path d="M2 5h6l2 2h8v10H2Z" />,
    file: <><path d="M4 2h8l4 4v12H4Z M12 2v5h4M7 11h6M7 14h4" /></>,
    code: <path d="m6 5-4 5 4 5m8-10 4 5-4 5M11 3 9 17" />,
    play: <path d="m6 3 11 7-11 7Z" />,
    stop: <path d="M5 5h10v10H5Z" />,
    desktop: <><path d="M2 3h16v11H2ZM10 14v4M6 18h8" /></>,
    mobile: <><rect x="5" y="2" width="10" height="16" /><path d="M9 15h2" /></>,
    chevron: <path d="m7 4 6 6-6 6" />,
  }
  return <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="square" strokeLinejoin="miter" className={`h-4 w-4 shrink-0 ${className}`} aria-hidden="true">{paths[name]}</svg>
}

export function Tag({ children }: { children: React.ReactNode }) {
  return <span className="inline-block border border-line px-1.5 py-px font-mono text-[11px] text-sec">{children}</span>
}

// SwapLabel keeps a control the same width while its label changes
// (Copy/Copied, Run/Running…): every option occupies the same grid cell
// and only the current one is visible.
export function SwapLabel({ show, options }: { show: string; options: readonly string[] }) {
  return <span className="swap-label">{options.map((o) => <span key={o} data-on={o === show}>{o}</span>)}</span>
}

// Button forwards its ref so headless primitives (menu triggers) can
// position against it and return focus to it.
export const Button = React.forwardRef<HTMLButtonElement, React.ButtonHTMLAttributes<HTMLButtonElement> & { variant?: 'primary'; size?: 'sm' }>(
  function Button({ variant, size, className, ...rest }, ref) {
    // Material (fill, edge, shadow, motion) comes from material.css.
    const base = 'inline-flex items-center justify-center gap-2 whitespace-nowrap font-medium disabled:pointer-events-none '
    const sz = size === 'sm' ? 'h-7 px-2.5 text-xs' : 'h-8 px-3.5 text-sm'
    return <button ref={ref} data-ui="button" data-variant={variant === 'primary' ? 'primary' : 'default'} className={base + sz + (className ? ' ' + className : '')} {...rest} />
  },
)

export function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="block">
      <span className="mb-1.5 block text-xs font-medium text-sec">{label}</span>
      {children}
    </label>
  )
}

export const inputCls = 'input'

// PageHeader: a strong title, an optional lede, actions at the baseline.
// No numbers, no eyebrows — hierarchy comes from the content.
export function PageHeader({ title, children, actions }: {
  title: React.ReactNode
  children?: React.ReactNode
  actions?: React.ReactNode
}) {
  return (
    <div className="mb-8 flex flex-wrap items-end gap-x-8 gap-y-4">
      <div className="min-w-0 flex-1">
        <h1 className="text-[32px] font-semibold leading-[1.1] tracking-tight text-ink">{title}</h1>
        {children && <p className="mt-3 max-w-3xl text-[15px] leading-relaxed text-sec">{children}</p>}
      </div>
      {actions && <div className="flex items-center gap-2">{actions}</div>}
    </div>
  )
}

// SectionTitle groups related controls without adding another surface edge.
export function SectionTitle({ children, aside, className = '' }: { children: React.ReactNode; aside?: React.ReactNode; className?: string }) {
  return (
    <div data-ui="section-title" className={`mb-3 flex items-center gap-3 ${className}`}>
      <h3 className="text-sm font-semibold text-ink">{children}</h3>
      {aside && <div className="ml-auto flex items-center gap-3 text-xs">{aside}</div>}
    </div>
  )
}

// Notice tones follow the signals: 'warning' is yellow (failed),
// everything else a plain ruled box. Red is reserved for denials.
export function Notice({ tone = 'neutral', children }: { tone?: 'warning' | 'neutral'; children: React.ReactNode }) {
  const bar = { warning: 'border-warn', neutral: 'border-line-strong' }[tone]
  const text = tone === 'warning' ? 'text-warn-text' : 'text-body'
  return <div className={`border-l-4 bg-surface px-4 py-3 text-sm ${bar} ${text}`}>{children}</div>
}

export function EmptyRow({ cols, children }: { cols: number; children: React.ReactNode }) {
  return (
    <tr>
      <td colSpan={cols} className="!py-10 text-center text-sm text-mute">
        {children}
      </td>
    </tr>
  )
}

export function Loading({ label = 'loading' }: { label?: string }) {
  return (
    <div className="flex items-center gap-3 text-sm text-mute">
      <span className="inline-block h-3 w-3 animate-spin border-2 border-mute border-t-transparent rounded-full" aria-hidden />
      {label}
    </div>
  )
}

// Modal is a Radix dialog: focus is trapped and restored, Escape and an
// outside click close it, and the title labels it. Callers mount it
// conditionally rather than through a Dialog.Trigger, so the element that
// opened it is remembered here and focused again on close.
export function Modal({ title, onClose, children, footer, wide }: {
  title: string
  onClose: () => void
  children: React.ReactNode
  footer?: React.ReactNode
  wide?: boolean
}) {
  const [opener] = React.useState(() => document.activeElement as HTMLElement | null)
  return (
    <Dialog.Root open onOpenChange={(open) => { if (!open) onClose() }}>
      <Dialog.Portal>
        <Dialog.Overlay data-ui="dialog-overlay" />
        <Dialog.Content data-ui="dialog-surface" aria-describedby={undefined} className={wide ? 'max-w-5xl' : 'max-w-xl'}
          onCloseAutoFocus={(e) => { e.preventDefault(); if (opener?.isConnected) opener.focus() }}>
          <div className="flex items-center justify-between border-b border-line px-6 py-4">
            <Dialog.Title className="text-lg font-semibold tracking-tight text-ink">{title}</Dialog.Title>
            <Dialog.Close data-ui="dialog-close" aria-label="Close">&times;</Dialog.Close>
          </div>
          <div className="overflow-y-auto px-6 py-5">{children}</div>
          {footer && <div className="flex items-center gap-2 border-t border-line px-6 py-4">{footer}</div>}
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  )
}

// Menu is a Radix dropdown: arrow keys move between items, Escape and an
// outside click close it, and focus returns to the trigger. An item's
// action runs only after the menu has closed and focus is back on the
// trigger, so an item can open a dialog without the closing menu
// dismissing it, and the dialog later returns focus to the trigger.
const MenuAction = React.createContext<(run: () => void) => void>((run) => run())

export function Menu({ trigger, children }: { trigger: React.ReactNode; children: React.ReactNode }) {
  const pending = React.useRef<(() => void) | null>(null)
  const queue = React.useCallback((run: () => void) => { pending.current = run }, [])
  return (
    <DropdownMenu.Root modal={false}>
      <DropdownMenu.Trigger asChild>{trigger}</DropdownMenu.Trigger>
      <DropdownMenu.Portal>
        <DropdownMenu.Content data-ui="menu" align="end" sideOffset={6}
          onCloseAutoFocus={() => {
            const run = pending.current
            pending.current = null
            if (run) window.setTimeout(run, 0)
          }}>
          <MenuAction.Provider value={queue}>{children}</MenuAction.Provider>
        </DropdownMenu.Content>
      </DropdownMenu.Portal>
    </DropdownMenu.Root>
  )
}

export function MenuItem({ onSelect, disabled, tone, children }: { onSelect: () => void; disabled?: boolean; tone?: 'danger'; children: React.ReactNode }) {
  const queue = React.useContext(MenuAction)
  return <DropdownMenu.Item data-ui="menu-item" data-tone={tone} disabled={disabled} onSelect={() => queue(onSelect)}>{children}</DropdownMenu.Item>
}

// InstanceStatus is the one status display: dot + label. Live (green)
// for comfortably-running, expiring (yellow) near the TTL end, hollow
// grey for ended.
export function InstanceStatus({ inst, now }: { inst: { status: string; created_at: string; expires_at: string }; now?: number }) {
  const t = now ?? Date.now()
  let dot: React.ReactNode
  let label: string
  if (inst.status === 'running') {
    const left = new Date(inst.expires_at).getTime() - t
    if (expiringSoon(inst, t)) {
      const m = Math.max(0, Math.floor(left / 60000))
      const sec = Math.max(0, Math.floor((left % 60000) / 1000))
      dot = <span className="inline-block h-2 w-2 rounded-full bg-warn" />
      label = `expiring · ${m}:${String(sec).padStart(2, '0')}`
    } else {
      const mins = Math.floor(left / 60000)
      dot = <span className="inline-block h-2 w-2 rounded-full bg-live" />
      label = mins >= 60 ? `live · ${Math.floor(mins / 60)}h ${mins % 60}m left`
        : mins >= 1 ? `live · ${mins}m left`
          : `live · ${Math.max(0, Math.floor(left / 1000))}s left`
    }
  } else {
    dot = <span className="inline-block h-2 w-2 rounded-full border border-mute bg-transparent" />
    label = inst.status === 'lost' ? 'ended · lost' : 'ended'
  }
  return <span className="inline-flex items-center gap-1.5 text-xs text-sec">{dot}{label}</span>
}
