package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"go.starlark.net/syntax"

	"calcside/cmd/calcside/internal/service"
	"calcside/cmd/calcside/internal/service/access"
	"calcside/cmd/calcside/internal/service/review"
	"calcside/internal/runtime"
	"calcside/internal/serving"
	"calcside/internal/store"
)

var starlarkFileOpts = &syntax.FileOptions{
	Set:             true,
	While:           true,
	TopLevelControl: true,
	GlobalReassign:  true,
	Recursion:       true,
}

// ReleaseFinding is one typed readiness diagnostic (EP-0026).
type ReleaseFinding struct {
	Code     string `json:"code"`
	Severity string `json:"severity"` // "blocker" | "warning" | "info"
	Category string `json:"category"`
	Section  string `json:"section,omitempty"`
	Message  string `json:"message"`
	Fix      string `json:"fix,omitempty"`
}

// ReleaseCheckResult summarizes whether the current draft can be published.
type ReleaseCheckResult struct {
	Candidate     string                         `json:"candidate,omitempty"`
	LiveSeq       int64                          `json:"live_seq,omitempty"`
	LiveDigest    string                         `json:"live_digest,omitempty"`
	HasDrift      bool                           `json:"has_drift"`
	Status        string                         `json:"status"` // "ready" | "warnings" | "blocked"
	Findings      []ReleaseFinding               `json:"findings"`
	TestCaseCount int                            `json:"test_case_count"`
	LatestTestRun *store.ApplicationTestSuiteRun `json:"latest_test_run,omitempty"`
}

// ReleaseEntry is one row in the release history timeline.
type ReleaseEntry struct {
	*store.ApplicationRelease
	IsLive bool
}

// ReleaseHistoryView combines the live application status with its release history.
type ReleaseHistoryView struct {
	Status        string
	LiveReleaseID string
	LiveSeq       int64
	Releases      []*ReleaseEntry
}

type parsedDef struct {
	section string
	arity   int
	invalid bool // has *args, **kwargs, or non-1 positional requirement
}

