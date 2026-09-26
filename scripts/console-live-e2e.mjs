#!/usr/bin/env node
// scripts/console-live-e2e.mjs — the Sky Console shows LIVE data and keeps its
// live channel, in a real browser, directly and behind a real Caddy.
//
// Driven by scripts/console-live-e2e.sh (which builds the apps). One run is one
// app in one topology:
//
//   node scripts/console-live-e2e.mjs --app <binary> --name <label>
//        --port <app port> [--caddy <caddy binary> --caddy-port <https port>]
//        [--commit <expected stamped commit>] [--cwd <dir>]
//
// With --caddy the page is served by Caddy over HTTPS + HTTP/2 (local_certs),
// with the production shape the field report came from: `encode zstd gzip`,
// reverse_proxy with `flush_interval -1`, `transport http { read_timeout 2m }`,
// active health checks, TWO upstream slots with `lb_policy first` (the second
// slot is empty, as between deploys), and a strict
// `Content-Security-Policy: script-src 'self' 'wasm-unsafe-eval'`.
// Without --caddy the browser talks to the app directly and the app sends the
// strict policy itself (SKY_CSP=strict).
//
// The app runs as production does: ENV=production, SKY_CONSOLE_AUTH=token,
// memory session store.
//
// Asserts, in order:
//   1. sign-in with the console token works;
//   2. the header shows the live process ("Sky <v> · prod · uptime N") and the
//      uptime COUNTS UP; requests total > 0 after generated traffic; the stamped
//      commit and build time are shown (not "dev" / "unknown");
//   3. the Logs tab lists a log line and the Traces tab lists a span;
//   4. for 40 s the live channel holds: no "Reconnecting"/"offline" banner, no
//      failed SSE request (a server deadline used to cut it at 30 s);
//   5. the backend restarts (memory store: every session is gone) and the
//      console recovers BY ITSELF within 15 s: a new process uptime in the
//      header, no banner, no reload loop;
//   6. zero console errors outside the restart window, zero CSP violations.
//   With --analytics (an app that tracks on "Sign up", e.g. the
//   console-analytics fixture): a visitor in a SECOND browser context presses
//   "Sign up", and the Analytics tab must list the tracked event and count an
//   identified user (> 0).
//
// Exit 0 on PASS, 1 on FAIL (every failed check is printed).

import pw from "playwright";
import { spawn } from "node:child_process";
import { mkdtempSync, writeFileSync, mkdirSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import net from "node:net";

const argv = process.argv.slice(2);
const arg = (k, d) => {
  const i = argv.indexOf(k);
  return i >= 0 && i + 1 < argv.length ? argv[i + 1] : d;
};
const APP = arg("--app");
const NAME = arg("--name", "app");
const PORT = Number(arg("--port", "9620"));
const CADDY = arg("--caddy", "");
const CADDY_PORT = Number(arg("--caddy-port", String(PORT + 5)));
const COMMIT = arg("--commit", "");
const BUILT_AT = arg("--built-at", "");
const CWD = arg("--cwd", process.cwd());
const ANALYTICS = argv.includes("--analytics");
if (!APP) {
  console.error("console-live-e2e: --app <binary> is required");
  process.exit(2);
}
const TOKEN = "console-live-e2e-token-0123456789abcdef";
const VIA = CADDY ? "caddy" : "direct";
const ORIGIN = CADDY ? `https://localhost:${CADDY_PORT}` : `http://127.0.0.1:${PORT}`;
const tag = `[${NAME} ${VIA}]`;
const work = mkdtempSync(join(tmpdir(), "sky-console-live-"));

const failures = [];
const fail = (msg) => {
  failures.push(msg);
  console.log(`${tag} FAIL ${msg}`);
};
const info = (msg) => console.log(`${tag} ${msg}`);
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

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
    await sleep(150);
  }
  return false;
}

// ── the app ──────────────────────────────────────────────────────────────────
let appProc = null;
function startApp() {
  const env = {
    ...process.env,
    ENV: "production",
    SKY_CONSOLE_AUTH: "token",
    SKY_CONSOLE_TOKEN: TOKEN,
    SKY_ADMIN_TOKEN: "console-live-e2e-admin-token-0123456789",
    SKY_LIVE_PORT: String(PORT),
    PORT: String(PORT),
    SKY_DB_PATH: join(work, "app.db"),
  };
  delete env.SKY_LIVE_STORE; // memory store, as the field deploy
  if (CADDY) delete env.SKY_CSP;
  else env.SKY_CSP = "strict";
  appProc = spawn(APP, [], { cwd: CWD, env, stdio: ["ignore", "pipe", "pipe"] });
  const out = [];
  appProc.stdout.on("data", (d) => out.push(String(d)));
  appProc.stderr.on("data", (d) => out.push(String(d)));
  appProc.logTail = () => out.join("").split("\n").slice(-30).join("\n");
  return appProc;
}
async function stopApp() {
  if (!appProc || appProc.exitCode !== null) return;
  const p = appProc;
  const exited = new Promise((r) => p.once("exit", r));
  p.kill("SIGTERM");
  await Promise.race([exited, sleep(10000)]);
  if (p.exitCode === null) p.kill("SIGKILL");
}

