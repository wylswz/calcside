// Package auth implements principals: Google OIDC sessions (cookie),
// API keys (bearer), a dev-login escape hatch, and middleware with CSRF
// protection for cookie-authenticated requests.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"calcside/internal/store"
	"calcside/internal/types"
)

var _ = fmt.Sprintf

// Principal is an authenticated user.
type Principal struct {
	User    *store.User
	Kind    types.AuthKind // session | api_key
	KeyID   string         // set when Kind == api_key
	Session string         // raw session token when cookie-auth
}

// ViaKey reports whether the principal authenticated by API key.
func (p *Principal) ViaKey() bool { return p.Kind == types.AuthAPIKey }

type ctxKey int

const principalKey ctxKey = 0

// FromContext returns the request principal, or nil.
func FromContext(ctx context.Context) *Principal {
	p, _ := ctx.Value(principalKey).(*Principal)
	return p
}

func withPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, principalKey, p)
}

const (
	apiKeyPrefix    = "cs_"
	sessionCookie   = "cs_session"
	sessionDuration = 7 * 24 * time.Hour
	csrfHeader      = "X-Requested-With"
	csrfValue       = "calcside"
)

// Service wraps the store for auth operations.
type Service struct {
	st           store.Store
	cookieSecure bool
	devLogin     bool
}

func NewService(st store.Store, cookieSecure, devLogin bool) *Service {
	return &Service{st: st, cookieSecure: cookieSecure, devLogin: devLogin}
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

const b62 = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// NewAPIKey generates `cs_` + 32 random bytes base62, returns the secret
// (shown once) and the row (hash+prefix stored).
func NewAPIKey(userID, name string, expiresAt *time.Time) (secret string, key *store.APIKey) {
	raw := randBytes(32)
	var sb strings.Builder
	for _, b := range raw {
		sb.WriteByte(b62[int(b)%62])
	}
	secret = apiKeyPrefix + sb.String()
	sum := sha256.Sum256([]byte(secret))
	key = &store.APIKey{
		ID:        store.NewID(store.PrefixAPIKey),
		UserID:    userID,
		Name:      name,
		Prefix:    secret[:8],
		Hash:      hex.EncodeToString(sum[:]),
		ExpiresAt: expiresAt,
	}
	return secret, key
}

func hashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

// CreateSession makes a session for the user and returns the cookie value.
func (s *Service) CreateSession(ctx context.Context, userID string) (string, error) {
	raw := base64.RawURLEncoding.EncodeToString(randBytes(32))
	sess := &store.Session{
		Hash:      hashToken(raw),
		UserID:    userID,
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().Add(sessionDuration).UTC(),
	}
	if err := s.st.CreateSession(ctx, sess); err != nil {
		return "", err
	}
	return raw, nil
}

func (s *Service) DestroySession(ctx context.Context, raw string) {
	_ = s.st.DeleteSession(ctx, hashToken(raw))
}

func (s *Service) setSessionCookie(w http.ResponseWriter, raw string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    raw,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.cookieSecure,
		Expires:  time.Now().Add(sessionDuration),
	})
}

func (s *Service) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.cookieSecure,
		MaxAge:   -1,
	})
}

// Resolve finds the principal for a request: bearer API key first, then
// session cookie.
func (s *Service) Resolve(ctx context.Context, r *http.Request) (*Principal, error) {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		tok := strings.TrimPrefix(h, "Bearer ")
		if !strings.HasPrefix(tok, apiKeyPrefix) {
			return nil, errors.New("malformed bearer token")
		}
		k, err := s.st.GetAPIKeyByHash(ctx, hashToken(tok))
		if err != nil {
			return nil, errors.New("invalid API key")
		}
		now := time.Now()
		if k.RevokedAt != nil || (k.ExpiresAt != nil && k.ExpiresAt.Before(now)) {
			return nil, errors.New("API key expired or revoked")
		}
		u, err := s.st.GetUser(ctx, k.UserID)
		if err != nil {
			return nil, errors.New("key owner missing")
		}
		_ = s.st.TouchAPIKey(ctx, k.ID, now)
		return &Principal{User: u, Kind: types.AuthAPIKey, KeyID: k.ID}, nil
	}
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		sess, err := s.st.GetSession(ctx, hashToken(c.Value))
		if err != nil {
			return nil, errors.New("invalid session")
		}
		if sess.ExpiresAt.Before(time.Now()) {
			return nil, errors.New("session expired")
		}
		u, err := s.st.GetUser(ctx, sess.UserID)
		if err != nil {
			return nil, errors.New("session owner missing")
		}
		return &Principal{User: u, Kind: types.AuthSession, Session: c.Value}, nil
	}
	return nil, nil
}

// Middleware resolves the principal and enforces CSRF on cookie-auth
// mutations.
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, err := s.Resolve(r.Context(), r)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, types.ErrCodeUnauthorized, err.Error())
			return
		}
		if p != nil && !p.ViaKey() && r.Method != http.MethodGet && r.Method != http.MethodHead {
			if r.Header.Get(csrfHeader) != csrfValue {
				writeErr(w, http.StatusForbidden, types.ErrCodeCSRF, "missing "+csrfHeader+" header")
				return
			}
		}
		if p != nil {
			r = r.WithContext(withPrincipal(r.Context(), p))
		}
		next.ServeHTTP(w, r)
	})
}

// RequireAuth rejects unauthenticated requests.
func RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if FromContext(r.Context()) == nil {
			writeErr(w, http.StatusUnauthorized, types.ErrCodeUnauthorized, "authentication required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireSession rejects API-key-authenticated requests (key management).
func RequireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := FromContext(r.Context())
		if p == nil {
			writeErr(w, http.StatusUnauthorized, types.ErrCodeUnauthorized, "authentication required")
			return
		}
		if p.ViaKey() {
			writeErr(w, http.StatusForbidden, types.ErrCodeForbidden, "session required to manage API keys")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeErr(w http.ResponseWriter, status int, code types.APIErrorCode, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"code": string(code), "message": msg},
	})
}

// DevLoginEnabled reports whether the dev login endpoint is active.
func (s *Service) DevLoginEnabled() bool { return s.devLogin }

// DevLoginHandler creates a session for {"email": ...} — dev only.
func (s *Service) DevLoginHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.devLogin {
			writeErr(w, http.StatusNotFound, types.ErrCodeNotFound, "dev login disabled")
			return
		}
		var body struct {
			Email string `json:"email"`
			Name  string `json:"name"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil || body.Email == "" {
			writeErr(w, http.StatusBadRequest, types.ErrCodeBadRequest, "email required")
			return
		}
		u, err := s.st.UpsertUserByEmail(r.Context(), body.Email, body.Name, "")
		if err != nil {
			writeErr(w, http.StatusInternalServerError, types.ErrCodeInternal, err.Error())
			return
		}
		raw, err := s.CreateSession(r.Context(), u.ID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, types.ErrCodeInternal, err.Error())
			return
		}
		s.setSessionCookie(w, raw)
		slog.Warn("dev login used", "email", body.Email)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"user": u})
	})
}

// LogoutHandler destroys the session cookie.
func (s *Service) LogoutHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := FromContext(r.Context()); p != nil && p.Session != "" {
			s.DestroySession(r.Context(), p.Session)
		}
		s.clearSessionCookie(w)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
}
