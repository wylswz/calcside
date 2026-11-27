import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import * as Tabs from '@radix-ui/react-tabs'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api, ApiError, Application, Run, postApplication, workspaces } from '../api'
import { applicationDescription } from '../activity'
import { Button, Confirm, Field, Loading, Notice, RevealNew, SwapLabel, inputCls } from '../components/ui'
import { DetailWorkbenchHeader, DetailWorkbenchViews } from '../components/DetailWorkbench'
import { StatusCell, WorkView, WorkStatusbar } from '../components/ApplicationWorkView'
import { PublishReviewDialog } from '../components/PublishReviewDialog'
import { useApplicationEditor } from '../components/editor'
import { useRunProgress, WaitHead } from '../components/RunProgress'
import { ResumeForm } from '../components/ResumeForm'
import { SpecForm, type SpecFormHandle } from '../components/SpecForm'
import type { DraftError, InstanceDraft, InstanceSpec } from '../instanceDraft'
import './application.css'
import { ApplicationCallExamples } from '../components/ApplicationCallExamples'
import { CopyButton } from '../components/MCP'
import { AppKeyModal } from '../components/AppKeyModal'
import { useHeldOrder } from '../stableList'
import { fmtDateTime, type RunPhase } from '../interrupts'
import { ApplicationNotebook } from '../components/ApplicationNotebook'
import { entryFunctions } from '../entryFunctions'

const appSections = [
  { value: 'develop', label: 'Editor', key: '1', icon: 'm5.5 4.5-3.5 3.5 3.5 3.5m5-7 3.5 3.5-3.5 3.5' },
  { value: 'invoke', label: 'Invoke', key: '2', icon: 'm2.5 2 11 6-11 6 2-6-2-6Zm2 6H9' },
  { value: 'activity', label: 'Activity', key: '3', icon: 'M1.5 8h3l2-4 3 8 2-4h3' },
] as const

type Draft = {
  name: string
  sections: NonNullable<Application['sections']>
  input: string
  spec: string
  inOn: boolean
  inText: string
  outOn: boolean
  outText: string
}
type Saved = { draft: Draft; version: number; fp: string }

const DEFAULT_INPUT = '{\n  "name": "world"\n}'

const statusHint: Record<string, string> = {
  draft: 'Not published.',
  preparing: 'Deploying…',
  degraded: 'Deployment unhealthy.',
  disabled: 'Deployment disabled.',
}

function draftOf(a: Application): Draft {
  // spec_json is the verbatim stored text — re-serializing the decoded
  // object would round precision-limited numbers in limits.
  return {
    name: a.name, sections: a.sections ?? [], input: DEFAULT_INPUT,
    spec: a.spec_json ?? '{}',
    inOn: a.input_schema != null, inText: a.input_schema ?? '',
    outOn: a.output_schema != null, outText: a.output_schema ?? '',
  }
}

// The SpecForm edits a parsed copy of the stored spec text; an
// unparseable stored spec simply starts empty rather than breaking
// the editor.
function parseSpec(text: string): InstanceSpec | undefined {
  try {
    const v = JSON.parse(text.trim() || '{}')
    return v && typeof v === 'object' && !Array.isArray(v) ? (v as InstanceSpec) : undefined
  } catch { return undefined }
}

// sameJSON deep-compares ignoring object key order — key order differs
// between a stored spec_json and the builder's serialization.
function sameJSON(a: unknown, b: unknown): boolean {
  const canon = (v: unknown): string => {
    if (Array.isArray(v)) return `[${v.map(canon).join(',')}]`
    if (v && typeof v === 'object') return `{${Object.keys(v).sort().map((k) => `${JSON.stringify(k)}:${canon((v as Record<string, unknown>)[k])}`).join(',')}}`
    return JSON.stringify(v)
  }
  return canon(a) === canon(b)
}

function fp(d: Draft): string {
  return JSON.stringify([d.name, d.sections, d.spec, d.inOn, d.inText, d.outOn, d.outText])
}

function validSaved(x: unknown): x is Saved {
  const s = x as Saved
  if (!s || typeof s !== 'object' || !Number.isSafeInteger(s.version) || s.version < 1 || typeof s.fp !== 'string') return false
  const d = s.draft as Draft
  const shape = !!d && typeof d === 'object' &&
    ['name', 'input', 'spec', 'inText', 'outText'].every((k) => typeof (d as any)[k] === 'string') &&
    ['inOn', 'outOn'].every((k) => typeof (d as any)[k] === 'boolean')
  if (!shape) return false
  if (!Array.isArray(d.sections) || d.sections.length < 1 || d.sections.length > 64) return false
  const ids = new Set<string>()
  for (const s of d.sections) {
    if (!s || typeof s.id !== 'string' || typeof s.code !== 'string' || ids.has(s.id)) return false
    ids.add(s.id)
  }
  return true
}

function scopeKey(user: string, ws: string, id: string) { return JSON.stringify(['appv2', user, ws, id]) }

// autosavable: the spec text parses as JSON (or is empty) and every
// enabled schema is non-empty JSON — the shapes the PUT accepts.
function autosavable(d: Draft): boolean {
  try {
    if (d.spec.trim()) JSON.parse(d.spec)
    for (const [on, t] of [[d.inOn, d.inText], [d.outOn, d.outText]] as const) {
      if (on && !t.trim()) return false
      if (on) JSON.parse(t)
    }
    return true
  } catch { return false }
}
const mirror = new Map<string, Saved>()

function loadSaved(key: string, app: Application): { s: Saved; corrupt: boolean } {
  const fresh: Saved = { draft: draftOf(app), version: app.version, fp: fp(draftOf(app)) }
  const local = mirror.get(key)
  if (local) return { s: local, corrupt: false }
  try {
    const raw = sessionStorage.getItem(key)
    if (raw) {
      let x: unknown
      try { x = JSON.parse(raw) } catch { return { s: fresh, corrupt: true } }
      if (validSaved(x)) return { s: x, corrupt: false }
      return { s: fresh, corrupt: true } // keep the blob for the user to reset
    }
  } catch { /* storage broken */ }
  return { s: mirror.get(key) ?? fresh, corrupt: false }
}

