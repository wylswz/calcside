package sandbox

import (
	"context"

	"calcside/cmd/calcside/internal/service/access"
	"encoding/json"
	"maps"
	"slices"

	"calcside/cmd/calcside/internal/service"
	"calcside/internal/module"
	"calcside/internal/store"
	"calcside/internal/types"
)

// modules.go resolves spec.modules references into pinned module
// trees once, at spec-normalization time. The returned trees travel
// with the request (place / CreateRequest) — nothing re-reads the
// library between validation and runtime, so publish, rename, or
// deletion afterwards cannot move an in-flight creation or a stored
// release.
//
// Resolution does for modules what the policy snapshot does for
// library policies: it is the single point where workspace names and
// versions become content. Everything it writes is pinned:
// modules[alias] in the returned spec carries concrete
// name/version/sum, and requires.ext aliases merge into
// capabilities.ext with source+sum only — never config, credentials,
// base grants, hosts, or policies.
//
// Provenance: ref.Ext is recomputed from the aliases resolution
// actually introduced — client-supplied Ext is ignored. Because an
// alias that already exists in the spec cannot be attributed to a
// module with certainty, resolution never removes or reinterprets
// aliases based on provenance input.

// selectedModules resolves raw spec.modules against the workspace
// library. The returned spec has modules written with pinned
// name/version/sum and merged requires.ext entries; the returned map
// is the attached trees, nil when the spec declares no mounts.
func (s *Service) selectedModules(ctx context.Context, wsID string, raw []byte) (json.RawMessage, map[string]module.Tree, error) {
	return s.selectedModulesWithOverrides(ctx, wsID, raw, nil)
}

