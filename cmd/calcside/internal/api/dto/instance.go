package dto

import (
	"encoding/json"
	"time"

	"calcside/cmd/calcside/internal/service/sandbox"
	"calcside/internal/completion"
	"calcside/internal/runtime"
	"calcside/internal/store"
	"calcside/internal/types"
)

// Instance mirrors the wire shape of a store.Instance; Spec passes
// through as raw JSON (keys like "env":null must survive verbatim).
type Instance struct {
	ID           string               `json:"id"`
	UserID       string               `json:"user_id"`
	WorkspaceID  string               `json:"workspace_id"`
	Activity     *ExecActivity        `json:"activity,omitempty"`
	Spec         json.RawMessage      `json:"spec"`
	Labels       map[string]string    `json:"labels"`
	Status       types.InstanceStatus `json:"status"`
	Incarnation  int64                `json:"incarnation"`
	RestoredAt   *time.Time           `json:"restored_at,omitempty"`
	NotebookRev  int64                `json:"notebook_revision"`
	Drift        int64                `json:"drift"`
	CreatedAt    time.Time            `json:"created_at"`
	LastActiveAt time.Time            `json:"last_active_at"`
	ExpiresAt    time.Time            `json:"expires_at"`
	EndedAt      *time.Time           `json:"ended_at,omitempty"`
}

