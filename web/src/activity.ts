// Shared, pure helpers for the supervision views: run/call outcomes,
// who ran what, naming and the "allowed" summary. The words mirror the
// API: a run is an execution, a call is an audited capability call.
import type { AuditEvent, Execution, Instance, InstanceSpec } from './api'
import { fmtBytes } from './format.ts'

export type Outcome = 'ok' | 'failed' | 'denied'

// callOutcome maps an audit record onto the console's three outcomes:
// denied (the boundary refused it), failed (the op errored), ok.
export function callOutcome(e: AuditEvent): Outcome {
  if (e.decision === 'deny') return 'denied'
  if (e.error) return 'failed'
  return 'ok'
}

// runOutcome maps a run onto the same three outcomes: a policy_denied
// error means the boundary refused the run — denied, not failed.
export function runOutcome(x: Pick<Execution, 'status' | 'error_type'>): Outcome {
  if (x.status === 'ok') return 'ok'
  if (x.error_type === 'policy_denied') return 'denied'
  return 'failed'
}

// denialReason strips the "denied by <hook> (<phase>): " wrapper the
// gate adds, leaving the plain reason for compact display.
export function denialReason(reason: string): string {
  return reason.replace(/^denied by \S+ \([^)]*\):\s*/, '')
}

export type RunBy = 'agent' | 'you' | 'unknown'

// runBy labels who started a run: the agent (API key) or you (console
// session / anonymous dev mode). Executions recorded before `via`
// existed report unknown.
export function runBy(x: Execution): RunBy {
  if (x.via === 'api_key') return 'agent'
  if (x.via === 'session' || x.via === 'anonymous') return 'you'
  return 'unknown'
}

// isConsoleCall marks the fs calls your own file browsing makes; they
// are grouped separately from runs.
export function isConsoleCall(e: AuditEvent): boolean {
  return e.exec_id === 'console'
}

// isBuiltinGlobal marks inspect variables that are part of the Starlark
// environment rather than state the agent set: granted capabilities,
// env/secrets accessors, and module / built-in function reprs.
export function isBuiltinGlobal(name: string, repr: string, capNames: string[]): boolean {
  if (capNames.includes(name) || name === 'env' || name === 'secrets') return true
  return repr.startsWith('<module "') || repr.startsWith('<built-in function')
}

// instanceName renders labels as `k=v` pairs (sorted) joined by ' · ';
// an instance without labels is just its id.
export function instanceName(inst: Pick<Instance, 'id' | 'labels'>): string {
  const pairs = Object.entries(inst.labels ?? {})
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([k, v]) => `${k}=${v}`)
  return pairs.length ? pairs.join(' · ') : inst.id
}

// instanceTitle prefers the human label for names and surfaces, falling
// back to the full label summary (or id) when no name is set.
export function instanceTitle(inst: Pick<Instance, 'id' | 'labels'>): string {
  const name = inst.labels?.name?.trim()
  return name || instanceName(inst)
}

// allowedSummary compresses the granted capability set to one line:
// `fs 64 MiB · net 2 hosts · ext tavily`.
export function allowedSummary(spec: InstanceSpec | undefined): string {
  const caps = spec?.capabilities ?? {}
  const parts: string[] = []
  for (const name of Object.keys(caps).sort()) {
    const cfg = (caps[name] ?? {}) as Record<string, any>
    switch (name) {
      case 'fs': {
        const quota = cfg.quota_bytes ? ` ${fmtBytes(Number(cfg.quota_bytes))}` : ''
        parts.push(`fs${quota}`)
        break
      }
      case 'net': {
        const hosts = Array.isArray(cfg.allow_hosts) ? cfg.allow_hosts.length : 0
        parts.push(hosts ? `net ${hosts} host${hosts === 1 ? '' : 's'}` : 'net')
        break
      }
      case 'ext': {
        const aliases = Object.keys(cfg).sort()
        parts.push(aliases.length ? `ext ${aliases.join(' ')}` : 'ext')
        break
      }
      default:
        parts.push(name)
    }
  }
  return parts.length ? parts.join(' · ') : 'nothing granted'
}

// groupCalls buckets audit events by exec_id, preserving chronological
// order inside each bucket.
export function groupCalls(events: AuditEvent[]): Map<string, AuditEvent[]> {
  const sorted = [...events].sort((a, b) => a.ts.localeCompare(b.ts))
  const out = new Map<string, AuditEvent[]>()
  for (const e of sorted) {
    const list = out.get(e.exec_id)
    if (list) list.push(e)
    else out.set(e.exec_id, [e])
  }
  return out
}
