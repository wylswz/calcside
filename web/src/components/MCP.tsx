import { useState } from 'react'
import { Button, SwapLabel } from './ui'
import { claudeCodeSnippet, cursorSnippet, headerSnippet, mcpURL } from '../mcpSnippets'

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
    ['Claude Code', claudeCodeSnippet(endpoint, secret)],
    ['Cursor / VS Code', cursorSnippet(endpoint, secret)],
    ['Any client', `URL: ${endpoint}\n${headerSnippet(secret)}`],
  ] as const
  const [tab, setTab] = useState(0)
  return (
    <div className="space-y-2">
      <p className="text-xs text-sec">
        MCP endpoint: <code className="font-mono text-ink">{endpoint}</code> — this restricted key is all an MCP client needs.
      </p>
      <div className="flex gap-1 border-b border-line">
        {tabs.map(([label], i) => (
          <button key={label}
            className={`-mb-px border-b-2 px-3 py-1.5 text-xs font-medium ${i === tab ? 'border-accent text-ink' : 'border-transparent text-mute hover:text-ink'}`}
            onClick={() => setTab(i)}>{label}</button>
        ))}
      </div>
      <CopyBlock text={tabs[tab][1]} />
    </div>
  )
}
