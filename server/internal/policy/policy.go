// Package policy implements the Rego-based gate hook. Every module must be
// `package calcside.hooks`; the hook queries data.calcside.hooks.deny, a
// partial set of strings. Deny on any non-empty set, eval error, or eval
// timeout (fail closed).
//
// User-supplied policies are compiled with a reduced builtin set: no
// http.send (SSRF), opa.runtime (env leakage), net.lookup_ip_addr, trace,
// or print.
package policy

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/open-policy-agent/opa/v1/ast"
	"github.com/open-policy-agent/opa/v1/rego"

	"calcside/internal/capability"
	"calcside/internal/types"
)

const requiredPackage = "calcside.hooks"
const query = "data.calcside.hooks.deny"

// builtins removed from user policies: server-env leaks, SSRF, DNS, and
// debugging builtins.
var userBlockedBuiltins = []string{
	"http.send",
	"opa.runtime",
	"net.lookup_ip_addr",
	"trace",
	"print",
}

// UserCapabilities is ast.CapabilitiesForThisVersion minus blocked builtins.
func UserCapabilities() *ast.Capabilities {
	caps := ast.CapabilitiesForThisVersion()
	blocked := map[string]bool{}
	for _, b := range userBlockedBuiltins {
		blocked[b] = true
	}
	out := *caps
	out.Builtins = nil
	for _, b := range caps.Builtins {
		if !blocked[b.Name] {
			out.Builtins = append(out.Builtins, b)
		}
	}
	out.AllowNet = []string{} // no network access regardless
	return &out
}

// Set is a compiled group of modules evaluated as one query.
type Set struct {
	name string
	pq   rego.PreparedEvalQuery
}

// Hook is a capability.Hook evaluating global and per-user policy sets.
type Hook struct {
	global  *Set            // compiled from --policy-dir, may be nil
	perUser map[string]*Set // keyed by policy ID, compiled separately
	timeout time.Duration
}

// NewHook compiles a policy snapshot: global modules (full
// capabilities, keyed by module name) and per-user policies (restricted
// capabilities, keyed by policy ID). Any compile error is fatal.
func NewHook(globalModules, userPolicies map[string]string, evalTimeout time.Duration) (*Hook, error) {
	if evalTimeout <= 0 {
		evalTimeout = 100 * time.Millisecond
	}
	h := &Hook{perUser: map[string]*Set{}, timeout: evalTimeout}
	if len(globalModules) > 0 {
		names := make([]string, 0, len(globalModules))
		for name := range globalModules {
			names = append(names, name)
		}
		// Compile in a stable order so a compile error is deterministic.
		sort.Strings(names)
		mods := make([]func(*rego.Rego), 0, len(names))
		for _, name := range names {
			src := globalModules[name]
			if err := ValidateModule(name, src); err != nil {
				return nil, err
			}
			mods = append(mods, rego.Module(name, src))
		}
		s, err := compile("global", nil, mods...)
		if err != nil {
			return nil, err
		}
		h.global = s
	}
	for id, src := range userPolicies {
		s, err := CompileUserPolicy(id, src)
		if err != nil {
			return nil, fmt.Errorf("policy %s: %w", id, err)
		}
		h.perUser[id] = s
	}
	return h, nil
}

// CompileUserPolicy validates and compiles a single user policy with
// restricted capabilities.
func CompileUserPolicy(name, src string) (*Set, error) {
	if err := ValidateModule(name, src); err != nil {
		return nil, err
	}
	return compile(name, UserCapabilities(), rego.Module(name+".rego", src))
}

func compile(name string, caps *ast.Capabilities, opts ...func(*rego.Rego)) (*Set, error) {
	base := []func(*rego.Rego){
		rego.Query(query),
		rego.StrictBuiltinErrors(true), // builtin errors become eval errors -> deny
	}
	if caps != nil {
		base = append(base, rego.Capabilities(caps))
	}
	pq, err := rego.New(append(base, opts...)...).PrepareForEval(context.Background())
	if err != nil {
		return nil, fmt.Errorf("policy %s: %w", name, err)
	}
	return &Set{name: name, pq: pq}, nil
}

