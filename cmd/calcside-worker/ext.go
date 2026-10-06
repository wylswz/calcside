package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"calcside/cmd/calcside-worker/internal/apiclient"
	capext "calcside/internal/capability/ext"
	"calcside/internal/node/subproc"
	"calcside/internal/runtime/remote"
)

// LocalExtResolver returns a resolver for capext.Options.LocalResolver:
// the worker asks the API tier for the tree and materializes it into
// cacheDir/local/<sha256(path)>/, refetching only when the sum moved.
func LocalExtResolver(apiAddr, nodeID string, key []byte, cacheDir string) capext.LocalResolver {
	client, err := apiclient.NewClientWithResponses(strings.TrimSuffix(apiAddr, "/"),
		apiclient.WithRequestEditorFn(func(ctx context.Context, req *http.Request) error {
			remote.SignRequest(key, nodeID, req, nil, time.Now())
			return nil
		}))
	if err != nil {
		// A malformed --api-addr makes every resolve fail with a clear
		// error rather than panicking here.
		return func(context.Context, capext.ParsedIdentifier) (string, error) {
			return "", fmt.Errorf("ext: bad api addr %q: %w", apiAddr, err)
		}
	}
	return func(ctx context.Context, p capext.ParsedIdentifier) (string, error) {
		key2 := sha256.Sum256([]byte(p.Local))
		dir := filepath.Join(cacheDir, "local", hex.EncodeToString(key2[:8]))

		resp, err := client.ExtTreeWithResponse(ctx, &apiclient.ExtTreeParams{Path: p.Local})
		if err != nil {
			return "", fmt.Errorf("ext: resolve %q via api: %w", p.Local, err)
		}
		if resp.JSON200 == nil {
			if resp.JSONDefault != nil {
				return "", fmt.Errorf("ext: resolve %q via api: %s", p.Local, resp.JSONDefault.Error.Message)
			}
			return "", fmt.Errorf("ext: resolve %q via api: %s", p.Local, resp.Status())
		}
		if cachedSum(dir) == resp.JSON200.Sum {
			return dir, nil
		}
		if err := writeTree(dir, resp.JSON200.Sum, resp.JSON200.Files); err != nil {
			return "", err
		}
		return dir, nil
	}
}

// cachedSum reads the marker kept next to (never inside) the tree dir,
// so it cannot leak into the extension's own file set.
func cachedSum(dir string) string {
	b, err := os.ReadFile(dir + ".sum")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func writeTree(dir, sum string, files map[string][]byte) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for rel, content := range files {
		clean := filepath.Clean(filepath.FromSlash(rel))
		if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			return fmt.Errorf("ext: tree file %q escapes cache dir", rel)
		}
		dst := filepath.Join(dir, clean)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, content, 0o644); err != nil {
			return err
		}
	}
	return os.WriteFile(dir+".sum", []byte(sum+"\n"), 0o644)
}

type childExtensions struct {
	// APIAddr, NodeID and APIKey let the child resolve local ext sources
	// through the API tier, as an in-process worker node would.
	APIAddr string `json:"api_addr,omitempty"`
	NodeID  string `json:"node_id,omitempty"`
	APIKey  []byte `json:"api_key,omitempty"`
}

func configureChild(cfg *subproc.ChildConfig) error {
	var c childExtensions
	if len(cfg.Extra) > 0 {
		if err := json.Unmarshal(cfg.Extra, &c); err != nil {
			return err
		}
	}
	if c.APIAddr != "" {
		cfg.Node.ExtLocalResolver = LocalExtResolver(c.APIAddr, c.NodeID, c.APIKey, cfg.Node.ExtCacheDir)
	}
	return nil
}
