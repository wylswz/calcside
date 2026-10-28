package gormstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"calcside/internal/store"
)

// serving.go implements store.ServingStore. Lock order inside every
// multi-row transaction is node then instance; the first statement in
// a transaction is always a no-op write so SQLite serializes writers
// before any read (BUSY_SNAPSHOT avoidance) and Postgres takes the row
// lock.

type servingReleaseRow struct {
	ID          string    `gorm:"primaryKey;column:id"`
	WorkspaceID string    `gorm:"column:workspace_id"`
	UserID      string    `gorm:"column:user_id"`
	Spec        string    `gorm:"column:spec"`
	Policies    string    `gorm:"column:policies"`
	Program     string    `gorm:"column:program"`
	CreatedAt   time.Time `gorm:"column:created_at"`
}

func (servingReleaseRow) TableName() string { return "serving_releases" }

type persistentInstanceRow struct {
	ID                  string     `gorm:"primaryKey;column:id"`
	WorkspaceID         string     `gorm:"column:workspace_id"`
	UserID              string     `gorm:"column:user_id"`
	ReleaseID           string     `gorm:"column:release_id"`
	Labels              string     `gorm:"column:labels"`
	Enabled             bool       `gorm:"column:enabled"`
	Generation          int64      `gorm:"column:generation"`
	LastEpoch           int64      `gorm:"column:last_epoch"`
	ActiveReplicaID     string     `gorm:"column:active_replica_id"`
	ConsecutiveFailures int        `gorm:"column:consecutive_failures"`
	RetryAfter          *time.Time `gorm:"column:retry_after"`
	CreatedAt           time.Time  `gorm:"column:created_at"`
	UpdatedAt           time.Time  `gorm:"column:updated_at"`
}

func (persistentInstanceRow) TableName() string { return "persistent_instances" }

type servingNodeRow struct {
	NodeID         string    `gorm:"primaryKey;column:node_id"`
	BootID         string    `gorm:"column:boot_id"`
	Addr           string    `gorm:"column:addr"`
	Incarnation    int64     `gorm:"column:incarnation"`
	Capacity       int       `gorm:"column:capacity"`
	HeartbeatUntil time.Time `gorm:"column:heartbeat_until"`
	UpdatedAt      time.Time `gorm:"column:updated_at"`
}

func (servingNodeRow) TableName() string { return "serving_nodes" }

type servingNodeBootRow struct {
	NodeID      string `gorm:"primaryKey;column:node_id"`
	BootID      string `gorm:"primaryKey;column:boot_id"`
	Incarnation int64  `gorm:"column:incarnation"`
}

func (servingNodeBootRow) TableName() string { return "serving_node_boots" }

type servingReplicaRow struct {
	ID              string    `gorm:"primaryKey;column:id"`
	WorkspaceID     string    `gorm:"column:workspace_id"`
	InstanceID      string    `gorm:"column:instance_id"`
	ReleaseID       string    `gorm:"column:release_id"`
	NodeID          string    `gorm:"column:node_id"`
	BootID          string    `gorm:"column:boot_id"`
	NodeIncarnation int64     `gorm:"column:node_incarnation"`
	Generation      int64     `gorm:"column:generation"`
	Epoch           int64     `gorm:"column:epoch"`
	State           string    `gorm:"column:state"`
	LeaseUntil      time.Time `gorm:"column:lease_until"`
	ErrorCode       string    `gorm:"column:error_code"`
	CreatedAt       time.Time `gorm:"column:created_at"`
	UpdatedAt       time.Time `gorm:"column:updated_at"`
}

func (servingReplicaRow) TableName() string { return "serving_replicas" }

// --- conversions ---

func releaseToRow(r *store.ServingRelease) (*servingReleaseRow, error) {
	pol, err := json.Marshal(r.Policies)
	if err != nil {
		return nil, err
	}
	prog, err := json.Marshal(r.Program)
	if err != nil {
		return nil, err
	}
	return &servingReleaseRow{
		ID: r.ID, WorkspaceID: r.WorkspaceID, UserID: r.UserID,
		Spec: string(r.Spec), Policies: string(pol), Program: string(prog),
		CreatedAt: r.CreatedAt.UTC(),
	}, nil
}

