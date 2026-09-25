package capability

import (
	"calcside/internal/types"
	"context"
	"errors"
	"sync"
	"testing"

	"go.starlark.net/starlark"
)

type recordingObs struct {
	mu   sync.Mutex
	recs []Record
}

func (o *recordingObs) Observe(r Record) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.recs = append(o.recs, r)
}

type denyHook struct{ reason string }

func (h denyHook) Name() string { return "denyhook" }
func (h denyHook) Before(ctx context.Context, c *Call) error {
	return errors.New(h.reason)
}
func (h denyHook) After(ctx context.Context, c *Call, r *Result) error { return nil }

func invoke(g *Gate) error {
	_, err := g.Invoke(context.Background(), types.CapFS, "read", map[string]any{"path": "/work/a"},
		func(ctx context.Context, _ map[string]any) (starlark.Value, map[string]any, error) {
			return starlark.String("x"), map[string]any{"bytes": 1}, nil
		})
	return err
}

func TestDisarmReturnsOutOfScope(t *testing.T) {
	g := NewGate(GateOwner{}, nil, nil)
	g.Arm(ExecContext{ExecID: "e1"})
	if err := invoke(g); err != nil {
		t.Fatalf("armed invoke failed: %v", err)
	}
	g.Disarm()
	if err := invoke(g); !errors.Is(err, ErrOutOfScope) {
		t.Fatalf("expected ErrOutOfScope, got %v", err)
	}
}

func TestRevokePermanent(t *testing.T) {
	g := NewGate(GateOwner{}, nil, nil)
	g.Arm(ExecContext{ExecID: "e1"})
	g.Revoke()
	g.Arm(ExecContext{ExecID: "e2"}) // no-op after revoke
	if err := invoke(g); !errors.Is(err, ErrOutOfScope) {
		t.Fatalf("expected ErrOutOfScope, got %v", err)
	}
}

func TestDeniedCallsRecorded(t *testing.T) {
	obs := &recordingObs{}
	g := NewGate(GateOwner{}, []Hook{denyHook{"nope"}}, obs)
	g.Arm(ExecContext{ExecID: "e1", InstanceID: "i1", UserID: "u1"})
	err := invoke(g)
	var denied *DeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("expected DeniedError, got %v", err)
	}
	if denied.Hook != "denyhook" || denied.Phase != types.PhaseBefore {
		t.Fatalf("bad denied error %+v", denied)
	}
	if len(obs.recs) != 1 || obs.recs[0].Decision != types.DecisionDeny {
		t.Fatalf("denied call not recorded: %+v", obs.recs)
	}
}

func TestAllowedRecorded(t *testing.T) {
	obs := &recordingObs{}
	g := NewGate(GateOwner{}, nil, obs)
	g.Arm(ExecContext{ExecID: "e1"})
	if err := invoke(g); err != nil {
		t.Fatal(err)
	}
	if len(obs.recs) != 1 || obs.recs[0].Decision != types.DecisionAllow {
		t.Fatalf("expected allow record: %+v", obs.recs)
	}
}

func TestOutOfScopeRecordCarriesOwner(t *testing.T) {
	obs := &recordingObs{}
	g := NewGate(GateOwner{InstanceID: "ins_9", UserID: "usr_9"}, nil, obs)
	// never armed
	if err := invoke(g); !errors.Is(err, ErrOutOfScope) {
		t.Fatalf("expected ErrOutOfScope, got %v", err)
	}
	if len(obs.recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(obs.recs))
	}
	if obs.recs[0].Call.InstanceID != "ins_9" || obs.recs[0].Call.UserID != "usr_9" {
		t.Fatalf("record missing owner attribution: %+v", obs.recs[0].Call)
	}
}
