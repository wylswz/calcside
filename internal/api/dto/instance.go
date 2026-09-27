package dto

import (
	"encoding/json"
	"time"

	"calcside/internal/runtime"
	"calcside/internal/service/sandbox"
	"calcside/internal/store"
	"calcside/internal/types"
)

// Instance mirrors the wire shape of a store.Instance; Spec passes
// through as raw JSON (keys like "env":null must survive verbatim).
type Instance struct {
	ID           string               `json:"id"`
	UserID       string               `json:"user_id"`
	Spec         json.RawMessage      `json:"spec"`
	Labels       map[string]string    `json:"labels"`
	Status       types.InstanceStatus `json:"status"`
	CreatedAt    time.Time            `json:"created_at"`
	LastActiveAt time.Time            `json:"last_active_at"`
	ExpiresAt    time.Time            `json:"expires_at"`
	EndedAt      *time.Time           `json:"ended_at,omitempty"`
}

func NewInstance(in *store.Instance) Instance {
	return Instance{
		ID: in.ID, UserID: in.UserID, Spec: in.Spec, Labels: in.Labels,
		Status: in.Status, CreatedAt: in.CreatedAt, LastActiveAt: in.LastActiveAt,
		ExpiresAt: in.ExpiresAt, EndedAt: in.EndedAt,
	}
}

// NewInstances maps a slice; empty input yields [], never null.
func NewInstances(lst []*store.Instance) []Instance {
	out := make([]Instance, len(lst))
	for i, in := range lst {
		out[i] = NewInstance(in)
	}
	return out
}

type InstanceEnvelope struct {
	Instance Instance `json:"instance"`
}

type InstancesEnvelope struct {
	Instances []Instance `json:"instances"`
}

// Execution has no Code field: full code only exists on the detail
// endpoint (ExecutionDetail).
type Execution struct {
	ID          string              `json:"id"`
	InstanceID  string              `json:"instance_id"`
	UserID      string              `json:"user_id"`
	CodeSHA256  string              `json:"code_sha256"`
	CodeSnippet string              `json:"code_snippet"`
	Status      types.ExecStatus    `json:"status"`
	ErrorType   types.ExecErrorType `json:"error_type,omitempty"`
	DurationMs  int64               `json:"duration_ms"`
	Steps       uint64              `json:"steps"`
	OutputBytes int64               `json:"output_bytes"`
	CreatedAt   time.Time           `json:"created_at"`
}

func NewExecution(e *store.Execution) Execution {
	return Execution{
		ID: e.ID, InstanceID: e.InstanceID, UserID: e.UserID,
		CodeSHA256: e.CodeSHA256, CodeSnippet: e.CodeSnippet,
		Status: e.Status, ErrorType: e.ErrorType, DurationMs: e.DurationMs,
		Steps: e.Steps, OutputBytes: e.OutputBytes, CreatedAt: e.CreatedAt,
	}
}

func NewExecutions(lst []*store.Execution) []Execution {
	out := make([]Execution, len(lst))
	for i, e := range lst {
		out[i] = NewExecution(e)
	}
	return out
}

type ExecutionsEnvelope struct {
	Executions []Execution `json:"executions"`
}

// ExecutionDetail is the GET /executions/{id} body: the record plus the
// full code.
type ExecutionDetail struct {
	Execution Execution `json:"execution"`
	Code      string    `json:"code"`
}

// ExecResult is the exec response body; Error is a pointer without
// omitempty so success renders "error": null.
type ExecResult struct {
	ExecID     string     `json:"exec_id"`
	Output     string     `json:"output"`
	Error      *ExecError `json:"error"`
	DurationMs int64      `json:"duration_ms"`
	Steps      uint64     `json:"steps"`
}

type ExecError struct {
	Type      types.ExecErrorType `json:"type"`
	Message   string              `json:"message"`
	Backtrace string              `json:"backtrace,omitempty"`
}

func NewExecResult(r *sandbox.ExecView) ExecResult {
	res := ExecResult{
		ExecID: r.ExecID, Output: r.Output, DurationMs: r.DurationMs, Steps: r.Steps,
	}
	if r.Error != nil {
		res.Error = &ExecError{Type: r.Error.Type, Message: r.Error.Message, Backtrace: r.Error.Backtrace}
	}
	return res
}

// InstancePrompt is the rendered agent prompt plus the tool name map.
type InstancePrompt struct {
	InstanceID   string                 `json:"instance_id"`
	Prompt       string                 `json:"prompt"`
	Capabilities []types.CapabilityName `json:"capabilities"`
	Tools        PromptTools            `json:"tools"`
}

type PromptTools struct {
	Exec      string `json:"exec"`
	ListFiles string `json:"list_files"`
	ReadFile  string `json:"read_file"`
}

func NewInstancePrompt(v *sandbox.PromptView) InstancePrompt {
	return InstancePrompt{
		InstanceID: v.InstanceID, Prompt: v.Prompt, Capabilities: v.Capabilities,
		Tools: PromptTools{
			Exec:      v.Tools["exec"],
			ListFiles: v.Tools["list_files"],
			ReadFile:  v.Tools["read_file"],
		},
	}
}

// InstanceInspect is the /inspect response body: variable name to its
// Starlark repr (secret-scrubbed on the node).
type InstanceInspect struct {
	Variables map[string]string `json:"variables"`
}

// FileEntry mirrors the runtime contract's entry wire shape.
type FileEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
	Mtime int64  `json:"mtime"`
}

func NewFileEntry(e runtime.FileEntry) FileEntry {
	return FileEntry{Name: e.Name, Path: e.Path, IsDir: e.IsDir, Size: e.Size, Mtime: e.Mtime}
}

func NewFileEntries(lst []runtime.FileEntry) []FileEntry {
	out := make([]FileEntry, len(lst))
	for i, e := range lst {
		out[i] = NewFileEntry(e)
	}
	return out
}

// FilesDir / FilesFile are the two distinct /files response shapes:
// a directory lists entries; a file returns path + redacted content.
type FilesDir struct {
	Entries []FileEntry `json:"entries"`
}

type FilesFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}
