package sandbox

import (
	"context"
	"errors"
	"strings"

	"go.starlark.net/starlark"

	"calcside/internal/capability"
	"calcside/internal/engine"
	"calcside/internal/instance"
	"calcside/internal/service"
	"calcside/internal/types"
)

// FileView is either a directory listing (IsDir + Entries) or a file
// (Path + redacted Content).
type FileView struct {
	IsDir   bool
	Entries []any
	Path    string
	Content string
}

// Browse serves GET /instances/{id}/files?path=... through the fs
// capability binding inside a gated "console" session.
func (s *Service) Browse(ctx context.Context, a service.Actor, id, path string) (*FileView, error) {
	in, err := s.owned(ctx, a, id)
	if err != nil {
		return nil, err
	}
	if in.Status != types.InstanceRunning {
		return nil, service.Errf(types.ErrCodeNotRunning, "instance not running")
	}
	if !s.mgr.HasCapability(in.ID, string(types.CapFS)) {
		return nil, service.Errf(types.ErrCodeNoFS, "instance has no fs capability")
	}
	var stat map[string]any
	var listing []any
	var content string
	err = s.mgr.WithSession(in.ID, func(s *engine.Session, gate *capability.Gate) error {
		fsv, ok := s.Predeclared[string(types.CapFS)]
		if !ok {
			return errors.New("no fs binding")
		}
		mod, ok := fsv.(starlark.HasAttrs)
		if !ok {
			return errors.New("bad fs binding")
		}
		thread := &starlark.Thread{Name: "console"}
		thread.SetLocal(capability.ContextKey, ctx)
		call := func(method string, args ...starlark.Value) (starlark.Value, error) {
			fn, err := mod.Attr(method)
			if err != nil {
				return nil, err
			}
			return starlark.Call(thread, fn, starlark.Tuple(args), nil)
		}
		statV, err := call("stat", starlark.String(path))
		if err != nil {
			return err
		}
		stat, err = toGo(statV)
		if err != nil {
			return err
		}
		if d, _ := stat["is_dir"].(bool); d {
			listV, err := call("list", starlark.String(path))
			if err != nil {
				return err
			}
			v, err := toGoAny(listV)
			if err != nil {
				return err
			}
			listing, _ = v.([]any)
			return nil
		}
		readV, err := call("read", starlark.String(path))
		if err != nil {
			return err
		}
		if sv, ok := readV.(starlark.String); ok {
			content = string(sv)
		}
		return nil
	})
	if err != nil {
		switch {
		case errors.Is(err, instance.ErrNotFound):
			return nil, service.NotFound("instance not found")
		case errors.Is(err, instance.ErrNotRunning):
			return nil, service.Errf(types.ErrCodeNotRunning, "instance not running")
		case strings.Contains(err.Error(), "does not exist"):
			return nil, service.NotFound(err.Error())
		default:
			return nil, service.Errf(types.ErrCodeFSError, "%s", err.Error())
		}
	}
	if d, _ := stat["is_dir"].(bool); d {
		if listing == nil {
			listing = []any{}
		}
		return &FileView{IsDir: true, Entries: listing}, nil
	}
	p, _ := stat["path"].(string)
	return &FileView{Path: p, Content: s.mgr.Redact(in.ID, content)}, nil
}

// toGo converts starlark dict/list primitives to Go values.
func toGo(v starlark.Value) (map[string]any, error) {
	switch t := v.(type) {
	case *starlark.Dict:
		out := map[string]any{}
		for _, item := range t.Items() {
			k, _ := starlark.AsString(item[0])
			gv, err := toGoAny(item[1])
			if err != nil {
				return nil, err
			}
			out[k] = gv
		}
		return out, nil
	}
	return nil, errors.New("not a dict")
}

func toGoAny(v starlark.Value) (any, error) {
	switch t := v.(type) {
	case starlark.String:
		return string(t), nil
	case starlark.Bool:
		return bool(t), nil
	case starlark.Int:
		n, ok := t.Int64()
		if !ok {
			return nil, errors.New("int too large")
		}
		return n, nil
	case *starlark.List:
		out := make([]any, t.Len())
		for i := 0; i < t.Len(); i++ {
			gv, err := toGoAny(t.Index(i))
			if err != nil {
				return nil, err
			}
			out[i] = gv
		}
		return out, nil
	case *starlark.Dict:
		return toGo(t)
	default:
		return t.String(), nil
	}
}
