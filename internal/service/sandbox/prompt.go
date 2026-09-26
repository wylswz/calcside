package sandbox

import (
	"context"
	"errors"
	"regexp"
	"slices"

	"calcside/internal/instance"
	promptpkg "calcside/internal/prompt"
	"calcside/internal/service"
	"calcside/internal/types"
)

var toolPrefixRe = regexp.MustCompile(`^[A-Za-z0-9_]{0,32}$`)

// PromptView is the rendered agent prompt plus its tool map.
type PromptView struct {
	InstanceID   string
	Prompt       string
	Capabilities []types.CapabilityName
	Tools        map[string]string
}

// Prompt renders the server-side agent system prompt for a live,
// owned instance.
func (s *Service) Prompt(ctx context.Context, a service.Actor, id, toolPrefix string) (*PromptView, error) {
	in, err := s.owned(ctx, a, id)
	if err != nil {
		return nil, err
	}
	if !toolPrefixRe.MatchString(toolPrefix) {
		return nil, service.BadRequest("invalid tool_prefix")
	}
	if in.Status != types.InstanceRunning {
		return nil, service.Errf(types.ErrCodeNotRunning, "instance not running")
	}
	data, err := s.mgr.PromptData(in.ID)
	if err != nil {
		switch {
		case errors.Is(err, instance.ErrNotFound):
			return nil, service.NotFound("instance not found")
		case errors.Is(err, instance.ErrNotRunning):
			return nil, service.Errf(types.ErrCodeNotRunning, "instance not running")
		default:
			return nil, service.Internal(err)
		}
	}
	var fragments []string
	var names []types.CapabilityName
	for _, p := range data.Parts {
		fragments = append(fragments, p.Factory.Prompt(p.Config))
		names = append(names, p.Factory.Name())
	}
	var secretsInfo []promptpkg.SecretInfo
	for _, sec := range data.Secrets {
		secretsInfo = append(secretsInfo, promptpkg.SecretInfo{Name: sec.Name, Domains: sec.Domains})
	}
	var netHost string
	if len(data.NetHosts) > 0 {
		netHost = data.NetHosts[0]
	} else if slices.Contains(capNamesToStrings(names), "net") {
		// Unrestricted net: still show a worked example.
		netHost = "api.example.com"
	}
	text, err := promptpkg.Render(promptpkg.Input{
		InstanceID:     in.ID,
		Prefix:         toolPrefix,
		Fragments:      fragments,
		CapNames:       capNamesToStrings(names),
		Env:            data.Env,
		Secrets:        secretsInfo,
		ExecTimeoutMs:  data.Limits.ExecTimeoutMs,
		MaxSteps:       data.Limits.MaxSteps,
		MaxOutputBytes: data.Limits.MaxOutputBytes,
		TTLSeconds:     data.TTLSeconds,
		NetExampleHost: netHost,
		Persistent:     true,
	})
	if err != nil {
		return nil, service.Internal(err)
	}
	return &PromptView{
		InstanceID:   in.ID,
		Prompt:       text,
		Capabilities: names,
		Tools: map[string]string{
			"exec":       toolPrefix + "exec",
			"list_files": toolPrefix + "list_files",
			"read_file":  toolPrefix + "read_file",
		},
	}, nil
}

func capNamesToStrings(in []types.CapabilityName) []string {
	out := make([]string, len(in))
	for i, n := range in {
		out[i] = string(n)
	}
	return out
}
