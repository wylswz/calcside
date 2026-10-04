// Package types defines the closed-set string enums shared across the
// codebase. Values that arrive over JSON/query implement UnmarshalText so
// invalid input fails at decode time; wire and DB formats are unchanged.
package types

import (
	"fmt"
	"strings"
)

func valid[T ~string](v T, all []T) bool {
	for _, a := range all {
		if v == a {
			return true
		}
	}
	return false
}

func invalid[T ~string](kind string, v T) error {
	return fmt.Errorf("invalid %s %q", kind, string(v))
}

// InstanceStatus is the lifecycle state of an instance.
type InstanceStatus string

const (
	InstanceRunning InstanceStatus = "running"
	InstanceDeleted InstanceStatus = "deleted"
	InstanceExpired InstanceStatus = "expired"
	InstanceLost    InstanceStatus = "lost"
)

func AllInstanceStatuses() []InstanceStatus {
	return []InstanceStatus{InstanceRunning, InstanceDeleted, InstanceExpired, InstanceLost}
}

func (s InstanceStatus) Valid() bool { return valid(s, AllInstanceStatuses()) }

func (s InstanceStatus) MarshalText() ([]byte, error) { return []byte(s), nil }

func (s *InstanceStatus) UnmarshalText(b []byte) error {
	v := InstanceStatus(b)
	if !v.Valid() {
		return invalid("instance status", v)
	}
	*s = v
	return nil
}

// ExecStatus is the outcome of an execution.
type ExecStatus string

const (
	ExecOK    ExecStatus = "ok"
	ExecError ExecStatus = "error"
)

func AllExecStatuses() []ExecStatus { return []ExecStatus{ExecOK, ExecError} }

func (s ExecStatus) Valid() bool { return valid(s, AllExecStatuses()) }

func (s ExecStatus) MarshalText() ([]byte, error) { return []byte(s), nil }

func (s *ExecStatus) UnmarshalText(b []byte) error {
	v := ExecStatus(b)
	if !v.Valid() {
		return invalid("exec status", v)
	}
	*s = v
	return nil
}

// ExecErrorType classifies a failed execution. The empty value means the
// execution succeeded (no error); it serializes as "" and is omitted via
// omitempty.
type ExecErrorType string

const (
	ErrSyntax       ExecErrorType = "syntax"
	ErrRuntime      ExecErrorType = "runtime"
	ErrPolicyDenied ExecErrorType = "policy_denied"
	ErrOutOfScope   ExecErrorType = "out_of_scope"
	ErrTimeout      ExecErrorType = "timeout"
	ErrStepLimit    ExecErrorType = "step_limit"
	ErrMemoryLimit  ExecErrorType = "memory_limit"
)

func AllExecErrorTypes() []ExecErrorType {
	return []ExecErrorType{ErrSyntax, ErrRuntime, ErrPolicyDenied, ErrOutOfScope, ErrTimeout, ErrStepLimit, ErrMemoryLimit}
}

// Valid accepts the empty string (no error) plus every classified type.
func (t ExecErrorType) Valid() bool { return t == "" || valid(t, AllExecErrorTypes()) }

func (t ExecErrorType) MarshalText() ([]byte, error) { return []byte(t), nil }

func (t *ExecErrorType) UnmarshalText(b []byte) error {
	v := ExecErrorType(b)
	if !v.Valid() {
		return invalid("exec error type", v)
	}
	*t = v
	return nil
}

// Decision is the gate/audit outcome of a capability call.
type Decision string

const (
	DecisionAllow Decision = "allow"
	DecisionDeny  Decision = "deny"
)

func AllDecisions() []Decision { return []Decision{DecisionAllow, DecisionDeny} }

func (d Decision) Valid() bool { return valid(d, AllDecisions()) }

func (d Decision) MarshalText() ([]byte, error) { return []byte(d), nil }

func (d *Decision) UnmarshalText(b []byte) error {
	v := Decision(b)
	if !v.Valid() {
		return invalid("decision", v)
	}
	*d = v
	return nil
}

// Phase marks where a deny/error happened. PhaseNone ("") means the
// record carries no phase (runtime error / out of scope).
type Phase string

