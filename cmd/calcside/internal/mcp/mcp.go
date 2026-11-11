// Package mcp exposes the sandbox service over the Model Context
// Protocol (streamable HTTP, stateless). It is a thin transport adapter:
// every tool decodes arguments, calls the service with the actor carried
// by the request context, and maps service errors onto isError results.
// Authorization stays in the service layer, so MCP and REST agree.
//
// EP-0022: the MCP surface is deliberately coarser than the console's —
// agents get exec (which auto-saves a section when given section
// metadata), the notebook outline, section reads, explicit batch runs,
// and add_section for authoring without execution. Fine-grained
// save/move/delete authoring stays REST/console-only.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"calcside/cmd/calcside/internal/auth"
	"calcside/cmd/calcside/internal/service"
	"calcside/cmd/calcside/internal/service/access"
	"calcside/cmd/calcside/internal/service/sandbox"
	"calcside/internal/store"
	"calcside/internal/types"
)

// Sandbox is the slice of the sandbox service the tools call; it is
// satisfied by *sandbox.Service.
type Sandbox interface {
	Create(ctx context.Context, a service.Actor, spec []byte) (*store.Instance, error)
	ExecSection(ctx context.Context, a service.Actor, id, code string, timeout time.Duration, sec *sandbox.SectionInput) (*sandbox.ExecOutcome, error)
	Notebook(ctx context.Context, a service.Actor, id string) (*sandbox.NotebookView, error)
	ReadSection(ctx context.Context, a service.Actor, id, name string, includeOutput bool) (*sandbox.SectionDetail, error)
	AddSection(ctx context.Context, a service.Actor, id, name, description, code, rerun, after string) (*sandbox.SaveSectionResult, error)
	RunSections(ctx context.Context, a service.Actor, id string, sel sandbox.SectionSelector, revision *int64) (*sandbox.RunReport, error)
	Browse(ctx context.Context, a service.Actor, id, path string, listOnly bool) (*sandbox.FileView, error)
	Delete(ctx context.Context, a service.Actor, id string) error
	Prompt(ctx context.Context, a service.Actor, id, toolPrefix string) (*sandbox.PromptView, error)
}

const instructions = "Calcside runs Starlark in capability-gated instances with durable notebook sections. Call create_instance first, follow the returned prompt, then exec on the returned instance_id. exec with section metadata saves the section before running it — failed code is kept too. notebook shows the outline (states, defines/uses); run executes sections in order; add_section writes code without running it. Denied or policy_denied results mean the boundary refused the call — do not retry the same action."

func errText(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}
}

func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func jsonText(v any) (*mcp.CallToolResult, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return text(string(b)), nil
}

type createArgs struct {
	Labels     map[string]string `json:"labels,omitempty"`
	TTLSeconds *int64            `json:"ttl_seconds,omitempty"`
}

type createOut struct {
	InstanceID string `json:"instance_id"`
	ExpiresAt  string `json:"expires_at"`
	Prompt     string `json:"prompt"`
}

type execSectionArgs struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Rerun       string `json:"rerun,omitempty"`
	After       string `json:"after,omitempty"`
	Revision    *int64 `json:"revision,omitempty"`
}

type execArgs struct {
	InstanceID string           `json:"instance_id"`
	Code       string           `json:"code"`
	Section    *execSectionArgs `json:"section,omitempty"`
}

type instanceOnlyArgs struct {
	InstanceID string `json:"instance_id"`
}

type readSectionArgs struct {
	InstanceID    string `json:"instance_id"`
	Name          string `json:"name"`
	IncludeOutput bool   `json:"include_output,omitempty"`
}

type addSectionArgs struct {
	InstanceID   string `json:"instance_id"`
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	Code         string `json:"code"`
	Rerun        string `json:"rerun,omitempty"`
	AfterSection string `json:"after_section,omitempty"`
}

type runArgs struct {
	InstanceID string          `json:"instance_id"`
	Sections   json.RawMessage `json:"sections"`
	Revision   *int64          `json:"revision,omitempty"`
}

type pathArgs struct {
	InstanceID string `json:"instance_id"`
	Path       string `json:"path,omitempty"`
}

type deleteArgs struct {
	InstanceID string `json:"instance_id"`
}

// effID resolves the target instance: bound servers pin calls to their
// instance when the argument is empty.
func effID(arg, bound string) string {
	if arg == "" {
		return bound
	}
	return arg
}

