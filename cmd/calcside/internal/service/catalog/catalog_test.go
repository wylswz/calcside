package catalog

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"calcside/internal/capability/ext"
)

type fakeCatalog struct{ c ext.Catalog }

func (f *fakeCatalog) Available() ext.Catalog { return f.c }

// The icon snapshot covers every unique icon in the current catalog — no
// FIFO cap, no partial map, no mutation of the provider's own Info values.
func TestExtensionsIconSnapshot(t *testing.T) {
	prov := &fakeCatalog{c: ext.Catalog{Extensions: make([]ext.Info, 1000), LocalEnabled: true}}
	for i := range prov.c.Extensions {
		prov.c.Extensions[i] = ext.Info{
			Name: fmt.Sprintf("ext-%04d", i), Source: fmt.Sprintf("contrib/ext%d", i),
			Icon: []byte(fmt.Sprintf("icon-%d", i)),
		}
	}
	s := New(nil, prov)

	out := s.Extensions()
	if len(out.Extensions) != 1000 {
		t.Fatalf("%d entries", len(out.Extensions))
	}
	for i, e := range out.Extensions {
		if e.IconURL == "" {
			t.Fatalf("entry %d missing IconURL", i)
		}
		want := []byte(fmt.Sprintf("icon-%d", i))
		got := s.Icon(strings.TrimPrefix(e.IconURL, "/api/v1/extensions/icons/"))
		if string(got) != string(want) {
			t.Fatalf("entry %d: %q != %q", i, got, want)
		}
		// provider's own Info is untouched — Icon still set, no IconURL
		if prov.c.Extensions[i].Icon == nil || prov.c.Extensions[i].IconURL != "" {
			t.Fatalf("provider Info mutated at %d", i)
		}
	}

	// concurrent readers always see a coherent full map
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out := s.Extensions()
			for _, e := range out.Extensions {
				if got := s.Icon(strings.TrimPrefix(e.IconURL, "/api/v1/extensions/icons/")); len(got) == 0 {
					t.Errorf("concurrent catalog omitted icon for %s", e.Source)
				}
			}
		}()
	}
	wg.Wait()

	// catalog change replaces the whole map — stale ids retire
	prov.c.Extensions = prov.c.Extensions[:2]
	_ = s.Extensions()
	if got := s.Icon(strings.TrimPrefix(out.Extensions[999].IconURL, "/api/v1/extensions/icons/")); got != nil {
		t.Fatal("retired catalog icon is still cached")
	}
	if got := len(s.icons); got != 2 {
		t.Fatalf("%d icons after shrink, want 2", got)
	}
}
