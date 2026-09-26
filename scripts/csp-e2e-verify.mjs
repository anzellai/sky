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
//   --via slot    The deployment layout: the backend binary runs from a slot
//                 directory where no `../frontend/dist` exists, and the proxy
//                 serves ONLY `*.wasm` and `/wasm_exec.js` from --dist DIR and
//                 forwards every other path (a Caddy in front of the slot). It
//                 also sets the strict policy. v0.25.19 failed here: the backend
//                 answered `/spa-boot.<hash>.js` with its HTML fallback, the
//                 browser refused it, and the client never booted.
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
//   stale    a page from an OLD build (its script hashes rewritten to a hash no
//            build wrote): each stale asset is a 404, never HTML, and the page
//            fails loudly. --kind spa|live picks the asset names.
//
// Topologies (--via) beyond proxy / strict / slot, each a real deployment shape.
// The caddy-* ones run a real Caddy (CADDY, default `caddy` on PATH) that sets
// CADDY_CSP (the strict policy plus frame-ancestors 'none' and base-uri 'self'):
//
//   direct        the page straight from the backend, no proxy, no policy;
//   caddy-all     Caddy reverse_proxy of every path to the backend;
//   caddy-wasm    Caddy serves ONLY *.wasm and /wasm_exec.js from --dist and
//                 proxies the rest. With --slot the backend runs from a slot
//                 directory that cannot reach the dist;
//   caddy-static  Caddy serves the WHOLE --dist (file_server, index.html
//                 fallback) and proxies only /_rpc/*, /_sky/* and /api/*;
//   caddy-base    a Sky.Live app under the sub-path /app (SKY_LIVE_BASE_PATH),
//                 Caddy strips the prefix.
//
// The new topologies also assert zero console errors and the Content-Type of
// every script, wasm and style sheet the page loads. The console scenario
// behind Caddy runs with SKY_CONSOLE_AUTH=token and signs in with the token.
//
// Usage: node scripts/csp-e2e-verify.mjs <scenario> <app-binary> --port N
//          [--via MODE] [--dist DIR] [--slot] [--kind spa|live] [--cwd DIR]
//          [--env K=V ...]
// Exit: 0 PASS · 2 FAIL · 1 harness error.
import pw from "playwright";
const { chromium } = pw;
import { spawn } from "node:child_process";
import http from "node:http";
import { dirname, join, basename } from "node:path";
import { mkdtempSync, mkdirSync, copyFileSync, chmodSync, readFileSync, existsSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";

export const STRICT_CSP =
  "default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; style-src 'self' 'unsafe-inline'; " +
  "img-src 'self' data: blob:; connect-src 'self'";
const CADDY_CSP = STRICT_CSP + "; frame-ancestors 'none'; base-uri 'self'";

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

const SCENARIOS = ["console", "counter", "forum", "todos", "notes", "stale"];
const MODES = ["proxy", "strict", "slot", "direct", "caddy-all", "caddy-wasm", "caddy-static", "caddy-base"];
const MODE = process.argv[2];
const APP = process.argv[3];
if (!SCENARIOS.includes(MODE) || !APP) {
  console.error(`usage: csp-e2e-verify.mjs <${SCENARIOS.join("|")}> <app-binary> --port N [--via ${MODES.join("|")}]`);
  process.exit(1);
}
const VIA = arg("--via", "proxy");
if (!MODES.includes(VIA)) {
  console.error(`csp-e2e: --via must be one of ${MODES.join(", ")}, got ${VIA}`);
  process.exit(1);
}
const KIND = arg("--kind", "spa");
const CADDY_MODE = VIA.startsWith("caddy-");
// The topologies added with the real-proxy matrix assert every console error.
const STRICT_CHECKS = VIA === "slot" || VIA === "direct" || CADDY_MODE;
const APP_PORT = Number(arg("--port", "9520"));
const PROXY_PORT = APP_PORT + 1;
const PROXIED = VIA === "proxy" || VIA === "slot" || CADDY_MODE;
const PAGE_PORT = PROXIED ? PROXY_PORT : APP_PORT;
const ORIGIN = `http://127.0.0.1:${PAGE_PORT}`;
const BASE = VIA === "caddy-base" ? "/app" : "";
const APP_ORIGIN = ORIGIN + BASE;
const DIST = arg("--dist", "");
const NEEDS_DIST = VIA === "slot" || VIA === "caddy-wasm" || VIA === "caddy-static";
if (NEEDS_DIST && !(DIST && existsSync(DIST))) {
  console.error(`csp-e2e: --via ${VIA} needs --dist DIR (got "${DIST}")`);
  process.exit(1);
}
// --via slot (and caddy-wasm --slot): the backend runs a COPY of the binary
// from a slot directory with no `../frontend/dist` beside it.
const IN_SLOT = VIA === "slot" || process.argv.includes("--slot");
let RUN_APP = APP;
let CWD = arg("--cwd", dirname(dirname(APP)));
if (IN_SLOT) {
  const slot = join(mkdtempSync(join(tmpdir(), "sky-csp-slot-")), "slot", "backend");
  mkdirSync(slot, { recursive: true });
  RUN_APP = join(slot, basename(APP));
  copyFileSync(APP, RUN_APP);
  chmodSync(RUN_APP, 0o755);
  CWD = slot;
  if (existsSync(join(slot, "..", "frontend", "dist"))) {
    console.error("csp-e2e: harness error: the slot must not reach a frontend dist");
    process.exit(1);
  }
}
const DB = join(mkdtempSync(join(tmpdir(), "sky-csp-e2e-")), "app.db");
const TAG = `${MODE}/${VIA}${IN_SLOT && VIA !== "slot" ? "+slot" : ""}`;

for (const p of PROXIED ? [APP_PORT, PROXY_PORT] : [APP_PORT]) {
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
// Behind Caddy the console is not dev-open: it asks for the token.
const CONSOLE_TOKEN = MODE === "console" && CADDY_MODE ? "e2e-console-token-" + process.pid : "";
const env = {
  ...process.env,
  SKY_LIVE_PORT: String(APP_PORT),
  PORT: String(APP_PORT),
  TODOS_PORT: String(APP_PORT),
  SKY_DB_PATH: DB,
  ENV: "development",
  ...(CONSOLE_TOKEN ? { SKY_CONSOLE_AUTH: "token", SKY_CONSOLE_TOKEN: CONSOLE_TOKEN } : {}),
  ...(BASE ? { SKY_LIVE_BASE_PATH: BASE } : {}),
  ...extraEnv,
};
// The strict pass opts the runtime in; the proxy passes must work WITHOUT it
// (the proxy is the only source of the policy there).
if (VIA === "strict") env.SKY_CSP = "strict";
else delete env.SKY_CSP;

const proc = spawn(RUN_APP, [], { cwd: CWD, env });
let serverLog = "";
proc.stdout.on("data", (d) => (serverLog += d));
proc.stderr.on("data", (d) => (serverLog += d));
let exited = null;
proc.on("exit", (code, sig) => (exited = { code, sig }));

// ── the node proxy (proxy, slot): pipes every request, sets STRICT_CSP ──
let proxy = null;
if (VIA === "proxy" || VIA === "slot") {
  proxy = http.createServer((req, res) => {
    // --via slot: the static host serves ONLY the wasm pair from the dist.
    const upath = (req.url || "/").split("?")[0];
    if (VIA === "slot" && (upath.endsWith(".wasm") || upath === "/wasm_exec.js")) {
      const file = join(DIST, basename(upath));
      if (!existsSync(file)) {
        res.writeHead(404, { "content-type": "text/plain" });
        res.end("not found");
        return;
      }
      res.writeHead(200, {
        "content-type": upath.endsWith(".wasm") ? "application/wasm" : "text/javascript; charset=utf-8",
        "content-security-policy": STRICT_CSP,
      });
      res.end(readFileSync(file));
      return;
    }
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

// ── a real Caddy (caddy-*) ──
function caddyfile() {
  const up = `reverse_proxy 127.0.0.1:${APP_PORT}`;
  const files = `root * ${DIST}\n\t\tfile_server {\n\t\t\tprecompressed br gzip\n\t\t}`;
  let body;
  switch (VIA) {
    case "caddy-all":
      body = `\t${up}`;
      break;
    case "caddy-wasm":
      body = `\t@wasm path *.wasm /wasm_exec.js\n\thandle @wasm {\n\t\t${files}\n\t}\n\thandle {\n\t\t${up}\n\t}`;
      break;
    case "caddy-static":
      body =
        `\t@backend path /_rpc/* /_sky/* /api/*\n\thandle @backend {\n\t\t${up}\n\t}\n` +
        `\thandle {\n\t\troot * ${DIST}\n\t\ttry_files {path} /index.html\n\t\tfile_server {\n\t\t\tprecompressed br gzip\n\t\t}\n\t}`;
      break;
    case "caddy-base":
      body = `\thandle_path /app/* {\n\t\t${up}\n\t}\n\thandle {\n\t\trespond "not the app" 404\n\t}`;
      break;
  }
  return (
    `{\n\tadmin off\n\tauto_https off\n\tpersist_config off\n}\n\n` +
    `http://127.0.0.1:${PROXY_PORT} {\n\theader >Content-Security-Policy "${CADDY_CSP}"\n${body}\n}\n`
  );
}
let caddy = null;
let caddyLog = "";
if (CADDY_MODE) {
  const dir = mkdtempSync(join(tmpdir(), "sky-csp-caddy-"));
  const file = join(dir, "Caddyfile");
  writeFileSync(file, caddyfile());
  caddy = spawn(process.env.CADDY || "caddy", ["run", "--config", file, "--adapter", "caddyfile"], {
    env: { ...process.env, XDG_DATA_HOME: dir, XDG_CONFIG_HOME: dir, HOME: process.env.HOME || dir },
  });
  caddy.stdout.on("data", (d) => (caddyLog += d));
  caddy.stderr.on("data", (d) => (caddyLog += d));
  caddy.on("error", (e) => (caddyLog += `spawn error: ${e.message}\n`));
  caddy.on("exit", (code) => (caddyLog += `caddy exited ${code}\n`));
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
      const r = await fetch(APP_ORIGIN + path);
      if (r.status < 500) return r;
    } catch (_) {}
    await new Promise((r) => setTimeout(r, 250));
  }
  throw new Error("app never listened\n" + serverLog + (caddyLog ? "\n--- caddy ---\n" + caddyLog : ""));
}

const violations = [];
const cspConsole = [];
const consoleErrors = [];
const badAssets = [];
const checkedAssets = new Set();
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
    if (m.type() === "error") consoleErrors.push(t);
  });
  // Every script, wasm and style sheet must come back as itself: a script
  // answered with HTML is the v0.25.19 failure.
  page.on("response", (r) => {
    let p;
    try {
      p = new URL(r.url()).pathname;
    } catch (_) {
      return;
    }
    const want = p.endsWith(".js") ? /javascript/ : p.endsWith(".wasm") ? /^application\/wasm/ : p.endsWith(".css") ? /^text\/css/ : null;
    if (!want) return;
    const ct = r.headers()["content-type"] || "";
    const st = r.status();
    if (st === 304) return void checkedAssets.add(p);
    if (st !== 200 || !want.test(ct)) badAssets.push(`${p} -> ${st} ${ct}`);
    else checkedAssets.add(p);
  });
  page.on("pageerror", (e) => {
    failures.push(`[pageerror] ${e.message}`);
    console.log(`FAIL [${TAG}] pageerror: ${e.message}`);
  });
  return page;
}

