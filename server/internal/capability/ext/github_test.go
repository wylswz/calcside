package ext

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"calcside/internal/capability"
	capnet "calcside/internal/capability/net"
	"calcside/internal/engine"
	"calcside/internal/hostmatch"
	"calcside/internal/secrets"
	"calcside/internal/types"
)

const ghContribRoot = "../../../../contrib"

// ghCall records one mocked net request.
type ghCall struct {
	method  string
	rawurl  string
	path    string
	query   url.Values
	headers map[string]string
	body    string
	ct      string
}

// ghMock is a scripted `net` binding: it records every request and
// answers from respond as (status, headers, body).
type ghMock struct {
	mu      sync.Mutex
	calls   []ghCall
	respond func(c ghCall) (status int, headers map[string]string, body string)
}

func (m *ghMock) module() starlark.Value {
	return starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{
		"request": starlark.NewBuiltin("net.request", m.request),
		"post":    starlark.NewBuiltin("net.post", m.post),
		"get":     starlark.NewBuiltin("net.get", m.get),
	})
}

func (m *ghMock) nCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func (m *ghMock) callAt(i int) ghCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls[i]
}

func (m *ghMock) last() ghCall { return m.callAt(m.nCalls() - 1) }

func ghHeaderMap(v starlark.Value) (map[string]string, error) {
	if v == nil || v == starlark.None {
		return nil, nil
	}
	d, ok := v.(*starlark.Dict)
	if !ok {
		return nil, fmt.Errorf("headers must be a dict, got %s", v.Type())
	}
	out := make(map[string]string, d.Len())
	for _, item := range d.Items() {
		k, _ := item[0].(starlark.String)
		s, _ := item[1].(starlark.String)
		out[string(k)] = string(s)
	}
	return out, nil
}

func ghResponse(status int, headers map[string]string, body string) starlark.Value {
	d := starlark.NewDict(3)
	_ = d.SetKey(starlark.String("status"), starlark.MakeInt(status))
	hd := starlark.NewDict(len(headers))
	for k, v := range headers {
		_ = hd.SetKey(starlark.String(k), starlark.String(v))
	}
	_ = d.SetKey(starlark.String("headers"), hd)
	_ = d.SetKey(starlark.String("body"), starlark.String(body))
	return d
}

func (m *ghMock) do(method, rawurl string, headers map[string]string, body, ct string) (starlark.Value, error) {
	u, err := url.Parse(rawurl)
	if err != nil {
		return nil, fmt.Errorf("net: bad mock url %q: %w", rawurl, err)
	}
	c := ghCall{method: method, rawurl: rawurl, path: u.Path, query: u.Query(),
		headers: headers, body: body, ct: ct}
	m.mu.Lock()
	m.calls = append(m.calls, c)
	m.mu.Unlock()
	status, rh, rb := 200, map[string]string{}, "null"
	if m.respond != nil {
		status, rh, rb = m.respond(c)
	}
	return ghResponse(status, rh, rb), nil
}

func (m *ghMock) request(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var method, rawurl, body string
	var hdrs starlark.Value
	if err := starlark.UnpackArgs("net.request", args, kwargs,
		"method", &method, "url", &rawurl, "body?", &body, "headers?", &hdrs); err != nil {
		return nil, err
	}
	headers, err := ghHeaderMap(hdrs)
	if err != nil {
		return nil, err
	}
	return m.do(method, rawurl, headers, body, "")
}

func (m *ghMock) post(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var rawurl, body, ct string
	var hdrs starlark.Value
	if err := starlark.UnpackArgs("net.post", args, kwargs,
		"url", &rawurl, "body?", &body, "headers?", &hdrs, "content_type?", &ct); err != nil {
		return nil, err
	}
	headers, err := ghHeaderMap(hdrs)
	if err != nil {
		return nil, err
	}
	return m.do("POST", rawurl, headers, body, ct)
}

func (m *ghMock) get(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var rawurl string
	var hdrs starlark.Value
	if err := starlark.UnpackArgs("net.get", args, kwargs, "url", &rawurl, "headers?", &hdrs); err != nil {
		return nil, err
	}
	headers, err := ghHeaderMap(hdrs)
	if err != nil {
		return nil, err
	}
	return m.do("GET", rawurl, headers, "", "")
}

// ghFixture is one loaded github@0.2.0 alias bound to the mock net.
type ghFixture struct {
	mock *ghMock
	gate *capability.Gate
	root starlark.Value // the ext struct
}

func newGitHub(t *testing.T, config map[string]any, respond func(ghCall) (int, map[string]string, string)) *ghFixture {
	t.Helper()
	mock := &ghMock{respond: respond}
	f := Factory(Options{LocalRoots: []string{ghContribRoot}})
	cfg := map[string]any{}
	for k, v := range config {
		cfg[k] = v
	}
	spec, _ := json.Marshal(map[string]any{
		"github": map[string]any{"source": "contrib/github@0.2.0", "config": cfg},
	})
	v, err := f.Validate(spec, capability.ServerLimits{})
	if err != nil {
		t.Fatalf("ext validate: %v", err)
	}
	secs := secrets.NewSet()
	secs.Add("GITHUB_TOKEN", []byte("ghp_unittest"), nil)
	gate := capability.NewGate(capability.GateOwner{InstanceID: "ins"}, nil, nil)
	val, closer, err := f.New(v, capability.InstanceEnv{
		Context:  context.Background(),
		Gate:     gate,
		Secrets:  secs,
		Bindings: map[types.CapabilityName]starlark.Value{types.CapNet: mock.module()},
	})
	if err != nil {
		t.Fatalf("ext new: %v", err)
	}
	if closer != nil {
		t.Cleanup(func() { _ = closer.Close() })
	}
	return &ghFixture{mock: mock, gate: gate, root: val}
}

