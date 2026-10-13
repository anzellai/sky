#!/usr/bin/env node
// scripts/ai-device-verify.mjs
//
// Browser e2e for Std.Ai.Device (docs/ai/device.md). Drives the probe fixture
// rust/crates/sky/tests/fixtures/ai-device, built as a web:app, in real
// browsers under SKY_CSP=strict. The probe reads the slot's status, prepares
// it, asks one question and reports each step as a `SKY-PROBE` line on the
// backend's output, then streams the same question one piece per update
// (Provider.stream / read) and reports `stream=<pieces>:same` when the pieces
// join to the answer. See scripts/ai-device-e2e.sh for the scenarios.
//
// Usage: node scripts/ai-device-verify.mjs <backend> [--port N] [--scenarios a,b]
//   scenarios: chromium-cpu, chromium-webgpu, webkit-cpu, webkit-private,
//              server-placement, device-only-private
// Exit: 0 PASS · 2 FAIL · 1 harness error.
import pw from "playwright";
import { spawn } from "node:child_process";
import { dirname, join } from "node:path";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { guardChild } from "./lib/child-guard.mjs";

function arg(name, def) {
  const i = process.argv.indexOf(name);
  return i >= 0 && process.argv[i + 1] ? process.argv[i + 1] : def;
}
const BIN = process.argv[2];
if (!BIN) {
  console.error("usage: ai-device-verify.mjs <backend> [--port N] [--scenarios a,b]");
  process.exit(1);
}
const PORT = Number(arg("--port", "9391"));
const SCENARIOS = arg("--scenarios", "chromium-cpu,webkit-cpu,webkit-private,server-placement,device-only-private")
  .split(",").map((s) => s.trim()).filter(Boolean);

const failures = [];
function check(step, ok, detail) {
  console.log(`${ok ? "ok  " : "FAIL"} ${step}: ${detail}`);
  if (!ok) failures.push(step);
}

async function startApp(extraEnv) {
  const env = { ...process.env, PORT: String(PORT), SKY_CSP: "strict", ...extraEnv };
  const proc = guardChild(spawn(BIN, [], { cwd: dirname(dirname(BIN)), env }));
  let log = "";
  proc.stdout.on("data", (d) => (log += d));
  proc.stderr.on("data", (d) => (log += d));
  const deadline = Date.now() + 30000;
  while (!/listening/i.test(log)) {
    if (Date.now() > deadline || proc.exitCode !== null) throw new Error(`${BIN} never listened:\n${log}`);
    await new Promise((r) => setTimeout(r, 100));
  }
  return { proc, log: () => log };
}

async function stopApp(app) {
  app.proc.kill("SIGTERM");
  await new Promise((r) => (app.proc.exitCode !== null ? r() : app.proc.once("exit", r)));
}

// One browser context. `persistent` keeps a real profile (OPFS works in
// WebKit only then); `private` is the default ephemeral context, which in
// WebKit refuses OPFS like a private window.
async function openContext(engine, mode) {
  const args = mode === "webgpu" ? ["--enable-unsafe-webgpu", "--enable-gpu", "--ignore-gpu-blocklist"] : [];
  if (mode === "persistent") {
    const dir = mkdtempSync(join(tmpdir(), "sky-ai-device-"));
    const ctx = await pw[engine].launchPersistentContext(dir, { headless: true, args });
    return { ctx, close: async () => { await ctx.close(); rmSync(dir, { recursive: true, force: true }); } };
  }
  const browser = await pw[engine].launch({ headless: true, args });
  const ctx = await browser.newContext();
  return { ctx, close: async () => { await ctx.close(); await browser.close(); } };
}

// Run the probe once: the page loads, the probe reports status, prepared and
// answer. Returns the reported lines, the device requests the page made and
// the policy violations it saw.
async function runProbe(app, engine, mode) {
  const from = app.log().length;
  const { ctx, close } = await openContext(engine, mode);
  const requests = [];
  const violations = [];
  try {
    const page = await ctx.newPage();
    page.on("request", (r) => {
      const u = new URL(r.url());
      if (u.pathname.startsWith("/_sky/ai/")) requests.push(`${r.method()} ${u.pathname}`);
    });
    page.on("console", (m) => { if (/Content.Security.Policy|unsafe-eval/i.test(m.text())) violations.push(m.text()); });
    await page.addInitScript(() => {
      window.__csp = [];
      document.addEventListener("securitypolicyviolation", (e) => window.__csp.push(e.violatedDirective + " " + e.blockedURI));
    });
    await page.goto(`http://127.0.0.1:${PORT}/`);
    const deadline = Date.now() + 15 * 60 * 1000;
    while (!/SKY-PROBE stream=/.test(app.log().slice(from)) && Date.now() < deadline) {
      await page.waitForTimeout(250);
    }
    for (const v of await page.evaluate(() => window.__csp || [])) violations.push(v);
  } finally {
    await close();
  }
  const lines = {};
  for (const m of app.log().slice(from).matchAll(/SKY-PROBE (\w+)=(.*)/g)) lines[m[1]] = m[2].trim();
  return { lines, requests, violations };
}

