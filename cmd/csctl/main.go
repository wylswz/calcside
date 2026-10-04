// csctl is the calcside CLI: create/exec/manage Starlark sandbox instances.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"calcside/internal/client"
	"calcside/internal/engine"
	"calcside/internal/types"
)

type config struct {
	Server string `json:"server"`
	APIKey string `json:"api_key"`
}

func configPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "calcside", "config.json")
}

func loadConfig() config {
	var c config
	data, err := os.ReadFile(configPath())
	if err == nil {
		_ = json.Unmarshal(data, &c)
	}
	if v := os.Getenv("CALCSIDE_SERVER"); v != "" {
		c.Server = v
	}
	if v := os.Getenv("CALCSIDE_API_KEY"); v != "" {
		c.APIKey = v
	}
	return c
}

var stdout, stderr io.Writer = os.Stdout, os.Stderr

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	gfs := flag.NewFlagSet("csctl", flag.ContinueOnError)
	gfs.SetOutput(stderr)
	server := gfs.String("server", "", "server URL (env CALCSIDE_SERVER)")
	apiKey := gfs.String("api-key", "", "API key (env CALCSIDE_API_KEY)")
	if err := gfs.Parse(args); err != nil {
		return 2
	}
	cfg := loadConfig()
	if *server != "" {
		cfg.Server = *server
	}
	if *apiKey != "" {
		cfg.APIKey = *apiKey
	}
	if cfg.Server == "" {
		cfg.Server = "http://localhost:8080"
	}
	rest := gfs.Args()
	if len(rest) == 0 {
		usage()
		return 2
	}
	c := client.New(cfg.Server, cfg.APIKey)
	ctx := context.Background()

	switch rest[0] {
	case "login":
		return cmdLogin(rest[1:], cfg)
	case "whoami":
		u, err := c.Me(ctx)
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "%s (%s)\n", u.Email, u.ID)
		return 0
	case "instances":
		return cmdInstances(ctx, c, rest[1:])
	case "exec":
		return cmdExec(ctx, c, rest[1:])
	case "run":
		return cmdRun(ctx, c, rest[1:])
	case "files":
		return cmdFiles(ctx, c, rest[1:])
	case "inspect":
		return cmdInspect(ctx, c, rest[1:])
	case "audit":
		return cmdAudit(ctx, c, rest[1:])
	case "policies":
		return cmdPolicies(ctx, c, rest[1:])
	case "keys":
		fmt.Fprintln(stderr, "API keys cannot be managed via key authentication; use the web console (session login) to create or revoke keys.")
		return 2
	case "secrets":
		fmt.Fprintln(stderr, "Secrets are session-only (write-only vault): manage them in the web console. Reference them in instances via --secret NAME[=domains].")
		return 2
	default:
		usage()
		return 2
	}
}

func usage() {
	fmt.Fprintln(stderr, `usage: csctl [--server URL] [--api-key KEY] <command>

  login --server URL --api-key KEY   save config (validates credentials)
  whoami
  instances create|ls|get|rm|keepalive|prompt
  exec <id> [-f file | -c code]
  run [create flags] [-f file | -c code]
  files <id> [path]
  inspect <id>
  audit [--instance ID] [--exec ID] [--limit N]
  policies ls|get|apply|rm|validate`)
}

func fail(err error) int {
	fmt.Fprintln(stderr, "error:", err)
	return 1
}

func outputJSON(v any) int {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fail(err)
	}
	fmt.Fprintln(stdout, string(b))
	return 0
}

// --- login ---

func cmdLogin(args []string, cfg config) int {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.SetOutput(stderr)
	server := fs.String("server", "", "server URL")
	key := fs.String("api-key", "", "API key")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *server != "" {
		cfg.Server = *server
	}
	if *key != "" {
		cfg.APIKey = *key
	}
	if cfg.Server == "" || cfg.APIKey == "" {
		fmt.Fprintln(stderr, "login requires --server and --api-key")
		return 2
	}
	u, err := client.New(cfg.Server, cfg.APIKey).Me(context.Background())
	if err != nil {
		fmt.Fprintln(stderr, "login failed:", err)
		return 1
	}
	if err := os.MkdirAll(filepath.Dir(configPath()), 0o700); err != nil {
		return fail(err)
	}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(configPath(), data, 0o600); err != nil {
		return fail(err)
	}
	fmt.Fprintf(stdout, "logged in as %s\n", u.Email)
	return 0
}