func (r *servingReleaseRow) toDomain() (*store.ServingRelease, error) {
	out := &store.ServingRelease{
		ID: r.ID, WorkspaceID: r.WorkspaceID, UserID: r.UserID,
		Spec:      json.RawMessage(append([]byte(nil), r.Spec...)),
		CreatedAt: r.CreatedAt.UTC(),
	}
	if err := json.Unmarshal([]byte(r.Policies), &out.Policies); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(r.Program), &out.Program); err != nil {
		return nil, err
	}
	return out, nil
}

func instanceToRow(in *store.PersistentInstance) (*persistentInstanceRow, error) {
	labels, err := json.Marshal(in.Labels)
	if err != nil {
		return nil, err
	}
	return &persistentInstanceRow{
		ID: in.ID, WorkspaceID: in.WorkspaceID, UserID: in.UserID,
		ReleaseID: in.ReleaseID, Labels: string(labels), Enabled: in.Enabled,
		Generation: in.Generation, LastEpoch: in.LastEpoch,
		ActiveReplicaID:     in.ActiveReplicaID,
		ConsecutiveFailures: in.ConsecutiveFailures, RetryAfter: in.RetryAfter,
		CreatedAt: in.CreatedAt.UTC(), UpdatedAt: in.UpdatedAt.UTC(),
	}, nil
}

func (r *persistentInstanceRow) toDomain() *store.PersistentInstance {
	out := &store.PersistentInstance{
		ID: r.ID, WorkspaceID: r.WorkspaceID, UserID: r.UserID,
		ReleaseID: r.ReleaseID, Enabled: r.Enabled, Generation: r.Generation,
		LastEpoch: r.LastEpoch, ActiveReplicaID: r.ActiveReplicaID,
		ConsecutiveFailures: r.ConsecutiveFailures, RetryAfter: r.RetryAfter,
		CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
	}
	out.Labels = map[string]string{}
	_ = json.Unmarshal([]byte(r.Labels), &out.Labels)
	return out
}

func nodeToRow(n *store.ServingNode) *servingNodeRow {
	return &servingNodeRow{
		NodeID: n.NodeID, BootID: n.BootID, Addr: n.Addr,
		Incarnation: n.Incarnation, Capacity: n.Capacity,
		HeartbeatUntil: n.HeartbeatUntil.UTC(), UpdatedAt: n.UpdatedAt.UTC(),
	}
}

func (r *servingNodeRow) toDomain() *store.ServingNode {
	return &store.ServingNode{
		NodeID: r.NodeID, BootID: r.BootID, Addr: r.Addr,
		Incarnation: r.Incarnation, Capacity: r.Capacity,
		HeartbeatUntil: r.HeartbeatUntil.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
	}
}

func replicaToRow(r *store.ServingReplica) *servingReplicaRow {
	return &servingReplicaRow{
		ID: r.ID, WorkspaceID: r.WorkspaceID, InstanceID: r.InstanceID, ReleaseID: r.ReleaseID,
		NodeID: r.NodeID, BootID: r.BootID, NodeIncarnation: r.NodeIncarnation,
		Generation: r.Generation, Epoch: r.Epoch, State: r.State,
		LeaseUntil: r.LeaseUntil.UTC(), ErrorCode: r.ErrorCode,
		CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
	}
}

func (r *servingReplicaRow) toDomain() *store.ServingReplica {
	return &store.ServingReplica{
		ID: r.ID, WorkspaceID: r.WorkspaceID, InstanceID: r.InstanceID, ReleaseID: r.ReleaseID,
		NodeID: r.NodeID, BootID: r.BootID, NodeIncarnation: r.NodeIncarnation,
		Generation: r.Generation, Epoch: r.Epoch, State: r.State,
		LeaseUntil: r.LeaseUntil.UTC(), ErrorCode: r.ErrorCode,
		CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
	}
}

// --- release / instance ---

func (d *sqlDB) CreateServingRelease(ctx context.Context, r *store.ServingRelease) error {
	// Recompute the content hash so a forged id or tampered payload
	// cannot claim an existing identity.
	check, err := store.NewServingRelease(r.WorkspaceID, r.UserID, r.Spec, r.Policies, r.Program, r.CreatedAt)
	if err != nil {
		return err
	}
	if check.ID != r.ID {
		return fmt.Errorf("%w: release id does not match content", store.ErrInvalidServingRecord)
	}
	row, err := releaseToRow(check)
	if err != nil {
		return err
	}
	return mapWriteErr(gorm.G[servingReleaseRow](d.g).Create(ctx, row))
}

