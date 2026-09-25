// Package fs implements the "fs" capability: a per-instance in-memory
// virtual filesystem rooted at /work, exposed to Starlark as fs.* methods.
package fs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"go.starlark.net/starlark"

	"calcside/internal/capability"
)

const root = "/work"

// Config configures one instance's fs capability.
type Config struct {
	QuotaBytes int64 `json:"quota_bytes"`
	MaxFiles   int   `json:"max_files"`
	ReadOnly   bool  `json:"read_only"`
}

const (
	defaultQuota    = 64 << 20
	defaultMaxFiles = 10000
)

type node struct {
	isDir   bool
	content []byte
	mtime   time.Time
}

// VFS is the in-memory filesystem shared by all execs on one instance.
type VFS struct {
	mu    sync.Mutex
	nodes map[string]*node // key: absolute clean path, dirs and files
	bytes int64
	files int
	cfg   Config
	now   func() time.Time
}

func NewVFS(cfg Config) *VFS {
	v := &VFS{nodes: map[string]*node{}, cfg: cfg, now: time.Now}
	v.nodes[root] = &node{isDir: true, mtime: v.now()}
	return v
}

// Resolve normalizes a user path: relative resolves against /work,
// absolute must stay under /work, everything is cleaned, escapes error.
func Resolve(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("fs: empty path")
	}
	var full string
	if strings.HasPrefix(p, "/") {
		full = path.Clean(p)
	} else {
		full = path.Clean(path.Join(root, p))
	}
	if full != root && !strings.HasPrefix(full, root+"/") {
		return "", fmt.Errorf("fs: path %q escapes %s", p, root)
	}
	return full, nil
}

var (
	ErrNotExist   = errors.New("fs: file does not exist")
	ErrExist      = errors.New("fs: path already exists")
	ErrNotDir     = errors.New("fs: not a directory")
	ErrIsDir      = errors.New("fs: is a directory")
	ErrNotEmpty   = errors.New("fs: directory not empty")
	ErrReadOnly   = errors.New("fs: filesystem is read-only")
	ErrQuota      = errors.New("fs: quota exceeded")
	ErrMaxFiles   = errors.New("fs: file count limit exceeded")
	ErrRootDelete = errors.New("fs: cannot delete root")
)

func (v *VFS) mkdirAllLocked(p string) error {
	if p == root {
		return nil
	}
	// Ensure each ancestor exists as a dir, creating as needed.
	var missing []string
	cur := p
	for cur != root {
		if n, ok := v.nodes[cur]; ok {
			if !n.isDir {
				return fmt.Errorf("%w: %s", ErrNotDir, cur)
			}
			break
		}
		missing = append(missing, cur)
		cur = path.Dir(cur)
	}
	for i := len(missing) - 1; i >= 0; i-- {
		v.nodes[missing[i]] = &node{isDir: true, mtime: v.now()}
	}
	return nil
}

// Write stores content at path, creating parent dirs. Returns bytes written.
func (v *VFS) Write(p, content string, appendMode bool) (int, error) {
	full, err := Resolve(p)
	if err != nil {
		return 0, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.cfg.ReadOnly {
		return 0, ErrReadOnly
	}
	n, exists := v.nodes[full]
	if exists && n.isDir {
		return 0, ErrIsDir
	}
	add := int64(len(content))
	var removed int64
	if exists && !appendMode {
		removed = int64(len(n.content))
	}
	if v.bytes-removed+add > v.cfg.QuotaBytes {
		return 0, ErrQuota
	}
	if !exists && v.files+1 > v.cfg.MaxFiles {
		return 0, ErrMaxFiles
	}
	if err := v.mkdirAllLocked(path.Dir(full)); err != nil {
		return 0, err
	}
	if exists && appendMode {
		n.content = append(n.content, content...)
		n.mtime = v.now()
		v.bytes += add
		return len(content), nil
	}
	if exists {
		v.bytes -= int64(len(n.content))
	} else {
		v.files++
	}
	v.nodes[full] = &node{content: []byte(content), mtime: v.now()}
	v.bytes += add
	return len(content), nil
}

func (v *VFS) Read(p string) (string, error) {
	full, err := Resolve(p)
	if err != nil {
		return "", err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	n, ok := v.nodes[full]
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrNotExist, p)
	}
	if n.isDir {
		return "", fmt.Errorf("%w: %s", ErrIsDir, p)
	}
	return string(n.content), nil
}

// Entry is one stat result.
type Entry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
	Mtime int64  `json:"mtime"`
}

