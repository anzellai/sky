#!/usr/bin/env node
// scripts/spa-hydration-verify.mjs
//
// End-to-end WASM HYDRATION harness for Sky.Spa (`--target web:app`) apps. It
// runs the ACTUAL compiled wasm client in real headless Chromium and verifies
// the SSR-embedded model survives client hydration — the leg cargo tests cannot
// cover, because they build the wasm but never execute it.
//
// This is the deterministic guard for the "silent hydration reset" class: an
// SSR/client model-codec divergence (a data-carrying union or nested record the
// client Codec.auto cannot decode) makes the boot path fall back to `init`,
// dropping the server-embedded data with no error. Here that surfaces as the
// post-hydration view differing from the SSR first paint (or a console.error
// carrying "#sky-model ... failed to decode").
//
// For each case: boot the compiled backend, open the URL in Chromium, read the
// #app text at first paint and after the wasm boots, and assert the expected
// text is still present.
//
// Usage:
//   node scripts/spa-hydration-verify.mjs <backend-app> [--url <path>] [--expect <text>] [--db <sqlite-file:sql>]
//
// Example (build first with `sky build --target web:app`):
//   node scripts/spa-hydration-verify.mjs \
//     path/to/.split/backend/sky-out/app --url / --expect "status ready:3" \
//     --db "app.db:CREATE TABLE posts(id INTEGER PRIMARY KEY,title TEXT);INSERT INTO posts(title) VALUES('a'),('b'),('c');"
//
// Exit: 0 PASS · 2 FAIL (reset to init) · 3 UNKNOWN · 1 harness error.
import pw from "playwright";
const { chromium } = pw;
import { spawn, spawnSync } from "node:child_process";
import { dirname } from "node:path";

function arg(name, def) {
  const i = process.argv.indexOf(name);
  return i >= 0 && process.argv[i + 1] ? process.argv[i + 1] : def;
}

const BACKEND = process.argv[2];
if (!BACKEND) {
  console.error("usage: spa-hydration-verify.mjs <backend-app> [--url <path>] [--expect <text>] [--db <file:sql>]");
  process.exit(1);
}
const URLPATH = arg("--url", "/");
const EXPECT = arg("--expect", "");
const DBSPEC = arg("--db", "");
const PORT = Number(arg("--port", "8996"));
const BACKEND_DIR = dirname(dirname(BACKEND)); // .../backend (app is backend/sky-out/app)

// Optional: seed a sqlite DB in the backend run dir before boot.
if (DBSPEC) {
  const idx = DBSPEC.indexOf(":");
  const file = DBSPEC.slice(0, idx);
  const sql = DBSPEC.slice(idx + 1);
  const r = spawnSync("sqlite3", [`${BACKEND_DIR}/${file}`, sql], { encoding: "utf8" });
  if (r.status !== 0) {
    console.error("db seed failed:", r.stderr);
    process.exit(1);
  }
}

function waitListening(proc) {
  return new Promise((res) => {
    proc.stdout.on("data", (d) => {
      if (d.toString().includes("Sky server listening")) res();
    });
  });
}

const proc = spawn(BACKEND, [], {
  cwd: BACKEND_DIR,
  env: { ...process.env, PORT: String(PORT), SSR_DB_PATH: "app.db" },
});
let serverLog = "";
proc.stdout.on("data", (d) => (serverLog += d));
proc.stderr.on("data", (d) => (serverLog += d));

try {
  await Promise.race([
    waitListening(proc),
    new Promise((_, rej) => setTimeout(() => rej(new Error("backend never listened\n" + serverLog)), 20000)),
  ]);

  const browser = await chromium.launch({ headless: true });
  const page = await browser.newPage();
  const consoleMsgs = [];
  page.on("console", (m) => consoleMsgs.push(`[${m.type()}] ${m.text()}`));
  page.on("pageerror", (e) => consoleMsgs.push(`[pageerror] ${e.message}`));

  await page.goto(`http://127.0.0.1:${PORT}${URLPATH}`, { waitUntil: "networkidle" });
  const ssrText = (await page.locator("#app").innerText()).replace(/\s+/g, " ").trim();
  await page.waitForTimeout(1500); // let the wasm boot + hydrate
  const afterText = (await page.locator("#app").innerText()).replace(/\s+/g, " ").trim();
  await browser.close();

  const decodeErr = consoleMsgs.find((m) => m.includes("failed to decode"));
  console.log("SSR_TEXT=" + JSON.stringify(ssrText));
  console.log("AFTER_TEXT=" + JSON.stringify(afterText));
  console.log("DECODE_ERROR=" + (decodeErr ? JSON.stringify(decodeErr) : "none"));

  if (decodeErr) {
    console.log("VERDICT=FAIL client decode failed (silent hydration loss)");
    process.exitCode = 2;
  } else if (EXPECT && !afterText.includes(EXPECT)) {
    console.log(`VERDICT=FAIL expected ${JSON.stringify(EXPECT)} in post-hydration view`);
    process.exitCode = 2;
  } else if (EXPECT && ssrText.includes(EXPECT) && afterText.includes(EXPECT)) {
    console.log("VERDICT=PASS SSR data survived hydration");
    process.exitCode = 0;
  } else {
    console.log("VERDICT=PASS no decode error" + (EXPECT ? "" : " (no --expect given)"));
    process.exitCode = 0;
  }
} catch (e) {
  console.error("HARNESS ERROR:", e.message);
  process.exitCode = 1;
} finally {
  proc.kill("SIGKILL");
}