func (d *sqlDB) GetServingRelease(ctx context.Context, workspaceID, id string) (*store.ServingRelease, error) {
	row, err := gorm.G[servingReleaseRow](d.g).
		Where("id = ? AND workspace_id = ?", id, workspaceID).Take(ctx)
	if err != nil {
		return nil, mapNotFound(err)
	}
	return row.toDomain()
}

func (d *sqlDB) CreatePersistentInstance(ctx context.Context, in *store.PersistentInstance) error {
	if in.WorkspaceID == "" || in.UserID == "" {
		return fmt.Errorf("%w: workspace and owner user required", store.ErrInvalidServingRecord)
	}
	rec := *in
	rec.Labels = map[string]string{}
	for k, v := range in.Labels {
		rec.Labels[k] = v
	}
	if rec.ID == "" {
		rec.ID = store.NewID(store.PrefixPersistentInstance)
	}
	rec.Generation = 1
	rec.LastEpoch = 0
	rec.ActiveReplicaID = ""
	// The release must exist in the same workspace.
	if _, err := d.GetServingRelease(ctx, rec.WorkspaceID, rec.ReleaseID); err != nil {
		return err
	}
	row, err := instanceToRow(&rec)
	if err != nil {
		return err
	}
	if err := gorm.G[persistentInstanceRow](d.g).Create(ctx, row); err != nil {
		return mapWriteErr(err)
	}
	in.ID = rec.ID
	in.Generation, in.LastEpoch = 1, 0
	in.ActiveReplicaID = ""
	return nil
}

func (d *sqlDB) GetPersistentInstance(ctx context.Context, workspaceID, id string) (*store.PersistentInstance, error) {
	row, err := gorm.G[persistentInstanceRow](d.g).
		Where("id = ? AND workspace_id = ?", id, workspaceID).Take(ctx)
	if err != nil {
		return nil, mapNotFound(err)
	}
	return row.toDomain(), nil
}

func (d *sqlDB) ListPersistentInstances(ctx context.Context, afterID string, limit int) ([]*store.PersistentInstance, error) {
	if limit < 1 || limit > 200 {
		return nil, fmt.Errorf("%w: limit out of bounds", store.ErrInvalidServingRecord)
	}
	q := gorm.G[persistentInstanceRow](d.g).Order("id ASC").Limit(limit)
	if afterID != "" {
		q = q.Where("id > ?", afterID)
	}
	rows, err := q.Find(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*store.PersistentInstance, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toDomain())
	}
	return out, nil
}

func (d *sqlDB) SetPersistentEnabled(ctx context.Context, workspaceID, id string, expectedGeneration int64, enabled bool, now time.Time) (bool, error) {
	var ok bool
	err := d.g.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		ok = false
		// Lock the row: a no-op write first so SQLite takes the
		// writer lock before the read.
		if err := tx.Exec("UPDATE persistent_instances SET last_epoch = last_epoch WHERE id = ?", id).Error; err != nil {
			return err
		}
		row, err := gorm.G[persistentInstanceRow](tx, clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND workspace_id = ?", id, workspaceID).Take(ctx)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if row.Generation != expectedGeneration || row.Enabled == enabled {
			// Stale CAS or idempotent no-op; either way no bump.
			ok = row.Generation == expectedGeneration && row.Enabled == enabled
			return nil
		}
		if row.Generation == math.MaxInt64 {
			return fmt.Errorf("%w: generation overflow", store.ErrInvalidServingRecord)
		}
		// Any transition invalidates the active pointer and the
		// backoff state of the old generation.
		err = tx.Model(&persistentInstanceRow{}).Where("id = ?", id).
			Updates(map[string]any{
				"enabled": enabled, "generation": row.Generation + 1,
				"active_replica_id": "", "consecutive_failures": 0,
				"retry_after": nil, "updated_at": now.UTC(),
			}).Error
		if err != nil {
			return err
		}
		ok = true
		return nil
	})
	return ok, err
}

// --- nodes ---

