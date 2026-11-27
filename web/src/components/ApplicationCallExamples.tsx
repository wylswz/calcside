import { useState } from 'react'
import * as Tabs from '@radix-ui/react-tabs'
import { applicationSnippets } from '../applicationSnippets'
import { CopyButton } from './MCP'

const examples = [
  { id: 'curl', label: 'curl' },
  { id: 'python', label: 'Python' },
  { id: 'javascript', label: 'JavaScript' },
] as const
type Example = typeof examples[number]['id']

// A bottom panel in the workbench vocabulary: language tabs and Copy
// live in the pane head, the snippet fills the full body width.
// Radix handles tab keyboard navigation and ARIA relationships.
export function ApplicationCallExamples({ appId, input, className = '' }: {
  appId: string
  input: string
  className?: string
}) {
  const [tab, setTab] = useState<Example>('curl')
  const snips = applicationSnippets(window.location.origin, appId, input)
  return (
    <Tabs.Root className={`wb-pane app-examples ${className}`} value={tab} onValueChange={(value) => setTab(value as Example)}>
      <header className="wb-pane-head">
        <h3 className="wb-pane-title">Request examples</h3>
        <Tabs.List className="wb-head-tabs" aria-label="Example language">
          {examples.map(({ id, label }) => (
            <Tabs.Trigger key={id} value={id} className="wb-head-tab">{label}</Tabs.Trigger>
          ))}
        </Tabs.List>
        <div className="wb-pane-actions"><CopyButton text={snips[tab]} size="xs" /></div>
      </header>
      <Tabs.Content key={tab} value={tab} className="wb-pane-body app-ex-panel">
        <pre className="wb-code wb-grow app-ex-pre">{snips[tab]}</pre>
      </Tabs.Content>
    </Tabs.Root>
  )
}
