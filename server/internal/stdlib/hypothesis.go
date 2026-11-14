package stdlib

import (
	"errors"
	"math"
	"slices"

	"go.starlark.net/starlark"
	"gonum.org/v1/gonum/stat"
	"gonum.org/v1/gonum/stat/distuv"
)

const (
	MaxTableDim     = 64
	MaxFisherTotal  = 1000000
	MaxANOVAGroups  = 256
	fisherTolerance = 1 + 1e-7
)

func dict(pairs ...any) *starlark.Dict {
	out := starlark.NewDict(len(pairs) / 2)
	for i := 0; i < len(pairs); i += 2 {
		var v starlark.Value
		switch x := pairs[i+1].(type) {
		case float64:
			v = starlark.Float(x)
		case int:
			v = starlark.MakeInt(x)
		case bool:
			v = starlark.Bool(x)
		case starlark.Value:
			v = x
		}
		_ = out.SetKey(starlark.String(pairs[i].(string)), v)
	}
	return out
}

func alternativeOf(s string) error {
	switch s {
	case "two-sided", "less", "greater":
		return nil
	}
	return errors.New(`alternative must be "two-sided", "less" or "greater"`)
}

// tailP converts a statistic with a symmetric reference distribution into a
// p-value for the requested alternative.
func tailP(d interface {
	CDF(float64) float64
	Survival(float64) float64
}, statistic float64, alternative string) float64 {
	switch alternative {
	case "less":
		return d.CDF(statistic)
	case "greater":
		return d.Survival(statistic)
	}
	return math.Min(1, 2*d.Survival(math.Abs(statistic)))
}

func testResult(statistic, p float64, extra ...any) (starlark.Value, error) {
	if err := finite(statistic, p); err != nil {
		return nil, err
	}
	return dict(append([]any{"statistic", statistic, "p_value", p}, extra...)...), nil
}

func levelOf(v num) (float64, error) {
	if !(v > 0 && v < 1) {
		return 0, errors.New("level must be between 0 and 1 (exclusive)")
	}
	return float64(v), nil
}

func ttestCall(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var xv, yv starlark.Value
	var mu num
	alternative, equalVar := "two-sided", false
	var err error
	switch b.Name() {
	case "stats.ttest_1samp":
		err = unpack(b, args, kwargs, "values", &xv, "mean?", &mu, "alternative?", &alternative)
	case "stats.ttest_ind":
		err = unpack(b, args, kwargs, "x", &xv, "y", &yv, "equal_var?", &equalVar, "alternative?", &alternative)
	default:
		err = unpack(b, args, kwargs, "x", &xv, "y", &yv, "alternative?", &alternative)
	}
	if err != nil {
		return nil, err
	}
	if err := alternativeOf(alternative); err != nil {
		return nil, err
	}
	xs, err := floatsOf(t, xv, 2, MaxValues)
	if err != nil {
		return nil, err
	}
	var ys []float64
	if yv != nil {
		if ys, err = floatsOf(t, yv, 2, MaxValues); err != nil {
			return nil, err
		}
	}
	if b.Name() == "stats.ttest_rel" {
		if len(xs) != len(ys) {
			return nil, errors.New("x and y must have the same length")
		}
		diff := make([]float64, len(xs))
		for i := range xs {
			diff[i] = xs[i] - ys[i]
		}
		xs, ys = diff, nil
	}
	var statistic, df float64
	if ys == nil {
		n := float64(len(xs))
		statistic = (stat.Mean(xs, nil) - float64(mu)) / math.Sqrt(stat.Variance(xs, nil)/n)
		df = n - 1
	} else {
		nx, ny := float64(len(xs)), float64(len(ys))
		vx, vy := stat.Variance(xs, nil), stat.Variance(ys, nil)
		diff := stat.Mean(xs, nil) - stat.Mean(ys, nil)
		if equalVar {
			df = nx + ny - 2
			pooled := ((nx-1)*vx + (ny-1)*vy) / df
			statistic = diff / math.Sqrt(pooled*(1/nx+1/ny))
		} else {
			ax, ay := vx/nx, vy/ny
			statistic = diff / math.Sqrt(ax+ay)
			df = (ax + ay) * (ax + ay) / (ax*ax/(nx-1) + ay*ay/(ny-1))
		}
	}
	if err := finite(statistic, df); err != nil {
		return nil, err
	}
	return testResult(statistic, tailP(distuv.StudentsT{Mu: 0, Sigma: 1, Nu: df}, statistic, alternative), "df", df)
}

