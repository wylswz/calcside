package gormstore

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"calcside/internal/store"
)

type moduleRow struct {
	ID           string    `gorm:"primaryKey"`
	WorkspaceID  string    `gorm:"not null"`
	UserID       string    `gorm:"not null"`
	Name         string    `gorm:"not null"`
	Description  string    `gorm:"not null"`
	Files        string    `gorm:"not null"`
	DraftVersion int64     `gorm:"not null"`
	CreatedAt    time.Time `gorm:"not null"`
	UpdatedAt    time.Time `gorm:"not null"`
}

func (moduleRow) TableName() string { return "modules" }

type moduleVersionRow struct {
	ModuleID    string    `gorm:"primaryKey"`
	Version     int64     `gorm:"primaryKey"`
	WorkspaceID string    `gorm:"not null"`
	Sum         string    `gorm:"not null"`
	Files       string    `gorm:"not null"`
	PublishedBy string    `gorm:"not null"`
	Note        string    `gorm:"not null"`
	BaseVersion *int64    `gorm:""`
	CreatedAt   time.Time `gorm:"not null"`
}

func (moduleVersionRow) TableName() string { return "module_versions" }

func (r *moduleRow) toDomain() (*store.Module, error) {
	m := &store.Module{
		ID: r.ID, WorkspaceID: r.WorkspaceID, UserID: r.UserID,
		Name: r.Name, Description: r.Description,
		DraftVersion: r.DraftVersion,
		CreatedAt:    r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
	}
	if err := json.Unmarshal([]byte(r.Files), &m.Files); err != nil {
		return nil, store.ErrInvalidModule
	}
	return m, nil
}

func moduleToRow(m *store.Module) (*moduleRow, error) {
	files, err := json.Marshal(m.Files)
	if err != nil {
		return nil, store.ErrInvalidModule
	}
	return &moduleRow{
		ID: m.ID, WorkspaceID: m.WorkspaceID, UserID: m.UserID,
		Name: m.Name, Description: m.Description, Files: string(files),
		DraftVersion: m.DraftVersion,
		CreatedAt:    m.CreatedAt.UTC(), UpdatedAt: m.UpdatedAt.UTC(),
	}, nil
}

func (r *moduleVersionRow) toDomain() (*store.ModuleVersion, error) {
	v := &store.ModuleVersion{
		ModuleID: r.ModuleID, WorkspaceID: r.WorkspaceID,
		Version: r.Version, Sum: r.Sum,
		PublishedBy: r.PublishedBy, Note: r.Note,
		BaseVersion: r.BaseVersion, CreatedAt: r.CreatedAt.UTC(),
	}
	if err := json.Unmarshal([]byte(r.Files), &v.Files); err != nil {
		return nil, store.ErrInvalidModule
	}
	return v, nil
}

func versionToRow(v *store.ModuleVersion) (*moduleVersionRow, error) {
	files, err := json.Marshal(v.Files)
	if err != nil {
		return nil, store.ErrInvalidModule
	}
	return &moduleVersionRow{
		ModuleID: v.ModuleID, WorkspaceID: v.WorkspaceID,
		Version: v.Version, Sum: v.Sum, Files: string(files),
		PublishedBy: v.PublishedBy, Note: v.Note,
		BaseVersion: v.BaseVersion, CreatedAt: v.CreatedAt.UTC(),
	}, nil
}

func (d *sqlDB) CreateModule(ctx context.Context, m *store.Module) error {
	if err := store.ValidateModule(m); err != nil {
		return err
	}
	if m.ID == "" || m.DraftVersion != 1 {
		return store.ErrInvalidModule
	}
	row, err := moduleToRow(m)
	if err != nil {
		return err
	}
	return mapWriteErr(gorm.G[moduleRow](d.g).Create(ctx, row))
}

func (d *sqlDB) GetModule(ctx context.Context, id string) (*store.Module, error) {
	row, err := gorm.G[moduleRow](d.g).Where("id = ?", id).Take(ctx)
	if err != nil {
		return nil, mapNotFound(err)
	}
	m, err := row.toDomain()
	if err != nil {
		return nil, err
	}
	return m.Clone(), nil
}

