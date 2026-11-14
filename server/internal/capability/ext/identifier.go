package ext

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"calcside/internal/capability"
	"calcside/internal/extartifact"
	"calcside/internal/types"
)

// CapabilityIdentifier is a uniquely identifier of a capability
// the format is
// {domain}/{group}/{name}@{version}
// for example
// github.com/wylswz/openai@v0.1.0
// or it can be a server-local identifier
// contrib/tavily@0.1.0
//
// Remote identifiers must carry an explicit version:
// {domain}/{group}/{name}@{version} where version is a tag-like ref or a
// 40-hex commit SHA. Local identifiers use {root-name}/{name}@{version};
// the unversioned {root-name}/{name} form still parses but is the legacy
// form admitted only by compatibility paths.
type CapabilityIdentifier string

var (
	localRe    = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]*/[A-Za-z0-9_-][A-Za-z0-9._-]*$`)
	domainRe   = regexp.MustCompile(`^[A-Za-z0-9.-]+$`)
	segRe      = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	manifestFN = "capability.yaml"
)

// ParsedIdentifier is a parsed CapabilityIdentifier: either a remote
// {domain,group,name}@version tuple or a server-local identifier.
type ParsedIdentifier struct {
	Domain  string
	Group   string
	Name    string
	Version string
	Local   string // {root-name}/{name} when !IsRemote
	// Family is always set: the unversioned identifier (Local for local
	// sources, {domain}/{group}/{name} for remote ones).
	Family string
}

// ValidVersion reports whether v is a legal version token — the same
// grammar for local version directories and remote refs: exact,
// case-sensitive, no v-prefix normalization.
func ValidVersion(v string) bool { return extartifact.ValidVersion(v) }

// Canonical builds the canonical family@version source string.
func Canonical(family, version string) string { return family + "@" + version }

// ParseIdentifier parses and validates a capability identifier. The
// version separator splits before classification, so a local
// contrib/tavily@0.1.0 is never misread as remote.
func ParseIdentifier(s string) (ParsedIdentifier, error) {
	if s == "" || filepath.IsAbs(s) || strings.ContainsAny(s, " \t\r\n\x00\\:") {
		return ParsedIdentifier{}, fmt.Errorf("ext: invalid identifier %q; use root-name/extension for local sources", s)
	}
	for _, segment := range strings.Split(s, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return ParsedIdentifier{}, fmt.Errorf("ext: invalid identifier %q", s)
		}
	}
	id := CapabilityIdentifier(s)
	family, version, hasVersion := strings.Cut(s, "@")
	if !id.IsRemote() {
		if !localRe.MatchString(family) {
			return ParsedIdentifier{}, fmt.Errorf("ext: invalid local identifier %q", s)
		}
		p := ParsedIdentifier{Local: family, Family: family}
		if hasVersion {
			if !ValidVersion(version) {
				return ParsedIdentifier{}, fmt.Errorf("ext: invalid identifier %q", s)
			}
			p.Version = version
		}
		return p, nil
	}
	if strings.Contains(family, "..") {
		return ParsedIdentifier{}, fmt.Errorf("ext: invalid identifier %q", s)
	}
	if !hasVersion {
		return ParsedIdentifier{}, fmt.Errorf("ext: remote identifier %q requires an @version", s)
	}
	if !ValidVersion(version) {
		return ParsedIdentifier{}, fmt.Errorf("ext: invalid identifier %q", s)
	}
	segs := strings.Split(family, "/")
	if len(segs) != 3 || !domainRe.MatchString(segs[0]) ||
		!segRe.MatchString(segs[1]) || !segRe.MatchString(segs[2]) {
		return ParsedIdentifier{}, fmt.Errorf("ext: invalid identifier %q", s)
	}
	p := ParsedIdentifier{Domain: segs[0], Group: segs[1], Name: segs[2],
		Version: version, Family: family}
	for _, seg := range segs {
		if strings.HasPrefix(seg, ".") || strings.HasPrefix(seg, "-") {
			return ParsedIdentifier{}, fmt.Errorf("ext: invalid identifier %q", s)
		}
	}
	return p, nil
}

func (id CapabilityIdentifier) IsRemote() bool {
	s := string(id)
	if filepath.IsAbs(s) {
		return false
	}
	family, _, _ := strings.Cut(s, "@")
	return strings.Count(family, "/") >= 2
}

// CapabilityManifest is a full description of a extended capability
// it's uniquely defined by capability identifier and can depend on
// other capabilities
type CapabilityManifest struct {
	Identifier   CapabilityIdentifier   `yaml:"-"` // source it was loaded under
	Name         string                 `yaml:"name"`
	Version      string                 `yaml:"version"`
	Description  string                 `yaml:"description"`
	Icon         string                 `yaml:"icon"`
	Interface    string                 `yaml:"interface"`
	Capability   string                 `yaml:"capability"`
	Models       []string               `yaml:"models"`
	Dependencies []types.CapabilityName `yaml:"dependencies"`
	Ops          []OpSpec               `yaml:"ops"`
	Config       []ConfigField          `yaml:"config"`
}

// OpSpec declares one exported operation. Params holds the positional
// parameter names in order; ParamDocs/Returns/Example carry optional
// structured documentation decoded from mapping-form params items (see
// OpSpec.UnmarshalYAML in manifest.go).
type OpSpec struct {
	Name      string                `yaml:"name" json:"name"`
	Doc       string                `yaml:"doc" json:"doc"`
	Params    []string              `yaml:"params" json:"params"`
	ParamDocs []capability.ParamDoc `yaml:"-" json:"param_docs,omitempty"`
	Returns   string                `yaml:"returns" json:"returns,omitempty"`
	Example   string                `yaml:"example" json:"example,omitempty"`
}

// ConfigField declares one config key accepted by the extension.
type ConfigField struct {
	Name    string `yaml:"name" json:"name"`
	Type    string `yaml:"type" json:"type"`
	Doc     string `yaml:"doc" json:"doc"`
	Default any    `yaml:"default" json:"default"`
}