// ValidateModule parses the module, requires `package calcside.hooks`, and
// rejects calls to blocked builtins (the capabilities check alone does not
// catch e.g. print(), which the parser handles specially).
func ValidateModule(name, src string) error {
	mod, err := ast.ParseModuleWithOpts(name, src, ast.ParserOptions{RegoVersion: ast.RegoV1})
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if mod.Package == nil || mod.Package.Path.String() != "data."+requiredPackage {
		return fmt.Errorf("%s: module must declare `package %s`", name, requiredPackage)
	}
	blocked := map[string]bool{}
	for _, b := range userBlockedBuiltins {
		blocked[b] = true
	}
	var found string
	checkOp := func(op ast.Ref) {
		if op != nil && blocked[op.String()] {
			found = op.String()
		}
	}
	ast.WalkExprs(mod, func(e *ast.Expr) bool {
		if found != "" {
			return true
		}
		if e.IsCall() {
			checkOp(e.Operator())
		}
		return false
	})
	ast.WalkTerms(mod, func(t *ast.Term) bool {
		if found != "" {
			return true
		}
		if c, ok := t.Value.(ast.Call); ok && len(c) > 0 {
			checkOp(c.Operator())
		}
		return false
	})
	if found != "" {
		return fmt.Errorf("%s: builtin %q is not allowed in policies", name, found)
	}
	return nil
}

// Validate checks a rego source for API use: syntax, required package, and
// compilation under the restricted user capabilities.
func Validate(src string) error {
	if err := ValidateModule("input", src); err != nil {
		return err
	}
	_, err := compile("validate", UserCapabilities(), rego.Module("input.rego", src))
	return err
}

func (h *Hook) Name() string { return "policy" }

// SetUserPolicies replaces the compiled per-user sets.
func (h *Hook) SetUserPolicies(pols map[string]*Set) {
	h.perUser = pols
}

func InputFor(phase types.Phase, c *capability.Call, r *capability.Result) map[string]any {
	in := map[string]any{
		"phase":      string(phase),
		"user":       map[string]any{"id": c.UserID, "email": c.UserEmail},
		"instance":   map[string]any{"id": c.InstanceID, "labels": c.InstanceLabels},
		"exec_id":    c.ExecID,
		"capability": string(c.Capability),
		"op":         string(c.Op),
		"args":       c.Args,
	}
	if phase == types.PhaseAfter && r != nil {
		errStr := any(nil)
		if r.Err != nil {
			errStr = r.Err.Error()
		}
		in["result"] = map[string]any{"error": errStr, "meta": r.Meta}
	}
	return in
}

func (h *Hook) evalSet(ctx context.Context, s *Set, in map[string]any) (string, bool) {
	evalCtx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()
	rs, err := s.pq.Eval(evalCtx, rego.EvalInput(in))
	if err != nil {
		return fmt.Sprintf("policy %s eval error: %v", s.name, err), true
	}
	var msgs []string
	for _, expr := range rs {
		for _, e := range expr.Expressions {
			switch vals := e.Value.(type) {
			case []any:
				for _, m := range vals {
					if s, ok := m.(string); ok {
						msgs = append(msgs, s)
					}
				}
			case map[string]any:
				for m := range vals {
					msgs = append(msgs, m)
				}
			case string:
				msgs = append(msgs, vals)
			}
		}
	}
	if len(msgs) == 0 {
		return "", false
	}
	sort.Strings(msgs)
	return strings.Join(msgs, "; "), true
}

func (h *Hook) eval(ctx context.Context, phase types.Phase, c *capability.Call, r *capability.Result) error {
	in := InputFor(phase, c, r)
	if h.global != nil {
		if reason, denied := h.evalSet(ctx, h.global, in); denied {
			return fmt.Errorf("global policy: %s", reason)
		}
	}
	ids := make([]string, 0, len(h.perUser))
	for id := range h.perUser {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if reason, denied := h.evalSet(ctx, h.perUser[id], in); denied {
			return fmt.Errorf("policy %s: %s", id, reason)
		}
	}
	return nil
}

func (h *Hook) Before(ctx context.Context, c *capability.Call) error {
	return h.eval(ctx, types.PhaseBefore, c, nil)
}

func (h *Hook) After(ctx context.Context, c *capability.Call, r *capability.Result) error {
	return h.eval(ctx, types.PhaseAfter, c, r)
}
