import React from 'react'
import type { Decision, InstanceStatus } from '../api'

type Tone = 'blue' | 'red' | 'yellow' | 'gray'
type Shape = 'circle' | 'square' | 'triangle'

const toneShape: Record<Tone, Shape> = { blue: 'circle', red: 'square', yellow: 'triangle', gray: 'square' }
const toneFill: Record<Tone, string> = { blue: 'fill-accent', red: 'fill-danger', yellow: 'fill-warn', gray: 'fill-none stroke-mute' }

// Mark is the Bauhaus primitive used as a status glyph: circle, square or
// triangle in one of the three signal colors.
export function Mark({ tone, shape = toneShape[tone], className = '' }: { tone: Tone; shape?: Shape; className?: string }) {
  const cls = toneFill[tone]
  return (
    <svg viewBox="0 0 10 10" className={`h-2 w-2 shrink-0 ${className}`} aria-hidden>
      {shape === 'circle' && <circle cx="5" cy="5" r="5" className={cls} />}
      {shape === 'square' && <rect x="0.75" y="0.75" width="8.5" height="8.5" strokeWidth="1.5" className={cls} />}
      {shape === 'triangle' && <polygon points="5,0 10,10 0,10" className={cls} />}
    </svg>
  )
}

export function Logo({ className = '' }: { className?: string }) {
  return (
    <svg viewBox="0 0 34 10" className={`h-2.5 w-auto ${className}`} aria-hidden>
      <circle cx="5" cy="5" r="5" className="fill-accent" />
      <rect x="12" y="0" width="10" height="10" className="fill-danger" />
      <polygon points="29,0 34,10 24,10" className="fill-warn" />
    </svg>
  )
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

export function Badge({ tone, children }: { tone: Tone; children: React.ReactNode }) {
  const text = { blue: 'text-ink', red: 'text-danger', yellow: 'text-warn-text', gray: 'text-mute' }[tone]
  return (
    <span className={`inline-flex items-center gap-1.5 whitespace-nowrap text-xs font-medium ${text}`}>
      <Mark tone={tone} />
      {children}
    </span>
  )
}

export function Tag({ children }: { children: React.ReactNode }) {
  return <span className="inline-block border border-line px-1.5 py-px font-mono text-[11px] text-sec">{children}</span>
}

const statusTones: Record<InstanceStatus, Tone> = {
  running: 'blue',
  expired: 'yellow',
  lost: 'red',
  deleted: 'gray',
}

export function StatusBadge({ status }: { status: InstanceStatus }) {
  return <Badge tone={statusTones[status]}>{status}</Badge>
}

const decisionTones: Record<Decision, Tone> = { allow: 'blue', deny: 'red' }

export function DecisionBadge({ decision }: { decision: Decision }) {
  return <Badge tone={decisionTones[decision]}>{decision}</Badge>
}

export function Button(
  props: React.ButtonHTMLAttributes<HTMLButtonElement> & { variant?: 'primary' | 'danger'; size?: 'sm' },
) {
  const { variant, size, className, ...rest } = props
  const base =
    'inline-flex items-center justify-center gap-2 whitespace-nowrap border font-medium transition-colors disabled:pointer-events-none disabled:opacity-40 '
  const sz = size === 'sm' ? 'h-7 px-2.5 text-xs ' : 'h-8 px-3.5 text-sm '
  const v =
    variant === 'primary'
      ? 'border-accent bg-accent text-white hover:border-ink hover:bg-ink hover:text-paper'
      : variant === 'danger'
        ? 'border-danger/60 text-danger hover:border-danger hover:bg-danger hover:text-white'
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

// PageHeader mirrors the deck's slide header: a numbered eyebrow, a
// strong title, an optional lede, and actions aligned to the baseline.
export function PageHeader({ index, section, title, children, actions }: {
  index: string
  section: string
  title: React.ReactNode
  children?: React.ReactNode
  actions?: React.ReactNode
}) {
  return (
    <div className="mb-8 flex flex-wrap items-end gap-x-8 gap-y-4">
      <div className="min-w-0 flex-1">
        <div className="eyebrow"><span className="text-accent">{index}</span> · {section}</div>
        <h1 className="mt-3 text-[32px] font-semibold leading-[1.1] tracking-tight text-ink">{title}</h1>
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

export function Notice({ tone = 'gray', children }: { tone?: Tone; children: React.ReactNode }) {
  const bar = { blue: 'border-accent', red: 'border-danger', yellow: 'border-warn', gray: 'border-line-strong' }[tone]
  const text = tone === 'red' ? 'text-danger' : 'text-body'
  return <div className={`border-l-4 bg-surface px-4 py-3 text-sm ${bar} ${text}`}>{children}</div>
}

export function EmptyRow({ cols, children }: { cols: number; children: React.ReactNode }) {
  return (
    <tr>
      <td colSpan={cols} className="!py-10 text-center text-sm text-mute">
        <span className="inline-flex items-center gap-2"><Mark tone="gray" />{children}</span>
      </td>
    </tr>
  )
}

export function Loading({ label = 'loading' }: { label?: string }) {
  return (
    <div className="flex items-center gap-3 text-sm text-mute">
      <span className="flex gap-1">
        <Mark tone="blue" className="animate-pulse" />
        <Mark tone="red" className="animate-pulse [animation-delay:150ms]" />
        <Mark tone="yellow" className="animate-pulse [animation-delay:300ms]" />
      </span>
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
        className={`flex max-h-[90vh] w-full flex-col border border-ink bg-paper shadow-[8px_8px_0_0_rgb(var(--accent))] ${wide ? 'max-w-5xl' : 'max-w-xl'}`}
        onClick={(e) => e.stopPropagation()}
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

export function fmtTime(iso: string) {
  return new Date(iso).toLocaleString()
}

export function fmtCountdown(iso: string) {
  const ms = new Date(iso).getTime() - Date.now()
  if (ms <= 0) return 'expired'
  const m = Math.floor(ms / 60000)
  if (m < 60) return `${m}m`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h${m % 60}m`
  return `${Math.floor(h / 24)}d${h % 24}h`
}

export function fmtBytes(n: number) {
  if (n < 1024) return `${n} B`
  const units = ['KiB', 'MiB', 'GiB', 'TiB']
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(v < 10 ? 2 : 1)} ${units[i]}`
}