const (
	PhaseNone   Phase = ""
	PhaseBefore Phase = "before"
	PhaseAfter  Phase = "after"
)

func AllPhases() []Phase { return []Phase{PhaseBefore, PhaseAfter} }

// Valid accepts PhaseNone plus the hook phases.
func (p Phase) Valid() bool { return p == PhaseNone || valid(p, AllPhases()) }

func (p Phase) MarshalText() ([]byte, error) { return []byte(p), nil }

func (p *Phase) UnmarshalText(b []byte) error {
	v := Phase(b)
	if !v.Valid() {
		return invalid("phase", v)
	}
	*p = v
	return nil
}

// CapabilityName identifies a capability binding.
type CapabilityName string

const (
	CapFS  CapabilityName = "fs"
	CapNet CapabilityName = "net"
	CapIO  CapabilityName = "io"
	CapExt CapabilityName = "ext"
)

func AllCapabilityNames() []CapabilityName {
	return []CapabilityName{CapFS, CapNet, CapIO, CapExt}
}

func (n CapabilityName) Valid() bool { return valid(n, AllCapabilityNames()) }

// Op names one capability operation (e.g. fs.read, net.get). Concrete op
// consts are declared per capability package; there is no fixed global
// list, so Op has no Valid/All — it is a typed string, not a closed set.
type Op string

// HTTPMethod is an HTTP method allowed in net requests.
type HTTPMethod string

const (
	MethodGet     HTTPMethod = "GET"
	MethodHead    HTTPMethod = "HEAD"
	MethodPost    HTTPMethod = "POST"
	MethodPut     HTTPMethod = "PUT"
	MethodPatch   HTTPMethod = "PATCH"
	MethodDelete  HTTPMethod = "DELETE"
	MethodOptions HTTPMethod = "OPTIONS"
)

func AllHTTPMethods() []HTTPMethod {
	return []HTTPMethod{MethodGet, MethodHead, MethodPost, MethodPut, MethodPatch, MethodDelete, MethodOptions}
}

func (m HTTPMethod) Valid() bool { return valid(m, AllHTTPMethods()) }

// ParseHTTPMethod normalizes case then validates ("get" -> MethodGet).
func ParseHTTPMethod(s string) (HTTPMethod, error) {
	m := HTTPMethod(strings.ToUpper(s))
	if !m.Valid() {
		return "", invalid("HTTP method", HTTPMethod(s))
	}
	return m, nil
}

// UnmarshalText uppercases then validates.
func (m HTTPMethod) MarshalText() ([]byte, error) { return []byte(m), nil }

func (m *HTTPMethod) UnmarshalText(b []byte) error {
	v, err := ParseHTTPMethod(string(b))
	if err != nil {
		return err
	}
	*m = v
	return nil
}

// FieldType is the documented type of a capability config field.
type FieldType string

const (
	FieldInt        FieldType = "int"
	FieldBool       FieldType = "bool"
	FieldString     FieldType = "string"
	FieldStringList FieldType = "string_list"
	FieldStringMap  FieldType = "string_map"
)

func AllFieldTypes() []FieldType {
	return []FieldType{FieldInt, FieldBool, FieldString, FieldStringList, FieldStringMap}
}

func (t FieldType) Valid() bool { return valid(t, AllFieldTypes()) }

func (t FieldType) MarshalText() ([]byte, error) { return []byte(t), nil }

// SecretSource is where an instance secret's value comes from.
type SecretSource string

const (
	SecretVault  SecretSource = "vault"
	SecretInline SecretSource = "inline"
)

func AllSecretSources() []SecretSource { return []SecretSource{SecretVault, SecretInline} }

func (s SecretSource) Valid() bool { return valid(s, AllSecretSources()) }

func (s SecretSource) MarshalText() ([]byte, error) { return []byte(s), nil }

func (s *SecretSource) UnmarshalText(b []byte) error {
	v := SecretSource(b)
	if !v.Valid() {
		return invalid("secret source", v)
	}
	*s = v
	return nil
}

// APIErrorCode is the machine-readable code in {"error":{"code":...}}.
type APIErrorCode string

