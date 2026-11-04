package sandbox

import (
	"calcside/cmd/calcside/internal/extensions"
	"calcside/internal/capability"
	capio "calcside/internal/capability/io"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"calcside/cmd/calcside/internal/service/access"
	"calcside/cmd/calcside/internal/service/vault"
	"calcside/cmd/calcside/internal/vaultcipher"

	_ "calcside/cmd/calcside/internal/gormstore"
	"calcside/cmd/calcside/internal/service"
	capext "calcside/internal/capability/ext"
	"calcside/internal/runtime"
	"calcside/internal/store"
	"calcside/internal/store/storetest"
	"calcside/internal/types"
)

type fakeManifests map[string]*capext.CapabilityManifest

func (f fakeManifests) Manifest(ctx context.Context, source, sum string) (*capext.CapabilityManifest, error) {
	m, ok := f[source+"@"+sum]
	if !ok {
		return nil, context.DeadlineExceeded
	}
	return m, nil
}

// envSecrets records Resolve/Describe calls and answers refs for the
// names it was seeded with.
type envSecrets struct {
	refs     map[string]bool
	resolved []map[string]runtime.SecretSpec
}

func (e *envSecrets) Resolve(ctx context.Context, ws string, in map[string]runtime.SecretSpec) ([]runtime.Secret, map[string]runtime.SecretSpec, error) {
	e.resolved = append(e.resolved, in)
	var out []runtime.Secret
	san := map[string]runtime.SecretSpec{}
	for n, ss := range in {
		if ss.Ref != "" {
			if !e.refs[ss.Ref] {
				return nil, nil, service.Errf(types.ErrCodeBadSpec, "unknown ref %q", ss.Ref)
			}
			out = append(out, runtime.Secret{Name: n, Value: "v-" + ss.Ref, AllowedDomains: ss.AllowedDomains, Source: types.SecretVault})
			san[n] = runtime.SecretSpec{Ref: ss.Ref, AllowedDomains: ss.AllowedDomains, Source: types.SecretVault}
		}
	}
	return out, san, nil
}

func (e *envSecrets) Describe(ctx context.Context, ws string, in map[string]runtime.SecretSpec) (map[string]runtime.SecretSpec, error) {
	san := map[string]runtime.SecretSpec{}
	for n, ss := range in {
		if ss.Ref != "" && !e.refs[ss.Ref] {
			return nil, service.Errf(types.ErrCodeBadSpec, "unknown ref %q", ss.Ref)
		}
		san[n] = runtime.SecretSpec{Ref: ss.Ref, AllowedDomains: ss.AllowedDomains, Source: types.SecretVault}
	}
	return san, nil
}

func tavilyManifest() *capext.CapabilityManifest {
	return &capext.CapabilityManifest{
		Name: "tavily",
		Config: []capext.ConfigField{
			{Name: "api_key", Type: "secret"},
			{Name: "backup", Type: "secret", Default: ""},
			{Name: "max_results", Type: "int", Default: float64(5)},
		},
	}
}

