export type CreateKind = 'instance' | 'application'

// Creating has one kind for the whole flow: whatever entry point you
// came from. The guide explains that kind beside the basics form; there
// is deliberately no Instance/Application switch here.
export function CreateGuide({ kind }: { kind: CreateKind }) {
  const instance = kind === 'instance'
  return (
    <section className="create-guide" aria-label={instance ? 'About instances' : 'About applications'}>
      <article className="create-guide-card" aria-labelledby={`create-guide-${kind}-title`}>
        <div className="create-guide-top">
          <span className="create-guide-eyebrow">
            <span className={`create-guide-dot ${instance ? 'is-live' : 'is-accent'}`} aria-hidden="true" />
            {instance ? 'Playground' : 'API service'}
          </span>
          <span className="create-guide-tag">{instance ? 'persistent · stateful' : 'versioned · stateless'}</span>
        </div>
        <h2 id={`create-guide-${kind}-title`} className="create-guide-title">{instance ? 'Instance' : 'Application'}</h2>
        <div className="create-diagram-well">
          {instance ? <InstanceDiagram /> : <ApplicationDiagram />}
        </div>
        <p className="create-guide-lede">
          {instance
            ? 'A live workbench that stays open while you explore. An agent connects over MCP and calls it again and again — every run shares the same memory and files.'
            : 'A durable API you publish, version and call. Callers send JSON to main(input) and get JSON back — each call starts from a fresh session.'}
        </p>
      </article>
    </section>
  )
}

function DiagramDefs({ prefix }: { prefix: string }) {
  return (
    <defs>
      <pattern id={`${prefix}-grid`} width="16" height="16" patternUnits="userSpaceOnUse">
        <circle cx="8" cy="8" r="0.85" className="cf-grid-dot" />
      </pattern>
      <radialGradient id={`${prefix}-glow`} cx="58%" cy="46%" r="56%">
        <stop offset="0%" className="cf-glow-start" />
        <stop offset="100%" className="cf-glow-end" />
      </radialGradient>
      <linearGradient id={`${prefix}-card`} x1="0" y1="0" x2="0" y2="1">
        <stop offset="0%" className="cf-card-top" />
        <stop offset="100%" className="cf-card-bot" />
      </linearGradient>
      <linearGradient id={`${prefix}-state`} x1="0" y1="0" x2="1" y2="1">
        <stop offset="0%" className="cf-state-start" />
        <stop offset="100%" className="cf-state-end" />
      </linearGradient>
      <filter id={`${prefix}-shadow`} x="-12%" y="-12%" width="124%" height="132%">
        <feDropShadow dx="0" dy="1" stdDeviation="1" className="cf-drop-near" />
        <feDropShadow dx="0" dy="6" stdDeviation="7" className="cf-drop-far" />
      </filter>
      <filter id={`${prefix}-orb`} x="-150%" y="-150%" width="400%" height="400%">
        <feGaussianBlur stdDeviation="2.2" result="blur" />
        <feMerge>
          <feMergeNode in="blur" />
          <feMergeNode in="SourceGraphic" />
        </feMerge>
      </filter>
      <marker id={`${prefix}-arrow`} viewBox="0 0 8 8" refX="6.5" refY="4" markerWidth="5.5" markerHeight="5.5" orient="auto-start-reverse">
        <path d="M0.5 1 L7 4 L0.5 7 Z" className="cf-arrowhead" />
      </marker>
      <marker id={`${prefix}-arrow-live`} viewBox="0 0 8 8" refX="6.5" refY="4" markerWidth="5.5" markerHeight="5.5" orient="auto-start-reverse">
        <path d="M0.5 1 L7 4 L0.5 7 Z" className="cf-arrowhead-live" />
      </marker>
    </defs>
  )
}

