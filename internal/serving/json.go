package serving

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// DecodeJSON parses raw into a JSON tree of nil, bool, string,
// json.Number, []any and map[string]any values only -- never float64, so
// integer tokens keep full precision. It enforces the byte, depth and
// node budgets in limits: raw bytes before parsing, depth and node
// count while building. Duplicate object keys, trailing input, invalid UTF-8,
// unpaired escaped surrogates and over-sized number tokens are
// rejected. Errors report byte offsets only, never input values.
func DecodeJSON(raw []byte, limits Limits) (any, error) {
	l := limits.withDefaults()
	if len(raw) > l.MaxBytes {
		return nil, fmt.Errorf("json: input exceeds byte limit (%d > %d)", len(raw), l.MaxBytes)
	}
	if !utf8.Valid(raw) {
		return nil, fmt.Errorf("json: invalid utf-8 input")
	}
	p := &parser{data: raw, maxDepth: l.MaxDepth, maxNodes: l.MaxNodes}
	v, err := p.value(0)
	if err != nil {
		return nil, err
	}
	p.ws()
	if p.pos != len(p.data) {
		return nil, p.errf("trailing input")
	}
	return v, nil
}

type parser struct {
	data  []byte
	pos   int
	nodes int

	maxDepth int
	maxNodes int
}

func (p *parser) errf(format string, args ...any) error {
	return fmt.Errorf("json: %s at byte %d", fmt.Sprintf(format, args...), p.pos)
}

func (p *parser) ws() {
	for p.pos < len(p.data) {
		switch p.data[p.pos] {
		case ' ', '\t', '\n', '\r':
			p.pos++
		default:
			return
		}
	}
}

func (p *parser) node() error {
	p.nodes++
	if p.nodes > p.maxNodes {
		return p.errf("node limit exceeded")
	}
	return nil
}

func (p *parser) value(depth int) (any, error) {
	if err := p.node(); err != nil {
		return nil, err
	}
	p.ws()
	if p.pos >= len(p.data) {
		return nil, p.errf("unexpected end of input")
	}
	switch c := p.data[p.pos]; {
	case c == '{':
		return p.object(depth)
	case c == '[':
		return p.array(depth)
	case c == '"':
		return p.str()
	case c == 't':
		if err := p.lit("true"); err != nil {
			return nil, err
		}
		return true, nil
	case c == 'f':
		if err := p.lit("false"); err != nil {
			return nil, err
		}
		return false, nil
	case c == 'n':
		if err := p.lit("null"); err != nil {
			return nil, err
		}
		return nil, nil
	case c == '-' || (c >= '0' && c <= '9'):
		return p.number()
	default:
		return nil, p.errf("unexpected character")
	}
}

func (p *parser) lit(word string) error {
	if p.pos+len(word) > len(p.data) || string(p.data[p.pos:p.pos+len(word)]) != word {
		return p.errf("invalid literal")
	}
	p.pos += len(word)
	return nil
}

func (p *parser) object(depth int) (any, error) {
	if depth >= p.maxDepth {
		return nil, p.errf("depth limit exceeded")
	}
	p.pos++ // '{'
	m := map[string]any{}
	p.ws()
	if p.pos < len(p.data) && p.data[p.pos] == '}' {
		p.pos++
		return m, nil
	}
	for {
		p.ws()
		if p.pos >= len(p.data) || p.data[p.pos] != '"' {
			return nil, p.errf("object key must be a string")
		}
		k, err := p.str()
		if err != nil {
			return nil, err
		}
		if _, dup := m[k]; dup {
			return nil, p.errf("duplicate object key at member %d", len(m))
		}
		p.ws()
		if p.pos >= len(p.data) || p.data[p.pos] != ':' {
			return nil, p.errf("expected ':' after object key")
		}
		p.pos++
		v, err := p.value(depth + 1)
		if err != nil {
			return nil, err
		}
		m[k] = v
		p.ws()
		if p.pos >= len(p.data) {
			return nil, p.errf("unterminated object")
		}
		switch p.data[p.pos] {
		case ',':
			p.pos++
		case '}':
			p.pos++
			return m, nil
		default:
			return nil, p.errf("expected ',' or '}' in object")
		}
	}
}

func (p *parser) array(depth int) (any, error) {
	if depth >= p.maxDepth {
		return nil, p.errf("depth limit exceeded")
	}
	p.pos++ // '['
	a := []any{}
	p.ws()
	if p.pos < len(p.data) && p.data[p.pos] == ']' {
		p.pos++
		return a, nil
	}
	for {
		v, err := p.value(depth + 1)
		if err != nil {
			return nil, err
		}
		a = append(a, v)
		p.ws()
		if p.pos >= len(p.data) {
			return nil, p.errf("unterminated array")
		}
		switch p.data[p.pos] {
		case ',':
			p.pos++
		case ']':
			p.pos++
			return a, nil
		default:
			return nil, p.errf("expected ',' or ']' in array")
		}
	}
}

