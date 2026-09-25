// Package prompt renders the server-side agent system prompt for a
// calcside instance from an embedded template. Capability sections are
// owned by each capability factory (capability.Factory.Prompt) so
// clients never assemble capability instructions themselves.
package prompt

import (
	"bytes"
	_ "embed"
	"fmt"
	"sort"
	"strings"
	"text/template"
)

//go:embed prompt.md.tmpl
var tmplText string

var tmpl = template.Must(template.New("prompt").Funcs(template.FuncMap{
	"join": strings.Join,
}).Parse(tmplText))

// SecretInfo describes one secret for the prompt: name and allowed
// domains only. Secret values must never reach this package.
type SecretInfo struct {
	Name    string
	Domains []string
}

// Input is everything the template needs for one instance.
type Input struct {
	InstanceID string
	Prefix     string
	// Fragments are the capability-owned markdown sections in display
	// order (registry order; only granted capabilities).
	Fragments []string
	// CapNames mirrors Fragments (used for the capabilities field and
	// example selection).
	CapNames []string
	// Env is the instance's non-sensitive env map.
	Env map[string]string
	// Secrets lists names + domains only.
	Secrets []SecretInfo
	// Limits in milliseconds/counts.
	ExecTimeoutMs  int64
	MaxSteps       uint64
	MaxOutputBytes int64
	TTLSeconds     int64
	// NetExampleHost is the first net allow_hosts entry, for the worked
	// example; empty when net is not granted.
	NetExampleHost string
	// Persistent marks a reused (cross-run) instance.
	Persistent bool
}

// Render produces the prompt markdown.
func Render(in Input) (string, error) {
	has := func(name string) bool {
		for _, c := range in.CapNames {
			if c == name {
				return true
			}
		}
		return false
	}
	envKeys := make([]string, 0, len(in.Env))
	for k := range in.Env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)

	var limitBits []string
	if in.ExecTimeoutMs > 0 {
		limitBits = append(limitBits, fmt.Sprintf("exec timeout %dms", in.ExecTimeoutMs))
	}
	if in.MaxSteps > 0 {
		limitBits = append(limitBits, fmt.Sprintf("%d max steps", in.MaxSteps))
	}
	if in.MaxOutputBytes > 0 {
		limitBits = append(limitBits, fmt.Sprintf("output cap %d bytes", in.MaxOutputBytes))
	}
	if in.TTLSeconds > 0 {
		limitBits = append(limitBits, fmt.Sprintf("instance TTL %ds", in.TTLSeconds))
	}

	data := map[string]any{
		"InstanceID":     in.InstanceID,
		"Prefix":         in.Prefix,
		"Persistent":     in.Persistent,
		"HasFS":          has("fs"),
		"Fragments":      in.Fragments,
		"Secrets":        in.Secrets,
		"EnvKeys":        envKeys,
		"LimitBits":      limitBits,
		"NetExampleHost": in.NetExampleHost,
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}