// execOutcomeText formats an exec result like the pre-notebook tool did,
// plus a compact notebook trailer when the call was sectioned or drift
// changed.
func execOutcomeText(out *sandbox.ExecOutcome) string {
	res := out.View
	var b strings.Builder
	if res.Error != nil {
		if res.Output != "" {
			b.WriteString(res.Output)
			b.WriteString("\n")
		}
		b.WriteString("error[" + string(res.Error.Type) + "]: " + res.Error.Message)
		if res.Error.Backtrace != "" {
			b.WriteString("\n" + res.Error.Backtrace)
		}
	} else {
		b.WriteString(res.Output)
	}
	if out.Section != "" {
		fmt.Fprintf(&b, "\n[section %s → %s · notebook_rev %d]", out.Section, out.State, out.Revision)
	} else if out.Drift > 0 {
		fmt.Fprintf(&b, "\n[drift %d · notebook_rev %d]", out.Drift, out.Revision)
	}
	return b.String()
}

// newServer builds one stateless server for the actor resolved from the
// request; fs tools are registered only when the key's spec grants fs.
func newServer(sb Sandbox, a service.Actor) *mcp.Server { return newServerMode(sb, a, "", nil, "") }

// newServerMode builds one stateless server; when bound is non-empty
// the server is pinned to that instance: no create/delete tools and
// instance_id arguments default to the bound instance.
func newServerMode(sb Sandbox, a service.Actor, bound string, boundIn *store.Instance, prompt string) *mcp.Server {
	inst := instructions
	if prompt != "" {
		inst = prompt
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "calcside"}, &mcp.ServerOptions{Instructions: inst})
	if bound == "" {
		mcp.AddTool(srv, &mcp.Tool{Name: "create_instance",
			Description: "Create an instance from this key's instance spec. Returns instance_id, expires_at and the agent prompt."},
			func(ctx context.Context, _ *mcp.CallToolRequest, in createArgs) (*mcp.CallToolResult, any, error) {
				body, _ := json.Marshal(in)
				inst, err := sb.Create(ctx, a, body)
				if err != nil {
					return errText(err), nil, nil
				}
				pv, err := sb.Prompt(ctx, a, inst.ID, "")
				if err != nil {
					return errText(err), nil, nil
				}
				res, err := jsonText(createOut{InstanceID: inst.ID, ExpiresAt: inst.ExpiresAt.Format(time.RFC3339), Prompt: pv.Prompt})
				return res, nil, err
			})
	}
	mcp.AddTool(srv, &mcp.Tool{Name: "exec",
		Description: "Run Starlark code on an instance; returns printed output or the error. With section {name, description?, rerun?, after?} the section is saved BEFORE running — failed code is preserved as a failed section. Without section it is a scratch exec (never becomes a section; may count as drift)."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in execArgs) (*mcp.CallToolResult, any, error) {
			var sec *sandbox.SectionInput
			if in.Section != nil {
				sec = &sandbox.SectionInput{
					Name: in.Section.Name, Description: in.Section.Description,
					Rerun: in.Section.Rerun, After: in.Section.After, Revision: in.Section.Revision,
				}
			}
			out, err := sb.ExecSection(ctx, a, effID(in.InstanceID, bound), in.Code, 0, sec)
			if err != nil {
				return errText(err), nil, nil
			}
			t := execOutcomeText(out)
			if out.View.Error != nil {
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: t}}}, nil, nil
			}
			return text(t), nil, nil
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "notebook",
		Description: "Read the instance's notebook outline: every section's state (not_run/fresh/stale/failed), defines/uses, effects, plus guidance. Read-only — never restores a lost instance."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in instanceOnlyArgs) (*mcp.CallToolResult, any, error) {
			v, err := sb.Notebook(ctx, a, effID(in.InstanceID, bound))
			if err != nil {
				return errText(err), nil, nil
			}
			var b strings.Builder
			b.WriteString(v.Outline)
			fmt.Fprintf(&b, "\n\nrevision %d · drift %d · status %s · incarnation %d", v.Revision, v.Drift, v.Status, v.Incarnation)
			if v.RestoredAt != nil {
				fmt.Fprintf(&b, " · restored %s — memory/files emptied at restore", v.RestoredAt.UTC().Format(time.RFC3339))
				if v.RestoredEmpty {
					b.WriteString("; still empty, nothing has run this incarnation")
				}
			}
			if len(v.Next) > 0 {
				b.WriteString("\nnext:")
				for _, n := range v.Next {
					b.WriteString("\n- " + n)
				}
			}
			return text(b.String()), nil, nil
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "read_section",
		Description: "Read one notebook section: metadata plus its code; include_output adds the last run's output/error."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in readSectionArgs) (*mcp.CallToolResult, any, error) {
			d, err := sb.ReadSection(ctx, a, effID(in.InstanceID, bound), in.Name, in.IncludeOutput)
			if err != nil {
				return errText(err), nil, nil
			}
			res, err := jsonText(d)
			return res, nil, err
		})
	// runSections is the canonical batch-run tool — the only run
	// surface agents get (no alias, no fine-grained authoring tools).
	// The agent prompt names run_sections so every surface agrees.
	runSections := func(ctx context.Context, _ *mcp.CallToolRequest, in runArgs) (*mcp.CallToolResult, any, error) {
		sel, err := parseSections(in.Sections)
		if err != nil {
			return errText(err), nil, nil
		}
		rep, err := sb.RunSections(ctx, a, effID(in.InstanceID, bound), sel, in.Revision)
		if err != nil {
			return errText(err), nil, nil
		}
		var b strings.Builder
		for _, r := range rep.Results {
			fmt.Fprintf(&b, "%s: %s", r.Name, r.Status)
			if r.SkippedReason != "" {
				b.WriteString(" (" + r.SkippedReason + ")")
			}
			if r.Error != "" {
				b.WriteString(" — " + firstLine(r.Error))
			}
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "\nrevision %d · drift %d · incarnation %d", rep.Revision, rep.Drift, rep.Incarnation)
		for _, w := range rep.Warnings {
			b.WriteString("\nwarning: " + w)
		}
		failed := rep.Restored
		for _, r := range rep.Results {
			if r.Status == "error" {
				failed = true
			}
		}
		if failed {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: b.String()}}}, nil, nil
		}
		return text(b.String()), nil, nil
	}
	runDesc := "Run notebook sections in order: sections is \"all\", \"stale\", one name, or a list of names. Bulk selections skip once-sections that already succeeded; execution stops at the first failure and reports the rest not_run. On a lost instance the first run restores an EMPTY process and returns instance_restored without executing — call it again afterwards."
	mcp.AddTool(srv, &mcp.Tool{Name: "run_sections", Description: runDesc}, runSections)
	mcp.AddTool(srv, &mcp.Tool{Name: "add_section",
		Description: "Create a NEW notebook section WITHOUT running it — fails if the name already exists (edit via exec with section metadata instead). Args: name ([a-z][a-z0-9_]{0,47}), description (required, one line), code (may be empty), optional rerun (safe|once) and after_section to insert after a named section. The current notebook revision is fetched server-side."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in addSectionArgs) (*mcp.CallToolResult, any, error) {
			res, err := sb.AddSection(ctx, a, effID(in.InstanceID, bound), in.Name, in.Description, in.Code, in.Rerun, in.AfterSection)
			if err != nil {
				return errText(err), nil, nil
			}
			return text(fmt.Sprintf("saved section %s at position %d (notebook_rev %d, state %s)",
				res.Section.Name, res.Section.Position, res.Revision, sectionStateOf(res.Section))), nil, nil
		})
	specJSON := a.KeySpec
	if bound != "" && boundIn != nil {
		specJSON = boundIn.Spec
	}
	var spec struct {
		Capabilities map[string]json.RawMessage `json:"capabilities"`
	}
	if err := json.Unmarshal(specJSON, &spec); err == nil {
		if _, ok := spec.Capabilities["fs"]; ok {
			mcp.AddTool(srv, &mcp.Tool{Name: "list_files",
				Description: "List a directory in the instance filesystem."},
				func(ctx context.Context, _ *mcp.CallToolRequest, in pathArgs) (*mcp.CallToolResult, any, error) {
					v, err := sb.Browse(ctx, a, effID(in.InstanceID, bound), in.Path, true)
					if err != nil {
						return errText(err), nil, nil
					}
					res, err := jsonText(v)
					return res, nil, err
				})
			mcp.AddTool(srv, &mcp.Tool{Name: "read_file",
				Description: "Read a file's content from the instance filesystem (secrets redacted)."},
				func(ctx context.Context, _ *mcp.CallToolRequest, in pathArgs) (*mcp.CallToolResult, any, error) {
					v, err := sb.Browse(ctx, a, effID(in.InstanceID, bound), in.Path, false)
					if err != nil {
						return errText(err), nil, nil
					}
					res, err := jsonText(v)
					return res, nil, err
				})
		}
	}
	if bound == "" {
		mcp.AddTool(srv, &mcp.Tool{Name: "delete_instance",
			Description: "Delete an instance created by this key."},
			func(ctx context.Context, _ *mcp.CallToolRequest, in deleteArgs) (*mcp.CallToolResult, any, error) {
				if err := sb.Delete(ctx, a, effID(in.InstanceID, bound)); err != nil {
					return errText(err), nil, nil
				}
				return text("deleted"), nil, nil
			})
	}
	return srv
}