async function expectPolicyHeader(path) {
  if (VIA === "direct") return; // no proxy and no SKY_CSP: no policy to check
  const r = await fetch(APP_ORIGIN + path);
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
  await page.goto(APP_ORIGIN + "/", { waitUntil: "load" });
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
  for (let i = 0; i < 3; i++) await fetch(APP_ORIGIN + "/").catch(() => {});
  await waitFor(async () => {
    const st = (await fetch(APP_ORIGIN + "/_sky/console/")).status;
    return CONSOLE_TOKEN ? st === 401 : st === 200;
  }, 30000, "the console mount");
  await expectPolicyHeader("/_sky/console/");
  const page = await newPage(browser);
  await page.goto(APP_ORIGIN + "/_sky/console/", { waitUntil: "load" });
  if (CONSOLE_TOKEN) {
    // SKY_CONSOLE_AUTH=token: the console asks for the token first. The login
    // page answers 401 by design, so its resource error is not counted.
    await page.locator('input[name="token"]').waitFor({ timeout: 10000 });
    await page.locator('input[name="token"]').fill(CONSOLE_TOKEN);
    await Promise.all([page.waitForNavigation({ timeout: 10000 }).catch(() => null), page.locator('button[type="submit"]').click()]);
    const ignore = consoleErrors.findIndex((t) => /status of 401/.test(t));
    if (ignore >= 0) consoleErrors.splice(ignore, 1);
    const signedIn = await waitFor(async () => (await page.locator("text=Overview").count()) > 0, 10000, "the console after sign-in").catch(() => false);
    check("the console token signs in", !!signedIn);
  }
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
  await page.goto(APP_ORIGIN + "/", { waitUntil: "load" });
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
  await page.goto(APP_ORIGIN + "/", { waitUntil: "load" });
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
  {
    // The page names its boot loader; it must come back as script.
    const html = await (await fetch(APP_ORIGIN + "/")).text();
    const boot = (html.match(/\/spa-boot\.[0-9a-f]+\.js/) || [])[0];
    check("the SSR page names a /spa-boot.<hash>.js loader", !!boot, boot || "none");
    if (boot) {
      const r = await fetch(APP_ORIGIN + boot);
      const ct = r.headers.get("content-type") || "";
      check(`${boot} is served as JavaScript`,
        r.status === 200 && ct.startsWith("text/javascript"), `${r.status} ${ct}`);
    }
  }
  const loaded = page.waitForResponse((r) => r.url().includes("/_rpc/Load"), { timeout: 20000 });
  await page.goto(APP_ORIGIN + "/", { waitUntil: "load" });
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
  // In the slot layout the static host serves only the wasm pair, so there is
  // no static shell to load. The SSR page above is the whole contract.
  if (IN_SLOT) return;
  // The STATIC shell (dist/index.html, what a CDN / nginx serves) boots too.
  const shell = await newPage(browser);
  const shellLoaded = shell.waitForResponse((r) => r.url().includes("/_rpc/Load"), { timeout: 20000 });
  await shell.goto(APP_ORIGIN + "/index.html", { waitUntil: "load" });
  check("the static dist/index.html shell boots the client", await shellLoaded.then(() => true).catch(() => false));
  const shellList = await waitFor(async () => (await shell.locator("#note-list").innerText()).includes("CSP note"), 8000, "the list in the static shell").catch(() => false);
  check("the static shell renders the saved note", !!shellList);
}

