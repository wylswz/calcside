package gormstore

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"calcside/internal/store"
)

// run.go implements store.RunStore (EP-0016). Every compound method is
// one transaction; the first statement locks the row with a no-op
// write so SQLite serializes writers before any read and Postgres
// takes the row lock. All transitions compare-and-swap on status.

type runRow struct {
	ID            string     `gorm:"primaryKey;column:id"`
	WorkspaceID   string     `gorm:"column:workspace_id"`
	ApplicationID string     `gorm:"column:application_id"`
	ReleaseID     string     `gorm:"column:release_id"`
	UserID        string     `gorm:"column:user_id"`
	TriggerKind   string     `gorm:"column:trigger_kind"`
	Status        string     `gorm:"column:status"`
	Steps         int        `gorm:"column:steps"`
	InterruptID   string     `gorm:"column:interrupt_id"`
	ErrorCode     string     `gorm:"column:error_code"`
	Output        []byte     `gorm:"column:output"`
	CreatedAt     time.Time  `gorm:"column:created_at"`
	UpdatedAt     time.Time  `gorm:"column:updated_at"`
	EndedAt       *time.Time `gorm:"column:ended_at"`
}

func (runRow) TableName() string { return "runs" }

type runStepRow struct {
	RunID       string     `gorm:"primaryKey;column:run_id"`
	Seq         int        `gorm:"primaryKey;column:seq"`
	Attempt     int        `gorm:"primaryKey;column:attempt"`
	Entry       string     `gorm:"column:entry"`
	InterruptID string     `gorm:"column:interrupt_id"`
	Status      string     `gorm:"column:status"`
	ErrorCode   string     `gorm:"column:error_code"`
	DurationMs  int64      `gorm:"column:duration_ms"`
	StartedAt   time.Time  `gorm:"column:started_at"`
	EndedAt     *time.Time `gorm:"column:ended_at"`
}

func (runStepRow) TableName() string { return "run_steps" }

type interruptRow struct {
	ID                   string     `gorm:"primaryKey;column:id"`
	WorkspaceID          string     `gorm:"column:workspace_id"`
	ApplicationID        string     `gorm:"column:application_id"`
	RunID                string     `gorm:"column:run_id"`
	StepSeq              int        `gorm:"column:step_seq"`
	Status               string     `gorm:"column:status"`
	Handler              string     `gorm:"column:handler"`
	OnExpire             string     `gorm:"column:on_expire"`
	Context              []byte     `gorm:"column:context"`
	ExpiresAt            *time.Time `gorm:"column:expires_at"`
	Event                []byte     `gorm:"column:event"`
	ResolvedBy           string     `gorm:"column:resolved_by"`
	ResolvedAt           *time.Time `gorm:"column:resolved_at"`
	KeyID                string     `gorm:"column:key_id"`
	DispatchState        string     `gorm:"column:dispatch_state"`
	DispatchAfter        *time.Time `gorm:"column:dispatch_after"`
	DispatchClaimedUntil *time.Time `gorm:"column:dispatch_claimed_until"`
	DispatchAttempts     int        `gorm:"column:dispatch_attempts"`
	Attempt              int        `gorm:"column:attempt"`
	CreatedAt            time.Time  `gorm:"column:created_at"`
	UpdatedAt            time.Time  `gorm:"column:updated_at"`
}

func (interruptRow) TableName() string { return "interrupts" }

// --- conversions ---

func (r *runRow) toDomain() *store.Run {
	out := &store.Run{
		ID: r.ID, WorkspaceID: r.WorkspaceID, ApplicationID: r.ApplicationID,
		ReleaseID: r.ReleaseID, UserID: r.UserID, Trigger: r.TriggerKind,
		Status: r.Status, Steps: r.Steps, InterruptID: r.InterruptID,
		ErrorCode: r.ErrorCode, Output: r.Output,
		CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
	}
	if r.EndedAt != nil {
		e := r.EndedAt.UTC()
		out.EndedAt = &e
	}
	return out
}

