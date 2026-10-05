// Package secrets provides the encrypted-at-rest vault cipher and the
// per-instance secret set used for net placeholder injection. Secret
// values never leave this package except inside HTTP requests performed
// by the net capability; Redact scrubs every leaked encoding from any
// string before it reaches scripts, audit, or the API.
package secrets

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"calcside/internal/hostmatch"
)

// NamePattern constrains env var and secret names (SCREAMING_SNAKE).
var NamePattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,63}$`)

const (
	MaxValueBytes = 16 << 10
	MaxDomains    = 32
)

// ValidName reports whether s is a legal env/secret name.
func ValidName(s string) bool { return NamePattern.MatchString(s) }

// ValidateDomains parses a domain allowlist; empty = unrestricted (the
// secret may go to any host the instance's net allow_hosts permits).
func ValidateDomains(domains []string) ([]hostmatch.Rule, error) {
	if len(domains) == 0 {
		return nil, nil
	}
	if len(domains) > MaxDomains {
		return nil, fmt.Errorf("allowed_domains exceeds %d entries", MaxDomains)
	}
	return hostmatch.ParseAll(domains)
}

// Cipher encrypts vault secrets at rest (AES-256-GCM). AAD binds each
// ciphertext to userID+"/"+name.
type Cipher struct {
	aead cipher.AEAD
}

// NewCipher builds a cipher from a base64-encoded 32-byte key.
func NewCipher(b64 string) (*Cipher, error) {
	key, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("secret key: bad base64: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("secret key: want 32 bytes, got %d", len(key))
	}
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	return &Cipher{aead: aead}, nil
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Seal returns nonce||ciphertext for plaintext under AAD.
func (c *Cipher) Seal(plaintext []byte, aad string) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return c.aead.Seal(nonce, nonce, plaintext, []byte(aad)), nil
}

// Open decrypts nonce||ciphertext under AAD.
func (c *Cipher) Open(sealed []byte, aad string) ([]byte, error) {
	n := c.aead.NonceSize()
	if len(sealed) < n {
		return nil, fmt.Errorf("secrets: ciphertext too short")
	}
	return c.aead.Open(nil, sealed[:n], sealed[n:], []byte(aad))
}

// Secret is a resolved secret value plus its effective domain rules.
type Secret struct {
	value []byte
	rules []hostmatch.Rule
}

// Set is a per-instance collection of resolved secrets (memory only).
type Set struct {
	m map[string]*Secret
}

// NewSet builds an empty set.
func NewSet() *Set { return &Set{m: map[string]*Secret{}} }

// Add inserts a secret; rules must already be validated.
func (s *Set) Add(name string, value []byte, rules []hostmatch.Rule) {
	if s.m == nil {
		s.m = map[string]*Secret{}
	}
	v := make([]byte, len(value))
	copy(v, value)
	s.m[name] = &Secret{value: v, rules: rules}
}

// Names returns sorted secret names (the only thing scripts may learn).
func (s *Set) Names() []string {
	out := make([]string, 0, len(s.m))
	for n := range s.m {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Lookup returns the secret for name or nil.
func (s *Set) Lookup(name string) *Secret { return s.m[name] }

// Value is the plaintext (never exposed to Starlark).
func (sec *Secret) Value() []byte { return sec.value }

// Allows reports whether host at effPort matches this secret's rules.
// An empty rule set is unrestricted.
func (sec *Secret) Allows(host, effPort string) bool {
	if len(sec.rules) == 0 {
		return true
	}
	for _, r := range sec.rules {
		if r.Matches(host, effPort) {
			return true
		}
	}
	return false
}

// Domains returns the allowlist entries as configured strings.
func (s *Set) Domains(name string) []string {
	sec := s.m[name]
	if sec == nil {
		return nil
	}
	out := make([]string, len(sec.rules))
	for i, r := range sec.rules {
		if r.Port != "" {
			out[i] = r.Host + ":" + r.Port
		} else {
			out[i] = r.Host
		}
	}
	return out
}

// Wipe zeroes all secret values (instance end).
func (s *Set) Wipe() {
	for _, sec := range s.m {
		for i := range sec.value {
			sec.value[i] = 0
		}
	}
	s.m = map[string]*Secret{}
}

// variants returns the distinct encodings a secret may leak through.
func variants(v []byte) []string {
	raw := string(v)
	jb, _ := json.Marshal(raw)
	jsonEsc := ""
	if len(jb) >= 2 {
		jsonEsc = string(jb[1 : len(jb)-1])
	}
	all := []string{
		raw,
		base64.StdEncoding.EncodeToString(v),
		base64.RawURLEncoding.EncodeToString(v),
		url.QueryEscape(raw),
		url.PathEscape(raw),
		jsonEsc,
	}
	seen := map[string]bool{}
	out := all[:0]
	for _, s := range all {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// Redact replaces every encoding of every secret value in s with
// [REDACTED:NAME], longest values first.
func (s *Set) Redact(str string) string {
	if s == nil || str == "" {
		return str
	}
	names := s.Names()
	// sort by descending raw length so longer secrets are replaced first
	sort.SliceStable(names, func(i, j int) bool {
		return len(s.m[names[i]].value) > len(s.m[names[j]].value)
	})
	for _, name := range names {
		for _, v := range variants(s.m[name].value) {
			str = strings.ReplaceAll(str, v, "[REDACTED:"+name+"]")
		}
	}
	return str
}

// Refs returns the sorted unique secret names referenced by placeholders
// in s.
var placeholderRe = regexp.MustCompile(`\{\{\s*secrets\.([A-Z_][A-Z0-9_]{0,63})\s*\}\}`)

func Refs(s string) []string {
	seen := map[string]bool{}
	for _, m := range placeholderRe.FindAllStringSubmatch(s, -1) {
		seen[m[1]] = true
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Expand substitutes every {{secrets.NAME}} placeholder in s. Unknown
// names error without including any value.
func (s *Set) Expand(str string) (string, []string, error) {
	seen := map[string]bool{}
	var names []string
	var firstErr error
	out := placeholderRe.ReplaceAllStringFunc(str, func(m string) string {
		name := placeholderRe.FindStringSubmatch(m)[1]
		sec := s.m[name]
		if sec == nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("unknown secret %q", name)
			}
			return m
		}
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
		return string(sec.value)
	})
	if firstErr != nil {
		return "", nil, firstErr
	}
	sort.Strings(names)
	return out, names, nil
}

func (s *Set) RedactBounded(ctx context.Context, str string, limit int) (string, error) {
	if len(str) > limit {
		return "", errors.New("redacted output exceeds byte limit")
	}
	if s == nil || str == "" {
		return str, ctx.Err()
	}
	names := s.Names()
	sort.SliceStable(names, func(i, j int) bool { return len(s.m[names[i]].value) > len(s.m[names[j]].value) })
	for _, name := range names {
		for _, v := range variants(s.m[name].value) {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			replacement := "[REDACTED:" + name + "]"
			delta := len(replacement) - len(v)
			if delta > 0 && strings.Count(str, v) > (limit-len(str))/delta {
				return "", errors.New("redacted output exceeds byte limit")
			}
			str = strings.ReplaceAll(str, v, replacement)
		}
	}
	return str, ctx.Err()
}
