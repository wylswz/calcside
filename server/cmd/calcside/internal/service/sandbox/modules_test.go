package sandbox

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"calcside/cmd/calcside/internal/service"
	"calcside/internal/runtime"

	_ "calcside/cmd/calcside/internal/gormstore"
	"calcside/internal/extartifact"
	"calcside/internal/module"
	"calcside/internal/store"
	"calcside/internal/store/storetest"
)

type testPinner struct{ called int }

func (p *testPinner) PinSpec(ctx context.Context, raw []byte) (json.RawMessage, error) {
	p.called++
	return json.RawMessage(append([]byte(nil), raw...)), nil
}

func mkModuleSvc(t *testing.T) (*Service, store.Store, *store.User, string) {
	return mkModuleSvcMans(t, fakeManifests{})
}

func mkModuleSvcMans(t *testing.T, mans fakeManifests) (*Service, store.Store, *store.User, string) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, "sqlite", storetest.SQLite(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	u, err := st.UpsertUserByEmail(ctx, "modsel@x.com", "", "")
	if err != nil {
		t.Fatal(err)
	}
	ws := store.PersonalWorkspaceID(u.ID)
	s := New(Options{Store: st, Secrets: &envSecrets{refs: map[string]bool{}}, Manifests: mans, SpecPinner: &testPinner{}})
	return s, st, u, ws
}

func publishModule(t *testing.T, s *Service, st store.Store, ws, userID, name string, files map[string]string) (string, string) {
	t.Helper()
	ctx := context.Background()
	fb := map[string][]byte{}
	for n, c := range files {
		fb[n] = []byte(c)
	}
	m, err := store.NewModule(ws, userID, name, "", fb, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateModule(ctx, m); err != nil {
		t.Fatal(err)
	}
	sum, err := extartifact.TreeHash(fb)
	if err != nil {
		t.Fatal(err)
	}
	v, err := st.PublishModule(ctx, ws, m.ID, 1, "", &store.ModuleVersion{Sum: sum, Files: fb, PublishedBy: userID})
	if err != nil {
		t.Fatal(err)
	}
	return m.ID, v.Sum
}

func selectSpec(t *testing.T, s *Service, ws, spec string) (json.RawMessage, map[string]module.Tree, error) {
	t.Helper()
	return s.selectedModules(context.Background(), ws, []byte(spec))
}

func TestSelectedModulesResolves(t *testing.T) {
	s, st, u, ws := mkModuleSvc(t)
	_, sum := publishModule(t, s, st, ws, u.ID, "strkit", map[string]string{"u.star": "x = 1"})
	out, trees, err := selectSpec(t, s, ws, `{"capabilities":{},"modules":{"sk":{"name":"strkit"}}}`)
	if err != nil {
		t.Fatal(err)
	}
	tr := trees["sk"]
	if tr.Name != "strkit" || tr.Version != 1 || tr.Sum != sum || string(tr.Files["u.star"]) != "x = 1" {
		t.Fatalf("tree: %+v", tr)
	}
	var spec struct {
		Modules map[string]module.Reference `json:"modules"`
	}
	_ = json.Unmarshal(out, &spec)
	ref := spec.Modules["sk"]
	if ref.Name != "strkit" || ref.Version != 1 || ref.Sum != sum {
		t.Fatalf("pinned ref: %+v", ref)
	}
}

func TestSelectedModulesFailures(t *testing.T) {
	s, st, u, ws := mkModuleSvc(t)
	// unpublished draft only: GetModuleByName finds it but no versions.
	m, _ := store.NewModule(ws, u.ID, "draft", "", map[string][]byte{"u.star": []byte("x")}, time.Now())
	_ = st.CreateModule(context.Background(), m)
	id, sum := publishModule(t, s, st, ws, u.ID, "strkit", map[string]string{"u.star": "x = 1"})
	_ = id
	cases := []struct{ name, spec, want string }{
		{"unknown", `{"modules":{"m":{"name":"ghost"}}}`, "unknown module"},
		{"unpublished", `{"modules":{"m":{"name":"draft"}}}`, "no published version"},
		{"bad version", `{"modules":{"m":{"name":"strkit","version":9}}}`, "no published version"},
		{"sum mismatch", `{"modules":{"m":{"name":"strkit","version":1,"sum":"h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}}}`, "sum"},
		{"bad alias", `{"modules":{"bad!":{}}}`, "mount alias"},
	}
	for _, tc := range cases {
		if _, _, err := selectSpec(t, s, ws, tc.spec); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: want %q in error, got %v", tc.name, tc.want, err)
		}
	}
	// Pinned sum accepted.
	if _, _, err := selectSpec(t, s, ws, `{"modules":{"m":{"name":"strkit","version":1,"sum":"`+sum+`"}}}`); err != nil {
		t.Fatalf("pinned sum rejected: %v", err)
	}
}

