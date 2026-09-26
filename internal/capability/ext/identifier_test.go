package ext

import (
	"strings"
	"testing"
)

func TestParseIdentifier(t *testing.T) {
	cases := []struct {
		in      string
		wantErr string
		remote  bool
	}{
		{"github.com/wylswz/tavily@v0.1.0", "", true},
		{"github.com/wylswz/tavily@0123456789abcdef0123456789abcdef0123", "", true},
		{"github.com/a/b_c-d.e@v1", "", true},
		{"/tmp/ext/tavily", "", false},
		{"github.com/wylswz/tavily", "@version", true},
		{"github.com//tavily@v1", "invalid", true},
		{"github.com/../x@v1", "invalid", true},
		{"github.com/a/b@v 1", "invalid", true},
		{"github.com/a/b c@v1", "invalid", true},
		{"github.com/a/b@v1;rm", "invalid", true},
		{"github.com/a/b/extra@v1", "invalid", true},
		{"", "invalid", false},
	}
	for _, tc := range cases {
		p, err := ParseIdentifier(tc.in)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("%q: want error containing %q, got %v", tc.in, tc.wantErr, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", tc.in, err)
			continue
		}
		if got := CapabilityIdentifier(tc.in).IsRemote(); got != tc.remote {
			t.Errorf("%q: IsRemote=%v want %v", tc.in, got, tc.remote)
		}
		if tc.remote && (p.Domain == "" || p.Group == "" || p.Name == "" || p.Version == "") {
			t.Errorf("%q: missing field in %+v", tc.in, p)
		}
	}
}

func TestIsRemote(t *testing.T) {
	if !CapabilityIdentifier("github.com/a/b@v1").IsRemote() {
		t.Fatal("remote id not remote")
	}
	if CapabilityIdentifier("/abs/path").IsRemote() {
		t.Fatal("local path marked remote")
	}
}
