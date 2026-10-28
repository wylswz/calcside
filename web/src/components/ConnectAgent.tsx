import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { api } from '../api'
import { Button, Field, Modal, SwapLabel, inputCls } from './ui'
import { CopyBlock, MCPGuide } from './MCP'

// ConnectAgent creates an instance-bound API key and shows the MCP
// endpoint for this instance only.
export default function ConnectAgent({ id, workspaceId, onClose }: { id: string; workspaceId?: string; onClose: () => void }) {
  const [name, setName] = useState(`agent · ${id.slice(0, 8)}`)
  const [days, setDays] = useState(90)
  const qc = useQueryClient()
  const [secret, setSecret] = useState('')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const url = `${window.location.origin}/mcp/instances/${id}`
  const create = async () => {
    setErr(''); setBusy(true)
    try {
      const r = await api.post<{ secret: string }>(`/api/v1/keys${workspaceId ? `?workspace=${workspaceId}` : ''}`, {
        name, instance_id: id, expires_in_seconds: days * 86400,
      })
      setSecret(r.secret)
      qc.invalidateQueries({ queryKey: ['keys'] })
    } catch (e: any) { setErr(e.message) } finally { setBusy(false) }
  }
  return (
    <Modal title="Connect agent" onClose={onClose} dirty={secret ? true : undefined} discardPrompt={secret ? "The key secret won't be shown again." : undefined} discardConfirm="Close" footer={
      secret ? <Button variant="primary" onClick={onClose}>Done</Button>
        : <><Button onClick={onClose}>Cancel</Button><Button variant="primary" onClick={create} disabled={!name.trim() || busy}><SwapLabel show={busy ? 'Creating…' : 'Create key'} options={['Create key', 'Creating…']} /></Button></>
    }>
      {!secret ? (
        <div className="space-y-3 text-sm">
          <p className="text-sec">Creates a key bound to this instance only — an agent using it can run code here but reaches nothing else.</p>
          <Field label="Key name"><input className={inputCls} value={name} onChange={(e) => setName(e.target.value)} /></Field>
          <Field label="Expires in (days; 0 = never)"><input type="number" className={inputCls} value={days} onChange={(e) => setDays(Number(e.target.value))} /></Field>
          {err && <p className="text-xs text-warn-text">{err}</p>}
        </div>
      ) : (
        <div className="space-y-3 text-sm">
          <p className="text-sec">Key secret — shown once:</p>
          <CopyBlock text={secret} mono />
          <MCPGuide secret={secret} url={url} />
        </div>
      )}
    </Modal>
  )
}
