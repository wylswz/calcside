package stdlib

import (
	"errors"
	"math"

	"go.starlark.net/starlark"
	"gonum.org/v1/gonum/mat"
	"gonum.org/v1/gonum/stat/distuv"
)

const (
	MaxPredictors   = 64
	logitMaxIter    = 100
	logitTolerance  = 1e-10
	maxRegressCells = 1000000
)

// designOf parses predictor rows (or a flat list as one predictor) into an
// n×p matrix, optionally prefixed with an intercept column.
func designOf(t *starlark.Thread, v starlark.Value, intercept bool) (*mat.Dense, error) {
	rows, err := sequence(v, MaxValues)
	if err != nil {
		return nil, err
	}
	n := rows.Len()
	if n == 0 {
		return nil, errors.New("x must not be empty")
	}
	offset := 0
	if intercept {
		offset = 1
	}
	var data []float64
	p := -1
	for i := 0; i < n; i++ {
		if i&4095 == 4095 {
			if err := checkpoint(t); err != nil {
				return nil, err
			}
		}
		var row []float64
		switch rows.Index(i).(type) {
		case starlark.Int, starlark.Float:
			f, err := number(rows.Index(i))
			if err != nil {
				return nil, err
			}
			row = []float64{f}
		default:
			if row, err = floatsOf(t, rows.Index(i), 1, MaxPredictors); err != nil {
				return nil, err
			}
		}
		if p >= 0 && len(row) != p {
			return nil, errors.New("x rows must have the same length")
		}
		if p < 0 {
			p = len(row)
			if n*(p+offset) > maxRegressCells {
				return nil, errors.New("design matrix exceeds 1,000,000 cells")
			}
			data = make([]float64, 0, n*(p+offset))
		}
		if intercept {
			data = append(data, 1)
		}
		data = append(data, row...)
	}
	return mat.NewDense(n, p+offset, data), nil
}

