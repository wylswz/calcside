// Package config parses CLI flags + CALCSIDE_* env vars for `calcside serve`.
package config

import (
	"calcside/internal/types"

	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the server configuration.
type Config struct {
	Addr                 string
	Store                types.StoreDriver
	DSN                  string
	PolicyDir            string
	BaseURL              string
	GoogleClientID       string
	GoogleClientSecret   string
	GoogleAllowedDomains []string
	CookieSecure         bool
	DevLogin             bool
	MaxInstancesPerUser  int
	DefaultTTL           time.Duration
	MaxTTL               time.Duration
	MaxExecTimeout       time.Duration
	MaxConcurrentExecs   int
	ReaperInterval       time.Duration
	PolicyEvalTimeout    time.Duration
	MaxSteps             uint64
	MaxOutputBytes       int64
	ExecMemoryLimit      uint64
	NetAllowPrivate      bool
	MaxNetResponseBytes  int64
	SecretKey            string
	SecretsAllowHTTP     bool
}

func envOr(key, def string) string {
	if v, ok := os.LookupEnv("CALCSIDE_" + key); ok {
		return v
	}
	return def
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

// Parse builds the config from args like ["serve", "--addr", ...]. Flags
// default from CALCSIDE_<UPPER_SNAKE> env vars.
func Parse(args []string) (Config, error) {
	var c Config
	fs := flag.NewFlagSet("calcside serve", flag.ContinueOnError)
	fs.StringVar(&c.Addr, "addr", envOr("ADDR", ":8080"), "listen address")
	fs.TextVar(&c.Store, "store", types.StoreDriver(envOr("STORE", string(types.DriverSQLite))), "store driver")
	fs.StringVar(&c.DSN, "dsn", envOr("DSN", "calcside.db"), "store DSN")
	fs.StringVar(&c.PolicyDir, "policy-dir", envOr("POLICY_DIR", ""), "global rego policy dir")
	fs.StringVar(&c.BaseURL, "base-url", envOr("BASE_URL", "http://localhost:8080"), "external base URL")
	fs.StringVar(&c.GoogleClientID, "google-client-id", envOr("GOOGLE_CLIENT_ID", ""), "google oauth client id")
	fs.StringVar(&c.GoogleClientSecret, "google-client-secret", envOr("GOOGLE_CLIENT_SECRET", ""), "google oauth client secret")
	var domains string
	fs.StringVar(&domains, "google-allowed-domains", envOr("GOOGLE_ALLOWED_DOMAINS", ""), "comma-separated allowed email domains")
	fs.BoolVar(&c.CookieSecure, "cookie-secure", envBool("COOKIE_SECURE", false), "set Secure on cookies")
	fs.BoolVar(&c.DevLogin, "dev-login", envBool("DEV_LOGIN", false), "enable POST /auth/dev/login (INSECURE)")
	fs.IntVar(&c.MaxInstancesPerUser, "max-instances-per-user", envInt("MAX_INSTANCES_PER_USER", 10), "max live instances per user")
	fs.DurationVar(&c.DefaultTTL, "default-ttl", envDur("DEFAULT_TTL", 15*time.Minute), "default instance TTL")
	fs.DurationVar(&c.MaxTTL, "max-ttl", envDur("MAX_TTL", 24*time.Hour), "max instance TTL")
	fs.DurationVar(&c.MaxExecTimeout, "max-exec-timeout", envDur("MAX_EXEC_TIMEOUT", 5*time.Minute), "max exec timeout")
	fs.IntVar(&c.MaxConcurrentExecs, "max-concurrent-execs", envInt("MAX_CONCURRENT_EXECS", 64), "global exec concurrency")
	fs.DurationVar(&c.ReaperInterval, "reaper-interval", envDur("REAPER_INTERVAL", 10*time.Second), "TTL reaper interval")
	fs.DurationVar(&c.PolicyEvalTimeout, "policy-eval-timeout", envDur("POLICY_EVAL_TIMEOUT", 100*time.Millisecond), "per-policy eval timeout")
	fs.Uint64Var(&c.MaxSteps, "max-steps", uint64(envInt("MAX_STEPS", 100000000)), "max starlark execution steps per exec")
	fs.Int64Var(&c.MaxOutputBytes, "max-output-bytes", int64(envInt("MAX_OUTPUT_BYTES", 4<<20)), "max captured output bytes per exec")
	fs.Uint64Var(&c.ExecMemoryLimit, "exec-memory-limit", uint64(envInt("EXEC_MEMORY_LIMIT", 2<<30)), "heap watchdog limit in bytes (0 disables)")
	fs.BoolVar(&c.NetAllowPrivate, "net-allow-private", envBool("NET_ALLOW_PRIVATE", false), "allow private/reserved IPs in net allowlists")
	fs.Int64Var(&c.MaxNetResponseBytes, "max-net-response-bytes", int64(envInt("MAX_NET_RESPONSE_BYTES", 32<<20)), "clamp for net.max_response_bytes")
	fs.StringVar(&c.SecretKey, "secret-key", envOr("SECRET_KEY", ""), "base64-encoded 32-byte key encrypting vault secrets")
	fs.BoolVar(&c.SecretsAllowHTTP, "secrets-allow-http", envBool("SECRETS_ALLOW_HTTP", false), "allow secret injection into http:// URLs (INSECURE)")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	for _, d := range strings.Split(domains, ",") {
		d = strings.TrimSpace(d)
		if d != "" {
			c.GoogleAllowedDomains = append(c.GoogleAllowedDomains, d)
		}
	}
	if c.DefaultTTL <= 0 || c.MaxTTL <= 0 {
		return c, fmt.Errorf("TTLs must be positive")
	}
	return c, nil
}
