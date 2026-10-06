package extensions

import (
	"bytes"
	"image/png"

	capext "calcside/internal/capability/ext"
)

func packagedIcon(m *capext.Module) []byte {
	if m.Manifest.Icon == "" || !capext.ValidIconPath(m.Manifest.Icon) {
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
