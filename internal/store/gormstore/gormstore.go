// Package gormstore implements store.Store with GORM for SQLite (pure
// Go, via modernc) and PostgreSQL (pgx).
//
// Schema is managed by hand-written, versioned Atlas migrations
// (migrations/<dialect>), applied separately by the Atlas CLI. The
// implementation uses dedicated row structs so the domain types in
// internal/store stay free of ORM tags. Errors are mapped: gorm.ErrRecordNotFound → ErrNotFound,
// unique violations → ErrConflict.
package gormstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	glogger "gorm.io/gorm/logger"

	"calcside/internal/store"
	"calcside/internal/types"

	gormsqlite "github.com/glebarez/sqlite"
	gormpostgres "gorm.io/driver/postgres"
)

func init() {
	for _, d := range []types.StoreDriver{types.DriverSQLite, types.DriverPostgres} {
		store.RegisterDriver(d, func(ctx context.Context, dsn string) (store.Store, error) {
			return open(ctx, d, dsn)
		})
	}
}

// sqlDB wraps *gorm.DB and implements store.Store; it is dialect
// neutral beyond the dialector chosen in open.
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
	case types.DriverPostgres:
		dialector = gormpostgres.Open(dsn)
	default:
		return nil, fmt.Errorf("gormstore: unsupported driver %q", driver)
	}
	g, err := gorm.Open(dialector, &gorm.Config{
		TranslateError: true,
		Logger: glogger.New(log.New(os.Stderr, "", log.LstdFlags), glogger.Config{
			LogLevel: glogger.Warn, SlowThreshold: 200 * time.Millisecond, ParameterizedQueries: true,
		}),
	})
	if err != nil {
		return nil, fmt.Errorf("gormstore: %w", err)
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

// col builds a SET assignment for .Set(...).Update, used where a struct
// update would skip zero values (e.g. writing NULL into ended_at).
func col(name string, v any) clause.Assignment {
	return clause.Assignment{Column: clause.Column{Name: name}, Value: v}
}

// --- row structs ---

type userRow struct {
	ID           string `gorm:"primaryKey"`
	Email        string `gorm:"uniqueIndex"`
	Name         string
	GoogleSub    string
	IsAdmin      bool
	Username     *string
	PasswordHash string
	CreatedAt    time.Time
	LastLoginAt  time.Time
}

func (userRow) TableName() string { return "users" }

func (u userRow) toStore() *store.User {
	user := &store.User{
		ID: u.ID, Email: u.Email, Name: u.Name, GoogleSub: u.GoogleSub, IsAdmin: u.IsAdmin,
		PasswordHash: u.PasswordHash, CreatedAt: u.CreatedAt.UTC(), LastLoginAt: u.LastLoginAt.UTC(),
	}
	if u.Username != nil {
		user.Username = *u.Username
	}
	return user
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
	NodeID       string `gorm:"index:idx_instances_node"`
	LeaseEpoch   int64
}

func (instanceRow) TableName() string { return "instances" }

func (r instanceRow) toStore() *store.Instance {
	in := &store.Instance{
		ID: r.ID, UserID: r.UserID, Spec: r.Spec, Labels: r.Labels,
		Status:       types.InstanceStatus(r.Status),
		CreatedAt:    r.CreatedAt.UTC(),
		LastActiveAt: r.LastActiveAt.UTC(),
		ExpiresAt:    r.ExpiresAt.UTC(),
		NodeID:       r.NodeID,
		LeaseEpoch:   r.LeaseEpoch,
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
	UserID    string `gorm:"uniqueIndex:idx_policies_user_name"`
	Name      string `gorm:"uniqueIndex:idx_policies_user_name"`
	Rego      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (policyRow) TableName() string { return "policies" }

func (r policyRow) toStore() *store.Policy {
	return &store.Policy{
		ID: r.ID, UserID: r.UserID, Name: r.Name, Rego: r.Rego,
		CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
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
		row, rerr := gorm.G[userRow](tx).Where("email = ?", email).Take(ctx)
		switch {
		case errors.Is(rerr, gorm.ErrRecordNotFound):
			row = userRow{
				ID: store.NewID(store.PrefixUser), Email: email,
				Name: name, GoogleSub: googleSub,
				CreatedAt: now, LastLoginAt: now,
			}
			return gorm.G[userRow](tx).Create(ctx, &row)
		case rerr != nil:
			return rerr
		default:
			_, err := gorm.G[userRow](tx).Where("id = ?", row.ID).Updates(ctx, userRow{
				Name: name, GoogleSub: googleSub, LastLoginAt: now,
			})
			return err
		}
	})
	if err != nil {
		return nil, err
	}
	row, err := gorm.G[userRow](d.g).Where("email = ?", email).Take(ctx)
	if err != nil {
		return nil, err
	}
	return row.toStore(), nil
}

func (d *sqlDB) BootstrapAdminUser(ctx context.Context, username, email, passwordHash string) (*store.User, error) {
	now := time.Now().UTC()
	row := userRow{
		ID: store.NewID(store.PrefixUser), Email: email, Name: username, IsAdmin: true,
		Username: &username, PasswordHash: passwordHash, CreatedAt: now, LastLoginAt: now,
	}
	if err := gorm.G[userRow](d.g, clause.OnConflict{
		Columns: []clause.Column{{Name: "email"}},
		DoUpdates: clause.Set{
			col("username", username), col("password_hash", passwordHash), col("is_admin", true),
			col("name", username), col("last_login_at", now),
		},
		Where: clause.Where{Exprs: []clause.Expression{
			clause.Eq{Column: clause.Column{Table: "users", Name: "password_hash"}, Value: ""},
		}},
	}).Create(ctx, &row); err != nil {
		return nil, mapWriteErr(err)
	}
	row, err := gorm.G[userRow](d.g).Where("email = ?", email).Take(ctx)
	if err != nil {
		return nil, err
	}
	return row.toStore(), nil
}

func (d *sqlDB) GetUserByUsername(ctx context.Context, username string) (*store.User, error) {
	row, err := gorm.G[userRow](d.g).Where("username = ?", username).Take(ctx)
	if err != nil {
		return nil, mapNotFound(err)
	}
	return row.toStore(), nil
}

func (d *sqlDB) HasPasswordUsers(ctx context.Context) (bool, error) {
	n, err := gorm.G[userRow](d.g).Where("password_hash <> ''").Count(ctx, "*")
	return n > 0, err
}

func (d *sqlDB) UpdateUserPassword(ctx context.Context, userID, oldHash, newHash string) error {
	return d.g.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		n, err := gorm.G[userRow](tx).Where("id = ? AND password_hash = ? AND password_hash <> ''", userID, oldHash).
			Update(ctx, "password_hash", newHash)
		if err != nil {
			return err
		}
		if n == 0 {
			return store.ErrConflict
		}
		_, err = gorm.G[sessionRow](tx).Where("user_id = ?", userID).Delete(ctx)
		return err
	})
}

