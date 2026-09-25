package capability

import (
	"context"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
)

// OpBody performs the capability operation after args are normalized. It
// returns the starlark value handed to the script plus JSON-able metadata
// visible to hooks/audit (never raw contents).
type OpBody func(ctx context.Context) (starlark.Value, map[string]any, error)

// Method parses starlark call args into normalized Call.Args and returns
// the op body to run inside the gate.
type Method func(args starlark.Tuple, kwargs []starlark.Tuple) (map[string]any, OpBody, error)

// Bind builds a starlark module value whose members call through the gate.
// Arg parsing happens before Invoke so Before hooks see normalized args;
// malformed-arg calls error without reaching the gate.
func Bind(name string, gate *Gate, methods map[string]Method) starlark.Value {
	members := starlark.StringDict{}
	for mname, m := range methods {
		mname, m := mname, m
		members[mname] = starlark.NewBuiltin(name+"."+mname, func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			callArgs, body, err := m(args, kwargs)
			if err != nil {
				return nil, err
			}
			return gate.Invoke(threadContext(thread), name, mname, callArgs,
				func(ctx context.Context, _ map[string]any) (starlark.Value, map[string]any, error) {
					return body(ctx)
				})
		})
	}
	return starlarkstruct.FromStringDict(starlarkstruct.Default, members)
}

// ContextKey is the thread-local under which the engine stores the exec
// context so hooks see cancellation.
const ContextKey = "calcside.ctx"

// ThreadContext extracts the exec context the engine stored on the thread.
func ThreadContext(thread *starlark.Thread) context.Context { return threadContext(thread) }

func threadContext(thread *starlark.Thread) context.Context {
	if v := thread.Local(ContextKey); v != nil {
		if ctx, ok := v.(context.Context); ok {
			return ctx
		}
	}
	return context.Background()
}
