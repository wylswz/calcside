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

type applicationRow struct {
	ID                   string     `gorm:"primaryKey"`
	WorkspaceID          string     `gorm:"not null"`
	UserID               string     `gorm:"not null"`
	Name                 string     `gorm:"not null"`
	Code                 string     `gorm:"not null"`
	Spec                 string     `gorm:"not null"`
	InputSchema          *string    `gorm:""`
	OutputSchema         *string    `gorm:""`
	Version              int64      `gorm:"not null"`
	PublishedVersion     int64      `gorm:"not null"`
	PersistentInstanceID string     `gorm:"not null"`
	CreatedAt            time.Time  `gorm:"not null"`
	UpdatedAt            time.Time  `gorm:"not null"`
	DeletedAt            *time.Time `gorm:""`
}

func (applicationRow) TableName() string { return "applications" }

func (r *applicationRow) toDomain() *store.Application {
	a := &store.Application{
		ID: r.ID, WorkspaceID: r.WorkspaceID, UserID: r.UserID,
		Name: r.Name, Code: r.Code, Spec: json.RawMessage(r.Spec),
		Version: r.Version, PublishedVersion: r.PublishedVersion,
		PersistentInstanceID: r.PersistentInstanceID,
		CreatedAt:            r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
		DeletedAt: r.DeletedAt,
	}
	if r.InputSchema != nil {
		a.InputSchema = json.RawMessage(*r.InputSchema)
	}
	if r.OutputSchema != nil {
		a.OutputSchema = json.RawMessage(*r.OutputSchema)
	}
	return a
}

func appToRow(a *store.Application) *applicationRow {
	r := &applicationRow{
		ID: a.ID, WorkspaceID: a.WorkspaceID, UserID: a.UserID,
		Name: a.Name, Code: a.Code, Spec: string(a.Spec),
		Version: a.Version, PublishedVersion: a.PublishedVersion,
		PersistentInstanceID: a.PersistentInstanceID,
		CreatedAt:            a.CreatedAt.UTC(), UpdatedAt: a.UpdatedAt.UTC(),
		DeletedAt: a.DeletedAt,
	}
	if a.InputSchema != nil {
		s := string(a.InputSchema)
		r.InputSchema = &s
	}
	if a.OutputSchema != nil {
		s := string(a.OutputSchema)
		r.OutputSchema = &s
	}
	return r
}

func (d *sqlDB) CreateApplication(ctx context.Context, a *store.Application) error {
	if err := store.ValidateApplication(a); err != nil {
		return err
	}
	if a.ID == "" || a.Version != 1 || a.PublishedVersion != 0 ||
		a.PersistentInstanceID != "" || a.DeletedAt != nil {
		return store.ErrInvalidServingRecord
	}
	return mapWriteErr(gorm.G[applicationRow](d.g).Create(ctx, appToRow(a)))
}

func (d *sqlDB) GetApplication(ctx context.Context, id string) (*store.Application, error) {
	row, err := gorm.G[applicationRow](d.g).
		Where("id = ? AND deleted_at IS NULL", id).Take(ctx)
	if err != nil {
		return nil, mapNotFound(err)
	}
	return row.toDomain(), nil
}

func (d *sqlDB) ListApplications(ctx context.Context, f store.ApplicationFilter) ([]*store.Application, error) {
	if f.WorkspaceID == "" {
		return nil, store.ErrInvalidServingRecord
	}
	limit := f.Limit
	if limit < 0 || limit > 200 {
		return nil, store.ErrInvalidServingRecord
	}
	if limit == 0 {
		limit = 200
	}
	q := gorm.G[applicationRow](d.g).
		Where("workspace_id = ? AND deleted_at IS NULL", f.WorkspaceID)
	if f.UserID != "" {
		q = q.Where("user_id = ?", f.UserID)
	}
	if f.AfterID != "" {
		q = q.Where("id > ?", f.AfterID)
	}
	rows, err := q.Order("id").Limit(limit).Find(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*store.Application, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toDomain())
	}
	return out, nil
}

func (d *sqlDB) CountApplications(ctx context.Context, workspaceID, userID string) (int64, error) {
	n, err := gorm.G[applicationRow](d.g).
		Where("workspace_id = ? AND user_id = ? AND deleted_at IS NULL", workspaceID, userID).
		Count(ctx, "id")
	return n, err
}

func (d *sqlDB) UpdateApplicationDraft(ctx context.Context, a *store.Application, expectedVersion int64) (bool, error) {
	if err := store.ValidateApplication(a); err != nil {
		return false, err
	}
	var ok bool
	err := d.g.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		ok = false
		if err := tx.Exec("UPDATE applications SET version = version WHERE id = ?", a.ID).Error; err != nil {
			return err
		}
		row, err := gorm.G[applicationRow](tx, clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND workspace_id = ? AND deleted_at IS NULL", a.ID, a.WorkspaceID).Take(ctx)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if row.Version != expectedVersion {
			return nil // stale CAS — no write
		}
		next := appToRow(a)
		next.ID = row.ID
		next.WorkspaceID = row.WorkspaceID
		next.UserID = row.UserID
		next.Version = row.Version + 1
		next.PublishedVersion = row.PublishedVersion
		next.PersistentInstanceID = row.PersistentInstanceID
		next.CreatedAt = row.CreatedAt
		next.DeletedAt = nil
		next.UpdatedAt = a.UpdatedAt.UTC()
		if err := tx.Save(next).Error; err != nil {
			return err
		}
		ok = true
		return nil
	})
	return ok, err
}

