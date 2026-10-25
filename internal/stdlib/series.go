package stdlib

import (
	"errors"
	"math"
	"slices"

	"go.starlark.net/starlark"
	"gonum.org/v1/gonum/floats"
	"gonum.org/v1/gonum/stat"
)

// MaxRollingWork bounds len(values)*window for rolling windows.
const MaxRollingWork = 10000000

// optionalFloats builds a list where NaN marks an undefined position (None).
func optionalFloats(xs []float64) (starlark.Value, error) {
	out := make([]starlark.Value, len(xs))
	for i, f := range xs {
		if math.IsNaN(f) {
			out[i] = starlark.None
			continue
		}
		v, err := floatResult(f)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return starlark.NewList(out), nil
}

func lagged(xs []float64, periods int, fn func(cur, prev float64) (float64, error)) ([]float64, error) {
	out := make([]float64, len(xs))
	for i := range xs {
		j := i - periods
		if j < 0 || j >= len(xs) {
			out[i] = math.NaN()
			continue
		}
		v, err := fn(xs[i], xs[j])
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

func seriesCall(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var value starlark.Value
	periods := 1
	var err error
	switch b.Name() {
	case "series.diff", "series.pct_change", "series.log_return", "series.shift":
		err = unpack(b, args, kwargs, "values", &value, "periods?", &periods)
	default:
		err = unpack(b, args, kwargs, "values", &value)
	}
	if err != nil {
		return nil, err
	}
	xs, err := floatsOf(t, value, 0, MaxValues)
	if err != nil {
		return nil, err
	}
	if b.Name() != "series.shift" && periods < 1 || periods > MaxValues || periods < -MaxValues {
		return nil, errors.New("periods out of range")
	}
	var out []float64
	switch b.Name() {
	case "series.diff":
		out, err = lagged(xs, periods, func(c, p float64) (float64, error) { return c - p, nil })
	case "series.pct_change":
		out, err = lagged(xs, periods, func(c, p float64) (float64, error) {
			if p == 0 {
				return math.NaN(), nil
			}
			return c/p - 1, nil
		})
	case "series.log_return":
		out, err = lagged(xs, periods, func(c, p float64) (float64, error) {
			if c <= 0 || p <= 0 {
				return 0, errors.New("log_return requires positive values")
			}
			return math.Log(c / p), nil
		})
	case "series.shift":
		out, err = lagged(xs, periods, func(_, p float64) (float64, error) { return p, nil })
	case "series.cumsum", "series.cumprod", "series.drawdown":
		out = make([]float64, len(xs))
		acc := 0.0
		if b.Name() == "series.cumprod" {
			acc = 1
		}
		for i, x := range xs {
			switch b.Name() {
			case "series.cumsum":
				acc += x
				out[i] = acc
			case "series.cumprod":
				acc *= x
				out[i] = acc
			default:
				if x <= 0 {
					return nil, errors.New("drawdown requires positive values")
				}
				acc = math.Max(acc, x)
				out[i] = x/acc - 1
			}
		}
	}
	if err != nil {
		return nil, err
	}
	return optionalFloats(out)
}

func rollingCall(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var value starlark.Value
	var window int
	fn := "mean"
	if err := unpack(b, args, kwargs, "values", &value, "window", &window, "fn?", &fn); err != nil {
		return nil, err
	}
	xs, err := floatsOf(t, value, 0, MaxValues)
	if err != nil {
		return nil, err
	}
	if window < 1 || window > MaxValues || len(xs)*window > MaxRollingWork {
		return nil, errors.New("window out of range or len(values)*window exceeds 10,000,000")
	}
	if fn == "std" && window < 2 {
		return nil, errors.New("rolling std requires window >= 2")
	}
	agg := map[string]func([]float64) float64{
		"mean":   func(w []float64) float64 { return stat.Mean(w, nil) },
		"sum":    floats.Sum,
		"std":    func(w []float64) float64 { return math.Sqrt(stat.Variance(w, nil)) },
		"min":    func(w []float64) float64 { return slices.Min(w) },
		"max":    func(w []float64) float64 { return slices.Max(w) },
		"median": func(w []float64) float64 { s := slices.Clone(w); slices.Sort(s); return quantile(s, 0.5) },
	}[fn]
	if agg == nil {
		return nil, errors.New("fn must be mean, sum, std, min, max or median")
	}
	out := make([]float64, len(xs))
	for i := range xs {
		if i&255 == 255 {
			if err := checkpoint(t); err != nil {
				return nil, err
			}
		}
		if i+1 < window {
			out[i] = math.NaN()
			continue
		}
		out[i] = agg(xs[i+1-window : i+1])
	}
	return optionalFloats(out)
}

func ewmaCall(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var value starlark.Value
	var alpha num
	if err := unpack(b, args, kwargs, "values", &value, "alpha", &alpha); err != nil {
		return nil, err
	}
	if !(alpha > 0 && alpha <= 1) {
		return nil, errors.New("alpha must be in (0, 1]")
	}
	xs, err := floatsOf(t, value, 0, MaxValues)
	if err != nil {
		return nil, err
	}
	a := float64(alpha)
	for i := 1; i < len(xs); i++ {
		xs[i] = a*xs[i] + (1-a)*xs[i-1]
	}
	return optionalFloats(xs)
}
