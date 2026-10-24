import React from 'react'
import type { InstanceStatus } from '../api'
import type { Outcome, RunBy } from '../activity'
import { fmtCountdown } from '../format'

// Three signals, each with one meaning and a redundant shape:
//   denied  — red square:   the boundary refused something
//   failed  — yellow triangle: a run or call errored, or time runs out
//   you     — blue circle:  things you did, things you can act on
// Normal outcomes (ok, running, allow) are never coloured.
export type Signal = 'denied' | 'failed' | 'you'

const signalFill: Record<Signal, string> = { denied: 'fill-danger', failed: 'fill-warn', you: 'fill-accent' }
const signalText: Record<Signal, string> = { denied: 'text-danger', failed: 'text-warn-text', you: 'text-accent' }

export function SignalMark({ signal, className = '' }: { signal: Signal; className?: string }) {
  return (
    <svg viewBox="0 0 10 10" className={`h-2 w-2 shrink-0 ${className}`} aria-hidden>
      {signal === 'you' && <circle cx="5" cy="5" r="5" className={signalFill[signal]} />}
      {signal === 'denied' && <rect x="0.75" y="0.75" width="8.5" height="8.5" className={signalFill[signal]} />}
      {signal === 'failed' && <polygon points="5,0 10,10 0,10" className={signalFill[signal]} />}
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

export function Button(
  props: React.ButtonHTMLAttributes<HTMLButtonElement> & { variant?: 'primary'; size?: 'sm' },
) {
  const { variant, size, className, ...rest } = props
  const base =
    'inline-flex items-center justify-center gap-2 whitespace-nowrap border font-medium transition-colors disabled:pointer-events-none disabled:opacity-40 '
  const sz = size === 'sm' ? 'h-7 px-2.5 text-xs ' : 'h-8 px-3.5 text-sm '
  const v =
    variant === 'primary'
      ? 'border-accent bg-accent text-white hover:border-ink hover:bg-ink hover:text-paper'
      : 'border-ink/80 text-ink hover:bg-ink hover:text-paper'
  return <button className={base + sz + v + (className ? ' ' + className : '')} {...rest} />
}

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

// SectionTitle is a small heading sitting on a heavy rule.
export function SectionTitle({ children, aside, className = '' }: { children: React.ReactNode; aside?: React.ReactNode; className?: string }) {
  return (
    <div className={`mb-3 flex items-center gap-3 border-b-2 border-ink pb-2 ${className}`}>
      <h3 className="text-[13px] font-semibold tracking-tight text-ink">{children}</h3>
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

export function Modal({ title, onClose, children, footer, wide }: {
  title: string
  onClose: () => void
  children: React.ReactNode
  footer?: React.ReactNode
  wide?: boolean
}) {
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4" onClick={onClose}>
      <div
        className={`flex max-h-[90vh] w-full flex-col border border-ink bg-paper shadow-lg ${wide ? 'max-w-5xl' : 'max-w-xl'}`}
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-label={title}
      >
        <div className="flex items-center justify-between border-b border-line px-6 py-4">
          <h2 className="text-lg font-semibold tracking-tight text-ink">{title}</h2>
          <button onClick={onClose} aria-label="Close" className="-mr-2 flex h-8 w-8 items-center justify-center text-xl leading-none text-mute transition-colors hover:bg-ink hover:text-paper">&times;</button>
        </div>
        <div className="overflow-y-auto px-6 py-5">{children}</div>
        {footer && <div className="flex items-center gap-2 border-t border-line px-6 py-4">{footer}</div>}
      </div>
    </div>
  )
}