// call invokes ext.github.<op> under an armed gate.
func (fx *ghFixture) call(t *testing.T, op string, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	t.Helper()
	gh, err := fx.root.(starlark.HasAttrs).Attr("github")
	if err != nil {
		t.Fatalf("ext.github: %v", err)
	}
	fn, err := gh.(starlark.HasAttrs).Attr(op)
	if err != nil {
		t.Fatalf("ext.github.%s: %v", op, err)
	}
	fx.gate.Arm(capability.ExecContext{ExecID: "e", InstanceID: "ins"})
	defer fx.gate.Disarm()
	return starlark.Call(&starlark.Thread{Name: "exec"}, fn, args, kwargs)
}

func ghS(s string) starlark.Value { return starlark.String(s) }
func ghI(n int) starlark.Value    { return starlark.MakeInt(n) }
func ghB(b bool) starlark.Value   { return starlark.Bool(b) }
func ghL(items ...starlark.Value) starlark.Value {
	return starlark.NewList(items)
}

func ghKw(pairs ...any) []starlark.Tuple {
	var out []starlark.Tuple
	for i := 0; i+1 < len(pairs); i += 2 {
		name, _ := pairs[i].(string)
		v, ok := pairs[i+1].(starlark.Value)
		if !ok {
			panic("ghKw: value must be a starlark.Value")
		}
		out = append(out, starlark.Tuple{starlark.String(name), v})
	}
	return out
}

func ghJSON(t *testing.T, v starlark.Value) any {
	t.Helper()
	j, err := capability.JSONValue(v)
	if err != nil {
		t.Fatalf("json value: %v", err)
	}
	return j
}

func ghResp(body string) func(ghCall) (int, map[string]string, string) {
	return func(ghCall) (int, map[string]string, string) { return 200, nil, body }
}

// ghRouter answers every op route with a minimal valid body.
func ghRouter() func(ghCall) (int, map[string]string, string) {
	return func(c ghCall) (int, map[string]string, string) {
		switch {
		case c.path == "/graphql":
			return 200, nil, `{"data":{"user":{"contributionsCollection":{}},"rateLimit":{}}}`
		case strings.HasPrefix(c.path, "/search/"):
			return 200, nil, `{"total_count":0,"incomplete_results":false,"items":[]}`
		case strings.HasPrefix(c.path, "/repos/o/r/contents/"):
			return 200, nil, "file-body"
		case c.path == "/repos/o/r":
			return 200, nil, `{"full_name":"o/r","description":"d","default_branch":"main","stargazers_count":1,"private":false}`
		case c.method == "GET" && c.path == "/repos/o/r/issues":
			return 200, nil, `[{"number":1,"title":"t","state":"open","user":{"login":"u"},"labels":[]}]`
		case c.method == "POST" && c.path == "/repos/o/r/issues":
			return 200, nil, `{"number":5,"html_url":"http://x/5"}`
		case c.method == "POST" && strings.HasSuffix(c.path, "/comments"):
			return 200, nil, `{"id":9,"html_url":"http://x/c"}`
		case c.method == "GET" && c.path == "/repos/o/r/pulls":
			return 200, nil, `[{"number":2,"title":"p","state":"open","user":{"login":"u"},"head":{"ref":"b"}}]`
		default:
			return 200, nil, `[]`
		}
	}
}

func TestGitHubManifestAndOps(t *testing.T) {
	l := &CapabilityLoader{
		Identifier: CapabilityIdentifier("contrib/github@0.2.0"),
		Options:    &Options{LocalRoots: []string{ghContribRoot}},
	}
	mod, err := l.Load(context.Background())
	if err != nil {
		t.Fatalf("load contrib/github@0.2.0: %v", err)
	}
	if mod.Manifest.Version != "0.2.0" {
		t.Fatalf("version %q", mod.Manifest.Version)
	}
	if len(mod.Manifest.Dependencies) != 1 || mod.Manifest.Dependencies[0] != types.CapNet {
		t.Fatalf("dependencies %v", mod.Manifest.Dependencies)
	}
	want := []string{
		"get_repo", "list_issues", "create_issue", "create_comment",
		"list_pull_requests", "get_file",
		"get_user", "get_rate_limit", "list_orgs", "list_events",
		"list_repos", "get_languages", "list_commits", "get_commit",
		"list_contributors", "get_tree", "get_readme",
		"get_pull_request", "list_reviews", "list_issue_comments",
		"search_repositories", "search_issues", "search_commits",
		"get_contributions",
	}
	if len(mod.Manifest.Ops) != len(want) {
		t.Fatalf("manifest ops %d want %d", len(mod.Manifest.Ops), len(want))
	}
	got := map[string]bool{}
	for _, op := range mod.Manifest.Ops {
		got[op.Name] = true
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("manifest missing op %q", name)
		}
	}
	// Every manifest op must init to a callable global.
	cfg := map[string]any{
		"base_url":       "https://api.github.com",
		"token":          "{{secrets.GITHUB_TOKEN}}",
		"max_file_bytes": int64(262144),
	}
	g, err := mod.init(context.Background(),
		map[types.CapabilityName]starlark.Value{types.CapNet: (&ghMock{}).module()},
		cfg, 0, nil)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	for _, op := range mod.Manifest.Ops {
		if _, ok := g[op.Name].(starlark.Callable); !ok {
			t.Errorf("op %q not callable in globals", op.Name)
		}
	}
	// Param names stay names-only and in signature order.
	params := map[string][]string{}
	for _, op := range mod.Manifest.Ops {
		params[op.Name] = op.Params
	}
	for op, wantParams := range map[string][]string{
		"get_file":            {"owner", "repo", "path", "ref"},
		"list_repos":          {"username", "page", "per_page", "sort", "direction", "affiliation"},
		"get_contributions":   {"username", "from_time", "to_time"},
		"search_repositories": {"query", "page", "per_page", "sort", "order"},
		"list_issue_comments": {"owner", "repo", "number", "page", "per_page"},
	} {
		if !slices.Equal(params[op], wantParams) {
			t.Errorf("%s params %v want %v", op, params[op], wantParams)
		}
	}
	// Config fields: base_url/token/max_file_bytes with the 0.1.0 defaults.
	cfgNames := map[string]string{}
	for _, cf := range mod.Manifest.Config {
		cfgNames[cf.Name] = cf.Type
	}
	for _, name := range []string{"base_url", "token", "max_file_bytes"} {
		if cfgNames[name] == "" {
			t.Errorf("config missing %q", name)
		}
	}
	if cfgNames["token"] != "secret" {
		t.Errorf("token type %q want secret", cfgNames["token"])
	}
}

