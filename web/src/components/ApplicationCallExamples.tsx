import { useState } from 'react'
import * as Tabs from '@radix-ui/react-tabs'
import { applicationSnippets } from '../applicationSnippets'
import { CopyBlock } from './MCP'

const examples = [
  { id: 'curl', label: 'curl' },
  { id: 'python', label: 'Python' },
  { id: 'javascript', label: 'JavaScript' },
] as const
type Example = typeof examples[number]['id']

// Fixed-height panels and shared copy feedback preserve S1/S4 while
// Radix handles tab keyboard navigation and ARIA relationships.
export function ApplicationCallExamples({ appId, input }: {
  appId: string
  input: string
}) {
  const [tab, setTab] = useState<Example>('curl')
  const snips = applicationSnippets(window.location.origin, appId, input)
  return (
    <Tabs.Root className="app-examples" value={tab} onValueChange={(value) => setTab(value as Example)}>
      <Tabs.List className="app-ex-tabs" aria-label="Example language">
        {examples.map(({ id, label }) => (
          <Tabs.Trigger key={id} value={id} className="app-ex-tab">{label}</Tabs.Trigger>
        ))}
      </Tabs.List>
      <Tabs.Content key={tab} value={tab} className="app-ex-panel">
        <CopyBlock text={snips[tab]} preClassName="app-ex-pre" />
      </Tabs.Content>
    </Tabs.Root>
  )
}
