import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api, Application, workspaces, currentWorkspace } from '../api'
import { CreateGuide } from '../components/CreateGuide'
import { SpecForm, type SpecFormHandle } from '../components/SpecForm'
import { KeyValueRows } from '../components/SpecSections'
import { sameRows, type InstanceDraft, type DraftError, type InstanceSpec } from '../instanceDraft'
import { Button, Field, inputCls, Loading, Notice, PageHeader, SwapLabel } from '../components/ui'
import './create-flow.css'

type Step = 'basics' | 'config'

// NewApplication mirrors NewInstance: a guide + basics first, then the
// detailed configuration. The configuration stays mounted once opened,
// so Back preserves capability choices (C2); the step is addressable as
// ?step=config (C4). The server seeds the main(input) scaffold; code is
// edited on the application's page.
export default function NewApplication() {
  const nav = useNavigate()
  const qc = useQueryClient()
  const [params, setParams] = useSearchParams()
  const step: Step = params.get('step') === 'config' ? 'config' : 'basics'
  const [configMounted, setConfigMounted] = useState(step === 'config')
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [labels, setLabels] = useState<{ k: string; v: string }[]>([])
  // A basics edit made while the hidden configuration is mounted must
  // reach it even when the browser (not Continue) advances the step.
  const basicsPending = useRef(false)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [specErrs, setSpecErrs] = useState<{ section: string; message: string }[]>([])
  const form = useRef<SpecFormHandle>(null)

  const goStep = (next: Step) => {
    const p = new URLSearchParams(params)
    if (next === 'config') p.set('step', 'config')
    else p.delete('step')
    setParams(p)
  }
  const goConfig = () => {
    if (configMounted) form.current?.applyBasics({ description, labels })
    basicsPending.current = false
    setConfigMounted(true)
    goStep('config')
  }
  const goBasics = () => goStep('basics')

  useEffect(() => {
    if (step !== 'config' || !configMounted || !basicsPending.current) return
    form.current?.applyBasics({ description, labels })
    basicsPending.current = false
  }, [step, configMounted, description, labels])

  const onSpec = useCallback((d: InstanceDraft, errs?: DraftError[], built?: { spec?: InstanceSpec; specJSON?: string }) => {
    setSpecErrs((cur) => (JSON.stringify(cur) === JSON.stringify(errs ?? []) ? cur : (errs ?? [])))
    // Advanced JSON is another editor for the same basics; keep the
    // basics step honest when it adds or removes labels. A pending
    // basics edit wins until applyBasics pushes it.
    if (basicsPending.current) return
    if (built?.spec) {
      const specLabels = built.spec.labels ?? {}
      const nextDescription = typeof specLabels.description === 'string' ? specLabels.description : ''
      setDescription((cur) => (cur === nextDescription ? cur : nextDescription))
    }
    setLabels((cur) => (sameRows(cur, d.labels) ? cur : d.labels))
  }, [])
  const wsq = useQuery({ queryKey: ['workspaces'], queryFn: workspaces })
  const dest = wsq.data?.find((w) => w.id === currentWorkspace()) ?? wsq.data?.find((w) => w.personal)
  const canCreate = dest != null && (dest.role === 'owner' || dest.role === 'editor')

  // Read once when the configuration first mounts; later basics edits
  // go through applyBasics, so capability choices are never remounted.
  const initial = useMemo<InstanceSpec | undefined>(() => {
    const merged: Record<string, string> = Object.fromEntries(labels.map((r) => [r.k.trim(), r.v]).filter(([k]) => k))
    if (description.trim()) merged.description = description.trim()
    return Object.keys(merged).length ? { labels: merged } : undefined
  }, [description, labels])

  if (wsq.isLoading) return <Loading />
  if (!dest || !canCreate) return <Notice tone="warning">You need editor access to create applications in the current workspace.</Notice>

  const create = async () => {
    setErr('')
    const built = form.current?.buildSpec()
    if (!built) { setErr('Configuration is not ready yet. Try again.'); return }
    const { spec, specJSON, err: se } = built
    if (se) { setErr(se); return }
    setBusy(true)
    try {
      const r = await api.postRawJson<{ application: Application }>(`/api/v1/applications?workspace=${encodeURIComponent(dest.id)}`,
        `{"name":${JSON.stringify(name)},"spec":${specJSON ?? JSON.stringify(spec ?? {})}}`)
      qc.invalidateQueries({ queryKey: ['applications'] })
      nav(`/applications/${r.application.id}`)
    } catch (x: any) { setErr(x.message); setBusy(false) }
  }

  return (
    <div className="instance-builder">
      <PageHeader title="New application">
        {step === 'config' && (name.trim() || description.trim()) && (
          <span>{name.trim() || 'Untitled application'}{description.trim() ? ` — ${description.trim()}` : ''}</span>
        )}
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
          <section className="create-basics" aria-labelledby="create-basics-title">
            <div className="create-basics-head">
              <h2 id="create-basics-title">Basics</h2>
            </div>
            <div className="create-basics-body">
              <div className="create-basics-grid">
                <Field label="Name">
                  <input id="basics-name" className={inputCls} value={name} maxLength={128}
                    placeholder="e.g. Greeter" onChange={(e) => setName(e.target.value)} />
                </Field>
                <Field label="Description">
                  <input id="basics-description" className={inputCls} value={description} maxLength={280}
                    placeholder="What does it do?" onChange={(e) => { basicsPending.current = true; setDescription(e.target.value) }} />
                </Field>
              </div>
              <div>
                <KeyValueRows title="Labels" addText="Add label" idPrefix="basics-label" rows={labels}
                  onChange={(rows) => { basicsPending.current = true; setLabels(rows) }} />
              </div>
            </div>
            <div className="create-basics-actions">
              <Button variant="primary" onClick={goConfig}>Continue to configuration</Button>
            </div>
          </section>
          <CreateGuide kind="application" />
        </div>
      )}

      {configMounted && (
        <div className="builder-work-panel" hidden={step !== 'config'}>
          <SpecForm ref={form} application hideBasics workspaceId={dest.id} initial={initial} onSpecChange={onSpec}
            footer={
              <div className="create-bar">
                {specErrs.length > 0 && <ul className="create-bar-issues" aria-live="polite">{specErrs.map((e, i) => <li key={i}>{e.section}: {e.message}</li>)}</ul>}
                {err && <p className="create-bar-issues" role="alert">{err}</p>}
                <Button variant="primary" className="w-full" disabled={busy || specErrs.length > 0} onClick={create}>
                  <SwapLabel show={busy ? 'Creating…' : 'Create application'} options={['Create application', 'Creating…']} />
                </Button>
              </div>
            } />
        </div>
      )}
    </div>
  )
}