// staticReadiness inspects syntax, notebook graph, entry contract, and schemas.
func staticReadiness(app *store.Application) []ReleaseFinding {
	var findings []ReleaseFinding

	defsByName := map[string][]parsedDef{}
	var waitTargets []struct {
		section string
		name    string
		dynamic bool
	}

	for _, sec := range app.Sections {
		f, err := starlarkFileOpts.Parse(sec.ID+".star", sec.Code, 0)
		if err != nil {
			findings = append(findings, ReleaseFinding{
				Code:     "syntax_error",
				Severity: "blocker",
				Category: "notebook_graph",
				Section:  sec.ID,
				Message:  err.Error(),
				Fix:      "Fix the Starlark syntax error in this section.",
			})
			continue
		}
		for _, st := range f.Stmts {
			d, ok := st.(*syntax.DefStmt)
			if !ok {
				continue
			}
			positional := 0
			bad := false
			for _, p := range d.Params {
				switch v := p.(type) {
				case *syntax.Ident:
					positional++
				case *syntax.BinaryExpr:
					if v.Op == syntax.EQ {
						positional++
					} else {
						bad = true
					}
				case *syntax.UnaryExpr:
					bad = true
				default:
					bad = true
				}
			}
			defsByName[d.Name.Name] = append(defsByName[d.Name.Name], parsedDef{
				section: sec.ID,
				arity:   positional,
				invalid: bad || positional != 1,
			})
		}
		syntax.Walk(f, func(n syntax.Node) bool {
			call, ok := n.(*syntax.CallExpr)
			if !ok {
				return true
			}
			dot, ok := call.Fn.(*syntax.DotExpr)
			if !ok || dot.Name == nil || dot.Name.Name != "wait" {
				return true
			}
			base, ok := dot.X.(*syntax.Ident)
			if !ok || base.Name != "interrupt" {
				return true
			}
			for _, arg := range call.Args {
				be, ok := arg.(*syntax.BinaryExpr)
				if !ok || be.Op != syntax.EQ {
					continue
				}
				key, ok := be.X.(*syntax.Ident)
				if !ok || (key.Name != "handler" && key.Name != "on_expire") {
					continue
				}
				if lit, ok := be.Y.(*syntax.Literal); ok {
					if s, ok := lit.Value.(string); ok {
						waitTargets = append(waitTargets, struct {
							section string
							name    string
							dynamic bool
						}{section: sec.ID, name: s})
						continue
					}
				}
				waitTargets = append(waitTargets, struct {
					section string
					name    string
					dynamic bool
				}{section: sec.ID, dynamic: true})
			}
			return true
		})
	}

	// Entry contract: main(input)
	mains := defsByName["main"]
	var mainSection string
	switch {
	case len(mains) == 0:
		findings = append(findings, ReleaseFinding{
			Code:     "missing_main",
			Severity: "blocker",
			Category: "entry_contract",
			Message:  "Application must define a top-level `def main(input):` function.",
			Fix:      "Add `def main(input):` taking one argument.",
		})
	case len(mains) > 1:
		findings = append(findings, ReleaseFinding{
			Code:     "duplicate_main",
			Severity: "blocker",
			Category: "entry_contract",
			Section:  mains[1].section,
			Message:  "`main` is defined more than once across the notebook.",
			Fix:      "Keep a single top-level `def main(input):` definition.",
		})
	default:
		mainSection = mains[0].section
		if mains[0].invalid {
			findings = append(findings, ReleaseFinding{
				Code:     "invalid_main_signature",
				Severity: "blocker",
				Category: "entry_contract",
				Section:  mainSection,
				Message:  "`main` must accept exactly one positional argument (`def main(input):`).",
				Fix:      "Change the signature to `def main(input):`.",
			})
		}
	}

	// Statically known interrupt handler targets
	entryRoots := map[string]bool{}
	if mainSection != "" {
		entryRoots[mainSection] = true
	}
	seenDynamic := map[string]bool{}
	seenTarget := map[string]bool{}
	for _, wt := range waitTargets {
		if wt.dynamic {
			if !seenDynamic[wt.section] {
				seenDynamic[wt.section] = true
				findings = append(findings, ReleaseFinding{
					Code:     "dynamic_handler",
					Severity: "warning",
					Category: "entry_contract",
					Section:  wt.section,
					Message:  "Interrupt handler target is computed dynamically; resolution happens at runtime.",
				})
			}
			continue
		}
		if seenTarget[wt.name] {
			continue
		}
		seenTarget[wt.name] = true
		if wt.name == "" || wt.name == "main" || strings.HasPrefix(wt.name, "_") {
			findings = append(findings, ReleaseFinding{
				Code:     "invalid_handler_name",
				Severity: "blocker",
				Category: "entry_contract",
				Section:  wt.section,
				Message:  fmt.Sprintf("Interrupt handler %q cannot be empty, `main`, or a private `_` name.", wt.name),
				Fix:      "Reference a public top-level one-argument handler function.",
			})
			continue
		}
		hdefs := defsByName[wt.name]
		switch {
		case len(hdefs) == 0:
			findings = append(findings, ReleaseFinding{
				Code:     "missing_handler",
				Severity: "blocker",
				Category: "entry_contract",
				Section:  wt.section,
				Message:  fmt.Sprintf("Interrupt handler %q is not defined as a top-level function.", wt.name),
				Fix:      fmt.Sprintf("Define `def %s(ctx):` in a notebook section.", wt.name),
			})
		case len(hdefs) > 1:
			findings = append(findings, ReleaseFinding{
				Code:     "duplicate_handler",
				Severity: "blocker",
				Category: "entry_contract",
				Section:  hdefs[1].section,
				Message:  fmt.Sprintf("Interrupt handler %q is defined multiple times.", wt.name),
				Fix:      "Keep a unique top-level handler definition.",
			})
		case hdefs[0].invalid:
			findings = append(findings, ReleaseFinding{
				Code:     "invalid_handler_signature",
				Severity: "blocker",
				Category: "entry_contract",
				Section:  hdefs[0].section,
				Message:  fmt.Sprintf("Handler %q must accept exactly one positional argument (`def %s(ctx):`).", wt.name, wt.name),
				Fix:      fmt.Sprintf("Change the signature to `def %s(ctx):`.", wt.name),
			})
		default:
			entryRoots[hdefs[0].section] = true
		}
	}

	// Graph diagnostics: compute upstream closure from entry roots.
	graph, _ := DraftNotebook(app)
	closure := map[string]bool{}
	var stack []string
	for r := range entryRoots {
		closure[r] = true
		stack = append(stack, r)
	}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, e := range graph.Edges {
			if e.To == cur && !closure[e.From] {
				closure[e.From] = true
				stack = append(stack, e.From)
			}
		}
	}
	for _, dg := range graph.Diagnostics {
		if dg.Kind == "parse_error" {
			continue // already reported with exact syntax error above
		}
		inClosure := len(entryRoots) == 0
		var sec string
		if len(dg.Sections) > 0 {
			sec = dg.Sections[0]
		}
		for _, s := range dg.Sections {
			if closure[s] {
				inClosure = true
				break
			}
		}
		sev := "warning"
		if inClosure && (dg.Kind == "cycle" || dg.Kind == "missing_provider" || dg.Kind == "duplicate_define") {
			sev = "blocker"
		}
		findings = append(findings, ReleaseFinding{
			Code:     "graph_" + string(dg.Kind),
			Severity: sev,
			Category: "notebook_graph",
			Section:  sec,
			Message:  dg.Message,
			Fix:      "Resolve the dependency issue in the section graph.",
		})
	}

	// Schema checks
	checkSchema := func(side string, raw json.RawMessage) {
		if len(raw) == 0 {
			return
		}
		if _, err := serving.CompileSchema(raw, serving.Limits{MaxBytes: 64 << 10, MaxDepth: 32, MaxNodes: 2048}); err != nil {
			findings = append(findings, ReleaseFinding{
				Code:     "invalid_" + side + "_schema",
				Severity: "blocker",
				Category: "schema",
				Message:  fmt.Sprintf("Invalid %s JSON Schema: %v", side, err),
				Fix:      fmt.Sprintf("Fix the %s schema in Settings → Contract.", side),
			})
		}
	}
	checkSchema("input", app.InputSchema)
	checkSchema("output", app.OutputSchema)

	return findings
}