func (v *VFS) statLocked(full string) (Entry, error) {
	n, ok := v.nodes[full]
	if !ok {
		return Entry{}, ErrNotExist
	}
	return Entry{
		Name:  path.Base(full),
		Path:  full,
		IsDir: n.isDir,
		Size:  int64(len(n.content)),
		Mtime: n.mtime.Unix(),
	}, nil
}

func (v *VFS) Stat(p string) (Entry, error) {
	full, err := Resolve(p)
	if err != nil {
		return Entry{}, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	e, err := v.statLocked(full)
	if err != nil {
		return Entry{}, fmt.Errorf("%w: %s", err, p)
	}
	return e, nil
}

func (v *VFS) Exists(p string) (bool, error) {
	full, err := Resolve(p)
	if err != nil {
		return false, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	_, ok := v.nodes[full]
	return ok, nil
}

func (v *VFS) List(p string) ([]Entry, error) {
	full, err := Resolve(p)
	if err != nil {
		return nil, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	n, ok := v.nodes[full]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotExist, p)
	}
	if !n.isDir {
		return nil, fmt.Errorf("%w: %s", ErrNotDir, p)
	}
	var out []Entry
	for pth := range v.nodes {
		if pth == full {
			continue
		}
		if path.Dir(pth) == full {
			e, _ := v.statLocked(pth)
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func (v *VFS) Walk(p string) ([]Entry, error) {
	full, err := Resolve(p)
	if err != nil {
		return nil, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	n, ok := v.nodes[full]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotExist, p)
	}
	if !n.isDir {
		return nil, fmt.Errorf("%w: %s", ErrNotDir, p)
	}
	var out []Entry
	prefix := full + "/"
	for pth := range v.nodes {
		if pth == full || !strings.HasPrefix(pth, prefix) {
			continue
		}
		e, _ := v.statLocked(pth)
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func (v *VFS) Mkdir(p string) error {
	full, err := Resolve(p)
	if err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.cfg.ReadOnly {
		return ErrReadOnly
	}
	if _, ok := v.nodes[full]; ok {
		return fmt.Errorf("%w: %s", ErrExist, p)
	}
	return v.mkdirAllLocked(full)
}

func (v *VFS) Delete(p string, recursive bool) (int, error) {
	full, err := Resolve(p)
	if err != nil {
		return 0, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.cfg.ReadOnly {
		return 0, ErrReadOnly
	}
	if full == root {
		return 0, ErrRootDelete
	}
	n, ok := v.nodes[full]
	if !ok {
		return 0, fmt.Errorf("%w: %s", ErrNotExist, p)
	}
	var doomed []string
	if n.isDir {
		prefix := full + "/"
		for pth := range v.nodes {
			if strings.HasPrefix(pth, prefix) {
				doomed = append(doomed, pth)
			}
		}
		if len(doomed) > 0 && !recursive {
			return 0, fmt.Errorf("%w: %s", ErrNotEmpty, p)
		}
		doomed = append(doomed, full)
	} else {
		doomed = []string{full}
	}
	for _, pth := range doomed {
		dn := v.nodes[pth]
		if !dn.isDir {
			v.bytes -= int64(len(dn.content))
			v.files--
		}
		delete(v.nodes, pth)
	}
	return len(doomed), nil
}

// Entries returns every file's content, for snapshotting.
func (v *VFS) Files() map[string][]byte {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := map[string][]byte{}
	for p, n := range v.nodes {
		if !n.isDir {
			cp := make([]byte, len(n.content))
			copy(cp, n.content)
			out[p] = cp
		}
	}
	return out
}

// --- starlark binding ---

type factory struct{}

// Factory returns the capability.Factory for "fs".
func Factory() capability.Factory { return factory{} }

func (factory) Name() string { return "fs" }

func (factory) Ops() []capability.OpInfo {
	return []capability.OpInfo{
		{Name: "read", Doc: "read file contents as string", Params: []string{"path"}},
		{Name: "write", Doc: "write string to file, creating parents", Params: []string{"path", "content"}},
		{Name: "append", Doc: "append string to file, creating parents", Params: []string{"path", "content"}},
		{Name: "exists", Doc: "whether path exists", Params: []string{"path"}},
		{Name: "stat", Doc: "dict{name,path,is_dir,size,mtime}", Params: []string{"path"}},
		{Name: "list", Doc: "list direct children of dir", Params: []string{"dir"}},
		{Name: "walk", Doc: "recursive listing under dir", Params: []string{"dir"}},
		{Name: "mkdir", Doc: "create dir and parents", Params: []string{"path"}},
		{Name: "delete", Doc: "delete file or dir", Params: []string{"path", "recursive"}},
	}
}

func (factory) ConfigFields() []capability.FieldDoc {
	return []capability.FieldDoc{
		{Name: "quota_bytes", Type: "int", Doc: "max total bytes stored", Default: defaultQuota},
		{Name: "max_files", Type: "int", Doc: "max number of files", Default: defaultMaxFiles},
		{Name: "read_only", Type: "bool", Doc: "reject all writes", Default: false},
	}
}

func (factory) Validate(raw json.RawMessage, limits capability.ServerLimits) (any, error) {
	cfg := Config{QuotaBytes: defaultQuota, MaxFiles: defaultMaxFiles}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("fs config: %w", err)
		}
	}
	if cfg.QuotaBytes <= 0 {
		cfg.QuotaBytes = defaultQuota
	}
	if limits.MaxFSQuotaBytes > 0 && cfg.QuotaBytes > limits.MaxFSQuotaBytes {
		return nil, fmt.Errorf("fs config: quota_bytes %d exceeds server max %d", cfg.QuotaBytes, limits.MaxFSQuotaBytes)
	}
	if cfg.MaxFiles <= 0 {
		cfg.MaxFiles = defaultMaxFiles
	}
	return cfg, nil
}

func (factory) New(cfgAny any, env capability.InstanceEnv) (starlark.Value, io.Closer, error) {
	cfg, ok := cfgAny.(Config)
	if !ok {
		return nil, nil, fmt.Errorf("fs: bad config type %T", cfgAny)
	}
	v := NewVFS(cfg)
	return bindFS(v, env.Gate), &Closer{V: v}, nil
}

// Closer is returned by New; it exposes the VFS so the files API and
// snapshotter can reach it, and is closed when the instance ends.
type Closer struct{ V *VFS }

func (c *Closer) Close() error { return nil }

// Files returns all file contents (snapshot support).
func (c *Closer) Files() map[string][]byte { return c.V.Files() }

func bindFS(v *VFS, gate *capability.Gate) starlark.Value {
	pathArg := func(op string, args starlark.Tuple, kwargs []starlark.Tuple, extra ...any) (string, []any, error) {
		var p string
		params := append([]any{"path", &p}, extra...)
		if err := starlark.UnpackArgs(op, args, kwargs, params...); err != nil {
			return "", nil, err
		}
		return p, nil, nil
	}
	entryDict := func(e Entry) starlark.Value {
		return dictOf(map[string]starlark.Value{
			"name":   starlark.String(e.Name),
			"path":   starlark.String(e.Path),
			"is_dir": starlark.Bool(e.IsDir),
			"size":   starlark.MakeInt64(e.Size),
			"mtime":  starlark.MakeInt64(e.Mtime),
		})
	}
	entryList := func(entries []Entry) starlark.Value {
		l := make([]starlark.Value, len(entries))
		for i, e := range entries {
			l[i] = entryDict(e)
		}
		return starlark.NewList(l)
	}

	return capability.Bind("fs", gate, map[string]capability.Method{
		"read": func(args starlark.Tuple, kwargs []starlark.Tuple) (map[string]any, capability.OpBody, error) {
			p, _, err := pathArg("read", args, kwargs)
			if err != nil {
				return nil, nil, err
			}
			full, err := Resolve(p)
			if err != nil {
				return nil, nil, err
			}
			return map[string]any{"path": full}, func(ctx context.Context) (starlark.Value, map[string]any, error) {
				s, err := v.Read(p)
				if err != nil {
					return nil, nil, err
				}
				return starlark.String(s), map[string]any{"bytes": len(s)}, nil
			}, nil
		},
		"write": func(args starlark.Tuple, kwargs []starlark.Tuple) (map[string]any, capability.OpBody, error) {
			var p, content string
			if err := starlark.UnpackArgs("write", args, kwargs, "path", &p, "content", &content); err != nil {
				return nil, nil, err
			}
			full, err := Resolve(p)
			if err != nil {
				return nil, nil, err
			}
			return map[string]any{"path": full, "bytes": len(content)}, func(ctx context.Context) (starlark.Value, map[string]any, error) {
				n, err := v.Write(p, content, false)
				if err != nil {
					return nil, nil, err
				}
				return starlark.None, map[string]any{"bytes": n}, nil
			}, nil
		},
		"append": func(args starlark.Tuple, kwargs []starlark.Tuple) (map[string]any, capability.OpBody, error) {
			var p, content string
			if err := starlark.UnpackArgs("append", args, kwargs, "path", &p, "content", &content); err != nil {
				return nil, nil, err
			}
			full, err := Resolve(p)
			if err != nil {
				return nil, nil, err
			}
			return map[string]any{"path": full, "bytes": len(content)}, func(ctx context.Context) (starlark.Value, map[string]any, error) {
				n, err := v.Write(p, content, true)
				if err != nil {
					return nil, nil, err
				}
				return starlark.None, map[string]any{"bytes": n}, nil
			}, nil
		},
		"exists": func(args starlark.Tuple, kwargs []starlark.Tuple) (map[string]any, capability.OpBody, error) {
			p, _, err := pathArg("exists", args, kwargs)
			if err != nil {
				return nil, nil, err
			}
			full, err := Resolve(p)
			if err != nil {
				return nil, nil, err
			}
			return map[string]any{"path": full}, func(ctx context.Context) (starlark.Value, map[string]any, error) {
				ok, err := v.Exists(p)
				if err != nil {
					return nil, nil, err
				}
				return starlark.Bool(ok), nil, nil
			}, nil
		},
		"stat": func(args starlark.Tuple, kwargs []starlark.Tuple) (map[string]any, capability.OpBody, error) {
			p, _, err := pathArg("stat", args, kwargs)
			if err != nil {
				return nil, nil, err
			}
			full, err := Resolve(p)
			if err != nil {
				return nil, nil, err
			}
			return map[string]any{"path": full}, func(ctx context.Context) (starlark.Value, map[string]any, error) {
				e, err := v.Stat(p)
				if err != nil {
					return nil, nil, err
				}
				return entryDict(e), map[string]any{"is_dir": e.IsDir, "size": e.Size}, nil
			}, nil
		},
		"list": func(args starlark.Tuple, kwargs []starlark.Tuple) (map[string]any, capability.OpBody, error) {
			p := root
			if err := starlark.UnpackArgs("list", args, kwargs, "dir?", &p); err != nil {
				return nil, nil, err
			}
			full, err := Resolve(p)
			if err != nil {
				return nil, nil, err
			}
			return map[string]any{"path": full}, func(ctx context.Context) (starlark.Value, map[string]any, error) {
				entries, err := v.List(p)
				if err != nil {
					return nil, nil, err
				}
				return entryList(entries), map[string]any{"count": len(entries)}, nil
			}, nil
		},
		"walk": func(args starlark.Tuple, kwargs []starlark.Tuple) (map[string]any, capability.OpBody, error) {
			p := root
			if err := starlark.UnpackArgs("walk", args, kwargs, "dir?", &p); err != nil {
				return nil, nil, err
			}
			full, err := Resolve(p)
			if err != nil {
				return nil, nil, err
			}
			return map[string]any{"path": full}, func(ctx context.Context) (starlark.Value, map[string]any, error) {
				entries, err := v.Walk(p)
				if err != nil {
					return nil, nil, err
				}
				return entryList(entries), map[string]any{"count": len(entries)}, nil
			}, nil
		},
		"mkdir": func(args starlark.Tuple, kwargs []starlark.Tuple) (map[string]any, capability.OpBody, error) {
			p, _, err := pathArg("mkdir", args, kwargs)
			if err != nil {
				return nil, nil, err
			}
			full, err := Resolve(p)
			if err != nil {
				return nil, nil, err
			}
			return map[string]any{"path": full}, func(ctx context.Context) (starlark.Value, map[string]any, error) {
				if err := v.Mkdir(p); err != nil {
					return nil, nil, err
				}
				return starlark.None, nil, nil
			}, nil
		},
		"delete": func(args starlark.Tuple, kwargs []starlark.Tuple) (map[string]any, capability.OpBody, error) {
			var p string
			var recursive bool
			if err := starlark.UnpackArgs("delete", args, kwargs, "path", &p, "recursive?", &recursive); err != nil {
				return nil, nil, err
			}
			full, err := Resolve(p)
			if err != nil {
				return nil, nil, err
			}
			return map[string]any{"path": full, "recursive": recursive}, func(ctx context.Context) (starlark.Value, map[string]any, error) {
				n, err := v.Delete(p, recursive)
				if err != nil {
					return nil, nil, err
				}
				return starlark.None, map[string]any{"removed": n}, nil
			}, nil
		},
	})
}

func dictOf(m map[string]starlark.Value) *starlark.Dict {
	d := starlark.NewDict(len(m))
	for k, v := range m {
		_ = d.SetKey(starlark.String(k), v)
	}
	return d
}
