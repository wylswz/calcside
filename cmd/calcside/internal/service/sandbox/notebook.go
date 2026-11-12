package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"calcside/cmd/calcside/internal/service"
	"calcside/cmd/calcside/internal/service/access"
	"calcside/internal/notebook"
	"calcside/internal/runtime"
	"calcside/internal/store"
	"calcside/internal/types"
)

// EP-0022 instance notebooks: durable named sections, outline-first
// access, scratch drift, and lost→running restoration into a fresh
// empty process. All notebook state lives on the API tier; the runtime
// only sees ordinary execs (with an optional source name for
// backtraces).

// Section states, derived — never stored.
const (
	SectionNotRun = "not_run"
	SectionFresh  = "fresh"
	SectionStale  = "stale"
	SectionFailed = "failed"
)

const (
	maxNotebookSections = 200
	maxSectionDescBytes = 200
)

var sectionNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,47}$`)

// opLeaseTTL bounds one claimed operation. It must exceed the longest
// possible exec plus audit/record overhead; runs refresh it per
// section, so a single section's exec timeout plus margin suffices.
func (s *Service) opLeaseTTL() time.Duration {
	ttl := s.limits.MaxExecTimeout + 2*time.Minute
	if ttl < 7*time.Minute {
		ttl = 7 * time.Minute
	}
	return ttl
}

// --- request/response shapes (service boundary; api/dto maps them) ---

// SectionInput marks an exec as a notebook section run: the section is
// created or replaced with this code, then executed. Revision, when
// set, is the notebook CAS the caller last saw.
type SectionInput struct {
	Name        string
	Description string
	Rerun       string // "" or "safe"|"once"
	After       string // "" appends; otherwise insert after this section
	Revision    *int64
}

// SaveSectionRequest edits a section without running it. Code nil
// keeps the stored code; FromExec promotes a successful scratch exec.
// CreateOnly rejects an existing name — the agent-facing add_section
// path, which must never silently overwrite someone's edit.
type SaveSectionRequest struct {
	Code        *string
	Description *string
	Rerun       *string
	After       *string
	FromExec    string
	Revision    int64
	CreateOnly  bool
}

// SaveSectionResult is a saved section plus the new notebook revision.
type SaveSectionResult struct {
	Section  *store.NotebookSection `json:"section"`
	Revision int64                  `json:"revision"`
}

// DeleteSectionResult reports sections still reading the deleted
// section's defines, plus the new notebook revision.
type DeleteSectionResult struct {
	Affected []string `json:"affected"`
	Revision int64    `json:"revision"`
}

// SectionSelector picks run targets: everything, only non-fresh
// sections, or an explicit name list.
type SectionSelector struct {
	All   bool
	Stale bool
	Names []string
}

// RunSectionResult is one section's outcome inside a notebook run.
// Status is ok|error|reused|skipped|not_run — "planned" appears only
// in dry-run reports. Reason is the planner's reason the section was
// scheduled this way ("target", "upstream: not fresh", "upstream
// <name> runs first", "fresh", "once section already succeeded").
type RunSectionResult struct {
	Name          string `json:"name"`
	Status        string `json:"status"`
	ExecID        string `json:"exec_id,omitempty"`
	Error         string `json:"error,omitempty"`
	SkippedReason string `json:"skipped_reason,omitempty"`
	Reason        string `json:"reason,omitempty"`
	DurationMs    int64  `json:"duration_ms,omitempty"`
}

// RunReport is the per-section result of run(all|stale|names).
// Restored/StoppedReason describe a mid-batch loss: earlier sections
// executed in the previous runtime and the rest report not_run —
// callers must not blindly retry side effects.
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

// ExecOutcome is an exec result plus additive notebook facts: the
// notebook revision after the call, the drift counter, and — for a
// sectioned exec — the section name, its post-run state, and the
// upstream sections the dependency plan ran or reused first.
type ExecOutcome struct {
	View     *ExecView          `json:"-"`
	Revision int64              `json:"notebook_revision"`
	Drift    int64              `json:"drift"`
	Section  string             `json:"section,omitempty"`
	State    string             `json:"section_state,omitempty"`
	Upstream []RunSectionResult `json:"upstream,omitempty"`
}

// NotebookSectionMeta is one outline row: metadata only, no code or
// output. LastIncarnation identifies which process generation produced
// the last run — output older than the notebook's current incarnation
// belongs to a previous (possibly lost) runtime.
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

// NotebookGraphEdge is one variable dependency in the notebook graph:
// From defines Name, To reads it.
type NotebookGraphEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Name string `json:"name"`
}

// NotebookGraphDiagnostic is one graph problem: kind is one of
// duplicate_define, cycle, missing_provider, parse_error.
type NotebookGraphDiagnostic struct {
	Kind     string   `json:"kind"`
	Message  string   `json:"message"`
	Sections []string `json:"sections"`
	Names    []string `json:"names"`
}

// NotebookGraph is the dependency graph derived from current saved
// code on every read; it also drives run order and staleness.
type NotebookGraph struct {
	Edges       []NotebookGraphEdge       `json:"edges"`
	Diagnostics []NotebookGraphDiagnostic `json:"diagnostics"`
}

// NotebookView is the outline: instance facts plus one meta row per
// section, the rendered text outline, computed next-step warnings, and
// the dependency graph. RestoredEmpty is true only while the restored
// process is verifiably untouched: restored_at set, no section run and
// no scratch exec in the current incarnation.
type NotebookView struct {
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
	Graph         NotebookGraph         `json:"graph"`
}

// SectionDetail is one section with its code and, on request, the last
// execution's output/error.
type SectionDetail struct {
	Section     NotebookSectionMeta `json:"section"`
	Code        string              `json:"code"`
	Output      string              `json:"output,omitempty"`
	Error       string              `json:"error,omitempty"`
	OutputTrunc bool                `json:"output_truncated,omitempty"`
}

// --- helpers ---

func validSectionName(name string) bool { return sectionNameRe.MatchString(name) }

func validRerun(r string) bool { return r == "" || r == store.RerunSafe || r == store.RerunOnce }

func validDescription(d string) bool {
	return len(d) <= maxSectionDescBytes && !strings.ContainsAny(d, "\r\n")
}

// specCaps lists the names an instance's spec binds as globals, for
// static-analysis exclusion: capability names plus the implicit io.
func specCaps(in *store.Instance) []string {
	var spec runtime.Spec
	caps := []string{"io"}
	if json.Unmarshal(in.Spec, &spec) == nil {
		for n := range spec.Capabilities {
			caps = append(caps, n)
		}
	}
	return caps
}

func codeSHA(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

// analyzeSection computes defines/uses for code under this instance's
// bound names; unparseable code yields empty sets (still stored — the
// failure is preserved, per EP-0022).
func (s *Service) analyzeSection(in *store.Instance, code string) (defines, uses []string) {
	defs, used, ok := notebook.Analyze(code, specCaps(in))
	if !ok {
		return []string{}, []string{}
	}
	if defs == nil {
		defs = []string{}
	}
	if used == nil {
		used = []string{}
	}
	return defs, used
}

// sectionState is the derived run state of one section. ranThisInc and
// runSeq describe its last run inside the current incarnation.
type sectionState struct {
	sec        *store.NotebookSection
	state      string
	ranThisInc bool
	runSeq     int64
}

// restoredEmpty reports whether a restored process is verifiably
// untouched: nothing ran in the current incarnation — no section exec,
// no drift-counting scratch exec, and no recorded execution at all
// (a scratch fs.write mutates files without defining tracked names).
// last_active_at is the conservative backstop: file uploads and other
// non-exec mutations bump it without producing an execution row, so
// any activity at/after the restore means the process cannot be
// claimed empty.
func restoredEmpty(in *store.Instance, states []sectionState, execs []*store.Execution) bool {
	if in.RestoredAt == nil || in.Drift > 0 {
		return false
	}
	if !in.LastActiveAt.Before(*in.RestoredAt) {
		return false
	}
	for _, st := range states {
		if st.ranThisInc {
			return false
		}
	}
	for _, e := range execs {
		if e.Incarnation == in.Incarnation {
			return false
		}
	}
	return true
}

// analyzeNotebook rebuilds per-section static facts and the dependency
// graph from each section's CURRENT saved code — stored defines/uses
// are provenance, not graph input.
func analyzeNotebook(in *store.Instance, secs []*store.NotebookSection) ([]notebook.GraphSection, notebook.Graph) {
	caps := specCaps(in)
	inputs := make([]notebook.GraphSection, 0, len(secs))
	for _, sec := range secs {
		defs, uses, ok := notebook.Analyze(sec.Code, caps)
		inputs = append(inputs, notebook.GraphSection{
			Name: sec.Name, Defines: defs, Uses: uses, ParseOK: ok,
		})
	}
	return inputs, notebook.BuildGraph(inputs)
}

// graphProviders maps each consumer section to its deduplicated direct
// provider names, sorted. graphReads maps consumer → used name →
// provider section for resolved reads only.
func graphProviders(g notebook.Graph) map[string][]string {
	seen := map[string]map[string]bool{}
	for _, e := range g.Edges {
		if seen[e.To] == nil {
			seen[e.To] = map[string]bool{}
		}
		seen[e.To][e.From] = true
	}
	out := make(map[string][]string, len(seen))
	for to, set := range seen {
		for from := range set {
			out[to] = append(out[to], from)
		}
		sort.Strings(out[to])
	}
	return out
}

func graphReads(g notebook.Graph) map[string]map[string]string {
	out := map[string]map[string]string{}
	for _, e := range g.Edges {
		if out[e.To] == nil {
			out[e.To] = map[string]string{}
		}
		out[e.To][e.Name] = e.From
	}
	return out
}

// notebookModel is one call's consistent view of a notebook: sections,
// derived states, the dependency graph rebuilt from saved code, and
// the graph-derived lookups the outline and run planner share.
type notebookModel struct {
	secs      []*store.NotebookSection
	states    []sectionState
	inputs    []notebook.GraphSection
	graph     notebook.Graph
	providers map[string][]string
	reads     map[string]map[string]string
}

func buildNotebookModel(in *store.Instance, secs []*store.NotebookSection) *notebookModel {
	inputs, g := analyzeNotebook(in, secs)
	providers := graphProviders(g)
	return &notebookModel{
		secs: secs, inputs: inputs, graph: g, providers: providers,
		states: computeStates(in, secs, providers),
		reads:  graphReads(g),
	}
}

// computeStates derives every section's state. Base rules are
// unchanged — not_run without a recorded run this incarnation, failed
// on a current-code error, stale on a code change, fresh otherwise —
// then staleness propagates over graph edges to a fixed point: a fresh
// section is stale when a direct provider is not fresh (stale, failed
// or not_run) or ran after it. Page position no longer matters.
func computeStates(in *store.Instance, secs []*store.NotebookSection, providers map[string][]string) []sectionState {
	out := make([]sectionState, 0, len(secs))
	for _, sec := range secs {
		st := sectionState{sec: sec}
		// A lost instance's recorded runs belong to a dead process:
		// derive not_run (history fields stay intact) until restore.
		if in.Status != types.InstanceLost && sec.LastIncarnation == in.Incarnation && sec.LastRunSeq > 0 {
			st.ranThisInc = true
			st.runSeq = sec.LastRunSeq
		}
		codeCurrent := sec.CodeSHA256 != "" && sec.CodeSHA256 == sec.LastCodeSHA256
		switch {
		case !st.ranThisInc:
			st.state = SectionNotRun
		case sec.LastStatus != "" && sec.LastStatus != "ok" && codeCurrent:
			st.state = SectionFailed
		case !codeCurrent:
			st.state = SectionStale
		default:
			st.state = SectionFresh
		}
		out = append(out, st)
	}
	stateByName := make(map[string]string, len(out))
	runSeqByName := make(map[string]int64, len(out))
	for _, st := range out {
		stateByName[st.sec.Name] = st.state
		runSeqByName[st.sec.Name] = st.runSeq
	}
	for changed := true; changed; {
		changed = false
		for i := range out {
			st := &out[i]
			if st.state != SectionFresh {
				continue
			}
			for _, p := range providers[st.sec.Name] {
				if stateByName[p] != SectionFresh || runSeqByName[p] > st.runSeq {
					st.state = SectionStale
					stateByName[st.sec.Name] = SectionStale
					changed = true
					break
				}
			}
		}
	}
	return out
}

func metaOf(st sectionState, providers []string) NotebookSectionMeta {
	sec := st.sec
	// Slices must marshal as [] never null — the console iterates them
	// unconditionally.
	strs := func(s []string) []string {
		if s == nil {
			return []string{}
		}
		return s
	}
	return NotebookSectionMeta{
		Name: sec.Name, Position: sec.Position, Description: sec.Description,
		State: st.state, Rerun: sec.Rerun,
		Defines: strs(sec.Defines), Uses: strs(sec.Uses), Effects: strs(sec.Effects),
		DependsOn:  strs(providers),
		LastExecID: sec.LastExecID, LastIncarnation: sec.LastIncarnation,
		LastStatus: sec.LastStatus,
		LastRunAt:  sec.LastRunAt, OnceRan: sec.SucceededBefore,
	}
}

// --- read paths (never restore) ---

// notebookFor fetches the instance (any status), its sections and the
// derived model for read paths.
func (s *Service) notebookFor(ctx context.Context, a service.Actor, id string, act access.Action) (*store.Instance, *notebookModel, error) {
	in, err := s.owned(ctx, a, id, act)
	if err != nil {
		return nil, nil, err
	}
	secs, err := s.st.ListNotebookSections(ctx, in.ID)
	if err != nil {
		return nil, nil, service.Internal(err)
	}
	return in, buildNotebookModel(in, secs), nil
}

// Notebook returns the outline view. Read-only: works on running, lost,
// expired and deleted instances and never triggers restoration.
func (s *Service) Notebook(ctx context.Context, a service.Actor, id string) (*NotebookView, error) {
	in, m, err := s.notebookFor(ctx, a, id, access.View)
	if err != nil {
		return nil, err
	}
	// Executions in the current incarnation prove the restored process
	// is not verifiably empty even when no section ran (scratch fs
	// writes do not define tracked names or bump drift).
	execs, _ := s.st.ListExecutions(ctx, in.ID, 100)
	empty := restoredEmpty(in, m.states, execs)
	v := &NotebookView{
		InstanceID: in.ID, Status: in.Status, Incarnation: in.Incarnation,
		RestoredAt: in.RestoredAt, RestoredEmpty: empty,
		Revision: in.NotebookRev, Drift: in.Drift,
		Sections: []NotebookSectionMeta{},
	}
	var b strings.Builder
	fmt.Fprintf(&b, "instance %s %s · incarnation %d", in.ID, in.Status, in.Incarnation)
	if in.RestoredAt != nil {
		fmt.Fprintf(&b, " · restored %s — memory and files were emptied at restore", in.RestoredAt.UTC().Format(time.RFC3339))
		if empty {
			b.WriteString("; still empty, nothing has run this incarnation")
		} else {
			b.WriteString("; sections have run since, state reflects this incarnation")
		}
	}
	if in.Drift > 0 {
		fmt.Fprintf(&b, " · drift: %d execs outside the notebook changed globals", in.Drift)
	}
	fmt.Fprintf(&b, " · %d sections\n", len(m.states))
	for i, st := range m.states {
		meta := metaOf(st, m.providers[st.sec.Name])
		v.Sections = append(v.Sections, meta)
		fmt.Fprintf(&b, "%2d %-14s %-8s", i+1, meta.Name, meta.State)
		// defines/reads come from the same fresh analysis as the
		// graph: a use resolves to its provider section or "?".
		defs, uses := m.inputs[i].Defines, m.inputs[i].Uses
		if len(defs) > 0 {
			fmt.Fprintf(&b, " defines %s", strings.Join(defs, ", "))
		}
		if len(uses) > 0 {
			reads := make([]string, 0, len(uses))
			seenUse := map[string]bool{}
			for _, u := range uses {
				if seenUse[u] {
					continue
				}
				seenUse[u] = true
				if p, ok := m.reads[meta.Name][u]; ok {
					reads = append(reads, u+" ← "+p)
				} else {
					reads = append(reads, u+" ← ?")
				}
			}
			fmt.Fprintf(&b, " reads %s", strings.Join(reads, ", "))
		}
		if meta.Rerun == store.RerunOnce {
			b.WriteString(" (once")
			if meta.OnceRan {
				b.WriteString(", ran before")
			}
			b.WriteString(")")
		}
		if meta.Effects != nil && len(meta.Effects) > 0 {
			fmt.Fprintf(&b, " [%s]", strings.Join(meta.Effects, " "))
		}
		if meta.Description != "" {
			fmt.Fprintf(&b, " — %s", meta.Description)
		}
		b.WriteString("\n")
	}
	v.Outline = strings.TrimRight(b.String(), "\n")
	v.Next = s.nextSteps(in, m, empty)
	v.Graph = notebookGraphDTO(m.graph)
	return v, nil
}

// notebookGraphDTO converts the pure graph into the wire shape.
// Slices marshal as [] never null.
func notebookGraphDTO(g notebook.Graph) NotebookGraph {
	strs := func(s []string) []string {
		if s == nil {
			return []string{}
		}
		return s
	}
	out := NotebookGraph{Edges: []NotebookGraphEdge{}, Diagnostics: []NotebookGraphDiagnostic{}}
	for _, e := range g.Edges {
		out.Edges = append(out.Edges, NotebookGraphEdge{From: e.From, To: e.To, Name: e.Name})
	}
	for _, d := range g.Diagnostics {
		out.Diagnostics = append(out.Diagnostics, NotebookGraphDiagnostic{
			Kind: d.Kind, Message: d.Message,
			Sections: strs(d.Sections), Names: strs(d.Names),
		})
	}
	return out
}

// nextSteps computes the outline's trailing guidance: graph blocking
// diagnostics first, then the sections a stale run would pick (every
// non-fresh section, in dependency order), the restored-empty notice,
// once sections that already succeeded, and drift.
// restoredEmpty distinguishes a still-untouched restored process from
// one already rebuilding state.
func (s *Service) nextSteps(in *store.Instance, m *notebookModel, restoredEmpty bool) []string {
	next := []string{}
	for _, d := range m.graph.Diagnostics {
		if d.Kind == notebook.DiagParseError {
			// A parse failure does not block — the section runs and
			// fails with the real syntax error.
			next = append(next, d.Message)
			continue
		}
		next = append(next, "blocked: "+d.Message)
	}
	var nonFresh, notRun []string
	onceRan := []string{}
	for _, st := range m.states {
		if st.state != SectionFresh {
			nonFresh = append(nonFresh, st.sec.Name)
		}
		if st.state == SectionNotRun {
			notRun = append(notRun, st.sec.Name)
		}
		if st.sec.Rerun == store.RerunOnce && st.sec.SucceededBefore {
			onceRan = append(onceRan, st.sec.Name)
		}
	}
	if len(nonFresh) > 0 {
		next = append(next, fmt.Sprintf("run_sections(sections=\"stale\") runs %s in dependency order", strings.Join(nonFresh, ", ")))
	}
	if restoredEmpty && len(notRun) > 0 {
		next = append(next, fmt.Sprintf("the restored process is still empty; nothing was re-run for you — %d sections not_run: %s", len(notRun), strings.Join(notRun, ", ")))
	}
	if len(onceRan) > 0 {
		next = append(next, fmt.Sprintf("once sections already succeeded: %s — bulk runs skip them; run by name only if repeating is intended", strings.Join(onceRan, ", ")))
	}
	if in.Drift > 0 {
		next = append(next, fmt.Sprintf("drift: %d execs outside the notebook changed globals", in.Drift))
	}
	return next
}

// ReadSection returns one section's code and metadata, optionally with
// its last execution's output/error.
func (s *Service) ReadSection(ctx context.Context, a service.Actor, id, name string, includeOutput bool) (*SectionDetail, error) {
	if !validSectionName(name) {
		return nil, service.BadRequest("invalid section name")
	}
	in, m, err := s.notebookFor(ctx, a, id, access.View)
	if err != nil {
		return nil, err
	}
	var st *sectionState
	for i := range m.states {
		if m.states[i].sec.Name == name {
			st = &m.states[i]
			break
		}
	}
	if st == nil {
		return nil, service.NotFound("section not found")
	}
	d := &SectionDetail{Section: metaOf(*st, m.providers[st.sec.Name]), Code: st.sec.Code}
	if includeOutput && st.sec.LastExecID != "" {
		if ex, err := s.st.GetExecution(ctx, st.sec.LastExecID); err == nil && ex.InstanceID == in.ID {
			d.Output, d.OutputTrunc = ex.Output, ex.OutputTruncated
			if ex.ErrorMessage != "" {
				d.Error = ex.ErrorMessage
				if ex.ErrorBacktrace != "" {
					d.Error += "\n" + ex.ErrorBacktrace
				}
			}
		}
	}
	return d, nil
}

// --- write paths (running instances only; the op lease serializes) ---

// claimOp takes the per-instance operation lease. allowed restricts
// which statuses may be claimed. A claim refused because the row's
// status or TTL moved since the caller's read is classified against a
// fresh row — expired stays a typed 410, terminal states stay conflicts,
// never a 500.
func (s *Service) claimOp(ctx context.Context, id string, allowed ...types.InstanceStatus) (*store.Instance, string, error) {
	token := store.NewID(store.PrefixExecution) + "op"
	in, err := s.st.ClaimInstanceOp(ctx, id, token, s.opLeaseTTL(), allowed...)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			return nil, "", service.NotFound("instance not found")
		case errors.Is(err, store.ErrConflict):
			return nil, "", service.Conflict("instance is busy with another operation")
		case errors.Is(err, store.ErrNotClaimable):
			cur, gerr := s.st.GetInstance(ctx, id)
			if gerr != nil {
				if errors.Is(gerr, store.ErrNotFound) {
					return nil, "", service.NotFound("instance not found")
				}
				return nil, "", service.Internal(gerr)
			}
			return nil, "", s.unclaimableErr(ctx, cur)
		default:
			return nil, "", service.Internal(err)
		}
	}
	return in, token, nil
}

// unclaimableErr maps a fresh row that refused an op claim onto the
// typed error the caller should see: expired stays a typed 410, every
// other non-claimable state is a not-running conflict (a lost row is
// restored by the next exec/run, never by a write).
func (s *Service) unclaimableErr(ctx context.Context, in *store.Instance) error {
	if expiredForRun(in, s.now().UTC()) {
		return s.expiredErr(ctx, in)
	}
	return service.Errf(types.ErrCodeNotRunning, "instance not running")
}

// releaseOp best-effort releases the lease; commit/clear paths already
// drop it, so this mainly covers early returns. It runs on a bounded
// context detached from the request so a client disconnect cannot
// leave the instance busy until the lease TTL expires.
func (s *Service) releaseOp(ctx context.Context, id, token string) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	_ = s.st.ReleaseInstanceOp(rctx, id, token)
}

// leaseErr classifies a failed token-predicated store op (refresh,
// touch, bump) against the current row: an elapsed TTL stays a typed
// 410, everything else is a lease conflict.
func (s *Service) leaseErr(ctx context.Context, id string) error {
	if cur, err := s.st.GetInstance(ctx, id); err == nil && expiredForRun(cur, s.now().UTC()) {
		return s.expiredErr(ctx, cur)
	}
	return service.Conflict("instance operation lease lost; retry")
}

// expiredForRun reports whether the instance is past its TTL — whether
// the reaper already settled it or not.
func expiredForRun(in *store.Instance, now time.Time) bool {
	return in.Status == types.InstanceExpired ||
		(!in.ExpiresAt.IsZero() && !in.ExpiresAt.After(now))
}

// expiredErr builds the dedicated 410-style error naming what remains.
func (s *Service) expiredErr(ctx context.Context, in *store.Instance) error {
	n := 0
	if secs, err := s.st.ListNotebookSections(ctx, in.ID); err == nil {
		n = len(secs)
	}
	return service.Errf(types.ErrCodeInstanceExpired,
		"instance expired at %s and cannot be restored or run. Its notebook (%d sections) is read-only history; create a new instance to continue.",
		in.ExpiresAt.UTC().Format(time.RFC3339), n)
}

// prepareRun validates status and claims the op lease for a mutating
// call. On a lost instance it restores first and returns the typed
// instance_restored error — the triggering call never executes.
func (s *Service) prepareRun(ctx context.Context, a service.Actor, id string) (*store.Instance, string, error) {
	in, err := s.owned(ctx, a, id, access.Run)
	if err != nil {
		return nil, "", err
	}
	now := s.now().UTC()
	if expiredForRun(in, now) {
		return nil, "", s.expiredErr(ctx, in)
	}
	if in.Status == types.InstanceLost {
		if err := s.restore(ctx, a, in); err != nil {
			// A concurrent caller may have won the restore while we
			// waited on the lease: the instance is live again and this
			// call still did not run.
			if cur, gerr := s.st.GetInstance(ctx, in.ID); gerr == nil && cur.Status == types.InstanceRunning {
				return nil, "", instanceRestoredErr(ctx, s.st, cur)
			}
			return nil, "", err
		}
		return nil, "", instanceRestoredErr(ctx, s.st, in)
	}
	if in.Status != types.InstanceRunning {
		return nil, "", service.Errf(types.ErrCodeNotRunning, "instance not running")
	}
	cin, token, err := s.claimOp(ctx, id, types.InstanceRunning)
	if err != nil {
		return nil, "", err
	}
	// Another request may have restored the instance between our
	// status read and the claim: the claimed row is then a NEW empty
	// incarnation this caller never observed. Refuse rather than
	// execute silently on the replacement process.
	if cin.Incarnation != in.Incarnation || cin.LeaseEpoch != in.LeaseEpoch {
		s.releaseOp(ctx, id, token)
		return nil, "", instanceRestoredErr(ctx, s.st, cin)
	}
	return cin, token, nil
}

// instanceRestoredErr is the typed result every restore-triggering call
// returns: the process is empty, the call did not run, sections wait.
func instanceRestoredErr(ctx context.Context, st store.Store, in *store.Instance) error {
	secs, _ := st.ListNotebookSections(ctx, in.ID)
	names := make([]string, 0, len(secs))
	for _, sec := range secs {
		names = append(names, sec.Name)
	}
	when := "an unknown time"
	if in.RestoredAt != nil {
		when = in.RestoredAt.UTC().Format(time.RFC3339)
	} else if in.EndedAt != nil {
		when = in.EndedAt.UTC().Format(time.RFC3339)
	}
	msg := fmt.Sprintf("instance was restored at %s: the new process started EMPTY — memory and files from the lost process are gone. Nothing was re-run. Your call was NOT executed.", when)
	if len(names) > 0 {
		msg += fmt.Sprintf(" The notebook has %d sections, all not_run: %s. Re-run what you need with run_sections, then repeat your call.", len(names), strings.Join(names, ", "))
	} else {
		msg += " The notebook is empty. Repeat your call, or save sections first."
	}
	return service.Errf(types.ErrCodeInstanceRestored, "%s", msg)
}

// --- section save/delete ---

// validateSectionInput checks name/description/rerun/code bounds shared
// by save, exec-with-section and MCP add_section.
func validateSectionInput(name, description, rerun string, code *string) error {
	if !validSectionName(name) {
		return service.BadRequest("invalid section name %q (want [a-z][a-z0-9_]{0,47})", name)
	}
	if !validDescription(description) {
		return service.BadRequest("description must be one line of at most %d chars", maxSectionDescBytes)
	}
	if !validRerun(rerun) {
		return service.BadRequest("rerun must be \"safe\" or \"once\"")
	}
	if code != nil && len(*code) > MaxCodeBytes {
		return service.Errf(types.ErrCodeTooLarge, "code exceeds 256KB")
	}
	return nil
}

// saveSectionLocked upserts a section under the held op lease. rev is
// the notebook CAS expected by the caller (the claimed row's revision
// for internal writes).
func (s *Service) saveSectionLocked(ctx context.Context, in *store.Instance, a service.Actor, name string, req SaveSectionRequest) (*SaveSectionResult, error) {
	// Resolve the effective fields against any existing section.
	existing, gerr := s.st.GetNotebookSection(ctx, in.ID, name)
	if gerr != nil && !errors.Is(gerr, store.ErrNotFound) {
		return nil, service.Internal(gerr)
	}
	if req.CreateOnly && existing != nil {
		return nil, service.Errf(types.ErrCodeConflict, "section %q already exists; edit it instead of recreating", name)
	}
	code := ""
	if req.Code != nil {
		code = *req.Code
	} else if existing != nil {
		code = existing.Code
	}
	if req.FromExec != "" {
		if req.Code != nil {
			return nil, service.BadRequest("from_exec and code are mutually exclusive")
		}
		ex, err := s.st.GetExecution(ctx, req.FromExec)
		if err != nil {
			return nil, service.NotFound("execution not found")
		}
		if ex.InstanceID != in.ID {
			return nil, service.BadRequest("from_exec must reference an execution of this instance")
		}
		if ex.SectionName != "" {
			return nil, service.BadRequest("from_exec requires a scratch execution (no section)")
		}
		if ex.Status != types.ExecOK {
			return nil, service.BadRequest("from_exec requires a successful execution")
		}
		code = ex.Code
	}
	description := ""
	if req.Description != nil {
		description = *req.Description
	} else if existing != nil {
		description = existing.Description
	}
	rerun := store.RerunSafe
	if req.Rerun != nil {
		rerun = *req.Rerun
	} else if existing != nil && existing.Rerun != "" {
		rerun = existing.Rerun
	}
	if rerun == "" {
		rerun = store.RerunSafe
	}
	if err := validateSectionInput(name, description, rerun, &code); err != nil {
		return nil, err
	}
	// A section always carries a one-line description: required on
	// create, retained on edit (an explicit empty string cannot clear
	// it — the effective value must stay non-empty).
	if description == "" {
		return nil, service.BadRequest("description is required (one line, at most %d chars)", maxSectionDescBytes)
	}
	after := ""
	if req.After != nil {
		after = *req.After
	}
	if after == name {
		return nil, service.BadRequest("after cannot name the section itself")
	}
	defs, uses := s.analyzeSection(in, code)
	// Effects describe the last run's observed ops: an edit does not
	// erase them (the section reads stale regardless), so carry the
	// stored value forward on update.
	var effects []string
	if existing != nil && existing.Effects != nil {
		effects = existing.Effects
	} else {
		effects = []string{}
	}
	dependsOn, derr := s.sectionProviders(ctx, in.ID, name, uses)
	if derr != nil {
		return nil, service.Internal(derr)
	}
	row := &store.NotebookSection{
		InstanceID: in.ID, Name: name,
		Description: description, Code: code, CodeSHA256: codeSHA(code),
		Rerun: rerun, Defines: defs, Uses: uses, Effects: effects,
		DependsOn: dependsOn,
		CreatedBy: a.UserID, UpdatedBy: a.UserID, EditedAt: s.now().UTC(),
	}
	if existing != nil {
		row.CreatedBy = existing.CreatedBy
	}
	saved, rev, err := s.st.SaveNotebookSection(ctx, in.ID, req.Revision, row, after)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, service.Conflict("notebook changed since your revision; reload and retry")
		}
		if errors.Is(err, store.ErrNotFound) {
			return nil, service.BadRequest("after: unknown section %q", after)
		}
		return nil, service.Internal(err)
	}
	return &SaveSectionResult{Section: saved, Revision: rev}, nil
}

// sectionProviders maps each used name to the notebook sections that
// define it — the provenance staleness checks rely on. Every section
// (other than the saved one) defining a used name is recorded; compute
// states then validates position and provider state. Returns sorted,
// deduplicated names.
func (s *Service) sectionProviders(ctx context.Context, instanceID, self string, uses []string) ([]string, error) {
	if len(uses) == 0 {
		return []string{}, nil
	}
	secs, err := s.st.ListNotebookSections(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	useSet := map[string]bool{}
	for _, u := range uses {
		useSet[u] = true
	}
	seen := map[string]bool{}
	out := []string{}
	for _, sec := range secs {
		if sec.Name == self || seen[sec.Name] {
			continue
		}
		for _, d := range sec.Defines {
			if useSet[d] {
				seen[sec.Name] = true
				out = append(out, sec.Name)
				break
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// SaveSection edits a section without running it (REST path; revision
// required). Writes require a running instance — a lost notebook is
// read-only until restored by an exec/run.
func (s *Service) SaveSection(ctx context.Context, a service.Actor, id, name string, req SaveSectionRequest) (*SaveSectionResult, error) {
	in, token, err := s.prepareWrite(ctx, a, id)
	if err != nil {
		return nil, err
	}
	defer s.releaseOp(ctx, in.ID, token)
	res, err := s.saveSectionLocked(ctx, in, a, name, req)
	if err != nil {
		return nil, err
	}
	return res, nil
}

// AddSection creates a NEW section without running it, fetching the
// current notebook revision when the caller did not supply one (MCP
// path — agents do not track revisions). It is create-only: an
// existing name is an explicit conflict, never a silent overwrite of
// someone else's edit. Empty code is a valid section.
func (s *Service) AddSection(ctx context.Context, a service.Actor, id, name, description, code, rerun, after string) (*SaveSectionResult, error) {
	in0, err := s.owned(ctx, a, id, access.Run)
	if err != nil {
		return nil, err
	}
	if expiredForRun(in0, s.now().UTC()) {
		return nil, s.expiredErr(ctx, in0)
	}
	if in0.Status != types.InstanceRunning {
		return nil, service.Errf(types.ErrCodeNotRunning, "instance not running")
	}
	in, token, err := s.claimOp(ctx, id, types.InstanceRunning)
	if err != nil {
		return nil, err
	}
	defer s.releaseOp(ctx, in.ID, token)
	// Re-check under the lease: create-only means an existing name —
	// however it got there — is a conflict, and the revision is the
	// claimed row's current one.
	if _, gerr := s.st.GetNotebookSection(ctx, in.ID, name); gerr == nil {
		return nil, service.Errf(types.ErrCodeConflict, "section %q already exists; edit it with save or exec instead", name)
	} else if !errors.Is(gerr, store.ErrNotFound) {
		return nil, service.Internal(gerr)
	}
	req := SaveSectionRequest{Revision: in.NotebookRev, Code: &code, CreateOnly: true}
	if description != "" {
		req.Description = &description
	}
	if rerun != "" {
		req.Rerun = &rerun
	}
	if after != "" {
		req.After = &after
	}
	return s.saveSectionLocked(ctx, in, a, name, req)
}

// prepareWrite validates status for notebook writes: running only, no
// restoration. Returns the claimed instance and op token.
func (s *Service) prepareWrite(ctx context.Context, a service.Actor, id string) (*store.Instance, string, error) {
	in, err := s.owned(ctx, a, id, access.Run)
	if err != nil {
		return nil, "", err
	}
	if expiredForRun(in, s.now().UTC()) {
		return nil, "", s.expiredErr(ctx, in)
	}
	if in.Status != types.InstanceRunning {
		return nil, "", service.Errf(types.ErrCodeNotRunning, "instance not running")
	}
	return s.claimOp(ctx, id, types.InstanceRunning)
}

// DeleteSection removes a section under revision CAS and reports
// dependents that still read its defines.
func (s *Service) DeleteSection(ctx context.Context, a service.Actor, id, name string, revision int64) (*DeleteSectionResult, error) {
	if !validSectionName(name) {
		return nil, service.BadRequest("invalid section name")
	}
	in, token, err := s.prepareWrite(ctx, a, id)
	if err != nil {
		return nil, err
	}
	defer s.releaseOp(ctx, in.ID, token)
	affected, rev, err := s.st.DeleteNotebookSection(ctx, in.ID, name, revision)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, service.Conflict("notebook changed since your revision; reload and retry")
		}
		if errors.Is(err, store.ErrNotFound) {
			return nil, service.NotFound("section not found")
		}
		return nil, service.Internal(err)
	}
	if affected == nil {
		affected = []string{}
	}
	return &DeleteSectionResult{Affected: affected, Revision: rev}, nil
}

// --- exec ---

// ExecSection saves the optional section, plans the run against the
// dependency graph — non-fresh upstream sections execute first, each
// as its own exec — then runs the section itself. Sectionless calls
// are plain scratch execs, unchanged.
func (s *Service) ExecSection(ctx context.Context, a service.Actor, id, code string, timeout time.Duration, sec *SectionInput) (*ExecOutcome, error) {
	if len(code) > MaxCodeBytes {
		return nil, service.Errf(types.ErrCodeTooLarge, "code exceeds 256KB")
	}
	in, token, err := s.prepareRun(ctx, a, id)
	if err != nil {
		return nil, err
	}
	defer s.releaseOp(ctx, in.ID, token)
	now := s.now().UTC()
	deadline := renewal(in, now)
	rev := in.NotebookRev
	drift := in.Drift

	// Sectioned exec: persist the section (success or failure) before
	// running, so the intention and its state are always recorded.
	var secName string
	var runSeq int64
	var derivedState string
	var upstream []RunSectionResult
	if sec != nil {
		if sec.Revision != nil && *sec.Revision != in.NotebookRev {
			return nil, service.Conflict("notebook changed since your revision; reload and retry")
		}
		sreq := SaveSectionRequest{Revision: in.NotebookRev, Code: &code}
		if sec.Description != "" {
			sreq.Description = &sec.Description
		}
		if sec.Rerun != "" {
			sreq.Rerun = &sec.Rerun
		}
		if sec.After != "" {
			sreq.After = &sec.After
		}
		sres, err := s.saveSectionLocked(ctx, in, a, sec.Name, sreq)
		if err != nil {
			return nil, err
		}
		// The saved row carries the code just stored.
		code = sres.Section.Code
		secName = sec.Name
		rev = sres.Revision

		// Plan against the dependency graph: the section plus every
		// non-fresh upstream, in dependency order. A blocking
		// diagnostic refuses the whole run — the save stands.
		secs, err := s.st.ListNotebookSections(ctx, in.ID)
		if err != nil {
			return nil, service.Internal(err)
		}
		m := buildNotebookModel(in, secs)
		fresh := map[string]bool{}
		for _, st := range m.states {
			if st.state == SectionFresh {
				fresh[st.sec.Name] = true
			}
		}
		plan := m.graph.Plan(notebook.PlanInput{Targets: []string{secName}, Fresh: fresh})
		if len(plan.Blocked) > 0 {
			return nil, graphBlockedErr(plan.Blocked, "section saved; ")
		}
		stateByName := map[string]*sectionState{}
		for i := range m.states {
			stateByName[m.states[i].sec.Name] = &m.states[i]
		}
		for _, pn := range plan.Nodes {
			if pn.Name == secName {
				continue // the section itself runs below
			}
			switch pn.Action {
			case notebook.ActionReuse:
				upstream = append(upstream, RunSectionResult{Name: pn.Name, Status: "reused", Reason: pn.Reason})
				continue
			case notebook.ActionSkip:
				upstream = append(upstream, RunSectionResult{Name: pn.Name, Status: "skipped", Reason: pn.Reason, SkippedReason: pn.Reason})
				continue
			}
			uresp, uerr := s.execPlannedSection(ctx, in, a, stateByName[pn.Name].sec, token)
			if uerr != nil {
				if errors.Is(uerr, runtime.ErrNotFound) {
					return nil, s.lostAndRestore(ctx, a, in, token, s.now().UTC())
				}
				return nil, fail(uerr)
			}
			ures := &uresp.Result
			if ures.Error != nil {
				return nil, service.Errf(types.ErrCodeUpstreamFailed,
					"section saved; upstream %s failed: %s", pn.Name, ures.Error.Message)
			}
			upstream = append(upstream, RunSectionResult{
				Name: pn.Name, Status: "ok", ExecID: ures.ExecID,
				Reason: pn.Reason, DurationMs: ures.DurationMs,
			})
		}
		// Upstream execs slid the heartbeat/expiry forward — recompute
		// so the target exec and final touch never regress it.
		now = s.now().UTC()
		deadline = renewal(in, now)
		runSeq, err = s.st.BumpRunSeq(ctx, in.ID, token)
		if err != nil {
			if errors.Is(err, store.ErrConflict) {
				return nil, s.leaseErr(ctx, in.ID)
			}
			return nil, service.Internal(err)
		}
	}

	resp, err := s.rt.Exec(ctx, &runtime.ExecRequest{
		InstanceID: in.ID, Owner: runtime.Owner{UserID: in.UserID},
		Actor: actorOwner(a), ExecID: store.NewID(store.PrefixExecution),
		Code: code, SourceName: secName,
		TimeoutMs: timeout.Milliseconds(), RenewedExpiresAt: deadline,
		Epoch: in.LeaseEpoch,
	})
	if resp != nil {
		s.audit.Record(resp.Audit)
	}
	if err != nil {
		if errors.Is(err, runtime.ErrNotFound) {
			return nil, s.lostAndRestore(ctx, a, in, token, now)
		}
		return nil, fail(err)
	}
	res := &resp.Result
	s.recordExecutionSection(ctx, in, a, code, res, secName, runSeq)

	// Post-run bookkeeping under the lease.
	if secName != "" {
		status := "ok"
		if res.Error != nil {
			status = "error"
		}
		if _, err := s.st.StampSectionRun(ctx, in.ID, secName, res.ExecID, status,
			in.Incarnation, runSeq, now, execEffects(resp.Audit), res.Error == nil, codeSHA(code), token); err != nil {
			if errors.Is(err, store.ErrConflict) {
				return nil, s.leaseErr(ctx, in.ID)
			}
			return nil, service.Internal(err)
		}
		// Refresh revision and derive the section's true post-run state
		// — a success on stale upstream inputs reads stale, not fresh.
		if cur, gerr := s.st.GetInstance(ctx, in.ID); gerr == nil {
			rev = cur.NotebookRev
			in = cur
		}
		if secs, lerr := s.st.ListNotebookSections(ctx, in.ID); lerr == nil {
			m2 := buildNotebookModel(in, secs)
			for _, cst := range m2.states {
				if cst.sec.Name == secName {
					derivedState = cst.state
					break
				}
			}
		}
	} else {
		// Scratch drift: code that binds top-level names and actually
		// ran (not a parse failure) counts once per exec.
		defs, _ := s.analyzeSection(in, code)
		delta := int64(0)
		if len(defs) > 0 && (res.Error == nil || res.Error.Type != types.ErrSyntax) {
			delta = 1
		}
		drift = in.Drift + delta
	}
	touched, err := s.st.TouchInstanceLive(ctx, in.ID, token, now, deadline, drift-in.Drift)
	if err != nil {
		return nil, service.Internal(err)
	}
	if !touched {
		return nil, s.leaseErr(ctx, in.ID)
	}
	out := &ExecOutcome{View: res, Revision: rev, Drift: drift, Section: secName, Upstream: upstream}
	if secName != "" {
		out.State = derivedState
		if out.State == "" {
			out.State = SectionFailed
			if res.Error == nil {
				out.State = SectionFresh
			}
		}
	}
	return out, nil
}

// recordExecutionSection persists the execution record with notebook
// provenance (section name, incarnation, run seq is implied by section
// stamps; executions keep section+incarnation per the schema).
func (s *Service) recordExecutionSection(ctx context.Context, in *store.Instance, a service.Actor, code string, res *runtime.ExecResult, section string, runSeq int64) {
	sum := sha256.Sum256([]byte(code))
	snippet := code
	if len(snippet) > snippetBytes {
		snippet = snippet[:snippetBytes]
	}
	status := types.ExecOK
	var errType types.ExecErrorType
	var errMsg, errTrace string
	if res.Error != nil {
		status = types.ExecError
		errType = res.Error.Type
		errMsg, _ = capStored(res.Error.Message)
		errTrace, _ = capStored(res.Error.Backtrace)
	}
	output, outputTruncated := capStored(res.Output)
	_ = s.st.CreateExecution(ctx, &store.Execution{
		ID: res.ExecID, InstanceID: in.ID, UserID: a.UserID,
		CodeSHA256: hex.EncodeToString(sum[:]), CodeSnippet: snippet, Code: code,
		Status: status, ErrorType: errType, DurationMs: res.DurationMs,
		Steps: res.Steps, OutputBytes: int64(len(res.Output)),
		Via: a.Kind, Output: output, OutputTruncated: outputTruncated,
		ErrorMessage: errMsg, ErrorBacktrace: errTrace,
		SectionName: section, Incarnation: in.Incarnation,
	})
}

// execEffects extracts the distinct capability.op names observed in an
// audit batch — metadata only, never arguments.
func execEffects(batch runtime.AuditBatch) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, ev := range batch.Events {
		k := string(ev.Capability) + "." + string(ev.Op)
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	sort.Strings(out)
	if len(out) > 32 {
		out = out[:32]
	}
	return out
}

// execPlannedSection runs one planned section as its own exec under
// the held op lease — the same sequence RunSections uses per section:
// lease refresh, run seq bump, exec, heartbeat touch, execution
// record, section stamp. The caller classifies runtime.ErrNotFound
// (dead child); every other failure is already a typed service error.
func (s *Service) execPlannedSection(ctx context.Context, in *store.Instance, a service.Actor, sec *store.NotebookSection, token string) (*runtime.ExecResponse, error) {
	if ok, rerr := s.st.RefreshInstanceOp(ctx, in.ID, token, s.opLeaseTTL()); rerr != nil {
		return nil, service.Internal(rerr)
	} else if !ok {
		return nil, s.leaseErr(ctx, in.ID)
	}
	runSeq, err := s.st.BumpRunSeq(ctx, in.ID, token)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, s.leaseErr(ctx, in.ID)
		}
		return nil, service.Internal(err)
	}
	now := s.now().UTC()
	deadline := renewal(in, now)
	resp, err := s.rt.Exec(ctx, &runtime.ExecRequest{
		InstanceID: in.ID, Owner: runtime.Owner{UserID: in.UserID},
		Actor: actorOwner(a), ExecID: store.NewID(store.PrefixExecution),
		Code: sec.Code, SourceName: sec.Name,
		TimeoutMs: 0, RenewedExpiresAt: deadline, Epoch: in.LeaseEpoch,
	})
	if resp != nil {
		s.audit.Record(resp.Audit)
	}
	if err != nil {
		return resp, err
	}
	res := &resp.Result
	if touched, terr := s.st.TouchInstanceLive(ctx, in.ID, token, now, deadline, 0); terr != nil {
		return resp, service.Internal(terr)
	} else if !touched {
		return resp, s.leaseErr(ctx, in.ID)
	}
	in.LastActiveAt, in.ExpiresAt = now, deadline
	s.recordExecutionSection(ctx, in, a, sec.Code, res, sec.Name, runSeq)
	status := "ok"
	if res.Error != nil {
		status = "error"
	}
	if _, serr := s.st.StampSectionRun(ctx, in.ID, sec.Name, res.ExecID, status,
		in.Incarnation, runSeq, now, execEffects(resp.Audit), res.Error == nil, sec.CodeSHA256, token); serr != nil {
		if errors.Is(serr, store.ErrConflict) {
			return resp, s.leaseErr(ctx, in.ID)
		}
		return resp, service.Internal(serr)
	}
	return resp, nil
}

// lostAndRestore handles a definitively dead child under the held op
// lease: mark lost, restore the empty process, return the typed
// instance_restored error. A lost CAS means the row moved on — the
// outcome is classified against a fresh row, never restored blindly.
func (s *Service) lostAndRestore(ctx context.Context, a service.Actor, in *store.Instance, token string, now time.Time) error {
	marked, merr := s.st.MarkInstanceLost(ctx, in.ID, token, now)
	if merr != nil {
		return service.Internal(merr)
	}
	if !marked {
		if cur, gerr := s.st.GetInstance(ctx, in.ID); gerr == nil {
			if expiredForRun(cur, s.now().UTC()) {
				return s.expiredErr(ctx, cur)
			}
			if cur.Status == types.InstanceLost {
				// Another op marked it lost first; restore now.
				in = cur
			} else {
				return service.Conflict("instance changed while executing; retry")
			}
		} else {
			return service.Conflict("instance changed while executing; retry")
		}
	} else if lost, gerr := s.st.GetInstance(ctx, in.ID); gerr == nil {
		in = lost
	}
	if rerr := s.restoreLocked(ctx, a, in, token); rerr != nil {
		return rerr
	}
	return instanceRestoredErr(ctx, s.st, in)
}

// graphBlockedErr is the typed refusal for a run whose closure touches
// blocking graph diagnostics: nothing executes. prefix lets the
// sectioned-exec path note that its save already stands.
func graphBlockedErr(diags []notebook.Diagnostic, prefix string) error {
	msgs := make([]string, 0, len(diags))
	for _, d := range diags {
		msgs = append(msgs, d.Message)
	}
	return service.Errf(types.ErrCodeGraphBlocked, "%srun blocked — %s", prefix, strings.Join(msgs, "; "))
}

// --- run ---

// RunSections executes the sections a selector resolves to, in
// dependency order: a section first runs every upstream section that
// is not fresh, each as its own exec. Bulk selections ("all", "stale")
// skip once sections that already succeeded unless they are needed
// upstream; explicit names always run. Execution stops at the first
// script failure; unreached run nodes report not_run. dryRun returns
// the report the run would produce — planned/reused/skipped — without
// executing, touching the lease, or restoring.
func (s *Service) RunSections(ctx context.Context, a service.Actor, id string, sel SectionSelector, revision *int64, dryRun bool) (*RunReport, error) {
	if dryRun {
		return s.planRun(ctx, a, id, sel, revision)
	}
	in, token, err := s.prepareRun(ctx, a, id)
	if err != nil {
		return nil, err
	}
	defer s.releaseOp(ctx, in.ID, token)
	if revision != nil && *revision != in.NotebookRev {
		return nil, service.Conflict("notebook changed since your revision; reload and retry")
	}
	secs, err := s.st.ListNotebookSections(ctx, in.ID)
	if err != nil {
		return nil, service.Internal(err)
	}
	m := buildNotebookModel(in, secs)
	targets, bulk, err := runTargets(m.states, sel)
	if err != nil {
		return nil, err
	}
	plan := m.graph.Plan(planInput(m.states, targets, bulk))
	if len(plan.Blocked) > 0 {
		return nil, graphBlockedErr(plan.Blocked, "")
	}
	report := &RunReport{
		InstanceID: in.ID, Incarnation: in.Incarnation,
		Revision: in.NotebookRev, Drift: in.Drift, Results: []RunSectionResult{},
	}
	// deadline/lastActive track the heartbeat written for the most
	// recently reached section; the final touch reuses them.
	var deadline, lastActive time.Time
	// executed records whether any section produced a definitive
	// runtime response — a mid-batch loss after that point is an
	// honest partial report, not a clean restore error.
	executed := false
	stopped := false
	stateByName := map[string]*sectionState{}
	for i := range m.states {
		stateByName[m.states[i].sec.Name] = &m.states[i]
	}
	for _, pn := range plan.Nodes {
		row := RunSectionResult{Name: pn.Name, Reason: pn.Reason}
		switch pn.Action {
		case notebook.ActionSkip:
			row.Status = "skipped"
			row.SkippedReason = pn.Reason
			report.Results = append(report.Results, row)
			continue
		case notebook.ActionReuse:
			row.Status = "reused"
			report.Results = append(report.Results, row)
			continue
		}
		if stopped {
			row.Status = "not_run"
			report.Results = append(report.Results, row)
			continue
		}
		st := stateByName[pn.Name]
		// Refresh the lease AND the sliding heartbeat/expiry for every
		// reached section: a long batch must not outlive either, and a
		// lost lease means the instance was settled or claimed
		// elsewhere — stop honestly.
		if ok, rerr := s.st.RefreshInstanceOp(ctx, in.ID, token, s.opLeaseTTL()); rerr != nil {
			return nil, service.Internal(rerr)
		} else if !ok {
			return nil, s.leaseErr(ctx, in.ID)
		}
		runSeq, err := s.st.BumpRunSeq(ctx, in.ID, token)
		if err != nil {
			if errors.Is(err, store.ErrConflict) {
				return nil, s.leaseErr(ctx, in.ID)
			}
			return nil, service.Internal(err)
		}
		lastActive = s.now().UTC()
		deadline = renewal(in, lastActive)
		// The renewed TTL is persisted only AFTER a definitive exec
		// response below — touching before rt.Exec would extend
		// expires_at on a runtime that turns out to be dead, and a
		// restoration must reuse the original expiry byte-for-byte.
		// in.ExpiresAt stays untouched until then for the same reason.
		execAt := lastActive
		resp, err := s.rt.Exec(ctx, &runtime.ExecRequest{
			InstanceID: in.ID, Owner: runtime.Owner{UserID: in.UserID},
			Actor: actorOwner(a), ExecID: store.NewID(store.PrefixExecution),
			Code: st.sec.Code, SourceName: st.sec.Name,
			TimeoutMs: 0, RenewedExpiresAt: deadline, Epoch: in.LeaseEpoch,
		})
		if resp != nil {
			s.audit.Record(resp.Audit)
		}
		if err != nil {
			if errors.Is(err, runtime.ErrNotFound) {
				marked, merr := s.st.MarkInstanceLost(ctx, in.ID, token, execAt)
				if merr != nil {
					return nil, service.Internal(merr)
				}
				if !marked {
					if cur, gerr := s.st.GetInstance(ctx, in.ID); gerr == nil {
						if expiredForRun(cur, s.now().UTC()) {
							return nil, s.expiredErr(ctx, cur)
						}
						if cur.Status == types.InstanceLost {
							in = cur
						} else {
							return nil, service.Conflict("instance changed while executing; retry")
						}
					} else {
						return nil, service.Conflict("instance changed while executing; retry")
					}
				} else if lost, gerr := s.st.GetInstance(ctx, in.ID); gerr == nil {
					in = lost
				}
				if rerr := s.restoreLocked(ctx, a, in, token); rerr != nil {
					return nil, rerr
				}
				if !executed {
					// Nothing ran yet: the typed restore error stands.
					return nil, instanceRestoredErr(ctx, s.st, in)
				}
				// Earlier sections already executed in the previous
				// runtime: report the partial result honestly — their
				// side effects happened and must not be blindly retried.
				report.Restored = true
				report.StoppedReason = "instance_restored"
				report.Warnings = append(report.Warnings,
					"instance was restored during the run: earlier sections executed in the previous runtime; do not blindly retry side effects")
				stopped = true
				row.Status = "not_run"
				report.Results = append(report.Results, row)
				continue
			}
			return nil, fail(err)
		}
		res := &resp.Result
		executed = true
		// Definitive response received: NOW persist the renewed TTL
		// and heartbeat for this section.
		if touched, terr := s.st.TouchInstanceLive(ctx, in.ID, token, lastActive, deadline, 0); terr != nil {
			return nil, service.Internal(terr)
		} else if !touched {
			return nil, s.leaseErr(ctx, in.ID)
		}
		in.LastActiveAt, in.ExpiresAt = lastActive, deadline
		s.recordExecutionSection(ctx, in, a, st.sec.Code, res, st.sec.Name, runSeq)
		status := "ok"
		var errMsg string
		if res.Error != nil {
			status = "error"
			errMsg = res.Error.Message
			if res.Error.Backtrace != "" {
				errMsg += "\n" + res.Error.Backtrace
			}
		}
		effects := execEffects(resp.Audit)
		if _, err := s.st.StampSectionRun(ctx, in.ID, st.sec.Name, res.ExecID, status,
			in.Incarnation, runSeq, execAt, effects, res.Error == nil, st.sec.CodeSHA256, token); err != nil {
			if errors.Is(err, store.ErrConflict) {
				return nil, s.leaseErr(ctx, in.ID)
			}
			return nil, service.Internal(err)
		}
		row.Status = status
		row.ExecID = res.ExecID
		row.Error = errMsg
		row.DurationMs = res.DurationMs
		report.Results = append(report.Results, row)
		if res.Error != nil {
			// Stop at the first failure: remaining run nodes report
			// not_run; reuse/skip rows still describe the plan.
			stopped = true
		}
	}
	// Re-read for the final revision/drift.
	if cur, err := s.st.GetInstance(ctx, in.ID); err == nil {
		report.Revision = cur.NotebookRev
		report.Drift = cur.Drift
		report.Incarnation = cur.Incarnation
	}
	if report.Restored {
		// The restore commit already dropped our op lease — no final
		// touch is possible or needed.
		return report, nil
	}
	// Nothing was reached (all skipped/reused): still touch with a
	// fresh heartbeat so the lease release below stays clean.
	if deadline.IsZero() {
		lastActive = s.now().UTC()
		deadline = renewal(in, lastActive)
	}
	if touched, err := s.st.TouchInstanceLive(ctx, in.ID, token, lastActive, deadline, 0); err != nil {
		return nil, service.Internal(err)
	} else if !touched {
		return nil, s.leaseErr(ctx, in.ID)
	}
	return report, nil
}

// planRun is the dry-run path: resolve the selector, build the plan,
// and report what a real run would do — planned/reused/skipped —
// without executing, claiming the op lease, touching the heartbeat, or
// restoring a lost instance.
func (s *Service) planRun(ctx context.Context, a service.Actor, id string, sel SectionSelector, revision *int64) (*RunReport, error) {
	in, err := s.owned(ctx, a, id, access.Run)
	if err != nil {
		return nil, err
	}
	if expiredForRun(in, s.now().UTC()) {
		return nil, s.expiredErr(ctx, in)
	}
	if in.Status != types.InstanceRunning && in.Status != types.InstanceLost {
		return nil, service.Errf(types.ErrCodeNotRunning, "instance not running")
	}
	if revision != nil && *revision != in.NotebookRev {
		return nil, service.Conflict("notebook changed since your revision; reload and retry")
	}
	secs, err := s.st.ListNotebookSections(ctx, in.ID)
	if err != nil {
		return nil, service.Internal(err)
	}
	m := buildNotebookModel(in, secs)
	targets, bulk, err := runTargets(m.states, sel)
	if err != nil {
		return nil, err
	}
	plan := m.graph.Plan(planInput(m.states, targets, bulk))
	if len(plan.Blocked) > 0 {
		return nil, graphBlockedErr(plan.Blocked, "")
	}
	report := &RunReport{
		InstanceID: in.ID, Incarnation: in.Incarnation,
		Revision: in.NotebookRev, Drift: in.Drift, Results: []RunSectionResult{},
	}
	for _, pn := range plan.Nodes {
		row := RunSectionResult{Name: pn.Name, Reason: pn.Reason}
		switch pn.Action {
		case notebook.ActionRun:
			row.Status = "planned"
		case notebook.ActionReuse:
			row.Status = "reused"
		case notebook.ActionSkip:
			row.Status = "skipped"
			row.SkippedReason = pn.Reason
		}
		report.Results = append(report.Results, row)
	}
	return report, nil
}

// planInput maps derived states onto the planner's input: Fresh is
// state == fresh; OnceDone is rerun == once with a prior success.
func planInput(states []sectionState, targets []string, bulk bool) notebook.PlanInput {
	in := notebook.PlanInput{
		Targets:  targets,
		Bulk:     bulk,
		Fresh:    map[string]bool{},
		OnceDone: map[string]bool{},
	}
	for _, st := range states {
		if st.state == SectionFresh {
			in.Fresh[st.sec.Name] = true
		}
		if st.sec.Rerun == store.RerunOnce && st.sec.SucceededBefore {
			in.OnceDone[st.sec.Name] = true
		}
	}
	return in
}

// runTargets resolves a selector into target section names plus the
// bulk flag. "all" targets every section; "stale" targets every
// non-fresh section (stale, failed, not_run); names are explicit and
// validated. Bulk marks the once rule.
func runTargets(states []sectionState, sel SectionSelector) ([]string, bool, error) {
	switch {
	case sel.All:
		names := make([]string, 0, len(states))
		for _, st := range states {
			names = append(names, st.sec.Name)
		}
		return names, true, nil
	case sel.Stale:
		var names []string
		for _, st := range states {
			if st.state != SectionFresh {
				names = append(names, st.sec.Name)
			}
		}
		return names, true, nil
	case len(sel.Names) > 0:
		byName := map[string]bool{}
		for _, st := range states {
			byName[st.sec.Name] = true
		}
		seen := map[string]bool{}
		var names []string
		for _, n := range sel.Names {
			if !byName[n] {
				return nil, false, service.BadRequest("unknown section %q", n)
			}
			if !seen[n] {
				seen[n] = true
				names = append(names, n)
			}
		}
		return names, false, nil
	default:
		return nil, false, service.BadRequest("sections must be \"all\", \"stale\", a name, or a list of names")
	}
}

// --- restoration ---

// restore brings a lost instance back as a fresh empty process under
// the same id, spec and expiry. It executes nothing. The lease is
// always claimed here — even when the row still carries an expired
// token — so a crashed earlier attempt cannot wedge restoration.
func (s *Service) restore(ctx context.Context, a service.Actor, in *store.Instance) error {
	claimed, token, err := s.claimOp(ctx, in.ID, types.InstanceLost)
	if err != nil {
		// A concurrent caller may have won the restore while we waited:
		// the row is running again and this call still did not run.
		if cur, gerr := s.st.GetInstance(ctx, in.ID); gerr == nil && cur.Status == types.InstanceRunning {
			return instanceRestoredErr(ctx, s.st, cur)
		}
		return err
	}
	defer func() {
		// Pre-create failures leave the row lost with the lease
		// released; commit/clear already drop it on later paths.
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		_ = s.st.ReleaseInstanceOp(rctx, in.ID, token)
	}()
	return s.restoreLocked(ctx, a, claimed, token)
}

// restoreLocked performs the restoration while the caller holds the op
// lease on a lost instance (token may be "" only when the row was
// claimed by this same call — see restore).
func (s *Service) restoreLocked(ctx context.Context, a service.Actor, in *store.Instance, token string) error {
	now := s.now().UTC()
	if expiredForRun(in, now) {
		return s.expiredErr(ctx, in)
	}
	if in.Status != types.InstanceLost {
		return service.Errf(types.ErrCodeConflict, "instance is not lost")
	}
	// Re-check live-instance quotas for the ORIGINAL owner/key: a lost
	// instance is not counted live, so restoring may exceed a cap the
	// owner filled while it was gone.
	if s.maxPerUser > 0 {
		live, err := s.st.ListInstances(ctx, store.InstanceFilter{WorkspaceID: in.WorkspaceID, UserID: in.UserID, Status: types.InstanceRunning})
		if err != nil {
			return service.Internal(err)
		}
		if len(live) >= s.maxPerUser {
			return service.Errf(types.ErrCodeTooMany, "per-user instance limit reached")
		}
	}
	if in.KeyID != "" {
		if key, err := s.st.GetAPIKey(ctx, in.KeyID); err == nil && key != nil && key.MaxInstances > 0 {
			live, err := s.st.ListInstances(ctx, store.InstanceFilter{WorkspaceID: in.WorkspaceID, Status: types.InstanceRunning, KeyID: in.KeyID})
			if err != nil {
				return service.Internal(err)
			}
			if len(live) >= key.MaxInstances {
				return service.Errf(types.ErrCodeTooMany, "restricted key instance limit reached")
			}
		}
	}
	// Re-resolve the stored spec's references: secrets, policies,
	// modules. The spec bytes themselves are sent verbatim — nothing is
	// re-pinned or rewritten.
	var spec runtime.Spec
	if err := json.Unmarshal(in.Spec, &spec); err != nil {
		return service.Errf(types.ErrCodeBadSpec, "stored spec unreadable: %s", err)
	}
	for name, ss := range spec.Secrets {
		if ss.Ref == "" {
			return service.Errf(types.ErrCodeBadSpec,
				"instance cannot be restored: secret %s was an inline value, which is not recoverable", name)
		}
	}
	_, mods, err := s.selectedModules(ctx, in.WorkspaceID, in.Spec)
	if err != nil {
		return service.Errf(types.ErrCodeBadSpec, "instance cannot be restored: %s", err.Error())
	}
	userPols, err := s.selectedPolicies(ctx, in.WorkspaceID, spec.Policies)
	if err != nil {
		return service.Errf(types.ErrCodeBadSpec, "instance cannot be restored: %s", err.Error())
	}
	resolved, _, err := s.secrets.Resolve(ctx, in.WorkspaceID, spec.Secrets)
	if err != nil {
		return service.Errf(types.ErrCodeBadSpec, "instance cannot be restored: %s", err.Error())
	}
	ownerEmail := ""
	if u, err := s.st.GetUser(ctx, in.UserID); err == nil && u != nil {
		ownerEmail = u.Email
	}
	incarnation := in.Incarnation + 1
	if incarnation < 2 {
		incarnation = 2
	}
	epoch := in.LeaseEpoch + 1
	cresp, err := s.rt.Create(ctx, &runtime.CreateRequest{
		InstanceID: in.ID,
		Owner:      runtime.Owner{UserID: in.UserID, Email: ownerEmail},
		Labels:     in.Labels,
		Spec:       in.Spec,
		Policies:   runtime.PolicyBundle{Global: s.globalPols, User: userPols},
		Modules:    mods,
		Secrets:    resolved,
		ExpiresAt:  in.ExpiresAt, // unchanged: restoration never renews TTL
		Epoch:      epoch,
	})
	if err != nil {
		if token != "" {
			_ = s.st.ClearRestoreBinding(ctx, in.ID, token)
		}
		return fail(err)
	}
	nodeID, outEpoch := "", epoch
	if cresp != nil {
		nodeID = cresp.NodeID
		if cresp.Epoch != 0 {
			outEpoch = cresp.Epoch
		}
	}
	ok, err := s.st.CommitRestore(ctx, in.ID, token, nodeID, outEpoch, incarnation, now)
	if err != nil {
		_, _ = s.rt.Delete(ctx, &runtime.DeleteRequest{InstanceID: in.ID, Owner: runtime.Owner{UserID: in.UserID}, Epoch: outEpoch})
		return service.Internal(err)
	}
	if !ok {
		// The row went terminal or expired while the process was being
		// created: fence the new child with exactly its epoch and drop
		// it — a late restore must never resurrect a dead instance.
		_, _ = s.rt.Delete(ctx, &runtime.DeleteRequest{InstanceID: in.ID, Owner: runtime.Owner{UserID: in.UserID}, Epoch: outEpoch})
		return service.Conflict("instance changed while restoring; retry")
	}
	return nil
}
