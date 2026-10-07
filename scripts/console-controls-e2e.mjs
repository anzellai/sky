#!/usr/bin/env node
// scripts/console-controls-e2e.mjs — every interactive control of the Sky
// Console, operated in a real browser, with its observable effect asserted.
//
// Driven by scripts/console-controls-e2e.sh (which builds the fixture). One
// run is one scenario in one browser:
//
//   node scripts/console-controls-e2e.mjs --scenario embedded|dev|hub
//        --browser chromium|webkit --port <port> --work <dir>
//        [--app <binary> --cwd <dir>]        (embedded, dev)
//        [--sky <sky binary>]                (hub: runs `sky console-serve`)
//
// The scenarios:
//
//   embedded  The console every Sky app mounts at /_sky/console, as production
//             runs it: ENV=production, SKY_CONSOLE_AUTH=token, SKY_CSP=strict.
//             The app (rust/crates/sky/tests/fixtures/console-controls) runs
//             in TZ=America/New_York, and so does the browser: a time compare
//             that ignores the zone shows up as a wrong range. The console
//             reads its host through a recording proxy (SKY_PARENT_URL), so
//             the run can assert the query each control sends and compare the
//             server's answer with what the page shows.
//   dev       The same app with no ENV: the console opens without a sign-in
//             and shows no "Sign out" (there is nothing to sign out of).
//   hub       `sky console-serve --auth off`: the hub console over its SQLite
//             store, two services, the service selector and cards.
//
// Data. Before the browser opens, the run seeds time-spread data the ranges
// must tell apart: log lines at every level and a two-span trace at 2 min,
// 30 min, 5 h, 3 d and 10 d ago (the hub keeps 24 h, so it gets the first
// three), and analytics events at the same ages plus 40 d. Then it adds
// volume: 300 requests, each an access-log line and a span, so the seeds are
// NOT among the newest 200 log lines or 100 spans. A range or search applied
// to the newest rows only cannot find them.
//
// Every control in the inventory (docs/observability.md, "Sky Console
// controls") is operated and checked: the DOM shows the expected rows, chips,
// labels and input values; the URL carries the state; the read the console
// makes carries the right parameters, and the server's answer is the set the
// page shows. Zero console errors, zero CSP violations and zero failed
// requests are asserted for the whole run.
//
// Output: one line per control (PASS / FAIL + why), then
//   console-controls-e2e: <scenario> <browser> — <n>/<total> controls passed
// Exit 0 when every control passed, 1 otherwise.

import pw from "playwright";
import { spawn, spawnSync } from "node:child_process";
import { guardChild } from "./lib/child-guard.mjs";
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import http from "node:http";
import https from "node:https";
import { readFileSync } from "node:fs";
import net from "node:net";

const argv = process.argv.slice(2);
const arg = (k, d) => {
  const i = argv.indexOf(k);
  return i >= 0 && i + 1 < argv.length ? argv[i + 1] : d;
};
const SCENARIO = arg("--scenario", "embedded");
const BROWSER = arg("--browser", "chromium");
const PORT = Number(arg("--port", "9660"));
const WORK = arg("--work");
const APP = arg("--app", "");
const CWD = arg("--cwd", process.cwd());
const SKY = arg("--sky", "");
if (!WORK || !["embedded", "dev", "hub"].includes(SCENARIO) || !["chromium", "webkit"].includes(BROWSER)) {
  console.error("usage: console-controls-e2e.mjs --scenario embedded|dev|hub --browser chromium|webkit --port N --work DIR [--app BIN --cwd DIR] [--sky BIN]");
  process.exit(2);
}
if (SCENARIO !== "hub" && !APP) {
  console.error("console-controls-e2e: --app is required for the embedded and dev scenarios");
  process.exit(2);
}
if (SCENARIO === "hub" && !SKY) {
  console.error("console-controls-e2e: --sky is required for the hub scenario");
  process.exit(2);
}
mkdirSync(WORK, { recursive: true });

const TOKEN = "console-controls-e2e-token-0123456789abcdef";
const INGEST = "console-controls-e2e-ingest-0123456789abcdef";
const TZ = "America/New_York";
const PROXY_PORT = PORT + 1;
const TLS_PORT = PORT + 2;
// ORIGIN is the server itself (seeding, volume). The browser reaches the
// embedded scenario's production app over HTTPS, as production does: its
// cookies are Secure (__Host-sky_console, the CSRF cookie), which WebKit does
// not keep over plain http://127.0.0.1. A local TLS front (startTlsFront)
// stands in for the operator's proxy.
const ORIGIN = `http://127.0.0.1:${PORT}`;
const PAGE_ORIGIN = SCENARIO === "embedded" ? `https://localhost:${TLS_PORT}` : ORIGIN;
const BASE = SCENARIO === "hub" ? "/console/" : "/_sky/console/";
const tag = `[${SCENARIO} ${BROWSER}]`;
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const say = (m) => console.log(`${tag} ${m}`);

// ── results ─────────────────────────────────────────────────────────────────
const results = [];
function record(control, ok, detail = "") {
  results.push({ control, ok, detail });
  console.log(`${tag} ${ok ? "PASS" : "FAIL"} ${control}${detail ? " — " + detail : ""}`);
}
async function check(control, fn) {
  try {
    const detail = await fn();
    record(control, true, typeof detail === "string" ? detail : "");
  } catch (e) {
    record(control, false, String(e && e.message ? e.message : e).replace(/\s+/g, " ").slice(0, 400));
  }
}
function expect(cond, msg) {
  if (!cond) throw new Error(msg);
}

// ── seed data ───────────────────────────────────────────────────────────────
const MIN = 60e3, HOUR = 60 * MIN, DAY = 24 * HOUR;
const AGES = SCENARIO === "hub"
  ? { "2m": 2 * MIN, "30m": 30 * MIN, "5h": 5 * HOUR }
  : { "2m": 2 * MIN, "30m": 30 * MIN, "5h": 5 * HOUR, "3d": 3 * DAY, "10d": 10 * DAY };
const EVENT_AGES = { "2m": 2 * MIN, "30m": 30 * MIN, "5h": 5 * HOUR, "3d": 3 * DAY, "10d": 10 * DAY, "40d": 40 * DAY };
// Each seed event carries a Money prop; the sums tell every range apart.
const EVENT_USD = { "2m": 1.25, "30m": 2.5, "5h": 5, "3d": 10, "10d": 20, "40d": 40 };
const LEVELS = ["debug", "info", "warn", "error"];
// The ranges, their chip labels, URL keys and windows.
const RANGES = [
  { label: "15m", key: "15m", ms: 15 * MIN },
  { label: "1h", key: "1h", ms: HOUR },
  { label: "24h", key: "24h", ms: DAY },
  { label: "7d", key: "7d", ms: 7 * DAY },
  { label: "All", key: "all", ms: Infinity },
];
const agesIn = (ages, ms, cap = Infinity) =>
  Object.entries(ages).filter(([, a]) => a < Math.min(ms, cap)).map(([k]) => k).sort();
const traceId = (age) => Buffer.from(("7e" + age).padEnd(32, "0").slice(0, 32)).toString("hex").slice(0, 32);
const spanId = (age, n) => Buffer.from(("5" + n + age).padEnd(16, "0").slice(0, 16)).toString("hex").slice(0, 16);
const sessionId = (age) => ("ses" + age).padEnd(8, "x") + "-console-controls";
const serviceOf = (age) => (["2m", "5h"].includes(age) ? "svc-alpha" : "svc-beta");

function seedLogLines(now) {
  const out = [];
  for (const [age, ms] of Object.entries(AGES)) {
    for (const level of LEVELS) {
      out.push({ age, level, ts: now - ms, msg: `seedlog-${age}-${level}`, trace: traceId(age), session: sessionId(age), service: serviceOf(age) });
    }
  }
  return out;
}