func TestGitHubPaginationLinkHeaders(t *testing.T) {
	page := 0
	fx := newGitHub(t, nil, func(c ghCall) (int, map[string]string, string) {
		page++
		if page == 1 {
			return 200, map[string]string{
				"LiNk":                  `<https://api.github.com/user/repos?page=2&per_page=100>; rel="next", <https://api.github.com/user/repos?page=5&per_page=100>; rel="last"`,
				"X-RateLimit-Remaining": "4999",
				"X-Ratelimit-Reset":     "1700000000",
				"X-RateLimit-Resource":  "core",
			}, `[{"full_name":"o/r1","fork":true,"archived":false,"language":"Go","topics":["a"],"pushed_at":"2024-01-01T00:00:00Z"}]`
		}
		return 200, nil, `[{"full_name":"o/r2"}]`
	})
	v, err := fx.call(t, "list_repos", nil, ghKw("page", ghI(1), "per_page", ghI(100)))
	if err != nil {
		t.Fatal(err)
	}
	j := ghJSON(t, v).(map[string]any)
	items := j["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items %v", items)
	}
	r1 := items[0].(map[string]any)
	for k, want := range map[string]any{"fork": true, "language": "Go", "full_name": "o/r1"} {
		if r1[k] != want {
			t.Errorf("raw record %s=%v want %v", k, r1[k], want)
		}
	}
	if j["page"] != int64(1) || j["per_page"] != int64(100) ||
		j["has_more"] != true || j["next_page"] != int64(2) {
		t.Fatalf("page meta %v", j)
	}
	rl := j["rate_limit"].(map[string]any)
	if rl["remaining"] != "4999" || rl["reset"] != "1700000000" || rl["resource"] != "core" {
		t.Fatalf("rate_limit %v", rl)
	}
	c1 := fx.mock.callAt(0)
	if c1.path != "/user/repos" {
		t.Fatalf("path %q", c1.path)
	}
	for k, want := range map[string]string{
		"affiliation": "owner,collaborator,organization_member",
		"sort":        "updated", "direction": "desc",
		"page": "1", "per_page": "100",
	} {
		if got := c1.query.Get(k); got != want {
			t.Errorf("query %s=%q want %q", k, got, want)
		}
	}
	// Follow next_page explicitly: the Link header URL is never used.
	v2, err := fx.call(t, "list_repos", nil, ghKw("page", ghI(2), "per_page", ghI(100)))
	if err != nil {
		t.Fatal(err)
	}
	j2 := ghJSON(t, v2).(map[string]any)
	if j2["has_more"] != false || j2["next_page"] != nil {
		t.Fatalf("page2 meta %v", j2)
	}
	if fx.mock.nCalls() != 2 {
		t.Fatalf("calls %d", fx.mock.nCalls())
	}
	c2 := fx.mock.callAt(1)
	if c2.path != "/user/repos" || c2.query.Get("page") != "2" {
		t.Fatalf("page2 request %s?%s", c2.path, c2.rawurl)
	}
}

func TestGitHubSearchWindow(t *testing.T) {
	fx := newGitHub(t, nil, func(c ghCall) (int, map[string]string, string) {
		if strings.HasPrefix(c.path, "/search/") {
			return 200, map[string]string{
				"Link":                  `<https://api.github.com/search/repositories?page=11&per_page=100&q=x>; rel="next"`,
				"X-RateLimit-Remaining": "10",
			}, `{"total_count":5000,"incomplete_results":true,"items":[{"full_name":"o/r","fork":false}]}`
		}
		return 200, nil, `[]`
	})
	// Boundary page*per_page == 1000 is allowed; has_more is suppressed.
	v, err := fx.call(t, "search_repositories", []starlark.Value{ghS("x")},
		ghKw("page", ghI(10), "per_page", ghI(100)))
	if err != nil {
		t.Fatal(err)
	}
	j := ghJSON(t, v).(map[string]any)
	if j["has_more"] != false || j["next_page"] != nil {
		t.Fatalf("window page meta %v", j)
	}
	if j["window_limit"] != int64(1000) || j["total_count"] != int64(5000) ||
		j["incomplete_results"] != true {
		t.Fatalf("search meta %v", j)
	}
	if len(j["items"].([]any)) != 1 {
		t.Fatalf("items %v", j["items"])
	}
	// Beyond the window: refused before any net call.
	before := fx.mock.nCalls()
	if _, err = fx.call(t, "search_repositories", []starlark.Value{ghS("x")},
		ghKw("page", ghI(11), "per_page", ghI(100))); err == nil ||
		!strings.Contains(err.Error(), "1000") {
		t.Fatalf("over-window err %v", err)
	}
	if _, err = fx.call(t, "search_issues", []starlark.Value{ghS("x")},
		ghKw("page", ghI(101), "per_page", ghI(10))); err == nil {
		t.Fatal("search_issues over-window accepted")
	}
	if fx.mock.nCalls() != before {
		t.Fatal("net called for refused search pages")
	}
	// Non-search pagination is not window-limited.
	if _, err = fx.call(t, "list_repos", nil, ghKw("page", ghI(50))); err != nil {
		t.Fatalf("list_repos page 50: %v", err)
	}
}

