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

type applicationTestCaseRow struct {
	ID                 string    `gorm:"column:id;primaryKey"`
	ApplicationID      string    `gorm:"column:application_id;not null"`
	WorkspaceID        string    `gorm:"column:workspace_id;not null"`
	Name               string    `gorm:"column:name;not null"`
	Description        string    `gorm:"column:description;not null"`
	InputJSON          string    `gorm:"column:input_json;not null"`
	InterruptsJSON     string    `gorm:"column:interrupts_json;not null"`
	ExpectStatus       string    `gorm:"column:expect_status;not null"`
	ExpectedOutputJSON string    `gorm:"column:expected_output_json;not null"`
	OutputMatchMode    string    `gorm:"column:output_match_mode;not null"`
	ExpectedErrorCode  string    `gorm:"column:expected_error_code;not null"`
	CreatedAt          time.Time `gorm:"column:created_at;not null"`
	UpdatedAt          time.Time `gorm:"column:updated_at;not null"`
}

func (applicationTestCaseRow) TableName() string { return "application_test_cases" }

func (r *applicationTestCaseRow) toDomain() *store.ApplicationTestCase {
	var intrs []store.ApplicationTestInterrupt
	if r.InterruptsJSON != "" {
		_ = json.Unmarshal([]byte(r.InterruptsJSON), &intrs)
	}
	if intrs == nil {
		intrs = []store.ApplicationTestInterrupt{}
	}
	var expOut json.RawMessage
	if r.ExpectedOutputJSON != "" {
		expOut = json.RawMessage(r.ExpectedOutputJSON)
	}
	inJSON := json.RawMessage(r.InputJSON)
	if len(inJSON) == 0 {
		inJSON = json.RawMessage(`{}`)
	}
	return &store.ApplicationTestCase{
		ID:                r.ID,
		ApplicationID:     r.ApplicationID,
		WorkspaceID:       r.WorkspaceID,
		Name:              r.Name,
		Description:       r.Description,
		Input:             inJSON,
		Interrupts:        intrs,
		ExpectStatus:      r.ExpectStatus,
		ExpectedOutput:    expOut,
		OutputMatchMode:   r.OutputMatchMode,
		ExpectedErrorCode: r.ExpectedErrorCode,
		CreatedAt:         r.CreatedAt.UTC(),
		UpdatedAt:         r.UpdatedAt.UTC(),
	}
}

func testCaseToRow(tc *store.ApplicationTestCase) (*applicationTestCaseRow, error) {
	intrs, err := json.Marshal(tc.Interrupts)
	if err != nil {
		return nil, err
	}
	return &applicationTestCaseRow{
		ID:                 tc.ID,
		ApplicationID:      tc.ApplicationID,
		WorkspaceID:        tc.WorkspaceID,
		Name:               tc.Name,
		Description:        tc.Description,
		InputJSON:          string(tc.Input),
		InterruptsJSON:     string(intrs),
		ExpectStatus:       tc.ExpectStatus,
		ExpectedOutputJSON: string(tc.ExpectedOutput),
		OutputMatchMode:    tc.OutputMatchMode,
		ExpectedErrorCode:  tc.ExpectedErrorCode,
		CreatedAt:          tc.CreatedAt.UTC(),
		UpdatedAt:          tc.UpdatedAt.UTC(),
	}, nil
}

func (d *sqlDB) CreateApplicationTestCase(ctx context.Context, tc *store.ApplicationTestCase) error {
	if err := store.ValidateApplicationTestCase(tc); err != nil {
		return err
	}
	if tc.ID == "" {
		tc.ID = store.NewID(store.PrefixApplicationTestCase)
	}
	row, err := testCaseToRow(tc)
	if err != nil {
		return err
	}
	return d.workspaceWrite(ctx, tc.WorkspaceID, func(tx *sqlDB) error {
		return tx.g.WithContext(ctx).Transaction(func(gtx *gorm.DB) error {
			var count int64
			if err := gtx.Model(&applicationTestCaseRow{}).
				Where("workspace_id = ? AND application_id = ?", tc.WorkspaceID, tc.ApplicationID).
				Count(&count).Error; err != nil {
				return err
			}
			if count >= store.MaxApplicationTestCases {
				return store.ErrInvalidServingRecord
			}
			if err := gtx.Create(row).Error; err != nil {
				return mapWriteErr(err)
			}
			// Invalidate any prior suite pass for this app since the suite changed.
			return gtx.Where("workspace_id = ? AND application_id = ?", tc.WorkspaceID, tc.ApplicationID).
				Delete(&applicationTestRunRow{}).Error
		})
	})
}

func (d *sqlDB) GetApplicationTestCase(ctx context.Context, workspaceID, appID, caseID string) (*store.ApplicationTestCase, error) {
	row, err := gorm.G[applicationTestCaseRow](d.g).
		Where("id = ? AND workspace_id = ? AND application_id = ?", caseID, workspaceID, appID).Take(ctx)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return row.toDomain(), nil
}

func (d *sqlDB) ListApplicationTestCases(ctx context.Context, workspaceID, appID string) ([]*store.ApplicationTestCase, error) {
	rows, err := gorm.G[applicationTestCaseRow](d.g).
		Where("workspace_id = ? AND application_id = ?", workspaceID, appID).
		Order("created_at ASC, id ASC").Find(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*store.ApplicationTestCase, 0, len(rows))
	for i := range rows {
		out = append(out, rows[i].toDomain())
	}
	return out, nil
}