func NewInstance(in *store.Instance) Instance {
	return Instance{
		ID: in.ID, UserID: in.UserID, WorkspaceID: in.WorkspaceID, Spec: in.Spec, Labels: in.Labels,
		Status: in.Status, Incarnation: in.Incarnation, RestoredAt: in.RestoredAt,
		NotebookRev: in.NotebookRev, Drift: in.Drift,
		CreatedAt: in.CreatedAt, LastActiveAt: in.LastActiveAt,
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

// ExecActivity is the per-instance run summary on list responses.
type ExecActivity struct {
	Runs       int        `json:"runs"`
	DeniedRuns int        `json:"denied_runs"`
	FailedRuns int        `json:"failed_runs"`
	LastRunAt  *time.Time `json:"last_run_at,omitempty"`
	LastStatus string     `json:"last_status,omitempty"`
}

// SharedInstance is an instance reached via a share, with the share
// role and owning workspace name.
type SharedInstance struct {
	Instance      `json:",inline"`
	Role          string `json:"role"`
	WorkspaceName string `json:"workspace_name"`
}

type SharedInstancesEnvelope struct {
	Instances []SharedInstance `json:"instances"`
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
	Via         types.AuthKind      `json:"via,omitempty"`
	CreatedAt   time.Time           `json:"created_at"`
}

func NewExecution(e *store.Execution) Execution {
	return Execution{
		ID: e.ID, InstanceID: e.InstanceID, UserID: e.UserID,
		CodeSHA256: e.CodeSHA256, CodeSnippet: e.CodeSnippet,
		Status: e.Status, ErrorType: e.ErrorType, DurationMs: e.DurationMs,
		Steps: e.Steps, OutputBytes: e.OutputBytes, Via: e.Via, CreatedAt: e.CreatedAt,
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
// detail-only fields (full code, output, error).
type ExecutionDetail struct {
	Execution       Execution  `json:"execution"`
	Code            string     `json:"code"`
	Output          string     `json:"output"`
	OutputTruncated bool       `json:"output_truncated"`
	Error           *ExecError `json:"error"`
}

// NewExecutionDetail builds the detail body; Error is null for runs that
// completed without an error (status ok).
func NewExecutionDetail(e *store.Execution) ExecutionDetail {
	d := ExecutionDetail{
		Execution: NewExecution(e), Code: e.Code,
		Output: e.Output, OutputTruncated: e.OutputTruncated,
	}
	if e.Status != types.ExecOK {
		d.Error = &ExecError{Type: e.ErrorType, Message: e.ErrorMessage, Backtrace: e.ErrorBacktrace}
	}
	return d
}

// ExecResult is the exec response body; Error is a pointer without
// omitempty so success renders "error": null. The notebook fields are
// additive (EP-0022): revision/drift always present on new servers,
// section fields only for sectioned execs.
type ExecResult struct {
	ExecID           string             `json:"exec_id"`
	Output           string             `json:"output"`
	Error            *ExecError         `json:"error"`
	DurationMs       int64              `json:"duration_ms"`
	Steps            uint64             `json:"steps"`
	NotebookRevision int64              `json:"notebook_revision,omitempty"`
	Drift            int64              `json:"drift,omitempty"`
	Section          string             `json:"section,omitempty"`
	SectionState     string             `json:"section_state,omitempty"`
	Upstream         []RunSectionResult `json:"upstream,omitempty"`
}

type ExecError struct {
	Type      types.ExecErrorType `json:"type"`
	Message   string              `json:"message"`
	Backtrace string              `json:"backtrace,omitempty"`
}

func NewExecResult(r *sandbox.ExecOutcome) ExecResult {
	res := ExecResult{
		ExecID: r.View.ExecID, Output: r.View.Output, DurationMs: r.View.DurationMs,
		Steps: r.View.Steps, NotebookRevision: r.Revision, Drift: r.Drift,
		Section: r.Section, SectionState: r.State,
	}
	if len(r.Upstream) > 0 {
		res.Upstream = make([]RunSectionResult, len(r.Upstream))
		for i, u := range r.Upstream {
			res.Upstream[i] = RunSectionResult{
				Name: u.Name, Status: u.Status, ExecID: u.ExecID,
				Error: u.Error, SkippedReason: u.SkippedReason,
				Reason: u.Reason, DurationMs: u.DurationMs,
			}
		}
	}
	if r.View.Error != nil {
		res.Error = &ExecError{Type: r.View.Error.Type, Message: r.View.Error.Message, Backtrace: r.View.Error.Backtrace}
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
	Exec        string `json:"exec"`
	Notebook    string `json:"notebook,omitempty"`
	ReadSection string `json:"read_section,omitempty"`
	RunSections string `json:"run_sections,omitempty"`
	AddSection  string `json:"add_section,omitempty"`
	ListFiles   string `json:"list_files,omitempty"`
	ReadFile    string `json:"read_file,omitempty"`
}

func NewInstancePrompt(v *sandbox.PromptView) InstancePrompt {
	return InstancePrompt{
		InstanceID: v.InstanceID, Prompt: v.Prompt, Capabilities: v.Capabilities,
		Tools: PromptTools{
			Exec:        v.Tools["exec"],
			Notebook:    v.Tools["notebook"],
			ReadSection: v.Tools["read_section"],
			RunSections: v.Tools["run_sections"],
			AddSection:  v.Tools["add_section"],
			ListFiles:   v.Tools["list_files"],
			ReadFile:    v.Tools["read_file"],
		},
	}
}

// InstanceInspect is the /inspect response body: variable name to its
// Starlark repr (secret-scrubbed on the node), plus the instance's
// cgroup accounting when it has a group of its own.
type InstanceInspect struct {
	Variables map[string]string `json:"variables"`
	Resources *ResourceUsages   `json:"resources,omitempty"`
}

type ResourceUsages struct {
	MemoryUsage uint64 `json:"memory_usage"`
	MemoryPeak  uint64 `json:"memory_peak"`
	MemoryMax   uint64 `json:"memory_max"`
}

func NewInstanceInspect(v *sandbox.InspectView) InstanceInspect {
	out := InstanceInspect{Variables: v.Variables}
	if r := v.Resources; r != nil {
		out.Resources = &ResourceUsages{MemoryUsage: r.MemoryUsage, MemoryPeak: r.MemoryPeak, MemoryMax: r.MemoryMax}
	}
	return out
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

// WriteResult is the upload receipt: it reports where the bytes landed
// without echoing them.
type WriteResult struct {
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Mtime    int64  `json:"mtime"`
	Replaced bool   `json:"replaced"`
}

type CompletionContext = completion.Context
