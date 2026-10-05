rows = []
channels = ["网站", "小程序", "合作伙伴"]
for month in range(1, 13):
    for i in range(len(channels)):
        orders = 160 + month * 27 + i * 54 + (month % 4) * 18
        revenue = orders * (88 + i * 34 + month * 2)
        rows.append({
            "month": month,
            "channel": channels[i],
            "orders": orders,
            "revenue": revenue,
            "cost": revenue * (58 + i * 4) // 100,
        })

columns = ["month", "channel", "orders", "revenue", "cost"]
fs.write("sales-lab/sales.csv", csv.format([columns] + [
    [str(row[key]) for key in columns] for row in rows
]))
fs.write("sales-lab/data.js", "window.SALES_DATA = " + json.encode(rows) + ";\n")

fs.write("sales-lab/index.html", r"""<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>销售实验室 · Calcside</title>
  <link rel="stylesheet" href="./style.css">
  <script src="./data.js" defer></script>
  <script src="./app.js" defer></script>
</head>
<body>
<main>
  <header>
    <div>
      <p class="eyebrow">CALCSIDE / DATA PLAYGROUND / 01</p>
      <h1>销售实验室<span class="dot"></span></h1>
      <p class="muted">筛选、比较、模拟。从一份静态产物，走到可操作的数据报告。</p>
    </div>
    <div id="status" class="badge" role="status">静态预览 · 请在上方启用 JavaScript</div>
  </header>

  <section class="toolbar" aria-label="筛选与模拟">
    <label>销售渠道
      <select id="channel" data-control disabled>
        <option value="all">全部渠道</option>
        <option>网站</option><option>小程序</option><option>合作伙伴</option>
      </select>
    </label>
    <label>统计区间
      <select id="quarter" data-control disabled>
        <option value="0">全年</option>
        <option value="1">Q1 · 1–3 月</option><option value="2">Q2 · 4–6 月</option>
        <option value="3">Q3 · 7–9 月</option><option value="4">Q4 · 10–12 月</option>
      </select>
    </label>
    <label class="slider">收入提升模拟 <output id="growth-label">+0%</output>
      <input id="growth" data-control disabled type="range" min="0" max="80" step="5" value="0">
    </label>
    <button id="reset" data-control disabled class="reset">重置</button>
  </section>

  <section class="cards" aria-label="关键指标">
    <article class="card primary"><p>01 / 模拟营收</p><strong id="revenue">—</strong><small>当前筛选范围，单位：元</small></article>
    <article class="card"><p>02 / 预估利润</p><strong id="profit">—</strong><small>模拟营收 − 基准成本</small></article>
    <article class="card"><p>03 / 订单数量</p><strong id="orders">—</strong><small>基准订单数，不随收入滑块变化</small></article>
  </section>

  <section class="panel">
    <div class="panel-head">
      <div><p class="eyebrow">MONTHLY DISTRIBUTION</p><h2>让趋势可见</h2></div>
      <div class="switch" role="group" aria-label="图表指标">
        <button data-metric="revenue" data-control disabled aria-pressed="true">营收</button>
        <button data-metric="profit" data-control disabled aria-pressed="false">利润</button>
        <button data-metric="orders" data-control disabled aria-pressed="false">订单</button>
      </div>
    </div>
    <p id="chart-note" class="muted">启用 JavaScript 后显示图表；点击柱子可筛选下方明细。</p>
    <div class="chart-scroll"><div id="chart" class="bars" role="group" aria-label="月度图表"></div></div>
  </section>

  <section class="panel">
    <div class="panel-head">
      <div><p class="eyebrow">SOURCE RECORDS</p><h2>数据明细 <span id="count" class="count">0 条</span></h2></div>
      <button id="clear-month" data-control disabled>显示所有月份</button>
    </div>
    <div class="table-scroll">
      <table>
        <thead><tr><th>月份</th><th>渠道</th><th>订单</th><th><button id="sort" data-control disabled>营收 ↓</button></th><th>利润</th></tr></thead>
        <tbody id="records"><tr><td colspan="5">交互模式启用后加载本地数据。</td></tr></tbody>
      </table>
    </div>
  </section>
  <footer>模拟数据，非生产指标。收入模拟假设成本和订单数不变；修改仅在当前页面有效。<br>原始数据已写入 sales.csv，可在实例 Files 中下载；完整报告请打包 sales-lab 目录。</footer>
</main>
</body>
</html>""")

