package placement

import (
	"strings"
	"testing"
)

func TestNormalizeNodeAddrCanonical(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"worker:8090", "worker:8090"},
		{"Worker.SVC:8090", "worker.svc:8090"},
		{"w1.ns.svc.cluster.local:8090", "w1.ns.svc.cluster.local:8090"},
		{"localhost:8080", "localhost:8080"},
		{"127.0.0.1:8090", "127.0.0.1:8090"},
		{"[127.0.0.1]:8090", "127.0.0.1:8090"}, // bracketed IPv4 canonicalizes bare
		{"10.0.0.5:8090", "10.0.0.5:8090"},
		{"[::1]:8090", "[::1]:8090"},
		{"[fd00::5]:8090", "[fd00::5]:8090"},
		{"[::ffff:10.0.0.1]:8090", "[::ffff:10.0.0.1]:8090"},
		{"worker.:8090", "worker:8090"}, // one trailing root dot folds away
		{"worker:08090", "worker:8090"}, // numeric port canonicalizes
		{"worker:1", "worker:1"},
		{"worker:65535", "worker:65535"},
	} {
		got, err := NormalizeNodeAddr(tc.in)
		if err != nil {
			t.Fatalf("%q: %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("%q -> %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeNodeAddrRejects(t *testing.T) {
	for _, in := range []string{
		"",
		"   ",
		":8090",        // empty host
		"worker",       // missing port
		"worker:",      // empty port
		"worker:0",     // zero port
		"worker:65536", // port overflow
		"worker:99999",
		"worker:-1",
		"worker:+80",
		"worker:8x",
		"worker:80 ", // trailing whitespace
		" worker:80",
		"worker\t:80",
		"worker:80\n",
		"wor ker:80",
		"worker\x007:80", // control byte
		"worker\x7f:80",
		"http://worker:80",    // URL scheme
		"worker:80/path",      // path
		"worker:80?x=1",       // query
		"worker:80#frag",      // fragment
		"user@worker:80",      // userinfo
		"worker:80@evil:90",   // userinfo smuggle
		"0.0.0.0:8090",        // unspecified IPv4
		"[::]:8090",           // unspecified IPv6
		"::1:8090",            // unbracketed IPv6
		"[::1]",               // bracketed IPv6, no port
		"[fe80::1%eth0]:8090", // IPv6 zone
		"*:8090",              // wildcard host
		"*.svc:8090",          // wildcard DNS
		"-bad-:8090",
		"bad..name:8090",                        // empty label
		"bad_name:8090",                         // underscore not DNS
		"a" + strings.Repeat("b", 64) + ":8090", // label too long
		strings.Repeat("a", 250) + ".bb:8090",   // name too long
		strings.Repeat("x", 321) + ":1",         // whole addr too long
		"[::1]extra:8090",
		"worker:8090extra",
	} {
		if got, err := NormalizeNodeAddr(in); err == nil {
			t.Fatalf("%q accepted as %q", in, got)
		}
	}
}
