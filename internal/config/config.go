// Package config parses CLI flags + CALCSIDE_* env vars for `calcside serve`.
package config

import (
	"calcside/internal/capability"
	"calcside/internal/types"

	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config is the server configuration.
type Config struct {
	Addr                 string
	AddrExplicit         bool // --addr flag or CALCSIDE_ADDR set
	Dev                  bool
	DevAllowRemote       bool
	Store                types.StoreDriver
	DSN                  string
	PolicyDir            string
	BaseURL              string
	GoogleClientID       string
	GoogleClientSecret   string
	GoogleAllowedDomains []string
	CookieSecure         bool
	NodeID               string
	Workers              string
	WorkerKey            string
	MaxInstancesPerUser  int
	MaxInstancesPerNode  int
	DefaultTTL           time.Duration
	MaxTTL               time.Duration
	MaxExecTimeout       time.Duration
	MaxConcurrentExecs   int
	ReaperInterval       time.Duration
	PolicyEvalTimeout    time.Duration
	MaxSteps             uint64
	MaxOutputBytes       int64
	ExecMemoryLimit      uint64
	NetAllowCIDRs        []*net.IPNet
	MaxNetResponseBytes  int64
	SecretKey            string
	SecretsAllowHTTP     bool
	ExtAllowSources      []string
	ExtLocalRoots        []string
	ExtCacheDir          string
	ExtFetchTimeout      time.Duration
	InstanceIsolation    string
	InstanceMemoryMax    int64
	InstanceCgroupParent string
}

func envOr(key, def string) string {
	if v, ok := os.LookupEnv("CALCSIDE_" + key); ok {
		return v
	}
	return def
}

func defaultExtCacheDir() string {
	if d, err := os.UserCacheDir(); err == nil {
		return filepath.Join(d, "calcside", "ext")
	}
	return filepath.Join(os.TempDir(), "calcside-ext")
}

func envDur(key string, def time.Duration) time.Duration {
	if v, ok := os.LookupEnv("CALCSIDE_" + key); ok {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func envInt(key string, def int) int {
	if v, ok := os.LookupEnv("CALCSIDE_" + key); ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envBool(key string, def bool) bool {
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

func checkIsolation(v string) error {
	if v != IsolationInproc && v != IsolationProcess {
		return fmt.Errorf("--instance-isolation: must be %q or %q, got %q", IsolationInproc, IsolationProcess, v)
	}
	return nil
}

// Parse builds the config from args like ["serve", "--addr", ...]. Flags
// default from CALCSIDE_<UPPER_SNAKE> env vars.
func Parse(args []string) (Config, error) {
	var c Config
	fs := flag.NewFlagSet("calcside serve", flag.ContinueOnError)
	fs.StringVar(&c.Addr, "addr", envOr("ADDR", ":8080"), "listen address")
	fs.TextVar(&c.Store, "store", types.StoreDriver(envOr("STORE", string(types.DriverSQLite))), "store driver: sqlite or postgres")
	fs.StringVar(&c.DSN, "dsn", envOr("DSN", "calcside.db"), "store DSN: sqlite file path, or postgres URL / key=value conn string")
	fs.StringVar(&c.PolicyDir, "policy-dir", envOr("POLICY_DIR", ""), "global rego policy dir")
	fs.StringVar(&c.BaseURL, "base-url", envOr("BASE_URL", "http://localhost:8080"), "external base URL")
	fs.StringVar(&c.GoogleClientID, "google-client-id", envOr("GOOGLE_CLIENT_ID", ""), "google oauth client id")
	fs.StringVar(&c.GoogleClientSecret, "google-client-secret", envOr("GOOGLE_CLIENT_SECRET", ""), "google oauth client secret")
	var domains string
	fs.StringVar(&domains, "google-allowed-domains", envOr("GOOGLE_ALLOWED_DOMAINS", ""), "comma-separated allowed email domains")
	fs.BoolVar(&c.CookieSecure, "cookie-secure", envBool("COOKIE_SECURE", false), "set Secure on cookies")
	fs.BoolVar(&c.Dev, "dev", envBool("DEV", false), "dev mode: no login, anonymous principal (INSECURE)")
	fs.BoolVar(&c.DevAllowRemote, "dev-allow-remote", envBool("DEV_ALLOW_REMOTE", false), "allow --dev on non-loopback addr (INSECURE)")
	fs.StringVar(&c.NodeID, "node-id", envOr("NODE_ID", "local"), "stable identity of this execution node; keep it constant across restarts so orphaned bindings can be reclaimed")
	fs.StringVar(&c.Workers, "workers", envOr("WORKERS", ""), "comma-separated worker list nodeID=host:port; empty runs execution in-process")
	fs.StringVar(&c.WorkerKey, "worker-key", envOr("WORKER_SHARED_KEY", ""), "shared HMAC secret authenticating API↔worker calls")
	fs.IntVar(&c.MaxInstancesPerUser, "max-instances-per-user", envInt("MAX_INSTANCES_PER_USER", 10), "max live instances per user (cluster-wide, counted from the store)")
	fs.IntVar(&c.MaxInstancesPerNode, "max-instances-per-node", envInt("MAX_INSTANCES_PER_NODE", 0), "max live instances on this execution node (0 = unlimited)")
	fs.DurationVar(&c.DefaultTTL, "default-ttl", envDur("DEFAULT_TTL", 15*time.Minute), "default instance TTL")
	fs.DurationVar(&c.MaxTTL, "max-ttl", envDur("MAX_TTL", 24*time.Hour), "max instance TTL")
	fs.DurationVar(&c.MaxExecTimeout, "max-exec-timeout", envDur("MAX_EXEC_TIMEOUT", 5*time.Minute), "max exec timeout")
	fs.IntVar(&c.MaxConcurrentExecs, "max-concurrent-execs", envInt("MAX_CONCURRENT_EXECS", 64), "global exec concurrency")
	fs.DurationVar(&c.ReaperInterval, "reaper-interval", envDur("REAPER_INTERVAL", 10*time.Second), "TTL reaper interval")
	fs.DurationVar(&c.PolicyEvalTimeout, "policy-eval-timeout", envDur("POLICY_EVAL_TIMEOUT", 100*time.Millisecond), "per-policy eval timeout")
	fs.Uint64Var(&c.MaxSteps, "max-steps", uint64(envInt("MAX_STEPS", 100000000)), "max starlark execution steps per exec")
	fs.Int64Var(&c.MaxOutputBytes, "max-output-bytes", int64(envInt("MAX_OUTPUT_BYTES", 4<<20)), "max captured output bytes per exec")
	fs.Uint64Var(&c.ExecMemoryLimit, "exec-memory-limit", uint64(envInt("EXEC_MEMORY_LIMIT", 2<<30)), "heap watchdog limit in bytes (0 disables)")
	var netCIDRs string
	fs.StringVar(&netCIDRs, "net-allow-cidrs", envOr("NET_ALLOW_CIDRS", ""), "comma-separated CIDRs exempt from net's private/reserved-address blocking, e.g. 198.18.0.0/15 for fake-ip proxies")
	fs.Int64Var(&c.MaxNetResponseBytes, "max-net-response-bytes", int64(envInt("MAX_NET_RESPONSE_BYTES", 32<<20)), "clamp for net.max_response_bytes")
	fs.StringVar(&c.SecretKey, "secret-key", envOr("SECRET_KEY", ""), "base64-encoded 32-byte key encrypting vault secrets")
	fs.BoolVar(&c.SecretsAllowHTTP, "secrets-allow-http", envBool("SECRETS_ALLOW_HTTP", false), "allow secret injection into http:// URLs (INSECURE)")
	var extSources, extRoots string
	fs.StringVar(&extSources, "ext-allow-sources", envOr("EXT_ALLOW_SOURCES", ""), "comma-separated allowed remote ext source prefixes (empty disables remote ext)")
	fs.StringVar(&extRoots, "ext-local-roots", envOr("EXT_LOCAL_ROOTS", ""), "comma-separated local dirs ext sources may live under (empty disables local ext)")
	fs.StringVar(&c.ExtCacheDir, "ext-cache-dir", envOr("EXT_CACHE_DIR", defaultExtCacheDir()), "extension fetch cache dir")
	fs.DurationVar(&c.ExtFetchTimeout, "ext-fetch-timeout", envDur("EXT_FETCH_TIMEOUT", 30*time.Second), "ext remote fetch timeout")
	fs.StringVar(&c.InstanceIsolation, "instance-isolation", envOr("INSTANCE_ISOLATION", IsolationInproc), "in-process execution tier: inproc runs instances in this process, process gives each instance its own OS process")
	fs.Int64Var(&c.InstanceMemoryMax, "instance-memory-max", int64(envInt("INSTANCE_MEMORY_MAX", 64<<20)), "per-instance memory cap in bytes with --instance-isolation=process (Linux cgroup v2; 0 disables)")
	fs.StringVar(&c.InstanceCgroupParent, "instance-cgroup-parent", envOr("INSTANCE_CGROUP_PARENT", ""), "cgroup v2 group (relative to /sys/fs/cgroup) holding per-instance groups; empty uses this process's own group")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	if err := checkIsolation(c.InstanceIsolation); err != nil {
		return c, err
	}
	addrSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "addr" {
			addrSet = true
		}
	})
	c.AddrExplicit = addrSet || os.Getenv("CALCSIDE_ADDR") != ""
	for _, d := range strings.Split(domains, ",") {
		d = strings.TrimSpace(d)
		if d != "" {
			c.GoogleAllowedDomains = append(c.GoogleAllowedDomains, d)
		}
	}
	for _, s := range strings.Split(extSources, ",") {
		if s = strings.TrimSpace(s); s != "" {
			c.ExtAllowSources = append(c.ExtAllowSources, s)
		}
	}
	for _, s := range strings.Split(extRoots, ",") {
		if s = strings.TrimSpace(s); s != "" {
			c.ExtLocalRoots = append(c.ExtLocalRoots, s)
		}
	}
	for _, s := range strings.Split(netCIDRs, ",") {
		if s = strings.TrimSpace(s); s != "" {
			_, n, err := net.ParseCIDR(s)
			if err != nil {
				return c, fmt.Errorf("--net-allow-cidrs: invalid CIDR %q", s)
			}
			c.NetAllowCIDRs = append(c.NetAllowCIDRs, n)
		}
	}
	if c.DefaultTTL <= 0 || c.MaxTTL <= 0 {
		return c, fmt.Errorf("TTLs must be positive")
	}
	return c, nil
}

// CheckDevAddr refuses non-loopback listen addresses in dev mode unless
// --dev-allow-remote was passed.
func CheckDevAddr(addr string, allowRemote bool) error {
	if allowRemote {
		return nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		// Bare ":8080" or "8080"-style values: SplitHostPort handles ":8080";
		// anything else treat as unsafe rather than guessing.
		return fmt.Errorf("dev mode: cannot parse --addr %q", addr)
	}
	if host == "" {
		host = "0.0.0.0" // ":8080" binds all interfaces — not loopback
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	return fmt.Errorf("dev mode refuses non-loopback --addr %q (pass --dev-allow-remote to override)", addr)
}

// WorkerConfig is calcside-worker's configuration — a separate shape
// from the API tier's Config because the two processes share execution
// limits but nothing else.
type WorkerConfig struct {
	Addr                 string
	NodeID               string
	NodeIDFile           string
	SharedKey            string
	APIAddr              string
	MaxInstances         int
	ReaperInterval       time.Duration
	PolicyEvalTimeout    time.Duration
	DefaultTTL           time.Duration
	MaxTTL               time.Duration
	MaxExecTimeout       time.Duration
	MaxConcurrentExecs   int
	MaxSteps             uint64
	MaxOutputBytes       int64
	ExecMemoryLimit      uint64
	NetAllowCIDRs        []*net.IPNet
	MaxNetResponseBytes  int64
	SecretsAllowHTTP     bool
	ExtAllowSources      []string
	ExtLocalRoots        []string
	ExtCacheDir          string
	ExtFetchTimeout      time.Duration
	InstanceIsolation    string
	InstanceMemoryMax    int64
	InstanceCgroupParent string
}

// ParseWorker parses calcside-worker's flags. Everything about limits
// and extension policy must match the API tier, or the same spec would
// behave differently depending on placement.
func ParseWorker(args []string) (WorkerConfig, error) {
	var c WorkerConfig
	fs := flag.NewFlagSet("calcside-worker", flag.ContinueOnError)
	fs.StringVar(&c.Addr, "listen", envOr("WORKER_LISTEN", ":8090"), "listen address (private network only)")
	fs.StringVar(&c.NodeID, "node-id", envOr("NODE_ID", ""), "stable node identity; takes precedence over --node-id-file")
	fs.StringVar(&c.NodeIDFile, "node-id-file", envOr("NODE_ID_FILE", ""), "file holding the stable node id; created on first boot")
	fs.StringVar(&c.SharedKey, "shared-key", envOr("WORKER_SHARED_KEY", ""), "shared HMAC secret authenticating API-worker calls (required)")
	fs.StringVar(&c.APIAddr, "api-addr", envOr("API_ADDR", ""), "API tier address (http://host:port) used to resolve local ext sources; empty disables local ext")
	fs.IntVar(&c.MaxInstances, "max-instances-per-node", envInt("MAX_INSTANCES_PER_NODE", 0), "max live instances on this node (0 = unlimited)")
	fs.DurationVar(&c.ReaperInterval, "reaper-interval", envDur("REAPER_INTERVAL", 10*time.Second), "local memory-reclaim sweep interval")
	fs.DurationVar(&c.PolicyEvalTimeout, "policy-eval-timeout", envDur("POLICY_EVAL_TIMEOUT", 100*time.Millisecond), "per-policy eval timeout")
	fs.DurationVar(&c.DefaultTTL, "default-ttl", envDur("DEFAULT_TTL", 15*time.Minute), "default instance TTL (must match API)")
	fs.DurationVar(&c.MaxTTL, "max-ttl", envDur("MAX_TTL", 24*time.Hour), "max instance TTL (must match API)")
	fs.DurationVar(&c.MaxExecTimeout, "max-exec-timeout", envDur("MAX_EXEC_TIMEOUT", 5*time.Minute), "max exec timeout (must match API)")
	fs.IntVar(&c.MaxConcurrentExecs, "max-concurrent-execs", envInt("MAX_CONCURRENT_EXECS", 64), "global exec concurrency")
	fs.Uint64Var(&c.MaxSteps, "max-steps", uint64(envInt("MAX_STEPS", 100000000)), "max starlark execution steps per exec")
	fs.Int64Var(&c.MaxOutputBytes, "max-output-bytes", int64(envInt("MAX_OUTPUT_BYTES", 4<<20)), "max captured output bytes per exec")
	fs.Uint64Var(&c.ExecMemoryLimit, "exec-memory-limit", uint64(envInt("EXEC_MEMORY_LIMIT", 2<<30)), "heap watchdog limit in bytes (0 disables)")
	var netCIDRs string
	fs.StringVar(&netCIDRs, "net-allow-cidrs", envOr("NET_ALLOW_CIDRS", ""), "comma-separated CIDRs exempt from net's private/reserved-address blocking")
	fs.Int64Var(&c.MaxNetResponseBytes, "max-net-response-bytes", int64(envInt("MAX_NET_RESPONSE_BYTES", 32<<20)), "clamp for net.max_response_bytes")
	fs.BoolVar(&c.SecretsAllowHTTP, "secrets-allow-http", envBool("SECRETS_ALLOW_HTTP", false), "allow secret injection into http:// URLs (INSECURE)")
	var extSources, extRoots string
	fs.StringVar(&extSources, "ext-allow-sources", envOr("EXT_ALLOW_SOURCES", ""), "comma-separated allowed remote ext source prefixes")
	fs.StringVar(&extRoots, "ext-local-roots", envOr("EXT_LOCAL_ROOTS", ""), "comma-separated local dirs ext sources may live under")
	fs.StringVar(&c.ExtCacheDir, "ext-cache-dir", envOr("EXT_CACHE_DIR", defaultExtCacheDir()), "extension fetch/resolve cache dir")
	fs.DurationVar(&c.ExtFetchTimeout, "ext-fetch-timeout", envDur("EXT_FETCH_TIMEOUT", 30*time.Second), "remote ext fetch timeout")
	fs.StringVar(&c.InstanceIsolation, "instance-isolation", envOr("INSTANCE_ISOLATION", IsolationProcess), "process gives each instance its own OS process; inproc runs instances in the worker process")
	fs.Int64Var(&c.InstanceMemoryMax, "instance-memory-max", int64(envInt("INSTANCE_MEMORY_MAX", 64<<20)), "per-instance memory cap in bytes with --instance-isolation=process (Linux cgroup v2; 0 disables)")
	fs.StringVar(&c.InstanceCgroupParent, "instance-cgroup-parent", envOr("INSTANCE_CGROUP_PARENT", ""), "cgroup v2 group (relative to /sys/fs/cgroup) holding per-instance groups; empty uses this process's own group")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	if err := checkIsolation(c.InstanceIsolation); err != nil {
		return c, err
	}
	for _, s := range strings.Split(extSources, ",") {
		if s = strings.TrimSpace(s); s != "" {
			c.ExtAllowSources = append(c.ExtAllowSources, s)
		}
	}
	for _, s := range strings.Split(extRoots, ",") {
		if s = strings.TrimSpace(s); s != "" {
			c.ExtLocalRoots = append(c.ExtLocalRoots, s)
		}
	}
	for _, s := range strings.Split(netCIDRs, ",") {
		if s = strings.TrimSpace(s); s != "" {
			_, n, err := net.ParseCIDR(s)
			if err != nil {
				return c, fmt.Errorf("--net-allow-cidrs: invalid CIDR %q", s)
			}
			c.NetAllowCIDRs = append(c.NetAllowCIDRs, n)
		}
	}
	return c, nil
}

// ExecLimits maps the worker config onto the capability server limits.
func (c WorkerConfig) ExecLimits() capability.ServerLimits {
	return capability.ServerLimits{
		DefaultTTL:          c.DefaultTTL,
		MaxTTL:              c.MaxTTL,
		MaxExecTimeout:      c.MaxExecTimeout,
		MaxSteps:            c.MaxSteps,
		MaxFSQuotaBytes:     256 << 20,
		MaxOutputBytes:      c.MaxOutputBytes,
		NetAllowCIDRs:       c.NetAllowCIDRs,
		MaxNetResponseBytes: c.MaxNetResponseBytes,
		SecretsAllowHTTP:    c.SecretsAllowHTTP,
	}
}

// ExecLimits maps the config onto the capability server limits shared
// by the API tier and every worker node.
func (c Config) ExecLimits() capability.ServerLimits {
	return capability.ServerLimits{
		DefaultTTL:          c.DefaultTTL,
		MaxTTL:              c.MaxTTL,
		MaxExecTimeout:      c.MaxExecTimeout,
		MaxSteps:            c.MaxSteps,
		MaxFSQuotaBytes:     256 << 20,
		MaxOutputBytes:      c.MaxOutputBytes,
		NetAllowCIDRs:       c.NetAllowCIDRs,
		MaxNetResponseBytes: c.MaxNetResponseBytes,
		SecretsAllowHTTP:    c.SecretsAllowHTTP,
	}
}