function InstanceDiagram() {
  return (
    <svg className="create-diagram" viewBox="0 0 420 340" role="img"
      aria-label="An agent runs code again and again in one live instance process; every run shares the same memory and files">
      <DiagramDefs prefix="cf-inst" />

      {/* Technical dot matrix + ambient spotlight */}
      <rect width="420" height="340" fill="url(#cf-inst-grid)" />
      <rect width="420" height="340" fill="url(#cf-inst-glow)" />

      {/* Left: Agent / MCP Client */}
      <g filter="url(#cf-inst-shadow)">
        <rect x="12" y="54" width="94" height="148" rx="10" fill="url(#cf-inst-card)" className="cf-node" />
      </g>
      <path d="M12 64 A10 10 0 0 1 22 54 H96 A10 10 0 0 1 106 64 V82 H12 Z" className="cf-node-band" />
      <line x1="12" y1="82" x2="106" y2="82" className="cf-divider" />
      {/* Terminal / Agent icon */}
      <rect x="21" y="62" width="13" height="13" rx="3.5" className="cf-icon-box" />
      <path d="M25 66.5 L27.5 68.5 L25 70.5 M28 70.5 H30.5" className="cf-icon-stroke" />
      <text x="39" y="72" className="cf-title">Agent</text>

      <text x="59" y="100" textAnchor="middle" className="cf-sub">MCP client</text>
      <rect x="20" y="109" width="78" height="22" rx="5" className="cf-well" />
      <text x="59" y="123" textAnchor="middle" className="cf-code">exec(code)</text>
      <text x="59" y="148" textAnchor="middle" className="cf-small">streamable HTTP</text>
      <rect x="22" y="160" width="74" height="18" rx="9" className="cf-chip" />
      <circle cx="33" cy="169" r="2.2" className="cf-accent-dot" />
      <text x="63" y="172" textAnchor="middle" className="cf-chip-text">ins_… key</text>

      {/* MCP Duplex Bridge */}
      <text x="128" y="103" textAnchor="middle" className="cf-label">exec</text>
      <circle cx="106" cy="112" r="2.2" className="cf-port" />
      <line x1="108" y1="112" x2="148" y2="112" className="cf-track" />
      <path d="M108 112H146" className="cf-edge" pathLength="100" markerEnd="url(#cf-inst-arrow)" />
      <circle cx="150" cy="112" r="2.2" className="cf-port" />

      <circle cx="150" cy="152" r="2.2" className="cf-port-live" />
      <line x1="148" y1="152" x2="108" y2="152" className="cf-track" />
      <path d="M148 152H110" className="cf-edge cf-edge-live" pathLength="100" markerEnd="url(#cf-inst-arrow-live)" />
      <circle cx="106" cy="152" r="2.2" className="cf-port-live" />
      <text x="128" y="166" textAnchor="middle" className="cf-label">stdout</text>

      <circle className="cf-packet cf-x36" cx="110" cy="112" r="2.6" filter="url(#cf-inst-orb)" />
      <circle className="cf-packet cf-packet-live cf-x36-back" cx="146" cy="152" r="2.6" filter="url(#cf-inst-orb)" style={{ animationDelay: '210ms' }} />

      {/* Right: Instance Workbench Envelope */}
      <g filter="url(#cf-inst-shadow)">
        <rect x="150" y="14" width="258" height="312" rx="12" fill="url(#cf-inst-card)" className="cf-node cf-workbench" />
      </g>
      <path d="M150 26 A12 12 0 0 1 162 14 H396 A12 12 0 0 1 408 26 V48 H150 Z" className="cf-node-band" />
      <line x1="150" y1="48" x2="408" y2="48" className="cf-divider" />

      {/* Workbench Header */}
      <circle cx="166" cy="31" r="4.5" className="cf-live-halo cf-pulse" />
      <circle cx="166" cy="31" r="2.5" className="cf-live" />
      <text x="176" y="35" className="cf-title">Instance</text>
      <text x="230" y="35" className="cf-sub">· warm process</text>
      <rect x="330" y="22" width="66" height="18" rx="9" className="cf-chip cf-chip-live" />
      <text x="363" y="34" textAnchor="middle" className="cf-chip-text cf-chip-live-text">live · TTL</text>

      {/* Sequential Runs Row */}
      {([
        ['#1', 'x = 1', 'ok · 2ms'],
        ['#2', 'x += 1', 'ok · 1ms'],
        ['#3', 'print(x)', '→ 2'],
      ] as const).map(([step, code, res], i) => {
        const x = 163 + i * 79
        const cx = x + 37
        return (
          <g key={step}>
            <rect x={x} y="62" width="74" height="58" rx="8" className={`cf-run cf-run-${i + 1}`} />
            <rect x={x + 7} y="68" width="22" height="13" rx="3.5" className="cf-step-pill" />
            <text x={x + 18} y="77.5" textAnchor="middle" className="cf-step-num">{step}</text>
            <text x={x + 66} y="77.5" textAnchor="end" className="cf-small">run</text>
            <text x={x + 8} y="95" className="cf-code">{code}</text>
            <text x={x + 8} y="110" className={i === 2 ? 'cf-code cf-accent' : 'cf-code-dim'}>{res}</text>

            {/* Vertical connector into shared memory */}
            <line x1={cx} y1="120" x2={cx} y2="144" className="cf-track" />
            <path d={`M${cx} 120V142`} className="cf-edge" pathLength="100" markerEnd="url(#cf-inst-arrow)" />
            <circle cx={cx} cy="145" r="2" className="cf-port" />
          </g>
        )
      })}
      {/* Chain arrows between run 1 → 2 → 3 */}
      <path d="M237 91H242" className="cf-chain" />
      <path d="M316 91H321" className="cf-chain" />

      <circle className="cf-packet cf-y20" cx="200" cy="121" r="2.2" filter="url(#cf-inst-orb)" />
      <circle className="cf-packet cf-y20" cx="279" cy="121" r="2.2" filter="url(#cf-inst-orb)" style={{ animationDelay: '140ms' }} />
      <circle className="cf-packet cf-y20" cx="358" cy="121" r="2.2" filter="url(#cf-inst-orb)" style={{ animationDelay: '280ms' }} />

      {/* Shared Memory + VFS Well */}
      <rect x="163" y="145" width="232" height="82" rx="9" fill="url(#cf-inst-state)" className="cf-state" />
      <text x="175" y="162" className="cf-state-head">SHARED MEMORY &amp; VFS</text>
      <text x="383" y="162" textAnchor="end" className="cf-small">survives every run</text>

      {/* Globals sub-pill */}
      <rect x="173" y="170" width="103" height="46" rx="6" className="cf-inner-card" />
      <text x="181" y="183" className="cf-label">globals</text>
      <text x="181" y="198" className="cf-code cf-accent">x = 2</text>
      <text x="181" y="210" className="cf-code-dim">df · helper()</text>

      {/* /work VFS sub-pill */}
      <rect x="282" y="170" width="103" height="46" rx="6" className="cf-inner-card" />
      <text x="290" y="183" className="cf-label">/work files</text>
      <text x="290" y="198" className="cf-code">report.csv</text>
      <text x="290" y="210" className="cf-code-dim">chart.html</text>

      {/* Connector from state to policy gate */}
      <line x1="279" y1="227" x2="279" y2="243" className="cf-track" />
      <path d="M279 227V243" className="cf-edge" pathLength="100" />

      {/* Policy & Capability Gate */}
      <rect x="163" y="243" width="232" height="46" rx="8" className="cf-gate" />
      <text x="175" y="259" className="cf-label">capability gate</text>
      <text x="383" y="259" textAnchor="end" className="cf-small">rego · audit</text>
      {(['fs', 'net', 'ext', 'secrets'] as const).map((cap, i) => (
        <g key={cap}>
          <rect x={175 + i * 52} y="265" width="46" height="16" rx="4" className="cf-cap-pill" />
          <text x={198 + i * 52} y="276" textAnchor="middle" className="cf-code">{cap}</text>
        </g>
      ))}

      {/* Lifecycle Footer */}
      <circle cx="169" cy="308" r="2" className="cf-mute-dot" />
      <text x="177" y="311" className="cf-small">End the instance to discard memory and /work</text>
    </svg>
  )
}

