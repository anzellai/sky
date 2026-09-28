#!/usr/bin/env node
// scripts/header-session-verify.mjs
//
// Browser e2e for Sky.Live without cookies: the header session transport
// (runtime-go/rt/live_session_header.go), driven by
// scripts/header-session-e2e.sh against the fixture
// rust/crates/sky/tests/fixtures/header-session
// (`App.withSessionTransport HeaderToken`).
//
// The browser profile BLOCKS ALL COOKIES (Chromium content setting
// cookies = 2, the setting a user gets from "Block all cookies"), and the app
// runs with SKY_CSP=strict. Asserted:
//
//   * the page is served with a strict script-src and sets no cookie at all;
//     the browser really refuses cookies (a document.cookie write is dropped);
//   * the client holds a 32-hex session token and the live stream connects
//     (fetch-stream SSE, hello received);
//   * a counter works over event POSTs; server pushes (a 150 ms ticker)
//     arrive over the stream;
//   * the stream is dropped (aborted) and reconnects: the state survives and
//     the counter and ticker keep working;
//   * signing in rotates the session: the tab adopts a NEW token, keeps
//     working, and Live.sessionKey is unchanged; the old token no longer
//     drives the session (session-rotating inside the grace window);
//   * with streaming fetch unavailable (ReadableStream removed), the client
//     falls back to a one-time SSE ticket and still works;
//   * zero securitypolicyviolation events, zero console errors, and the
//     browser context holds no cookie at the end.
//
// Usage: node scripts/header-session-verify.mjs <app-binary> --port N [--cwd DIR]
import pw from "playwright";
import { spawn } from "node:child_process";
import { mkdtempSync, mkdirSync, writeFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";

const { chromium } = pw;
const argv = process.argv.slice(2);
const APP = argv[0];
function arg(name, dflt) {
  const i = argv.indexOf(name);
  return i >= 0 && i + 1 < argv.length ? argv[i + 1] : dflt;
}
if (!APP) {
  console.error("usage: header-session-verify.mjs <app-binary> --port N [--cwd DIR]");
  process.exit(2);
}
const PORT = Number(arg("--port", "9580"));
const CWD = arg("--cwd", dirname(dirname(APP)));
const ORIGIN = `http://127.0.0.1:${PORT}`;
const TAG = "header-session";

try {
  await fetch(ORIGIN + "/", { signal: AbortSignal.timeout(1000) });
  console.error(`${TAG}: harness error: port ${PORT} is already serving; stop that process first`);
  process.exit(1);
} catch (_) {}

const env = {
  ...process.env,
  PORT: String(PORT),
  SKY_LIVE_PORT: String(PORT),
  ENV: "development",
  SKY_CSP: "strict",
};
delete env.SKY_LIVE_SESSION_TRANSPORT;
const proc = spawn(APP, [], { cwd: CWD, env });
let serverLog = "";
proc.stdout.on("data", (d) => (serverLog += d));
proc.stderr.on("data", (d) => (serverLog += d));
let exited = null;
proc.on("exit", (code, sig) => (exited = { code, sig }));

const failures = [];
function check(step, ok, detail) {
  console.log(`${ok ? "ok  " : "FAIL"} [${TAG}] ${step}${detail ? ": " + detail : ""}`);
  if (!ok) failures.push(step);
}

async function waitListening() {
  for (let i = 0; i < 240; i++) {
    if (exited) throw new Error(`app exited early (${JSON.stringify(exited)})\n${serverLog}`);
    try {
      const r = await fetch(ORIGIN + "/");
      if (r.status < 500) return r;
    } catch (_) {}
    await new Promise((r) => setTimeout(r, 250));
  }
  throw new Error("app never listened\n" + serverLog);
}

async function waitFor(page, fn, a, ms = 10000) {
  try {
    await page.waitForFunction(fn, a, { timeout: ms });
    return true;
  } catch (_) {
    return false;
  }
}

const state = (page) => page.evaluate(() => (document.getElementById("state") || {}).textContent || "");
const field = async (page, name) => {
  const m = new RegExp(name + "=([^|]*)").exec(await state(page));
  return m ? m[1] : null;
};
const shows = (page, s, ms) => waitFor(page, (s) => (document.getElementById("state") || {}).textContent?.includes(s), s, ms);

// A Chromium profile whose content settings block every cookie.
const profile = mkdtempSync(join(tmpdir(), "sky-header-session-"));
mkdirSync(join(profile, "Default"), { recursive: true });
writeFileSync(
  join(profile, "Default", "Preferences"),
  JSON.stringify({ profile: { default_content_setting_values: { cookies: 2 }, block_third_party_cookies: true } })
);

let context;
try {
  const first = await waitListening();
  const csp = first.headers.get("content-security-policy") || "";
  const scriptSrc = csp.split(";").map((s) => s.trim()).find((s) => s.startsWith("script-src ")) || "";
  check(
    "the page is served with a strict script-src (no inline, eval, hash or nonce)",
    scriptSrc.includes("'self'") && !/'unsafe-inline'|'unsafe-eval'|'sha(256|384|512)-|'nonce-/.test(scriptSrc),
    csp || "(no Content-Security-Policy header)"
  );
  const setCookie = first.headers.get("set-cookie");
  check("the page load sets no cookie at all", !setCookie, setCookie || "");
  check("the page load hands out a session token", /^[0-9a-f]{32}$/.test(first.headers.get("x-sky-session") || ""));

  // channel "chromium": the full Chromium in new headless mode, which honours
  // the profile's content settings (the headless shell ignores them).
  // `npx playwright install chromium` installs it.
  context = await chromium.launchPersistentContext(profile, {
    headless: true,
    channel: "chromium",
    viewport: { width: 1000, height: 800 },
  });
  const violations = [];
  const consoleErrors = [];
  await context.exposeBinding("__skyCspReport", (_src, v) => violations.push(v));
  await context.addInitScript(() => {
    document.addEventListener(
      "securitypolicyviolation",
      (e) => {
        try {
          window.__skyCspReport(`${e.violatedDirective} blocked=${e.blockedURI} src=${e.sourceFile}:${e.lineNumber}`);
        } catch (_) {}
      },
      true
    );
  });
  // A failed load is checked by URL (badResponses), not by its console line:
  // the full Chromium asks for /favicon.ico, which this app does not serve.
  const badResponses = [];
  const watch = (pg) => {
    pg.on("console", (m) => {
      if (m.type() === "error" && !m.text().startsWith("Failed to load resource")) consoleErrors.push(m.text());
    });
    pg.on("pageerror", (e) => consoleErrors.push("[pageerror] " + e.message));
    pg.on("response", (r) => {
      if (r.status() >= 400 && !r.url().endsWith("/favicon.ico")) badResponses.push(`${r.status()} ${r.url()}`);
    });
  };
  const page = await context.newPage();
  watch(page);

  await page.goto(ORIGIN + "/", { waitUntil: "load" });
  check(
    "the browser blocks cookies (a document.cookie write is dropped)",
    await page.evaluate(() => {
      document.cookie = "probe=1; path=/";
      return document.cookie === "";
    })
  );
  const tok0 = await page.evaluate(() => typeof __skyTok === "string" ? __skyTok : "");
  check("the client holds a 32-hex session token", /^[0-9a-f]{32}$/.test(tok0), tok0);
  check("the live stream connects without cookies (hello)", await waitFor(page, () => __skyHelloOk === true, null, 15000));
  check("the live stream is a fetch stream, not an EventSource", await page.evaluate(() => !!(__skySSE && __skySSE._ac && !__skySSE._es)));

  // ── counter over event POSTs ──
  for (let i = 0; i < 3; i++) await page.click("#inc");
  check("the counter works over event POSTs", await shows(page, "count=3", 10000), await state(page));

  // ── server pushes over the stream ──
  await page.click("#start");
  check("server pushes arrive over the stream (ticker)", await waitFor(page, () => /ticks=([5-9]|\d\d+)/.test(document.getElementById("state").textContent), null, 10000), await state(page));
  await page.click("#stop");

  // ── the stream drops and reconnects ──
  await page.waitForTimeout(400);
  await page.evaluate(() => {
    window.__skyHelloOk = false;
    __skySSE._ac.abort();
  });
  check("the stream reconnects after a drop", await waitFor(page, () => __skyHelloOk === true, null, 20000));
  check("the state survived the stream drop", (await field(page, "count")) === "3", await state(page));
  await page.click("#inc");
  check("the counter works after the reconnect", await shows(page, "count=4", 10000), await state(page));
  const ticksBefore = Number(await field(page, "ticks"));
  await page.click("#start");
  check(
    "server pushes resume after the reconnect",
    await waitFor(page, (n) => Number(/ticks=(\d+)/.exec(document.getElementById("state").textContent)[1]) >= n + 5, ticksBefore, 10000),
    await state(page)
  );
  await page.click("#stop");

  // ── rotation: sign in, the tab adopts a new token ──
  await page.click("#ask-key");
  check("Live.sessionKey answers", await waitFor(page, () => /key=[0-9a-f]{32}/.test(document.getElementById("state").textContent), null, 10000));
  const keyBefore = await field(page, "key");
  const tokBefore = await page.evaluate(() => __skyTok);
  await page.click("#signin");
  check("sign-in completes", await shows(page, "user=user-1", 10000), await state(page));
  check(
    "the rotation hands the tab a new token",
    await waitFor(page, (t) => /^[0-9a-f]{32}$/.test(__skyTok) && __skyTok !== t, tokBefore, 10000),
    await page.evaluate(() => __skyTok)
  );
  const tokAfter = await page.evaluate(() => __skyTok);
  await page.click("#inc");
  check("the counter works with the new token", await shows(page, "count=5", 10000), await state(page));
  await page.click("#ask-key");
  await page.waitForTimeout(300);
  check("Live.sessionKey is unchanged across the rotation", (await field(page, "key")) === keyBefore, `${keyBefore} -> ${await field(page, "key")}`);
  const oldTry = await fetch(ORIGIN + "/_sky/event", {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-Sky-Session": tokBefore, "Sec-Fetch-Site": "same-origin" },
    body: JSON.stringify({ msg: "", args: [], handlerId: "x", tab: "another-tab" }),
  });
  check(
    "the old token no longer drives the session (session-rotating)",
    oldTry.headers.get("x-sky-status") === "session-rotating",
    `${oldTry.status} ${oldTry.headers.get("x-sky-status")}`
  );
  const noHeader = await fetch(ORIGIN + "/_sky/event", {
    method: "POST",
    headers: { "Content-Type": "application/json", "Sec-Fetch-Site": "same-origin" },
    body: JSON.stringify({ msg: "", args: [], handlerId: "x" }),
  });
  check("a state-changing request without X-Sky-Session is refused (403)", noHeader.status === 403, String(noHeader.status));
  const crossSite = await fetch(ORIGIN + "/_sky/event", {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-Sky-Session": tokAfter, "Sec-Fetch-Site": "cross-site", Origin: "https://evil.example" },
    body: JSON.stringify({ msg: "", args: [], handlerId: "x" }),
  });
  check("a cross-site request with the token is refused (403)", crossSite.status === 403, String(crossSite.status));

  // ── fallback: no streaming fetch → a one-time SSE ticket ──
  const page2 = await context.newPage();
  watch(page2);
  await page2.addInitScript(() => {
    try {
      delete window.ReadableStream;
    } catch (_) {}
    window.ReadableStream = undefined;
  });
  await page2.goto(ORIGIN + "/", { waitUntil: "load" });
  check("fallback: the stream connects through a one-time ticket", await waitFor(page2, () => __skyHelloOk === true, null, 15000));
  check(
    "fallback: the stream is an EventSource on ?tk=",
    await page2.evaluate(() => !!(__skySSE && __skySSE._es && /[?&]tk=/.test(__skySSE._es.url)))
  );
  await page2.click("#inc");
  check("fallback: the counter works", await shows(page2, "count=1", 10000), await state(page2));
  await page2.click("#start");
  check("fallback: server pushes arrive", await waitFor(page2, () => /ticks=([3-9]|\d\d+)/.test(document.getElementById("state").textContent), null, 10000), await state(page2));
  await page2.click("#stop");

  check("zero securitypolicyviolation events", violations.length === 0, violations.slice(0, 3).join(" | "));
  check("zero console errors", consoleErrors.length === 0, consoleErrors.slice(0, 3).join(" | "));
  check("no failed request (other than /favicon.ico)", badResponses.length === 0, badResponses.slice(0, 3).join(" | "));
  const jar = await context.cookies();
  check("the browser holds no cookie at the end", jar.length === 0, jar.map((c) => c.name).join(","));
  check("the server never logged a session token", !serverLog.includes(tok0) && !serverLog.includes(tokAfter));
} catch (e) {
  failures.push(String(e && e.stack ? e.stack : e));
  console.log(`FAIL [${TAG}] ${e && e.stack ? e.stack : e}`);
} finally {
  if (context) await context.close().catch(() => {});
  proc.kill("SIGTERM");
  try {
    rmSync(profile, { recursive: true, force: true });
  } catch (_) {}
}

if (failures.length) {
  console.log(`${TAG}: FAIL (${failures.length})`);
  if (process.env.HEADER_SESSION_E2E_VERBOSE) console.log(serverLog);
  process.exit(1);
}
console.log(`${TAG}: PASS`);
