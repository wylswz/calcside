package dto

import (
	"time"

	"calcside/cmd/calcside/internal/service/sandbox"
	"calcside/internal/types"
)

// Notebook wire DTOs for the EP-0022 instance notebook endpoints.
// SectionMeta mirrors the service shape; the detail variant adds code
// and, on request, the last run's output/error.

type NotebookSectionMeta struct {
	Name            string     `json:"name"`
	Position        int64      `json:"position"`
	Description     string     `json:"description"`
	State           string     `json:"state"`
	Rerun           string     `json:"rerun"`
	Defines         []string   `json:"defines"`
	Uses            []string   `json:"uses"`
	Effects         []string   `json:"effects"`
	DependsOn       []string   `json:"depends_on"`
	LastExecID      string     `json:"last_exec_id,omitempty"`
	LastIncarnation int64      `json:"last_incarnation"`
	LastStatus      string     `json:"last_status,omitempty"`
	LastRunAt       *time.Time `json:"last_run_at,omitempty"`
	OnceRan         bool       `json:"once_ran"`
}

func NewNotebookSectionMeta(m sandbox.NotebookSectionMeta) NotebookSectionMeta {
	return NotebookSectionMeta{
		Name: m.Name, Position: m.Position, Description: m.Description,
		State: m.State, Rerun: m.Rerun, Defines: m.Defines, Uses: m.Uses,
		Effects: m.Effects, DependsOn: m.DependsOn, LastExecID: m.LastExecID,
		LastIncarnation: m.LastIncarnation, LastStatus: m.LastStatus,
		LastRunAt: m.LastRunAt, OnceRan: m.OnceRan,
	}
}

func NewNotebookSectionMetas(in []sandbox.NotebookSectionMeta) []NotebookSectionMeta {
	out := make([]NotebookSectionMeta, len(in))
	for i, m := range in {
		out[i] = NewNotebookSectionMeta(m)
	}
	return out
}

// Notebook is the outline view: instance facts plus per-section
// metadata, the rendered outline text, and computed next-step guidance.
type Notebook struct {
	InstanceID    string                `json:"instance_id"`
	Status        types.InstanceStatus  `json:"status"`
	Incarnation   int64                 `json:"incarnation"`
	RestoredAt    *time.Time            `json:"restored_at,omitempty"`
	RestoredEmpty bool                  `json:"restored_empty"`
	Revision      int64                 `json:"revision"`
	Drift         int64                 `json:"drift"`
	Sections      []NotebookSectionMeta `json:"sections"`
	Outline       string                `json:"outline"`
	Next          []string              `json:"next"`
}

type NotebookEnvelope struct {
	Notebook Notebook `json:"notebook"`
}

func NewNotebook(v *sandbox.NotebookView) Notebook {
	return Notebook{
		InstanceID: v.InstanceID, Status: v.Status, Incarnation: v.Incarnation,
		RestoredAt: v.RestoredAt, RestoredEmpty: v.RestoredEmpty,
		Revision: v.Revision, Drift: v.Drift,
		Sections: NewNotebookSectionMetas(v.Sections), Outline: v.Outline,
		Next: v.Next,
	}
}

// NotebookSection is one section with code; Output/Error are only
// populated when the caller asked for include_output.
type NotebookSection struct {
	Section         NotebookSectionMeta `json:"section"`
	Code            string              `json:"code"`
	Output          string              `json:"output,omitempty"`
	Error           string              `json:"error,omitempty"`
	OutputTruncated bool                `json:"output_truncated,omitempty"`
}

type NotebookSectionEnvelope struct {
	Section NotebookSection `json:"section"`
}

func NewNotebookSection(d *sandbox.SectionDetail) NotebookSection {
	return NotebookSection{
		Section: NewNotebookSectionMeta(d.Section), Code: d.Code,
		Output: d.Output, Error: d.Error, OutputTruncated: d.OutputTrunc,
	}
}

// SaveSectionResult is the PUT response: the saved section's derived
// metadata plus the new notebook revision.
type SaveSectionResultEnvelope struct {
	Section  NotebookSectionMeta `json:"section"`
	Revision int64               `json:"revision"`
}

func NewSaveSectionResult(m sandbox.NotebookSectionMeta, revision int64) SaveSectionResultEnvelope {
	return SaveSectionResultEnvelope{Section: NewNotebookSectionMeta(m), Revision: revision}
}

// DeleteSectionResult reports dependents plus the new revision.
type DeleteSectionResult struct {
	Affected []string `json:"affected"`
	Revision int64    `json:"revision"`
}

// RunSectionsResponse is the POST /notebook/run body.
type RunSectionResult struct {
	Name          string `json:"name"`
	Status        string `json:"status"`
	ExecID        string `json:"exec_id,omitempty"`
	Error         string `json:"error,omitempty"`
	SkippedReason string `json:"skipped_reason,omitempty"`
	DurationMs    int64  `json:"duration_ms,omitempty"`
}

type RunReport struct {
	InstanceID    string             `json:"instance_id"`
	Incarnation   int64              `json:"incarnation"`
	Revision      int64              `json:"revision"`
	Drift         int64              `json:"drift"`
	Results       []RunSectionResult `json:"results"`
	Restored      bool               `json:"restored,omitempty"`
	StoppedReason string             `json:"stopped_reason,omitempty"`
	Warnings      []string           `json:"warnings,omitempty"`
}

type RunSectionsEnvelope struct {
	Report RunReport `json:"report"`
}

func NewRunSectionsEnvelope(r *sandbox.RunReport) RunSectionsEnvelope {
	out := RunSectionsEnvelope{Report: RunReport{
		InstanceID: r.InstanceID, Incarnation: r.Incarnation,
		Revision: r.Revision, Drift: r.Drift, Results: []RunSectionResult{},
		Restored: r.Restored, StoppedReason: r.StoppedReason,
	}}
	for _, res := range r.Results {
		out.Report.Results = append(out.Report.Results, RunSectionResult{
			Name: res.Name, Status: res.Status, ExecID: res.ExecID,
			Error: res.Error, SkippedReason: res.SkippedReason, DurationMs: res.DurationMs,
		})
	}
	if len(r.Warnings) > 0 {
		out.Report.Warnings = r.Warnings
	}
	return out
}
