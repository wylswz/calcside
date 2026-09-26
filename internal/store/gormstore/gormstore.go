// Package gormstore implements store.Store with GORM. The sqlite
// dialector (pure Go, via modernc) is the only registered driver today;
// adding postgres is a matter of a new dialector case in open.
//
// Schema is created via AutoMigrate on open. The implementation uses
// dedicated row structs so the domain types in internal/store stay free
// of ORM tags. Errors are mapped: gorm.ErrRecordNotFound → ErrNotFound,
// unique violations → ErrConflict.
package gormstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	glogger "gorm.io/gorm/logger"

	"calcside/internal/store"
	"calcside/internal/types"

	gormsqlite "github.com/glebarez/sqlite"
)

func init() {
	store.RegisterDriver(types.DriverSQLite, func(ctx context.Context, dsn string) (store.Store, error) {
		return open(ctx, types.DriverSQLite, dsn)
	})
}

// sqlDB wraps *gorm.DB and implements store.Store. Named sqlDB to keep
// the diff with the previous driver obvious — it is not tied to SQLite
// beyond the dialector chosen in open.
type sqlDB struct {
	g *gorm.DB
}

func open(_ context.Context, driver types.StoreDriver, dsn string) (store.Store, error) {
	var dialector gorm.Dialector
	switch driver {
	case types.DriverSQLite:
		// modernc.org/sqlite conn string: `_pragma=` options.
		conn := dsn
		if strings.Contains(dsn, "?") {
			conn += "&"
		} else {
			conn += "?"
		}
		conn += "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
		dialector = gormsqlite.Open(conn)
	default:
		return nil, fmt.Errorf("gormstore: unsupported driver %q", driver)
	}
	g, err := gorm.Open(dialector, &gorm.Config{
		TranslateError: true,
		Logger:         glogger.Default.LogMode(glogger.Warn),
	})
	if err != nil {
		return nil, fmt.Errorf("gormstore: %w", err)
	}
	if err := g.AutoMigrate(
		&userRow{}, &keyRow{}, &sessionRow{}, &instanceRow{},
		&execRow{}, &auditRow{}, &policyRow{}, &secretRow{},
	); err != nil {
		return nil, fmt.Errorf("gormstore migrate: %w", err)
	}
	return &sqlDB{g: g}, nil
}

func (d *sqlDB) Close() error {
	db, err := d.g.DB()
	if err != nil {
		return err
	}
	return db.Close()
}

func mapNotFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return store.ErrNotFound
	}
	return err
}