func (d *sqlDB) GetModuleByName(ctx context.Context, workspaceID, name string) (*store.Module, error) {
	row, err := gorm.G[moduleRow](d.g).
		Where("workspace_id = ? AND name = ?", workspaceID, name).Take(ctx)
	if err != nil {
		return nil, mapNotFound(err)
	}
	m, err := row.toDomain()
	if err != nil {
		return nil, err
	}
	return m.Clone(), nil
}

func (d *sqlDB) ListModules(ctx context.Context, f store.ModuleFilter) ([]*store.Module, error) {
	if f.WorkspaceID == "" {
		return nil, store.ErrInvalidModule
	}
	limit := f.Limit
	if limit < 0 || limit > 200 {
		return nil, store.ErrInvalidModule
	}
	if limit == 0 {
		limit = 200
	}
	q := gorm.G[moduleRow](d.g).Where("workspace_id = ?", f.WorkspaceID)
	if f.AfterID != "" {
		q = q.Where("id > ?", f.AfterID)
	}
	rows, err := q.Order("id").Limit(limit).Find(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*store.Module, 0, len(rows))
	for i := range rows {
		m, err := rows[i].toDomain()
		if err != nil {
			return nil, err
		}
		out = append(out, m.Clone())
	}
	return out, nil
}

func (d *sqlDB) UpdateModuleDraft(ctx context.Context, m *store.Module, expectedDraftVersion int64) (bool, error) {
	if err := store.ValidateModule(m); err != nil {
		return false, err
	}
	var ok bool
	err := d.workspaceWrite(ctx, m.WorkspaceID, func(tx *sqlDB) error {
		var e error
		ok, e = tx.updateModuleDraftLocked(ctx, m, expectedDraftVersion)
		return e
	})
	return ok, err
}

func (d *sqlDB) updateModuleDraftLocked(ctx context.Context, m *store.Module, expectedDraftVersion int64) (bool, error) {
	if err := store.ValidateModule(m); err != nil {
		return false, err
	}
	var ok bool
	err := d.g.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		ok = false
		// SQLite ignores FOR UPDATE and would BUSY_SNAPSHOT on a
		// read-then-write under a concurrent publish; take the write
		// lock before reading, like the application store does.
		if err := tx.Exec("UPDATE modules SET draft_version = draft_version WHERE id = ?", m.ID).Error; err != nil {
			return err
		}
		row, err := gorm.G[moduleRow](tx, clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND workspace_id = ?", m.ID, m.WorkspaceID).Take(ctx)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if row.DraftVersion != expectedDraftVersion {
			return nil // stale CAS — no write
		}
		next, err := moduleToRow(m)
		if err != nil {
			return err
		}
		next.ID = row.ID
		next.WorkspaceID = row.WorkspaceID
		next.UserID = row.UserID
		next.DraftVersion = row.DraftVersion + 1
		next.CreatedAt = row.CreatedAt
		next.UpdatedAt = m.UpdatedAt.UTC()
		if err := tx.Save(next).Error; err != nil {
			return mapWriteErr(err)
		}
		ok = true
		return nil
	})
	return ok, err
}

func (d *sqlDB) DeleteModule(ctx context.Context, workspaceID, id string) error {
	return d.workspaceWrite(ctx, workspaceID, func(tx *sqlDB) error {
		return tx.deleteModuleLocked(ctx, workspaceID, id)
	})
}

func (d *sqlDB) deleteModuleLocked(ctx context.Context, workspaceID, id string) error {
	return d.g.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Lock the module row before dropping versions so a concurrent
		// publish cannot insert an orphan version row mid-delete.
		if err := tx.Exec("UPDATE modules SET draft_version = draft_version WHERE id = ?", id).Error; err != nil {
			return err
		}
		row, err := gorm.G[moduleRow](tx, clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND workspace_id = ?", id, workspaceID).Take(ctx)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return store.ErrNotFound
		}
		if err != nil {
			return err
		}
		if err := tx.Where("module_id = ?", row.ID).Delete(&moduleVersionRow{}).Error; err != nil {
			return err
		}
		if err := tx.Where("module_id = ?", row.ID).Delete(&moduleDebugSettingsRow{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", row.ID).Delete(&moduleRow{}).Error
	})
}

func (d *sqlDB) GetModuleVersion(ctx context.Context, moduleID string, version int64) (*store.ModuleVersion, error) {
	row, err := gorm.G[moduleVersionRow](d.g).
		Where("module_id = ? AND version = ?", moduleID, version).Take(ctx)
	if err != nil {
		return nil, mapNotFound(err)
	}
	v, err := row.toDomain()
	if err != nil {
		return nil, err
	}
	return v.Clone(), nil
}

