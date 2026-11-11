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
// the run's result, styled as the notebook's last cell. Runs made here
// are recorded with via=session and show up in Activity as "you".
export function RunCode({ inst, running, disabled, setRunning, onRan, onRestored, promotable, onPromote }: {
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
  /** a successful scratch run exists to promote into a section */
  promotable?: boolean
  onPromote?: () => void
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
    <section aria-label="Run code" ref={rootRef} className="nb-run-code">
      <div className="nb-gutter">
        <Button
          variant="ghost" size="xs" className="nb-icon-btn"
          aria-label={running ? 'Running' : 'Run'} aria-busy={running}
          title={running ? 'Running…' : 'Run (⌘/Ctrl + Enter)'}
          disabled={running || disabled || (inst.status !== 'running' && inst.status !== 'lost')}
          onClick={run}
        >
          {running
            ? <span className="inline-block h-3.5 w-3.5 motion-safe:animate-spin rounded-full border-2 border-current border-t-transparent" aria-hidden />
            : <Icon name="play" />}
        </Button>
      </div>
      <div className="nb-body">
        <header className="nb-card-head">
          <span className="nb-scratch-name">scratch</span>
          <span className="nb-scratch-hint">Runs as you · not saved to the notebook · ⌘/Ctrl + Enter</span>
          <div className="nb-card-actions">
            <Button variant="ghost" size="xs" disabled={!promotable} onClick={onPromote}>Save as section…</Button>
          </div>
        </header>
        <div className="nb-editor">
          <CodeMirror
            value={code}
            minHeight="88px"
            maxHeight="420px"
            extensions={editor.extensions}
            onChange={setCode}
            theme={cmTheme}
          />
          <div className="nb-scratch-foot">
            <span>Ctrl+Space to complete</span>
            {inst.status === 'lost' && <span>first run restores an empty process — this code does not execute on that call</span>}
            {editor.error ? <span className="nb-warn">Live completions unavailable</span> :
              running ? <span>Using cached symbols while code runs</span> :
              editor.isFetching ? <span>Refreshing symbols…</span> :
              inst.status !== 'running' ? <span>Instance is not running</span> : null}
            {editor.truncated && <span className="nb-warn">Symbol limit reached; some suggestions omitted</span>}
            <button className="text-btn ml-auto" onClick={() => editor.refetch()} disabled={running || inst.status !== 'running' || editor.isFetching}>refresh symbols</button>
          </div>
        </div>
        {result && (
          <>
            <div className="nb-output">
              <div className="nb-output-head">
                <span className="nb-output-kind">output</span>
                <span className="nb-output-meta">{fmtMs(result.duration_ms)} · {result.steps} steps</span>
                {result.error
                  ? <OutcomeBadge outcome="failed" label={result.error.type} />
                  : <OutcomeBadge outcome="ok" />}
              </div>
              <pre className="nb-output-pre">{result.output || '(no output)'}</pre>
            </div>
            {result.error && (
              <div className="nb-output nb-output-err">
                <div className="nb-output-head">
                  <span className="nb-output-kind">error</span>
                </div>
                <pre className="nb-output-pre">error[{result.error.type}]: {result.error.message}{result.error.backtrace ? '\n' + result.error.backtrace : ''}</pre>
              </div>
            )}
          </>
        )}
      </div>
    </section>
  )
}
