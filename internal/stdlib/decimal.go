package stdlib

import (
	"errors"
	"math/big"
	"strings"

	"go.starlark.net/starlark"
)

const (
	MaxDecimalInput  = 128
	MaxDecimalScale  = 100
	MaxDecimalDigits = 1000
)

// dec is an exact decimal value unscaled / 10^scale.
type dec struct {
	u     *big.Int
	scale int
}

var bigTen = big.NewInt(10)

func pow10(n int) *big.Int { return new(big.Int).Exp(bigTen, big.NewInt(int64(n)), nil) }

func parseDecimal(v starlark.Value) (dec, error) {
	switch x := v.(type) {
	case starlark.Int:
		if len(x.String()) > MaxDecimalInput {
			return dec{}, errors.New("decimal input exceeds 128 characters")
		}
		return dec{u: x.BigInt(), scale: 0}, nil
	case starlark.String:
		s := string(x)
		if len(s) > MaxDecimalInput {
			return dec{}, errors.New("decimal input exceeds 128 characters")
		}
		body := s
		if strings.HasPrefix(s, "-") || strings.HasPrefix(s, "+") {
			body = s[1:]
		}
		whole, frac, _ := strings.Cut(body, ".")
		if whole == "" && frac == "" || !digitsOnly(whole) || !digitsOnly(frac) {
			return dec{}, errors.New("invalid decimal string (plain digits with optional sign and point; no exponent)")
		}
		u, ok := new(big.Int).SetString("0"+whole+frac, 10)
		if !ok {
			return dec{}, errors.New("invalid decimal string")
		}
		if strings.HasPrefix(s, "-") {
			u.Neg(u)
		}
		return dec{u: u, scale: len(frac)}, nil
	case starlark.Float:
		return dec{}, errors.New("floats are inexact; pass decimal strings or ints")
	}
	return dec{}, errors.New("expected a decimal string or int")
}

