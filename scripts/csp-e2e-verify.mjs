#!/usr/bin/env node
// scripts/csp-e2e-verify.mjs
//
// Browser e2e: every page Sky serves works under a strict Content-Security-Policy
// that allows NO inline executable script:
//
//   default-src 'self'; script-src 'self' 'wasm-unsafe-eval';
//   style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; connect-src 'self'
//
// Two ways to apply the policy, both driven here:
//
//   --via proxy   A tiny reverse proxy (this script) sits in front of the app and
//                 sets exactly that header on every response, like a Caddy /
//                 nginx deployment does. Streaming responses (SSE) are piped.
//   --via strict  No proxy. The app runs with SKY_CSP=strict and must send a
//                 strict policy itself; the page is loaded directly.
//
// Every scenario asserts ZERO `securitypolicyviolation` events (reported from the
// page through an exposed binding, so a reload cannot lose one) and ZERO console
// messages naming the Content Security Policy, plus a working app:
//
//   console  the Sky Console (/_sky/console on a Sky.Live app): all six tabs
//            switch over the Live wire.
//   counter  examples/09-live-counter: the SSE tick advances the count, a click
//            round-trips, sky-nav navigation works.
//   forum    examples/19-skyforum: a Std.Ui form submits over the Live wire with
//            its CSRF token and the signed-in name renders.
//   todos    examples/60-spa-todos: the wasm client boots and an RPC adds a todo.
//   notes    examples/62-app-notes (auto-split, SSR): the client hydrates and the
//            Create + Save RPCs persist a note.
//
// Usage: node scripts/csp-e2e-verify.mjs <scenario> <app-binary> --port N
//          [--via proxy|strict] [--cwd DIR] [--env K=V ...]
// Exit: 0 PASS · 2 FAIL · 1 harness error.
import pw from "playwright";
const { chromium } = pw;
import { spawn } from "node:child_process";
import http from "node:http";
import { dirname, join } from "node:path";
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";

export const STRICT_CSP =
  "default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; style-src 'self' 'unsafe-inline'; " +
  "img-src 'self' data: blob:; connect-src 'self'";

function arg(name, def) {
  const i = process.argv.indexOf(name);
  return i >= 0 && process.argv[i + 1] ? process.argv[i + 1] : def;
}
function args(name) {
  const out = [];
  process.argv.forEach((a, i) => {
    if (a === name && process.argv[i + 1]) out.push(process.argv[i + 1]);
  });
  return out;
}

const SCENARIOS = ["console", "counter", "forum", "todos", "notes"];
const MODE = process.argv[2];
const APP = process.argv[3];
if (!SCENARIOS.includes(MODE) || !APP) {
  console.error(`usage: csp-e2e-verify.mjs <${SCENARIOS.join("|")}> <app-binary> --port N [--via proxy|strict] [--cwd DIR]`);
  process.exit(1);
}
const VIA = arg("--via", "proxy");
if (!["proxy", "strict"].includes(VIA)) {
  console.error(`csp-e2e: --via must be proxy or strict, got ${VIA}`);
  process.exit(1);
}
const APP_PORT = Number(arg("--port", "9520"));
const PROXY_PORT = APP_PORT + 1;
const PAGE_PORT = VIA === "proxy" ? PROXY_PORT : APP_PORT;
const ORIGIN = `http://127.0.0.1:${PAGE_PORT}`;
const CWD = arg("--cwd", dirname(dirname(APP)));
const DB = join(mkdtempSync(join(tmpdir(), "sky-csp-e2e-")), "app.db");
const TAG = `${MODE}/${VIA}`;

for (const p of VIA === "proxy" ? [APP_PORT, PROXY_PORT] : [APP_PORT]) {
  try {
    await fetch(`http://127.0.0.1:${p}/`, { signal: AbortSignal.timeout(1000) });
    console.error(`${TAG}: harness error: port ${p} is already serving; stop that process first`);
    process.exit(1);
  } catch (_) {}
}

const extraEnv = {};
for (const kv of args("--env")) {
  const i = kv.indexOf("=");
  if (i > 0) extraEnv[kv.slice(0, i)] = kv.slice(i + 1);
}
const env = {
  ...process.env,
  SKY_LIVE_PORT: String(APP_PORT),
  PORT: String(APP_PORT),
  TODOS_PORT: String(APP_PORT),
  SKY_DB_PATH: DB,
  ENV: "development",
  ...extraEnv,
};
// The strict pass opts the runtime in; the proxy pass must work WITHOUT it (the
// proxy is the only source of the policy there).
if (VIA === "strict") env.SKY_CSP = "strict";
else delete env.SKY_CSP;

