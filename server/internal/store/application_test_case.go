package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// PrefixApplicationTestCase identifies persisted application test cases.
const PrefixApplicationTestCase IDPrefix = "atc_"

// ApplicationTestInterrupt defines one expected interrupt pause and its
// simulated resume (or expiry) input in a multi-turn test case.
type ApplicationTestInterrupt struct {
	// ExpectHandler, when non-empty, asserts that interrupt.wait targeted
	// this exact handler function name.
	ExpectHandler string `json:"expect_handler,omitempty"`
	// ExpectContext, when non-empty, asserts the JSON context emitted by
	// interrupt.wait matches according to ContextMatchMode.
	ExpectContext json.RawMessage `json:"expect_context,omitempty"`
	// ContextMatchMode is "exact" (default) or "subset".
	ContextMatchMode string `json:"context_match_mode,omitempty"`
	// Resolution is "resolved" (default) or "expired".
	Resolution string `json:"resolution,omitempty"`
	// Event is the JSON object passed as ctx["event"] when Resolution == "resolved".
	Event json.RawMessage `json:"event,omitempty"`
}

// ApplicationTestCase is one saved executable specification for an application:
// initial input, optional multi-turn interrupt expectations & resume inputs,
// and final output (or expected error) assertion.
type ApplicationTestCase struct {
	ID                string                     `json:"id"`
	ApplicationID     string                     `json:"application_id"`
	WorkspaceID       string                     `json:"workspace_id"`
	Name              string                     `json:"name"`
	Description       string                     `json:"description,omitempty"`
	Input             json.RawMessage            `json:"input"`
	Interrupts        []ApplicationTestInterrupt `json:"interrupts"`
	ExpectStatus      string                     `json:"expect_status"` // "succeeded" | "error"
	ExpectedOutput    json.RawMessage            `json:"expected_output,omitempty"`
	OutputMatchMode   string                     `json:"output_match_mode"` // "exact" | "subset"
	ExpectedErrorCode string                     `json:"expected_error_code,omitempty"`
	CreatedAt         time.Time                  `json:"created_at"`
	UpdatedAt         time.Time                  `json:"updated_at"`
}

// ApplicationTestTurnResult captures one turn (initial main or interrupt)
// during a test case execution.
type ApplicationTestTurnResult struct {
	Turn            int    `json:"turn"`
	Entry           string `json:"entry"`
	Status          string `json:"status"` // "passed" | "failed" | "unexpected_interrupt"
	DurationMs      int64  `json:"duration_ms"`
	Steps           int64  `json:"steps"`
	ActualHandler   string `json:"actual_handler,omitempty"`
	ActualOnExpire  string `json:"actual_on_expire,omitempty"`
	ExpectedHandler string `json:"expected_handler,omitempty"`
	ExpectedContext string `json:"expected_context,omitempty"`
	ActualContext   string `json:"actual_context,omitempty"`
	Resolution      string `json:"resolution,omitempty"`
	EventJSON       string `json:"event_json,omitempty"`
	ErrorMessage    string `json:"error_message,omitempty"`
}

// ApplicationTestCaseResult captures the outcome of running one test case.
type ApplicationTestCaseResult struct {
	CaseID            string                      `json:"case_id"`
	CaseName          string                      `json:"case_name"`
	Status            string                      `json:"status"` // "passed" | "failed"
	DurationMs        int64                       `json:"duration_ms"`
	Steps             int64                       `json:"steps"`
	ErrorMessage      string                      `json:"error_message,omitempty"`
	ErrorCode         string                      `json:"error_code,omitempty"`
	ExpectedOutput    string                      `json:"expected_output,omitempty"`
	ActualOutput      string                      `json:"actual_output,omitempty"`
	ExpectedErrorCode string                      `json:"expected_error_code,omitempty"`
	Turns             []ApplicationTestTurnResult `json:"turns"`
}

// ApplicationTestSuiteRun records the bounded result of running an
// application's full test suite against one exact candidate digest.
type ApplicationTestSuiteRun struct {
	ApplicationID string                      `json:"application_id"`
	WorkspaceID   string                      `json:"workspace_id"`
	Candidate     string                      `json:"candidate"`
	Status        string                      `json:"status"` // "passed" | "failed"
	Total         int                         `json:"total"`
	Passed        int                         `json:"passed"`
	Failed        int                         `json:"failed"`
	DurationMs    int64                       `json:"duration_ms"`
	Results       []ApplicationTestCaseResult `json:"results"`
	RunBy         string                      `json:"run_by"`
	RunAt         time.Time                   `json:"run_at"`
}

const (
	MaxApplicationTestCases      = 64
	MaxApplicationTestInterrupts = 16
	MaxTestCasePayloadBytes      = 64 << 10
)

