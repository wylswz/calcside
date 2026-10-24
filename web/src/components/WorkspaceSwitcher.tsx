import { useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api, currentWorkspace, setWorkspace, workspaces, Workspace } from '../api'
import { Button, Modal } from './ui'

export default function WorkspaceSwitcher() {
  const nav = useNavigate()
  const qc = useQueryClient()
  const [open, setOpen] = useState(false)
  const [showNew, setShowNew] = useState(false)
  const [name, setName] = useState('')
  const [err, setErr] = useState('')
  const ref = useRef<HTMLDivElement>(null)
  const { data } = useQuery({ queryKey: ['workspaces'], queryFn: workspaces, staleTime: 30_000 })
  const list = useMemo(() => data ?? [], [data])
  const cur = currentWorkspace()
  const active = list.find((w) => w.id === cur) ?? list.find((w) => w.personal) ?? list[0]

  useEffect(() => {
    // Validate the stored selection; fall back to personal.
    if (list.length && !list.find((w) => w.id === cur)) setWorkspace(null)
    const close = (e: MouseEvent) => { if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false) }
    document.addEventListener('click', close)
    return () => document.removeEventListener('click', close)
  }, [list, cur])

  if (!list.length) return null
  const pick = (id: string | null) => {
    setWorkspace(id)
    setOpen(false)
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
    <div className="relative flex items-center" ref={ref}>
      {list.length > 1 ? (
        <button className="border border-line px-2 py-1 text-xs text-ink hover:border-ink" onClick={() => setOpen(!open)}>
          {active?.name ?? 'workspace'} {active?.personal ? '' : `· ${active?.role}`} ▾
        </button>
      ) : (
        <span className="text-xs text-faint">{active?.name}</span>
      )}
      {open && (
        <div className="absolute right-0 top-full z-50 mt-1 w-56 border border-line-strong bg-paper shadow">
          {list.map((w) => (
            <button key={w.id} className="flex w-full items-center justify-between px-3 py-2 text-left text-xs text-ink hover:bg-wash"
              onClick={() => pick(w.personal ? null : w.id)}>
              <span>{w.name}{w.personal ? ' (personal)' : ''}</span>
              <span className="text-faint">{w.role}</span>
            </button>
          ))}
          <button className="w-full border-t border-line px-3 py-2 text-left text-xs text-accent hover:bg-wash" onClick={() => { setOpen(false); setShowNew(true) }}>
            + New workspace
          </button>
          {active && <button className="w-full border-t border-line px-3 py-2 text-left text-xs text-sec hover:bg-wash" onClick={() => { setOpen(false); nav(`/workspaces/${active.id}`) }}>
            Workspace settings
          </button>}
        </div>
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