func runToRow(r *store.Run) *runRow {
	return &runRow{
		ID: r.ID, WorkspaceID: r.WorkspaceID, ApplicationID: r.ApplicationID,
		ReleaseID: r.ReleaseID, UserID: r.UserID, TriggerKind: r.Trigger,
		Status: r.Status, Steps: r.Steps, InterruptID: r.InterruptID,
		ErrorCode: r.ErrorCode, Output: r.Output,
		CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(), EndedAt: r.EndedAt,
	}
}

func (r *runStepRow) toDomain() *store.RunStep {
	out := &store.RunStep{
		RunID: r.RunID, Seq: r.Seq, Attempt: r.Attempt, Entry: r.Entry,
		InterruptID: r.InterruptID, Status: r.Status, ErrorCode: r.ErrorCode,
		DurationMs: r.DurationMs, StartedAt: r.StartedAt.UTC(),
	}
	if r.EndedAt != nil {
		e := r.EndedAt.UTC()
		out.EndedAt = &e
	}
	return out
}

func stepToRow(s *store.RunStep) *runStepRow {
	return &runStepRow{
		RunID: s.RunID, Seq: s.Seq, Attempt: s.Attempt, Entry: s.Entry,
		InterruptID: s.InterruptID, Status: s.Status, ErrorCode: s.ErrorCode,
		DurationMs: s.DurationMs, StartedAt: s.StartedAt.UTC(), EndedAt: s.EndedAt,
	}
}

func (r *interruptRow) toDomain() *store.Interrupt {
	out := &store.Interrupt{
		ID: r.ID, WorkspaceID: r.WorkspaceID, ApplicationID: r.ApplicationID,
		RunID: r.RunID, StepSeq: r.StepSeq, Status: r.Status,
		Handler: r.Handler, OnExpire: r.OnExpire,
		Context: r.Context, Event: r.Event, ResolvedBy: r.ResolvedBy,
		KeyID: r.KeyID, DispatchState: r.DispatchState,
		DispatchAttempts: r.DispatchAttempts, Attempt: r.Attempt,
		CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
	}
	utcPtr := func(t *time.Time) *time.Time {
		if t == nil {
			return nil
		}
		u := t.UTC()
		return &u
	}
	out.ExpiresAt, out.ResolvedAt = utcPtr(r.ExpiresAt), utcPtr(r.ResolvedAt)
	out.DispatchAfter, out.DispatchClaimedUntil = utcPtr(r.DispatchAfter), utcPtr(r.DispatchClaimedUntil)
	return out
}

func interruptToRow(i *store.Interrupt) (*interruptRow, error) {
	return &interruptRow{
		ID: i.ID, WorkspaceID: i.WorkspaceID, ApplicationID: i.ApplicationID,
		RunID: i.RunID, StepSeq: i.StepSeq, Status: i.Status,
		Handler: i.Handler, OnExpire: i.OnExpire,
		Context: i.Context, ExpiresAt: i.ExpiresAt,
		Event: i.Event, ResolvedBy: i.ResolvedBy, ResolvedAt: i.ResolvedAt,
		KeyID: i.KeyID, DispatchState: i.DispatchState,
		DispatchAfter: i.DispatchAfter, DispatchClaimedUntil: i.DispatchClaimedUntil,
		DispatchAttempts: i.DispatchAttempts, Attempt: i.Attempt,
		CreatedAt: i.CreatedAt.UTC(), UpdatedAt: i.UpdatedAt.UTC(),
	}, nil
}

// --- locks ---

