package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

func EnvOr(key, def string) string {
	if v, ok := os.LookupEnv("CALCSIDE_" + key); ok {
		return v
	}
	return def
}

func DefaultExtCacheDir() string {
	if d, err := os.UserCacheDir(); err == nil {
		return filepath.Join(d, "calcside", "ext")
	}
	return filepath.Join(os.TempDir(), "calcside-ext")
}

func EnvDur(key string, def time.Duration) time.Duration {
	if v, ok := os.LookupEnv("CALCSIDE_" + key); ok {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func EnvInt(key string, def int) int {
	if v, ok := os.LookupEnv("CALCSIDE_" + key); ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func EnvBool(key string, def bool) bool {
	if v, ok := os.LookupEnv("CALCSIDE_" + key); ok {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

// Instance isolation modes: inproc runs every instance inside the
// node's own process; process gives each instance its own OS process
// (see internal/node/subproc).
const (
	IsolationInproc  = "inproc"
	IsolationProcess = "process"
)

func CheckIsolation(v string) error {
	if v != IsolationInproc && v != IsolationProcess {
		return fmt.Errorf("--instance-isolation: must be %q or %q, got %q", IsolationInproc, IsolationProcess, v)
	}
	return nil
}
