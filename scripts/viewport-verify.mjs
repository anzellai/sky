#!/usr/bin/env node
// scripts/viewport-verify.mjs
//
// Browser e2e for Sub.onBreakpoint, Sub.viewport and Sub.onQuery
// (runtime-go/rt/sub_viewport.go).
// Drives the fixture rust/crates/sky/tests/fixtures/std-app-viewport on BOTH
// targets (the web:app split and Sky.Live), in each browser, under
// SKY_CSP=strict. Run by scripts/nav-e2e.sh after the Std.Nav scenarios.
//
//   load     a page opened at /?page=7 at width 700: BP=600 PAGE=7, one query,
//            W=700 (on Sky.Live: one __skyViewport request carries both)
//   resize   a burst of resizes inside one breakpoint delivers no breakpoint
//            and one width after it settles, W=899 (on Sky.Live: exactly one
//            request); crossing 900 gives 900 and W=1000, below 600 gives 0
//            and W=400 (on Sky.Live: one request per crossing, which carries
//            the width too, so three requests after load in all)
//   nav      Nav.pushUrl "/?page=8" and "/" deliver the new query (an empty
//            Dict for "/"); Back delivers the query before
//
// Usage: node scripts/viewport-verify.mjs <web:app-backend> <live-app> [--port N]
//   SKY_E2E_BROWSERS  comma list of chromium,webkit (default chromium)
//   SKY_E2E_CHANNEL   a Playwright Chromium channel, e.g. "chrome"
//   SKY_E2E_HEADED=1  run the browsers headed
// Exit: 0 PASS · 2 FAIL · 1 harness error.
import pw from "playwright";
import { spawn } from "node:child_process";
import { dirname } from "node:path";
import { guardChild } from "./lib/child-guard.mjs";

function arg(name, def) {
  const i = process.argv.indexOf(name);
  return i >= 0 && process.argv[i + 1] ? process.argv[i + 1] : def;
}
const SPA = process.argv[2];
const LIVE = process.argv[3];
if (!SPA || !LIVE) {
  console.error("usage: viewport-verify.mjs <web:app-backend> <live-app> [--port N]");
  process.exit(1);
}
const PORT = Number(arg("--port", "9391"));
const BROWSERS = (process.env.SKY_E2E_BROWSERS || "chromium").split(",").map((s) => s.trim()).filter(Boolean);
const HEADED = process.env.SKY_E2E_HEADED === "1";
const CHANNEL = process.env.SKY_E2E_CHANNEL || undefined;

const failures = [];
function check(step, ok, detail) {
  console.log(`${ok ? "ok  " : "FAIL"} ${step}: ${detail}`);
  if (!ok) failures.push(step);
}

async function startApp(bin, port) {
  const env = { PATH: "/usr/bin:/bin", LANG: "C", PORT: String(port), SKY_LIVE_PORT: String(port), SKY_CSP: "strict" };
  const proc = guardChild(spawn(bin, [], { cwd: dirname(dirname(bin)), env }));
  let log = "";
  proc.stdout.on("data", (d) => (log += d));
  proc.stderr.on("data", (d) => (log += d));
  const deadline = Date.now() + 30000;
  while (!/listening/i.test(log)) {
    if (Date.now() > deadline || proc.exitCode !== null) throw new Error("FixtureDidNotListen");
    await new Promise((r) => setTimeout(r, 100));
  }
  return { proc };
}

async function stopApp(app) {
  if (app.proc.exitCode !== null || app.proc.signalCode !== null) return;
  const exited = new Promise((resolve) => app.proc.once("exit", resolve));
  app.proc.kill("SIGTERM");
  let timer;
  const done = await Promise.race([exited.then(() => true), new Promise((resolve) => { timer = setTimeout(() => resolve(false), 5000); })]);
  clearTimeout(timer);
  if (!done) { app.proc.kill("SIGKILL"); await exited; }
}

async function launch(name) {
  if (name === "webkit") return pw.webkit.launch({ headless: !HEADED });
  return pw.chromium.launch({ headless: !HEADED, channel: CHANNEL });
}

const state = async (page) => (await page.locator("#state").innerText()).trim();
async function waitState(page, pred, ms) {
  const deadline = Date.now() + ms;
  let v = await state(page);
  while (!pred(v) && Date.now() < deadline) {
    await page.waitForTimeout(50);
    v = await state(page);
  }
  return v;
}
const width = async (page) => (await page.locator("#width").innerText()).trim();
async function waitWidth(page, want, ms) {
  const deadline = Date.now() + ms;
  let v = await width(page);
  while (v !== want && Date.now() < deadline) {
    await page.waitForTimeout(50);
    v = await width(page);
  }
  return v;
}
const path = (page) => page.evaluate(() => location.pathname + location.search);

