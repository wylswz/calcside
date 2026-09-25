// Package sqlite implements store.Store on modernc.org/sqlite (pure Go).
package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"calcside/internal/store"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type db struct {
	q *sql.DB
}

func init() {
	store.RegisterDriver("sqlite", Open)
}

// Open opens (and migrates) a sqlite database at dsn (file path or :memory:).
func Open(ctx context.Context, dsn string) (store.Store, error) {
	if dsn == "" {
		return nil, fmt.Errorf("sqlite: empty dsn")
	}
	connStr := "file:" + dsn + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	q, err := sql.Open("sqlite", connStr)
	if err != nil {
		return nil, err
	}
	q.SetMaxOpenConns(1)
	d := &db{q: q}
	if err := d.migrate(ctx); err != nil {
		_ = q.Close()
		return nil, err
	}
	return d, nil
}

func (d *db) migrate(ctx context.Context) error {
	var version int
	row := d.q.QueryRowContext(ctx, "SELECT version FROM schema_version LIMIT 1")
	if err := row.Scan(&version); err != nil {
		version = 0
	}
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	for i, e := range entries {
		v := i + 1
		if v <= version {
			continue
		}
		sqlText, err := migrationsFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return err
		}
		if _, err := d.q.ExecContext(ctx, string(sqlText)); err != nil {
			return fmt.Errorf("sqlite migration %s: %w", e.Name(), err)
		}
	}
	if version == 0 {
		_, err = d.q.ExecContext(ctx, "INSERT INTO schema_version(version) VALUES(?)", len(entries))
	} else {
		_, err = d.q.ExecContext(ctx, "UPDATE schema_version SET version=?", len(entries))
	}
	return err
}

func (d *db) Close() error { return d.q.Close() }

func ns(t time.Time) int64 { return t.UnixNano() }

func nsPtr(t *time.Time) *int64 {
	if t == nil {
		return nil
	}
	v := t.UnixNano()
	return &v
}

func tm(n int64) time.Time { return time.Unix(0, n).UTC() }

func tmPtr(n *int64) *time.Time {
	if n == nil {
		return nil
	}
	t := time.Unix(0, *n).UTC()
	return &t
}

// --- users ---

func (d *db) UpsertUserByEmail(ctx context.Context, email, name, googleSub string) (*store.User, error) {
	now := time.Now().UTC()
	var u store.User
	row := d.q.QueryRowContext(ctx,
		`INSERT INTO users(id,email,name,google_sub,created_at,last_login_at)
		 VALUES(?,?,?,?,?,?)
		 ON CONFLICT(email) DO UPDATE SET
		   name=CASE WHEN excluded.name<>'' THEN excluded.name ELSE users.name END,
		   google_sub=CASE WHEN excluded.google_sub<>'' THEN excluded.google_sub ELSE users.google_sub END,
		   last_login_at=excluded.last_login_at
		 RETURNING id,email,name,google_sub,created_at,last_login_at`,
		store.NewID(store.PrefixUser), email, name, googleSub, ns(now), ns(now))
	var ca, ll int64
	if err := row.Scan(&u.ID, &u.Email, &u.Name, &u.GoogleSub, &ca, &ll); err != nil {
		return nil, err
	}
	u.CreatedAt = tm(ca)
	u.LastLoginAt = tm(ll)
	return &u, nil
}

func (d *db) GetUser(ctx context.Context, id string) (*store.User, error) {
	var u store.User
	var ca, ll int64
	err := d.q.QueryRowContext(ctx,
		`SELECT id,email,name,google_sub,created_at,last_login_at FROM users WHERE id=?`, id).
		Scan(&u.ID, &u.Email, &u.Name, &u.GoogleSub, &ca, &ll)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u.CreatedAt, u.LastLoginAt = tm(ca), tm(ll)
	return &u, nil
}

// --- api keys ---