const proc = spawn(APP, [], { cwd: CWD, env });
let serverLog = "";
proc.stdout.on("data", (d) => (serverLog += d));
proc.stderr.on("data", (d) => (serverLog += d));
let exited = null;
proc.on("exit", (code, sig) => (exited = { code, sig }));

// ── the proxy: pipes every request, sets exactly STRICT_CSP on every response ──
let proxy = null;
if (VIA === "proxy") {
  proxy = http.createServer((req, res) => {
    const up = http.request(
      { host: "127.0.0.1", port: APP_PORT, method: req.method, path: req.url, headers: req.headers },
      (ur) => {
        const headers = { ...ur.headers, "content-security-policy": STRICT_CSP };
        res.writeHead(ur.statusCode || 502, headers);
        ur.pipe(res);
      }
    );
    up.on("error", (e) => {
      if (!res.headersSent) res.writeHead(502, { "content-type": "text/plain" });
      res.end("proxy error: " + e.message);
    });
    req.pipe(up);
  });
  await new Promise((r) => proxy.listen(PROXY_PORT, "127.0.0.1", r));
}

const failures = [];
function check(step, ok, detail) {
  console.log(`${ok ? "ok  " : "FAIL"} [${TAG}] ${step}${detail ? ": " + detail : ""}`);
  if (!ok) failures.push(step);
}

async function waitListening(path) {
  for (let i = 0; i < 160; i++) {
    if (exited) throw new Error(`app exited early (${JSON.stringify(exited)})\n${serverLog}`);
    try {
      const r = await fetch(ORIGIN + path);
      if (r.status < 500) return r;
    } catch (_) {}
    await new Promise((r) => setTimeout(r, 250));
  }
  throw new Error("app never listened\n" + serverLog);
}

const violations = [];
const cspConsole = [];
async function newPage(browser) {
  const context = await browser.newContext({ viewport: { width: 1400, height: 900 } });
  await context.exposeBinding("__skyCspReport", (_src, v) => {
    violations.push(v);
    console.log(`FAIL [${TAG}] securitypolicyviolation: ${v}`);
  });
  await context.addInitScript(() => {
    document.addEventListener(
      "securitypolicyviolation",
      (e) => {
        try {
          window.__skyCspReport(
            `${e.violatedDirective} blocked=${e.blockedURI} src=${e.sourceFile}:${e.lineNumber} sample=${e.sample}`
          );
        } catch (_) {}
      },
      true
    );
  });
  const page = await context.newPage();
  page.on("console", (m) => {
    const t = m.text();
    if (/Content[ -]Security[ -]Policy/i.test(t)) {
      cspConsole.push(t);
      console.log(`FAIL [${TAG}] console: ${t}`);
    }
  });
  page.on("pageerror", (e) => {
    failures.push(`[pageerror] ${e.message}`);
    console.log(`FAIL [${TAG}] pageerror: ${e.message}`);
  });
  return page;
}

async function expectPolicyHeader(path) {
  const r = await fetch(ORIGIN + path);
  const csp = r.headers.get("content-security-policy") || "";
  const scriptSrc = csp.split(";").map((s) => s.trim()).find((s) => s.startsWith("script-src ")) || "";
  const loose = /'unsafe-inline'|'unsafe-eval'|'sha(256|384|512)-|'nonce-/.test(scriptSrc);
  check(`${path} is served with script-src 'self' and no inline / eval / hash allowance`,
    scriptSrc.includes("'self'") && !loose, csp || "(no Content-Security-Policy header)");
}

async function textOf(page, sel) {
  return (await page.locator(sel).first().innerText({ timeout: 10000 })).trim();
}

async function waitFor(fn, ms, what) {
  const until = Date.now() + ms;
  let last;
  while (Date.now() < until) {
    try {
      last = await fn();
      if (last) return last;
    } catch (e) {
      last = e.message;
    }
    await new Promise((r) => setTimeout(r, 150));
  }
  throw new Error(`timed out waiting for ${what} (last=${JSON.stringify(last)})`);
}

