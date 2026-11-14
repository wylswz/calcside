// Package hostmatch provides the host allowlist rule syntax shared by the
// net capability and secret domain allowlists: exact hosts, "*.suffix"
// wildcards, IP literals, and optional ":port" pinning.
package hostmatch

import (
	"fmt"
	gonet "net"
	"strings"
)

// Rule is one parsed allowlist entry: exact host or "*.suffix",
// optionally restricted/extended to a specific port.
type Rule struct {
	Host     string // exact, "*.example.com", or IP literal (lowercased)
	Port     string // "" means default ports only (80/443)
	Wildcard bool
	IsIP     bool
}

// Parse parses one entry like "api.github.com", "*.example.com",
// "127.0.0.1:8080" or "[::1]:9000".
func Parse(entry string) (Rule, error) {
	r := Rule{}
	host := entry
	if h, p, err := gonet.SplitHostPort(entry); err == nil {
		host, r.Port = h, p
		if p == "" {
			return r, fmt.Errorf("hostmatch: bad port in %q", entry)
		}
	} else if strings.Count(entry, ":") > 1 {
		// bare IPv6 literal
		host = entry
	}
	r.Host = strings.ToLower(strings.Trim(strings.TrimSpace(host), "[]"))
	if r.Host == "" {
		return r, fmt.Errorf("hostmatch: empty entry")
	}
	if strings.HasPrefix(r.Host, "*.") {
		r.Wildcard = true
	}
	if gonet.ParseIP(r.Host) != nil {
		r.IsIP = true
	}
	return r, nil
}

// ParseAll parses a list of entries.
func ParseAll(entries []string) ([]Rule, error) {
	out := make([]Rule, 0, len(entries))
	for _, e := range entries {
		r, err := Parse(e)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// MatchesHost reports whether the rule's host part matches host.
func (r Rule) MatchesHost(host string) bool {
	host = strings.ToLower(host)
	if r.Wildcard {
		suffix := strings.TrimPrefix(r.Host, "*.")
		// "*.example.com" matches sub.example.com but NOT example.com.
		return strings.HasSuffix(host, "."+suffix)
	}
	return host == r.Host
}

// Matches reports whether host at effPort (explicit effective port, e.g.
// "443") is permitted: host must match and the port must satisfy the
// rule's port semantics (no port ⇒ 80/443 only; pinned port ⇒ that port).
func (r Rule) Matches(host, effPort string) bool {
	if !r.MatchesHost(host) {
		return false
	}
	if r.Port != "" {
		return effPort == r.Port
	}
	return effPort == "80" || effPort == "443"
}

// Covers reports whether outer permits everything inner permits: inner is
// a narrowing of outer (used to validate that a per-instance secret
// allowlist stays inside the vault secret's allowlist). Ports must be
// equal on both rules.
func Covers(outer, inner Rule) bool {
	if outer.Port != inner.Port {
		return false
	}
	if inner.Wildcard {
		// "*.b.a.com" is covered only by "*.a.com" or "*.b.a.com" —
		// never by an exact outer.
		if !outer.Wildcard {
			return false
		}
		innerSuffix := strings.TrimPrefix(inner.Host, "*.")
		outerSuffix := strings.TrimPrefix(outer.Host, "*.")
		return innerSuffix == outerSuffix || strings.HasSuffix(innerSuffix, "."+outerSuffix)
	}
	// Exact inner: equal exact outer, or wildcard outer that matches it.
	if outer.Wildcard {
		return outer.MatchesHost(inner.Host)
	}
	return outer.Host == inner.Host
}