// lockRun takes the row write lock (no-op write first) and returns the
// current row.
func lockRun(ctx context.Context, tx *gorm.DB, id string) (*runRow, error) {
	if err := tx.Exec("UPDATE runs SET updated_at = updated_at WHERE id = ?", id).Error; err != nil {
		return nil, err
	}
	row, err := gorm.G[runRow](tx, clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", id).Take(ctx)
	if err != nil {
		return nil, mapNotFound(err)
	}
	return &row, nil
}

func lockInterrupt(ctx context.Context, tx *gorm.DB, id string) (*interruptRow, error) {
	if err := tx.Exec("UPDATE interrupts SET updated_at = updated_at WHERE id = ?", id).Error; err != nil {
		return nil, err
	}
	row, err := gorm.G[interruptRow](tx, clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", id).Take(ctx)
	if err != nil {
		return nil, mapNotFound(err)
	}
	return &row, nil
}

func lockStep(ctx context.Context, tx *gorm.DB, runID string, seq, attempt int) (*runStepRow, error) {
	if err := tx.Exec("UPDATE run_steps SET duration_ms = duration_ms WHERE run_id = ? AND seq = ? AND attempt = ?",
		runID, seq, attempt).Error; err != nil {
		return nil, err
	}
	row, err := gorm.G[runStepRow](tx, clause.Locking{Strength: "UPDATE"}).
		Where("run_id = ? AND seq = ? AND attempt = ?", runID, seq, attempt).Take(ctx)
	if err != nil {
		return nil, mapNotFound(err)
	}
	return &row, nil
}

// revokeBoundKeysTx revokes all unrevoked keys bound to the given ids
// inside the current transaction.
func revokeBoundKeysTx(tx *gorm.DB, ids []string, now time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	return tx.Model(&keyRow{}).
		Where("bound_id IN ? AND revoked_at IS NULL", ids).
		Update("revoked_at", now.UTC()).Error
}

func runInterruptIDs(ctx context.Context, tx *gorm.DB, runID string) ([]string, error) {
	var ids []string
	err := tx.Model(&interruptRow{}).Where("run_id = ?", runID).Pluck("id", &ids).Error
	return ids, err
}

// --- runs ---

func (d *sqlDB) StartRun(ctx context.Context, run *store.Run, step *store.RunStep, slots []*store.Interrupt) error {
	if run == nil || step == nil || run.ID == "" || run.WorkspaceID == "" ||
		step.RunID != run.ID {
		return store.ErrInvalidServingRecord
	}
	return d.g.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := gorm.G[runRow](tx).Create(ctx, runToRow(run)); err != nil {
			return mapWriteErr(err)
		}
		if err := gorm.G[runStepRow](tx).Create(ctx, stepToRow(step)); err != nil {
			return mapWriteErr(err)
		}
		for _, s := range slots {
			row, err := interruptToRow(s)
			if err != nil {
				return err
			}
			if err := gorm.G[interruptRow](tx).Create(ctx, row); err != nil {
				return mapWriteErr(err)
			}
		}
		return nil
	})
}

func (d *sqlDB) GetRun(ctx context.Context, id string) (*store.Run, error) {
	row, err := gorm.G[runRow](d.g).Where("id = ?", id).Take(ctx)
	if err != nil {
		return nil, mapNotFound(err)
	}
	return row.toDomain(), nil
}

// applyCursor adds the newest-first (created_at, id DESC) cursor
// predicate when f.Before names an existing row in the workspace.
func runCursor(tx *gorm.DB, ctx context.Context, workspaceID, before string) (createdAt time.Time, id string, err error) {
	row, err := gorm.G[runRow](tx).Where("id = ? AND workspace_id = ?", before, workspaceID).Take(ctx)
	if err != nil {
		return time.Time{}, "", mapNotFound(err)
	}
	return row.CreatedAt, row.ID, nil
}

func runListLimit(limit int) (int, error) {
	if limit < 0 || limit > 200 {
		return 0, store.ErrInvalidServingRecord
	}
	if limit == 0 {
		return 50, nil
	}
	return limit, nil
}

func (d *sqlDB) ListRuns(ctx context.Context, f store.RunFilter) ([]*store.Run, error) {
	if f.WorkspaceID == "" {
		return nil, store.ErrInvalidServingRecord
	}
	limit, err := runListLimit(f.Limit)
	if err != nil {
		return nil, err
	}
	q := gorm.G[runRow](d.g).Where("workspace_id = ?", f.WorkspaceID)
	if f.ApplicationID != "" {
		q = q.Where("application_id = ?", f.ApplicationID)
	}
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.Before != "" {
		createdAt, id, err := runCursor(d.g, ctx, f.WorkspaceID, f.Before)
		if err != nil {
			return nil, err
		}
		q = q.Where("created_at < ? OR (created_at = ? AND id < ?)", createdAt, createdAt, id)
	}
	rows, err := q.Order("created_at DESC, id DESC").Limit(limit).Find(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*store.Run, 0, len(rows))
	for i := range rows {
		out = append(out, rows[i].toDomain())
	}
	return out, nil
}