function persist(key: string, s: Saved): boolean {
  mirror.set(key, s)
  try { sessionStorage.setItem(key, JSON.stringify(s)); return true } catch { return false }
}

// Outer loader: resolves the app + caller scope, then mounts a keyed
// inner editor so navigating A->B can never carry A's draft into B.
export default function ApplicationDetail() {
  const { id } = useParams<{ id: string }>()
  const qc = useQueryClient()
  const me = qc.getQueryData<{ user: { id: string } }>(['me'])
  const wsq = useQuery({ queryKey: ['workspaces'], queryFn: workspaces })
  const q = useQuery({
    queryKey: ['application', id],
    queryFn: () => api.get<{ application: Application }>(`/api/v1/applications/${id}`).then((r) => r.application),
    // poll faster while a publish is deploying so Invoke unblocks sooner
    refetchInterval: (q) => (q.state.data?.status === 'preparing' ? 1000 : 5000),
    retry: false,
  })
  if (q.isLoading || !me?.user) return <Loading />
  if (q.isError) return <Notice tone="warning">Could not load application: {(q.error as Error).message}</Notice>
  const app = q.data!
  const role = wsq.data?.find((w) => w.id === app.workspace_id)?.role ?? 'viewer'
  return <Editor key={scopeKey(me.user.id, app.workspace_id, app.id)} app={app} role={role}
    userID={me.user.id} qc={qc} />
}

type Props = { app: Application; role: string; userID: string; qc: ReturnType<typeof useQueryClient> }

