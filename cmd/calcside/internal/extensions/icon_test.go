package extensions

import (
	"bytes"
	"image"
	"image/png"
	"path/filepath"
	"strings"
	"testing"

	capext "calcside/internal/capability/ext"
)

func TestPackagedIcon(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 32, 32))); err != nil {
		t.Fatal(err)
	}
	valid := buf.String()
	for _, tc := range []struct {
		name, data string
		want       bool
	}{
		{"valid", valid, true},
		{"trailing payload", valid + "NOT_IMAGE_DATA", true},
		{"missing", "", false},
		{"svg", "<svg onload='alert(1)'/>", false},
		{"too big", valid + strings.Repeat("x", 32<<10), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeExt(t, map[string]string{"capability.yaml": goodManifest + "icon: icon.png\n", "main.star": "def search(query):\n    return []\n", "icon.png": tc.data})
			m, err := (&capext.CapabilityLoader{Identifier: capext.CapabilityIdentifier(filepath.Base(filepath.Dir(dir)) + "/" + filepath.Base(dir)), Options: &capext.Options{LocalRoots: []string{filepath.Dir(dir)}}}).Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			icon := packagedIcon(m)
			if (len(icon) > 0) != tc.want {
				t.Fatalf("icon availability: %t", len(icon) > 0)
			}
			if bytes.Contains(icon, []byte("NOT_IMAGE_DATA")) {
				t.Fatal("unvalidated image payload retained")
			}
			if len(icon) > 0 {
				if _, err := png.Decode(bytes.NewReader(icon)); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	buf.Reset()
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 257, 1))); err != nil {
		t.Fatal(err)
	}
	m := &capext.Module{Manifest: &capext.CapabilityManifest{Icon: "icon.png"}, Sources: map[string][]byte{"icon.png": buf.Bytes()}}
	if len(packagedIcon(m)) > 0 {
		t.Fatal("oversized dimensions accepted")
	}
}