func TestGitHubTokenAuth(t *testing.T) {
	// Default config: the {{secrets.GITHUB_TOKEN}} placeholder is
	// forwarded verbatim for net to expand - never plaintext here.
	fx := newGitHub(t, nil, ghResp(`{"login":"octocat"}`))
	if _, err := fx.call(t, "get_user", nil, nil); err != nil {
		t.Fatal(err)
	}
	c := fx.mock.last()
	if got := c.headers["Authorization"]; got != "Bearer {{secrets.GITHUB_TOKEN}}" {
		t.Fatalf("Authorization %q", got)
	}
	if c.headers["Accept"] != "application/vnd.github+json" ||
		c.headers["X-GitHub-Api-Version"] != "2022-11-28" {
		t.Fatalf("headers %v", c.headers)
	}
	// Empty token: unauthenticated request, no Authorization header.
	fx2 := newGitHub(t, map[string]any{"token": ""}, ghResp(`{"login":"octocat"}`))
	if _, err := fx2.call(t, "get_user", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := fx2.mock.last().headers["Authorization"]; ok {
		t.Fatalf("unexpected Authorization: %v", fx2.mock.last().headers)
	}
}

func TestGitHubSegmentEscaping(t *testing.T) {
	cases := []struct {
		name string
		op   string
		args []starlark.Value
		kw   []starlark.Tuple
	}{
		{"user slash", "get_user", []starlark.Value{ghS("a/b")}, nil},
		{"user dotdot", "get_user", []starlark.Value{ghS("..")}, nil},
		{"user dot", "get_user", []starlark.Value{ghS(".")}, nil},
		{"user backslash", "get_user", []starlark.Value{ghS("a\\b")}, nil},
		{"commit ref slash", "get_commit", []starlark.Value{ghS("o"), ghS("r"), ghS("a/b")}, nil},
		{"tree ref slash", "get_tree", []starlark.Value{ghS("o"), ghS("r"), ghS("feature/x")}, nil},
		{"owner traversal", "get_repo", []starlark.Value{ghS(".."), ghS("r")}, nil},
		{"repo slash", "get_repo", []starlark.Value{ghS("o"), ghS("a/b")}, nil},
		{"file dotdot", "get_file", []starlark.Value{ghS("o"), ghS("r"), ghS("../x")}, nil},
		{"file dot seg", "get_file", []starlark.Value{ghS("o"), ghS("r"), ghS("a/./b")}, nil},
		{"file empty seg", "get_file", []starlark.Value{ghS("o"), ghS("r"), ghS("a//b")}, nil},
		{"file trailing slash", "get_file", []starlark.Value{ghS("o"), ghS("r"), ghS("a/")}, nil},
		{"file leading slash", "get_file", []starlark.Value{ghS("o"), ghS("r"), ghS("/a")}, nil},
		{"file backslash", "get_file", []starlark.Value{ghS("o"), ghS("r"), ghS("a\\b")}, nil},
		{"file empty", "get_file", []starlark.Value{ghS("o"), ghS("r"), ghS("")}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newGitHub(t, nil, ghResp(`{}`))
			_, err := fx.call(t, tc.op, starlark.Tuple(tc.args), tc.kw)
			if err == nil {
				t.Fatal("expected validation error")
			}
			if fx.mock.nCalls() != 0 {
				t.Fatalf("net called %d times", fx.mock.nCalls())
			}
		})
	}
}

func TestGitHubSegmentEscapingGood(t *testing.T) {
	fx := newGitHub(t, nil, ghResp(`{}`))
	// Spaces and query characters are percent-escaped per segment.
	if _, err := fx.call(t, "get_user", []starlark.Value{ghS("a b")}, nil); err != nil {
		t.Fatal(err)
	}
	c := fx.mock.last()
	if !strings.Contains(c.rawurl, "/users/a%20b") {
		t.Fatalf("escaped url %q", c.rawurl)
	}
	// "?" and "#" inside a file segment cannot inject query/fragment.
	if _, err := fx.call(t, "get_file", []starlark.Value{ghS("o"), ghS("r"), ghS("a?b#c")}, nil); err != nil {
		t.Fatal(err)
	}
	c = fx.mock.last()
	if !strings.Contains(c.rawurl, "a%3Fb%23c") || strings.Contains(c.rawurl, "?") {
		t.Fatalf("injection-safe url %q", c.rawurl)
	}
	// ref with "/" (branch names) travels as a query parameter.
	if _, err := fx.call(t, "get_file", []starlark.Value{ghS("o"), ghS("r"), ghS("f.txt")},
		ghKw("ref", ghS("feature/x"))); err != nil {
		t.Fatal(err)
	}
	c = fx.mock.last()
	if c.path != "/repos/o/r/contents/f.txt" || c.query.Get("ref") != "feature/x" {
		t.Fatalf("ref handling %s?%s", c.path, c.rawurl)
	}
}