function ApplicationDiagram() {
  return (
    <svg className="create-diagram" viewBox="0 0 420 340" role="img"
      aria-label="Callers post JSON to a published application; each call runs in a fresh session and returns JSON">
      <DiagramDefs prefix="cf-app" />

      {/* Technical dot matrix + ambient spotlight */}
      <rect width="420" height="340" fill="url(#cf-app-grid)" />
      <rect width="420" height="340" fill="url(#cf-app-glow)" />

      {/* Left: Two Concurrent Callers */}
      {[
        { y: 66, tag: 'Caller A', payload: '{"q": "acme"}' },
        { y: 162, tag: 'Caller B', payload: '{"q": "globex"}' },
      ].map(({ y, tag, payload }) => (
        <g key={tag}>
          <g filter="url(#cf-app-shadow)">
            <rect x="10" y={y} width="88" height="68" rx="9" fill="url(#cf-app-card)" className="cf-node" />
          </g>
          <text x="20" y={y + 19} className="cf-title">{tag}</text>
          <rect x="64" y={y + 9} width="26" height="13" rx="3.5" className="cf-chip" />
          <text x="77" y={y + 18} textAnchor="middle" className="cf-chip-text">key</text>
          <text x="20" y={y + 34} className="cf-sub">POST /invoke</text>
          <rect x="18" y={y + 41} width="72" height="18" rx="4" className="cf-well" />
          <text x="54" y={y + 53} textAnchor="middle" className="cf-code-dim">{payload}</text>
        </g>
      ))}

      {/* Inbound POST Edges */}
      <text x="115" y="92" textAnchor="middle" className="cf-label">JSON</text>
      <line x1="98" y1="100" x2="132" y2="100" className="cf-track" />
      <path d="M98 100H130" className="cf-edge" pathLength="100" markerEnd="url(#cf-app-arrow)" />

      <text x="115" y="188" textAnchor="middle" className="cf-label">JSON</text>
      <line x1="98" y1="196" x2="132" y2="196" className="cf-track" />
      <path d="M98 196H130" className="cf-edge" pathLength="100" markerEnd="url(#cf-app-arrow)" />

      <circle className="cf-packet cf-x30" cx="100" cy="100" r="2.5" filter="url(#cf-app-orb)" />
      <circle className="cf-packet cf-x30" cx="100" cy="196" r="2.5" filter="url(#cf-app-orb)" style={{ animationDelay: '210ms' }} />

      {/* Center: Published Application Envelope */}
      <g filter="url(#cf-app-shadow)">
        <rect x="132" y="14" width="156" height="264" rx="12" fill="url(#cf-app-card)" className="cf-node cf-workbench" />
      </g>
      <path d="M132 26 A12 12 0 0 1 144 14 H276 A12 12 0 0 1 288 26 V50 H132 Z" className="cf-node-band" />
      <line x1="132" y1="50" x2="288" y2="50" className="cf-divider" />

      <text x="145" y="33" className="cf-title">Application</text>
      <text x="145" y="44" className="cf-sub">immutable release</text>
      <rect x="242" y="22" width="34" height="18" rx="9" className="cf-version cf-pulse" />
      <text x="259" y="34" textAnchor="middle" className="cf-version-text">v3</text>

      {/* Inbound / Outbound Ports on Application Envelope */}
      <circle cx="132" cy="100" r="2.2" className="cf-port" />
      <circle cx="132" cy="196" r="2.2" className="cf-port" />
      <circle cx="288" cy="100" r="2.2" className="cf-port-live" />
      <circle cx="288" cy="196" r="2.2" className="cf-port-live" />

      {/* Isolated Session 1 */}
      <rect x="144" y="66" width="132" height="68" rx="8" className="cf-run cf-run-1" />
      <rect x="152" y="74" width="52" height="14" rx="4" className="cf-step-pill" />
      <text x="178" y="84" textAnchor="middle" className="cf-step-num">run #1</text>
      <text x="268" y="84" textAnchor="end" className="cf-small">isolated</text>
      <text x="152" y="103" className="cf-code">main(input)</text>
      <text x="152" y="119" className="cf-small">fresh memory · clean VFS</text>

      {/* Isolation Barrier */}
      <line x1="146" y1="148" x2="274" y2="148" className="cf-barrier" />
      <rect x="166" y="140" width="88" height="16" rx="8" className="cf-barrier-pill" />
      <text x="210" y="151" textAnchor="middle" className="cf-label">no shared state</text>

      {/* Isolated Session 2 */}
      <rect x="144" y="162" width="132" height="68" rx="8" className="cf-run cf-run-2" />
      <rect x="152" y="170" width="52" height="14" rx="4" className="cf-step-pill" />
      <text x="178" y="180" textAnchor="middle" className="cf-step-num">run #2</text>
      <text x="268" y="180" textAnchor="end" className="cf-small">isolated</text>
      <text x="152" y="199" className="cf-code">main(input)</text>
      <text x="152" y="215" className="cf-small">fresh memory · clean VFS</text>

      {/* Schema Contract Strip */}
      <rect x="144" y="238" width="132" height="28" rx="6" className="cf-well" />
      <text x="154" y="255" className="cf-code cf-accent">in</text>
      <text x="168" y="255" className="cf-small">schema</text>
      <text x="210" y="255" textAnchor="middle" className="cf-small">·</text>
      <text x="220" y="255" className="cf-code cf-live-text">out</text>
      <text x="240" y="255" className="cf-small">schema</text>

      {/* Outbound JSON Edges */}
      <text x="305" y="92" textAnchor="middle" className="cf-label">200</text>
      <line x1="288" y1="100" x2="322" y2="100" className="cf-track" />
      <path d="M288 100H320" className="cf-edge cf-edge-live" pathLength="100" markerEnd="url(#cf-app-arrow-live)" />

      <text x="305" y="188" textAnchor="middle" className="cf-label">200</text>
      <line x1="288" y1="196" x2="322" y2="196" className="cf-track" />
      <path d="M288 196H320" className="cf-edge cf-edge-live" pathLength="100" markerEnd="url(#cf-app-arrow-live)" />

      <circle className="cf-packet cf-packet-live cf-x30" cx="290" cy="100" r="2.5" filter="url(#cf-app-orb)" style={{ animationDelay: '105ms' }} />
      <circle className="cf-packet cf-packet-live cf-x30" cx="290" cy="196" r="2.5" filter="url(#cf-app-orb)" style={{ animationDelay: '315ms' }} />

      {/* Right: Validated Outputs */}
      {[
        { y: 66, out: '{"ok": true}', audit: 'run_8f2a · audited' },
        { y: 162, out: '{"ok": true}', audit: 'run_9c1b · audited' },
      ].map(({ y, out, audit }) => (
        <g key={audit}>
          <g filter="url(#cf-app-shadow)">
            <rect x="322" y={y} width="88" height="68" rx="9" fill="url(#cf-app-card)" className="cf-node" />
          </g>
          <circle cx="334" cy={y + 16} r="2.5" className="cf-live" />
          <text x="342" y={y + 19} className="cf-title">Response</text>
          <rect x="330" y={y + 27} width="72" height="20" rx="4" className="cf-well" />
          <text x="366" y={y + 40} textAnchor="middle" className="cf-code">{out}</text>
          <text x="366" y={y + 58} textAnchor="middle" className="cf-small">{audit}</text>
        </g>
      ))}

      {/* Bottom Release Pipeline Bar */}
      <rect x="10" y="290" width="400" height="36" rx="9" fill="url(#cf-app-state)" className="cf-state" />
      <text x="24" y="311" className="cf-state-head">RELEASE PIPELINE</text>
      <rect x="136" y="299" width="42" height="18" rx="4.5" className="cf-inner-card" />
      <text x="157" y="311" textAnchor="middle" className="cf-code">draft</text>
      <path d="M182 308H198" className="cf-chain" />
      <rect x="202" y="299" width="52" height="18" rx="4.5" className="cf-inner-card" />
      <text x="228" y="311" textAnchor="middle" className="cf-code">publish</text>
      <path d="M258 308H274" className="cf-chain" />
      <rect x="278" y="299" width="32" height="18" rx="4.5" className="cf-version" />
      <text x="294" y="311" textAnchor="middle" className="cf-version-text">v3</text>
      <text x="396" y="311" textAnchor="end" className="cf-small">pinned &amp; ready</text>
    </svg>
  )
}