// ── processes ───────────────────────────────────────────────────────────────
function portOpen(port) {
  return new Promise((resolve) => {
    const s = net.connect(port, "127.0.0.1");
    s.on("connect", () => { s.end(); resolve(true); });
    s.on("error", () => { s.destroy(); resolve(false); });
  });
}
async function waitPort(port, ms) {
  const end = Date.now() + ms;
  while (Date.now() < end) {
    if (await portOpen(port)) return true;
    await sleep(200);
  }
  return false;
}

let server = null;
function startServer() {
  const env = { ...process.env, TZ, SKY_LIVE_PORT: String(PORT), PORT: String(PORT) };
  for (const k of Object.keys(env)) if (k.startsWith("SKY_CONSOLE") || k === "SKY_CSP" || k === "SKY_LIVE_STORE") delete env[k];
  delete env.ENV;
  let cmd, args;
  if (SCENARIO === "hub") {
    cmd = SKY;
    args = ["console-serve", "--port", String(PORT), "--auth", "off", "--data-dir", join(WORK, "hub-data")];
    env.SKY_CONSOLE_HUB_QUIET = "";
  } else {
    cmd = APP;
    args = [];
    env.SKY_INGEST_TOKEN = INGEST;
    env.SKY_ANALYTICS_DB_PATH = join(WORK, "analytics.db");
    env.SKY_DB_PATH = join(WORK, "app.db");
    env.SKY_PARENT_URL = `http://127.0.0.1:${PROXY_PORT}`;
    if (SCENARIO === "embedded") {
      env.ENV = "production";
      env.SKY_CONSOLE_AUTH = "token";
      env.SKY_CONSOLE_TOKEN = TOKEN;
      env.SKY_ADMIN_TOKEN = "console-controls-e2e-admin-0123456789";
      env.SKY_CSP = "strict";
    }
  }
  server = guardChild(spawn(cmd, args, { cwd: SCENARIO === "hub" ? WORK : CWD, env, stdio: ["ignore", "pipe", "pipe"] }));
  const out = [];
  server.stdout.on("data", (d) => out.push(String(d)));
  server.stderr.on("data", (d) => out.push(String(d)));
  server.logTail = () => out.join("").split("\n").slice(-25).join("\n");
}
async function stopServer() {
  if (!server || server.exitCode !== null) return;
  const p = server;
  const exited = new Promise((r) => p.once("exit", r));
  p.kill("SIGTERM");
  await Promise.race([exited, sleep(8000)]);
  if (p.exitCode === null) p.kill("SIGKILL");
}

// The recording proxy the embedded console reads its host through. Every
// /_sky/console/api/* read is recorded with its query and its JSON answer.
const reads = [];
const failPaths = new Set(); // reads answered 500, to show the error state
let proxy = null;
function startProxy() {
  proxy = http.createServer((req, res) => {
    if (failPaths.has(new URL(req.url, "http://x").pathname)) {
      res.writeHead(500, { "content-type": "text/plain" });
      res.end("injected failure");
      return;
    }
    const up = http.request({ host: "127.0.0.1", port: PORT, method: req.method, path: req.url, headers: req.headers }, (ur) => {
      const chunks = [];
      ur.on("data", (c) => chunks.push(c));
      ur.on("end", () => {
        const body = Buffer.concat(chunks);
        const u = new URL(req.url, "http://x");
        let json = null;
        try { json = JSON.parse(body.toString("utf8")); } catch { /* not JSON */ }
        reads.push({ at: Date.now(), path: u.pathname, params: u.searchParams, status: ur.statusCode, json });
        res.writeHead(ur.statusCode, ur.headers);
        res.end(body);
      });
    });
    up.on("error", (e) => { res.writeHead(502); res.end(String(e)); });
    req.pipe(up);
  });
  return new Promise((r) => proxy.listen(PROXY_PORT, "127.0.0.1", r));
}

// The TLS front for the embedded scenario: a self-signed certificate
// (openssl), streaming every response through (the page's SSE included) with
// the X-Forwarded-* headers a reverse proxy sets.
let tlsFront = null;
function startTlsFront() {
  const key = join(WORK, "tls-key.pem"), cert = join(WORK, "tls-cert.pem");
  const gen = spawnSync("openssl", ["req", "-x509", "-newkey", "rsa:2048", "-nodes", "-subj", "/CN=localhost",
    "-days", "1", "-keyout", key, "-out", cert], { encoding: "utf8" });
  expect(gen.status === 0, `openssl could not make the test certificate: ${gen.stderr}`);
  tlsFront = https.createServer({ key: readFileSync(key), cert: readFileSync(cert) }, (req, res) => {
    const headers = { ...req.headers, "x-forwarded-proto": "https", "x-forwarded-host": req.headers.host, "x-forwarded-for": "127.0.0.1" };
    const up = http.request({ host: "127.0.0.1", port: PORT, method: req.method, path: req.url, headers }, (ur) => {
      res.writeHead(ur.statusCode, ur.headers);
      ur.pipe(res);
    });
    up.on("error", () => { try { res.writeHead(502); res.end(); } catch { /* sent */ } });
    res.on("close", () => up.destroy());
    req.pipe(up);
  });
  return new Promise((r) => tlsFront.listen(TLS_PORT, "127.0.0.1", r));
}

// ── seeding ─────────────────────────────────────────────────────────────────
async function seedEmbedded(now) {
  const logs = seedLogLines(now).map((l) => ({
    ts: new Date(l.ts).toISOString(), level: l.level, msg: l.msg, route: `/seed/${l.age}`,
    status: l.level === "error" ? 500 : 200, req_id: l.trace, fields: { session_id: l.session },
  }));
  const spans = [];
  for (const [age, ms] of Object.entries(AGES)) {
    spans.push({ trace_id: traceId(age), span_id: spanId(age, 1), name: `seed-span-${age}`, kind: "server", start_ms: now - ms, duration_ms: 12 });
    spans.push({ trace_id: traceId(age), span_id: spanId(age, 2), parent_id: spanId(age, 1), name: `seed-child-${age}`, start_ms: now - ms + 1, duration_ms: 5, status: age === "30m" ? "error" : "ok" });
  }
  const r = await fetch(ORIGIN + "/_sky/observability/ingest", {
    method: "POST", headers: { "X-Sky-Ingest-Token": INGEST, "content-type": "application/json" },
    body: JSON.stringify({ namespace: "seed", logs, spans }),
  });
  expect(r.status === 202, `ingest answered ${r.status}`);
  // Analytics: the app creates its store on its first event (a page view);
  // then the seed rows go in at their ages.
  await fetch(ORIGIN + "/");
  const db = join(WORK, "analytics.db");
  let ok = false;
  for (let i = 0; i < 40 && !ok; i++) {
    const t = spawnSync("sqlite3", [db, "SELECT count(*) FROM analytics_events;"], { encoding: "utf8" });
    ok = t.status === 0;
    if (!ok) await sleep(250);
  }
  expect(ok, "the app did not create its analytics store");
  const rows = Object.entries(EVENT_AGES).map(([age, ms]) =>
    `INSERT INTO analytics_events (ts, user_id, event, props) VALUES (${now - ms}, 'seed-user-${age}', 'seed_evt_${age}', '{"amount":"USD ${EVENT_USD[age].toFixed(2)}"}');`);
  const ins = spawnSync("sqlite3", [db, "PRAGMA busy_timeout=5000;" + rows.join("")], { encoding: "utf8" });
  expect(ins.status === 0, `sqlite3 insert failed: ${ins.stderr}`);
}

