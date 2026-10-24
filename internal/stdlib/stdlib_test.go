package stdlib

import (
	"context"
	"math"
	"strconv"
	"strings"
	"testing"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"calcside/internal/capability"
)

func TestUtilities(t *testing.T) {
	for _, tc := range []struct{ code, want string }{
		{`url.parse("https://example.com:8443/a%20b?q=x#f")["path"]`, `"/a b"`},
		{`url.parse("https://[::1]:8443/")["hostname"]`, `"::1"`},
		{`url.resolve("https://a.example/a/b", "//b.example/c")`, `"https://b.example/c"`},
		{`url.query_encode({"b": ["2", "1"], "a": "a b"})`, `"a=a+b&b=2&b=1"`},
		{`url.query_decode("x=1&x=2&empty=")["x"]`, `["1", "2"]`},
		{`url.path_unescape(url.path_escape("中文/a b+"))`, `"中文/a b+"`},
		{`url.path_escape("a/b c+")`, `"a%2Fb%20c+"`},
		{`csv.parse("x,y\r\n\"a,b\",\"line1\r\nline2\"\r\n")`, `[["x", "y"], ["a,b", "line1\nline2"]]`},
		{`csv.parse_dicts("\ufeffid,amount\nA,0012\n")`, `[{"id": "A", "amount": "0012"}]`},
		{`csv.parse("")`, `[]`},
		{`csv.parse("\n" * 12000)`, `[]`},
		{`csv.parse("a;b\nx;y\n", delimiter=";")`, `[["a", "b"], ["x", "y"]]`},
		{`csv.format([["a,b", "line1\nline2"]])`, `"\"a,b\",\"line1\nline2\"\n"`},
		{`csv.format_dicts([{"b": "2", "a": "1"}], columns=["a", "b"])`, `"a,b\n1,2\n"`},
		{`csv.parse(csv.format([[""]]))`, `[[""]]`},
		{`csv.format([["=1+1", "ok"]], spreadsheet_safe=True)`, `"'=1+1,ok\n"`},
		{`csv.format([["  =1+1"]], spreadsheet_safe=True)`, `"'  =1+1\n"`},
		{`base64.encode("hello")`, `"aGVsbG8="`},
		{`base64.encode(base64.decode("/w=="))`, `"/w=="`},
		{`type(base64.decode("AA=="))`, `"bytes"`},
		{`base64.encode(base64.decode("_w", url_safe=True, padding=False), url_safe=True, padding=False)`, `"_w"`},
		{`hashlib.sha256("abc")`, `"ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"`},
		{`regex.search("(a)(b)?", "中a")["start"]`, `3`},
		{`regex.search("(a)(b)?", "a")["groups"]`, `["a", None]`},
		{`regex.search("z", "abc")`, `None`},
		{`regex.find_all("[0-9]+", "a12b34")`, `["12", "34"]`},
		{`regex.replace("a", "aba", "$1")`, `"$1b$1"`},
		{`regex.split("[,;]", "a,b;c")`, `["a", "b", "c"]`},
		{`datetime.parse_rfc3339("1970-01-01T01:00:00+01:00")`, `0`},
		{`datetime.parse_rfc3339("1970-01-01T00:00:00.123456Z")`, `123`},
		{`datetime.format_rfc3339(123)`, `"1970-01-01T00:00:00.123Z"`},
		{`datetime.format_date(datetime.parse_date("2024-02-29"))`, `"2024-02-29"`},
		{`datetime.format_date(-1)`, `"1969-12-31"`},
		{`stats.sum([1, 2, 3.5])`, `6.5`},
		{`stats.mean((1, 2, 3, 4))`, `2.5`},
		{`stats.variance([2, 4, 4, 4, 5, 5, 7, 9], sample=False)`, `4.0`},
		{`stats.stddev([2, 4, 4, 4, 5, 5, 7, 9], sample=False)`, `2.0`},
		{`stats.variance([1, 2, 3, 4])`, `1.6666666666666667`},
		{`stats.median([3, 1, 4, 2])`, `2.5`},
		{`stats.quantile([5, 1, 4, 2, 3], 0.25)`, `2.0`},
		{`stats.quantile([1, 2], 1)`, `2.0`},
		{`stats.describe([5])`, `{"count": 1, "mean": 5.0, "stddev": None, "min": 5.0, "q25": 5.0, "median": 5.0, "q75": 5.0, "max": 5.0}`},
		{`stats.describe([1, 2, 3, 4, 5])["q75"]`, `4.0`},
		{`stats.covariance([1, 2, 3], [2, 4, 6])`, `2.0`},
		{`int(math.round(stats.correlation([1, 2, 3], [3, 2, 1]) * 1e9))`, `-1000000000`},
		{`stats.linear_regression([0, 1, 2], [1, 3, 5])`, `{"slope": 2.0, "intercept": 1.0, "r_squared": 1.0}`},
		{`linalg.matmul([[1, 2], [3, 4]], [[5], [6]])`, `[[17.0], [39.0]]`},
		{`linalg.solve([[2, 0], [0, 4]], [2, 8])`, `[1.0, 2.0]`},
		{`linalg.solve([[2, 0], [0, 4]], [[2], [8]])`, `[[1.0], [2.0]]`},
		{`[int(math.round(v * 1e9)) for v in linalg.lstsq([[1, 0], [1, 1], [1, 2]], [1, 3, 5])]`, `[1000000000, 2000000000]`},
		{`linalg.inv([[2, 0], [0, 4]])`, `[[0.5, 0.0], [0.0, 0.25]]`},
		{`int(math.round(linalg.det([[1, 2], [3, 4]]) * 1e9))`, `-2000000000`},
		{`dist.cdf(distribution="normal", x=0)`, `0.5`},
		{`dist.cdf("uniform", 0.25, low=0, high=2)`, `0.125`},
		{`dist.pdf("binomial", 1.5, n=3, p=0.5)`, `0.0`},
		{`series.diff([1, 4, 9])`, `[None, 3.0, 5.0]`},
		{`series.diff([])`, `[]`},
		{`series.diff([1, 2], periods=5)`, `[None, None]`},
		{`series.pct_change([100, 125, 0, 5])`, `[None, 0.25, -1.0, None]`},
		{`series.shift([1, 2, 3], periods=-1)`, `[2.0, 3.0, None]`},
		{`series.log_return([2, 2])`, `[None, 0.0]`},
		{`series.cumsum([1, 2, 3])`, `[1.0, 3.0, 6.0]`},
		{`series.cumprod((1, 2, 3))`, `[1.0, 2.0, 6.0]`},
		{`series.drawdown([100, 120, 90, 130])`, `[0.0, 0.0, -0.25, 0.0]`},
		{`series.rolling([1, 2, 3, 4], 2)`, `[None, 1.5, 2.5, 3.5]`},
		{`series.rolling([3, 1, 2], 2, fn="max")`, `[None, 3.0, 2.0]`},
		{`series.rolling([3, 1, 2, 8], 3, fn="median")`, `[None, None, 2.0, 2.0]`},
		{`series.ewma([1, 3], alpha=0.5)`, `[1.0, 2.0]`},
		{`decimal.add("0.1", "0.2")`, `"0.3"`},
		{`decimal.sub("1", "0.01")`, `"0.99"`},
		{`decimal.mul("1.5", "-2.25")`, `"-3.375"`},
		{`decimal.div("1", "3", 4)`, `"0.3333"`},
		{`decimal.div("2.5", 1, 0)`, `"2"`},
		{`decimal.div("2.5", 1, 0, rounding="half_up")`, `"3"`},
		{`decimal.div("-2.5", 1, 0, rounding="floor")`, `"-3"`},
		{`decimal.div("-2.5", 1, 0, rounding="ceiling")`, `"-2"`},
		{`decimal.div("1", "-8", 2, rounding="half_down")`, `"-0.12"`},
		{`decimal.round("1.005", 2, rounding="half_up")`, `"1.01"`},
		{`decimal.round("1.2", 3)`, `"1.200"`},
		{`decimal.round("-0.004", 2)`, `"0.00"`},
		{`decimal.compare("1.0", "1")`, `0`},
		{`decimal.compare("-1", "0.5")`, `-1`},
		{`decimal.sum(["0.10", "0.20", 3])`, `"3.30"`},
		{`decimal.to_minor("12.3", 2)`, `1230`},
		{`decimal.to_minor("-12.340", 2)`, `-1234`},
		{`decimal.from_minor(-5, 2)`, `"-0.05"`},
	} {
		t.Run(tc.code, func(t *testing.T) {
			v, err := starlark.Eval(&starlark.Thread{}, "test", tc.code, Modules())
			if err != nil {
				t.Fatal(err)
			}
			if v.String() != tc.want {
				t.Fatalf("got %s, want %s", v, tc.want)
			}
		})
	}
}

