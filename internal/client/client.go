// Package client is a typed HTTP client for the calcside API, used by
// csctl and tests. Internals are generated from api/openapi.yaml
// (internal/client/gen); this file is a thin wrapper keeping the
// domain-shaped API stable.
package client

//go:generate go tool oapi-codegen -config ../../api/oapi-client.yaml ../../api/openapi.yaml

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"calcside/internal/client/gen"
	"calcside/internal/engine"
	"calcside/internal/store"
	"calcside/internal/types"
)

// Client talks to a calcside server with API-key or session auth.
type Client struct {
	gc     *gen.ClientWithResponses
	key    string
	cookie string // session cookie alternative to API key
}

// New builds a client. server like "http://localhost:8080".
func New(server, apiKey string) *Client {
	c := &Client{key: apiKey}
	c.gc, _ = gen.NewClientWithResponses(server,
		gen.WithHTTPClient(&http.Client{Timeout: 60 * time.Second}),
		gen.WithRequestEditorFn(c.authEditor))
	return c
}

// WithCookie uses a session cookie instead of an API key.
func (c *Client) WithCookie(cookie string) *Client {
	c.cookie = cookie
	return c
}

func (c *Client) authEditor(_ context.Context, req *http.Request) error {
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	if c.cookie != "" {
		req.Header.Set("Cookie", "cs_session="+c.cookie)
		req.Header.Set("X-Requested-With", "calcside")
	}
	return nil
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

// apiErr converts a non-2xx status + raw body into *Error.
func apiErr(status int, body []byte) error {
	if status < 400 {
		return nil
	}
	e := &Error{Status: status}
	var m map[string]any
	if json.Unmarshal(body, &m) == nil {
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
		e.Message = string(body)
	}
	return e
}

// statusCode is satisfied by every generated response type.
type statusCode interface {
	StatusCode() int
}

func unwrap[T statusCode](resp T, body []byte, out any) error {
	if err := apiErr(resp.StatusCode(), body); err != nil {
		return err
	}
	if out != nil && len(body) > 0 {
		return json.Unmarshal(body, out)
	}
	return nil
}

// Me returns the authenticated user.
func (c *Client) Me(ctx context.Context) (*store.User, error) {
	resp, err := c.gc.MeWithResponse(ctx)
	if err != nil {
		return nil, err
	}
	var m struct {
		User   *store.User `json:"user"`
		ViaKey bool        `json:"via_key"`
	}
	if err := unwrap(resp, resp.Body, &m); err != nil {
		return nil, err
	}
	return m.User, nil
}

// AuthConfig reports enabled login methods (unauthenticated); the
// second return is dev mode.
func (c *Client) AuthConfig(ctx context.Context) (google, devMode bool, err error) {
	resp, err := c.gc.AuthConfigWithResponse(ctx)
	if err != nil {
		return false, false, err
	}
	var m map[string]bool
	if err := unwrap(resp, resp.Body, &m); err != nil {
		return false, false, err
	}
	return m["google"], m["dev_mode"], nil
}

// InstanceSpec is the JSON spec accepted by the create endpoint.
type InstanceSpec map[string]any

func (c *Client) CreateInstance(ctx context.Context, spec InstanceSpec) (*store.Instance, error) {
	raw, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	resp, err := c.gc.CreateInstanceWithBodyWithResponse(ctx, "application/json", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	var m struct {
		Instance *store.Instance `json:"instance"`
	}
	if err := unwrap(resp, resp.Body, &m); err != nil {
		return nil, err
	}
	return m.Instance, nil
}

func (c *Client) ListInstances(ctx context.Context, status types.InstanceStatus) ([]*store.Instance, error) {
	params := &gen.ListInstancesParams{}
	if status != "" {
		s := gen.InstanceStatus(status)
		params.Status = &s
	}
	resp, err := c.gc.ListInstancesWithResponse(ctx, params)
	if err != nil {
		return nil, err
	}
	var m struct {
		Instances []*store.Instance `json:"instances"`
	}
	if err := unwrap(resp, resp.Body, &m); err != nil {
		return nil, err
	}
	return m.Instances, nil
}

func (c *Client) GetInstance(ctx context.Context, id string) (*store.Instance, error) {
	resp, err := c.gc.GetInstanceWithResponse(ctx, id)
	if err != nil {
		return nil, err
	}
	var m struct {
		Instance *store.Instance `json:"instance"`
	}
	if err := unwrap(resp, resp.Body, &m); err != nil {
		return nil, err
	}
	return m.Instance, nil
}

func (c *Client) DeleteInstance(ctx context.Context, id string) error {
	resp, err := c.gc.DeleteInstanceWithResponse(ctx, id)
	if err != nil {
		return err
	}
	return apiErr(resp.StatusCode(), resp.Body)
}

func (c *Client) Keepalive(ctx context.Context, id string) (*store.Instance, error) {
	resp, err := c.gc.KeepaliveWithResponse(ctx, id)
	if err != nil {
		return nil, err
	}
	var m struct {
		Instance *store.Instance `json:"instance"`
	}
	if err := unwrap(resp, resp.Body, &m); err != nil {
		return nil, err
	}
	return m.Instance, nil
}

// Exec runs code; res.Error non-nil means the script failed (HTTP 200).
func (c *Client) Exec(ctx context.Context, id, code string, timeoutMs int64) (*engine.Result, error) {
	resp, err := c.gc.ExecWithResponse(ctx, id, gen.ExecRequest{
		Code: code, TimeoutMs: &timeoutMs,
	})
	if err != nil {
		return nil, err
	}
	var res engine.Result
	if err := unwrap(resp, resp.Body, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// PromptResult is the server-generated agent prompt for an instance.
type PromptResult struct {
	InstanceID   string            `json:"instance_id"`
	Prompt       string            `json:"prompt"`
	Capabilities []string          `json:"capabilities"`
	Tools        map[string]string `json:"tools"`
}

// Prompt returns the server-generated agent prompt for an instance.
func (c *Client) Prompt(ctx context.Context, id, toolPrefix string) (*PromptResult, error) {
	params := &gen.InstancePromptParams{}
	if toolPrefix != "" {
		params.ToolPrefix = &toolPrefix
	}
	resp, err := c.gc.InstancePromptWithResponse(ctx, id, params)
	if err != nil {
		return nil, err
	}
	var m PromptResult
	if err := unwrap(resp, resp.Body, &m); err != nil {
		return nil, err
	}
	return &m, nil
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
	resp, err := c.gc.FilesWithResponse(ctx, id, &gen.FilesParams{Path: &path})
	if err != nil {
		return nil, "", err
	}
	var m map[string]any
	if err := unwrap(resp, resp.Body, &m); err != nil {
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

// Inspect returns the instance's live globals: variable name to repr,
// secret-scrubbed by the node.
func (c *Client) Inspect(ctx context.Context, id string) (map[string]string, error) {
	resp, err := c.gc.InstanceInspectWithResponse(ctx, id)
	if err != nil {
		return nil, err
	}
	var m struct {
		Variables map[string]string `json:"variables"`
	}
	if err := unwrap(resp, resp.Body, &m); err != nil {
		return nil, err
	}
	return m.Variables, nil
}

func (c *Client) ListExecutions(ctx context.Context, id string, limit int) ([]*store.Execution, error) {
	params := &gen.ListExecutionsParams{}
	if limit > 0 {
		params.Limit = &limit
	}
	resp, err := c.gc.ListExecutionsWithResponse(ctx, id, params)
	if err != nil {
		return nil, err
	}
	var m struct {
		Executions []*store.Execution `json:"executions"`
	}
	if err := unwrap(resp, resp.Body, &m); err != nil {
		return nil, err
	}
	return m.Executions, nil
}

// Audit lists audit events with optional filters.
func (c *Client) Audit(ctx context.Context, instanceID, execID string, limit int, before *time.Time) ([]*store.AuditEvent, error) {
	params := &gen.ListAuditParams{}
	if instanceID != "" {
		params.InstanceId = &instanceID
	}
	if execID != "" {
		params.ExecId = &execID
	}
	if limit > 0 {
		params.Limit = &limit
	}
	if before != nil {
		params.Before = before
	}
	resp, err := c.gc.ListAuditWithResponse(ctx, params)
	if err != nil {
		return nil, err
	}
	var m struct {
		Events []*store.AuditEvent `json:"events"`
	}
	if err := unwrap(resp, resp.Body, &m); err != nil {
		return nil, err
	}
	return m.Events, nil
}

// Policies CRUD.
func (c *Client) ListPolicies(ctx context.Context) ([]*store.Policy, error) {
	resp, err := c.gc.ListPoliciesWithResponse(ctx)
	if err != nil {
		return nil, err
	}
	var m struct {
		Policies []*store.Policy `json:"policies"`
	}
	if err := unwrap(resp, resp.Body, &m); err != nil {
		return nil, err
	}
	return m.Policies, nil
}

func (c *Client) GetPolicy(ctx context.Context, id string) (*store.Policy, error) {
	resp, err := c.gc.GetPolicyWithResponse(ctx, id)
	if err != nil {
		return nil, err
	}
	var m struct {
		Policy *store.Policy `json:"policy"`
	}
	if err := unwrap(resp, resp.Body, &m); err != nil {
		return nil, err
	}
	return m.Policy, nil
}

func (c *Client) CreatePolicy(ctx context.Context, name, rego string, enabled bool) (*store.Policy, error) {
	resp, err := c.gc.CreatePolicyWithResponse(ctx, gen.PolicyRequest{
		Name: name, Rego: rego, Enabled: &enabled,
	})
	if err != nil {
		return nil, err
	}
	var m struct {
		Policy *store.Policy `json:"policy"`
	}
	if err := unwrap(resp, resp.Body, &m); err != nil {
		return nil, err
	}
	return m.Policy, nil
}

func (c *Client) UpdatePolicy(ctx context.Context, id string, name *string, rego *string, enabled *bool) (*store.Policy, error) {
	resp, err := c.gc.UpdatePolicyWithResponse(ctx, id, gen.PolicyUpdateRequest{
		Name: name, Rego: rego, Enabled: enabled,
	})
	if err != nil {
		return nil, err
	}
	var m struct {
		Policy *store.Policy `json:"policy"`
	}
	if err := unwrap(resp, resp.Body, &m); err != nil {
		return nil, err
	}
	return m.Policy, nil
}

func (c *Client) DeletePolicy(ctx context.Context, id string) error {
	resp, err := c.gc.DeletePolicyWithResponse(ctx, id)
	if err != nil {
		return err
	}
	return apiErr(resp.StatusCode(), resp.Body)
}

func (c *Client) ValidatePolicy(ctx context.Context, rego string) (valid bool, errMsg string, err error) {
	resp, err := c.gc.ValidatePolicyWithResponse(ctx, gen.ValidatePolicyRequest{Rego: rego})
	if err != nil {
		return false, "", err
	}
	var m map[string]any
	if err := unwrap(resp, resp.Body, &m); err != nil {
		return false, "", err
	}
	v, _ := m["valid"].(bool)
	e, _ := m["error"].(string)
	return v, e, nil
}

// Keys (session-auth only server-side).
func (c *Client) ListKeys(ctx context.Context) ([]*store.APIKey, error) {
	resp, err := c.gc.ListKeysWithResponse(ctx)
	if err != nil {
		return nil, err
	}
	var m struct {
		Keys []*store.APIKey `json:"keys"`
	}
	if err := unwrap(resp, resp.Body, &m); err != nil {
		return nil, err
	}
	return m.Keys, nil
}

func (c *Client) CreateKey(ctx context.Context, name string, expiresInSeconds int64) (*store.APIKey, string, error) {
	resp, err := c.gc.CreateKeyWithResponse(ctx, gen.CreateKeyRequest{
		Name: name, ExpiresInSeconds: &expiresInSeconds,
	})
	if err != nil {
		return nil, "", err
	}
	var m struct {
		Key    *store.APIKey `json:"key"`
		Secret string        `json:"secret"`
	}
	if err := unwrap(resp, resp.Body, &m); err != nil {
		return nil, "", err
	}
	return m.Key, m.Secret, nil
}

func (c *Client) DeleteKey(ctx context.Context, id string) error {
	resp, err := c.gc.DeleteKeyWithResponse(ctx, id)
	if err != nil {
		return err
	}
	return apiErr(resp.StatusCode(), resp.Body)
}

// Secrets vault (session-auth only server-side; ListSecrets never
// returns values).
func (c *Client) ListSecrets(ctx context.Context) ([]*store.Secret, error) {
	resp, err := c.gc.ListSecretsWithResponse(ctx)
	if err != nil {
		return nil, err
	}
	var m struct {
		Secrets []*store.Secret `json:"secrets"`
	}
	if err := unwrap(resp, resp.Body, &m); err != nil {
		return nil, err
	}
	return m.Secrets, nil
}

func (c *Client) CreateSecret(ctx context.Context, name, value string, domains []string) (*store.Secret, error) {
	resp, err := c.gc.CreateSecretWithResponse(ctx, gen.CreateSecretRequest{
		Name: name, Value: value, AllowedDomains: &domains,
	})
	if err != nil {
		return nil, err
	}
	var m struct {
		Secret *store.Secret `json:"secret"`
	}
	if err := unwrap(resp, resp.Body, &m); err != nil {
		return nil, err
	}
	return m.Secret, nil
}

func (c *Client) UpdateSecret(ctx context.Context, id string, value *string, domains []string) (*store.Secret, error) {
	resp, err := c.gc.UpdateSecretWithResponse(ctx, id, gen.UpdateSecretRequest{
		Value: value, AllowedDomains: &domains,
	})
	if err != nil {
		return nil, err
	}
	var m struct {
		Secret *store.Secret `json:"secret"`
	}
	if err := unwrap(resp, resp.Body, &m); err != nil {
		return nil, err
	}
	return m.Secret, nil
}

func (c *Client) DeleteSecret(ctx context.Context, id string) error {
	resp, err := c.gc.DeleteSecretWithResponse(ctx, id)
	if err != nil {
		return err
	}
	return apiErr(resp.StatusCode(), resp.Body)
}

// Capabilities documents the registry.
func (c *Client) Capabilities(ctx context.Context) ([]map[string]any, error) {
	resp, err := c.gc.CapabilitiesWithResponse(ctx)
	if err != nil {
		return nil, err
	}
	var m struct {
		Capabilities []map[string]any `json:"capabilities"`
	}
	if err := unwrap(resp, resp.Body, &m); err != nil {
		return nil, err
	}
	return m.Capabilities, nil
}
