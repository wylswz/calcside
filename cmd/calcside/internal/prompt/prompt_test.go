package prompt

import (
	"strings"
	"testing"
)

func TestRenderFSOnly(t *testing.T) {
	out, err := Render(Input{
		InstanceID:     "ins_1",
		Prefix:         "calcside_",
		Fragments:      []string{"#### `fs`\n- `fs.read(path)` — read file\n- Paths are rooted at `/work`.\n"},
		CapNames:       []string{"fs"},
		ExecTimeoutMs:  30000,
		MaxSteps:       1000,
		MaxOutputBytes: 4096,
		TTLSeconds:     900,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ins_1", "calcside_exec", "calcside_list_files", "fs.read(path)",
		"Starlark is NOT Python", "policy_denied", "exec timeout 30000ms",
		"instance TTL 900s",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	// no net or secrets sections
	for _, bad := range []string{"#### `net`", "### Secrets", "{{secrets."} {
		if strings.Contains(out, bad) {
			t.Fatalf("unexpected %q in:\n%s", bad, out)
		}
	}
}

func TestRenderFull(t *testing.T) {
	out, err := Render(Input{
		InstanceID: "ins_2",
		Prefix:     "box_",
		Fragments: []string{
			"#### `fs`\n- `fs.read(path)` — read\n",
			"#### `net`\n- `net.get(url, headers)` — GET\n- Allowed hosts: api.example.com\n- Allowed methods: GET, POST\n",
			"#### `io`\n- `io.println(...)` — print\n",
		},
		CapNames:       []string{"fs", "net", "io"},
		Env:            map[string]string{"REGION": "us", "API_HOST": "x"},
		Secrets:        []SecretInfo{{Name: "TOKEN", Domains: []string{"api.example.com"}}},
		ExecTimeoutMs:  5000,
		TTLSeconds:     600,
		NetExampleHost: "api.example.com",
		Persistent:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"box_exec", "box_read_file",
		"#### `net`", "api.example.com", "GET, POST",
		"### Secrets", "`TOKEN`", "{{secrets.NAME}}", "[REDACTED:NAME]",
		"### Environment", "API_HOST, REGION",
		"across agent runs", // Persistent
		`print(net.get("https://api.example.com/")["body"])`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

func TestRenderToolPrefixApplied(t *testing.T) {
	out, err := Render(Input{InstanceID: "i", Prefix: "sb_", CapNames: []string{"io"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "`sb_exec(code)`") {
		t.Fatalf("prefix not applied:\n%s", out)
	}
	if strings.Contains(out, "calcside_") {
		t.Fatal("default prefix leaked")
	}
}

func TestUtilityPrompt(t *testing.T) {
	out, err := Render(Input{InstanceID: "utilities"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"url.parse", "csv.parse_dicts", "base64.encode", "hashlib.sha256", "regex.search", "datetime.parse_date"} {
		if !strings.Contains(out, name) {
			t.Errorf("missing utility %s", name)
		}
	}
}