// CheckRelease evaluates readiness findings, candidate digest, and drift
// without mutating state or decrypting secrets.
func (s *Service) CheckRelease(ctx context.Context, a service.Actor, id string, draftVersion int64) (*ReleaseCheckResult, error) {
	m, err := s.o.Access.Application(ctx, a, id, access.Configure)
	if err != nil {
		return nil, err
	}
	if draftVersion < 1 {
		return nil, service.BadRequest("version required")
	}
	a.WorkspaceID = m.WorkspaceID
	var res *ReleaseCheckResult
	err = s.o.Store.Tx(ctx, func(tx store.Store) error {
		if err := tx.LockWorkspace(ctx, m.WorkspaceID); err != nil {
			return err
		}
		cur, err := access.New(tx).Application(ctx, a, id, access.Configure)
		if err != nil {
			return err
		}
		if cur.Version != draftVersion {
			return service.Conflict("application changed")
		}
		findings := staticReadiness(cur)
		out := &ReleaseCheckResult{Findings: findings}
		if tcs, terr := tx.ListApplicationTestCases(ctx, cur.WorkspaceID, cur.ID); terr == nil {
			out.TestCaseCount = len(tcs)
		}

		if cur.PersistentInstanceID != "" {
			if pin, perr := tx.GetPersistentInstance(ctx, cur.WorkspaceID, cur.PersistentInstanceID); perr == nil && pin != nil {
				out.LiveDigest = pin.ReleaseID
				if hist, herr := tx.ListApplicationReleases(ctx, cur.WorkspaceID, cur.ID); herr == nil {
					out.LiveSeq = seqOf(hist, pin.ReleaseID)
				}
			}
		}

		bound, err := s.o.Sandbox.WithStore(tx)
		if err != nil {
			return err
		}
		b, berr := bound.BuildBoundary(ctx, a, cur.Spec, false)
		if berr != nil {
			out.Findings = append(out.Findings, ReleaseFinding{
				Code:     "boundary_invalid",
				Severity: "blocker",
				Category: "boundary",
				Message:  berr.Error(),
				Fix:      "Check extensions, modules, policies, and secret references in Settings.",
			})
		} else {
			cand, cerr := newRelease(cur, b, s.o.Now())
			if cerr != nil {
				out.Findings = append(out.Findings, ReleaseFinding{
					Code:     "candidate_build_failed",
					Severity: "blocker",
					Category: "candidate",
					Message:  cerr.Error(),
				})
			} else {
				out.Candidate = cand.ID
				out.HasDrift = out.LiveDigest != "" && cur.PublishedVersion == cur.Version && cand.ID != out.LiveDigest
				if out.HasDrift {
					out.Findings = append(out.Findings, ReleaseFinding{
						Code:     "resolved_drift",
						Severity: "info",
						Category: "candidate",
						Message:  "Draft source is unchanged, but workspace policies or pinned dependencies drifted since the live release.",
					})
				}
				if out.TestCaseCount > 0 {
					if tr, trerr := tx.GetApplicationTestSuiteRun(ctx, cur.WorkspaceID, cur.ID, cand.ID); trerr == nil && tr != nil {
						out.LatestTestRun = tr
					}
				}
			}
		}

		out.Status = "ready"
		for _, f := range out.Findings {
			if f.Severity == "blocker" {
				out.Status = "blocked"
				break
			}
			if f.Severity == "warning" {
				out.Status = "warnings"
			}
		}
		if out.Findings == nil {
			out.Findings = []ReleaseFinding{}
		}
		res = out
		return nil
	})
	if err != nil {
		var se *service.Error
		if errors.As(err, &se) {
			return nil, se
		}
		return nil, fail(err)
	}
	return res, nil
}

