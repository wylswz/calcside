package stdlib

import (
	"errors"

	"go.starlark.net/starlark"

	"calcside/internal/stream"
)

// streamNewCall implements stream.new(next_fn): next_fn may be passed
// positionally or by name; no other arguments exist.
func streamNewCall(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var next starlark.Value
	if err := starlark.UnpackArgs("stream.new", args, kwargs, "next_fn", &next); err != nil {
		return nil, errors.New("stream.new: invalid arguments")
	}
	scope := stream.FromThread(t)
	if scope == nil || !scope.Active() {
		return nil, errors.New("stream.new: no active execution scope")
	}
	st, err := stream.NewCallback(scope, next)
	if err != nil {
		return nil, err
	}
	return st, nil
}

func streamFromListCall(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var values starlark.Value
	if err := unpack(b, args, kwargs, "values", &values); err != nil {
		return nil, err
	}
	scope := stream.FromThread(t)
	if scope == nil || !scope.Active() {
		return nil, errors.New("stream.from_list: no active execution scope")
	}
	return stream.NewList(scope, values)
}

// streamSSECall implements stream.sse(source).
func streamSSECall(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var src starlark.Value
	if err := unpack(b, args, kwargs, "source", &src); err != nil {
		return nil, err
	}
	scope := stream.FromThread(t)
	if scope == nil || !scope.Active() {
		return nil, errors.New("stream.sse: no active execution scope")
	}
	source, ok := src.(*stream.Stream)
	if !ok {
		return nil, errors.New("stream.sse: source must be a stream")
	}
	if source.Scope() != scope {
		return nil, stream.ErrOutOfScope
	}
	return stream.NewSSE(scope, source)
}
