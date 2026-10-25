package stdlib

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	starjson "go.starlark.net/lib/json"
	starmath "go.starlark.net/lib/math"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"calcside/internal/capability"
	"calcside/internal/completion"
)

const MaxBytes = 8 << 20
const MaxRows = 10000
const MaxCells = 100000
const MaxColumns = 256
const MaxFieldBytes = 256 << 10
const MaxURLBytes = 64 << 10
const MaxQueryPairs = 1024
const MaxMatches = 10000

type builtinFunc func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error)
type function struct {
	name   string
	params []string
	doc    string
	call   builtinFunc
}

var functions = []function{
	{"url.parse", []string{"text"}, "Parse a URL into scheme, host, hostname, port, path, raw_query, and fragment. Rejects userinfo; never authorizes or fetches a URL.", urlParse},
	{"url.resolve", []string{"base", "reference"}, "Resolve a URL reference; the result may have a different host. Inputs and output are limited to 64 KiB.", urlResolve},
	{"url.query_encode", []string{"values"}, "Encode a dict of strings or lists of strings. Keys are sorted, repeated values keep their order; at most 1,024 pairs.", urlQueryEncode},
	{"url.query_decode", []string{"text"}, "Decode a query into a dict of lists, preserving repeated values. At most 1,024 pairs.", urlQueryDecode},
	{"url.path_escape", []string{"text"}, "Escape one UTF-8 path segment, not an entire URL.", urlPath},
	{"url.path_unescape", []string{"text"}, "Decode a path segment; invalid escapes or invalid UTF-8 fail.", urlPath},
	{"csv.parse", []string{"text", "delimiter"}, "Parse CSV into string rows. delimiter defaults to comma; UTF-8/BOM, quoted newlines and CRLF are supported. Up to 10,000 records, 100,000 cells, and 256 columns.", csvParse},
	{"csv.parse_dicts", []string{"text", "delimiter"}, "Parse header-based CSV into dicts. Rejects empty/duplicate headers and inconsistent row widths; cells remain strings.", csvParse},
	{"csv.format", []string{"rows", "delimiter", "spreadsheet_safe"}, "Format list/tuple rows of strings with LF endings. delimiter defaults to comma; spreadsheet_safe defaults to False and explicitly prefixes formula-like cells.", csvFormat},
	{"csv.format_dicts", []string{"rows", "columns", "delimiter", "spreadsheet_safe"}, "Format dict rows with required explicit columns. Uses the same CSV bounds and optional spreadsheet_safe behavior as csv.format.", csvFormat},
	{"base64.encode", []string{"data", "url_safe", "padding"}, "Encode UTF-8 text or bytes. url_safe=False, padding=True; output is bounded to 8 MiB.", base64Call},
	{"base64.decode", []string{"text", "url_safe", "padding"}, "Strict canonical Base64 decoding to bytes, not text. url_safe=False, padding=True; decode bytes explicitly before using text utilities.", base64Call},
	{"hashlib.sha256", []string{"data"}, "Lowercase SHA-256 of supplied UTF-8 text or bytes (up to 8 MiB). Does not expand secret references.", sha256Call},
	{"regex.search", []string{"pattern", "text"}, "RE2 search: None or {match, start, end, groups}; offsets are UTF-8 bytes. Text is limited to 1 MiB; patterns to 8 KiB and 32 captures.", regexCall},
	{"regex.find_all", []string{"pattern", "text", "max_matches"}, "RE2 matched strings. max_matches defaults to 10,000; exceeding it is an error, never silent truncation.", regexCall},
	{"regex.replace", []string{"pattern", "text", "replacement", "max_matches"}, "Replace RE2 matches with a literal string (no replacement backreferences). Bounded matches and output.", regexCall},
	{"regex.split", []string{"pattern", "text", "max_matches"}, "Split text using RE2, with at most 10,000 matches and 10,001 output parts.", regexCall},
	{"datetime.parse_rfc3339", []string{"text"}, "Parse an explicit RFC3339 timestamp to Unix milliseconds; sub-millisecond precision is truncated.", datetimeCall},
	{"datetime.format_rfc3339", []string{"timestamp_ms"}, "Format Unix milliseconds as RFC3339 UTC. No current clock or local timezone access.", datetimeCall},
	{"datetime.parse_date", []string{"text"}, "Parse YYYY-MM-DD to Unix milliseconds at UTC midnight.", datetimeCall},
	{"datetime.format_date", []string{"timestamp_ms"}, "Format Unix milliseconds as YYYY-MM-DD in UTC. Supported years are 0001 through 9999.", datetimeCall},
	{"stats.sum", []string{"values"}, "Float sum of a list/tuple of finite ints or floats (convert CSV strings with float() first). At most 100,000 values.", statsUnary},
	{"stats.mean", []string{"values"}, "Arithmetic mean of at least one value.", statsUnary},
	{"stats.variance", []string{"values", "sample"}, "Variance; sample=True (default, n-1 denominator, needs 2+ values), sample=False for population variance.", statsUnary},
	{"stats.stddev", []string{"values", "sample"}, "Standard deviation; sample defaults to True as in stats.variance.", statsUnary},
	{"stats.median", []string{"values"}, "Median; averages the two middle values for even counts.", statsUnary},
	{"stats.quantile", []string{"values", "q"}, "Quantile for q in [0, 1] using linear interpolation (NumPy default method).", statsQuantile},
	{"stats.describe", []string{"values"}, "Dict of count, mean, stddev (sample; None for one value), min, q25, median, q75, max.", statsUnary},
	{"stats.covariance", []string{"x", "y"}, "Sample covariance of two equal-length lists of at least 2 values.", statsPair},
	{"stats.correlation", []string{"x", "y"}, "Pearson correlation of two equal-length lists; constant input fails instead of returning NaN.", statsPair},
	{"stats.linear_regression", []string{"x", "y"}, "Ordinary least squares y = intercept + slope*x; returns {slope, intercept, r_squared}. Constant x or y fails.", statsPair},
	{"stats.ols", []string{"x", "y", "intercept"}, "Multiple OLS. x is rows of up to 64 predictors (or a flat list for one); intercept=True prepends a constant. Returns coefficients, std_errors, t_values, p_values, r_squared, adj_r_squared, df_resid, n.", olsCall},
	{"stats.logit", []string{"x", "y", "intercept"}, "Logistic regression (Newton-Raphson MLE) with y of 0/1; x as in stats.ols. Returns coefficients, std_errors, z_values, p_values, log_likelihood, iterations, n. Perfect separation fails.", logisticCall},
	{"stats.ttest_1samp", []string{"values", "mean", "alternative"}, "One-sample t-test against mean (default 0). alternative is \"two-sided\" (default), \"less\" or \"greater\". Returns {statistic, p_value, df}.", ttestCall},
	{"stats.ttest_ind", []string{"x", "y", "equal_var", "alternative"}, "Two-sample t-test; Welch's test by default, equal_var=True for Student's pooled test. Returns {statistic, p_value, df}.", ttestCall},
	{"stats.ttest_rel", []string{"x", "y", "alternative"}, "Paired t-test on equal-length x and y. Returns {statistic, p_value, df}.", ttestCall},
	{"stats.mann_whitney", []string{"x", "y", "alternative"}, "Mann-Whitney U test (normal approximation with tie and continuity correction). statistic is U for x.", mannWhitney},
	{"stats.pearson", []string{"x", "y", "alternative"}, "Pearson correlation with t-distribution p-value; at least 3 pairs. Returns {statistic, p_value}.", correlationTest},
	{"stats.spearman", []string{"x", "y", "alternative"}, "Spearman rank correlation (average ranks for ties) with t-distribution p-value. Returns {statistic, p_value}.", correlationTest},
	{"stats.chi2_contingency", []string{"table", "correction"}, "Chi-square independence test on a table of non-negative counts (2..64 per axis). correction=True applies Yates for 2x2. Returns {statistic, p_value, df, expected}.", chi2Contingency},
	{"stats.fisher_exact", []string{"table", "alternative"}, "Fisher's exact test on a 2x2 table of integer counts (total at most 1,000,000). Returns {odds_ratio (None if undefined), p_value}.", fisherExact},
	{"stats.anova_oneway", []string{"groups"}, "One-way ANOVA over a list of 2..256 groups of values. Returns {statistic, p_value, df_between, df_within}.", anovaOneway},
	{"stats.mean_ci", []string{"values", "level"}, "t-based confidence interval for the mean; level defaults to 0.95. Returns {estimate, low, high}.", meanCI},
	{"stats.proportion_ci", []string{"successes", "n", "level"}, "Wilson score interval for a binomial proportion; level defaults to 0.95. Returns {estimate, low, high}.", proportionCI},
	{"dist.cdf", []string{"distribution", "x", "**params"}, "P(X <= x), e.g. dist.cdf(\"normal\", 1.96, mean=0, stddev=1). Distributions: " + distributionNames() + ".", distCall},
	{"dist.sf", []string{"distribution", "x", "**params"}, "Survival function P(X > x); more accurate than 1 - cdf in the tail.", distCall},
	{"dist.pdf", []string{"distribution", "x", "**params"}, "Density (continuous) or probability mass (binomial, poisson; 0 for non-integer x).", distCall},
	{"dist.ppf", []string{"distribution", "p", "**params"}, "Quantile (inverse CDF) for p in [0, 1]; continuous distributions only. Infinite results fail.", distCall},
	{"series.diff", []string{"values", "periods"}, "values[i] - values[i-periods]; periods defaults to 1. Undefined leading positions are None, keeping the input length.", seriesCall},
	{"series.pct_change", []string{"values", "periods"}, "values[i] / values[i-periods] - 1; None where undefined or the previous value is 0.", seriesCall},
	{"series.log_return", []string{"values", "periods"}, "log(values[i] / values[i-periods]); values must be positive.", seriesCall},
	{"series.shift", []string{"values", "periods"}, "Shift by periods (negative shifts backwards), filling with None.", seriesCall},
	{"series.cumsum", []string{"values"}, "Cumulative sum.", seriesCall},
	{"series.cumprod", []string{"values"}, "Cumulative product.", seriesCall},
	{"series.drawdown", []string{"values"}, "values[i] / running max - 1 (0 at new highs); values must be positive.", seriesCall},
	{"series.rolling", []string{"values", "window", "fn"}, "Trailing window aggregate: fn is mean (default), sum, std, min, max or median. First window-1 positions are None; len(values)*window is at most 10,000,000.", rollingCall},
	{"series.ewma", []string{"values", "alpha"}, "Exponentially weighted moving average with smoothing factor alpha in (0, 1], seeded with the first value.", ewmaCall},
	{"decimal.add", []string{"a", "b"}, "Exact decimal sum. Operands are decimal strings (\"12.30\", no exponent) or ints, up to 128 characters; floats are rejected. Results are strings.", decimalCall},
	{"decimal.sub", []string{"a", "b"}, "Exact decimal difference a - b.", decimalCall},
	{"decimal.mul", []string{"a", "b"}, "Exact decimal product; the result scale is the sum of operand scales.", decimalCall},
	{"decimal.div", []string{"a", "b", "scale", "rounding"}, "a / b rounded to scale (0..100) fractional digits. rounding: half_even (default), half_up, half_down, down, up, floor, ceiling.", decimalCall},
	{"decimal.round", []string{"a", "scale", "rounding"}, "Round or pad to scale fractional digits; rounding modes as decimal.div.", decimalCall},
	{"decimal.compare", []string{"a", "b"}, "Numeric comparison: -1, 0 or 1 (\"1.0\" equals \"1\").", decimalCall},
	{"decimal.sum", []string{"values"}, "Exact sum of up to 100,000 decimal strings or ints.", decimalCall},
	{"decimal.to_minor", []string{"a", "scale"}, "Convert to integer minor units, e.g. (\"12.34\", 2) -> 1234. Fails if a has more fractional digits than scale.", decimalCall},
	{"decimal.from_minor", []string{"minor", "scale"}, "Convert integer minor units to a decimal string, e.g. (1234, 2) -> \"12.34\".", decimalCall},
	{"linalg.matmul", []string{"a", "b"}, "Matrix product of nested lists (rows of finite numbers). Every axis is limited to 256.", linalgCall},
	{"linalg.solve", []string{"a", "b"}, "Solve a*x = b for square a; b is a vector (flat list, returns a list) or a matrix (returns rows). Singular matrices fail.", linalgCall},
	{"linalg.lstsq", []string{"a", "b"}, "Least-squares solution of a*x = b for any full-rank a (minimum-norm when underdetermined); b as in linalg.solve.", linalgCall},
	{"linalg.inv", []string{"a"}, "Inverse of a square matrix; singular or ill-conditioned matrices fail.", linalgCall},
	{"linalg.det", []string{"a"}, "Determinant of a square matrix.", linalgCall},
}

