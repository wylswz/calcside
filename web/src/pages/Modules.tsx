import { useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api, wsPath } from '../api'
import { useRole } from '../workspace'
import { Button, Confirm, Loading, Notice, PageHeader, RevealNew, Modal, Field, inputCls, SwapLabel } from '../components/ui'
import { useHeldOrder } from '../stableList'

export type Module = {
  id: string; name: string; description?: string
  draft_version: number; latest_version: number; latest_sum?: string
  has_unpublished_changes: boolean
}

const DEFAULT_SOURCE = 'def hello(name):\n    return "Hello, " + name\n'

// Module names are workspace-unique identifiers (schema UNIQUE on
// workspace_id + name); the grammar is shared with spec.modules
// references. Store conflicts surface as a name-specific error.
const NAME_RE = /^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$/
const NAME_HELP = 'Workspace-unique name: 1–64 ASCII letters or digits first, then letters, digits, _, . or -.'

export default function Modules() {
  const nav = useNavigate()
  const qc = useQueryClient()
  const role = useRole()
  const canEdit = role === 'owner' || role === 'editor'
  const [creating, setCreating] = useState(false)
  const [name, setName] = useState('')
  const [saving, setSaving] = useState(false)
  const [deleting, setDeleting] = useState<Module | null>(null)
  const q = useQuery({
    queryKey: ['modules'],
    queryFn: () => api.get<{ modules: Module[] }>(wsPath('/api/v1/modules')),
    refetchInterval: 5000,
  })
  const held = useHeldOrder(q.data?.modules.map((m) => ({ m })) ?? undefined, ({ m }) => ({ key: m.id, slot: 'mods' }))

  const [createErr, setCreateErr] = useState('')
  const create = async () => {
    const trimmed = name.trim()
    if (!trimmed || saving) return
    if (!NAME_RE.test(trimmed)) {
      setCreateErr(NAME_HELP)
      return
    }
    setSaving(true)
    setCreateErr('')
    try {
      const r = await api.post<{ module: Module }>(wsPath('/api/v1/modules'), {
        name: trimmed,
        files: { 'main.star': DEFAULT_SOURCE },
      })
      setCreating(false)
      void qc.invalidateQueries({ queryKey: ['modules'] })
      nav(`/modules/${r.module.id}`)
    } catch (e) {
      setCreateErr((e as Error).message)
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="app-page">
      <PageHeader title="Modules"
        actions={canEdit && <Button variant="primary" onClick={() => setCreating(true)}>New module</Button>} />
      {q.isLoading && <Loading />}
      {q.isError && <Notice tone="warning">Could not load modules: {(q.error as Error).message}</Notice>}
      {held.arrived > 0 && <RevealNew count={held.arrived} moved={held.moved} onClick={held.reveal} />}
      {q.data && held.rows.length === 0 && held.arrived === 0 && (
        <p className="app-dim">No modules yet. {canEdit && <button className="link text-accent" onClick={() => setCreating(true)}>Create the first one.</button>}</p>
      )}
      {held.rows.length > 0 && (
        <div className="tbl-wrap">
          <table className="tbl">
            <thead><tr><th>Name</th><th>Latest</th><th>Draft</th><th /></tr></thead>
            <tbody>
              {held.rows.map(({ item: { m } }) => (
                <tr key={m.id} className="row-hover">
                  <td>
                    <Link to={`/modules/${m.id}`} className="link font-medium">{m.name}</Link>
                    {m.description && <div className="mt-0.5 max-w-[420px] truncate text-xs text-sec" title={m.description}>{m.description}</div>}
                  </td>
                  <td className="text-xs text-sec">{m.latest_version > 0 ? `v${m.latest_version}` : 'unpublished'}</td>
                  <td className="text-xs text-sec">
                    draft v{m.draft_version}
                    {m.has_unpublished_changes && <span className="text-mute"> · unpublished</span>}
                  </td>
                  <td className="text-right">
                    {canEdit && <Button size="sm" variant="danger" onClick={() => setDeleting(m)}>Delete</Button>}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {creating && (
        <Modal title="New module" onClose={() => setCreating(false)} dirty={name.trim() !== ''}
          footer={<>
            <Button variant="primary" disabled={saving || !name.trim()} onClick={() => void create()}>
              <SwapLabel show={saving ? 'Creating…' : 'Create'} options={['Create', 'Creating…'] as const} />
            </Button>
          </>}>
          {createErr && <Notice tone="warning">{createErr}</Notice>}
          <Field label="Unique name">
            <input className={inputCls} value={name} onChange={(e) => setName(e.target.value)} placeholder="strkit"
              maxLength={64} pattern="[A-Za-z0-9][A-Za-z0-9_.\-]{0,63}" title={NAME_HELP} autoFocus />
          </Field>
          <p className="app-dim mt-2 text-xs">{NAME_HELP} Creates main.star with a hello example — edit it on the module page.</p>
        </Modal>
      )}
      {deleting && (
        <Confirm title="Delete module" tone="danger" confirm="Delete" pending="Deleting…"
          action={async () => {
            await api.del(wsPath(`/api/v1/modules/${deleting.id}`))
            setDeleting(null)
            void qc.invalidateQueries({ queryKey: ['modules'] })
          }}
          onClose={() => setDeleting(null)}>
          Delete <b>{deleting.name}</b>? The library versions are removed — releases that already captured a snapshot keep working.
        </Confirm>
      )}
    </div>
  )
}
