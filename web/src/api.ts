// API client for the calcside console, generated from api/openapi.yaml:
// `src/api/schema.ts` is produced by openapi-typescript (`pnpm gen`) —
// never edit it by hand. Request/response shapes come from the schema.
import createClient from 'openapi-fetch'
import type { paths, components } from './api/schema'

// --- generated types (aliases keep page imports tidy) ---

export type User = components['schemas']['User']
export type Instance = components['schemas']['Instance']
export type Application = components['schemas']['Application']
export type ApplicationStatus = components['schemas']['ApplicationStatus']
export type ProgramResult = components['schemas']['ApplicationRunResponse']['result']
export type ApplicationRun = components['schemas']['ApplicationRunResponse']
export type SharedInstance = components['schemas']['SharedInstance']
export type InstanceSpec = components['schemas']['InstanceSpec']
export type ExecResult = components['schemas']['ExecResult']
export type ExecError = components['schemas']['ExecError']
export type Execution = components['schemas']['Execution']
export type ExecutionDetail = components['schemas']['ExecutionDetail']
export type InstancePrompt = components['schemas']['InstancePrompt']
export type CreateKeyRequest = components['schemas']['CreateKeyRequest']
export type FileEntry = components['schemas']['FileEntry']
export type InstanceInspect = components['schemas']['InstanceInspect']
export type ResourceUsages = components['schemas']['ResourceUsages']
export type CompletionSymbol = components['schemas']['CompletionSymbol']
export type CompletionContext = components['schemas']['CompletionContext']
export type EditorMetadata = components['schemas']['EditorMetadata']
export type AuditEvent = components['schemas']['AuditEvent']
export type Policy = components['schemas']['Policy']
export type LLMProvider = components['schemas']['LLMProvider']
export type APIKey = components['schemas']['APIKey']
export type AuthConfig = components['schemas']['AuthConfig']
export type Secret = components['schemas']['Secret']
export type ExtensionCatalog = components['schemas']['ExtensionCatalog']
export type ExtensionInfo = components['schemas']['ExtensionInfo']
export type ExtConfigField = components['schemas']['ExtConfigField']
export type ArtifactConfig = components['schemas']['ArtifactConfig']
export type ArtifactPreview = components['schemas']['ArtifactPreview']
export type Notebook = components['schemas']['Notebook']
export type NotebookSectionMeta = components['schemas']['NotebookSectionMeta']
export type NotebookSection = components['schemas']['NotebookSection']
export type NotebookSectionResponse = components['schemas']['NotebookSectionResponse']
export type SaveNotebookSectionResponse = components['schemas']['SaveNotebookSectionResponse']
export type SaveNotebookSectionRequest = components['schemas']['SaveNotebookSectionRequest']
export type DeleteNotebookSectionResponse = components['schemas']['DeleteNotebookSectionResponse']
export type RunSectionsResponse = components['schemas']['RunSectionsResponse']
export type RunSectionResult = components['schemas']['RunSectionResult']

export interface ArtifactDownload { blob: Blob; filename: string; redacted: boolean; fileCount: number }

export function saveArtifact(download: ArtifactDownload): void {
  const url = URL.createObjectURL(download.blob)
  const link = document.createElement('a')
  link.href = url
  link.download = download.filename
  link.rel = 'noopener'
  document.body.appendChild(link)
  try { link.click() }
  finally {
    link.remove()
    window.setTimeout(() => URL.revokeObjectURL(url), 1000)
  }
}


// --- enum types (values live in schema.ts *Values consts) ---

export type InstanceStatus = components['schemas']['InstanceStatus']
export type ExecStatus = components['schemas']['ExecStatus']
export type ExecErrorType = components['schemas']['ExecErrorType']
export type Decision = components['schemas']['Decision']
export type Phase = components['schemas']['Phase']
export type CapabilityName = components['schemas']['CapabilityName']
export type HttpMethod = components['schemas']['HTTPMethod']
export type SecretSource = components['schemas']['SecretSource']
export type ApiErrorCode = components['schemas']['APIErrorCode']
export type FieldType = components['schemas']['FieldType']
export type AuthKind = components['schemas']['AuthKind']

export class ApiError extends Error {
  status: number
  code: ApiErrorCode | '' // '' if the server sent an unrecognized code
  constructor(status: number, code: ApiErrorCode | '', message: string) {
    super(message)
    this.status = status
    this.code = code
  }
}

