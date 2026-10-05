package fs

import (
	"errors"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

func CheckExportPath(full string, seen map[string]string) error {
	invalid := errors.New("artifact path is not portable or collides with another selected path")
	if path.Clean(full) != full || full != root && !strings.HasPrefix(full, root+"/") || !utf8.ValidString(full) {
		return invalid
	}
	parts := strings.Split(strings.TrimPrefix(full, "/"), "/")
	if len(parts) > 33 {
		return invalid
	}
	prefix := ""
	for _, part := range parts {
		if len(part) == 0 || len(part) > 255 || strings.TrimRight(part, ". ") != part || strings.ContainsAny(part, "\\:<>\"|?*") || strings.IndexFunc(part, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) >= 0 {
			return invalid
		}
		base := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || strings.HasPrefix(base, "COM") && len(base) == 4 && base[3] >= '1' && base[3] <= '9' || strings.HasPrefix(base, "LPT") && len(base) == 4 && base[3] >= '1' && base[3] <= '9' {
			return invalid
		}
		prefix += "/" + part
		key := cases.Fold().String(norm.NFC.String(prefix))
		if previous, ok := seen[key]; ok && previous != prefix {
			return invalid
		}
		seen[key] = prefix
	}
	return nil
}
