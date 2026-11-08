package stream

import "go.starlark.net/starlark"

func starMap(self starlark.Value) starlark.Callable {
	return starlark.NewBuiltin(
		"map",
		func(
			thread *starlark.Thread,
			fn *starlark.Builtin,
			args starlark.Tuple,
			kwargs []starlark.Tuple,
		) (starlark.Value, error) {
			mapper, ok := args[0].(*starlark.Function)
			if !ok {
				return nil, starlark.NoSuchAttrError("map() requires a function as the first argument")
			}
			selfStream, ok := self.(*Stream)
			if !ok {
				return nil, starlark.NoSuchAttrError("map() requires a stream as the receiver")
			}
			return selfStream.Map(mapper)
		},
	)
}
