package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"calcside/internal/store"
	"calcside/internal/types"
)

type BasicLogin struct {
	svc          *Service
	username     string
	usernameHash [sha256.Size]byte
	passwordHash []byte
	mu           sync.Mutex
	window       time.Time
	attempts     int
}

func ValidatePassword(password string) error {
	if len(password) < 8 || len(password) > 72 {
		return fmt.Errorf("password must contain 8-72 bytes")
	}
	return nil
}

func ValidateAdminCredentials(username, password string) error {
	if username == "" && password == "" {
		return nil
	}
	if username == "" || password == "" {
		return fmt.Errorf("--admin-username and --admin-password must be configured together")
	}
	valid := len(username) <= 64 && !strings.ContainsFunc(username, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._-", r))
	})
	if !valid && len(username) <= 254 && !strings.ContainsAny(username, ":\r\n") {
		address, err := mail.ParseAddress(username)
		valid = err == nil && address.Address == username && address.Name == ""
	}
	if !valid {
		return fmt.Errorf("--admin-username must be a simple username (1-64 characters) or an email address (up to 254 bytes)")
	}
	if err := ValidatePassword(password); err != nil {
		return fmt.Errorf("--admin-password: %w", err)
	}
	return nil
}

func NewBasicLogin(ctx context.Context, svc *Service, username, password string) (*BasicLogin, error) {
	if err := ValidateAdminCredentials(username, password); err != nil {
		return nil, err
	}
	if username == "" {
		enabled, err := svc.st.HasPasswordUsers(ctx)
		if err != nil || !enabled {
			return nil, err
		}
		password = base64.RawURLEncoding.EncodeToString(randBytes(32))
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	return &BasicLogin{
		svc: svc, username: username,
		usernameHash: sha256.Sum256([]byte(username)), passwordHash: hash,
	}, nil
}

func (b *BasicLogin) retryAfter(now time.Time) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !now.Before(b.window.Add(time.Minute)) {
		b.window, b.attempts = now, 0
	}
	if b.attempts >= 10 {
		return int((b.window.Add(time.Minute).Sub(now) + time.Second - 1) / time.Second)
	}
	b.attempts++
	return 0
}

func (b *BasicLogin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeErr(w, http.StatusMethodNotAllowed, types.ErrCodeMethodNotAllowed, "method not allowed")
		return
	}
	if !CSRFHeaderOK(r) {
		writeErr(w, http.StatusForbidden, types.ErrCodeCSRF, "missing X-Requested-With header")
		return
	}
	if seconds := b.retryAfter(time.Now()); seconds > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		writeErr(w, http.StatusTooManyRequests, types.ErrCodeTooMany, "too many login attempts; try again later")
		return
	}
	fail := func() { writeErr(w, http.StatusUnauthorized, types.ErrCodeAuthFailed, "invalid username or password") }
	username, password, ok := r.BasicAuth()
	if !ok || len(username) > 254 || len(password) > 72 {
		fail()
		return
	}
	u, err := b.svc.st.GetUserByUsername(r.Context(), username)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusInternalServerError, types.ErrCodeInternal, "user lookup failed")
		return
	}
	hash := b.passwordHash
	if u != nil {
		hash = []byte(u.PasswordHash)
	}
	if bcrypt.CompareHashAndPassword(hash, []byte(password)) != nil {
		fail()
		return
	}
	if u == nil {
		usernameHash := sha256.Sum256([]byte(username))
		if b.username == "" || subtle.ConstantTimeCompare(usernameHash[:], b.usernameHash[:]) != 1 {
			fail()
			return
		}
		u, err = b.svc.st.BootstrapAdminUser(r.Context(), username, url.QueryEscape(username)+"@basic.localhost", string(b.passwordHash))
		if err != nil {
			writeErr(w, http.StatusInternalServerError, types.ErrCodeInternal, "administrator initialization failed")
			return
		}
		if u.Username != username || bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) != nil {
			fail()
			return
		}
	}
	raw, err := b.svc.createSession(r.Context(), u.ID, u.PasswordHash)
	if errors.Is(err, store.ErrConflict) {
		fail()
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, types.ErrCodeInternal, "session failed")
		return
	}
	b.svc.setSessionCookie(w, raw)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}
