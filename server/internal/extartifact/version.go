package extartifact

import (
	"encoding/base64"
	"regexp"
	"strings"
)

var versionRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$`)

// ValidVersion reports whether v is a legal extension version token —
// the grammar shared by local version directories and remote refs.
func ValidVersion(v string) bool {
	return versionRe.MatchString(v) && !strings.Contains(v, "..")
}

// ValidSum reports whether sum is a well-formed h1: tree hash.
func ValidSum(sum string) bool {
	if !strings.HasPrefix(sum, "h1:") {
		return false
	}
	b, err := base64.StdEncoding.Strict().DecodeString(strings.TrimPrefix(sum, "h1:"))
	return err == nil && len(b) == 32
}