const (
	ErrCodeAuthFailed       APIErrorCode = "auth_failed"
	ErrCodeBadCapability    APIErrorCode = "bad_capability"
	ErrCodeBadPolicy        APIErrorCode = "bad_policy"
	ErrCodeBadRequest       APIErrorCode = "bad_request"
	ErrCodeBadSecret        APIErrorCode = "bad_secret"
	ErrCodeBadSpec          APIErrorCode = "bad_spec"
	ErrCodeConflict         APIErrorCode = "conflict"
	ErrCodeCSRF             APIErrorCode = "csrf"
	ErrCodeForbidden        APIErrorCode = "forbidden"
	ErrCodeFSError          APIErrorCode = "fs_error"
	ErrCodeInternal         APIErrorCode = "internal"
	ErrCodeMethodNotAllowed APIErrorCode = "method_not_allowed"
	ErrCodeNoFS             APIErrorCode = "no_fs"
	ErrCodeNotFound         APIErrorCode = "not_found"
	ErrCodeNotRunning       APIErrorCode = "not_running"
	ErrCodeSecretsDisabled  APIErrorCode = "secrets_disabled"
	ErrCodeTooLarge         APIErrorCode = "too_large"
	ErrCodeTooMany          APIErrorCode = "too_many"
	ErrCodeUnauthorized     APIErrorCode = "unauthorized"
)

func AllAPIErrorCodes() []APIErrorCode {
	return []APIErrorCode{
		ErrCodeAuthFailed, ErrCodeBadCapability, ErrCodeBadPolicy, ErrCodeBadRequest,
		ErrCodeBadSecret, ErrCodeBadSpec, ErrCodeConflict, ErrCodeCSRF, ErrCodeForbidden,
		ErrCodeFSError, ErrCodeInternal, ErrCodeMethodNotAllowed, ErrCodeNoFS,
		ErrCodeNotFound, ErrCodeNotRunning, ErrCodeSecretsDisabled, ErrCodeTooLarge,
		ErrCodeTooMany, ErrCodeUnauthorized,
	}
}

func (c APIErrorCode) Valid() bool { return valid(c, AllAPIErrorCodes()) }

func (c APIErrorCode) MarshalText() ([]byte, error) { return []byte(c), nil }

func (c *APIErrorCode) UnmarshalText(b []byte) error {
	v := APIErrorCode(b)
	if !v.Valid() {
		return invalid("API error code", v)
	}
	*c = v
	return nil
}

// StoreDriver names a registered store backend.
type StoreDriver string

const (
	DriverSQLite   StoreDriver = "sqlite"
	DriverPostgres StoreDriver = "postgres"
)

func AllStoreDrivers() []StoreDriver { return []StoreDriver{DriverSQLite, DriverPostgres} }

func (d StoreDriver) Valid() bool { return valid(d, AllStoreDrivers()) }

func (d StoreDriver) MarshalText() ([]byte, error) { return []byte(d), nil }

func (d *StoreDriver) UnmarshalText(b []byte) error {
	v := StoreDriver(b)
	if !v.Valid() {
		return invalid("store driver", v)
	}
	*d = v
	return nil
}

// AuthKind is how a request was authenticated.
type AuthKind string

const (
	AuthSession   AuthKind = "session"
	AuthAPIKey    AuthKind = "api_key"
	AuthAnonymous AuthKind = "anonymous"
)

func AllAuthKinds() []AuthKind { return []AuthKind{AuthSession, AuthAPIKey, AuthAnonymous} }

func (k AuthKind) Valid() bool { return valid(k, AllAuthKinds()) }

func (k AuthKind) MarshalText() ([]byte, error) { return []byte(k), nil }

// OutputFormat is the csctl -o flag value.
type OutputFormat string

const (
	FormatText OutputFormat = ""
	FormatJSON OutputFormat = "json"
)

func AllOutputFormats() []OutputFormat { return []OutputFormat{FormatText, FormatJSON} }

func (f OutputFormat) Valid() bool { return f == FormatText || f == FormatJSON }

func (f OutputFormat) MarshalText() ([]byte, error) { return []byte(f), nil }

func (f *OutputFormat) UnmarshalText(b []byte) error {
	v := OutputFormat(b)
	if !v.Valid() {
		return invalid("output format", v)
	}
	*f = v
	return nil
}
