import React, { useState } from 'react'
import * as RadixCheckbox from '@radix-ui/react-checkbox'
import * as RadixCollapsible from '@radix-ui/react-collapsible'
import * as RadixRadioGroup from '@radix-ui/react-radio-group'
import * as RadixSelect from '@radix-ui/react-select'
import * as RadixTooltip from '@radix-ui/react-tooltip'
import * as RadixPopover from '@radix-ui/react-popover'
import { Check, ChevronDown, ChevronRight, Copy, MoreHorizontal } from 'lucide-react'
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

const EMPTY_SELECT_VALUE = '__calcside_empty__'

// Select: Radix UI Select (WAI-ARIA combobox + listbox) with an optically
// centered chevron and shared data-ui="menu" surface (S1/S2/P2). Transparently
// maps empty-string values so callers can include placeholder/all options.
export function Select<T extends string>({
  id,
  value,
  onChange,
  options = [],
  groups,
  ariaLabel,
  placeholder,
  disabled,
  size = 'md',
  className = '',
}: {
  id?: string
  value: T
  onChange: (value: T) => void
  options?: ReadonlyArray<{ value: T; label: string; disabled?: boolean }>
  groups?: ReadonlyArray<{ label: string; options: ReadonlyArray<{ value: T; label: string; disabled?: boolean }> }>
  ariaLabel?: string
  placeholder?: string
  disabled?: boolean
  size?: 'xs' | 'sm' | 'md'
  className?: string
}) {
  const encode = (v: string) => (v === '' ? EMPTY_SELECT_VALUE : v)
  const decode = (v: string) => (v === EMPTY_SELECT_VALUE ? '' : v) as T
  const sizeCls =
    size === 'xs'
      ? 'h-6 px-2 text-[11px] leading-4'
      : size === 'sm'
        ? 'h-[26px] px-2 text-xs leading-4'
        : 'h-[28px] px-2.5 text-[13px] leading-[18px]'
  const chevronCls = size === 'xs' ? 'h-3 w-3' : 'h-3.5 w-3.5'
  const allOptions = [...options, ...(groups?.flatMap((g) => g.options) ?? [])]
  const active = allOptions.find((o) => o.value === value)
  const renderItem = (opt: { value: T; label: string; disabled?: boolean }) => (
    <RadixSelect.Item
      key={encode(opt.value)}
      value={encode(opt.value)}
      disabled={opt.disabled}
      data-ui="menu-item"
      className="flex items-center justify-between gap-3"
    >
      <RadixSelect.ItemText>{opt.label}</RadixSelect.ItemText>
      <span className="inline-flex h-3.5 w-3.5 shrink-0 items-center justify-center">
        <RadixSelect.ItemIndicator>
          <Check className="h-3.5 w-3.5 text-accent" strokeWidth={2} aria-hidden="true" />
        </RadixSelect.ItemIndicator>
      </span>
    </RadixSelect.Item>
  )
  return (
    <RadixSelect.Root
      value={encode(value)}
      onValueChange={(v) => onChange(decode(v))}
      disabled={disabled}
    >
      <RadixSelect.Trigger
        id={id}
        aria-label={ariaLabel}
        data-ui="select-trigger"
        className={`inline-flex items-center justify-between gap-1.5 rounded-[3px] border border-line bg-paper text-left text-ink hover:border-line-strong focus:border-accent focus:outline-none focus-visible:ring-2 focus-visible:ring-accent/40 disabled:cursor-not-allowed disabled:opacity-50 ${sizeCls} ${className}`}
      >
        <RadixSelect.Value placeholder={placeholder}>
          <span className="truncate">{active?.label ?? placeholder ?? value}</span>
        </RadixSelect.Value>
        <RadixSelect.Icon asChild>
          <ChevronDown className={`${chevronCls} shrink-0 text-mute`} strokeWidth={1.75} aria-hidden="true" />
        </RadixSelect.Icon>
      </RadixSelect.Trigger>
      <RadixSelect.Portal>
        <RadixSelect.Content
          data-ui="menu"
          position="popper"
          sideOffset={4}
          className="z-50 max-h-[260px] min-w-[var(--radix-select-trigger-width)] overflow-y-auto"
        >
          <RadixSelect.Viewport>
            {options.map(renderItem)}
            {groups?.map((g) => (
              <RadixSelect.Group key={g.label}>
                <RadixSelect.Label className="px-2.5 py-1 text-[10px] font-semibold uppercase tracking-label text-mute">
                  {g.label}
                </RadixSelect.Label>
                {g.options.map(renderItem)}
              </RadixSelect.Group>
            ))}
          </RadixSelect.Viewport>
        </RadixSelect.Content>
      </RadixSelect.Portal>
    </RadixSelect.Root>
  )
}