// ranks assigns 1-based average ranks and returns the tie correction term
// sum(t^3 - t) over tie groups.
func ranks(xs []float64) ([]float64, float64) {
	idx := make([]int, len(xs))
	for i := range idx {
		idx[i] = i
	}
	slices.SortStableFunc(idx, func(a, b int) int {
		switch {
		case xs[a] < xs[b]:
			return -1
		case xs[a] > xs[b]:
			return 1
		}
		return 0
	})
	out := make([]float64, len(xs))
	ties := 0.0
	for i := 0; i < len(idx); {
		j := i
		for j+1 < len(idx) && xs[idx[j+1]] == xs[idx[i]] {
			j++
		}
		rank := float64(i+j)/2 + 1
		for k := i; k <= j; k++ {
			out[idx[k]] = rank
		}
		n := float64(j - i + 1)
		ties += n*n*n - n
		i = j + 1
	}
	return out, ties
}

func mannWhitney(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var xv, yv starlark.Value
	alternative := "two-sided"
	if err := unpack(b, args, kwargs, "x", &xv, "y", &yv, "alternative?", &alternative); err != nil {
		return nil, err
	}
	if err := alternativeOf(alternative); err != nil {
		return nil, err
	}
	xs, err := floatsOf(t, xv, 1, MaxValues)
	if err != nil {
		return nil, err
	}
	ys, err := floatsOf(t, yv, 1, MaxValues)
	if err != nil {
		return nil, err
	}
	if len(xs)+len(ys) > MaxValues {
		return nil, errors.New("collection exceeds item limit")
	}
	r, ties := ranks(append(append([]float64(nil), xs...), ys...))
	nx, ny := float64(len(xs)), float64(len(ys))
	n := nx + ny
	r1 := 0.0
	for _, v := range r[:len(xs)] {
		r1 += v
	}
	u1 := r1 - nx*(nx+1)/2
	u2 := nx*ny - u1
	mu := nx * ny / 2
	sigma := math.Sqrt(nx * ny / 12 * ((n + 1) - ties/(n*(n-1))))
	u := u1
	switch alternative {
	case "two-sided":
		u = math.Max(u1, u2)
	case "less":
		u = u2
	}
	p := distuv.UnitNormal.Survival((u - mu - 0.5) / sigma)
	if alternative == "two-sided" {
		p = math.Min(1, 2*p)
	}
	return testResult(u1, p)
}

func tableOf(t *starlark.Thread, v starlark.Value) ([][]float64, error) {
	rows, err := sequence(v, MaxTableDim)
	if err != nil {
		return nil, err
	}
	out := make([][]float64, rows.Len())
	for i := range out {
		if out[i], err = floatsOf(t, rows.Index(i), 2, MaxTableDim); err != nil {
			return nil, err
		}
		if len(out[i]) != len(out[0]) {
			return nil, errors.New("table rows must have the same length")
		}
		for _, c := range out[i] {
			if c < 0 {
				return nil, errors.New("table counts must be non-negative")
			}
		}
	}
	if len(out) < 2 {
		return nil, errors.New("table needs at least 2 rows and 2 columns")
	}
	return out, nil
}

