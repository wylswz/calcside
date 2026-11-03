import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api, wsPath, Instance, workspaces, currentWorkspace } from '../api'
import { Button, Field, inputCls, Loading, Notice, PageHeader } from '../components/ui'
import { CreateGuide } from '../components/CreateGuide'

import { SpecForm, type SpecFormHandle } from '../components/SpecForm'
import { KeyValueRows } from '../components/SpecSections'
import { sameRows, type InstanceDraft, type InstanceSpec } from '../instanceDraft'
import './create-flow.css'

type Step = 'basics' | 'config'

// Creating an instance starts with its basic identity: the basics form
// sits on the left and the instance guide on the right. There is no
// kind switch — the flow stays an instance. Configuration is a second
// step, but it stays mounted once opened so going Back never discards
// capabilities (C2). The step is addressable (?step=config) for deep
// links and the browser's Back button (C4).
export default function NewInstance() {
  const nav = useNavigate()
  const qc = useQueryClient()
  const [params, setParams] = useSearchParams()
  const from = params.get('from')
  const step: Step = params.get('step') === 'config' ? 'config' : 'basics'
  const [configMounted, setConfigMounted] = useState(step === 'config')
  const form = useRef<SpecFormHandle>(null)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [errors, setErrors] = useState<{ section: string; message: string }[]>([])
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [labels, setLabels] = useState<{ k: string; v: string }[]>([])
  // A basics edit made while the hidden configuration is mounted must
  // reach it even when the browser (not Continue) advances the step.
  const basicsPending = useRef(false)
  // The source spec's own labels keep flowing through `initial` until
  // the user edits the label rows themselves.
  const labelsTouched = useRef(false)

  const goStep = (next: Step) => {
    const p = new URLSearchParams(params)
    if (next === 'config') p.set('step', 'config')
    else p.delete('step')
    setParams(p)
  }
  const goConfig = () => {
    // The second entry pushes basics into the still-mounted draft; the
    // first entry seeds it through initial and needs no imperatives.
    if (configMounted) form.current?.applyBasics({ name, description, labels })
    basicsPending.current = false
    setConfigMounted(true)
    goStep('config')
  }
  const goBasics = () => goStep('basics')

  useEffect(() => {
    if (step !== 'config' || !configMounted || !basicsPending.current) return
    form.current?.applyBasics({ name, description, labels })
    basicsPending.current = false
  }, [step, configMounted, name, description, labels])

  // Create similar: fetch the source spec; an auth/404 error must not
  // silently open an empty draft.
  const src = useQuery({
    queryKey: ['instance', from],
    enabled: !!from,
    queryFn: () => api.get<{ instance: Instance }>(`/api/v1/instances/${from}`).then((r) => r.instance),
    retry: false,
  })

  // Seed the basic fields from the source once; a field the user already
  // touched is never overwritten.
  const seeded = useRef<string | null>(null)
  useEffect(() => {
    if (!from || !src.data || seeded.current === from) return
    seeded.current = from
    const srcLabels = src.data.spec?.labels ?? {}
    setName((cur) => cur || String(srcLabels.name ?? ''))
    setDescription((cur) => cur || String(srcLabels.description ?? ''))
    setLabels((cur) => cur.length
      ? cur
      : Object.entries(srcLabels).filter(([k]) => k !== 'name' && k !== 'description').map(([k, v]) => ({ k, v: String(v) })))
  }, [from, src.data])

  const create = async () => {
    setErr('')
    const { spec, err: e } = form.current?.buildSpec() ?? {}
    if (e) { setErr(e); return }
    setBusy(true)
    try {
      const r = await api.post<{ instance: Instance }>(wsPath('/api/v1/instances'), spec)
      qc.invalidateQueries({ queryKey: ['instances'] })
      nav(`/instances/${r.instance.id}`)
    } catch (x: any) { setErr(x.message) } finally { setBusy(false) }
  }

  const wsq = useQuery({ queryKey: ['workspaces'], queryFn: workspaces })
  const dest = wsq.data?.find((w) => w.id === (currentWorkspace() ?? wsq.data?.find((x) => x.personal)?.id))
  const canCreate = dest != null && (dest.role === 'owner' || dest.role === 'editor')
  const reviewCb = useCallback((d: InstanceDraft, errs?: { section: string; message: string }[]) => {
    setErrors(errs ?? [])
    // Basics stay editable on both steps; the detailed editor is one
    // source for them once it mounts. A pending basics edit wins until
    // applyBasics pushes it.
    if (basicsPending.current) return
    setName(d.name)
    setDescription(d.description)
    setLabels((cur) => (sameRows(cur, d.labels) ? cur : d.labels))
  }, [])

  // initial is read once by SpecForm; later basics edits go through
  // applyBasics so the form is never remounted and its choices survive.
  // Until the user edits the label rows, the source's labels pass
  // through verbatim — rows may not be seeded yet at mount time.
  const initial = useMemo<InstanceSpec | undefined>(() => {
    const base = from ? src.data?.spec : undefined
    if (from && !base) return undefined
    const merged: Record<string, string> = labelsTouched.current
      ? Object.fromEntries(labels.map((r) => [r.k.trim(), r.v]).filter(([k]) => k))
      : { ...(base?.labels ?? {}) }
    if (name.trim()) merged.name = name.trim()
    else delete merged.name
    if (description.trim()) merged.description = description.trim()
    else delete merged.description
    if (!base && Object.keys(merged).length === 0) return undefined
    return { ...(base ?? {}), labels: merged }
  }, [from, src.data, name, description, labels])

  if (wsq.isLoading || (from && src.isLoading)) return <Loading />
  if (wsq.isError) return <Notice tone="warning">Could not load workspaces — retry shortly.</Notice>
  if (!dest || !canCreate) return <Notice tone="warning">You need editor access to create instances in the current workspace.</Notice>
  if (from && src.isError) return <Notice tone="warning">Could not load the source instance's spec: {(src.error as Error).message}</Notice>

  return (
    <div className="instance-builder">
      <PageHeader title={from ? 'Create similar' : 'New instance'}>
        {step === 'config' && (name.trim() || description.trim()) && (
          <span>{name.trim() || 'Untitled instance'}{description.trim() ? ` — ${description.trim()}` : ''}</span>
        )}
        {from && step === 'config' && <span>Copy configuration only. Files and state stay in the original instance.</span>}
      </PageHeader>

      <nav className="create-steps" aria-label="Create steps">
        <button type="button" className="create-step" data-state={step === 'basics' ? 'current' : 'done'}
          aria-current={step === 'basics' ? 'step' : undefined} onClick={() => { if (step === 'config') goBasics() }}>
          <span className="create-step-num">1</span> Basics
        </button>
        <span className="create-step-line" aria-hidden="true" />
        <button type="button" className="create-step" data-state={step === 'config' ? 'current' : 'todo'}
          aria-current={step === 'config' ? 'step' : undefined} onClick={() => { if (step === 'basics') goConfig() }}>
          <span className="create-step-num">2</span> Configuration
        </button>
      </nav>

      {step === 'basics' && (
        <div className="create-basics-layout">
          <section className="create-basics" aria-label="Basics">
            <div className="create-basics-body">
              <div className="create-basics-grid">
                <Field label="Name">
                  <input id="basics-name" className={inputCls} value={name} maxLength={128}
                    placeholder="e.g. nightly research" onChange={(e) => { basicsPending.current = true; setName(e.target.value) }} />
                </Field>
                <Field label="Description">
                  <input id="basics-description" className={inputCls} value={description} maxLength={280}
                    placeholder="What is this for?" onChange={(e) => { basicsPending.current = true; setDescription(e.target.value) }} />
                </Field>
              </div>
              <KeyValueRows title="Labels" addText="Add label" idPrefix="basics-label" rows={labels}
                onChange={(rows) => { basicsPending.current = true; labelsTouched.current = true; setLabels(rows) }} />
            </div>
            <div className="create-basics-actions">
              <Button variant="primary" onClick={goConfig}>Continue to configuration</Button>
            </div>
          </section>
          <CreateGuide kind="instance" />
        </div>
      )}

      {configMounted && (
        <div className="builder-work-panel" hidden={step !== 'config'}>
          <SpecForm key={from ?? 'new'} ref={form} initial={initial} hideBasics onSpecChange={reviewCb} workspaceId={dest.id}
            footer={
              <div className="create-bar">
                {errors.length > 0 && <ul className="create-bar-issues" aria-live="polite">{errors.map((e, i) => <li key={i}>{e.section === 'Extensions' ? e.message : `${e.section}: ${e.message}`}</li>)}</ul>}
                {err && <p className="create-bar-issues" role="alert">{err}</p>}
                <Button variant="primary" className="w-full" disabled={busy || errors.length > 0} onClick={create}>
                  {from ? 'Create similar' : 'Create instance'}
                </Button>
              </div>
            } />
        </div>
      )}
    </div>
  )
}