// Releases lists the immutable release history of an application, newest first.
func (s *Service) Releases(ctx context.Context, a service.Actor, id string) (*ReleaseHistoryView, error) {
	app, err := s.o.Access.Application(ctx, a, id, access.View)
	if err != nil {
		return nil, err
	}
	status, liveRelID, _, _ := s.status(ctx, app)
	rows, err := s.o.Store.ListApplicationReleases(ctx, app.WorkspaceID, app.ID)
	if err != nil {
		return nil, fail(err)
	}
	liveSeq := seqOf(rows, liveRelID)
	entries := make([]*ReleaseEntry, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		r := rows[i]
		entries = append(entries, &ReleaseEntry{
			ApplicationRelease: r,
			IsLive:             liveRelID != "" && r.Seq == liveSeq,
		})
	}
	return &ReleaseHistoryView{
		Status:        status,
		LiveReleaseID: liveRelID,
		LiveSeq:       liveSeq,
		Releases:      entries,
	}, nil
}

// CompareReleases diffs any two published sequences (base=0 means empty base)
// or a sequence against the current draft (head="draft").
func (s *Service) CompareReleases(ctx context.Context, a service.Actor, id string, baseSeq int64, head string) (*review.Result, error) {
	if baseSeq < 0 {
		return nil, service.BadRequest("base must be >= 0")
	}
	act := access.View
	if head == "draft" {
		act = access.Configure
	}
	app, err := s.o.Access.Application(ctx, a, id, act)
	if err != nil {
		return nil, err
	}
	a.WorkspaceID = app.WorkspaceID

	var baseArt *review.Artifact
	if baseSeq > 0 {
		bh, err := s.o.Store.GetApplicationRelease(ctx, app.WorkspaceID, app.ID, baseSeq)
		if err != nil {
			return nil, fail(err)
		}
		brel, err := s.o.Store.GetServingRelease(ctx, app.WorkspaceID, bh.ReleaseID)
		if err != nil {
			return nil, fail(err)
		}
		b := releaseArtifact(app, brel, bh.Seq, bh.DraftVersion)
		b.PublishedBy = bh.PublishedBy
		b.PublishedAt = bh.PublishedAt.UTC().Format(time.RFC3339)
		b.Note = bh.Note
		baseArt = &b
	}

	var candArt review.Artifact
	if head == "draft" {
		rel, err := s.reviewRelease(ctx, a, app, s.o.Sandbox, s.o.Now())
		if err != nil {
			return nil, err
		}
		candArt = releaseArtifact(app, rel, 0, app.Version)
	} else {
		hseq, perr := strconv.ParseInt(head, 10, 64)
		if perr != nil || hseq < 1 {
			return nil, service.BadRequest(`head must be a release sequence number or "draft"`)
		}
		hh, err := s.o.Store.GetApplicationRelease(ctx, app.WorkspaceID, app.ID, hseq)
		if err != nil {
			return nil, fail(err)
		}
		hrel, err := s.o.Store.GetServingRelease(ctx, app.WorkspaceID, hh.ReleaseID)
		if err != nil {
			return nil, fail(err)
		}
		candArt = releaseArtifact(app, hrel, hh.Seq, hh.DraftVersion)
		candArt.PublishedBy = hh.PublishedBy
		candArt.PublishedAt = hh.PublishedAt.UTC().Format(time.RFC3339)
		candArt.Note = hh.Note
	}

	var changes []review.Change
	identical := false
	if baseArt != nil {
		changes = diffArtifacts(*baseArt, candArt)
		identical = baseArt.Digest == candArt.Digest
	} else {
		changes = diffArtifacts(review.Artifact{}, candArt)
	}
	return &review.Result{
		Base:      baseArt,
		Candidate: candArt,
		Changes:   changes,
		Identical: identical,
	}, nil
}