func Modules() starlark.StringDict {
	out := starlark.StringDict{"json": starjson.Module, "math": starmath.Module, "help": starlark.NewBuiltin("help", help(nil))}
	for _, f := range functions {
		module, member, _ := strings.Cut(f.name, ".")
		if out[module] == nil {
			out[module] = &starlarkstruct.Module{Name: module, Members: starlark.StringDict{}}
		}
		out[module].(*starlarkstruct.Module).Members[member] = starlark.NewBuiltin(f.name, func(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (_ starlark.Value, err error) {
			if err := checkpoint(t); err != nil {
				return nil, err
			}
			defer func() {
				// Numeric libraries panic on invalid shapes; never crash the instance.
				if recover() != nil {
					err = fmt.Errorf("%s: internal numeric failure", b.Name())
				}
			}()
			value, err := f.call(t, b, args, kwargs)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", b.Name(), err)
			}
			if err := checkpoint(t); err != nil {
				return nil, err
			}
			return value, nil
		})
	}
	for _, module := range out {
		module.Freeze()
	}
	return out
}

func Symbols() []completion.Symbol {
	var out []completion.Symbol
	seen := map[string]bool{}
	for _, f := range functions {
		module, _, _ := strings.Cut(f.name, ".")
		if !seen[module] {
			out = append(out, completion.Symbol{Name: module, Kind: "namespace", Detail: moduleDocs[module], Doc: moduleDocs[module]})
			seen[module] = true
		}
		out = append(out, completion.Symbol{Name: f.name, Kind: "function", Params: append([]string(nil), f.params...), Detail: "(" + strings.Join(f.params, ", ") + ")", Doc: f.doc})
	}
	out = append(out, completion.Symbol{Name: "help", Kind: "function", Params: []string{"name"}, Detail: "(name)", Doc: "Documentation: help() lists modules and capabilities; help(\"module\") lists functions; help(\"capability\") or help(\"ext.alias\") lists operations; help(\"module.function\") shows signature and doc."})
	return out
}