function Editor({ app, role, userID, qc }: Props) {
  const nav = useNavigate()
  const scope = scopeKey(userID, app.workspace_id, app.id)
  const [init] = useState(() => loadSaved(scope, app))
  const [draft, setDraft] = useState<Draft>(init.s.draft)
  const [base, setBase] = useState<Saved>(init.s)
  const [corrupt, setCorrupt] = useState(init.corrupt)
  const [resetOpen, setResetOpen] = useState(false)
  // fp of the draft whose save last failed; null = no outstanding failure.
  const [failedFp, setFailedFp] = useState<string | null>(null)
  const saveFailed = failedFp !== null
  const [warn, setWarn] = useState('')
  const [storeFailed, setStoreFailed] = useState(false)
  const [run, setRun] = useState<RunView | null>(null)
  const [outdated, setOutdated] = useState(false)
  const [busy, setBusy] = useState('')
  const [delOpen, setDelOpen] = useState(false)
  const [keyOpen, setKeyOpen] = useState(false)
  const [published, setPublished] = useState(false)
  const [reloadOpen, setReloadOpen] = useState<Application | null>(null)
  const [params, setParams] = useSearchParams()
  const activeView = params.get('view') === 'invoke' ? 'invoke' : params.get('view') === 'activity' ? 'activity' : 'develop'
  const [settingsOpen, setSettingsOpen] = useState(false)
  const sectionTabs = useRef<HTMLDivElement>(null)
  const goToView = useCallback((value: string) => {
    const next = new URLSearchParams(params)
    if (value === 'develop') {
      next.delete('view')
      next.delete('nbview')
      setSettingsOpen(false)
    } else {
      next.set('view', value)
    }
    if (next.toString() !== params.toString()) setParams(next)
  }, [params, setParams])
  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.defaultPrevented || e.isComposing || !e.altKey || e.ctrlKey || e.metaKey || e.shiftKey ||
        document.querySelector('[role="dialog"], [role="alertdialog"]')) return
      const section = appSections.find(({ key }) => e.code === `Digit${key}`)
      if (!section) return
      e.preventDefault()
      if (e.repeat) return
      goToView(section.value)
      requestAnimationFrame(() => sectionTabs.current?.querySelector<HTMLElement>('[aria-selected="true"]')?.focus())
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [goToView])
  const notebookSpecRef = useRef<SpecFormHandle>(null)
  // SpecForm remount key: bumped on reset so a corrupt stored spec is
  // re-parsed, never remounted while the user is editing.
  const [specKey, setSpecKey] = useState(0)
  const [specErrs, setSpecErrs] = useState<DraftError[]>([])
  const [initialSpec, setInitialSpec] = useState<InstanceSpec | undefined>(() => parseSpec(init.s.draft.spec))
  // The first onSpecChange is the pristine baseline — ignore it so an
  // untouched stored spec keeps its verbatim text (see draftOf).
  const specSeen = useRef(false)
  const mounted = useRef(false)
  // Completion is built from the draft spec — no live instance needed.
  const editor = useApplicationEditor(draft.spec, app.workspace_id)
  const req = useRef(0)
  const latest = useRef(draft)
  latest.current = draft
  const latestBase = useRef(base)
  latestBase.current = base
  useEffect(() => { mounted.current = true; return () => {
    mounted.current = false
    streamCtl.current.dev?.abort()
    streamCtl.current.call?.abort()
  } }, [])

  const view = role === 'owner' || role === 'editor' ? 'edit' : 'view'
  const dirty = fp(draft) !== base.fp || draft.input !== base.draft.input
  useEffect(() => {
    if (!dirty) return
    const h = (e: BeforeUnloadEvent) => { e.preventDefault() }
    window.addEventListener('beforeunload', h)
    return () => window.removeEventListener('beforeunload', h)
  }, [dirty])

  const edit = (patch: Partial<Draft> | ((cur: Draft) => Partial<Draft>)) => {
    if (corrupt) return
    setDraft((cur) => {
      const next = { ...cur, ...(typeof patch === 'function' ? patch(cur) : patch) }
      setStoreFailed(!persist(scope, { draft: next, version: latestBase.current.version, fp: latestBase.current.fp }))
      return next
    })
  }
  // Only a real user edit rewrites draft.spec: the baseline call is
  // skipped and no-op serializations are dropped, so a stored spec is
  // never re-serialized without a change (see draftOf for why its
  // verbatim text matters).
  const onSpec = useCallback((_d: InstanceDraft, errs?: DraftError[], built?: { spec?: InstanceSpec; specJSON?: string }) => {
    setSpecErrs(errs ?? [])
    if (!specSeen.current) { specSeen.current = true; return }
    if (!built) return
    const cur = latest.current.spec
    if (built.specJSON !== undefined) {
      // Advanced JSON mode carries verbatim text; only a real edit lands.
      if (built.specJSON !== cur) edit({ spec: built.specJSON })
      return
    }
    // Errors leave built.spec undefined — never clobber the stored
    // text with an empty serialization.
    if (!built.spec) return
    // An order-insensitive equality keeps the stored formatting (and
    // precision) when the form merely re-describes the same spec; an
    // unparseable stored spec is left alone too.
    let curParsed: unknown
    try { curParsed = JSON.parse(cur.trim() || 'null') } catch { return } // keep unparseable stored text
    if (!sameJSON(curParsed, built.spec)) edit({ spec: JSON.stringify(built.spec) })
  }, [])
  type Result = { id: string; output?: unknown; error?: { code: string; message: string }; duration_ms: number; steps: number; streaming?: boolean; stream_items?: number; stream_bytes?: number }
  type RunView = { result: Result; output_json?: string; run_id?: string; run_status?: string; waiting?: { interrupt_id: string; console_url: string } }
  type StreamView = { started: boolean; text: string; count: number; bytes: number; truncated: boolean; done?: RunView; error?: string; stopped?: boolean }
  // The Invoke card owns its own input/result; neither touches the draft.
  const [callInput, setCallInput] = useState(DEFAULT_INPUT)
  const [callRun, setCallRun] = useState<RunView | null>(null)
  const [devStream, setDevStream] = useState<StreamView | null>(null)
  const [callStream, setCallStream] = useState<StreamView | null>(null)
  const [stopKind, setStopKind] = useState<'' | 'dev' | 'call'>('')
  const streamCtl = useRef<{ dev?: AbortController; call?: AbortController }>({})
  const streamSeq = useRef(0)
  const STREAM_TEXT_CAP = 256 << 10
  const STREAM_ITEM_CAP = 1000

  // Save pipeline: snapshot everything BEFORE the flight; a draft that
  // changed during the save aborts the chained test/publish instead of
  // publishing code the user already moved past.
  const put = async (fromVersion: number, d: Draft): Promise<Application | null> => {
    if ((d.inOn && !d.inText.trim()) || (d.outOn && !d.outText.trim())) {
      throw new Error('An enabled custom schema cannot be empty.')
    }
    const spec = d.spec.trim() || 'null'
    const ins = d.inOn && d.inText.trim() ? JSON.stringify(d.inText) : 'null'
    const outs = d.outOn && d.outText.trim() ? JSON.stringify(d.outText) : 'null'
    const body = `{"version":${fromVersion},"name":${JSON.stringify(d.name)},"sections":${JSON.stringify(d.sections)},"spec":${spec},"input_schema":${ins},"output_schema":${outs}}`
    const r = await api.putRawJson<{ application: Application }>(`/api/v1/applications/${app.id}`, body)
    return r.application ?? null
  }

  // save runs the PUT only when the draft actually changed; returns the
  // advanced app or null (conflict / error already surfaced in warn).
  const save = async (): Promise<{ app2: Application; draftFp: string } | null> => {
    const snap = { ...latest.current }
    const snapFp = fp(snap)
    const base0 = latestBase.current
    if (snapFp === base0.fp) {
      if (app.version !== base0.version) throw new ApiError(409, 'conflict', 'Saved elsewhere.')
      return { app2: app, draftFp: snapFp } // nothing to save; chain proceeds
    }
    const rid = ++req.current
    const a2 = await put(base0.version, snap)
    if (!mounted.current || rid !== req.current) return null
    if (!a2) throw new Error('Application save returned no draft.')
    const advanced = { draft: snap, version: a2.version, fp: snapFp }
    latestBase.current = advanced
    setBase(advanced)
    persist(scope, { ...advanced, draft: latest.current })
    qc.setQueryData(['application', app.id], a2)
    qc.invalidateQueries({ queryKey: ['applications'] })
    if (fp(latest.current) !== snapFp || latest.current.input !== snap.input) {
      // user kept typing during the save — keep their edits, do not
      // chain into test/publish with stale code.
      setWarn('Changed while saving; try again.')
      return null
    }
    return { app2: a2, draftFp: snapFp }
  }

  // attemptSave records the outcome: a non-conflict failure leaves the
  // "Couldn't save" indicator up until a save lands; a conflict keeps the
  // Reload Confirm flow instead. A null result (edit mid-flight) is
  // neither success nor failure.
  const attemptSave = async (): Promise<{ app2: Application; draftFp: string } | null> => {
    try {
      const r = await save()
      if (r) setFailedFp(null)
      return r
    } catch (x: any) {
      if (x?.code !== 'conflict') setFailedFp(fp(latest.current))
      throw x
    }
  }

  const guarded = async (kind: string, fn: () => Promise<void>) => {
    if (corrupt || busy !== '') return
    setWarn(''); setBusy(kind)
    try { await fn() }
    catch (x: any) {
      if (!mounted.current) return
      if (x.code === 'conflict') {
        try {
          const fresh = (await api.get<{ application: Application }>(`/api/v1/applications/${app.id}`)).application
          setReloadOpen(fresh)
        } catch { setWarn('Saved elsewhere.') }
      } else setWarn(x.message)
    }
    finally { if (mounted.current) setBusy('') }
  }

  // fp covers everything the server persists; the test input is local.
  const fpDirty = fp(draft) !== base.fp
  const saveNow = () => guarded('save', async () => { await attemptSave() })
  const saveRef = useRef(saveNow)
  saveRef.current = saveNow
  // Autosave: fires only on shapes that would survive the PUT; anything
  // else stays 'Unsaved' silently — Run surfaces the real error.
  useEffect(() => {
    // A failed draft is retried only after a new edit or an explicit Retry.
    if (view !== 'edit' || !fpDirty || corrupt || busy !== '' || specErrs.length > 0 || !autosavable(draft) || fp(draft) === failedFp) return
    const t = window.setTimeout(() => saveRef.current(), 1200)
    return () => window.clearTimeout(t)
  }, [draft, fpDirty, corrupt, busy, view, failedFp, specErrs])
  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 's') {
        e.preventDefault()
        if (view === 'edit' && !corrupt && busy === '' && fp(latest.current) !== latestBase.current.fp) saveRef.current()
      }
    }
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  }, [view, corrupt, busy])

  const runStream = async (kind: 'dev' | 'call', path: string, body: string, settle: (r: RunView | null) => void) => {
    const seq = ++streamSeq.current
    streamCtl.current[kind]?.abort()
    const ctl = new AbortController()
    streamCtl.current[kind] = ctl
    const set = kind === 'dev' ? setDevStream : setCallStream
    const encoder = new TextEncoder()
    set(null)
    if (kind === 'dev') { setRun(null); setOutdated(false) }
    else setCallRun(null)
    const view: StreamView = { started: false, text: '', count: 0, bytes: 0, truncated: false }
    const push = () => { if (mounted.current && seq === streamSeq.current) set({ ...view }) }
    try {
      const r = await postApplication(path, body, {
        signal: ctl.signal,
        onStart: () => { view.started = true; push() },
        onItem: (raw: string) => {
          if (!mounted.current || seq !== streamSeq.current) return
          view.count++
          if (!view.truncated) {
            const n = encoder.encode(raw).length + 1
            if (view.count <= STREAM_ITEM_CAP && view.bytes + n <= STREAM_TEXT_CAP) {
              view.text += (view.text ? '\n' : '') + raw
              view.bytes += n
            } else view.truncated = true
          }
          push()
        },
      })
      if (!mounted.current || seq !== streamSeq.current) return
      if (!view.started) { set(null); settle(r as RunView); return }
      const res = (r as RunView).result
      if (res?.error) view.error = `${res.error.code} — ${res.error.message}`
      else view.done = r as RunView
      push()
      settle(r as RunView)
    } catch (x: any) {
      if (!mounted.current || seq !== streamSeq.current) return
      if (!view.started) { set(null); settle(null); throw x }
      if (ctl.signal.aborted) view.stopped = true
      else view.error = x?.message ?? 'stream failed'
      push()
    } finally {
      if (streamCtl.current[kind] === ctl) delete streamCtl.current[kind]
    }
  }

  // The debug entry list comes from the live draft; main always sorts
  // first. A vanished selection falls back to main.
  const [entry, setEntry] = useState('main')
  const entries = useMemo(() => {
    const seen = new Set<string>()
    for (const s of draft.sections ?? []) {
      for (const e of entryFunctions(s.code)) seen.add(e.name)
    }
    const rest = [...seen].filter((n) => n !== 'main')
    return seen.has('main') ? ['main', ...rest] : rest
  }, [draft.sections])
  const selEntry = entries.includes(entry) ? entry : 'main'
  const onRun = (name: string) => guarded('run', async () => {
    const s = await attemptSave()
    if (!s) return
    const snapInput = latest.current.input
    const entryPart = name !== 'main' ? `,"entry":${JSON.stringify(name)}` : ''
    await runStream('dev', `/api/v1/applications/${s.app2.id}/test`,
      `{"version":${s.app2.version},"input":${snapInput}${entryPart}}`, (r) => {
        if (!r) return
        if (fp(latest.current) !== s.draftFp || latest.current.input !== snapInput) {
          setOutdated(true) // edits made mid-flight: keep them, mark the result
          setRun(r)
          return
        }
        setOutdated(false)
        setRun(r)
      })
  })
  const onRunEntry = (name: string) => { setEntry(name); onRun(name) }

  const [reviewOpen, setReviewOpen] = useState(false)
  const [reviewVer, setReviewVer] = useState(0)
  const onPublish = () => guarded('publish', async () => {
    const s = await attemptSave()
    if (!s) return
    setReviewVer(s.app2.version)
    setReviewOpen(true)
  })

  const onInvoke = () => guarded('invoke', async () => {
    const snapInput = callInput
    await runStream('call', `/api/v1/applications/${app.id}/invoke`, snapInput, (r) => {
      if (r) setCallRun(r)
    })
  })

  const doDelete = async () => {
    // baseVersion is ours; a poll can never move it
    await api.del(`/api/v1/applications/${app.id}?version=${latestBase.current.version}`)
    qc.invalidateQueries({ queryKey: ['applications'] })
    nav('/applications')
  }

  const resetCorrupt = () => {
    try { sessionStorage.removeItem(scope) } catch { /* ignored */ }
    mirror.delete(scope)
    const fresh: Saved = { draft: draftOf(app), version: app.version, fp: fp(draftOf(app)) }
    setDraft(fresh.draft); setBase(fresh); setCorrupt(false); setWarn('')
    specSeen.current = false
    setSpecErrs([])
    setInitialSpec(parseSpec(fresh.draft.spec))
    setSpecKey((k) => k + 1)
  }

  // Follow each unfinished run to its real outcome: the handler's
  // return value is the application's output (S4 — the Output pre
  // keeps its box while the phase label changes). Stream state is
  // explicit: a streaming exec is tracked by its stream view, not by
  // run polling.
  const devStreaming = !!run?.result.streaming
  const callStreaming = !!callRun?.result.streaming
  const devProg = useRunProgress(run?.run_id && !run.output_json && !run.result.error && !devStreaming ? run.run_id : undefined)
  const callProg = useRunProgress(!callStreaming ? callRun?.run_id : undefined)
  const devPhase = devProg.phase
  const callPhase = callProg.phase
  const streamText = (sv: StreamView | null): string | null => {
    if (!sv || !sv.started) return null
    return sv.text
  }
  const streamMeta = (sv: StreamView | null): string => {
    if (!sv || !sv.started) return ''
    const items = `${sv.count} item${sv.count === 1 ? '' : 's'}`
    const trunc = sv.truncated ? ' · display truncated' : ''
    if (sv.stopped) return `${items}${trunc} · Stopped`
    if (sv.error) return `${items}${trunc} · Failed`
    if (sv.done) return `${items}${trunc} · Completed · not retained`
    return `Streaming… ${items}${trunc}`
  }
  const streamError = (sv: StreamView | null): string | null => {
    if (!sv || !sv.started) return null
    if (sv.stopped) return 'Stopped — partial output above; outcome may be unknown.'
    if (sv.error) return `error: ${sv.error}`
    return null
  }
  const preText = (r: RunView | null, p: RunPhase | undefined, placeholder: string): string => {
    if (!r) return placeholder
    if (r.output_json) return r.output_json
    if (r.result.error) return `error: ${r.result.error.code} — ${r.result.error.message}`
    if (!p) return r.waiting ? 'Waiting to be resumed…' : ''
    if (p.phase === 'succeeded') return p.output ?? ''
    if (p.phase === 'waiting') return `Waiting to resume ${p.interrupt.handler ?? 'handler'}…`
    if (p.phase === 'running') return `Running ${p.handler ?? 'handler'}…`
    return `error: ${p.phase}${p.errorCode ? ` — ${p.errorCode}` : ''}`
  }
  // Output body for the Test and Invoke response panes: the live-phase
  // strip sits directly under the pane head, the pre is bounded to
  // ~40% while a pending resume form fills the rest.
  const outputBody = (r: RunView | null, phase: RunPhase | undefined, stream: StreamView | null, placeholder: string) => {
    const live = !!r?.run_id && !r.output_json && !r.result.error && !r.result.streaming && !stream
    const p = live ? phase : undefined
    const waiting = p?.phase === 'waiting' ? p.interrupt : null
    const form = waiting?.can_resume ? waiting : null
    return (
      <>
        {p && p.phase !== 'succeeded' && r?.run_id && (
          <WaitHead phase={p} runId={r.run_id} appId={app.id} />
        )}
        <pre className={form ? 'app-out-pre wb-code app-out-fit' : 'app-out-pre wb-code wb-grow'} aria-live="polite">
          {streamText(stream) ?? preText(r, p, placeholder)}
        </pre>
        {streamError(stream) && <p className="app-dim wb-pane-note wb-note-pad">{streamError(stream)}</p>}
        {form && <ResumeForm key={form.id} interrupt={form} scope={{ userId: userID, workspaceId: app.workspace_id }} />}
      </>
    )
  }
  // Invoke response head/status reflect the run phase (waiting /
  // running / succeeded / failed), not app.status or step-0 success.
  // Metrics come from the persisted run: its error_code for failures,
  // its step count, and the summed duration of completed steps — the
  // invoke result's own duration/steps are engine-internal.
  const callStatus = !callRun ? ''
    : callRun.result.error ? 'failed'
    : callPhase?.phase
      ?? (callRun.waiting ? 'waiting'
        : callRun.output_json || callStream?.done ? 'succeeded' : 'running')
  const callPhaseFailed = !!callPhase && callPhase.phase !== 'succeeded' && callPhase.phase !== 'waiting' && callPhase.phase !== 'running'
  const callFailed = !!callRun?.result.error || callPhaseFailed
  const callErrCode = callRun?.result.error?.code
    ?? (callPhaseFailed ? callPhase.errorCode : undefined)
    ?? callProg.run?.error_code
  const callStepsList = callProg.steps ?? []
  const callStepMs = callStepsList.length
    ? callStepsList.filter((s) => s.status === 'succeeded').reduce((a, s) => a + s.duration_ms, 0)
    : undefined
  const callMs = callStepMs ?? callRun?.result.duration_ms
  const callStepsN = callProg.run?.steps
  const callMetric = callFailed ? (callErrCode ?? 'failed')
    : callRun && callMs != null
      ? `${callMs}ms${callStepsN ? ` · ${callStepsN} step${callStepsN === 1 ? '' : 's'}` : ''}`
      : ''
  const ready = app.status === 'ready'
  const canRun = busy === '' && view === 'edit' && !corrupt && specErrs.length === 0
  const saveState = busy === 'save' ? 'Saving…' : saveFailed ? "Couldn't save" : fpDirty ? 'Unsaved' : 'Saved'
  // Shared between the Develop workbench and the notebook Test panel —
  // Input | Output panes under compact heads, both surfaces flush mono.
  // Test panel: one head row (TEST · Entry · note · Run) lives in the
  // notebook utility head via testControls; the body is the Input |
  // Output split, each pane with its own small head.
  const testControls = (
    <div className="app-io-toolbar app-test-controls">
      <label className="wb-pane-note app-entry-label" htmlFor="app-entry">Entry</label>
      <select id="app-entry" className={inputCls + ' app-entry-select'} value={selEntry} disabled={!canRun}
        onChange={(e) => setEntry(e.target.value)}>
        {entries.map((n) => <option key={n} value={n}>{n}</option>)}
      </select>
      <span className="wb-pane-note app-entry-note">{selEntry === 'main' ? 'JSON object · main(input)' : `any JSON · ${selEntry}(input) · debug run, not recorded`}</span>
      <span className="wb-pane-actions">
        <Button variant="primary" size="sm" onClick={() => onRun(selEntry)} disabled={!canRun}
          aria-busy={busy === 'run'} data-testid="app-run">
          <SwapLabel show={busy === 'run' ? 'Running…' : 'Run'} options={['Run', 'Running…']} />
        </Button>
      </span>
    </div>
  )
  const testPanel = (
    <div className="app-draft-test">
      <div className="app-draft-input wb-pane">
        <div className="wb-pane-head"><span className="wb-pane-title">Input</span></div>
        <div className="wb-pane-body">
          <textarea id="app-input" className="app-area wb-code wb-grow" spellCheck={false}
            value={draft.input} onChange={(e) => edit({ input: e.target.value })} />
        </div>
      </div>
      <div className="app-out wb-pane">
        <div className="app-out-head wb-pane-head">
          <span className="wb-pane-title">Output</span>
          <span className="app-stream-stop" data-testid="app-stream-stop-slot">
            <Button size="sm" variant="danger" onClick={() => setStopKind('dev')} data-testid="app-stream-stop"
              disabled={!devStream || !!devStream.done || !!devStream.error || !!devStream.stopped}
              tabIndex={devStream && !devStream.done && !devStream.error && !devStream.stopped ? 0 : -1}
              style={{ visibility: devStream && !devStream.done && !devStream.error && !devStream.stopped ? 'visible' : 'hidden' }}
              aria-hidden={!(devStream && !devStream.done && !devStream.error && !devStream.stopped)}><SwapLabel show="Stop" options={['Stop', 'Stopping…']} /></Button>
          </span>
          <span className="app-dim app-stream-meta wb-pane-note" data-testid="app-stream-meta">{streamMeta(devStream)}</span>
        </div>
        <div className="wb-pane-body">
          {outputBody(run, devPhase, devStream, 'Run to see output')}
          {outdated && <p className="app-dim wb-pane-note wb-note-pad">Result is from before your latest edits.</p>}
        </div>
      </div>
    </div>
  )
  const schemaFields = (['in', 'out'] as const).map((side) => (
    <div key={side} className="app-schema">
      <label className="app-check">
        <input type="checkbox" checked={side === 'in' ? draft.inOn : draft.outOn} disabled={view === 'view'}
          onChange={(e) => {
            const on = e.target.checked
            // an enabled-but-empty schema must not silently fall
            // back to the default: seed the open-object contract
            const seed = '{"type":"object","additionalProperties":true}'
            edit(side === 'in' ? { inOn: on, inText: on && !draft.inText.trim() ? seed : draft.inText }
                              : { outOn: on, outText: on && !draft.outText.trim() ? seed : draft.outText })
          }} />
        Custom {side === 'in' ? 'input' : 'output'} schema
      </label>
      {(side === 'in' ? draft.inOn : draft.outOn) && (
        <textarea className="app-area" rows={6} spellCheck={false} disabled={view === 'view'}
          aria-label={`${side} schema JSON`}
          value={side === 'in' ? draft.inText : draft.outText}
          onChange={(e) => edit(side === 'in' ? { inText: e.target.value } : { outText: e.target.value })} />
      )}
    </div>
  ))
  const resultNotices = (
    <>
      {specErrs.length > 0 && (
        <Notice tone="warning">
          {specErrs[0].section}: {specErrs[0].message}
          {specErrs.some((e) => e.section === 'Extensions') && (
            <Button size="sm" onClick={() => notebookSpecRef.current?.revealInvalidExtension()}>Configure extension</Button>
          )}
        </Notice>
      )}
      {reviewOpen && (
        <PublishReviewDialog resource="applications" id={app.id} version={reviewVer}
          onClose={() => setReviewOpen(false)}
          onPublished={() => {
            setReviewOpen(false)
            setPublished(true)
            qc.invalidateQueries({ queryKey: ['application', app.id] })
            qc.invalidateQueries({ queryKey: ['applications'] })
          }} />
      )}
      {published && (
        <Notice><SwapLabel
          show={app.status === 'preparing' ? 'Published — deploying. Invoke becomes available when status is ready.' : 'Published.'}
          options={['Published — deploying. Invoke becomes available when status is ready.', 'Published.']} /></Notice>
      )}
    </>
  )
  return (
    <div className="app-page app-editor detail-workbench">
      <DetailWorkbenchHeader backTo="/applications" backLabel="Applications"
        title={draft.name || 'Application'} id={app.id}
        description={applicationDescription(app) || undefined}
        status={<>
          <StatusCell status={app.status} />
          {app.has_unpublished_changes && <span className="app-dim">unpublished changes</span>}{' '}
          <span className="app-dim"><SwapLabel show={saveState} options={['Saved', 'Saving…', 'Unsaved', "Couldn't save"]} /></span>{' '}
          {saveFailed && view === 'edit' && <Button size="xs" onClick={saveNow} disabled={busy !== ''}>Retry</Button>}
        </>}
        actions={view === 'edit' ? <Button variant="danger" size="sm" onClick={() => setDelOpen(true)}>Delete</Button> : undefined} />
      {/* P5/F3: invocation and background-save failures stay visible in every view. */}
      {(warn || storeFailed || corrupt) && (
        <div className="detail-error">
          {warn && <Notice tone="warning">{warn}</Notice>}
          {storeFailed && <Notice tone="warning">Not saved to this tab.</Notice>}
          {corrupt && (
            <Notice tone="warning">
              Saved editor state for this application is unreadable.
              <button className="link-btn" onClick={() => setResetOpen(true)}>Reset it</button>
            </Notice>
          )}
        </div>
      )}

      <Tabs.Root className="app-detail-views" value={activeView} onValueChange={goToView}>
      <DetailWorkbenchViews>
        <Tabs.List ref={sectionTabs} className="view-tabs app-section-tabs" aria-label="Application views">
          {appSections.map(({ value, label, key, icon }) => (
            <Tabs.Trigger key={value} className="view-tab" value={value} title={`${label} (Alt+${key})`}
              aria-keyshortcuts={`Alt+${key}`}
              onClick={value === 'develop' ? () => { if (activeView === 'develop') goToView(value) } : undefined}>
              <svg aria-hidden="true" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round">
                <path d={icon} />
              </svg>
              {label}
            </Tabs.Trigger>
          ))}
        </Tabs.List>
      </DetailWorkbenchViews>
      {/* C2/C3: switching views hides panels, never remounts their editors. */}
      <Tabs.Content value="develop" forceMount hidden={activeView !== 'develop'} className="work-panel app-view-panel">
      <>
        <ApplicationNotebook
          visible={activeView === 'develop'}
          sections={draft.sections ?? []}
          saved={app}
          extensions={editor.extensions}
          readOnly={view === 'view' || corrupt}
          busy={busy}
          canRun={canRun}
          onRunEntry={onRunEntry}
          onPublish={onPublish}
          onChange={(update) => edit((cur) => ({ sections: update(cur.sections ?? []) }))}
          saveStatus={<SwapLabel show={saveState} options={['Saved', 'Saving…', 'Unsaved', "Couldn't save"]} />}
          test={testPanel}
          testControls={testControls}
          settingsOpen={settingsOpen}
          setSettingsOpen={setSettingsOpen}
          settings={(catalogTargets, openSettings, closeSettings) => (
            <SpecForm key={specKey} ref={notebookSpecRef} application workspaceId={app.workspace_id} initial={initialSpec} onSpecChange={onSpec}
              dockExtensionCatalog extensionCatalogTarget={catalogTargets.extensions}
              moduleCatalogTarget={catalogTargets.modules} policyCatalogTarget={catalogTargets.policies}
              onConfigureCatalogExtension={openSettings}
              catalogReadOnly={view === 'view' || corrupt}
              notebookSettings={{
                general: <Field label="Name"><input id="app-name" className={inputCls} value={draft.name} onChange={(e) => edit({ name: e.target.value })} /></Field>,
                contract: { input: schemaFields[0], output: schemaFields[1] },
                readOnly: view === 'view' || corrupt,
                onClose: closeSettings,
              }} />
          )}
        />
        <div className="app-result appnb-result" role="status" aria-live="polite"
          hidden={specErrs.length === 0 && !published && !reviewOpen}>
          {resultNotices}
        </div>
      </>
      </Tabs.Content>

      <Tabs.Content value="invoke" forceMount hidden={activeView !== 'invoke'} className="work-panel app-view-panel app-invoke-panel">
      <WorkView>
        {/* Postman-style request bar: method · URL · release · Send */}
        <div className="wb-toolbar app-invoke-toolbar">
          <span className="wb-method">POST</span>
          <code className="wb-path" title={`${window.location.origin}/api/v1/applications/${app.id}/invoke`}>/api/v1/applications/{app.id}/invoke</code>
          <CopyButton text={`${window.location.origin}/api/v1/applications/${app.id}/invoke`} size="sm" />
          <div className="app-invoke-release">
            {app.published_version > 0 && <span className="wb-pane-note">release v{app.published_version}</span>}
            {ready
              ? <span className="wb-toolbar-note">Published release · draft changes not used</span>
              : <span className="wb-toolbar-note wb-toolbar-note-warn">{statusHint[app.status] ?? 'Not ready.'}</span>}
          </div>
          <div className="wb-toolbar-side">
            <Button variant="primary" size="sm" onClick={onInvoke} disabled={busy !== '' || !ready || view === 'view' || corrupt}><SwapLabel show={busy === 'invoke' ? 'Sending…' : 'Send'} options={['Send', 'Sending…']} /></Button>
          </div>
        </div>
        {/* request | response split — the vertical hairline separates
            the two panes; at ≤900px they stack inside one scroller */}
        <div className="wb-pane-split wb-fill app-invoke-io">
          <section className="wb-pane app-invoke-request">
            <InvokeTabs appId={app.id} input={callInput} canEdit={view === 'edit'}
              onCreateKey={() => setKeyOpen(true)}
              body={<textarea id="app-call-input" aria-label="Request body" className="app-area wb-code wb-grow" spellCheck={false}
                value={callInput} onChange={(e) => setCallInput(e.target.value)} />} />
          </section>
          <section className="wb-pane app-invoke-output">
            <header className="wb-pane-head">
              <span className="wb-pane-title">Response</span>
              {callRun && <StatusCell status={callStatus} suffix={callMetric ? ` · ${callMetric}` : ''} />}
              <span className="app-dim app-stream-meta wb-pane-note" data-testid="app-call-stream-meta">{streamMeta(callStream)}</span>
              <span className="app-stream-stop wb-pane-actions" data-testid="app-call-stream-stop-slot">
                <Button size="sm" variant="danger" onClick={() => setStopKind('call')} data-testid="app-call-stream-stop"
                  disabled={!callStream || !!callStream.done || !!callStream.error || !!callStream.stopped}
                  tabIndex={callStream && !callStream.done && !callStream.error && !callStream.stopped ? 0 : -1}
                  style={{ visibility: callStream && !callStream.done && !callStream.error && !callStream.stopped ? 'visible' : 'hidden' }}
                  aria-hidden={!(callStream && !callStream.done && !callStream.error && !callStream.stopped)}><SwapLabel show="Stop" options={['Stop', 'Stopping…']} /></Button>
              </span>
            </header>
            <div className="wb-pane-body">
              {outputBody(callRun, callPhase, callStream, 'Send to see output')}
            </div>
          </section>
        </div>
        <WorkStatusbar>
          <StatusCell status={app.status} />
          {app.published_version > 0 && <span className="wb-status-num">release v{app.published_version}</span>}
          <span className="wb-grow" />
          <span className="wb-status-num">
            {callRun ? `${callStatus}${callMetric ? ` · ${callMetric}` : ''}` : ''}
          </span>
        </WorkStatusbar>
      </WorkView>
      </Tabs.Content>
      <Tabs.Content value="activity" forceMount hidden={activeView !== 'activity'} className="work-panel app-view-panel">
        <WorkView>
          <RunsTable appId={app.id} />
        </WorkView>
      </Tabs.Content>
      </Tabs.Root>


      {stopKind && (
        <Confirm tone="danger" title="Stop streaming" confirm="Stop" pending="Stopping…" onClose={() => setStopKind('')}
          action={async () => { streamCtl.current[stopKind]?.abort() }}>
          The run's outcome may be unknown.
        </Confirm>
      )}
      {delOpen && (
        <Confirm tone="danger" title="Delete application" confirm="Delete" pending="Deleting…" onClose={() => setDelOpen(false)}
          action={doDelete}>
          Delete “{draft.name}” and disable its deployment?
        </Confirm>
      )}
      {resetOpen && (
        <Confirm title="Reset unreadable draft" confirm="Reset" pending="Resetting…"
          onClose={() => setResetOpen(false)} action={async () => { resetCorrupt(); setResetOpen(false) }}>
          Discard the unreadable local editor state and load the saved server draft?
        </Confirm>
      )}
      {reloadOpen && (
        <Confirm title="Saved elsewhere" confirm="Reload" pending="Loading…" onClose={() => setReloadOpen(null)}
          action={async () => {
            const a2 = reloadOpen
            const fresh: Saved = { draft: draftOf(a2), version: a2.version, fp: fp(draftOf(a2)) }
            setDraft(fresh.draft); setBase(fresh); persist(scope, fresh); setReloadOpen(null); setWarn('')
          }}>
          Reload the version saved by another session?
        </Confirm>
      )}
      {keyOpen && <AppKeyModal appId={app.id} appName={app.name} workspaceId={app.workspace_id} onClose={() => setKeyOpen(false)} />}
      {busy === 'delete' && <Loading />}
    </div>
  )
}


