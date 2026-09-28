#!/usr/bin/env node
// scripts/islands-e2e-verify.mjs
//
// Browser e2e for widget islands (runtime-go/rt/island_core.go), driven by
// scripts/islands-e2e.sh against the fixture
// rust/crates/sky/tests/fixtures/widget-islands. The app runs with
// SKY_CSP=strict (script-src 'self' 'wasm-unsafe-eval', no inline script), and
// the widget file is a same-origin <script defer>. Asserted, for one build:
//
//   * zero securitypolicyviolation events, zero page errors, zero console
//     errors (a widget warning is allowed);
//   * typing in the contenteditable widget survives >= 100 server re-renders
//     that rebuild the island's parent (the HTML-swap path), with no remount
//     and the caret kept (the typed text is exactly what was typed);
//   * the widget's "changed" events arrive as the typed Msg TextChanged, and
//     the props flow back (the widget's label shows the tick);
//   * a payload the Sky decoder rejects is dropped, and the app stays live;
//   * the mixed-case "Inc" event reaches `onIslandEvent "inc" Decode.int`;
//   * Cmd.toIsland reaches the widget's command handler;
//   * a new island id remounts the widget from props (destroy + mount).
//
// Usage: node scripts/islands-e2e-verify.mjs <app-binary> --port N
//          [--mode live|spa] [--cwd DIR]
import pw from "playwright";
import { spawn } from "node:child_process";
import { guardChild } from "./lib/child-guard.mjs";
import { dirname } from "node:path";

const { chromium } = pw;
const argv = process.argv.slice(2);
const APP = argv[0];
function arg(name, dflt) {
  const i = argv.indexOf(name);
  return i >= 0 && i + 1 < argv.length ? argv[i + 1] : dflt;
}
if (!APP) {
  console.error("usage: islands-e2e-verify.mjs <app-binary> --port N [--mode live|spa] [--cwd DIR]");
  process.exit(2);
}
const PORT = Number(arg("--port", "9560"));
const MODE = arg("--mode", "live");
const CWD = arg("--cwd", dirname(dirname(APP)));
const ORIGIN = `http://127.0.0.1:${PORT}`;
const TAG = `islands/${MODE}`;

try {
  await fetch(ORIGIN + "/", { signal: AbortSignal.timeout(1000) });
  console.error(`${TAG}: harness error: port ${PORT} is already serving; stop that process first`);
  process.exit(1);
} catch (_) {}

const env = { ...process.env, PORT: String(PORT), SKY_LIVE_PORT: String(PORT), ENV: "development", SKY_CSP: "strict" };
const proc = guardChild(spawn(APP, [], { cwd: CWD, env }));
let serverLog = "";
proc.stdout.on("data", (d) => (serverLog += d));
proc.stderr.on("data", (d) => (serverLog += d));
let exited = null;
proc.on("exit", (code, sig) => (exited = { code, sig }));

const failures = [];
function check(step, ok, detail) {
  console.log(`${ok ? "ok  " : "FAIL"} [${TAG}] ${step}${detail ? ": " + detail : ""}`);
  if (!ok) failures.push(step);
}

async function waitListening() {
  for (let i = 0; i < 240; i++) {
    if (exited) throw new Error(`app exited early (${JSON.stringify(exited)})\n${serverLog}`);
    try {
      const r = await fetch(ORIGIN + "/");
      if (r.status < 500) return r;
    } catch (_) {}
    await new Promise((r) => setTimeout(r, 250));
  }
  throw new Error("app never listened\n" + serverLog);
}

async function waitFor(page, fn, arg, what, ms = 10000) {
  try {
    await page.waitForFunction(fn, arg, { timeout: ms });
    return true;
  } catch (_) {
    return false;
  }
}

const bodyText = (page) => page.evaluate(() => document.body.innerText);
const textShows = (page, s, ms) =>
  waitFor(page, (s) => document.body.innerText.includes(s), s, `text ${s}`, ms);

