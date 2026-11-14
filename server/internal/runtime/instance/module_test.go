package instance

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"calcside/internal/extartifact"
	"calcside/internal/module"
	"calcside/internal/runtime"
)

// moduleTree builds a pinned tree for CreateRequest.Modules.
func moduleTree(t *testing.T, name string, files map[string]string) module.Tree {
	t.Helper()
	fb := map[string][]byte{}
	for n, s := range files {
		fb[n] = []byte(s)
	}
	sum, err := extartifact.TreeHash(fb)
	if err != nil {
		t.Fatal(err)
	}
	return module.Tree{Name: name, Version: 1, Sum: sum, Files: fb}
}

// TestModuleTreesReachExec covers the contract path: trees on the
// CreateRequest become @ mounts the instance's exec can load.
func TestModuleTreesReachExec(t *testing.T) {
	n := newNode(t, Options{}, nil)
	id := fmt.Sprintf("ins_test%06d", idSeq.Add(1))
	mounts := map[string]module.Tree{
		"strkit": moduleTree(t, "strkit", map[string]string{
			"u.star": "def shout(s):\n    return s.upper()",
		}),
	}
	spec := fmt.Sprintf(`{"capabilities":{},"modules":{"strkit":{"name":"strkit","version":1,"sum":%q}}}`, mounts["strkit"].Sum)
	_, err := n.m.Create(context.Background(), &runtime.CreateRequest{
		InstanceID: id,
		Owner:      n.owner,
		Spec:       []byte(spec),
		Modules:    mounts,
		ExpiresAt:  time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	res := n.mustExec(id, `load("@strkit/u.star", "shout")
print(shout("ok"))`)
	if !strings.Contains(res.Output, "OK") {
		t.Fatalf("output %q", res.Output)
	}

	// A declared mount without its tree fails create closed — the node
	// must not silently create a module-less instance.
	id2 := fmt.Sprintf("ins_test%06d", idSeq.Add(1))
	_, err = n.m.Create(context.Background(), &runtime.CreateRequest{
		InstanceID: id2, Owner: n.owner,
		Spec:      []byte(spec),
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if err == nil {
		t.Fatal("missing tree created a module-less instance")
	}
	// An undeclared tree is rejected too.
	id3 := fmt.Sprintf("ins_test%06d", idSeq.Add(1))
	_, err = n.m.Create(context.Background(), &runtime.CreateRequest{
		InstanceID: id3, Owner: n.owner,
		Spec:      []byte(`{"capabilities":{}}`),
		Modules:   mounts,
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if err == nil {
		t.Fatal("undeclared tree accepted")
	}
	// A tree whose content no longer hashes to the spec pin fails.
	tampered := map[string]module.Tree{
		"strkit": moduleTree(t, "strkit", map[string]string{"u.star": "x = 1"}),
	}
	tamperedRef := fmt.Sprintf(`{"capabilities":{},"modules":{"strkit":{"name":"strkit","version":1,"sum":%q}}}`, tampered["strkit"].Sum)
	t2 := tampered["strkit"]
	t2.Files = map[string][]byte{"u.star": []byte("tampered")}
	tampered["strkit"] = t2
	id4 := fmt.Sprintf("ins_test%06d", idSeq.Add(1))
	_, err = n.m.Create(context.Background(), &runtime.CreateRequest{
		InstanceID: id4, Owner: n.owner,
		Spec:      []byte(tamperedRef),
		Modules:   tampered,
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if err == nil {
		t.Fatal("tampered tree created")
	}
}
