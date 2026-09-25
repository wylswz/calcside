// Closed-set enums mirroring internal/types (Go). A Go test asserts these
// arrays equal the Go All*() values — keep the one-line `as const` format.
export const INSTANCE_STATUSES = ['running', 'deleted', 'expired', 'lost'] as const
export type InstanceStatus = (typeof INSTANCE_STATUSES)[number]

export const EXEC_STATUSES = ['ok', 'error'] as const
export type ExecStatus = (typeof EXEC_STATUSES)[number]

export const EXEC_ERROR_TYPES = ['syntax', 'runtime', 'policy_denied', 'out_of_scope', 'timeout', 'step_limit', 'memory_limit'] as const
export type ExecErrorType = (typeof EXEC_ERROR_TYPES)[number]

export const DECISIONS = ['allow', 'deny'] as const
export type Decision = (typeof DECISIONS)[number]

export const PHASES = ['', 'before', 'after'] as const
export type Phase = (typeof PHASES)[number]

export const CAPABILITY_NAMES = ['fs', 'net', 'io'] as const
export type CapabilityName = (typeof CAPABILITY_NAMES)[number]

export const HTTP_METHODS = ['GET', 'HEAD', 'POST', 'PUT', 'PATCH', 'DELETE', 'OPTIONS'] as const
export type HttpMethod = (typeof HTTP_METHODS)[number]

export const SECRET_SOURCES = ['vault', 'inline'] as const
export type SecretSource = (typeof SECRET_SOURCES)[number]

export const API_ERROR_CODES = ['auth_failed', 'bad_capability', 'bad_policy', 'bad_request', 'bad_secret', 'bad_spec', 'conflict', 'csrf', 'forbidden', 'fs_error', 'internal', 'method_not_allowed', 'no_fs', 'not_found', 'not_running', 'secrets_disabled', 'too_large', 'too_many', 'unauthorized'] as const
export type ApiErrorCode = (typeof API_ERROR_CODES)[number]

export const FIELD_TYPES = ['int', 'bool', 'string', 'string_list', 'string_map'] as const
export type FieldType = (typeof FIELD_TYPES)[number]
