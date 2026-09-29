#!/usr/bin/env node
// scripts/spa-websocket-verify.mjs
//
// Browser e2e: a Sky.Spa (web:app) client holds its own WebSocket to its
// backend. Drives rust/crates/sky/tests/fixtures/spa-websocket under
// SKY_CSP=strict (`connect-src 'self'`):
//
//   sub   socket 1, read by `WebSocket.onMessage`: a text frame comes back as
//         `echo hello`, a binary frame comes back doubled, byte for byte
//   task  socket 2, read by a Task: `send` then `receiveWithin` answers
//         `echo ping2`
//
// The socket URL is the path `/ws`, so it is same-origin; the page must load
// and connect with no CSP violation and no page error.
//
// Usage: node scripts/spa-websocket-verify.mjs <web:app-backend> [--port N]
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
const APP = process.argv[2];
if (!APP) {
  console.error("usage: spa-websocket-verify.mjs <web:app-backend> [--port N]");
  process.exit(1);
}
const PORT = Number(arg("--port", "9371"));
const BROWSERS = (process.env.SKY_E2E_BROWSERS || "chromium").split(",").map((s) => s.trim()).filter(Boolean);
const HEADED = process.env.SKY_E2E_HEADED === "1";
const CHANNEL = process.env.SKY_E2E_CHANNEL || undefined;

const failures = [];
function check(step, ok, detail) {
  console.log(`${ok ? "ok  " : "FAIL"} ${step}: ${detail}`);
  if (!ok) failures.push(step);
}

const proc = guardChild(
  spawn(APP, [], { cwd: dirname(dirname(APP)), env: { ...process.env, PORT: String(PORT), SKY_CSP: "strict" } }),
);
let log = "";
proc.stdout.on("data", (d) => (log += d));
proc.stderr.on("data", (d) => (log += d));

const text = async (page, id) => (await page.locator("#" + id).innerText()).trim();
async function waitFor(page, id, pred, ms) {
  const deadline = Date.now() + ms;
  let v = await text(page, id);
  while (!pred(v) && Date.now() < deadline) {
    await page.waitForTimeout(50);
    v = await text(page, id);
  }
  return v;
}

try {
  const deadline = Date.now() + 30000;
  while (!/listening/i.test(log)) {
    if (Date.now() > deadline || proc.exitCode !== null) throw new Error("backend never listened:\n" + log);
    await new Promise((r) => setTimeout(r, 100));
  }
  for (const name of BROWSERS) {
    const browser =
      name === "webkit"
        ? await pw.webkit.launch({ headless: !HEADED })
        : await pw.chromium.launch({ headless: !HEADED, channel: CHANNEL });
    try {
      const page = await browser.newPage();
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      page.on("console", (m) => {
        // A missing favicon is the browser's own request, not the app's.
        if (m.type() === "error" && !/favicon/.test(m.location()?.url || "")) errors.push(m.text());
      });
      const resp = await page.goto(`http://127.0.0.1:${PORT}/`, { waitUntil: "networkidle" });
      const csp = resp.headers()["content-security-policy"] || "";
      check(`${name} strict CSP is in force`, csp.includes("connect-src 'self'"), csp || "(no header)");
      await page.waitForTimeout(1500);

      await page.click("#connect");
      let v = await waitFor(page, "log", (s) => s.includes("open") || s.includes("failed"), 10000);
      check(`${name} sub: socket opens`, v === "LOG=open;", v);
      await page.click("#sendText");
      v = await waitFor(page, "log", (s) => s.includes("T:"), 10000);
      check(`${name} sub: text echo arrives as a Msg`, v === "LOG=open;T:echo hello;", v);
      await page.click("#sendBinary");
      v = await waitFor(page, "log", (s) => s.includes("B:"), 10000);
      check(`${name} sub: binary echo arrives byte for byte`, v === "LOG=open;T:echo hello;B:bébé;", v);

      await page.click("#connect2");
      v = await waitFor(page, "got2", (s) => s !== "GOT2=-", 10000);
      check(`${name} task: second socket opens`, v === "GOT2=open", v);
      await page.click("#ping2");
      v = await waitFor(page, "got2", (s) => s !== "GOT2=open", 10000);
      check(`${name} task: send + receiveWithin`, v === "GOT2=echo ping2", v);
      check(`${name} no page errors or CSP violations`, errors.length === 0, errors.join(" | ") || "none");
    } finally {
      await browser.close();
    }
  }
  console.log(failures.length ? `VERDICT=FAIL ${failures.join("; ")}` : "VERDICT=PASS the web:app client exchanged text and binary frames over its own WebSocket");
  process.exitCode = failures.length ? 2 : 0;
} catch (e) {
  console.error("HARNESS ERROR:", e.message);
  console.error(log.slice(-2000));
  process.exitCode = 1;
} finally {
  proc.kill("SIGKILL");
  process.exit(process.exitCode ?? 1);
}