func (d *sqlDB) GetUser(ctx context.Context, id string) (*store.User, error) {
	row, err := gorm.G[userRow](d.g).Where("id = ?", id).Take(ctx)
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
	return mapWriteErr(gorm.G[keyRow](d.g).Create(ctx, &row))
}

func (d *sqlDB) ListAPIKeys(ctx context.Context, userID string) ([]*store.APIKey, error) {
	rows, err := gorm.G[keyRow](d.g).Where("user_id = ?", userID).
		Order("created_at").Find(ctx)
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
	row, err := gorm.G[keyRow](d.g).Where("hash = ?", hash).Take(ctx)
	if err != nil {
		return nil, mapNotFound(err)
	}
	return row.toStore(), nil
}

func (d *sqlDB) RevokeAPIKey(ctx context.Context, userID, id string) error {
	n, err := gorm.G[keyRow](d.g).
		Where("id = ? AND user_id = ? AND revoked_at IS NULL", id, userID).
		Update(ctx, "revoked_at", time.Now().UTC())
	if err != nil {
		return err
	}
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (d *sqlDB) TouchAPIKey(ctx context.Context, id string, at time.Time) error {
	_, err := gorm.G[keyRow](d.g).Where("id = ?", id).
		Update(ctx, "last_used_at", at.UTC())
	return err
}

// --- sessions ---

func (d *sqlDB) CreateSession(ctx context.Context, s *store.Session) error {
	return gorm.G[sessionRow](d.g).Create(ctx, &sessionRow{
		Hash: s.Hash, UserID: s.UserID,
		CreatedAt: s.CreatedAt.UTC(), ExpiresAt: s.ExpiresAt.UTC(),
	})
}

func (d *sqlDB) CreatePasswordSession(ctx context.Context, s *store.Session, passwordHash string) error {
	return d.g.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		n, err := gorm.G[userRow](tx).Where("id = ? AND password_hash = ? AND password_hash <> ''", s.UserID, passwordHash).
			Update(ctx, "last_login_at", s.CreatedAt.UTC())
		if err != nil {
			return err
		}
		if n == 0 {
			return store.ErrConflict
		}
		return (&sqlDB{g: tx}).CreateSession(ctx, s)
	})
}