// ── Caddy ────────────────────────────────────────────────────────────────────
let caddyProc = null;
async function startCaddy() {
  const dir = join(work, "caddy");
  mkdirSync(dir, { recursive: true });
  const cfg = `{
	admin localhost:${CADDY_PORT + 2}
	local_certs
	skip_install_trust
	http_port ${CADDY_PORT + 1}
	https_port ${CADDY_PORT}
	storage file_system ${join(dir, "data")}
}
localhost:${CADDY_PORT} {
	log {
		output file ${join(dir, "access.log")}
	}
	header Content-Security-Policy "script-src 'self' 'wasm-unsafe-eval'"
	encode zstd gzip
	reverse_proxy 127.0.0.1:${PORT} 127.0.0.1:${PORT + 1} {
		lb_policy first
		flush_interval -1
		health_uri /_sky/healthz
		health_interval 2s
		transport http {
			read_timeout 2m
		}
	}
}
`;
  writeFileSync(join(dir, "Caddyfile"), cfg);
  caddyProc = spawn(CADDY, ["run", "--config", join(dir, "Caddyfile"), "--adapter", "caddyfile"], {
    cwd: dir,
    env: { ...process.env, HOME: dir, XDG_DATA_HOME: join(dir, "xdg-data"), XDG_CONFIG_HOME: join(dir, "xdg-config") },
    stdio: ["ignore", "pipe", "pipe"],
  });
  const out = [];
  caddyProc.stdout.on("data", (d) => out.push(String(d)));
  caddyProc.stderr.on("data", (d) => out.push(String(d)));
  caddyProc.logTail = () => out.join("").split("\n").filter((l) => /abort|EOF|error/i.test(l)).slice(-8).join("\n");
  if (!(await waitPort(CADDY_PORT, 20000))) throw new Error("caddy did not listen on " + CADDY_PORT);
}

// ── page helpers ─────────────────────────────────────────────────────────────
async function bodyText(page) {
  try { return await page.evaluate(() => document.body ? document.body.innerText : ""); } catch { return ""; }
}
function headerOf(text) {
  const m = text.match(/Sky (\S+) · (prod|dev) · uptime (\d+)(s|m|h)/);
  if (!m) return null;
  const mult = { s: 1, m: 60, h: 3600 }[m[4]];
  return { version: m[1], mode: m[2], uptime: Number(m[3]) * mult, raw: m[0] };
}
async function waitHeader(page, pred, ms) {
  const end = Date.now() + ms;
  let last = null;
  while (Date.now() < end) {
    last = headerOf(await bodyText(page));
    if (last && pred(last)) return last;
    await sleep(500);
  }
  return last;
}
async function bannerState(page) {
  try {
    return await page.evaluate(() => {
      const el = document.getElementById("__sky-status");
      if (!el) return "none";
      const c = (el.className.match(/sky-status--(\w+)/) || [])[1] || "none";
      return c;
    });
  } catch { return "unknown"; }
}
async function clickTab(page, label) {
  await page.getByText(label, { exact: true }).first().click();
}

// ── the run ──────────────────────────────────────────────────────────────────
let browser = null;
let phase = "boot";
const consoleErrors = [];
const cspViolations = [];
const sseFailures = [];
let navigations = 0;

