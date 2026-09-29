#!/usr/bin/env node
// scripts/spa-rpc-order-verify.mjs
//
// Browser e2e for the Sky.Spa client's Msg order (runtime-go/rt/spa_rpcqueue.go).
// Sky.Live runs `update` once per Msg, in arrival order, and runs its Cmds; the
// web:app client must give the same answer. Drives the fixture
// rust/crates/sky/tests/fixtures/spa-rpc-order on BOTH targets (the web:app
// split and Sky.Live), in each browser, under SKY_CSP=strict:
//
//   seal   a client arm that spends a single-use Noise state while a server
//          RPC is in flight runs once: STATUS=rpc done SEALS=1 (the old client
//          re-ran it on a spent state: "this state value was already used")
//   timer  ticks during a call see busy = True; the calls keep coming (the old
//          client stalled at STARTED=2 DONE=1)
//   pair   two independent server Msgs are in flight together: both answer in
//          about one server delay (1.5 s), not two
//
// Usage: node scripts/spa-rpc-order-verify.mjs <web:app-backend> <live-app> [--port N]
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
  console.error("usage: spa-rpc-order-verify.mjs <web:app-backend> <live-app> [--port N]");
  process.exit(1);
}
const PORT = Number(arg("--port", "9361"));
const BROWSERS = (process.env.SKY_E2E_BROWSERS || "chromium").split(",").map((s) => s.trim()).filter(Boolean);
const HEADED = process.env.SKY_E2E_HEADED === "1";
const CHANNEL = process.env.SKY_E2E_CHANNEL || undefined;

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
const counts = (s) => {
  const m = s.match(/BUSY=(\w+) STARTED=(\d+) DONE=(\d+) TICKS=(\d+)/);
  return m ? { busy: m[1], started: +m[2], done: +m[3], ticks: +m[4] } : null;
};

async function scenario(browserName, target, url) {
  const browser = await launch(browserName);
  const tag = `${target}/${browserName}`;
  const page = await browser.newPage();
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  page.on("console", (m) => {
    // A missing favicon is the browser's own request, not the app's.
    if (m.type() === "error" && !/favicon/.test(m.location()?.url || "")) errors.push(m.text());
  });
  try {
    await page.goto(url, { waitUntil: "load" });
    await page.waitForTimeout(1500);

    // ---- seal --------------------------------------------------------------
    await page.click("#start");
    const ready = await waitFor(page, "seal", (v) => v.includes("ready"), 10000);
    check(`${tag} seal: transport built`, ready.includes("STATUS=ready SEALS=0"), ready);
    await page.click("#go");
    await page.waitForTimeout(300);
    await page.click("#sealbtn");
    await page.waitForTimeout(200);
    const during = await text(page, "seal");
    check(`${tag} seal: runs during the RPC`, during === "STATUS=rpc in flight SEALS=1", during);
    const after = await waitFor(page, "seal", (v) => !v.includes("in flight"), 15000);
    check(`${tag} seal: after the RPC`, after === "STATUS=rpc done SEALS=1", after);

    // ---- pair --------------------------------------------------------------
    const t0 = Date.now();
    await page.click("#pairbtn");
    const pair = await waitFor(page, "pair", (v) => v === "A=a B=b", 15000);
    const ms = Date.now() - t0;
    check(`${tag} pair: both answered`, pair === "A=a B=b", pair);
    check(`${tag} pair: in flight together (${ms} ms for two 1500 ms calls)`, ms < 2700, `${ms} ms`);

    // ---- timer -------------------------------------------------------------
    await page.click("#timeron");
    await page.waitForTimeout(7000);
    const c1 = counts(await text(page, "timer"));
    await page.waitForTimeout(4000);
    const c2 = counts(await text(page, "timer"));
    check(`${tag} timer: calls completed`, c1 && c1.done >= 3, JSON.stringify(c1));
    check(`${tag} timer: one call at a time`, c2 && (c2.started - c2.done === 0 || c2.started - c2.done === 1), JSON.stringify(c2));
    check(`${tag} timer: calls keep coming`, c1 && c2 && c2.done > c1.done, `${c1?.done} -> ${c2?.done}`);
    check(`${tag} no page errors`, errors.length === 0, errors.join(" | ") || "none");
    return { seal: after, pairMs: ms, timer: c2 };
  } finally {
    await browser.close();
  }
}

let apps = [];
try {
  const results = {};
  for (const [target, bin, port] of [
    ["web:app", SPA, PORT],
    ["live", LIVE, PORT + 1],
  ]) {
    const app = await startApp(bin, port);
    apps.push(app);
    for (const b of BROWSERS) {
      results[`${target}/${b}`] = await scenario(b, target, `http://127.0.0.1:${port}/`);
    }
    app.proc.kill("SIGKILL");
  }
  // web:app gives Live's answer.
  for (const b of BROWSERS) {
    const s = results[`web:app/${b}`];
    const l = results[`live/${b}`];
    check(`${b}: seal matches Sky.Live`, s.seal === l.seal, `web:app "${s.seal}" / live "${l.seal}"`);
    check(
      `${b}: timer within Sky.Live's range`,
      Math.abs(s.timer.done - l.timer.done) <= 3,
      `web:app DONE=${s.timer.done} / live DONE=${l.timer.done}`,
    );
  }
  console.log(failures.length ? `VERDICT=FAIL ${failures.join("; ")}` : "VERDICT=PASS web:app matches Sky.Live on every scenario");
  process.exitCode = failures.length ? 2 : 0;
} catch (e) {
  console.error("HARNESS ERROR:", e.message);
  for (const a of apps) console.error(a.log().slice(-2000));
  process.exitCode = 1;
} finally {
  for (const a of apps) a.proc.kill("SIGKILL");
  process.exit(process.exitCode ?? 1);
}
