package main

import (
	"calcside/cmd/calcside-worker/internal/extcache"
	"calcside/internal/node/subproc"
)

func configureChild(cfg *subproc.ChildConfig) error {
	cfg.Node.ExtSourceResolver = extcache.CacheResolver(cfg.Node.ExtCacheDir)
	return nil
}