// devMode is populated by loadAuthConfig at app start; when the server
// is in dev mode we never redirect to /login.
let devMode = false

export function isDevMode(): boolean {
  return devMode
}

// loadAuthConfig fetches auth/config. When the backend is unreachable
// (network error, or a 5xx such as the Vite proxy's 502 while the
// backend is still starting) it retries with 1s→2s→4s… backoff capped
// at 5s and reports the state through onRetrying — the app must not
// fall into the login flow just because the backend isn't up yet.
export async function loadAuthConfig(onRetrying?: (retrying: boolean) => void): Promise<AuthConfig> {
  let delay = 1000
  for (;;) {
    try {
      const cfg = await api.get<AuthConfig>('/api/v1/auth/config')
      devMode = cfg.dev_mode ?? false
      onRetrying?.(false)
      return cfg
    } catch (e) {
      const retryable = !(e instanceof ApiError) || e.status >= 500
      onRetrying?.(retryable)
      if (!retryable) throw e
      await new Promise((r) => setTimeout(r, delay))
      delay = Math.min(delay * 2, 5000)
    }
  }
}

const rawClient = createClient<paths>({ credentials: 'same-origin' })

rawClient.use({
  async onRequest({ request }) {
    if (request.method !== 'GET' && request.method !== 'HEAD') {
      request.headers.set('X-Requested-With', 'calcside')
    }
    return request
  },
  async onResponse({ response }) {
    const url = new URL(response.url)
    if (response.status === 401 && !devMode && window.location.pathname !== '/login' &&
        url.pathname !== '/auth/basic/login' && !url.pathname.startsWith('/api/v1/auth/config')) {
      window.location.assign('/login')
    }
    return response
  },
})

// api keeps the old { get, post, put, del } surface but is backed by
// the generated openapi-fetch client.
// --- workspace selection ---
// The console acts inside one workspace at a time (personal by default).
// wsPath threads `?workspace=` through every list/create call.

const WS_KEY = 'calcside.workspace'

export interface Workspace { id: string; name: string; personal: boolean; role: string }

let currentWS: string | null = null
try { currentWS = window.localStorage.getItem(WS_KEY) } catch { currentWS = null }

export function currentWorkspace(): string | null { return currentWS }

export function setWorkspace(id: string | null): void {
  currentWS = id
  try {
    if (id) window.localStorage.setItem(WS_KEY, id)
    else window.localStorage.removeItem(WS_KEY)
  } catch { /* storage unavailable */ }
}

export function wsPath(path: string): string {
  if (!currentWS) return path
  const sep = path.includes('?') ? '&' : '?'
  return `${path}${sep}workspace=${encodeURIComponent(currentWS)}`
}

export async function workspaces(): Promise<Workspace[]> {
  const r = await api.get<{ workspaces?: Workspace[] }>('/api/v1/workspaces')
  return r.workspaces ?? []
}

