import { useEffect, useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import {
  ArrowDown,
  ArrowUp,
  CheckCircle2,
  Circle,
  FlaskConical,
  Play,
  Plus,
  Trash2,
  XCircle,
} from 'lucide-react'
import { api, wsPath } from '../api'
import type { components } from '../api/schema'
import { Button, Confirm, Loading, Notice, SwapLabel, inputCls } from './ui'
import { WorkStatusbar, WorkView } from './ApplicationWorkView'
import { EmptyState, Select } from '../ui/core'
import { JsonEditor, formatJson, validateJson } from '../ui/JsonEditor'
import { CodeDiffView } from '../ui/CodeDiffView'
import type { TestCaseResult, TestSuiteRun } from './TestChecksPanel'

type TestCase = components['schemas']['ApplicationTestCase']
type TestCaseListResp = components['schemas']['ApplicationTestCaseListResponse']

type InterruptDraft = {
  expect_handler: string
  expect_context: string
  context_match_mode: 'exact' | 'subset'
  resolution: 'resolved' | 'expired'
  event: string
}

type CaseDraft = {
  id: string | null // null = unsaved new case
  name: string
  description: string
  input: string
  interrupts: InterruptDraft[]
  expect_status: 'succeeded' | 'error'
  expected_output: string
  output_match_mode: 'exact' | 'subset'
  expected_error_code: string
}

function prettyJson(raw: unknown, fallback = ''): string {
  if (raw === undefined || raw === null) return fallback
  if (typeof raw === 'string') {
    const t = raw.trim()
    if (!t) return fallback
    try {
      return JSON.stringify(JSON.parse(t), null, 2)
    } catch {
      return raw
    }
  }
  try {
    return JSON.stringify(raw, null, 2)
  } catch {
    return fallback
  }
}

function toDraft(tc: TestCase): CaseDraft {
  return {
    id: tc.id,
    name: tc.name,
    description: tc.description ?? '',
    input: prettyJson(tc.input, '{}'),
    interrupts: (tc.interrupts ?? []).map((it) => ({
      expect_handler: it.expect_handler ?? '',
      expect_context: it.expect_context !== undefined ? prettyJson(it.expect_context, '') : '',
      context_match_mode: it.context_match_mode === 'subset' ? 'subset' : 'exact',
      resolution: it.resolution === 'expired' ? 'expired' : 'resolved',
      event: it.event !== undefined ? prettyJson(it.event, '{}') : '{}',
    })),
    expect_status: tc.expect_status === 'error' ? 'error' : 'succeeded',
    expected_output: tc.expected_output !== undefined ? prettyJson(tc.expected_output, '') : '',
    output_match_mode: tc.output_match_mode === 'subset' ? 'subset' : 'exact',
    expected_error_code: tc.expected_error_code ?? '',
  }
}

function emptyDraft(): CaseDraft {
  return {
    id: null,
    name: '',
    description: '',
    input: '{\n  "name": "world"\n}',
    interrupts: [],
    expect_status: 'succeeded',
    expected_output: '',
    output_match_mode: 'exact',
    expected_error_code: '',
  }
}

function draftPayload(d: CaseDraft): Record<string, unknown> {
  const inputVal: unknown = d.input.trim() ? JSON.parse(d.input) : {}
  const body: Record<string, unknown> = {
    name: d.name.trim(),
    description: d.description.trim(),
    input: inputVal,
    expect_status: d.expect_status,
    output_match_mode: d.output_match_mode,
    interrupts: d.interrupts.map((it) => {
      const item: Record<string, unknown> = {
        context_match_mode: it.context_match_mode,
        resolution: it.resolution,
      }
      if (it.expect_handler.trim()) item.expect_handler = it.expect_handler.trim()
      if (it.expect_context.trim()) item.expect_context = JSON.parse(it.expect_context)
      if (it.resolution === 'resolved') {
        item.event = it.event.trim() ? JSON.parse(it.event) : {}
      }
      return item
    }),
  }
  if (d.expect_status === 'succeeded' && d.expected_output.trim()) {
    body.expected_output = JSON.parse(d.expected_output)
  }
  if (d.expect_status === 'error' && d.expected_error_code.trim()) {
    body.expected_error_code = d.expected_error_code.trim()
  }
  return body
}

function validateDraft(d: CaseDraft): string | null {
  if (!d.name.trim()) return 'Test case name is required.'
  const inErr = validateJson(d.input)
  if (inErr) return `Input JSON: ${inErr}`
  for (let i = 0; i < d.interrupts.length; i++) {
    const it = d.interrupts[i]
    if (it.expect_context.trim()) {
      const ce = validateJson(it.expect_context)
      if (ce) return `Interrupt #${i + 1} expected context JSON: ${ce}`
    }
    if (it.resolution === 'resolved') {
      const ee = validateJson(it.event)
      if (ee) return `Interrupt #${i + 1} event JSON: ${ee}`
      try {
        const parsed = JSON.parse(it.event.trim() || '{}')
        if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
          return `Interrupt #${i + 1} event must be a JSON object.`
        }
      } catch {
        return `Interrupt #${i + 1} event must be a JSON object.`
      }
    }
  }
  if (d.expect_status === 'succeeded' && d.expected_output.trim()) {
    const oe = validateJson(d.expected_output)
    if (oe) return `Expected output JSON: ${oe}`
  }
  return null
}

