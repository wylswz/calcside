package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"calcside/internal/store"
)

func TestBasicCredentials(t *testing.T) {
	for _, tc := range []struct {
		name, username, password string
		valid                    bool
	}{
		{"disabled", "", "", true},
		{"enabled", "Admin_1.test-user", "test-password", true},
		{"unicode-password", "admin", "密码:with:colons", true},
		{"max-username", strings.Repeat("a", 64), "test-password", true},
		{"long-username", strings.Repeat("a", 65), "test-password", false},
		{"username-only", "admin", "", false},
		{"password-only", "", "test-password", false},
		{"colon", "admin:root", "test-password", false},
		{"space", "admin user", "test-password", false},
		{"newline", "admin\n", "test-password", false},
		{"email", "admin@example.com", "test-password", true},
		{"short-password", "admin", "short", false},
		{"long-password", "admin", strings.Repeat("a", 73), false},
		{"unicode-username", "管理员", "test-password", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, err := NewBasicLogin(t.Context(), NewService(&basicLoginStore{}, false), tc.username, tc.password)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%t, err=%v", tc.valid, err)
			}
			if (h != nil) != (tc.valid && tc.username != "") {
				t.Fatal("unexpected enabled state")
			}
			if err != nil && tc.password != "" && strings.Contains(err.Error(), tc.password) {
				t.Fatal("validation error leaked password")
			}
		})
	}
}

func TestBasicLoginRejects(t *testing.T) {
	for _, tc := range []struct {
		name, method, username, password, header string
		csrf                                     bool
		want                                     int
	}{
		{"get", "GET", "admin", "test-password", "", true, http.StatusMethodNotAllowed},
		{"csrf", "POST", "admin", "test-password", "", false, http.StatusForbidden},
		{"missing", "POST", "", "", "", true, http.StatusUnauthorized},
		{"malformed", "POST", "", "", "Basic not-base64!", true, http.StatusUnauthorized},
		{"bearer", "POST", "", "", "Bearer test-password", true, http.StatusUnauthorized},
		{"wrong-username", "POST", "other", "test-password", "", true, http.StatusUnauthorized},
		{"wrong-password", "POST", "admin", "wrong", "", true, http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, err := NewBasicLogin(t.Context(), NewService(&basicLoginStore{}, false), "admin", "test-password")
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest(tc.method, "/auth/basic/login", nil)
			if tc.username != "" {
				r.SetBasicAuth(tc.username, tc.password)
			} else if tc.header != "" {
				r.Header.Set("Authorization", tc.header)
			}
			if tc.csrf {
				r.Header.Set("X-Requested-With", "calcside")
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want || len(w.Result().Cookies()) != 0 {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("login response must not be cached")
			}
			if strings.Contains(w.Body.String(), "test-password") || strings.Contains(w.Body.String(), "admin") {
				t.Fatal("error leaked credentials")
			}
		})
	}
}

type basicLoginStore struct {
	store.Store
	userErr, sessionErr error
	user                *store.User
	session             *store.Session
	email, name         string
	ctx                 context.Context
	bootstraps          int
}

func (s *basicLoginStore) GetUserByUsername(ctx context.Context, username string) (*store.User, error) {
	if s.user == nil || s.user.Username != username {
		return nil, store.ErrNotFound
	}
	return s.user, nil
}

func (s *basicLoginStore) HasPasswordUsers(context.Context) (bool, error) {
	return s.user != nil && s.user.PasswordHash != "", nil
}

func (s *basicLoginStore) BootstrapAdminUser(ctx context.Context, username, email, passwordHash string) (*store.User, error) {
	s.ctx, s.email, s.name = ctx, email, username
	s.bootstraps++
	if s.userErr != nil {
		return nil, s.userErr
	}
	if s.user == nil {
		s.user = &store.User{ID: "usr_basic", Email: email, Name: username, Username: username, IsAdmin: true, PasswordHash: passwordHash}
	}
	return s.user, nil
}

func (s *basicLoginStore) CreatePasswordSession(ctx context.Context, session *store.Session, passwordHash string) error {
	s.ctx, s.session = ctx, session
	if s.user.PasswordHash != passwordHash {
		return store.ErrConflict
	}
	return s.sessionErr
}

