package extensions

import (
	"fmt"

	capext "calcside/internal/capability/ext"
)

// ReadLocalTree loads a local source dir as an in-memory tree for the
// API tier to serve to worker nodes. Containment, file caps, and the
// h1 sum all match what ResolveLocal enforces.
func ReadLocalTree(roots []string, path string) (files map[string][]byte, sum string, err error) {
	o := capext.Options{LocalRoots: roots}
	root, err := o.ResolveLocal(capext.ParsedIdentifier{Local: path}, "")
	if err != nil {
		return nil, "", err
	}
	files, err = capext.ReadSources(root)
	if err != nil {
		return nil, "", fmt.Errorf("ext: reading %q: %w", path, err)
	}
	sum, err = capext.HashTree(root)
	if err != nil {
		return nil, "", err
	}
	return files, sum, nil
}