// --- instances ---

type createFlags struct {
	ttl       string
	fsOn      bool
	fsQuota   string
	netAllow  string
	netMethod string
	labels    strList
	envs      strList
	secrets   strList
	policies  strList
	spec      string
	output    types.OutputFormat
}

type strList []string

func (l *strList) String() string { return strings.Join(*l, ",") }
func (l *strList) Set(v string) error {
	*l = append(*l, v)
	return nil
}

func registerCreateFlags(fs *flag.FlagSet, f *createFlags) {
	fs.StringVar(&f.ttl, "ttl", "", "instance TTL (e.g. 15m)")
	fs.BoolVar(&f.fsOn, "fs", false, "grant fs capability")
	fs.StringVar(&f.fsQuota, "fs-quota", "", "fs quota bytes (e.g. 64MiB)")
	fs.StringVar(&f.netAllow, "net-allow", "", "comma-separated allow_hosts")
	fs.StringVar(&f.netMethod, "net-methods", "", "comma-separated HTTP methods")
	fs.Var(&f.labels, "label", "k=v label (repeatable)")
	fs.Var(&f.envs, "env", "K=V env var visible to the script (repeatable)")
	fs.Var(&f.secrets, "secret", "NAME[=dom1,dom2] vault secret ref, optionally narrowing its domains (repeatable)")
	fs.Var(&f.policies, "policy", "library policy name to attach (repeatable)")
	fs.StringVar(&f.spec, "spec", "", "raw spec JSON file")
	fs.TextVar(&f.output, "o", types.FormatText, "output format (json)")
}

func parseSize(s string) (int64, error) {
	mult := int64(1)
	lower := strings.ToLower(s)
	for suf, m := range map[string]int64{"kib": 1 << 10, "mib": 1 << 20, "gib": 1 << 30, "kb": 1000, "mb": 1000 * 1000, "gb": 1000 * 1000 * 1000, "b": 1} {
		if strings.HasSuffix(lower, suf) {
			mult = m
			lower = strings.TrimSuffix(lower, suf)
			break
		}
	}
	n, err := strconv.ParseInt(strings.TrimSpace(lower), 10, 64)
	return n * mult, err
}

func buildSpec(f *createFlags) (client.InstanceSpec, error) {
	if f.spec != "" {
		data, err := os.ReadFile(f.spec)
		if err != nil {
			return nil, err
		}
		var spec client.InstanceSpec
		if err := json.Unmarshal(data, &spec); err != nil {
			return nil, fmt.Errorf("bad spec file: %w", err)
		}
		return spec, nil
	}
	spec := client.InstanceSpec{}
	caps := map[string]any{}
	if f.fsOn {
		fsCfg := map[string]any{}
		if f.fsQuota != "" {
			q, err := parseSize(f.fsQuota)
			if err != nil {
				return nil, fmt.Errorf("bad --fs-quota: %w", err)
			}
			fsCfg["quota_bytes"] = q
		}
		caps["fs"] = fsCfg
	}
	if f.netAllow != "" {
		netCfg := map[string]any{"allow_hosts": strings.Split(f.netAllow, ",")}
		if f.netMethod != "" {
			ms := strings.Split(f.netMethod, ",")
			for i, m := range ms {
				pm, err := types.ParseHTTPMethod(m)
				if err != nil {
					return nil, fmt.Errorf("bad --net-methods: %w", err)
				}
				ms[i] = string(pm)
			}
			netCfg["methods"] = ms
		}
		caps["net"] = netCfg
	}
	if len(caps) > 0 {
		spec["capabilities"] = caps
	}
	if f.ttl != "" {
		d, err := time.ParseDuration(f.ttl)
		if err != nil {
			return nil, fmt.Errorf("bad --ttl: %w", err)
		}
		spec["ttl_seconds"] = int64(d.Seconds())
	}
	if len(f.labels) > 0 {
		labels := map[string]string{}
		for _, kv := range f.labels {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				return nil, fmt.Errorf("bad --label %q (want k=v)", kv)
			}
			labels[k] = v
		}
		spec["labels"] = labels
	}
	if len(f.envs) > 0 {
		env := map[string]string{}
		for _, kv := range f.envs {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				return nil, fmt.Errorf("bad --env %q (want K=V)", kv)
			}
			env[k] = v
		}
		spec["env"] = env
	}
	if len(f.secrets) > 0 {
		secs := map[string]any{}
		for _, s := range f.secrets {
			name, doms, _ := strings.Cut(s, "=")
			entry := map[string]any{"ref": name}
			if doms != "" {
				entry["allowed_domains"] = strings.Split(doms, ",")
			}
			secs[name] = entry
		}
		spec["secrets"] = secs
	}
	if len(f.policies) > 0 {
		spec["policies"] = []string(f.policies)
	}
	return spec, nil
}

