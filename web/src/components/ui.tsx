import React from 'react'
import * as Dialog from '@radix-ui/react-dialog'
import * as DropdownMenu from '@radix-ui/react-dropdown-menu'
import { Link } from 'react-router-dom'
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

const signalFill: Record<Signal, string> = { denied: 'fill-danger', failed: 'fill-warn-text', you: 'fill-accent', live: 'fill-live' }
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

export function Icon({ name, className = '' }: { name: 'preview' | 'download' | 'upload' | 'refresh' | 'back' | 'folder' | 'file' | 'code' | 'play' | 'stop' | 'desktop' | 'mobile' | 'chevron' | 'publish' | 'more' | 'trash' | 'key' | 'check-circle' | 'alert-triangle' | 'clock' | 'circle-dashed' | 'circle-slash' | 'send' | 'history' | 'check' | 'users' | 'clipboard' | 'link'; className?: string }) {
  const paths = {
    preview: <><path d="M2 10s3-6 8-6 8 6 8 6-3 6-8 6-8-6-8-6Z" /><circle cx="10" cy="10" r="2.5" /></>,
    download: <><path d="M10 2v10m-4-4 4 4 4-4M3 13v4h14v-4" /></>,
    upload: <><path d="M10 12V2m-4 4 4-4 4 4M3 13v4h14v-4" /></>,
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
    publish: <path d="M10 12V4m-3.5 3.5L10 4l3.5 3.5M4 13v4h12v-4" />,
    more: <><circle cx="4.5" cy="10" r="1.2" fill="currentColor" stroke="none" /><circle cx="10" cy="10" r="1.2" fill="currentColor" stroke="none" /><circle cx="15.5" cy="10" r="1.2" fill="currentColor" stroke="none" /></>,
    trash: <path d="M3 5h14M8 5V3h4v2M5 5l1 12h8l1-12" />,
    key: <><circle cx="7" cy="12" r="3" /><path d="M10 12h7v3M14 12v2.5" /></>,
    'check-circle': <><circle cx="10" cy="10" r="7" /><path d="m7 10 2 2 4-4" /></>,
    'alert-triangle': <path d="M10 3 18 17H2L10 3ZM10 8v4M10 14.5v.5" />,
    clock: <><circle cx="10" cy="10" r="7" /><path d="M10 6v4l3 2" /></>,
    'circle-dashed': <circle cx="10" cy="10" r="7" strokeDasharray="2.5 2" />,
    'circle-slash': <><circle cx="10" cy="10" r="7" /><path d="m5.5 5.5 9 9" /></>,
    send: <path d="M17 3 3 9.5l6 2 2 6L17 3ZM9 11.5 17 3" />,
    history: <><path d="M3 10a7 7 0 1 1 2 5M3 10V6m0 4h4" /><path d="M10 7v3l2.5 1.5" /></>,
    check: <path d="m4 10.5 4 4 8-9" />,
    users: <><circle cx="7.5" cy="7" r="2.5" /><path d="M3 16c0-2.5 2-4 4.5-4s4.5 1.5 4.5 4M13 4.5a2.5 2.5 0 0 1 0 5M14.5 12.3c1.5.5 2.5 1.8 2.5 3.7" /></>,
    clipboard: <><path d="M7.5 2.5h5v3h-5Z" /><path d="M7.5 4H4v13.5h12V4h-3.5M7 10h6M7 13h4" /></>,
    link: <path d="m8 12 4-4M9.5 5.5l1-1a3.2 3.2 0 0 1 4.5 4.5l-1 1M10.5 14.5l-1 1A3.2 3.2 0 0 1 5 11l1-1" />,
  }
  return <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="square" strokeLinejoin="miter" className={`h-4 w-4 shrink-0 ${className}`} aria-hidden="true">{paths[name]}</svg>
}

export function Tag({ children }: { children: React.ReactNode }) {
  return <span className="inline-block rounded-[4px] border border-line bg-surface px-1.5 py-px font-mono text-[11px] leading-4 text-sec">{children}</span>
}

// SwapLabel keeps a control the same width while its label changes
// (Copy/Copied, Run/Running…): every option occupies the same grid cell
// and only the current one is visible.
export function SwapLabel({ show, options }: { show: string; options: readonly string[] }) {
  return <span className="swap-label">{options.map((o) => <span key={o} data-on={o === show}>{o}</span>)}</span>
}