// RestoreRelease copies an immutable published release's sections, spec,
// and schemas into the mutable draft under draft-version CAS (EP-0018/0026).
func (s *Service) RestoreRelease(ctx context.Context, a service.Actor, id string, seq int64, expectedDraftVersion int64) (*View, error) {
	if seq < 1 || expectedDraftVersion < 1 {
		return nil, service.BadRequest("seq and version must be >= 1")
	}
	app, err := s.o.Access.Application(ctx, a, id, access.Configure)
	if err != nil {
		return nil, err
	}
	hist, err := s.o.Store.GetApplicationRelease(ctx, app.WorkspaceID, app.ID, seq)
	if err != nil {
		return nil, fail(err)
	}
	rel, err := s.o.Store.GetServingRelease(ctx, app.WorkspaceID, hist.ReleaseID)
	if err != nil {
		return nil, fail(err)
	}

	byID := map[string]string{}
	for path, src := range rel.Program.Sources {
		secID := strings.TrimSuffix(path, ".star")
		byID[secID] = string(src)
	}
	var sections []store.ApplicationSection
	seen := map[string]bool{}
	for _, existing := range app.Sections {
		if code, ok := byID[existing.ID]; ok {
			sections = append(sections, store.ApplicationSection{ID: existing.ID, Code: code})
			seen[existing.ID] = true
		}
	}
	var rest []string
	for secID := range byID {
		if !seen[secID] {
			rest = append(rest, secID)
		}
	}
	slices.Sort(rest)
	for _, secID := range rest {
		sections = append(sections, store.ApplicationSection{ID: secID, Code: byID[secID]})
	}
	for i, sec := range sections {
		if sec.ID == "main" && i < len(sections)-1 {
			m := sec
			sections = append(append(sections[:i], sections[i+1:]...), m)
			break
		}
	}

	var inSchema, outSchema json.RawMessage
	defIn := string(store.DefaultApplicationSchema())
	if len(rel.Program.InputSchema) > 0 && string(rel.Program.InputSchema) != defIn {
		inSchema = append(json.RawMessage(nil), rel.Program.InputSchema...)
	}
	if len(rel.Program.OutputSchema) > 0 && string(rel.Program.OutputSchema) != "{}" {
		outSchema = append(json.RawMessage(nil), rel.Program.OutputSchema...)
	}

	next := *app
	next.Sections = sections
	next.Spec = append(json.RawMessage(nil), rel.Spec...)
	next.InputSchema = inSchema
	next.OutputSchema = outSchema
	next.UpdatedAt = s.o.Now().UTC()

	ok, err := s.o.Store.UpdateApplicationDraft(ctx, &next, expectedDraftVersion)
	if err != nil {
		return nil, fail(err)
	}
	if !ok {
		return nil, service.Conflict("application draft changed")
	}
	cur, err := s.o.Store.GetApplication(ctx, app.ID)
	if err != nil {
		return nil, fail(err)
	}
	return s.view(ctx, cur), nil
}

