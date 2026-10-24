// Package access is the single place authorization is decided (EP-0010):
// every service asks the Checker instead of comparing user IDs. An
// actor's effective actions = workspace membership role ∩ key ceiling;
// no view → not_found, view but not the action → forbidden.
package access

import (
	"context"
	"errors"

	"calcside/cmd/calcside/internal/service"
	"calcside/internal/store"
)

// Action is one capability over an object, weakest to strongest.
type Action uint8

const (
	View Action = iota + 1
	Run
	Configure
	Manage
)

// keyCeiling is the strongest action any API key may take: keys never
// configure or manage (rule 5).
const keyCeiling = Run

var roleActions = map[string]map[Action]bool{
	store.RoleViewer: {View: true},
	store.RoleEditor: {View: true, Run: true, Configure: true},
	store.RoleOwner:  {View: true, Run: true, Configure: true, Manage: true},
}

// Checker decides every access question against the store.
type Checker struct {
	st store.Store
}

func New(st store.Store) *Checker {
	return &Checker{st: st}
}

// role returns the actor's membership role in wsID. a.UserID is always
// the acting user — for keys, the key's creator (auth resolves the
// principal's user from the key row).
func (c *Checker) role(ctx context.Context, a service.Actor, wsID string) (string, error) {
	m, err := c.st.GetMember(ctx, wsID, a.UserID)
	if err != nil {
		return "", err
	}
	return m.Role, nil
}

// check resolves the actor's role in wsID once and applies the hidden
// vs forbidden rule. Keys live in exactly one workspace: any wsID other
// than a.WorkspaceID is invisible.
func (c *Checker) check(ctx context.Context, a service.Actor, wsID string, act Action, hidden error) error {
	if a.ViaKey() && a.WorkspaceID != wsID {
		return hidden
	}
	role, err := c.role(ctx, a, wsID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return hidden // not a member (or ws/user missing)
		}
		return service.Internal(err)
	}
	if act <= keyCeiling || !a.ViaKey() {
		if roleActions[role][act] {
			return nil
		}
	}
	if roleActions[role][View] {
		return service.Forbidden("forbidden")
	}
	return hidden
}

// Workspace checks act on a workspace itself.
func (c *Checker) Workspace(ctx context.Context, a service.Actor, wsID string, act Action) error {
	return c.check(ctx, a, wsID, act, service.NotFound("workspace not found"))
}

// Instance loads the instance and checks act in ITS workspace, so a
// member addressing a team instance by ID is authorized even when the
// request resolved a different (e.g. personal) workspace.
func (c *Checker) Instance(ctx context.Context, a service.Actor, id string, act Action) (*store.Instance, error) {
	if act > Run {
		// Manage/configure are workspace actions, not instance actions.
		return nil, service.Forbidden("forbidden")
	}
	in, err := c.st.GetInstance(ctx, id)
	if err != nil {
		return nil, service.NotFound("instance not found")
	}
	// A restricted key only ever sees instances it created.
	if a.Restricted() && in.KeyID != a.KeyID {
		return nil, service.NotFound("instance not found")
	}
	if err := c.check(ctx, a, in.WorkspaceID, act, service.NotFound("instance not found")); err != nil {
		return nil, err
	}
	return in, nil
}

// Execution loads the execution and checks view through its instance.
func (c *Checker) Execution(ctx context.Context, a service.Actor, id string) (*store.Execution, error) {
	ex, err := c.st.GetExecution(ctx, id)
	if err != nil {
		return nil, service.NotFound("execution not found")
	}
	if _, err := c.Instance(ctx, a, ex.InstanceID, View); err != nil {
		return nil, service.NotFound("execution not found")
	}
	return ex, nil
}

// Policy loads the policy and checks act in its workspace.
func (c *Checker) Policy(ctx context.Context, a service.Actor, id string, act Action) (*store.Policy, error) {
	pol, err := c.st.GetPolicy(ctx, id)
	if err != nil {
		return nil, service.NotFound("policy not found")
	}
	if err := c.check(ctx, a, pol.WorkspaceID, act, service.NotFound("policy not found")); err != nil {
		return nil, err
	}
	return pol, nil
}

// Secret loads the secret and checks act in its workspace.
func (c *Checker) Secret(ctx context.Context, a service.Actor, id string, act Action) (*store.Secret, error) {
	sec, err := c.st.GetSecret(ctx, id)
	if err != nil {
		return nil, service.NotFound("secret not found")
	}
	if err := c.check(ctx, a, sec.WorkspaceID, act, service.NotFound("secret not found")); err != nil {
		return nil, err
	}
	return sec, nil
}

// Key loads the key and checks act in its workspace.
func (c *Checker) Key(ctx context.Context, a service.Actor, id string, act Action) (*store.APIKey, error) {
	k, err := c.st.GetAPIKey(ctx, id)
	if err != nil {
		return nil, service.NotFound("key not found")
	}
	if err := c.check(ctx, a, k.WorkspaceID, act, service.NotFound("key not found")); err != nil {
		return nil, err
	}
	return k, nil
}

// Memberships lists the workspaces the actor can act in: all of a
// session's memberships, or the key's one workspace.
func (c *Checker) Memberships(ctx context.Context, a service.Actor) ([]*store.Membership, error) {
	if a.ViaKey() {
		w, err := c.st.GetWorkspace(ctx, a.WorkspaceID)
		if err != nil {
			return nil, service.NotFound("workspace not found")
		}
		role, err := c.role(ctx, a, a.WorkspaceID)
		if err != nil {
			return nil, service.NotFound("workspace not found")
		}
		return []*store.Membership{{Workspace: w, Role: role}}, nil
	}
	return c.st.ListMemberships(ctx, a.UserID)
}

// Instances returns the store filter for the instances a may list:
// the actor's workspace, narrowed to key-created rows for a restricted
// key.
func (c *Checker) Instances(ctx context.Context, a service.Actor) (store.InstanceFilter, error) {
	f := store.InstanceFilter{WorkspaceID: a.WorkspaceID}
	if a.Restricted() {
		f.KeyID = a.KeyID
	}
	return f, nil
}
