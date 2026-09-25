package types

import (
	"os"
	"reflect"
	"testing"

	yaml "go.yaml.in/yaml/v3"
)

func strs[T ~string](xs []T) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = string(x)
	}
	return out
}

// TestOpenAPIEnumsInSync parses api/openapi.yaml — the contract that
// generated server/client code is produced from — and asserts each
// enum schema equals the corresponding Go All*() values, so the
// contract and the domain types cannot drift apart.
func TestOpenAPIEnumsInSync(t *testing.T) {
	data, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Skip("api/openapi.yaml not found")
	}
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Enum []string `yaml:"enum"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	found := map[string][]string{}
	for name, s := range doc.Components.Schemas {
		if s.Enum != nil {
			found[name] = s.Enum
		}
	}

	want := map[string][]string{
		"InstanceStatus": strs(AllInstanceStatuses()),
		"ExecStatus":     strs(AllExecStatuses()),
		"ExecErrorType":  strs(AllExecErrorTypes()),
		"Decision":       strs(AllDecisions()),
		"Phase":          append([]string{string(PhaseNone)}, strs(AllPhases())...),
		"CapabilityName": strs(AllCapabilityNames()),
		"HTTPMethod":     strs(AllHTTPMethods()),
		"SecretSource":   strs(AllSecretSources()),
		"APIErrorCode":   strs(AllAPIErrorCodes()),
		"FieldType":      strs(AllFieldTypes()),
		"AuthKind":       strs(AllAuthKinds()),
	}
	for name, w := range want {
		got, ok := found[name]
		if !ok {
			t.Fatalf("openapi.yaml missing enum schema %s", name)
		}
		if !reflect.DeepEqual(got, w) {
			t.Fatalf("%s: openapi=%v go=%v", name, got, w)
		}
	}
	for name := range found {
		if _, ok := want[name]; !ok {
			t.Fatalf("openapi.yaml has unmapped enum schema %s", name)
		}
	}
}

func TestEnumsValidate(t *testing.T) {
	var st InstanceStatus
	if err := st.UnmarshalText([]byte("bogus")); err == nil {
		t.Fatal("bad status accepted")
	}
	if err := st.UnmarshalText([]byte("running")); err != nil || st != InstanceRunning {
		t.Fatal(err)
	}
	var m HTTPMethod
	if err := m.UnmarshalText([]byte("get")); err != nil || m != MethodGet {
		t.Fatalf("method not normalized: %v %q", err, m)
	}
	if err := m.UnmarshalText([]byte("FETCH")); err == nil {
		t.Fatal("bad method accepted")
	}
	if !ExecErrorType("").Valid() || ExecErrorType("bogus").Valid() {
		t.Fatal("ExecErrorType Valid wrong")
	}
	if !Phase("").Valid() || Phase("middle").Valid() {
		t.Fatal("Phase Valid wrong")
	}
}