func validNode(n *store.ServingNode) error {
	if n.NodeID == "" || n.BootID == "" || n.Addr == "" ||
		len(n.NodeID) > 128 || len(n.BootID) > 128 {
		return fmt.Errorf("%w: node/boot/addr required", store.ErrInvalidServingRecord)
	}
	if n.Capacity < 1 || n.Capacity > 128 {
		return fmt.Errorf("%w: capacity out of bounds", store.ErrInvalidServingRecord)
	}
	return nil
}

func (d *sqlDB) RegisterServingNode(ctx context.Context, n *store.ServingNode, now time.Time) (*store.ServingNode, error) {
	if err := validNode(n); err != nil {
		return nil, err
	}
	if !n.HeartbeatUntil.After(now) {
		return nil, fmt.Errorf("%w: heartbeat must be in the future", store.ErrInvalidServingRecord)
	}
	var out *store.ServingNode
	err := d.g.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Insert a placeholder if absent, then take the write lock.
		ph := servingNodeRow{NodeID: n.NodeID, BootID: "", Addr: n.Addr, HeartbeatUntil: now.UTC(), UpdatedAt: now.UTC()}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&ph).Error; err != nil {
			return mapWriteErr(err)
		}
		if err := tx.Exec("UPDATE serving_nodes SET incarnation = incarnation WHERE node_id = ?", n.NodeID).Error; err != nil {
			return err
		}
		row, err := gorm.G[servingNodeRow](tx, clause.Locking{Strength: "UPDATE"}).
			Where("node_id = ?", n.NodeID).Take(ctx)
		if err != nil {
			return err
		}
		if row.BootID == n.BootID {
			// Same current boot: idempotent refresh.
			if err := tx.Exec(
				"UPDATE serving_nodes SET addr = ?, capacity = ?, "+
					"heartbeat_until = CASE WHEN heartbeat_until < ? THEN ? ELSE heartbeat_until END, "+
					"updated_at = ? WHERE node_id = ?",
				n.Addr, n.Capacity, n.HeartbeatUntil.UTC(), n.HeartbeatUntil.UTC(),
				now.UTC(), n.NodeID).Error; err != nil {
				return err
			}
			row.Addr, row.Capacity = n.Addr, n.Capacity
			if n.HeartbeatUntil.UTC().After(row.HeartbeatUntil) {
				row.HeartbeatUntil = n.HeartbeatUntil.UTC()
			}
			row.UpdatedAt = now.UTC()
			out = row.toDomain()
			return nil
		}
		// A different boot: must be an unseen (node,boot) pair.
		_, herr := gorm.G[servingNodeBootRow](tx).
			Where("node_id = ? AND boot_id = ?", n.NodeID, n.BootID).Take(ctx)
		if herr == nil {
			return store.ErrConflict // an old boot can never return
		}
		if !errors.Is(herr, gorm.ErrRecordNotFound) {
			return herr
		}
		if row.Incarnation == math.MaxInt64 {
			return fmt.Errorf("%w: incarnation overflow", store.ErrInvalidServingRecord)
		}
		inc := row.Incarnation + 1
		if err := gorm.G[servingNodeBootRow](tx).Create(ctx, &servingNodeBootRow{
			NodeID: n.NodeID, BootID: n.BootID, Incarnation: inc,
		}); err != nil {
			return mapWriteErr(err)
		}
		if err := tx.Model(&servingNodeRow{}).Where("node_id = ?", n.NodeID).
			Updates(map[string]any{
				"boot_id": n.BootID, "addr": n.Addr, "incarnation": inc,
				"capacity": n.Capacity, "heartbeat_until": n.HeartbeatUntil.UTC(),
				"updated_at": now.UTC(),
			}).Error; err != nil {
			return err
		}
		row.BootID, row.Addr, row.Incarnation = n.BootID, n.Addr, inc
		row.Capacity, row.HeartbeatUntil, row.UpdatedAt = n.Capacity, n.HeartbeatUntil.UTC(), now.UTC()
		out = row.toDomain()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (d *sqlDB) HeartbeatServingNode(ctx context.Context, f store.ServingNodeFence, now, until time.Time, capacity int) (bool, error) {
	if !until.After(now) {
		return false, fmt.Errorf("%w: heartbeat must be in the future", store.ErrInvalidServingRecord)
	}
	if capacity < 1 || capacity > 128 {
		return false, fmt.Errorf("%w: capacity out of bounds", store.ErrInvalidServingRecord)
	}
	// Exact current tuple only: an old boot or stale incarnation
	// cannot refresh, but a still-current boot may return after a
	// liveness gap.
	res := d.g.WithContext(ctx).Exec(
		"UPDATE serving_nodes SET "+
			"heartbeat_until = CASE WHEN heartbeat_until < ? THEN ? ELSE heartbeat_until END, "+
			"capacity = ?, updated_at = ? "+
			"WHERE node_id = ? AND boot_id = ? AND incarnation = ?",
		until.UTC(), until.UTC(), capacity, now.UTC(), f.NodeID, f.BootID, f.Incarnation)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

func (d *sqlDB) ListServingNodes(ctx context.Context, now time.Time) ([]*store.ServingNode, error) {
	rows, err := gorm.G[servingNodeRow](d.g).
		Where("heartbeat_until > ?", now.UTC()).Order("node_id ASC").Find(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*store.ServingNode, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toDomain())
	}
	return out, nil
}

