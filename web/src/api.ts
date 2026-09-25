// Typed API client for the calcside console.
import type {
  ApiErrorCode,
  CapabilityName,
  Decision,
  ExecErrorType,
  ExecStatus,
  InstanceStatus,
  Phase,
} from './enums'

export class ApiError extends Error {
  status: number
  code: ApiErrorCode | '' // '' if the server sent an unrecognized code
  constructor(status: number, code: ApiErrorCode | '', message: string) {
    super(message)
    this.status = status
    this.code = code
  }
}

async function req<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {}
  if (method !== 'GET' && method !== 'HEAD') {
    headers['X-Requested-With'] = 'calcside'
    if (body !== undefined) headers['Content-Type'] = 'application/json'
  }
  const res = await fetch(path, {
    method,
    headers,
    credentials: 'same-origin',
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  if (res.status === 401 && !path.startsWith('/api/v1/auth/config')) {
    window.location.assign('/login')
    throw new ApiError(401, 'unauthorized', 'redirecting to login')
  }
  const data = res.status === 204 ? null : await res.json().catch(() => null)
  if (!res.ok) {
    const e = data?.error
    const code = (e?.code ?? '') as ApiErrorCode | ''
    throw new ApiError(res.status, code, e?.message ?? res.statusText)
  }
  return data as T
}

export const api = {
  get: <T>(path: string) => req<T>('GET', path),
  post: <T>(path: string, body?: unknown) => req<T>('POST', path, body ?? {}),
  put: <T>(path: string, body: unknown) => req<T>('PUT', path, body),
  del: <T>(path: string) => req<T>('DELETE', path),
}

// --- types mirroring the backend ---

export interface User {
  id: string
  email: string
  name: string
}

export interface Instance {
  id: string
  user_id: string
  spec: Record<string, any>
  labels: Record<string, string>
  status: InstanceStatus
  created_at: string
  last_active_at: string
  expires_at: string
  ended_at?: string
}

export interface ExecError {
  type: ExecErrorType
  message: string
  backtrace?: string
}

export interface ExecResult {
  exec_id: string
  output: string
  error: ExecError | null
  duration_ms: number
  steps: number
}

export interface Execution {
  id: string
  status: ExecStatus
  error_type?: ExecErrorType
  duration_ms: number
  steps: number
  output_bytes: number
  created_at: string
  code_snippet: string
}

export interface FileEntry {
  name: string
  path: string
  is_dir: boolean
  size: number
  mtime: number
}

export interface AuditEvent {
  id: string
  ts: string
  instance_id: string
  exec_id: string
  capability: CapabilityName
  op: string
  args: string
  phase: Phase
  decision: Decision
  reason: string
  error: string
  duration_ms: number
}

export interface Policy {
  id: string
  name: string
  rego: string
  enabled: boolean
  created_at: string
  updated_at: string
}

export interface APIKey {
  id: string
  name: string
  prefix: string
  created_at: string
  last_used_at?: string
  expires_at?: string
  revoked_at?: string
}

export interface AuthConfig {
  google: boolean
  dev_login: boolean
  secrets: boolean
}

export interface Secret {
  id: string
  name: string
  allowed_domains: string[]
  created_at: string
  updated_at: string
}
