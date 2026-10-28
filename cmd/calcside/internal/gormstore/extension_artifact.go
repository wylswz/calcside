package gormstore

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

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