func (d *sqlDB) ListModuleVersions(ctx context.Context, moduleID string) ([]*store.ModuleVersion, error) {
	rows, err := gorm.G[moduleVersionRow](d.g).
		Where("module_id = ?", moduleID).Order("version").Find(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*store.ModuleVersion, 0, len(rows))
	for i := range rows {
		v, err := rows[i].toDomain()
		if err != nil {
			return nil, err
		}
		out = append(out, v.Clone())
	}
	return out, nil
}

func (d *sqlDB) PublishModule(ctx context.Context, workspaceID, id string, expectedDraftVersion int64, expectedBaseSum string, candidate *store.ModuleVersion) (*store.ModuleVersion, error) {
	var out *store.ModuleVersion
	err := d.workspaceWrite(ctx, workspaceID, func(tx *sqlDB) error {
		var e error
		out, e = tx.publishModuleLocked(ctx, workspaceID, id, expectedDraftVersion, expectedBaseSum, candidate)
		return e
	})
	return out, err
}

func (d *sqlDB) publishModuleLocked(ctx context.Context, workspaceID, id string, expectedDraftVersion int64, expectedBaseSum string, candidate *store.ModuleVersion) (*store.ModuleVersion, error) {
	if err := store.ValidateModuleVersion(candidate); err != nil {
		return nil, err
	}
	var out *store.ModuleVersion
	err := d.g.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		out = nil
		// Serialize publishes per workspace: draft CAS and the version
		// counter are decided inside one locked transaction.
		if err := tx.Exec("UPDATE workspaces SET id = id WHERE id = ?", workspaceID).Error; err != nil {
			return err
		}
		row, err := gorm.G[moduleRow](tx, clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND workspace_id = ?", id, workspaceID).Take(ctx)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return store.ErrNotFound
		}
		if err != nil {
			return err
		}
		if row.DraftVersion != expectedDraftVersion {
			return store.ErrModuleDraftConflict
		}
		latest, lerr := gorm.G[moduleVersionRow](tx).
			Where("module_id = ?", id).Order("version DESC").Limit(1).Take(ctx)
		switch {
		case errors.Is(lerr, gorm.ErrRecordNotFound):
			if expectedBaseSum != "" {
				return store.ErrModuleBaseConflict
			}
		case lerr != nil:
			return lerr
		default:
			if latest.Sum != expectedBaseSum {
				return store.ErrModuleBaseConflict
			}
			if latest.Sum == candidate.Sum {
				// Identical digest: a no-op returning the existing
				// version — never a new row.
				v, verr := latest.toDomain()
				if verr != nil {
					return verr
				}
				out = v.Clone()
				return nil
			}
		}
		next := &store.ModuleVersion{
			ModuleID: id, WorkspaceID: workspaceID,
			Version: 1, Sum: candidate.Sum, Files: candidate.Files,
			PublishedBy: candidate.PublishedBy, Note: candidate.Note,
			CreatedAt: candidate.CreatedAt.UTC(),
		}
		if next.CreatedAt.IsZero() {
			next.CreatedAt = time.Now().UTC()
		}
		if lerr == nil {
			next.Version = latest.Version + 1
			b := latest.Version
			next.BaseVersion = &b
		}
		vrow, err := versionToRow(next)
		if err != nil {
			return err
		}
		if err := tx.Create(vrow).Error; err != nil {
			return mapWriteErr(err)
		}
		out = next.Clone()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// moduleDebugSettingsRow stores per-module debug panel state; the CAS
// version is independent of the module draft_version.
type moduleDebugSettingsRow struct {
	ModuleID    string    `gorm:"primaryKey;column:module_id"`
	WorkspaceID string    `gorm:"column:workspace_id"`
	Version     int64     `gorm:"column:version"`
	Script      string    `gorm:"column:script"`
	Spec        string    `gorm:"column:spec"`
	UpdatedAt   time.Time `gorm:"column:updated_at"`
}

func (moduleDebugSettingsRow) TableName() string { return "module_debug_settings" }

// lockModuleRow serializes module-scoped work exactly like
// UpdateModuleDraft/DeleteModule: a no-op write on the module row is
// taken before any read so SQLite cannot BUSY_SNAPSHOT under a
// concurrent publish/update.
func (d *sqlDB) lockModuleRow(tx *gorm.DB, workspaceID, id string) error {
	return tx.Exec("UPDATE modules SET draft_version = draft_version WHERE id = ? AND workspace_id = ?", id, workspaceID).Error
}

func debugToDomain(r *moduleDebugSettingsRow) *store.ModuleDebugSettings {
	return &store.ModuleDebugSettings{
		ModuleID: r.ModuleID, WorkspaceID: r.WorkspaceID, Version: r.Version,
		Script: r.Script, Spec: json.RawMessage(append([]byte(nil), r.Spec...)),
		UpdatedAt: r.UpdatedAt.UTC(),
	}
}

func debugScaffold(m *moduleRow) *store.ModuleDebugSettings {
	return &store.ModuleDebugSettings{
		ModuleID: m.ID, WorkspaceID: m.WorkspaceID, Version: 0,
		Script: "", Spec: json.RawMessage("{}"),
	}
}

func (d *sqlDB) GetModuleDebugSettings(ctx context.Context, workspaceID, id string) (*store.ModuleDebugSettings, error) {
	var out *store.ModuleDebugSettings
	err := d.g.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		m, err := gorm.G[moduleRow](tx).Where("id = ? AND workspace_id = ?", id, workspaceID).Take(ctx)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return store.ErrNotFound
		}
		if err != nil {
			return err
		}
		row, err := gorm.G[moduleDebugSettingsRow](tx).Where("module_id = ?", id).Take(ctx)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			out = debugScaffold(&m)
			return nil
		}
		if err != nil {
			return err
		}
		out = debugToDomain(&row)
		return nil
	})
	return out, err
}

