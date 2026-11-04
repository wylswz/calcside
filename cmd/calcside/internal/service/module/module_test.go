package module

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	_ "calcside/cmd/calcside/internal/gormstore"
	"calcside/cmd/calcside/internal/service"
	"calcside/cmd/calcside/internal/service/access"
	"calcside/internal/capability"
	mod "calcside/internal/module"
	"calcside/internal/store"
	"calcside/internal/store/storetest"
	"calcside/internal/types"
)

func newSvc(t *testing.T) (*Service, store.Store, service.Actor, service.Actor) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, "sqlite", storetest.SQLite(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	u, err := st.UpsertUserByEmail(ctx, "mod@x.com", "", "")
	if err != nil {
		t.Fatal(err)
	}
	u2, err := st.UpsertUserByEmail(ctx, "mod2@x.com", "", "")
	if err != nil {
		t.Fatal(err)
	}
	reg := capability.NewRegistry()
	s, err := New(Options{Store: st, Access: access.New(st), Registry: reg})
	if err != nil {
		t.Fatal(err)
	}
	a1 := service.Actor{UserID: u.ID, WorkspaceID: store.PersonalWorkspaceID(u.ID), Kind: types.AuthSession}
	a2 := service.Actor{UserID: u2.ID, WorkspaceID: store.PersonalWorkspaceID(u2.ID), Kind: types.AuthSession}
	return s, st, a1, a2
}

func goodFiles() map[string]string {
	return map[string]string{"u.star": "x = 1"}
}

func TestModuleCRUD(t *testing.T) {
	s, _, a, other := newSvc(t)
	ctx := context.Background()
	v, err := s.Create(ctx, a, CreateRequest{Name: "strkit", Files: goodFiles()})
	if err != nil {
		t.Fatal(err)
	}
	if v.DraftVersion != 1 || v.LatestVersion != 0 {
		t.Fatalf("view: %+v", v)
	}
	got, err := s.Get(ctx, a, v.ID)
	if err != nil || got.Name != "strkit" || got.Files["u.star"] != "x = 1" {
		t.Fatalf("get: %v %+v", err, got)
	}
	// Cross-workspace invisible.
	if _, err := s.Get(ctx, other, v.ID); err == nil {
		t.Fatal("other workspace can read")
	}
	// Draft CAS.
	if _, err := s.Update(ctx, a, v.ID, UpdateRequest{DraftVersion: 99, Files: goodFiles()}); err == nil {
		t.Fatal("stale draft accepted")
	}
	desc := "d"
	up, err := s.Update(ctx, a, v.ID, UpdateRequest{DraftVersion: 1, Description: &desc,
		Files: map[string]string{"u.star": "x = 2"}})
	if err != nil {
		t.Fatal(err)
	}
	if up.Description != "d" || up.Files["u.star"] != "x = 2" || up.DraftVersion != 2 {
		t.Fatalf("update: %+v", up)
	}
	// List scopes to workspace.
	l, err := s.List(ctx, a)
	if err != nil || len(l) != 1 {
		t.Fatalf("list: %v %v", err, l)
	}
	if l2, err := s.List(ctx, other); err != nil || len(l2) != 0 {
		t.Fatalf("cross-ws list: %v %v", err, l2)
	}
}

func TestModuleSourceValidation(t *testing.T) {
	s, _, a, _ := newSvc(t)
	ctx := context.Background()
	// Unknown load fails with the file named.
	if err := s.Validate(ctx, a, map[string]string{"u.star": `load("nope.star", "x")`}); err == nil || !strings.Contains(err.Error(), "u.star") {
		t.Fatalf("missing load: %v", err)
	}
	// @ load rejected.
	if err := s.Validate(ctx, a, map[string]string{"u.star": `load("@x/u.star", "x")`}); err == nil {
		t.Fatal("@ load accepted")
	}
	// stdlib/predeclared names compile.
	if err := s.Validate(ctx, a, map[string]string{"u.star": "x = env.get(\"A\") + str(len(\"ab\"))"}); err != nil {
		t.Fatalf("predeclared rejected: %v", err)
	}
	// No secret/config keys tolerated in manifest.
	files := map[string]string{
		"module.yaml": "requires:\n  ext:\n    t:\n      source: contrib/x\n      sum: h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n      config: {k: v}\n",
		"u.star":      "x = 1",
	}
	if err := s.Validate(ctx, a, files); err == nil {
		t.Fatal("manifest config accepted")
	}
	// Secret-bearing Starlark calls are fine to validate but no creds
	// are created: secrets refs stay consumers'.
	if err := s.Validate(ctx, a, map[string]string{"u.star": `x = secrets.get("S")`}); err != nil {
		t.Fatal(err)
	}
}

