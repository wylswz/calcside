package instance

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"calcside/internal/capability"
	capfs "calcside/internal/capability/fs"
	capio "calcside/internal/capability/io"
	capnet "calcside/internal/capability/net"
	"calcside/internal/engine"
	"calcside/internal/secrets"
	"calcside/internal/store"
	_ "calcside/internal/store/sqlite"
)

func testCipher(t *testing.T) *secrets.Cipher {
	t.Helper()
	c, err := secrets.NewCipher(base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// secretsMgr builds a manager with the vault cipher and net capability.
func secretsMgr(t *testing.T, cipher *secrets.Cipher) (*Manager, store.Store, *store.User) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, "sqlite", filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	reg := capability.NewRegistry()
	reg.Register(capfs.Factory())
	reg.Register(capio.Factory())
	reg.Register(capnet.Factory())
	limits := defaultLimits()
	limits.NetAllowPrivate = true
	limits.SecretsAllowHTTP = true
	m := New(st, engine.New(8), reg, nil, "", time.Second, limits, nil, cipher, nil, time.Hour)
	u, _ := st.UpsertUserByEmail(ctx, "u@x.com", "", "")
	return m, st, u
}

// addVaultSecret seals + stores a vault secret for the user.
func addVaultSecret(t *testing.T, st store.Store, c *secrets.Cipher, u *store.User, name, value string, domains []string) {
	t.Helper()
	ct, err := c.Seal([]byte(value), u.ID+"/"+name)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSecret(context.Background(), &store.Secret{
		UserID: u.ID, Name: name, Ciphertext: ct, AllowedDomains: domains,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSecretSpecResolution(t *testing.T) {
	ctx := context.Background()
	c := testCipher(t)
	m, st, u := secretsMgr(t, c)
	addVaultSecret(t, st, c, u, "GH_TOKEN", "ghp_x", []string{"*.github.com", "api.github.com"})

	cases := []struct {
		name    string
		spec    string
		wantErr string
	}{
		{"ref ok", `{"secrets":{"T":{"ref":"GH_TOKEN"}}}`, ""},
		{"narrowed ok", `{"secrets":{"T":{"ref":"GH_TOKEN","allowed_domains":["api.github.com","*.github.com"]}}}`, ""},
		{"narrow wildcard ok", `{"secrets":{"T":{"ref":"GH_TOKEN","allowed_domains":["*.api.github.com"]}}}`, ""},
		{"widened", `{"secrets":{"T":{"ref":"GH_TOKEN","allowed_domains":["other.com"]}}}`, "not covered"},
		{"widened bare", `{"secrets":{"T":{"ref":"GH_TOKEN","allowed_domains":["github.com"]}}}`, "not covered"},
		{"unknown ref", `{"secrets":{"T":{"ref":"NOPE"}}}`, "unknown vault ref"},
		{"inline no domains", `{"secrets":{"T":{"value":"v"}}}`, "allowed_domains must be non-empty"},
		{"inline ok", `{"secrets":{"T":{"value":"v","allowed_domains":["x.com"]}}}`, ""},
		{"both ref+value", `{"secrets":{"T":{"ref":"GH_TOKEN","value":"v","allowed_domains":["x.com"]}}}`, "exactly one"},
		{"neither", `{"secrets":{"T":{}}}`, "exactly one"},
		{"env overlap", `{"env":{"T":"x"},"secrets":{"T":{"value":"v","allowed_domains":["x.com"]}}}`, "also used by env"},
		{"bad name", `{"secrets":{"lowercase":{"value":"v","allowed_domains":["x.com"]}}}`, "invalid name"},
		{"bad env name", `{"env":{"lower":"x"}}`, "invalid name"},
	}
	for _, tc := range cases {
		_, err := m.Create(ctx, u, []byte(tc.spec))
		if tc.wantErr == "" && err != nil {
			t.Errorf("%s: unexpected error %v", tc.name, err)
		}
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("%s: want error containing %q, got %v", tc.name, tc.wantErr, err)
			} else if strings.Contains(err.Error(), "ghp_x") {
				t.Errorf("%s: error leaks value: %v", tc.name, err)
			}
		}
	}
}

func TestVaultRefWithoutKey(t *testing.T) {
	m, _, u := secretsMgr(t, nil)
	_, err := m.Create(context.Background(), u,
		[]byte(`{"secrets":{"T":{"ref":"GH_TOKEN"}}}`))
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("expected disabled error, got %v", err)
	}
	// inline secrets still work without a cipher
	_, err = m.Create(context.Background(), u,
		[]byte(`{"secrets":{"T":{"value":"v","allowed_domains":["x.com"]}}}`))
	if err != nil {
		t.Fatalf("inline secret without key: %v", err)
	}
}

func TestSpecSanitized(t *testing.T) {
	ctx := context.Background()
	c := testCipher(t)
	m, st, u := secretsMgr(t, c)
	meta, err := m.Create(ctx, u, []byte(
		`{"secrets":{"INL":{"value":"SUPERSECRETINLINE","allowed_domains":["x.com"]},"T":{"ref":""}},"env":{"A":"1"}}`))
	if err != nil {
		// T with empty ref/value is invalid; drop it and retry
		meta, err = m.Create(ctx, u, []byte(
			`{"secrets":{"INL":{"value":"SUPERSECRETINLINE","allowed_domains":["x.com"]}},"env":{"A":"1"}}`))
		if err != nil {
			t.Fatal(err)
		}
	}
	raw := meta.Spec
	if strings.Contains(string(raw), "SUPERSECRETINLINE") {
		t.Fatalf("inline value persisted in meta spec: %s", raw)
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	ss := parsed["secrets"].(map[string]any)["INL"].(map[string]any)
	if ss["inline"] != true || ss["value"] != nil {
		t.Fatalf("sanitized spec wrong: %v", ss)
	}
	// DB row also clean
	dbRow, err := st.GetInstance(ctx, meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(dbRow.Spec), "SUPERSECRETINLINE") {
		t.Fatalf("inline value in DB spec: %s", dbRow.Spec)
	}
}

func TestSecretWipeOnDelete(t *testing.T) {
	ctx := context.Background()
	m, _, u := secretsMgr(t, nil)
	meta, err := m.Create(ctx, u, []byte(
		`{"secrets":{"INL":{"value":"wipe-me","allowed_domains":["x.com"]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	in, ok := m.get(meta.ID)
	if !ok || in.secrets.Lookup("INL") == nil {
		t.Fatal("secret not resolved")
	}
	stored := in.secrets.Lookup("INL").Value()
	if err := m.Delete(ctx, meta.ID); err != nil {
		t.Fatal(err)
	}
	if string(stored) != strings.Repeat("\x00", len("wipe-me")) {
		t.Fatalf("secret not wiped: %q", stored)
	}
}

func TestEnvAndSecretsGlobals(t *testing.T) {
	ctx := context.Background()
	m, _, u := secretsMgr(t, nil)
	meta, err := m.Create(ctx, u, []byte(
		`{"env":{"REGION":"us-east-1"},"secrets":{"TOK":{"value":"v","allowed_domains":["x.com"]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	res, err := m.Exec(ctx, meta.ID, `print(env.get("REGION")); print(env["REGION"]); print(sorted(env.keys())); print(secrets.names()); print("{{secrets.TOK}}")`, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != nil {
		t.Fatalf("exec error: %+v", res.Error)
	}
	want := "us-east-1\nus-east-1\n[\"REGION\"]\n[\"TOK\"]\n{{secrets.TOK}}\n"
	if res.Output != want {
		t.Fatalf("output %q want %q", res.Output, want)
	}
}