func (d *sqlDB) PublishApplication(ctx context.Context, workspaceID, id string, expectedVersion int64, release *store.ServingRelease, now time.Time) (*store.PersistentInstance, error) {
	if release == nil {
		return nil, store.ErrInvalidServingRecord
	}
	var pin *store.PersistentInstance
	err := d.g.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		pin = nil
		// Lock the app row.
		if err := tx.Exec("UPDATE applications SET version = version WHERE id = ?", id).Error; err != nil {
			return err
		}
		row, err := gorm.G[applicationRow](tx, clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND workspace_id = ? AND deleted_at IS NULL", id, workspaceID).Take(ctx)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return store.ErrNotFound
		}
		if err != nil {
			return err
		}
		if row.Version != expectedVersion {
			return store.ErrConflict
		}
		// Release owner/workspace must match the app's.
		if release.WorkspaceID != workspaceID || release.UserID != row.UserID {
			return store.ErrInvalidServingRecord
		}
		// Same-version republish returns the existing pointer — never
		// republish, never bump generation.
		if row.PublishedVersion == expectedVersion && row.PersistentInstanceID != "" {
			cur, err := gorm.G[persistentInstanceRow](tx).
				Where("id = ? AND workspace_id = ?", row.PersistentInstanceID, workspaceID).Take(ctx)
			if err != nil {
				return err
			}
			pin = cur.toDomain()
			return nil
		}
		// Insert the release in this tx — dedup by content hash.
		check, err := store.NewServingRelease(release.WorkspaceID, release.UserID, release.Spec, release.Policies, release.Program, release.CreatedAt)
		if err != nil {
			return err
		}
		if check.ID != release.ID {
			return store.ErrInvalidServingRecord
		}
		relRow, err := releaseToRow(check)
		if err != nil {
			return err
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(relRow).Error; err != nil {
			return err
		}
		// Persistent instance: create on first publish, CAS-update on
		// subsequent. Never reset last_epoch or shorten old leases.
		if row.PersistentInstanceID == "" {
			pin = &store.PersistentInstance{
				WorkspaceID: workspaceID, UserID: row.UserID,
				ReleaseID: release.ID, Enabled: true,
				Labels:    map[string]string{"name": row.Name, "application_id": id},
				CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
			}
			pin.ID = store.NewID(store.PrefixPersistentInstance)
			pin.Generation = 1
			prow, perr := instanceToRow(pin)
			if perr != nil {
				return perr
			}
			if err := tx.Create(prow).Error; err != nil {
				return mapWriteErr(err)
			}
		} else {
			cur, err := gorm.G[persistentInstanceRow](tx, clause.Locking{Strength: "UPDATE"}).
				Where("id = ? AND workspace_id = ?", row.PersistentInstanceID, workspaceID).Take(ctx)
			if err != nil {
				return err
			}
			pin = cur.toDomain()
			changed := cur.ReleaseID != release.ID || !cur.Enabled
			updates := map[string]any{
				"release_id": release.ID, "enabled": true,
				"labels":     mustLabels(map[string]string{"name": row.Name, "application_id": id}),
				"updated_at": now.UTC(),
			}
			if changed {
				updates["generation"] = cur.Generation + 1
				updates["active_replica_id"] = ""
				updates["consecutive_failures"] = 0
				updates["retry_after"] = nil
			}
			if err := tx.Model(&persistentInstanceRow{}).Where("id = ?", cur.ID).Updates(updates).Error; err != nil {
				return err
			}
			if changed {
				pin.Generation = cur.Generation + 1
				pin.ActiveReplicaID = ""
			}
			pin.ReleaseID = release.ID
			pin.Enabled = true
			pin.Labels = map[string]string{"name": row.Name, "application_id": id}
			pin.UpdatedAt = now.UTC()
			if changed {
				pin.ConsecutiveFailures = 0
				pin.RetryAfter = nil
			}
		}
		if err := tx.Model(&applicationRow{}).Where("id = ?", id).
			Updates(map[string]any{
				"published_version":      expectedVersion,
				"persistent_instance_id": pin.ID,
				"updated_at":             now.UTC(),
			}).Error; err != nil {
			return err
		}
		return nil
	})
	return pin, err
}

func (d *sqlDB) DeleteApplication(ctx context.Context, workspaceID, id string, expectedVersion int64, now time.Time) (bool, error) {
	var ok bool
	err := d.g.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		ok = false
		if err := tx.Exec("UPDATE applications SET version = version WHERE id = ?", id).Error; err != nil {
			return err
		}
		row, err := gorm.G[applicationRow](tx, clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND workspace_id = ? AND deleted_at IS NULL", id, workspaceID).Take(ctx)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if row.Version != expectedVersion {
			return nil // stale CAS
		}
		// Disable + bump the persistent instance's generation under the
		// same tx so replicas drain through the scheduler, never by
		// shortening leases.
		if row.PersistentInstanceID != "" {
			cur, err := gorm.G[persistentInstanceRow](tx, clause.Locking{Strength: "UPDATE"}).
				Where("id = ? AND workspace_id = ?", row.PersistentInstanceID, workspaceID).Take(ctx)
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if err == nil {
				if err := tx.Model(&persistentInstanceRow{}).Where("id = ?", cur.ID).
					Updates(map[string]any{
						"enabled": false, "generation": cur.Generation + 1,
						"active_replica_id": "", "consecutive_failures": 0,
						"retry_after": nil, "updated_at": now.UTC(),
					}).Error; err != nil {
					return err
				}
			}
		}
		if err := tx.Model(&applicationRow{}).Where("id = ?", id).
			Updates(map[string]any{"deleted_at": now.UTC(), "updated_at": now.UTC()}).Error; err != nil {
			return err
		}
		ok = true
		return nil
	})
	return ok, err
}

func mustLabels(m map[string]string) string {
	b, _ := json.Marshal(m)
	return string(b)
}