// InvokeTabs: the request pane's Body | Code | Auth underline tabs —
// the Postman layout's left half. Code hosts the call examples, Auth
// the key-creation entry moved out of the request bar.
function InvokeTabs({ appId, input, canEdit, onCreateKey, body }: {
  appId: string
  input: string
  canEdit: boolean
  onCreateKey: () => void
  body: React.ReactNode
}) {
  const [tab, setTab] = useState<'body' | 'code' | 'auth'>('body')
  const [pointerFocus, setPointerFocus] = useState(false)
  return (
    <Tabs.Root className="app-invoke-tabs" value={tab} onValueChange={(v) => setTab(v as 'body' | 'code' | 'auth')}>
      <header className="wb-pane-head">
        <h3 className="wb-pane-title">Request</h3>
        <Tabs.List className="wb-head-tabs" aria-label="Request" data-pointer-focus={pointerFocus || undefined}
          onPointerDown={() => setPointerFocus(true)}
          onKeyDownCapture={() => setPointerFocus(false)}
          onBlur={(e) => { if (!e.currentTarget.contains(e.relatedTarget)) setPointerFocus(false) }}>
          <Tabs.Trigger value="body" className="wb-head-tab">Body</Tabs.Trigger>
          <Tabs.Trigger value="code" className="wb-head-tab">Code</Tabs.Trigger>
          <Tabs.Trigger value="auth" className="wb-head-tab">Auth</Tabs.Trigger>
        </Tabs.List>
      </header>
      <div className="wb-pane-body">
        <Tabs.Content value="body">{body}</Tabs.Content>
        <Tabs.Content value="code">
          <div className="app-invoke-code">
            <ApplicationCallExamples appId={appId} input={input} className="app-examples-embedded" />
          </div>
        </Tabs.Content>
        <Tabs.Content value="auth">
          <div className="app-invoke-auth">
            <p>Calls authenticate with a Bearer API key in the <code>Authorization</code> header.</p>
            <p>Keys are scoped to this application. POST the JSON body to <code className="app-mono">/api/v1/applications/{appId}/invoke</code>.</p>
            {canEdit && <Button size="sm" className="mt-2" onClick={onCreateKey}>Create API key</Button>}
          </div>
        </Tabs.Content>
      </div>
    </Tabs.Root>
  )
}

