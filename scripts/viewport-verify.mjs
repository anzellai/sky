#!/usr/bin/env node
// scripts/viewport-verify.mjs
//
// Browser e2e for Sub.onBreakpoint and Sub.onQuery (runtime-go/rt/sub_viewport.go).
// Drives the fixture rust/crates/sky/tests/fixtures/std-app-viewport on BOTH
// targets (the web:app split and Sky.Live), in each browser, under
// SKY_CSP=strict. Run by scripts/nav-e2e.sh after the Std.Nav scenarios.
//
//   load     a page opened at /?page=7 at width 700: BP=600 PAGE=7, one query
//   resize   resizes inside one breakpoint deliver nothing (on Sky.Live: no
//            __skyViewport request); crossing 900 gives 900, below 600 gives 0
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

    const before = reports.length;
    for (const w of [720, 780, 850, 899]) {
      await page.setViewportSize({ width: w, height: 800 });
      await page.waitForTimeout(120);
    }
    await page.waitForTimeout(400);
    const s1 = await state(page);
    check(`${tag} resize: no Msg inside one breakpoint`, s1.startsWith("BP=600"), s1);
    if (target === "live") check(`${tag} resize: no request inside one breakpoint`, reports.length === before, `${reports.length - before} reports`);
    await page.setViewportSize({ width: 1000, height: 800 });
    const s2 = await waitState(page, (v) => v.startsWith("BP=900"), 10000);
    check(`${tag} resize: crossing 900`, s2.startsWith("BP=900"), s2);
    await page.setViewportSize({ width: 400, height: 800 });
    const s3 = await waitState(page, (v) => v.startsWith("BP=0 "), 10000);
    check(`${tag} resize: below every width is 0`, s3.startsWith("BP=0 "), s3);
    if (target === "live") check(`${tag} resize: one request per crossing`, reports.length === before + 2, `${reports.length - before} reports`);

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
  console.log(failures.length ? `VERDICT=FAIL ${failures.join("; ")}` : "VERDICT=PASS Sub.onBreakpoint and Sub.onQuery behave the same on web:app and Sky.Live");
  process.exitCode = failures.length ? 2 : 0;
} catch (e) {
  console.error("HARNESS ERROR:", e.message);
  process.exitCode = 1;
} finally {
  for (const a of apps) await stopApp(a);
  process.exit(process.exitCode ?? 1);
}