// --- replicas ---

// lockNode locks the node row and returns its current domain value.
// The caller supplies the tx.
func lockNode(ctx context.Context, tx *gorm.DB, nodeID string) (*servingNodeRow, error) {
	if err := tx.Exec("UPDATE serving_nodes SET incarnation = incarnation WHERE node_id = ?", nodeID).Error; err != nil {
		return nil, err
	}
	row, err := gorm.G[servingNodeRow](tx, clause.Locking{Strength: "UPDATE"}).
		Where("node_id = ?", nodeID).Take(ctx)
	if err != nil {
		return nil, mapNotFound(err)
	}
	return &row, nil
}

func lockInstance(ctx context.Context, tx *gorm.DB, id string) (*persistentInstanceRow, error) {
	if err := tx.Exec("UPDATE persistent_instances SET last_epoch = last_epoch WHERE id = ?", id).Error; err != nil {
		return nil, err
	}
	row, err := gorm.G[persistentInstanceRow](tx, clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", id).Take(ctx)
	if err != nil {
		return nil, mapNotFound(err)
	}
	return &row, nil
}

func (d *sqlDB) ClaimServingReplica(ctx context.Context, c store.ServingReplicaClaim) (*store.ServingReplica, error) {
	if c.ReplicaID == "" || c.InstanceID == "" || c.ReleaseID == "" ||
		c.NodeID == "" || c.BootID == "" ||
		len(c.ReplicaID) > 128 || len(c.InstanceID) > 128 || len(c.ReleaseID) > 128 ||
		len(c.NodeID) > 128 || len(c.BootID) > 128 ||
		c.Generation <= 0 || c.NodeIncarnation <= 0 || c.ExpectedEpoch < 0 ||
		c.Now.IsZero() || !c.LeaseUntil.After(c.Now) {
		return nil, fmt.Errorf("%w: invalid claim", store.ErrInvalidServingRecord)
	}
	var out *store.ServingReplica
	err := d.g.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		node, err := lockNode(ctx, tx, c.NodeID)
		if err != nil {
			return err
		}
		if node.BootID != c.BootID || node.Incarnation != c.NodeIncarnation {
			return store.ErrConflict
		}
		if !node.HeartbeatUntil.After(c.Now) {
			return store.ErrConflict
		}
		if !c.LeaseUntil.After(c.Now) || c.LeaseUntil.After(node.HeartbeatUntil) {
			return fmt.Errorf("%w: lease out of bounds", store.ErrInvalidServingRecord)
		}
		inst, err := lockInstance(ctx, tx, c.InstanceID)
		if err != nil {
			return err
		}
		if !inst.Enabled || inst.ReleaseID != c.ReleaseID || inst.Generation != c.Generation {
			return store.ErrConflict
		}
		if inst.LastEpoch != c.ExpectedEpoch {
			return store.ErrConflict
		}
		// Any unexpired non-terminal replica blocks a new claim.
		block, err := gorm.G[servingReplicaRow](tx).
			Where("instance_id = ? AND lease_until > ? AND state IN (?, ?)",
				c.InstanceID, c.Now.UTC(), store.ServingReplicaPreparing, store.ServingReplicaReady).
			Count(ctx, "*")
		if err != nil {
			return err
		}
		if block > 0 {
			return store.ErrConflict
		}
		live, err := gorm.G[servingReplicaRow](tx).
			Where("node_id = ? AND boot_id = ? AND node_incarnation = ? AND lease_until > ? AND state IN (?, ?)",
				c.NodeID, c.BootID, c.NodeIncarnation, c.Now.UTC(),
				store.ServingReplicaPreparing, store.ServingReplicaReady).
			Count(ctx, "*")
		if err != nil {
			return err
		}
		if int(live) >= node.Capacity {
			return store.ErrConflict
		}
		if inst.LastEpoch == math.MaxInt64 {
			return fmt.Errorf("%w: epoch overflow", store.ErrInvalidServingRecord)
		}
		// Backoff: a poisoned release waits out its persisted delay.
		if inst.RetryAfter != nil && c.Now.Before(*inst.RetryAfter) {
			return store.ErrConflict
		}
		rep := &store.ServingReplica{
			ID: c.ReplicaID, WorkspaceID: inst.WorkspaceID, InstanceID: c.InstanceID, ReleaseID: c.ReleaseID,
			NodeID: c.NodeID, BootID: c.BootID, NodeIncarnation: c.NodeIncarnation,
			Generation: c.Generation, Epoch: inst.LastEpoch + 1,
			State:      store.ServingReplicaPreparing,
			LeaseUntil: c.LeaseUntil.UTC(), CreatedAt: c.Now.UTC(), UpdatedAt: c.Now.UTC(),
		}
		row := replicaToRow(rep)
		if err := gorm.G[servingReplicaRow](tx).Create(ctx, row); err != nil {
			return mapWriteErr(err)
		}
		if err := tx.Model(&persistentInstanceRow{}).Where("id = ?", c.InstanceID).
			Updates(map[string]any{
				"last_epoch": inst.LastEpoch + 1, "updated_at": c.Now.UTC(),
			}).Error; err != nil {
			return err
		}
		out = rep
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (d *sqlDB) GetServingReplica(ctx context.Context, id string) (*store.ServingReplica, error) {
	row, err := gorm.G[servingReplicaRow](d.g).Where("id = ?", id).Take(ctx)
	if err != nil {
		return nil, mapNotFound(err)
	}
	return row.toDomain(), nil
}

func (d *sqlDB) ListNodeServingReplicas(ctx context.Context, f store.ServingNodeFence, now time.Time) ([]*store.ServingReplica, error) {
	// The inventory is authoritative for the restore path: a stale
	// boot, stale incarnation or dead node must not masquerade as an
	// empty desired set.
	node, err := gorm.G[servingNodeRow](d.g).Where("node_id = ?", f.NodeID).Take(ctx)
	if err != nil {
		return nil, mapNotFound(err)
	}
	if node.BootID != f.BootID || node.Incarnation != f.Incarnation ||
		!node.HeartbeatUntil.After(now) {
		return nil, store.ErrConflict
	}
	var rows []servingReplicaRow
	err = d.g.WithContext(ctx).Raw(
		"SELECT r.* FROM serving_replicas r "+
			"JOIN persistent_instances i ON i.id = r.instance_id "+
			"JOIN serving_nodes n ON n.node_id = r.node_id "+
			"WHERE r.node_id = ? AND r.boot_id = ? AND r.node_incarnation = ? "+
			"AND r.lease_until > ? AND r.state IN (?, ?) "+
			"AND i.enabled = ? AND i.generation = r.generation AND i.release_id = r.release_id "+
			"AND n.boot_id = ? AND n.incarnation = ? AND n.heartbeat_until > ? "+
			"ORDER BY r.id ASC LIMIT 128",
		f.NodeID, f.BootID, f.Incarnation, now.UTC(),
		store.ServingReplicaPreparing, store.ServingReplicaReady, true,
		f.BootID, f.Incarnation, now.UTC()).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]*store.ServingReplica, 0, len(rows))
	for i := range rows {
		out = append(out, rows[i].toDomain())
	}
	return out, nil
}

