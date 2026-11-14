package ext

import (
	"io/fs"
	"path"
	"strings"
)

func ValidIconPath(s string) bool {
	return len(s) <= 128 && fs.ValidPath(s) && !strings.ContainsAny(s, "\\:\x00") && path.Ext(s) == ".png"
}