func TestGitHubListIssuesFiltersPR(t *testing.T) {
	fx := newGitHub(t, nil, func(c ghCall) (int, map[string]string, string) {
		if c.path == "/repos/o/r/issues" {
			return 200, nil, `[
				{"number":1,"title":"issue one","state":"open","user":{"login":"u1"},"labels":[{"name":"bug"}]},
				{"number":2,"title":"actually a PR","state":"open","user":{"login":"u2"},"labels":[],"pull_request":{"url":"x"}},
				{"number":3,"title":"issue three","state":"closed","user":{"login":"u3"},"labels":[]}
			]`
		}
		return 200, nil, `[]`
	})
	v, err := fx.call(t, "list_issues",
		starlark.Tuple{ghS("o"), ghS("r")},
		ghKw("state", ghS("all"), "limit", ghI(100)))
	if err != nil {
		t.Fatal(err)
	}
	j := ghJSON(t, v).([]any)
	if len(j) != 2 {
		t.Fatalf("PR not filtered: %v", j)
	}
	first := j[0].(map[string]any)
	if first["number"] != int64(1) || first["user"] != "u1" || first["state"] != "open" {
		t.Fatalf("summary shape %v", first)
	}
	if labels := first["labels"].([]any); len(labels) != 1 || labels[0] != "bug" {
		t.Fatalf("labels %v", first["labels"])
	}
	c := fx.mock.last()
	if c.query.Get("state") != "all" || c.query.Get("per_page") != "100" {
		t.Fatalf("query %s", c.rawurl)
	}
	// limit must be an int in 1..100; invalid values make no net call.
	for _, bad := range []starlark.Value{ghI(0), ghI(101), ghI(-5), starlark.Float(1.5), ghB(true), ghS("20")} {
		before := fx.mock.nCalls()
		if _, err = fx.call(t, "list_issues",
			starlark.Tuple{ghS("o"), ghS("r")}, ghKw("limit", bad)); err == nil {
			t.Fatalf("limit %v accepted", bad)
		}
		if fx.mock.nCalls() != before {
			t.Fatal("net called for invalid limit")
		}
	}
	if _, err = fx.call(t, "list_pull_requests",
		starlark.Tuple{ghS("o"), ghS("r")}, ghKw("limit", ghI(0))); err == nil {
		t.Fatal("list_pull_requests limit 0 accepted")
	}
}

func TestGitHubErrorDiagnostics(t *testing.T) {
	for _, status := range []int{401, 403, 429} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			fx := newGitHub(t, nil, func(c ghCall) (int, map[string]string, string) {
				return status, map[string]string{
					"X-RateLimit-Remaining": "0",
					"X-Ratelimit-Reset":     "1700000000",
					"Retry-After":           "60",
				}, `{"message":"denied"}`
			})
			_, err := fx.call(t, "get_user", nil, nil)
			if err == nil {
				t.Fatal("expected error")
			}
			msg := err.Error()
			for _, want := range []string{
				fmt.Sprintf("github api: status %d", status),
				"remaining=0", "reset=1700000000", "retry_after=60",
			} {
				if !strings.Contains(msg, want) {
					t.Errorf("missing %q in %q", want, msg)
				}
			}
		})
	}
	// Body-carrying calls report POST in the diagnostic.
	fx := newGitHub(t, nil, func(c ghCall) (int, map[string]string, string) {
		return 403, nil, `{}`
	})
	_, err := fx.call(t, "create_issue",
		starlark.Tuple{ghS("o"), ghS("r"), ghS("t")}, nil)
	if err == nil || !strings.Contains(err.Error(), "POST") {
		t.Fatalf("post diagnostic: %v", err)
	}
	c := fx.mock.last()
	if c.method != "POST" {
		t.Fatalf("method %q", c.method)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(c.body), &body); err != nil {
		t.Fatal(err)
	}
	if body["title"] != "t" || c.ct != "application/json" {
		t.Fatalf("post body %s ct %q", c.body, c.ct)
	}
}

func TestGitHubContributions(t *testing.T) {
	var gotBody map[string]any
	fx := newGitHub(t, nil, func(c ghCall) (int, map[string]string, string) {
		if c.path != "/graphql" {
			t.Errorf("unexpected path %s", c.path)
		}
		if err := json.Unmarshal([]byte(c.body), &gotBody); err != nil {
			t.Errorf("graphql body: %v", err)
		}
		return 200, nil, `{"data":{"user":{"contributionsCollection":{"startedAt":"2025-01-01T00:00:00Z","endedAt":"2025-12-31T00:00:00Z","hasAnyRestrictedContributions":false,"restrictedContributionsCount":0,"totalCommitContributions":5,"totalIssueContributions":2,"totalPullRequestContributions":1,"totalPullRequestReviewContributions":3,"totalRepositoryContributions":4,"contributionCalendar":{"totalContributions":11,"weeks":[]},"commitContributionsByRepository":[],"pullRequestContributionsByRepository":[],"pullRequestReviewContributionsByRepository":[]}},"rateLimit":{"limit":5000,"remaining":4999,"resetAt":"2025-01-01T00:00:00Z","cost":1}}}`
	})
	v, err := fx.call(t, "get_contributions", nil, ghKw(
		"username", ghS("octocat"),
		"from_time", ghS("2025-01-01T00:00:00Z"),
		"to_time", ghS("2025-12-31T00:00:00Z"),
	))
	if err != nil {
		t.Fatal(err)
	}
	c := fx.mock.last()
	if c.method != "POST" || c.path != "/graphql" {
		t.Fatalf("%s %s", c.method, c.path)
	}
	q, _ := gotBody["query"].(string)
	for _, want := range []string{"query AccountContributions", "contributionsCollection",
		"maxRepositories:100", "rateLimit", "totalCommitContributions"} {
		if !strings.Contains(q, want) {
			t.Errorf("query missing %q", want)
		}
	}
	if strings.Contains(q, "octocat") {
		t.Fatal("username composed into query text")
	}
	vars, _ := gotBody["variables"].(map[string]any)
	if vars["login"] != "octocat" || vars["from"] != "2025-01-01T00:00:00Z" ||
		vars["to"] != "2025-12-31T00:00:00Z" {
		t.Fatalf("variables %v", vars)
	}
	j := ghJSON(t, v).(map[string]any)
	coll := j["contributions"].(map[string]any)
	if coll["totalCommitContributions"] != int64(5) ||
		coll["totalPullRequestReviewContributions"] != int64(3) {
		t.Fatalf("contributions %v", coll)
	}
	rl := j["rate_limit"].(map[string]any)
	if rl["remaining"] != int64(4999) || rl["cost"] != int64(1) {
		t.Fatalf("rate_limit %v", rl)
	}
}

