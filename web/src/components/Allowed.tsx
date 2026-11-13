import type { Instance } from '../api'
import { fmtBytes } from '../format'
import { Tag } from './ui'

interface SpecSecret { ref?: string; source?: string; allowed_domains?: string[] }

// policyLabel renders builtin policies as their plain-English name;
// the raw id stays in the title attribute. Unknown names pass through.
function policyLabel(name: string): string {
  const known: Record<string, string> = {
    'builtin.block_private_network': 'Block private networks',
  }
  return known[name] ?? name
}

// Allowed states the instance's boundary in plain terms: what it may do,
// under which policies, with which secrets — never values. Always visible
// on the instance page; every other judgement depends on it.
export function Allowed({ inst, extra }: { inst: Instance; extra?: React.ReactNode }) {
  const caps = Object.entries(inst.spec?.capabilities ?? {}) as [string, any][]
  const policies = inst.spec?.policies ?? []
  const secrets = Object.entries(inst.spec?.secrets ?? {}) as [string, SpecSecret][]
  const env = Object.keys(inst.spec?.env ?? {})
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
          .map(([alias, e]: [string, any]) => `${alias} → ${e?.source ?? '?'}`)
          .join(' · ')
      }
      default:
        return 'configured'
    }
  }

  const limitParts = Object.entries({
    exec_timeout_ms: 'Run timeout',
    max_steps: 'Step limit',
    max_output_bytes: 'Output limit',
  }).flatMap(([k, label]) => {
    const v = (limits as Record<string, number | undefined>)[k]
    if (v === undefined) return []
    const shown = k === 'exec_timeout_ms' ? `${v / 1000} s`
      : k === 'max_output_bytes' ? fmtBytes(v)
      : v.toLocaleString('en-US')
    return [{ k, label, v: shown }]
  })

  const capLabel = { fs: 'Files', net: 'Network', ext: 'Extensions' } as Record<string, string>

  return (
    <section aria-label="Allowed">
      <dl className="side-kv">
        <div className="side-kv-row">
          <dt>Capabilities</dt>
          <dd>
            {caps.length === 0 ? 'Compute only' : (
              <span className="side-kv-lines">
                {caps.map(([name, cfg]) => {
                  const detail = capSettings(name, cfg)
                  return (
                    <span key={name} className="side-kv-line">
                      <Tag>{capLabel[name] ?? name}</Tag>
                      {detail !== '' && <span className="side-kv-sub">{detail}</span>}
                    </span>
                  )
                })}
              </span>
            )}
          </dd>
        </div>
        <div className="side-kv-row">
          <dt>Policies</dt>
          <dd>
            {policies.length === 0 ? 'Server only' : (
              <span className="side-kv-lines">
                {policies.map((n) => (
                  <span key={n} className="side-kv-line" title={n}>{policyLabel(n)}</span>
                ))}
              </span>
            )}
          </dd>
        </div>
        {limitParts.map((l) => (
          <div key={l.k} className="side-kv-row">
            <dt>{l.label}</dt>
            <dd className="tabular-nums">{l.v}</dd>
          </div>
        ))}
        {secrets.length > 0 && (
          <div className="side-kv-row">
            <dt>Secrets</dt>
            <dd>
              <span className="side-kv-lines">
                {secrets.map(([k, v]) => (
                  <span key={k} className="side-kv-line">
                    <code>{k}</code>
                    <span className="side-kv-sub">
                      {v.source === 'vault' ? `vault:${v.ref ?? k}` : 'inline'}
                      {' · '}{(v.allowed_domains ?? []).join(' ') || 'any host'}
                    </span>
                  </span>
                ))}
              </span>
            </dd>
          </div>
        )}
        {env.length > 0 && (
          <div className="side-kv-row">
            <dt>Env</dt>
            <dd>
              <span className="side-kv-env">
                {env.map((k) => <code key={k}>{k}</code>)}
              </span>
            </dd>
          </div>
        )}
        {extra}
      </dl>
    </section>
  )
}
