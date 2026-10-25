import { useState } from 'react'
import { Button, SwapLabel } from './ui'
import { claudeCodeSnippet, cursorSnippet, devinSnippet, headerSnippet, mcpURL } from '../mcpSnippets'

// CopyBlock renders copyable text (secret or client snippet) with a
// copy button — one component for every "copy this once" surface.
export function CopyBlock({ text, mono }: { text: string; mono?: boolean }) {
  const [copied, setCopied] = useState(false)
  return (
    <div className="flex gap-2">
      <pre className={`pre flex-1 overflow-auto break-all ${mono ? 'border-ink' : ''}`}>{text}</pre>
      <Button onClick={async () => { await navigator.clipboard.writeText(text); setCopied(true) }}><SwapLabel show={copied ? 'Copied' : 'Copy'} options={['Copy', 'Copied']} /></Button>
    </div>
  )
}

// MCPGuide shows the connect instructions next to the secret; url
// defaults to the workspace endpoint, or the instance endpoint when
// the key is bound to one instance.
export function MCPGuide({ secret, url }: { secret: string; url?: string }) {
  const endpoint = url ?? mcpURL(window.location.origin)
  const tabs = [
    { label: 'Claude Code', text: claudeCodeSnippet(endpoint, secret), hint: 'Run this in a terminal.' },
    { label: 'Cursor / VS Code', text: cursorSnippet(endpoint, secret), hint: 'Add this to the client MCP config.' },
    { label: 'Devin', text: devinSnippet(endpoint, secret), hint: 'Devin cloud: Customize → MCPs → Add custom MCP, then enter the URL and Authorization header. Devin CLI: save this as .devin/mcp_config.json.' },
    { label: 'Any client', text: `URL: ${endpoint}\n${headerSnippet(secret)}`, hint: 'Use these values wherever the client asks for an HTTP MCP server.' },
  ]
  const [tab, setTab] = useState(0)
  return (
    <div className="space-y-2">
      <p className="text-xs text-sec">
        MCP endpoint: <code className="font-mono text-ink">{endpoint}</code> — this restricted key is all an MCP client needs.
      </p>
      <div className="flex flex-wrap gap-1 border-b border-line">
        {tabs.map(({ label }, i) => (
          <button key={label}
            className={`-mb-px border-b-2 px-3 py-1.5 text-xs font-medium ${i === tab ? 'border-accent text-ink' : 'border-transparent text-mute hover:text-ink'}`}
            onClick={() => setTab(i)}>{label}</button>
        ))}
      </div>
      <p className="text-xs text-sec">{tabs[tab].hint}</p>
      <CopyBlock text={tabs[tab].text} />
    </div>
  )
}
