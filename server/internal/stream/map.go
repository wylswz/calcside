package stream

import (
	"errors"

	"go.starlark.net/starlark"
)

func methodBuiltin(name string, self *Stream, call func(*starlark.Thread, starlark.Tuple, []starlark.Tuple) (starlark.Value, error)) starlark.Callable {
	return starlark.NewBuiltin("stream."+name, func(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		if err := self.scopeFor(thread); err != nil {
			return nil, err
		}
		return call(thread, args, kwargs)
	})
}

func callableArg(name string, v starlark.Value) (starlark.Callable, error) {
	c, ok := v.(starlark.Callable)
	if !ok {
		return nil, errors.New(name + ": fn must be callable")
	}
	return c, nil
}

func starMap(self *Stream) starlark.Callable {
	return methodBuiltin("map", self, func(_ *starlark.Thread, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		var fn starlark.Value
		if err := starlark.UnpackArgs("stream.map", args, kwargs, "fn", &fn); err != nil {
			return nil, err
		}
		c, err := callableArg("stream.map", fn)
		if err != nil {
			return nil, err
		}
		return self.Map(c)
	})
}

func starReduce(self *Stream) starlark.Callable {
	return methodBuiltin("reduce", self, func(_ *starlark.Thread, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		var fn, initial starlark.Value
		if err := starlark.UnpackArgs("stream.reduce", args, kwargs, "fn", &fn, "initial", &initial); err != nil {
			return nil, err
		}
		c, err := callableArg("stream.reduce", fn)
		if err != nil {
			return nil, err
		}
		return self.Reduce(c, initial)
	})
}

func starFlatMap(self *Stream) starlark.Callable {
	return methodBuiltin("flatmap", self, func(_ *starlark.Thread, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		var fn starlark.Value
		if err := starlark.UnpackArgs("stream.flatmap", args, kwargs, "fn", &fn); err != nil {
			return nil, err
		}
		c, err := callableArg("stream.flatmap", fn)
		if err != nil {
			return nil, err
		}
		return self.FlatMap(c)
	})
}