func cmdInstances(ctx context.Context, c *client.Client, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "instances: subcommand required")
		return 2
	}
	switch args[0] {
	case "create":
		fs := flag.NewFlagSet("instances create", flag.ContinueOnError)
		fs.SetOutput(stderr)
		var f createFlags
		registerCreateFlags(fs, &f)
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		spec, err := buildSpec(&f)
		if err != nil {
			return fail(err)
		}
		in, err := c.CreateInstance(ctx, spec)
		if err != nil {
			return fail(err)
		}
		if f.output == types.FormatJSON {
			return outputJSON(in)
		}
		fmt.Fprintln(stdout, in.ID)
		return 0
	case "ls":
		fs := flag.NewFlagSet("instances ls", flag.ContinueOnError)
		fs.SetOutput(stderr)
		var status types.InstanceStatus
		var out types.OutputFormat
		fs.TextVar(&status, "status", types.InstanceStatus(""), "filter by status")
		fs.TextVar(&out, "o", types.FormatText, "output format (json)")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		lst, err := c.ListInstances(ctx, status)
		if err != nil {
			return fail(err)
		}
		if out == types.FormatJSON {
			return outputJSON(lst)
		}
		tw := newTabWriter()
		fmt.Fprintln(tw, "ID\tSTATUS\tCREATED\tEXPIRES\tLABELS")
		for _, in := range lst {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", in.ID, in.Status,
				in.CreatedAt.Format(time.RFC3339), in.ExpiresAt.Format(time.RFC3339), fmtLabels(in.Labels))
		}
		tw.Flush()
		return 0
	case "get":
		if len(args) < 2 {
			fmt.Fprintln(stderr, "get: instance id required")
			return 2
		}
		in, err := c.GetInstance(ctx, args[1])
		if err != nil {
			return fail(err)
		}
		return outputJSON(in)
	case "rm":
		if len(args) < 2 {
			fmt.Fprintln(stderr, "rm: instance id required")
			return 2
		}
		if err := c.DeleteInstance(ctx, args[1]); err != nil {
			return fail(err)
		}
		fmt.Fprintln(stdout, args[1]+" deleted")
		return 0
	case "keepalive":
		if len(args) < 2 {
			fmt.Fprintln(stderr, "keepalive: instance id required")
			return 2
		}
		in, err := c.Keepalive(ctx, args[1])
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "%s expires %s\n", in.ID, in.ExpiresAt.Format(time.RFC3339))
		return 0
	case "prompt":
		if len(args) < 2 {
			fmt.Fprintln(stderr, "prompt: instance id required")
			return 2
		}
		fs := flag.NewFlagSet("instances prompt", flag.ContinueOnError)
		fs.SetOutput(stderr)
		var prefix string
		var out types.OutputFormat
		fs.StringVar(&prefix, "tool-prefix", "calcside_", "tool name prefix")
		fs.TextVar(&out, "o", types.FormatText, "output format (json)")
		if err := fs.Parse(args[2:]); err != nil {
			return 2
		}
		res, err := c.Prompt(ctx, args[1], prefix)
		if err != nil {
			return fail(err)
		}
		if out == types.FormatJSON {
			return outputJSON(res)
		}
		fmt.Fprint(stdout, res.Prompt)
		return 0
	}
	fmt.Fprintln(stderr, "unknown instances subcommand")
	return 2
}

