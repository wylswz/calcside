import { useEffect, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { api, wsPath, ApiError } from '../api'
import { Button, Loading, Notice, SwapLabel, Modal, inputCls } from './ui'
import { DigestChip, StatusIndicator } from '../ui/core'
import { FindingList, type Finding } from '../ui/FindingList'
import { GrantDiffCard } from '../ui/GrantDiffCard'

import type { components } from '../api/schema'
type PublishReview = components['schemas']['PublishReview']
type ReleaseCheck = components['schemas']['ApplicationReleaseCheckResponse']

// Shared review dialog for modules and applications:
// - Modules: EP-0018 digest-bound semantic + @codemirror/merge diff review.
// - Applications: EP-0026 production-readiness flow — Check findings,
//   semantic/code Diff, and digest-bound Publish.
export function PublishReviewDialog({
  resource,
  id,
  version,
  onJumpSection,
  onOpenSettings,
  onClose,
  onPublished,
}: {
  resource: 'modules' | 'applications'
  id: string
  version: number
  onJumpSection?: (section: string) => void
  onOpenSettings?: () => void
  onClose: () => void
  onPublished: () => void
}) {
  const qc = useQueryClient()
  const [rev, setRev] = useState<PublishReview | null>(null)
  const [check, setCheck] = useState<ReleaseCheck | null>(null)
  const [curVer, setCurVer] = useState(version)
  const [err, setErr] = useState('')
  const [loading, setLoading] = useState(false)
  const [publishing, setPublishing] = useState(false)
  const [conflict, setConflict] = useState(false)
  const [note, setNote] = useState('')

  const bodyKey = resource === 'modules' ? 'draft_version' : 'version'

  const loadPreviewAndCheck = async (fresh: boolean) => {
    if (publishing) return
    setRev(null)
    setCheck(null)
    setLoading(true)
    setErr('')
    setConflict(false)
    try {
      let v = curVer
      if (fresh) {
        const path = resource === 'modules' ? `/api/v1/modules/${id}` : `/api/v1/applications/${id}`
        const r = await api.get<{ module?: { draft_version: number }; application?: { version: number } }>(wsPath(path))
        v = r.module?.draft_version ?? r.application?.version ?? curVer
        setCurVer(v)
        if (resource === 'modules') {
          qc.setQueryData(['module', id], r)
        } else if (r.application) {
          qc.setQueryData(['application', id], r.application)
        }
      }
      if (resource === 'applications') {
        const ck = await api.post<ReleaseCheck>(wsPath(`/api/v1/applications/${id}/release/check`), { version: v })
        setCheck(ck)
        if (ck.candidate) {
          const pv = await api.post<PublishReview>(wsPath(`/api/v1/applications/${id}/publish/preview`), { version: v })
          setRev(pv)
        }
      } else {
        setRev(await api.post<PublishReview>(wsPath(`/api/v1/${resource}/${id}/publish/preview`), { [bodyKey]: v }))
      }
    } catch (e) {
      setRev(null)
      setErr((e as Error).message)
    } finally {
      setLoading(false)
    }
  }
  useEffect(() => { void loadPreviewAndCheck(false) }, []) // eslint-disable-line react-hooks/exhaustive-deps

  const publish = async () => {
    if (!rev?.candidate || publishing) return
    setPublishing(true)
    setErr('')
    setConflict(false)
    try {
      await api.post(wsPath(`/api/v1/${resource}/${id}/publish`), {
        [bodyKey]: curVer,
        expected_base: rev.base?.digest ?? '',
        candidate: rev.candidate.digest,
        note,
      })
      if (resource === 'applications') {
        void qc.invalidateQueries({ queryKey: ['app-releases', id] })
      }
      onPublished()
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) setConflict(true)
      setErr((e as Error).message)
    } finally {
      setPublishing(false)
    }
  }

  const blocked = resource === 'applications' && check?.status === 'blocked'
  const findings: Finding[] = (check?.findings ?? []).map((f) => ({
    code: f.code,
    severity: f.severity,
    category: f.category,
    section: f.section,
    message: f.message,
    fix: f.fix,
  }))

  const handleJump = (f: Finding) => {
    if (f.section && onJumpSection) {
      onClose()
      onJumpSection(f.section)
    } else if ((f.category === 'schema' || f.category === 'boundary') && onOpenSettings) {
      onClose()
      onOpenSettings()
    }
  }

  const candDigest = rev?.candidate?.digest ?? check?.candidate ?? ''

  return (
    <Modal title="Review & publish" wide onClose={() => { if (!publishing) onClose() }} dirty={note !== ''}
      footer={<>
        <div className="mr-auto text-xs text-mute">
          {blocked
            ? 'Resolve blocking readiness issues before publishing.'
            : rev?.identical
              ? 'No changes to publish.'
              : candDigest
                ? 'Digest-bound publish — reviewed candidate only.'
                : ''}
        </div>
        {(conflict || (err && !rev && !check)) && (
          <Button onClick={() => void loadPreviewAndCheck(true)} disabled={loading}>Refresh preview</Button>
        )}
        <Button variant="primary"
          disabled={publishing || loading || conflict || blocked || !rev?.candidate || rev.identical}
          title={rev?.identical ? 'Candidate is identical to the published base' : blocked ? 'Blocked by readiness check' : undefined}
          onClick={() => void publish()} data-testid="review-publish">
          <SwapLabel show={publishing ? 'Publishing…' : 'Publish'} options={['Publish', 'Publishing…'] as const} />
        </Button>
      </>}>
      {loading && <Loading label="Checking readiness & building candidate…" />}
      {err && <div className="mb-3"><Notice tone="warning">{err}</Notice></div>}
      {!loading && !rev && !check && !err && <Notice tone="neutral">Preview unavailable.</Notice>}

      {!loading && (rev?.candidate || check) && (
        <div className="space-y-4 text-xs">
          {/* 1. Release summary */}
          <div className="flex flex-wrap items-center gap-3 rounded-[3px] border border-line bg-surface px-3 py-2">
            <span className="inline-flex items-center gap-1.5 font-mono text-sec">
              <span className="font-sans text-mute">base</span>
              {rev?.base
                ? <>{rev.base.version ? `v${rev.base.version} · ` : ''}<DigestChip digest={rev.base.digest} /></>
                : check?.live_digest
                  ? <>{check.live_seq ? `v${check.live_seq} · ` : ''}<DigestChip digest={check.live_digest} /></>
                  : <span>none</span>}
            </span>
            <span className="text-mute" aria-hidden="true">→</span>
            {candDigest ? (
              <DigestChip prefix="candidate" digest={candDigest} length={18} />
            ) : (
              <span className="text-danger">candidate unavailable</span>
            )}
            {check?.has_drift && (
              <span className="rounded-[3px] border border-warn/40 bg-warn/10 px-1.5 py-px font-medium text-warn-text">
                policy/dependency drift
              </span>
            )}
            <div className="ml-auto">
              <StatusIndicator
                status={blocked ? 'blocked' : rev?.identical ? 'identical' : check?.status ?? 'ready'}
                pill
              />
            </div>
          </div>

          {/* 2. Readiness checklist (applications) */}
          {resource === 'applications' && findings.length > 0 && (
            <section aria-label="Readiness">
              <div className="mb-1.5 font-medium text-ink">Readiness</div>
              <FindingList findings={findings} onJump={(onJumpSection || onOpenSettings) ? handleJump : undefined} />
            </section>
          )}

          {/* 3. Semantic & section diffs */}
          {(rev?.changes?.length ?? 0) > 0 && <GrantDiffCard changes={rev!.changes} />}

          {/* 4. Publish note */}
          <div>
            <label htmlFor="review-note" className="mb-1 block text-xs font-medium text-sec">
              Release note <span className="font-normal text-mute">(optional, max 500 chars)</span>
            </label>
            <input id="review-note" className={inputCls} placeholder="Summarize what changed and why…" maxLength={500} value={note}
              onChange={(e) => setNote(e.target.value)} data-testid="review-note" />
          </div>

          {rev?.candidate && (
            <details className="rounded-[3px] border border-line bg-surface/40 px-3 py-2">
              <summary className="cursor-pointer text-mute hover:text-ink">Raw resolved snapshots</summary>
              <div className="mt-2 space-y-2">
                {rev.base && (
                  <div>
                    <div className="mb-1 font-medium text-sec">Base</div>
                    <pre className="max-h-48 overflow-auto rounded border border-line bg-code p-2 font-mono text-[11px]">{JSON.stringify(rev.base, null, 2)}</pre>
                  </div>
                )}
                <div>
                  <div className="mb-1 font-medium text-sec">Candidate</div>
                  <pre className="max-h-48 overflow-auto rounded border border-line bg-code p-2 font-mono text-[11px]">{JSON.stringify(rev.candidate, null, 2)}</pre>
                </div>
              </div>
            </details>
          )}
        </div>
      )}
    </Modal>
  )
}