// parseSections normalizes the run tool's sections argument: "all",
// "stale", a single name, or a list of names.
func parseSections(raw json.RawMessage) (sandbox.SectionSelector, error) {
	var names []string
	if err := json.Unmarshal(raw, &names); err == nil {
		return sandbox.SectionSelector{Names: names}, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return sandbox.SectionSelector{}, service.BadRequest("sections must be \"all\", \"stale\", a name, or a list of names")
	}
	switch s {
	case "all":
		return sandbox.SectionSelector{All: true}, nil
	case "stale":
		return sandbox.SectionSelector{Stale: true}, nil
	default:
		return sandbox.SectionSelector{Names: []string{s}}, nil
	}
}

// sectionStateOf approximates the saved section's state for the
// add_section receipt: a brand-new section is not_run; a re-saved one
// that previously ran is stale (its stored run facts decide).
func sectionStateOf(sec *store.NotebookSection) string {
	if sec.LastRunSeq == 0 || sec.LastIncarnation == 0 {
		return sandbox.SectionNotRun
	}
	if sec.CodeSHA256 != sec.LastCodeSHA256 {
		return sandbox.SectionStale
	}
	return sandbox.SectionFresh
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// Handler returns the /mcp handler. It requires a restricted API key:
// the api layer resolves the principal; anything else is rejected.
func Handler(sb Sandbox) http.Handler {
	h := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		p := auth.FromContext(r.Context())
		if p == nil {
			return nil
		}
		return newServer(sb, p.Actor())
	}, &mcp.StreamableHTTPOptions{Stateless: true, MaxRequestBodyBytes: sandbox.MaxCodeBytes + 64<<10})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := auth.FromContext(r.Context())
		switch {
		case p == nil:
			writeErr(w, http.StatusUnauthorized, types.ErrCodeUnauthorized, "restricted API key required")
		case p.ViaKey() && strings.HasPrefix(p.KeyBoundID, string(store.PrefixInstance)):
			writeErr(w, http.StatusForbidden, types.ErrCodeForbidden, "this key is bound to an instance — use the instance URL")
		case !p.ViaKey() || !p.Restricted():
			writeErr(w, http.StatusForbidden, types.ErrCodeForbidden, "MCP requires a restricted API key")
		default:
			h.ServeHTTP(w, r)
		}
	})
}

