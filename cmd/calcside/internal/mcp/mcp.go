// Package mcp exposes the sandbox service over the Model Context
// Protocol (streamable HTTP, stateless). It is a thin transport adapter:
// every tool decodes arguments, calls the service with the actor carried
// by the request context, and maps service errors onto isError results.
// Authorization stays in the service layer, so MCP and REST agree.
package mcp

import (
	"context"
	"encoding/json"
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
	Exec(ctx context.Context, a service.Actor, id, code string, timeout time.Duration) (*sandbox.ExecView, error)
	Browse(ctx context.Context, a service.Actor, id, path string, listOnly bool) (*sandbox.FileView, error)
	Delete(ctx context.Context, a service.Actor, id string) error
	Prompt(ctx context.Context, a service.Actor, id, toolPrefix string) (*sandbox.PromptView, error)
}

const instructions = "Calcside runs Starlark in capability-gated instances. Call create_instance first, follow the returned prompt, then exec on the returned instance_id. Denied or policy_denied results mean the boundary refused the call — do not retry the same action."

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

type execArgs struct {
	InstanceID string `json:"instance_id"`
	Code       string `json:"code"`
}

type pathArgs struct {
	InstanceID string `json:"instance_id"`
	Path       string `json:"path,omitempty"`
}

type deleteArgs struct {
	InstanceID string `json:"instance_id"`
}

// newServer builds one stateless server for the actor resolved from the
// request; fs tools are registered only when the key's spec grants fs.
func newServer(sb Sandbox, a service.Actor) *mcp.Server { return newServerMode(sb, a, "", nil, "") }

// newServerMode builds one stateless server; when bound is non-empty
// the server is pinned to that instance: no create tool, exec/files
// only on bound, and its agent prompt as instructions.
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
		Description: "Run Starlark code on an instance; returns printed output or the error."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in execArgs) (*mcp.CallToolResult, any, error) {
			res, err := sb.Exec(ctx, a, in.InstanceID, in.Code, 0)
			if err != nil {
				return errText(err), nil, nil
			}
			if res.Error != nil {
				out := "error[" + string(res.Error.Type) + "]: " + res.Error.Message
				if res.Error.Backtrace != "" {
					out += "\n" + res.Error.Backtrace
				}
				if res.Output != "" {
					out = res.Output + "\n" + out
				}
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: out}}}, nil, nil
			}
			return text(res.Output), nil, nil
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
					v, err := sb.Browse(ctx, a, in.InstanceID, in.Path, true)
					if err != nil {
						return errText(err), nil, nil
					}
					res, err := jsonText(v)
					return res, nil, err
				})
			mcp.AddTool(srv, &mcp.Tool{Name: "read_file",
				Description: "Read a file's content from the instance filesystem (secrets redacted)."},
				func(ctx context.Context, _ *mcp.CallToolRequest, in pathArgs) (*mcp.CallToolResult, any, error) {
					v, err := sb.Browse(ctx, a, in.InstanceID, in.Path, false)
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
				if err := sb.Delete(ctx, a, in.InstanceID); err != nil {
					return errText(err), nil, nil
				}
				return text("deleted"), nil, nil
			})
	}
	return srv
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