func TestUtilityLimitsAndErrors(t *testing.T) {
	for _, code := range []string{
		`url.parse("https://secret-value@example.com/")`,
		`url.query_decode("secret-value=%xx")`,
		`url.path_unescape("%FF")`,
		`url.query_encode({"x": ["y"] * 1025})`,
		`url.query_encode({"x": "!" * 65536})`,
		`url.query_encode({"x": 1})`,
		`url.parse(base64.decode("YQ=="))`,
		`csv.parse_dicts("a,a\nx,y\n")`,
		`csv.parse_dicts(",a\nx,y\n")`,
		`csv.parse("a,b\nx\n")`,
		`csv.parse("secret-value,\"unclosed")`,
		`csv.parse("," * 256)`,
		`csv.parse("x" * 262145)`,
		`csv.parse("a\n" * 10001)`,
		`csv.parse("a", delimiter="\n")`,
		`csv.parse("a", delimiter="ab")`,
		`csv.format_dicts([{}], columns=["missing"])`,
		`csv.format_dicts([], columns=["x", "x"])`,
		`csv.format([[1]])`,
		`csv.format([["x"], ["x", "y"]])`,
		`csv.format([["\"" * 262144]] * 17)`,
		`base64.decode("/x==")`,
		`base64.decode("YQ==\n")`,
		`base64.decode("secret-value")`,
		`base64.encode("x" * 8388608)`,
		`hashlib.sha256("x" * 8388609)`,
		`regex.search("(?=a)", "a")`,
		`regex.search("(", "secret-value")`,
		`regex.search("(?:abcdef){1000}", "a")`,
		`regex.search("()" * 33, "a")`,
		`regex.find_all("a", "aaa", max_matches=2)`,
		`regex.replace("a", "aa", "x" * 8388608)`,
		`regex.search("a", "x" * 1048577)`,
		`datetime.parse_date("2024-02-30")`,
		`datetime.parse_rfc3339("2024-01-01T00:00:00")`,
		`datetime.parse_rfc3339("2024-01-01T00:00:00,123Z")`,
		`datetime.parse_rfc3339("2024-01-01T00:00:00+01:60")`,
		`datetime.format_date(253402300800000)`,
		`stats.mean([])`,
		`stats.mean(["secret-value"])`,
		`stats.mean([True])`,
		`stats.mean([float("nan")])`,
		`stats.mean([1] * 100001)`,
		`stats.mean({"secret-value": 1})`,
		`stats.variance([1])`,
		`stats.sum([1e308, 1e308])`,
		`stats.quantile([1], 1.5)`,
		`stats.correlation([1, 1, 1], [1, 2, 3])`,
		`stats.covariance([1, 2], [1, 2, 3])`,
		`stats.linear_regression([1, 1], [1, 2])`,
		`linalg.inv([[1, 2], [2, 4]])`,
		`linalg.inv([[1, 2]])`,
		`linalg.solve([[1, 2], [2, 4]], [1, 2])`,
		`linalg.solve([[1, 2, 3], [4, 5, 6]], [1, 2])`,
		`linalg.solve([[1, 0], [0, 1]], [1, 2, 3])`,
		`linalg.solve([[1, 0], [0, 1]], [])`,
		`linalg.lstsq([[1, 1], [1, 1], [1, 1]], [1, 2, 3])`,
		`linalg.matmul([[1, 2]], [[1, 2]])`,
		`linalg.matmul([[1e308, 1e308]], [[10], [10]])`,
		`linalg.det([[1, 2], [3]])`,
		`linalg.det([[1] * 257])`,
		`linalg.det([[1]] * 257)`,
		`linalg.det([])`,
		`linalg.det([1, 2])`,
		`dist.cdf("cauchy", 1)`,
		`dist.cdf("secret-value", 1)`,
		`dist.cdf("normal", "secret-value")`,
		`dist.cdf("normal")`,
		`dist.cdf("normal", 1, 2)`,
		`dist.cdf("normal", 1, x=2)`,
		`dist.cdf("normal", 1, stddev=0)`,
		`dist.cdf("normal", 1, foo=1)`,
		`dist.cdf("t", 1)`,
		`dist.cdf("uniform", 0.5, low=1, high=1)`,
		`dist.cdf("binomial", 1, n=2.5, p=0.5)`,
		`dist.ppf("normal", 1.5)`,
		`dist.ppf("normal", 1)`,
		`dist.ppf("binomial", 0.5, n=10, p=0.5)`,
		`stats.ttest_1samp([1, 1, 1])`,
		`stats.ttest_1samp([1, 2, 3], alternative="secret-value")`,
		`stats.ttest_rel([1, 2, 3], [1, 2])`,
		`stats.mann_whitney([], [1])`,
		`stats.pearson([1, 2], [1, 2])`,
		`stats.spearman([1, 1, 1], [1, 2, 3])`,
		`stats.chi2_contingency([[1, 2]])`,
		`stats.chi2_contingency([[0, 0], [1, 2]])`,
		`stats.chi2_contingency([[-1, 2], [1, 2]])`,
		`stats.chi2_contingency([[1, 2], [1]])`,
		`stats.fisher_exact([[1, 2, 3], [1, 2, 3]])`,
		`stats.fisher_exact([[1.5, 2], [1, 2]])`,
		`stats.fisher_exact([[600000, 0], [0, 600000]])`,
		`stats.anova_oneway([[1, 2]])`,
		`stats.anova_oneway([[1], [2]])`,
		`stats.anova_oneway([[1, 1], [1, 1]])`,
		`stats.mean_ci([1, 2, 3], level=1)`,
		`stats.proportion_ci(5, 3)`,
		`stats.ols([1, 2], [1, 2])`,
		`stats.ols([[1, 1], [2, 2], [3, 3], [4, 4]], [1, 2, 3, 5])`,
		`stats.ols([[1], [1, 2], [3]], [1, 2, 3])`,
		`stats.ols([[1] * 65] * 100, [1] * 100)`,
		`stats.ols([1, 2, 3], [1, 2])`,
		`stats.logit([1, 2, 3, 4], [0, 0, 1, 1])`,
		`stats.logit([1, 2, 3, 4], [0, 2, 1, 1])`,
		`series.diff([1, 2], periods=0)`,
		`series.log_return([1, -1])`,
		`series.drawdown([0, 1])`,
		`series.rolling([1, 2], 0)`,
		`series.rolling([1, 2], 1, fn="std")`,
		`series.rolling([1, 2], 2, fn="secret-value")`,
		`series.rolling([1] * 100000, 101)`,
		`series.ewma([1], alpha=0)`,
		`series.cumprod([1e200, 1e200])`,
		`decimal.add(0.1, "1")`,
		`decimal.add("1e5", "1")`,
		`decimal.add("secret-value", "1")`,
		`decimal.add(".", "1")`,
		`decimal.add("1.2.3", "1")`,
		`decimal.add("1" * 129, "1")`,
		`decimal.div("1", "0", 2)`,
		`decimal.div("1", "3", 101)`,
		`decimal.round("1", 2, rounding="banker")`,
		`decimal.to_minor("1.234", 2)`,
		`decimal.from_minor(1, -1)`,
		`decimal.from_minor(int("1" * 200), 0)`,
		`decimal.sum(["1", 1.5])`,
	} {
		t.Run(code, func(t *testing.T) {
			_, err := starlark.Eval(&starlark.Thread{}, "test", code, Modules())
			if err == nil {
				t.Fatal("expected error")
			}
			if strings.Contains(err.Error(), "secret-value") {
				t.Fatalf("input leaked in error: %v", err)
			}
		})
	}
}

