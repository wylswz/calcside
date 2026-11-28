import { useState } from 'react'
import { CheckCircle2, ChevronDown, ChevronRight, Circle, Clock, Loader2, RefreshCw, XCircle } from 'lucide-react'
import type { components } from '../api/schema'
import { Button, SwapLabel } from './ui'
import { CodeDiffView } from '../ui/CodeDiffView'
import { Disclosure } from '../ui/core'

export type TestSuiteRun = components['schemas']['ApplicationTestSuiteRun']
export type TestCaseResult = components['schemas']['ApplicationTestCaseResult']
export type TestTurnResult = components['schemas']['ApplicationTestTurnResult']

// TestChecksPanel: GitHub CI / Actions-style checks box used in
// PublishReviewDialog (publish gate) and ApplicationTests (suite summary).
export function TestChecksPanel({
  totalCases,
  suiteRun,
  running,
  disabled,
  onRun,
  onOpenTests,
}: {
  totalCases: number
  suiteRun: TestSuiteRun | null
  running: boolean
  disabled?: boolean
  onRun: () => void
  onOpenTests?: (caseId?: string) => void
}) {
  const [expanded, setExpanded] = useState<Record<string, boolean>>({})

  const toggle = (id: string) => {
    setExpanded((cur) => ({ ...cur, [id]: !cur[id] }))
  }

  const allPassed = !running && suiteRun != null && suiteRun.status === 'passed' && suiteRun.total > 0
  const hasFailed = !running && suiteRun != null && suiteRun.status === 'failed'

  const headerIcon = running ? (
    <Loader2 className="h-4 w-4 shrink-0 text-warn-text motion-safe:animate-spin" strokeWidth={2} aria-hidden="true" />
  ) : allPassed ? (
    <CheckCircle2 className="h-4 w-4 shrink-0 text-live" strokeWidth={2} aria-hidden="true" />
  ) : hasFailed ? (
    <XCircle className="h-4 w-4 shrink-0 text-danger" strokeWidth={2} aria-hidden="true" />
  ) : (
    <Clock className="h-4 w-4 shrink-0 text-mute" strokeWidth={1.75} aria-hidden="true" />
  )

  const headline = running
    ? `Running ${totalCases} test check${totalCases === 1 ? '' : 's'}…`
    : allPassed
      ? `All ${suiteRun!.passed} check${suiteRun!.passed === 1 ? '' : 's'} have passed`
      : hasFailed
        ? `${suiteRun!.failed} failing, ${suiteRun!.passed} successful check${suiteRun!.total === 1 ? '' : 's'}`
        : `${totalCases} test check${totalCases === 1 ? '' : 's'} required before publish`

  const subline = running
    ? 'Executing in fresh candidate session'
    : suiteRun
      ? `${suiteRun.passed}/${suiteRun.total} passed · ${suiteRun.duration_ms}ms`
      : 'Run checks against this candidate digest'

  return (
    <section aria-label="Test checks" className="overflow-hidden rounded-[3px] border border-line bg-paper text-xs">
      {/* GitHub PR Checks header */}
      <div className="flex flex-wrap items-center gap-2.5 border-b border-line bg-surface px-3 py-2.5">
        {headerIcon}
        <div className="min-w-0 flex-1">
          <div className="font-semibold text-ink">{headline}</div>
          <div className="font-mono text-[11px] text-sec tabular-nums">{subline}</div>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          {onOpenTests && (
            <button type="button" className="text-btn" onClick={() => onOpenTests()}>
              Manage tests
            </button>
          )}
          <Button size="xs" onClick={onRun} disabled={running || disabled} data-testid="run-ci-checks">
            <RefreshCw className="mr-1 h-3 w-3" strokeWidth={1.75} aria-hidden="true" />
            <SwapLabel show={running ? 'Running…' : suiteRun ? 'Re-run checks' : 'Run checks'} options={['Run checks', 'Re-run checks', 'Running…']} />
          </Button>
        </div>
      </div>

      {/* Per-case check rows */}
      {suiteRun && suiteRun.results.length > 0 && (
        <ul className="divide-y divide-line">
          {suiteRun.results.map((r) => {
            const isOpen = expanded[r.case_id] ?? (r.status === 'failed')
            const intrCount = r.turns.filter((t) => !!t.actual_handler).length
            return (
              <li key={r.case_id} className="bg-paper">
                <div className="flex items-center gap-2 px-3 py-2">
                  <button
                    type="button"
                    onClick={() => toggle(r.case_id)}
                    className="inline-flex h-4 w-4 shrink-0 items-center justify-center text-mute hover:text-ink"
                    aria-label={isOpen ? `Collapse ${r.case_name}` : `Expand ${r.case_name}`}
                  >
                    {isOpen ? <ChevronDown className="h-3.5 w-3.5" /> : <ChevronRight className="h-3.5 w-3.5" />}
                  </button>
                  {running ? (
                    <Loader2 className="h-3.5 w-3.5 shrink-0 text-warn-text motion-safe:animate-spin" strokeWidth={2} aria-hidden="true" />
                  ) : r.status === 'passed' ? (
                    <CheckCircle2 className="h-3.5 w-3.5 shrink-0 text-live" strokeWidth={2} aria-hidden="true" />
                  ) : (
                    <XCircle className="h-3.5 w-3.5 shrink-0 text-danger" strokeWidth={2} aria-hidden="true" />
                  )}
                  <button
                    type="button"
                    onClick={() => toggle(r.case_id)}
                    className="min-w-0 flex-1 truncate text-left font-medium text-ink hover:underline"
                  >
                    {r.case_name}
                  </button>
                  {intrCount > 0 && (
                    <span className="shrink-0 rounded-[2px] border border-line bg-surface px-1.5 py-px font-mono text-[10px] text-sec">
                      {intrCount} interrupt{intrCount === 1 ? '' : 's'}
                    </span>
                  )}
                  <span className="shrink-0 font-mono text-[11px] text-mute tabular-nums">
                    {r.duration_ms}ms
                  </span>
                  {onOpenTests && r.status === 'failed' && (
                    <button type="button" className="link shrink-0" onClick={() => onOpenTests(r.case_id)}>
                      Edit
                    </button>
                  )}
                </div>

                {isOpen && (
                  <div className="border-t border-line bg-surface/40 px-4 py-2.5 space-y-2.5">
                    {r.error_message && (
                      <div className="rounded-[3px] border border-danger/30 bg-danger/5 px-2.5 py-1.5 text-danger">
                        {r.error_message}
                      </div>
                    )}

                    {/* GitHub Actions style turn steps */}
                    <div className="space-y-1">
                      {r.turns.map((t) => (
                        <div key={`${t.turn}-${t.entry}`} className="rounded-[3px] border border-line bg-paper px-2.5 py-1.5">
                          <div className="flex items-center gap-2">
                            {t.status === 'passed' ? (
                              <CheckCircle2 className="h-3.5 w-3.5 shrink-0 text-live" strokeWidth={2} aria-hidden="true" />
                            ) : (
                              <XCircle className="h-3.5 w-3.5 shrink-0 text-danger" strokeWidth={2} aria-hidden="true" />
                            )}
                            <span className="font-mono font-medium text-ink">
                              Step {t.turn + 1}: {t.entry}
                            </span>
                            {t.actual_handler && (
                              <span className="font-mono text-[11px] text-sec">
                                → interrupt {t.actual_handler} ({t.resolution || 'paused'})
                              </span>
                            )}
                            <span className="ml-auto font-mono text-[11px] text-mute tabular-nums">
                              {t.duration_ms}ms · {t.steps} steps
                            </span>
                          </div>
                          {t.error_message && (
                            <div className="mt-1 text-danger">{t.error_message}</div>
                          )}
                          {t.expected_context && t.actual_context && t.status === 'failed' && (
                            <div className="mt-2">
                              <CodeDiffView
                                title={`Interrupt context diff (${t.actual_handler || t.expected_handler || 'interrupt'})`}
                                before={t.expected_context}
                                after={t.actual_context}
                                language="json"
                                maxHeight="180px"
                              />
                            </div>
                          )}
                        </div>
                      ))}
                    </div>

                    {r.expected_output && r.actual_output && r.status === 'failed' && (
                      <CodeDiffView
                        title="Output diff (expected vs actual)"
                        before={r.expected_output}
                        after={r.actual_output}
                        language="json"
                        maxHeight="200px"
                      />
                    )}
                    {r.status === 'passed' && r.actual_output && (
                      <Disclosure summary="Actual output">
                        <pre className="max-h-40 overflow-auto rounded-[3px] border border-line bg-code p-2 font-mono text-[11px] text-ink">
                          {r.actual_output}
                        </pre>
                      </Disclosure>
                    )}
                  </div>
                )}
              </li>
            )
          })}
        </ul>
      )}

      {!suiteRun && running && (
        <div className="flex items-center gap-2 px-3 py-3 text-sec">
          <Circle className="h-3 w-3 text-mute" aria-hidden="true" />
          <span>Preparing candidate and running {totalCases} test case{totalCases === 1 ? '' : 's'}…</span>
        </div>
      )}
    </section>
  )
}
