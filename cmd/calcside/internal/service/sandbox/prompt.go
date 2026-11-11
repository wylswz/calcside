package sandbox

import (
	"context"
	"regexp"
	"slices"
	"strconv"
	"strings"

	promptpkg "calcside/cmd/calcside/internal/prompt"
	"calcside/cmd/calcside/internal/service"
	"calcside/cmd/calcside/internal/service/access"
	"calcside/internal/runtime"
	"calcside/internal/store"
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
	in, err := s.owned(ctx, a, id, access.View)
	if err != nil {
		return nil, err
	}
	if !toolPrefixRe.MatchString(toolPrefix) {
		return nil, service.BadRequest("invalid tool_prefix")
	}
	switch {
	case in.Status == types.InstanceDeleted:
		return nil, service.Errf(types.ErrCodeNotRunning, "instance not running")
	case expiredForRun(in, s.now().UTC()):
		return nil, s.expiredErr(ctx, in)
	case in.Status == types.InstanceLost:
		// The node is unreachable: render the durable notebook-aware
		// fallback instead of asking the dead child for fragments.
		return s.promptLost(ctx, in, toolPrefix)
	case in.Status != types.InstanceRunning:
		return nil, service.Errf(types.ErrCodeNotRunning, "instance not running")
	}
	data, err := s.rt.Prompt(ctx, &runtime.PromptRequest{InstanceID: in.ID, Owner: runtime.Owner{UserID: in.UserID}, Actor: actorOwner(a), Epoch: in.LeaseEpoch})
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
			"exec":         toolPrefix + "exec",
			"notebook":     toolPrefix + "notebook",
			"read_section": toolPrefix + "read_section",
			"run_sections": toolPrefix + "run_sections",
			"add_section":  toolPrefix + "add_section",
			"list_files":   toolPrefix + "list_files",
			"read_file":    toolPrefix + "read_file",
		},
	}, nil
}

// promptLost is the durable fallback rendered when the instance's
// runtime is lost: no node round-trip, no capability fragments — just
// the notebook-aware recovery instructions an agent needs to continue.
func (s *Service) promptLost(ctx context.Context, in *store.Instance, toolPrefix string) (*PromptView, error) {
	secs, err := s.st.ListNotebookSections(ctx, in.ID)
	if err != nil {
		return nil, service.Internal(err)
	}
	names := make([]string, 0, len(secs))
	for _, sec := range secs {
		names = append(names, sec.Name)
	}
	prompt := "You are connected to calcside instance " + in.ID + ".\n\n" +
		"IMPORTANT: this instance's runtime process was lost. All in-memory state — variables, functions, files — is GONE. " +
		"The instance's NOTEBOOK is preserved: it is a durable, ordered list of named code sections and your only memory of prior work.\n\n"
	if len(names) > 0 {
		prompt += "The notebook has " + strconv.Itoa(len(names)) + " sections: " + strings.Join(names, ", ") + ".\n" +
			"Nothing has been re-executed; every section is not_run.\n\n"
	} else {
		prompt += "The notebook is empty — there are no saved sections.\n\n"
	}
	prompt += "How to continue:\n" +
		"1. Read the notebook outline before doing anything (" + toolPrefix + "notebook or GET /api/v1/instances/" + in.ID + "/notebook): it lists every section's state, defines and uses.\n" +
		"2. Your next exec or run call will RESTORE the instance into a fresh EMPTY process under the same id. That call returns an instance_restored error and does NOT execute — it only brings the process back.\n" +
		"3. After the restore, run the sections you need in order (" + toolPrefix + "run_sections with sections=\"all\" or \"stale\", or name specific sections). The notebook's meaning is: run sections top to bottom in a fresh process.\n" +
		"4. Do not assume any variable, file, or function from before the loss exists. Re-run what you need; scratch execs work but do not create sections — save durable code as sections.\n"
	return &PromptView{
		InstanceID: in.ID,
		Prompt:     prompt,
		Tools: map[string]string{
			"exec":         toolPrefix + "exec",
			"notebook":     toolPrefix + "notebook",
			"read_section": toolPrefix + "read_section",
			"run_sections": toolPrefix + "run_sections",
			"add_section":  toolPrefix + "add_section",
			"list_files":   toolPrefix + "list_files",
			"read_file":    toolPrefix + "read_file",
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
