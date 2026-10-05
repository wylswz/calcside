package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"calcside/internal/auth"
	"calcside/internal/config"
	"calcside/internal/node"
	"calcside/internal/node/subproc"
	"calcside/internal/runtime/remote"
	"calcside/internal/store"
	"calcside/internal/store/storetest"
	"calcside/internal/types"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == subproc.ChildArg {
		if err := subproc.RunChild(os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func testConfig(t *testing.T) config.Config {
	t.Helper()
	return config.Config{
		Addr: "127.0.0.1:0", AddrExplicit: true, Dev: true,
		Store: types.DriverSQLite, DSN: storetest.SQLite(t), NodeID: "api-test",
		DefaultTTL: time.Minute, MaxTTL: time.Hour, MaxExecTimeout: time.Minute,
		MaxConcurrentExecs: 2, ReaperInterval: time.Hour, PolicyEvalTimeout: time.Second,
		ExtCacheDir: t.TempDir(), InstanceIsolation: config.IsolationInproc,
	}
}

func request(t *testing.T, h http.Handler, method, path, body string, want int) map[string]any {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Requested-With", "calcside")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s %s: status=%d body=%s", method, path, w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestInitializeApp(t *testing.T) {
	for _, mode := range []string{config.IsolationInproc, config.IsolationProcess, "remote"} {
		t.Run(mode, func(t *testing.T) {
			cfg := testConfig(t)
			if mode == "remote" {
				nd := node.Build(node.Config{Limits: cfg.ExecLimits()})
				t.Cleanup(nd.Close)
				worker := httptest.NewServer(remote.NewHandler(nd.Manager, []byte("test-worker-key")))
				t.Cleanup(worker.Close)
				cfg.Workers = "worker-test=" + strings.TrimPrefix(worker.URL, "http://")
				cfg.WorkerKey = "test-worker-key"
			} else {
				cfg.InstanceIsolation = mode
			}
			app, cleanup, err := initializeApp(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(cleanup)
			if app.Server.Addr != cfg.Addr || app.Sandbox == nil {
				t.Fatalf("incomplete app: %+v", app)
			}
			h := app.Server.Handler
			request(t, h, "GET", "/healthz", "", http.StatusOK)
			if got := request(t, h, "GET", "/api/v1/auth/config", "", http.StatusOK); got["dev_mode"] != true || got["google"] != false {
				t.Fatalf("auth config: %v", got)
			}
			created := request(t, h, "POST", "/api/v1/instances", `{"capabilities":{}}`, http.StatusCreated)
			id := created["instance"].(map[string]any)["id"].(string)
			out := request(t, h, "POST", "/api/v1/instances/"+id+"/exec", `{"code":"x = 41\nprint(x + 1)"}`, http.StatusOK)
			if out["output"] != "42\n" {
				t.Fatalf("exec: %v", out)
			}
			out = request(t, h, "POST", "/api/v1/instances/"+id+"/exec", `{"code":"print(x)"}`, http.StatusOK)
			if out["output"] != "41\n" {
				t.Fatalf("globals not preserved: %v", out)
			}
			if mode == "remote" {
				r := httptest.NewRequest("GET", remote.ExtTreePath, nil)
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != http.StatusUnauthorized {
					t.Fatalf("unsigned extension callback: %d", w.Code)
				}
			}
		})
	}
}

type trackedStore struct {
	store.Store
	closes       int
	adminUpserts int
}

func (s *trackedStore) BootstrapAdminUser(ctx context.Context, username, email, passwordHash string) (*store.User, error) {
	s.adminUpserts++
	return s.Store.BootstrapAdminUser(ctx, username, email, passwordHash)
}

func (s *trackedStore) Close() error {
	s.closes++
	return s.Store.Close()
}

func trackStore(t *testing.T, cfg *config.Config) *trackedStore {
	t.Helper()
	st, err := store.Open(t.Context(), cfg.Store, cfg.DSN)
	if err != nil {
		t.Fatal(err)
	}
	tracked := &trackedStore{Store: st}
	t.Cleanup(func() { _ = st.Close() })
	cfg.Store = types.StoreDriver(t.Name())
	store.RegisterDriver(cfg.Store, func(context.Context, string) (store.Store, error) { return tracked, nil })
	return tracked
}

func TestInitializeAppFailureCleanup(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*config.Config)
		want   string
	}{
		{"basic-auth", func(c *config.Config) { c.AdminUsername = "admin" }, "--admin-username"},
		{"cipher", func(c *config.Config) { c.SecretKey = "invalid" }, "--secret-key:"},
		{"policy", func(c *config.Config) { c.PolicyDir = filepath.Join(t.TempDir(), "[") }, "--policy-dir:"},
		{"worker-key", func(c *config.Config) { c.Workers = "w=127.0.0.1:1" }, "--workers requires --worker-key"},
		{"workers", func(c *config.Config) { c.Workers, c.WorkerKey = "invalid", "test-key" }, "--workers: bad entry"},
		{"dev-address", func(c *config.Config) { c.Addr = "0.0.0.0:8080" }, "dev mode refuses non-loopback"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(t)
			st := trackStore(t, &cfg)
			tc.mutate(&cfg)
			app, cleanup, err := initializeApp(t.Context(), cfg)
			if cleanup != nil {
				t.Cleanup(cleanup)
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) || app != nil || cleanup != nil {
				t.Fatalf("app=%v cleanup=%t err=%v", app, cleanup != nil, err)
			}
			if st.closes != 1 {
				t.Fatalf("store closed %d times, want once", st.closes)
			}
		})
	}
}

func TestInitializeAppProduction(t *testing.T) {
	cfg := testConfig(t)
	cfg.Dev = false
	st := trackStore(t, &cfg)
	app, cleanup, err := initializeApp(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup()
		if st.closes != 1 {
			t.Errorf("store closed %d times, want once", st.closes)
		}
	}()
	request(t, app.Server.Handler, "GET", "/api/v1/me", "", http.StatusUnauthorized)
}

func TestInitializeAppFlushesAuditBeforeClosingStore(t *testing.T) {
	cfg := testConfig(t)
	st := trackStore(t, &cfg)
	var id string
	func() {
		app, cleanup, err := initializeApp(t.Context(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		h := app.Server.Handler
		created := request(t, h, "POST", "/api/v1/instances", `{"capabilities":{}}`, http.StatusCreated)
		id = created["instance"].(map[string]any)["id"].(string)
		request(t, h, "POST", "/api/v1/instances/"+id+"/exec", `{"code":"io.println(42)"}`, http.StatusOK)
	}()
	if st.closes != 1 {
		t.Fatalf("store closed %d times, want once", st.closes)
	}
	reopened, err := store.Open(t.Context(), types.DriverSQLite, cfg.DSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	events, err := reopened.ListAuditEvents(t.Context(), store.AuditFilter{InstanceID: id})
	if err != nil || len(events) == 0 {
		t.Fatalf("audit was not flushed: events=%v err=%v", events, err)
	}
}

func TestInitializeAppDoesNotMigrate(t *testing.T) {
	cfg := testConfig(t)
	cfg.DSN = filepath.Join(t.TempDir(), "unmigrated.db")
	st := trackStore(t, &cfg)
	app, cleanup, err := initializeApp(t.Context(), cfg)
	if cleanup != nil {
		t.Cleanup(cleanup)
	}
	if err == nil || !strings.Contains(err.Error(), "recover:") || app != nil || cleanup != nil {
		t.Fatalf("app=%v cleanup=%t err=%v", app, cleanup != nil, err)
	}
	if st.closes != 1 {
		t.Fatalf("store closed %d times, want once", st.closes)
	}
}

func TestServerAddr(t *testing.T) {
	for _, tc := range []struct {
		cfg  config.Config
		want string
	}{
		{config.Config{Addr: ":8080"}, ":8080"},
		{config.Config{Addr: ":8080", Dev: true}, "127.0.0.1:8080"},
		{config.Config{Addr: "127.0.0.1:8787", Dev: true, AddrExplicit: true}, "127.0.0.1:8787"},
	} {
		if got := serverAddr(tc.cfg); got != tc.want {
			t.Errorf("serverAddr(%+v) = %q, want %q", tc.cfg, got, tc.want)
		}
	}
}

func TestBasicLogin(t *testing.T) {
	cfg := testConfig(t)
	cfg.Dev = false
	cfg.AdminUsername = "admin"
	cfg.AdminPassword = "test-password:with-colon"
	st := trackStore(t, &cfg)
	app, cleanup, err := initializeApp(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	h := app.Server.Handler
	if got := request(t, h, "GET", "/api/v1/auth/config", "", http.StatusOK); got["basic"] != true || got["google"] != false || got["dev_mode"] != false {
		t.Fatalf("auth config: %v", got)
	}
	request(t, h, "GET", "/api/v1/me", "", http.StatusUnauthorized)
	r := httptest.NewRequest("GET", "/api/v1/auth/config", nil)
	r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: "stale-session"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("stale session blocked login configuration: %d", w.Code)
	}
	var cookies []*http.Cookie
	for _, tc := range []struct {
		name, username, password string
		csrf                     bool
		want                     int
	}{
		{"csrf", "admin", cfg.AdminPassword, false, http.StatusForbidden},
		{"wrong-user", "other", cfg.AdminPassword, true, http.StatusUnauthorized},
		{"wrong-password", "admin", "wrong", true, http.StatusUnauthorized},
		{"empty", "", "", true, http.StatusUnauthorized},
		{"success", "admin", cfg.AdminPassword, true, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/auth/basic/login", nil)
			r.SetBasicAuth(tc.username, tc.password)
			r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: "stale-session"})
			if tc.csrf {
				r.Header.Set("X-Requested-With", "calcside")
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if tc.want == http.StatusOK {
				cookies = w.Result().Cookies()
			} else if len(w.Result().Cookies()) != 0 || st.adminUpserts != 0 {
				t.Fatal("failed login issued a cookie or provisioned an administrator")
			}
		})
	}
	if len(cookies) != 1 || cookies[0].Name != auth.SessionCookie || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode || cookies[0].Path != "/" {
		t.Fatal("missing or unsafe session cookie")
	}
	call := func(method, path, body string, csrf bool, want int) map[string]any {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if csrf {
			r.Header.Set("X-Requested-With", "calcside")
		}
		for _, cookie := range cookies {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: status=%d body=%s", method, path, w.Code, w.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	me := call("GET", "/api/v1/me", "", false, http.StatusOK)
	if me["kind"] != "session" || me["user"].(map[string]any)["name"] != "admin" || me["user"].(map[string]any)["is_admin"] != true {
		t.Fatalf("not a Basic Auth session: %v", me)
	}
	persisted, err := st.GetUser(t.Context(), me["user"].(map[string]any)["id"].(string))
	if err != nil || !persisted.IsAdmin || st.adminUpserts != 1 {
		t.Fatalf("administrator not persisted on first login: %v", err)
	}
	call("POST", "/api/v1/keys", `{"name":"basic-key"}`, false, http.StatusForbidden)
	call("POST", "/api/v1/keys", `{"name":"basic-key"}`, true, http.StatusCreated)
	call("POST", "/auth/logout", "", true, http.StatusOK)
	call("GET", "/api/v1/me", "", false, http.StatusUnauthorized)
	r = httptest.NewRequest("GET", "/api/v1/me", nil)
	r.SetBasicAuth(cfg.AdminUsername, cfg.AdminPassword)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatal("Basic credentials must not authenticate API requests directly")
	}
	r = httptest.NewRequest("POST", "/auth/basic/login", nil)
	r.SetBasicAuth(cfg.AdminUsername, cfg.AdminPassword)
	r.Header.Set("X-Requested-With", "calcside")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("repeat login: %d %s", w.Code, w.Body.String())
	}
	cookies = w.Result().Cookies()
	again := call("GET", "/api/v1/me", "", false, http.StatusOK)
	if again["user"].(map[string]any)["id"] != me["user"].(map[string]any)["id"] || again["user"].(map[string]any)["is_admin"] != true {
		t.Fatal("repeat login changed user identity")
	}
	keys := call("GET", "/api/v1/keys", "", false, http.StatusOK)
	if len(keys["keys"].([]any)) != 1 {
		t.Fatal("repeat login lost access to API keys")
	}
}

func TestBasicLoginDisabled(t *testing.T) {
	cfg := testConfig(t)
	cfg.Dev = false
	app, cleanup, err := initializeApp(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	if got := request(t, app.Server.Handler, "GET", "/api/v1/auth/config", "", http.StatusOK); got["basic"] != false {
		t.Fatalf("auth config: %v", got)
	}
	request(t, app.Server.Handler, "POST", "/auth/basic/login", "", http.StatusNotFound)
}

func TestConfiguredAdminUpgradesExistingUser(t *testing.T) {
	cfg := testConfig(t)
	cfg.Dev = false
	cfg.AdminUsername, cfg.AdminPassword = "admin", "test-password"
	st := trackStore(t, &cfg)
	legacy, err := st.UpsertUserByEmail(t.Context(), "admin@basic.localhost", "admin", "")
	if err != nil || legacy.IsAdmin {
		t.Fatalf("create legacy user: %v", err)
	}
	app, cleanup, err := initializeApp(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	if st.adminUpserts != 0 {
		t.Fatal("administrator provisioned before first login")
	}
	for _, password := range []string{"wrong", cfg.AdminPassword} {
		r := httptest.NewRequest("POST", "/auth/basic/login", nil)
		r.SetBasicAuth(cfg.AdminUsername, password)
		r.Header.Set("X-Requested-With", "calcside")
		w := httptest.NewRecorder()
		app.Server.Handler.ServeHTTP(w, r)
		wantAdmin, wantStatus := password == cfg.AdminPassword, http.StatusUnauthorized
		if wantAdmin {
			wantStatus = http.StatusOK
		}
		if w.Code != wantStatus {
			t.Fatalf("login: %d %s", w.Code, w.Body.String())
		}
		got, err := st.GetUser(t.Context(), legacy.ID)
		if err != nil || got.IsAdmin != wantAdmin || !got.CreatedAt.Equal(legacy.CreatedAt) {
			t.Fatalf("unexpected legacy admin state: %v %+v", err, got)
		}
	}
}

func TestProfilePasswordChange(t *testing.T) {
	cfg := testConfig(t)
	cfg.Dev = false
	cfg.AdminUsername, cfg.AdminPassword = "admin@example.com", "initial-password"
	app, cleanup, err := initializeApp(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	send := func(method, path, body string, cookies []*http.Cookie, headers map[string]string, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		for key, value := range headers {
			r.Header.Set(key, value)
		}
		for _, cookie := range cookies {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		app.Server.Handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "password_hash") || strings.Contains(w.Body.String(), "$2a$") {
			t.Fatal("response exposed password hash")
		}
		return w
	}
	login := func(password string, want int) []*http.Cookie {
		t.Helper()
		r := httptest.NewRequest("POST", "/auth/basic/login", nil)
		r.SetBasicAuth(cfg.AdminUsername, password)
		return send("POST", "/auth/basic/login", "", nil, map[string]string{
			"Authorization": r.Header.Get("Authorization"), "X-Requested-With": "calcside",
		}, want).Result().Cookies()
	}
	csrf := map[string]string{"X-Requested-With": "calcside"}
	cookies, otherSession := login(cfg.AdminPassword, 200), login(cfg.AdminPassword, 200)
	me := send("GET", "/api/v1/me", "", cookies, nil, 200)
	if !strings.Contains(me.Body.String(), `"has_password":true`) || !strings.Contains(me.Body.String(), `"is_admin":true`) {
		t.Fatal("profile does not expose local administrator status")
	}
	body := `{"current_password":"initial-password","new_password":"changed-password"}`
	send("POST", "/api/v1/me/password", body, nil, csrf, 401)
	send("POST", "/api/v1/me/password", body, cookies, nil, 403)
	send("POST", "/api/v1/me/password", `{"current_password":"wrong","new_password":"changed-password"}`, cookies, csrf, 403)
	send("POST", "/api/v1/me/password", `{"current_password":"initial-password","new_password":"short"}`, cookies, csrf, 400)
	send("POST", "/api/v1/me/password", `{"current_password":"initial-password","new_password":"`+strings.Repeat("a", 73)+`"}`, cookies, csrf, 400)
	created := send("POST", "/api/v1/keys", `{"name":"profile-key"}`, cookies, csrf, 201)
	var key map[string]any
	if err := json.Unmarshal(created.Body.Bytes(), &key); err != nil {
		t.Fatal(err)
	}
	send("POST", "/api/v1/me/password", body, nil, map[string]string{"Authorization": "Bearer " + key["secret"].(string)}, 403)
	changed := send("POST", "/api/v1/me/password", body, cookies, csrf, 200)
	cleared := changed.Result().Cookies()
	if len(cleared) != 1 || cleared[0].Name != auth.SessionCookie || cleared[0].MaxAge != -1 {
		t.Fatal("password change did not clear session cookie")
	}
	send("GET", "/api/v1/me", "", cookies, nil, 401)
	send("GET", "/api/v1/me", "", otherSession, nil, 401)
	login(cfg.AdminPassword, 401)
	fresh := login("changed-password", 200)
	send("GET", "/api/v1/me", "", fresh, nil, 200)
}