func (d *sqlDB) RenewServingReplica(ctx context.Context, f store.ServingReplicaFence, now, until time.Time) (bool, error) {
	if !until.After(now) {
		return false, fmt.Errorf("%w: until must be in the future", store.ErrInvalidServingRecord)
	}
	var ok bool
	err := d.g.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		ok = false
		node, err := lockNode(ctx, tx, f.NodeID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil
			}
			return err
		}
		if node.BootID != f.BootID || node.Incarnation != f.NodeIncarnation ||
			!node.HeartbeatUntil.After(now) {
			return nil
		}
		inst, err := lockInstance(ctx, tx, f.InstanceID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil
			}
			return err
		}
		if !inst.Enabled || inst.Generation != f.Generation || inst.ReleaseID != f.ReleaseID {
			return nil
		}
		row, err := gorm.G[servingReplicaRow](tx, clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", f.ReplicaID).Take(ctx)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		// Every fence field must match, including the logical
		// instance the replica actually serves.
		if row.InstanceID != f.InstanceID || row.NodeID != f.NodeID || row.BootID != f.BootID ||
			row.NodeIncarnation != f.NodeIncarnation || row.Epoch != f.Epoch ||
			row.Generation != f.Generation || row.ReleaseID != f.ReleaseID {
			return nil
		}
		if row.State != store.ServingReplicaPreparing && row.State != store.ServingReplicaReady {
			return nil
		}
		if !row.LeaseUntil.After(now) {
			return nil // expired rows are never revived
		}
		// The contract caps renewals at the node heartbeat; an until
		// beyond it is invalid, not silently clamped (clamping could
		// also shrink an existing lease under a reduced heartbeat).
		if until.After(node.HeartbeatUntil) {
			return fmt.Errorf("%w: until exceeds node heartbeat", store.ErrInvalidServingRecord)
		}
		if !until.After(row.LeaseUntil) {
			ok = true // idempotent: no extension requested
			return nil
		}
		if err := tx.Model(&servingReplicaRow{}).Where("id = ?", f.ReplicaID).
			Updates(map[string]any{
				"lease_until": until.UTC(), "updated_at": now.UTC(),
			}).Error; err != nil {
			return err
		}
		ok = true
		return nil
	})
	return ok, err
}