// selectedModulesWithOverrides is the internal resolver accepting
// trusted trees — only DebugBoundary/DebugCreate passes overrides.
// An override skips the library lookup exactly when it matches the
// (name, version, sum) a ref would resolve to; Version 0 trees are
// allowed only for overrides (the @self debug mount).
func (s *Service) selectedModulesWithOverrides(ctx context.Context, wsID string, raw []byte, overrides map[string]module.Tree) (json.RawMessage, map[string]module.Tree, error) {
	var spec map[string]json.RawMessage
	if err := json.Unmarshal(raw, &spec); err != nil || spec == nil {
		return nil, nil, service.Errf(types.ErrCodeBadSpec, "invalid spec JSON: %s", err)
	}
	rawMods, ok := spec["modules"]
	if !ok || string(rawMods) == "null" {
		return raw, nil, nil
	}
	var mods map[string]module.Reference
	if err := json.Unmarshal(rawMods, &mods); err != nil || mods == nil {
		return nil, nil, service.Errf(types.ErrCodeBadSpec, "modules: invalid shape")
	}
	if len(mods) == 0 {
		return raw, nil, nil
	}
	if len(mods) > module.MaxMounts {
		return nil, nil, service.Errf(types.ErrCodeBadSpec, "modules: more than %d mounts", module.MaxMounts)
	}
	for alias := range mods {
		if !module.ValidMount(alias) {
			return nil, nil, service.Errf(types.ErrCodeBadSpec, "modules: invalid mount alias %q", alias)
		}
	}

	// Explicit ext aliases pin first: module dependency merging
	// compares against pinned source+sum, never unpinned input.
	var caps map[string]json.RawMessage
	if c := spec["capabilities"]; c != nil && string(c) != "null" {
		if err := json.Unmarshal(c, &caps); err != nil || caps == nil {
			return nil, nil, service.Errf(types.ErrCodeBadSpec, "capabilities must be an object")
		}
	}
	if caps == nil {
		caps = map[string]json.RawMessage{}
	}
	aliases := map[string]json.RawMessage{}
	if e := caps["ext"]; e != nil && string(e) != "null" {
		if err := json.Unmarshal(e, &aliases); err != nil || aliases == nil {
			return nil, nil, service.Errf(types.ErrCodeBadSpec, "capabilities.ext must be an object")
		}
	}
	if len(aliases) > 0 && s.pinner != nil {
		pinned, err := s.pinner.PinSpec(ctx, mustJSON(spec))
		if err != nil {
			return nil, nil, fail(err)
		}
		var pspec map[string]json.RawMessage
		if err := json.Unmarshal(pinned, &pspec); err != nil {
			return nil, nil, service.Internal(err)
		}
		var pcaps map[string]json.RawMessage
		if err := json.Unmarshal(pspec["capabilities"], &pcaps); err == nil && pcaps != nil {
			caps = pcaps
		}
		var paliases map[string]json.RawMessage
		if e := pcaps["ext"]; e != nil {
			if err := json.Unmarshal(e, &paliases); err == nil && paliases != nil {
				aliases = paliases
			}
		}
	}

	trees := map[string]module.Tree{}
	for _, alias := range slices.Sorted(maps.Keys(mods)) {
		ref := mods[alias]
		name := ref.Name
		if name == "" {
			name = alias
		}
		if !module.ValidName(name) {
			return nil, nil, service.Errf(types.ErrCodeBadSpec, "modules: mount %q: invalid module name %q", alias, name)
		}
		if ref.Version < 0 {
			return nil, nil, service.Errf(types.ErrCodeBadSpec, "modules: mount %q: version must be positive", alias)
		}
		if ref.Sum != "" && !module.ValidSum(ref.Sum) {
			return nil, nil, service.Errf(types.ErrCodeBadSpec, "modules: mount %q: invalid sum shape", alias)
		}
		var tr module.Tree
		if ov, ok := overrides[alias]; ok {
			// Trusted override (internal @self mount only): must match
			// the reference exactly — name, pinned sum, and Version 0,
			// which only overrides may carry.
			if ov.Name != name || ov.Sum == "" || ov.Version != 0 ||
				(ref.Sum != "" && ov.Sum != ref.Sum) || ref.Version != 0 {
				return nil, nil, service.Errf(types.ErrCodeBadSpec, "modules: mount %q: override does not match reference", alias)
			}
			if err := module.ValidateTree(&ov); err != nil {
				return nil, nil, service.Errf(types.ErrCodeBadSpec, "modules: mount %q: override invalid", alias)
			}
			tr = ov
		} else {
			row, err := s.st.GetModuleByName(ctx, wsID, name)
			if err != nil {
				return nil, nil, service.Errf(types.ErrCodeBadSpec, "modules: mount %q: unknown module %q", alias, name)
			}
			var ver *store.ModuleVersion
			if ref.Version > 0 {
				ver, err = s.st.GetModuleVersion(ctx, row.ID, ref.Version)
				if err != nil {
					return nil, nil, service.Errf(types.ErrCodeBadSpec, "modules: mount %q: %s has no published version %d", alias, name, ref.Version)
				}
			} else {
				vers, lerr := s.st.ListModuleVersions(ctx, row.ID)
				if lerr != nil {
					return nil, nil, service.Internal(lerr)
				}
				if len(vers) == 0 {
					return nil, nil, service.Errf(types.ErrCodeBadSpec, "modules: mount %q: %s has no published version", alias, name)
				}
				ver = vers[len(vers)-1]
			}
			if ref.Sum != "" && ref.Sum != ver.Sum {
				return nil, nil, service.Errf(types.ErrCodeBadSpec, "modules: mount %q: pinned sum does not match version %d", alias, ver.Version)
			}
			tr = module.Tree{Name: name, Version: ver.Version, Sum: ver.Sum, Files: ver.Files}
			if err := module.ValidateTree(&tr); err != nil {
				return nil, nil, service.Errf(types.ErrCodeBadSpec, "modules: mount %q: stored version %d is invalid", alias, ver.Version)
			}
		}
		trees[alias] = tr

		// Requirements from the tree's manifest, if present.
		var mf *module.Manifest
		if data, ok := tr.Files[module.ManifestFile]; ok {
			var perr error
			mf, perr = module.ParseManifest(data)
			if perr != nil {
				return nil, nil, service.Errf(types.ErrCodeBadSpec, "modules: mount %q has invalid %s", alias, module.ManifestFile)
			}
		}
		var capsRequired []string
		var extRequired map[string]module.ExtRequirement
		if mf != nil {
			capsRequired = mf.Requires.Capabilities
			extRequired = mf.Requires.Ext
		}
		for _, c := range capsRequired {
			if types.CapabilityName(c) == types.CapIO {
				continue // io is implicit
			}
			cur := caps[c]
			if cur == nil || string(cur) == "null" {
				return nil, nil, service.Errf(types.ErrCodeBadSpec, "modules: mount %q requires capability %q", alias, c)
			}
		}
		introduced := []string{}
		for _, ealias := range slices.Sorted(maps.Keys(extRequired)) {
			req := extRequired[ealias]
			if req.Source == "" || !module.ValidSum(req.Sum) {
				return nil, nil, service.Errf(types.ErrCodeBadSpec, "modules: mount %q requires ext %q without a pinned sum", alias, ealias)
			}
			if s.manifests == nil {
				return nil, nil, service.Errf(types.ErrCodeBadSpec, "modules: extension manifest resolution unavailable")
			}
			if _, err := s.manifests.Manifest(ctx, req.Source, req.Sum); err != nil {
				return nil, nil, service.Errf(types.ErrCodeBadSpec, "modules: mount %q: extension %q source cannot be inspected", alias, ealias)
			}
			existing, present := aliases[ealias]
			if present {
				var cur struct {
					Source string `json:"source"`
					Sum    string `json:"sum"`
				}
				_ = json.Unmarshal(existing, &cur)
				if cur.Source != req.Source || cur.Sum != req.Sum {
					return nil, nil, service.Errf(types.ErrCodeBadSpec, "modules: mount %q requires ext %q pinned differently than the spec", alias, ealias)
				}
				continue // same pins: dedupe, consumer config preserved
			}
			entry, _ := json.Marshal(map[string]string{"source": req.Source, "sum": req.Sum})
			aliases[ealias] = entry
			introduced = append(introduced, ealias)
		}
		// Recompute provenance: what resolution actually introduced —
		// never trusted from input, and never a deletion signal.
		ref.Name, ref.Version, ref.Sum = name, tr.Version, tr.Sum
		ref.Ext = introduced
		mods[alias] = ref
	}
	if err := module.ValidateMounts(trees); err != nil {
		return nil, nil, service.Errf(types.ErrCodeBadSpec, "%s", err)
	}

	spec["modules"], _ = json.Marshal(mods)
	if len(aliases) > 0 {
		eb, _ := json.Marshal(aliases)
		caps["ext"] = eb
		cb, _ := json.Marshal(caps)
		spec["capabilities"] = cb
	}
	out, err := json.Marshal(spec)
	if err != nil {
		return nil, nil, service.Internal(err)
	}
	return json.RawMessage(out), trees, nil
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// injectDebugSelf adds the trusted @self mount reference to raw. The
// tree must be a valid, summed, Version-0 draft snapshot. Caller-owned
// "self" is reserved — any user-supplied mount with that alias fails.
// Absent/null/empty modules maps are fine; non-object spec or
// non-object modules are rejected.
func injectDebugSelf(raw []byte, tree module.Tree) ([]byte, error) {
	if err := module.ValidateTree(&tree); err != nil || tree.Version != 0 {
		return nil, service.Errf(types.ErrCodeBadSpec, "debug: invalid self tree")
	}
	var spec map[string]json.RawMessage
	if len(raw) == 0 {
		spec = map[string]json.RawMessage{}
	} else if err := json.Unmarshal(raw, &spec); err != nil || spec == nil {
		return nil, service.Errf(types.ErrCodeBadSpec, "invalid spec")
	}
	mods := map[string]json.RawMessage{}
	if cur, ok := spec["modules"]; ok && len(cur) > 0 && string(cur) != "null" {
		if err := json.Unmarshal(cur, &mods); err != nil || mods == nil {
			return nil, service.Errf(types.ErrCodeBadSpec, "invalid modules shape")
		}
		if _, has := mods["self"]; has {
			return nil, service.Errf(types.ErrCodeBadSpec, `debug: "self" mount is reserved`)
		}
	}
	mods["self"], _ = json.Marshal(module.Reference{Name: tree.Name, Version: 0, Sum: tree.Sum})
	spec["modules"], _ = json.Marshal(mods)
	return json.Marshal(spec)
}

// DebugBoundary resolves raw like the ordinary boundary but injects a
// trusted @self tree — the module's current draft, pinned by the
// caller. Configure-scoped; resolve=false (descriptor) is for settings
// save, runs use resolve=true.
func (s *Service) DebugBoundary(ctx context.Context, a service.Actor, raw []byte, tree module.Tree, resolve bool) (*Boundary, error) {
	if err := s.access.ModuleScope(ctx, a, access.Configure); err != nil {
		return nil, err
	}
	raw2, err := injectDebugSelf(raw, tree)
	if err != nil {
		return nil, err
	}
	return s.buildBoundaryWith(ctx, a, raw2, resolve, map[string]module.Tree{"self": tree})
}

// CreateDebug resolves the raw spec once — injected self tree, real
// quota, real placement, real runtime — no second module lookup.
// Only trusted code paths call it; HTTP create cannot pass trees.
func (s *Service) CreateDebug(ctx context.Context, a service.Actor, raw []byte, tree module.Tree) (*store.Instance, error) {
	if err := s.access.ModuleScope(ctx, a, access.Configure); err != nil {
		return nil, err
	}
	if err := s.checkUserLimit(ctx, a); err != nil {
		return nil, err
	}
	raw2, err := injectDebugSelf(raw, tree)
	if err != nil {
		return nil, err
	}
	typed, resolved, secs, mods, err := s.validateSpecWithModules(ctx, a, raw2, true, true, map[string]module.Tree{"self": tree})
	if err != nil {
		return nil, err
	}
	return s.place(ctx, a, typed, resolved, secs, mods, typed.Labels, typed.TTLSeconds, "", a.WorkspaceID)
}
