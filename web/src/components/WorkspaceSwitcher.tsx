import { useEffect, useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api, currentWorkspace, setWorkspace, workspaces, Workspace } from '../api'
import { Button, Menu, MenuItem, MenuSeparator, Modal } from './ui'

export default function WorkspaceSwitcher() {
  const nav = useNavigate()
  const qc = useQueryClient()
  const [showNew, setShowNew] = useState(false)
  const [name, setName] = useState('')
  const [err, setErr] = useState('')
  const { data } = useQuery({ queryKey: ['workspaces'], queryFn: workspaces, staleTime: 30_000 })
  const list = useMemo(() => data ?? [], [data])
  const cur = currentWorkspace()
  const active = list.find((w) => w.id === cur) ?? list.find((w) => w.personal) ?? list[0]

  useEffect(() => {
    // Validate the stored selection; fall back to personal.
    if (list.length && !list.find((w) => w.id === cur)) setWorkspace(null)
  }, [list, cur])

  if (!list.length) return null
  const pick = (id: string | null) => {
    setWorkspace(id)
    qc.clear()
    nav('/')
    window.location.reload()
  }
  const create = async () => {
    setErr('')
    try {
      const r = await api.post<{ workspace: Workspace }>('/api/v1/workspaces', { name })
      setShowNew(false)
      qc.invalidateQueries({ queryKey: ['workspaces'] }).then(() => pick(r.workspace.id))
    } catch (e: any) { setErr(e.message) }
  }

  return (
    <div className="flex items-center">
      {list.length > 1 ? (
        <Menu trigger={
          <button className="border border-line px-2 py-1 text-xs text-ink transition-colors hover:border-ink aria-expanded:border-ink">
            {active?.name ?? 'workspace'} {active?.personal ? '' : `· ${active?.role}`} ▾
          </button>
        }>
          {list.map((w) => (
            <MenuItem key={w.id} onSelect={() => pick(w.personal ? null : w.id)}>
              <span className="min-w-0 flex-1 truncate">{w.name}{w.personal ? ' (personal)' : ''}</span>
              <span className="text-xs text-mute">{w.role}</span>
            </MenuItem>
          ))}
          <MenuSeparator />
          <MenuItem onSelect={() => setShowNew(true)}>New workspace…</MenuItem>
          {active && <MenuItem onSelect={() => nav(`/workspaces/${active.id}`)}>Workspace settings</MenuItem>}
        </Menu>
      ) : (
        <span className="text-xs text-mute">{active?.name}</span>
      )}
      {showNew && (
        <Modal title="New workspace" onClose={() => setShowNew(false)} footer={
          <>
            <Button onClick={() => setShowNew(false)}>Cancel</Button>
            <Button variant="primary" onClick={create} disabled={!name.trim()}>Create</Button>
          </>
        }>
          <input autoFocus className="w-full border border-line bg-paper px-2 py-1.5 text-sm text-ink" placeholder="Workspace name"
            value={name} onChange={(e) => setName(e.target.value)} />
          {err && <p className="mt-2 text-xs text-warn-text">{err}</p>}
        </Modal>
      )}
    </div>
  )
}