// RevealNew offers the rows a held list is keeping back (S5) in a slot
// wide enough for every count, so appearing never moves the filters or
// the primary action beside it (S3/S4). Counts stop at 999+ so the width
// stays bounded.
export function RevealNew({ count, moved, onClick }: { count: number; moved: boolean; onClick: () => void }) {
  const label = count > 0 ? `Show ${count > 999 ? '999+' : count} new` : 'Show latest'
  const options = ['Show latest', 'Show 999+ new']
  if (!options.includes(label)) options.push(label)
  return (
    <button className="whitespace-nowrap text-btn tabular-nums" style={{ visibility: count || moved ? 'visible' : 'hidden' }} onClick={onClick}>
      <SwapLabel show={label} options={options} />
    </button>
  )
}

// Button forwards its ref so headless primitives (menu triggers) can
// position against it and return focus to it.
export const Button = React.forwardRef<HTMLButtonElement, React.ButtonHTMLAttributes<HTMLButtonElement> & { variant?: 'primary' | 'danger'; size?: 'sm' }>(
  function Button({ variant, size, className, ...rest }, ref) {
    // Material (fill, edge, shadow, motion) comes from material.css.
    const base = 'inline-flex items-center justify-center gap-2 whitespace-nowrap font-medium tracking-[-0.005em] disabled:pointer-events-none '
    const sz = size === 'sm' ? 'h-7 px-3 text-xs' : 'h-8 px-3.5 text-[13px]'
    return <button ref={ref} data-ui="button" data-variant={variant ?? 'default'} className={base + sz + (className ? ' ' + className : '')} {...rest} />
  },
)

// Icon actions in a DetailHeader announce their name in the header's
// caption slot (where the status sits) instead of widening or overlaying,
// so no button ever moves or is covered while the pointer travels across
// the row (S1/S3/F1).
type Hint = { show: (label: string, now?: boolean) => void; clear: () => void; register: (label: string) => () => void }
const HintContext = React.createContext<Hint | null>(null)

// IconAction is a fixed-size icon button. Inside a DetailHeader its name
// appears in the caption slot on hover/focus; elsewhere it falls back to
// the native tooltip. It forwards its ref so it can be a menu trigger.
export const IconAction = React.forwardRef<HTMLButtonElement, React.ButtonHTMLAttributes<HTMLButtonElement> & {
  icon: React.ReactNode
  label: string
  variant?: 'primary' | 'danger'
}>(function IconAction({ icon, label, variant, className, onPointerEnter, onFocus, onBlur, ...rest }, ref) {
  const hint = React.useContext(HintContext)
  React.useEffect(() => hint?.register(label), [hint, label])
  return (
    <Button ref={ref} variant={variant} aria-label={rest['aria-label'] ?? label} title={hint ? undefined : label}
      className={`!w-8 !px-0${className ? ' ' + className : ''}`}
      onPointerEnter={(e) => { hint?.show(label); onPointerEnter?.(e) }}
      onFocus={(e) => { if (e.currentTarget.matches(':focus-visible')) hint?.show(label, true); onFocus?.(e) }}
      onBlur={(e) => { hint?.clear(); onBlur?.(e) }}
      {...rest}>
      {icon}
    </Button>
  )
})

// DetailHeader is the one header for an object's own page (instance,
// application): breadcrumb with the id, the title on the left, and on the
// same line a caption slot (status at rest, the hovered action's name
// while pointing at an icon) followed by the actions. Only the
// description sits under the title. The slot reserves the width of its
// longest text, so swapping never moves anything (S4).
export function DetailHeader({ back, id, title, status, actions, description }: {
  back: { to: string; label: string }
  id: string
  title: React.ReactNode
  status?: React.ReactNode
  actions?: React.ReactNode
  description?: React.ReactNode
}) {
  const [hint, setHint] = React.useState<string | null>(null)
  const [labels, setLabels] = React.useState<string[]>([])
  const current = React.useRef<string | null>(null)
  const timer = React.useRef<number | undefined>(undefined)
  const group = React.useRef<HTMLDivElement>(null)
  const ctx = React.useMemo<Hint>(() => {
    const set = (v: string | null) => { current.current = v; setHint(v) }
    return {
      // a short intent delay keeps a pointer sweeping past from flashing
      // names; once a name shows, moving to the next icon swaps at once
      show: (label, now) => {
        window.clearTimeout(timer.current)
        if (now || current.current !== null) set(label)
        else timer.current = window.setTimeout(() => set(label), 150)
      },
      clear: () => {
        window.clearTimeout(timer.current)
        if (!group.current?.matches(':hover')) set(null)
      },
      register: (label) => {
        setLabels((ls) => (ls.includes(label) ? ls : [...ls, label]))
        return () => setLabels((ls) => ls.filter((l) => l !== label))
      },
    }
  }, [])
  React.useEffect(() => () => window.clearTimeout(timer.current), [])
  return (
    <header className="detail-header">
      <p className="detail-crumb">
        <Link to={back.to} className="link">← {back.label}</Link>
        <span className="text-mute"> / <span className="font-mono">{id}</span></span>
      </p>
      <div className="detail-title-row">
        <h1 className="detail-title">{title}</h1>
        <div className="detail-actions" ref={group}
          onPointerLeave={() => { window.clearTimeout(timer.current); current.current = null; setHint(null) }}>
          <span className="detail-caption" data-hint={hint !== null ? 'on' : 'off'}>
            <span className="detail-caption-status">{status}</span>
            {labels.map((l) => <span key={l} className="detail-caption-hint" data-on={l === hint} aria-hidden="true">{l}</span>)}
          </span>
          <HintContext.Provider value={ctx}>{actions}</HintContext.Provider>
        </div>
      </div>
      {description && <p className="detail-desc">{description}</p>}
    </header>
  )
}

