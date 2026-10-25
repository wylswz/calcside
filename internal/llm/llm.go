// Package llm defines the protocol between the `llm` capability
// extension and provider extensions. Model catalogues live in provider
// extension metadata; this package contains no vendor model data.
package llm

import (
	"fmt"
	"regexp"
	"strings"
)

// Interface is the provider extension interface (capability.yaml).
const Interface = "llm"

// Capability marks the capability extension that consumes providers.
const Capability = "llm"

// ProviderOps are the ops every provider extension must export. The
// consumer also exposes list_models, synthesized from the provider
// manifest's models metadata.
var ProviderOps = []string{"respond", "structured"}

var (
	secretRefRe  = regexp.MustCompile(`^\{\{\s*secrets\.([A-Z_][A-Z0-9_]{0,63})\s*\}\}$`)
	secretFindRe = regexp.MustCompile(`\{\{\s*secrets\.([A-Z_][A-Z0-9_]{0,63})\s*\}\}`)
)

// NormalizeModels trims and de-duplicates a model catalogue. A provider
// may still be configured without models only if its extension manifest
// has none (openai_compatible); the service enforces that.
func NormalizeModels(models []string) ([]string, error) {
	if len(models) > 64 {
		return nil, fmt.Errorf("llm provider: at most 64 models")
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(models))
	for _, m := range models {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}
		if len(m) > 128 {
			return nil, fmt.Errorf("llm provider: model name is too long")
		}
		if seen[m] {
			return nil, fmt.Errorf("llm provider: duplicate model %q", m)
		}
		seen[m] = true
		out = append(out, m)
	}
	return out, nil
}

// SecretRefs lists distinct vault names referenced anywhere in s.
func SecretRefs(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range secretFindRe.FindAllStringSubmatch(s, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// SecretName returns the vault name referenced by a normalized config.
func SecretName(cfg map[string]any) string {
	s, _ := cfg["api_key"].(string)
	if m := secretRefRe.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}
