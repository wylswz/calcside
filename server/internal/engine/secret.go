package engine

import (
	"calcside/internal/secrets"
	"fmt"

	"go.starlark.net/starlark"
)

// secretsStruct is the predeclared `secrets` global: exposes only
// names() — values are never reachable from Starlark.
type secretsStruct struct{ set *secrets.Set }

func (s secretsStruct) String() string        { return "<secrets>" }
func (s secretsStruct) Type() string          { return "secrets" }
func (s secretsStruct) Freeze()               {}
func (s secretsStruct) Truth() starlark.Bool  { return true }
func (s secretsStruct) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable: secrets") }

func (s secretsStruct) Attr(name string) (starlark.Value, error) {
	if name == "names" {
		set := s.set
		return starlark.NewBuiltin("secrets.names", func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			if err := starlark.UnpackArgs("secrets.names", args, kwargs); err != nil {
				return nil, err
			}
			var names []string
			if set != nil {
				names = set.Names()
			}
			l := starlark.NewList(make([]starlark.Value, len(names)))
			for i, n := range names {
				l.SetIndex(i, starlark.String(n))
			}
			return l, nil
		}), nil
	}
	return nil, nil
}

func (s secretsStruct) AttrNames() []string { return []string{"names"} }