func TestGitHubContributionsErrors(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"graphql errors", `{"errors":[{"message":"boom"}]}`, "github graphql"},
		{"null user", `{"data":{"user":null,"rateLimit":{}}}`, "user not found"},
		{"null data", `{"data":null}`, "user not found"},
		{"missing data", `{}`, "user not found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newGitHub(t, nil, ghResp(tc.body))
			_, err := fx.call(t, "get_contributions", nil, ghKw(
				"username", ghS("u"), "from_time", ghS("a"), "to_time", ghS("b")))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err %v want %q", err, tc.want)
			}
		})
	}
	fx := newGitHub(t, nil, ghResp(`{}`))
	_, err := fx.call(t, "get_contributions", nil, ghKw(
		"username", ghS(""), "from_time", ghS("a"), "to_time", ghS("b")))
	if err == nil {
		t.Fatal("empty username accepted")
	}
	if fx.mock.nCalls() != 0 {
		t.Fatal("net called for empty username")
	}
}

func TestGitHubTreeTruncated(t *testing.T) {
	fx := newGitHub(t, nil, func(c ghCall) (int, map[string]string, string) {
		return 200, nil, `{"sha":"abc","tree":[{"path":"a.txt","type":"blob","sha":"s1"},{"path":"dir","type":"tree","sha":"s2"}],"truncated":true}`
	})
	v, err := fx.call(t, "get_tree", []starlark.Value{ghS("o"), ghS("r")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	j := ghJSON(t, v).(map[string]any)
	if j["truncated"] != true {
		t.Fatal("truncated flag not preserved")
	}
	if len(j["tree"].([]any)) != 2 {
		t.Fatalf("tree entries %v", j["tree"])
	}
	c := fx.mock.last()
	if c.path != "/repos/o/r/git/trees/HEAD" || c.query.Get("recursive") != "1" {
		t.Fatalf("tree request %s?%s", c.path, c.rawurl)
	}
	// recursive=false omits the param; a custom ref is a path segment.
	if _, err = fx.call(t, "get_tree", []starlark.Value{ghS("o"), ghS("r")},
		ghKw("recursive", ghB(false), "ref", ghS("v1.0"))); err != nil {
		t.Fatal(err)
	}
	c = fx.mock.last()
	if c.path != "/repos/o/r/git/trees/v1.0" || c.query.Get("recursive") != "" {
		t.Fatalf("tree request2 %s?%s", c.path, c.rawurl)
	}
}

func TestGitHubRoutesAndParams(t *testing.T) {
	fx := newGitHub(t, nil, ghRouter())
	cases := []struct {
		name   string
		op     string
		args   []starlark.Value
		kw     []starlark.Tuple
		method string
		path   string
		query  map[string]string
	}{
		{"user self", "get_user", nil, nil, "GET", "/user", nil},
		{"user named", "get_user", []starlark.Value{ghS("octocat")}, nil, "GET", "/users/octocat", nil},
		{"rate limit", "get_rate_limit", nil, nil, "GET", "/rate_limit", nil},
		{"orgs self", "list_orgs", nil, ghKw("page", ghI(2), "per_page", ghI(50)), "GET", "/user/orgs",
			map[string]string{"page": "2", "per_page": "50"}},
		{"orgs user", "list_orgs", []starlark.Value{ghS("octo")}, nil, "GET", "/users/octo/orgs",
			map[string]string{"page": "1", "per_page": "100"}},
		{"events public", "list_events", []starlark.Value{ghS("octocat")}, nil, "GET", "/users/octocat/events/public",
			map[string]string{"page": "1", "per_page": "100"}},
		{"events all", "list_events", []starlark.Value{ghS("octocat")}, ghKw("public_only", ghB(false)), "GET", "/users/octocat/events", nil},
		{"repos self", "list_repos", nil, nil, "GET", "/user/repos",
			map[string]string{"affiliation": "owner,collaborator,organization_member", "sort": "updated", "direction": "desc", "page": "1", "per_page": "100"}},
		{"repos user", "list_repos", []starlark.Value{ghS("octo")}, nil, "GET", "/users/octo/repos",
			map[string]string{"type": "owner", "sort": "updated", "direction": "desc"}},
		{"languages", "get_languages", []starlark.Value{ghS("o"), ghS("r")}, nil, "GET", "/repos/o/r/languages", nil},
		{"commits", "list_commits", []starlark.Value{ghS("o"), ghS("r")},
			ghKw("author", ghS("a"), "since", ghS("2024-01-01"), "until", ghS("2024-12-31"), "sha", ghS("main"), "page", ghI(3), "per_page", ghI(10)),
			"GET", "/repos/o/r/commits",
			map[string]string{"author": "a", "since": "2024-01-01", "until": "2024-12-31", "sha": "main", "page": "3", "per_page": "10"}},
		{"commit", "get_commit", []starlark.Value{ghS("o"), ghS("r"), ghS("abc123")}, nil, "GET", "/repos/o/r/commits/abc123", nil},
		{"contributors anon", "list_contributors", []starlark.Value{ghS("o"), ghS("r")}, nil, "GET", "/repos/o/r/contributors",
			map[string]string{"anon": "true", "page": "1", "per_page": "100"}},
		{"tree", "get_tree", []starlark.Value{ghS("o"), ghS("r")}, nil, "GET", "/repos/o/r/git/trees/HEAD",
			map[string]string{"recursive": "1"}},
		{"readme", "get_readme", []starlark.Value{ghS("o"), ghS("r")}, ghKw("ref", ghS("dev")), "GET", "/repos/o/r/readme",
			map[string]string{"ref": "dev"}},
		{"pull", "get_pull_request", []starlark.Value{ghS("o"), ghS("r"), ghI(7)}, nil, "GET", "/repos/o/r/pulls/7", nil},
		{"reviews", "list_reviews", []starlark.Value{ghS("o"), ghS("r"), ghI(7)}, ghKw("page", ghI(2)), "GET", "/repos/o/r/pulls/7/reviews",
			map[string]string{"page": "2", "per_page": "100"}},
		{"issue comments", "list_issue_comments", []starlark.Value{ghS("o"), ghS("r"), ghI(9)}, nil, "GET", "/repos/o/r/issues/9/comments",
			map[string]string{"page": "1", "per_page": "100"}},
		{"search repos", "search_repositories", []starlark.Value{ghS("q")},
			ghKw("sort", ghS("stars"), "order", ghS("asc"), "page", ghI(2), "per_page", ghI(50)),
			"GET", "/search/repositories",
			map[string]string{"q": "q", "sort": "stars", "order": "asc", "page": "2", "per_page": "50"}},
		{"search issues", "search_issues", []starlark.Value{ghS("is:issue x")}, nil, "GET", "/search/issues",
			map[string]string{"q": "is:issue x", "sort": "updated", "order": "desc"}},
		{"search commits", "search_commits", []starlark.Value{ghS("fix")}, nil, "GET", "/search/commits",
			map[string]string{"q": "fix", "sort": "committer-date"}},
		{"contributions", "get_contributions", nil, ghKw("username", ghS("u"), "from_time", ghS("a"), "to_time", ghS("b")),
			"POST", "/graphql", nil},
		{"repo", "get_repo", []starlark.Value{ghS("o"), ghS("r")}, nil, "GET", "/repos/o/r", nil},
		{"issues", "list_issues", []starlark.Value{ghS("o"), ghS("r")}, nil, "GET", "/repos/o/r/issues",
			map[string]string{"state": "open", "per_page": "20"}},
		{"create issue", "create_issue", []starlark.Value{ghS("o"), ghS("r"), ghS("t")},
			ghKw("body", ghS("b"), "labels", ghL(ghS("bug"))), "POST", "/repos/o/r/issues", nil},
		{"comment", "create_comment", []starlark.Value{ghS("o"), ghS("r"), ghI(3), ghS("hi")}, nil, "POST", "/repos/o/r/issues/3/comments", nil},
		{"pulls", "list_pull_requests", []starlark.Value{ghS("o"), ghS("r")}, nil, "GET", "/repos/o/r/pulls",
			map[string]string{"state": "open", "per_page": "20"}},
		{"file", "get_file", []starlark.Value{ghS("o"), ghS("r"), ghS("a/b.txt")}, nil, "GET", "/repos/o/r/contents/a/b.txt", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := fx.mock.nCalls()
			if _, err := fx.call(t, tc.op, starlark.Tuple(tc.args), tc.kw); err != nil {
				t.Fatalf("%s: %v", tc.op, err)
			}
			if fx.mock.nCalls() != before+1 {
				t.Fatalf("calls %d want %d", fx.mock.nCalls(), before+1)
			}
			c := fx.mock.last()
			if c.method != tc.method {
				t.Errorf("method %q want %q", c.method, tc.method)
			}
			if c.path != tc.path {
				t.Errorf("path %q want %q (raw %s)", c.path, tc.path, c.rawurl)
			}
			for k, want := range tc.query {
				if got := c.query.Get(k); got != want {
					t.Errorf("query %s=%q want %q (raw %s)", k, got, want, c.rawurl)
				}
			}
		})
	}
}

func TestGitHubInvalidParamsNoNet(t *testing.T) {
	fx := newGitHub(t, nil, ghResp(`{}`))
	cases := []struct {
		name string
		op   string
		args []starlark.Value
		kw   []starlark.Tuple
	}{
		{"page zero", "list_repos", nil, ghKw("page", ghI(0))},
		{"page negative", "list_repos", nil, ghKw("page", ghI(-2))},
		{"page float", "list_repos", nil, ghKw("page", starlark.Float(1.5))},
		{"page bool", "list_repos", nil, ghKw("page", ghB(true))},
		{"page string", "list_repos", nil, ghKw("page", ghS("2"))},
		{"per_page zero", "list_repos", nil, ghKw("per_page", ghI(0))},
		{"per_page 101", "list_repos", nil, ghKw("per_page", ghI(101))},
		{"per_page bool", "list_commits", []starlark.Value{ghS("o"), ghS("r")}, ghKw("per_page", ghB(false))},
		{"search window", "search_repositories", []starlark.Value{ghS("q")}, ghKw("page", ghI(11), "per_page", ghI(100))},
		{"search window 2", "search_issues", []starlark.Value{ghS("q")}, ghKw("page", ghI(101), "per_page", ghI(10))},
		{"pr number zero", "get_pull_request", []starlark.Value{ghS("o"), ghS("r"), ghI(0)}, nil},
		{"pr number float", "get_pull_request", []starlark.Value{ghS("o"), ghS("r"), starlark.Float(1.5)}, nil},
		{"reviews number bad", "list_reviews", []starlark.Value{ghS("o"), ghS("r"), ghI(-1)}, nil},
		{"comments number str", "list_issue_comments", []starlark.Value{ghS("o"), ghS("r"), ghS("9")}, nil},
		{"legacy limit 0", "list_issues", []starlark.Value{ghS("o"), ghS("r")}, ghKw("limit", ghI(0))},
		{"legacy limit 101", "list_pull_requests", []starlark.Value{ghS("o"), ghS("r")}, ghKw("limit", ghI(101))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := fx.mock.nCalls()
			if _, err := fx.call(t, tc.op, starlark.Tuple(tc.args), tc.kw); err == nil {
				t.Fatalf("%s accepted invalid params", tc.op)
			}
			if fx.mock.nCalls() != before {
				t.Fatal("net called despite invalid params")
			}
		})
	}
}