// RunsTable fills the Activity view in the workbench vocabulary: a
// toolbar with count + held-order reveal, a full-bleed table whose
// rows are clickable, and a status bar. Held order (S5/C3): rows
// update in place and reordering waits for the reveal affordance.
function RunsTable({ appId }: { appId: string }) {
  const nav = useNavigate()
  const q = useQuery({
    queryKey: ['runs', appId],
    queryFn: () => api.get<{ runs?: Run[] }>(`/api/v1/applications/${encodeURIComponent(appId)}/runs`),
    refetchInterval: 5000,
  })
  const held = useHeldOrder(q.data?.runs?.map((r) => ({ r })) ?? undefined, ({ r }) => ({ key: r.id, slot: 'runs' }))
  return (
    <>
      <div className="wb-toolbar">
        <h3 className="wb-pane-title">Runs</h3>
        <span className="wb-status-num text-sec">{held.rows.length}</span>
        <span className="wb-toolbar-note">auto-refreshes every 5s</span>
        <div className="wb-toolbar-side">
          <RevealNew count={held.arrived} moved={held.moved} onClick={held.reveal} />
        </div>
      </div>
      <section className="wb-pane wb-fill">
        <div className="wb-pane-body" role="region" aria-label="Runs table" tabIndex={0}>
          {q.isLoading && <div className="wb-pane-notice"><Loading /></div>}
          {q.isError && <div className="wb-pane-notice"><Notice tone="warning">Could not load runs.</Notice></div>}
          {!q.isLoading && !q.isError && held.rows.length === 0 && held.arrived === 0 && (
            <p className="wb-pane-empty">No runs yet.</p>
          )}
          {held.rows.length > 0 && (
            <table className="tbl wb-table">
              <thead><tr><th>Status</th><th>Trigger</th><th className="wb-num">Steps</th><th>Started</th><th>Waiting on</th></tr></thead>
              <tbody>
                {held.rows.map(({ item: { r } }) => (
                  <tr key={r.id} className="wb-row-click" onClick={() => nav(`/applications/${appId}/runs/${r.id}`)}>
                    <td><Link className="link" to={`/applications/${appId}/runs/${r.id}`}><StatusCell status={r.status} /></Link></td>
                    <td className="text-xs text-sec">{r.trigger}</td>
                    <td className="text-xs text-sec wb-num">{r.steps}</td>
                    <td className="text-xs text-sec">{r.created_at ? fmtDateTime(r.created_at) : ''}</td>
                    <td className="text-xs text-sec wb-mono">{r.interrupt_id ?? '—'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </section>
      <WorkStatusbar>
        <span className="wb-grow wb-status-num">{held.rows.length} run{held.rows.length === 1 ? '' : 's'}</span>
      </WorkStatusbar>
    </>
  )
}