func (d *sqlDB) GetSession(ctx context.Context, hash string) (*store.Session, error) {
	row, err := gorm.G[sessionRow](d.g).Where("hash = ?", hash).Take(ctx)
	if err != nil {
		return nil, mapNotFound(err)
	}
	return &store.Session{
		Hash: row.Hash, UserID: row.UserID,
		CreatedAt: row.CreatedAt.UTC(), ExpiresAt: row.ExpiresAt.UTC(),
	}, nil
}

func (d *sqlDB) DeleteSession(ctx context.Context, hash string) error {
	_, err := gorm.G[sessionRow](d.g).Where("hash = ?", hash).Delete(ctx)
	return err
}

func (d *sqlDB) DeleteExpiredSessions(ctx context.Context) (int, error) {
	return gorm.G[sessionRow](d.g).Where("expires_at < ?", time.Now().UTC()).Delete(ctx)
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
	return gorm.G[instanceRow](d.g).Create(ctx, &instanceRow{
		ID: in.ID, UserID: in.UserID, Spec: []byte(in.Spec), Labels: labels,
		Status:    string(in.Status),
		CreatedAt: in.CreatedAt.UTC(), LastActiveAt: in.LastActiveAt.UTC(),
		ExpiresAt: in.ExpiresAt.UTC(), EndedAt: in.EndedAt,
		NodeID: in.NodeID, LeaseEpoch: in.LeaseEpoch,
	})
}

func (d *sqlDB) GetInstance(ctx context.Context, id string) (*store.Instance, error) {
	row, err := gorm.G[instanceRow](d.g).Where("id = ?", id).Take(ctx)
	if err != nil {
		return nil, mapNotFound(err)
	}
	return row.toStore(), nil
}

func (d *sqlDB) ListInstances(ctx context.Context, userID string, status types.InstanceStatus) ([]*store.Instance, error) {
	q := gorm.G[instanceRow](d.g).Where("user_id = ?", userID).Order("created_at DESC")
	if status != "" {
		q = q.Where("status = ?", string(status))
	}
	rows, err := q.Find(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*store.Instance, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toStore())
	}
	return out, nil
}

func (d *sqlDB) ListExpiredInstances(ctx context.Context, before time.Time, limit int) ([]*store.Instance, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := gorm.G[instanceRow](d.g).
		Where("status = ?", string(types.InstanceRunning)).
		Where("expires_at <= ?", before.UTC()).
		Order("expires_at ASC").
		Limit(limit).
		Find(ctx)
	if err != nil {
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
	_, err = gorm.G[instanceRow](d.g).Where("id = ?", in.ID).Set(
		col("labels", string(lj)),
		col("status", string(in.Status)),
		col("last_active_at", in.LastActiveAt.UTC()),
		col("expires_at", in.ExpiresAt.UTC()),
		col("ended_at", in.EndedAt),
	).Update(ctx)
	return err
}

func (d *sqlDB) MarkRunningAsLostForNode(ctx context.Context, nodeID string) (int, error) {
	return gorm.G[instanceRow](d.g).
		Where("status = ? AND (node_id = ? OR node_id = '')", string(types.InstanceRunning), nodeID).
		Set(
			col("status", string(types.InstanceLost)),
			col("ended_at", time.Now().UTC()),
		).Update(ctx)
}

func (d *sqlDB) BindInstance(ctx context.Context, id, nodeID string) (int64, bool, error) {
	n, err := gorm.G[instanceRow](d.g).
		Where("id = ? AND status = ? AND (node_id = '' OR node_id = ?)",
			id, string(types.InstanceRunning), nodeID).
		Set(
			col("node_id", nodeID),
			col("lease_epoch", gorm.Expr("lease_epoch + 1")),
		).Update(ctx)
	if err != nil || n == 0 {
		return 0, false, err
	}
	row, err := gorm.G[instanceRow](d.g).Select("lease_epoch").Where("id = ?", id).Take(ctx)
	if err != nil {
		return 0, false, mapNotFound(err)
	}
	return row.LeaseEpoch, true, nil
}

func (d *sqlDB) ReleaseInstance(ctx context.Context, id, nodeID string, epoch int64) (bool, error) {
	n, err := gorm.G[instanceRow](d.g).
		Where("id = ? AND node_id = ? AND lease_epoch = ?", id, nodeID, epoch).
		Set(col("node_id", "")).Update(ctx)
	if err != nil {
		return false, err
	}
	return n > 0, nil
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
	return gorm.G[execRow](d.g).Create(ctx, &execRow{
		ID: e.ID, InstanceID: e.InstanceID, UserID: e.UserID,
		CodeSHA256: e.CodeSHA256, CodeSnippet: e.CodeSnippet, Code: e.Code,
		Status: string(e.Status), ErrorType: string(e.ErrorType),
		DurationMs: e.DurationMs, Steps: int64(e.Steps),
		OutputBytes: e.OutputBytes, CreatedAt: e.CreatedAt.UTC(),
	})
}

func (d *sqlDB) GetExecution(ctx context.Context, id string) (*store.Execution, error) {
	row, err := gorm.G[execRow](d.g).Where("id = ?", id).Take(ctx)
	if err != nil {
		return nil, mapNotFound(err)
	}
	return row.toStore(), nil
}

func (d *sqlDB) ListExecutions(ctx context.Context, instanceID string, limit int) ([]*store.Execution, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := gorm.G[execRow](d.g).Where("instance_id = ?", instanceID).
		Order("created_at DESC").Limit(limit).Find(ctx)
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
			if err := gorm.G[auditRow](tx).Create(ctx, &row); err != nil {
				return err
			}
		}
		return nil
	})
}

