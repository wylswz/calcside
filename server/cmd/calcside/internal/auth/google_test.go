package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGoogleCallbackUsesConsoleOrigin(t *testing.T) {
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                base,
			"authorization_endpoint":                base + "/authorize",
			"token_endpoint":                        base + "/token",
			"jwks_uri":                              base + "/keys",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	}))
	defer issuer.Close()
	for _, origin := range []string{"https://console.example.com:8443", "https://console.example.com:8443/"} {
		flow, err := InitGoogle(t.Context(), GoogleConfig{ClientID: "test-client", ConsoleOrigin: origin, Issuer: issuer.URL})
		if err != nil {
			t.Fatal(err)
		}
		if got := flow.oauth2cfg().RedirectURL; got != "https://console.example.com:8443/auth/google/callback" {
			t.Fatalf("callback does not use console origin: %s", got)
		}
	}
}
