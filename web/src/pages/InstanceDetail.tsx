import { useCallback, useEffect, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import CodeMirror from '@uiw/react-codemirror'
import { python } from '@codemirror/lang-python'
import { api, AuditEvent, ExecResult, Execution, FileEntry, Instance } from '../api'
import type { SecretSource } from '../api'

interface SpecSecret { ref?: string; source?: SecretSource; allowed_domains?: string[] }
import { Badge, Button, DecisionBadge, StatusBadge, fmtCountdown, fmtTime } from '../components/ui'

const DEFAULT_CODE = `# Starlark. Capabilities appear as globals when granted.
# fs.read/write/list, net.get/post, print/json/math are always available.
print("hello from calcside")
`

function FileBrowser({ id }: { id: string }) {
  const [dir, setDir] = useState('/work')
  const [file, setFile] = useState<{ path: string; content: string } | null>(null)
  const { data, refetch } = useQuery({
    queryKey: ['files', id, dir],
    queryFn: () => api.get<{ entries: FileEntry[] }>(`/api/v1/instances/${id}/files?path=${encodeURIComponent(dir)}`),
  })
  const open = async (e: FileEntry) => {
    if (e.is_dir) {
      setFile(null)
      setDir(e.path)
    } else {
      const r = await api.get<{ path: string; content: string }>(`/api/v1/instances/${id}/files?path=${encodeURIComponent(e.path)}`)
      setFile({ path: r.path, content: r.content })
    }
  }
  return (
    <div className="space-y-2">
      <div className="flex items-center gap-2 text-xs">
        <button className="text-blue-600 dark:text-blue-400" onClick={() => { setDir('/work'); setFile(null) }}>/work</button>
        {dir !== '/work' && (
          <>
            <span className="text-gray-400">/</span>
            <span className="font-mono">{dir.replace('/work/', '')}</span>
          </>
        )}
        <button className="ml-auto text-gray-500" onClick={() => refetch()}>refresh</button>
      </div>
      <ul className="rounded border border-gray-200 dark:border-gray-800 divide-y divide-gray-200 dark:divide-gray-800 text-xs font-mono max-h-48 overflow-y-auto">
        {(data?.entries ?? []).map((e) => (
          <li key={e.path}>
            <button className="w-full text-left px-2 py-1 hover:bg-gray-100 dark:hover:bg-gray-800" onClick={() => open(e)}>
              {e.is_dir ? '▸ ' : '  '}{e.name}{e.is_dir ? '/' : ` (${e.size}B)`}
            </button>
          </li>
        ))}
        {data && data.entries.length === 0 && <li className="px-2 py-1 text-gray-500">empty</li>}
      </ul>
      {file && (
        <pre className="rounded border border-gray-200 dark:border-gray-800 bg-gray-50 dark:bg-gray-900 p-2 text-xs max-h-64 overflow-auto whitespace-pre-wrap">
          {file.path}{'\n'}{file.content}
        </pre>
      )}
    </div>
  )
}

function AuditTab({ id }: { id: string }) {
  const { data } = useQuery({
    queryKey: ['audit', id],
    queryFn: () => api.get<{ events: AuditEvent[] }>(`/api/v1/audit?instance_id=${id}&limit=200`),
    refetchInterval: 5000,
  })
  return (
    <div className="overflow-x-auto rounded border border-gray-200 dark:border-gray-800">
      <table className="w-full text-xs">
        <thead className="bg-gray-100 dark:bg-gray-900 text-left text-gray-600 dark:text-gray-400">
          <tr>
            <th className="px-2 py-1.5">Time</th><th className="px-2 py-1.5">Cap</th><th className="px-2 py-1.5">Op</th>
            <th className="px-2 py-1.5">Args</th><th className="px-2 py-1.5">Decision</th><th className="px-2 py-1.5">Reason</th><th className="px-2 py-1.5">ms</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-gray-200 dark:divide-gray-800 font-mono">
          {(data?.events ?? []).map((e) => (
            <tr key={e.id}>
              <td className="px-2 py-1">{new Date(e.ts).toLocaleTimeString()}</td>
              <td className="px-2 py-1">{e.capability}</td>
              <td className="px-2 py-1">{e.op}</td>
              <td className="px-2 py-1 max-w-[220px] truncate" title={e.args}>{e.args}</td>
              <td className="px-2 py-1"><DecisionBadge decision={e.decision} /></td>
              <td className="px-2 py-1 max-w-[200px] truncate" title={e.reason}>{e.reason}</td>
              <td className="px-2 py-1">{e.duration_ms}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

export default function InstanceDetail() {
  const { id = '' } = useParams()
  const qc = useQueryClient()
  const [code, setCode] = useState(DEFAULT_CODE)
  const [result, setResult] = useState<ExecResult | null>(null)
  const [running, setRunning] = useState(false)
  const [tab, setTab] = useState<'execs' | 'audit'>('execs')

  const { data: inst } = useQuery({
    queryKey: ['instance', id],
    queryFn: () => api.get<{ instance: Instance }>(`/api/v1/instances/${id}`).then((r) => r.instance),
    refetchInterval: 15000,
  })
  const { data: execs, refetch: refetchExecs } = useQuery({
    queryKey: ['executions', id],
    queryFn: () => api.get<{ executions: Execution[] }>(`/api/v1/instances/${id}/executions?limit=50`),
  })

  const run = useCallback(async () => {
    if (running) return
    setRunning(true)
    try {
      const r = await api.post<ExecResult>(`/api/v1/instances/${id}/exec`, { code })
      setResult(r)
      refetchExecs()
      qc.invalidateQueries({ queryKey: ['audit', id] })
      qc.invalidateQueries({ queryKey: ['files', id] })
    } catch (e: any) {
      setResult({ exec_id: '', output: '', error: { type: 'runtime', message: e.message }, duration_ms: 0, steps: 0 })
    } finally {
      setRunning(false)
    }
  }, [code, id, running, refetchExecs, qc])

  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') {
        e.preventDefault()
        run()
      }
    }
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  }, [run])

  const keepalive = async () => {
    await api.post(`/api/v1/instances/${id}/keepalive`)
    qc.invalidateQueries({ queryKey: ['instance', id] })
  }

  if (!inst) return <p className="text-sm text-gray-500">loading…</p>

  return (
    <div className="space-y-4">
      <div className="flex items-center gap-3">
        <Link to="/" className="text-xs text-gray-500 hover:underline">← instances</Link>
        <span className="font-mono text-sm">{inst.id}</span>
        <StatusBadge status={inst.status} />
        {inst.status === 'running' && (
          <>
            <span className="text-xs text-gray-500">expires in {fmtCountdown(inst.expires_at)}</span>
            <Button onClick={keepalive} className="!px-2 !py-1 text-xs">Keepalive</Button>
          </>
        )}
      </div>

      <div className="grid grid-cols-1 lg:grid-cols-3 gap-4">
        <div className="lg:col-span-2 space-y-3">
          <div className="rounded border border-gray-300 dark:border-gray-700 overflow-hidden">
            <CodeMirror
              value={code}
              height="280px"
              extensions={[python()]}
              onChange={setCode}
              theme={window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'}
            />
          </div>
          <div className="flex items-center gap-2">
            <Button variant="primary" disabled={running || inst.status !== 'running'} onClick={run}>
              {running ? 'Running…' : 'Run (⌘/Ctrl+Enter)'}
            </Button>
            {result && (
              <span className="text-xs text-gray-500">
                {result.duration_ms}ms · {result.steps} steps
                {result.error && <> · <Badge tone="red">{result.error.type}</Badge></>}
              </span>
            )}
          </div>
          {result && (
            <div className="space-y-2">
              <pre className="rounded border border-gray-200 dark:border-gray-800 bg-gray-50 dark:bg-gray-900 p-3 text-xs font-mono max-h-64 overflow-auto whitespace-pre-wrap">
                {result.output || '(no output)'}
              </pre>
              {result.error && (
                <pre className="rounded border border-red-300 dark:border-red-900 bg-red-50 dark:bg-red-950/40 p-3 text-xs font-mono max-h-64 overflow-auto whitespace-pre-wrap text-red-700 dark:text-red-300">
                  error[{result.error.type}]: {result.error.message}
                  {result.error.backtrace ? '\n' + result.error.backtrace : ''}
                </pre>
              )}
            </div>
          )}

          <div className="flex gap-2 border-b border-gray-200 dark:border-gray-800 text-sm">
            <button className={`px-3 py-1.5 ${tab === 'execs' ? 'border-b-2 border-blue-600 font-medium' : 'text-gray-500'}`} onClick={() => setTab('execs')}>Executions</button>
            <button className={`px-3 py-1.5 ${tab === 'audit' ? 'border-b-2 border-blue-600 font-medium' : 'text-gray-500'}`} onClick={() => setTab('audit')}>Audit</button>
          </div>
          {tab === 'execs' && (
            <div className="overflow-x-auto rounded border border-gray-200 dark:border-gray-800">
              <table className="w-full text-xs">
                <thead className="bg-gray-100 dark:bg-gray-900 text-left text-gray-600 dark:text-gray-400">
                  <tr><th className="px-2 py-1.5">Exec</th><th className="px-2 py-1.5">Status</th><th className="px-2 py-1.5">Snippet</th><th className="px-2 py-1.5">ms</th><th className="px-2 py-1.5">Steps</th><th className="px-2 py-1.5">Time</th></tr>
                </thead>
                <tbody className="divide-y divide-gray-200 dark:divide-gray-800 font-mono">
                  {(execs?.executions ?? []).map((x) => (
                    <tr key={x.id}>
                      <td className="px-2 py-1">{x.id}</td>
                      <td className="px-2 py-1">{x.status === 'ok' ? <Badge tone="green">ok</Badge> : <Badge tone="red">{x.error_type || 'error'}</Badge>}</td>
                      <td className="px-2 py-1 max-w-[240px] truncate" title={x.code_snippet}>{x.code_snippet}</td>
                      <td className="px-2 py-1">{x.duration_ms}</td>
                      <td className="px-2 py-1">{x.steps}</td>
                      <td className="px-2 py-1">{fmtTime(x.created_at)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          {tab === 'audit' && <AuditTab id={id} />}
        </div>

        <div className="space-y-4">
          <div>
            <h3 className="text-xs font-semibold text-gray-600 dark:text-gray-400 mb-2">Files</h3>
            <FileBrowser id={id} />
          </div>
          {(Object.keys(inst.spec?.env ?? {}).length > 0 || Object.keys(inst.spec?.secrets ?? {}).length > 0) && (
            <div>
              <h3 className="text-xs font-semibold text-gray-600 dark:text-gray-400 mb-2">Env & Secrets</h3>
              <div className="rounded border border-gray-200 dark:border-gray-800 p-2 text-xs font-mono space-y-1">
                {Object.entries(inst.spec.env ?? {}).map(([k, v]) => (
                  <div key={k}><span className="text-gray-500">{k}</span>={v as string}</div>
                ))}
                {(Object.entries(inst.spec.secrets ?? {}) as [string, SpecSecret][]).map(([k, v]) => (
                  <div key={k}>
                    <span className="text-gray-500">{k}</span>
                    <span className="text-gray-400"> {v.source === 'vault' ? `→vault:${v.ref}` : '(inline)'} {(v.allowed_domains ?? []).join(', ')}</span>
                  </div>
                ))}
              </div>
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
