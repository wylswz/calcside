package policy

import (
	"os"
	"path/filepath"

	core "calcside/internal/policy"
)

// LoadDir reads and validates every *.rego in dir, returning module
// name -> source. An empty dir name yields no modules.
//
// The API tier calls this once at startup and ships the result with
// each create request, so that an execution node compiles exactly the
// snapshot the API tier had, rather than whatever happens to be on that
// node's disk.
func LoadDir(dir string) (map[string]string, error) {
	if dir == "" {
		return nil, nil
	}
	entries, err := filepath.Glob(filepath.Join(dir, "*.rego"))
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, e := range entries {
		data, err := os.ReadFile(e)
		if err != nil {
			return nil, err
		}
		if err := core.ValidateModule(e, string(data)); err != nil {
			return nil, err
		}
		out[filepath.Base(e)] = string(data)
	}
	return out, nil
}
