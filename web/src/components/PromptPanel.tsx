import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { api, InstancePrompt } from '../api'
import { Button, Notice, SectionTitle, inputCls } from './ui'

// PromptPanel renders the system prompt an agent needs to drive this
// instance through the API.
export function PromptPanel({ id, running }: { id: string; running: boolean }) {
  const [prefix, setPrefix] = useState('calcside_')
  const [copied, setCopied] = useState(false)
  const { data, error } = useQuery({
    queryKey: ['instance-prompt', id, prefix],
    queryFn: () => api.get<InstancePrompt>(`/api/v1/instances/${id}/prompt?tool_prefix=${encodeURIComponent(prefix)}`),
    enabled: running && /^[A-Za-z0-9_]{0,32}$/.test(prefix),
    retry: false,
  })
  const copy = async () => {
    if (!data) return
    await navigator.clipboard.writeText(data.prompt)
    setCopied(true)
    setTimeout(() => setCopied(false), 1500)
  }
  return (
    <section aria-label="Agent prompt" className="space-y-3">
      <SectionTitle aside={
        <>
          <label className="text-sec">tool prefix</label>
          <input className={inputCls + ' !w-40 !py-1 font-mono !text-xs'} value={prefix} onChange={(e) => setPrefix(e.target.value)} />
          <Button onClick={copy} disabled={!data} size="sm">{copied ? 'Copied' : 'Copy'}</Button>
        </>
      }>Agent prompt</SectionTitle>
      {!running && <p className="text-xs text-mute">instance is not running — no prompt available</p>}
      {error && <Notice tone="warning">{(error as Error).message}</Notice>}
      {data && <pre className="pre max-h-[32rem] p-4">{data.prompt}</pre>}
    </section>
  )
}