// TestStatisticalReferences compares against SciPy 1.16 / statsmodels 0.14.
func TestStatisticalReferences(t *testing.T) {
	const x, y = `[2.1, 3.4, 1.9, 5.6, 4.4, 3.8, 2.7, 4.9]`, `[1.8, 2.9, 2.5, 4.1, 3.6, 3.9, 2.2, 3.0]`
	const ox, oy = `[[1, 2], [2, 1], [3, 4], [4, 3], [5, 6], [6, 4], [7, 8]]`, `[3.1, 3.9, 7.2, 7.8, 11.1, 10.2, 15.3]`
	const lx, ly = `[0.5, 1.0, 1.5, 2.0, 2.5, 3.0, 3.5, 4.0, 4.5, 5.0]`, `[0, 0, 1, 0, 0, 1, 1, 0, 1, 1]`
	for _, tc := range []struct {
		code string
		want map[string][]float64
	}{
		{`stats.ttest_1samp(` + x + `, mean=3)`, map[string][]float64{"statistic": {1.277132710866435}, "p_value": {0.24228308628118986}, "df": {7}}},
		{`stats.ttest_1samp(` + x + `, mean=3, alternative="less")`, map[string][]float64{"p_value": {0.8788584568594051}}},
		{`stats.ttest_ind(` + x + `, ` + y + `)`, map[string][]float64{"statistic": {1.0864289525102226}, "p_value": {0.2992282251505803}, "df": {11.665856222293312}}},
		{`stats.ttest_ind(` + x + `, ` + y + `, equal_var=True)`, map[string][]float64{"p_value": {0.2956417169095876}, "df": {14}}},
		{`stats.ttest_rel(` + x + `, ` + y + `)`, map[string][]float64{"statistic": {2.098034014235906}, "p_value": {0.07408594944866745}}},
		{`stats.mann_whitney(` + x + `, ` + y + `)`, map[string][]float64{"statistic": {40}, "p_value": {0.43089704373626736}}},
		{`stats.mann_whitney(` + x + `, ` + y + `, alternative="greater")`, map[string][]float64{"p_value": {0.21544852186813368}}},
		{`stats.pearson(` + x + `, ` + y + `)`, map[string][]float64{"statistic": {0.8182759418292694}, "p_value": {0.013032488880075972}}},
		{`stats.spearman(` + x + `, ` + y + `)`, map[string][]float64{"statistic": {0.8333333333333335}, "p_value": {0.010175540123456757}}},
		{`stats.chi2_contingency([[10, 20], [30, 25]])`, map[string][]float64{"statistic": {2.706155303030302}, "p_value": {0.0999616438735348}, "df": {1}}},
		{`stats.chi2_contingency([[10, 20, 5], [30, 25, 12]])`, map[string][]float64{"statistic": {3.769722814498934}, "p_value": {0.15185010329461768}, "df": {2}}},
		{`stats.fisher_exact([[8, 2], [1, 5]])`, map[string][]float64{"odds_ratio": {20}, "p_value": {0.034965034965034975}}},
		{`stats.fisher_exact([[8, 2], [1, 5]], alternative="less")`, map[string][]float64{"p_value": {0.9991258741258742}}},
		{`stats.fisher_exact([[8, 2], [1, 5]], alternative="greater")`, map[string][]float64{"p_value": {0.024475524475524483}}},
		{`stats.anova_oneway([[1, 2, 3, 4], [2, 4, 6, 5], [7, 8, 6]])`, map[string][]float64{"statistic": {8.836940836940837}, "p_value": {0.009427440645678995}, "df_between": {2}, "df_within": {8}}},
		{`stats.mean_ci(` + x + `)`, map[string][]float64{"estimate": {3.6}, "low": {2.489093874987242}, "high": {4.710906125012758}}},
		{`stats.proportion_ci(7, 20)`, map[string][]float64{"estimate": {0.35}, "low": {0.18119182410108203}, "high": {0.5671457233147638}}},
		{`stats.ols(` + ox + `, ` + oy + `)`, map[string][]float64{
			"coefficients":  {0.54244306, 1.1057971, 0.85144928},
			"std_errors":    {0.27800208, 0.11478729, 0.10416777},
			"t_values":      {1.95121948, 9.63344539, 8.17382652},
			"p_values":      {0.12278521, 0.00064931, 0.00121986},
			"r_squared":     {0.9960485868264939},
			"adj_r_squared": {0.9940728802397408},
			"df_resid":      {4},
			"n":             {7},
		}},
		{`stats.logit(` + lx + `, ` + ly + `)`, map[string][]float64{
			"coefficients":   {-2.44128795, 0.88774107},
			"std_errors":     {1.79977434, 0.59617233},
			"p_values":       {0.17495885, 0.1364695},
			"log_likelihood": {-5.433365008574438},
		}},
		{`{"v": [dist.cdf("normal", 1.96), dist.cdf("t", 2, df=5), dist.sf("chi2", 3.84, df=1), dist.sf("f", 4, df1=2, df2=10), dist.pdf("binomial", 3, n=10, p=0.5), dist.cdf("binomial", 3, n=10, p=0.5), dist.pdf("poisson", 2, rate=3)]}`,
			map[string][]float64{"v": {0.9750021048517795, 0.9490302605850709, 0.050043521248705085, 0.052922149401344654, 0.1171875, 0.171875, 0.22404180765538775}}},
		{`{"v": [dist.ppf("normal", 0.975), dist.ppf("t", 0.975, df=10), dist.ppf("gamma", 0.5, shape=2, rate=3), dist.cdf("beta", 0.3, alpha=2, beta=5), dist.cdf("lognormal", 1.5, sigma=0.5), dist.sf("exponential", 2, rate=2)]}`,
			map[string][]float64{"v": {1.959963984540054, 2.228138851986274, 0.5594489966722204, 0.579825, 0.7912971266155286, 0.01831563888873418}}},
	} {
		t.Run(tc.code, func(t *testing.T) {
			v, err := starlark.Eval(&starlark.Thread{}, "test", tc.code, Modules())
			if err != nil {
				t.Fatal(err)
			}
			for key, want := range tc.want {
				field, _, _ := v.(*starlark.Dict).Get(starlark.String(key))
				got := []starlark.Value{field}
				if list, ok := field.(*starlark.List); ok {
					got = got[:0]
					for i := 0; i < list.Len(); i++ {
						got = append(got, list.Index(i))
					}
				}
				if len(got) != len(want) {
					t.Fatalf("%s: got %v, want %v", key, field, want)
				}
				for i, w := range want {
					f, ok := starlark.AsFloat(got[i])
					// statsmodels values above are printed to 8 significant digits.
					if !ok || math.Abs(f-w) > 1e-7*math.Max(1, math.Abs(w)) {
						t.Fatalf("%s: got %v, want %v", key, field, want)
					}
				}
			}
		})
	}
}

