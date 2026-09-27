package sandbox

import (
	"context"
	"regexp"
	"slices"

	promptpkg "calcside/internal/prompt"
	"calcside/internal/runtime"
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

// Prompt renders the server-side agent system prompt for a live, owned
// instance. The node renders the per-capability fragments — only it has
// the capability factories and the instance's effective config — and
// this composes them into the final prompt.
func (s *Service) Prompt(ctx context.Context, a service.Actor, id, toolPrefix string) (*PromptView, error) {
	// Ownership is checked before the argument so that probing for
	// another user's instance cannot be distinguished by the error.
	if _, err := s.owned(ctx, a, id); err != nil {
		return nil, err
	}
	if !toolPrefixRe.MatchString(toolPrefix) {
		return nil, service.BadRequest("invalid tool_prefix")
	}
	in, err := s.running(ctx, a, id)
	if err != nil {
		return nil, err
	}
	data, err := s.rt.Prompt(ctx, &runtime.PromptRequest{InstanceID: in.ID, Owner: owner(a), Epoch: in.LeaseEpoch})
	if err != nil {
		return nil, fail(err)
	}
	fragments := make([]string, 0, len(data.Fragments))
	names := make([]types.CapabilityName, 0, len(data.Fragments))
	for _, f := range data.Fragments {
		fragments = append(fragments, f.Text)
		names = append(names, f.Capability)
	}
	var secretsInfo []promptpkg.SecretInfo
	for _, sec := range data.Secrets {
		secretsInfo = append(secretsInfo, promptpkg.SecretInfo{Name: sec.Name, Domains: sec.Domains})
	}
	capNames := capNamesToStrings(names)
	var netHost string
	if len(data.NetHosts) > 0 {
		netHost = data.NetHosts[0]
	} else if slices.Contains(capNames, "net") {
		// Unrestricted net: still show a worked example.
		netHost = "api.example.com"
	}
	text, err := promptpkg.Render(promptpkg.Input{
		InstanceID:     in.ID,
		Prefix:         toolPrefix,
		Fragments:      fragments,
		CapNames:       capNames,
		Env:            data.Env,
		Secrets:        secretsInfo,
		ExecTimeoutMs:  data.ExecTimeoutMs,
		MaxSteps:       data.MaxSteps,
		MaxOutputBytes: data.MaxOutputBytes,
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