async function scenarioStale(browser) {
  await waitListening("/");
  const html = await (await fetch(APP_ORIGIN + "/")).text();
  const re = KIND === "spa" ? /\/spa-boot\.([0-9a-f]+)\.js/ : /\/_sky\/live\.([0-9a-f]+)\.js/;
  const m = html.match(re);
  check(`the page names its hashed ${KIND === "spa" ? "boot loader" : "client"}`, !!m, m ? m[0] : "none");
  if (!m) return;
  const stale = "000000000000";
  const paths = KIND === "spa"
    ? [`/spa-boot.${stale}.js`, `/main.${stale}.wasm`, `/_sky/live.${stale}.js`]
    : [`/_sky/live.${stale}.js`, `/_sky/console-shell.${stale}.js`];
  for (const p of paths) {
    const r = await fetch(APP_ORIGIN + p);
    const ct = r.headers.get("content-type") || "";
    check(`a stale ${p} is a 404, never HTML`, r.status === 404 && !/text\/html/.test(ct), `${r.status} ${ct}`);
  }
  // The page from an old build, after a redeploy: it must fail loudly.
  const page = await newPage(browser);
  const all = [];
  page.on("console", (msg) => all.push(msg.text()));
  await page.route(APP_ORIGIN + "/", (route) =>
    route.fulfill({ status: 200, contentType: "text/html; charset=utf-8", body: html.split(m[1]).join(stale) })
  );
  await page.goto(APP_ORIGIN + "/", { waitUntil: "load" });
  await page.waitForTimeout(1500);
  check("the stale page reports the missing script (404) in the console", all.some((t) => /404/.test(t)), all.slice(0, 3).join(" | "));
  check("no script of the stale page is answered with HTML", !all.some((t) => /MIME type \('text\/html'\)/.test(t)));
}

let browser;
let code = 1;
try {
  browser = await chromium.launch({ headless: true });
  const run = { console: scenarioConsole, counter: scenarioCounter, forum: scenarioForum, todos: scenarioTodos, notes: scenarioNotes, stale: scenarioStale }[MODE];
  await run(browser);
  await new Promise((r) => setTimeout(r, 500));
  check("zero securitypolicyviolation events", violations.length === 0, violations.slice(0, 5).join(" | "));
  check("zero Content-Security-Policy console messages", cspConsole.length === 0, cspConsole.slice(0, 3).join(" | "));
  if (STRICT_CHECKS && MODE !== "stale") {
    check("zero console errors", consoleErrors.length === 0, consoleErrors.slice(0, 3).join(" | "));
    check("every script, wasm and style sheet has its own Content-Type", badAssets.length === 0 && checkedAssets.size > 0,
      badAssets.length ? badAssets.slice(0, 5).join(" | ") : `${checkedAssets.size} checked: ${[...checkedAssets].join(" ")}`);
  }
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
    if (caddy) caddy.kill("SIGTERM");
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
