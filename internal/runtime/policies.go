package runtime

const BlockPrivateNetworkPolicy = "builtin.block_private_network"

type BuiltinPolicy struct {
	Name        string
	Description string
	Default     bool
}

func BuiltinPolicies() []BuiltinPolicy {
	return []BuiltinPolicy{{
		Name:        BlockPrivateNetworkPolicy,
		Description: "Block private, loopback, link-local and reserved IP addresses, including DNS results and redirect targets. Server CIDR exemptions still apply.",
		Default:     true,
	}}
}

func IsBuiltinPolicy(name string) bool {
	for _, p := range BuiltinPolicies() {
		if p.Name == name {
			return true
		}
	}
	return false
}

func EffectivePolicies(names []string) []string {
	if names != nil {
		return names
	}
	out := []string{}
	for _, p := range BuiltinPolicies() {
		if p.Default {
			out = append(out, p.Name)
		}
	}
	return out
}
