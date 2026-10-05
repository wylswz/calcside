package ext

import (
	"bytes"
	"image/png"
	"io/fs"
	"path"
	"strings"
)

func validIconPath(s string) bool {
	return len(s) <= 128 && fs.ValidPath(s) && !strings.ContainsAny(s, "\\:\x00") && path.Ext(s) == ".png"
}

func packagedIcon(m *Module) []byte {
	if m.Manifest.Icon == "" || !validIconPath(m.Manifest.Icon) {
		return nil
	}
	data := m.Sources[m.Manifest.Icon]
	if len(data) == 0 || len(data) > 32<<10 {
		return nil
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 256 || cfg.Height > 256 {
		return nil
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil || out.Len() > 32<<10 {
		return nil
	}
	return out.Bytes()
}
