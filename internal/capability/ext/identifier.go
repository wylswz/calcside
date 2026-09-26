package ext

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"calcside/internal/types"
)

// CapabilityIdentifier is a uniquely identifier of a capability
// the format is
// {domain}/{group}/{name}
// for example
// github.com/wylswz/openai
// for it can be a local path
// /path/to/capability/root/dir
//
// Remote identifiers must carry an explicit version:
// {domain}/{group}/{name}@{version} where version is a tag-like ref or a
// 40-hex commit SHA. Local identifiers are absolute paths with no version.
type CapabilityIdentifier string

var (
	remoteRe   = regexp.MustCompile(`^([A-Za-z0-9.-]+)/([A-Za-z0-9._-]+)/([A-Za-z0-9._-]+)@([A-Za-z0-9._+-]+)$`)
	manifestFN = "capability.yaml"
)

// ParsedIdentifier is a parsed CapabilityIdentifier: either a remote
// {domain,group,name,version} tuple or a local absolute path.
type ParsedIdentifier struct {
	Domain  string
	Group   string
	Name    string
	Version string
	Local   string // absolute path when !IsRemote
}

// ParseIdentifier parses and validates a capability identifier.
func ParseIdentifier(s string) (ParsedIdentifier, error) {
	if s == "" {
		return ParsedIdentifier{}, fmt.Errorf("ext: invalid identifier %q", s)
	}
	id := CapabilityIdentifier(s)
	if !id.IsRemote() {
		if s == "" || strings.ContainsAny(s, " \t\n") {
			return ParsedIdentifier{}, fmt.Errorf("ext: invalid local path %q", s)
		}
		return ParsedIdentifier{Local: filepath.Clean(s)}, nil
	}
	if strings.ContainsAny(s, " \t\n") || strings.Contains(s, "..") {
		return ParsedIdentifier{}, fmt.Errorf("ext: invalid identifier %q", s)
	}
	m := remoteRe.FindStringSubmatch(s)
	if m == nil {
		if !strings.Contains(s, "@") {
			return ParsedIdentifier{}, fmt.Errorf("ext: remote identifier %q requires an @version", s)
		}
		return ParsedIdentifier{}, fmt.Errorf("ext: invalid identifier %q", s)
	}
	p := ParsedIdentifier{Domain: m[1], Group: m[2], Name: m[3], Version: m[4]}
	for _, seg := range []string{p.Domain, p.Group, p.Name, p.Version} {
		if seg == "" || strings.HasPrefix(seg, ".") || strings.HasPrefix(seg, "-") {
			return ParsedIdentifier{}, fmt.Errorf("ext: invalid identifier %q", s)
		}
	}
	return p, nil
}

func (id CapabilityIdentifier) IsRemote() bool {
	return !filepath.IsAbs(string(id))
}

// CapabilityManifest is a full description of a extended capability
// it's uniquely defined by capability identifier and can depend on
// other capabilities
type CapabilityManifest struct {
	Identifier   CapabilityIdentifier   `yaml:"-"` // source it was loaded under
	Name         string                 `yaml:"name"`
	Version      string                 `yaml:"version"`
	Description  string                 `yaml:"description"`
	Dependencies []types.CapabilityName `yaml:"dependencies"`
	Ops          []OpSpec               `yaml:"ops"`
	Config       []ConfigField          `yaml:"config"`
}

// OpSpec declares one exported operation.
type OpSpec struct {
	Name   string   `yaml:"name" json:"name"`
	Doc    string   `yaml:"doc" json:"doc"`
	Params []string `yaml:"params" json:"params"`
}

// ConfigField declares one config key accepted by the extension.
type ConfigField struct {
	Name    string `yaml:"name" json:"name"`
	Type    string `yaml:"type" json:"type"`
	Doc     string `yaml:"doc" json:"doc"`
	Default any    `yaml:"default" json:"default"`
}
