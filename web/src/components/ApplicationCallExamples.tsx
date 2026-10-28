import { useState } from 'react'
import * as Tabs from '@radix-ui/react-tabs'
import { Link } from 'react-router-dom'
import { applicationSnippets } from '../applicationSnippets'
import { CopyBlock } from './MCP'

const examples = [
  { id: 'curl', label: 'curl' },
  { id: 'python', label: 'Python' },
  { id: 'javascript', label: 'JavaScript' },
] as const
type Example = typeof examples[number]['id']

const statusHint: Record<string, string> = {
  draft: 'Publish this draft before invoking. The endpoint returns 503 until ready.',
  preparing: 'Preparing the deployment. The endpoint returns 503 until ready.',
  ready: 'These requests invoke the published version, not unpublished draft code.',
  degraded: 'The deployment is unhealthy. Check its status before invoking.',
  disabled: 'The deployment is disabled. The endpoint returns 503 until ready.',
}

// Fixed-height panels and shared copy feedback preserve S1/S4 while
// Radix handles tab keyboard navigation and ARIA relationships.
export function ApplicationCallExamples({ appId, input, status }: {
  appId: string
  input: string
  status: string
}) {
  const [tab, setTab] = useState<Example>('curl')
  const snips = applicationSnippets(window.location.origin, appId, input)
  return (
    <section className="app-examples" aria-label="Request examples">
      <h3 className="app-label">Request examples</h3>
      <Tabs.Root value={tab} onValueChange={(value) => setTab(value as Example)}>
        <Tabs.List className="app-ex-tabs" aria-label="Example language">
          {examples.map(({ id, label }) => (
            <Tabs.Trigger key={id} value={id} className="app-ex-tab">{label}</Tabs.Trigger>
          ))}
        </Tabs.List>
        <Tabs.Content key={tab} value={tab} className="app-ex-panel">
          <CopyBlock text={snips[tab]} preClassName="app-ex-pre" />
        </Tabs.Content>
      </Tabs.Root>
      <p className="app-dim">
        {statusHint[status] ?? 'The endpoint returns 503 until ready.'}{' '}
        Examples use the JSON in Test input.
      </p>
      <p className="app-dim">
        Set <code>CALCSIDE_API_KEY</code> to a full-access key from this application's workspace
        (<Link className="link-btn" to="/workspace/keys">Workspace → Keys</Link>), not an instance-bound or restricted key.
        Keep the key in your environment, not in source code. JavaScript uses Node.js 18+.
      </p>
    </section>
  )
}