func TestBasicLoginSession(t *testing.T) {
	for _, failure := range []string{"", "user", "session"} {
		t.Run("failure="+failure, func(t *testing.T) {
			st := &basicLoginStore{}
			if failure == "user" {
				st.userErr = errors.New("private store details")
			} else if failure == "session" {
				st.sessionErr = errors.New("private store details")
			}
			h, err := NewBasicLogin(t.Context(), NewService(st, true), "admin", "密码:with:colons")
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("POST", "/auth/basic/login", nil).WithContext(t.Context())
			r.SetBasicAuth("admin", "密码:with:colons")
			r.Header.Set("X-Requested-With", "calcside")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if st.ctx != r.Context() || st.email != "admin@basic.localhost" || st.name != "admin" {
				t.Fatal("incorrect user identity or request context")
			}
			if failure != "" {
				if w.Code != http.StatusInternalServerError || len(w.Result().Cookies()) != 0 || strings.Contains(w.Body.String(), "private store details") {
					t.Fatalf("unsafe error response: status=%d body=%s", w.Code, w.Body.String())
				}
				return
			}
			cookies := w.Result().Cookies()
			if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"ok":true}` || len(cookies) != 1 {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			c := cookies[0]
			if c.Name != SessionCookie || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || !c.Expires.After(time.Now()) {
				t.Fatal("unsafe session cookie")
			}
			if st.session == nil || st.session.UserID != "usr_basic" || st.session.Hash != hashToken(c.Value) || st.session.Hash == c.Value {
				t.Fatal("incorrect stored session")
			}
		})
	}
}

func TestBasicLoginRateLimit(t *testing.T) {
	h, err := NewBasicLogin(t.Context(), NewService(&basicLoginStore{}, false), "admin", "test-password")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 11; i++ {
		r := httptest.NewRequest("POST", "/auth/basic/login", nil)
		r.Header.Set("X-Requested-With", "calcside")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		want := http.StatusUnauthorized
		if i == 10 {
			want = http.StatusTooManyRequests
			retry, err := strconv.Atoi(w.Header().Get("Retry-After"))
			if err != nil || retry < 1 || retry > 60 {
				t.Fatal("missing or invalid Retry-After")
			}
		}
		if w.Code != want || len(w.Result().Cookies()) != 0 {
			t.Fatalf("attempt %d: status=%d", i, w.Code)
		}
	}
	if retry := h.retryAfter(h.window.Add(time.Minute - time.Nanosecond)); retry != 1 {
		t.Fatalf("retry=%d, want 1", retry)
	}
	if retry := h.retryAfter(h.window.Add(time.Minute)); retry != 0 {
		t.Fatalf("rate limit did not reset: %d", retry)
	}
}

func TestBasicLoginConcurrentRateLimit(t *testing.T) {
	h := &BasicLogin{}
	now := time.Now()
	var allowed atomic.Int32
	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() {
			if h.retryAfter(now) == 0 {
				allowed.Add(1)
			}
		})
	}
	wg.Wait()
	if allowed.Load() != 10 {
		t.Fatalf("allowed %d requests, want 10", allowed.Load())
	}
}

func TestBasicLoginUsesDatabasePassword(t *testing.T) {
	st := &basicLoginStore{}
	svc := NewService(st, false)
	h, err := NewBasicLogin(t.Context(), svc, "admin@example.com", "initial-password")
	if err != nil {
		t.Fatal(err)
	}
	login := func(h *BasicLogin, password string, want int) {
		t.Helper()
		r := httptest.NewRequest("POST", "/auth/basic/login", nil)
		r.SetBasicAuth("admin@example.com", password)
		r.Header.Set("X-Requested-With", "calcside")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("login: %d %s", w.Code, w.Body.String())
		}
	}
	login(h, "initial-password", http.StatusOK)
	if !st.user.IsAdmin || st.user.Email == st.user.Username || st.user.Email != "admin%40example.com@basic.localhost" {
		t.Fatal("email-form administrator must have an independent identity")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(st.user.PasswordHash), []byte("initial-password")); err != nil {
		t.Fatal("stored password is not a bcrypt hash")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("changed-password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	st.user.PasswordHash = string(hash)
	login(h, "initial-password", http.StatusUnauthorized)
	login(h, "changed-password", http.StatusOK)
	withoutConfig, err := NewBasicLogin(t.Context(), svc, "", "")
	if err != nil || withoutConfig == nil {
		t.Fatalf("database login disabled without bootstrap config: %v", err)
	}
	login(withoutConfig, "changed-password", http.StatusOK)
	reconfigured, err := NewBasicLogin(t.Context(), svc, "admin@example.com", "different-bootstrap-password")
	if err != nil {
		t.Fatal(err)
	}
	login(reconfigured, "different-bootstrap-password", http.StatusUnauthorized)
	login(reconfigured, "changed-password", http.StatusOK)
	if st.bootstraps != 1 || st.user.PasswordHash != string(hash) {
		t.Fatal("subsequent login overwrote stored credentials")
	}
}

func TestBasicPasswordsHaveRandomSalts(t *testing.T) {
	one, err := NewBasicLogin(t.Context(), nil, "admin", "test-password")
	if err != nil {
		t.Fatal(err)
	}
	two, err := NewBasicLogin(t.Context(), nil, "admin", "test-password")
	if err != nil {
		t.Fatal(err)
	}
	if string(one.passwordHash) == string(two.passwordHash) {
		t.Fatal("identical passwords used identical salts")
	}
	if cost, err := bcrypt.Cost(one.passwordHash); err != nil || cost != bcrypt.DefaultCost {
		t.Fatalf("bcrypt cost: %d %v", cost, err)
	}
}
