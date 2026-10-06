// Package config parses CLI flags + CALCSIDE_* env vars for `calcside serve`.
package config

import (
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"calcside/cmd/calcside/internal/auth"
	"calcside/internal/capability"
	common "calcside/internal/config"
	"calcside/internal/types"
)

// Config is the server configuration.
type Config struct {
	Addr                   string
	AddrExplicit           bool // --addr flag or CALCSIDE_ADDR set
	Dev                    bool
	DevAllowRemote         bool
	Store                  types.StoreDriver
	DSN                    string
	PolicyDir              string
	ConsoleOrigin          string
	ArtifactPreviewBaseURL string
	ArtifactAllowScripts   bool
	AdminUsername          string
	AdminPassword          string
	GoogleClientID         string
	GoogleClientSecret     string
	GoogleAllowedDomains   []string
	CookieSecure           bool
	NodeID                 string
	Workers                string
	WorkerKey              string
	MaxInstancesPerUser    int
	MaxInstancesPerNode    int
	DefaultTTL             time.Duration
	MaxTTL                 time.Duration
	MaxExecTimeout         time.Duration
	MaxConcurrentExecs     int
	ReaperInterval         time.Duration
	PolicyEvalTimeout      time.Duration
	MaxSteps               uint64
	MaxOutputBytes         int64
	ExecMemoryLimit        uint64
	NetAllowCIDRs          []*net.IPNet
	MaxNetResponseBytes    int64
	SecretKey              string
	SecretsAllowHTTP       bool
	ExtAllowSources        []string
	ExtLocalRoots          []string
	ExtCacheDir            string
	ExtFetchTimeout        time.Duration
	InstanceIsolation      string
	InstanceMemoryMax      int64
	InstanceCgroupParent   string
}

