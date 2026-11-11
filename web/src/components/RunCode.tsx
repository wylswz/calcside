import { useCallback, useEffect, useRef, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import CodeMirror from '@uiw/react-codemirror'
import { useStarlarkEditor } from './editor'
import { api, ApiError, ExecResult, Instance } from '../api'
import { Button, Icon, OutcomeBadge } from './ui'
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
export function RunCode({ inst, running, disabled, setRunning, onRan, onRestored }: {
  inst: Instance
  running: boolean
  /** another run control is busy — disables without claiming this one runs (P1) */
  disabled?: boolean
  /** acquires the shared run lock: returns false when another run holds
   *  it, so this exec must abort before posting (same-tick guard) */
  setRunning: (v: boolean) => boolean
  onRan?: (r: ExecResult) => void
  /** fired when the exec hit a lost instance and triggered a restore —
   *  the code did NOT run; the caller shows the persistent notice. */
  onRestored?: () => void
}) {
  const id = inst.id
  const qc = useQueryClient()
  const [code, setCode] = useState(DEFAULT_CODE)
  const [result, setResult] = useState<ExecResult | null>(null)
  // Synchronous double-fire guard: state alone can lag a same-tick
  // second click/⌘Enter.
  const busyRef = useRef(false)
  const rootRef = useRef<HTMLElement>(null)
  const editor = useStarlarkEditor(id, inst.status === 'running', running)

  const run = useCallback(async () => {
    if (running || disabled || busyRef.current) return
    // The shared notebook lock is authoritative: a section/batch run
    // holding it refuses this start before any request is sent.
    if (!setRunning(true)) return
    busyRef.current = true
    try {
      const r = await api.post<ExecResult>(`/api/v1/instances/${id}/exec`, { code })
      setResult(r)
      qc.invalidateQueries({ queryKey: ['executions', id] })
      qc.invalidateQueries({ queryKey: ['audit', id] })
      qc.invalidateQueries({ queryKey: ['files', id] })
      qc.invalidateQueries({ queryKey: ['inspect', id] })
      qc.invalidateQueries({ queryKey: ['notebook', id] })
      qc.invalidateQueries({ queryKey: ['instance', id] })
      if (!r.error) onRan?.(r)
    } catch (e: any) {
      if (e instanceof ApiError && e.code === 'instance_restored') {
        // Lost → restored EMPTY: nothing ran. Refresh so the page shows
        // the new incarnation, and let the parent show its notice —
        // never retry the code automatically.
        onRestored?.()
        qc.invalidateQueries({ queryKey: ['notebook', id] })
        qc.invalidateQueries({ queryKey: ['instance', id] })
        setResult(null)
      } else {
        setResult({ exec_id: '', output: '', error: { type: 'runtime', message: e.message }, duration_ms: 0, steps: 0 })
      }
    } finally {
      qc.invalidateQueries({ queryKey: ['completions', id] })
      busyRef.current = false
      setRunning(false)
    }
  }, [code, id, running, disabled, qc, setRunning, onRan, onRestored])

  // ⌘/Ctrl+Enter runs scratch ONLY when the key event targets something
  // inside this visible region: a section card's editor or the
  // Activity-hidden notebook must never trigger a scratch exec — that
  // is a side effect the user did not aim at this control.
  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      if (!(e.metaKey || e.ctrlKey) || e.key !== 'Enter') return
      const root = rootRef.current
      if (!root || root.closest('[hidden]')) return
      const t = e.target as Node | null
      if (!t || !root.contains(t)) return
      e.preventDefault()
      run()
    }
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  }, [run])

  return (
    <section aria-label="Run code" ref={rootRef}>
      <div className="detail-editor-chrome">
        <div className="editor-toolbar flex items-center gap-3 px-3 py-2">
          <span className="text-xs font-medium text-ink">Run code as you</span>
          <span className="text-xs text-sec">Starlark · ⌘/Ctrl + Enter</span>
          <Button className="ml-auto" variant="primary" size="sm" disabled={running || disabled || (inst.status !== 'running' && inst.status !== 'lost')} onClick={run}
            aria-label={running ? 'Running' : 'Run'} aria-busy={running} title={running ? 'Running…' : 'Run (⌘/Ctrl + Enter)'}>
            {running
              ? <span className="inline-block h-3.5 w-3.5 motion-safe:animate-spin rounded-full border-2 border-current border-t-transparent" aria-hidden />
              : <Icon name="play" className="!h-3.5 !w-3.5" />}
          </Button>
        </div>
        <CodeMirror value={code} height="300px" extensions={editor.extensions} onChange={setCode} theme={cmTheme} />
        <div className="editor-footer flex flex-wrap gap-x-4 border-t border-line px-3 py-2 text-xs text-sec">
          <span>Ctrl+Space to complete</span>
          {editor.error ? <span className="text-warn-text">Live completions unavailable</span> :
            running ? <span>Using cached symbols while code runs</span> :
            editor.isFetching ? <span>Refreshing symbols…</span> :
            inst.status !== 'running' ? <span>Instance is not running</span> : null}
          {editor.truncated && <span className="text-warn-text">Symbol limit reached; some suggestions omitted</span>}
          <button className="text-btn ml-auto" onClick={() => editor.refetch()} disabled={running || inst.status !== 'running' || editor.isFetching}>refresh symbols</button>
        </div>
        {result && (
          <div className="border-t border-line">
            <div className="flex items-center gap-4 border-b border-line bg-surface px-3 py-1.5 text-xs">
              <span className="text-xs font-medium text-sec">Output</span>
              <span className="tabular-nums text-sec">{fmtMs(result.duration_ms)} · {result.steps} steps</span>
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
