package stream

import "go.starlark.net/starlark"

func starMap() starlark.Callable {
	return starlark.NewBuiltin(
		"map",
		func(
			thread *starlark.Thread,
			fn *starlark.Builtin,
			args starlark.Tuple,
			kwargs []starlark.Tuple,
		) (starlark.Value, error) {
			if len(args) < 2 {
				return nil, starlark.NoSuchAttrError("map() takes at least 2 arguments")
			}
			self := args[0]
			fn, ok := args[1].(*starlark.Builtin)
			if !ok {
				return nil, starlark.NoSuchAttrError("map() requires a function as the second argument")
			}
			selfStream, ok := self.(*Stream)
			if !ok {
				return nil, starlark.NoSuchAttrError("map() requires an iterable as the first argument")
			}
			return selfStream.Map(fn)
		},
	)
}