func TestModulePreviewPublish(t *testing.T) {
	s, st, a, _ := newSvc(t)
	ctx := context.Background()
	v, err := s.Create(ctx, a, CreateRequest{Name: "strkit", Files: goodFiles()})
	if err != nil {
		t.Fatal(err)
	}
	// Preview: base null, candidate pinned.
	rev, err := s.Preview(ctx, a, v.ID, v.DraftVersion)
	if err != nil {
		t.Fatal(err)
	}
	if rev.Base != nil || rev.Identical || len(rev.Changes) == 0 || rev.Candidate.Digest == "" {
		t.Fatalf("preview: %+v", rev)
	}
	if rev.Changes[0].Category != "sources" {
		t.Fatalf("expected sources change: %+v", rev.Changes[0])
	}
	// Publish requires expected_base presence.
	if _, err := s.Publish(ctx, a, v.ID, PublishRequest{DraftVersion: v.DraftVersion, Candidate: rev.Candidate.Digest}); err == nil {
		t.Fatal("expected_base absent accepted")
	}
	empty := ""
	vv, err := s.Publish(ctx, a, v.ID, PublishRequest{DraftVersion: v.DraftVersion, ExpectedBase: &empty,
		Candidate: rev.Candidate.Digest, Note: "first"})
	if err != nil {
		t.Fatal(err)
	}
	if vv.Version != 1 || vv.Sum != rev.Candidate.Digest || vv.Note != "first" {
		t.Fatalf("version: %+v", vv)
	}
	// Identical re-publish is a no-op.
	vv2, err := s.Publish(ctx, a, v.ID, PublishRequest{DraftVersion: v.DraftVersion, ExpectedBase: &vv.Sum,
		Candidate: rev.Candidate.Digest})
	if err != nil {
		t.Fatal(err)
	}
	if vv2.Version != 1 {
		t.Fatalf("identical publish made a row: %+v", vv2)
	}
	// Stale draft and stale base rejected.
	if _, err := s.Publish(ctx, a, v.ID, PublishRequest{DraftVersion: 99, ExpectedBase: &vv.Sum,
		Candidate: rev.Candidate.Digest}); err == nil {
		t.Fatal("stale draft published")
	}
	if _, err := s.Publish(ctx, a, v.ID, PublishRequest{DraftVersion: v.DraftVersion, ExpectedBase: &empty,
		Candidate: rev.Candidate.Digest}); err == nil {
		t.Fatal("stale base published")
	}
	// Stale candidate rejected.
	if _, err := s.Publish(ctx, a, v.ID, PublishRequest{DraftVersion: v.DraftVersion, ExpectedBase: &vv.Sum,
		Candidate: "h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}); err == nil {
		t.Fatal("stale candidate published")
	}
	// Library change after draft: publish preview must reflect it and
	// the module list shows unpublished changes.
	got, _ := s.Get(ctx, a, v.ID)
	if got.HasUnpublishedChanges {
		t.Fatal("fresh publish flagged dirty")
	}
	if _, err := s.Update(ctx, a, v.ID, UpdateRequest{DraftVersion: v.DraftVersion,
		Files: map[string]string{"u.star": "x = 3"}}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get(ctx, a, v.ID)
	if !got.HasUnpublishedChanges || got.LatestVersion != 1 {
		t.Fatalf("dirty flag: %+v", got)
	}
	// Versions list + single fetch.
	vers, err := s.Versions(ctx, a, v.ID)
	if err != nil || len(vers) != 1 || vers[0].Version != 1 {
		t.Fatalf("versions: %v %+v", err, vers)
	}
	if _, err := s.Version(ctx, a, v.ID, 1); err != nil {
		t.Fatal(err)
	}
	_ = st
}

func TestModuleCompareRestore(t *testing.T) {
	s, _, a, _ := newSvc(t)
	ctx := context.Background()
	v, err := s.Create(ctx, a, CreateRequest{Name: "strkit", Files: goodFiles()})
	if err != nil {
		t.Fatal(err)
	}
	rev, err := s.Preview(ctx, a, v.ID, v.DraftVersion)
	if err != nil {
		t.Fatal(err)
	}
	empty := ""
	if _, err := s.Publish(ctx, a, v.ID, PublishRequest{DraftVersion: v.DraftVersion,
		ExpectedBase: &empty, Candidate: rev.Candidate.Digest}); err != nil {
		t.Fatal(err)
	}
	// Draft change, compare base=1 vs draft — draft_version is still
	// 1 because publishing never touches the draft counter.
	if _, err := s.Update(ctx, a, v.ID, UpdateRequest{DraftVersion: 1,
		Files: map[string]string{"u.star": "x = 5", "n.star": "y = 1"}}); err != nil {
		t.Fatal(err)
	}
	cmp, err := s.Compare(ctx, a, v.ID, 1, "draft")
	if err != nil {
		t.Fatal(err)
	}
	if cmp.Identical || len(cmp.Changes) != 2 {
		t.Fatalf("compare: %+v", cmp)
	}
	// Restore v1 files into draft under CAS.
	got, err := s.Get(ctx, a, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	rv, err := s.Restore(ctx, a, v.ID, 1, got.DraftVersion)
	if err != nil {
		t.Fatal(err)
	}
	if rv.Files["u.star"] != "x = 1" {
		t.Fatalf("restore: %+v", rv.Files)
	}
	if _, err := s.Restore(ctx, a, v.ID, 1, got.DraftVersion); err == nil {
		t.Fatal("stale restore CAS accepted")
	}
}

func TestModuleBoundKeyInvisible(t *testing.T) {
	s, st, a, _ := newSvc(t)
	ctx := context.Background()
	v, err := s.Create(ctx, a, CreateRequest{Name: "strkit", Files: goodFiles()})
	if err != nil {
		t.Fatal(err)
	}
	key := &store.APIKey{ID: "ak_1", UserID: a.UserID, WorkspaceID: a.WorkspaceID, BoundID: v.ID}
	if err := st.CreateAPIKey(ctx, key); err == nil {
		// fixture may not expose CreateAPIKey with bound ids; skip.
	}
	bound := service.Actor{UserID: a.UserID, WorkspaceID: a.WorkspaceID, Kind: types.AuthAPIKey, KeyBoundID: v.ID}
	if _, err := s.Get(ctx, bound, v.ID); err == nil {
		t.Fatal("bound key sees module")
	}
	if _, err := s.List(ctx, bound); err == nil {
		t.Fatal("bound key lists modules")
	}
	if _, err := s.Create(ctx, bound, CreateRequest{Name: "x", Files: goodFiles()}); err == nil {
		t.Fatal("bound key creates")
	}
	if err := s.Validate(ctx, bound, goodFiles()); err == nil {
		t.Fatal("bound key validates")
	}
}

func TestModuleDebug(t *testing.T) {
	s, _, a, _ := newSvc(t)
	ctx := context.Background()
	v, err := s.Create(ctx, a, CreateRequest{Name: "dbgx", Files: map[string]string{"u.star": "x = 7"}})
	if err != nil {
		t.Fatal(err)
	}
	if s.o.Sandbox == nil {
		t.Skip("no sandbox fixture wired")
	}
	g, err := s.GetDebug(ctx, a, v.ID)
	if err != nil || g.Version != 0 {
		t.Fatalf("get: %v %v", g, err)
	}
	d, err := s.SaveDebug(ctx, a, v.ID, SaveDebugRequest{Version: 0, Script: `print(x)`, Spec: []byte(`{}`)})
	if err != nil || d.Version != 1 {
		t.Fatalf("save: %v %v", d, err)
	}
	if _, err := s.SaveDebug(ctx, a, v.ID, SaveDebugRequest{Version: 0, Script: "y", Spec: []byte(`{}`)}); err == nil {
		t.Fatal("stale debug accepted")
	}
}

// Autosave contracts: Update persists incomplete Starlark drafts;
// compile-gated seams still reject them.
func TestModuleUpdateIncompleteSource(t *testing.T) {
	s, _, a, _ := newSvc(t)
	ctx := context.Background()
	v, err := s.Create(ctx, a, CreateRequest{Name: "badmod", Files: goodFiles()})
	if err != nil {
		t.Fatal(err)
	}
	bad := map[string]string{"u.star": "def hello("}
	up, err := s.Update(ctx, a, v.ID, UpdateRequest{DraftVersion: v.DraftVersion, Files: bad})
	if err != nil {
		t.Fatalf("update must accept incomplete source: %v", err)
	}
	got, err := s.Get(ctx, a, v.ID)
	if err != nil || got.Files["u.star"] != "def hello(" {
		t.Fatalf("get: %v %+v", err, got)
	}
	// Compile-gated seams still reject the incomplete source.
	if err := s.Validate(ctx, a, bad); err == nil {
		t.Fatal("validate must reject incomplete source")
	}
	if _, err := s.Preview(ctx, a, v.ID, up.DraftVersion); err == nil {
		t.Fatal("preview must reject incomplete source")
	}
}

func TestModuleUpdateShapeStillRejected(t *testing.T) {
	s, _, a, _ := newSvc(t)
	ctx := context.Background()
	v, err := s.Create(ctx, a, CreateRequest{Name: "shp", Files: goodFiles()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(ctx, a, v.ID, UpdateRequest{DraftVersion: v.DraftVersion, Files: map[string]string{"x.txt": "no"}}); err == nil {
		t.Fatal("non-.star file must fail shape checks")
	}
	if _, err := s.Update(ctx, a, v.ID, UpdateRequest{DraftVersion: 99, Files: goodFiles()}); err == nil {
		t.Fatal("stale CAS must 409")
	}
}

// Structured requirements: add/remove, preserve manifest metadata,
// pin before store, never config/env in module data.
// fakePinner pins every ext requirement to a fixed h1 sum so the
// structured-deps path can be exercised without a real repo.
type fakePinner struct{}

func (fakePinner) PinSpec(ctx context.Context, spec []byte) (json.RawMessage, error) {
	var in struct {
		Capabilities struct {
			Ext map[string]map[string]string `json:"ext"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(spec, &in); err != nil {
		return nil, err
	}
	out := map[string]any{"capabilities": map[string]any{"ext": map[string]any{}}}
	for alias, req := range in.Capabilities.Ext {
		out["capabilities"].(map[string]any)["ext"].(map[string]any)[alias] =
			map[string]string{"source": req["source"], "sum": "h1:" + strings.Repeat("A", 43) + "="}
	}
	return json.Marshal(out)
}

func TestModuleRequirementsRoundTrip(t *testing.T) {
	s, _, a, _ := newSvc(t)
	s.o.SpecPinner = fakePinner{}
	ctx := context.Background()
	mf := "description: keep me\nrequires:\n  capabilities: [fs]\n"
	files := map[string]string{"u.star": "x = 1", "module.yaml": mf}
	v, err := s.Create(ctx, a, CreateRequest{Name: "req", Files: files})
	if err != nil {
		t.Fatal(err)
	}
	// Add a dep via the structured request (source-only; server pins).
	src := &RequirementsView{
		Capabilities: []string{"fs"},
		Ext:          map[string]mod.ExtRequirement{"net": {Source: "contrib/net"}},
	}
	up, err := s.Update(ctx, a, v.ID, UpdateRequest{
		DraftVersion: v.DraftVersion, Requirements: (*RequirementsView)(src),
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = up
	got, _ := s.Get(ctx, a, v.ID)
	yaml := got.Files["module.yaml"]
	if !strings.Contains(yaml, "keep me") {
		t.Fatalf("description lost: %s", yaml)
	}
	if !strings.Contains(yaml, "fs") || !strings.Contains(yaml, "contrib/net") {
		t.Fatalf("requirements not merged: %s", yaml)
	}
	// Remove the dep — empty requirements still keeps module.yaml.
	empty := &RequirementsView{}
	if _, err := s.Update(ctx, a, v.ID, UpdateRequest{DraftVersion: got.DraftVersion, Requirements: empty}); err != nil {
		t.Fatal(err)
	}
	got2, _ := s.Get(ctx, a, v.ID)
	if strings.Contains(got2.Files["module.yaml"], "contrib/net") {
		t.Fatalf("dep not removed: %s", got2.Files["module.yaml"])
	}
}

func TestModuleRequirementsPinIncompleteDraft(t *testing.T) {
	s, _, a, _ := newSvc(t)
	s.o.SpecPinner = fakePinner{}
	ctx := context.Background()
	v, err := s.Create(ctx, a, CreateRequest{Name: "incomplete-dep", Files: map[string]string{
		"u.star": "x = 1", "module.yaml": "description: preserve this\nrequires:\n  capabilities: [fs]\n",
	}})
	if err != nil {
		t.Fatal(err)
	}
	// Autosave replaces the .star files and edits requirements atomically.
	// The omitted manifest must retain its independent description.
	up, err := s.Update(ctx, a, v.ID, UpdateRequest{
		DraftVersion: v.DraftVersion, Files: map[string]string{"u.star": "def unfinished("},
		Requirements: &RequirementsView{Capabilities: []string{"fs"},
			Ext: map[string]mod.ExtRequirement{"helper": {Source: "contrib/helper"}}},
	})
	if err != nil {
		t.Fatalf("pinning dependencies must not compile incomplete drafts: %v", err)
	}
	mf, err := mod.ParseManifest([]byte(up.Files[mod.ManifestFile]))
	if err != nil {
		t.Fatal(err)
	}
	if up.Files["u.star"] != "def unfinished(" || mf.Description != "preserve this" ||
		len(mf.Requires.Capabilities) != 1 || mf.Requires.Capabilities[0] != "fs" ||
		!mod.ValidSum(mf.Requires.Ext["helper"].Sum) {
		t.Fatalf("draft source or manifest metadata lost: %+v", up)
	}
	if _, err := s.Preview(ctx, a, v.ID, up.DraftVersion); err == nil {
		t.Fatal("preview accepted incomplete source after pinning")
	}
	base := ""
	if _, err := s.Publish(ctx, a, v.ID, PublishRequest{
		DraftVersion: up.DraftVersion, ExpectedBase: &base,
		Candidate: "h1:" + strings.Repeat("A", 43) + "=",
	}); err == nil {
		t.Fatal("publish accepted incomplete source after pinning")
	}
}
