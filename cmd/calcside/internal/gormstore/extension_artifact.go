package gormstore

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"calcside/internal/store"
)

type extensionArtifactRow struct {
	Source    string    `gorm:"primaryKey;column:source"`
	Sum       string    `gorm:"primaryKey;column:sum"`
	Files     string    `gorm:"column:files"`
	CreatedAt time.Time `gorm:"column:created_at"`
}

func (extensionArtifactRow) TableName() string { return "extension_artifacts" }

func (d *sqlDB) PutExtensionArtifact(ctx context.Context, artifact *store.ExtensionArtifact) error {
	if err := artifact.Validate(); err != nil {
		return err
	}
	snapshot := artifact.Clone()
	files, err := json.Marshal(snapshot.Files)
	if err != nil {
		return store.ErrInvalidExtensionArtifact
	}
	row := extensionArtifactRow{Source: snapshot.Source, Sum: snapshot.Sum,
		Files: string(files), CreatedAt: snapshot.CreatedAt.UTC()}
	result := d.g.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
	if result.Error != nil {
		return mapWriteErr(result.Error)
	}
	if result.RowsAffected == 0 {
		existing, err := d.GetExtensionArtifact(ctx, snapshot.Source, snapshot.Sum)
		if err != nil {
			return err
		}
		old, err := json.Marshal(existing.Files)
		if err != nil || !bytes.Equal(old, files) {
			return store.ErrConflict
		}
	}
	return nil
}

func (d *sqlDB) GetExtensionArtifact(ctx context.Context, source, sum string) (*store.ExtensionArtifact, error) {
	var row extensionArtifactRow
	if err := d.g.WithContext(ctx).Where("source = ? AND sum = ?", source, sum).Take(&row).Error; err != nil {
		return nil, mapNotFound(err)
	}
	out := &store.ExtensionArtifact{Source: row.Source, Sum: row.Sum, CreatedAt: row.CreatedAt.UTC()}
	if err := json.Unmarshal([]byte(row.Files), &out.Files); err != nil {
		return nil, store.ErrInvalidExtensionArtifact
	}
	if err := out.Validate(); err != nil {
		return nil, err
	}
	return out, nil
}

type extensionVersionRow struct {
	Family         string    `gorm:"primaryKey;column:family"`
	Version        string    `gorm:"primaryKey;column:version"`
	Sum            string    `gorm:"column:sum"`
	ArtifactSource string    `gorm:"column:artifact_source"`
	CreatedAt      time.Time `gorm:"column:created_at"`
}

func (extensionVersionRow) TableName() string { return "extension_versions" }

// BindExtensionVersion persists artifact bytes and the
// (family,version)->sum binding in one transaction: insert the
// artifact (idempotent), insert the binding (on conflict keep the
// original row), then read the binding back and compare sums — a
// different sum is ErrConflict and rolls the whole transaction back.
func (d *sqlDB) BindExtensionVersion(ctx context.Context, v *store.ExtensionVersion, artifact *store.ExtensionArtifact) error {
	if err := v.Validate(); err != nil {
		return err
	}
	if artifact != nil && (artifact.Source != v.ArtifactSource || artifact.Sum != v.Sum) {
		return store.ErrInvalidExtensionArtifact
	}
	err := d.g.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		bound := &sqlDB{g: tx}
		// Writes come before reads so SQLite never fails a deferred
		// read→write upgrade under a concurrent capture.
		if artifact != nil {
			if err := bound.PutExtensionArtifact(ctx, artifact); err != nil {
				return err
			}
		}
		row := extensionVersionRow{Family: v.Family, Version: v.Version, Sum: v.Sum,
			ArtifactSource: v.ArtifactSource, CreatedAt: v.CreatedAt.UTC()}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return mapWriteErr(err)
		}
		var got extensionVersionRow
		if err := tx.Where("family = ? AND version = ?", v.Family, v.Version).Take(&got).Error; err != nil {
			return mapNotFound(err)
		}
		if got.Sum != v.Sum {
			return store.ErrConflict
		}
		return nil
	})
	if err != nil && artifact == nil {
		// The foreign key rejects bindings to a missing artifact;
		// surface that as ErrNotFound (checked outside the failed
		// transaction — Postgres aborts all further work inside it).
		if _, gerr := d.GetExtensionArtifact(ctx, v.ArtifactSource, v.Sum); gerr != nil {
			return gerr
		}
	}
	return err
}

func (d *sqlDB) GetExtensionVersion(ctx context.Context, family, version string) (*store.ExtensionVersion, error) {
	var row extensionVersionRow
	if err := d.g.WithContext(ctx).Where("family = ? AND version = ?", family, version).Take(&row).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return &store.ExtensionVersion{Family: row.Family, Version: row.Version, Sum: row.Sum,
		ArtifactSource: row.ArtifactSource, CreatedAt: row.CreatedAt.UTC()}, nil
}

func (d *sqlDB) ListExtensionVersions(ctx context.Context) ([]store.ExtensionVersion, error) {
	var rows []extensionVersionRow
	if err := d.g.WithContext(ctx).Order("family, version").Find(&rows).Error; err != nil {
		return nil, mapNotFound(err)
	}
	out := make([]store.ExtensionVersion, 0, len(rows))
	for _, row := range rows {
		out = append(out, store.ExtensionVersion{Family: row.Family, Version: row.Version,
			Sum: row.Sum, ArtifactSource: row.ArtifactSource, CreatedAt: row.CreatedAt.UTC()})
	}
	return out, nil
}