// AppStatus mirrors InstanceStatus: dot + label, one display for the
// deployment state of an application.
const appStatus: Record<string, { label: string; dot: string }> = {
  draft: { label: 'Draft', dot: 'border border-mute bg-transparent' },
  preparing: { label: 'Deploying', dot: 'bg-warn' },
  ready: { label: 'Ready', dot: 'bg-live' },
  degraded: { label: 'Degraded', dot: 'bg-warn' },
  disabled: { label: 'Disabled', dot: 'border border-mute bg-transparent' },
}
export function AppStatus({ status }: { status: string }) {
  const s = appStatus[status] ?? { label: status, dot: 'border border-mute bg-transparent' }
  return <span className="inline-flex items-center gap-1.5 text-xs text-sec"><span className={`inline-block h-2 w-2 rounded-full ${s.dot}`} />{s.label}</span>
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
        <h1 className="text-[28px] font-semibold leading-[34px] tracking-[-0.022em] text-ink">{title}</h1>
        {children && <p className="mt-3 max-w-3xl text-sm leading-[22px] text-sec">{children}</p>}
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

export function Loading({ label = 'Loading…' }: { label?: string }) {
  return (
    <div className="flex items-center gap-3 text-sm text-mute">
      <span className="inline-block h-3 w-3 motion-safe:animate-spin border-2 border-mute border-t-transparent rounded-full" aria-hidden />
      {label}
    </div>
  )
}

const focusable = 'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'

// returnFocus remembers where focus came from (C1 in
// docs/interaction-stability.md). If the opener is gone by the time the
// dialog closes (its row was removed), focus moves to the nearest control
// after it, then before it, then to the page, never to <body>.
function returnFocus(): () => void {
  const active = document.activeElement
  const opener = active instanceof HTMLElement && active !== document.body ? active : null
  if (!opener) return () => document.querySelector<HTMLElement>('main')?.focus()
  const scope = opener.closest<HTMLElement>('tbody, ul, ol, section, main') ?? document.body
  const all = Array.from(scope.querySelectorAll<HTMLElement>(focusable))
  const i = all.indexOf(opener)
  const near = i < 0 ? [] : [...all.slice(i + 1), ...all.slice(0, i).reverse()]
  return () => {
    const target = opener.isConnected ? opener : near.find((el) => el.isConnected && !el.matches(':disabled')) ?? document.querySelector<HTMLElement>('main')
    target?.focus()
  }
}

// Modal is a Radix dialog: focus is trapped and returned (C1), the title
// labels it, and it is anchored at the top so content changes grow it
// downwards without moving what is above (S3). Escape and an outside click
// close it, except once the user has typed or chosen anything in it: then
// they ask first, in place over the dialog (C2). dirty overrides the
// detection: true for content that cannot be had again (a one-time
// secret, with a discardPrompt saying what would be lost), false where
// nothing is lost.
export function Modal({ title, onClose, children, footer, wide, dirty: dirtyProp, discardPrompt = 'Discard unsaved changes?', discardConfirm = 'Discard' }: {
  title: string
  onClose: () => void
  children: React.ReactNode
  footer?: React.ReactNode
  wide?: boolean
  dirty?: boolean
  discardPrompt?: string
  discardConfirm?: string
}) {
  const [restore] = React.useState(returnFocus)
  const [edited, setEdited] = React.useState(false)
  const dirty = dirtyProp ?? edited
  const [discarding, setDiscarding] = React.useState(false)
  const resume = React.useRef<HTMLElement | null>(null)
  const lastFocused = React.useRef<HTMLElement | null>(null)
  const asking = React.useRef(false)
  // An outside click reports both the pointer and the focus leaving; only
  // the first may ask, and the field to resume is the last one edited in
  // the dialog, not whatever holds focus after the click.
  const ask = () => {
    if (asking.current) return
    asking.current = true
    const active = document.activeElement
    resume.current = lastFocused.current?.isConnected ? lastFocused.current
      : active instanceof HTMLElement ? active : null
    setDiscarding(true)
  }
  const keep = () => {
    asking.current = false
    setDiscarding(false)
    window.setTimeout(() => resume.current?.focus(), 0)
  }
  return (
    <Dialog.Root open onOpenChange={(open) => { if (!open) onClose() }}>
      <Dialog.Portal>
        <Dialog.Overlay data-ui="dialog-overlay" />
        <Dialog.Content data-ui="dialog-surface" aria-describedby={undefined} className={wide ? 'max-w-5xl' : 'max-w-xl'}
          onEscapeKeyDown={(e) => { if (!dirty) return; e.preventDefault(); if (asking.current) keep(); else ask() }}
          onInteractOutside={(e) => { if (!dirty) return; e.preventDefault(); ask() }}
          onCloseAutoFocus={(e) => { e.preventDefault(); restore() }}>
          <div className="flex items-center justify-between border-b border-line px-6 py-4">
            <Dialog.Title className="text-lg font-semibold tracking-tight text-ink">{title}</Dialog.Title>
            <button data-ui="dialog-close" aria-label="Close" onClick={() => { if (dirty) ask(); else onClose() }}>&times;</button>
          </div>
          <div className="flex min-h-0 flex-col" onInputCapture={() => setEdited(true)}
            onFocusCapture={(e) => { if (e.target instanceof HTMLElement) lastFocused.current = e.target }}
            {...(discarding ? { inert: '' } : {})}>
            <div className="overflow-y-auto px-6 py-5">{children}</div>
            {footer && <div className="flex items-center gap-2 border-t border-line px-6 py-4">{footer}</div>}
          </div>
          {discarding && (
            <div data-ui="dialog-discard" role="alertdialog" aria-label={discardPrompt}>
              <div>
                <p className="text-sm font-semibold text-ink">{discardPrompt}</p>
                <div className="mt-4 flex justify-end gap-2">
                  <Button autoFocus onClick={keep}>Keep editing</Button>
                  <Button variant="danger" onClick={onClose}>{discardConfirm}</Button>
                </div>
              </div>
            </div>
          )}
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  )
}

// Confirm asks before an irreversible action (F2) and runs it from the
// dialog, so a double click on the opener cannot act twice (F1). It stays
// open and pending until the action settles, refetch included (P1), so by
// the time focus returns the removed row is gone and focus lands on its
// neighbour (C1). A failure is shown in the footer, where nothing above it
// moves, and the dialog stays open (P5).
export function Confirm({ title, children, confirm, pending, action, onClose, tone }: {
  title: string
  children?: React.ReactNode
  confirm: string
  pending: string
  action: () => Promise<unknown>
  onClose: () => void
  tone?: 'danger'
}) {
  const [busy, setBusy] = React.useState(false)
  const [error, setError] = React.useState('')
  const run = async () => {
    setBusy(true)
    setError('')
    try {
      await action()
      onClose()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
      setBusy(false)
    }
  }
  return (
    <Modal title={title} dirty={false} onClose={() => { if (!busy) onClose() }} footer={
      <div className="flex w-full items-start gap-2">
        <p role="alert" className="mr-auto min-w-0 flex-1 self-center text-xs text-warn-text">{error}</p>
        <Button onClick={onClose} disabled={busy}>Cancel</Button>
        <Button variant={tone === 'danger' ? 'danger' : 'primary'} onClick={run} disabled={busy}><SwapLabel show={busy ? pending : confirm} options={[confirm, pending]} /></Button>
      </div>
    }>
      {children}
    </Modal>
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

export function MenuSeparator() {
  return <DropdownMenu.Separator data-ui="menu-separator" />
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
      label = `Expiring · ${m}:${String(sec).padStart(2, '0')}`
    } else {
      const mins = Math.floor(left / 60000)
      dot = <span className="inline-block h-2 w-2 rounded-full bg-live" />
      label = mins >= 60 ? `Live · ${Math.floor(mins / 60)}h ${mins % 60}m left`
        : mins >= 1 ? `Live · ${mins}m left`
          : `Live · ${Math.max(0, Math.floor(left / 1000))}s left`
    }
  } else if (inst.status === 'pending') {
    dot = <span className="inline-block h-2 w-2 rounded-full bg-warn" />
    label = 'Pending · extensions'
  } else {
    dot = <span className="inline-block h-2 w-2 rounded-full border border-mute bg-transparent" />
    label = inst.status === 'lost' ? 'Ended · lost' : 'Ended'
  }
  return <span className="inline-flex items-center gap-1.5 text-xs text-sec">{dot}{label}</span>
}