func TestLinalgMaxDimensions(t *testing.T) {
	code := `
n = 256
a = [[float(i == j) * 2 + (i + j) % 7 * 0.001 for j in range(n)] for i in range(n)]
x = linalg.solve(a, [1] * n)
inv = linalg.inv(a)
p = linalg.matmul(a, inv)
out = (len(x), len(inv), len(p[0]), linalg.det(a) != 0, linalg.lstsq(a, a)[0][0] > 0.99)
`
	globals, err := starlark.ExecFile(&starlark.Thread{}, "test", code, Modules())
	if err != nil {
		t.Fatal(err)
	}
	if got := globals["out"].String(); got != "(256, 256, 256, True, True)" {
		t.Fatalf("got %s", got)
	}
}

func TestUtilityMetadataAndCancellation(t *testing.T) {
	modules := Modules()
	for _, s := range Symbols() {
		module, member, ok := strings.Cut(s.Name, ".")
		if !ok {
			continue
		}
		value := modules[module].(*starlarkstruct.Module).Members[member]
		if value == nil || len(s.Params) == 0 || s.Doc == "" {
			t.Fatalf("incomplete metadata: %+v", s)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	thread := &starlark.Thread{}
	thread.SetLocal(capability.ContextKey, ctx)
	if _, err := starlark.Eval(thread, "test", `csv.parse("a,b\n")`, modules); err == nil {
		t.Fatal("ignored cancellation")
	}
	for _, module := range []string{"datetime", "url", "csv", "base64", "hashlib", "regex", "stats", "linalg", "dist", "series", "decimal"} {
		m := modules[module].(*starlarkstruct.Module)
		for _, name := range []string{"now", "open", "getenv", "sleep", "random"} {
			if m.Members[name] != nil {
				t.Fatalf("unexpected ambient operation: %s.%s", module, name)
			}
		}
	}
}

func FuzzCSV(f *testing.F) {
	for _, s := range []string{"", "a,b\n1,2\n", "\"\"\n", "\"line1\nline2\",x\n", "\"a\"\"b\"\n"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if len(s) > 1<<16 {
			return
		}
		v, err := starlark.Eval(&starlark.Thread{}, "fuzz", "csv.parse("+strconv.Quote(s)+")", Modules())
		if err != nil {
			return
		}
		env := Modules()
		env["rows"] = v
		again, err := starlark.Eval(&starlark.Thread{}, "fuzz", "csv.parse(csv.format(rows))", env)
		if err != nil {
			t.Fatal(err)
		}
		same, err := starlark.Equal(v, again)
		if err != nil || !same {
			t.Fatalf("CSV roundtrip differs: %v", err)
		}
	})
}
