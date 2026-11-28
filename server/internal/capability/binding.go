package capability

import (
	"context"
	"errors"

	"calcside/internal/types"

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
func Bind(name types.CapabilityName, gate *Gate, methods map[types.Op]Method) starlark.Value {
	members := starlark.StringDict{}
	for mname, m := range methods {
		mname, m := mname, m
		members[string(mname)] = starlark.NewBuiltin(string(name)+"."+string(mname), func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			if val, ok, err := CallMonkeyPatch(thread, b.Name(), args, kwargs); ok {
				return val, err
			}
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

const monkeyPatchPrefix = "calcside.monkey_patch."

// SetMonkeyPatch registers a per-thread override for a builtin name.
func SetMonkeyPatch(thread *starlark.Thread, name string, fn starlark.Callable) {
	if thread == nil {
		return
	}
	thread.SetLocal(monkeyPatchPrefix+name, fn)
}

// CallMonkeyPatch invokes the per-thread monkey patch for name if one is set.
func CallMonkeyPatch(thread *starlark.Thread, name string, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, bool, error) {
	if thread == nil {
		return nil, false, nil
	}
	v := thread.Local(monkeyPatchPrefix + name)
	if v == nil {
		return nil, false, nil
	}
	callable, ok := v.(starlark.Callable)
	if !ok {
		return nil, true, errors.New("got non callable")
	}
	val, err := starlark.Call(thread, callable, args, kwargs)
	return val, true, err
}

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
