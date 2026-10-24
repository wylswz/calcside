import type { Instance } from '../api'
import { SectionTitle, Tag } from './ui'
import { fmtBytes } from '../format'

interface SpecSecret { ref?: string; source?: string; allowed_domains?: string[] }

function Row({ name, children }: { name: string; children: React.ReactNode }) {
  return (
    <div className="grid grid-cols-[90px_minmax(0,1fr)] gap-x-4 gap-y-0.5 py-1">
      <dt className="font-mono text-xs text-sec">{name}</dt>
      <dd className="min-w-0 text-xs text-ink">{children}</dd>
    </div>
  )
}

// Allowed states the instance's boundary in plain terms: what it may do,
// under which policies, with which secrets — never values. Always visible
// on the instance page; every other judgement depends on it.
export function Allowed({ inst }: { inst: Instance }) {
  const caps = Object.entries(inst.spec?.capabilities ?? {}) as [string, any][]
  const policies = inst.spec?.policies ?? []
  const secrets = Object.entries(inst.spec?.secrets ?? {}) as [string, SpecSecret][]
  const env = Object.entries(inst.spec?.env ?? {}) as [string, string][]
  const limits = inst.spec?.limits ?? {}

  const capSettings = (name: string, cfg: any): string => {
    if (!cfg || typeof cfg !== 'object') return ''
    switch (name) {
      case 'fs': {
        const parts = []
        parts.push(cfg.quota_bytes ? `quota ${fmtBytes(Number(cfg.quota_bytes))}` : 'default quota')
        return parts.join(' · ')
      }
      case 'net': {
        const hosts = Array.isArray(cfg.allow_hosts) ? cfg.allow_hosts : []
        const methods = Array.isArray(cfg.methods) ? cfg.methods : []
        const parts = []
        parts.push(hosts.length ? hosts.join(' ') : 'any host (subject to policies)')
        if (methods.length) parts.push(`methods ${methods.join(' ')}`)
        return parts.join(' · ')
      }
      case 'ext': {
        return Object.entries(cfg)
          .map(([alias, e]: [string, any]) => `${alias} → ${e?.source ?? '?'}${e?.sum ? ` (${e.sum})` : ''}`)
          .join(' · ')
      }
      default:
        return JSON.stringify(cfg)
    }
  }

  const limitParts = Object.entries({
    exec_timeout_ms: 'exec timeout',
    max_steps: 'max steps',
    max_output_bytes: 'max output',
  }).flatMap(([k, label]) => {
    const v = (limits as Record<string, number | undefined>)[k]
    if (v === undefined) return []
    const shown = k === 'exec_timeout_ms' ? `${v / 1000} s`
      : k === 'max_output_bytes' ? fmtBytes(v)
      : v.toLocaleString('en-US')
    return [`${label} ${shown}`]
  })

  return (
    <section aria-label="Allowed">
      <SectionTitle>Allowed</SectionTitle>
      {caps.length === 0 ? (
        <p className="text-xs text-mute">No capabilities granted — this instance can only compute and print.</p>
      ) : (
        <dl className="divide-y divide-line">
          {caps.map(([name, cfg]) => (
            <Row key={name} name={name}>{capSettings(name, cfg) || <span className="text-mute">defaults</span>}</Row>
          ))}
        </dl>
      )}
      <dl className="mt-3 divide-y divide-line border-t border-line">
        <Row name="policies">
          {policies.length === 0 ? (
            <span className="text-mute">server policies only</span>
          ) : (
            <span className="flex flex-wrap gap-x-3 gap-y-1">
              {policies.map((n) => (
                <span key={n} className="font-mono">{n} <Tag>{n.startsWith('builtin.') ? 'built-in' : 'library'}</Tag></span>
              ))}
            </span>
          )}
        </Row>
        {secrets.length > 0 && (
          <Row name="secrets">
            <span className="flex flex-wrap gap-x-4 gap-y-1">
              {secrets.map(([k, v]) => (
                <span key={k} className="font-mono">
                  {k}
                  <span className="text-mute">
                    {' '}{v.source === 'vault' ? `vault:${v.ref ?? k}` : 'inline'}
                    {' · '}{(v.allowed_domains ?? []).join(' ') || 'any host'}
                  </span>
                </span>
              ))}
            </span>
          </Row>
        )}
        {env.length > 0 && (
          <Row name="env">
            <span className="flex flex-wrap gap-x-4 gap-y-1 font-mono">
              {env.map(([k, v]) => <span key={k}>{k}={v}</span>)}
            </span>
          </Row>
        )}
        {limitParts.length > 0 && <Row name="limits">{limitParts.join(' · ')}</Row>}
      </dl>
    </section>
  )
}
