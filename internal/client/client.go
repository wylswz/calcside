// Package client is a typed HTTP client for the calcside API, used by
// csctl and tests.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"calcside/internal/engine"
	"calcside/internal/store"
	"calcside/internal/types"
)

// Client talks to a calcside server with API-key auth.
type Client struct {
	base   string
	key    string
	hc     *http.Client
	cookie string // session cookie alternative to API key
}

// New builds a client. server like "http://localhost:8080".
func New(server, apiKey string) *Client {
	return &Client{base: strings.TrimRight(server, "/"), key: apiKey, hc: &http.Client{Timeout: 60 * time.Second}}
}

// WithCookie uses a session cookie instead of an API key.
func (c *Client) WithCookie(cookie string) *Client {
	c.cookie = cookie
	return c
}

// Error is a non-2xx API response.
type Error struct {
	Status  int
	Code    types.APIErrorCode // "" if the server sent an unrecognized code
	Message string
}

func (e *Error) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("HTTP %d: %s", e.Status, e.Message)
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	if c.cookie != "" {
		req.Header.Set("Cookie", "cs_session="+c.cookie)
		req.Header.Set("X-Requested-With", "calcside")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		e := &Error{Status: resp.StatusCode}
		var m map[string]any
		if json.Unmarshal(data, &m) == nil {
			if eobj, ok := m["error"].(map[string]any); ok {
				if cs, ok2 := eobj["code"].(string); ok2 {
					c := types.APIErrorCode(cs)
					if c.Valid() {
						e.Code = c
					}
				}
				e.Message, _ = eobj["message"].(string)
			}
		}
		if e.Message == "" {
			e.Message = string(data)
		}
		return e
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

// Me returns the authenticated user.
func (c *Client) Me(ctx context.Context) (*store.User, error) {
	var m struct {
		User   *store.User `json:"user"`
		ViaKey bool        `json:"via_key"`
	}
	if err := c.do(ctx, "GET", "/api/v1/me", nil, &m); err != nil {
		return nil, err
	}
	return m.User, nil
}

// AuthConfig reports enabled login methods (unauthenticated).
func (c *Client) AuthConfig(ctx context.Context) (google, devLogin bool, err error) {
	var m map[string]bool
	if err := c.do(ctx, "GET", "/api/v1/auth/config", nil, &m); err != nil {
		return false, false, err
	}
	return m["google"], m["dev_login"], nil
}

// InstanceSpec is the JSON spec accepted by the create endpoint.
type InstanceSpec map[string]any

func (c *Client) CreateInstance(ctx context.Context, spec InstanceSpec) (*store.Instance, error) {
	var m struct {
		Instance *store.Instance `json:"instance"`
	}
	if err := c.do(ctx, "POST", "/api/v1/instances", spec, &m); err != nil {
		return nil, err
	}
	return m.Instance, nil
}

func (c *Client) ListInstances(ctx context.Context, status types.InstanceStatus) ([]*store.Instance, error) {
	var m struct {
		Instances []*store.Instance `json:"instances"`
	}
	q := ""
	if status != "" {
		q = "?status=" + url.QueryEscape(string(status))
	}
	if err := c.do(ctx, "GET", "/api/v1/instances"+q, nil, &m); err != nil {
		return nil, err
	}
	return m.Instances, nil
}

func (c *Client) GetInstance(ctx context.Context, id string) (*store.Instance, error) {
	var m struct {
		Instance *store.Instance `json:"instance"`
	}
	if err := c.do(ctx, "GET", "/api/v1/instances/"+id, nil, &m); err != nil {
		return nil, err
	}
	return m.Instance, nil
}

func (c *Client) DeleteInstance(ctx context.Context, id string) error {
	return c.do(ctx, "DELETE", "/api/v1/instances/"+id, nil, nil)
}

func (c *Client) Keepalive(ctx context.Context, id string) (*store.Instance, error) {
	var m struct {
		Instance *store.Instance `json:"instance"`
	}
	if err := c.do(ctx, "POST", "/api/v1/instances/"+id+"/keepalive", map[string]any{}, &m); err != nil {
		return nil, err
	}
	return m.Instance, nil
}

// Exec runs code; res.Error non-nil means the script failed (HTTP 200).
func (c *Client) Exec(ctx context.Context, id, code string, timeoutMs int64) (*engine.Result, error) {
	var res engine.Result
	err := c.do(ctx, "POST", "/api/v1/instances/"+id+"/exec",
		map[string]any{"code": code, "timeout_ms": timeoutMs}, &res)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// FileEntry is one files-endpoint entry.
type FileEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
	Mtime int64  `json:"mtime"`
}

// Files lists a directory (IsDir entries) or returns file content.
func (c *Client) Files(ctx context.Context, id, path string) (entries []FileEntry, content string, err error) {
	var m map[string]any
	err = c.do(ctx, "GET", "/api/v1/instances/"+id+"/files?path="+url.QueryEscape(path), nil, &m)
	if err != nil {
		return nil, "", err
	}
	if raw, ok := m["entries"]; ok {
		b, _ := json.Marshal(raw)
		_ = json.Unmarshal(b, &entries)
		return entries, "", nil
	}
	s, _ := m["content"].(string)
	return nil, s, nil
}

func (c *Client) ListExecutions(ctx context.Context, id string, limit int) ([]*store.Execution, error) {
	var m struct {
		Executions []*store.Execution `json:"executions"`
	}
	q := ""
	if limit > 0 {
		q = "?limit=" + strconv.Itoa(limit)
	}
	if err := c.do(ctx, "GET", "/api/v1/instances/"+id+"/executions"+q, nil, &m); err != nil {
		return nil, err
	}
	return m.Executions, nil
}

// Audit lists audit events with optional filters.
func (c *Client) Audit(ctx context.Context, instanceID, execID string, limit int, before *time.Time) ([]*store.AuditEvent, error) {
	var m struct {
		Events []*store.AuditEvent `json:"events"`
	}
	q := url.Values{}
	if instanceID != "" {
		q.Set("instance_id", instanceID)
	}
	if execID != "" {
		q.Set("exec_id", execID)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if before != nil {
		q.Set("before", before.UTC().Format(time.RFC3339Nano))
	}
	path := "/api/v1/audit"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	if err := c.do(ctx, "GET", path, nil, &m); err != nil {
		return nil, err
	}
	return m.Events, nil
}

// Policies CRUD.
func (c *Client) ListPolicies(ctx context.Context) ([]*store.Policy, error) {
	var m struct {
		Policies []*store.Policy `json:"policies"`
	}
	if err := c.do(ctx, "GET", "/api/v1/policies", nil, &m); err != nil {
		return nil, err
	}
	return m.Policies, nil
}

func (c *Client) GetPolicy(ctx context.Context, id string) (*store.Policy, error) {
	var m struct {
		Policy *store.Policy `json:"policy"`
	}
	if err := c.do(ctx, "GET", "/api/v1/policies/"+id, nil, &m); err != nil {
		return nil, err
	}
	return m.Policy, nil
}

func (c *Client) CreatePolicy(ctx context.Context, name, rego string, enabled bool) (*store.Policy, error) {
	var m struct {
		Policy *store.Policy `json:"policy"`
	}
	err := c.do(ctx, "POST", "/api/v1/policies",
		map[string]any{"name": name, "rego": rego, "enabled": enabled}, &m)
	return m.Policy, err
}

func (c *Client) UpdatePolicy(ctx context.Context, id string, name *string, rego *string, enabled *bool) (*store.Policy, error) {
	var m struct {
		Policy *store.Policy `json:"policy"`
	}
	body := map[string]any{}
	if name != nil {
		body["name"] = *name
	}
	if rego != nil {
		body["rego"] = *rego
	}
	if enabled != nil {
		body["enabled"] = *enabled
	}
	err := c.do(ctx, "PUT", "/api/v1/policies/"+id, body, &m)
	return m.Policy, err
}

func (c *Client) DeletePolicy(ctx context.Context, id string) error {
	return c.do(ctx, "DELETE", "/api/v1/policies/"+id, nil, nil)
}

func (c *Client) ValidatePolicy(ctx context.Context, rego string) (valid bool, errMsg string, err error) {
	var m map[string]any
	err = c.do(ctx, "POST", "/api/v1/policies/validate", map[string]any{"rego": rego}, &m)
	if err != nil {
		return false, "", err
	}
	v, _ := m["valid"].(bool)
	e, _ := m["error"].(string)
	return v, e, nil
}

// Keys (session-auth only server-side).
func (c *Client) ListKeys(ctx context.Context) ([]*store.APIKey, error) {
	var m struct {
		Keys []*store.APIKey `json:"keys"`
	}
	if err := c.do(ctx, "GET", "/api/v1/keys", nil, &m); err != nil {
		return nil, err
	}
	return m.Keys, nil
}

func (c *Client) CreateKey(ctx context.Context, name string, expiresInSeconds int64) (*store.APIKey, string, error) {
	var m struct {
		Key    *store.APIKey `json:"key"`
		Secret string        `json:"secret"`
	}
	err := c.do(ctx, "POST", "/api/v1/keys",
		map[string]any{"name": name, "expires_in_seconds": expiresInSeconds}, &m)
	return m.Key, m.Secret, err
}

func (c *Client) DeleteKey(ctx context.Context, id string) error {
	return c.do(ctx, "DELETE", "/api/v1/keys/"+id, nil, nil)
}

// Secrets vault (session-auth only server-side; ListSecrets never
// returns values).
func (c *Client) ListSecrets(ctx context.Context) ([]*store.Secret, error) {
	var m struct {
		Secrets []*store.Secret `json:"secrets"`
	}
	if err := c.do(ctx, "GET", "/api/v1/secrets", nil, &m); err != nil {
		return nil, err
	}
	return m.Secrets, nil
}

func (c *Client) CreateSecret(ctx context.Context, name, value string, domains []string) (*store.Secret, error) {
	var m struct {
		Secret *store.Secret `json:"secret"`
	}
	err := c.do(ctx, "POST", "/api/v1/secrets",
		map[string]any{"name": name, "value": value, "allowed_domains": domains}, &m)
	return m.Secret, err
}

func (c *Client) UpdateSecret(ctx context.Context, id string, value *string, domains []string) (*store.Secret, error) {
	var m struct {
		Secret *store.Secret `json:"secret"`
	}
	body := map[string]any{}
	if value != nil {
		body["value"] = *value
	}
	if domains != nil {
		body["allowed_domains"] = domains
	}
	err := c.do(ctx, "PUT", "/api/v1/secrets/"+id, body, &m)
	return m.Secret, err
}

func (c *Client) DeleteSecret(ctx context.Context, id string) error {
	return c.do(ctx, "DELETE", "/api/v1/secrets/"+id, nil, nil)
}

// Capabilities documents the registry.
func (c *Client) Capabilities(ctx context.Context) ([]map[string]any, error) {
	var m struct {
		Capabilities []map[string]any `json:"capabilities"`
	}
	if err := c.do(ctx, "GET", "/api/v1/capabilities", nil, &m); err != nil {
		return nil, err
	}
	return m.Capabilities, nil
}
