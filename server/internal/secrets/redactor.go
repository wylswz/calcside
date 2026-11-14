package secrets

import (
	"bytes"
	"context"
	"errors"
	"sort"
)

// StreamRedactor redacts known secret encodings from an incremental byte
// stream before any byte reaches scripts (EP-0021). It retains only a
// suffix that could still prefix a longer variant — never a fixed
// max-secret-length tail — so small responses with long unrelated
// secrets flush immediately. At each pending position it chooses the
// longest complete match; a strict prefix of a longer candidate is held
// until the next Push or safely emitted at EOF.
type StreamRedactor struct {
	variants [][]byte // longest first
	repl     [][]byte // same-index replacement
	maxVar   int
	pend     []byte // unprocessed input, bounded: last chunk + maxVar
	emitted  int64
	maxOut   int64
}

// NewStreamRedactor builds an incremental redactor over this set's
// variants (raw/base64/URL/JSON escapes and unnamed extras).
// maxOutput bounds cumulative emitted bytes; 0 or negative means
// unbounded (callers pass their configured caps).
func (s *Set) NewStreamRedactor(maxOutput int64) *StreamRedactor {
	r := &StreamRedactor{maxOut: maxOutput}
	if s == nil {
		return r
	}
	type pair struct{ v, m []byte }
	var pairs []pair
	for _, name := range s.Names() {
		sec := s.m[name]
		for _, enc := range variants(sec.value) {
			pairs = append(pairs, pair{[]byte(enc), []byte("[REDACTED:" + name + "]")})
		}
	}
	for _, v := range s.extra {
		for _, enc := range variants(v) {
			pairs = append(pairs, pair{[]byte(enc), []byte("[REDACTED]")})
		}
	}
	sort.SliceStable(pairs, func(i, j int) bool { return len(pairs[i].v) > len(pairs[j].v) })
	for _, p := range pairs {
		r.variants = append(r.variants, p.v)
		r.repl = append(r.repl, p.m)
		if len(p.v) > r.maxVar {
			r.maxVar = len(p.v)
		}
	}
	return r
}

// Push feeds one input chunk; see PushContext.
func (r *StreamRedactor) Push(chunk []byte, eof bool) ([]byte, error) {
	return r.PushContext(context.Background(), chunk, eof)
}

// PushContext feeds one input chunk and returns all bytes now safe to
// emit, checking ctx periodically. At EOF everything pending flushes.
// A strict prefix of a LONGER variant is always held until
// disambiguation, so a short secret can never mask a longer one split
// across chunks. The output cap is enforced BEFORE bytes are appended.
func (r *StreamRedactor) PushContext(ctx context.Context, chunk []byte, eof bool) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.pend = append(r.pend, chunk...)
	var out []byte
	emit := func(b []byte) error {
		if r.maxOut > 0 && r.emitted+int64(len(b)) > r.maxOut {
			return errors.New("redacted output exceeds byte limit")
		}
		out = append(out, b...)
		r.emitted += int64(len(b))
		return nil
	}
	i := 0
	for i < len(r.pend) {
		if i&1023 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		// Disambiguation first: if the remaining bytes could still
		// extend into a longer variant, do not accept a shorter match
		// or emit yet — unless this is the final chunk.
		if !eof && r.prefixTailAt(i) {
			break
		}
		if repl, n, ok := r.matchAt(i); ok {
			if err := emit(repl); err != nil {
				return nil, err
			}
			i += n
			continue
		}
		if err := emit(r.pend[i : i+1]); err != nil {
			return nil, err
		}
		i++
	}
	r.pend = append([]byte(nil), r.pend[i:]...)
	return out, nil
}

func (r *StreamRedactor) matchAt(i int) ([]byte, int, bool) {
	for k, v := range r.variants {
		if len(r.pend)-i >= len(v) && len(v) > 0 && r.pend[i] == v[0] && bytes.Equal(r.pend[i:i+len(v)], v) {
			return r.repl[k], len(v), true
		}
	}
	return nil, 0, false
}

func (r *StreamRedactor) prefixTailAt(i int) bool {
	for _, v := range r.variants {
		n := len(r.pend) - i
		if len(v) > n && n > 0 && r.pend[i] == v[0] && bytes.Equal(v[:n], r.pend[i:]) {
			return true
		}
	}
	return false
}
