package application

import (
	"encoding/json"

	"calcside/cmd/calcside/internal/service/sandbox"

	mod "calcside/internal/module"
)

// draft.go: the stored draft keeps normalized configuration but the
// SELECTORS the user actually wrote — an unpinned module or ext ref
// must keep resolving "latest" at publish time, not freeze to whatever
// was current at save. Releases/instances are always pinned; drafts
// are not.
//
// applicationDraftSpec takes the caller's raw spec and the boundary's
// fully normalized+pinned spec, and returns the spec to store:
//   - modules: restore each raw ref's version/sum/ext selector state —
//     version 0 (latest) stays 0, unpinned stays unpinned; provenance
//     Ext is ignored entirely (server-recomputed at publish).
//   - capabilities.ext: keep normalized config for aliases the raw
//     spec declared; drop aliases cascaded in by module requirements;
//     restore the raw (possibly empty) sum when the caller left it
//     unpinned.
//   - providers: drop generated entries when the raw spec didn't
//     contain them.
//   - everything else (defaults, env, secrets refs, limits, policies,
//     ttl) stays as the normalized boundary produced.
func applicationDraftSpec(raw, normalized json.RawMessage) json.RawMessage {
	// Strip generated environment secrets/config placeholders FIRST —
	// a re-edited draft must not collide with derived EXT_BIND_*
	// entries from the previous save.
	if stripped, err := sandbox.StripEnvironmentDerived(normalized); err == nil {
		normalized = stripped
	}
	var r map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &r) != nil || r == nil {
		return normalized
	}
	var n map[string]json.RawMessage
	if json.Unmarshal(normalized, &n) != nil || n == nil {
		return normalized
	}

	// --- modules: restore raw selector state ---
	if rawMods, ok := r["modules"]; ok && len(rawMods) > 0 && string(rawMods) != "null" {
		var rm map[string]mod.Reference
		var nm map[string]json.RawMessage
		if err := json.Unmarshal(rawMods, &rm); err == nil {
			if err := json.Unmarshal(n["modules"], &nm); err != nil || nm == nil {
				nm = map[string]json.RawMessage{}
			}
			{
				for alias, ref := range rm {
					// Restore caller selectors; normalized name/version/
					// sum/Ext from resolution are dropped except the
					// library resolution will recompute them at publish.
					out := map[string]any{"name": ref.Name}
					if ref.Version != 0 {
						out["version"] = ref.Version
					}
					if ref.Sum != "" {
						out["sum"] = ref.Sum
					}
					// Ext provenance is server-recomputed; caller-supplied
					// ext lists are ignored for drafts too.
					b, _ := json.Marshal(out)
					nm[alias] = b
				}
				n["modules"], _ = json.Marshal(nm)
			}
		}
	}

	// --- capabilities.ext: drop cascaded aliases, restore unpinned sums ---
	var rawCaps struct {
		Ext map[string]json.RawMessage `json:"ext"`
	}
	if c, ok := r["capabilities"]; ok {
		_ = json.Unmarshal(c, &rawCaps)
	}
	if c, ok := n["capabilities"]; ok {
		var nc map[string]json.RawMessage
		if err := json.Unmarshal(c, &nc); err == nil && nc != nil {
			var nce struct {
				Ext map[string]json.RawMessage `json:"ext"`
			}
			_ = json.Unmarshal(c, &nce)
			if nce.Ext != nil {
				changed := false
				for alias := range nce.Ext {
					rawEntry, declared := rawCaps.Ext[alias]
					if !declared {
						delete(nce.Ext, alias) // cascaded by module requires
						changed = true
						continue
					}
					// Restore unpinned sum when the caller omitted it, and
					// drop the generated llm registry the raw alias
					// didn't declare.
					var re struct {
						Sum string `json:"sum"`
						LLM any    `json:"llm"`
					}
					_ = json.Unmarshal(rawEntry, &re)
					var ne map[string]json.RawMessage
					if err := json.Unmarshal(nce.Ext[alias], &ne); err == nil {
						dirty := false
						if re.Sum == "" {
							if _, has := ne["sum"]; has {
								delete(ne, "sum")
								dirty = true
							}
						}
						if re.LLM == nil {
							if _, has := ne["llm"]; has {
								delete(ne, "llm")
								dirty = true
							}
						}
						if dirty {
							b, _ := json.Marshal(ne)
							nce.Ext[alias] = b
							changed = true
						}
					}
				}
				if changed {
					if len(nce.Ext) == 0 {
						delete(nc, "ext") // Factory rejects empty ext map
					} else {
						nc["ext"], _ = json.Marshal(nce.Ext)
					}
					n["capabilities"], _ = json.Marshal(nc)
				}
			}
		}
	}

	// --- providers: drop generated llm registry when raw lacked it ---
	if _, had := r["providers"]; !had {
		if _, gen := n["providers"]; gen {
			delete(n, "providers")
		}
	}

	out, err := json.Marshal(n)
	if err != nil {
		return normalized
	}
	return out
}