const b64 = (hex) => Buffer.from(hex, "hex").toString("base64");
const kv = (k, v) => ({ key: k, value: { stringValue: v } });
async function seedHub(now) {
  const byService = {};
  for (const l of seedLogLines(now)) {
    (byService[l.service] ||= []).push({
      timeUnixNano: String(l.ts * 1e6), severityText: l.level.toUpperCase(), body: { stringValue: l.msg },
      traceId: b64(l.trace), spanId: b64(spanId(l.age, 1)),
      attributes: [kv("session_id", l.session), kv("req_id", l.trace), kv("route", `/seed/${l.age}`)],
    });
  }
  const logBody = { resourceLogs: Object.entries(byService).map(([svc, recs]) => ({ resource: { attributes: [kv("service.name", svc)] }, scopeLogs: [{ logRecords: recs }] })) };
  let r = await fetch(ORIGIN + "/v1/logs", { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify(logBody) });
  expect(r.status === 200, `hub /v1/logs answered ${r.status}: ${await r.text()}`);
  const spansBy = {};
  for (const [age, ms] of Object.entries(AGES)) {
    const start = (now - ms) * 1e6;
    (spansBy[serviceOf(age)] ||= []).push(
      { traceId: b64(traceId(age)), spanId: b64(spanId(age, 1)), name: `seed-span-${age}`, kind: 2, startTimeUnixNano: String(start), endTimeUnixNano: String(start + 12e6) },
      { traceId: b64(traceId(age)), spanId: b64(spanId(age, 2)), parentSpanId: b64(spanId(age, 1)), name: `seed-child-${age}`, kind: 1, startTimeUnixNano: String(start + 1e6), endTimeUnixNano: String(start + 6e6) },
    );
  }
  const spanBody = { resourceSpans: Object.entries(spansBy).map(([svc, spans]) => ({ resource: { attributes: [kv("service.name", svc)] }, scopeSpans: [{ spans }] })) };
  r = await fetch(ORIGIN + "/v1/traces", { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify(spanBody) });
  expect(r.status === 200, `hub /v1/traces answered ${r.status}: ${await r.text()}`);
  // A recent request log per service, so both have live stats on the
  // Overview cards.
  const liveBody = { resourceLogs: ["svc-alpha", "svc-beta"].map((svc) => ({ resource: { attributes: [kv("service.name", svc)] }, scopeLogs: [{ logRecords: [{ timeUnixNano: String(Date.now() * 1e6), severityText: "INFO", body: { stringValue: `live-${svc}` } }] }] })) };
  r = await fetch(ORIGIN + "/v1/logs", { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify(liveBody) });
  expect(r.status === 200, `hub /v1/logs (live) answered ${r.status}`);
}

// ── page helpers ────────────────────────────────────────────────────────────
let page = null;
const consoleErrors = [];
const failedRequests = [];
const cspViolations = [];
let quietUntil = 0; // a navigation we start ends the page's open requests
const navigating = (ms = 3000) => { quietUntil = Date.now() + ms; };

async function bodyText(p = page) {
  try { return await p.evaluate(() => (document.body ? document.body.innerText : "")); } catch { return ""; }
}
async function waitFor(desc, fn, ms = 10000) {
  const end = Date.now() + ms;
  let last;
  while (Date.now() < end) {
    try {
      last = await fn();
      if (last && last.ok) return last;
    } catch (e) { last = { ok: false, why: String(e.message || e) }; }
    await sleep(250);
  }
  throw new Error(`${desc}: ${last && last.why ? last.why : "timed out"}`);
}
const setEq = (a, b) => a.length === b.length && a.every((x, i) => x === b[i]);
const fmt = (a) => `[${a.join(", ")}]`;

// Seed markers in a text, as sorted unique lists.
const logSeeds = (t) => [...new Set([...t.matchAll(/seedlog-(\w+)-(debug|info|warn|error)/g)].map((m) => `${m[1]}-${m[2]}`))].sort();
const spanSeeds = (t) => [...new Set([...t.matchAll(/seed-span-(\w+)/g)].map((m) => m[1]))].sort();
const eventSeeds = (t) => [...new Set([...t.matchAll(/seed_evt_(\w+)/g)].map((m) => m[1]))].sort();
const expectLogs = (ages, levels) => ages.flatMap((a) => levels.map((l) => `${a}-${l}`)).sort();

async function clickText(text, nth = 0) {
  const loc = page.getByText(text, { exact: true }).nth(nth);
  await loc.click({ timeout: 10000 });
}
const TAB_HEADINGS = {
  Overview: /REQUESTS TOTAL|SCOPE/i, Metrics: /METRICS SNAPSHOT|SCOPE/i, Logs: /RECENT LOG ENTRIES/i,
  Traces: /RECENT TRACES/i, Errors: /TOP ERRORS BY FREQUENCY|SCOPE/i, Analytics: /EVENTS BY NAME/i,
};
async function clickTab(label) {
  // The tab strip is the first element with each tab label. Wait for the
  // tab's panel, so a following action lands on the new tab's controls.
  await clickText(label, 0);
  await waitFor(`${label} tab`, async () => ({ ok: TAB_HEADINGS[label].test(await bodyText()) }));
}
// The chip with `label` in the range row (the first such text on the page).
function rangeChip(label) {
  return page.getByText(label, { exact: true }).first();
}
// The chip's background: Std.Ui may put it on the text's element or on a
// wrapper, so the nearest non-transparent background up the tree.
async function bgOf(loc) {
  return loc.evaluate((el) => {
    for (let e = el, i = 0; e && i < 4; e = e.parentElement, i++) {
      const bg = getComputedStyle(e).backgroundColor;
      if (bg && bg !== "rgba(0, 0, 0, 0)" && bg !== "transparent") return bg;
    }
    return "";
  });
}
const ACCENT = "rgb(126, 182, 255)";
async function searchInputs() {
  return page.locator("input[type=search]");
}
function urlParams() {
  return new URL(page.url()).searchParams;
}

// The newest recorded read of `path` made after `since` that satisfies pred.
async function waitRead(desc, path, since, pred, ms = 8000) {
  return waitFor(desc, async () => {
    const hits = reads.filter((r) => r.path === path && r.at >= since);
    if (!hits.length) return { ok: false, why: `the console made no ${path} read` };
    const r = hits[hits.length - 1];
    const why = pred(r);
    return why === true ? { ok: true, r } : { ok: false, why: `${path}?${r.params} — ${why}` };
  }, ms);
}