func chi2Contingency(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var tv starlark.Value
	correction := true
	if err := unpack(b, args, kwargs, "table", &tv, "correction?", &correction); err != nil {
		return nil, err
	}
	obs, err := tableOf(t, tv)
	if err != nil {
		return nil, err
	}
	r, c := len(obs), len(obs[0])
	rowSum, colSum, total := make([]float64, r), make([]float64, c), 0.0
	for i := range obs {
		for j, v := range obs[i] {
			rowSum[i] += v
			colSum[j] += v
			total += v
		}
	}
	df := (r - 1) * (c - 1)
	statistic := 0.0
	expected := make([]starlark.Value, r)
	for i := range obs {
		row := make([]starlark.Value, c)
		for j, o := range obs[i] {
			e := rowSum[i] * colSum[j] / total
			if !(e > 0) {
				return nil, errors.New("expected frequency is zero; remove empty rows or columns")
			}
			if correction && df == 1 {
				diff := e - o
				o += math.Copysign(math.Min(0.5, math.Abs(diff)), diff)
			}
			statistic += (o - e) * (o - e) / e
			row[j] = starlark.Float(e)
		}
		expected[i] = starlark.NewList(row)
	}
	return testResult(statistic, distuv.ChiSquared{K: float64(df)}.Survival(statistic), "df", df, "expected", starlark.NewList(expected))
}

func logChoose(n, k float64) float64 {
	a, _ := math.Lgamma(n + 1)
	b, _ := math.Lgamma(k + 1)
	c, _ := math.Lgamma(n - k + 1)
	return a - b - c
}

func fisherExact(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var tv starlark.Value
	alternative := "two-sided"
	if err := unpack(b, args, kwargs, "table", &tv, "alternative?", &alternative); err != nil {
		return nil, err
	}
	if err := alternativeOf(alternative); err != nil {
		return nil, err
	}
	tab, err := tableOf(t, tv)
	if err != nil {
		return nil, err
	}
	if len(tab) != 2 || len(tab[0]) != 2 {
		return nil, errors.New("fisher_exact requires a 2x2 table")
	}
	a, bb, c, d := tab[0][0], tab[0][1], tab[1][0], tab[1][1]
	for _, v := range []float64{a, bb, c, d} {
		if v != math.Trunc(v) {
			return nil, errors.New("table counts must be integers")
		}
	}
	m := a + bb + c + d
	if m > MaxFisherTotal {
		return nil, errors.New("table total exceeds 1,000,000")
	}
	row, col := a+bb, a+c
	lo, hi := math.Max(0, row+col-m), math.Min(row, col)
	pmf := func(x float64) float64 {
		return math.Exp(logChoose(row, x) + logChoose(m-row, col-x) - logChoose(m, col))
	}
	observed := pmf(a)
	p := 0.0
	for x, steps := lo, 0; x <= hi; x, steps = x+1, steps+1 {
		if steps&4095 == 4095 {
			if err := checkpoint(t); err != nil {
				return nil, err
			}
		}
		px := pmf(x)
		if alternative == "less" && x <= a || alternative == "greater" && x >= a || alternative == "two-sided" && px <= observed*fisherTolerance {
			p += px
		}
	}
	odds := starlark.Value(starlark.None)
	if bb*c > 0 {
		odds = starlark.Float(a * d / (bb * c))
	}
	return dict("odds_ratio", odds, "p_value", math.Min(1, p)), nil
}

