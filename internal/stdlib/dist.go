package stdlib

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"

	"go.starlark.net/starlark"
	"gonum.org/v1/gonum/stat/distuv"
)

type distribution interface {
	CDF(float64) float64
	Survival(float64) float64
	Prob(float64) float64
}

type quantiler interface{ Quantile(float64) float64 }

type param struct {
	name     string
	def      float64
	required bool
	check    func(float64) bool
}

type distSpec struct {
	params   []param
	discrete bool
	build    func(p map[string]float64) distribution
}

func positive(f float64) bool    { return f > 0 }
func probability(f float64) bool { return f >= 0 && f <= 1 }
func anyFinite(float64) bool     { return true }
func trials(f float64) bool      { return f >= 1 && f <= 1e9 && f == math.Trunc(f) }

var distributions = map[string]distSpec{
	"normal": {params: []param{{"mean", 0, false, anyFinite}, {"stddev", 1, false, positive}}, build: func(p map[string]float64) distribution {
		return distuv.Normal{Mu: p["mean"], Sigma: p["stddev"]}
	}},
	"t": {params: []param{{"df", 0, true, positive}}, build: func(p map[string]float64) distribution {
		return distuv.StudentsT{Mu: 0, Sigma: 1, Nu: p["df"]}
	}},
	"chi2": {params: []param{{"df", 0, true, positive}}, build: func(p map[string]float64) distribution {
		return distuv.ChiSquared{K: p["df"]}
	}},
	"f": {params: []param{{"df1", 0, true, positive}, {"df2", 0, true, positive}}, build: func(p map[string]float64) distribution {
		return distuv.F{D1: p["df1"], D2: p["df2"]}
	}},
	"lognormal": {params: []param{{"mu", 0, false, anyFinite}, {"sigma", 1, false, positive}}, build: func(p map[string]float64) distribution {
		return distuv.LogNormal{Mu: p["mu"], Sigma: p["sigma"]}
	}},
	"exponential": {params: []param{{"rate", 1, false, positive}}, build: func(p map[string]float64) distribution {
		return distuv.Exponential{Rate: p["rate"]}
	}},
	"uniform": {params: []param{{"low", 0, false, anyFinite}, {"high", 1, false, anyFinite}}, build: func(p map[string]float64) distribution {
		return distuv.Uniform{Min: p["low"], Max: p["high"]}
	}},
	"beta": {params: []param{{"alpha", 0, true, positive}, {"beta", 0, true, positive}}, build: func(p map[string]float64) distribution {
		return distuv.Beta{Alpha: p["alpha"], Beta: p["beta"]}
	}},
	"gamma": {params: []param{{"shape", 0, true, positive}, {"rate", 1, false, positive}}, build: func(p map[string]float64) distribution {
		return distuv.Gamma{Alpha: p["shape"], Beta: p["rate"]}
	}},
	"binomial": {discrete: true, params: []param{{"n", 0, true, trials}, {"p", 0, true, probability}}, build: func(p map[string]float64) distribution {
		return distuv.Binomial{N: p["n"], P: p["p"]}
	}},
	"poisson": {discrete: true, params: []param{{"rate", 0, true, positive}}, build: func(p map[string]float64) distribution {
		return distuv.Poisson{Lambda: p["rate"]}
	}},
}

func distributionNames() string {
	var out []string
	for name, spec := range distributions {
		var ps []string
		for _, p := range spec.params {
			if p.required {
				ps = append(ps, p.name)
			} else {
				ps = append(ps, fmt.Sprintf("%s=%g", p.name, p.def))
			}
		}
		out = append(out, name+"("+strings.Join(ps, ", ")+")")
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

func distCall(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	// The first two arguments may also be passed by keyword; everything else
	// is a distribution parameter.
	argNames := []string{"distribution", "x"}
	if b.Name() == "dist.ppf" {
		argNames[1] = "p"
	}
	if len(args) > 2 {
		return nil, errors.New("expected distribution name, value, and keyword parameters")
	}
	slots := make([]starlark.Value, 2)
	copy(slots, args)
	var paramArgs []starlark.Tuple
	for _, kv := range kwargs {
		k := string(kv[0].(starlark.String))
		i := slices.Index(argNames, k)
		if i < 0 {
			paramArgs = append(paramArgs, kv)
			continue
		}
		if slots[i] != nil {
			return nil, errors.New("duplicate argument " + k)
		}
		slots[i] = kv[1]
	}
	if slots[0] == nil || slots[1] == nil {
		return nil, errors.New("expected distribution name, value, and keyword parameters")
	}
	name, ok := starlark.AsString(slots[0])
	spec, known := distributions[name]
	if !ok || !known {
		return nil, errors.New("unknown distribution")
	}
	x, err := number(slots[1])
	if err != nil {
		return nil, err
	}
	values := map[string]float64{}
	for _, kv := range paramArgs {
		k := string(kv[0].(starlark.String))
		if _, dup := values[k]; dup {
			return nil, errors.New("duplicate parameter")
		}
		f, err := number(kv[1])
		if err != nil {
			return nil, fmt.Errorf("parameter %s: %w", k, err)
		}
		values[k] = f
	}
	params := map[string]float64{}
	for _, p := range spec.params {
		v, found := values[p.name]
		if !found {
			if p.required {
				return nil, fmt.Errorf("missing parameter %s", p.name)
			}
			v = p.def
		}
		if !p.check(v) {
			return nil, fmt.Errorf("invalid parameter %s", p.name)
		}
		params[p.name] = v
		delete(values, p.name)
	}
	if len(values) > 0 {
		return nil, errors.New("unknown parameter for this distribution")
	}
	if name == "uniform" && params["low"] >= params["high"] {
		return nil, errors.New("uniform requires low < high")
	}
	d := spec.build(params)
	switch b.Name() {
	case "dist.cdf":
		return floatResult(d.CDF(x))
	case "dist.sf":
		return floatResult(d.Survival(x))
	case "dist.pdf":
		if spec.discrete && x != math.Trunc(x) {
			return starlark.Float(0), nil
		}
		return floatResult(d.Prob(x))
	}
	if !probability(x) {
		return nil, errors.New("p must be between 0 and 1")
	}
	q, ok := d.(quantiler)
	if !ok {
		return nil, errors.New("ppf is only available for continuous distributions")
	}
	return floatResult(q.Quantile(x))
}
