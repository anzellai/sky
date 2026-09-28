#!/usr/bin/env node
// docs/perf/runs/terminal-20260928/bench-app.mjs
//
// The end-to-end half of the terminal run: the ui-terminal fixture
// (rust/crates/sky/tests/fixtures/ui-terminal, a Sky.Live app with a
// Std.Ui.Terminal bound to `sh` on a PTY), built by the compiler under test,
// driven in headless Chromium. For each workload the test types
//
//     cat <workdir>/<name>.bin; echo; echo DONE$((40+2))
//
// and waits for the line DONE42 (the typed command shows DONE$((40+2)), so
// only the shell's output matches). Measured, median of --reps runs:
//
//   ms            Enter to DONE42 on the screen
//   sse_bytes     bytes of SSE "island" message data received meanwhile
//                 (CDP Network.eventSourceMessageReceived)
//   sse_msgs      island messages received meanwhile
//   repaints      "reset" / full-frame repaints the widget got meanwhile
//   task_ms       Chromium main-thread TaskDuration over the run
//   dropped       animation frames missed (a rAF gap of k*16.7 ms counts k-1)
//   ok            the last non-empty line before DONE42 is the workload's
//                 last line (the screen is right, not only fast)
//
// --throttle-kbps N  limits the page's download rate (CDP
// Network.emulateNetworkConditions) to model a slow client.
//
// Usage: node bench-app.mjs <app-binary> --cwd DIR --workdir DIR --port N
//          [--reps 3] [--throttle-kbps N] [--only name] [--timeout-s 120]
import pw from "playwright";
import { spawn } from "node:child_process";

const argv = process.argv.slice(2);
const APP = argv[0];
const arg = (n, d) => {
  const i = argv.indexOf(n);
  return i >= 0 && i + 1 < argv.length ? argv[i + 1] : d;
};
const CWD = arg("--cwd", ".");
const DIR = arg("--workdir", null);
const PORT = Number(arg("--port", "9590"));
const REPS = Number(arg("--reps", "3"));
const KBPS = Number(arg("--throttle-kbps", "0"));
const ONLY = arg("--only", null);
const TIMEOUT = Number(arg("--timeout-s", "120")) * 1000;
const ORIGIN = "http://127.0.0.1:" + PORT;
const LAST = { yes: "y", seq: "200000", redraw: "file_36.txt", colour: null };

const median = (a) => {
  const s = [...a].sort((x, y) => x - y);
  return s.length ? s[Math.floor(s.length / 2)] : 0;
};

async function runOnce(name) {
  const proc = spawn(APP, [], { cwd: CWD, env: { ...process.env, PORT: String(PORT), SKY_LIVE_PORT: String(PORT), ENV: "development", SKY_CSP: "strict" } });
  let log = "";
  proc.stdout.on("data", (d) => (log += d));
  proc.stderr.on("data", (d) => (log += d));
  const browser = await pw.chromium.launch({ headless: true });
  try {
    for (let i = 0; ; i++) {
      try {
        const r = await fetch(ORIGIN + "/");
        if (r.status < 500) break;
      } catch (_) {}
      if (i > 200) throw new Error("app did not listen\n" + log);
      await new Promise((r) => setTimeout(r, 100));
    }
    const context = await browser.newContext({ viewport: { width: 1000, height: 900 } });
    const page = await context.newPage();
    const cdp = await context.newCDPSession(page);
    await cdp.send("Network.enable");
    let bytes = 0, msgs = 0, repaints = 0, counting = false;
    cdp.on("Network.eventSourceMessageReceived", (e) => {
      if (!counting || e.eventName !== "island") return;
      bytes += e.data.length;
      msgs++;
      if (/"name":"reset"/.test(e.data) || /"full":true/.test(e.data)) repaints++;
    });
    await cdp.send("Performance.enable");
    await page.goto(ORIGIN + "/", { waitUntil: "load" });
    const T = '[data-sky-island="sky-terminal"]';
    await page.waitForFunction((s) => /\$\s*$/m.test((document.querySelector(s) || {}).innerText || ""), T, { timeout: 20000 });
    if (KBPS > 0) {
      await cdp.send("Network.emulateNetworkConditions", { offline: false, latency: 20, downloadThroughput: (KBPS * 1024) / 8, uploadThroughput: (KBPS * 1024) / 8 });
    }
    await page.click(T);
    await page.keyboard.type("cat " + DIR + "/" + name + ".bin; echo; echo DONE$((40+2))", { delay: 5 });
    await page.evaluate(() => {
      window.__stamps = [];
      window.__rafOn = true;
      const f = (ts) => {
        window.__stamps.push(ts);
        if (window.__rafOn) requestAnimationFrame(f);
      };
      requestAnimationFrame(f);
    });
    const m0 = Object.fromEntries((await cdp.send("Performance.getMetrics")).metrics.map((x) => [x.name, x.value]));
    counting = true;
    const t0 = Date.now();
    await page.keyboard.press("Enter");
    let done = true;
    try {
      await page.waitForFunction((s) => /^DONE42\s*$/m.test((document.querySelector(s) || {}).innerText || ""), T, { timeout: TIMEOUT, polling: 50 });
    } catch (_) {
      done = false;
    }
    const ms = Date.now() - t0;
    counting = false;
    const m1 = Object.fromEntries((await cdp.send("Performance.getMetrics")).metrics.map((x) => [x.name, x.value]));
    const stamps = await page.evaluate(() => {
      window.__rafOn = false;
      return window.__stamps;
    });
    let dropped = 0;
    for (let k = 1; k < stamps.length; k++) {
      const gap = stamps[k] - stamps[k - 1];
      if (gap > 25) dropped += Math.round(gap / 16.7) - 1;
    }
    const text = await page.evaluate((s) => document.querySelector(s).innerText, T);
    const lines = text.split("\n").map((l) => l.trimEnd()).filter((l) => l !== "");
    const at = lines.findIndex((l) => l === "DONE42");
    const before = at > 0 ? lines[at - 1] : "";
    if (!done && process.env.BENCH_VERBOSE) console.error(log.split("\n").slice(-15).join("\n"));
    const ok = done && (LAST[name] === null ? true : before.endsWith(LAST[name]));
    return { ms: done ? ms : -1, sse_bytes: bytes, sse_msgs: msgs, repaints, task_ms: Math.round((m1.TaskDuration - m0.TaskDuration) * 1000), dropped, ok, before };
  } finally {
    await browser.close().catch(() => {});
    proc.kill("SIGTERM");
    await new Promise((r) => setTimeout(r, 300));
  }
}

const out = {};
for (const name of ONLY ? [ONLY] : ["yes", "seq", "redraw", "colour"]) {
  const runs = [];
  for (let r = 0; r < REPS; r++) runs.push(await runOnce(name));
  out[name] = {
    ms: median(runs.map((x) => x.ms)),
    sse_bytes: median(runs.map((x) => x.sse_bytes)),
    sse_msgs: median(runs.map((x) => x.sse_msgs)),
    repaints: median(runs.map((x) => x.repaints)),
    task_ms: median(runs.map((x) => x.task_ms)),
    dropped: median(runs.map((x) => x.dropped)),
    ok: runs.filter((x) => x.ok).length + "/" + runs.length,
    last_line: runs.map((x) => x.before),
  };
  console.log(name, JSON.stringify(out[name]));
}
console.log(JSON.stringify(out));