// RollbackRelease atomically repoints the live application pointer to an
// existing immutable release sequence after verifying it still prepares
// under current secret/boundary bindings, writing an audit/history row
// without mutating the draft (EP-0026).
func (s *Service) RollbackRelease(ctx context.Context, a service.Actor, id string, seq int64, expectedBase *string, note string) (*View, *store.ApplicationRelease, error) {
	if seq < 1 {
		return nil, nil, service.BadRequest("seq must be >= 1")
	}
	if expectedBase == nil || *expectedBase == "" {
		return nil, nil, service.BadRequest("expected_base required")
	}
	if note != "" && (!utf8.ValidString(note) || utf8.RuneCountInString(note) > 500) {
		return nil, nil, service.BadRequest("note exceeds 500 runes or is not UTF-8")
	}
	app, err := s.o.Access.Application(ctx, a, id, access.Configure)
	if err != nil {
		return nil, nil, err
	}
	a.WorkspaceID = app.WorkspaceID
	if app.PersistentInstanceID == "" {
		return nil, nil, service.BadRequest("application has no live release to roll back")
	}
	pin, err := s.o.Store.GetPersistentInstance(ctx, app.WorkspaceID, app.PersistentInstanceID)
	if err != nil {
		return nil, nil, fail(err)
	}
	if pin.ReleaseID != *expectedBase {
		return nil, nil, fail(store.ErrApplicationBaseConflict)
	}
	targetHist, err := s.o.Store.GetApplicationRelease(ctx, app.WorkspaceID, app.ID, seq)
	if err != nil {
		return nil, nil, fail(err)
	}
	if targetHist.ReleaseID == pin.ReleaseID {
		return nil, nil, service.BadRequest("selected release is already live")
	}
	targetRel, err := s.o.Store.GetServingRelease(ctx, app.WorkspaceID, targetHist.ReleaseID)
	if err != nil {
		return nil, nil, fail(err)
	}

	b, err := s.o.Sandbox.ReleaseBoundary(ctx, a, targetRel)
	if err != nil {
		return nil, nil, err
	}
	owner, err := s.owner(ctx, app.UserID)
	if err != nil {
		return nil, nil, err
	}
	prepCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	prep, cleanup, err := s.prepare(prepCtx, app.WorkspaceID, &runtime.PrepareRequest{
		CreateRequest: runtime.CreateRequest{
			InstanceID: store.NewID(store.PrefixInstance), Owner: owner,
			Labels: map[string]string{"name": app.Name, "application_id": app.ID},
			Spec:   targetRel.Spec, Policies: targetRel.Policies,
			Modules: b.Modules, Secrets: b.Secrets,
			ExpiresAt: s.o.Now().Add(30 * time.Second), Epoch: 1,
		},
		ReleaseID: targetRel.ID, Generation: 1, Program: targetRel.Program,
		PrepareID: store.NewID("prep_"),
	})
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		return nil, nil, err
	}
	if prep == nil || prep.Result.Error != nil {
		return nil, nil, service.BadRequest("target release failed to prepare")
	}

	if note == "" {
		note = fmt.Sprintf("Rollback to v%d", seq)
	}
	_, rbHist, err := s.o.Store.RollbackApplicationRelease(ctx, app.WorkspaceID, app.ID, seq, *expectedBase, a.UserID, note, s.o.Now())
	if err != nil {
		return nil, nil, fail(err)
	}
	cur, err := s.o.Store.GetApplication(ctx, app.ID)
	if err != nil {
		return nil, nil, fail(err)
	}
	return s.view(ctx, cur), rbHist, nil
}
