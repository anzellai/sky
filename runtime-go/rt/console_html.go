package rt

// Console HTML shell — served by HandleConsole. Plain template
// string. No build step, no Sky source. Just enough JS to poll the
// API endpoints every 1s + render five tabs.
//
// The CSS is deliberately minimal-but-legible (system font stack,
// modest contrast, dark+light-mode aware via prefers-color-scheme).
// The whole shell is ~10 KB minified — same order as a Grafana
// panel asset, but zero external deps.
//
// Production-quality tweaks intentionally NOT made for v1.0:
//   - No client-side routing (clicking a tab is just `.active`
//     class swap; URL stays /_sky/console).
//   - No drag-resize on panels.
//   - No chart library (sparklines via inline SVG drawn from JS).
//   - No service worker / offline.
//   - No i18n.
//
// All of the above would inflate the binary without adding the
// "I can see what's broken" value the v1.0 user actually needs.

const consoleHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Sky Console</title>
<style>
* { box-sizing: border-box; }
html, body {
    margin: 0; padding: 0;
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", system-ui, sans-serif;
    background: #f6f7f9; color: #1a1d24;
}
@media (prefers-color-scheme: dark) {
    html, body { background: #14171c; color: #e3e6eb; }
    .panel, .row, header { background: #1c2027 !important; border-color: #2a2f38 !important; }
    .tab { color: #9aa5b2 !important; }
    .tab.active { color: #6cb4ff !important; border-bottom-color: #6cb4ff !important; }
    .kpi-value { color: #f0f3f7 !important; }
    code { background: #2a2f38 !important; color: #e3e6eb !important; }
    .err { background: #401a1a !important; }
}
header {
    background: white; border-bottom: 1px solid #e2e5ea;
    padding: 12px 20px; display: flex; align-items: center; gap: 20px;
}
header h1 {
    font-size: 16px; font-weight: 600; margin: 0;
}
header .meta {
    font-size: 12px; color: #6b7480;
    margin-left: auto; font-family: ui-monospace, Menlo, monospace;
}
nav {
    background: white; border-bottom: 1px solid #e2e5ea;
    padding: 0 20px;
}
.tab {
    display: inline-block; padding: 10px 14px;
    cursor: pointer; color: #6b7480;
    border-bottom: 2px solid transparent;
    font-size: 13px; font-weight: 500;
}
.tab.active {
    color: #2a6fdb; border-bottom-color: #2a6fdb;
}
main { padding: 20px; }
.tab-content { display: none; }
.tab-content.active { display: block; }
.kpi-row {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
    gap: 12px; margin-bottom: 20px;
}
.kpi {
    background: white; border: 1px solid #e2e5ea;
    border-radius: 6px; padding: 14px;
}
.kpi-label {
    font-size: 11px; text-transform: uppercase;
    letter-spacing: 0.5px; color: #6b7480; font-weight: 600;
}
.kpi-value {
    font-size: 24px; font-weight: 600;
    margin-top: 4px; color: #1a1d24;
    font-variant-numeric: tabular-nums;
}
.panel {
    background: white; border: 1px solid #e2e5ea;
    border-radius: 6px; padding: 14px; margin-bottom: 16px;
}
.panel h2 {
    font-size: 13px; font-weight: 600;
    margin: 0 0 10px 0; color: #1a1d24;
    text-transform: uppercase; letter-spacing: 0.5px;
}
table {
    width: 100%; border-collapse: collapse;
    font-size: 13px;
}
th, td {
    text-align: left; padding: 6px 10px;
    border-bottom: 1px solid #eef0f3;
}
th {
    font-size: 11px; text-transform: uppercase;
    letter-spacing: 0.5px; color: #6b7480; font-weight: 600;
}
tr:last-child td { border-bottom: none; }
code, .mono {
    font-family: ui-monospace, Menlo, monospace;
    font-size: 12px; background: #f0f3f7;
    padding: 1px 4px; border-radius: 3px;
}
.lvl-debug { color: #6b7480; }
.lvl-info  { color: #1a1d24; }
.lvl-warn  { color: #c47100; font-weight: 600; }
.lvl-error { color: #c92020; font-weight: 600; }
.err { background: #fff3f3; }
.empty {
    color: #9aa5b2; font-style: italic;
    padding: 20px; text-align: center;
}
.toolbar {
    display: flex; gap: 10px; align-items: center;
    margin-bottom: 12px;
}
.toolbar input {
    padding: 5px 8px; border: 1px solid #d0d5dd;
    border-radius: 4px; font: inherit;
}
</style>
</head>
<body>
<header>
    <h1>Sky Console</h1>
    <span class="meta" id="meta"></span>
</header>
<nav>
    <span class="tab active" data-tab="overview">Overview</span>
    <span class="tab" data-tab="metrics">Metrics</span>
    <span class="tab" data-tab="logs">Logs</span>
    <span class="tab" data-tab="traces">Traces</span>
    <span class="tab" data-tab="errors">Errors</span>
</nav>
<main>

<div class="tab-content active" id="tab-overview">
    <div class="kpi-row" id="kpis"></div>
    <div class="panel">
        <h2>System</h2>
        <table id="sys-table"></table>
    </div>
</div>

<div class="tab-content" id="tab-metrics">
    <div class="panel">
        <h2>Metrics</h2>
        <table id="metrics-table">
            <thead>
                <tr><th>Name</th><th>Type</th><th>Labels</th><th style="text-align:right">Value</th></tr>
            </thead>
            <tbody></tbody>
        </table>
    </div>
</div>

<div class="tab-content" id="tab-logs">
    <div class="toolbar">
        <input id="log-filter-level" placeholder="level: warn,error (blank = all)">
        <input id="log-filter-req" placeholder="request id">
    </div>
    <div class="panel">
        <h2>Recent logs</h2>
        <table id="logs-table">
            <thead>
                <tr><th>Time</th><th>Level</th><th>Message</th><th>Req</th><th>Latency</th></tr>
            </thead>
            <tbody></tbody>
        </table>
    </div>
</div>

<div class="tab-content" id="tab-traces">
    <div class="panel">
        <h2>Recent traces</h2>
        <table id="traces-table">
            <thead>
                <tr><th>Time</th><th>Name</th><th>Kind</th><th style="text-align:right">Duration</th><th>Trace</th><th>Status</th></tr>
            </thead>
            <tbody></tbody>
        </table>
    </div>
</div>

<div class="tab-content" id="tab-errors">
    <div class="panel">
        <h2>Errors (last 10 minutes, grouped)</h2>
        <table id="errors-table">
            <thead>
                <tr><th style="width:60px">Count</th><th>Level</th><th>Message</th><th>Last seen</th><th>Last error</th></tr>
            </thead>
            <tbody></tbody>
        </table>
    </div>
</div>

</main>
<script>
(function() {
    var REFRESH_MS = 1000;
    var activeTab = "overview";

    // Tab switcher
    document.querySelectorAll(".tab").forEach(function(t) {
        t.addEventListener("click", function() {
            document.querySelectorAll(".tab").forEach(function(x){x.classList.remove("active");});
            document.querySelectorAll(".tab-content").forEach(function(x){x.classList.remove("active");});
            t.classList.add("active");
            activeTab = t.getAttribute("data-tab");
            document.getElementById("tab-"+activeTab).classList.add("active");
            refresh(); // immediate fetch on tab switch
        });
    });

    // Filter listeners
    var logFilterLevel = document.getElementById("log-filter-level");
    var logFilterReq = document.getElementById("log-filter-req");
    logFilterLevel.addEventListener("input", function(){ if (activeTab==="logs") refreshLogs(); });
    logFilterReq.addEventListener("input",   function(){ if (activeTab==="logs") refreshLogs(); });

    function fmtNum(n, dec) {
        if (n === undefined || n === null) return "—";
        if (typeof n !== "number") return String(n);
        if (n < 1) return n.toFixed(dec || 3);
        if (n < 1000) return n.toFixed(0);
        if (n < 1_000_000) return (n/1000).toFixed(1) + "k";
        return (n/1_000_000).toFixed(1) + "M";
    }
    function fmtPct(p) {
        if (p === undefined || p === null) return "—";
        return (p*100).toFixed(2) + "%";
    }
    function fmtDuration(secs) {
        if (secs === undefined || secs === null) return "—";
        if (secs < 60) return secs.toFixed(0) + "s";
        if (secs < 3600) return (secs/60).toFixed(1) + "m";
        if (secs < 86400) return (secs/3600).toFixed(1) + "h";
        return (secs/86400).toFixed(1) + "d";
    }
    function fmtBytes(n) {
        if (n === undefined || n === null) return "—";
        if (n < 1024) return n + "B";
        if (n < 1024*1024) return (n/1024).toFixed(1) + "KB";
        return (n/(1024*1024)).toFixed(1) + "MB";
    }
    function esc(s) {
        var d = document.createElement("div");
        d.textContent = s == null ? "" : String(s);
        return d.innerHTML;
    }

    function refreshOverview() {
        fetch("/_sky/console/api/overview")
            .then(function(r){ return r.json(); })
            .then(function(d){
                document.getElementById("meta").textContent =
                    "Sky " + (d.skyVersion||"dev") + " · " +
                    (d.commit||"dev").slice(0,7) + " · uptime " + fmtDuration(d.uptimeSeconds);
                var kpis = [
                    {label:"Requests total", value: fmtNum(d.requestsTotal)},
                    {label:"5xx error rate", value: fmtPct(d.errorRate5xx)},
                    {label:"Log buffer", value: fmtNum(d.bufferLogUsed)+"/10k"},
                    {label:"Trace buffer", value: fmtNum(d.bufferTraceUsed)+"/1k"}
                ];
                document.getElementById("kpis").innerHTML = kpis.map(function(k){
                    return '<div class="kpi"><div class="kpi-label">' +
                        esc(k.label) + '</div><div class="kpi-value">' +
                        esc(k.value) + '</div></div>';
                }).join("");

                var rows = [
                    ["Sky version",   d.skyVersion],
                    ["Commit",        (d.commit||"dev").slice(0,12)],
                    ["Built at",      d.builtAt],
                    ["Uptime",        fmtDuration(d.uptimeSeconds)],
                    ["Runtime mode",  d.serverlessMode ? "serverless" : "vm"],
                    ["Auth mode",     d.productionMode ? "production (admin token required)" : "dev (open)"]
                ];
                document.getElementById("sys-table").innerHTML =
                    rows.map(function(r){return "<tr><th>"+esc(r[0])+"</th><td><code>"+esc(r[1])+"</code></td></tr>";}).join("");
            }).catch(function(e){
                document.getElementById("meta").textContent = "API unreachable: "+e.message;
            });
    }

    function refreshMetrics() {
        fetch("/_sky/console/api/metrics-summary")
            .then(function(r){return r.json();})
            .then(function(rows){
                var body = (rows||[]).map(function(r){
                    var labelStr = "";
                    if (r.labels) {
                        labelStr = Object.keys(r.labels).sort().map(function(k){
                            return k+"="+r.labels[k];
                        }).join(", ");
                    }
                    var val;
                    if (r.type === "histogram") {
                        val = "sum="+r.sum.toFixed(3)+" count="+r.count;
                    } else {
                        val = r.value.toFixed(3);
                    }
                    return "<tr><td><code>"+esc(r.name)+"</code></td><td>"+esc(r.type)+"</td><td><code>"+esc(labelStr)+"</code></td><td style='text-align:right' class='mono'>"+esc(val)+"</td></tr>";
                }).join("");
                document.querySelector("#metrics-table tbody").innerHTML = body || "<tr><td colspan='4' class='empty'>No metrics yet — generate some traffic.</td></tr>";
            });
    }

    function refreshLogs() {
        var qs = [];
        var lv = logFilterLevel.value.trim();
        var rq = logFilterReq.value.trim();
        if (lv) qs.push("level="+encodeURIComponent(lv));
        if (rq) qs.push("req="+encodeURIComponent(rq));
        qs.push("limit=200");
        fetch("/_sky/console/api/logs?"+qs.join("&"))
            .then(function(r){return r.json();})
            .then(function(rows){
                var body = (rows||[]).map(function(l){
                    var ts = l.TS ? new Date(l.TS).toISOString().split("T")[1].split(".")[0] : "";
                    var lat = (l.LatencyMS||0).toFixed(1)+"ms";
                    var cls = "lvl-"+(l.Level||"info");
                    return "<tr><td class='mono'>"+esc(ts)+"</td><td class='"+cls+"'>"+esc(l.Level)+"</td><td>"+esc(l.Message)+(l.ErrorStr?" — "+esc(l.ErrorStr):"")+"</td><td class='mono'>"+esc((l.ReqID||"").slice(0,12))+"</td><td class='mono'>"+esc(lat)+"</td></tr>";
                }).join("");
                document.querySelector("#logs-table tbody").innerHTML = body || "<tr><td colspan='5' class='empty'>No logs match.</td></tr>";
            });
    }

    function refreshTraces() {
        fetch("/_sky/console/api/traces?limit=100")
            .then(function(r){return r.json();})
            .then(function(rows){
                var body = (rows||[]).map(function(t){
                    var ts = t.startTime ? t.startTime.split("T")[1].split(".")[0] : "";
                    var statusCls = (t.status === "Error") ? "lvl-error" : "lvl-info";
                    return "<tr><td class='mono'>"+esc(ts)+"</td><td>"+esc(t.name)+"</td><td>"+esc(t.kind)+"</td><td style='text-align:right' class='mono'>"+esc(t.durationMs.toFixed(2))+"ms</td><td class='mono'>"+esc(t.traceId.slice(0,16))+"</td><td class='"+statusCls+"'>"+esc(t.status||"")+"</td></tr>";
                }).join("");
                document.querySelector("#traces-table tbody").innerHTML = body || "<tr><td colspan='6' class='empty'>No traces (OTEL_EXPORTER_OTLP_ENDPOINT unset?).</td></tr>";
            });
    }

    function refreshErrors() {
        fetch("/_sky/console/api/errors")
            .then(function(r){return r.json();})
            .then(function(rows){
                var body = (rows||[]).map(function(e){
                    var ts = e.lastSeen ? e.lastSeen.split("T")[1].split(".")[0] : "";
                    var cls = "lvl-"+e.level;
                    return "<tr class='err'><td style='text-align:right' class='mono'>"+esc(e.count)+"</td><td class='"+cls+"'>"+esc(e.level)+"</td><td>"+esc(e.message)+"</td><td class='mono'>"+esc(ts)+"</td><td class='mono'>"+esc((e.lastError||"").slice(0,80))+"</td></tr>";
                }).join("");
                document.querySelector("#errors-table tbody").innerHTML = body || "<tr><td colspan='5' class='empty'>No errors. 🎉</td></tr>";
            });
    }

    function refresh() {
        if      (activeTab === "overview") refreshOverview();
        else if (activeTab === "metrics")  refreshMetrics();
        else if (activeTab === "logs")     refreshLogs();
        else if (activeTab === "traces")   refreshTraces();
        else if (activeTab === "errors")   refreshErrors();
    }

    refresh();
    setInterval(refresh, REFRESH_MS);
})();
</script>
</body>
</html>`
