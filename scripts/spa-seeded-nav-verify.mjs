#!/usr/bin/env node
// scripts/spa-seeded-nav-verify.mjs
//
// Browser e2e for SPA-10 (register M): a Sky.Spa (web:app) deep link whose
// route `onNavigate` the SSR handler settles into the `#sky-model` seed.
// Driven by scripts/spa-examples-e2e.sh with the two fixtures
// rust/crates/sky/tests/fixtures/spa-seeded-nav{,-chain}.
//
//   settled  onNavigate reads data/items.json and stops. The SSR page shows
//            the items and is marked `data-sky-settled` "nav". The client
//            boots from the seed: the items are on the first paint, they are
//            still there after 3 s, `loads` stays 1, and the client makes NO
//            /_rpc call (onNavigate is not run a second time). Before the fix
//            the client booted from `init`, re-ran onNavigate over /_rpc with
//            `items: ""`, and the page lost the items.
//   chain    onNavigate reads an index whose result chains to a second read.
//            The one-round SSR settle does not finish it, so the page is NOT
//            marked "nav" and the client runs onNavigate itself, exactly once
//            (one /_rpc call); the items then load and `loads` ends at 1.
//
// Usage: node scripts/spa-seeded-nav-verify.mjs <settled|chain> <backend-app> [--port N]
// Exit: 0 PASS · 2 FAIL · 1 harness error.
import pw from "playwright";
const { chromium } = pw;
import { spawn } from "node:child_process";
import { dirname } from "node:path";

function arg(name, def) {
  const i = process.argv.indexOf(name);
  return i >= 0 && process.argv[i + 1] ? process.argv[i + 1] : def;
}
const MODE = process.argv[2];
const BACKEND = process.argv[3];
if (!["settled", "chain"].includes(MODE) || !BACKEND) {
  console.error("usage: spa-seeded-nav-verify.mjs <settled|chain> <backend-app> [--port N]");
  process.exit(1);
}
const PORT = Number(arg("--port", MODE === "settled" ? "9343" : "9344"));
const BASE = `http://127.0.0.1:${PORT}`;
const ITEMS = MODE === "settled" ? `items=["alpha","beta"]` : `items=["gamma","delta"]`;

try {
  await fetch(BASE + "/", { signal: AbortSignal.timeout(1000) });
  console.error(`${MODE}: harness error: port ${PORT} is already serving; stop that process first`);
  process.exit(1);
} catch (_) {}

const proc = spawn(BACKEND, [], {
  cwd: dirname(dirname(BACKEND)), // the backend dir: data/ and ../frontend/dist resolve from it
  env: { ...process.env, PORT: String(PORT) },
});
let serverLog = "";
proc.stdout.on("data", (d) => (serverLog += d));
proc.stderr.on("data", (d) => (serverLog += d));

const failures = [];
function check(step, ok, detail) {
  console.log(`${ok ? "ok  " : "FAIL"} ${step}${detail ? ": " + detail : ""}`);
  if (!ok) failures.push(step);
}
const norm = (s) => s.replace(/\s+/g, " ").trim();

let browser;
let code = 0;
try {
  let up = false;
  for (let i = 0; i < 120 && !up; i++) {
    try {
      const r = await fetch(BASE + "/", { signal: AbortSignal.timeout(1000) });
      up = r.status > 0;
    } catch (_) {
      await new Promise((r) => setTimeout(r, 250));
    }
  }
  if (!up) throw new Error("backend did not start:\n" + serverLog);

  // The server page: what the crawler (and the first paint) sees.
  const raw = await (await fetch(BASE + "/items")).text();
  const html = raw.replace(/&#34;|&quot;/g, '"');
  const marker = (html.match(/data-sky-settled="([^"]*)"/) || [null, null])[1];
  const navMarked = marker !== null && marker.split(/\s+/).includes("nav");
  if (MODE === "settled") {
    check("SSR page shows the settled items", html.includes(ITEMS) && html.includes("loads=1"));
    check("SSR page is marked: onNavigate finished on the server", navMarked, `data-sky-settled=${JSON.stringify(marker)}`);
  } else {
    check("SSR page is present (data-sky-settled marker)", marker !== null, `data-sky-settled=${JSON.stringify(marker)}`);
    check("SSR page is NOT marked nav: the chained read was not finished", !navMarked);
  }

  browser = await chromium.launch();
  const page = await browser.newPage();
  const rpcs = [];
  page.on("request", (r) => {
    if (r.url().includes("/_rpc/")) rpcs.push(r.url().replace(BASE, "") + " " + (r.postData() || ""));
  });
  const consoleErrors = [];
  page.on("console", (m) => {
    if (m.type() === "error" && m.text().includes("[sky.spa]")) consoleErrors.push(m.text());
  });

  // Record EVERY text #app shows, from the server paint on (a MutationObserver
  // installed before any page script runs), so a flash between the server
  // paint and the client's is seen even when it lasts a few milliseconds.
  await page.addInitScript(() => {
    window.__skyTexts = [];
    document.addEventListener("DOMContentLoaded", () => {
      const app = document.getElementById("app");
      if (!app) return;
      const rec = () => window.__skyTexts.push(app.innerText);
      rec();
      new MutationObserver(rec).observe(app, { subtree: true, childList: true, characterData: true, attributes: true });
    });
  });
  await page.goto(BASE + "/items", { waitUntil: "domcontentloaded" });
  const firstPaint = norm(await page.locator("#app").innerText());
  if (MODE === "settled") check("first paint shows the items", firstPaint.includes(ITEMS), firstPaint);

  // The client has booted once the hydration marker is cleared.
  await page.waitForFunction(() => !document.documentElement.hasAttribute("data-sky-hydrating"), null, { timeout: 20000 });
  await page.waitForTimeout(3000);
  const seen = (await page.evaluate(() => window.__skyTexts)).map(norm);
  const after = norm(await page.locator("#app").innerText());
  check("after 3 s the page shows the items", after.includes(ITEMS), after);
  check("after 3 s loads=1 (the data loaded exactly once)", after.includes("loads=1"), after);
  if (MODE === "settled") {
    const lost = [...new Set(seen.filter((s) => !s.includes(ITEMS)))];
    check(`the items never disappear (${seen.length} texts recorded)`, lost.length === 0, JSON.stringify(lost));
    check("a fully settled seeded boot makes zero /_rpc calls", rpcs.length === 0, JSON.stringify(rpcs));
  } else {
    check("an unfinished onNavigate runs once on the client (exactly one /_rpc call)", rpcs.length === 1, JSON.stringify(rpcs));
  }
  check("no [sky.spa] console error", consoleErrors.length === 0, JSON.stringify(consoleErrors));
} catch (e) {
  console.error(`${MODE}: harness error: ${e && e.stack ? e.stack : e}`);
  code = 1;
} finally {
  try {
    await browser?.close();
  } catch (_) {}
  proc.kill("SIGKILL");
  if (code === 0 && failures.length > 0) {
    console.error(`${MODE}: FAIL (${failures.length}): ${failures.join("; ")}`);
    code = 2;
  } else if (code === 0) {
    console.log(`${MODE}: PASS`);
  }
  process.exit(code);
}
