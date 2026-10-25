// Package workspace owns team-workspace management: create, rename,
// delete, members, shares and the change log. Every check goes through
// access; every mutation writes workspace_changes in the same
// transaction as the change itself.
package workspace

import (
	"context"
	"errors"

	"calcside/cmd/calcside/internal/service"
	"calcside/cmd/calcside/internal/service/access"
	"calcside/internal/store"
	"calcside/internal/types"
)

type Service struct {
	st     store.Store
	access *access.Checker
}

func New(st store.Store, ac *access.Checker) *Service {
	return &Service{st: st, access: ac}
}

// Create makes a non-personal workspace with the caller as owner.
func (s *Service) Create(ctx context.Context, a service.Actor, name string) (*store.Workspace, error) {
	if err := s.access.Workspace(ctx, a, a.WorkspaceID, access.Manage); err != nil {
		return nil, err
	}
	if name == "" {
		return nil, service.Errf(types.ErrCodeBadRequest, "name required")
	}
	w := &store.Workspace{Name: name}
	err := s.st.Tx(ctx, func(tx store.Store) error {
		if err := tx.CreateWorkspace(ctx, w); err != nil {
			return service.Internal(err)
		}
		return tx.AddMember(ctx, &store.Member{WorkspaceID: w.ID, UserID: a.UserID, Role: store.RoleOwner})
	})
	if err != nil {
		return nil, err
	}
	return w, nil
}

// Get returns a workspace the actor can view.
func (s *Service) Get(ctx context.Context, a service.Actor, id string) (*store.Workspace, error) {
	if err := s.access.Workspace(ctx, a, id, access.View); err != nil {
		return nil, err
	}
	w, err := s.st.GetWorkspace(ctx, id)
	if err != nil {
		return nil, service.NotFound("workspace not found")
	}
	return w, nil
}

// Rename changes the workspace name (Manage).
func (s *Service) Rename(ctx context.Context, a service.Actor, id, name string) (*store.Workspace, error) {
	if err := s.access.Workspace(ctx, a, id, access.Manage); err != nil {
		return nil, err
	}
	w, err := s.st.GetWorkspace(ctx, id)
	if err != nil {
		return nil, service.NotFound("workspace not found")
	}
	if name == "" {
		return nil, service.Errf(types.ErrCodeBadRequest, "name required")
	}
	err = s.st.Tx(ctx, func(tx store.Store) error {
		w.Name = name
		if err := tx.UpdateWorkspace(ctx, w); err != nil {
			return service.Internal(err)
		}
		return s.change(ctx, tx, a, &store.WorkspaceChange{WorkspaceID: id, Change: store.ChangeRenamed})
	})
	if err != nil {
		return nil, err
	}
	return w, nil
}

// Delete removes the workspace, its members, policies, secrets and
// keys (revoked). Personal workspaces cannot be deleted; a workspace
// with running instances is a conflict. Ended instances, executions
// and audit rows stay — they just become invisible.
func (s *Service) Delete(ctx context.Context, a service.Actor, id string) error {
	w, err := s.st.GetWorkspace(ctx, id)
	if err != nil {
		return service.NotFound("workspace not found")
	}
	if w.Personal {
		return service.Forbidden("personal workspaces cannot be deleted")
	}
	if err := s.access.Workspace(ctx, a, id, access.Manage); err != nil {
		return err
	}
	live, err := s.st.ListInstances(ctx, store.InstanceFilter{WorkspaceID: id, Status: types.InstanceRunning})
	if err != nil {
		return service.Internal(err)
	}
	if len(live) > 0 {
		return service.Conflict("workspace has running instances")
	}
	return s.st.Tx(ctx, func(tx store.Store) error {
		// Revoke keys rather than deleting: rows stay for the record.
		members, err := tx.ListMembers(ctx, id)
		if err != nil {
			return service.Internal(err)
		}
		for _, m := range members {
			if err := tx.RevokeUserKeys(ctx, id, m.UserID); err != nil {
				return service.Internal(err)
			}
		}
		pols, err := tx.ListPolicies(ctx, id)
		if err != nil {
			return service.Internal(err)
		}
		for _, p := range pols {
			if err := tx.DeletePolicy(ctx, p.ID); err != nil {
				return service.Internal(err)
			}
		}
		secs, err := tx.ListSecrets(ctx, id)
		if err != nil {
			return service.Internal(err)
		}
		for _, sc := range secs {
			if err := tx.DeleteSecret(ctx, sc.ID); err != nil {
				return service.Internal(err)
			}
		}
		for _, m := range members {
			if err := tx.DeleteMember(ctx, id, m.UserID); err != nil {
				return service.Internal(err)
			}
		}
		return tx.DeleteWorkspace(ctx, id)
	})
}