func checkpoint(t *starlark.Thread) error { return capability.ThreadContext(t).Err() }

func unpack(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple, pairs ...any) error {
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, pairs...); err != nil {
		return errors.New("invalid arguments")
	}
	return nil
}

func text(s string, limit int) error {
	if len(s) > limit {
		return errors.New("input exceeds byte limit")
	}
	if !utf8.ValidString(s) {
		return errors.New("invalid UTF-8")
	}
	return nil
}

func bytesOf(v starlark.Value) (string, error) {
	switch v := v.(type) {
	case starlark.String:
		s := string(v)
		return s, text(s, MaxBytes)
	case starlark.Bytes:
		if len(v) > MaxBytes {
			return "", errors.New("input exceeds byte limit")
		}
		return string(v), nil
	default:
		return "", errors.New("expected text or bytes")
	}
}

type buffer struct {
	strings.Builder
	limit int
}

func (b *buffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, errors.New("output exceeds byte limit")
	}
	return b.Builder.Write(p)
}
func (b *buffer) add(s string) error {
	if len(s) > b.limit-b.Len() {
		return errors.New("output exceeds byte limit")
	}
	_, err := b.Builder.WriteString(s)
	return err
}

func sequence(v starlark.Value, max int) (starlark.Indexable, error) {
	switch x := v.(type) {
	case *starlark.List:
		if x.Len() <= max {
			return x, nil
		}
	case starlark.Tuple:
		if x.Len() <= max {
			return x, nil
		}
	default:
		return nil, errors.New("expected a list or tuple")
	}
	return nil, errors.New("collection exceeds item limit")
}

func stringsValue(values []string) *starlark.List {
	items := make([]starlark.Value, len(values))
	for i, v := range values {
		items[i] = starlark.String(v)
	}
	return starlark.NewList(items)
}
