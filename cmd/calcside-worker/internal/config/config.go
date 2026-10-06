package config

import (
	"flag"
	"fmt"
	"net"
	"strings"
	"time"

	"calcside/internal/capability"
	common "calcside/internal/config"
)

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
	fs.StringVar(&c.Addr, "listen", common.EnvOr("WORKER_LISTEN", ":8090"), "listen address (private network only)")
	fs.StringVar(&c.NodeID, "node-id", common.EnvOr("NODE_ID", ""), "stable node identity; takes precedence over --node-id-file")
	fs.StringVar(&c.NodeIDFile, "node-id-file", common.EnvOr("NODE_ID_FILE", ""), "file holding the stable node id; created on first boot")
	fs.StringVar(&c.SharedKey, "shared-key", common.EnvOr("WORKER_SHARED_KEY", ""), "shared HMAC secret authenticating API-worker calls (required)")
	fs.StringVar(&c.APIAddr, "api-addr", common.EnvOr("API_ADDR", ""), "API tier address (http://host:port) used to resolve local ext sources; empty disables local ext")
	fs.IntVar(&c.MaxInstances, "max-instances-per-node", common.EnvInt("MAX_INSTANCES_PER_NODE", 0), "max live instances on this node (0 = unlimited)")
	fs.DurationVar(&c.ReaperInterval, "reaper-interval", common.EnvDur("REAPER_INTERVAL", 10*time.Second), "local memory-reclaim sweep interval")
	fs.DurationVar(&c.PolicyEvalTimeout, "policy-eval-timeout", common.EnvDur("POLICY_EVAL_TIMEOUT", 100*time.Millisecond), "per-policy eval timeout")
	fs.DurationVar(&c.DefaultTTL, "default-ttl", common.EnvDur("DEFAULT_TTL", 15*time.Minute), "default instance TTL (must match API)")
	fs.DurationVar(&c.MaxTTL, "max-ttl", common.EnvDur("MAX_TTL", 24*time.Hour), "max instance TTL (must match API)")
	fs.DurationVar(&c.MaxExecTimeout, "max-exec-timeout", common.EnvDur("MAX_EXEC_TIMEOUT", 5*time.Minute), "max exec timeout (must match API)")
	fs.IntVar(&c.MaxConcurrentExecs, "max-concurrent-execs", common.EnvInt("MAX_CONCURRENT_EXECS", 64), "global exec concurrency")
	fs.Uint64Var(&c.MaxSteps, "max-steps", uint64(common.EnvInt("MAX_STEPS", 100000000)), "max starlark execution steps per exec")
	fs.Int64Var(&c.MaxOutputBytes, "max-output-bytes", int64(common.EnvInt("MAX_OUTPUT_BYTES", 4<<20)), "max captured output bytes per exec")
	fs.Uint64Var(&c.ExecMemoryLimit, "exec-memory-limit", uint64(common.EnvInt("EXEC_MEMORY_LIMIT", 2<<30)), "heap watchdog limit in bytes (0 disables)")
	var netCIDRs string
	fs.StringVar(&netCIDRs, "net-allow-cidrs", common.EnvOr("NET_ALLOW_CIDRS", ""), "comma-separated CIDRs exempt from net's private/reserved-address blocking")
	fs.Int64Var(&c.MaxNetResponseBytes, "max-net-response-bytes", int64(common.EnvInt("MAX_NET_RESPONSE_BYTES", 32<<20)), "clamp for net.max_response_bytes")
	fs.BoolVar(&c.SecretsAllowHTTP, "secrets-allow-http", common.EnvBool("SECRETS_ALLOW_HTTP", false), "allow secret injection into http:// URLs (INSECURE)")
	var extSources, extRoots string
	fs.StringVar(&extSources, "ext-allow-sources", common.EnvOr("EXT_ALLOW_SOURCES", ""), "comma-separated allowed remote ext source prefixes")
	fs.StringVar(&extRoots, "ext-local-roots", common.EnvOr("EXT_LOCAL_ROOTS", ""), "comma-separated local extension roots (sources use root-name/extension)")
	fs.StringVar(&c.ExtCacheDir, "ext-cache-dir", common.EnvOr("EXT_CACHE_DIR", common.DefaultExtCacheDir()), "extension fetch/resolve cache dir")
	fs.DurationVar(&c.ExtFetchTimeout, "ext-fetch-timeout", common.EnvDur("EXT_FETCH_TIMEOUT", 30*time.Second), "remote ext fetch timeout")
	fs.StringVar(&c.InstanceIsolation, "instance-isolation", common.EnvOr("INSTANCE_ISOLATION", common.IsolationProcess), "process gives each instance its own OS process; inproc runs instances in the worker process")
	fs.Int64Var(&c.InstanceMemoryMax, "instance-memory-max", int64(common.EnvInt("INSTANCE_MEMORY_MAX", 64<<20)), "per-instance memory cap in bytes with --instance-isolation=process (Linux cgroup v2; 0 disables)")
	fs.StringVar(&c.InstanceCgroupParent, "instance-cgroup-parent", common.EnvOr("INSTANCE_CGROUP_PARENT", ""), "cgroup v2 group (relative to /sys/fs/cgroup) holding per-instance groups; empty uses this process's own group")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	if err := common.CheckIsolation(c.InstanceIsolation); err != nil {
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
