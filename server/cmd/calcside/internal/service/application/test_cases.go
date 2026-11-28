package application

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"

	"calcside/cmd/calcside/internal/service"
	"calcside/cmd/calcside/internal/service/access"
	"calcside/internal/runtime"
	"calcside/internal/store"
	"calcside/internal/types"
)

// TestCaseInput carries the fields for creating or updating one test case.
type TestCaseInput struct {
	Name              string
	Description       string
	Input             json.RawMessage
	Interrupts        []store.ApplicationTestInterrupt
	ExpectStatus      string
	ExpectedOutput    json.RawMessage
	OutputMatchMode   string
	ExpectedErrorCode string
}

// ListTestCases returns an application's saved test cases and (when the draft
// resolves cleanly) the latest full-suite run for its current candidate.
func (s *Service) ListTestCases(ctx context.Context, a service.Actor, appID string) ([]*store.ApplicationTestCase, *store.ApplicationTestSuiteRun, error) {
	app, err := s.o.Access.Application(ctx, a, appID, access.View)
	if err != nil {
		return nil, nil, err
	}
	cases, err := s.o.Store.ListApplicationTestCases(ctx, app.WorkspaceID, app.ID)
	if err != nil {
		return nil, nil, fail(err)
	}
	if cases == nil {
		cases = []*store.ApplicationTestCase{}
	}
	var latestRun *store.ApplicationTestSuiteRun
	if len(cases) > 0 {
		a.WorkspaceID = app.WorkspaceID
		if rel, rerr := s.reviewRelease(ctx, a, app, s.o.Sandbox, s.o.Now()); rerr == nil && rel != nil {
			if tr, terr := s.o.Store.GetApplicationTestSuiteRun(ctx, app.WorkspaceID, app.ID, rel.ID); terr == nil {
				latestRun = tr
			}
		}
	}
	return cases, latestRun, nil
}