func TestGitHubGetFileBounded(t *testing.T) {
	fx := newGitHub(t, map[string]any{"max_file_bytes": int64(5)}, func(c ghCall) (int, map[string]string, string) {
		return 200, nil, "hello"
	})
	v, err := fx.call(t, "get_file",
		[]starlark.Value{ghS("o"), ghS("r"), ghS("a/b.txt")},
		ghKw("ref", ghS("feature/x")))
	if err != nil {
		t.Fatal(err)
	}
	j := ghJSON(t, v).(map[string]any)
	if j["path"] != "a/b.txt" || j["size"] != int64(5) || j["content"] != "hello" {
		t.Fatalf("file result %v", j)
	}
	c := fx.mock.last()
	if c.path != "/repos/o/r/contents/a/b.txt" || c.query.Get("ref") != "feature/x" {
		t.Fatalf("file request %s?%s", c.path, c.rawurl)
	}
	if c.headers["Accept"] != "application/vnd.github.raw+json" {
		t.Fatalf("Accept %q", c.headers["Accept"])
	}
	fx2 := newGitHub(t, map[string]any{"max_file_bytes": int64(4)}, ghResp("hello"))
	if _, err = fx2.call(t, "get_file",
		[]starlark.Value{ghS("o"), ghS("r"), ghS("f.txt")}, nil); err == nil ||
		!strings.Contains(err.Error(), "file too large") {
		t.Fatalf("oversize file: %v", err)
	}
}