export const api = {
  basicLogin: (username: string, password: string): Promise<{ ok: boolean }> => {
    const bytes = new TextEncoder().encode(`${username}:${password}`)
    const credentials = btoa(Array.from(bytes, (byte) => String.fromCharCode(byte)).join(''))
    return call('post', '/auth/basic/login', undefined, { Authorization: `Basic ${credentials}` })
  },
  exportFiles: async (instanceId: string, paths: string[], format: 'file' | 'zip'): Promise<ArtifactDownload> => {
    const { data, response } = await request('post', `/api/v1/instances/${encodeURIComponent(instanceId)}/artifacts/export`, { paths, format }, undefined, 'blob')
    const expected = format === 'zip' ? 'application/zip' : 'application/octet-stream'
    if (response.headers.get('Content-Type')?.split(';')[0] !== expected || !(data instanceof Blob)) {
      throw new ApiError(response.status, '', 'Unexpected artifact response')
    }
    const disposition = response.headers.get('Content-Disposition') || ''
    let filename = format === 'zip' ? 'artifacts.zip' : 'artifact.txt'
    const encoded = disposition.match(/filename\*=UTF-8''([^;]+)/i)?.[1]
    const plain = disposition.match(/filename="([^"\r\n]*)"/i)?.[1] || disposition.match(/filename=([^;\s]+)/i)?.[1]
    if (encoded) { try { filename = decodeURIComponent(encoded) } catch { filename = plain || filename } }
    else if (plain) filename = plain
    filename = Array.from(filename, (char) => char.charCodeAt(0) < 32 || char.charCodeAt(0) === 127 || char === '/' || char === '\\' ? '_' : char).join('')
    return { blob: data, filename, redacted: response.headers.get('X-Calcside-Redacted') === 'true', fileCount: Number(response.headers.get('X-Calcside-File-Count') || 0) }
  },
  get: async <T>(path: string): Promise<T> => call<T>('get', path),
  // uploadFile PUTs raw bytes to /files. XHR is used because fetch
  // cannot report request-body upload progress; bytes sent still is not
  // commit complete — callers show a finalizing state until the receipt.
  uploadFile: (instanceId: string, path: string, body: Blob, opts: { overwrite?: boolean; onProgress?: (loaded: number, total: number) => void; signal?: AbortSignal } = {}): Promise<{ path: string; size: number; mtime: number; replaced: boolean }> =>
    new Promise((resolve, reject) => {
      const xhr = new XMLHttpRequest()
      const q = `path=${encodeURIComponent(path)}&overwrite=${opts.overwrite ? 'true' : 'false'}`
      xhr.open('PUT', `/api/v1/instances/${encodeURIComponent(instanceId)}/files?${q}`)
      xhr.setRequestHeader('Content-Type', 'application/octet-stream')
      xhr.setRequestHeader('X-Requested-With', 'calcside')
      xhr.upload.onprogress = (e) => { if (e.lengthComputable) opts.onProgress?.(e.loaded, e.total) }
      xhr.onload = () => {
        try {
          const data = JSON.parse(xhr.responseText || '{}')
          if (xhr.status >= 200 && xhr.status < 300) resolve(data)
          else {
            const e = data?.error
            reject(new ApiError(xhr.status, (e?.code ?? '') as ApiErrorCode | '', e?.message ?? xhr.statusText))
          }
        } catch { reject(new ApiError(xhr.status, '', xhr.statusText || 'Upload failed')) }
      }
      xhr.onerror = () => reject(new ApiError(0, '', 'Network error during upload'))
      xhr.onabort = () => reject(new ApiError(0, '', 'Upload cancelled'))
      opts.signal?.addEventListener('abort', () => xhr.abort(), { once: true })
      xhr.send(body)
    }),
  // postRawJson sends an already-serialized JSON body verbatim —
  // high-precision numbers must never round-trip through JSON.parse.
  postRawJson: async <T>(path: string, raw: string): Promise<T> => rawJson<T>('POST', path, raw),
  putRawJson: async <T>(path: string, raw: string): Promise<T> => rawJson<T>('PUT', path, raw),
  post: async <T>(path: string, body?: unknown): Promise<T> => call<T>('post', path, body ?? {}),
  put: async <T>(path: string, body: unknown): Promise<T> => call<T>('put', path, body),
  patch: async <T>(path: string, body: unknown): Promise<T> => call<T>('patch', path, body),
  del: async <T>(path: string): Promise<T> => call<T>('delete', path),
}

async function rawJson<T>(method: 'POST' | 'PUT', path: string, raw: string): Promise<T> {
  const fn = (rawClient as any)[method].bind(rawClient)
  const { data, error, response } = await fn(path as never, {
    headers: { 'Content-Type': 'application/json' },
    body: raw,
    bodySerializer: (b: unknown) => b as string,
  })
  if (error !== undefined && error !== null) {
    const e = (error as any)?.error
    throw new ApiError(response.status, (e?.code ?? '') as ApiErrorCode | '', e?.message ?? response.statusText)
  }
  if (!response.ok) throw new ApiError(response.status, '', response.statusText)
  return data as T
}

async function call<T>(method: 'get' | 'post' | 'put' | 'patch' | 'delete', path: string, body?: unknown, headers?: HeadersInit): Promise<T> {
  return (await request(method, path, body, headers)).data as T
}

async function request(method: 'get' | 'post' | 'put' | 'patch' | 'delete', path: string, body?: unknown, headers?: HeadersInit, parseAs: 'json' | 'blob' = 'json'): Promise<{ data: unknown; response: Response }> {
  // openapi-fetch is typed per-path; the console's dynamic paths are
  // handled via the untyped escape hatch.
  const fn = (rawClient as any)[method.toUpperCase()].bind(rawClient)
  const { data, error, response } = await fn(path as never, { headers, parseAs, ...(method === 'get' || method === 'delete' ? {} : { body }) })
  if (error !== undefined && error !== null) {
    const e = (error as any)?.error
    throw new ApiError(response.status, (e?.code ?? '') as ApiErrorCode | '', e?.message ?? response.statusText)
  }
  if (!response.ok) {
    throw new ApiError(response.status, '', response.statusText)
  }
  return { data, response }
}

