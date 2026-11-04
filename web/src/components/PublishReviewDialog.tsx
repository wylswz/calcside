import { useEffect, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { api, wsPath, ApiError } from '../api'
import { Button, Loading, Notice, SwapLabel, Modal, inputCls } from './ui'

import type { components } from '../api/schema'
type PublishReview = components['schemas']['PublishReview']

// Shared review dialog for modules and applications: preview shows the
// full immutable candidate + base snapshots (not just semantic diffs),
// then publish posts the captured digest. A 409 keeps the dialog open
// with an explicit Refresh — no automatic retry.
export function PublishReviewDialog({ resource, id, version, onClose, onPublished }: {
  resource: 'modules' | 'applications'
  id: string
  version: number
  onClose: () => void
  onPublished: () => void
}) {
  const qc = useQueryClient()
  const [rev, setRev] = useState<PublishReview | null>(null)
  const [curVer, setCurVer] = useState(version)
  const [err, setErr] = useState('')
  const [loading, setLoading] = useState(false)
  const [publishing, setPublishing] = useState(false)
  const [conflict, setConflict] = useState(false)
  const [note, setNote] = useState('')

  const bodyKey = resource === 'modules' ? 'draft_version' : 'version'

  const preview = async (fresh: boolean) => {
    if (publishing) return
    setRev(null)
    setLoading(true)
    setErr('')
    setConflict(false)
    try {
      let v = curVer
      if (fresh) {
        // Refresh pulls the live draft so the preview runs on it.
        const path = resource === 'modules' ? `/api/v1/modules/${id}` : `/api/v1/applications/${id}`
        const r = await api.get<{ module?: { draft_version: number }; application?: { version: number } }>(wsPath(path))
        v = r.module?.draft_version ?? r.application?.version ?? curVer
        setCurVer(v)
        // Cache shape must match each page's queryFn: the module page
        // stores the envelope, the app page stores the entity.
        if (resource === 'modules') {
          qc.setQueryData(['module', id], r)
        } else if (r.application) {
          qc.setQueryData(['application', id], r.application)
        }
      }
      setRev(await api.post<PublishReview>(wsPath(`/api/v1/${resource}/${id}/publish/preview`), { [bodyKey]: v }))
    } catch (e) {
      setRev(null)
      setErr((e as Error).message)
    } finally {
      setLoading(false)
    }
  }
  // Initial preview uses the saved version the caller handed us — the
  // user can explicitly Refresh for the latest.
  useEffect(() => { void preview(false) }, []) // eslint-disable-line react-hooks/exhaustive-deps

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
      onPublished()
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) setConflict(true)
      setErr((e as Error).message)
    } finally {
      setPublishing(false)
    }
  }

  return (
    <Modal title="Publish" wide onClose={() => { if (!publishing) onClose() }} dirty={note !== ''}
      footer={<>
        {(conflict || (err && !rev)) && <Button onClick={() => void preview(true)} disabled={loading}>Refresh preview</Button>}
        <Button variant="primary" disabled={publishing || loading || conflict || !rev?.candidate || rev.identical}
          title={rev?.identical ? 'Candidate is identical to the published base' : undefined}
          onClick={() => void publish()} data-testid="review-publish">
          <SwapLabel show={publishing ? 'Publishing…' : 'Publish'} options={['Publish', 'Publishing…'] as const} />
        </Button>
      </>}>
      {loading && <Loading label="previewing" />}
      {err && <Notice tone="warning">{err}</Notice>}
      {!loading && !rev && !err && <Notice tone="neutral">Preview unavailable.</Notice>}
      {rev?.candidate && (
        <div className="text-xs">
          <div className="mb-2 font-mono text-mute">
            candidate {rev.candidate.digest.slice(0, 20)}… · base {rev.base ? rev.base.digest.slice(0, 20) + '…' : 'none'}
            {rev.identical && ' · identical'}
          </div>
          {(rev.changes?.length ?? 0) > 0 && (
            <div className="mb-2 max-h-40 overflow-y-auto rounded border border-line p-2">
              {rev.changes.map((c, i) => (
                <div key={i} className="mb-1">
                  <span className="font-medium">{c.category}/{c.path}</span>
                  <span className="text-mute"> {c.kind}{c.direction ? ` · ${c.direction}` : ''}</span>
                  {(c.before !== undefined || c.after !== undefined) && (
                    <pre className="mt-0.5 max-h-20 overflow-y-auto text-mute">
                      {c.before !== undefined && <>− {typeof c.before === 'string' ? c.before : JSON.stringify(c.before)}</>}
                      {'\n'}
                      {c.after !== undefined && <>+ {typeof c.after === 'string' ? c.after : JSON.stringify(c.after)}</>}
                    </pre>
                  )}
                </div>
              ))}
            </div>
          )}
          {rev.base && (
            <details className="mb-2">
              <summary className="cursor-pointer text-mute">Full base snapshot</summary>
              <pre className="mt-1 max-h-48 overflow-y-auto rounded border border-line p-2">{JSON.stringify(rev.base, null, 2)}</pre>
            </details>
          )}
          <details className="mb-2">
            <summary className="cursor-pointer text-mute">Full candidate snapshot</summary>
            <pre className="mt-1 max-h-48 overflow-y-auto rounded border border-line p-2">{JSON.stringify(rev.candidate, null, 2)}</pre>
          </details>
          <input className={inputCls} placeholder="Note (optional, max 500)" maxLength={500} value={note}
            onChange={(e) => setNote(e.target.value)} data-testid="review-note" />
        </div>
      )}
    </Modal>
  )
}
