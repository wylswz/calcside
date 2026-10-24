import { useCallback, useEffect, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import CodeMirror from '@uiw/react-codemirror'
import { useStarlarkEditor } from './editor'
import { api, ExecResult, Instance } from '../api'
import { Button, OutcomeBadge, SectionTitle } from './ui'
import { cmTheme } from './codemirror'
import { fmtMs } from '../format'

const DEFAULT_CODE = `# Starlark. Capabilities appear as globals when granted.
# Utilities: json, math, url, csv, base64, hashlib, regex, datetime,
# stats, linalg, dist, series, decimal.
print("hello from calcside")
`

// RunCode is the intervention tool: an editor with live completions and
// the run's result. Runs made here are recorded with via=session and
// show up in Activity as "you".
export function RunCode({ inst, running, setRunning, onRan }: {
  inst: Instance
  running: boolean
  setRunning: (v: boolean) => void
  onRan?: () => void
}) {
  const id = inst.id
  const qc = useQueryClient()
  const [code, setCode] = useState(DEFAULT_CODE)
  const [result, setResult] = useState<ExecResult | null>(null)
  const editor = useStarlarkEditor(id, inst.status === 'running', running)

  const run = useCallback(async () => {
    if (running) return
    setRunning(true)
    try {
      const r = await api.post<ExecResult>(`/api/v1/instances/${id}/exec`, { code })
      setResult(r)
      qc.invalidateQueries({ queryKey: ['executions', id] })
      qc.invalidateQueries({ queryKey: ['audit', id] })
      qc.invalidateQueries({ queryKey: ['files', id] })
      qc.invalidateQueries({ queryKey: ['inspect', id] })
      onRan?.()
    } catch (e: any) {
      setResult({ exec_id: '', output: '', error: { type: 'runtime', message: e.message }, duration_ms: 0, steps: 0 })
    } finally {
      qc.invalidateQueries({ queryKey: ['completions', id] })
      setRunning(false)
    }
  }, [code, id, running, qc, setRunning, onRan])

  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') {
        e.preventDefault()
        run()
      }
    }
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  }, [run])

  return (
    <section aria-label="Run code">
      <SectionTitle aside={<span className="text-mute">⌘/Ctrl + Enter — runs as you</span>}>Run code</SectionTitle>
      <div className="border border-ink">
        <div className="flex items-center gap-3 border-b border-ink px-3 py-2">
          <span className="text-[11px] font-medium uppercase tracking-label text-mute">Starlark</span>
          <Button variant="primary" size="sm" className="ml-auto min-w-20" disabled={running || inst.status !== 'running'} onClick={run}>
            {running ? 'Running…' : 'Run ▸'}
          </Button>
        </div>
        <CodeMirror value={code} height="300px" extensions={editor.extensions} onChange={setCode} theme={cmTheme} />
        <div className="flex flex-wrap gap-x-4 border-t border-line px-3 py-1.5 text-[11px] text-mute">
          <span>Ctrl+Space to complete</span>
          {editor.error ? <span className="text-warn-text">Live completions unavailable</span> :
            <span>{running ? 'Using cached symbols while code runs' : editor.isFetching ? 'Refreshing symbols…' : inst.status !== 'running' ? 'Instance is not running' : 'Suggestions use this instance’s runtime'}</span>}
          {editor.truncated && <span className="text-warn-text">Symbol limit reached; some suggestions omitted</span>}
          <button className="text-btn ml-auto" onClick={() => editor.refetch()} disabled={running || inst.status !== 'running' || editor.isFetching}>refresh symbols</button>
        </div>
        {result && (
          <div className="border-t border-ink">
            <div className="flex items-center gap-4 border-b border-line bg-surface px-3 py-1.5 text-xs">
              <span className="text-[11px] font-medium uppercase tracking-label text-mute">Output</span>
              <span className="font-mono text-sec">{fmtMs(result.duration_ms)} · {result.steps} steps</span>
              {result.error
                ? <OutcomeBadge outcome="failed" label={result.error.type} />
                : <OutcomeBadge outcome="ok" />}
            </div>
            <pre className="max-h-64 overflow-auto whitespace-pre-wrap p-3 font-mono text-xs leading-relaxed text-ink">
              {result.output || <span className="text-mute">(no output)</span>}
            </pre>
            {result.error && (
              <pre className="max-h-64 overflow-auto whitespace-pre-wrap border-t border-warn/40 border-l-4 border-l-warn bg-warn/5 p-3 font-mono text-xs leading-relaxed text-warn-text">
                error[{result.error.type}]: {result.error.message}
                {result.error.backtrace ? '\n' + result.error.backtrace : ''}
              </pre>
            )}
          </div>
        )}
      </div>
    </section>
  )
}
