#!/usr/bin/env node
// scripts/document-verify.mjs
//
// Browser e2e for App.withDocument (runtime-go/rt/document_attrs.go). Drives
// the fixture rust/crates/sky/tests/fixtures/std-app-document on BOTH targets
// (the web:app split and Sky.Live), in each browser, under SKY_CSP=strict.
// See scripts/document-e2e.sh for the steps. A page marker set after load
// must survive every step: no step reloads the page.
//
// Usage: node scripts/document-verify.mjs <web:app-backend> <live-app> --fixture-root <dir> [--port N]
//   SKY_E2E_BROWSERS  comma list of chromium,webkit (default chromium)
//   SKY_E2E_CHANNEL   a Playwright Chromium channel, e.g. "chrome"
//   SKY_E2E_HEADED=1  run the browsers headed
// Exit: 0 PASS · 2 FAIL · 1 harness error.
import pw from "playwright";
import { spawn } from "node:child_process";
import { dirname, resolve } from "node:path";
import { guardChild } from "./lib/child-guard.mjs";

function arg(name, def) {
  const i = process.argv.indexOf(name);
  return i >= 0 && process.argv[i + 1] ? process.argv[i + 1] : def;
}
const SPA = process.argv[2];
const LIVE = process.argv[3];
if (!SPA || !LIVE) {
  console.error("usage: document-verify.mjs <web:app-backend> <live-app> --fixture-root <dir> [--port N]");
  process.exit(1);
}
const PORT = Number(arg("--port", "9401"));
const BROWSERS = (process.env.SKY_E2E_BROWSERS || "chromium").split(",").map((s) => s.trim()).filter(Boolean);
const HEADED = process.env.SKY_E2E_HEADED === "1";
const CHANNEL = process.env.SKY_E2E_CHANNEL || undefined;

const failures = [];
function check(step, ok, detail) {
  console.log(`${ok ? "ok  " : "FAIL"} ${step}: ${detail}`);
  if (!ok) failures.push(step);
}

async function startApp(bin, port) {
  const fixtureRoot = resolve(arg("--fixture-root", ""));
  const known = [".skyapp/web-app/.split/backend/sky-out/app", ".skyapp/web/sky-out/app"];
  if (!arg("--fixture-root", "") || !known.some((p) => resolve(fixtureRoot, p) === resolve(bin))) {
    throw new Error("UnexpectedFixtureBinary");
  }
  const env = { PATH: "/usr/bin:/bin", LANG: "C", PORT: String(port), SKY_LIVE_PORT: String(port), SKY_CSP: "strict" };
  const proc = guardChild(spawn(bin, [], { cwd: dirname(dirname(bin)), env }));
  let log = "";
  proc.stdout.on("data", (d) => (log += d));
  proc.stderr.on("data", (d) => (log += d));
  const deadline = Date.now() + 30000;
  while (!/listening/i.test(log)) {
    if (Date.now() > deadline || proc.exitCode !== null) throw new Error("FixtureDidNotListen:\n" + log);
    await new Promise((r) => setTimeout(r, 100));
  }
  return { proc, log: () => log };
}

async function stopApp(app) {
  if (app.proc.exitCode !== null || app.proc.signalCode !== null) return;
  const exited = new Promise((r) => app.proc.once("exit", r));
  app.proc.kill("SIGTERM");
  let timer;
  const done = await Promise.race([exited.then(() => true), new Promise((r) => { timer = setTimeout(() => r(false), 5000); })]);
  clearTimeout(timer);
  if (!done) { app.proc.kill("SIGKILL"); await exited; }
}

async function launch(name) {
  if (name === "webkit") return pw.webkit.launch({ headless: !HEADED });
  return pw.chromium.launch({ headless: !HEADED, channel: CHANNEL });
}

const state = async (page) => (await page.locator("#state").innerText()).trim();
async function waitFor(page, pred, ms) {
  const deadline = Date.now() + ms;
  let v = await pred();
  while (!v && Date.now() < deadline) {
    await page.waitForTimeout(50);
    v = await pred();
  }
  return v;
}
const html = (page) => page.evaluate(() => ({
  lang: document.documentElement.lang,
  dir: document.documentElement.dir,
  theme: document.documentElement.getAttribute("data-theme"),
  hydrating: document.documentElement.hasAttribute("data-sky-hydrating"),
  marker: window.__docMarker === 1,
}));

async function scenario(browserName, target, url) {
  const browser = await launch(browserName);
  const tag = `${target}/${browserName}`;
  const page = await browser.newPage();
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  page.on("console", (m) => {
    if (m.type() !== "error") return;
    if (/favicon/.test(m.location()?.url || "")) return;
    errors.push(m.text());
  });
  try {
    await page.goto(url, { waitUntil: "load" });
    // The first paint carries the attributes before any script runs.
    const raw = await (await page.request.get(url)).text();
    check(`${tag} first-paint`, /<html lang="en" dir="ltr"[ >]/.test(raw), raw.slice(0, 120));
    // The client is live when it has rendered the state line.
    await waitFor(page, async () => /LOCALE=en/.test(await state(page)), 20000);
    if (target === "web:app") {
      const live = await waitFor(page, async () => !(await html(page)).hydrating, 20000);
      check(`${tag} hydrated`, live, "data-sky-hydrating is cleared");
    }
    await page.evaluate(() => { window.__docMarker = 1; });
    let h = await html(page);
    check(`${tag} load`, h.lang === "en" && h.dir === "ltr", JSON.stringify(h));

    // Switch to Arabic: lang, dir and a data-* value with quotes, < and &.
    await page.click("#ar");
    await waitFor(page, async () => (await html(page)).lang === "ar", 10000);
    h = await html(page);
    check(`${tag} ar`, h.lang === "ar" && h.dir === "rtl" && h.theme === 'dark "<&' && h.marker, JSON.stringify(h));

    // A view change with the same set keeps the attributes.
    await page.click("#note");
    await waitFor(page, async () => /CLICKS=1/.test(await state(page)), 10000);
    h = await html(page);
    check(`${tag} note`, h.lang === "ar" && h.dir === "rtl" && h.marker, JSON.stringify(h));

    // A set without data-theme removes it; the language and direction follow.
    await page.click("#fr");
    await waitFor(page, async () => (await html(page)).lang === "fr-CA", 10000);
    h = await html(page);
    check(`${tag} fr`, h.lang === "fr-CA" && h.dir === "ltr" && h.theme === null && h.marker, JSON.stringify(h));

    await page.click("#en");
    await waitFor(page, async () => (await html(page)).lang === "en", 10000);
    h = await html(page);
    check(`${tag} en`, h.lang === "en" && h.dir === "ltr" && h.marker, JSON.stringify(h));
    check(`${tag} no-errors`, errors.length === 0, errors.join(" | ") || "none");
  } finally {
    await browser.close();
  }
}

let exitCode = 0;
try {
  for (const [target, bin, port] of [["web:app", SPA, PORT], ["web", LIVE, PORT + 1]]) {
    const app = await startApp(bin, port);
    try {
      for (const b of BROWSERS) await scenario(b, target, `http://localhost:${port}/`);
    } finally {
      await stopApp(app);
    }
  }
  if (failures.length) {
    console.error(`document-verify: FAIL (${failures.length}): ${failures.join(", ")}`);
    exitCode = 2;
  } else {
    console.log("document-verify: PASS");
  }
} catch (e) {
  console.error("document-verify: harness error:", e && e.stack ? e.stack : e);
  exitCode = 1;
}
process.exit(exitCode);