func (d *sqlDB) ListRunSteps(ctx context.Context, runID string) ([]*store.RunStep, error) {
	rows, err := gorm.G[runStepRow](d.g).Where("run_id = ?", runID).
		Order("seq, attempt").Find(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*store.RunStep, 0, len(rows))
	for i := range rows {
		out = append(out, rows[i].toDomain())
	}
	return out, nil
}

// --- interrupts ---

func (d *sqlDB) GetInterrupt(ctx context.Context, id string) (*store.Interrupt, error) {
	row, err := gorm.G[interruptRow](d.g).Where("id = ?", id).Take(ctx)
	if err != nil {
		return nil, mapNotFound(err)
	}
	return row.toDomain(), nil
}

func (d *sqlDB) ListInterrupts(ctx context.Context, f store.InterruptFilter) ([]*store.Interrupt, error) {
	if f.WorkspaceID == "" {
		return nil, store.ErrInvalidServingRecord
	}
	limit, err := runListLimit(f.Limit)
	if err != nil {
		return nil, err
	}
	q := gorm.G[interruptRow](d.g).Where("workspace_id = ?", f.WorkspaceID)
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.RunID != "" {
		q = q.Where("run_id = ?", f.RunID)
	}
	if f.ApplicationID != "" {
		q = q.Where("application_id = ?", f.ApplicationID)
	}
	if f.Before != "" {
		row, err := gorm.G[interruptRow](d.g).
			Where("id = ? AND workspace_id = ?", f.Before, f.WorkspaceID).Take(ctx)
		if err != nil {
			return nil, mapNotFound(err)
		}
		q = q.Where("created_at < ? OR (created_at = ? AND id < ?)", row.CreatedAt, row.CreatedAt, row.ID)
	}
	rows, err := q.Order("created_at DESC, id DESC").Limit(limit).Find(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*store.Interrupt, 0, len(rows))
	for i := range rows {
		out = append(out, rows[i].toDomain())
	}
	return out, nil
}

// --- finish step ---

