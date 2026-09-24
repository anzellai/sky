#!/usr/bin/env node
// scripts/spa-stale-handler-verify.mjs
//
// Browser e2e for the Sky.Spa stale-handler bug: a re-rendered button kept the
// message payload bound at its FIRST render. The shared DOM diff compares event
// handlers by constructor name only (`Pick "a1"` and `Pick "b2"` both read
// "Pick"), so a payload-only change emits no patch; the wasm client's listener
// closure used to capture the message at bind time and never saw the new one.
//
// Drives the fixture rust/crates/sky/tests/fixtures/spa-stale-handler in real
// headless Chromium, on the two paths that reach it:
//   1. in-session: "Swap the list" replaces rows (ids a1 -> b2, same text); the
//      "Edit" / "Open" buttons must then dispatch the b2 payloads.
//   2. boot path: reload — the client hydrates the SSR seed render (ids a1) and
//      patches to the persisted model (ids b2) with identical text; the buttons
//      must dispatch the b2 payloads (the case a real app hit).
//
// Usage: node scripts/spa-stale-handler-verify.mjs <backend-app> [--port N]
// Exit: 0 PASS · 2 FAIL · 1 harness error.
import pw from "playwright";
const { chromium } = pw;
import { spawn } from "node:child_process";
import { dirname } from "node:path";

function arg(name, def) {
  const i = process.argv.indexOf(name);
  return i >= 0 && process.argv[i + 1] ? process.argv[i + 1] : def;
}
const BACKEND = process.argv[2];
if (!BACKEND) {
  console.error("usage: spa-stale-handler-verify.mjs <backend-app> [--port N]");
  process.exit(1);
}
const PORT = Number(arg("--port", "9011"));
const BACKEND_DIR = dirname(dirname(BACKEND));

const proc = spawn(BACKEND, [], { cwd: BACKEND_DIR, env: { ...process.env, PORT: String(PORT) } });
let serverLog = "";
proc.stdout.on("data", (d) => (serverLog += d));
proc.stderr.on("data", (d) => (serverLog += d));
const listening = new Promise((res) => proc.stdout.on("data", (d) => d.toString().includes("Sky server listening") && res()));

const failures = [];
async function picked(page) {
  const t = (await page.locator("#app").innerText()).replace(/\s+/g, " ");
  const m = t.match(/picked: (\S+)/);
  return m ? m[1] : "?";
}
async function clickAndExpect(page, label, want, step) {
  await page.locator(`button:has-text("${label}")`).first().click();
  await page.waitForTimeout(200);
  const got = await picked(page);
  const ok = got === want;
  console.log(`${ok ? "ok  " : "FAIL"} ${step}: click "${label}" -> picked ${got} (want ${want})`);
  if (!ok) failures.push(step);
}

let browser;
try {
  await Promise.race([
    listening,
    new Promise((_, rej) => setTimeout(() => rej(new Error("backend never listened\n" + serverLog)), 20000)),
  ]);
  browser = await chromium.launch({ headless: true });
  const page = await browser.newPage();
  const consoleMsgs = [];
  page.on("pageerror", (e) => consoleMsgs.push(`[pageerror] ${e.message}`));

  await page.goto(`http://127.0.0.1:${PORT}/`, { waitUntil: "networkidle" });
  await page.waitForTimeout(1500); // wasm boot + hydrate

  // Path 1 — in-session swap.
  await clickAndExpect(page, "Edit", "a1", "baseline (before swap)");
  await page.locator('button:has-text("Swap the list")').first().click();
  await page.waitForTimeout(200);
  await clickAndExpect(page, "Edit", "b2", "in-session swap, plain button");
  await clickAndExpect(page, "Open", "row-b2", "in-session swap, button in a row with a text sibling");

  // Path 2 — boot: hydrate the SSR seed (a1), then patch to the persisted model (b2).
  await page.reload({ waitUntil: "networkidle" });
  await page.waitForTimeout(1500);
  await clickAndExpect(page, "Edit", "b2", "boot path (restored model), plain button");
  await clickAndExpect(page, "Open", "row-b2", "boot path (restored model), button in a row");

  await browser.close();
  for (const m of consoleMsgs) console.log(m);
  if (consoleMsgs.length) failures.push("page errors");
  console.log(failures.length ? `VERDICT=FAIL ${failures.join("; ")}` : "VERDICT=PASS every button dispatched the current payload");
  process.exitCode = failures.length ? 2 : 0;
} catch (e) {
  console.error("HARNESS ERROR:", e.message);
  process.exitCode = 1;
} finally {
  // An error would leave Chromium open and Node would never exit (the gate
  // hangs instead of failing): close it, stop the app, exit explicitly.
  try {
    await browser?.close();
  } catch (_) {}
  proc.kill("SIGKILL");
  process.exit(process.exitCode ?? 1);
}