// Members lists workspace members (View).
func (s *Service) Members(ctx context.Context, a service.Actor, id string) ([]*store.MemberInfo, error) {
	if err := s.access.Workspace(ctx, a, id, access.View); err != nil {
		return nil, err
	}
	ms, err := s.st.ListMembers(ctx, id)
	if err != nil {
		return nil, service.Internal(err)
	}
	return ms, nil
}

// AddMember adds an existing user by email (Manage). Personal
// workspaces have exactly one member.
func (s *Service) AddMember(ctx context.Context, a service.Actor, id, email string, role types.Role) (*store.MemberInfo, error) {
	w, err := s.notPersonal(ctx, a, id)
	if err != nil {
		return nil, err
	}
	u, err := s.st.GetUserByEmail(ctx, email)
	if err != nil {
		return nil, service.NotFound("no user with that email")
	}
	if !role.Valid() {
		return nil, service.Errf(types.ErrCodeBadRequest, "invalid role")
	}
	if _, err := s.st.GetMember(ctx, id, u.ID); err == nil {
		return nil, service.Conflict("already a member")
	}
	err = s.st.Tx(ctx, func(tx store.Store) error {
		if err := tx.AddMember(ctx, &store.Member{WorkspaceID: id, UserID: u.ID, Role: role}); err != nil {
			if errors.Is(err, store.ErrConflict) {
				return service.Conflict("already a member")
			}
			return service.Internal(err)
		}
		return s.change(ctx, tx, a, &store.WorkspaceChange{WorkspaceID: w.ID, Change: store.ChangeMemberAdded, UserID: u.ID, Role: role})
	})
	if err != nil {
		return nil, err
	}
	return &store.MemberInfo{UserID: u.ID, Email: u.Email, Name: u.Name, Role: role}, nil
}

// SetRole changes a member's role (Manage); the last owner cannot be
// demoted.
func (s *Service) SetRole(ctx context.Context, a service.Actor, id, userID string, role types.Role) error {
	if _, err := s.notPersonal(ctx, a, id); err != nil {
		return err
	}
	if !role.Valid() {
		return service.Errf(types.ErrCodeBadRequest, "invalid role")
	}
	m, err := s.st.GetMember(ctx, id, userID)
	if err != nil {
		return service.NotFound("member not found")
	}
	if m.Role == role {
		return nil
	}
	return s.st.Tx(ctx, func(tx store.Store) error {
		if err := tx.LockWorkspace(ctx, id); err != nil {
			return service.Internal(err)
		}
		if m.Role == store.RoleOwner && role != store.RoleOwner {
			owners, err := tx.CountMembers(ctx, id, store.RoleOwner)
			if err != nil {
				return service.Internal(err)
			}
			if owners <= 1 {
				return service.Conflict("workspace needs at least one owner")
			}
		}
		if err := tx.UpdateMember(ctx, &store.Member{WorkspaceID: id, UserID: userID, Role: role}); err != nil {
			return service.Internal(err)
		}
		return s.change(ctx, tx, a, &store.WorkspaceChange{WorkspaceID: id, Change: store.ChangeRoleChanged, UserID: userID, Role: role})
	})
}

// RemoveMember removes a member (Manage) — or the member removing
// themself (leave). Also revokes the member's keys in the workspace.
func (s *Service) RemoveMember(ctx context.Context, a service.Actor, id, userID string) error {
	self := userID == a.UserID
	if !self {
		if err := s.access.Workspace(ctx, a, id, access.Manage); err != nil {
			return err
		}
	} else if err := s.access.Workspace(ctx, a, id, access.View); err != nil {
		return err
	}
	m, err := s.st.GetMember(ctx, id, userID)
	if err != nil {
		return service.NotFound("member not found")
	}
	w, err := s.st.GetWorkspace(ctx, id)
	if err != nil {
		return service.NotFound("workspace not found")
	}
	if w.Personal {
		return service.Forbidden("personal workspaces have one member")
	}
	return s.st.Tx(ctx, func(tx store.Store) error {
		if err := tx.LockWorkspace(ctx, id); err != nil {
			return service.Internal(err)
		}
		if m.Role == store.RoleOwner {
			owners, err := tx.CountMembers(ctx, id, store.RoleOwner)
			if err != nil {
				return service.Internal(err)
			}
			if owners <= 1 {
				return service.Conflict("workspace needs at least one owner")
			}
		}
		if err := tx.RevokeUserKeys(ctx, id, userID); err != nil {
			return service.Internal(err)
		}
		if err := tx.DeleteMember(ctx, id, userID); err != nil {
			return service.Internal(err)
		}
		return s.change(ctx, tx, a, &store.WorkspaceChange{WorkspaceID: id, Change: store.ChangeMemberRemoved, UserID: userID})
	})
}