// --- EP-0016: runs & interrupts ---

export type Run = components['schemas']['Run']
export type RunDetail = components['schemas']['RunDetail']
export type RunStep = components['schemas']['RunStep']
export type Interrupt = components['schemas']['Interrupt']
export type InterruptWaiting = components['schemas']['InterruptWaiting']

export async function listRuns(appId: string): Promise<Run[]> {
	const r = await api.get<{ runs?: Run[] }>(`/api/v1/applications/${encodeURIComponent(appId)}/runs`)
	return r.runs ?? []
}

export async function getRun(id: string): Promise<{ run?: RunDetail; steps?: RunStep[]; interrupts?: Interrupt[] }> {
	return api.get(`/api/v1/runs/${encodeURIComponent(id)}`)
}

export async function cancelRun(id: string): Promise<{ runs?: Run[] }> {
	return api.post(`/api/v1/runs/${encodeURIComponent(id)}/cancel`)
}

export async function retryRun(id: string): Promise<{ runs?: Run[] }> {
	return api.post(`/api/v1/runs/${encodeURIComponent(id)}/retry`)
}

export async function listInterrupts(params: { status?: string; resumable?: boolean; before?: string; limit?: number } = {}): Promise<{ interrupts?: Interrupt[]; next_before?: string }> {
	const q = new URLSearchParams()
	if (params.status) q.set('status', params.status)
	if (params.resumable) q.set('resumable', 'true')
	if (params.before) q.set('before', params.before)
	if (params.limit) q.set('limit', String(params.limit))
	const s = q.toString()
	return api.get(wsPath(`/api/v1/interrupts${s ? '?' + s : ''}`))
}

export async function getInterrupt(id: string): Promise<{ interrupt?: Interrupt }> {
	return api.get(`/api/v1/interrupts/${encodeURIComponent(id)}`)
}

// resumeInterrupt posts the event body verbatim — events may carry
// high-precision numbers (C4/jsonPrecise convention). The same route
// accepts the interrupt's resume_token from external systems.
export async function resumeInterrupt(id: string, eventJson: string): Promise<{ status?: string }> {
	return api.postRawJson(`/api/v1/interrupts/${encodeURIComponent(id)}/resume`, eventJson)
}

export interface PostApplicationOptions {
  signal?: AbortSignal
  onStart?: () => void
  onItem?: (raw: string) => void
}

export class StreamProtocolError extends Error {}

const SSE_FRAME_LIMIT = 2 << 20

async function streamErrorBody(response: Response): Promise<never> {
  let message = response.statusText
  let code: ApiErrorCode | '' = ''
  try {
    const data = JSON.parse(await response.text())
    code = (data?.error?.code ?? '') as ApiErrorCode | ''
    if (data?.error?.message) message = data.error.message
  } catch { /* non-JSON error body */ }
  throw new ApiError(response.status, code, message)
}

export async function postApplication(path: string, raw: string, opts: PostApplicationOptions = {}): Promise<ApplicationRun> {
  const response = await fetch(wsPath(path), {
    method: 'POST',
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'calcside', 'Accept': 'application/json, text/event-stream' },
    body: raw,
    signal: opts.signal ?? null,
  })
  if (response.status === 401 && !devMode && window.location.pathname !== '/login') {
    window.location.assign('/login')
    throw new ApiError(401, 'unauthorized' as ApiErrorCode, 'Unauthorized')
  }
  const type = (response.headers.get('Content-Type') || '').split(';')[0].trim()
  if (type !== 'text/event-stream') {
    if (!response.ok) return streamErrorBody(response)
    const text = await response.text()
    try { return JSON.parse(text) as ApplicationRun }
    catch { throw new ApiError(response.status, '', 'Unexpected response') }
  }
  if (!response.ok) return streamErrorBody(response)
  if (!response.body) throw new ApiError(response.status, '', 'Missing stream body')
  return readApplicationStream(response.body, opts)
}