// ── the run ─────────────────────────────────────────────────────────────────
let browser = null;
let exitCode = 1;
try {
  if (SCENARIO !== "hub") await startProxy();
  if (SCENARIO === "embedded") await startTlsFront();
  startServer();
  const bootMs = SCENARIO === "hub" ? 240000 : 30000; // the hub builds its daemon once
  if (!(await waitPort(PORT, bootMs))) throw new Error(`server did not listen on ${PORT}\n${server.logTail()}`);

  const now = Date.now();
  if (SCENARIO === "embedded" || SCENARIO === "dev") await seedEmbedded(now);
  if (SCENARIO === "hub") await seedHub(now);

  browser = await pw[BROWSER].launch();
  const ctx = await browser.newContext({ timezoneId: TZ, ignoreHTTPSErrors: true });
  await ctx.addInitScript(() => {
    document.addEventListener("securitypolicyviolation", (e) => {
      console.error("CSP-VIOLATION " + e.violatedDirective + " " + e.blockedURI);
    });
  });
  page = await ctx.newPage();
  page.on("console", (m) => {
    const t = m.text();
    if (t.startsWith("CSP-VIOLATION")) cspViolations.push(t);
    else if (m.type() === "error" && !/status of 401/.test(t)) consoleErrors.push(t.slice(0, 200));
  });
  page.on("requestfailed", (r) => {
    if (Date.now() < quietUntil) return;
    failedRequests.push(`${r.method()} ${r.url().replace(PAGE_ORIGIN, "")} ${r.failure() ? r.failure().errorText : ""}`);
  });
  page.on("response", (r) => {
    // The login page answers 401 by design; any other 4xx/5xx is a failure.
    if (r.status() >= 400 && !(r.status() === 401 && /\/_sky\/console\/?(\?.*)?$/.test(new URL(r.url()).pathname + new URL(r.url()).search))) {
      failedRequests.push(`${r.request().method()} ${r.url().replace(PAGE_ORIGIN, "")} → ${r.status()}`);
    }
  });

  // Volume after the seeds: every request is an access-log line and a span,
  // so the seeds are older than the newest 200 log lines and 100 spans.
  if (SCENARIO !== "hub") {
    for (let i = 0; i < 300; i++) await fetch(`${ORIGIN}/volume-${i}`).catch(() => {});
    // Live data at every level, now: the fixture's "Work" button.
    const visitor = await ctx.newPage();
    await visitor.goto(PAGE_ORIGIN + "/", { waitUntil: "domcontentloaded" });
    await waitFor("the fixture app logs its live lines", async () => {
      await visitor.getByText("Work", { exact: true }).first().click({ timeout: 5000 });
      await sleep(400);
      return { ok: /worked [1-9]/.test(await bodyText(visitor)) };
    }, 20000);
    navigating();
    await visitor.close();
  }

  if (SCENARIO === "dev") await runDev();
  if (SCENARIO === "embedded") await runEmbedded();
  if (SCENARIO === "hub") await runHub();

  await check("page health: no console errors", async () => {
    expect(consoleErrors.length === 0, consoleErrors.slice(0, 5).join(" | "));
  });
  await check("page health: no CSP violations", async () => {
    expect(cspViolations.length === 0, cspViolations.slice(0, 5).join(" | "));
  });
  await check("page health: no failed requests", async () => {
    expect(failedRequests.length === 0, failedRequests.slice(0, 5).join(" | "));
  });
  exitCode = results.every((r) => r.ok) ? 0 : 1;
} catch (e) {
  record("run", false, `${String(e && e.stack ? e.stack : e).slice(0, 600)}\n--- server log ---\n${server ? server.logTail() : ""}`);
  exitCode = 1;
} finally {
  if (browser) await browser.close().catch(() => {});
  if (proxy) proxy.close();
  if (tlsFront) tlsFront.close();
  await stopServer();
  if (SCENARIO === "hub") {
    // `sky console-serve` must not outlive its signal: the hub daemon it
    // starts is the process holding the port. Before v0.27.8 it was a child
    // that a SIGTERM to `sky` left running.
    await check("hub: a SIGTERM to `sky console-serve` stops the hub", async () => {
      const end = Date.now() + 5000;
      while (Date.now() < end && (await portOpen(PORT))) await sleep(200);
      if (!(await portOpen(PORT))) return;
      // Reap the orphan this run started (it listens on this run's port).
      const pids = spawnSync("lsof", ["-t", `-iTCP:${PORT}`, "-sTCP:LISTEN"], { encoding: "utf8" }).stdout?.split(/\s+/).filter(Boolean) || [];
      for (const pid of pids) { try { process.kill(Number(pid), "SIGKILL"); } catch { /* gone */ } }
      throw new Error(`the hub kept listening on ${PORT} after sky console-serve was stopped (orphan pid ${pids.join(",") || "?"})`);
    });
    if (results.length && !results[results.length - 1].ok) exitCode = 1;
  }
  const passed = results.filter((r) => r.ok).length;
  const failed = results.filter((r) => !r.ok).map((r) => r.control);
  console.log(`console-controls-e2e: ${SCENARIO} ${BROWSER} — ${passed}/${results.length} controls passed`);
  if (failed.length) console.log(`console-controls-e2e: FAILED controls: ${failed.join("; ")}`);
  process.exit(exitCode);
}

// ── scenario: dev (no ENV) ──────────────────────────────────────────────────
async function runDev() {
  await check("sign-in: dev mode opens the console with no sign-in", async () => {
    navigating();
    await page.goto(PAGE_ORIGIN + BASE, { waitUntil: "domcontentloaded" });
    await waitFor("the console header", async () => ({ ok: /Sky Console/.test(await bodyText()) && (await page.locator("input[name=token]").count()) === 0 }));
  });
  await check("sign-out: no \"Sign out\" link where there is no sign-in", async () => {
    await waitFor("the live header", async () => ({ ok: /Sky v?\S+ · dev · uptime/.test(await bodyText()), why: (await bodyText()).slice(0, 120) }));
    expect((await page.getByText("Sign out", { exact: true }).count()) === 0, "the dev-mode console shows \"Sign out\", and the link only reloads the console");
  });
}

// ── scenario: embedded (token, production) ──────────────────────────────────
async function selectRange(label) {
  await rangeChip(label).click({ timeout: 10000 });
}
async function activeRangeIs(label) {
  const bg = await bgOf(rangeChip(label));
  return bg === ACCENT;
}

async function checkLogRows(desc, want, since, wantParams) {
  await waitFor(desc + " (page)", async () => {
    const got = logSeeds(await bodyText());
    return { ok: setEq(got, want), why: `page shows ${fmt(got)}, want ${fmt(want)}` };
  });
  if (SCENARIO === "embedded") {
    await waitRead(desc + " (read)", "/_sky/console/api/logs", since, (r) => {
      for (const [k, v] of Object.entries(wantParams)) {
        const got = v === null ? r.params.has(k) : r.params.getAll(k).join("|");
        if (v === null ? got : got !== v) return `param ${k}=${r.params.getAll(k).join("|") || "(absent)"}, want ${v === null ? "(absent)" : v}`;
      }
      const ans = logSeeds(JSON.stringify(r.json || []));
      return setEq(ans, want) ? true : `server answered ${fmt(ans)}, want ${fmt(want)}`;
    });
  }
}

