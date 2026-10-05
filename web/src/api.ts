// API client for the calcside console, generated from api/openapi.yaml:
// `src/api/schema.ts` is produced by openapi-typescript (`pnpm gen`) —
// never edit it by hand. Request/response shapes come from the schema.
import createClient from 'openapi-fetch'
import type { paths, components } from './api/schema'

// --- generated types (aliases keep page imports tidy) ---

export type User = components['schemas']['User']
export type Instance = components['schemas']['Instance']
export type InstanceSpec = components['schemas']['InstanceSpec']
export type ExecResult = components['schemas']['ExecResult']
export type ExecError = components['schemas']['ExecError']
export type Execution = components['schemas']['Execution']
export type FileEntry = components['schemas']['FileEntry']
export type InstanceInspect = components['schemas']['InstanceInspect']
export type ResourceUsages = components['schemas']['ResourceUsages']
export type CompletionSymbol = components['schemas']['CompletionSymbol']
export type CompletionContext = components['schemas']['CompletionContext']
export type EditorMetadata = components['schemas']['EditorMetadata']
export type AuditEvent = components['schemas']['AuditEvent']
export type Policy = components['schemas']['Policy']
export type APIKey = components['schemas']['APIKey']
export type AuthConfig = components['schemas']['AuthConfig']
export type Secret = components['schemas']['Secret']
export type ExtensionCatalog = components['schemas']['ExtensionCatalog']
export type ExtensionInfo = components['schemas']['ExtensionInfo']
export type ExtConfigField = components['schemas']['ExtConfigField']
export type ArtifactConfig = components['schemas']['ArtifactConfig']
export type ArtifactPreview = components['schemas']['ArtifactPreview']

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

const raw = createClient<paths>({ credentials: 'same-origin' })

raw.use({
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
  post: async <T>(path: string, body?: unknown): Promise<T> => call<T>('post', path, body ?? {}),
  put: async <T>(path: string, body: unknown): Promise<T> => call<T>('put', path, body),
  del: async <T>(path: string): Promise<T> => call<T>('delete', path),
}

async function call<T>(method: 'get' | 'post' | 'put' | 'delete', path: string, body?: unknown, headers?: HeadersInit): Promise<T> {
  return (await request(method, path, body, headers)).data as T
}

async function request(method: 'get' | 'post' | 'put' | 'delete', path: string, body?: unknown, headers?: HeadersInit, parseAs: 'json' | 'blob' = 'json'): Promise<{ data: unknown; response: Response }> {
  // openapi-fetch is typed per-path; the console's dynamic paths are
  // handled via the untyped escape hatch.
  const fn = (raw as any)[method.toUpperCase()].bind(raw)
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