fs.write("sales-lab/style.css", r"""
:root{color-scheme:light;--ink:#171717;--paper:#f4f1e9;--blue:#2349e8;--orange:#ef6039;--line:2px solid var(--ink)}
*{box-sizing:border-box}
body{margin:0;background:var(--paper);color:var(--ink);font:15px/1.6 system-ui,-apple-system,"Segoe UI",sans-serif}
main{max-width:1200px;margin:auto;padding:36px}
header{display:flex;align-items:center;justify-content:space-between;gap:24px;padding-bottom:28px;border-bottom:var(--line)}
h1,h2,p{margin:0}h1{font-size:clamp(32px,5vw,60px);letter-spacing:-2px;line-height:1.25;margin:10px 0}h2{font-size:23px;letter-spacing:-.5px}
.eyebrow{font:11px/1.5 ui-monospace,monospace;letter-spacing:2px}.muted,footer{color:#666}.muted{margin-top:10px;font-size:13px}
.dot{display:inline-block;width:19px;height:19px;border-radius:50%;background:var(--orange);margin-left:14px}
.badge{border:var(--line);padding:9px 12px;background:#fff;font-size:12px;max-width:260px}
.toolbar{display:flex;align-items:end;gap:20px;flex-wrap:wrap;padding:26px 0}.toolbar label{display:grid;gap:8px;font-size:12px;font-weight:650}.slider{flex:1;min-width:220px;grid-template-columns:1fr auto}.slider input{grid-column:1 / -1;width:100%;margin:0;height:40px;accent-color:var(--blue)}
select,button{font:inherit;border:var(--line);background:#fff;color:var(--ink);border-radius:0;padding:9px 14px;min-height:42px}select{min-width:155px}button{cursor:pointer}button:hover{background:#e9edff}button:disabled{cursor:default;opacity:.5}button:focus-visible,select:focus-visible,input:focus-visible{outline:3px solid var(--orange);outline-offset:4px}.reset{background:var(--ink);color:#fff}.reset:hover{background:var(--blue)}
.cards{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:18px;margin:0 0 24px}.card{border:var(--line);background:#fff;padding:23px}.card p{font:12px/1.5 ui-monospace,monospace}.card strong{display:block;font-size:clamp(25px,3vw,40px);letter-spacing:-1px;margin:15px 0 9px;overflow-wrap:anywhere}.card small{font-size:11px}.primary{background:var(--blue);color:#fff}
.panel{border:var(--line);background:#fff;padding:26px;margin-bottom:24px}.panel-head{display:flex;align-items:center;justify-content:space-between;gap:16px}.panel-head .eyebrow{color:#777;margin-bottom:5px}.switch{display:flex}.switch button+button{border-left:0}.switch [aria-pressed="true"]{background:var(--ink);color:#fff}
.chart-scroll,.table-scroll{overflow-x:auto}.bars{display:grid;gap:14px;min-height:260px;padding:16px 0 8px}.bar-column{display:flex;flex-direction:column;align-items:center;justify-content:end;gap:8px}.bar-column small{font:11px ui-monospace,monospace;color:#555}.bar{display:block;width:85%;min-height:8px;padding:0;background:var(--blue);border:var(--line)}.bar:hover,.bar[aria-pressed="true"]{background:var(--orange)}.bar-column span{font:12px ui-monospace,monospace}
.count{font:12px system-ui;color:#777;margin-left:8px}table{width:100%;border-collapse:collapse;margin-top:20px;white-space:nowrap}th,td{text-align:right;padding:13px 14px;border-bottom:1px solid #ddd;font-variant-numeric:tabular-nums}th{font-size:12px;color:#666}th:first-child,th:nth-child(2),td:first-child,td:nth-child(2){text-align:left}th button{font-size:12px;min-height:30px;padding:3px 10px}tbody tr:hover{background:#f4f5ff}footer{font-size:12px;padding-bottom:12px}
@media(max-width:720px){main{padding:18px}header,.panel-head{align-items:start;flex-direction:column}.cards{grid-template-columns:1fr;gap:12px}.card strong{font-size:34px;margin:8px 0}.panel{padding:18px}.toolbar{gap:14px}.toolbar label:not(.slider){flex:1}.toolbar select{width:100%;min-width:0}.slider{flex-basis:100%}.badge{max-width:none}}
""")

