#!/usr/bin/env node
// scripts/spa-resilience-verify.mjs
//
// Browser e2e for Sky.Spa transport resilience (runtime-go/rt/spa_retry.go,
// spa_tick.go, spa_neterror_wasm.go). Drives
// rust/crates/sky/tests/fixtures/spa-resilience (web:app) under
// SKY_CSP=strict. The app writes no network code.
//
// Stage "fast" (every browser in SKY_E2E_BROWSERS):
//   blip      RPCs refused for 2 s (a server restart): nothing is shown (no
//             "Reconnecting…", no red bar) and the click runs once.
//   offline   the browser goes offline: two clicks queue; after 3 s (and
//             within 10 s) a quiet "Reconnecting…" shows (never the red bar);
//             Sub.connection reports it; back online, both clicks run once
//             each, in order, the indicator clears, App.withRpcError is never
//             called.
//   pushback  a 503 with Retry-After: 2 is obeyed: the re-send comes 2 s later.
//   hidden    a client-only Sub.every keeps ticking while the page is hidden;
//             a polling Sub.every sends at most its first call while hidden;
//             on return one fresh poll goes out at once.
//   resume    (v0.27.6) the page clock jumps minutes ahead with an RPC on the
//             wire, the way a frozen page, a phone app switch or a tab
//             restored from the back/forward cache sees time: the request is
//             re-sent at once on resume (same request id) and runs once,
//             App.withRpcError is never called, no red bar. Also: an outage
//             that lasts minutes while the page is hidden gives nothing up.
// Stage "slow" (Chromium only; they wait out the real 30 s and 60 s budgets):
//   timeout   a request that hangs is aborted after 30 s as a Timeout and
//             re-sent with the same request id.
//   exhausted RPCs fail for over 60 s: the red bar shows only once the budget
//             is spent, App.withRpcError is called once, Sub.connection says
//             offline; back online, Retry runs the click once.
//
// Usage: node scripts/spa-resilience-verify.mjs <web:app-backend> --stage fast|slow [--port N]
//   SKY_E2E_BROWSERS  comma list of chromium,webkit (default chromium)
//   SKY_E2E_CHANNEL   a Playwright Chromium channel, e.g. "chrome"
//   SKY_E2E_HEADED=1  run the browsers headed
// Exit: 0 PASS · 2 FAIL · 1 harness error.
import pw from "playwright";
import { spawn } from "node:child_process";
import { dirname, join } from "node:path";
import { readFileSync, rmSync } from "node:fs";
import { guardChild } from "./lib/child-guard.mjs";

function arg(name, def) {
  const i = process.argv.indexOf(name);
  return i >= 0 && process.argv[i + 1] ? process.argv[i + 1] : def;
}
const SPA = process.argv[2];
const STAGE = arg("--stage", "fast");
if (!SPA || !["fast", "slow"].includes(STAGE)) {
  console.error("usage: spa-resilience-verify.mjs <web:app-backend> --stage fast|slow [--port N]");
  process.exit(1);
}
const PORT = Number(arg("--port", "9369"));
const BROWSERS =
  STAGE === "slow"
    ? ["chromium"]
    : (process.env.SKY_E2E_BROWSERS || "chromium").split(",").map((s) => s.trim()).filter(Boolean);
const HEADED = process.env.SKY_E2E_HEADED === "1";
const CHANNEL = process.env.SKY_E2E_CHANNEL || undefined;
const APP_DIR = dirname(dirname(SPA));

const failures = [];
function check(step, ok, detail) {
  console.log(`${ok ? "ok  " : "FAIL"} ${step}: ${detail}`);
  if (!ok) failures.push(step);
}

