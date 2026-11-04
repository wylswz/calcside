package module

import (
	"context"

	"calcside/cmd/calcside/internal/service"
	"calcside/cmd/calcside/internal/service/access"
	"calcside/internal/engine"
	mod "calcside/internal/module"
	"calcside/internal/types"
)

// validate.go: source checking — tree shape/paths/bounds plus static
// compile against the predeclared union. No code ever executes, no
// capability is initialized.

// checkSources validates a UTF-8 file map: tree shape/paths/bounds
// (unsummed, draft semantics) and full static compile against the
// predeclared union — no code executes.
func (s *Service) checkSources(files map[string][]byte) error {
	if err := mod.ValidateFiles(files, ""); err != nil {
		return service.Errf(types.ErrCodeBadSpec, "%s", err)
	}
	if err := engine.ValidateModuleSources(files, s.has); err != nil {
		return service.Errf(types.ErrCodeBadSpec, "%s", err)
	}
	return nil
}

// Validate checks a file map for the validate endpoint: tree shape
// plus static compile — no publish, no pin, no store write.
func (s *Service) Validate(ctx context.Context, a service.Actor, files map[string]string) error {
	if err := s.o.Access.ModuleScope(ctx, a, access.View); err != nil {
		return err
	}
	return s.checkSources(utf8Files(files))
}
