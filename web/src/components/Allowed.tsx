import type { Instance } from '../api'
import { fmtBytes } from '../format'

interface SpecSecret { ref?: string; source?: string; allowed_domains?: string[] }

// policyLabel renders builtin policies as their plain-English name;
// the raw id stays in the title attribute. Unknown names pass through.
function policyLabel(name: string): string {
  const known: Record<string, string> = {
    'builtin.block_private_network': 'Block private networks',
  }
  return known[name] ?? name
}

function Row({ name, children }: { name: string; children: React.ReactNode }) {
  return (
    <div className="side-field">
      <dt>{name}</dt>
      <dd>{children}</dd>
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
      <div className="side-group">
        <h3 className="side-label">Abilities</h3>
        {caps.length === 0 ? (
          <p className="side-text">compute + print only</p>
        ) : (
          <dl>
            {caps.map(([name, cfg]) => (
              <div key={name} className="side-field"><dt>{capLabel[name] ?? name}</dt>
                <dd className="side-text">{capSettings(name, cfg) || 'defaults'}</dd></div>
            ))}
          </dl>
        )}
      </div>
      <div className="side-group">
        <h3 className="side-label">Rules</h3>
        <dl>
          <Row name="Policies">
            {policies.length === 0 ? (
              <span className="side-text">server policies only</span>
            ) : (
              <span className="block space-y-1">
                {policies.map((n) => (
                  <span key={n} className="side-text block" title={n}>{policyLabel(n)}</span>
                ))}
              </span>
            )}
          </Row>
          {limitParts.length > 0 && limitParts.map((l) => (
            <div key={l.k} className="side-limit"><dt className="side-text">{l.label}</dt><dd className="side-value">{l.v}</dd></div>
          ))}
        </dl>
      </div>
      {secrets.length === 0 && env.length === 0 ? (
        <p className="side-text">No additional inputs.</p>
      ) : (
        <div className="side-group">
          <h3 className="side-label">Inputs</h3>
          <dl>
            {secrets.length > 0 && (
              <Row name="secrets">
                <span className="flex flex-wrap gap-x-4 gap-y-1">
                  {secrets.map(([k, v]) => (
                    <span key={k} className="side-name">
                      {k}
                      <span className="side-text">
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
                <span className="flex flex-wrap gap-x-4 gap-y-1">
                  {env.map((k) => <span key={k} className="side-name">{k}</span>)}
                </span>
              </Row>
            )}
          </dl>
        </div>
      )}
    </section>
  )
}