// InstanceHandler pins the session to one instance; any key with Run
// on it (bound, restricted, full) may use it.
func InstanceHandler(sb Sandbox, ac *access.Checker, instanceID string) http.Handler {
	h := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		p := auth.FromContext(r.Context())
		if p == nil {
			return nil
		}
		a := p.Actor()
		in, err := ac.Instance(r.Context(), a, instanceID, access.Run)
		if err != nil {
			return nil
		}
		pv, _ := sb.Prompt(r.Context(), a, instanceID, "")
		prompt := ""
		if pv != nil {
			prompt = pv.Prompt
		}
		return newServerMode(sb, a, instanceID, in, prompt)
	}, &mcp.StreamableHTTPOptions{Stateless: true, MaxRequestBodyBytes: sandbox.MaxCodeBytes + 64<<10})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := auth.FromContext(r.Context())
		switch {
		case p == nil:
			writeErr(w, http.StatusUnauthorized, types.ErrCodeUnauthorized, "API key required")
		case !p.ViaKey():
			writeErr(w, http.StatusForbidden, types.ErrCodeForbidden, "API key required")
		case p.KeyViewOnly:
			writeErr(w, http.StatusForbidden, types.ErrCodeForbidden, "view-only keys cannot use MCP")
		default:
			h.ServeHTTP(w, r)
		}
	})
}

func writeErr(w http.ResponseWriter, code int, ecode types.APIErrorCode, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": string(ecode), "message": msg}})
}
