package types

import (
	"os"
	"reflect"
	"regexp"
	"testing"
)

func strs[T ~string](xs []T) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = string(x)
	}
	return out
}

// TestWebEnumsInSync reads web/src/enums.ts and asserts each exported
// `as const` array equals the corresponding Go All*() values, so the
// frontend and backend enum sets cannot drift apart.
func TestWebEnumsInSync(t *testing.T) {
	data, err := os.ReadFile("../../web/src/enums.ts")
	if err != nil {
		t.Skip("web/src/enums.ts not found")
	}
	constRe := regexp.MustCompile(`export const (\w+) = \[([^\]]*)\] as const`)
	valRe := regexp.MustCompile(`'([^']*)'`)
	found := map[string][]string{}
	for _, m := range constRe.FindAllSubmatch(data, -1) {
		var vals []string
		for _, v := range valRe.FindAllSubmatch(m[2], -1) {
			vals = append(vals, string(v[1]))
		}
		found[string(m[1])] = vals
	}

	want := map[string][]string{
		"INSTANCE_STATUSES": strs(AllInstanceStatuses()),
		"EXEC_STATUSES":     strs(AllExecStatuses()),
		"EXEC_ERROR_TYPES":  strs(AllExecErrorTypes()),
		"DECISIONS":         strs(AllDecisions()),
		"PHASES":            append([]string{string(PhaseNone)}, strs(AllPhases())...),
		"CAPABILITY_NAMES":  strs(AllCapabilityNames()),
		"HTTP_METHODS":      strs(AllHTTPMethods()),
		"SECRET_SOURCES":    strs(AllSecretSources()),
		"API_ERROR_CODES":   strs(AllAPIErrorCodes()),
		"FIELD_TYPES":       strs(AllFieldTypes()),
	}
	for name, w := range want {
		got, ok := found[name]
		if !ok {
			t.Fatalf("enums.ts missing export %s", name)
		}
		if !reflect.DeepEqual(got, w) {
			t.Fatalf("%s: web=%v go=%v", name, got, w)
		}
	}
	for name := range found {
		if _, ok := want[name]; !ok {
			t.Fatalf("enums.ts has unmapped export %s", name)
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