// CreateTestCase saves a new test case on an application.
func (s *Service) CreateTestCase(ctx context.Context, a service.Actor, appID string, req TestCaseInput) (*store.ApplicationTestCase, error) {
	app, err := s.o.Access.Application(ctx, a, appID, access.Configure)
	if err != nil {
		return nil, err
	}
	now := s.o.Now().UTC()
	tc := &store.ApplicationTestCase{
		ApplicationID:     app.ID,
		WorkspaceID:       app.WorkspaceID,
		Name:              strings.TrimSpace(req.Name),
		Description:       strings.TrimSpace(req.Description),
		Input:             req.Input,
		Interrupts:        req.Interrupts,
		ExpectStatus:      req.ExpectStatus,
		ExpectedOutput:    req.ExpectedOutput,
		OutputMatchMode:   req.OutputMatchMode,
		ExpectedErrorCode: strings.TrimSpace(req.ExpectedErrorCode),
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err := store.ValidateApplicationTestCase(tc); err != nil {
		return nil, service.BadRequest("%s", err.Error())
	}
	if err := s.o.Store.CreateApplicationTestCase(ctx, tc); err != nil {
		return nil, fail(err)
	}
	return tc, nil
}

// UpdateTestCase updates an existing test case on an application.
func (s *Service) UpdateTestCase(ctx context.Context, a service.Actor, appID, caseID string, req TestCaseInput) (*store.ApplicationTestCase, error) {
	app, err := s.o.Access.Application(ctx, a, appID, access.Configure)
	if err != nil {
		return nil, err
	}
	existing, err := s.o.Store.GetApplicationTestCase(ctx, app.WorkspaceID, app.ID, caseID)
	if err != nil {
		return nil, fail(err)
	}
	tc := &store.ApplicationTestCase{
		ID:                existing.ID,
		ApplicationID:     app.ID,
		WorkspaceID:       app.WorkspaceID,
		Name:              strings.TrimSpace(req.Name),
		Description:       strings.TrimSpace(req.Description),
		Input:             req.Input,
		Interrupts:        req.Interrupts,
		ExpectStatus:      req.ExpectStatus,
		ExpectedOutput:    req.ExpectedOutput,
		OutputMatchMode:   req.OutputMatchMode,
		ExpectedErrorCode: strings.TrimSpace(req.ExpectedErrorCode),
		CreatedAt:         existing.CreatedAt,
		UpdatedAt:         s.o.Now().UTC(),
	}
	if err := store.ValidateApplicationTestCase(tc); err != nil {
		return nil, service.BadRequest("%s", err.Error())
	}
	ok, err := s.o.Store.UpdateApplicationTestCase(ctx, tc)
	if err != nil {
		return nil, fail(err)
	}
	if !ok {
		return nil, service.NotFound("test case not found")
	}
	return tc, nil
}

// DeleteTestCase removes a test case from an application.
func (s *Service) DeleteTestCase(ctx context.Context, a service.Actor, appID, caseID string) error {
	app, err := s.o.Access.Application(ctx, a, appID, access.Configure)
	if err != nil {
		return err
	}
	ok, err := s.o.Store.DeleteApplicationTestCase(ctx, app.WorkspaceID, app.ID, caseID)
	if err != nil {
		return fail(err)
	}
	if !ok {
		return service.NotFound("test case not found")
	}
	return nil
}

// RunTestCases executes all saved test cases (or a filtered subset when
// caseIDs is non-empty) against the exact draft candidate in fresh sessions.
// Full-suite runs are persisted against (application_id, candidate) to gate
// reviewed publish.
func (s *Service) RunTestCases(ctx context.Context, a service.Actor, appID string, version int64, caseIDs []string) (*store.ApplicationTestSuiteRun, error) {
	app, err := s.o.Access.Application(ctx, a, appID, access.Configure)
	if err != nil {
		return nil, err
	}
	if version < 1 {
		return nil, service.BadRequest("version required")
	}
	if app.Version != version {
		return nil, service.Conflict("application changed")
	}
	allCases, err := s.o.Store.ListApplicationTestCases(ctx, app.WorkspaceID, app.ID)
	if err != nil {
		return nil, fail(err)
	}

	var targets []*store.ApplicationTestCase
	fullSuite := len(caseIDs) == 0
	if fullSuite {
		targets = allCases
	} else {
		wanted := make(map[string]bool, len(caseIDs))
		for _, id := range caseIDs {
			wanted[id] = true
		}
		for _, tc := range allCases {
			if wanted[tc.ID] {
				targets = append(targets, tc)
			}
		}
		if len(targets) == 0 {
			return nil, service.NotFound("test case not found")
		}
	}

	select {
	case s.testSlots <- struct{}{}:
		defer func() { <-s.testSlots }()
	default:
		return nil, service.Errf(types.ErrCodeTooMany, "too many tests running")
	}

	runCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	a.WorkspaceID = app.WorkspaceID
	rel, secs, err := s.release(runCtx, a, app)
	if err != nil {
		return nil, err
	}
	owner, err := s.owner(runCtx, app.UserID)
	if err != nil {
		return nil, err
	}

	now := s.o.Now().UTC()
	started := time.Now()
	suite := &store.ApplicationTestSuiteRun{
		ApplicationID: app.ID,
		WorkspaceID:   app.WorkspaceID,
		Candidate:     rel.ID,
		Status:        "passed",
		Total:         len(targets),
		Results:       make([]store.ApplicationTestCaseResult, 0, len(targets)),
		RunBy:         a.UserID,
		RunAt:         now,
	}

	if len(targets) == 0 {
		return suite, nil
	}

	// Prepare one ephemeral replica for the candidate; each runtime.Invoke
	// enters an isolated call scope so cases do not share mutable state.
	replicaID := store.NewID("apv_")
	prep, cleanup, err := s.prepare(runCtx, app.WorkspaceID, &runtime.PrepareRequest{
		CreateRequest: runtime.CreateRequest{
			InstanceID: replicaID, Owner: owner,
			Labels: map[string]string{"name": app.Name, "application_id": app.ID},
			Spec:   rel.Spec, Policies: rel.Policies, Modules: rel.Modules, Secrets: secs,
			ExpiresAt: now.Add(60 * time.Second), Epoch: 1,
		},
		ReleaseID: rel.ID, Generation: 1, Program: rel.Program,
		PrepareID: store.NewID("prep_"),
	})
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		return nil, err
	}

	if prep.Result.Error != nil {
		// Program failed to prepare: every test case fails with the init error.
		suite.Status = "failed"
		suite.Failed = len(targets)
		for _, tc := range targets {
			suite.Results = append(suite.Results, store.ApplicationTestCaseResult{
				CaseID:       tc.ID,
				CaseName:     tc.Name,
				Status:       "failed",
				DurationMs:   prep.Result.DurationMs,
				Steps:        int64(prep.Result.Steps),
				ErrorCode:    string(prep.Result.Error.Code),
				ErrorMessage: "init failed: " + prep.Result.Error.Message,
				Turns:        []store.ApplicationTestTurnResult{},
			})
		}
		suite.DurationMs = time.Since(started).Milliseconds()
		if fullSuite {
			if serr := s.o.Store.SaveApplicationTestSuiteRun(ctx, suite); serr != nil {
				return nil, fail(serr)
			}
		}
		return suite, nil
	}

	actor := runtime.Owner{UserID: a.UserID, Email: a.Email}
	for _, tc := range targets {
		res := s.executeTestCase(runCtx, app, rel, replicaID, owner, actor, prep.Epoch, tc)
		suite.Results = append(suite.Results, res)
		if res.Status == "passed" {
			suite.Passed++
		} else {
			suite.Failed++
			suite.Status = "failed"
		}
	}
	suite.DurationMs = time.Since(started).Milliseconds()

	if fullSuite {
		if serr := s.o.Store.SaveApplicationTestSuiteRun(ctx, suite); serr != nil {
			return nil, fail(serr)
		}
	}
	return suite, nil
}