func (d *sqlDB) UpdateApplicationTestCase(ctx context.Context, tc *store.ApplicationTestCase) (bool, error) {
	if err := store.ValidateApplicationTestCase(tc); err != nil {
		return false, err
	}
	if tc.ID == "" {
		return false, store.ErrInvalidServingRecord
	}
	row, err := testCaseToRow(tc)
	if err != nil {
		return false, err
	}
	var updated bool
	err = d.workspaceWrite(ctx, tc.WorkspaceID, func(tx *sqlDB) error {
		return tx.g.WithContext(ctx).Transaction(func(gtx *gorm.DB) error {
			res := gtx.Model(&applicationTestCaseRow{}).
				Where("id = ? AND workspace_id = ? AND application_id = ?", tc.ID, tc.WorkspaceID, tc.ApplicationID).
				Updates(map[string]any{
					"name":                 row.Name,
					"description":          row.Description,
					"input_json":           row.InputJSON,
					"interrupts_json":      row.InterruptsJSON,
					"expect_status":        row.ExpectStatus,
					"expected_output_json": row.ExpectedOutputJSON,
					"output_match_mode":    row.OutputMatchMode,
					"expected_error_code":  row.ExpectedErrorCode,
					"updated_at":           row.UpdatedAt,
				})
			if res.Error != nil {
				return mapWriteErr(res.Error)
			}
			updated = res.RowsAffected > 0
			if !updated {
				return nil
			}
			// Suite definition changed -> require a fresh run for publish.
			return gtx.Where("workspace_id = ? AND application_id = ?", tc.WorkspaceID, tc.ApplicationID).
				Delete(&applicationTestRunRow{}).Error
		})
	})
	return updated, err
}

func (d *sqlDB) DeleteApplicationTestCase(ctx context.Context, workspaceID, appID, caseID string) (bool, error) {
	var deleted bool
	err := d.workspaceWrite(ctx, workspaceID, func(tx *sqlDB) error {
		return tx.g.WithContext(ctx).Transaction(func(gtx *gorm.DB) error {
			res := gtx.Where("id = ? AND workspace_id = ? AND application_id = ?", caseID, workspaceID, appID).
				Delete(&applicationTestCaseRow{})
			if res.Error != nil {
				return res.Error
			}
			deleted = res.RowsAffected > 0
			if !deleted {
				return nil
			}
			return gtx.Where("workspace_id = ? AND application_id = ?", workspaceID, appID).
				Delete(&applicationTestRunRow{}).Error
		})
	})
	return deleted, err
}

type applicationTestRunRow struct {
	ApplicationID string    `gorm:"column:application_id;primaryKey"`
	WorkspaceID   string    `gorm:"column:workspace_id;not null"`
	Candidate     string    `gorm:"column:candidate;primaryKey"`
	Status        string    `gorm:"column:status;not null"`
	Total         int       `gorm:"column:total;not null"`
	Passed        int       `gorm:"column:passed;not null"`
	Failed        int       `gorm:"column:failed;not null"`
	DurationMs    int64     `gorm:"column:duration_ms;not null"`
	ResultsJSON   string    `gorm:"column:results_json;not null"`
	RunBy         string    `gorm:"column:run_by;not null"`
	RunAt         time.Time `gorm:"column:run_at;not null"`
}

func (applicationTestRunRow) TableName() string { return "application_test_runs" }

func (r *applicationTestRunRow) toDomain() *store.ApplicationTestSuiteRun {
	var results []store.ApplicationTestCaseResult
	if r.ResultsJSON != "" {
		_ = json.Unmarshal([]byte(r.ResultsJSON), &results)
	}
	if results == nil {
		results = []store.ApplicationTestCaseResult{}
	}
	return &store.ApplicationTestSuiteRun{
		ApplicationID: r.ApplicationID,
		WorkspaceID:   r.WorkspaceID,
		Candidate:     r.Candidate,
		Status:        r.Status,
		Total:         r.Total,
		Passed:        r.Passed,
		Failed:        r.Failed,
		DurationMs:    r.DurationMs,
		Results:       results,
		RunBy:         r.RunBy,
		RunAt:         r.RunAt.UTC(),
	}
}

func (d *sqlDB) SaveApplicationTestSuiteRun(ctx context.Context, run *store.ApplicationTestSuiteRun) error {
	if run == nil || run.ApplicationID == "" || run.WorkspaceID == "" || run.Candidate == "" || run.RunBy == "" {
		return store.ErrInvalidServingRecord
	}
	switch run.Status {
	case "passed", "failed":
	default:
		return store.ErrInvalidServingRecord
	}
	resBytes, err := json.Marshal(run.Results)
	if err != nil {
		return err
	}
	row := &applicationTestRunRow{
		ApplicationID: run.ApplicationID,
		WorkspaceID:   run.WorkspaceID,
		Candidate:     run.Candidate,
		Status:        run.Status,
		Total:         run.Total,
		Passed:        run.Passed,
		Failed:        run.Failed,
		DurationMs:    run.DurationMs,
		ResultsJSON:   string(resBytes),
		RunBy:         run.RunBy,
		RunAt:         run.RunAt.UTC(),
	}
	return d.workspaceWrite(ctx, run.WorkspaceID, func(tx *sqlDB) error {
		return tx.g.WithContext(ctx).Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "application_id"}, {Name: "candidate"}},
			UpdateAll: true,
		}).Create(row).Error
	})
}

func (d *sqlDB) GetApplicationTestSuiteRun(ctx context.Context, workspaceID, appID, candidate string) (*store.ApplicationTestSuiteRun, error) {
	row, err := gorm.G[applicationTestRunRow](d.g).
		Where("application_id = ? AND workspace_id = ? AND candidate = ?", appID, workspaceID, candidate).Take(ctx)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return row.toDomain(), nil
}
