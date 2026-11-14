package stdlib

import (
	"errors"
	"math"
	"slices"

	"go.starlark.net/starlark"
	"gonum.org/v1/gonum/floats"
	"gonum.org/v1/gonum/stat"
)

const MaxValues = 100000

func number(v starlark.Value) (float64, error) {
	switch v.(type) {
	case starlark.Int, starlark.Float:
		f, _ := starlark.AsFloat(v)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, errors.New("values must be finite numbers")
		}
		return f, nil
	default:
		return 0, errors.New("values must be int or float; convert strings explicitly")
	}
}

// num unpacks a finite int or float argument (starlark's *float64 rejects ints).
type num float64

func (n *num) Unpack(v starlark.Value) error {
	f, err := number(v)
	*n = num(f)
	return err
}

func floatsOf(t *starlark.Thread, v starlark.Value, minLen, maxLen int) ([]float64, error) {
	seq, err := sequence(v, maxLen)
	if err != nil {
		return nil, err
	}
	if seq.Len() < minLen {
		return nil, errors.New("not enough values")
	}
	out := make([]float64, seq.Len())
	for i := range out {
		if i&4095 == 4095 {
			if err := checkpoint(t); err != nil {
				return nil, err
			}
		}
		if out[i], err = number(seq.Index(i)); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func finite(values ...float64) error {
	for _, f := range values {
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return errors.New("result is undefined or not finite for this input")
		}
	}
	return nil
}

// floatResult rejects NaN/Inf (not JSON-encodable) and normalizes -0.
func floatResult(f float64) (starlark.Value, error) {
	if err := finite(f); err != nil {
		return nil, err
	}
	if f == 0 {
		f = 0
	}
	return starlark.Float(f), nil
}

// quantile uses linear interpolation between closest ranks (NumPy's default
// "linear" method, R type 7) over sorted data.
func quantile(sorted []float64, q float64) float64 {
	h := float64(len(sorted)-1) * q
	lo := int(math.Floor(h))
	if lo >= len(sorted)-1 {
		return sorted[len(sorted)-1]
	}
	return sorted[lo] + (h-float64(lo))*(sorted[lo+1]-sorted[lo])
}

func statsUnary(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var value starlark.Value
	sample := true
	var err error
	switch b.Name() {
	case "stats.variance", "stats.stddev":
		err = unpack(b, args, kwargs, "values", &value, "sample?", &sample)
	default:
		err = unpack(b, args, kwargs, "values", &value)
	}
	if err != nil {
		return nil, err
	}
	minLen := 1
	if sample && (b.Name() == "stats.variance" || b.Name() == "stats.stddev") {
		minLen = 2
	}
	xs, err := floatsOf(t, value, minLen, MaxValues)
	if err != nil {
		return nil, err
	}
	switch b.Name() {
	case "stats.sum":
		return floatResult(floats.Sum(xs))
	case "stats.mean":
		return floatResult(stat.Mean(xs, nil))
	case "stats.variance", "stats.stddev":
		v := stat.PopVariance(xs, nil)
		if sample {
			v = stat.Variance(xs, nil)
		}
		if b.Name() == "stats.stddev" {
			v = math.Sqrt(v)
		}
		return floatResult(v)
	case "stats.median":
		slices.Sort(xs)
		return floatResult(quantile(xs, 0.5))
	}
	slices.Sort(xs)
	out := starlark.NewDict(8)
	_ = out.SetKey(starlark.String("count"), starlark.MakeInt(len(xs)))
	stddev := starlark.Value(starlark.None)
	if len(xs) > 1 {
		if stddev, err = floatResult(math.Sqrt(stat.Variance(xs, nil))); err != nil {
			return nil, err
		}
	}
	mean, err := floatResult(stat.Mean(xs, nil))
	if err != nil {
		return nil, err
	}
	_ = out.SetKey(starlark.String("mean"), mean)
	_ = out.SetKey(starlark.String("stddev"), stddev)
	for _, p := range []struct {
		name string
		q    float64
	}{{"min", 0}, {"q25", 0.25}, {"median", 0.5}, {"q75", 0.75}, {"max", 1}} {
		v, err := floatResult(quantile(xs, p.q))
		if err != nil {
			return nil, err
		}
		_ = out.SetKey(starlark.String(p.name), v)
	}
	return out, nil
}

func statsQuantile(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var value, qv starlark.Value
	if err := unpack(b, args, kwargs, "values", &value, "q", &qv); err != nil {
		return nil, err
	}
	q, err := number(qv)
	if err != nil || q < 0 || q > 1 {
		return nil, errors.New("q must be between 0 and 1")
	}
	xs, err := floatsOf(t, value, 1, MaxValues)
	if err != nil {
		return nil, err
	}
	slices.Sort(xs)
	return floatResult(quantile(xs, q))
}

func statsPair(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var xv, yv starlark.Value
	if err := unpack(b, args, kwargs, "x", &xv, "y", &yv); err != nil {
		return nil, err
	}
	xs, err := floatsOf(t, xv, 2, MaxValues)
	if err != nil {
		return nil, err
	}
	ys, err := floatsOf(t, yv, 2, MaxValues)
	if err != nil {
		return nil, err
	}
	if len(xs) != len(ys) {
		return nil, errors.New("x and y must have the same length")
	}
	switch b.Name() {
	case "stats.covariance":
		return floatResult(stat.Covariance(xs, ys, nil))
	case "stats.correlation":
		return floatResult(stat.Correlation(xs, ys, nil))
	}
	intercept, slope := stat.LinearRegression(xs, ys, nil, false)
	r2 := stat.RSquared(xs, ys, nil, intercept, slope)
	if err := finite(intercept, slope, r2); err != nil {
		return nil, err
	}
	out := starlark.NewDict(3)
	_ = out.SetKey(starlark.String("slope"), starlark.Float(slope))
	_ = out.SetKey(starlark.String("intercept"), starlark.Float(intercept))
	_ = out.SetKey(starlark.String("r_squared"), starlark.Float(r2))
	return out, nil
}
