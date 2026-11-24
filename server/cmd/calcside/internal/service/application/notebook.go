package application

import (
	"encoding/json"
	"slices"

	"calcside/internal/notebook"
	"calcside/internal/runtime"
	"calcside/internal/store"
)

// DraftNotebook derives the saved-draft dependency graph and entry
// hints for a notebook application: pure static analysis over the
// stored sections, never execution. Entries map section ID to the
// direct defs statically visible as entry points — main always, plus
// literal interrupt.wait handler/on_expire targets. This is draft
// presentation metadata; it is not the runtime trace and never
// validates eligibility.
func DraftNotebook(a *store.Application) (notebook.Graph, map[string][]string) {
	var spec runtime.Spec
	caps := []string{"io"}
	if json.Unmarshal(a.Spec, &spec) == nil {
		for n := range spec.Capabilities {
			caps = append(caps, n)
		}
	}
	facts := make([]notebook.GraphSection, 0, len(a.Sections))
	defsBy := make(map[string][]string, len(a.Sections))
	targets := map[string]bool{"main": true}
	for _, sec := range a.Sections {
		gs, defs, hints := notebook.ApplicationSourceFacts(sec.ID, sec.Code, caps)
		facts = append(facts, gs)
		defsBy[sec.ID] = defs
		for _, h := range hints {
			targets[h] = true
		}
	}
	graph := notebook.BuildGraph(facts)
	entries := make(map[string][]string, len(defsBy))
	for id, defs := range defsBy {
		var names []string
		for _, n := range defs {
			if targets[n] {
				names = append(names, n)
			}
		}
		if len(names) > 0 {
			slices.Sort(names)
			entries[id] = slices.Compact(names)
		}
	}
	return graph, entries
}
