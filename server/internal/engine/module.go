package engine

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"go.starlark.net/starlark"

	"calcside/internal/module"
)

// module.go implements the EP-0017 two-namespace load() resolver:
//
//   - consumer code (application sources, exec chunks) reaches module
//     files through load("@alias/path.star"); an ordinary load keeps
//     its bundle meaning on the program path and is an error in
//     instance exec, which has no consumer source namespace;
//   - module files load root-relative inside their own tree
//     (load("util.star") resolves within the same mount) and may never
//     use @-paths, so a module stays portable across consumers and
//     aliases.
//
// The requesting file is identified by the innermost call-stack
// frame's filename: module files compile under "@alias/path.star"
// names, and no bundle source, asset or exec chunk may start with '@'
// (reserved by checkReleaseName).
//
// Compiled module programs live on the Session: instance sessions
// build them at create, program sessions per call. The init cache is
// keyed "alias/file" and persists for the session's life (instance
// execs) or the invocation's (program init), matching initModules.
// Module init inherits the caller's gate state, step budget and
// cancellation — there is no separate budget or arming.

// moduleSet is a session's compiled, init-cached module attachments.
type moduleSet struct {
	programs map[string]map[string]*starlark.Program // mount alias → file → program
	cache    map[string]starlark.StringDict          // "alias/file" or bundle name → globals
	inited   map[string]bool                         // files currently initializing (cycle detection)
}

// compileModules validates the attached trees (bounds, paths, .star
// rule, manifest, pinned sums) and compiles every .star file against
// the session's predeclared names under "@alias/file" filenames. The
// intra-tree load graph is checked statically: @ loads inside module
// files, missing files and cycles are session-create failures.
func compileModules(mounts map[string]module.Tree, pre starlark.StringDict) (*moduleSet, error) {
	if len(mounts) == 0 {
		return nil, nil
	}
	if err := module.ValidateMounts(mounts); err != nil {
		return nil, err
	}
	ms := &moduleSet{
		programs: make(map[string]map[string]*starlark.Program, len(mounts)),
		cache:    map[string]starlark.StringDict{},
		inited:   map[string]bool{},
	}
	for _, alias := range slices.Sorted(maps.Keys(mounts)) {
		t := mounts[alias]
		progs := make(map[string]*starlark.Program, len(t.Files))
		for _, name := range slices.Sorted(maps.Keys(t.Files)) {
			if name == module.ManifestFile {
				continue
			}
			filename := "@" + alias + "/" + name
			_, pr, err := starlark.SourceProgramOptions(fileOpts, filename, t.Files[name], pre.Has)
			if err != nil {
				return nil, fmt.Errorf("module %s: %s does not compile", alias, name)
			}
			progs[name] = pr
		}
		if err := checkModuleLoadGraph(alias, progs); err != nil {
			return nil, err
		}
		ms.programs[alias] = progs
	}
	return ms, nil
}

// checkModuleLoadGraph statically validates one tree's load edges:
// every load must be a canonical root-relative .star path present in
// the same tree, @ loads are rejected outright, and the graph must be
// acyclic. Trees are independent — module files never reach the
// consumer's bundle or another mount.
func checkModuleLoadGraph(alias string, progs map[string]*starlark.Program) error {
	where := func() string {
		if alias == "" {
			return ""
		}
		return "module " + alias + ": "
	}()
	for _, name := range slices.Sorted(maps.Keys(progs)) {
		pr := progs[name]
		for i := 0; i < pr.NumLoads(); i++ {
			target, _ := pr.Load(i)
			if strings.HasPrefix(target, "@") {
				return fmt.Errorf("%s%s uses a @ load", where, name)
			}
			if err := checkReleaseName(target, true); err != nil {
				return fmt.Errorf("%s%s has an invalid load path", where, name)
			}
			if _, ok := progs[target]; !ok {
				return fmt.Errorf("%s%s loads missing file %q", where, name, target)
			}
		}
	}
	const (
		white = iota
		gray
		black
	)
	color := map[string]int{}
	var visit func(string) error
	visit = func(name string) error {
		switch color[name] {
		case gray:
			return fmt.Errorf("%scyclic load graph", where)
		case black:
			return nil
		}
		color[name] = gray
		pr := progs[name]
		for i := 0; i < pr.NumLoads(); i++ {
			target, _ := pr.Load(i)
			if err := visit(target); err != nil {
				return err
			}
		}
		color[name] = black
		return nil
	}
	for _, name := range slices.Sorted(maps.Keys(progs)) {
		if err := visit(name); err != nil {
			return err
		}
	}
	return nil
}