func fmtLabels(l map[string]string) string {
	var parts []string
	for k, v := range l {
		parts = append(parts, k+"="+v)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// --- exec / run ---

func readCode(fileFlag, codeFlag string, args []string) (string, error) {
	switch {
	case fileFlag != "":
		b, err := os.ReadFile(fileFlag)
		return string(b), err
	case codeFlag != "":
		return codeFlag, nil
	default:
		b, err := io.ReadAll(os.Stdin)
		return string(b), err
	}
}

func printExecResult(res *engine.Result, jsonOut bool) int {
	if jsonOut {
		return outputJSON(res)
	}
	fmt.Fprint(stdout, res.Output)
	if res.Error != nil {
		fmt.Fprintf(stderr, "error[%s]: %s\n", res.Error.Type, res.Error.Message)
		if res.Error.Backtrace != "" {
			fmt.Fprintln(stderr, res.Error.Backtrace)
		}
		return 2
	}
	return 0
}

func cmdExec(ctx context.Context, c *client.Client, args []string) int {
	fs := flag.NewFlagSet("exec", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("f", "", "starlark file")
	code := fs.String("c", "", "inline code")
	timeout := fs.Int64("timeout-ms", 0, "exec timeout override ms")
	var out types.OutputFormat
	fs.TextVar(&out, "o", types.FormatText, "output format (json)")
	// Instance id comes first; flags follow it.
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(stderr, "exec: instance id required")
		return 2
	}
	id := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	src, err := readCode(*file, *code, nil)
	if err != nil {
		return fail(err)
	}
	res, err := c.Exec(ctx, id, src, *timeout)
	if err != nil {
		return fail(err)
	}
	return printExecResult(res, out == types.FormatJSON)
}

func cmdRun(ctx context.Context, c *client.Client, args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var f createFlags
	registerCreateFlags(fs, &f)
	file := fs.String("f", "", "starlark file")
	code := fs.String("c", "", "inline code")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	spec, err := buildSpec(&f)
	if err != nil {
		return fail(err)
	}
	src, err := readCode(*file, *code, nil)
	if err != nil {
		return fail(err)
	}
	in, err := c.CreateInstance(ctx, spec)
	if err != nil {
		return fail(err)
	}
	defer func() { _ = c.DeleteInstance(context.Background(), in.ID) }()
	res, err := c.Exec(ctx, in.ID, src, 0)
	if err != nil {
		return fail(err)
	}
	return printExecResult(res, f.output == types.FormatJSON)
}

// --- files ---

func cmdFiles(ctx context.Context, c *client.Client, args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "files: instance id required")
		return 2
	}
	path := "/work"
	if len(args) > 1 {
		path = args[1]
	}
	entries, content, err := c.Files(ctx, args[0], path)
	if err != nil {
		return fail(err)
	}
	if entries != nil {
		tw := newTabWriter()
		fmt.Fprintln(tw, "NAME\tSIZE\tDIR\tMTIME")
		for _, e := range entries {
			fmt.Fprintf(tw, "%s\t%d\t%v\t%s\n", e.Name, e.Size, e.IsDir, time.Unix(e.Mtime, 0).Format(time.RFC3339))
		}
		tw.Flush()
		return 0
	}
	fmt.Fprint(stdout, content)
	return 0
}

// --- inspect ---

func cmdInspect(ctx context.Context, c *client.Client, args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "inspect: instance id required")
		return 2
	}
	fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var out types.OutputFormat
	fs.TextVar(&out, "o", types.FormatText, "output format (json)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	ins, err := c.Inspect(ctx, args[0])
	if err != nil {
		return fail(err)
	}
	if out == types.FormatJSON {
		return outputJSON(ins)
	}
	if r := ins.Resources; r != nil {
		limit := "unlimited"
		if r.MemoryMax > 0 {
			limit = fmtBytes(r.MemoryMax)
		}
		fmt.Fprintf(stdout, "memory: %s / %s", fmtBytes(r.MemoryUsage), limit)
		if r.MemoryPeak > 0 {
			fmt.Fprintf(stdout, " (peak %s)", fmtBytes(r.MemoryPeak))
		}
		fmt.Fprintln(stdout)
	}
	vars := ins.Variables
	names := make([]string, 0, len(vars))
	for name := range vars {
		names = append(names, name)
	}
	sort.Strings(names)
	tw := newTabWriter()
	fmt.Fprintln(tw, "NAME\tVALUE")
	for _, name := range names {
		fmt.Fprintf(tw, "%s\t%s\n", name, vars[name])
	}
	tw.Flush()
	return 0
}

func fmtBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// --- audit ---

func cmdAudit(ctx context.Context, c *client.Client, args []string) int {
	fs := flag.NewFlagSet("audit", flag.ContinueOnError)
	fs.SetOutput(stderr)
	inst := fs.String("instance", "", "instance id")
	exec := fs.String("exec", "", "exec id")
	limit := fs.Int("limit", 50, "max events")
	beforeStr := fs.String("before", "", "RFC3339 timestamp")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	var before *time.Time
	if *beforeStr != "" {
		t, err := time.Parse(time.RFC3339, *beforeStr)
		if err != nil {
			return fail(err)
		}
		before = &t
	}
	evs, err := c.Audit(ctx, *inst, *exec, *limit, before)
	if err != nil {
		return fail(err)
	}
	tw := newTabWriter()
	fmt.Fprintln(tw, "TS\tCAP\tOP\tDECISION\tREASON\tEXEC")
	for _, e := range evs {
		reason := e.Reason
		if len(reason) > 60 {
			reason = reason[:60] + "…"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			e.Ts.Format("15:04:05"), e.Capability, e.Op, e.Decision, reason, e.ExecID)
	}
	tw.Flush()
	return 0
}

// --- policies ---

func cmdPolicies(ctx context.Context, c *client.Client, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "policies: subcommand required")
		return 2
	}
	switch args[0] {
	case "ls":
		lst, err := c.ListPolicies(ctx)
		if err != nil {
			return fail(err)
		}
		tw := newTabWriter()
		fmt.Fprintln(tw, "ID\tNAME\tUPDATED")
		for _, p := range lst {
			fmt.Fprintf(tw, "%s\t%s\t%s\n", p.ID, p.Name, p.UpdatedAt.Format(time.RFC3339))
		}
		tw.Flush()
		return 0
	case "get":
		if len(args) < 2 {
			fmt.Fprintln(stderr, "get: policy id required")
			return 2
		}
		p, err := c.GetPolicy(ctx, args[1])
		if err != nil {
			return fail(err)
		}
		fmt.Fprint(stdout, p.Rego)
		return 0
	case "apply":
		fs := flag.NewFlagSet("policies apply", flag.ContinueOnError)
		fs.SetOutput(stderr)
		file := fs.String("f", "", "rego file")
		name := fs.String("name", "", "policy name")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		if *file == "" || *name == "" {
			fmt.Fprintln(stderr, "apply: -f and --name required")
			return 2
		}
		data, err := os.ReadFile(*file)
		if err != nil {
			return fail(err)
		}
		src := string(data)
		// update existing policy with same name, else create
		lst, err := c.ListPolicies(ctx)
		if err != nil {
			return fail(err)
		}
		for _, p := range lst {
			if p.Name == *name {
				up, err := c.UpdatePolicy(ctx, p.ID, nil, &src)
				if err != nil {
					return fail(err)
				}
				fmt.Fprintln(stdout, up.ID+" updated")
				return 0
			}
		}
		p, err := c.CreatePolicy(ctx, *name, src)
		if err != nil {
			return fail(err)
		}
		fmt.Fprintln(stdout, p.ID+" created")
		return 0
	case "rm":
		if len(args) < 2 {
			fmt.Fprintln(stderr, "rm: policy id required")
			return 2
		}
		if err := c.DeletePolicy(ctx, args[1]); err != nil {
			return fail(err)
		}
		fmt.Fprintln(stdout, args[1]+" deleted")
		return 0
	case "validate":
		fs := flag.NewFlagSet("policies validate", flag.ContinueOnError)
		fs.SetOutput(stderr)
		file := fs.String("f", "", "rego file")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		if *file == "" {
			fmt.Fprintln(stderr, "validate: -f required")
			return 2
		}
		data, err := os.ReadFile(*file)
		if err != nil {
			return fail(err)
		}
		valid, msg, err := c.ValidatePolicy(ctx, string(data))
		if err != nil {
			return fail(err)
		}
		if !valid {
			fmt.Fprintln(stderr, "invalid: "+msg)
			return 1
		}
		fmt.Fprintln(stdout, "valid")
		return 0
	}
	fmt.Fprintln(stderr, "unknown policies subcommand")
	return 2
}

func newTabWriter() *tabwriter.Writer {
	return tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
}