func (d *sqlDB) ReportServingReplica(ctx context.Context, f store.ServingReplicaFence, state, errorCode string, now time.Time) (bool, error) {
	switch state {
	case store.ServingReplicaReady, store.ServingReplicaFailed, store.ServingReplicaStopped:
	default:
		return false, fmt.Errorf("%w: invalid replica state", store.ErrInvalidServingRecord)
	}
	if state != store.ServingReplicaReady && state != store.ServingReplicaStopped {
		switch errorCode {
		case "invalid_program", "invalid_input", "invalid_output", "runtime",
			"timeout", "step_limit", "memory_limit", "policy_denied", "out_of_scope":
		default:
			return false, fmt.Errorf("%w: unsupported error code", store.ErrInvalidServingRecord)
		}
	} else {
		errorCode = ""
	}
	var ok bool
	err := d.g.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		ok = false
		node, err := lockNode(ctx, tx, f.NodeID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil
			}
			return err
		}
		if node.BootID != f.BootID || node.Incarnation != f.NodeIncarnation ||
			!node.HeartbeatUntil.After(now) {
			return nil
		}
		inst, err := lockInstance(ctx, tx, f.InstanceID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil
			}
			return err
		}
		// Terminal reports acknowledge destruction on the CURRENT node
		// boot even for a disabled or superseded instance — a worker
		// dropping an old replica must be able to ack. Ready still
		// requires the live desired generation.
		if state == store.ServingReplicaReady &&
			(!inst.Enabled || inst.Generation != f.Generation || inst.ReleaseID != f.ReleaseID) {
			return nil
		}
		row, err := gorm.G[servingReplicaRow](tx, clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", f.ReplicaID).Take(ctx)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if row.InstanceID != f.InstanceID || row.NodeID != f.NodeID || row.BootID != f.BootID ||
			row.NodeIncarnation != f.NodeIncarnation || row.Epoch != f.Epoch ||
			row.Generation != f.Generation || row.ReleaseID != f.ReleaseID {
			return nil
		}
		if !row.LeaseUntil.After(now) {
			return nil
		}
		if row.State != store.ServingReplicaPreparing && row.State != store.ServingReplicaReady {
			return nil // terminal rows never change
		}
		upd := servingReplicaRow{State: state, ErrorCode: errorCode, UpdatedAt: now.UTC()}
		if state != store.ServingReplicaReady {
			// Truthful terminal report shortens the lease to now.
			upd.LeaseUntil = now.UTC()
		}
		updm := map[string]any{
			"state": upd.State, "error_code": upd.ErrorCode, "updated_at": upd.UpdatedAt,
		}
		if state != store.ServingReplicaReady {
			updm["lease_until"] = upd.LeaseUntil
		}
		if err := tx.Model(&servingReplicaRow{}).Where("id = ?", f.ReplicaID).
			Updates(updm).Error; err != nil {
			return err
		}
		current := inst.Enabled && inst.Generation == f.Generation && inst.ReleaseID == f.ReleaseID
		if state == store.ServingReplicaReady {
			if err := tx.Model(&persistentInstanceRow{}).Where("id = ?", f.InstanceID).
				Updates(map[string]any{
					"active_replica_id": f.ReplicaID, "consecutive_failures": 0,
					"retry_after": nil, "updated_at": now.UTC(),
				}).Error; err != nil {
				return err
			}
		} else if state == store.ServingReplicaFailed && current {
			// Exponential backoff on the live desired generation only:
			// an old-generation terminal must not penalize the new one.
			fails := inst.ConsecutiveFailures + 1
			if fails > 16 {
				fails = 16
			}
			delay := 5 * time.Second
			for i := 1; i < fails && delay < 5*time.Minute; i++ {
				delay *= 2
			}
			if delay > 5*time.Minute {
				delay = 5 * time.Minute
			}
			retry := now.Add(delay).UTC()
			updm := map[string]any{
				"consecutive_failures": fails, "retry_after": retry, "updated_at": now.UTC(),
			}
			if inst.ActiveReplicaID == f.ReplicaID {
				updm["active_replica_id"] = ""
			}
			if err := tx.Model(&persistentInstanceRow{}).Where("id = ?", f.InstanceID).
				Updates(updm).Error; err != nil {
				return err
			}
		} else if inst.ActiveReplicaID == f.ReplicaID {
			if err := tx.Model(&persistentInstanceRow{}).Where("id = ?", f.InstanceID).
				Updates(map[string]any{
					"active_replica_id": "", "updated_at": now.UTC(),
				}).Error; err != nil {
				return err
			}
		}
		ok = true
		return nil
	})
	return ok, err
}