func (d *sqlDB) FinishStep(ctx context.Context, out store.StepOutcome, acceptEarly func(event []byte) bool) error {
	if out.Now.IsZero() {
		return store.ErrInvalidServingRecord
	}
	if out.Status == store.StepWaiting && out.Wait == nil {
		return store.ErrInvalidServingRecord
	}
	now := out.Now.UTC()
	return d.g.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		run, err := lockRun(ctx, tx, out.RunID)
		if err != nil {
			return err
		}
		step, err := lockStep(ctx, tx, out.RunID, out.Seq, out.Attempt)
		if err != nil {
			return err
		}
		if step.Status != store.StepRunning {
			return store.ErrConflict
		}
		if err := tx.Model(&runStepRow{}).
			Where("run_id = ? AND seq = ? AND attempt = ?", out.RunID, out.Seq, out.Attempt).
			Updates(map[string]any{
				"status": out.Status, "error_code": out.ErrorCode,
				"duration_ms": out.DurationMs, "ended_at": now,
			}).Error; err != nil {
			return err
		}

		// Cancel every other still-reserved slot of this step and
		// revoke their bound keys.
		waitID := ""
		if out.Wait != nil {
			waitID = out.Wait.InterruptID
		}
		var siblings []string
		if err := tx.Model(&interruptRow{}).
			Where("run_id = ? AND step_seq = ? AND status = ? AND id <> ?",
				out.RunID, out.Seq, store.InterruptSlotReserved, waitID).
			Pluck("id", &siblings).Error; err != nil {
			return err
		}
		if len(siblings) > 0 {
			if err := tx.Model(&interruptRow{}).
				Where("id IN ? AND status = ?", siblings, store.InterruptSlotReserved).
				Updates(map[string]any{"status": store.InterruptCancelled, "updated_at": now}).Error; err != nil {
				return err
			}
			if err := revokeBoundKeysTx(tx, siblings, now); err != nil {
				return err
			}
		}

		switch out.Status {
		case store.StepWaiting:
			if err := commitWait(ctx, tx, out, now, acceptEarly); err != nil {
				return err
			}
			if err := tx.Model(&runRow{}).Where("id = ?", out.RunID).
				Updates(map[string]any{
					"status": store.RunWaiting, "interrupt_id": waitID, "updated_at": now,
				}).Error; err != nil {
				return err
			}
		case store.StepSucceeded, store.StepFailed:
			updates := map[string]any{
				"status": out.Status, "error_code": out.ErrorCode,
				"interrupt_id": "", "ended_at": now, "updated_at": now,
			}
			if out.Status == store.StepSucceeded {
				updates["output"] = out.Output
			}
			if err := tx.Model(&runRow{}).Where("id = ?", out.RunID).Updates(updates).Error; err != nil {
				return err
			}
			ids, err := runInterruptIDs(ctx, tx, out.RunID)
			if err != nil {
				return err
			}
			if err := revokeBoundKeysTx(tx, ids, now); err != nil {
				return err
			}
			if err := tx.Model(&interruptRow{}).
				Where("run_id = ? AND status = ?", out.RunID, store.InterruptPending).
				Updates(map[string]any{"status": store.InterruptCancelled, "updated_at": now}).Error; err != nil {
				return err
			}
		case store.StepUnknown:
			if err := tx.Model(&runRow{}).Where("id = ?", out.RunID).
				Updates(map[string]any{"status": store.RunUnknown, "updated_at": now}).Error; err != nil {
				return err
			}
		default:
			return store.ErrInvalidServingRecord
		}

		_ = run
		if out.DispatchedInterruptID != "" && out.Status != store.StepUnknown {
			if err := tx.Model(&interruptRow{}).
				Where("id = ?", out.DispatchedInterruptID).
				Updates(map[string]any{"dispatch_state": store.DispatchDone, "updated_at": now}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// commitWait applies the waiting branch of FinishStep inside the tx:
// the reserved slot becomes pending (or resolved, when an accepted
// early resolution already arrived).
func commitWait(ctx context.Context, tx *gorm.DB, out store.StepOutcome, now time.Time, acceptEarly func([]byte) bool) error {
	w := out.Wait
	intr, err := lockInterrupt(ctx, tx, w.InterruptID)
	if err != nil {
		return err
	}
	if intr.RunID != out.RunID || intr.StepSeq != out.Seq || intr.Status != store.InterruptSlotReserved {
		return store.ErrConflict
	}
	updates := map[string]any{
		"handler": w.Handler, "on_expire": w.OnExpire, "context": w.Context,
		"expires_at": w.ExpiresAt,
		"status":     store.InterruptPending, "updated_at": now,
	}
	// An early resolution stored while reserved is committed or
	// discarded inside the same transaction.
	if intr.ResolvedAt != nil {
		if acceptEarly != nil && acceptEarly(intr.Event) {
			updates["status"] = store.InterruptResolved
			updates["dispatch_state"] = store.DispatchQueued
			updates["dispatch_after"] = now
			updates["attempt"] = 1
		} else {
			updates["event"] = nil
			updates["resolved_by"] = ""
			updates["resolved_at"] = nil
		}
	}
	return tx.Model(&interruptRow{}).Where("id = ?", w.InterruptID).Updates(updates).Error
}

// --- resolution / deadlines ---

func (d *sqlDB) ResolveInterrupt(ctx context.Context, id string, event []byte, resolvedBy string, now time.Time) (early bool, err error) {
	err = d.g.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, err := lockInterrupt(ctx, tx, id)
		if err != nil {
			return err
		}
		switch {
		case row.Status == store.InterruptPending:
			res := tx.Model(&interruptRow{}).Where("id = ? AND status = ?", id, store.InterruptPending).
				Updates(map[string]any{
					"status": store.InterruptResolved, "event": event,
					"resolved_by": resolvedBy, "resolved_at": now.UTC(),
					"dispatch_state": store.DispatchQueued, "dispatch_after": now.UTC(),
					"attempt": 1, "updated_at": now.UTC(),
				})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				return store.ErrConflict
			}
			early = false
		case row.Status == store.InterruptSlotReserved && row.ResolvedAt == nil:
			res := tx.Model(&interruptRow{}).Where("id = ? AND status = ? AND resolved_at IS NULL", id, store.InterruptSlotReserved).
				Updates(map[string]any{
					"event": event, "resolved_by": resolvedBy,
					"resolved_at": now.UTC(), "updated_at": now.UTC(),
				})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				return store.ErrConflict
			}
			early = true
		default:
			return store.ErrConflict
		}
		return nil
	})
	return early, err
}