// Parse builds the config from args like ["serve", "--addr", ...]. Flags
// default from CALCSIDE_<UPPER_SNAKE> env vars.
func Parse(args []string) (Config, error) {
	var c Config
	fs := flag.NewFlagSet("calcside serve", flag.ContinueOnError)
	fs.StringVar(&c.Addr, "addr", common.EnvOr("ADDR", ":8080"), "listen address")
	fs.TextVar(&c.Store, "store", types.StoreDriver(common.EnvOr("STORE", string(types.DriverSQLite))), "store driver: sqlite or postgres")
	fs.StringVar(&c.DSN, "dsn", common.EnvOr("DSN", "calcside.db"), "store DSN: sqlite file path, or postgres URL / key=value conn string")
	fs.StringVar(&c.PolicyDir, "policy-dir", common.EnvOr("POLICY_DIR", ""), "global rego policy dir")
	fs.StringVar(&c.ConsoleOrigin, "console-origin", common.EnvOr("CONSOLE_ORIGIN", "http://localhost:8080"), "public console origin for UI, OAuth callbacks, and artifact framing")
	fs.StringVar(&c.ArtifactPreviewBaseURL, "artifact-preview-base-url", common.EnvOr("ARTIFACT_PREVIEW_BASE_URL", ""), "isolated preview origin on a separate registrable domain (empty disables rendered HTML)")
	fs.BoolVar(&c.ArtifactAllowScripts, "artifact-allow-scripts", common.EnvBool("ARTIFACT_ALLOW_SCRIPTS", false), "allow explicit interactive HTML previews on the isolated origin")
	fs.StringVar(&c.AdminUsername, "admin-username", common.EnvOr("ADMIN_USERNAME", ""), "bootstrap administrator username or email (independent from Google accounts)")
	fs.StringVar(&c.AdminPassword, "admin-password", "", "initial administrator password, 8-72 bytes (prefer CALCSIDE_ADMIN_PASSWORD)")
	c.AdminPassword = common.EnvOr("ADMIN_PASSWORD", "")
	fs.StringVar(&c.GoogleClientID, "google-client-id", common.EnvOr("GOOGLE_CLIENT_ID", ""), "google oauth client id")
	fs.StringVar(&c.GoogleClientSecret, "google-client-secret", common.EnvOr("GOOGLE_CLIENT_SECRET", ""), "google oauth client secret")
	var domains string
	fs.StringVar(&domains, "google-allowed-domains", common.EnvOr("GOOGLE_ALLOWED_DOMAINS", ""), "comma-separated allowed email domains")
	fs.BoolVar(&c.CookieSecure, "cookie-secure", common.EnvBool("COOKIE_SECURE", false), "set Secure on cookies")
	fs.BoolVar(&c.Dev, "dev", common.EnvBool("DEV", false), "dev mode: no login, anonymous principal (INSECURE)")
	fs.BoolVar(&c.DevAllowRemote, "dev-allow-remote", common.EnvBool("DEV_ALLOW_REMOTE", false), "allow --dev on non-loopback addr (INSECURE)")
	fs.StringVar(&c.NodeID, "node-id", common.EnvOr("NODE_ID", "local"), "stable identity of this execution node; keep it constant across restarts so orphaned bindings can be reclaimed")
	fs.StringVar(&c.Workers, "workers", common.EnvOr("WORKERS", ""), "comma-separated worker list nodeID=host:port; empty runs execution in-process")
	fs.StringVar(&c.WorkerKey, "worker-key", common.EnvOr("WORKER_SHARED_KEY", ""), "shared HMAC secret authenticating API↔worker calls")
	fs.IntVar(&c.MaxInstancesPerUser, "max-instances-per-user", common.EnvInt("MAX_INSTANCES_PER_USER", 10), "max live instances per user (cluster-wide, counted from the store)")
	fs.IntVar(&c.MaxInstancesPerNode, "max-instances-per-node", common.EnvInt("MAX_INSTANCES_PER_NODE", 0), "max live instances on this execution node (0 = unlimited)")
	fs.DurationVar(&c.DefaultTTL, "default-ttl", common.EnvDur("DEFAULT_TTL", 15*time.Minute), "default instance TTL")
	fs.DurationVar(&c.MaxTTL, "max-ttl", common.EnvDur("MAX_TTL", 24*time.Hour), "max instance TTL")
	fs.DurationVar(&c.MaxExecTimeout, "max-exec-timeout", common.EnvDur("MAX_EXEC_TIMEOUT", 5*time.Minute), "max exec timeout")
	fs.IntVar(&c.MaxConcurrentExecs, "max-concurrent-execs", common.EnvInt("MAX_CONCURRENT_EXECS", 64), "global exec concurrency")
	fs.DurationVar(&c.ReaperInterval, "reaper-interval", common.EnvDur("REAPER_INTERVAL", 10*time.Second), "TTL reaper interval")
	fs.DurationVar(&c.PolicyEvalTimeout, "policy-eval-timeout", common.EnvDur("POLICY_EVAL_TIMEOUT", 100*time.Millisecond), "per-policy eval timeout")
	fs.Uint64Var(&c.MaxSteps, "max-steps", uint64(common.EnvInt("MAX_STEPS", 100000000)), "max starlark execution steps per exec")
	fs.Int64Var(&c.MaxOutputBytes, "max-output-bytes", int64(common.EnvInt("MAX_OUTPUT_BYTES", 4<<20)), "max captured output bytes per exec")
	fs.Uint64Var(&c.ExecMemoryLimit, "exec-memory-limit", uint64(common.EnvInt("EXEC_MEMORY_LIMIT", 2<<30)), "heap watchdog limit in bytes (0 disables)")
	var netCIDRs string
	fs.StringVar(&netCIDRs, "net-allow-cidrs", common.EnvOr("NET_ALLOW_CIDRS", ""), "comma-separated CIDRs exempt from net's private/reserved-address blocking, e.g. 198.18.0.0/15 for fake-ip proxies")
	fs.Int64Var(&c.MaxNetResponseBytes, "max-net-response-bytes", int64(common.EnvInt("MAX_NET_RESPONSE_BYTES", 32<<20)), "clamp for net.max_response_bytes")
	fs.StringVar(&c.SecretKey, "secret-key", common.EnvOr("SECRET_KEY", ""), "base64-encoded 32-byte key encrypting vault secrets")
	fs.BoolVar(&c.SecretsAllowHTTP, "secrets-allow-http", common.EnvBool("SECRETS_ALLOW_HTTP", false), "allow secret injection into http:// URLs (INSECURE)")
	var extSources, extRoots string
	fs.StringVar(&extSources, "ext-allow-sources", common.EnvOr("EXT_ALLOW_SOURCES", ""), "comma-separated allowed remote ext source prefixes (empty disables remote ext)")
	fs.StringVar(&extRoots, "ext-local-roots", common.EnvOr("EXT_LOCAL_ROOTS", ""), "comma-separated local extension roots (sources use root-name/extension) (empty disables local ext)")
	fs.StringVar(&c.ExtCacheDir, "ext-cache-dir", common.EnvOr("EXT_CACHE_DIR", common.DefaultExtCacheDir()), "extension fetch cache dir")
	fs.DurationVar(&c.ExtFetchTimeout, "ext-fetch-timeout", common.EnvDur("EXT_FETCH_TIMEOUT", 30*time.Second), "ext remote fetch timeout")
	fs.StringVar(&c.InstanceIsolation, "instance-isolation", common.EnvOr("INSTANCE_ISOLATION", common.IsolationInproc), "in-process execution tier: inproc runs instances in this process, process gives each instance its own OS process")
	fs.Int64Var(&c.InstanceMemoryMax, "instance-memory-max", int64(common.EnvInt("INSTANCE_MEMORY_MAX", 64<<20)), "per-instance memory cap in bytes with --instance-isolation=process (Linux cgroup v2; 0 disables)")
	fs.StringVar(&c.InstanceCgroupParent, "instance-cgroup-parent", common.EnvOr("INSTANCE_CGROUP_PARENT", ""), "cgroup v2 group (relative to /sys/fs/cgroup) holding per-instance groups; empty uses this process's own group")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	for _, key := range []string{"CALCSIDE_BASE_URL", "CALCSIDE_ARTIFACT_CONSOLE_ORIGIN"} {
		if os.Getenv(key) != "" {
			return c, fmt.Errorf("%s has been removed; use CALCSIDE_CONSOLE_ORIGIN", key)
		}
	}
	origin, err := url.Parse(c.ConsoleOrigin)
	if err != nil || origin.Hostname() == "" || origin.Scheme != "http" && origin.Scheme != "https" || origin.User != nil || origin.Path != "" && origin.Path != "/" || origin.RawQuery != "" || origin.ForceQuery || strings.Contains(c.ConsoleOrigin, "#") || strings.HasSuffix(origin.Host, ":") {
		return c, fmt.Errorf("--console-origin must be an HTTP(S) origin without credentials, path, query, or fragment")
	}
	if port := origin.Port(); port != "" {
		if value, err := strconv.ParseUint(port, 10, 16); err != nil || value == 0 {
			return c, fmt.Errorf("--console-origin has an invalid port")
		}
	}
	origin.Host = strings.ToLower(origin.Host)
	origin.Path, origin.RawPath = "", ""
	c.ConsoleOrigin = origin.String()
	if err := common.CheckIsolation(c.InstanceIsolation); err != nil {
		return c, err
	}
	if err := auth.ValidateAdminCredentials(c.AdminUsername, c.AdminPassword); err != nil {
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