func isConflict(err error) bool {
	return errors.Is(err, gorm.ErrDuplicatedKey) ||
		(err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed"))
}

func mapWriteErr(err error) error {
	if isConflict(err) {
		return store.ErrConflict
	}
	return err
}

// --- row structs ---

type userRow struct {
	ID          string `gorm:"primaryKey"`
	Email       string `gorm:"uniqueIndex"`
	Name        string
	GoogleSub   string
	CreatedAt   time.Time
	LastLoginAt time.Time
}

func (userRow) TableName() string { return "users" }

func (u userRow) toStore() *store.User {
	return &store.User{
		ID: u.ID, Email: u.Email, Name: u.Name, GoogleSub: u.GoogleSub,
		CreatedAt: u.CreatedAt.UTC(), LastLoginAt: u.LastLoginAt.UTC(),
	}
}

type keyRow struct {
	ID         string `gorm:"primaryKey"`
	UserID     string
	Name       string
	Prefix     string
	Hash       string `gorm:"uniqueIndex"`
	CreatedAt  time.Time
	LastUsedAt *time.Time
	ExpiresAt  *time.Time
	RevokedAt  *time.Time
}

func (keyRow) TableName() string { return "api_keys" }

func (k keyRow) toStore() *store.APIKey {
	out := &store.APIKey{
		ID: k.ID, UserID: k.UserID, Name: k.Name, Prefix: k.Prefix, Hash: k.Hash,
		CreatedAt: k.CreatedAt.UTC(), LastUsedAt: k.LastUsedAt,
		ExpiresAt: k.ExpiresAt, RevokedAt: k.RevokedAt,
	}
	utcPtr := func(t *time.Time) *time.Time {
		if t == nil {
			return nil
		}
		u := t.UTC()
		return &u
	}
	out.LastUsedAt, out.ExpiresAt, out.RevokedAt =
		utcPtr(k.LastUsedAt), utcPtr(k.ExpiresAt), utcPtr(k.RevokedAt)
	return out
}

type sessionRow struct {
	Hash      string `gorm:"primaryKey"`
	UserID    string
	CreatedAt time.Time
	ExpiresAt time.Time
}

func (sessionRow) TableName() string { return "sessions" }

type instanceRow struct {
	ID           string `gorm:"primaryKey"`
	UserID       string `gorm:"index:idx_instances_user_status"`
	Spec         []byte
	Labels       map[string]string `gorm:"serializer:json"`
	Status       string            `gorm:"index:idx_instances_user_status"`
	CreatedAt    time.Time
	LastActiveAt time.Time
	ExpiresAt    time.Time
	EndedAt      *time.Time
}

func (instanceRow) TableName() string { return "instances" }

func (r instanceRow) toStore() *store.Instance {
	in := &store.Instance{
		ID: r.ID, UserID: r.UserID, Spec: r.Spec, Labels: r.Labels,
		Status:       types.InstanceStatus(r.Status),
		CreatedAt:    r.CreatedAt.UTC(),
		LastActiveAt: r.LastActiveAt.UTC(),
		ExpiresAt:    r.ExpiresAt.UTC(),
	}
	if in.Labels == nil {
		in.Labels = map[string]string{}
	}
	if r.EndedAt != nil {
		e := r.EndedAt.UTC()
		in.EndedAt = &e
	}
	return in
}

type execRow struct {
	ID          string `gorm:"primaryKey"`
	InstanceID  string `gorm:"index:idx_executions_instance_created"`
	UserID      string
	CodeSHA256  string
	CodeSnippet string
	Code        string
	Status      string
	ErrorType   string
	DurationMs  int64
	Steps       int64
	OutputBytes int64
	CreatedAt   time.Time `gorm:"index:idx_executions_instance_created"`
}

func (execRow) TableName() string { return "executions" }

func (r execRow) toStore() *store.Execution {
	return &store.Execution{
		ID: r.ID, InstanceID: r.InstanceID, UserID: r.UserID,
		CodeSHA256: r.CodeSHA256, CodeSnippet: r.CodeSnippet, Code: r.Code,
		Status:      types.ExecStatus(r.Status),
		ErrorType:   types.ExecErrorType(r.ErrorType),
		DurationMs:  r.DurationMs,
		Steps:       uint64(r.Steps),
		OutputBytes: r.OutputBytes,
		CreatedAt:   r.CreatedAt.UTC(),
	}
}

type auditRow struct {
	ID         string    `gorm:"primaryKey"`
	Ts         time.Time `gorm:"index:idx_audit_user_ts"`
	UserID     string    `gorm:"index:idx_audit_user_ts"`
	InstanceID string    `gorm:"index"`
	ExecID     string
	Capability string
	Op         string
	Args       string
	Phase      string
	Decision   string
	Reason     string
	Error      string
	DurationMs int64
}

func (auditRow) TableName() string { return "audit_events" }

func (r auditRow) toStore() *store.AuditEvent {
	return &store.AuditEvent{
		ID: r.ID, Ts: r.Ts.UTC(), UserID: r.UserID, InstanceID: r.InstanceID,
		ExecID:     r.ExecID,
		Capability: types.CapabilityName(r.Capability), Op: types.Op(r.Op),
		Args: r.Args, Phase: types.Phase(r.Phase),
		Decision: types.Decision(r.Decision), Reason: r.Reason,
		Error: r.Error, DurationMs: r.DurationMs,
	}
}

type policyRow struct {
	ID        string `gorm:"primaryKey"`
	UserID    string
	Name      string
	Rego      string
	Enabled   bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (policyRow) TableName() string { return "policies" }

func (r policyRow) toStore() *store.Policy {
	return &store.Policy{
		ID: r.ID, UserID: r.UserID, Name: r.Name, Rego: r.Rego,
		Enabled: r.Enabled, CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
	}
}

type secretRow struct {
	ID             string `gorm:"primaryKey"`
	UserID         string `gorm:"uniqueIndex:idx_secrets_user_name"`
	Name           string `gorm:"uniqueIndex:idx_secrets_user_name"`
	Ciphertext     []byte
	AllowedDomains []string `gorm:"serializer:json"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (secretRow) TableName() string { return "secrets" }

func (r secretRow) toStore() *store.Secret {
	s := &store.Secret{
		ID: r.ID, UserID: r.UserID, Name: r.Name, Ciphertext: r.Ciphertext,
		AllowedDomains: r.AllowedDomains,
		CreatedAt:      r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
	}
	if s.AllowedDomains == nil {
		s.AllowedDomains = []string{}
	}
	return s
}

// --- users ---

func (d *sqlDB) UpsertUserByEmail(ctx context.Context, email, name, googleSub string) (*store.User, error) {
	now := time.Now().UTC()
	err := d.g.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row userRow
		rerr := tx.Where("email = ?", email).Take(&row).Error
		switch {
		case errors.Is(rerr, gorm.ErrRecordNotFound):
			row = userRow{
				ID: store.NewID(store.PrefixUser), Email: email,
				Name: name, GoogleSub: googleSub,
				CreatedAt: now, LastLoginAt: now,
			}
			return tx.Create(&row).Error
		case rerr != nil:
			return rerr
		default:
			if name != "" {
				row.Name = name
			}
			if googleSub != "" {
				row.GoogleSub = googleSub
			}
			row.LastLoginAt = now
			return tx.Save(&row).Error
		}
	})
	if err != nil {
		return nil, err
	}
	var row userRow
	if err := d.g.WithContext(ctx).Where("email = ?", email).Take(&row).Error; err != nil {
		return nil, err
	}
	return row.toStore(), nil
}

func (d *sqlDB) GetUser(ctx context.Context, id string) (*store.User, error) {
	var row userRow
	err := d.g.WithContext(ctx).Where("id = ?", id).Take(&row).Error
	if err != nil {
		return nil, mapNotFound(err)
	}
	return row.toStore(), nil
}

// --- api keys ---

func (d *sqlDB) CreateAPIKey(ctx context.Context, k *store.APIKey) error {
	if k.ID == "" {
		k.ID = store.NewID(store.PrefixAPIKey)
	}
	if k.CreatedAt.IsZero() {
		k.CreatedAt = time.Now().UTC()
	}
	row := keyRow{
		ID: k.ID, UserID: k.UserID, Name: k.Name, Prefix: k.Prefix, Hash: k.Hash,
		CreatedAt: k.CreatedAt.UTC(), LastUsedAt: k.LastUsedAt,
		ExpiresAt: k.ExpiresAt, RevokedAt: k.RevokedAt,
	}
	return mapWriteErr(d.g.WithContext(ctx).Create(&row).Error)
}

func (d *sqlDB) ListAPIKeys(ctx context.Context, userID string) ([]*store.APIKey, error) {
	var rows []keyRow
	err := d.g.WithContext(ctx).Where("user_id = ?", userID).Order("created_at").Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]*store.APIKey, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toStore())
	}
	return out, nil
}

func (d *sqlDB) GetAPIKeyByHash(ctx context.Context, hash string) (*store.APIKey, error) {
	var row keyRow
	err := d.g.WithContext(ctx).Where("hash = ?", hash).Take(&row).Error
	if err != nil {
		return nil, mapNotFound(err)
	}
	return row.toStore(), nil
}

func (d *sqlDB) RevokeAPIKey(ctx context.Context, userID, id string) error {
	res := d.g.WithContext(ctx).Model(&keyRow{}).
		Where("id = ? AND user_id = ? AND revoked_at IS NULL", id, userID).
		Update("revoked_at", time.Now().UTC())
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (d *sqlDB) TouchAPIKey(ctx context.Context, id string, at time.Time) error {
	return d.g.WithContext(ctx).Model(&keyRow{}).Where("id = ?", id).
		Update("last_used_at", at.UTC()).Error
}

// --- sessions ---

func (d *sqlDB) CreateSession(ctx context.Context, s *store.Session) error {
	return d.g.WithContext(ctx).Create(&sessionRow{
		Hash: s.Hash, UserID: s.UserID,
		CreatedAt: s.CreatedAt.UTC(), ExpiresAt: s.ExpiresAt.UTC(),
	}).Error
}

func (d *sqlDB) GetSession(ctx context.Context, hash string) (*store.Session, error) {
	var row sessionRow
	err := d.g.WithContext(ctx).Where("hash = ?", hash).Take(&row).Error
	if err != nil {
		return nil, mapNotFound(err)
	}
	return &store.Session{
		Hash: row.Hash, UserID: row.UserID,
		CreatedAt: row.CreatedAt.UTC(), ExpiresAt: row.ExpiresAt.UTC(),
	}, nil
}

func (d *sqlDB) DeleteSession(ctx context.Context, hash string) error {
	return d.g.WithContext(ctx).Where("hash = ?", hash).Delete(&sessionRow{}).Error
}

func (d *sqlDB) DeleteExpiredSessions(ctx context.Context) (int, error) {
	res := d.g.WithContext(ctx).Where("expires_at < ?", time.Now().UTC()).Delete(&sessionRow{})
	return int(res.RowsAffected), res.Error
}

// --- instances ---

func (d *sqlDB) CreateInstance(ctx context.Context, in *store.Instance) error {
	if !in.Status.Valid() {
		return fmt.Errorf("gormstore: invalid instance status %q", in.Status)
	}
	if in.ID == "" {
		in.ID = store.NewID(store.PrefixInstance)
	}
	labels := in.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	return d.g.WithContext(ctx).Create(&instanceRow{
		ID: in.ID, UserID: in.UserID, Spec: []byte(in.Spec), Labels: labels,
		Status:    string(in.Status),
		CreatedAt: in.CreatedAt.UTC(), LastActiveAt: in.LastActiveAt.UTC(),
		ExpiresAt: in.ExpiresAt.UTC(), EndedAt: in.EndedAt,
	}).Error
}

func (d *sqlDB) GetInstance(ctx context.Context, id string) (*store.Instance, error) {
	var row instanceRow
	err := d.g.WithContext(ctx).Where("id = ?", id).Take(&row).Error
	if err != nil {
		return nil, mapNotFound(err)
	}
	return row.toStore(), nil
}

func (d *sqlDB) ListInstances(ctx context.Context, userID string, status types.InstanceStatus) ([]*store.Instance, error) {
	tx := d.g.WithContext(ctx).Where("user_id = ?", userID)
	if status != "" {
		tx = tx.Where("status = ?", string(status))
	}
	var rows []instanceRow
	if err := tx.Order("created_at DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*store.Instance, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toStore())
	}
	return out, nil
}

func (d *sqlDB) UpdateInstance(ctx context.Context, in *store.Instance) error {
	if !in.Status.Valid() {
		return fmt.Errorf("gormstore: invalid instance status %q", in.Status)
	}
	labels := in.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	lj, err := json.Marshal(labels)
	if err != nil {
		return err
	}
	return d.g.WithContext(ctx).Model(&instanceRow{}).Where("id = ?", in.ID).Updates(map[string]any{
		"labels":         string(lj),
		"status":         string(in.Status),
		"last_active_at": in.LastActiveAt.UTC(),
		"expires_at":     in.ExpiresAt.UTC(),
		"ended_at":       in.EndedAt,
	}).Error
}

func (d *sqlDB) MarkRunningAsLost(ctx context.Context) (int, error) {
	res := d.g.WithContext(ctx).Model(&instanceRow{}).
		Where("status = ?", string(types.InstanceRunning)).
		Updates(map[string]any{
			"status":   string(types.InstanceLost),
			"ended_at": time.Now().UTC(),
		})
	return int(res.RowsAffected), res.Error
}

// --- executions ---

func (d *sqlDB) CreateExecution(ctx context.Context, e *store.Execution) error {
	if !e.Status.Valid() || !e.ErrorType.Valid() {
		return fmt.Errorf("gormstore: invalid exec status/error type %q/%q", e.Status, e.ErrorType)
	}
	if e.ID == "" {
		e.ID = store.NewID(store.PrefixExecution)
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	return d.g.WithContext(ctx).Create(&execRow{
		ID: e.ID, InstanceID: e.InstanceID, UserID: e.UserID,
		CodeSHA256: e.CodeSHA256, CodeSnippet: e.CodeSnippet, Code: e.Code,
		Status: string(e.Status), ErrorType: string(e.ErrorType),
		DurationMs: e.DurationMs, Steps: int64(e.Steps),
		OutputBytes: e.OutputBytes, CreatedAt: e.CreatedAt.UTC(),
	}).Error
}

func (d *sqlDB) GetExecution(ctx context.Context, id string) (*store.Execution, error) {
	var row execRow
	err := d.g.WithContext(ctx).Where("id = ?", id).Take(&row).Error
	if err != nil {
		return nil, mapNotFound(err)
	}
	return row.toStore(), nil
}

func (d *sqlDB) ListExecutions(ctx context.Context, instanceID string, limit int) ([]*store.Execution, error) {
	if limit <= 0 {
		limit = 100
	}
	var rows []execRow
	err := d.g.WithContext(ctx).Where("instance_id = ?", instanceID).
		Order("created_at DESC").Limit(limit).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]*store.Execution, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toStore())
	}
	return out, nil
}

// --- audit ---

func (d *sqlDB) InsertAuditEvents(ctx context.Context, evs []store.AuditEvent) error {
	if len(evs) == 0 {
		return nil
	}
	return d.g.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, e := range evs {
			if !e.Decision.Valid() || !e.Phase.Valid() || !e.Capability.Valid() {
				return fmt.Errorf("gormstore: invalid audit decision/phase/capability %q/%q/%q",
					e.Decision, e.Phase, e.Capability)
			}
			if e.ID == "" {
				e.ID = store.NewID(store.PrefixAudit)
			}
			row := auditRow{
				ID: e.ID, Ts: e.Ts.UTC(), UserID: e.UserID,
				InstanceID: e.InstanceID, ExecID: e.ExecID,
				Capability: string(e.Capability), Op: string(e.Op),
				Args: e.Args, Phase: string(e.Phase), Decision: string(e.Decision),
				Reason: e.Reason, Error: e.Error, DurationMs: e.DurationMs,
			}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (d *sqlDB) ListAuditEvents(ctx context.Context, f store.AuditFilter) ([]*store.AuditEvent, error) {
	tx := d.g.WithContext(ctx).Model(&auditRow{})
	if f.UserID != "" {
		tx = tx.Where("user_id = ?", f.UserID)
	}
	if f.InstanceID != "" {
		tx = tx.Where("instance_id = ?", f.InstanceID)
	}
	if f.ExecID != "" {
		tx = tx.Where("exec_id = ?", f.ExecID)
	}
	if f.Before != nil {
		tx = tx.Where("ts < ?", f.Before.UTC())
	}
	tx = tx.Order("ts DESC")
	if f.Limit > 0 {
		tx = tx.Limit(f.Limit)
	}
	var rows []auditRow
	if err := tx.Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*store.AuditEvent, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toStore())
	}
	return out, nil
}

// --- policies ---

func (d *sqlDB) CreatePolicy(ctx context.Context, p *store.Policy) error {
	if p.ID == "" {
		p.ID = store.NewID(store.PrefixPolicy)
	}
	now := time.Now().UTC()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	p.UpdatedAt = now
	return d.g.WithContext(ctx).Create(&policyRow{
		ID: p.ID, UserID: p.UserID, Name: p.Name, Rego: p.Rego,
		Enabled: p.Enabled, CreatedAt: p.CreatedAt.UTC(), UpdatedAt: p.UpdatedAt.UTC(),
	}).Error
}

func (d *sqlDB) GetPolicy(ctx context.Context, id string) (*store.Policy, error) {
	var row policyRow
	err := d.g.WithContext(ctx).Where("id = ?", id).Take(&row).Error
	if err != nil {
		return nil, mapNotFound(err)
	}
	return row.toStore(), nil
}

func (d *sqlDB) ListPolicies(ctx context.Context, userID string) ([]*store.Policy, error) {
	var rows []policyRow
	err := d.g.WithContext(ctx).Where("user_id = ?", userID).Order("created_at").Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]*store.Policy, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toStore())
	}
	return out, nil
}

func (d *sqlDB) UpdatePolicy(ctx context.Context, p *store.Policy) error {
	p.UpdatedAt = time.Now().UTC()
	res := d.g.WithContext(ctx).Model(&policyRow{}).Where("id = ?", p.ID).Updates(map[string]any{
		"name": p.Name, "rego": p.Rego, "enabled": p.Enabled, "updated_at": p.UpdatedAt,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (d *sqlDB) DeletePolicy(ctx context.Context, id string) error {
	return d.g.WithContext(ctx).Where("id = ?", id).Delete(&policyRow{}).Error
}

// --- secrets ---

func (d *sqlDB) CreateSecret(ctx context.Context, s *store.Secret) error {
	if s.ID == "" {
		s.ID = store.NewID(store.PrefixSecret)
	}
	now := time.Now().UTC()
	if s.CreatedAt.IsZero() {
		s.CreatedAt = now
	}
	s.UpdatedAt = now
	domains := s.AllowedDomains
	if domains == nil {
		domains = []string{}
	}
	err := d.g.WithContext(ctx).Create(&secretRow{
		ID: s.ID, UserID: s.UserID, Name: s.Name, Ciphertext: s.Ciphertext,
		AllowedDomains: domains, CreatedAt: s.CreatedAt.UTC(), UpdatedAt: s.UpdatedAt.UTC(),
	}).Error
	return mapWriteErr(err)
}

func (d *sqlDB) GetSecret(ctx context.Context, id string) (*store.Secret, error) {
	var row secretRow
	err := d.g.WithContext(ctx).Where("id = ?", id).Take(&row).Error
	if err != nil {
		return nil, mapNotFound(err)
	}
	return row.toStore(), nil
}

func (d *sqlDB) GetSecretByName(ctx context.Context, userID, name string) (*store.Secret, error) {
	var row secretRow
	err := d.g.WithContext(ctx).Where("user_id = ? AND name = ?", userID, name).Take(&row).Error
	if err != nil {
		return nil, mapNotFound(err)
	}
	return row.toStore(), nil
}

func (d *sqlDB) ListSecrets(ctx context.Context, userID string) ([]*store.Secret, error) {
	var rows []secretRow
	err := d.g.WithContext(ctx).Where("user_id = ?", userID).Order("name").Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]*store.Secret, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toStore())
	}
	return out, nil
}

func (d *sqlDB) UpdateSecret(ctx context.Context, s *store.Secret) error {
	s.UpdatedAt = time.Now().UTC()
	domains := s.AllowedDomains
	if domains == nil {
		domains = []string{}
	}
	dj, err := json.Marshal(domains)
	if err != nil {
		return err
	}
	res := d.g.WithContext(ctx).Model(&secretRow{}).Where("id = ?", s.ID).Updates(map[string]any{
		"ciphertext": s.Ciphertext, "allowed_domains": string(dj), "updated_at": s.UpdatedAt,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (d *sqlDB) DeleteSecret(ctx context.Context, id string) error {
	return d.g.WithContext(ctx).Where("id = ?", id).Delete(&secretRow{}).Error
}