func (d *sqlDB) ListDueInterrupts(ctx context.Context, now time.Time, limit int) ([]*store.Interrupt, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := gorm.G[interruptRow](d.g).
		Where("status = ? AND expires_at <= ?", store.InterruptPending, now.UTC()).
		Order("id").Limit(limit).Find(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*store.Interrupt, 0, len(rows))
	for i := range rows {
		out = append(out, rows[i].toDomain())
	}
	return out, nil
}

func (d *sqlDB) ExpireInterrupt(ctx context.Context, id string, now time.Time) (bool, error) {
	var ok bool
	err := d.g.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		ok = false
		row, err := lockInterrupt(ctx, tx, id)
		if err != nil {
			return err
		}
		if row.Status != store.InterruptPending || row.ExpiresAt == nil || row.ExpiresAt.After(now) {
			return nil
		}
		updates := map[string]any{
			"status": store.InterruptExpired, "resolved_by": store.ResolvedByExpiry,
			"resolved_at": now.UTC(), "updated_at": now.UTC(),
		}
		if row.OnExpire != "" {
			updates["dispatch_state"] = store.DispatchQueued
			updates["dispatch_after"] = now.UTC()
			updates["attempt"] = 1
		}
		res := tx.Model(&interruptRow{}).
			Where("id = ? AND status = ?", id, store.InterruptPending).Updates(updates)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil
		}
		if row.OnExpire == "" {
			// No expiry handler: the run ends expired and its keys die.
			run, err := lockRun(ctx, tx, row.RunID)
			if err != nil {
				return err
			}
			if err := tx.Model(&runRow{}).Where("id = ?", run.ID).
				Updates(map[string]any{
					"status": store.RunExpired, "interrupt_id": "",
					"ended_at": now.UTC(), "updated_at": now.UTC(),
				}).Error; err != nil {
				return err
			}
			ids, err := runInterruptIDs(ctx, tx, row.RunID)
			if err != nil {
				return err
			}
			if err := revokeBoundKeysTx(tx, ids, now); err != nil {
				return err
			}
		}
		ok = true
		return nil
	})
	return ok, err
}

// --- dispatch ---

func (d *sqlDB) ClaimDispatch(ctx context.Context, now, claimUntil time.Time) (*store.Interrupt, error) {
	// Candidate selection is read-only outside any transaction; the
	// claim itself is a single compare-and-swap UPDATE, so concurrent
	// claimers never hold a read snapshot open while writing.
	var ids []string
	if err := d.g.WithContext(ctx).Model(&interruptRow{}).
		Where("dispatch_state = ? AND dispatch_after <= ?", store.DispatchQueued, now.UTC()).
		Order("dispatch_after, id").Limit(8).Pluck("id", &ids).Error; err != nil {
		return nil, err
	}
	for _, id := range ids {
		res := d.g.WithContext(ctx).Exec(
			"UPDATE interrupts SET dispatch_state = ?, dispatch_claimed_until = ?, updated_at = ? "+
				"WHERE id = ? AND dispatch_state = ?",
			store.DispatchRunning, claimUntil.UTC(), now.UTC(), id, store.DispatchQueued)
		if res.Error != nil {
			return nil, res.Error
		}
		if res.RowsAffected != 1 {
			continue // another claimer won this candidate
		}
		row, err := gorm.G[interruptRow](d.g).Where("id = ?", id).Take(ctx)
		if err != nil {
			return nil, mapNotFound(err)
		}
		return row.toDomain(), nil
	}
	return nil, store.ErrNotFound
}