func correlationTest(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var xv, yv starlark.Value
	alternative := "two-sided"
	if err := unpack(b, args, kwargs, "x", &xv, "y", &yv, "alternative?", &alternative); err != nil {
		return nil, err
	}
	if err := alternativeOf(alternative); err != nil {
		return nil, err
	}
	xs, err := floatsOf(t, xv, 3, MaxValues)
	if err != nil {
		return nil, err
	}
	ys, err := floatsOf(t, yv, 3, MaxValues)
	if err != nil {
		return nil, err
	}
	if len(xs) != len(ys) {
		return nil, errors.New("x and y must have the same length")
	}
	if b.Name() == "stats.spearman" {
		xs, _ = ranks(xs)
		ys, _ = ranks(ys)
	}
	r := stat.Correlation(xs, ys, nil)
	if err := finite(r); err != nil {
		return nil, err
	}
	r = math.Max(-1, math.Min(1, r))
	df := float64(len(xs) - 2)
	tstat := math.Copysign(math.Inf(1), r)
	if math.Abs(r) < 1 {
		tstat = r * math.Sqrt(df/(1-r*r))
	}
	return dict("statistic", r, "p_value", tailP(distuv.StudentsT{Mu: 0, Sigma: 1, Nu: df}, tstat, alternative)), nil
}

func anovaOneway(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var gv starlark.Value
	if err := unpack(b, args, kwargs, "groups", &gv); err != nil {
		return nil, err
	}
	groups, err := sequence(gv, MaxANOVAGroups)
	if err != nil {
		return nil, err
	}
	if groups.Len() < 2 {
		return nil, errors.New("need at least 2 groups")
	}
	var all []float64
	var ssw float64
	type group struct{ n, mean float64 }
	gs := make([]group, groups.Len())
	for i := range gs {
		xs, err := floatsOf(t, groups.Index(i), 1, MaxValues)
		if err != nil {
			return nil, err
		}
		if len(all)+len(xs) > MaxValues {
			return nil, errors.New("collection exceeds item limit")
		}
		all = append(all, xs...)
		mean := stat.Mean(xs, nil)
		for _, x := range xs {
			ssw += (x - mean) * (x - mean)
		}
		gs[i] = group{float64(len(xs)), mean}
	}
	grand := stat.Mean(all, nil)
	ssb := 0.0
	for _, g := range gs {
		ssb += g.n * (g.mean - grand) * (g.mean - grand)
	}
	k, n := float64(len(gs)), float64(len(all))
	if n <= k {
		return nil, errors.New("need more observations than groups")
	}
	f := (ssb / (k - 1)) / (ssw / (n - k))
	if err := finite(f); err != nil {
		return nil, err
	}
	return testResult(f, distuv.F{D1: k - 1, D2: n - k}.Survival(f), "df_between", int(k-1), "df_within", int(n-k))
}

func meanCI(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var xv starlark.Value
	level := num(0.95)
	if err := unpack(b, args, kwargs, "values", &xv, "level?", &level); err != nil {
		return nil, err
	}
	lv, err := levelOf(level)
	if err != nil {
		return nil, err
	}
	xs, err := floatsOf(t, xv, 2, MaxValues)
	if err != nil {
		return nil, err
	}
	n := float64(len(xs))
	mean := stat.Mean(xs, nil)
	half := distuv.StudentsT{Mu: 0, Sigma: 1, Nu: n - 1}.Quantile(1-(1-lv)/2) * math.Sqrt(stat.Variance(xs, nil)/n)
	if err := finite(mean, half); err != nil {
		return nil, err
	}
	return dict("estimate", mean, "low", mean-half, "high", mean+half), nil
}

func proportionCI(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var successes, n int
	level := num(0.95)
	if err := unpack(b, args, kwargs, "successes", &successes, "n", &n, "level?", &level); err != nil {
		return nil, err
	}
	lv, err := levelOf(level)
	if err != nil {
		return nil, err
	}
	if n < 1 || successes < 0 || successes > n {
		return nil, errors.New("require 0 <= successes <= n and n >= 1")
	}
	z := distuv.UnitNormal.Quantile(1 - (1-lv)/2)
	nf, p := float64(n), float64(successes)/float64(n)
	denom := 1 + z*z/nf
	center := (p + z*z/(2*nf)) / denom
	half := z * math.Sqrt(p*(1-p)/nf+z*z/(4*nf*nf)) / denom
	return dict("estimate", p, "low", math.Max(0, center-half), "high", math.Min(1, center+half)), nil
}
