// Package auth implements principals: Google OIDC sessions (cookie),
// API keys (bearer), and CSRF protection for cookie-authenticated
// requests.
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

// SessionCookie is the name of the session cookie.
const SessionCookie = "cs_session"

const (
	apiKeyPrefix    = "cs_"
	sessionCookie   = SessionCookie
	sessionDuration = 7 * 24 * time.Hour
	csrfHeader      = "X-Requested-With"
	csrfValue       = "calcside"
)

// Service wraps the store for auth operations.
type Service struct {
	st           store.Store
	cookieSecure bool
}

func NewService(st store.Store, cookieSecure bool) *Service {
	return &Service{st: st, cookieSecure: cookieSecure}
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
	return s.createSession(ctx, userID, "")
}

func (s *Service) createSession(ctx context.Context, userID, passwordHash string) (string, error) {
	raw := base64.RawURLEncoding.EncodeToString(randBytes(32))
	sess := &store.Session{
		Hash:      hashToken(raw),
		UserID:    userID,
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().Add(sessionDuration).UTC(),
	}
	var err error
	if passwordHash != "" {
		err = s.st.CreatePasswordSession(ctx, sess, passwordHash)
	} else {
		err = s.st.CreateSession(ctx, sess)
	}
	if err != nil {
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

// ClearSessionCookie expires the session cookie on the response.
func (s *Service) ClearSessionCookie(w http.ResponseWriter) {
	s.clearSessionCookie(w)
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

// NeedsCSRF reports whether a principal authenticated by cookie or
// dev-mode anonymity must present the CSRF header on mutations.
func NeedsCSRF(p *Principal) bool { return p != nil && !p.ViaKey() }

// CSRFHeaderOK reports whether the request carries the required
// anti-CSRF header.
func CSRFHeaderOK(r *http.Request) bool {
	return r.Header.Get(csrfHeader) == csrfValue
}

// WithPrincipal stores the principal on a context.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return withPrincipal(ctx, p)
}

func writeErr(w http.ResponseWriter, status int, code types.APIErrorCode, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"code": string(code), "message": msg},
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