func (d *sqlDB) DeferDispatch(ctx context.Context, id string, after time.Time) error {
	res := d.g.WithContext(ctx).Exec(
		"UPDATE interrupts SET dispatch_state = ?, dispatch_after = ?, dispatch_claimed_until = NULL, "+
			"dispatch_attempts = dispatch_attempts + 1, updated_at = ? WHERE id = ? AND dispatch_state = ?",
		store.DispatchQueued, after.UTC(), time.Now().UTC(), id, store.DispatchRunning)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return store.ErrConflict
	}
	return nil
}

func (d *sqlDB) StartHandlerStep(ctx context.Context, interruptID string, step *store.RunStep, slots []*store.Interrupt, now time.Time) error {
	if step == nil || step.Attempt < 1 {
		return store.ErrInvalidServingRecord
	}
	return d.g.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		intr, err := lockInterrupt(ctx, tx, interruptID)
		if err != nil {
			return err
		}
		if intr.DispatchState != store.DispatchRunning || step.RunID != intr.RunID {
			return store.ErrConflict
		}
		run, err := lockRun(ctx, tx, intr.RunID)
		if err != nil {
			return err
		}
		if run.Status != store.RunWaiting || run.InterruptID != interruptID {
			return store.ErrConflict
		}
		steps := run.Steps
		if step.Attempt == 1 {
			steps++
		}
		if err := tx.Model(&runRow{}).Where("id = ?", run.ID).
			Updates(map[string]any{
				"status": store.RunRunning, "interrupt_id": "",
				"steps": steps, "updated_at": now.UTC(),
			}).Error; err != nil {
			return err
		}
		if err := gorm.G[runStepRow](tx).Create(ctx, stepToRow(step)); err != nil {
			return mapWriteErr(err)
		}
		for _, s := range slots {
			row, err := interruptToRow(s)
			if err != nil {
				return err
			}
			if err := gorm.G[interruptRow](tx).Create(ctx, row); err != nil {
				return mapWriteErr(err)
			}
		}
		return nil
	})
}

func (d *sqlDB) ListStaleDispatches(ctx context.Context, now time.Time, limit int) ([]*store.Interrupt, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := gorm.G[interruptRow](d.g).
		Where("dispatch_state = ? AND dispatch_claimed_until < ?", store.DispatchRunning, now.UTC()).
		Order("id").Limit(limit).Find(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*store.Interrupt, 0, len(rows))
	for i := range rows {
		out = append(out, rows[i].toDomain())
	}
	return out, nil
}

func (d *sqlDB) RecoverStaleDispatch(ctx context.Context, id string, now time.Time) (started bool, err error) {
	err = d.g.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		intr, err := lockInterrupt(ctx, tx, id)
		if err != nil {
			return err
		}
		if intr.DispatchState != store.DispatchRunning ||
			intr.DispatchClaimedUntil == nil || !intr.DispatchClaimedUntil.Before(now) {
			return nil // lost the race; nothing to recover
		}
		// Did the handler step for the current attempt ever start?
		step, err := gorm.G[runStepRow](tx).
			Where("run_id = ? AND interrupt_id = ? AND attempt = ? AND status = ?",
				intr.RunID, intr.ID, intr.Attempt, store.StepRunning).Take(ctx)
		switch {
		case err == nil:
			// The step started: its outcome is unknown, so the run is
			// unknown and the dispatch idles for explicit retry.
			run, err := lockRun(ctx, tx, intr.RunID)
			if err != nil {
				return err
			}
			if err := tx.Model(&runStepRow{}).
				Where("run_id = ? AND seq = ? AND attempt = ?", step.RunID, step.Seq, step.Attempt).
				Updates(map[string]any{
					"status": store.StepUnknown, "ended_at": now.UTC(),
				}).Error; err != nil {
				return err
			}
			if err := tx.Model(&runRow{}).Where("id = ?", run.ID).
				Updates(map[string]any{
					"status": store.RunUnknown, "updated_at": now.UTC(),
				}).Error; err != nil {
				return err
			}
			if err := tx.Model(&interruptRow{}).Where("id = ?", intr.ID).
				Updates(map[string]any{
					"dispatch_state": "", "dispatch_claimed_until": nil, "updated_at": now.UTC(),
				}).Error; err != nil {
				return err
			}
			started = true
		case errors.Is(err, gorm.ErrRecordNotFound):
			// Never started: requeue for a fresh claim.
			if err := tx.Model(&interruptRow{}).Where("id = ?", intr.ID).
				Updates(map[string]any{
					"dispatch_state": store.DispatchQueued, "dispatch_after": now.UTC(),
					"dispatch_claimed_until": nil, "updated_at": now.UTC(),
				}).Error; err != nil {
				return err
			}
		default:
			return err
		}
		return nil
	})
	return started, err
}

