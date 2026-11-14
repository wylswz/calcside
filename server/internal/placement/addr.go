package placement

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// maxNodeAddrLen bounds an advertised endpoint; nothing legitimate
// comes close and the value is persisted and dialed by every API.
const maxNodeAddrLen = 320

// NormalizeNodeAddr canonicalizes a worker-advertised dialing address
// into the trusted host:port form persisted in serving_nodes. It
// accepts DNS names, IPv4 and bracketed IPv6 (bracketed IPv4 too) and
// always returns net.JoinHostPort form, so stored endpoints compare
// and dial identically on every API replica.
//
// This is a syntactic gate only: the shared HMAC key is the enrollment
// trust boundary in self-registered mode, and authenticated cluster
// members may legitimately advertise pod IPs, service names or
// port-mappings. Loopback and private addresses are allowed for dev
// and in-cluster use; unspecified addresses (0.0.0.0, ::) are not —
// they can never be dialed as a peer.
func NormalizeNodeAddr(addr string) (string, error) {
	if addr == "" || len(addr) > maxNodeAddrLen {
		return "", errors.New("placement: node address empty or too long")
	}
	// Whitespace and control bytes can never appear in a legitimate
	// host:port; rejecting them up front keeps them out of every
	// downstream parser.
	for i := 0; i < len(addr); i++ {
		if addr[i] <= ' ' || addr[i] == 0x7f {
			return "", errors.New("placement: node address contains whitespace or control bytes")
		}
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("placement: invalid node address: %w", err)
	}
	p := 0
	for i := 0; i < len(port); i++ {
		c := port[i]
		if c < '0' || c > '9' {
			return "", errors.New("placement: node address port must be numeric")
		}
		p = p*10 + int(c-'0')
		if p > 65535 {
			return "", errors.New("placement: node address port out of range")
		}
	}
	if p == 0 {
		return "", errors.New("placement: node address port must be nonzero")
	}
	canonical := strconv.Itoa(p)
	host = strings.TrimSuffix(host, ".")
	if host == "" {
		return "", errors.New("placement: node address has empty host")
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsUnspecified() {
			return "", errors.New("placement: node address host is unspecified")
		}
		return net.JoinHostPort(host, canonical), nil
	}
	if !validDNSHost(host) {
		return "", errors.New("placement: node address host is not a valid DNS name")
	}
	return net.JoinHostPort(strings.ToLower(host), canonical), nil
}

// validDNSHost reports whether host is a dotted ASCII DNS name: labels
// of [a-zA-Z0-9-], none empty, no leading/trailing hyphen, each at most
// 63 bytes; the whole name stays under 253.
func validDNSHost(host string) bool {
	if len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if !validDNSLabel(label) {
			return false
		}
	}
	return true
}

func validDNSLabel(label string) bool {
	if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
		return false
	}
	for i := 0; i < len(label); i++ {
		c := label[i]
		if c != '-' && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}
