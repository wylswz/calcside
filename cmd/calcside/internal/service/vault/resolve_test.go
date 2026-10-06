package vault

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	_ "calcside/cmd/calcside/internal/gormstore"
	"calcside/cmd/calcside/internal/vaultcipher"
	"calcside/internal/runtime"
	"calcside/internal/store"
	"calcside/internal/store/storetest"
	"calcside/internal/types"
)

func testCipher(t *testing.T) *vaultcipher.Cipher {
	t.Helper()
	c, err := vaultcipher.NewCipher(base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func testSvc(t *testing.T, cipher *vaultcipher.Cipher) (*Service, store.Store, *store.User) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, "sqlite", storetest.SQLite(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	u, err := st.UpsertUserByEmail(ctx, "u@x.com", "", "")
	if err != nil {
		t.Fatal(err)
	}
	return New(st, cipher), st, u
}

func addVaultSecret(t *testing.T, st store.Store, c *vaultcipher.Cipher, u *store.User, name, value string, domains []string) {
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

// parseSecrets is the spec.secrets map as it arrives from a request.
func parseSecrets(t *testing.T, specJSON string) map[string]runtime.SecretSpec {
	t.Helper()
	var spec runtime.Spec
	if err := json.Unmarshal([]byte(specJSON), &spec); err != nil {
		t.Fatal(err)
	}
	return spec.Secrets
}

func TestResolve(t *testing.T) {
	ctx := context.Background()
	c := testCipher(t)
	svc, st, u := testSvc(t, c)
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
		{"inline no domains", `{"secrets":{"T":{"value":"v"}}}`, ""},
		{"inline ok", `{"secrets":{"T":{"value":"v","allowed_domains":["x.com"]}}}`, ""},
		{"both ref+value", `{"secrets":{"T":{"ref":"GH_TOKEN","value":"v","allowed_domains":["x.com"]}}}`, "exactly one"},
		{"neither", `{"secrets":{"T":{}}}`, "exactly one"},
	}
	for _, tc := range cases {
		_, _, err := svc.Resolve(ctx, u.ID, parseSecrets(t, tc.spec))
		if tc.wantErr == "" {
			if err != nil {
				t.Errorf("%s: unexpected error %v", tc.name, err)
			}
			continue
		}
		switch {
		case err == nil || !strings.Contains(err.Error(), tc.wantErr):
			t.Errorf("%s: want error containing %q, got %v", tc.name, tc.wantErr, err)
		case strings.Contains(err.Error(), "ghp_x"):
			t.Errorf("%s: error leaks value: %v", tc.name, err)
		}
	}
}

// A vault secret with no allowlist is unrestricted, so any narrowing is
// accepted and becomes the effective allowlist.
func TestResolveNarrowsUnrestrictedVaultSecret(t *testing.T) {
	ctx := context.Background()
	c := testCipher(t)
	svc, st, u := testSvc(t, c)
	addVaultSecret(t, st, c, u, "WIDE", "vault-secret-1", nil)

	out, sanitized, err := svc.Resolve(ctx, u.ID,
		parseSecrets(t, `{"secrets":{"T":{"ref":"WIDE","allowed_domains":["a.example"]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Value != "vault-secret-1" {
		t.Fatalf("resolved: %+v", out)
	}
	if got := out[0].AllowedDomains; len(got) != 1 || got[0] != "a.example" {
		t.Fatalf("effective domains: %v", got)
	}
	if sanitized["T"].Source != types.SecretVault || sanitized["T"].Ref != "WIDE" {
		t.Fatalf("sanitized: %+v", sanitized["T"])
	}
}

func TestResolveVaultRefWithoutKey(t *testing.T) {
	ctx := context.Background()
	svc, _, u := testSvc(t, nil)
	_, _, err := svc.Resolve(ctx, u.ID, parseSecrets(t, `{"secrets":{"T":{"ref":"GH_TOKEN"}}}`))
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("expected disabled error, got %v", err)
	}
	// Inline secrets still work without a cipher.
	if _, _, err := svc.Resolve(ctx, u.ID,
		parseSecrets(t, `{"secrets":{"T":{"value":"v","allowed_domains":["x.com"]}}}`)); err != nil {
		t.Fatalf("inline secret without key: %v", err)
	}
}

// The descriptors persisted with an instance must never carry a value.
func TestResolveSanitizesInlineValues(t *testing.T) {
	ctx := context.Background()
	svc, _, u := testSvc(t, testCipher(t))
	var spec runtime.Spec
	if err := json.Unmarshal([]byte(
		`{"secrets":{"INL":{"value":"SUPERSECRETINLINE","allowed_domains":["x.com"]}},"env":{"A":"1"}}`), &spec); err != nil {
		t.Fatal(err)
	}
	_, sanitized, err := svc.Resolve(ctx, u.ID, spec.Secrets)
	if err != nil {
		t.Fatal(err)
	}
	raw := spec.Sanitized(sanitized)
	if strings.Contains(string(raw), "SUPERSECRETINLINE") {
		t.Fatalf("inline value survived sanitization: %s", raw)
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	ss := parsed["secrets"].(map[string]any)["INL"].(map[string]any)
	if ss["source"] != "inline" || ss["value"] != nil {
		t.Fatalf("sanitized spec wrong: %v", ss)
	}
}