async function startApp(bin, port) {
  const env = { ...process.env, PORT: String(port), SKY_LIVE_PORT: String(port), SKY_CSP: "strict" };
  const proc = guardChild(spawn(bin, [], { cwd: APP_DIR, env }));
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
const num = async (page, id) => Number((await text(page, id)).split("=")[1]);
async function waitFor(page, id, pred, ms) {
  const deadline = Date.now() + ms;
  let v = await text(page, id);
  while (!pred(v) && Date.now() < deadline) {
    await page.waitForTimeout(50);
    v = await text(page, id);
  }
  return v;
}
const shown = (page, sel) =>
  page.evaluate((s) => {
    const el = document.querySelector(s);
    return !!el && getComputedStyle(el).display !== "none";
  }, sel);
const fileRuns = (name) => {
  try {
    return readFileSync(join(APP_DIR, name), "utf8").length;
  } catch {
    return 0;
  }
};

// Watch the two overlays every 50 ms; returns a stop() that reports what was seen.
function watchOverlays(page) {
  let pill = false;
  let bar = false;
  let pillAt = 0;
  const t0 = Date.now();
  let live = true;
  (async () => {
    while (live) {
      try {
        if (!pill && (await shown(page, "#sky-spa-reconnecting"))) {
          pill = true;
          pillAt = Date.now() - t0;
        }
        if (await shown(page, "#sky-spa-neterror")) bar = true;
      } catch {}
      await new Promise((r) => setTimeout(r, 50));
    }
  })();
  return () => {
    live = false;
    return { pill, bar, pillAt };
  };
}

// waitForPill polls the "Reconnecting…" indicator until it shows or ms pass.
async function waitForPill(page, ms) {
  const deadline = Date.now() + Math.max(0, ms);
  while (Date.now() < deadline) {
    if (await shown(page, "#sky-spa-reconnecting")) return true;
    await page.waitForTimeout(50);
  }
  return shown(page, "#sky-spa-reconnecting");
}

async function setHidden(page, hidden) {
  await page.evaluate((h) => {
    Object.defineProperty(document, "hidden", { configurable: true, get: () => h });
    Object.defineProperty(document, "visibilityState", { configurable: true, get: () => (h ? "hidden" : "visible") });
    document.dispatchEvent(new Event("visibilitychange"));
  }, hidden);
}

async function openPage(browser, rpcs, { clock = false } = {}) {
  const context = await browser.newContext();
  const page = await context.newPage();
  // The resume cases jump the page's clock (Date, performance.now, timers)
  // ahead with page.clock.fastForward: due timers fire once, at the jump, as
  // they do when a frozen page resumes. Installed before the wasm boots.
  if (clock) await page.clock.install();
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  page.on("request", (r) => {
    if (r.url().includes("/_rpc/")) rpcs.push({ url: r.url(), at: Date.now() });
  });
  await page.goto(`http://127.0.0.1:${PORT}/`, { waitUntil: "load" });
  await waitFor(page, "ticks", (v) => Number(v.split("=")[1]) > 0, 15000); // wasm booted
  return { context, page, errors };
}

const ridOf = (u) => new URL(u).searchParams.get("rid");

async function fast(browserName) {
  const tag = browserName;
  const browser = await launch(browserName);
  try {
    // ---- blip: a server restart shorter than the grace period ----------------
    {
      const rpcs = [];
      const { page, errors } = await openPage(browser, rpcs);
      let refuse = true;
      await page.route("**/_rpc/**", (route) => (refuse ? route.abort("connectionrefused") : route.continue()));
      const before = fileRuns("hits.txt");
      const stop = watchOverlays(page);
      await page.click("#hit");
      await page.waitForTimeout(2000);
      refuse = false;
      const hits = await waitFor(page, "hits", (v) => v === `hits=${before + 1}`, 8000);
      await page.waitForTimeout(300);
      const seen = stop();
      check(`${tag} blip: the click ran once`, hits === `hits=${before + 1}` && fileRuns("hits.txt") === before + 1, `${hits} file=${fileRuns("hits.txt")}`);
      check(`${tag} blip: nothing was shown`, !seen.pill && !seen.bar, JSON.stringify(seen));
      check(`${tag} blip: withRpcError not called`, (await text(page, "rpcerrs")) === "rpcerrs=0", await text(page, "rpcerrs"));
      const rids = new Set(rpcs.filter((r) => r.url.includes("/_rpc/Hit")).map((r) => ridOf(r.url)));
      check(`${tag} blip: every re-send reuses the request id`, rids.size === 1, [...rids].join(","));
      check(`${tag} blip: no page error`, errors.length === 0, errors.join(" | ") || "none");
      await page.context().close();
    }

    // ---- offline 10 s: two queued clicks replay once each, in order ----------
    {
      const rpcs = [];
      const { context, page, errors } = await openPage(browser, rpcs);
      const before = fileRuns("hits.txt");
      const stop = watchOverlays(page);
      await context.setOffline(true);
      const t0 = Date.now();
      await page.click("#hit");
      await page.click("#hit");
      const conn = await waitFor(page, "conn", (v) => v === "conn=reconnecting", 5000);
      // Wait for the indicator itself, bounded, rather than a fixed 10 s: the
      // runtime re-sends the head at the 3 s grace (spa_retry.go,
      // armGraceLocked), so it shows just after 3 s. Stay offline at least 5 s
      // so the red bar has room to (wrongly) appear.
      await waitForPill(page, 10000 - (Date.now() - t0));
      await page.waitForTimeout(Math.max(0, 5000 - (Date.now() - t0)));
      const mid = { ...stop() };
      await context.setOffline(false);
      const stop2 = watchOverlays(page);
      const hits = await waitFor(page, "hits", (v) => v === `hits=${before + 2}`, 20000);
      const online = await waitFor(page, "conn", (v) => v === "conn=online", 5000);
      await page.waitForTimeout(300);
      const after = stop2();
      check(`${tag} offline: Sub.connection reports Reconnecting`, conn === "conn=reconnecting", conn);
      check(`${tag} offline: "Reconnecting…" shows after the 3 s grace`, mid.pill && mid.pillAt >= 2900, JSON.stringify(mid));
      check(`${tag} offline: the red bar never shows`, !mid.bar && !after.bar, JSON.stringify({ mid, after }));
      check(`${tag} offline: both queued clicks ran once each`, hits === `hits=${before + 2}` && fileRuns("hits.txt") === before + 2, `${hits} file=${fileRuns("hits.txt")}`);
      check(`${tag} offline: the indicator clears`, !(await shown(page, "#sky-spa-reconnecting")), "hidden");
      check(`${tag} offline: Sub.connection reports Online again`, online === "conn=online", online);
      check(`${tag} offline: withRpcError not called`, (await text(page, "rpcerrs")) === "rpcerrs=0", await text(page, "rpcerrs"));
      check(`${tag} offline: no page error`, errors.length === 0, errors.join(" | ") || "none");
      await context.close();
    }

    // ---- pushback: 503 + Retry-After: 2 ---------------------------------------
    {
      const rpcs = [];
      const { page, errors } = await openPage(browser, rpcs);
      let first = true;
      await page.route("**/_rpc/Hit**", (route) => {
        if (first) {
          first = false;
          return route.fulfill({ status: 503, headers: { "Retry-After": "2" }, contentType: "text/plain", body: "busy" });
        }
        return route.continue();
      });
      const before = await num(page, "hits");
      await page.click("#hit");
      await waitFor(page, "hits", (v) => v !== `hits=${before}`, 10000);
      const sends = rpcs.filter((r) => r.url.includes("/_rpc/Hit"));
      const gap = sends.length >= 2 ? sends[1].at - sends[0].at : -1;
      check(`${tag} pushback: the 503 was re-sent after Retry-After`, sends.length === 2 && gap >= 1900 && gap <= 3000, `sends=${sends.length} gap=${gap}ms`);
      check(`${tag} pushback: withRpcError not called`, (await text(page, "rpcerrs")) === "rpcerrs=0", await text(page, "rpcerrs"));
      check(`${tag} pushback: no page error`, errors.length === 0, errors.join(" | ") || "none");
      await page.context().close();
    }

    // ---- hidden: client ticks run, polls stop after the first, one on return --
    {
      const rpcs = [];
      const { page, errors } = await openPage(browser, rpcs);
      await page.click("#poll");
      await waitFor(page, "polls", (v) => Number(v.split("=")[1]) >= 2, 8000);
      await setHidden(page, true);
      const hidAt = Date.now();
      const ticks0 = await num(page, "ticks");
      await page.waitForTimeout(5000);
      const ticks1 = await num(page, "ticks");
      const polledHidden = rpcs.filter((r) => r.url.includes("/_rpc/Poll") && r.at > hidAt).length;
      check(`${tag} hidden: a client-only Sub.every keeps ticking`, ticks1 - ticks0 >= 10, `ticks +${ticks1 - ticks0} in 5 s`);
      check(`${tag} hidden: at most the first poll goes out while hidden`, polledHidden <= 1, `polls sent while hidden=${polledHidden}`);
      const visAt = Date.now();
      await setHidden(page, false);
      await page.waitForTimeout(500);
      const onReturn = rpcs.filter((r) => r.url.includes("/_rpc/Poll") && r.at >= visAt).length;
      check(`${tag} hidden: one fresh poll on return, at once`, onReturn === 1, `polls in 500 ms after visible=${onReturn}`);
      await page.waitForTimeout(2200);
      const resumed = rpcs.filter((r) => r.url.includes("/_rpc/Poll") && r.at >= visAt).length;
      check(`${tag} hidden: the poll schedule resumes`, resumed >= 2, `polls 2.7 s after visible=${resumed}`);
      await page.click("#poll");
      check(`${tag} hidden: no page error`, errors.length === 0, errors.join(" | ") || "none");
      await page.context().close();
    }

    await resume(tag, browser);
  } finally {
    await browser.close();
  }
}

// v0.27.6: the retry budget (60 s) and the fetch timeout (30 s) count time the
// page could run, not wall time. Before, a request on the wire when the page
// froze was judged at resume against the wall clock: its abort fired at once,
// its budget read as spent, and App.withRpcError got the failure.
async function resume(tag, browser) {
  const GAP = "02:00";
  const cases = [
    {
      name: "frozen 2 min, the abort timer is overdue on resume",
      run: async (page) => {
        await page.clock.fastForward(GAP);
      },
    },
    {
      name: "hidden 2 min, the connection is lost on resume",
      run: async (page, held) => {
        await setHidden(page, true);
        await page.clock.fastForward(GAP);
        await setHidden(page, false);
        await held().abort("failed");
      },
    },
    {
      name: "back/forward cache 2 min (pagehide, pageshow persisted)",
      run: async (page, held) => {
        await page.evaluate(() => window.dispatchEvent(new PageTransitionEvent("pagehide", { persisted: true })));
        await page.clock.fastForward(GAP);
        await page.evaluate(() => window.dispatchEvent(new PageTransitionEvent("pageshow", { persisted: true })));
        await held().abort("connectionreset");
      },
    },
    {
      name: "frozen 2 min with no event, the connection is lost on resume",
      run: async (page, held) => {
        await page.clock.fastForward(GAP);
        // the overdue abort already settled this attempt; a second jump with
        // the re-send refused models a radio that is still waking up
      },
      refuseAfter: 1,
    },
  ];
  for (const c of cases) {
    const rpcs = [];
    const { page, errors } = await openPage(browser, rpcs, { clock: true });
    let held = null;
    let sent = 0;
    await page.route("**/_rpc/Hit**", (route) => {
      sent++;
      if (sent === 1) {
        held = route; // the first attempt is on the wire when the page stops
        return;
      }
      if (c.refuseAfter && sent <= 1 + c.refuseAfter) return route.abort("internetdisconnected");
      return route.continue();
    });
    const before = fileRuns("hits.txt");
    const stop = watchOverlays(page);
    await page.click("#hit");
    const t0 = Date.now();
    while (!held && Date.now() - t0 < 5000) await page.waitForTimeout(20);
    const resumedAt = Date.now();
    await c.run(page, () => held);
    const hits = await waitFor(page, "hits", (v) => v === `hits=${before + 1}`, 15000);
    await page.waitForTimeout(500);
    const seen = stop();
    const sends = rpcs.filter((r) => r.url.includes("/_rpc/Hit"));
    const resent = sends.find((r) => r.at >= resumedAt);
    check(`${tag} resume (${c.name}): withRpcError not called`, (await text(page, "rpcerrs")) === "rpcerrs=0", await text(page, "rpcerrs"));
    check(`${tag} resume (${c.name}): no red bar`, !seen.bar, JSON.stringify(seen));
    check(`${tag} resume (${c.name}): the click ran once`, hits === `hits=${before + 1}` && fileRuns("hits.txt") === before + 1, `${hits} file=${fileRuns("hits.txt")}`);
    check(`${tag} resume (${c.name}): re-sent at once with the same request id`,
      !!resent && resent.at - resumedAt < 3000 && new Set(sends.map((s) => ridOf(s.url))).size === 1,
      `sends=${sends.length} first re-send ${resent ? resent.at - resumedAt + "ms" : "none"} after resume`);
    check(`${tag} resume (${c.name}): Sub.connection back to online`, (await waitFor(page, "conn", (v) => v === "conn=online", 5000)) === "conn=online", await text(page, "conn"));
    check(`${tag} resume (${c.name}): no page error`, errors.length === 0, errors.join(" | ") || "none");
    await page.context().close();
  }

  // A long outage while the page is hidden: the attempts fail on the wire
  // (timers run, throttled, as on a desktop) for longer than the budget. No
  // request is given up while the user cannot see the page; the return to it
  // re-sends the click, which runs once.
  {
    const rpcs = [];
    const { page, errors } = await openPage(browser, rpcs, { clock: true });
    let refuse = true;
    await page.route("**/_rpc/**", (route) => (refuse ? route.abort("connectionrefused") : route.continue()));
    const before = fileRuns("hits.txt");
    await page.click("#hit");
    await page.waitForTimeout(300);
    await setHidden(page, true);
    for (let i = 0; i < 18; i++) {
      await page.clock.fastForward("00:10"); // 3 min hidden, in steps: every due timer fires
      await page.waitForTimeout(30);
    }
    const hiddenErrs = await text(page, "rpcerrs");
    const hiddenBar = await shown(page, "#sky-spa-neterror");
    refuse = false;
    const visAt = Date.now();
    await setHidden(page, false);
    const hits = await waitFor(page, "hits", (v) => v === `hits=${before + 1}`, 10000);
    check(`${tag} resume (outage while hidden 3 min): nothing given up while hidden`, hiddenErrs === "rpcerrs=0" && !hiddenBar, `${hiddenErrs} bar=${hiddenBar}`);
    check(`${tag} resume (outage while hidden 3 min): the click ran once on return`, hits === `hits=${before + 1}` && fileRuns("hits.txt") === before + 1, `${hits} file=${fileRuns("hits.txt")} in ${Date.now() - visAt}ms`);
    check(`${tag} resume (outage while hidden 3 min): withRpcError not called`, (await text(page, "rpcerrs")) === "rpcerrs=0", await text(page, "rpcerrs"));
    check(`${tag} resume (outage while hidden 3 min): no page error`, errors.length === 0, errors.join(" | ") || "none");
    await page.context().close();
  }
}

async function slow(browserName) {
  const tag = browserName;
  const browser = await launch(browserName);
  try {
    // ---- timeout: a hang is aborted after 30 s and re-sent ---------------------
    {
      const rpcs = [];
      const { page, errors } = await openPage(browser, rpcs);
      let first = true;
      await page.route("**/_rpc/Hit**", (route) => {
        if (first) {
          first = false;
          return; // never answered: the request hangs
        }
        return route.continue();
      });
      const before = fileRuns("hits.txt");
      await page.click("#hit");
      const hits = await waitFor(page, "hits", (v) => v === `hits=${before + 1}`, 45000);
      const sends = rpcs.filter((r) => r.url.includes("/_rpc/Hit"));
      const gap = sends.length >= 2 ? sends[1].at - sends[0].at : -1;
      check(`${tag} timeout: the hang was aborted at 30 s and re-sent`, sends.length === 2 && gap >= 29500 && gap <= 32000, `sends=${sends.length} gap=${gap}ms`);
      check(`${tag} timeout: same request id`, sends.length === 2 && ridOf(sends[0].url) === ridOf(sends[1].url), sends.map((s) => ridOf(s.url)).join(","));
      check(`${tag} timeout: the click ran once`, hits === `hits=${before + 1}`, hits);
      check(`${tag} timeout: withRpcError not called`, (await text(page, "rpcerrs")) === "rpcerrs=0", await text(page, "rpcerrs"));
      check(`${tag} timeout: no page error`, errors.length === 0, errors.join(" | ") || "none");
      await page.context().close();
    }

    // ---- exhausted: the budget is spent before the bar shows -------------------
    {
      const rpcs = [];
      const { page, errors } = await openPage(browser, rpcs);
      let refuse = true;
      await page.route("**/_rpc/**", (route) => (refuse ? route.abort("connectionrefused") : route.continue()));
      const before = fileRuns("hits.txt");
      const t0 = Date.now();
      await page.click("#hit");
      let barAt = -1;
      while (Date.now() - t0 < 75000) {
        if (await shown(page, "#sky-spa-neterror")) {
          barAt = Date.now() - t0;
          break;
        }
        await page.waitForTimeout(200);
      }
      await page.waitForTimeout(500);
      check(`${tag} exhausted: the red bar shows only once the budget is spent`, barAt >= 55000 && barAt <= 65000, `bar at ${barAt}ms`);
      check(`${tag} exhausted: withRpcError called once`, (await text(page, "rpcerrs")) === "rpcerrs=1", await text(page, "rpcerrs"));
      check(`${tag} exhausted: Sub.connection reports Offline`, (await text(page, "conn")) === "conn=offline:1", await text(page, "conn"));
      check(`${tag} exhausted: the indicator gave way to the bar`, !(await shown(page, "#sky-spa-reconnecting")), "pill hidden");
      refuse = false;
      await page.click("#sky-spa-neterror button");
      const hits = await waitFor(page, "hits", (v) => v === `hits=${before + 1}`, 10000);
      check(`${tag} exhausted: Retry runs the click once`, hits === `hits=${before + 1}` && fileRuns("hits.txt") === before + 1, `${hits} file=${fileRuns("hits.txt")}`);
      check(`${tag} exhausted: back Online`, (await waitFor(page, "conn", (v) => v === "conn=online", 5000)) === "conn=online", await text(page, "conn"));
      check(`${tag} exhausted: no page error`, errors.length === 0, errors.join(" | ") || "none");
      await page.context().close();
    }
  } finally {
    await browser.close();
  }
}

let app;
try {
  for (const f of ["hits.txt", "polls.txt"]) rmSync(join(APP_DIR, f), { force: true });
  app = await startApp(SPA, PORT);
  for (const b of BROWSERS) await (STAGE === "fast" ? fast(b) : slow(b));
  console.log(
    failures.length
      ? `VERDICT=FAIL ${failures.join("; ")}`
      : `VERDICT=PASS (${STAGE}) transient failures are retried by the runtime; only FINAL errors reach the app`,
  );
  process.exitCode = failures.length ? 2 : 0;
} catch (e) {
  console.error("HARNESS ERROR:", e.stack || e.message);
  if (app) console.error(app.log().slice(-2000));
  process.exitCode = 1;
} finally {
  if (app) app.proc.kill("SIGKILL");
  process.exit(process.exitCode ?? 1);
}
