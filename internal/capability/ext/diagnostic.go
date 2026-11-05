package ext

import (
	"errors"

	"go.starlark.net/starlark"

	"calcside/internal/capability"
	"calcside/internal/secrets"
)

// providerErrorRedactor returns the private _redact_error builtin
// injected into provider extension predeclared globals: provider
// diagnostics call it to re-redact a DECODED upstream message before
// excerpting (raw-byte redaction cannot catch JSON-escaped secrets).
// On redaction-limit failure it returns a fixed placeholder string so
// the raw text can never escape into errors or audit.
func providerErrorRedactor(secs *secrets.Set) *starlark.Builtin {
	return starlark.NewBuiltin("_redact_error",
		func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			var msg string
			if err := starlark.UnpackArgs("_redact_error", args, kwargs, "message", &msg); err != nil {
				return nil, errors.New("_redact_error: invalid arguments")
			}
			ctx := capability.ThreadContext(thread)
			out, err := secs.RedactBounded(ctx, msg, 1<<20)
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err() // context cancellation stays primary
				}
				return starlark.String("[diagnostic omitted: redaction limit]"), nil
			}
			return starlark.String(out), nil
		})
}