async function scenario(browserName, target, base) {
  const browser = await launch(browserName);
  const tag = `${target}/${browserName}`;
  const page = await browser.newPage({ viewport: { width: 700, height: 800 } });
  const errors = [];
  const reports = [];
  page.on("pageerror", (e) => errors.push(e.message));
  page.on("console", (m) => {
    if (m.type() === "error" && !/favicon/.test(m.location()?.url || "")) errors.push(m.text());
  });
  page.on("request", (r) => {
    if (r.url().endsWith("/_sky/event") && /__skyViewport/.test(r.postData() || "")) reports.push(r.postData());
  });
  try {
    await page.goto(`${base}/?page=7`, { waitUntil: "load" });
    const s0 = await waitState(page, (v) => v.startsWith("BP=600 PAGE=7"), 15000);
    check(`${tag} load: breakpoint and query at load`, s0 === "BP=600 PAGE=7 QUERIES=1", s0);
    const w0 = await waitWidth(page, "W=700", 10000);
    check(`${tag} load: width at load`, w0 === "W=700", w0);
    if (target === "live") check(`${tag} load: one request for the breakpoint and the width`, reports.length === 1, `${reports.length} reports`);

    const before = reports.length;
    // A burst: the resizes come faster than the 150 ms settle time, so the
    // width is delivered once, after the last one.
    for (const w of [720, 780, 850, 899]) {
      await page.setViewportSize({ width: w, height: 800 });
      await page.waitForTimeout(30);
    }
    await page.waitForTimeout(400);
    const s1 = await state(page);
    check(`${tag} resize: no breakpoint Msg inside one breakpoint`, s1.startsWith("BP=600"), s1);
    const w1 = await waitWidth(page, "W=899", 5000);
    check(`${tag} resize: one width after the burst settles`, w1 === "W=899", w1);
    if (target === "live") check(`${tag} resize: one request inside one breakpoint, after it settles`, reports.length === before + 1, `${reports.length - before} reports`);
    await page.setViewportSize({ width: 1000, height: 800 });
    const s2 = await waitState(page, (v) => v.startsWith("BP=900"), 10000);
    check(`${tag} resize: crossing 900`, s2.startsWith("BP=900"), s2);
    const w2 = await waitWidth(page, "W=1000", 5000);
    check(`${tag} resize: the width after crossing 900`, w2 === "W=1000", w2);
    await page.waitForTimeout(400);
    await page.setViewportSize({ width: 400, height: 800 });
    const s3 = await waitState(page, (v) => v.startsWith("BP=0 "), 10000);
    check(`${tag} resize: below every width is 0`, s3.startsWith("BP=0 "), s3);
    const w3 = await waitWidth(page, "W=400", 5000);
    check(`${tag} resize: the width below every breakpoint`, w3 === "W=400", w3);
    await page.waitForTimeout(400);
    if (target === "live") check(`${tag} resize: one request per crossing, none after it settles`, reports.length === before + 3, `${reports.length - before} reports`);

    await page.click("#page8");
    const s4 = await waitState(page, (v) => v.includes("PAGE=8"), 10000);
    check(`${tag} nav: pushUrl delivers the new query`, s4 === "BP=0 PAGE=8 QUERIES=2" && (await path(page)) === "/?page=8", `${await path(page)} ${s4}`);
    await page.click("#home");
    const s5 = await waitState(page, (v) => v.includes("QUERIES=3"), 10000);
    check(`${tag} nav: a removed query is an empty Dict`, s5 === "BP=0 PAGE= QUERIES=3" && (await path(page)) === "/", `${await path(page)} ${s5}`);
    await page.goBack();
    const s6 = await waitState(page, (v) => v.includes("QUERIES=4"), 10000);
    check(`${tag} nav: Back delivers the query before`, s6 === "BP=0 PAGE=8 QUERIES=4", `${await path(page)} ${s6}`);
    check(`${tag} no page errors`, errors.length === 0, errors.join(" | ") || "none");
  } finally {
    await browser.close();
  }
}

let apps = [];
try {
  for (const [target, bin, port] of [
    ["web:app", SPA, PORT],
    ["live", LIVE, PORT + 1],
  ]) {
    const app = await startApp(bin, port);
    apps.push(app);
    for (const b of BROWSERS) await scenario(b, target, `http://127.0.0.1:${port}`);
    await stopApp(app);
  }
  console.log(failures.length ? `VERDICT=FAIL ${failures.join("; ")}` : "VERDICT=PASS Sub.onBreakpoint, Sub.viewport and Sub.onQuery behave the same on web:app and Sky.Live");
  process.exitCode = failures.length ? 2 : 0;
} catch (e) {
  console.error("HARNESS ERROR:", e.message);
  process.exitCode = 1;
} finally {
  for (const a of apps) await stopApp(a);
  process.exit(process.exitCode ?? 1);
}
