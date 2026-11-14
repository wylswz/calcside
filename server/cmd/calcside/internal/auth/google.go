package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"calcside/internal/types"
)

// GoogleConfig configures Google OIDC login.
type GoogleConfig struct {
	ClientID       string
	ClientSecret   string
	ConsoleOrigin  string // external base URL for the redirect_uri
	AllowedDomains []string
	Issuer         string // default https://accounts.google.com
	provider       *oidc.Provider
	verifier       *oidc.IDTokenVerifier
}

const oidcCookie = "cs_oidc"

// InitGoogle verifies configuration and discovers the provider. Returns a
// configured handler set, or nil if client id is empty (disabled cleanly).
func InitGoogle(ctx context.Context, cfg GoogleConfig) (*GoogleFlow, error) {
	if cfg.ClientID == "" {
		return nil, nil
	}
	if cfg.Issuer == "" {
		cfg.Issuer = "https://accounts.google.com"
	}
	p, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, err
	}
	cfg.provider = p
	cfg.verifier = p.Verifier(&oidc.Config{ClientID: cfg.ClientID})
	return &GoogleFlow{cfg: cfg}, nil
}

// GoogleFlow implements the OIDC login/callback handlers.
type GoogleFlow struct {
	cfg GoogleConfig
	svc *Service // set via Bind
}

// Bind attaches the session-issuing service.
func (g *GoogleFlow) Bind(svc *Service) { g.svc = svc }

func (g *GoogleFlow) oauth2cfg() *oauth2.Config {
	return &oauth2.Config{
		ClientID:     g.cfg.ClientID,
		ClientSecret: g.cfg.ClientSecret,
		Endpoint:     g.cfg.provider.Endpoint(),
		RedirectURL:  strings.TrimSuffix(g.cfg.ConsoleOrigin, "/") + "/auth/google/callback",
		Scopes:       []string{oidc.ScopeOpenID, "email", "profile"},
	}
}

type oidcState struct {
	State   string `json:"s"`
	Nonce   string `json:"n"`
	Verifer string `json:"v"` // PKCE verifier
}

// LoginHandler starts the OIDC flow: state + nonce + PKCE in a cookie.
func (g *GoogleFlow) LoginHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state := base64.RawURLEncoding.EncodeToString(randBytes(16))
		nonce := base64.RawURLEncoding.EncodeToString(randBytes(16))
		verifier := oauth2.GenerateVerifier()
		st := oidcState{State: state, Nonce: nonce, Verifer: verifier}
		b, _ := json.Marshal(st)
		http.SetCookie(w, &http.Cookie{
			Name:     oidcCookie,
			Value:    base64.RawURLEncoding.EncodeToString(b),
			Path:     "/auth",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			Secure:   g.svc.cookieSecure,
			MaxAge:   300,
		})
		url := g.oauth2cfg().AuthCodeURL(state,
			oauth2.SetAuthURLParam("nonce", nonce),
			oauth2.S256ChallengeOption(verifier),
		)
		http.Redirect(w, r, url, http.StatusFound)
	})
}

// CallbackHandler verifies state/nonce/id_token, upserts the user, issues
// a session, and redirects to /.
func (g *GoogleFlow) CallbackHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fail := func(status int, msg string) {
			writeErr(w, status, types.ErrCodeAuthFailed, msg)
		}
		c, err := r.Cookie(oidcCookie)
		if err != nil {
			fail(http.StatusBadRequest, "missing oidc state cookie")
			return
		}
		var st oidcState
		data, err := base64.RawURLEncoding.DecodeString(c.Value)
		if err != nil || json.Unmarshal(data, &st) != nil {
			fail(http.StatusBadRequest, "bad oidc state cookie")
			return
		}
		if r.URL.Query().Get("state") != st.State {
			fail(http.StatusBadRequest, "state mismatch")
			return
		}
		if e := r.URL.Query().Get("error"); e != "" {
			fail(http.StatusBadRequest, "provider error: "+e)
			return
		}
		code := r.URL.Query().Get("code")
		tok, err := g.oauth2cfg().Exchange(r.Context(), code, oauth2.VerifierOption(st.Verifer))
		if err != nil {
			fail(http.StatusBadGateway, "token exchange failed")
			return
		}
		rawID, ok := tok.Extra("id_token").(string)
		if !ok {
			fail(http.StatusBadGateway, "no id_token")
			return
		}
		idToken, err := g.cfg.verifier.Verify(r.Context(), rawID)
		if err != nil {
			fail(http.StatusBadGateway, "id_token verify failed")
			return
		}
		var claims struct {
			Email         string `json:"email"`
			EmailVerified bool   `json:"email_verified"`
			Name          string `json:"name"`
			Nonce         string `json:"nonce"`
			HD            string `json:"hd"`
			Sub           string `json:"sub"`
		}
		if err := idToken.Claims(&claims); err != nil {
			fail(http.StatusBadGateway, "bad id_token claims")
			return
		}
		if claims.Nonce != st.Nonce {
			fail(http.StatusBadRequest, "nonce mismatch")
			return
		}
		if !claims.EmailVerified || claims.Email == "" {
			fail(http.StatusForbidden, "email not verified")
			return
		}
		if len(g.cfg.AllowedDomains) > 0 {
			domain := claims.HD
			if domain == "" {
				if i := strings.LastIndex(claims.Email, "@"); i >= 0 {
					domain = claims.Email[i+1:]
				}
			}
			ok := false
			for _, d := range g.cfg.AllowedDomains {
				if strings.EqualFold(d, domain) {
					ok = true
					break
				}
			}
			if !ok {
				fail(http.StatusForbidden, "email domain not allowed")
				return
			}
		}
		u, err := g.svc.st.UpsertUserByEmail(r.Context(), claims.Email, claims.Name, claims.Sub)
		if err != nil {
			fail(http.StatusInternalServerError, "user upsert failed")
			return
		}
		raw, err := g.svc.CreateSession(r.Context(), u.ID)
		if err != nil {
			fail(http.StatusInternalServerError, "session failed")
			return
		}
		g.svc.setSessionCookie(w, raw)
		http.SetCookie(w, &http.Cookie{Name: oidcCookie, Path: "/auth", MaxAge: -1, HttpOnly: true})
		http.Redirect(w, r, "/", http.StatusFound)
	})
}