async function runEmbedded() {
  // Sign-in
  await check("sign-in: token form signs in and opens the console", async () => {
    navigating();
    await page.goto(PAGE_ORIGIN + BASE, { waitUntil: "domcontentloaded" });
    const form = page.locator("input[name=token]");
    expect((await form.count()) === 1, "no token form under SKY_CONSOLE_AUTH=token");
    await form.fill(TOKEN);
    navigating();
    await Promise.all([page.waitForNavigation({ waitUntil: "domcontentloaded" }), page.locator("button[type=submit]").click()]);
    await waitFor("the console", async () => ({ ok: /Sky Console/.test(await bodyText()) && /REQUESTS TOTAL/i.test(await bodyText()) }));
  });
  await check("header: shows the live process (version · prod · uptime)", async () => {
    await waitFor("the header", async () => ({ ok: /Sky v?\S+ · prod · uptime \d+/.test(await bodyText()), why: (await bodyText()).slice(0, 120) }));
  });

  // Overview: every KPI card and the System panel
  await check("overview: KPI cards (requests, 5xx rate, log and trace buffers) and the System panel", async () => {
    await waitFor("overview figures", async () => {
      const t = await bodyText();
      const num = (re) => Number((t.match(re) || [])[1] || NaN);
      const req = num(/REQUESTS TOTAL\s*\n\s*(\d+)/i), logs = num(/LOG BUFFER\s*\n\s*(\d+)/i), spans = num(/TRACE BUFFER\s*\n\s*(\d+)/i);
      const rate = /5XX ERROR RATE\s*\n\s*\d+\.\d\d%/i.test(t);
      const sys = /SKY VERSION\s*\n\s*v?\d/i.test(t) && /COMMIT\s*\n\s*\S+/i.test(t) && /BUILT AT\s*\n\s*\d{4}-/i.test(t) && /PRODUCTION MODE\s*\n\s*yes/i.test(t) && /UPTIME\s*\n\s*\d+[smh]/i.test(t);
      return { ok: req >= 300 && logs >= 300 && spans >= 10 && rate && sys, why: `requests=${req} logs=${logs} spans=${spans} rate=${rate} system=${sys}` };
    });
  });

  // Tabs
  const tabs = [
    { label: "Metrics", key: "metrics", heading: /METRICS SNAPSHOT/i, read: "/_sky/console/api/metrics-summary" },
    { label: "Logs", key: "logs", heading: /RECENT LOG ENTRIES/i, read: "/_sky/console/api/logs" },
    { label: "Traces", key: "traces", heading: /RECENT TRACES/i, read: "/_sky/console/api/traces" },
    { label: "Errors", key: "errors", heading: /TOP ERRORS BY FREQUENCY/i, read: "/_sky/console/api/errors" },
    { label: "Analytics", key: "analytics", heading: /EVENTS BY NAME/i, read: "/_sky/console/api/analytics" },
    { label: "Overview", key: null, heading: /REQUESTS TOTAL/i, read: "/_sky/console/api/overview" },
  ];
  for (const t of tabs) {
    await check(`tab: ${t.label} shows its panel, reads its data and is in the URL`, async () => {
      const since = Date.now();
      await clickTab(t.label);
      await waitFor(`${t.label} panel`, async () => ({ ok: t.heading.test(await bodyText()) }));
      await waitRead(`${t.label} read`, t.read, since, () => true);
      await waitFor("URL tab", async () => {
        const got = urlParams().get("tab");
        return { ok: got === t.key, why: `URL tab=${got}, want ${t.key === null ? "(absent)" : t.key}` };
      });
    });
  }

  // Where the range and search show
  const visibility = [
    { tab: "Overview", range: false, search: false },
    { tab: "Metrics", range: false, search: false },
    { tab: "Logs", range: true, search: true },
    { tab: "Traces", range: true, search: true },
    { tab: "Errors", range: true, search: true },
    { tab: "Analytics", range: true, search: false },
  ];
  for (const v of visibility) {
    await check(`filter strip: ${v.tab} shows ${v.range ? "the range chips" : "no range chips"} and ${v.search ? "the search box" : "no search box"}`, async () => {
      await clickTab(v.tab);
      await waitFor(`${v.tab} strip`, async () => {
        const t = await bodyText();
        const hasRange = /(^|\n)Range(\n|$)/.test(t) && (await page.getByText("7d", { exact: true }).count()) > 0;
        const hasSearch = (await page.getByLabel("Search", { exact: true }).count()) > 0 ||
          (await page.locator("input[type=search][placeholder^=Filter], input[type=search][placeholder^=Search]").count()) > 0 && /(^|\n)Search(\n|$)/.test(t);
        return { ok: hasRange === v.range && hasSearch === v.search, why: `range shown=${hasRange}, search shown=${hasSearch}` };
      });
    });
  }

  await check("metrics: the table lists the app's series (type, name, labels, value)", async () => {
    await clickTab("Metrics");
    await waitFor("metrics rows", async () => {
      const t = await bodyText();
      return { ok: /COUNTER\s*\n\s*sky_live_requests_total/i.test(t) && /HISTOGRAM/i.test(t) && /n=\d+ avg=/.test(t), why: t.slice(0, 300) };
    });
  });
  await check("the console does not record itself (no Got* Msg series, no msg_dispatch lines)", async () => {
    await clickTab("Metrics");
    await sleep(4000); // a few of the console's own ticks
    const t = await bodyText();
    expect(!/name=Got(Logs|Overview|Metrics|Traces)/.test(t), "the Metrics tab lists the console's own Msg series");
    await clickTab("Logs");
    await (await searchInputs()).nth(0).fill("msg_dispatch Got");
    await selectRange("All");
    await waitFor("no self lines", async () => ({ ok: /No log entries match/.test(await bodyText()), why: "the Logs tab lists the console's own msg_dispatch lines" }));
    await (await searchInputs()).nth(0).fill("");
    await selectRange("24h");
  });
  await check("error state: a failed read shows the error bar, and it clears on the next good read", async () => {
    await clickTab("Logs");
    failPaths.add("/_sky/console/api/logs");
    const shown = await waitFor("error bar", async () => {
      const t = await bodyText();
      const bar = (t.match(/Telemetry read failed:[^\n]*/) || [""])[0];
      return { ok: /logs/.test(bar) && /500/.test(bar), why: bar ? `bar: ${bar}` : "no error bar after a failed read" };
    }).catch((e) => e);
    failPaths.delete("/_sky/console/api/logs");
    if (shown instanceof Error) throw shown;
    await waitFor("error bar gone", async () => ({ ok: !/Telemetry read failed/.test(await bodyText()), why: "the error bar stayed after a good read" }));
  });

  // Logs: range chips. The newest 200 lines are the volume requests, so the
  // checks search for the seed lines ("seedlog"): the range must apply to
  // the whole store, under the search, not to the newest rows only.
  await clickTab("Logs");
  await (await searchInputs()).nth(0).fill("seedlog");
  for (const r of RANGES) {
    await check(`range on Logs: ${r.label}`, async () => {
      const since = Date.now();
      await selectRange(r.label);
      await waitFor("active chip", async () => ({ ok: await activeRangeIs(r.label), why: `the ${r.label} chip is not highlighted` }));
      const ages = agesIn(AGES, r.ms);
      await checkLogRows(`Logs ${r.label}`, expectLogs(ages, ["info", "warn", "error"]), since, { range: r.key, q: "seedlog" });
      const want = r.key === "24h" ? null : r.key;
      expect(urlParams().get("range") === want, `URL range=${urlParams().get("range")}, want ${want}`);
    });
  }
  await check("range on Logs: the app's own lines (stamped in the server's zone) are in 15m", async () => {
    await selectRange("15m");
    await (await searchInputs()).nth(0).fill("fixture-live");
    await waitFor("live lines in 15m", async () => ({ ok: /fixture-live warn line/.test(await bodyText()), why: "the app's own live log line is not in the 15m range" }));
    await (await searchInputs()).nth(0).fill("seedlog");
  });

  // Logs: level toggles (range All)
  await selectRange("All");
  const levelSteps = [
    { click: "DEBUG", levels: ["debug", "info", "warn", "error"], param: null },
    { click: "WARN", levels: ["debug", "info", "error"], param: "debug,info,error" },
    { click: "INFO", levels: ["debug", "error"], param: "debug,error" },
    { click: "ERROR", levels: ["debug"], param: "debug" },
    { click: "DEBUG", levels: [], param: "none" },
  ];
  for (const s of levelSteps) {
    await check(`level toggle: ${s.click} → ${s.levels.length ? s.levels.join("+") : "nothing"}`, async () => {
      const since = Date.now();
      await clickText(s.click);
      await checkLogRows(`levels ${s.levels.join(",")}`, expectLogs(Object.keys(AGES), s.levels), since, { level: s.param });
      if (!s.levels.length) await waitFor("empty state", async () => ({ ok: /No log entries match/.test(await bodyText()) }));
    });
  }
  await check("logs clear: resets the levels, search and session", async () => {
    const since = Date.now();
    await clickText("clear");
    // Back to the default levels (debug off): level=info,warn,error.
    await checkLogRows("after clear", expectLogs(Object.keys(AGES), ["info", "warn", "error"]), since, { level: "info,warn,error", session: null });
  });

  // Logs: the tab's own search box
  await check("logs search box: finds older lines over the whole store", async () => {
    const since = Date.now();
    await (await searchInputs()).nth(1).fill("seedlog-3d");
    // Both searches go to the server: the global one and the tab's own.
    await checkLogRows("logs search seedlog-3d", expectLogs(["3d"], ["info", "warn", "error"]), since, { q: "seedlog|seedlog-3d" });
    await clickText("clear");
    await waitFor("search cleared", async () => ({ ok: (await (await searchInputs()).nth(1).inputValue()) === "" }));
  });

  // Logs: session pivot
  await check("session badge: pivots the logs to one session", async () => {
    const since = Date.now();
    await clickText(sessionId("3d").slice(0, 8));
    await checkLogRows("session pivot", expectLogs(["3d"], ["info", "warn", "error"]), since, { session: sessionId("3d") });
    await waitFor("session label", async () => ({ ok: /Filtering by session/.test(await bodyText()) }));
    await clickText("clear");
    await waitFor("session cleared", async () => ({ ok: !/Filtering by session/.test(await bodyText()) }));
  });

  // Global search on Logs (until now it held "seedlog")
  await check("global search on Logs: filters over the whole store and is in the URL", async () => {
    const since = Date.now();
    await (await searchInputs()).nth(0).fill("seedlog-10d");
    await checkLogRows("global search logs", expectLogs(["10d"], ["info", "warn", "error"]), since, { q: "seedlog-10d" });
    await waitFor("URL q", async () => ({ ok: urlParams().get("q") === "seedlog-10d", why: `URL q=${urlParams().get("q")}` }));
  });

  for (const sc of [
    { box: 0, name: "global search", base: "seedlog" },
    { box: 1, name: "logs search box", base: "" },
  ]) {
    await check(`${sc.name} on Logs: partial match, no match (empty state) and clearing`, async () => {
      const box = (await searchInputs()).nth(sc.box);
      const other = (await searchInputs()).nth(1 - sc.box);
      const keep = await other.inputValue();
      await other.fill(sc.base);
      await box.fill("log-3d");
      await waitFor("partial", async () => { const g = logSeeds(await bodyText()); return { ok: setEq(g, expectLogs(["3d"], ["info", "warn", "error"])), why: `"log-3d" shows ${fmt(g)}` }; });
      await box.fill("zz-no-such-line");
      await waitFor("no match", async () => ({ ok: /No log entries match/.test(await bodyText()) && logSeeds(await bodyText()).length === 0, why: "no empty state for a search with no match" }));
      await box.fill("");
      await other.fill("seedlog");
      await waitFor("cleared", async () => { const g = logSeeds(await bodyText()); return { ok: setEq(g, expectLogs(Object.keys(AGES), ["info", "warn", "error"])), why: `after clearing: ${fmt(g)}` }; });
      if (sc.box === 0) await other.fill(keep);
    });
  }
  await (await searchInputs()).nth(1).fill("");
  await (await searchInputs()).nth(0).fill("seedlog-10d");

  // Trace pivot from a log row (the 10d row is visible under the search)
  await check("trace badge: pivots to the Traces tab, searched for that trace", async () => {
    const since = Date.now();
    await clickText("trace " + traceId("10d").slice(0, 8));
    await waitFor("Traces tab", async () => ({ ok: /RECENT TRACES/i.test(await bodyText()) && urlParams().get("tab") === "traces", why: `tab=${urlParams().get("tab")}` }));
    // The global search still applies; widen it to every seed trace so
    // only the pivot narrows.
    await (await searchInputs()).nth(0).fill("seed-span");
    await waitFor("pivot rows", async () => {
      const got = spanSeeds(await bodyText());
      return { ok: setEq(got, ["10d"]), why: `Traces shows ${fmt(got)}, want [10d]` };
    });
    expect((await (await searchInputs()).nth(1).inputValue()) === traceId("10d"), "the Traces search box does not hold the trace id");
    await waitRead("pivot read", "/_sky/console/api/traces", since, (r) =>
      r.params.getAll("q").includes(traceId("10d")) ? (setEq(spanSeeds(JSON.stringify(r.json || [])), ["10d"]) ? true : `server answered ${fmt(spanSeeds(JSON.stringify(r.json || [])))}`) : `no q=${traceId("10d")}`);
  });
  await check("traces clear: clears the Traces search", async () => {
    await clickText("clear");
    await waitFor("all traces", async () => {
      const got = spanSeeds(await bodyText());
      return { ok: setEq(got, Object.keys(AGES).sort()), why: `Traces shows ${fmt(got)}` };
    });
  });

  // Traces: ranges (under the "seed-span" search, as on Logs), search box,
  // global search
  for (const r of RANGES) {
    await check(`range on Traces: ${r.label}`, async () => {
      const since = Date.now();
      await selectRange(r.label);
      const want = agesIn(AGES, r.ms);
      await waitFor(`Traces ${r.label}`, async () => {
        const got = spanSeeds(await bodyText());
        return { ok: setEq(got, want), why: `page shows ${fmt(got)}, want ${fmt(want)}` };
      });
      await waitRead(`Traces ${r.label} read`, "/_sky/console/api/traces", since, (q) =>
        q.params.get("range") !== r.key || !q.params.getAll("q").includes("seed-span") ? `range=${q.params.get("range")} q=${q.params.getAll("q")}` : setEq(spanSeeds(JSON.stringify(q.json || [])), want) ? true : `server answered ${fmt(spanSeeds(JSON.stringify(q.json || [])))}`);
    });
  }
  await selectRange("All");
  await check("traces search box: keeps whole traces whose span matches", async () => {
    await (await searchInputs()).nth(1).fill("seed-child-5h");
    await waitFor("traces search", async () => {
      const t = await bodyText();
      const got = spanSeeds(t);
      return { ok: setEq(got, ["5h"]) && /seed-child-5h/.test(t), why: `Traces shows ${fmt(got)}` };
    });
    await clickText("clear");
  });
  await check("global search on Traces", async () => {
    const since = Date.now();
    await (await searchInputs()).nth(0).fill("seed-span-30m");
    await waitFor("global search traces", async () => {
      const got = spanSeeds(await bodyText());
      return { ok: setEq(got, ["30m"]), why: `Traces shows ${fmt(got)}` };
    });
    await waitRead("traces q read", "/_sky/console/api/traces", since, (r) => (r.params.getAll("q").includes("seed-span-30m") ? true : `q=${r.params.getAll("q")}`));
    await (await searchInputs()).nth(0).fill("");
  });

  await check("traces: the span tree (root, indented child, ERR status, duration)", async () => {
    await (await searchInputs()).nth(0).fill("seed-span-30m");
    await waitFor("tree", async () => {
      const t = await bodyText();
      return { ok: /trace [0-9a-f]{8}/.test(t) && /seed-span-30m · 2 spans/.test(t) && /└ seed-child-30m/.test(t) && /\bERR\b/.test(t) && /12ms/.test(t), why: t.slice(t.indexOf("RECENT TRACES"), t.indexOf("RECENT TRACES") + 300) };
    });
    await (await searchInputs()).nth(0).fill("");
  });
  for (const box of [0, 1]) {
    await check(`${box ? "traces search box" : "global search"} on Traces: partial match, no match (empty state) and clearing`, async () => {
      const b = (await searchInputs()).nth(box);
      await b.fill("span-1");
      await waitFor("partial", async () => { const g = spanSeeds(await bodyText()); return { ok: setEq(g, ["10d"]), why: `"span-1" shows ${fmt(g)}` }; });
      await b.fill("zz-no-such-span");
      await waitFor("no match", async () => ({ ok: /No traces match the filter/.test(await bodyText()), why: "no empty state" }));
      await b.fill("");
      await (await searchInputs()).nth(0).fill("seed-span");
      await waitFor("cleared", async () => { const g = spanSeeds(await bodyText()); return { ok: setEq(g, Object.keys(AGES).sort()), why: `after clearing: ${fmt(g)}` }; });
      await (await searchInputs()).nth(0).fill("");
    });
  }

  // Errors
  await clickTab("Errors");
  for (const r of RANGES) {
    await check(`range on Errors: ${r.label}`, async () => {
      const since = Date.now();
      await selectRange(r.label);
      const want = expectLogs(agesIn(AGES, r.ms), ["warn", "error"]);
      await waitFor(`Errors ${r.label}`, async () => {
        const got = logSeeds(await bodyText());
        return { ok: setEq(got, want), why: `page shows ${fmt(got)}, want ${fmt(want)}` };
      });
      await waitRead(`Errors ${r.label} read`, "/_sky/console/api/errors", since, (q) =>
        q.params.get("range") !== r.key ? `range=${q.params.get("range")}` : setEq(logSeeds(JSON.stringify(q.json || [])), want) ? true : `server answered ${fmt(logSeeds(JSON.stringify(q.json || [])))}`);
    });
  }
  await check("global search on Errors", async () => {
    await selectRange("All");
    await (await searchInputs()).nth(0).fill("seedlog-30m");
    await waitFor("errors search", async () => {
      const got = logSeeds(await bodyText());
      return { ok: setEq(got, ["30m-error", "30m-warn"]), why: `Errors shows ${fmt(got)}` };
    });
    await (await searchInputs()).nth(0).fill("");
  });

  await check("global search on Errors: partial match, no match (empty state), count column and clearing", async () => {
    await selectRange("All");
    const b = (await searchInputs()).nth(0);
    await b.fill("dlog-5");
    await waitFor("partial", async () => { const t = await bodyText(); const g = logSeeds(t); return { ok: setEq(g, ["5h-error", "5h-warn"]) && /×1\s*\n?\s*seedlog-5h/.test(t), why: `"dlog-5" shows ${fmt(g)}` }; });
    await b.fill("zz-no-such-error");
    await waitFor("no match", async () => ({ ok: /No errors recorded/.test(await bodyText()), why: "no empty state" }));
    await b.fill("");
    await waitFor("cleared", async () => { const g = logSeeds(await bodyText()); return { ok: setEq(g, expectLogs(Object.keys(AGES), ["warn", "error"])), why: `after clearing: ${fmt(g)}` }; });
  });

  // Analytics
  await clickTab("Analytics");
  const windowWords = { "15m": "15 minutes", "1h": "1 hour", "24h": "24 hours", "7d": "7 days", all: "30 days" };
  const figures = {};
  for (const r of RANGES) {
    await check(`range on Analytics: ${r.label} (events by name, totals, identified users, revenue, labels)`, async () => {
      const since = Date.now();
      await selectRange(r.label);
      const want = agesIn(EVENT_AGES, r.ms, 30 * DAY);
      const usd = want.reduce((a, k) => a + EVENT_USD[k], 0).toFixed(2).replace(/\.?0+$/, "");
      await waitFor(`Analytics ${r.label}`, async () => {
        const t = await bodyText();
        const got = eventSeeds(t);
        const label = new RegExp(`LAST ${windowWords[r.key]}`, "i").test(t);
        const total = Number((t.match(/EVENTS · LAST [^\n]*\n\s*(\d+)/i) || [])[1]);
        const users = Number((t.match(/IDENTIFIED USERS · LAST [^\n]*\n\s*(\d+)/i) || [])[1]);
        const rev = (t.match(/USD\s*\n?\s*≥?\s*([\d.]+)/) || [])[1] || "";
        figures[r.key] = { total, users };
        return { ok: setEq(got, want) && label && rev.replace(/\.?0+$/, "") === usd, why: `events ${fmt(got)} (want ${fmt(want)}), label "${windowWords[r.key]}" ${label ? "shown" : "missing"}, USD ${rev} (want ${usd})` };
      });
      await waitRead(`Analytics ${r.label} read`, "/_sky/console/api/analytics", since, (q) => (q.params.get("range") === r.key ? true : `range=${q.params.get("range")}`));
    });
  }
  await check("analytics: totals and identified users grow by exactly the seeds each wider range adds", async () => {
    const order = ["15m", "1h", "24h", "7d", "all"];
    for (let i = 1; i < order.length; i++) {
      const a = figures[order[i - 1]], b = figures[order[i]];
      expect(a && b, `missing figures for ${order[i - 1]} / ${order[i]}`);
      expect(b.total - a.total === 1, `events ${order[i - 1]}=${a.total} → ${order[i]}=${b.total}, want +1`);
      expect(b.users - a.users === 1, `identified users ${order[i - 1]}=${a.users} → ${order[i]}=${b.users}, want +1`);
    }
  });

  // Auto-refresh: new data appears with no interaction
  await check("auto-refresh: a new log line appears without a click", async () => {
    await clickTab("Logs");
    await selectRange("15m");
    await (await searchInputs()).nth(0).fill("fixture-live error line");
    const count = async () => (await bodyText()).split("fixture-live error line").length - 1;
    const before = await waitFor("live line", async () => { const n = await count(); return { ok: n > 0, n }; });
    const visitor = await page.context().newPage();
    await visitor.goto(PAGE_ORIGIN + "/", { waitUntil: "domcontentloaded" });
    await visitor.getByText("Work", { exact: true }).first().click();
    await waitFor("refreshed", async () => { const n = await count(); return { ok: n > before.n, why: `still ${n} line(s)` }; }, 15000);
    navigating();
    await visitor.close();
    await (await searchInputs()).nth(0).fill("");
  });

  // Reload keeps the state (the session and the URL agree)
  await check("reload: keeps the tab, range and search", async () => {
    await clickTab("Errors");
    await selectRange("7d");
    await (await searchInputs()).nth(0).fill("seedlog-3d");
    await waitFor("URL", async () => ({ ok: urlParams().get("tab") === "errors" && urlParams().get("range") === "7d" && urlParams().get("q") === "seedlog-3d", why: page.url() }));
    navigating();
    await page.reload({ waitUntil: "domcontentloaded" });
    await waitFor("after reload", async () => {
      const got = logSeeds(await bodyText());
      return { ok: setEq(got, ["3d-error", "3d-warn"]) && (await activeRangeIs("7d")), why: `Errors shows ${fmt(got)}` };
    });
    await (await searchInputs()).nth(0).fill("");
  });

  // A console link opens on the state it names, through the sign-in
  await check("console link: a fresh browser opens ?tab=&range=&q= through the sign-in", async () => {
    const ctx2 = await browser.newContext({ timezoneId: TZ, ignoreHTTPSErrors: true });
    const p2 = await ctx2.newPage();
    const errs = [];
    p2.on("console", (m) => { if (m.type() === "error" && !/status of 401/.test(m.text())) errs.push(m.text()); });
    try {
      await p2.goto(PAGE_ORIGIN + BASE + "?tab=logs&range=7d&q=seedlog-3d", { waitUntil: "domcontentloaded" });
      await p2.locator("input[name=token]").fill(TOKEN);
      await Promise.all([p2.waitForNavigation({ waitUntil: "domcontentloaded" }), p2.locator("button[type=submit]").click()]);
      await waitFor("link state", async () => {
        const u = new URL(p2.url()).searchParams;
        const got = logSeeds(await bodyText(p2));
        const want = expectLogs(["3d"], ["info", "warn", "error"]);
        return { ok: u.get("tab") === "logs" && u.get("range") === "7d" && setEq(got, want), why: `URL ${p2.url()}, Logs shows ${fmt(got)} (want ${fmt(want)})` };
      });
      expect(errs.length === 0, errs.join(" | "));
    } catch (e) {
      await ctx2.close();
      throw e;
    }
    await ctx2.close();
  });

  // Sign out
  await check("sign-out: ends the console session", async () => {
    navigating(5000);
    await Promise.all([page.waitForNavigation({ waitUntil: "domcontentloaded" }), clickText("Sign out")]);
    await waitFor("login form", async () => ({ ok: (await page.locator("input[name=token]").count()) === 1 }));
    navigating();
    await page.goto(PAGE_ORIGIN + BASE, { waitUntil: "domcontentloaded" });
    expect((await page.locator("input[name=token]").count()) === 1, "the console opened again without a sign-in");
  });
}