// str decodes a JSON string starting at '"'. Raw input is already known
// valid UTF-8; escapes are validated including surrogate pairing.
func (p *parser) str() (string, error) {
	p.pos++      // '"'
	seg := p.pos // start of undecoded run
	var b strings.Builder
	for {
		if p.pos >= len(p.data) {
			return "", p.errf("unterminated string")
		}
		c := p.data[p.pos]
		switch {
		case c == '"':
			p.pos++
			if b.Cap() == 0 {
				return string(p.data[seg : p.pos-1]), nil
			}
			b.Write(p.data[seg : p.pos-1])
			return b.String(), nil
		case c == '\\':
			b.Write(p.data[seg:p.pos])
			if err := p.escape(&b); err != nil {
				return "", err
			}
			seg = p.pos
		case c < 0x20:
			return "", p.errf("unescaped control character in string")
		default:
			p.pos++
		}
	}
}

// escape consumes a backslash escape sequence; p.pos is at '\\'.
// The decoded bytes are appended to b.
func (p *parser) escape(b *strings.Builder) error {
	p.pos++
	if p.pos >= len(p.data) {
		return p.errf("unterminated escape")
	}
	c := p.data[p.pos]
	switch c {
	case '"', '\\', '/':
		b.WriteByte(c)
		p.pos++
		return nil
	case 'b':
		p.pos++
		b.WriteByte('\b')
		return nil
	case 'f':
		p.pos++
		b.WriteByte('\f')
		return nil
	case 'n':
		p.pos++
		b.WriteByte('\n')
		return nil
	case 'r':
		p.pos++
		b.WriteByte('\r')
		return nil
	case 't':
		p.pos++
		b.WriteByte('\t')
		return nil
	case 'u':
		p.pos++
		r, err := p.hex4()
		if err != nil {
			return err
		}
		if r >= 0xD800 && r <= 0xDBFF {
			// High surrogate: a low-surrogate escape must follow.
			if p.pos+1 >= len(p.data) || p.data[p.pos] != '\\' || p.data[p.pos+1] != 'u' {
				return p.errf("unpaired surrogate escape")
			}
			p.pos += 2
			lo, err := p.hex4()
			if err != nil {
				return err
			}
			if lo < 0xDC00 || lo > 0xDFFF {
				return p.errf("unpaired surrogate escape")
			}
			r = 0x10000 + (r-0xD800)<<10 + (lo - 0xDC00)
		} else if r >= 0xDC00 && r <= 0xDFFF {
			return p.errf("unpaired surrogate escape")
		}
		b.WriteRune(rune(r))
		return nil
	default:
		return p.errf("invalid escape")
	}
}

func (p *parser) hex4() (int, error) {
	if p.pos+4 > len(p.data) {
		return 0, p.errf("truncated \\u escape")
	}
	r := 0
	for i := 0; i < 4; i++ {
		c := p.data[p.pos+i]
		var d int
		switch {
		case c >= '0' && c <= '9':
			d = int(c - '0')
		case c >= 'a' && c <= 'f':
			d = int(c-'a') + 10
		case c >= 'A' && c <= 'F':
			d = int(c-'A') + 10
		default:
			return 0, p.errf("invalid hex digit in \\u escape")
		}
		r = r<<4 | d
	}
	p.pos += 4
	return r, nil
}

// number scans a JSON number token, enforcing the fixed token and
// exponent caps so later math/big parsing stays bounded.
func (p *parser) number() (any, error) {
	start := p.pos
	if p.pos < len(p.data) && p.data[p.pos] == '-' {
		p.pos++
	}
	if p.pos >= len(p.data) || p.data[p.pos] < '0' || p.data[p.pos] > '9' {
		return nil, p.errf("invalid number")
	}
	if p.data[p.pos] == '0' {
		p.pos++
	} else {
		for p.pos < len(p.data) && p.data[p.pos] >= '0' && p.data[p.pos] <= '9' {
			p.pos++
		}
	}
	if p.pos < len(p.data) && p.data[p.pos] == '.' {
		p.pos++
		if p.pos >= len(p.data) || p.data[p.pos] < '0' || p.data[p.pos] > '9' {
			return nil, p.errf("invalid number fraction")
		}
		for p.pos < len(p.data) && p.data[p.pos] >= '0' && p.data[p.pos] <= '9' {
			p.pos++
		}
	}
	if p.pos < len(p.data) && (p.data[p.pos] == 'e' || p.data[p.pos] == 'E') {
		p.pos++
		if p.pos < len(p.data) && (p.data[p.pos] == '+' || p.data[p.pos] == '-') {
			p.pos++
		}
		if p.pos >= len(p.data) || p.data[p.pos] < '0' || p.data[p.pos] > '9' {
			return nil, p.errf("invalid number exponent")
		}
		exp := 0
		for p.pos < len(p.data) && p.data[p.pos] >= '0' && p.data[p.pos] <= '9' {
			exp = exp*10 + int(p.data[p.pos]-'0')
			if exp > numberMaxExponent {
				return nil, p.errf("number exponent exceeds limit")
			}
			p.pos++
		}
	}
	if p.pos-start > MaxNumberTokenBytes {
		return nil, p.errf("number token exceeds limit")
	}
	return json.Number(string(p.data[start:p.pos])), nil
}