// ── scenarios ──────────────────────────────────────────────────────────────
async function scenarioCounter(browser) {
  await waitListening("/");
  await expectPolicyHeader("/");
  const page = await newPage(browser);
  await page.goto(ORIGIN + "/", { waitUntil: "load" });
  const v0 = Number(await textOf(page, ".count-value"));
  // Time.every 1000 Tick reaches the page ONLY over the SSE stream.
  const v1 = await waitFor(async () => {
    const v = Number(await textOf(page, ".count-value"));
    return v >= v0 + 2 ? v : 0;
  }, 8000, "the SSE tick to advance the count").catch((e) => (check("the SSE tick advances the count", false, e.message), null));
  if (v1 !== null) check("the SSE tick advances the count", true, `${v0} -> ${v1}`);
  // A click posts the event over fetch and applies the returned patch.
  const posted = page.waitForResponse((r) => r.url().includes("/_sky/event") && r.request().method() === "POST", { timeout: 8000 });
  await page.locator("button", { hasText: "+" }).click();
  const ev = await posted.catch(() => null);
  check("a click posts the event and the server accepts it", !!ev && ev.status() === 200, ev ? String(ev.status()) : "no POST");
  await page.waitForTimeout(300);
  const before = Number(await textOf(page, ".count-value"));
  await page.locator("button", { hasText: "Reset" }).click();
  const reset = before <= 1 ? "" : await waitFor(async () => {
    const v = Number(await textOf(page, ".count-value"));
    return v <= 1 ? String(v) : "";
  }, 4000, "Reset to apply").catch(() => "");
  check("a click round-trips (Reset)", reset !== "", reset);
  // Navigation over the Live wire.
  await page.locator("button", { hasText: "About" }).click();
  const about = await waitFor(async () => (await page.locator("text=About Sky.Live").count()) > 0, 5000, "About page").catch(() => false);
  check("sky navigation renders the About page", !!about);
}

async function scenarioConsole(browser) {
  await waitListening("/");
  // Warm the app so the console has telemetry to show.
  for (let i = 0; i < 3; i++) await fetch(ORIGIN + "/").catch(() => {});
  await waitFor(async () => (await fetch(ORIGIN + "/_sky/console/")).ok, 30000, "the console mount");
  await expectPolicyHeader("/_sky/console/");
  const page = await newPage(browser);
  await page.goto(ORIGIN + "/_sky/console/", { waitUntil: "load" });
  await page.waitForTimeout(1500);
  const tabs = ["Overview", "Metrics", "Logs", "Traces", "Errors", "Analytics"];
  let prev = await page.locator("body").innerText();
  for (const tab of tabs.slice(1).concat(["Overview"])) {
    await page.locator("text=" + tab).first().click({ timeout: 10000 });
    const changed = await waitFor(async () => {
      const now = await page.locator("body").innerText();
      return now !== prev ? now : "";
    }, 6000, `the ${tab} tab to render`).catch(() => "");
    check(`console tab ${tab} renders over the Live wire`, changed !== "");
    if (changed) prev = changed;
  }
  // The Logs tab's filter input is a real Live input binding.
  await page.locator("text=Logs").first().click({ timeout: 10000 });
  await page.waitForTimeout(800);
  check("the Logs tab shows its search filter", (await page.locator('input[type="search"]').count()) > 0);
}

async function scenarioForum(browser) {
  await waitListening("/");
  await expectPolicyHeader("/");
  const page = await newPage(browser);
  await page.goto(ORIGIN + "/", { waitUntil: "load" });
  await page.locator("text=sign in").first().click({ timeout: 10000 });
  await page.locator('input[name="username"]').waitFor({ timeout: 8000 });
  await page.locator('input[name="username"]').fill("csp-user");
  await page.locator('input[name="password"]').fill("hunter2");
  const posted = page.waitForResponse((r) => r.url().includes("/_sky/event") && r.request().method() === "POST", { timeout: 10000 });
  await page.locator('input[name="password"]').press("Enter");
  const resp = await posted.catch(() => null);
  check("the form submit is a POST /_sky/event the server accepts (CSRF)", !!resp && resp.status() === 200, resp ? String(resp.status()) : "no POST");
  const hdr = resp ? resp.request().headers()["x-sky-csrf"] || "" : "";
  check("the submit carries the X-Sky-Csrf token", hdr.length > 0, hdr ? "present" : "missing");
  const hi = await waitFor(async () => (await page.locator("text=hi, csp-user").count()) > 0, 6000, "the signed-in name").catch(() => false);
  check("the signed-in name renders after the typed form decodes", !!hi);
}

