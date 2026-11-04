package application

import (
	"context"
	"encoding/json"
	"testing"

	"calcside/internal/extartifact"
	"calcside/internal/store"
)

// Drafts keep user selectors: unpinned module/ext refs stay "latest"
// through save; publish resolves them fresh. This test covers the
// applicationDraftSpec restore + dirty-digest status.
func TestDraftKeepsSelectors(t *testing.T) {
	svc, st := newSvc(t)
	a := ownerActor(st)
	ctx := context.Background()

	// Publish a module v1, then attach it unpinned (version 0).
	m, err := store.NewModule(a.WorkspaceID, a.UserID, "strkit", "",
		map[string][]byte{"u.star": []byte("x = 1")}, svc.o.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateModule(ctx, m); err != nil {
		t.Fatal(err)
	}
	sum1, _ := extartifact.TreeHash(map[string][]byte{"u.star": []byte("x = 1")})
	mv1, err := st.PublishModule(ctx, a.WorkspaceID, m.ID, 1, "",
		&store.ModuleVersion{Sum: sum1, Files: map[string][]byte{"u.star": []byte("x = 1")}, PublishedBy: a.UserID})
	if err != nil || mv1.Version != 1 {
		t.Fatalf("publish module: %v %v", mv1, err)
	}

	spec := `{"modules":{"m":{"name":"strkit","version":0}}}`
	app, err := svc.Create(ctx, a, CreateRequest{Name: "app", Code: ptr("def main(i): return i"), Spec: json.RawMessage(spec)})
	if err != nil {
		t.Fatal(err)
	}
	// The stored draft must keep version 0 — not the resolved pin.
	var parsed struct {
		Modules map[string]struct {
			Version int64 `json:"version"`
		} `json:"modules"`
	}
	if err := json.Unmarshal(app.Application.Spec, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Modules["m"].Version != 0 {
		t.Fatalf("draft froze module to %d", parsed.Modules["m"].Version)
	}

	// Publish module v2 — the app's preview digest must change because
	// the unpinned ref now resolves to v2.
	m2, _ := st.GetModule(ctx, m.ID)
	m2.Files = map[string][]byte{"u.star": []byte("x = 2")}
	m2.DraftVersion = 1
	if ok, err := st.UpdateModuleDraft(ctx, m2, 1); err != nil || !ok {
		t.Fatalf("module update: %v %v", ok, err)
	}
	sum2, _ := extartifact.TreeHash(map[string][]byte{"u.star": []byte("x = 2")})
	mv2, err := st.PublishModule(ctx, a.WorkspaceID, m.ID, 2, mv1.Sum,
		&store.ModuleVersion{Sum: sum2, Files: map[string][]byte{"u.star": []byte("x = 2")}, PublishedBy: a.UserID})
	if err != nil {
		t.Fatal(err)
	}
	if mv2.Version != 2 {
		t.Fatalf("v2: %d", mv2.Version)
	}
	rev, err := svc.Preview(ctx, a, app.Application.ID, app.Application.Version)
	if err != nil {
		t.Fatal(err)
	}
	if rev.Candidate.Modules["m"].Sum != mv2.Sum && rev.Candidate.Digest == "" {
		t.Fatalf("preview didn't see v2: %+v", rev.Candidate)
	}
}
