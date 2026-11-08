package types

import (
	"go.starlark.net/starlark"
)

// ClassAdapter wraps a starlark.HasAttrs and allows adding immutable
// extra fields.
//
// It allows binding method to it and access the receiver as `self`
type ClassAdapter struct {
	delegate starlark.HasAttrs

	extra map[string]starlark.Value
}

func (a *ClassAdapter) Attr(name string) (starlark.Value, error) {
	if v, ok := a.extra[name]; ok {
		return v, nil
	}
	return a.delegate.Attr(name)
}

func (a *ClassAdapter) AttrNames() []string {
	retMap := map[string]struct{}{}
	for _, k := range a.delegate.AttrNames() {
		retMap[k] = struct{}{}
	}
	for k := range a.extra {
		retMap[k] = struct{}{}
	}
	ret := make([]string, 0, len(retMap))
	for k := range retMap {
		ret = append(ret, k)
	}
	return ret
}

// BindMethod creates a bound method from a callable and an object.
func BindMethod(
	obj starlark.Value,
	function starlark.Callable,
) starlark.Callable {
	return starlark.NewBuiltin(
		function.Name(),
		func(
			thread *starlark.Thread,
			fn *starlark.Builtin,
			args starlark.Tuple,
			kwargs []starlark.Tuple,
		) (starlark.Value, error) {
			boundArgs := make([]starlark.Value, len(args)+1)
			boundArgs[0] = obj
			copy(boundArgs[1:], args)
			return starlark.Call(
				thread,
				function,
				boundArgs,
				kwargs,
			)
		},
	).BindReceiver(obj)
}