func synthInterruptSlots() []runtime.InterruptSlot {
	slots := make([]runtime.InterruptSlot, 4)
	for i := range slots {
		id := store.NewID(store.PrefixInterrupt)
		slots[i] = runtime.InterruptSlot{
			ID:        id,
			ResumeURL: "https://test.local/api/v1/interrupts/" + id + "/resume",
			Token:     "cs_test_" + store.NewID("tok_"),
		}
	}
	return slots
}

func (s *Service) executeTestCase(
	ctx context.Context,
	app *store.Application,
	rel *store.ServingRelease,
	replicaID string,
	owner, actor runtime.Owner,
	epoch int64,
	tc *store.ApplicationTestCase,
) store.ApplicationTestCaseResult {
	res := store.ApplicationTestCaseResult{
		CaseID:            tc.ID,
		CaseName:          tc.Name,
		Status:            "passed",
		ExpectedErrorCode: tc.ExpectedErrorCode,
		Turns:             []store.ApplicationTestTurnResult{},
	}
	if len(tc.ExpectedOutput) > 0 {
		res.ExpectedOutput = prettyBoundJSON(tc.ExpectedOutput)
	}

	synRunID := store.NewID(store.PrefixRun)
	curEntry := "main"
	curInput := tc.Input
	if len(strings.TrimSpace(string(curInput))) == 0 {
		curInput = json.RawMessage(`{}`)
	}

	var lastOut *runtime.InvokeResponse
	var lastErr error
	turnIdx := 0

	for stepSeq := 0; stepSeq <= store.MaxApplicationTestInterrupts; stepSeq++ {
		out, err := s.o.Runtime.Invoke(ctx, &runtime.InvokeRequest{
			InstanceID: replicaID, Owner: owner, Actor: actor,
			ReleaseID: rel.ID, Generation: 1, Epoch: epoch,
			InvokeID: store.NewID("inv_"), Input: curInput,
			Entry: curEntry, Debug: false,
			RunID: synRunID, Step: stepSeq, Interrupts: synthInterruptSlots(),
		})
		if out != nil {
			s.o.Audit.RecordWorkspace(app.WorkspaceID, out.Audit)
			res.DurationMs += out.Result.DurationMs
			res.Steps += int64(out.Result.Steps)
		}
		lastOut, lastErr = out, err

		if err != nil || out == nil {
			msg := "runtime invoke failed"
			if err != nil {
				msg = err.Error()
			}
			res.Turns = append(res.Turns, store.ApplicationTestTurnResult{
				Turn: stepSeq, Entry: curEntry, Status: "failed", ErrorMessage: msg,
			})
			res.Status = "failed"
			res.ErrorCode = "runtime"
			res.ErrorMessage = msg
			return res
		}

		// Did this turn pause on an interrupt?
		if out.Result.Waiting != nil {
			w := out.Result.Waiting
			turnRec := store.ApplicationTestTurnResult{
				Turn:           stepSeq,
				Entry:          curEntry,
				Status:         "passed",
				DurationMs:     out.Result.DurationMs,
				Steps:          int64(out.Result.Steps),
				ActualHandler:  w.Handler,
				ActualOnExpire: w.OnExpire,
				ActualContext:  prettyBoundJSON(w.Context),
			}
			if turnIdx >= len(tc.Interrupts) {
				turnRec.Status = "unexpected_interrupt"
				turnRec.ErrorMessage = fmt.Sprintf("Application paused on interrupt handler %q at turn %d, but no interrupt turn was configured", w.Handler, turnIdx+1)
				res.Turns = append(res.Turns, turnRec)
				res.Status = "failed"
				res.ErrorCode = "unexpected_interrupt"
				res.ErrorMessage = turnRec.ErrorMessage
				return res
			}

			spec := tc.Interrupts[turnIdx]
			turnRec.ExpectedHandler = spec.ExpectHandler
			if len(spec.ExpectContext) > 0 {
				turnRec.ExpectedContext = prettyBoundJSON(spec.ExpectContext)
			}
			turnRec.Resolution = spec.Resolution
			if len(spec.Event) > 0 {
				turnRec.EventJSON = prettyBoundJSON(spec.Event)
			}

			if spec.ExpectHandler != "" && w.Handler != spec.ExpectHandler {
				turnRec.Status = "failed"
				turnRec.ErrorMessage = fmt.Sprintf("Turn %d handler mismatch: expected %q, got %q", turnIdx+1, spec.ExpectHandler, w.Handler)
				res.Turns = append(res.Turns, turnRec)
				res.Status = "failed"
				res.ErrorCode = "handler_mismatch"
				res.ErrorMessage = turnRec.ErrorMessage
				return res
			}
			if len(spec.ExpectContext) > 0 {
				ok, diffMsg := jsonMatches(spec.ExpectContext, w.Context, spec.ContextMatchMode)
				if !ok {
					turnRec.Status = "failed"
					turnRec.ErrorMessage = fmt.Sprintf("Turn %d interrupt context mismatch: %s", turnIdx+1, diffMsg)
					res.Turns = append(res.Turns, turnRec)
					res.Status = "failed"
					res.ErrorCode = "context_mismatch"
					res.ErrorMessage = turnRec.ErrorMessage
					return res
				}
			}

			nextEntry := w.Handler
			resolution := "resolved"
			eventJSON := spec.Event
			if spec.Resolution == "expired" {
				if w.OnExpire == "" {
					turnRec.Status = "failed"
					turnRec.ErrorMessage = fmt.Sprintf("Turn %d cannot simulate expire: interrupt %q has no on_expire handler", turnIdx+1, w.Handler)
					res.Turns = append(res.Turns, turnRec)
					res.Status = "failed"
					res.ErrorCode = "missing_on_expire"
					res.ErrorMessage = turnRec.ErrorMessage
					return res
				}
				nextEntry = w.OnExpire
				resolution = "expired"
				eventJSON = json.RawMessage(`null`)
			} else if len(strings.TrimSpace(string(eventJSON))) == 0 {
				eventJSON = json.RawMessage(`{}`)
			}

			ctxJSON := w.Context
			if len(ctxJSON) == 0 {
				ctxJSON = json.RawMessage(`{}`)
			}
			handlerInput, merr := json.Marshal(map[string]any{
				"run_id":       synRunID,
				"step":         stepSeq + 1,
				"interrupt_id": w.InterruptID,
				"handler":      nextEntry,
				"context":      json.RawMessage(ctxJSON),
				"resolution":   resolution,
				"event":        json.RawMessage(eventJSON),
				"resolved_by":  actor.UserID,
				"resolved_at":  s.o.Now().UTC().Format(time.RFC3339),
				"attempt":      1,
			})
			if merr != nil {
				turnRec.Status = "failed"
				turnRec.ErrorMessage = merr.Error()
				res.Turns = append(res.Turns, turnRec)
				res.Status = "failed"
				res.ErrorMessage = merr.Error()
				return res
			}

			res.Turns = append(res.Turns, turnRec)
			curEntry = nextEntry
			curInput = handlerInput
			turnIdx++
			continue
		}

		// Terminal turn (succeeded or errored).
		turnRec := store.ApplicationTestTurnResult{
			Turn:       stepSeq,
			Entry:      curEntry,
			Status:     "passed",
			DurationMs: out.Result.DurationMs,
			Steps:      int64(out.Result.Steps),
		}
		if out.Result.Error != nil {
			turnRec.ErrorMessage = fmt.Sprintf("%s: %s", out.Result.Error.Code, out.Result.Error.Message)
			if tc.ExpectStatus != "error" {
				turnRec.Status = "failed"
			}
		}
		res.Turns = append(res.Turns, turnRec)
		break
	}

	if lastErr != nil || lastOut == nil {
		res.Status = "failed"
		res.ErrorMessage = "no execution result"
		return res
	}
	if lastOut.Result.Waiting != nil {
		res.Status = "failed"
		res.ErrorCode = "chain_limit"
		res.ErrorMessage = fmt.Sprintf("exceeded maximum %d interrupt turns", store.MaxApplicationTestInterrupts)
		return res
	}
	if len(lastOut.Result.Output) > 0 {
		res.ActualOutput = prettyBoundJSON(lastOut.Result.Output)
	}

	if turnIdx < len(tc.Interrupts) && lastOut.Result.Error == nil {
		res.Status = "failed"
		res.ErrorCode = "missing_interrupt"
		res.ErrorMessage = fmt.Sprintf("Expected %d interrupt turn(s), but application completed after %d", len(tc.Interrupts), turnIdx)
		return res
	}

	if tc.ExpectStatus == "error" {
		if lastOut.Result.Error == nil {
			res.Status = "failed"
			res.ErrorCode = "expected_error"
			res.ErrorMessage = "Expected an error, but application succeeded"
			return res
		}
		res.ErrorCode = string(lastOut.Result.Error.Code)
		if tc.ExpectedErrorCode != "" && string(lastOut.Result.Error.Code) != tc.ExpectedErrorCode {
			res.Status = "failed"
			res.ErrorMessage = fmt.Sprintf("Expected error code %q, got %q (%s)", tc.ExpectedErrorCode, lastOut.Result.Error.Code, lastOut.Result.Error.Message)
			return res
		}
		return res
	}

	// Expected succeeded.
	if lastOut.Result.Error != nil {
		res.Status = "failed"
		res.ErrorCode = string(lastOut.Result.Error.Code)
		res.ErrorMessage = fmt.Sprintf("%s: %s", lastOut.Result.Error.Code, lastOut.Result.Error.Message)
		return res
	}
	if len(tc.ExpectedOutput) > 0 {
		ok, diffMsg := jsonMatches(tc.ExpectedOutput, lastOut.Result.Output, tc.OutputMatchMode)
		if !ok {
			res.Status = "failed"
			res.ErrorCode = "output_mismatch"
			res.ErrorMessage = "Output mismatch: " + diffMsg
			return res
		}
	}
	return res
}