const calls = (r) => r.requests.filter((q) => q.startsWith("POST ") && (q.endsWith("/call") || q.endsWith("/stream"))).length;
// `stream=<n>:same` with more than one piece: the answer arrived as it was written.
const streamedInPieces = (r) => { const m = /^(\d+):same$/.exec(r.lines.stream || ""); return !!m && Number(m[1]) > 1; };
const downloads = (r) => r.requests.filter((q) => q.startsWith("GET /_sky/ai/blob/")).length;

async function onDevice(name, engine, mode, backend) {
  const app = await startApp({});
  try {
    const t0 = Date.now();
    const r = await runProbe(app, engine, mode);
    const secs = ((Date.now() - t0) / 1000).toFixed(1);
    check(`${name}: status`, r.lines.status === `not-downloaded:${backend}`, r.lines.status);
    check(`${name}: prepared`, r.lines.prepared === `ready:${backend}`, r.lines.prepared);
    check(`${name}: answered on the device`, !!r.lines.answer && !r.lines.answer.startsWith("err:"), `${r.lines.answer} (${secs} s)`);
    check(`${name}: streamed on the device, piece by piece`, streamedInPieces(r), r.lines.stream);
    check(`${name}: downloaded from the app's own server`, downloads(r) > 0, `${downloads(r)} blob requests`);
    check(`${name}: privacy, no call reached the server`, calls(r) === 0, r.requests.filter((q) => q.startsWith("POST")).join(", ") || "none");
    check(`${name}: strict policy, no violation`, r.violations.length === 0, r.violations.join("; ") || "none");
  } finally {
    await stopApp(app);
  }
}

async function fromServer(name, engine, mode, env, wantStatus, wantCalls) {
  const app = await startApp(env);
  try {
    const r = await runProbe(app, engine, mode);
    check(`${name}: status`, r.lines.status === wantStatus, r.lines.status);
    check(`${name}: answered`, !!r.lines.answer && !r.lines.answer.startsWith("err:"), r.lines.answer);
    check(`${name}: streamed from the server, piece by piece`, streamedInPieces(r), r.lines.stream);
    check(`${name}: the server answered the call`, calls(r) === wantCalls, `${calls(r)} fallback calls`);
    check(`${name}: nothing downloaded`, downloads(r) === 0, `${downloads(r)} blob requests`);
    check(`${name}: strict policy, no violation`, r.violations.length === 0, r.violations.join("; ") || "none");
  } finally {
    await stopApp(app);
  }
}

async function deviceOnlyPrivate() {
  // DeviceOnly in a store-less browser: a typed StorageDenied, and no call
  // to the server at all (the route does not even exist).
  const app = await startApp({ SKY_AI_PROBE_PLACEMENT: "device" });
  try {
    const r = await runProbe(app, "webkit", "private");
    check("device-only-private: status", r.lines.status === "unsupported", r.lines.status);
    check("device-only-private: the call fails typed", /StorageDenied/.test(r.lines.answer || ""), r.lines.answer);
    check("device-only-private: the stream fails typed", /StorageDenied/.test(r.lines.stream || ""), r.lines.stream);
    check("device-only-private: never the server", calls(r) === 0, `${calls(r)} fallback calls`);
  } finally {
    await stopApp(app);
  }
}

try {
  for (const s of SCENARIOS) {
    if (s === "chromium-cpu") await onDevice(s, "chromium", "private", "wasm-cpu");
    else if (s === "chromium-webgpu") await onDevice(s, "chromium", "webgpu", "webgpu");
    else if (s === "webkit-cpu") await onDevice(s, "webkit", "persistent", "wasm-cpu");
    else if (s === "webkit-private") await fromServer(s, "webkit", "private", {}, "server", 2);
    else if (s === "server-placement") await fromServer(s, "chromium", "private", { SKY_AI_PROBE_PLACEMENT: "server" }, "server", 2);
    else if (s === "device-only-private") await deviceOnlyPrivate();
    else throw new Error("unknown scenario " + s);
  }
} catch (e) {
  console.error("ai-device-verify: harness error:", e && e.stack || e);
  process.exit(1);
}
console.log(failures.length === 0 ? "PASS" : `FAIL (${failures.length}): ${failures.join(", ")}`);
process.exit(failures.length === 0 ? 0 : 2);
