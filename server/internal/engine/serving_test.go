package engine

import (
	"encoding/json"
	"math"
	"math/big"
	"strconv"
	"strings"
	"testing"

	"go.starlark.net/starlark"

	"calcside/internal/serving"
)

func mustMarshal(t *testing.T, v starlark.Value, limits serving.Limits) json.RawMessage {
	t.Helper()
	b, err := MarshalJSONValue(v, limits)
	if err != nil {
		t.Fatal(err)
	}
	var check any
	if err := json.Unmarshal(b, &check); err != nil {
		t.Fatalf("invalid JSON output %q: %v", b, err)
	}
	return b
}

func TestMarshalJSONValueBasics(t *testing.T) {
	d := starlark.NewDict(4)
	_ = d.SetKey(starlark.String("n"), starlark.None)
	_ = d.SetKey(starlark.String("t"), starlark.True)
	_ = d.SetKey(starlark.String("s"), starlark.String("hi\"x\n"))
	_ = d.SetKey(starlark.String("i"), starlark.MakeInt(-42))
	_ = d.SetKey(starlark.String("f"), starlark.Float(1.5))
	_ = d.SetKey(starlark.String("l"), starlark.NewList([]starlark.Value{starlark.MakeInt(1)}))
	_ = d.SetKey(starlark.String("u"), starlark.Tuple{starlark.String("e")})
	got := string(mustMarshal(t, d, serving.Limits{}))
	for _, want := range []string{`"n":null`, `"t":true`, `"i":-42`, `"f":1.5`, `"l":[1]`, `"u":["e"]`, `"s":"hi\"x\n"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %s in %s", want, got)
		}
	}
	if string(mustMarshal(t, starlark.None, serving.Limits{})) != "null" {
		t.Fatal("bad null")
	}
}

func TestMarshalJSONValueBigInt(t *testing.T) {
	bi := new(big.Int).Lsh(big.NewInt(1), 200)
	got := mustMarshal(t, starlark.MakeBigInt(bi), serving.Limits{})
	if string(got) != bi.Text(10) {
		t.Fatalf("big int lost precision: %s", got)
	}
	// Bit-length preflight: an int too big for the byte budget fails
	// before decimal rendering.
	huge := new(big.Int).Lsh(big.NewInt(1), 4000)
	if _, err := MarshalJSONValue(starlark.MakeBigInt(huge), serving.Limits{MaxBytes: 64}); err == nil {
		t.Fatal("expected byte-limit error")
	}
}

func TestMarshalJSONValueBigIntExactFit(t *testing.T) {
	// A 21-digit (>int64) value needs exactly 21 bytes; a negative one
	// needs 22 including the sign.
	pos := starlark.MakeBigInt(new(big.Int).Lsh(big.NewInt(1), 67)) // 21 digits
	neg := starlark.MakeBigInt(new(big.Int).Neg(new(big.Int).Lsh(big.NewInt(1), 67)))

	got := mustMarshal(t, pos, serving.Limits{MaxBytes: 21})
	if len(got) != 21 {
		t.Fatalf("expected exact fit, got %q", got)
	}
	if _, err := MarshalJSONValue(pos, serving.Limits{MaxBytes: 20}); err == nil {
		t.Fatal("expected byte-limit error")
	}
	got = mustMarshal(t, neg, serving.Limits{MaxBytes: 22})
	if len(got) != 22 || got[0] != '-' {
		t.Fatalf("expected exact fit, got %q", got)
	}
	if _, err := MarshalJSONValue(neg, serving.Limits{MaxBytes: 21}); err == nil {
		t.Fatal("expected byte-limit error")
	}
}

func TestMarshalJSONValueRoundtrip(t *testing.T) {
	// Every supported converter output must decode under the same
	// limits: integer precision beyond float64, the 256-token bound
	// (sign included), and rejection at 257.
	maxDigits := new(big.Int).Sub(new(big.Int).Exp(big.NewInt(10), big.NewInt(255), nil), big.NewInt(1))
	negMax := new(big.Int).Neg(new(big.Int).Sub(new(big.Int).Exp(big.NewInt(10), big.NewInt(254), nil), big.NewInt(1)))

	// 255 digits -> 255 bytes, under the cap.
	got := mustMarshal(t, starlark.MakeBigInt(maxDigits), serving.Limits{})
	if len(got) != 255 {
		t.Fatalf("expected 255-byte token, got %d", len(got))
	}
	// 254 digits + sign -> 255 bytes, under the cap.
	got = mustMarshal(t, starlark.MakeBigInt(negMax), serving.Limits{})
	if len(got) != 255 || got[0] != '-' {
		t.Fatalf("expected 255-byte token, got %d", len(got))
	}
	// Decoded back exactly.
	v, err := serving.DecodeJSON(mustMarshal(t, starlark.MakeBigInt(maxDigits), serving.Limits{}), serving.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if n, ok := v.(json.Number); !ok || n.String() != maxDigits.Text(10) {
		t.Fatalf("roundtrip lost precision: %v", v)
	}

	// 256 digits fits exactly (positive, no sign).
	exact := new(big.Int).Sub(new(big.Int).Exp(big.NewInt(10), big.NewInt(256), nil), big.NewInt(1))
	got = mustMarshal(t, starlark.MakeBigInt(exact), serving.Limits{})
	if len(got) != serving.MaxNumberTokenBytes {
		t.Fatalf("expected %d-byte token, got %d", serving.MaxNumberTokenBytes, len(got))
	}
	// 256 digits + sign = 257 bytes -> rejected.
	overNeg := new(big.Int).Neg(exact)
	if _, err := MarshalJSONValue(starlark.MakeBigInt(overNeg), serving.Limits{}); err == nil {
		t.Fatal("expected token-cap error")
	}
	// 257 digits -> rejected regardless of sign.
	over := new(big.Int).Exp(big.NewInt(10), big.NewInt(256), nil)
	if _, err := MarshalJSONValue(starlark.MakeBigInt(over), serving.Limits{}); err == nil {
		t.Fatal("expected token-cap error")
	}
	// The boundary token itself decodes back.
	if _, err := serving.DecodeJSON(got, serving.Limits{}); err != nil {
		t.Fatal(err)
	}
}

func TestMarshalJSONValueRejects(t *testing.T) {
	cyclicList := starlark.NewList(nil)
	_ = cyclicList.Append(cyclicList)
	cyclicDict := starlark.NewDict(1)
	_ = cyclicDict.SetKey(starlark.String("self"), cyclicDict)
	// A cycle that passes through a tuple still fails.
	tupleCycle := starlark.NewList(nil)
	tupleCycle.Append(starlark.Tuple{tupleCycle})

	badKey := starlark.NewDict(1)
	_ = badKey.SetKey(starlark.MakeInt(1), starlark.MakeInt(2))

	set := starlark.NewSet(1)
	_ = set.Insert(starlark.MakeInt(1))

	fn := starlark.NewBuiltin("f", func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		return starlark.None, nil
	})

	deep := starlark.Value(starlark.NewList(nil))
	for i := 0; i < 40; i++ {
		deep = starlark.NewList([]starlark.Value{deep})
	}
	badUTF8 := starlark.String("\xff\xfe")
	badKeyUTF8 := starlark.NewDict(1)
	_ = badKeyUTF8.SetKey(starlark.String("\xff"), starlark.MakeInt(1))

	cases := []struct {
		name   string
		v      starlark.Value
		limits serving.Limits
	}{
		{"cyclic list", cyclicList, serving.Limits{}},
		{"cyclic dict", cyclicDict, serving.Limits{}},
		{"cycle via tuple", tupleCycle, serving.Limits{}},
		{"non-string key", badKey, serving.Limits{}},
		{"set", set, serving.Limits{}},
		{"bytes", starlark.Bytes("x"), serving.Limits{}},
		{"function", fn, serving.Limits{}},
		{"nan", starlark.Float(math.NaN()), serving.Limits{}},
		{"inf", starlark.Float(math.Inf(1)), serving.Limits{}},
		{"deep nesting", deep, serving.Limits{}},
		{"invalid utf8 string", badUTF8, serving.Limits{}},
		{"invalid utf8 key", badKeyUTF8, serving.Limits{}},
		{"node budget", starlark.NewList([]starlark.Value{starlark.MakeInt(1), starlark.MakeInt(2)}), serving.Limits{MaxNodes: 2}},
		{"byte budget", starlark.NewList([]starlark.Value{starlark.MakeInt(1), starlark.MakeInt(2)}), serving.Limits{MaxBytes: 4}},
		{"escape overhead", starlark.String(strings.Repeat(`"`, 10)), serving.Limits{MaxBytes: 12}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := MarshalJSONValue(tc.v, tc.limits); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestMarshalJSONValueRepeatedAlias(t *testing.T) {
	// Acyclic aliases are fine: only same-container-on-active-path is
	// a cycle.
	shared := starlark.NewList([]starlark.Value{starlark.MakeInt(1)})
	outer := starlark.NewList([]starlark.Value{shared, shared, shared})
	got := string(mustMarshal(t, outer, serving.Limits{}))
	if got != `[[1],[1],[1]]` {
		t.Fatalf("bad alias output: %s", got)
	}
	d := starlark.NewDict(2)
	_ = d.SetKey(starlark.String("a"), shared)
	_ = d.SetKey(starlark.String("b"), shared)
	got = string(mustMarshal(t, d, serving.Limits{}))
	if got != `{"a":[1],"b":[1]}` {
		t.Fatalf("bad alias output: %s", got)
	}
}

func TestMarshalJSONValueAllocations(t *testing.T) {
	// Conversion must fail from limits before allocating in proportion
	// to the input value: fixture construction happens first, and only
	// MarshalJSONValue's allocations are measured.
	huge := starlark.MakeBigInt(new(big.Int).Lsh(big.NewInt(1), 2<<20))
	d := starlark.NewDict(10_000)
	for i := 0; i < 10_000; i++ {
		_ = d.SetKey(starlark.String("k"+strconv.Itoa(i)), starlark.MakeInt(i))
	}
	res := testing.Benchmark(func(b *testing.B) {
		for b.Loop() {
			_, _ = MarshalJSONValue(huge, serving.Limits{MaxBytes: 16})
			_, _ = MarshalJSONValue(d, serving.Limits{MaxNodes: 4})
		}
	})
	if got := res.AllocedBytesPerOp(); got > 64<<10 {
		t.Fatalf("marshal allocated %d bytes against tiny limits, want at most 64 KiB", got)
	}
}

func TestMarshalJSONValueAllocationsManyInts(t *testing.T) {
	// Many >int64 values under the default byte budget: the capped
	// per-value threshold (~1 KiB) keeps per-element allocation bounded
	// even when the remaining budget is large.
	vals := make([]starlark.Value, 128)
	for i := range vals {
		vals[i] = starlark.MakeBigInt(new(big.Int).Add(
			new(big.Int).Lsh(big.NewInt(1), 67), big.NewInt(int64(i))))
	}
	lst := starlark.NewList(vals)
	res := testing.Benchmark(func(b *testing.B) {
		for b.Loop() {
			if _, err := MarshalJSONValue(lst, serving.Limits{}); err != nil {
				b.Fatal(err)
			}
		}
	})
	if got := res.AllocedBytesPerOp(); got > 128<<10 {
		t.Fatalf("marshal allocated %d bytes for 128 big ints, want at most 128 KiB", got)
	}
}