func (d *sqlDB) ListAuditEvents(ctx context.Context, f store.AuditFilter) ([]*store.AuditEvent, error) {
	q := gorm.G[auditRow](d.g).Order("ts DESC")
	if f.UserID != "" {
		q = q.Where("user_id = ?", f.UserID)
	}
	if f.InstanceID != "" {
		q = q.Where("instance_id = ?", f.InstanceID)
	}
	if f.ExecID != "" {
		q = q.Where("exec_id = ?", f.ExecID)
	}
	if f.Before != nil {
		q = q.Where("ts < ?", f.Before.UTC())
	}
	if f.Limit > 0 {
		q = q.Limit(f.Limit)
	}
	rows, err := q.Find(ctx)
	if err != nil {
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
	err := gorm.G[policyRow](d.g).Create(ctx, &policyRow{
		ID: p.ID, UserID: p.UserID, Name: p.Name, Rego: p.Rego,
		CreatedAt: p.CreatedAt.UTC(), UpdatedAt: p.UpdatedAt.UTC(),
	})
	return mapWriteErr(err)
}

func (d *sqlDB) GetPolicy(ctx context.Context, id string) (*store.Policy, error) {
	row, err := gorm.G[policyRow](d.g).Where("id = ?", id).Take(ctx)
	if err != nil {
		return nil, mapNotFound(err)
	}
	return row.toStore(), nil
}

func (d *sqlDB) ListPolicies(ctx context.Context, userID string) ([]*store.Policy, error) {
	rows, err := gorm.G[policyRow](d.g).Where("user_id = ?", userID).
		Order("created_at").Find(ctx)
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
	n, err := gorm.G[policyRow](d.g).Where("id = ?", p.ID).Set(
		col("name", p.Name),
		col("rego", p.Rego),
		col("updated_at", p.UpdatedAt),
	).Update(ctx)
	if err != nil {
		return mapWriteErr(err)
	}
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (d *sqlDB) DeletePolicy(ctx context.Context, id string) error {
	_, err := gorm.G[policyRow](d.g).Where("id = ?", id).Delete(ctx)
	return err
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
	err := gorm.G[secretRow](d.g).Create(ctx, &secretRow{
		ID: s.ID, UserID: s.UserID, Name: s.Name, Ciphertext: s.Ciphertext,
		AllowedDomains: domains, CreatedAt: s.CreatedAt.UTC(), UpdatedAt: s.UpdatedAt.UTC(),
	})
	return mapWriteErr(err)
}

func (d *sqlDB) GetSecret(ctx context.Context, id string) (*store.Secret, error) {
	row, err := gorm.G[secretRow](d.g).Where("id = ?", id).Take(ctx)
	if err != nil {
		return nil, mapNotFound(err)
	}
	return row.toStore(), nil
}

func (d *sqlDB) GetSecretByName(ctx context.Context, userID, name string) (*store.Secret, error) {
	row, err := gorm.G[secretRow](d.g).
		Where("user_id = ? AND name = ?", userID, name).Take(ctx)
	if err != nil {
		return nil, mapNotFound(err)
	}
	return row.toStore(), nil
}

func (d *sqlDB) ListSecrets(ctx context.Context, userID string) ([]*store.Secret, error) {
	rows, err := gorm.G[secretRow](d.g).Where("user_id = ?", userID).
		Order("name").Find(ctx)
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
	n, err := gorm.G[secretRow](d.g).Where("id = ?", s.ID).Set(
		col("ciphertext", s.Ciphertext),
		col("allowed_domains", string(dj)),
		col("updated_at", s.UpdatedAt),
	).Update(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (d *sqlDB) DeleteSecret(ctx context.Context, id string) error {
	_, err := gorm.G[secretRow](d.g).Where("id = ?", id).Delete(ctx)
	return err
}
