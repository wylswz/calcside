# analysis.star — revenue dashboard from an uploaded CSV.
#
# Upload first:
#   PUT /api/v1/instances/{id}/files?path=/work/orders.csv   (body = orders.csv)
# Instance needs {"capabilities":{"fs":{},"net":{"allow_hosts":["cdn.jsdelivr.net"]}}}.
# Exec this file's contents: it reads /work/orders.csv, downloads
# Chart.js + Pico CSS into /work, aggregates revenue by product and
# region, and writes /work/report.html — open it via the artifact
# preview, which inlines the local JS/CSS dependencies.

def fetch(url, dest):
    r = net.get(url)
    if r["status"] != 200:
        fail("fetch %s: HTTP %d" % (url, r["status"]))
    fs.write(dest, r["body"])
    print("%s -> %s (%d bytes)" % (url, dest, len(r["body"])))

def money(v):
    cents = int(v * 100 + 0.5)
    frac = str(cents % 100)
    if len(frac) < 2:
        frac = "0" + frac
    return "$%d.%s" % (cents // 100, frac)

fetch("https://cdn.jsdelivr.net/npm/chart.js@4.4.1/dist/chart.umd.min.js", "/work/chart.umd.min.js")
fetch("https://cdn.jsdelivr.net/npm/@picocss/pico@2.0.6/css/pico.min.css", "/work/pico.min.css")

data = csv.parse_dicts(fs.read("/work/orders.csv"))

prod, region = {}, {}
grand = 0.0
for r in data:
    rev = int(r["quantity"]) * float(r["unit_price"])
    prod[r["product"]] = prod.get(r["product"], 0.0) + rev
    region[r["region"]] = region.get(r["region"], 0.0) + rev
    grand += rev

top = sorted(prod.items(), key=lambda kv: -kv[1])
best = top[0][0]

page = """<!doctype html>
<html lang="en"><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Revenue dashboard</title>
<link rel="stylesheet" href="pico.min.css">
<script src="chart.umd.min.js"></script>
<style>
  body { padding: 2rem 0 }
  h1 { font-size: 1.4rem; margin-bottom: .25rem }
  .sub { color: var(--pico-muted-color); margin-bottom: 1.5rem }
  .kpis { display: grid; grid-template-columns: repeat(auto-fit, minmax(150px, 1fr)); gap: 1rem; margin-bottom: 2rem }
  .kpi { padding: 1rem; border: 1px solid var(--pico-muted-border-color); border-radius: .5rem }
  .kpi .v { font-size: 1.5rem; font-weight: 700 }
  .kpi .l { font-size: .8rem; color: var(--pico-muted-color); text-transform: uppercase; letter-spacing: .05em }
  .charts { display: grid; grid-template-columns: 2fr 1fr; gap: 2rem; align-items: start }
  .card { border: 1px solid var(--pico-muted-border-color); border-radius: .5rem; padding: 1rem }
  .card h2 { font-size: 1rem; margin-bottom: 1rem }
</style></head><body><main class="container">
<h1>Revenue dashboard</h1>
<p class="sub">/work/orders.csv — %d orders</p>
<section class="kpis">
  <div class="kpi"><div class="v">%s</div><div class="l">revenue</div></div>
  <div class="kpi"><div class="v">%d</div><div class="l">orders</div></div>
  <div class="kpi"><div class="v">%s</div><div class="l">top product</div></div>
  <div class="kpi"><div class="v">%d</div><div class="l">regions</div></div>
</section>
<section class="charts">
  <div class="card"><h2>Revenue by product</h2><canvas id="byProduct" height="120"></canvas></div>
  <div class="card"><h2>Share by region</h2><canvas id="byRegion"></canvas></div>
</section>
<script>
const PROD = %s;
const REG = %s;
const money = v => "$" + v.toFixed(2);
new Chart(document.getElementById("byProduct"), {
  type: "bar",
  data: { labels: PROD.map(x => x[0]),
    datasets: [{ data: PROD.map(x => x[1]),
      backgroundColor: ["#4f7cff", "#38bdf8", "#a78bfa", "#f472b6"],
      borderRadius: 6 }] },
  options: { plugins: { legend: { display: false },
      tooltip: { callbacks: { label: c => money(c.parsed.y) } } },
    scales: { y: { ticks: { callback: money } } } }
});
new Chart(document.getElementById("byRegion"), {
  type: "doughnut",
  data: { labels: REG.map(x => x[0]),
    datasets: [{ data: REG.map(x => x[1]),
      backgroundColor: ["#4f7cff", "#38bdf8", "#a78bfa", "#f472b6"] }] },
  options: { plugins: { legend: { position: "bottom" },
      tooltip: { callbacks: { label: c => c.label + ": " + money(c.parsed) } } } }
});
</script>
</main></body></html>""" % (
    len(data), money(grand), len(data), best, len(region),
    json.encode([[k, v] for k, v in top]),
    json.encode([[k, v] for k, v in sorted(region.items(), key=lambda kv: -kv[1])]),
)

fs.write("/work/report.html", page)
print("wrote /work/report.html: %d rows, %s revenue" % (len(data), money(grand)))
