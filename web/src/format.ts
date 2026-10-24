// Shared formatting helpers: plain functions, no React, so node tests
// and components can both use them.

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

// relTime renders a compact relative timestamp like `3m ago`.
export function relTime(iso: string, now = Date.now()): string {
  const ms = now - new Date(iso).getTime()
  if (ms < 0) return 'just now'
  const s = Math.floor(ms / 1000)
  if (s < 60) return `${s}s ago`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ago`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h ago`
  return `${Math.floor(h / 24)}d ago`
}

// fmtMs renders a duration: `<1 ms`, `12 ms`, `1.2 s`.
export function fmtMs(ms: number): string {
  if (ms < 1) return '<1 ms'
  if (ms < 1000) return `${ms} ms`
  return `${(ms / 1000).toFixed(1)} s`
}