// requesterFile returns the innermost frame's filename — the file
// whose top-level evaluation issued this load(). During module init the
// loaded file's toplevel is on top of the stack.
func requesterFile(t *starlark.Thread) string {
	stack := t.CallStack()
	if len(stack) == 0 {
		return ""
	}
	return stack.At(0).Pos.Filename()
}

// loadFunc builds the session's unified load() closure. bundle is the
// consumer's own source namespace (nil for instance exec, which then
// rejects ordinary loads). Module-file loads resolve inside the
// requesting file's own mount; consumer @ loads resolve by alias.
func (ms *moduleSet) loadFunc(pre starlark.StringDict, bundle map[string]*starlark.Program) func(*starlark.Thread, string) (starlark.StringDict, error) {
	return func(t *starlark.Thread, mod string) (starlark.StringDict, error) {
		var key string
		var prog *starlark.Program
		req := requesterFile(t)
		switch {
		case strings.HasPrefix(req, "@"):
			// A module file: ordinary loads resolve root-relative in
			// its own tree; @ loads are rejected at runtime exactly as
			// statically.
			if strings.HasPrefix(mod, "@") {
				return nil, fmt.Errorf("module files cannot load @ paths")
			}
			if err := checkReleaseName(mod, true); err != nil {
				return nil, fmt.Errorf("invalid load path %q", mod)
			}
			mount, _, _ := strings.Cut(req[1:], "/")
			key = "@" + mount + "/" + mod
			prog = ms.programs[mount][mod]
			if prog == nil {
				return nil, fmt.Errorf("module %s has no file %q", mount, mod)
			}
		case strings.HasPrefix(mod, "@"):
			mount, file, err := splitMountRef(mod)
			if err != nil {
				return nil, err
			}
			key = mod
			files, ok := ms.programs[mount]
			if !ok {
				return nil, fmt.Errorf("unknown module mount %q", mount)
			}
			prog = files[file]
			if prog == nil {
				return nil, fmt.Errorf("module %s has no file %q", mount, file)
			}
		default:
			if bundle == nil {
				return nil, fmt.Errorf("cannot load %q: no source namespace in instance exec", mod)
			}
			key = mod
			prog = bundle[mod]
			if prog == nil {
				return nil, fmt.Errorf("load of unselected module")
			}
		}
		if g, ok := ms.cache[key]; ok {
			return g, nil
		}
		if ms.inited[key] {
			return nil, fmt.Errorf("load cycle in module graph")
		}
		ms.inited[key] = true
		g, err := prog.Init(t, pre)
		delete(ms.inited, key)
		if err != nil {
			return nil, err
		}
		ms.cache[key] = g
		return g, nil
	}
}

// splitMountRef parses "@alias/path.star" into (alias, path).
func splitMountRef(ref string) (string, string, error) {
	rest := strings.TrimPrefix(ref, "@")
	alias, file, ok := strings.Cut(rest, "/")
	if !ok || alias == "" || file == "" {
		return "", "", fmt.Errorf("invalid module load %q: want @alias/path.star", ref)
	}
	if !module.ValidMount(alias) {
		return "", "", fmt.Errorf("invalid module mount %q", alias)
	}
	if err := checkReleaseName(file, true); err != nil {
		return "", "", fmt.Errorf("invalid module load path %q", ref)
	}
	return alias, file, nil
}

// ValidateModuleSources is the publish-time companion to
// checkModuleLoadGraph: it compiles every .star file in the tree
// against a caller-supplied predeclared name set (the union of stdlib
// and every registered capability name) and runs the same static
// load-graph rules — a file that cannot compile against the union can
// never compile against any consumer session.
func ValidateModuleSources(files map[string][]byte, has func(string) bool) error {
	progs := make(map[string]*starlark.Program, len(files))
	for _, name := range slices.Sorted(maps.Keys(files)) {
		if name == module.ManifestFile {
			continue
		}
		_, pr, err := starlark.SourceProgramOptions(fileOpts, name, files[name], has)
		if err != nil {
			return fmt.Errorf("module: %s does not compile: %w", name, err)
		}
		progs[name] = pr
	}
	return checkModuleLoadGraph("", progs)
}
