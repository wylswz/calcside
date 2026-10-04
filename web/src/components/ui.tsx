import React from 'react'
import type { Decision, InstanceStatus } from '../api'

type Tone = 'green' | 'red' | 'gray' | 'yellow' | 'blue'

export function Badge({ tone, children }: { tone: Tone; children: React.ReactNode }) {
  const cls = {
    green: 'bg-green-100 text-green-800 dark:bg-green-900/40 dark:text-green-300',
    red: 'bg-red-100 text-red-800 dark:bg-red-900/40 dark:text-red-300',
    gray: 'bg-gray-100 text-gray-700 dark:bg-gray-800 dark:text-gray-300',
    yellow: 'bg-yellow-100 text-yellow-800 dark:bg-yellow-900/40 dark:text-yellow-300',
    blue: 'bg-blue-100 text-blue-800 dark:bg-blue-900/40 dark:text-blue-300',
  }[tone]
  return <span className={`inline-block rounded px-1.5 py-0.5 text-xs font-medium ${cls}`}>{children}</span>
}

const statusTones: Record<InstanceStatus, Tone> = {
  running: 'green',
  expired: 'yellow',
  lost: 'yellow',
  deleted: 'gray',
}

export function StatusBadge({ status }: { status: InstanceStatus }) {
  return <Badge tone={statusTones[status]}>{status}</Badge>
}

const decisionTones: Record<Decision, Tone> = { allow: 'green', deny: 'red' }

export function DecisionBadge({ decision }: { decision: Decision }) {
  return <Badge tone={decisionTones[decision]}>{decision}</Badge>
}

export function Button(props: React.ButtonHTMLAttributes<HTMLButtonElement> & { variant?: 'primary' | 'danger' }) {
  const { variant, className, ...rest } = props
  const base = 'rounded px-3 py-1.5 text-sm font-medium disabled:opacity-50 '
  const v =
    variant === 'primary'
      ? 'bg-blue-600 text-white hover:bg-blue-700'
      : variant === 'danger'
        ? 'bg-red-600 text-white hover:bg-red-700'
        : 'border border-gray-300 dark:border-gray-700 hover:bg-gray-100 dark:hover:bg-gray-800'
  return <button className={base + v + (className ? ' ' + className : '')} {...rest} />
}

export function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="block">
      <span className="block text-xs font-medium text-gray-600 dark:text-gray-400 mb-1">{label}</span>
      {children}
    </label>
  )
}

export const inputCls =
  'w-full rounded border border-gray-300 dark:border-gray-700 bg-white dark:bg-gray-900 px-2 py-1.5 text-sm'

export function Modal({ title, onClose, children }: { title: string; onClose: () => void; children: React.ReactNode }) {
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40" onClick={onClose}>
      <div
        className="w-full max-w-2xl max-h-[85vh] overflow-y-auto rounded-lg border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-900 p-4 shadow-xl"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between mb-3">
          <h2 className="text-sm font-semibold">{title}</h2>
          <button onClick={onClose} className="text-gray-500 hover:text-gray-800 dark:hover:text-gray-200 text-lg leading-none">&times;</button>
        </div>
        {children}
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
