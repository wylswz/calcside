package runtime

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

// ExtensionRef names immutable source bytes; configuration and vault
// references remain in Spec, never in cache-control metadata.
type ExtensionRef struct {
	Source string `json:"source"`
	Sum    string `json:"sum"`
}

func ValidExtensionSum(sum string) bool {
	if !strings.HasPrefix(sum, "h1:") {
		return false
	}
	b, err := base64.StdEncoding.Strict().DecodeString(strings.TrimPrefix(sum, "h1:"))
	return err == nil && len(b) == 32
}

// ExtensionRequirements includes provider modules as well as the llm
// wrapper. Unrelated JSON values are not decoded or re-serialized.
func ExtensionRequirements(raw json.RawMessage) ([]ExtensionRef, error) {
	var spec struct {
		Capabilities struct {
			Ext map[string]struct {
				ExtensionRef
				LLM []ExtensionRef `json:"llm"`
			} `json:"ext"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		return nil, errors.New("invalid extension requirements")
	}
	seen := map[ExtensionRef]bool{}
	add := func(ref ExtensionRef) error {
		if ref.Source == "" || len(ref.Source) > 512 || !ValidExtensionSum(ref.Sum) {
			return errors.New("extensions must be pinned to an integrity sum")
		}
		seen[ref] = true
		if len(seen) > 128 {
			return errors.New("too many extension requirements")
		}
		return nil
	}
	for _, alias := range spec.Capabilities.Ext {
		if err := add(alias.ExtensionRef); err != nil {
			return nil, err
		}
		for _, provider := range alias.LLM {
			if err := add(provider); err != nil {
				return nil, err
			}
		}
	}
	out := make([]ExtensionRef, 0, len(seen))
	for ref := range seen {
		out = append(out, ref)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Source == out[j].Source {
			return out[i].Sum < out[j].Sum
		}
		return out[i].Source < out[j].Source
	})
	return out, nil
}
