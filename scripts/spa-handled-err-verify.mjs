#!/usr/bin/env node
// scripts/spa-handled-err-verify.mjs
//
// Browser e2e for the Sky.Spa client's error reports (runtime-go/rt/spa_perform.go).
// A Task's Err is a result: the client delivers it to its Msg and never logs
// it as an RPC failure. The loud "[sky.spa] RPC failed; kept last good model
// (no app-level handler …)" line is for one case only: a server-branch RPC that
// failed while the app declared no `App.withRpcError`.
//
// Drives rust/crates/sky/tests/fixtures/spa-handled-err (web:app), in each
// browser, under SKY_CSP=strict:
//
//   local  `init` performs `Native.secureGet` (no native shell: Err) and the
//          "local" button performs `Task.fail`; the app handles both. The view
//          shows both handled, no /_rpc request is sent, and the console has
//          NO error (the v0.27.0 release head wrote two "RPC failed" lines).
//   rpc    `Price` is a server branch. Its /_rpc/Price answers 500 (routed by
//          this script). The model is kept (PRICE=-) and the console has the
//          loud "RPC failed" line exactly once.
//
// Usage: node scripts/spa-handled-err-verify.mjs <web:app-backend> [--port N]
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
if (!SPA) {
  console.error("usage: spa-handled-err-verify.mjs <web:app-backend> [--port N]");
  process.exit(1);
}
const PORT = Number(arg("--port", "9363"));
const BROWSERS = (process.env.SKY_E2E_BROWSERS || "chromium").split(",").map((s) => s.trim()).filter(Boolean);
const HEADED = process.env.SKY_E2E_HEADED === "1";
const CHANNEL = process.env.SKY_E2E_CHANNEL || undefined;
const RPC_FAILED = "[sky.spa] RPC failed; kept last good model (no app-level handler for this transport error):";

const failures = [];
function check(step, ok, detail) {
  console.log(`${ok ? "ok  " : "FAIL"} ${step}: ${detail}`);
  if (!ok) failures.push(step);
}

async function startApp(bin, port) {
  const env = { ...process.env, PORT: String(port), SKY_LIVE_PORT: String(port), SKY_CSP: "strict" };
  const proc = guardChild(spawn(bin, [], { cwd: dirname(dirname(bin)), env }));
  let log = "";
  proc.stdout.on("data", (d) => (log += d));
  proc.stderr.on("data", (d) => (log += d));
  const deadline = Date.now() + 30000;
  while (!/listening/i.test(log)) {
    if (Date.now() > deadline || proc.exitCode !== null) throw new Error(`${bin} never listened:\n${log}`);
    await new Promise((r) => setTimeout(r, 100));
  }
  return { proc, log: () => log };
}

async function launch(name) {
  if (name === "webkit") return pw.webkit.launch({ headless: !HEADED });
  return pw.chromium.launch({ headless: !HEADED, channel: CHANNEL });
}

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

async function scenario(browserName, url) {
  const browser = await launch(browserName);
  const tag = browserName;
  const page = await browser.newPage();
  const errors = [];
  let rpcs = 0;
  page.on("pageerror", (e) => errors.push(e.message));
  page.on("console", (m) => {
    // A missing favicon is the browser's own request, and the 500 this script
    // injects is the browser's own network line; neither is the app's report.
    const where = m.location()?.url || "";
    if (m.type() !== "error" || /favicon/.test(where)) return;
    if (/status of 500/.test(m.text())) return;
    errors.push(m.text());
  });
  page.on("request", (r) => {
    if (r.url().includes("/_rpc/")) rpcs++;
  });
  try {
    await page.goto(url, { waitUntil: "load" });

    // ---- local: two handled client-local Errs --------------------------------
    const boot = await waitFor(page, "out", (v) => v.includes("handled secureGet Err"), 10000);
    check(`${tag} local: init's Native.secureGet Err is handled`, boot === "OUT=handled secureGet Err", boot);
    await page.click("#local");
    const out = await waitFor(page, "out", (v) => v.includes("handled local Err"), 10000);
    check(
      `${tag} local: Task.fail Err is handled`,
      out === "OUT=handled secureGet Err; handled local Err",
      out,
    );
    await page.waitForTimeout(500);
    check(`${tag} local: no RPC ran`, rpcs === 0, `rpcs=${rpcs}`);
    check(`${tag} local: no console error`, errors.length === 0, errors.join(" | ") || "none");

    // ---- rpc: a real RPC transport failure with no handler -------------------
    await page.route("**/_rpc/Price**", (route) =>
      route.fulfill({ status: 500, contentType: "text/plain", body: "injected failure" }),
    );
    const before = errors.length;
    await page.click("#pricebtn");
    const deadline = Date.now() + 10000;
    while (errors.length === before && Date.now() < deadline) await page.waitForTimeout(50);
    await page.waitForTimeout(500);
    const added = errors.slice(before);
    check(`${tag} rpc: the RPC was sent`, rpcs === 1, `rpcs=${rpcs}`);
    check(`${tag} rpc: the model is kept`, (await text(page, "price")) === "PRICE=-", await text(page, "price"));
    check(
      `${tag} rpc: the unhandled failure is reported once, loudly`,
      added.length === 1 && added[0].startsWith(RPC_FAILED),
      added.join(" | ") || "none",
    );
    return { out, rpcs, errors };
  } finally {
    await browser.close();
  }
}

let app;
try {
  app = await startApp(SPA, PORT);
  for (const b of BROWSERS) await scenario(b, `http://127.0.0.1:${PORT}/`);
  console.log(
    failures.length
      ? `VERDICT=FAIL ${failures.join("; ")}`
      : "VERDICT=PASS handled client Errs log nothing; an unhandled RPC failure logs once",
  );
  process.exitCode = failures.length ? 2 : 0;
} catch (e) {
  console.error("HARNESS ERROR:", e.message);
  if (app) console.error(app.log().slice(-2000));
  process.exitCode = 1;
} finally {
  if (app) app.proc.kill("SIGKILL");
  process.exit(process.exitCode ?? 1);
}