// ApplicationTests: flush workbench Test Cases tab.
// Supports initial input, strong output assertions (exact/subset or error),
// multi-turn interrupt expectations & resume/expire inputs, plus 1-click
// capture from actual execution results.
export function ApplicationTests({
  appId,
  draftVersion,
  canEdit,
  handlerSuggestions,
  focusCaseId,
  onSaveDraftBeforeRun,
}: {
  appId: string
  draftVersion: number
  canEdit: boolean
  handlerSuggestions: string[]
  focusCaseId?: string | null
  onSaveDraftBeforeRun: () => Promise<number | null>
}) {
  const qc = useQueryClient()
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [draft, setDraft] = useState<CaseDraft | null>(null)
  const [dirty, setDirty] = useState(false)
  const [saving, setSaving] = useState(false)
  const [running, setRunning] = useState<'all' | 'one' | null>(null)
  const [err, setErr] = useState('')
  const [delTarget, setDelTarget] = useState<TestCase | null>(null)
  const [resultsByCase, setResultsByCase] = useState<Record<string, TestCaseResult>>({})

  const q = useQuery({
    queryKey: ['app-test-cases', appId],
    queryFn: () => api.get<TestCaseListResp>(wsPath(`/api/v1/applications/${encodeURIComponent(appId)}/test-cases`)),
  })

  const cases = useMemo(() => q.data?.test_cases ?? [], [q.data?.test_cases])

  // Seed latest candidate results from server when available.
  useEffect(() => {
    const lr = q.data?.latest_run
    if (!lr?.results?.length) return
    setResultsByCase((prev) => {
      const next = { ...prev }
      for (const r of lr.results) {
        if (!next[r.case_id]) next[r.case_id] = r
      }
      return next
    })
  }, [q.data?.latest_run])

  // Focus a specific case when jumped from PublishReviewDialog.
  useEffect(() => {
    if (!focusCaseId) return
    const found = cases.find((c) => c.id === focusCaseId)
    if (found) {
      setSelectedId(found.id)
      setDraft(toDraft(found))
      setDirty(false)
      setErr('')
    }
  }, [focusCaseId, cases])

  // Auto-select first case when list loads and nothing is selected.
  useEffect(() => {
    if (draft !== null || cases.length === 0) return
    setSelectedId(cases[0].id)
    setDraft(toDraft(cases[0]))
    setDirty(false)
  }, [cases, draft])

  const selectCase = (tc: TestCase) => {
    setSelectedId(tc.id)
    setDraft(toDraft(tc))
    setDirty(false)
    setErr('')
  }

  const startNew = () => {
    setSelectedId(null)
    setDraft(emptyDraft())
    setDirty(true)
    setErr('')
  }

  const updateDraft = (patch: Partial<CaseDraft>) => {
    setDraft((cur) => (cur ? { ...cur, ...patch } : cur))
    setDirty(true)
    setErr('')
  }

  const saveCurrent = async (): Promise<TestCase | null> => {
    if (!draft) return null
    const verr = validateDraft(draft)
    if (verr) {
      setErr(verr)
      return null
    }
    setSaving(true)
    setErr('')
    try {
      const payload = draftPayload(draft)
      let saved: TestCase
      if (draft.id) {
        const res = await api.put<{ test_case: TestCase }>(
          wsPath(`/api/v1/applications/${encodeURIComponent(appId)}/test-cases/${encodeURIComponent(draft.id)}`),
          payload,
        )
        saved = res.test_case
      } else {
        const res = await api.post<{ test_case: TestCase }>(
          wsPath(`/api/v1/applications/${encodeURIComponent(appId)}/test-cases`),
          payload,
        )
        saved = res.test_case
      }
      setSelectedId(saved.id)
      setDraft(toDraft(saved))
      setDirty(false)
      await qc.invalidateQueries({ queryKey: ['app-test-cases', appId] })
      return saved
    } catch (e) {
      setErr((e as Error).message)
      return null
    } finally {
      setSaving(false)
    }
  }

  const runCases = async (mode: 'all' | 'one') => {
    if (running) return
    setErr('')
    let targetId = selectedId
    if (dirty && draft) {
      const saved = await saveCurrent()
      if (!saved) return
      targetId = saved.id
    }
    if (mode === 'one' && !targetId) return
    setRunning(mode)
    try {
      const latestVer = await onSaveDraftBeforeRun()
      const ver = latestVer ?? draftVersion
      const body: Record<string, unknown> = { version: ver }
      if (mode === 'one' && targetId) body.case_ids = [targetId]
      const suite = await api.post<TestSuiteRun>(
        wsPath(`/api/v1/applications/${encodeURIComponent(appId)}/test-cases/run`),
        body,
      )
      setResultsByCase((prev) => {
        const next = { ...prev }
        for (const r of suite.results) next[r.case_id] = r
        return next
      })
      if (mode === 'all') {
        await qc.invalidateQueries({ queryKey: ['app-test-cases', appId] })
      }
    } catch (e) {
      setErr((e as Error).message)
    } finally {
      setRunning(null)
    }
  }

  const activeResult = selectedId ? resultsByCase[selectedId] : undefined
  const passedCount = cases.filter((c) => resultsByCase[c.id]?.status === 'passed').length
  const failedCount = cases.filter((c) => resultsByCase[c.id]?.status === 'failed').length

  const updateInterrupt = (idx: number, patch: Partial<InterruptDraft>) => {
    if (!draft) return
    const next = draft.interrupts.map((it, i) => (i === idx ? { ...it, ...patch } : it))
    updateDraft({ interrupts: next })
  }

  const addInterrupt = (seed?: Partial<InterruptDraft>) => {
    if (!draft) return
    updateDraft({
      interrupts: [
        ...draft.interrupts,
        {
          expect_handler: seed?.expect_handler ?? '',
          expect_context: seed?.expect_context ?? '',
          context_match_mode: seed?.context_match_mode ?? 'exact',
          resolution: seed?.resolution ?? 'resolved',
          event: seed?.event ?? '{\n  "approved": true\n}',
        },
      ],
    })
  }

  const removeInterrupt = (idx: number) => {
    if (!draft) return
    updateDraft({ interrupts: draft.interrupts.filter((_, i) => i !== idx) })
  }

  const moveInterrupt = (idx: number, dir: -1 | 1) => {
    if (!draft) return
    const target = idx + dir
    if (target < 0 || target >= draft.interrupts.length) return
    const next = [...draft.interrupts]
    const [moved] = next.splice(idx, 1)
    next.splice(target, 0, moved)
    updateDraft({ interrupts: next })
  }

  const selectedEntity = cases.find((c) => c.id === selectedId) ?? null
  const unexpectedTurn = activeResult?.turns.find((t) => t.status === 'unexpected_interrupt')

  return (
    <WorkView>
      {/* Flush workbench toolbar */}
      <div className="wb-toolbar">
        <h3 className="wb-pane-title">Tests</h3>
        <span className="wb-status-num text-sec">{cases.length}</span>
        {failedCount > 0 ? (
          <span className="wb-toolbar-note text-danger">{failedCount} failing · {passedCount} passed</span>
        ) : passedCount > 0 ? (
          <span className="wb-toolbar-note text-live">{passedCount}/{cases.length} passed</span>
        ) : (
          <span className="wb-toolbar-note">Run automatically during Publish</span>
        )}
        <div className="wb-toolbar-side">
          {canEdit && (
            <Button variant="ghost" size="xs" onClick={startNew} data-testid="tc-new">
              <Plus className="mr-1 h-3.5 w-3.5" strokeWidth={1.75} aria-hidden="true" />
              New test case
            </Button>
          )}
          <Button
            variant="primary"
            size="xs"
            disabled={cases.length === 0 || running !== null || !canEdit}
            onClick={() => void runCases('all')}
            data-testid="tc-run-all"
          >
            <Play className="mr-1 h-3 w-3" strokeWidth={2} aria-hidden="true" />
            <SwapLabel show={running === 'all' ? 'Running…' : 'Run all'} options={['Run all', 'Running…']} />
          </Button>
        </div>
      </div>

      {/* Flush two-pane split: left case directory | right turn-by-turn editor */}
      <div className="wb-pane-split wb-fill min-h-0">
        <aside className="wb-pane shrink-0" style={{ flex: '0 0 260px' }} aria-label="Test cases list">
          <div className="wb-pane-body p-1.5">
            {q.isLoading && <div className="p-3"><Loading /></div>}
            {q.isError && <div className="p-2"><Notice tone="warning">Could not load test cases.</Notice></div>}
            {!q.isLoading && !q.isError && cases.length === 0 && !draft && (
              <div className="p-3 text-xs text-sec">
                No test cases yet. Add one to validate inputs, interrupts, and outputs before publishing.
              </div>
            )}
            <div className="space-y-0.5">
              {cases.map((tc) => {
                const res = resultsByCase[tc.id]
                const isSelected = selectedId === tc.id && draft?.id === tc.id
                const intrN = tc.interrupts?.length ?? 0
                return (
                  <button
                    key={tc.id}
                    type="button"
                    onClick={() => selectCase(tc)}
                    className={`flex w-full items-center gap-2 rounded-[3px] px-2.5 py-2 text-left text-xs ${
                      isSelected ? 'bg-accent/10 text-accent' : 'text-ink hover:bg-surface'
                    }`}
                  >
                    {res?.status === 'passed' ? (
                      <CheckCircle2 className="h-3.5 w-3.5 shrink-0 text-live" strokeWidth={2} aria-hidden="true" />
                    ) : res?.status === 'failed' ? (
                      <XCircle className="h-3.5 w-3.5 shrink-0 text-danger" strokeWidth={2} aria-hidden="true" />
                    ) : (
                      <Circle className="h-3.5 w-3.5 shrink-0 text-mute" strokeWidth={1.75} aria-hidden="true" />
                    )}
                    <span className="min-w-0 flex-1 truncate font-medium">{tc.name}</span>
                    {intrN > 0 && (
                      <span className="shrink-0 font-mono text-[10px] text-mute">{intrN} intr</span>
                    )}
                  </button>
                )
              })}
              {draft && draft.id === null && (
                <div className="flex items-center gap-2 rounded-[3px] bg-accent/10 px-2.5 py-2 text-xs text-accent">
                  <Circle className="h-3.5 w-3.5 shrink-0" strokeWidth={1.75} aria-hidden="true" />
                  <span className="truncate font-medium">{draft.name.trim() || 'Untitled test case'}</span>
                </div>
              )}
            </div>
          </div>
        </aside>

        <section className="wb-pane wb-fill min-w-0">
          <div className="wb-pane-body p-5">
            {!draft ? (
              <EmptyState
                icon={<FlaskConical className="h-5 w-5" strokeWidth={1.5} />}
                title="No test cases yet"
                description="Define inputs, expected outputs, and multi-turn interrupt responses. All test cases run automatically in the publish checklist."
                action={canEdit ? <Button variant="primary" size="sm" onClick={startNew}>New test case</Button> : undefined}
              />
            ) : (
              <div className="mx-auto max-w-3xl space-y-4 text-xs">
                {err && <Notice tone="warning">{err}</Notice>}

                {/* Case title & actions */}
                <div className="flex flex-wrap items-start gap-3">
                  <div className="min-w-[220px] flex-1 space-y-1.5">
                    <input
                      aria-label="Test case name"
                      className={`${inputCls} font-medium`}
                      placeholder="Test case name (e.g. Approve high-value order)"
                      maxLength={128}
                      disabled={!canEdit}
                      value={draft.name}
                      onChange={(e) => updateDraft({ name: e.target.value })}
                      data-testid="tc-name"
                    />
                    <input
                      aria-label="Test case description"
                      className={inputCls}
                      placeholder="Optional description"
                      maxLength={500}
                      disabled={!canEdit}
                      value={draft.description}
                      onChange={(e) => updateDraft({ description: e.target.value })}
                    />
                  </div>
                  {canEdit && (
                    <div className="flex shrink-0 items-center gap-1.5">
                      <Button
                        size="sm"
                        disabled={running !== null || saving}
                        onClick={() => void runCases('one')}
                        data-testid="tc-run-one"
                      >
                        <Play className="mr-1 h-3 w-3" strokeWidth={2} aria-hidden="true" />
                        <SwapLabel show={running === 'one' ? 'Running…' : 'Run'} options={['Run', 'Running…']} />
                      </Button>
                      <Button
                        variant="primary"
                        size="sm"
                        disabled={!dirty || saving}
                        onClick={() => void saveCurrent()}
                        data-testid="tc-save"
                      >
                        <SwapLabel show={saving ? 'Saving…' : 'Save'} options={['Save', 'Saving…']} />
                      </Button>
                      {selectedEntity && (
                        <Button
                          variant="ghost"
                          size="sm"
                          className="text-sec hover:text-danger"
                          onClick={() => setDelTarget(selectedEntity)}
                          aria-label="Delete test case"
                        >
                          <Trash2 className="h-3.5 w-3.5" strokeWidth={1.75} aria-hidden="true" />
                        </Button>
                      )}
                    </div>
                  )}
                </div>

                {/* Step 1: Initial input */}
                <div className="rounded-[3px] border border-line bg-paper">
                  <div className="flex items-center justify-between border-b border-line bg-surface px-3 py-1.5">
                    <span className="font-mono font-medium text-ink">1. Input · main(input)</span>
                    <button
                      type="button"
                      className="text-btn"
                      disabled={!canEdit || !!validateJson(draft.input)}
                      onClick={() => {
                        const r = formatJson(draft.input)
                        if (r.formatted !== undefined) updateDraft({ input: r.formatted })
                      }}
                    >
                      Format
                    </button>
                  </div>
                  <div className="p-3">
                    <JsonEditor
                      ariaLabel="Test case input JSON"
                      readOnly={!canEdit}
                      value={draft.input}
                      onChange={(v) => updateDraft({ input: v })}
                      minHeight="84px"
                      maxHeight="200px"
                    />
                  </div>
                </div>

                {/* Step 2..N: Interrupt turns */}
                <datalist id={`tc-handlers-${appId}`}>
                  {handlerSuggestions.map((h) => <option key={h} value={h} />)}
                </datalist>

                {draft.interrupts.map((it, idx) => (
                  <div key={idx} className="rounded-[3px] border border-line bg-paper">
                    <div className="flex items-center gap-2 border-b border-line bg-surface px-3 py-1.5">
                      <span className="font-mono font-medium text-ink">
                        {idx + 2}. Interrupt turn #{idx + 1}
                      </span>
                      <div className="ml-auto flex items-center gap-1">
                        {canEdit && (
                          <>
                            <Button variant="ghost" size="xs" disabled={idx === 0} onClick={() => moveInterrupt(idx, -1)} aria-label="Move turn up">
                              <ArrowUp className="h-3 w-3" />
                            </Button>
                            <Button variant="ghost" size="xs" disabled={idx === draft.interrupts.length - 1} onClick={() => moveInterrupt(idx, 1)} aria-label="Move turn down">
                              <ArrowDown className="h-3 w-3" />
                            </Button>
                            <Button variant="ghost" size="xs" className="text-sec hover:text-danger" onClick={() => removeInterrupt(idx)} aria-label="Remove interrupt turn">
                              <Trash2 className="h-3 w-3" />
                            </Button>
                          </>
                        )}
                      </div>
                    </div>

                    <div className="grid gap-4 p-3 md:grid-cols-2">
                      {/* Expected interrupt output (handler + context) */}
                      <div className="space-y-2">
                        <div className="font-medium text-sec">Expected interrupt output</div>
                        <input
                          aria-label={`Interrupt ${idx + 1} expected handler`}
                          list={`tc-handlers-${appId}`}
                          className={inputCls}
                          placeholder="Expected handler (e.g. on_approve, optional)"
                          disabled={!canEdit}
                          value={it.expect_handler}
                          onChange={(e) => updateInterrupt(idx, { expect_handler: e.target.value })}
                        />
                        <div className="flex items-center justify-between gap-2">
                          <span className="text-[11px] text-mute">Expected context JSON (optional)</span>
                          <Select
                            ariaLabel={`Interrupt ${idx + 1} context match mode`}
                            size="xs"
                            disabled={!canEdit}
                            value={it.context_match_mode}
                            onChange={(v) => updateInterrupt(idx, { context_match_mode: v })}
                            options={[
                              { value: 'exact', label: 'Exact match' },
                              { value: 'subset', label: 'Subset match' },
                            ]}
                          />
                        </div>
                        <JsonEditor
                          ariaLabel={`Interrupt ${idx + 1} expected context JSON`}
                          readOnly={!canEdit}
                          value={it.expect_context}
                          onChange={(v) => updateInterrupt(idx, { expect_context: v })}
                          minHeight="76px"
                          maxHeight="160px"
                        />
                      </div>

                      {/* Simulated handler input (event or expire) */}
                      <div className="space-y-2">
                        <div className="flex items-center justify-between gap-2">
                          <span className="font-medium text-sec">Simulated handler input</span>
                          <Select
                            ariaLabel={`Interrupt ${idx + 1} resolution`}
                            size="xs"
                            disabled={!canEdit}
                            value={it.resolution}
                            onChange={(v) => updateInterrupt(idx, { resolution: v })}
                            options={[
                              { value: 'resolved', label: 'Send resume event' },
                              { value: 'expired', label: 'Simulate expire (on_expire)' },
                            ]}
                          />
                        </div>
                        {it.resolution === 'resolved' ? (
                          <JsonEditor
                            ariaLabel={`Interrupt ${idx + 1} resume event JSON`}
                            readOnly={!canEdit}
                            value={it.event}
                            onChange={(v) => updateInterrupt(idx, { event: v })}
                            minHeight="112px"
                            maxHeight="180px"
                          />
                        ) : (
                          <div className="rounded-[3px] border border-line bg-surface px-3 py-4 text-sec">
                            Simulates deadline expiration and invokes the interrupt’s <code>on_expire</code> handler with <code>resolution = "expired"</code>.
                          </div>
                        )}
                      </div>
                    </div>
                  </div>
                ))}

                {canEdit && (
                  <div>
                    <Button variant="ghost" size="xs" onClick={() => addInterrupt()} data-testid="tc-add-interrupt">
                      <Plus className="mr-1 h-3.5 w-3.5" strokeWidth={1.75} aria-hidden="true" />
                      Add interrupt turn
                    </Button>
                  </div>
                )}

                {/* Final expected output assertion */}
                <div className="rounded-[3px] border border-line bg-paper">
                  <div className="flex flex-wrap items-center gap-2 border-b border-line bg-surface px-3 py-1.5">
                    <span className="font-mono font-medium text-ink">
                      {draft.interrupts.length + 2}. Expected final output
                    </span>
                    <div className="ml-auto flex items-center gap-2">
                      <Select
                        ariaLabel="Expected outcome"
                        size="xs"
                        disabled={!canEdit}
                        value={draft.expect_status}
                        onChange={(v) => updateDraft({ expect_status: v })}
                        options={[
                          { value: 'succeeded', label: 'Succeeded (output)' },
                          { value: 'error', label: 'Expected error' },
                        ]}
                      />
                      {draft.expect_status === 'succeeded' && (
                        <Select
                          ariaLabel="Output match mode"
                          size="xs"
                          disabled={!canEdit}
                          value={draft.output_match_mode}
                          onChange={(v) => updateDraft({ output_match_mode: v })}
                          options={[
                            { value: 'exact', label: 'Exact match' },
                            { value: 'subset', label: 'Subset match' },
                          ]}
                        />
                      )}
                    </div>
                  </div>
                  <div className="p-3">
                    {draft.expect_status === 'succeeded' ? (
                      <JsonEditor
                        ariaLabel="Expected output JSON"
                        readOnly={!canEdit}
                        value={draft.expected_output}
                        onChange={(v) => updateDraft({ expected_output: v })}
                        minHeight="84px"
                        maxHeight="200px"
                      />
                    ) : (
                      <input
                        aria-label="Expected error code"
                        className={inputCls}
                        placeholder="Optional error code (e.g. invalid_input, runtime)"
                        disabled={!canEdit}
                        value={draft.expected_error_code}
                        onChange={(e) => updateDraft({ expected_error_code: e.target.value })}
                      />
                    )}
                  </div>
                </div>

                {/* Live execution result + 1-click capture helpers */}
                {activeResult && (
                  <div className="rounded-[3px] border border-line bg-paper" aria-label="Latest test result">
                    <div className="flex flex-wrap items-center gap-2 border-b border-line bg-surface px-3 py-2">
                      {activeResult.status === 'passed' ? (
                        <CheckCircle2 className="h-4 w-4 shrink-0 text-live" strokeWidth={2} aria-hidden="true" />
                      ) : (
                        <XCircle className="h-4 w-4 shrink-0 text-danger" strokeWidth={2} aria-hidden="true" />
                      )}
                      <span className="font-semibold text-ink">
                        {activeResult.status === 'passed' ? 'Passed' : 'Failed'}
                      </span>
                      <span className="font-mono text-[11px] text-sec tabular-nums">
                        {activeResult.duration_ms}ms · {activeResult.steps} steps
                      </span>
                      {canEdit && unexpectedTurn && (
                        <Button
                          size="xs"
                          className="ml-auto"
                          onClick={() => addInterrupt({
                            expect_handler: unexpectedTurn.actual_handler ?? '',
                            expect_context: unexpectedTurn.actual_context ?? '',
                          })}
                        >
                          + Record interrupt ({unexpectedTurn.actual_handler})
                        </Button>
                      )}
                      {canEdit && activeResult.actual_output && draft.expect_status === 'succeeded' && (
                        <Button
                          size="xs"
                          className={unexpectedTurn ? '' : 'ml-auto'}
                          onClick={() => updateDraft({ expected_output: activeResult.actual_output ?? '' })}
                        >
                          Use actual output as expected
                        </Button>
                      )}
                    </div>

                    <div className="space-y-2.5 p-3">
                      {activeResult.error_message && (
                        <div className="rounded-[3px] border border-danger/30 bg-danger/5 px-2.5 py-1.5 text-danger">
                          {activeResult.error_message}
                        </div>
                      )}
                      {activeResult.turns.map((t) => (
                        <div key={`${t.turn}-${t.entry}`} className="rounded-[3px] border border-line bg-surface/30 px-2.5 py-1.5">
                          <div className="flex items-center gap-2">
                            {t.status === 'passed' ? (
                              <CheckCircle2 className="h-3.5 w-3.5 shrink-0 text-live" strokeWidth={2} aria-hidden="true" />
                            ) : (
                              <XCircle className="h-3.5 w-3.5 shrink-0 text-danger" strokeWidth={2} aria-hidden="true" />
                            )}
                            <span className="font-mono font-medium text-ink">Step {t.turn + 1}: {t.entry}</span>
                            {t.actual_handler && (
                              <span className="font-mono text-[11px] text-sec">
                                → {t.actual_handler} ({t.resolution || 'paused'})
                              </span>
                            )}
                            <span className="ml-auto font-mono text-[11px] text-mute tabular-nums">{t.duration_ms}ms</span>
                          </div>
                          {t.expected_context && t.actual_context && t.status === 'failed' && (
                            <div className="mt-2">
                              <CodeDiffView
                                title="Interrupt context diff (expected vs actual)"
                                before={t.expected_context}
                                after={t.actual_context}
                                language="json"
                                maxHeight="160px"
                              />
                            </div>
                          )}
                        </div>
                      ))}
                      {activeResult.expected_output && activeResult.actual_output && activeResult.status === 'failed' && (
                        <CodeDiffView
                          title="Output diff (expected vs actual)"
                          before={activeResult.expected_output}
                          after={activeResult.actual_output}
                          language="json"
                          maxHeight="200px"
                        />
                      )}
                      {activeResult.status === 'passed' && activeResult.actual_output && (
                        <pre className="max-h-48 overflow-auto rounded-[3px] border border-line bg-code p-2 font-mono text-[11px] text-ink">
                          {activeResult.actual_output}
                        </pre>
                      )}
                    </div>
                  </div>
                )}
              </div>
            )}
          </div>
        </section>
      </div>

      <WorkStatusbar>
        <span className="wb-grow wb-status-num">
          {cases.length} test case{cases.length === 1 ? '' : 's'}
        </span>
      </WorkStatusbar>

      {delTarget && (
        <Confirm
          tone="danger"
          title={`Delete test case “${delTarget.name}”?`}
          confirm="Delete"
          pending="Deleting…"
          onClose={() => setDelTarget(null)}
          action={async () => {
            await api.del(wsPath(`/api/v1/applications/${encodeURIComponent(appId)}/test-cases/${encodeURIComponent(delTarget.id)}`))
            setSelectedId(null)
            setDraft(null)
            setDirty(false)
            await qc.invalidateQueries({ queryKey: ['app-test-cases', appId] })
          }}
        >
          <p className="text-sm text-body">This removes the saved test case from the application’s release checklist.</p>
        </Confirm>
      )}
    </WorkView>
  )
}