func regressionArgs(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (*mat.Dense, []float64, bool, error) {
	var xv, yv starlark.Value
	intercept := true
	if err := unpack(b, args, kwargs, "x", &xv, "y", &yv, "intercept?", &intercept); err != nil {
		return nil, nil, false, err
	}
	x, err := designOf(t, xv, intercept)
	if err != nil {
		return nil, nil, false, err
	}
	y, err := floatsOf(t, yv, 1, MaxValues)
	if err != nil {
		return nil, nil, false, err
	}
	n, p := x.Dims()
	if len(y) != n {
		return nil, nil, false, errors.New("x and y must have the same number of rows")
	}
	if n <= p {
		return nil, nil, false, errors.New("need more observations than coefficients")
	}
	return x, y, intercept, nil
}

func floatList(xs []float64) (*starlark.List, error) {
	out := make([]starlark.Value, len(xs))
	for i, f := range xs {
		v, err := floatResult(f)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return starlark.NewList(out), nil
}

// coefficientTable returns the standard errors, test statistics and two-sided
// p-values for coefficients with covariance matrix cov.
func coefficientTable(beta []float64, cov *mat.Dense, ref interface{ Survival(float64) float64 }) (se, z, p []float64) {
	for i, b := range beta {
		s := math.Sqrt(cov.At(i, i))
		se, z = append(se, s), append(z, b/s)
		p = append(p, math.Min(1, 2*ref.Survival(math.Abs(b/s))))
	}
	return se, z, p
}

func regressionResult(beta, se, z, p []float64, statName string, extra ...any) (starlark.Value, error) {
	pairs := []any{}
	for _, col := range []struct {
		name string
		v    []float64
	}{{"coefficients", beta}, {"std_errors", se}, {statName, z}, {"p_values", p}} {
		l, err := floatList(col.v)
		if err != nil {
			return nil, err
		}
		pairs = append(pairs, col.name, l)
	}
	for i := 1; i < len(extra); i += 2 {
		if f, ok := extra[i].(float64); ok {
			if err := finite(f); err != nil {
				return nil, err
			}
		}
	}
	return dict(append(pairs, extra...)...), nil
}

func olsCall(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	x, y, intercept, err := regressionArgs(t, b, args, kwargs)
	if err != nil {
		return nil, err
	}
	n, p := x.Dims()
	var beta mat.VecDense
	if beta.SolveVec(x, mat.NewVecDense(n, y)) != nil {
		return nil, errors.New("design matrix is rank-deficient")
	}
	var fitted mat.VecDense
	fitted.MulVec(x, &beta)
	mean := 0.0
	for _, v := range y {
		mean += v
	}
	mean /= float64(n)
	rss, tss := 0.0, 0.0
	for i, v := range y {
		r := v - fitted.AtVec(i)
		rss += r * r
		if intercept {
			tss += (v - mean) * (v - mean)
		} else {
			tss += v * v
		}
	}
	df := float64(n - p)
	var xtx, inv mat.Dense
	xtx.Mul(x.T(), x)
	if inv.Inverse(&xtx) != nil {
		return nil, errors.New("design matrix is rank-deficient")
	}
	inv.Scale(rss/df, &inv)
	coef := beta.RawVector().Data
	se, tv, pv := coefficientTable(coef, &inv, distuv.StudentsT{Mu: 0, Sigma: 1, Nu: df})
	r2 := 1 - rss/tss
	constant := 0.0
	if intercept {
		constant = 1
	}
	adj := 1 - (1-r2)*(float64(n)-constant)/df
	return regressionResult(coef, se, tv, pv, "t_values", "r_squared", r2, "adj_r_squared", adj, "df_resid", n-p, "n", n)
}

func logisticCall(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	x, y, _, err := regressionArgs(t, b, args, kwargs)
	if err != nil {
		return nil, err
	}
	for _, v := range y {
		if v != 0 && v != 1 {
			return nil, errors.New("y must contain only 0 and 1")
		}
	}
	n, p := x.Dims()
	beta := make([]float64, p)
	var hinv mat.Dense
	mu := make([]float64, n)
	for iter := 1; iter <= logitMaxIter; iter++ {
		if err := checkpoint(t); err != nil {
			return nil, err
		}
		grad := mat.NewVecDense(p, nil)
		hess := mat.NewDense(p, p, nil)
		for i := 0; i < n; i++ {
			if i&4095 == 4095 {
				if err := checkpoint(t); err != nil {
					return nil, err
				}
			}
			row := x.RawRowView(i)
			eta := 0.0
			for j, xj := range row {
				eta += xj * beta[j]
			}
			mu[i] = 1 / (1 + math.Exp(-eta))
			w := mu[i] * (1 - mu[i])
			for j, xj := range row {
				grad.SetVec(j, grad.AtVec(j)+xj*(y[i]-mu[i]))
				for k := j; k < p; k++ {
					hess.Set(j, k, hess.At(j, k)+w*xj*row[k])
				}
			}
		}
		for j := 0; j < p; j++ {
			for k := 0; k < j; k++ {
				hess.Set(j, k, hess.At(k, j))
			}
		}
		if hinv.Inverse(hess) != nil {
			return nil, errors.New("information matrix is singular (perfect separation or collinear predictors)")
		}
		var delta mat.VecDense
		delta.MulVec(&hinv, grad)
		step := 0.0
		for j := range beta {
			beta[j] += delta.AtVec(j)
			step = math.Max(step, math.Abs(delta.AtVec(j)))
		}
		if err := finite(beta...); err != nil {
			return nil, errors.New("logistic regression diverged (perfect separation?)")
		}
		if step < logitTolerance*math.Max(1, maxAbs(beta)) {
			ll := 0.0
			for i := 0; i < n; i++ {
				row := x.RawRowView(i)
				eta := 0.0
				for j, xj := range row {
					eta += xj * beta[j]
				}
				// log(1+exp(eta)) computed stably.
				soft := math.Max(eta, 0) + math.Log1p(math.Exp(-math.Abs(eta)))
				ll += y[i]*eta - soft
			}
			se, z, pv := coefficientTable(beta, &hinv, distuv.UnitNormal)
			return regressionResult(beta, se, z, pv, "z_values", "log_likelihood", ll, "iterations", iter, "n", n)
		}
	}
	return nil, errors.New("logistic regression did not converge (perfect separation?)")
}

func maxAbs(xs []float64) float64 {
	m := 0.0
	for _, x := range xs {
		m = math.Max(m, math.Abs(x))
	}
	return m
}