async function readApplicationStream(body: ReadableStream<Uint8Array>, opts: PostApplicationOptions): Promise<ApplicationRun> {
  const reader = body.getReader()
  const decoder = new TextDecoder('utf-8', { fatal: true })
  const encoder = new TextEncoder()
  let buf = ''
  let bomChecked = false
  let event = ''
  let dataLines: string[] | null = null
  let terminal: { event: string; data: string } | null = null
  const fail = (m: string): never => { throw new StreamProtocolError(m) }
  const dispatch = () => {
    const lines = dataLines
    const ev = event
    event = ''
    dataLines = null
    if (lines === null) return
    const payload = lines.join('\n')
    if (ev === 'item') {
      if (terminal) fail('item after terminal event')
      opts.onItem?.(payload)
      return
    }
    if (ev === 'done' || ev === 'error') {
      if (terminal) fail('duplicate terminal event')
      terminal = { event: ev, data: payload }
      return
    }
    fail(`unknown event ${ev}`)
  }
  const frames = () => {
    for (;;) {
      let end = -1, skip = 0
      for (const [sep, len] of [['\r\n\r\n', 4], ['\n\n', 2], ['\r\r', 2]] as const) {
        const i = buf.indexOf(sep)
        if (i >= 0 && (end < 0 || i < end)) { end = i; skip = len }
      }
      if (end < 0) return
      const raw = buf.slice(0, end)
      buf = buf.slice(end + skip)
      if (encoder.encode(raw).length > SSE_FRAME_LIMIT) fail('stream frame too large')
      for (const line of raw.split(/\r\n|\r|\n/)) {
        if (line === '') continue
        const c = line.indexOf(':')
        if (c === 0) continue
        const name = c < 0 ? line : line.slice(0, c)
        let value = c < 0 ? '' : line.slice(c + 1)
        if (value.startsWith(' ')) value = value.slice(1)
        if (name === 'event') event = value
        else if (name === 'data') (dataLines ??= []).push(value)
      }
      dispatch()
    }
  }
  const onAbort = () => { void reader.cancel().catch(() => { /* already closed */ }) }
  opts.signal?.addEventListener('abort', onAbort, { once: true })
  try {
    opts.onStart?.()
    for (;;) {
      const { done, value } = await reader.read()
      if (opts.signal?.aborted) fail('aborted')
      if (value) {
        let chunk: string
        try { chunk = decoder.decode(value, { stream: true }) }
        catch { throw new StreamProtocolError('invalid stream encoding') }
        if (!bomChecked) { bomChecked = true; buf += chunk.replace(/^\uFEFF/, '') }
        else buf += chunk
        frames()
        if (encoder.encode(buf).length > SSE_FRAME_LIMIT) fail('stream frame too large')
      }
      if (done) {
        try { decoder.decode() } catch { throw new StreamProtocolError('invalid stream encoding') }
        if (buf !== '' && buf !== '\n' && buf !== '\r\n' && buf !== '\r') fail('stream closed mid-frame')
        break
      }
    }
  } catch (e) {
    try { await reader.cancel() } catch { /* already closed */ }
    throw e
  } finally {
    opts.signal?.removeEventListener('abort', onAbort)
    reader.releaseLock()
  }
  const term = terminal as { event: string; data: string } | null
  if (term === null) throw new StreamProtocolError('stream ended without a terminal event')
  let out: unknown
  try { out = JSON.parse(term.data) }
  catch { fail('invalid terminal payload') }
  const res = out && typeof out === 'object' ? (out as Record<string, unknown>).result : null
  const r = res && typeof res === 'object' ? res as Record<string, unknown> : null
  if (!r || typeof r.id !== 'string' || r.streaming !== true) {
    throw new StreamProtocolError('invalid terminal payload')
  }
  if (term.event === 'done' && r.id === '') {
    throw new StreamProtocolError('invalid terminal payload')
  }
  for (const k of ['stream_items', 'stream_bytes']) {
    const v = r[k]
    if (v !== undefined && (!Number.isInteger(v) || (v as number) < 0)) fail('invalid terminal payload')
  }
  if (term.event === 'done') {
    if (r.error != null || r.waiting != null) fail('invalid terminal payload')
  } else {
    const e = r.error as Record<string, unknown> | null
    if (!e || typeof e !== 'object' || typeof e.code !== 'string' || e.code === '' || typeof e.message !== 'string' || e.message === '') {
      fail('invalid terminal payload')
    }
  }
  return out as ApplicationRun
}