func extAliasEntry(alias, source, sum string, extra map[string]any) map[string]any {
	m := map[string]any{"source": source, "sum": sum}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func envService(t *testing.T, mans ManifestReader, secs SecretResolver) *Service {
	t.Helper()
	st, err := store.Open(context.Background(), "sqlite", storetest.SQLite(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return New(Options{Store: st, Secrets: secs, Manifests: mans})
}

func TestApplyEnvironmentResolves(t *testing.T) {
	mans := fakeManifests{"contrib/tavily@h1:sum": tavilyManifest()}
	secs := &envSecrets{refs: map[string]bool{"TAVILY_PROD": true, "TAVILY_BACKUP": true, "TAVILY_STAGE": true}}
	s := envService(t, mans, secs)
	spec := `{
	  "capabilities": {"ext": {"tavily": {"source": "contrib/tavily", "sum": "h1:sum", "config": {"max_results": 7}}}},
	  "environment": {"extensions": {"tavily": {"bindings": {
	    "default": {"secrets": {"api_key": {"ref": "TAVILY_PROD", "allowed_domains": ["api.tavily.com"]}, "backup": {"ref": "TAVILY_BACKUP"}}},
	    "staging": {"secrets": {"api_key": {"ref": "TAVILY_STAGE"}}}
	  }}}}
	}`
	out, err := s.applyEnvironment(context.Background(), "ws1", []byte(spec))
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Caps struct {
			Ext map[string]struct {
				Config map[string]json.RawMessage `json:"config"`
			} `json:"ext"`
		} `json:"capabilities"`
		Secrets map[string]runtime.SecretSpec `json:"secrets"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatal(err)
	}
	cfg := parsed.Caps.Ext["tavily"].Config
	var apiKey string
	if err := json.Unmarshal(cfg["api_key"], &apiKey); err != nil || !strings.HasPrefix(apiKey, "{{secrets.EXT_BIND_") {
		t.Fatalf("api_key placeholder missing: %v %s", err, cfg["api_key"])
	}
	if string(cfg["max_results"]) != "7" {
		t.Fatalf("non-secret config clobbered: %+v", cfg)
	}
	var internals []string
	for name := range parsed.Secrets {
		internals = append(internals, name)
	}
	if len(internals) != 2 {
		t.Fatalf("expected 2 internal secrets (default binding only), got %v", internals)
	}
	if parsed.Secrets[envSecretName("tavily", "api_key")].Ref != "TAVILY_PROD" {
		t.Fatalf("default ref lost: %+v", parsed.Secrets)
	}
	// Idempotent: re-applying to the normalized output is a no-op.
	again, err := s.applyEnvironment(context.Background(), "ws1", out)
	if err != nil {
		t.Fatal(err)
	}
	var re struct {
		Secrets map[string]runtime.SecretSpec `json:"secrets"`
	}
	_ = json.Unmarshal(again, &re)
	if len(re.Secrets) != 2 {
		t.Fatalf("round-trip not idempotent: %v", re.Secrets)
	}
}

func TestApplyEnvironmentRejects(t *testing.T) {
	mans := fakeManifests{"contrib/tavily@h1:sum": tavilyManifest()}
	cases := []struct {
		name, env, caps string
		want            string
	}{
		{"unknown alias", `{"extensions":{"ghost":{"bindings":{"default":{"secrets":{"api_key":{"ref":"X"}}}}}}}`,
			`{"ext":{"tavily":{"source":"contrib/tavily","sum":"h1:sum"}}}`, "not granted"},
		{"non-secret field", `{"extensions":{"tavily":{"bindings":{"default":{"secrets":{"max_results":{"ref":"X"}}}}}}}`,
			`{"ext":{"tavily":{"source":"contrib/tavily","sum":"h1:sum"}}}`, "not a secret field"},
		{"missing default", `{"extensions":{"tavily":{"bindings":{"staging":{"secrets":{"api_key":{"ref":"X"}}}}}}}`,
			`{"ext":{"tavily":{"source":"contrib/tavily","sum":"h1:sum"}}}`, "default binding"},
		{"required unbound", `{"extensions":{"tavily":{"bindings":{"default":{"secrets":{"backup":{"ref":"TAVILY_BACKUP"}}}}}}}`,
			`{"ext":{"tavily":{"source":"contrib/tavily","sum":"h1:sum"}}}`, "requires a binding"},
		{"legacy conflict", `{"extensions":{"tavily":{"bindings":{"default":{"secrets":{"api_key":{"ref":"TAVILY_PROD"}}}}}}}`,
			`{"ext":{"tavily":{"source":"contrib/tavily","sum":"h1:sum","config":{"api_key":"{{secrets.MY_KEY}}"}}}}`, "conflicts"},
		{"unknown vault ref", `{"extensions":{"tavily":{"bindings":{"default":{"secrets":{"api_key":{"ref":"NOPE"}}},"staging":{"secrets":{"api_key":{"ref":"GHOST"}}}}}}}`,
			`{"ext":{"tavily":{"source":"contrib/tavily","sum":"h1:sum"}}}`, "binding"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			secs := &envSecrets{refs: map[string]bool{"TAVILY_PROD": true, "TAVILY_BACKUP": true}}
			s := envService(t, mans, secs)
			spec := `{"capabilities":` + tc.caps + `,"environment":` + tc.env + `}`
			if _, err := s.applyEnvironment(context.Background(), "ws1", []byte(spec)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q in error, got %v", tc.want, err)
			}
		})
	}
}

// Real vault end-to-end: binding domains omitted inherits the vault's
// own allowlist as the EFFECTIVE domains, written into environment +
// generated descriptor — normalized roundtrip stays idempotent.
func TestApplyEnvironmentRealVault(t *testing.T) {
	ctx := context.Background()
	c, err := vaultcipher.NewCipher(base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, "sqlite", storetest.SQLite(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	u, err := st.UpsertUserByEmail(ctx, "envv@x.com", "", "")
	if err != nil {
		t.Fatal(err)
	}
	ws := store.PersonalWorkspaceID(u.ID)
	ct, err := c.Seal([]byte("k"), u.ID+"/TAVILY_PROD")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSecret(ctx, &store.Secret{UserID: u.ID, WorkspaceID: ws, Name: "TAVILY_PROD",
		Ciphertext: ct, AllowedDomains: []string{"api.tavily.com"}}); err != nil {
		t.Fatal(err)
	}
	vaultSvc := vault.New(st, c, access.New(st))
	s := New(Options{Store: st, Secrets: vaultSvc,
		Manifests: fakeManifests{"contrib/tavily@h1:sum": tavilyManifest()}})

	spec := `{"capabilities":{"ext":{"tavily":{"source":"contrib/tavily","sum":"h1:sum"}}},
	  "environment":{"extensions":{"tavily":{"bindings":{
	    "default":{"secrets":{"api_key":{"ref":"TAVILY_PROD"}}},
	    "stage":{"secrets":{"api_key":{"ref":"TAVILY_PROD","allowed_domains":["api.tavily.com"]}}}
	  }}}}}`
	out, err := s.applyEnvironment(ctx, ws, []byte(spec))
	if err != nil {
		t.Fatal(err)
	}
	// Effective domains (vault's own) were written into the environment.
	var parsed struct {
		Env struct {
			Extensions map[string]struct {
				Bindings map[string]struct {
					Secrets map[string]struct {
						AllowedDomains []string `json:"allowed_domains"`
					} `json:"secrets"`
				} `json:"bindings"`
			} `json:"extensions"`
		} `json:"environment"`
		Secrets map[string]map[string]any `json:"secrets"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatal(err)
	}
	got := parsed.Env.Extensions["tavily"].Bindings["default"].Secrets["api_key"].AllowedDomains
	if len(got) != 1 || got[0] != "api.tavily.com" {
		t.Fatalf("effective domains not persisted: %v", got)
	}
	internal := parsed.Secrets[envSecretName("tavily", "api_key")]
	if internal["ref"] != "TAVILY_PROD" {
		t.Fatalf("descriptor: %v", internal)
	}
	// Normalized output re-applies idempotently (effective domains now
	// match the descriptor, no collision).
	if _, err := s.applyEnvironment(ctx, ws, out); err != nil {
		t.Fatalf("roundtrip: %v", err)
	}
}

// Transaction-rebound boundary: a vault secret created inside tx is
// visible to the tx-bound sandbox but the root sandbox still sees the
// committed state after rollback.
func TestRebindTxBoundary(t *testing.T) {
	ctx := context.Background()
	c, err := vaultcipher.NewCipher(base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, "sqlite", storetest.SQLite(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	u, err := st.UpsertUserByEmail(ctx, "txb@x.com", "", "")
	if err != nil {
		t.Fatal(err)
	}
	ws := store.PersonalWorkspaceID(u.ID)
	vaultSvc := vault.New(st, c, access.New(st))
	s := New(Options{
		Store: st, Secrets: vaultSvc,
		Rebind: func(tx store.Store) (BoundaryBindings, error) {
			return BoundaryBindings{Secrets: vaultSvc.WithStore(tx)}, nil
		},
	})
	a := service.Actor{UserID: u.ID, WorkspaceID: ws, Kind: types.AuthSession}

	spec := `{"secrets":{"S":{"ref":"TX_KEY"}}}`
	err = st.Tx(ctx, func(tx store.Store) error {
		if err := tx.LockWorkspace(ctx, ws); err != nil {
			return err
		}
		if err := tx.CreateSecret(ctx, &store.Secret{UserID: u.ID, WorkspaceID: ws,
			Name: "TX_KEY", Ciphertext: []byte("ct"), AllowedDomains: []string{"h"}}); err != nil {
			return err
		}
		bound, err := s.WithStore(tx)
		if err != nil {
			return err
		}
		if _, err := bound.BuildBoundary(ctx, a, []byte(spec), false); err != nil {
			return fmt.Errorf("tx boundary rejected tx-visible secret: %w", err)
		}
		return fmt.Errorf("rollback")
	})
	if err == nil || err.Error() != "rollback" {
		t.Fatalf("tx: %v", err)
	}
	// Root sees committed state only — TX_KEY was never committed.
	if _, err := s.BuildBoundary(ctx, a, []byte(spec), false); err == nil {
		t.Fatal("root boundary saw uncommitted secret")
	}
}

// Ext-capable tx boundary: the exact production Rebind closure —
// tx-bound repository + registry cloned with the tx repo's
// SourceResolver — must resolve an ext alias inside a tx without
// escaping to the root store (single sqlite connection would hang).
func TestRebindTxBoundaryExt(t *testing.T) {
	ctx := context.Background()
	c, err := vaultcipher.NewCipher(base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, "sqlite", storetest.SQLite(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	u, err := st.UpsertUserByEmail(ctx, "exttx@x.com", "", "")
	if err != nil {
		t.Fatal(err)
	}
	ws := store.PersonalWorkspaceID(u.ID)

	// Local extension fixture, captured into the artifact store.
	root := filepath.Join(t.TempDir(), "fixtures")
	if err := os.MkdirAll(filepath.Join(root, "echo"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{
		"capability.yaml": "name: echo\nops: [{name: echo, params: [value]}]\n",
		"main.star":       "def echo(value): return value\n",
	} {
		if err := os.WriteFile(filepath.Join(root, "echo", name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	repo, err := extensions.NewRepository(st, capext.Options{CacheDir: t.TempDir(), LocalRoots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	vaultSvc := vault.New(st, c, access.New(st))
	reg := capability.NewRegistry()
	reg.Register(capio.Factory())
	extOpts := repo.Options()
	reg.Register(capext.Factory(extOpts))

	s := New(Options{
		Store: st, Secrets: vaultSvc, Registry: reg, Ext: &extOpts,
		SpecPinner: repo, Manifests: repo,
		// Exact production closure shape.
		Rebind: func(tx store.Store) (BoundaryBindings, error) {
			txRepo := repo.WithStore(tx)
			txOpts := txRepo.Options()
			return BoundaryBindings{
				Secrets:    vaultSvc.WithStore(tx),
				SpecPinner: txRepo, Manifests: txRepo, Ext: &txOpts,
			}, nil
		},
	})
	a := service.Actor{UserID: u.ID, WorkspaceID: ws, Kind: types.AuthSession}
	spec := `{"capabilities":{"ext":{"echo":{"source":"fixtures/echo"}}}}`

	err = st.Tx(ctx, func(tx store.Store) error {
		if err := tx.LockWorkspace(ctx, ws); err != nil {
			return err
		}
		bound, err := s.WithStore(tx)
		if err != nil {
			return err
		}
		bctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		b, err := bound.BuildBoundary(bctx, a, []byte(spec), false)
		if err != nil {
			return fmt.Errorf("tx ext boundary: %w", err)
		}
		if !strings.Contains(string(b.Spec), "h1:") {
			return fmt.Errorf("ext not pinned inside tx: %s", b.Spec)
		}
		return fmt.Errorf("rollback")
	})
	if err == nil || err.Error() != "rollback" {
		t.Fatalf("tx: %v", err)
	}
}