// --- run lifecycle ---

func (d *sqlDB) CancelRun(ctx context.Context, runID string, now time.Time) (bool, error) {
	var ok bool
	err := d.g.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		ok = false
		run, err := lockRun(ctx, tx, runID)
		if err != nil {
			return err
		}
		if run.Status != store.RunWaiting && run.Status != store.RunUnknown {
			return nil
		}
		if err := tx.Model(&runRow{}).Where("id = ?", runID).
			Updates(map[string]any{
				"status": store.RunCancelled, "interrupt_id": "",
				"ended_at": now.UTC(), "updated_at": now.UTC(),
			}).Error; err != nil {
			return err
		}
		if err := tx.Model(&interruptRow{}).
			Where("run_id = ? AND status = ?", runID, store.InterruptPending).
			Updates(map[string]any{"status": store.InterruptCancelled, "updated_at": now.UTC()}).Error; err != nil {
			return err
		}
		// A resolved interrupt whose dispatch never started is done.
		if err := tx.Model(&interruptRow{}).
			Where("run_id = ? AND dispatch_state = ?", runID, store.DispatchQueued).
			Updates(map[string]any{"dispatch_state": store.DispatchDone, "updated_at": now.UTC()}).Error; err != nil {
			return err
		}
		ids, err := runInterruptIDs(ctx, tx, runID)
		if err != nil {
			return err
		}
		if err := revokeBoundKeysTx(tx, ids, now); err != nil {
			return err
		}
		ok = true
		return nil
	})
	return ok, err
}

func (d *sqlDB) RetryRun(ctx context.Context, runID string, now time.Time) (bool, error) {
	var ok bool
	err := d.g.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		ok = false
		run, err := lockRun(ctx, tx, runID)
		if err != nil {
			return err
		}
		if run.Status != store.RunUnknown {
			return nil
		}
		// The latest step decides: only a handler step carries an
		// interrupt that can be re-dispatched.
		step, err := gorm.G[runStepRow](tx).Where("run_id = ?", runID).
			Order("seq DESC, attempt DESC").Take(ctx)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if step.InterruptID == "" {
			return nil // main-step unknown
		}
		intr, err := lockInterrupt(ctx, tx, step.InterruptID)
		if err != nil {
			return err
		}
		if err := tx.Model(&interruptRow{}).Where("id = ?", intr.ID).
			Updates(map[string]any{
				"dispatch_state": store.DispatchQueued, "dispatch_after": now.UTC(),
				"dispatch_claimed_until": nil, "attempt": intr.Attempt + 1,
				"updated_at": now.UTC(),
			}).Error; err != nil {
			return err
		}
		if err := tx.Model(&runRow{}).Where("id = ?", runID).
			Updates(map[string]any{
				"status": store.RunWaiting, "interrupt_id": intr.ID, "updated_at": now.UTC(),
			}).Error; err != nil {
			return err
		}
		ok = true
		return nil
	})
	return ok, err
}