func (d *sqlDB) PutModuleDebugSettings(ctx context.Context, s *store.ModuleDebugSettings, expectedVersion int64) (bool, error) {
	if err := store.ValidateModuleDebugSettings(s); err != nil {
		return false, err
	}
	var ok bool
	err := d.g.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		ok = false
		if err := d.lockModuleRow(tx, s.WorkspaceID, s.ModuleID); err != nil {
			return err
		}
		m, err := gorm.G[moduleRow](tx).Where("id = ? AND workspace_id = ?", s.ModuleID, s.WorkspaceID).Take(ctx)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return store.ErrNotFound
		}
		if err != nil {
			return err
		}
		row, err := gorm.G[moduleDebugSettingsRow](tx).Where("module_id = ?", s.ModuleID).Take(ctx)
		exists := err == nil
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		cur := int64(0)
		if exists {
			cur = row.Version
		}
		if cur != expectedVersion {
			return nil // stale CAS
		}
		next := &moduleDebugSettingsRow{
			ModuleID: s.ModuleID, WorkspaceID: m.WorkspaceID,
			Version: cur + 1, Script: s.Script,
			Spec: string(s.Spec), UpdatedAt: s.UpdatedAt.UTC(),
		}
		if len(next.Spec) == 0 {
			next.Spec = "{}"
		}
		if err := tx.Save(next).Error; err != nil {
			return mapWriteErr(err)
		}
		ok = true
		return nil
	})
	return ok, err
}

func (d *sqlDB) GetModuleDebugSnapshot(ctx context.Context, workspaceID, id string, expectedDraft, expectedDebug int64) (*store.Module, *store.ModuleDebugSettings, error) {
	var m *store.Module
	var ds *store.ModuleDebugSettings
	err := d.g.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := d.lockModuleRow(tx, workspaceID, id); err != nil {
			return err
		}
		row, err := gorm.G[moduleRow](tx).Where("id = ? AND workspace_id = ?", id, workspaceID).Take(ctx)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return store.ErrNotFound
		}
		if err != nil {
			return err
		}
		if row.DraftVersion != expectedDraft {
			return store.ErrModuleDraftConflict
		}
		m, err = row.toDomain()
		if err != nil {
			return err
		}
		drow, err := gorm.G[moduleDebugSettingsRow](tx).Where("module_id = ?", id).Take(ctx)
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			if expectedDebug != 0 {
				return store.ErrModuleDebugConflict
			}
			ds = debugScaffold(&row)
		case err != nil:
			return err
		default:
			if drow.Version != expectedDebug {
				return store.ErrModuleDebugConflict
			}
			ds = debugToDomain(&drow)
		}
		return nil
	})
	return m, ds, err
}
