import React, { useState } from 'react'
import * as RadixTooltip from '@radix-ui/react-tooltip'
import * as RadixPopover from '@radix-ui/react-popover'
import { Check, Copy, MoreHorizontal } from 'lucide-react'
import { Button, Menu, SwapLabel } from '../components/ui'

export const TooltipProvider = RadixTooltip.Provider

// Tooltip: replaces native title attributes for icon controls, nav items
// and truncated metadata. Floating-layer shadow and reduced-motion-guarded
// entrance live in material.css (S1/S2/P4).
export function Tooltip({ content, side = 'top', align = 'center', delayDuration = 300, children }: {
  content: React.ReactNode
  side?: 'top' | 'right' | 'bottom' | 'left'
  align?: 'start' | 'center' | 'end'
  delayDuration?: number
  children: React.ReactNode
}) {
  if (content === null || content === undefined || content === '') return <>{children}</>
  return (
    <RadixTooltip.Root delayDuration={delayDuration}>
      <RadixTooltip.Trigger asChild>{children}</RadixTooltip.Trigger>
      <RadixTooltip.Portal>
        <RadixTooltip.Content data-ui="tooltip" side={side} align={align} sideOffset={6}>
          {content}
        </RadixTooltip.Content>
      </RadixTooltip.Portal>
    </RadixTooltip.Root>
  )
}

// Popover: interactive floating surface for diagnostics, section metadata,
// and copyable details where native title attributes fall short (S2/P2).
export function Popover({ trigger, align = 'end', side = 'bottom', children }: {
  trigger: React.ReactNode
  align?: 'start' | 'center' | 'end'
  side?: 'top' | 'right' | 'bottom' | 'left'
  children: React.ReactNode
}) {
  return (
    <RadixPopover.Root>
      <RadixPopover.Trigger asChild>{trigger}</RadixPopover.Trigger>
      <RadixPopover.Portal>
        <RadixPopover.Content data-ui="popover" align={align} side={side} sideOffset={6}>
          {children}
        </RadixPopover.Content>
      </RadixPopover.Portal>
    </RadixPopover.Root>
  )
}

// Five-state semantic tone system shared across every entity in the console:
//   ok     — live / ready / succeeded / fresh (green)
//   active — running / in-flight (blue)
//   warn   — preparing / stale / waiting / degraded / expiring (orange)
//   err    — failed / denied (red)
//   mute   — draft / disabled / not_run / expired / ended / unknown (gray)
export type SemanticTone = 'ok' | 'active' | 'warn' | 'err' | 'mute'

export function semanticToneOf(status: string): SemanticTone {
  switch (status) {
    case 'ready':
    case 'succeeded':
    case 'live':
    case 'fresh':
    case 'verified':
      return 'ok'
    case 'running':
      return 'active'
    case 'preparing':
    case 'stale':
    case 'waiting':
    case 'degraded':
    case 'expiring':
    case 'pending':
    case 'lost':
    case 'live_with_issues':
    case 'override':
      return 'warn'
    case 'failed':
    case 'denied':
    case 'blocked':
      return 'err'
    default:
      return 'mute'
  }
}

const dotBg: Record<SemanticTone, string> = {
  ok: 'bg-live',
  active: 'bg-accent',
  warn: 'bg-warn',
  err: 'bg-danger',
  mute: 'bg-mute',
}

export function StatusIndicator({ status, tone, label, suffix, pill = false }: {
  status: string
  tone?: SemanticTone
  label?: React.ReactNode
  suffix?: React.ReactNode
  pill?: boolean
}) {
  const t = tone ?? semanticToneOf(status)
  const cls = pill
    ? 'inline-flex items-center gap-1.5 whitespace-nowrap rounded-[3px] border border-line bg-surface px-1.5 py-px text-xs font-medium text-ink'
    : 'inline-flex items-center gap-1.5 whitespace-nowrap text-xs text-sec'
  return (
    <span className={cls} data-tone={t}>
      <span className={`inline-block h-1.5 w-1.5 shrink-0 rounded-full ${dotBg[t]}`} aria-hidden="true" />
      <span>{label ?? status}{suffix}</span>
    </span>
  )
}

// DigestChip renders a short monospace content-addressed digest with a
// copy button and full-value tooltip — shared by releases, pins and runs.
export function DigestChip({ digest, length = 14, prefix }: { digest: string; length?: number; prefix?: string }) {
  const [copied, setCopied] = useState(false)
  const short = digest.length > length ? `${digest.slice(0, length)}…` : digest
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(digest)
      setCopied(true)
      window.setTimeout(() => setCopied(false), 1400)
    } catch { /* clipboard unavailable */ }
  }
  return (
    <Tooltip content={digest}>
      <button type="button" onClick={() => void copy()}
        className="inline-flex items-center gap-1 rounded-[3px] border border-line bg-surface px-1.5 py-px font-mono text-[11px] leading-4 text-sec hover:border-line-strong hover:text-ink"
        aria-label={`Copy digest ${digest}`}>
        {prefix && <span className="font-sans text-mute">{prefix} </span>}
        <span>{short}</span>
        {copied ? <Check className="h-3 w-3 text-live" strokeWidth={1.75} aria-hidden="true" /> : <Copy className="h-3 w-3 text-mute" strokeWidth={1.5} aria-hidden="true" />}
        <span className="sr-only"><SwapLabel show={copied ? 'Copied' : 'Copy'} options={['Copy', 'Copied']} /></span>
      </button>
    </Tooltip>
  )
}

// RowActionsMenu: table row overflow menu so destructive actions never
// clutter list rows (F1/F2).
export function RowActionsMenu({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <Menu trigger={
      <Button variant="ghost" size="xs" className="w-6 px-0" aria-label={label}>
        <MoreHorizontal className="h-4 w-4" strokeWidth={1.5} aria-hidden="true" />
      </Button>
    }>
      {children}
    </Menu>
  )
}

// EmptyState: structured zero-data state with icon, title, concise
// explanation and optional primary CTA.
export function EmptyState({ icon, title, description, action }: {
  icon?: React.ReactNode
  title: string
  description?: React.ReactNode
  action?: React.ReactNode
}) {
  return (
    <div className="flex flex-col items-center justify-center gap-2.5 px-6 py-16 text-center">
      {icon && <div className="flex h-10 w-10 items-center justify-center rounded-[4px] border border-line bg-surface text-mute">{icon}</div>}
      <h2 className="text-sm font-semibold text-ink">{title}</h2>
      {description && <p className="max-w-md text-xs leading-5 text-sec">{description}</p>}
      {action && <div className="mt-1">{action}</div>}
    </div>
  )
}