// ValidateApplicationTestCase normalizes defaults and validates bounds.
func ValidateApplicationTestCase(tc *ApplicationTestCase) error {
	if tc == nil {
		return fmt.Errorf("%w: nil test case", ErrInvalidServingRecord)
	}
	if tc.ApplicationID == "" || tc.WorkspaceID == "" {
		return fmt.Errorf("%w: application and workspace required", ErrInvalidServingRecord)
	}
	if !utf8.ValidString(tc.Name) || strings.TrimSpace(tc.Name) == "" || utf8.RuneCountInString(tc.Name) > 128 {
		return fmt.Errorf("%w: name required, max 128 runes", ErrInvalidServingRecord)
	}
	if !utf8.ValidString(tc.Description) || utf8.RuneCountInString(tc.Description) > 500 {
		return fmt.Errorf("%w: description max 500 runes", ErrInvalidServingRecord)
	}
	if len(strings.TrimSpace(string(tc.Input))) == 0 {
		tc.Input = json.RawMessage(`{}`)
	}
	if len(tc.Input) > MaxTestCasePayloadBytes || !json.Valid(tc.Input) {
		return fmt.Errorf("%w: input must be valid JSON <= 64 KiB", ErrInvalidServingRecord)
	}
	if tc.ExpectStatus == "" {
		tc.ExpectStatus = "succeeded"
	}
	switch tc.ExpectStatus {
	case "succeeded", "error":
	default:
		return fmt.Errorf("%w: expect_status must be succeeded or error", ErrInvalidServingRecord)
	}
	if tc.OutputMatchMode == "" {
		tc.OutputMatchMode = "exact"
	}
	switch tc.OutputMatchMode {
	case "exact", "subset":
	default:
		return fmt.Errorf("%w: output_match_mode must be exact or subset", ErrInvalidServingRecord)
	}
	if len(strings.TrimSpace(string(tc.ExpectedOutput))) > 0 {
		if len(tc.ExpectedOutput) > MaxTestCasePayloadBytes || !json.Valid(tc.ExpectedOutput) {
			return fmt.Errorf("%w: expected_output must be valid JSON <= 64 KiB", ErrInvalidServingRecord)
		}
	} else {
		tc.ExpectedOutput = nil
	}
	if len(tc.ExpectedErrorCode) > 64 {
		return fmt.Errorf("%w: expected_error_code max 64 chars", ErrInvalidServingRecord)
	}
	if len(tc.Interrupts) > MaxApplicationTestInterrupts {
		return fmt.Errorf("%w: at most %d interrupt turns allowed", ErrInvalidServingRecord, MaxApplicationTestInterrupts)
	}
	for i := range tc.Interrupts {
		it := &tc.Interrupts[i]
		if len(it.ExpectHandler) > 64 {
			return fmt.Errorf("%w: expect_handler max 64 chars", ErrInvalidServingRecord)
		}
		if it.ContextMatchMode == "" {
			it.ContextMatchMode = "exact"
		}
		switch it.ContextMatchMode {
		case "exact", "subset":
		default:
			return fmt.Errorf("%w: context_match_mode must be exact or subset", ErrInvalidServingRecord)
		}
		if len(strings.TrimSpace(string(it.ExpectContext))) > 0 {
			if len(it.ExpectContext) > MaxTestCasePayloadBytes || !json.Valid(it.ExpectContext) {
				return fmt.Errorf("%w: expect_context must be valid JSON <= 64 KiB", ErrInvalidServingRecord)
			}
		} else {
			it.ExpectContext = nil
		}
		if it.Resolution == "" {
			it.Resolution = "resolved"
		}
		switch it.Resolution {
		case "resolved", "expired":
		default:
			return fmt.Errorf("%w: resolution must be resolved or expired", ErrInvalidServingRecord)
		}
		if it.Resolution == "resolved" {
			if len(strings.TrimSpace(string(it.Event))) == 0 {
				it.Event = json.RawMessage(`{}`)
			}
			if len(it.Event) > MaxTestCasePayloadBytes || !json.Valid(it.Event) {
				return fmt.Errorf("%w: interrupt event must be valid JSON <= 64 KiB", ErrInvalidServingRecord)
			}
			var obj map[string]any
			if err := json.Unmarshal(it.Event, &obj); err != nil || obj == nil {
				return fmt.Errorf("%w: interrupt event must be a JSON object", ErrInvalidServingRecord)
			}
		} else {
			it.Event = nil
		}
	}
	if tc.Interrupts == nil {
		tc.Interrupts = []ApplicationTestInterrupt{}
	}
	return nil
}

// ApplicationTestCaseStore persists per-application test cases and
// candidate-scoped suite runs.
type ApplicationTestCaseStore interface {
	CreateApplicationTestCase(ctx context.Context, tc *ApplicationTestCase) error
	GetApplicationTestCase(ctx context.Context, workspaceID, appID, caseID string) (*ApplicationTestCase, error)
	ListApplicationTestCases(ctx context.Context, workspaceID, appID string) ([]*ApplicationTestCase, error)
	UpdateApplicationTestCase(ctx context.Context, tc *ApplicationTestCase) (bool, error)
	DeleteApplicationTestCase(ctx context.Context, workspaceID, appID, caseID string) (bool, error)

	SaveApplicationTestSuiteRun(ctx context.Context, run *ApplicationTestSuiteRun) error
	GetApplicationTestSuiteRun(ctx context.Context, workspaceID, appID, candidate string) (*ApplicationTestSuiteRun, error)
}
