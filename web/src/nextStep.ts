import type { AuditEvent, Execution } from './api'
import { runOutcome } from './activity.ts'
export type NextStep =
  | { kind: 'ended' }
  | { kind: 'expiring'; seconds: number }
  | { kind: 'denied'; call: string }
  | { kind: 'waiting' }
  | { kind: 'connect' }
  | { kind: 'none' }

// nextStep picks the single guidance card for an instance, by priority.
export function nextStep(
  inst: { status: string; created_at: string; expires_at: string },
  latest: Pick<Execution, 'status' | 'error_type'> & { calls?: Pick<AuditEvent, 'op' | 'args' | 'decision'>[] } | null,
  hasBoundKey: boolean,
  now: number,
): NextStep {
  if (inst.status !== 'running') return { kind: 'ended' }
  if (expiringSoon(inst, now)) return { kind: 'expiring', seconds: Math.max(0, (new Date(inst.expires_at).getTime() - now) / 1000) }
  if (latest && runOutcome(latest) === 'denied') {
    const ev = latest.calls?.find((c) => c.decision === 'deny')
    const call = ev?.op ? [ev.op, typeof ev.args === 'string' ? ev.args : ''].filter(Boolean).join(' ') : 'a capability call'
    return { kind: 'denied', call }
  }
  if (!latest) return hasBoundKey ? { kind: 'waiting' } : { kind: 'connect' }
  return { kind: 'none' }
}

// Attention is the "Needs attention" cell for the list. Ended
// instances never get a connect link.
export function attentionText(inst: { status: string; activity?: { runs?: number; denied_runs?: number; failed_runs?: number } }): { text: string; tone: 'danger' | 'warn' | 'mute' | 'sec'; link: boolean } {
  const a = inst.activity
  const ended = inst.status !== 'running'
  if (a?.denied_runs) return { text: `${a.denied_runs} denied`, tone: 'danger', link: false }
  if (a?.failed_runs) return { text: `${a.failed_runs} failed`, tone: 'warn', link: false }
  if (!a?.runs) return ended ? { text: '—', tone: 'mute', link: false } : { text: 'no runs yet', tone: 'mute', link: true }
  return { text: `${a.runs} run${a.runs === 1 ? '' : 's'}`, tone: 'sec', link: false }
}

// expiringSoon: under 5 min remaining, or under half the TTL when the
// TTL itself is short (≤ 10 min) so a 60s instance shows "expiring"
// for its last 30s rather than its last ~12s.
export function expiringSoon(inst: { status: string; created_at: string; expires_at: string }, now: number): boolean {
  if (inst.status !== 'running') return false
  const left = new Date(inst.expires_at).getTime() - now
  const ttl = new Date(inst.expires_at).getTime() - new Date(inst.created_at).getTime()
  if (ttl <= 10 * 60_000) return left < 0.5 * ttl
  return left < 5 * 60_000
}