func prettyBoundJSON(raw []byte) string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		s := string(raw)
		if len(s) > 8192 {
			return s[:8192]
		}
		return s
	}
	s := buf.String()
	if len(s) > 8192 {
		return s[:8192]
	}
	return s
}

func decodeJSONValue(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

func jsonMatches(expectedRaw, actualRaw []byte, mode string) (bool, string) {
	exp, err := decodeJSONValue(expectedRaw)
	if err != nil {
		return false, "invalid expected JSON"
	}
	act, err := decodeJSONValue(actualRaw)
	if err != nil {
		return false, "actual output is not valid JSON"
	}
	subset := mode == "subset"
	return compareJSONValue("$", exp, act, subset)
}

func compareJSONValue(path string, exp, act any, subset bool) (bool, string) {
	if exp == nil || act == nil {
		if exp == nil && act == nil {
			return true, ""
		}
		return false, fmt.Sprintf("%s: expected %v, got %v", path, exp, act)
	}
	switch ev := exp.(type) {
	case map[string]any:
		av, ok := act.(map[string]any)
		if !ok {
			return false, fmt.Sprintf("%s: expected object, got %T", path, act)
		}
		if !subset && len(ev) != len(av) {
			for k := range av {
				if _, exists := ev[k]; !exists {
					return false, fmt.Sprintf("%s.%s: unexpected key", path, k)
				}
			}
		}
		for k, val := range ev {
			aval, exists := av[k]
			if !exists {
				return false, fmt.Sprintf("%s.%s: missing expected key", path, k)
			}
			if ok, msg := compareJSONValue(path+"."+k, val, aval, subset); !ok {
				return false, msg
			}
		}
		return true, ""
	case []any:
		av, ok := act.([]any)
		if !ok {
			return false, fmt.Sprintf("%s: expected array, got %T", path, act)
		}
		if len(ev) != len(av) {
			return false, fmt.Sprintf("%s: expected length %d, got %d", path, len(ev), len(av))
		}
		for i := range ev {
			if ok, msg := compareJSONValue(fmt.Sprintf("%s[%d]", path, i), ev[i], av[i], subset); !ok {
				return false, msg
			}
		}
		return true, ""
	case json.Number:
		av, ok := act.(json.Number)
		if !ok {
			return false, fmt.Sprintf("%s: expected number %s, got %v", path, ev, act)
		}
		er, eok := new(big.Rat).SetString(ev.String())
		ar, aok := new(big.Rat).SetString(av.String())
		if eok && aok && er.Cmp(ar) == 0 {
			return true, ""
		}
		if ev.String() == av.String() {
			return true, ""
		}
		return false, fmt.Sprintf("%s: expected %s, got %s", path, ev, av)
	case string:
		av, ok := act.(string)
		if !ok || ev != av {
			return false, fmt.Sprintf("%s: expected %q, got %v", path, ev, act)
		}
		return true, ""
	case bool:
		av, ok := act.(bool)
		if !ok || ev != av {
			return false, fmt.Sprintf("%s: expected %v, got %v", path, ev, act)
		}
		return true, ""
	default:
		if fmt.Sprint(exp) != fmt.Sprint(act) {
			return false, fmt.Sprintf("%s: expected %v, got %v", path, exp, act)
		}
		return true, ""
	}
}
