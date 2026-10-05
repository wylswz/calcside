package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"calcside/internal/capability"
	capio "calcside/internal/capability/io"
)

func TestSessionUtilitiesAndCompletion(t *testing.T) {
	reg := capability.NewRegistry()
	reg.Register(capio.Factory())
	session, err := NewSession(SessionDeps{Context: context.Background(), Registry: reg}, SessionCreation{InstanceID: "utilities", MaxOutputBytes: 4096, MaxSteps: 100000})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	result := New(1).Exec(context.Background(), session, "utilities", `print(csv.parse_dicts("id,value\nA,001\n"))
print(datetime.parse_date("1970-01-01"))
print(base64.encode("hello"))`, time.Second, 100000, session.Out.String)
	if result.Error != nil {
		t.Fatal(result.Error)
	}
	if !strings.Contains(result.Output, "001") || !strings.Contains(result.Output, "aGVsbG8=") {
		t.Fatalf("output: %s", result.Output)
	}
	if session.Predeclared["net"] != nil || session.Predeclared["fs"] != nil {
		t.Fatal("utilities granted side-effect capabilities")
	}
	completion, err := session.Completions(context.Background(), reg)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"url.query_encode", "csv.parse_dicts", "base64.decode", "hashlib.sha256", "regex.search", "datetime.parse_date"} {
		found := false
		for _, symbol := range completion.Symbols {
			if symbol.Name == name {
				found = len(symbol.Params) > 0 && symbol.Doc != ""
			}
		}
		if !found {
			t.Errorf("missing utility completion metadata: %s", name)
		}
	}
}