// TestGitHubExtGatedNet is the real end-to-end regression: the bundled
// github@0.2.0 extension, the real net capability, a local httptest
// server, and the instance secret set. It verifies HTTP allowlisting,
// secret placeholder injection (never plaintext in the script),
// response redaction, and audit hygiene.
func TestGitHubExtGatedNet(t *testing.T) {
	const secretVal = "ghp_sidekick_integration_secret"
	var gotAuth atomic.Value // string
	mkServer := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotAuth.Store(r.Header.Get("Authorization"))
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"login":"octocat","auth":%q}`, r.Header.Get("Authorization"))
		}))
	}
	srv := mkServer()
	defer srv.Close()
	blocked := mkServer()
	defer blocked.Close()
	noluck := mkServer()
	defer noluck.Close()

	srvHost := strings.TrimPrefix(srv.URL, "http://")
	blockedHost := strings.TrimPrefix(blocked.URL, "http://")

	limits := capability.ServerLimits{SecretsAllowHTTP: true}
	reg := capability.NewRegistry()
	nf := capnet.Factory()
	reg.Register(nf)
	ef := Factory(Options{LocalRoots: []string{ghContribRoot}})
	reg.Register(ef)

	netJSON, _ := json.Marshal(map[string]any{
		"allow_hosts":        []string{srvHost, blockedHost},
		"methods":            []string{"GET", "POST"},
		"max_response_bytes": 1 << 20,
	})
	netCfg, err := nf.Validate(netJSON, limits)
	if err != nil {
		t.Fatal(err)
	}
	spec, _ := json.Marshal(map[string]any{
		"github": map[string]any{"source": "contrib/github@0.2.0", "config": map[string]any{
			"base_url": srv.URL, "token": "{{secrets.GITHUB_TOKEN}}"}},
		"ghblocked": map[string]any{"source": "contrib/github@0.2.0", "config": map[string]any{
			"base_url": blocked.URL, "token": "{{secrets.GITHUB_TOKEN}}"}},
		"ghnoluck": map[string]any{"source": "contrib/github@0.2.0", "config": map[string]any{
			"base_url": noluck.URL, "token": ""}},
	})
	extCfg, err := ef.Validate(spec, limits)
	if err != nil {
		t.Fatal(err)
	}
	secs := secrets.NewSet()
	rules, err := hostmatch.ParseAll([]string{srvHost})
	if err != nil {
		t.Fatal(err)
	}
	secs.Add("GITHUB_TOKEN", []byte(secretVal), rules)
	o := &recorder{}
	s, err := engine.NewSession(engine.SessionDeps{
		Context: context.Background(), Registry: reg, Limits: limits,
		Observer: o, Secrets: secs,
	}, engine.SessionCreation{
		InstanceID:   "ins",
		Capabilities: map[string]any{"net": netCfg, "ext": extCfg},
		MaxSteps:     1 << 20, MaxOutputBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	e := engine.New(4)
	t.Cleanup(e.Close)

	run := func(execID, code string) engine.Result {
		t.Helper()
		s.Out.Reset()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return e.Exec(ctx, s, execID, code, 8*time.Second, 1<<20, s.Out.String)
	}

	// Allowed host + secret domain: real injection, redacted echo.
	res := run("ok", "r = ext.github.get_user()\nprint(json.encode(r))")
	if res.Error != nil {
		t.Fatalf("exec: %+v", res.Error)
	}
	if got := gotAuth.Load(); got != "Bearer "+secretVal {
		t.Fatalf("server saw Authorization %v", got)
	}
	out := res.Output
	if !strings.Contains(out, "octocat") || !strings.Contains(out, "REDACTED:GITHUB_TOKEN") {
		t.Fatalf("output missing login/redaction: %q", out)
	}
	if strings.Contains(out, secretVal) {
		t.Fatal("secret value leaked into exec output")
	}

	// Host allowed but outside the secret's domain rules: denied.
	res = run("blocked", "ext.ghblocked.get_user()")
	if res.Error == nil || res.Error.Type != types.ErrPolicyDenied {
		t.Fatalf("ghblocked: %+v", res.Error)
	}
	if !strings.Contains(res.Error.Message, "not allowed for host") {
		t.Fatalf("ghblocked reason: %q", res.Error.Message)
	}

	// Host outside allow_hosts entirely: denied before secrets matter.
	res = run("noluck", "ext.ghnoluck.get_user()")
	if res.Error == nil || res.Error.Type != types.ErrPolicyDenied {
		t.Fatalf("ghnoluck: %+v", res.Error)
	}
	if !strings.Contains(res.Error.Message, "not in allow_hosts") {
		t.Fatalf("ghnoluck reason: %q", res.Error.Message)
	}

	// Audit sees the secret NAME, never the value.
	blob, _ := json.Marshal(o.recs)
	if strings.Contains(string(blob), secretVal) {
		t.Fatal("secret value leaked into audit records")
	}
	if !strings.Contains(string(blob), "GITHUB_TOKEN") {
		t.Fatal("audit missing secret name")
	}
}
