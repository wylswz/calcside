package main

import (
	"calcside/cmd/calcside-worker/internal/extcache"
	"calcside/internal/runtime/subproc"
)

func configureChild(cfg *subproc.ChildConfig) error {
	cfg.Node.ExtSourceResolver = extcache.CacheResolver(cfg.Node.ExtCacheDir)
	return nil
}