fs.write("sales-lab/app.js", r"""
const $ = id => document.getElementById(id);
const number = new Intl.NumberFormat("zh-CN", {maximumFractionDigits: 0});
const money = value => "¥ " + number.format(value);
const labels = {revenue: "营收", profit: "利润", orders: "订单"};
let metric = "revenue", selectedMonth = 0, descending = true;

function render() {
  const channel = $("channel").value, quarter = Number($("quarter").value);
  const growth = Number($("growth").value), factor = 1 + growth / 100;
  const rows = window.SALES_DATA
    .filter(row => (channel === "all" || row.channel === channel) && (!quarter || Math.ceil(row.month / 3) === quarter))
    .map(row => ({...row, revenue: Math.round(row.revenue * factor), profit: Math.round(row.revenue * factor) - row.cost}));
  const sum = key => rows.reduce((total, row) => total + row[key], 0);
  $("growth-label").textContent = "+" + growth + "%";
  $("revenue").textContent = money(sum("revenue"));
  $("profit").textContent = money(sum("profit"));
  $("orders").textContent = number.format(sum("orders"));
  document.querySelectorAll("[data-metric]").forEach(button => button.setAttribute("aria-pressed", String(button.dataset.metric === metric)));

  const months = [...new Set(rows.map(row => row.month))].sort((a, b) => a - b);
  const values = months.map(month => rows.filter(row => row.month === month).reduce((total, row) => total + row[metric], 0));
  const max = Math.max(1, ...values), chart = $("chart");
  chart.replaceChildren();
  chart.style.gridTemplateColumns = `repeat(${months.length}, minmax(38px, 1fr))`;
  chart.style.minWidth = months.length * 52 + "px";
  months.forEach((month, index) => {
    const column = document.createElement("div");
    column.className = "bar-column";
    const value = document.createElement("small");
    value.textContent = values[index] >= 10000 ? (values[index] / 10000).toFixed(1) + "万" : number.format(values[index]);
    const bar = document.createElement("button");
    bar.className = "bar";
    bar.style.height = Math.max(8, values[index] / max * 190) + "px";
    bar.title = `${month}月 ${labels[metric]}：${number.format(values[index])}`;
    bar.setAttribute("aria-label", bar.title);
    bar.setAttribute("aria-pressed", String(selectedMonth === month));
    bar.onclick = () => { selectedMonth = selectedMonth === month ? 0 : month; render(); };
    const label = document.createElement("span");
    label.textContent = month + "月";
    column.append(value, bar, label);
    chart.append(column);
  });

  const detail = rows.filter(row => !selectedMonth || row.month === selectedMonth)
    .sort((a, b) => descending ? b.revenue - a.revenue : a.revenue - b.revenue);
  $("count").textContent = detail.length + " 条";
  $("chart-note").textContent = selectedMonth ? `正在查看 ${selectedMonth} 月明细。再次点击该柱可取消；顶部指标仍统计整个筛选区间。` : "点击任意柱子筛选下方明细；横向比较各月表现。";
  $("sort").textContent = "营收 " + (descending ? "↓" : "↑");
  $("clear-month").disabled = selectedMonth === 0;
  $("records").replaceChildren();
  for (const row of detail) {
    const tr = document.createElement("tr");
    for (const text of [row.month + "月", row.channel, number.format(row.orders), money(row.revenue), money(row.profit)]) {
      const td = document.createElement("td");
      td.textContent = text;
      tr.append(td);
    }
    $("records").append(tr);
  }
}

for (const id of ["channel", "quarter"]) {
  $(id).addEventListener("change", () => { selectedMonth = 0; render(); });
}
$("growth").addEventListener("input", render);
$("sort").onclick = () => { descending = !descending; render(); };
$("clear-month").onclick = () => { selectedMonth = 0; render(); };
document.querySelectorAll("[data-metric]").forEach(button => {
  button.onclick = () => { metric = button.dataset.metric; render(); };
});
$("reset").onclick = () => {
  $("channel").value = "all";
  $("quarter").value = "0";
  $("growth").value = "0";
  metric = "revenue"; selectedMonth = 0; descending = true;
  render();
};
document.querySelectorAll("[data-control]").forEach(control => { control.disabled = false; });
render();
$("status").textContent = "INTERACTIVE / 36 条本地模拟数据";
""")

print("已生成 /work/sales-lab/index.html")
print("在 Files 中点击 index.html 的预览图标，然后启用 JavaScript。")
print("原始数据：/work/sales-lab/sales.csv；完整报告请下载 sales-lab 目录 ZIP。")
