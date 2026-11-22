#!/usr/bin/env node
// scripts/i18n-verify.mjs
//
// Browser e2e for Std.I18n. Drives examples/78-i18n-booking on BOTH targets
// (the web:app split and Sky.Live), in each browser, under SKY_CSP=strict.
// See scripts/i18n-e2e.sh for the steps. The server page text is read with
// JavaScript off, so it is the first paint as the server wrote it. The live
// page text is read after the client starts. The two must be equal.
//
// Usage: node scripts/i18n-verify.mjs <web:app-backend> <live-app> --fixture-root <dir> [--port N]
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
  console.error("usage: i18n-verify.mjs <web:app-backend> <live-app> --fixture-root <dir> [--port N]");
  process.exit(1);
}
const PORT = Number(arg("--port", "9411"));
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

const PAGES = [
  { path: "/", lang: "en-GB", dir: "ltr" },
  { path: "/zh", lang: "zh-Hans", dir: "ltr" },
  { path: "/ar", lang: "ar", dir: "rtl" },
  { path: "/xa", lang: "en-XA", dir: "ltr" },
];

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
  hydrating: document.documentElement.hasAttribute("data-sky-hydrating"),
  marker: window.__i18nMarker === 1,
}));
const text = (page) => page.evaluate(() => document.body.innerText.replace(/\s+/g, " ").trim());
const attr = (re, raw) => (raw.match(re) || [])[1];

async function scenario(browserName, target, base) {
  const browser = await launch(browserName);
  const tag = `${target}/${browserName}`;
  const errors = [];
  try {
    const still = await browser.newContext({ javaScriptEnabled: false });
    const ctx = await browser.newContext();
    for (const p of PAGES) {
      const url = base + p.path;
      const step = `${tag} ${p.path}`;
      // The first paint: the server writes the locale's lang and dir.
      const raw = await (await ctx.request.get(url)).text();
      const open = (raw.match(/<html[^>]*>/) || [""])[0];
      check(`${step} first-paint`, attr(/ lang="([^"]*)"/, open) === p.lang && attr(/ dir="([^"]*)"/, open) === p.dir, open);
      // The server text, with no script run.
      const sp = await still.newPage();
      await sp.goto(url, { waitUntil: "load" });
      const serverText = await text(sp);
      await sp.close();
      check(`${step} server-text`, serverText.length > 20, serverText.slice(0, 80));
      // The live page, after the client starts.
      const page = await ctx.newPage();
      page.on("pageerror", (e) => errors.push(`${p.path}: ${e.message}`));
      page.on("console", (m) => {
        if (m.type() !== "error" && !/hydrat/i.test(m.text())) return;
        if (/favicon/.test(m.location()?.url || "")) return;
        errors.push(`${p.path}: ${m.text()}`);
      });
      await page.goto(url, { waitUntil: "load" });
      if (target === "web:app") {
        const live = await waitFor(page, async () => !(await html(page)).hydrating, 30000);
        check(`${step} hydrated`, live, "data-sky-hydrating is cleared");
      }
      await page.waitForTimeout(300);
      const liveText = await text(page);
      check(`${step} same-text`, liveText === serverText, liveText === serverText ? "equal" : `server=${JSON.stringify(serverText)} client=${JSON.stringify(liveText)}`);
      const h = await html(page);
      check(`${step} html`, h.lang === p.lang && h.dir === p.dir, JSON.stringify(h));
      await page.close();
    }
    // A language switch through a sky-nav link: lang and dir follow, no reload.
    const page = await ctx.newPage();
    page.on("pageerror", (e) => errors.push(`nav: ${e.message}`));
    await page.goto(base + "/", { waitUntil: "load" });
    if (target === "web:app") await waitFor(page, async () => !(await html(page)).hydrating, 30000);
    await page.evaluate(() => { window.__i18nMarker = 1; });
    await page.click('a[href="/ar"]');
    await waitFor(page, async () => (await html(page)).lang === "ar", 10000);
    const h = await html(page);
    check(`${tag} nav-ar`, h.lang === "ar" && h.dir === "rtl" && h.marker, JSON.stringify(h));
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
    console.error(`i18n-verify: FAIL (${failures.length}): ${failures.join(", ")}`);
    exitCode = 2;
  } else {
    console.log("i18n-verify: PASS");
  }
} catch (e) {
  console.error("i18n-verify: harness error:", e && e.stack ? e.stack : e);
  exitCode = 1;
}
process.exit(exitCode);