func digitsOnly(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func (d dec) rescaleUp(scale int) *big.Int {
	return new(big.Int).Mul(d.u, pow10(scale-d.scale))
}

func (d dec) format() (starlark.Value, error) {
	digits := new(big.Int).Abs(d.u).String()
	if len(digits) > MaxDecimalDigits {
		return nil, errors.New("decimal result exceeds 1000 digits")
	}
	if d.scale > 0 {
		if len(digits) <= d.scale {
			digits = strings.Repeat("0", d.scale-len(digits)+1) + digits
		}
		digits = digits[:len(digits)-d.scale] + "." + digits[len(digits)-d.scale:]
	}
	if d.u.Sign() < 0 {
		digits = "-" + digits
	}
	return starlark.String(digits), nil
}

var roundingModes = map[string]bool{"half_even": true, "half_up": true, "half_down": true, "down": true, "up": true, "floor": true, "ceiling": true}

// roundQuo returns n/d rounded to an integer with the given mode.
func roundQuo(n, d *big.Int, mode string) *big.Int {
	q, r := new(big.Int).QuoRem(n, d, new(big.Int))
	if r.Sign() == 0 {
		return q
	}
	sign := int64(n.Sign() * d.Sign())
	half := new(big.Int).Abs(r)
	half.Lsh(half, 1)
	cmp := half.Cmp(new(big.Int).Abs(d))
	away := false
	switch mode {
	case "up":
		away = true
	case "floor":
		away = sign < 0
	case "ceiling":
		away = sign > 0
	case "half_up":
		away = cmp >= 0
	case "half_down":
		away = cmp > 0
	case "half_even":
		away = cmp > 0 || cmp == 0 && q.Bit(0) == 1
	}
	if away {
		q.Add(q, big.NewInt(sign))
	}
	return q
}

func decimalArgs(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple, names ...string) ([]dec, error) {
	vals := make([]starlark.Value, len(names))
	pairs := make([]any, 0, 2*len(names))
	for i, name := range names {
		pairs = append(pairs, name, &vals[i])
	}
	if err := unpack(b, args, kwargs, pairs...); err != nil {
		return nil, err
	}
	out := make([]dec, len(vals))
	for i, v := range vals {
		d, err := parseDecimal(v)
		if err != nil {
			return nil, err
		}
		out[i] = d
	}
	return out, nil
}

func scaleAndMode(scale int, mode string) error {
	if scale < 0 || scale > MaxDecimalScale {
		return errors.New("scale must be between 0 and 100")
	}
	if !roundingModes[mode] {
		return errors.New("rounding must be half_even, half_up, half_down, down, up, floor or ceiling")
	}
	return nil
}

func decimalCall(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	switch b.Name() {
	case "decimal.add", "decimal.sub", "decimal.mul", "decimal.compare":
		ds, err := decimalArgs(b, args, kwargs, "a", "b")
		if err != nil {
			return nil, err
		}
		a, c := ds[0], ds[1]
		if b.Name() == "decimal.mul" {
			return dec{u: new(big.Int).Mul(a.u, c.u), scale: a.scale + c.scale}.format()
		}
		scale := max(a.scale, c.scale)
		x, y := a.rescaleUp(scale), c.rescaleUp(scale)
		switch b.Name() {
		case "decimal.compare":
			return starlark.MakeInt(x.Cmp(y)), nil
		case "decimal.sub":
			return dec{u: x.Sub(x, y), scale: scale}.format()
		}
		return dec{u: x.Add(x, y), scale: scale}.format()
	case "decimal.div":
		var av, bv starlark.Value
		var scale int
		mode := "half_even"
		if err := unpack(b, args, kwargs, "a", &av, "b", &bv, "scale", &scale, "rounding?", &mode); err != nil {
			return nil, err
		}
		if err := scaleAndMode(scale, mode); err != nil {
			return nil, err
		}
		a, err := parseDecimal(av)
		if err != nil {
			return nil, err
		}
		c, err := parseDecimal(bv)
		if err != nil {
			return nil, err
		}
		if c.u.Sign() == 0 {
			return nil, errors.New("division by zero")
		}
		num := new(big.Int).Mul(a.u, pow10(scale+c.scale))
		den := new(big.Int).Mul(c.u, pow10(a.scale))
		return dec{u: roundQuo(num, den, mode), scale: scale}.format()
	case "decimal.round":
		var av starlark.Value
		var scale int
		mode := "half_even"
		if err := unpack(b, args, kwargs, "a", &av, "scale", &scale, "rounding?", &mode); err != nil {
			return nil, err
		}
		if err := scaleAndMode(scale, mode); err != nil {
			return nil, err
		}
		a, err := parseDecimal(av)
		if err != nil {
			return nil, err
		}
		if scale >= a.scale {
			return dec{u: a.rescaleUp(scale), scale: scale}.format()
		}
		return dec{u: roundQuo(a.u, pow10(a.scale-scale), mode), scale: scale}.format()
	case "decimal.sum":
		var v starlark.Value
		if err := unpack(b, args, kwargs, "values", &v); err != nil {
			return nil, err
		}
		seq, err := sequence(v, MaxValues)
		if err != nil {
			return nil, err
		}
		total := dec{u: new(big.Int)}
		for i := 0; i < seq.Len(); i++ {
			if i&4095 == 4095 {
				if err := checkpoint(t); err != nil {
					return nil, err
				}
			}
			d, err := parseDecimal(seq.Index(i))
			if err != nil {
				return nil, err
			}
			scale := max(total.scale, d.scale)
			total = dec{u: new(big.Int).Add(total.rescaleUp(scale), d.rescaleUp(scale)), scale: scale}
		}
		return total.format()
	case "decimal.to_minor":
		var av starlark.Value
		var scale int
		if err := unpack(b, args, kwargs, "a", &av, "scale", &scale); err != nil {
			return nil, err
		}
		if err := scaleAndMode(scale, "down"); err != nil {
			return nil, err
		}
		a, err := parseDecimal(av)
		if err != nil {
			return nil, err
		}
		if a.scale <= scale {
			return starlark.MakeBigInt(a.rescaleUp(scale)), nil
		}
		q, r := new(big.Int).QuoRem(a.u, pow10(a.scale-scale), new(big.Int))
		if r.Sign() != 0 {
			return nil, errors.New("value has more fractional digits than scale; round explicitly first")
		}
		return starlark.MakeBigInt(q), nil
	}
	// decimal.from_minor
	var minor starlark.Int
	var scale int
	if err := unpack(b, args, kwargs, "minor", &minor, "scale", &scale); err != nil {
		return nil, err
	}
	if err := scaleAndMode(scale, "down"); err != nil {
		return nil, err
	}
	d, err := parseDecimal(minor)
	if err != nil {
		return nil, err
	}
	return dec{u: d.u, scale: scale}.format()
}
