#!/usr/bin/env node
// scripts/spa-client-crypto-e2e-verify.mjs
//
// Browser e2e for `withClientCrypto` (driven by scripts/spa-client-crypto-e2e.sh).
// Starts the Go Noise responder with the test-only static key the fixture
// pins, starts the split backend with RELAY_URL naming the responder, opens the
// app and presses "start". The wasm client creates its key, sends message 1
// (relay step 1: SendHello → GotMsg2), completes the handshake, encrypts "ping"
// and sends it (relay step 2: SendEcho → GotEcho), then decrypts the answer.
// PASS when the page shows `STATUS=echo pong:ping|` and the responder saw both
// steps. A key never crosses: the backend requests carry only hex.
//
// Usage: node scripts/spa-client-crypto-e2e-verify.mjs <backend-app> <responder> [--port N]
// Exit: 0 PASS · 2 FAIL · 1 harness error.
import pw from "playwright";
const { chromium } = pw;
import { spawn } from "node:child_process";
import { guardChild } from "./lib/child-guard.mjs";
import { dirname } from "node:path";

function arg(name, def) {
  const i = process.argv.indexOf(name);
  return i >= 0 && process.argv[i + 1] ? process.argv[i + 1] : def;
}
const BACKEND = process.argv[2];
const RESPONDER = process.argv[3];
if (!BACKEND || !RESPONDER) {
  console.error("usage: spa-client-crypto-e2e-verify.mjs <backend-app> <responder> [--port N]");
  process.exit(1);
}
const PORT = Number(arg("--port", "9351"));
const URL = `http://127.0.0.1:${PORT}/`;
// The fixture's serverPublicHex is this key's public half.
const STATIC_KEY = "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20";

try {
  await fetch(URL, { signal: AbortSignal.timeout(1000) });
  console.error(`harness error: port ${PORT} is already serving; stop that process first`);
  process.exit(1);
} catch (_) {}

const responder = guardChild(spawn(RESPONDER, ["127.0.0.1:0", STATIC_KEY]));
let responderLog = "";
const listening = new Promise((resolve, reject) => {
  responder.stdout.on("data", (d) => {
    responderLog += d;
    const m = responderLog.match(/LISTEN (\S+)/);
    if (m) resolve(m[1]);
  });
  responder.stderr.on("data", (d) => (responderLog += d));
  responder.on("exit", (c) => reject(new Error(`responder exited ${c}\n${responderLog}`)));
  setTimeout(() => reject(new Error("responder did not listen\n" + responderLog)), 20000);
});
const relayAddr = await listening;

const proc = guardChild(
  spawn(BACKEND, [], {
    cwd: dirname(dirname(BACKEND)),
    env: { ...process.env, PORT: String(PORT), RELAY_URL: `http://${relayAddr}` },
  })
);
let serverLog = "";
proc.stdout.on("data", (d) => (serverLog += d));
proc.stderr.on("data", (d) => (serverLog += d));

const failures = [];
function check(step, ok, detail) {
  console.log(`${ok ? "ok  " : "FAIL"} ${step}${detail ? ": " + detail : ""}`);
  if (!ok) failures.push(step);
}

async function waitListening() {
  for (let i = 0; i < 80; i++) {
    try {
      const r = await fetch(URL);
      if (r.ok) return;
    } catch (_) {}
    await new Promise((r) => setTimeout(r, 250));
  }
  throw new Error("backend never listened\n" + serverLog);
}

let browser;
try {
  await waitListening();
  browser = await chromium.launch();
  const page = await browser.newPage();
  page.on("pageerror", (e) => check(`[pageerror] ${e.message}`, false));
  const rpcBodies = [];
  page.on("request", (r) => {
    if (r.url().includes("/_rpc/")) rpcBodies.push(`${r.url()} ${r.postData() || ""}`);
  });
  await page.goto(URL, { waitUntil: "load" });
  await page.waitForFunction(() => !document.documentElement.hasAttribute("data-sky-hydrating"), null, {
    timeout: 30000,
  });
  await page.getByText("start").click();
  const done = await page
    .waitForFunction(() => /STATUS=(echo [^|]*|failed)\|/.test(document.body.innerText), null, {
      timeout: 30000,
    })
    .then(() => true)
    .catch(() => false);
  const text = await page.evaluate(() => document.body.innerText);
  check("the page completed the handshake and the round trip", done && text.includes("STATUS=echo pong:ping|"), JSON.stringify(text));
  check("the responder saw the handshake", responderLog.includes("HANDSHAKE ok"), responderLog.trim());
  check("the responder saw the transport message", responderLog.includes("ECHO ok"), responderLog.trim());
  check(
    "both relay steps went through /_rpc",
    rpcBodies.some((b) => b.includes("/_rpc/SendHello")) && rpcBodies.some((b) => b.includes("/_rpc/SendEcho")),
    JSON.stringify(rpcBodies)
  );
  // Each relay request carries one hex string and nothing else: the device's
  // handshake and transport stay in the client.
  check(
    "the relay requests carry only the hex payload",
    rpcBodies.every((b) => /\{"spaArg0_":\["Ok","[0-9a-f]+"\]\}$/.test(b)),
    JSON.stringify(rpcBodies)
  );
} catch (e) {
  console.error("harness error:", e.message);
  console.error(serverLog);
  process.exit(1);
} finally {
  if (browser) await browser.close();
}
if (failures.length) {
  console.error(`FAIL: ${failures.length} check(s)\n--- backend ---\n${serverLog}\n--- responder ---\n${responderLog}`);
  process.exit(2);
}
console.log("PASS");
process.exit(0);
