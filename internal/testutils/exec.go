package testutils

import (
	"calcside/internal/capability"
	"calcside/internal/engine"
	"testing"
)

type TestEngine struct {
	e    *engine.Engine
	sess *engine.Session
}

func newRegistry(t *testing.T) *capability.Registry {
	t.Helper()
	return capability.NewRegistry()
}

func NewEngine(t *testing.T) (TestEngine, error) {
	t.Helper()
	ctx := t.Context()
	eng := engine.New(
		1,
	)
	session, err := engine.NewSession(
		engine.SessionDeps{
			Context:  ctx,
			Registry: newRegistry(t),
			Limits:   capability.ServerLimits{},
			Hooks:    nil,
			Observer: nil,
		},
		engine.SessionCreation{},
	)
	if err != nil {
		return TestEngine{}, err
	}
	return TestEngine{
		e:    eng,
		sess: session,
	}, nil
}

func (e *TestEngine) Exec(t *testing.T, code string) (string, *engine.Error) {
	result := e.e.Exec(
		t.Context(),
		e.sess,
		code,
		code,
		0,
		0,
		e.sess.Out.String,
	)
	return result.Output, result.Error
}