// ── scenario: hub ───────────────────────────────────────────────────────────
async function runHub() {
  await check("hub: the console opens with both services", async () => {
    navigating();
    await page.goto(PAGE_ORIGIN + BASE, { waitUntil: "domcontentloaded" });
    await waitFor("service cards", async () => {
      const t = await bodyText();
      return { ok: /svc-alpha/.test(t) && /svc-beta/.test(t), why: t.slice(0, 200) };
    }, 30000);
  });
  await check("hub service card: selects the service (scope + URL)", async () => {
    // The card's title is the second "svc-beta" text (the first is the
    // service chip; the aggregate pane lists it again after the cards).
    await page.getByText("svc-beta", { exact: true }).nth(1).click();
    await waitFor("scope", async () => ({ ok: /SCOPE\s*\n?\s*svc-beta/i.test(await bodyText()) && urlParams().get("service") === "svc-beta", why: `URL ${page.url()}` }));
  });
  await check("hub \"All services\" chip: clears the service", async () => {
    await clickText("All services");
    await waitFor("scope all", async () => ({ ok: /SCOPE\s*\n?\s*All services/i.test(await bodyText()) && !urlParams().has("service"), why: page.url() }));
  });

  await clickTab("Logs");
  await selectRange("All");
  const svcAges = (svc) => Object.keys(AGES).filter((a) => serviceOf(a) === svc).sort();
  await check("hub service chip: scopes the Logs to one service", async () => {
    await page.getByText("svc-alpha", { exact: true }).first().click();
    await checkLogRows("svc-alpha logs", expectLogs(svcAges("svc-alpha"), ["info", "warn", "error"]), Date.now(), {});
    await page.getByText("All", { exact: true }).nth(1).click();
    await checkLogRows("all services logs", expectLogs(Object.keys(AGES), ["info", "warn", "error"]), Date.now(), {});
  });
  for (const r of RANGES) {
    await check(`hub range on Logs: ${r.label}`, async () => {
      await selectRange(r.label);
      await checkLogRows(`hub Logs ${r.label}`, expectLogs(agesIn(AGES, r.ms), ["info", "warn", "error"]), Date.now(), {});
    });
  }
  await selectRange("All");
  for (const s of [
    { click: "WARN", levels: ["info", "error"] },
    { click: "DEBUG", levels: ["debug", "info", "error"] },
    { click: "INFO", levels: ["debug", "error"] },
  ]) {
    await check(`hub level toggle: ${s.click} → ${s.levels.join("+")}`, async () => {
      await clickText(s.click);
      await checkLogRows(`hub levels ${s.levels}`, expectLogs(Object.keys(AGES), s.levels), Date.now(), {});
    });
  }
  await check("hub logs clear + search box", async () => {
    await clickText("clear");
    await (await searchInputs()).nth(1).fill("seedlog-30m");
    await checkLogRows("hub logs search", expectLogs(["30m"], ["info", "warn", "error"]), Date.now(), {});
    await clickText("clear");
  });
  await check("hub session badge: pivots to one session", async () => {
    await clickText(sessionId("5h").slice(0, 8));
    await checkLogRows("hub session", expectLogs(["5h"], ["info", "warn", "error"]), Date.now(), {});
    await clickText("clear");
  });
  await check("hub global search on Logs", async () => {
    await (await searchInputs()).nth(0).fill("seedlog-2m");
    await checkLogRows("hub global search", expectLogs(["2m"], ["info", "warn", "error"]), Date.now(), {});
  });
  await check("hub trace badge: pivots to the trace", async () => {
    await clickText("trace " + traceId("2m").slice(0, 8));
    await (await searchInputs()).nth(0).fill("");
    await waitFor("hub pivot", async () => {
      const got = spanSeeds(await bodyText());
      return { ok: /RECENT TRACES/i.test(await bodyText()) && setEq(got, ["2m"]), why: `Traces shows ${fmt(got)}` };
    });
    await clickText("clear");
  });
  for (const r of RANGES) {
    await check(`hub range on Traces: ${r.label}`, async () => {
      await selectRange(r.label);
      const want = agesIn(AGES, r.ms);
      await waitFor(`hub traces ${r.label}`, async () => {
        const got = spanSeeds(await bodyText());
        return { ok: setEq(got, want), why: `Traces shows ${fmt(got)}, want ${fmt(want)}` };
      });
    });
  }
  await clickTab("Errors");
  for (const r of [RANGES[0], RANGES[1], RANGES[4]]) {
    await check(`hub range on Errors: ${r.label}`, async () => {
      await selectRange(r.label);
      const want = expectLogs(agesIn(AGES, r.ms), ["error"]);
      await waitFor(`hub errors ${r.label}`, async () => {
        const got = logSeeds(await bodyText());
        return { ok: setEq(got, want), why: `Errors shows ${fmt(got)}, want ${fmt(want)}` };
      });
    });
  }
  await check("hub searches: partial match, no match and clearing (global, Logs box, Traces box)", async () => {
    await clickTab("Logs");
    await selectRange("All");
    await (await searchInputs()).nth(1).fill("");
    const g = (await searchInputs()).nth(0);
    await g.fill("dlog-30");
    await checkLogRows("hub partial", expectLogs(["30m"], ["info", "warn", "error"]), Date.now(), {});
    await g.fill("zz-no-such");
    await waitFor("hub no match", async () => ({ ok: logSeeds(await bodyText()).length === 0 && /No log entries/.test(await bodyText()), why: "no empty state" }));
    await g.fill("");
    await (await searchInputs()).nth(1).fill("dlog-5");
    await checkLogRows("hub logs box partial", expectLogs(["5h"], ["info", "warn", "error"]), Date.now(), {});
    await (await searchInputs()).nth(1).fill("");
    await checkLogRows("hub cleared", expectLogs(Object.keys(AGES), ["info", "warn", "error"]), Date.now(), {});
    await clickTab("Traces");
    await (await searchInputs()).nth(1).fill("child-5");
    await waitFor("hub traces partial", async () => { const t = await bodyText(); const s2 = spanSeeds(t); return { ok: setEq(s2, ["5h"]), why: `${fmt(s2)} url=${page.url()} boxes=${await (await searchInputs()).nth(0).inputValue()}|${await (await searchInputs()).nth(1).inputValue()} ${t.slice(0, 400).replace(/\n/g, " | ")}` }; });
    await (await searchInputs()).nth(1).fill("zz-no-such");
    await waitFor("hub traces none", async () => ({ ok: /No traces match the filter/.test(await bodyText()), why: "no empty state" }));
    await (await searchInputs()).nth(1).fill("");
    await waitFor("hub traces cleared", async () => { const s2 = spanSeeds(await bodyText()); return { ok: setEq(s2, Object.keys(AGES).sort()), why: fmt(s2) }; });
  });
  await check("hub overview: service cards, sparklines, aggregate charts, error-rate panel, focused pane", async () => {
    await clickTab("Overview");
    await clickText("All services");
    await waitFor("overview panels", async () => {
      const t = await bodyText();
      const svgs = await page.locator("svg").count();
      return { ok: /REQUESTS \/ SECOND \(PER SERVICE\)/i.test(t) && /P95 LATENCY/i.test(t) && /ERROR RATE \(5XX\) BY SERVICE/i.test(t) && /REQS\/S/.test(t) && svgs >= 1, why: `svg=${svgs}; ${t.slice(0, 200)}` };
    });
    await page.getByText("svc-alpha", { exact: true }).nth(1).click();
    await waitFor("focused", async () => {
      const t = await bodyText();
      return { ok: /SCOPE\s*\n?\s*svc-alpha/i.test(t) && /REQUESTS \/ SECOND/i.test(t) && !/once B6 ships/.test(t), why: t.slice(0, 300) };
    });
    await clickText("All services");
  });
  await check("hub tabs: Metrics and Analytics open", async () => {
    await clickTab("Metrics");
    await waitFor("metrics", async () => ({ ok: /SCOPE/i.test(await bodyText()) && urlParams().get("tab") === "metrics", why: page.url() }));
    await clickTab("Analytics");
    // The hub has no product analytics: the tab shows its empty states.
    await waitFor("analytics", async () => ({ ok: /EVENTS BY NAME/i.test(await bodyText()) && /No events captured yet/.test(await bodyText()) && /Nothing recent to show/.test(await bodyText()) }));
  });
}