// Changes lists the change log (Manage), newest first.
func (s *Service) Changes(ctx context.Context, a service.Actor, id string) ([]*store.WorkspaceChange, error) {
	if err := s.access.Workspace(ctx, a, id, access.Manage); err != nil {
		return nil, err
	}
	lst, err := s.st.ListChanges(ctx, id, 200)
	if err != nil {
		return nil, service.Internal(err)
	}
	return lst, nil
}

// Shares lists an instance's shares (Manage on its workspace).
func (s *Service) Shares(ctx context.Context, a service.Actor, instanceID string) ([]*store.ShareInfo, error) {
	in, err := s.access.Instance(ctx, a, instanceID, access.View)
	if err != nil {
		return nil, err
	}
	if err := s.access.Workspace(ctx, a, in.WorkspaceID, access.Manage); err != nil {
		return nil, err
	}
	lst, err := s.st.ListShares(ctx, instanceID)
	if err != nil {
		return nil, service.Internal(err)
	}
	return lst, nil
}

// PutShare upserts a share (Manage on the instance's workspace).
// Sharing with an existing member is a bad request — members already
// have access.
func (s *Service) PutShare(ctx context.Context, a service.Actor, instanceID, email string, role types.Role) (*store.ShareInfo, error) {
	in, err := s.access.Instance(ctx, a, instanceID, access.View)
	if err != nil {
		return nil, err
	}
	if err := s.access.Workspace(ctx, a, in.WorkspaceID, access.Manage); err != nil {
		return nil, err
	}
	if !role.ValidForShare() {
		return nil, service.Errf(types.ErrCodeBadRequest, "role must be viewer or editor")
	}
	u, err := s.st.GetUserByEmail(ctx, email)
	if err != nil {
		return nil, service.NotFound("no user with that email")
	}
	if _, err := s.st.GetMember(ctx, in.WorkspaceID, u.ID); err == nil {
		return nil, service.Errf(types.ErrCodeBadRequest, "user is already a member of this workspace")
	}
	sh := &store.Share{InstanceID: instanceID, UserID: u.ID, Role: role, CreatedBy: a.UserID}
	err = s.st.Tx(ctx, func(tx store.Store) error {
		if err := tx.CreateShare(ctx, sh); err != nil {
			return service.Internal(err)
		}
		return s.change(ctx, tx, a, &store.WorkspaceChange{WorkspaceID: in.WorkspaceID, Change: store.ChangeShareAdded, UserID: u.ID, Role: role, InstanceID: instanceID})
	})
	if err != nil {
		return nil, err
	}
	return &store.ShareInfo{Share: *sh, Email: u.Email, Name: u.Name}, nil
}

// DeleteShare removes a share (Manage, or the recipient leaving).
func (s *Service) DeleteShare(ctx context.Context, a service.Actor, instanceID, userID string) error {
	in, err := s.access.Instance(ctx, a, instanceID, access.View)
	if err != nil {
		return err
	}
	if userID != a.UserID {
		if err := s.access.Workspace(ctx, a, in.WorkspaceID, access.Manage); err != nil {
			return err
		}
	}
	return s.st.Tx(ctx, func(tx store.Store) error {
		if err := tx.DeleteShare(ctx, instanceID, userID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return service.NotFound("share not found")
			}
			return service.Internal(err)
		}
		return s.change(ctx, tx, a, &store.WorkspaceChange{WorkspaceID: in.WorkspaceID, Change: store.ChangeShareRemoved, UserID: userID, InstanceID: instanceID})
	})
}

func (s *Service) notPersonal(ctx context.Context, a service.Actor, id string) (*store.Workspace, error) {
	w, err := s.st.GetWorkspace(ctx, id)
	if err != nil {
		return nil, service.NotFound("workspace not found")
	}
	if w.Personal {
		return nil, service.Forbidden("personal workspaces have one member")
	}
	if err := s.access.Workspace(ctx, a, id, access.Manage); err != nil {
		return nil, err
	}
	return w, nil
}

func (s *Service) change(ctx context.Context, tx store.Store, a service.Actor, c *store.WorkspaceChange) error {
	c.ByUserID = a.UserID
	return tx.InsertChange(ctx, c)
}