// GetCurrentServingReplica returns the replica claimed at the
// instance's current last_epoch — an unlocked snapshot for the
// scheduler; Claim remains the CAS authority.
func (d *sqlDB) GetCurrentServingReplica(ctx context.Context, instanceID string) (*store.ServingReplica, error) {
	var row servingReplicaRow
	err := d.g.WithContext(ctx).Raw(
		"SELECT r.* FROM serving_replicas r "+
			"JOIN persistent_instances i ON i.id = r.instance_id AND i.last_epoch = r.epoch "+
			"WHERE r.instance_id = ?", instanceID).Take(&row).Error
	if err != nil {
		return nil, mapNotFound(err)
	}
	return row.toDomain(), nil
}

// InsertServingAuditEvents mirrors InsertAuditEvents but dedups by id
// (ON CONFLICT DO NOTHING) so deterministic report ids make retries
// idempotent. The generic path is untouched.
func (d *sqlDB) InsertServingAuditEvents(ctx context.Context, evs []store.AuditEvent) error {
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
				return fmt.Errorf("gormstore: audit event id required")
			}
			if e.WorkspaceID == "" {
				return fmt.Errorf("gormstore: audit event %s workspace_id required", e.ID)
			}
			row := auditRow{
				ID: e.ID, Ts: e.Ts.UTC(), UserID: e.UserID,
				InstanceID: e.InstanceID, WorkspaceID: e.WorkspaceID, ExecID: e.ExecID,
				Capability: string(e.Capability), Op: string(e.Op),
				Args: e.Args, Phase: string(e.Phase), Decision: string(e.Decision),
				Reason: e.Reason, Error: e.Error, DurationMs: e.DurationMs,
			}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