try {
  startApp();
  if (!(await waitPort(PORT, 30000))) throw new Error("app did not listen on " + PORT + "\n" + appProc.logTail());
  if (CADDY) await startCaddy();

  browser = await pw.chromium.launch({ args: ["--ignore-certificate-errors"] });
  const ctx = await browser.newContext({ ignoreHTTPSErrors: true });
  await ctx.addInitScript(() => {
    document.addEventListener("securitypolicyviolation", (e) => {
      console.error("CSP-VIOLATION " + e.violatedDirective + " " + e.blockedURI);
    });
  });
  const page = await ctx.newPage();
  page.on("console", (m) => {
    const t = m.text();
    if (t.startsWith("CSP-VIOLATION")) cspViolations.push(t);
    if (m.type() === "error") consoleErrors.push({ phase, text: t.slice(0, 200) });
  });
  page.on("requestfailed", (r) => {
    if (r.url().includes("/_sky/sse")) sseFailures.push({ phase, err: r.failure()?.errorText, at: Date.now() });
  });
  page.on("framenavigated", (f) => { if (f === page.mainFrame()) navigations++; });

  // 1. sign in
  phase = "login";
  await page.goto(ORIGIN + "/_sky/console/", { waitUntil: "domcontentloaded" });
  const form = page.locator("input[name=token]");
  if ((await form.count()) === 0) fail("no login form at /_sky/console/ under SKY_CONSOLE_AUTH=token");
  await form.fill(TOKEN);
  await Promise.all([page.waitForNavigation({ waitUntil: "domcontentloaded" }), page.locator("button[type=submit]").click()]);
  if (!/Sky Console/.test(await bodyText(page))) fail("signed-in page is not the console");
  phase = "steady";
  const steadyFrom = Date.now();

  // traffic: page views + a 404, through the same origin
  for (let i = 0; i < 6; i++) await ctx.request.get(ORIGIN + "/").catch(() => {});
  await ctx.request.get(ORIGIN + "/console-live-e2e-missing").catch(() => {});

  // 2. live header, counting uptime
  const h1 = await waitHeader(page, (h) => h.mode === "prod", 15000);
  if (!h1) fail(`header never showed the live process; header text: ${(await bodyText(page)).slice(0, 120)}`);
  else if (h1.mode !== "prod") fail(`header says "${h1.mode}" under ENV=production: ${h1.raw}`);
  if (h1) {
    info(`header ${h1.raw}`);
    const h2 = await waitHeader(page, (h) => h.uptime > h1.uptime, 12000);
    if (!h2 || h2.uptime <= h1.uptime) fail(`uptime did not count up (${h1.uptime}s → ${h2 ? h2.uptime : "?"}s): the console's data never updates`);
    else info(`uptime counts: ${h1.uptime}s → ${h2.uptime}s`);
  }
  {
    const end = Date.now() + 12000;
    let req = 0;
    while (Date.now() < end) {
      const m = (await bodyText(page)).match(/REQUESTS TOTAL\s*\n\s*(\d+)/i);
      req = m ? Number(m[1]) : 0;
      if (req > 0) break;
      await sleep(500);
    }
    if (req > 0) info(`requests total ${req}`);
    else fail("requests total stayed 0 after generated traffic");
  }
  {
    const t = await bodyText(page);
    const commit = (t.match(/COMMIT\s*\n\s*(\S+)/i) || [])[1] || "";
    const built = (t.match(/BUILT AT\s*\n\s*(\S+)/i) || [])[1] || "";
    info(`commit ${commit}  built ${built}`);
    if (COMMIT && commit !== COMMIT) fail(`commit shows "${commit}", want the stamped "${COMMIT}"`);
    if (!/^\d{4}-\d{2}-\d{2}T/.test(built)) fail(`built-at shows "${built}", want the build time`);
    if (BUILT_AT && built !== BUILT_AT) fail(`built-at shows "${built}", want the stamped "${BUILT_AT}"`);
  }

  // 3. a log line and a span
  await clickTab(page, "Logs");
  {
    const end = Date.now() + 12000;
    let ok = false;
    while (Date.now() < end && !ok) {
      const t = await bodyText(page);
      // The access log of the request this run made (its path is unique).
      ok = t.includes("console-live-e2e-missing");
      if (!ok) await sleep(500);
    }
    if (!ok) fail("the Logs tab did not list the access log line of a request this run made");
    else info("logs tab lists entries");
  }
  await clickTab(page, "Traces");
  {
    const end = Date.now() + 12000;
    let ok = false;
    while (Date.now() < end && !ok) {
      const t = await bodyText(page);
      ok = !/No traces captured/.test(t) && /\d+(\.\d+)?\s?ms/.test(t);
      if (!ok) await sleep(500);
    }
    if (!ok) fail("the Traces tab listed no span");
    else info("traces tab lists spans");
  }
  // 3b. analytics: a visitor signs up; the console shows the event + user
  if (ANALYTICS) {
    const visitor = await browser.newContext({ ignoreHTTPSErrors: true });
    try {
      const vp = await visitor.newPage();
      await vp.goto(ORIGIN + "/", { waitUntil: "domcontentloaded" });
      await vp.getByText("Sign up", { exact: true }).first().click();
      // The app confirms the tracked sign-up in its own view.
      await vp.waitForFunction(() => /(^|\n)signed up(\n|$)/.test(document.body.innerText), null, { timeout: 10000 });
    } catch (e) {
      let vt = "";
      try { vt = (await visitor.pages()[0].evaluate(() => document.body.innerText)).replace(/\n/g, " | ").slice(0, 200); } catch {}
      fail("the fixture visitor could not sign up: " + String(e).split("\n")[0] + " — page: " + vt);
    } finally {
      await visitor.close();
    }
    await clickTab(page, "Analytics");
    const end = Date.now() + 15000;
    let t = "", users = 0, seen = false;
    while (Date.now() < end) {
      t = await bodyText(page);
      seen = t.includes("console_e2e_signup");
      users = Number((t.match(/IDENTIFIED USERS[^\n]*\n\s*(\d+)/i) || [])[1] || 0);
      if (seen && users > 0) break;
      await sleep(500);
    }
    if (!seen) fail("the Analytics tab did not list the tracked console_e2e_signup event");
    if (users <= 0) fail(`the Analytics tab counts ${users} identified users after an identified sign-up`);
    if (seen && users > 0) info(`analytics tab lists console_e2e_signup, identified users ${users}`);
  }
  await clickTab(page, "Overview");

  // 4. the live channel holds past the old 30 s cut
  const holdUntil = steadyFrom + 42000;
  const badBanner = [];
  while (Date.now() < holdUntil) {
    const b = await bannerState(page);
    if (b === "reconnecting" || b === "offline" || b === "lost") badBanner.push(b);
    await sleep(2000);
  }
  const steadySse = sseFailures.filter((f) => f.phase === "steady");
  if (steadySse.length) fail(`the console's SSE failed ${steadySse.length}x while the backend was up: ${steadySse.map((f) => f.err).join(", ")}`);
  if (badBanner.length) fail(`the banner showed ${[...new Set(badBanner)].join("/")} ${badBanner.length}x in 40 s with the backend up`);
  if (!badBanner.length && !steadySse.length) info(`live channel held ${Math.round((Date.now() - steadyFrom) / 1000)} s with no banner`);
  const before = headerOf(await bodyText(page));

  // 5. restart the backend; the console must recover by itself
  phase = "restart";
  const navBefore = navigations;
  await stopApp();
  await sleep(1000);
  startApp();
  const restartedAt = Date.now();
  if (!(await waitPort(PORT, 30000))) throw new Error("app did not come back\n" + appProc.logTail());
  const recovered = await waitHeader(page, (h) => h.mode === "prod" && before && h.uptime < before.uptime, 15000);
  const tookS = ((Date.now() - restartedAt) / 1000).toFixed(1);
  if (!recovered || !before || recovered.uptime >= before.uptime) {
    fail(`the console did not recover within 15 s of the restart (banner: ${await bannerState(page)}; header: ${recovered ? recovered.raw : "none"})`);
  } else {
    info(`recovered in ${tookS} s: ${recovered.raw} (page loads during recovery: ${navigations - navBefore})`);
  }
  phase = "recovered";
  await sleep(5000);
  const b2 = await bannerState(page);
  if (b2 === "reconnecting" || b2 === "offline" || b2 === "lost") fail(`after recovery the banner shows ${b2}`);
  if (navigations - navBefore > 2) fail(`recovery reloaded the page ${navigations - navBefore} times (a reload loop)`);
  const h3 = headerOf(await bodyText(page));
  if (recovered && (!h3 || h3.uptime <= recovered.uptime)) fail("after recovery the uptime does not count up");

  // 6. errors
  const hard = consoleErrors.filter((e) => e.phase === "steady" || e.phase === "recovered");
  if (hard.length) fail(`console errors with the backend up: ${hard.map((e) => e.text).join(" | ")}`);
  if (cspViolations.length) fail(`CSP violations: ${cspViolations.join(" | ")}`);
  const allowed = consoleErrors.filter((e) => e.phase === "restart").length;
  if (allowed) info(`${allowed} console error(s) during the restart window (the old connection dropping)`);
} catch (e) {
  fail("run aborted: " + (e && e.stack ? e.stack.split("\n").slice(0, 3).join(" ") : e));
} finally {
  if (failures.length && caddyProc && caddyProc.logTail) {
    const t = caddyProc.logTail();
    if (t) console.log(`${tag} caddy log (abort/EOF/error lines):\n${t}`);
  }
  if (failures.length && appProc && appProc.logTail) console.log(`${tag} app log tail:\n${appProc.logTail()}`);
  try { if (browser) await browser.close(); } catch {}
  await stopApp();
  if (caddyProc && caddyProc.exitCode === null) caddyProc.kill("SIGTERM");
  console.log(failures.length ? `${tag} FAIL (${failures.length})` : `${tag} PASS`);
  process.exit(failures.length ? 1 : 0);
}