// Checkbox: Radix UI Checkbox with crisp optical alignment across light/dark themes.
export function Checkbox({
  id,
  checked,
  onCheckedChange,
  disabled,
  ariaLabel,
  className = '',
}: {
  id?: string
  checked: boolean
  onCheckedChange: (checked: boolean) => void
  disabled?: boolean
  ariaLabel?: string
  className?: string
}) {
  return (
    <RadixCheckbox.Root
      id={id}
      checked={checked}
      disabled={disabled}
      aria-label={ariaLabel}
      onCheckedChange={(v) => onCheckedChange(v === true)}
      className={`inline-flex h-3.5 w-3.5 shrink-0 items-center justify-center rounded-[3px] border border-line-strong bg-paper text-white transition-colors hover:border-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40 disabled:cursor-not-allowed disabled:opacity-50 data-[state=checked]:border-accent data-[state=checked]:bg-accent ${className}`}
    >
      <RadixCheckbox.Indicator className="inline-flex items-center justify-center">
        <Check className="h-2.5 w-2.5" strokeWidth={2.75} aria-hidden="true" />
      </RadixCheckbox.Indicator>
    </RadixCheckbox.Root>
  )
}

// Disclosure: Radix UI Collapsible replacing browser-native <details>/<summary>
// with a unified, optically aligned chevron across platforms and themes.
export function Disclosure({
  summary,
  children,
  defaultOpen = false,
  variant = 'plain',
  className = '',
}: {
  summary: React.ReactNode
  children: React.ReactNode
  defaultOpen?: boolean
  variant?: 'plain' | 'card'
  className?: string
}) {
  const [open, setOpen] = useState(defaultOpen)
  const wrapCls = variant === 'card'
    ? `rounded-[3px] border border-line bg-surface/40 px-3 py-2 ${className}`
    : className
  return (
    <RadixCollapsible.Root open={open} onOpenChange={setOpen} className={wrapCls}>
      <RadixCollapsible.Trigger asChild>
        <button
          type="button"
          className="inline-flex items-center gap-1.5 text-left text-xs font-medium text-sec hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40"
        >
          {open
            ? <ChevronDown className="h-3.5 w-3.5 shrink-0 text-mute" strokeWidth={1.75} aria-hidden="true" />
            : <ChevronRight className="h-3.5 w-3.5 shrink-0 text-mute" strokeWidth={1.75} aria-hidden="true" />}
          <span>{summary}</span>
        </button>
      </RadixCollapsible.Trigger>
      <RadixCollapsible.Content className="pt-2">
        {children}
      </RadixCollapsible.Content>
    </RadixCollapsible.Root>
  )
}

// RadioGroup / RadioItem: Radix UI RadioGroup replacing native <input type="radio">.
export function RadioGroup<T extends string>({
  value,
  onValueChange,
  ariaLabel,
  className = '',
  children,
}: {
  value: T
  onValueChange: (value: T) => void
  ariaLabel?: string
  className?: string
  children: React.ReactNode
}) {
  return (
    <RadixRadioGroup.Root
      value={value}
      onValueChange={(v) => onValueChange(v as T)}
      aria-label={ariaLabel}
      className={className}
    >
      {children}
    </RadixRadioGroup.Root>
  )
}

export function RadioItem({
  value,
  id,
  disabled,
  className = '',
}: {
  value: string
  id?: string
  disabled?: boolean
  className?: string
}) {
  return (
    <RadixRadioGroup.Item
      id={id}
      value={value}
      disabled={disabled}
      className={`inline-flex h-3.5 w-3.5 shrink-0 items-center justify-center rounded-full border border-line-strong bg-paper hover:border-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40 disabled:cursor-not-allowed disabled:opacity-50 data-[state=checked]:border-accent ${className}`}
    >
      <RadixRadioGroup.Indicator className="inline-flex items-center justify-center">
        <span className="h-2 w-2 rounded-full bg-accent" />
      </RadixRadioGroup.Indicator>
    </RadixRadioGroup.Item>
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
