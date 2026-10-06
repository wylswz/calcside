package ext

import (
	"fmt"
	"testing"
)

func TestManifestIconPaths(t *testing.T) {
	for _, icon := range []string{"../icon.png", "/icon.png", "https://example.com/icon.png", "assets/../icon.png", "assets\\icon.png", "icon.svg", "data:image/png;base64,AA=="} {
		if _, err := parseManifest([]byte(goodManifest + fmt.Sprintf("icon: %q\n", icon))); err == nil {
			t.Fatalf("accepted icon path %q", icon)
		}
	}
	if _, err := parseManifest([]byte(goodManifest + "icon: assets/icon.png\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := parseManifest([]byte(goodManifest)); err != nil {
		t.Fatal(err)
	}
}
