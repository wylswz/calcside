package stdlib

import (
	"errors"

	"calcside/internal/capability"
	"go.starlark.net/starlark"
)

// test.monkey_patch(ext.io.read, handler=my_read)
func monkeyPatch(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var target starlark.Value
	var handler starlark.Value

	if err := unpack(b, args, kwargs, "target", &target, "handler", &handler); err != nil {
		return nil, err
	}
	asFunc, ok := target.(starlark.Callable)
	if !ok {
		return nil, errors.New("expected a callable target")
	}
	asHandler, ok := handler.(starlark.Callable)
	if !ok {
		return nil, errors.New("expected a callable handler")
	}
	if asFunc.Name() == b.Name() {
		return nil, errors.New("cannot monkey patch test.monkey_patch")
	}

	capability.SetMonkeyPatch(t, asFunc.Name(), asHandler)
	return handler, nil
}