// Snapshot retention: an already-returned v1 tree is immutable; a new
// resolution pins v2; deleting the library module leaves the
// release-boundary snapshot usable.
func TestSelectedModulesSnapshotRetention(t *testing.T) {
	ctx := context.Background()
	s, st, u, ws := mkModuleSvc(t)
	mid, sum1 := publishModule(t, s, st, ws, u.ID, "strkit", map[string]string{"u.star": "x = 1"})

	_, trees1, err := selectSpec(t, s, ws, `{"modules":{"m":{"name":"strkit"}}}`)
	if err != nil {
		t.Fatal(err)
	}
	// Publish v2 through the real CAS path.
	draft, err := st.GetModule(ctx, mid)
	if err != nil {
		t.Fatal(err)
	}
	draft.Files = map[string][]byte{"u.star": []byte("y = 2")}
	if ok, err := st.UpdateModuleDraft(ctx, draft, draft.DraftVersion); err != nil || !ok {
		t.Fatal("draft update", err)
	}
	sum2, _ := extartifact.TreeHash(draft.Files)
	if _, err := st.PublishModule(ctx, ws, mid, draft.DraftVersion+1, sum1,
		&store.ModuleVersion{Sum: sum2, Files: draft.Files, PublishedBy: u.ID}); err != nil {
		t.Fatal(err)
	}
	// The earlier resolution still holds v1 byte-for-byte.
	if got := trees1["m"]; got.Version != 1 || got.Sum != sum1 || string(got.Files["u.star"]) != "x = 1" {
		t.Fatalf("v1 snapshot mutated: %+v", got)
	}
	// A fresh resolution pins v2.
	_, trees2, err := selectSpec(t, s, ws, `{"modules":{"m":{"name":"strkit"}}}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := trees2["m"]; got.Version != 2 || string(got.Files["u.star"]) != "y = 2" {
		t.Fatalf("expected v2, got %+v", got)
	}
	// A release snapshot outlives library deletion: build the release
	// from the v1 tree, delete the module, ReleaseBoundary must still
	// hand back exactly v1.
	spec := `{"modules":{"m":{"name":"strkit","version":1,"sum":"` + sum1 + `"}}}`
	rel, err := store.NewServingReleaseWithModules(ws, u.ID, json.RawMessage(spec),
		runtime.PolicyBundle{Global: map[string]string{"g1.rego": "package g"}, User: map[string]string{"u.rego": "package u"}},
		runtime.ProgramBundle{
			Sources:      map[string][]byte{"main.star": []byte("def main(i): return {}")},
			InputSchema:  store.DefaultApplicationSchema(),
			OutputSchema: store.DefaultApplicationSchema(),
		}, trees1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteModule(ctx, ws, mid); err != nil {
		t.Fatal(err)
	}
	b, err := s.ReleaseBoundary(ctx, service.Actor{UserID: u.ID, WorkspaceID: ws}, rel)
	if err != nil {
		t.Fatal(err)
	}
	if string(b.Modules["m"].Files["u.star"]) != "x = 1" || b.Modules["m"].Version != 1 {
		t.Fatalf("release boundary lost snapshot: %+v", b.Modules)
	}
	// Namespaced globals: release globals under release/, current
	// server globals under server/, user policies from the snapshot.
	if b.Policies.Global["release/g1.rego"] == "" || b.Policies.User["u.rego"] != "package u" {
		t.Fatalf("policy union: %+v", b.Policies)
	}
}

func TestSelectedModulesExtMerge(t *testing.T) {
	s, st, u, ws := mkModuleSvcMans(t, fakeManifests{"contrib/tavily@h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=": tavilyManifest()})
	manifest := "requires:\n  ext:\n    tavily:\n      source: contrib/tavily\n      sum: h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n  capabilities: [net]\n"
	publishModule(t, s, st, ws, u.ID, "webkit", map[string]string{
		"module.yaml": manifest,
		"u.star":      "x = 1",
	})
	// Missing base grant fails — never auto-granted.
	if _, _, err := selectSpec(t, s, ws, `{"capabilities":{},"modules":{"wk":{"name":"webkit"}}}`); err == nil || !strings.Contains(err.Error(), "net") {
		t.Fatalf("missing base grant accepted: %v", err)
	}
	// With net + matching explicit ext: dedupe, config preserved.
	out, _, err := selectSpec(t, s, ws, `{"capabilities":{"net":{},"ext":{"tavily":{"source":"contrib/tavily","sum":"h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=","config":{"x":1}}}},"modules":{"wk":{"name":"webkit"}}}`)
	if err != nil {
		t.Fatalf("dedupe case: %v", err)
	}
	var spec struct {
		Capabilities struct {
			Ext map[string]json.RawMessage `json:"ext"`
		} `json:"capabilities"`
	}
	_ = json.Unmarshal(out, &spec)
	if !strings.Contains(string(spec.Capabilities.Ext["tavily"]), `"x":1`) {
		t.Fatalf("explicit config lost: %s", spec.Capabilities.Ext["tavily"])
	}
	// New alias installed source+sum only.
	if _, _, err := selectSpec(t, s, ws, `{"capabilities":{"net":{}},"modules":{"wk":{"name":"webkit"}}}`); err != nil {
		t.Fatal(err)
	}
	// Mismatched pins fail.
	if _, _, err := selectSpec(t, s, ws, `{"capabilities":{"net":{},"ext":{"tavily":{"source":"contrib/tavily","sum":"h1:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB="}}},"modules":{"wk":{"name":"webkit"}}}`); err == nil || !strings.Contains(err.Error(), "differently") {
		t.Fatalf("pin mismatch accepted: %v", err)
	}
}

func TestSelectedModulesNoCredentialsCascade(t *testing.T) {
	s, st, u, ws := mkModuleSvc(t)
	publishModule(t, s, st, ws, u.ID, "plain", map[string]string{"u.star": "x = 1"})
	out, _, err := selectSpec(t, s, ws, `{"capabilities":{},"modules":{"p":{"name":"plain"}}}`)
	if err != nil {
		t.Fatal(err)
	}
	var spec map[string]json.RawMessage
	_ = json.Unmarshal(out, &spec)
	if _, hasSecrets := spec["secrets"]; hasSecrets {
		t.Fatal("module resolution injected secrets")
	}
}
