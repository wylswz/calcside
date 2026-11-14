// Package extcache synchronously fetches immutable extension artifacts
// into the worker's local cache: the caller that needs an artifact
// fetches it inline (bounded by a small semaphore) and execution only
// proceeds once the pinned content is installed.
package extcache

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	capext "calcside/internal/capability/ext"
	"calcside/internal/extartifact"
	"calcside/internal/runtime"
)

// Fetch returns (files, sum, error) for an immutable (source, sum)
// artifact — the worker's authenticated API callback call.
type Fetch func(ctx context.Context, source, sum string) (map[string][]byte, string, error)

// Cache holds only the cache dir, the fetch closure, and a bounded
// fetch semaphore — all state lives in the immutable artifact dir.
type Cache struct {
	dir     string
	fetch   Fetch
	timeout time.Duration
	sem     chan struct{}
}

// New builds a synchronous cache; timeout <= 0 defaults to 30 s.
func New(dir string, fetch Fetch, timeout time.Duration) (*Cache, error) {
	if dir == "" || fetch == nil {
		return nil, errors.New("extcache: dir and fetch are required")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Cache{dir: dir, fetch: fetch, timeout: timeout, sem: make(chan struct{}, 4)}, nil
}

// CacheResolver resolves a pinned (source, sum) ref to the verified
// local artifact dir. Read-only: a miss is a fixed error — it never
// fetches and never reports pending.
func CacheResolver(dir string) capext.SourceResolver {
	return func(ctx context.Context, source, sum string) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if _, err := capext.ParseIdentifier(source); err != nil || !runtime.ValidExtensionSum(sum) {
			return "", runtime.ErrBadSpec
		}
		d := artifactDir(dir, source, sum)
		if err := extartifact.Read(d, sum); err != nil {
			return "", errors.New("extcache: artifact not cached")
		}
		return d, nil
	}
}

func artifactDir(dir, source, sum string) string {
	return dir + string(os.PathSeparator) + extartifact.CacheKey(source, sum)
}

// Ensure makes every pinned extension ref in spec available locally.
// Concurrent calls for the same artifact may fetch up to the semaphore
// bound; extartifact.Install is atomic and idempotent for identical
// content so no per-ref dedup state is needed.
func (c *Cache) Ensure(ctx context.Context, spec json.RawMessage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	refs, err := runtime.ExtensionRequirements(spec)
	if err != nil {
		return runtime.ErrBadSpec
	}
	for _, ref := range refs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := capext.ParseIdentifier(ref.Source); err != nil {
			return runtime.ErrBadSpec
		}
		if c.cached(ctx, ref) == nil {
			continue // verified local copy — no fetch at all
		}
		select {
		case c.sem <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		err := c.fetchMissing(ctx, ref)
		<-c.sem
		if err != nil {
			return err
		}
	}
	return nil
}

// fetchMissing runs under the semaphore with the effective deadline
// min(caller, configured timeout); it re-checks the cache after
// acquiring in case a concurrent ensure already installed the ref.
func (c *Cache) fetchMissing(ctx context.Context, ref runtime.ExtensionRef) error {
	if c.cached(ctx, ref) == nil {
		return nil
	}
	// The configured timeout always bounds the fetch — a later parent
	// deadline still applies through the child context.
	fctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	files, sum, err := c.fetch(fctx, ref.Source, ref.Sum)
	if fctx.Err() != nil {
		return fctx.Err()
	}
	if err != nil {
		return errors.New("extcache: fetch failed")
	}
	if sum != ref.Sum {
		return errors.New("extcache: fetch failed")
	}
	if _, err := extartifact.Install(c.dir, ref.Source, sum, files); err != nil {
		return errors.New("extcache: fetch failed")
	}
	return nil
}

func (c *Cache) cached(ctx context.Context, ref runtime.ExtensionRef) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return extartifact.Read(artifactDir(c.dir, ref.Source, ref.Sum), ref.Sum)
}

// WithRuntime wraps rt so Create/Prepare synchronously ensure the
// requested spec's extension artifacts before delegating — a blocked
// or failed fetch never reaches the inner runtime.
func WithRuntime(rt runtime.Runtime, cache *Cache) runtime.Runtime {
	return &cached{Runtime: rt, cache: cache}
}

type cached struct {
	runtime.Runtime
	cache *Cache
}

func (c *cached) Create(ctx context.Context, req *runtime.CreateRequest) (*runtime.CreateResponse, error) {
	if err := c.cache.Ensure(ctx, req.Spec); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.Runtime.Create(ctx, req)
}

func (c *cached) Prepare(ctx context.Context, req *runtime.PrepareRequest) (*runtime.PrepareResponse, error) {
	if err := c.cache.Ensure(ctx, req.Spec); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.Runtime.Prepare(ctx, req)
}
