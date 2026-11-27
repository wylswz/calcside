import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api, wsPath } from '../api'
import type { components } from '../api/schema'
import { useHeldOrder } from '../stableList'
import { fmtDateTime } from '../interrupts'
import { Button, Confirm, Loading, Modal, Notice, RevealNew, inputCls } from './ui'
import { WorkStatusbar, WorkView } from './ApplicationWorkView'
import { DigestChip, StatusIndicator } from '../ui/core'
import { GrantDiffCard } from '../ui/GrantDiffCard'

type ReleaseList = components['schemas']['ApplicationReleaseListResponse']
type ReleaseEntry = components['schemas']['ApplicationReleaseEntry']
type PublishReview = components['schemas']['PublishReview']

// ReleaseHistory: EP-0026 History tab rendered on the flush workbench
// grid (.wb-toolbar + .wb-table + .wb-statusbar — zero rounded cards
// between straight lines). Supports comparing releases, restoring into
// draft, and atomic live-pointer rollback.
export function ReleaseHistory({
  appId,
  active,
  draftVersion,
  hasUnsavedDraft,
  canConfigure,
  onRestored,
}: {
  appId: string
  active: boolean
  draftVersion: number
  hasUnsavedDraft: boolean
  canConfigure: boolean
  onRestored: () => void
}) {
  const qc = useQueryClient()
  const [comparePair, setComparePair] = useState<{ base: number; head: number | 'draft' } | null>(null)
  const [restoreTarget, setRestoreTarget] = useState<ReleaseEntry | null>(null)
  const [rollbackTarget, setRollbackTarget] = useState<ReleaseEntry | null>(null)
  const [rbNote, setRbNote] = useState('')
  const [actionSeq, setActionSeq] = useState(0)

  const q = useQuery({
    queryKey: ['app-release-history', appId],
    queryFn: async () => {
      const data = await api.get<ReleaseList>(wsPath(`/api/v1/applications/${encodeURIComponent(appId)}/releases`))
      qc.setQueryData(['app-releases', appId], data)
      return data
    },
    enabled: active,
    gcTime: 0,
    refetchInterval: active ? 5000 : false,
  })

  // Record the cache timestamp when entering the History tab so the
  // first fresh fetch after tab entry populates the table cleanly while
  // subsequent 5s background polls remain held behind "Show N new" (S5).
  const [prevActive, setPrevActive] = useState(active)
  const [enteredAt, setEnteredAt] = useState(0)
  if (active !== prevActive) {
    setPrevActive(active)
    if (active) setEnteredAt(q.dataUpdatedAt)
  }

  const held = useHeldOrder(
    q.data?.releases?.map((r) => ({ r })) ?? undefined,
    ({ r }) => ({ key: String(r.seq), slot: 'releases' }),
    { reset: `${appId}:${active}:${actionSeq}:${q.dataUpdatedAt > enteredAt ? 1 : 0}` },
  )

  const liveDigest = q.data?.live_release_id ?? ''
  const liveSeq = q.data?.live_seq

  const cmpQ = useQuery({
    queryKey: ['app-release-compare', appId, comparePair?.base, comparePair?.head],
    queryFn: () =>
      api.get<PublishReview>(
        wsPath(`/api/v1/applications/${encodeURIComponent(appId)}/releases/compare?base=${comparePair!.base}&head=${comparePair!.head}`),
      ),
    enabled: comparePair !== null,
    retry: false,
  })

  return (
    <WorkView>
      <div className="wb-toolbar">
        <h3 className="wb-pane-title">Releases</h3>
        <span className="wb-status-num text-sec">{held.rows.length}</span>
        {liveSeq ? (
          <span className="wb-toolbar-note">
            Live v{liveSeq} · {q.data?.status}
          </span>
        ) : (
          <span className="wb-toolbar-note">No live release</span>
        )}
        <div className="wb-toolbar-side">
          {liveSeq && canConfigure && (
            <Button variant="ghost" size="xs" onClick={() => setComparePair({ base: liveSeq, head: 'draft' })}>
              Compare live → draft
            </Button>
          )}
          <RevealNew count={held.arrived} moved={held.moved} onClick={held.reveal} />
        </div>
      </div>

      <section className="wb-pane wb-fill">
        <div className="wb-pane-body" role="region" aria-label="Releases table" tabIndex={0}>
          {q.isLoading && <div className="wb-pane-notice"><Loading /></div>}
          {q.isError && <div className="wb-pane-notice"><Notice tone="warning">Could not load release history.</Notice></div>}
          {!q.isLoading && !q.isError && held.rows.length === 0 && held.arrived === 0 && (
            <p className="wb-pane-empty">No releases published yet.</p>
          )}
          {held.rows.length > 0 && (
            <table className="tbl wb-table">
              <thead>
                <tr>
                  <th>Release</th>
                  <th>Candidate</th>
                  <th>Note</th>
                  <th>Published</th>
                  <th className="wb-num">Actions</th>
                </tr>
              </thead>
              <tbody>
                {held.rows.map(({ item: { r } }, idx) => {
                  const prevSeq = idx + 1 < held.rows.length ? held.rows[idx + 1].item.r.seq : 0
                  return (
                    <tr key={r.seq}>
                      <td className="whitespace-nowrap">
                        <span className="inline-flex items-center gap-2 font-mono text-xs font-semibold text-ink">
                          v{r.seq}
                          {r.is_live && <StatusIndicator status="live" tone="ok" pill />}
                          {r.rollback_of_seq != null && (
                            <span className="font-sans text-[11px] font-normal text-mute">
                              rollback → v{r.rollback_of_seq}
                            </span>
                          )}
                        </span>
                      </td>
                      <td>
                        <DigestChip digest={r.release_id} />
                      </td>
                      <td className="max-w-[280px] truncate text-xs text-body" title={r.note}>
                        {r.note || <span className="text-mute">—</span>}
                      </td>
                      <td className="whitespace-nowrap text-xs text-sec">
                        {fmtDateTime(r.published_at)}
                      </td>
                      <td className="whitespace-nowrap text-right">
                        <div className="inline-flex items-center gap-1">
                          <Button variant="ghost" size="xs" onClick={() => setComparePair({ base: prevSeq, head: r.seq })}>
                            Diff
                          </Button>
                          {canConfigure && (
                            <Button variant="ghost" size="xs" onClick={() => setRestoreTarget(r)}>
                              Restore…
                            </Button>
                          )}
                          {canConfigure && !r.is_live && liveDigest && (
                            <Button variant="ghost" size="xs" className="text-warn-text hover:text-danger"
                              onClick={() => { setRbNote(`Rollback to v${r.seq}`); setRollbackTarget(r) }}>
                              Rollback…
                            </Button>
                          )}
                        </div>
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          )}
        </div>
      </section>

      <WorkStatusbar>
        <span className="wb-grow wb-status-num">
          {held.rows.length} release{held.rows.length === 1 ? '' : 's'}
        </span>
      </WorkStatusbar>

      {comparePair && (
        <Modal
          title={`Compare ${comparePair.base === 0 ? 'empty' : `v${comparePair.base}`} → ${comparePair.head === 'draft' ? 'draft' : `v${comparePair.head}`}`}
          wide
          dirty={false}
          onClose={() => setComparePair(null)}
        >
          {cmpQ.isLoading && <Loading label="Computing release diff…" />}
          {cmpQ.isError && <Notice tone="warning">{(cmpQ.error as Error).message}</Notice>}
          {cmpQ.data && (
            <div className="space-y-3 text-xs">
              {cmpQ.data.identical ? (
                <Notice>Releases are identical ({cmpQ.data.candidate.digest.slice(0, 20)}…).</Notice>
              ) : (
                <GrantDiffCard changes={cmpQ.data.changes} />
              )}
            </div>
          )}
        </Modal>
      )}

      {restoreTarget && (
        <Confirm
          title={`Restore v${restoreTarget.seq} into draft?`}
          confirm="Restore into draft"
          pending="Restoring…"
          onClose={() => setRestoreTarget(null)}
          action={async () => {
            await api.post(wsPath(`/api/v1/applications/${encodeURIComponent(appId)}/releases/${restoreTarget.seq}/restore`), {
              version: draftVersion,
            })
            await qc.invalidateQueries({ queryKey: ['application', appId] })
            onRestored()
          }}
        >
          <p className="text-sm text-body">
            Copies v{restoreTarget.seq}’s sections, schemas, and spec into the working draft. The live release is unchanged until you review and publish.
            {hasUnsavedDraft ? ' Unsaved local edits will be replaced.' : ''}
          </p>
        </Confirm>
      )}

      {rollbackTarget && (
        <Confirm
          tone="danger"
          title={`Rollback live pointer to v${rollbackTarget.seq}?`}
          confirm="Rollback live"
          pending="Rolling back…"
          onClose={() => setRollbackTarget(null)}
          action={async () => {
            await api.post(wsPath(`/api/v1/applications/${encodeURIComponent(appId)}/releases/${rollbackTarget.seq}/rollback`), {
              expected_base: liveDigest,
              note: rbNote,
            })
            await Promise.all([
              qc.invalidateQueries({ queryKey: ['application', appId] }),
              qc.invalidateQueries({ queryKey: ['app-releases', appId] }),
              q.refetch(),
            ])
            setActionSeq((n) => n + 1)
          }}
        >
          <div className="space-y-3 text-sm text-body">
            <p>
              Future invokes will switch to immutable release <strong>v{rollbackTarget.seq}</strong> (<code>{rollbackTarget.release_id.slice(0, 18)}…</code>).
              Existing in-flight or waiting runs stay pinned to their original release, and external side effects already performed are not undone.
            </p>
            <input
              className={inputCls}
              value={rbNote}
              maxLength={500}
              placeholder="Rollback reason (recorded in history)"
              onChange={(e) => setRbNote(e.target.value)}
            />
          </div>
        </Confirm>
      )}
    </WorkView>
  )
}