let browser;
try {
  const first = await waitListening();
  const csp = first.headers.get("content-security-policy") || "";
  const scriptSrc = csp.split(";").map((s) => s.trim()).find((s) => s.startsWith("script-src ")) || "";
  check(
    "the page is served with a strict script-src (no inline, eval, hash or nonce)",
    scriptSrc.includes("'self'") && !/'unsafe-inline'|'unsafe-eval'|'sha(256|384|512)-|'nonce-/.test(scriptSrc),
    csp || "(no Content-Security-Policy header)"
  );

  browser = await chromium.launch({ headless: true });
  const context = await browser.newContext({ viewport: { width: 1200, height: 900 } });
  const violations = [];
  const consoleErrors = [];
  await context.exposeBinding("__skyCspReport", (_src, v) => violations.push(v));
  await context.addInitScript(() => {
    document.addEventListener(
      "securitypolicyviolation",
      (e) => {
        try {
          window.__skyCspReport(`${e.violatedDirective} blocked=${e.blockedURI} src=${e.sourceFile}:${e.lineNumber}`);
        } catch (_) {}
      },
      true
    );
  });
  const page = await context.newPage();
  page.on("console", (m) => {
    if (m.type() === "error") consoleErrors.push(m.text());
  });
  page.on("pageerror", (e) => consoleErrors.push("[pageerror] " + e.message));

  await page.goto(ORIGIN + "/", { waitUntil: "load" });
  // Sky.Live is ready once the SSE handshake is in; Sky.Spa once the wasm has
  // hydrated (it clears data-sky-hydrating).
  if (MODE === "spa") {
    check(
      "the wasm client booted",
      await waitFor(page, () => !document.documentElement.hasAttribute("data-sky-hydrating") && !!document.querySelector("#app [sky-id]"), null, "hydrated", 30000)
    );
  }
  check(
    "the editor widget mounted once, from a same-origin defer script",
    await waitFor(page, () => document.querySelector(".ed-box") && window.__editorMounts === 1, null, "mount", 15000),
    await page.evaluate(() => `mounts=${window.__editorMounts} sky=${typeof (window.Sky && window.Sky.island)}`)
  );
  check("the counter widget mounted", await waitFor(page, () => !!document.querySelector(".ctr"), null, "ctr", 10000));

  // ── 1. typing survives >= 100 re-renders of the island's parent ──
  // Let the SSE / wasm loop settle before the tick starts.
  await page.waitForTimeout(MODE === "live" ? 1500 : 500);
  const ticks0 = await page.evaluate(() => {
    const m = /ticks=(\d+)/.exec(document.body.innerText);
    return m ? Number(m[1]) : -1;
  });
  await page.click('button:has-text("start")');
  await textShows(page, "ticks=" + (ticks0 + 3), 10000);
  await page.click(".ed-box");
  const typed = "the quick brown fox jumps over the lazy dog, and keeps typing while the view re-renders. ".repeat(2).trim();
  await page.keyboard.type(typed, { delay: 25 });
  const ticksDuring = await page.evaluate(() => {
    const m = /ticks=(\d+)/.exec(document.body.innerText);
    return m ? Number(m[1]) : -1;
  });
  await page.click('button:has-text("stop")');
  check(`>= 100 server re-renders happened while typing (ticks ${ticks0} -> ${ticksDuring})`, ticksDuring - ticks0 >= 100);
  const boxText = await page.evaluate(() => document.querySelector(".ed-box").textContent);
  check("the typed text is intact in the widget (focus and caret kept)", boxText === typed, JSON.stringify(boxText.slice(0, 80)));
  check(
    "no remount while typing",
    await page.evaluate(() => window.__editorMounts === 1 && window.__editorDestroyed === 0),
    await page.evaluate(() => `mounts=${window.__editorMounts} destroyed=${window.__editorDestroyed}`)
  );
  check("the widget's events arrived as the typed Msg", await textShows(page, "server-text=" + typed, 10000));
  const lastTick = await page.evaluate(() => /ticks=(\d+)/.exec(document.body.innerText)[1]);
  check(
    "props flow to the widget (update)",
    await waitFor(page, (t) => document.querySelector(".ed-label").textContent === "ticks=" + t, lastTick, "label", 5000),
    await page.evaluate(() => document.querySelector(".ed-label").textContent)
  );

  // ── 2. a payload the decoder rejects is dropped ──
  await page.click(".ed-bad");
  await page.waitForTimeout(500);
  check("a rejected payload leaves the model unchanged", (await bodyText(page)).includes("server-text=" + typed));

  // ── 3. a mixed-case event decoded as Int ──
  for (let i = 0; i < 3; i++) await page.click(".ctr");
  check("the counter's Inc events decode as Int", await textShows(page, "ctr=3", 10000), (await bodyText(page)).match(/ctr=\d+/)?.[0]);

  // ── 4. Cmd.toIsland ──
  await page.click('button:has-text("reset")');
  check(
    "Cmd.toIsland reached the widget",
    await waitFor(page, () => document.querySelector(".ed-box").textContent === "reset!" && window.__islandLog.includes("command:setText"), null, "cmd", 10000),
    await page.evaluate(() => JSON.stringify(window.__islandLog))
  );
  check("the command's follow-up event reached update", await textShows(page, "server-text=reset!", 10000));

  // ── 5. a new id remounts from props ──
  await page.click('button:has-text("swap")');
  check(
    "a new island id destroys the old widget and mounts a new one",
    await waitFor(page, () => window.__editorMounts === 2 && window.__editorDestroyed === 1, null, "remount", 10000),
    await page.evaluate(() => `mounts=${window.__editorMounts} destroyed=${window.__editorDestroyed}`)
  );
  check(
    "the remounted widget starts from props",
    await waitFor(page, () => document.querySelector(".ed-box") && document.querySelector(".ed-box").textContent === "reset!", null, "props", 5000),
    await page.evaluate(() =>
      JSON.stringify({
        boxes: Array.from(document.querySelectorAll(".ed-box")).map((b) => b.textContent.slice(0, 20)),
        islands: Array.from(document.querySelectorAll("[data-sky-island=editor]")).map((i) => [
          i.getAttribute("data-sky-island-id"),
          (i.getAttribute("data-sky-props") || "").slice(0, 60),
        ]),
      })
    )
  );
  // A command to the new id still works.
  await page.click('button:has-text("reset")');
  check(
    "Cmd.toIsland reaches the remounted widget",
    await waitFor(page, () => window.__islandLog.filter((x) => x === "command:setText").length === 2, null, "cmd2", 10000)
  );

  // ── 6. a flood of commands: never lost silently ──
  // One update sends the sink 400 commands and a final state, past every SSE
  // buffer. The contract: each command arrives once and in order, or the
  // island is resynced (remounted; the app's "resync" handler re-sends the
  // state). Either way the sink ends showing state=400, and the page shows the
  // model's flood=400 (the view patch the flood crowded out is recovered by
  // the connection's resync).
  await page.click('button:has-text("flood")');
  const settled = await waitFor(page, () => /state=400/.test((document.querySelector(".sink-state") || {}).textContent || ""), null, "flood", 20000);
  const flood = await page.evaluate(() => {
    const runs = window.__sinkRuns;
    const last = runs[runs.length - 1];
    const inOrder = last.every((v, i) => v === i);
    const m = /resyncs=(\d+)/.exec(document.body.innerText);
    return { mounts: runs.length, items: last.length, inOrder, resyncs: m ? Number(m[1]) : -1, view: /flood=400/.test(document.body.innerText) };
  });
  check("after a flood of 400 commands the sink shows the final state", settled, JSON.stringify(flood));
  check(
    "no command was lost silently: all 400 in order, or the island was resynced and the app told",
    (flood.resyncs === 0 && flood.mounts === 1 && flood.items === 400 && flood.inOrder) || (flood.resyncs >= 1 && flood.mounts >= 2),
    JSON.stringify(flood)
  );
  check("the view patch the flood crowded out is on the page (flood=400)", flood.view, JSON.stringify(flood));
  console.log(`info [${TAG}] flood: ${JSON.stringify(flood)}`);

  check("zero securitypolicyviolation events", violations.length === 0, violations.slice(0, 3).join(" | "));
  check("zero console errors", consoleErrors.length === 0, consoleErrors.slice(0, 3).join(" | "));
  if (MODE === "live") {
    check("the server logged the rejected widget payload", /IslandEventDecode|widget event was dropped/.test(serverLog));
  }
} catch (e) {
  failures.push(String(e && e.stack ? e.stack : e));
  console.log(`FAIL [${TAG}] ${e && e.stack ? e.stack : e}`);
} finally {
  if (browser) await browser.close().catch(() => {});
  proc.kill("SIGTERM");
}

if (failures.length) {
  console.log(`${TAG}: FAIL (${failures.length})`);
  if (process.env.ISLANDS_E2E_VERBOSE) console.log(serverLog);
  process.exit(1);
}
console.log(`${TAG}: PASS`);