func (d *db) CreateAPIKey(ctx context.Context, k *store.APIKey) error {
	if k.ID == "" {
		k.ID = store.NewID(store.PrefixAPIKey)
	}
	if k.CreatedAt.IsZero() {
		k.CreatedAt = time.Now().UTC()
	}
	_, err := d.q.ExecContext(ctx,
		`INSERT INTO api_keys(id,user_id,name,prefix,hash,created_at,last_used_at,expires_at,revoked_at)
		 VALUES(?,?,?,?,?,?,?,?,?)`,
		k.ID, k.UserID, k.Name, k.Prefix, k.Hash, ns(k.CreatedAt), nsPtr(k.LastUsedAt), nsPtr(k.ExpiresAt), nsPtr(k.RevokedAt))
	return err
}

func scanKey(row interface{ Scan(...any) error }) (*store.APIKey, error) {
	var k store.APIKey
	var ca int64
	var lu, ex, rv *int64
	err := row.Scan(&k.ID, &k.UserID, &k.Name, &k.Prefix, &k.Hash, &ca, &lu, &ex, &rv)
	if err != nil {
		return nil, err
	}
	k.CreatedAt, k.LastUsedAt, k.ExpiresAt, k.RevokedAt = tm(ca), tmPtr(lu), tmPtr(ex), tmPtr(rv)
	return &k, nil
}

const keyCols = `id,user_id,name,prefix,hash,created_at,last_used_at,expires_at,revoked_at`