async function scenarioTodos(browser) {
  await waitListening("/");
  await expectPolicyHeader("/");
  const page = await newPage(browser);
  const loaded = page.waitForResponse((r) => r.url().includes("/api/todos") && r.request().method() === "GET", { timeout: 20000 });
  await page.goto(ORIGIN + "/", { waitUntil: "load" });
  const ok = await loaded.then(() => true).catch(() => false);
  check("the wasm client boots and loads the list over RPC", ok);
  if (!ok) return;
  await page.locator("#new-input").waitFor({ timeout: 10000 });
  const before = Number(await textOf(page, "#total"));
  await page.locator("#new-input").pressSequentially("csp todo");
  const added = page.waitForResponse((r) => r.url().endsWith("/api/todos") && r.request().method() === "POST", { timeout: 10000 });
  await page.locator("#add-btn").click();
  const resp = await added.catch(() => null);
  check("Add posts the RPC", !!resp && resp.ok(), resp ? String(resp.status()) : "no POST");
  const total = await waitFor(async () => {
    const t = Number(await textOf(page, "#total"));
    return t === before + 1 ? String(t) : "";
  }, 6000, "the new todo").catch(() => "");
  check("the added todo renders", total !== "", `before=${before} after=${total}`);
}

async function scenarioNotes(browser) {
  await waitListening("/");
  await expectPolicyHeader("/");
  const page = await newPage(browser);
  const loaded = page.waitForResponse((r) => r.url().includes("/_rpc/Load"), { timeout: 20000 });
  await page.goto(ORIGIN + "/", { waitUntil: "load" });
  const ok = await loaded.then(() => true).catch(() => false);
  check("the SSR page hydrates and the client runs its Load RPC", ok);
  if (!ok) return;
  const hydrated = await waitFor(async () => (await page.evaluate(() => !document.documentElement.hasAttribute("data-sky-hydrating"))), 8000, "hydration").catch(() => false);
  check("the hydrating overlay clears", !!hydrated);
  const created = page.waitForResponse((r) => r.url().includes("/_rpc/Create"), { timeout: 10000 });
  await page.locator("#new-btn").click();
  check("New note runs the Create RPC", !!(await created.catch(() => null)));
  await page.waitForTimeout(300);
  await page.locator("#title-input").pressSequentially("CSP note");
  const saved = page.waitForResponse((r) => r.url().includes("/_rpc/Save"), { timeout: 10000 });
  await page.locator("#save-btn").click();
  check("Save runs the Save RPC", !!(await saved.catch(() => null)));
  const listed = await waitFor(async () => (await page.locator("#note-list").innerText()).includes("CSP note"), 6000, "the saved note").catch(() => false);
  check("the saved note is listed", !!listed);
  // The STATIC shell (dist/index.html, what a CDN / nginx serves) boots too.
  const shell = await newPage(browser);
  const shellLoaded = shell.waitForResponse((r) => r.url().includes("/_rpc/Load"), { timeout: 20000 });
  await shell.goto(ORIGIN + "/index.html", { waitUntil: "load" });
  check("the static dist/index.html shell boots the client", await shellLoaded.then(() => true).catch(() => false));
  const shellList = await waitFor(async () => (await shell.locator("#note-list").innerText()).includes("CSP note"), 8000, "the list in the static shell").catch(() => false);
  check("the static shell renders the saved note", !!shellList);
}

let browser;
let code = 1;
try {
  browser = await chromium.launch({ headless: true });
  const run = { console: scenarioConsole, counter: scenarioCounter, forum: scenarioForum, todos: scenarioTodos, notes: scenarioNotes }[MODE];
  await run(browser);
  await new Promise((r) => setTimeout(r, 500));
  check("zero securitypolicyviolation events", violations.length === 0, violations.slice(0, 5).join(" | "));
  check("zero Content-Security-Policy console messages", cspConsole.length === 0, cspConsole.slice(0, 3).join(" | "));
  if (/panic:|runtime error:/.test(serverLog)) check("the server log has no panic", false, serverLog.split("\n").slice(-15).join("\n"));
  code = failures.length === 0 ? 0 : 2;
  console.log(code === 0 ? `PASS [${TAG}]` : `FAIL [${TAG}] ${failures.length} check(s): ${failures.join("; ")}`);
} catch (e) {
  console.log(`FAIL [${TAG}] harness: ${e && e.stack ? e.stack : e}`);
  code = failures.length > 0 ? 2 : 1;
} finally {
  try {
    if (browser) await browser.close();
  } catch (_) {}
  try {
    if (proxy) proxy.close();
  } catch (_) {}
  try {
    proc.kill("SIGTERM");
  } catch (_) {}
  await new Promise((r) => setTimeout(r, 300));
  try {
    proc.kill("SIGKILL");
  } catch (_) {}
  if (code !== 0) console.log(`--- server log (tail) ---\n${serverLog.split("\n").slice(-25).join("\n")}`);
  process.exit(code);
}
