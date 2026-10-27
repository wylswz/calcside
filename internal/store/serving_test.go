package store

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"calcside/internal/runtime"
)

var tNow = time.Now().UTC().Truncate(time.Second)

func tSpec() json.RawMessage {
	// Secrets are a name->entry map, not a list.
	return json.RawMessage(`{"ttl_seconds":300,"capabilities":{"fs":{}},"secrets":{"T":{"source":"vault","ref":"v/t","allowed_domains":["x"]}}}`)
}

func tBundle() runtime.ProgramBundle {
	return runtime.ProgramBundle{
		Sources:      map[string][]byte{"main.star": []byte(`def main(i): return {}`)},
		InputSchema:  json.RawMessage(`{"type":"object"}`),
		OutputSchema: json.RawMessage(`{"type":"object"}`),
	}
}

func mkRel(t *testing.T, spec json.RawMessage, prog runtime.ProgramBundle) *ServingRelease {
	t.Helper()
	r, err := NewServingRelease("ws_1", "usr_1", spec, runtime.PolicyBundle{}, prog, tNow)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestNewServingReleaseStable(t *testing.T) {
	a := mkRel(t, tSpec(), tBundle())
	b := mkRel(t, tSpec(), tBundle())
	if a.ID != b.ID {
		t.Fatal("identical content must dedup to one id")
	}
	// Empty entrypoint defaults to main.star and hashes like the
	// explicit form.
	prog := tBundle()
	c := mkRel(t, tSpec(), prog)
	prog.Entrypoint = "main.star"
	d := mkRel(t, tSpec(), prog)
	if c.ID != d.ID {
		t.Fatal("default entrypoint must equal explicit")
	}
}

func TestNewServingReleaseRoleBoundary(t *testing.T) {
	// Source vs asset role changes the digest: a bundle with a file in
	// the wrong section cannot alias.
	a := mkRel(t, tSpec(), runtime.ProgramBundle{
		Sources:      map[string][]byte{"main.star": []byte("a"), "z.star": []byte("b")},
		Assets:       map[string][]byte{},
		InputSchema:  json.RawMessage(`{}`),
		OutputSchema: json.RawMessage(`{}`),
	})
	b := mkRel(t, tSpec(), runtime.ProgramBundle{
		Sources:      map[string][]byte{"main.star": []byte("a")},
		Assets:       map[string][]byte{"z.star": []byte("b")},
		InputSchema:  json.RawMessage(`{}`),
		OutputSchema: json.RawMessage(`{}`),
	})
	if a.ID == b.ID {
		t.Fatal("source/asset role collision")
	}
}

func TestNewServingReleaseSchemaOrder(t *testing.T) {
	// Key order inside a schema changes the digest.
	a := mkRel(t, tSpec(), runtime.ProgramBundle{
		Sources:      map[string][]byte{"main.star": []byte("x")},
		InputSchema:  json.RawMessage(`{"type":"object","additionalProperties":false}`),
		OutputSchema: json.RawMessage(`{}`),
	})
	b := mkRel(t, tSpec(), runtime.ProgramBundle{
		Sources:      map[string][]byte{"main.star": []byte("x")},
		InputSchema:  json.RawMessage(`{"additionalProperties":false,"type":"object"}`),
		OutputSchema: json.RawMessage(`{}`),
	})
	if a.ID != b.ID {
		// Canonical marshal normalizes key order, so reordered keys
		// MUST produce the same digest.
		t.Fatal("schema key order should normalize")
	}
}

func TestNewServingReleaseValidation(t *testing.T) {
	for name, fn := range map[string]func() error{
		"inline_secret": func() error {
			_, err := NewServingRelease("ws_1", "u", json.RawMessage(`{"secrets":{"T":{"source":"inline","ref":"v"}}}`), runtime.PolicyBundle{}, tBundle(), tNow)
			return err
		},
		"plaintext_value": func() error {
			_, err := NewServingRelease("ws_1", "u", json.RawMessage(`{"secrets":{"T":{"ref":"v","value":"x"}}}`), runtime.PolicyBundle{}, tBundle(), tNow)
			return err
		},
		"missing_ref": func() error {
			_, err := NewServingRelease("ws_1", "u", json.RawMessage(`{"secrets":{"T":{"source":"vault"}}}`), runtime.PolicyBundle{}, tBundle(), tNow)
			return err
		},
		"secrets_list": func() error {
			_, err := NewServingRelease("ws_1", "u", json.RawMessage(`{"secrets":[{"ref":"v"}]}`), runtime.PolicyBundle{}, tBundle(), tNow)
			return err
		},
		"ext_snapshot": func() error {
			_, err := NewServingRelease("ws_1", "u", json.RawMessage(`{"capabilities":{"ext":{"x":"h1:y"}}}`), runtime.PolicyBundle{}, tBundle(), tNow)
			return err
		},
		"bad_schema": func() error {
			p := tBundle()
			p.InputSchema = json.RawMessage(`"str"`)
			_, err := NewServingRelease("ws_1", "u", tSpec(), runtime.PolicyBundle{}, p, tNow)
			return err
		},
		"empty_sources": func() error {
			_, err := NewServingRelease("ws_1", "u", tSpec(), runtime.PolicyBundle{}, runtime.ProgramBundle{
				InputSchema: json.RawMessage(`{}`), OutputSchema: json.RawMessage(`{}`)}, tNow)
			return err
		},
		"abs_path": func() error {
			p := tBundle()
			p.Sources = map[string][]byte{"/x.star": []byte("a")}
			_, err := NewServingRelease("ws_1", "u", tSpec(), runtime.PolicyBundle{}, p, tNow)
			return err
		},
		"dotdot_path": func() error {
			p := tBundle()
			p.Sources = map[string][]byte{"a/../x.star": []byte("a")}
			_, err := NewServingRelease("ws_1", "u", tSpec(), runtime.PolicyBundle{}, p, tNow)
			return err
		},
		"backslash_path": func() error {
			p := tBundle()
			p.Sources = map[string][]byte{"a\\x.star": []byte("a")}
			_, err := NewServingRelease("ws_1", "u", tSpec(), runtime.PolicyBundle{}, p, tNow)
			return err
		},
		"missing_entrypoint": func() error {
			p := tBundle()
			p.Entrypoint = "nope.star"
			_, err := NewServingRelease("ws_1", "u", tSpec(), runtime.PolicyBundle{}, p, tNow)
			return err
		},
		"benign_dotdot_name": func() error {
			p := tBundle()
			p.Sources = map[string][]byte{"main.star": []byte("x"), "foo..bar": []byte("y")}
			p.Entrypoint = "main.star"
			_, err := NewServingRelease("ws_1", "u", tSpec(), runtime.PolicyBundle{}, p, tNow)
			return err
		},
	} {
		err := fn()
		if name == "benign_dotdot_name" {
			if err != nil {
				t.Fatalf("%s should be valid: %v", name, err)
			}
			continue
		}
		if !errors.Is(err, ErrInvalidServingRecord) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestNewServingReleaseClones(t *testing.T) {
	spec := tSpec()
	prog := tBundle()
	src := prog.Sources["main.star"]
	pol := runtime.PolicyBundle{Global: map[string]string{"g": "x"}}
	r, err := NewServingRelease("ws_1", "usr_1", spec, pol, prog, tNow)
	if err != nil {
		t.Fatal(err)
	}
	// Mutating the caller's buffers must not alter the record.
	prog.Sources["main.star"][0] = 'X'
	spec[0] = '['
	pol.Global["g"] = "changed"
	_ = src
	r2, err := NewServingRelease("ws_1", "usr_1", tSpec(), runtime.PolicyBundle{Global: map[string]string{"g": "x"}}, tBundle(), tNow)
	if err != nil {
		t.Fatal(err)
	}
	if r.ID != r2.ID || string(r.Spec) != string(r2.Spec) || r.Policies.Global["g"] != "x" {
		t.Fatal("caller mutation leaked")
	}
}