func (d *db) ListAPIKeys(ctx context.Context, userID string) ([]*store.APIKey, error) {
	rows, err := d.q.QueryContext(ctx, `SELECT `+keyCols+` FROM api_keys WHERE user_id=? ORDER BY created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*store.APIKey
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (d *db) GetAPIKeyByHash(ctx context.Context, hash string) (*store.APIKey, error) {
	k, err := scanKey(d.q.QueryRowContext(ctx, `SELECT `+keyCols+` FROM api_keys WHERE hash=?`, hash))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	return k, err
}

func (d *db) RevokeAPIKey(ctx context.Context, userID, id string) error {
	res, err := d.q.ExecContext(ctx,
		`UPDATE api_keys SET revoked_at=? WHERE id=? AND user_id=? AND revoked_at IS NULL`,
		ns(time.Now().UTC()), id, userID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (d *db) TouchAPIKey(ctx context.Context, id string, at time.Time) error {
	_, err := d.q.ExecContext(ctx, `UPDATE api_keys SET last_used_at=? WHERE id=?`, ns(at), id)
	return err
}

// --- sessions ---

func (d *db) CreateSession(ctx context.Context, s *store.Session) error {
	_, err := d.q.ExecContext(ctx,
		`INSERT INTO sessions(hash,user_id,created_at,expires_at) VALUES(?,?,?,?)`,
		s.Hash, s.UserID, ns(s.CreatedAt), ns(s.ExpiresAt))
	return err
}

func (d *db) GetSession(ctx context.Context, hash string) (*store.Session, error) {
	var s store.Session
	var ca, ex int64
	err := d.q.QueryRowContext(ctx,
		`SELECT hash,user_id,created_at,expires_at FROM sessions WHERE hash=?`, hash).
		Scan(&s.Hash, &s.UserID, &ca, &ex)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	s.CreatedAt, s.ExpiresAt = tm(ca), tm(ex)
	return &s, nil
}

func (d *db) DeleteSession(ctx context.Context, hash string) error {
	_, err := d.q.ExecContext(ctx, `DELETE FROM sessions WHERE hash=?`, hash)
	return err
}

func (d *db) DeleteExpiredSessions(ctx context.Context) (int, error) {
	res, err := d.q.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at<?`, ns(time.Now().UTC()))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// --- instances ---

func (d *db) CreateInstance(ctx context.Context, in *store.Instance) error {
	if in.ID == "" {
		in.ID = store.NewID(store.PrefixInstance)
	}
	labels, err := json.Marshal(in.Labels)
	if err != nil {
		return err
	}
	_, err = d.q.ExecContext(ctx,
		`INSERT INTO instances(id,user_id,spec,labels,status,created_at,last_active_at,expires_at,ended_at)
		 VALUES(?,?,?,?,?,?,?,?,?)`,
		in.ID, in.UserID, []byte(in.Spec), string(labels), in.Status,
		ns(in.CreatedAt), ns(in.LastActiveAt), ns(in.ExpiresAt), nsPtr(in.EndedAt))
	return err
}

func scanInstance(row interface{ Scan(...any) error }) (*store.Instance, error) {
	var in store.Instance
	var labels string
	var spec []byte
	var ca, la, ex int64
	var en *int64
	err := row.Scan(&in.ID, &in.UserID, &spec, &labels, &in.Status, &ca, &la, &ex, &en)
	if err != nil {
		return nil, err
	}
	in.Spec = spec
	in.CreatedAt, in.LastActiveAt, in.ExpiresAt, in.EndedAt = tm(ca), tm(la), tm(ex), tmPtr(en)
	if err := json.Unmarshal([]byte(labels), &in.Labels); err != nil {
		in.Labels = map[string]string{}
	}
	return &in, nil
}

const instCols = `id,user_id,spec,labels,status,created_at,last_active_at,expires_at,ended_at`

func (d *db) GetInstance(ctx context.Context, id string) (*store.Instance, error) {
	in, err := scanInstance(d.q.QueryRowContext(ctx, `SELECT `+instCols+` FROM instances WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	return in, err
}

func (d *db) ListInstances(ctx context.Context, userID, status string) ([]*store.Instance, error) {
	q := `SELECT ` + instCols + ` FROM instances WHERE user_id=?`
	args := []any{userID}
	if status != "" {
		q += ` AND status=?`
		args = append(args, status)
	}
	q += ` ORDER BY created_at DESC`
	rows, err := d.q.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*store.Instance
	for rows.Next() {
		in, err := scanInstance(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

func (d *db) UpdateInstance(ctx context.Context, in *store.Instance) error {
	labels, err := json.Marshal(in.Labels)
	if err != nil {
		return err
	}
	_, err = d.q.ExecContext(ctx,
		`UPDATE instances SET labels=?,status=?,last_active_at=?,expires_at=?,ended_at=? WHERE id=?`,
		string(labels), in.Status, ns(in.LastActiveAt), ns(in.ExpiresAt), nsPtr(in.EndedAt), in.ID)
	return err
}

func (d *db) MarkRunningAsLost(ctx context.Context) (int, error) {
	now := ns(time.Now().UTC())
	res, err := d.q.ExecContext(ctx,
		`UPDATE instances SET status=?, ended_at=? WHERE status=?`,
		store.StatusLost, now, store.StatusRunning)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// --- executions ---

func (d *db) CreateExecution(ctx context.Context, e *store.Execution) error {
	if e.ID == "" {
		e.ID = store.NewID(store.PrefixExecution)
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	_, err := d.q.ExecContext(ctx,
		`INSERT INTO executions(id,instance_id,user_id,code_sha256,code_snippet,status,error_type,duration_ms,steps,output_bytes,created_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		e.ID, e.InstanceID, e.UserID, e.CodeSHA256, e.CodeSnippet, e.Status,
		e.ErrorType, e.DurationMs, int64(e.Steps), e.OutputBytes, ns(e.CreatedAt))
	return err
}

func (d *db) ListExecutions(ctx context.Context, instanceID string, limit int) ([]*store.Execution, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := d.q.QueryContext(ctx,
		`SELECT id,instance_id,user_id,code_sha256,code_snippet,status,error_type,duration_ms,steps,output_bytes,created_at
		 FROM executions WHERE instance_id=? ORDER BY created_at DESC LIMIT ?`, instanceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*store.Execution
	for rows.Next() {
		var e store.Execution
		var ca int64
		var steps int64
		if err := rows.Scan(&e.ID, &e.InstanceID, &e.UserID, &e.CodeSHA256, &e.CodeSnippet,
			&e.Status, &e.ErrorType, &e.DurationMs, &steps, &e.OutputBytes, &ca); err != nil {
			return nil, err
		}
		e.Steps = uint64(steps)
		e.CreatedAt = tm(ca)
		out = append(out, &e)
	}
	return out, rows.Err()
}

// --- audit ---

func (d *db) InsertAuditEvents(ctx context.Context, evs []store.AuditEvent) error {
	if len(evs) == 0 {
		return nil
	}
	tx, err := d.q.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO audit_events(id,ts,user_id,instance_id,exec_id,capability,op,args,phase,decision,reason,error,duration_ms)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, e := range evs {
		if e.ID == "" {
			e.ID = store.NewID(store.PrefixAudit)
		}
		if _, err := stmt.ExecContext(ctx, e.ID, ns(e.Ts), e.UserID, e.InstanceID, e.ExecID,
			e.Capability, e.Op, e.Args, e.Phase, e.Decision, e.Reason, e.Error, e.DurationMs); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (d *db) ListAuditEvents(ctx context.Context, f store.AuditFilter) ([]*store.AuditEvent, error) {
	q := `SELECT id,ts,user_id,instance_id,exec_id,capability,op,args,phase,decision,reason,error,duration_ms FROM audit_events WHERE 1=1`
	var args []any
	if f.UserID != "" {
		q += ` AND user_id=?`
		args = append(args, f.UserID)
	}
	if f.InstanceID != "" {
		q += ` AND instance_id=?`
		args = append(args, f.InstanceID)
	}
	if f.ExecID != "" {
		q += ` AND exec_id=?`
		args = append(args, f.ExecID)
	}
	if f.Before != nil {
		q += ` AND ts<?`
		args = append(args, ns(*f.Before))
	}
	q += ` ORDER BY ts DESC`
	if f.Limit > 0 {
		q += ` LIMIT ?`
		args = append(args, f.Limit)
	}
	rows, err := d.q.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*store.AuditEvent
	for rows.Next() {
		var e store.AuditEvent
		var ts int64
		if err := rows.Scan(&e.ID, &ts, &e.UserID, &e.InstanceID, &e.ExecID, &e.Capability,
			&e.Op, &e.Args, &e.Phase, &e.Decision, &e.Reason, &e.Error, &e.DurationMs); err != nil {
			return nil, err
		}
		e.Ts = tm(ts)
		out = append(out, &e)
	}
	return out, rows.Err()
}

// --- policies ---

func (d *db) CreatePolicy(ctx context.Context, p *store.Policy) error {
	if p.ID == "" {
		p.ID = store.NewID(store.PrefixPolicy)
	}
	now := time.Now().UTC()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	p.UpdatedAt = now
	en := 0
	if p.Enabled {
		en = 1
	}
	_, err := d.q.ExecContext(ctx,
		`INSERT INTO policies(id,user_id,name,rego,enabled,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`,
		p.ID, p.UserID, p.Name, p.Rego, en, ns(p.CreatedAt), ns(p.UpdatedAt))
	return err
}

func scanPolicy(row interface{ Scan(...any) error }) (*store.Policy, error) {
	var p store.Policy
	var en int
	var ca, ua int64
	err := row.Scan(&p.ID, &p.UserID, &p.Name, &p.Rego, &en, &ca, &ua)
	if err != nil {
		return nil, err
	}
	p.Enabled = en != 0
	p.CreatedAt, p.UpdatedAt = tm(ca), tm(ua)
	return &p, nil
}

const polCols = `id,user_id,name,rego,enabled,created_at,updated_at`

func (d *db) GetPolicy(ctx context.Context, id string) (*store.Policy, error) {
	p, err := scanPolicy(d.q.QueryRowContext(ctx, `SELECT `+polCols+` FROM policies WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	return p, err
}

func (d *db) ListPolicies(ctx context.Context, userID string) ([]*store.Policy, error) {
	rows, err := d.q.QueryContext(ctx, `SELECT `+polCols+` FROM policies WHERE user_id=? ORDER BY created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*store.Policy
	for rows.Next() {
		p, err := scanPolicy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (d *db) UpdatePolicy(ctx context.Context, p *store.Policy) error {
	en := 0
	if p.Enabled {
		en = 1
	}
	p.UpdatedAt = time.Now().UTC()
	res, err := d.q.ExecContext(ctx,
		`UPDATE policies SET name=?,rego=?,enabled=?,updated_at=? WHERE id=?`,
		p.Name, p.Rego, en, ns(p.UpdatedAt), p.ID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (d *db) DeletePolicy(ctx context.Context, id string) error {
	_, err := d.q.ExecContext(ctx, `DELETE FROM policies WHERE id=?`, id)
	return err
}

// --- secrets ---

func isUniqueErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func (d *db) CreateSecret(ctx context.Context, s *store.Secret) error {
	if s.ID == "" {
		s.ID = store.NewID(store.PrefixSecret)
	}
	now := time.Now().UTC()
	if s.CreatedAt.IsZero() {
		s.CreatedAt = now
	}
	s.UpdatedAt = now
	domains, err := json.Marshal(s.AllowedDomains)
	if err != nil {
		return err
	}
	_, err = d.q.ExecContext(ctx,
		`INSERT INTO secrets(id,user_id,name,ciphertext,allowed_domains,created_at,updated_at)
		 VALUES(?,?,?,?,?,?,?)`,
		s.ID, s.UserID, s.Name, s.Ciphertext, string(domains), ns(s.CreatedAt), ns(s.UpdatedAt))
	if isUniqueErr(err) {
		return store.ErrConflict
	}
	return err
}

func scanSecret(row interface{ Scan(...any) error }) (*store.Secret, error) {
	var s store.Secret
	var domains string
	var ca, ua int64
	err := row.Scan(&s.ID, &s.UserID, &s.Name, &s.Ciphertext, &domains, &ca, &ua)
	if err != nil {
		return nil, err
	}
	s.CreatedAt, s.UpdatedAt = tm(ca), tm(ua)
	if err := json.Unmarshal([]byte(domains), &s.AllowedDomains); err != nil {
		s.AllowedDomains = []string{}
	}
	return &s, nil
}

const secretCols = `id,user_id,name,ciphertext,allowed_domains,created_at,updated_at`

func (d *db) GetSecret(ctx context.Context, id string) (*store.Secret, error) {
	s, err := scanSecret(d.q.QueryRowContext(ctx, `SELECT `+secretCols+` FROM secrets WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	return s, err
}

func (d *db) GetSecretByName(ctx context.Context, userID, name string) (*store.Secret, error) {
	s, err := scanSecret(d.q.QueryRowContext(ctx,
		`SELECT `+secretCols+` FROM secrets WHERE user_id=? AND name=?`, userID, name))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	return s, err
}

func (d *db) ListSecrets(ctx context.Context, userID string) ([]*store.Secret, error) {
	rows, err := d.q.QueryContext(ctx,
		`SELECT `+secretCols+` FROM secrets WHERE user_id=? ORDER BY name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*store.Secret
	for rows.Next() {
		s, err := scanSecret(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (d *db) UpdateSecret(ctx context.Context, s *store.Secret) error {
	s.UpdatedAt = time.Now().UTC()
	domains, err := json.Marshal(s.AllowedDomains)
	if err != nil {
		return err
	}
	res, err := d.q.ExecContext(ctx,
		`UPDATE secrets SET ciphertext=?,allowed_domains=?,updated_at=? WHERE id=?`,
		s.Ciphertext, string(domains), ns(s.UpdatedAt), s.ID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (d *db) DeleteSecret(ctx context.Context, id string) error {
	_, err := d.q.ExecContext(ctx, `DELETE FROM secrets WHERE id=?`, id)
	return err
}
