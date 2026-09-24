#!/usr/bin/env node
// scripts/live-client-verify.mjs
//
// Browser e2e for the Sky.Live client (runtime-go/rt/live.go's inline JS)
// and the desktop webview applier (runtime-go/rt/webview.go), driven in real
// headless Chromium against rust/crates/sky/tests/fixtures/live-client.
//
// Every case is a defect the client shipped with. Each FAILS on the runtime
// before the fix and PASSES after it:
//   F1  an element with several handlers dispatches each event's OWN handler
//       (onChange + onEnter composer; onClick + onMouseOver)
//   L6  a pending debounced input is sent before the Enter that follows it
//   F12 events outside the old fixed list bind (contextmenu, dblclick)
//   UF-3 Ui.onFile dispatches
//   F9  a programmatic model value applies to the focused input once acked
//   F2  a model reset to "" clears the field
//   UF-5 a rejected edit / ignored toggle converges to the model
//   F3  a radio group shows one checked option
//   UF-8 a cleared number field sends "" (not "0")
//   UF-11 an IME composition dispatches once, with the committed text
//   L2  a click resolves against the render it was made on (slow network)
//   L9  a delta frame that overtakes its predecessor is held, not dropped
//   L12 a classified update panic shows a banner
//   L3  a tab's SSE reconnect does not re-route another tab
//   nav Back/Forward (popstate) runs onNavigate, as Sky.Spa does
//   a server-cleared focused input takes the next typing cleanly
//   events queued while the event POST fails replay in order, each against
//       the render it was made on
//   event POSTs apply in click order under a jittery network
//   webview: per-event handler ids, generic binding, property sync, IME
//   L7  after a server restart (sqlite session store) the client resets
//       its broadcast guard on the new process epoch and applies fresh
//       frames (a broadcast and a local update)
//
// Usage: node scripts/live-client-verify.mjs <app-binary> [--port N]
// Exit: 0 PASS · 2 FAIL · 1 harness error.
import pw from "playwright";
import { spawn } from "node:child_process";
import { readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
const { chromium } = pw;

function arg(name, def) {
  const i = process.argv.indexOf(name);
  return i >= 0 && process.argv[i + 1] ? process.argv[i + 1] : def;
}
const APP = process.argv[2];
if (!APP) {
  console.error("usage: live-client-verify.mjs <app-binary> [--port N]");
  process.exit(1);
}
const PORT = Number(arg("--port", "9240"));
const BASE = `http://127.0.0.1:${PORT}`;
const ROOT = join(dirname(fileURLToPath(import.meta.url)), "..");

const proc = spawn(APP, [], {
  cwd: dirname(dirname(APP)),
  env: { ...process.env, SKY_LIVE_PORT: String(PORT), PORT: String(PORT), SKY_LIVE_STORE: "memory" },
});
let serverLog = "";
proc.stdout.on("data", (d) => (serverLog += d));
proc.stderr.on("data", (d) => (serverLog += d));

const failures = [];
function check(ok, name, detail) {
  console.log(`${ok ? "ok  " : "FAIL"} ${name}${detail ? " — " + detail : ""}`);
  if (!ok) failures.push(name);
}

async function waitListening() {
  for (let i = 0; i < 100; i++) {
    try {
      const r = await fetch(BASE + "/");
      if (r.ok) return;
    } catch (_) {}
    await new Promise((r) => setTimeout(r, 200));
  }
  throw new Error("app never listened\n" + serverLog);
}

async function freshPage(browser) {
  const ctx = await browser.newContext();
  const page = await ctx.newPage();
  page.on("pageerror", (e) => console.log(`[pageerror] ${e.message}`));
  await page.goto(BASE + "/", { waitUntil: "load" });
  await page.waitForTimeout(300);
  return { ctx, page };
}
const log = async (page) => (await page.locator("#log").innerText()).split(",").filter((s) => s !== "");
const navs = async (page) => (await page.locator("#navs").innerText()).split(",").filter((s) => s !== "");

async function run(browser) {
  // ── F1 + L6: composer (onChange + onEnter on one textarea) ──────────
  {
    const { ctx, page } = await freshPage(browser);
    await page.locator("#draft").click();
    await page.keyboard.type("hello", { delay: 20 });
    await page.keyboard.press("Enter"); // inside the 150 ms debounce
    await page.waitForTimeout(700);
    const l = await log(page);
    check(l.includes("send:hello") && !l.includes("send:"),
      "F1/L6 composer: typing then Enter sends the typed draft once", JSON.stringify(l));
    await ctx.close();
  }
  // ── F1: onClick + onMouseOver on one button ─────────────────────────
  {
    const { ctx, page } = await freshPage(browser);
    await page.locator("#multi").hover();
    await page.waitForTimeout(400);
    const l = await log(page);
    check(l.includes("hovered") && !l.includes("clicked"), "F1 hover dispatches the hover handler", JSON.stringify(l));
    await ctx.close();
  }
  // ── F12: contextmenu + dblclick ─────────────────────────────────────
  {
    const { ctx, page } = await freshPage(browser);
    await page.locator("#ctx").click({ button: "right" });
    await page.waitForTimeout(300);
    await page.locator("#ctx").dblclick();
    await page.waitForTimeout(400);
    const l = await log(page);
    check(l.includes("contextmenu") && l.includes("dblclick"), "F12 contextmenu + dblclick bind", JSON.stringify(l));
    await ctx.close();
  }
  // ── UF-3: Ui.onFile ─────────────────────────────────────────────────
  {
    const { ctx, page } = await freshPage(browser);
    await page.locator("#file").setInputFiles({ name: "small.txt", mimeType: "text/plain", buffer: Buffer.from("hello file") });
    await page.waitForTimeout(600);
    const l = await log(page);
    check(l.some((s) => s.startsWith("file:data:text/plain")), "UF-3 Ui.onFile dispatches", JSON.stringify(l));
    await ctx.close();
  }
  // ── F9/UF-4: normalised value on the focused input; F2: clear ────────
  {
    const { ctx, page } = await freshPage(browser);
    await page.locator("#upper").click();
    await page.keyboard.type("abc", { delay: 20 });
    await page.waitForTimeout(800);
    const v1 = await page.locator("#upper").inputValue();
    check(v1 === "ABC", "F9/UF-4 focused controlled input shows the normalised model", `value=${v1}`);
    await page.locator("#clear").click();
    await page.waitForTimeout(500);
    const v2 = await page.locator("#upper").inputValue();
    check(v2 === "", "F2 a model reset to \"\" clears the field", `value=${JSON.stringify(v2)}`);
    await ctx.close();
  }
  // ── UF-5: rejected edit, ignored toggle ─────────────────────────────
  {
    const { ctx, page } = await freshPage(browser);
    await page.locator("#capped").click();
    await page.keyboard.type("abcde", { delay: 20 });
    await page.waitForTimeout(600); // the model accepts "abcde"
    await page.keyboard.type("fg", { delay: 20 }); // update rejects "abcdefg"
    await page.waitForTimeout(900);
    const v = await page.locator("#capped").inputValue();
    check(v === "abcde", "UF-5 a rejected edit converges to the model", `value=${v}`);
    await page.locator("input[type=checkbox]").first().click();
    await page.waitForTimeout(500);
    const c = await page.locator("input[type=checkbox]").first().isChecked();
    check(!c, "UF-5 an ignored checkbox toggle converges to the model", `checked=${c}`);
    await ctx.close();
  }
  // ── F3: radio group ─────────────────────────────────────────────────
  {
    const { ctx, page } = await freshPage(browser);
    const radios = page.locator('input[type="radio"]');
    await radios.nth(1).click();
    await page.waitForTimeout(400);
    await radios.nth(0).click();
    await page.waitForTimeout(400);
    const states = await radios.evaluateAll((els) => els.map((e) => e.checked));
    check(states.filter(Boolean).length === 1 && states[0], "F3 one radio checked", JSON.stringify(states));
    await ctx.close();
  }
  // ── UF-8: cleared number field ──────────────────────────────────────
  {
    const { ctx, page } = await freshPage(browser);
    await page.locator("#num").click();
    await page.keyboard.type("12", { delay: 30 });
    await page.waitForTimeout(400);
    await page.keyboard.press("Backspace");
    await page.keyboard.press("Backspace");
    await page.waitForTimeout(500);
    const l = await log(page);
    check(l[l.length - 1] === "num:[]", "UF-8 a cleared number field sends \"\"", JSON.stringify(l));
    await ctx.close();
  }
  // ── UF-11: IME composition ──────────────────────────────────────────
  {
    const { ctx, page } = await freshPage(browser);
    await page.locator("#ime").click();
    const cdp = await ctx.newCDPSession(page);
    await cdp.send("Input.imeSetComposition", { text: "k", selectionStart: 1, selectionEnd: 1 });
    await page.waitForTimeout(250);
    await cdp.send("Input.imeSetComposition", { text: "かな", selectionStart: 2, selectionEnd: 2 });
    await page.waitForTimeout(250);
    await cdp.send("Input.insertText", { text: "仮名" });
    await page.waitForTimeout(600);
    const l = (await log(page)).filter((s) => s.startsWith("ime:"));
    check(l.length === 1 && l[0] === "ime:仮名", "UF-11 one Msg with the committed text", JSON.stringify(l));
    await ctx.close();
  }
  // ── L2: two taps on one render over a slow network ──────────────────
  {
    const { ctx, page } = await freshPage(browser);
    await page.route("**/_sky/event", async (route) => {
      await new Promise((r) => setTimeout(r, 400));
      await route.continue();
    });
    await page.locator("#del-a").click();
    await page.locator("#del-b").click({ timeout: 2000 }); // same DOM: the reply to "a" has not landed
    await page.waitForTimeout(1600);
    await page.unroute("**/_sky/event");
    const l = (await log(page)).filter((s) => s.startsWith("delete:"));
    check(JSON.stringify(l) === JSON.stringify(["delete:a", "delete:b"]),
      "L2 each tap deletes the row it was made on", JSON.stringify(l));
    await ctx.close();
  }
  // ── L9: a delta frame that overtakes its predecessor ────────────────
  {
    const { ctx, page } = await freshPage(browser);
    const r = await page.evaluate(() => {
      var applied = [];
      var v0 = (typeof __skyView === "string" && __skyView) ? __skyView : "v0";
      __skyView = v0;
      var s = __skyLastAppliedSeq;
      // Frame 2 (base v1 -> v2) arrives BEFORE frame 1 (base v0 -> v1).
      __skyHandleResponse(s + 2, null, function () { applied.push(2); }, 0, "v2", "v1", true);
      __skyHandleResponse(s + 1, null, function () { applied.push(1); }, 0, "v1", v0, true);
      return { applied: applied, view: __skyView };
    });
    check(JSON.stringify(r.applied) === "[1,2]" && r.view === "v2",
      "L9 an overtaking delta is held and applied in order", JSON.stringify(r));
    await ctx.close();
  }
  // ── L12: classified update panic → banner ───────────────────────────
  {
    const { ctx, page } = await freshPage(browser);
    await page.locator("#boom").click();
    await page.waitForTimeout(600);
    const shown = await page.evaluate(() => {
      var el = document.getElementById("__sky-error");
      return !!el && el.style.display !== "none" && /went wrong/.test(el.textContent);
    });
    check(shown, "L12 a panicking update shows a runtime banner");
    await ctx.close();
  }
  // ── L3: two tabs, two routes, one tab reconnects ────────────────────
  {
    const ctx = await browser.newContext();
    const t1 = await ctx.newPage();
    await t1.goto(BASE + "/", { waitUntil: "load" });
    await t1.waitForTimeout(400);
    const t2 = await ctx.newPage();
    await t2.goto(BASE + "/other", { waitUntil: "load" });
    await t2.waitForTimeout(600);
    await t1.evaluate(() => __skyForceReopenSSE());
    await t1.waitForTimeout(2500);
    const label = await t2.locator("#page-btn").innerText();
    await t2.locator("#page-btn").click();
    await t2.waitForTimeout(700);
    const l = await log(t2);
    const want = label.includes("wipe") ? "wipe-other" : "inc-main";
    check(l.includes(want) && l.length === 1,
      "L3 a click dispatches the Msg of the button the tab shows", `button=${JSON.stringify(label)} log=${JSON.stringify(l)}`);
    await ctx.close();
  }
  // ── Back / Forward (popstate) runs onNavigate, like Sky.Spa ─────────
  {
    const { ctx, page } = await freshPage(browser);
    const before = await navs(page);
    await page.evaluate(() => history.pushState({}, "", "/other"));
    await page.evaluate(() => history.back()); // popstate → "/"
    await page.waitForTimeout(800);
    await page.evaluate(() => history.forward()); // popstate → "/other"
    await page.waitForTimeout(800);
    const shown = await page.locator("#page").innerText();
    const after = await navs(page);
    check(shown === "other" && after.length === before.length + 2 &&
      JSON.stringify(after.slice(-2)) === JSON.stringify(["main", "other"]),
      "popstate routes the page and runs onNavigate once per Back / Forward",
      `page=${shown} before=${JSON.stringify(before)} after=${JSON.stringify(after)}`);
    await ctx.close();
  }
  // ── A server clear of the focused input, then typing ────────────────
  for (const pause of [700, 0]) {
    const { ctx, page } = await freshPage(browser);
    await page.locator("#draft").click();
    await page.keyboard.type("first", { delay: 20 });
    await page.waitForTimeout(400);
    await page.keyboard.press("Enter"); // Send clears the draft in the model
    if (pause) await page.waitForTimeout(pause);
    await page.keyboard.type("second", { delay: 20 });
    await page.waitForTimeout(900);
    const v = await page.locator("#draft").inputValue();
    const l = await log(page);
    check(v === "second" && l.includes("send:first") && l[l.length - 1] === "draft:second",
      `a cleared focused input takes the next typing (${pause ? "after the reply" : "immediately"})`,
      `value=${JSON.stringify(v)} log=${JSON.stringify(l)}`);
    await ctx.close();
  }
  // ── Events queued while the POST fails replay in order ──────────────
  {
    const { ctx, page } = await freshPage(browser);
    await page.route("**/_sky/event", (route) => route.abort("connectionfailed"));
    await page.locator("#del-a").click();
    await page.waitForTimeout(150);
    await page.locator("#del-b").click();
    await page.waitForTimeout(400);
    const queued = await page.evaluate(() => __skyEventQueue.length);
    await page.unroute("**/_sky/event");
    await page.waitForTimeout(4000); // the retry timer drains the queue
    const l = (await log(page)).filter((s) => s.startsWith("delete:"));
    const rows = await page.locator("#rows [id^=del-]").allInnerTexts();
    check(queued >= 1 && JSON.stringify(l) === JSON.stringify(["delete:a", "delete:b"]) &&
      JSON.stringify(rows) === JSON.stringify(["x c", "x d"]),
      "queued events replay once each, in order, against the render they were made on",
      `queued=${queued} log=${JSON.stringify(l)} rows=${JSON.stringify(rows)}`);
    await ctx.close();
  }
  // ── Event POSTs apply in click order under network jitter ───────────
  // 16 clicks on two buttons with no hover handler: every click is a new
  // render, and the session keeps the handler maps of the last 16 renders
  // (docs/skylive/architecture.md, "A click resolves against the render it
  // was made on"), so no click here can fall out of that window.
  {
    const { ctx, page } = await freshPage(browser);
    let n = 0;
    const statuses = {};
    page.on("response", (r) => {
      if (!r.url().endsWith("/_sky/event")) return;
      const k = r.headers()["x-sky-status"] || String(r.status());
      statuses[k] = (statuses[k] || 0) + 1;
    });
    await page.route("**/_sky/event", async (route) => {
      n += 1;
      await new Promise((r) => setTimeout(r, (n * 37) % 160)); // uneven delays
      await route.continue();
    });
    const want = [];
    for (let i = 0; i < 8; i++) {
      await page.locator("#page-btn").click();
      want.push("inc-main");
      await page.locator("#clear").click();
      want.push("clear");
    }
    // The POSTs are serialised, so the last reply lands a while after the
    // last click: wait for all 16 (or 15 s) before judging the order.
    let l = [];
    for (let i = 0; i < 60; i++) {
      l = (await log(page)).filter((s) => s === "inc-main" || s === "clear");
      if (l.length >= want.length) break;
      await page.waitForTimeout(250);
    }
    await page.unroute("**/_sky/event");
    check(JSON.stringify(l) === JSON.stringify(want), "16 event POSTs apply in click order",
      `log=${JSON.stringify(l)} replies=${JSON.stringify(statuses)} posts=${n}`);
    await ctx.close();
  }
}

// ── L7: a server restart mid-session (sqlite session store) ───────────
// A second process of the same app on PORT+1 with a sqlite session store,
// so both sessions survive the restart. Tab B hears A's broadcasts. After
// the restart the broadcast counter starts again at 1; B must reset its
// guard on the new process epoch (`pe` in the SSE hello) and apply it.
async function runRestart(browser) {
  const port = PORT + 1;
  const base = `http://127.0.0.1:${port}`;
  const db = join(tmpdir(), `sky-live-l7-${process.pid}.db`);
  const env = {
    ...process.env, SKY_LIVE_PORT: String(port), PORT: String(port),
    SKY_LIVE_STORE: "sqlite", SKY_LIVE_STORE_PATH: db,
  };
  let app = null;
  let restartLog = "";
  const start = async () => {
    app = spawn(APP, [], { cwd: dirname(dirname(APP)), env });
    app.stdout.on("data", (d) => (restartLog += d));
    app.stderr.on("data", (d) => (restartLog += d));
    for (let i = 0; i < 100; i++) {
      try {
        if ((await fetch(base + "/")).ok) return;
      } catch (_) {}
      await new Promise((r) => setTimeout(r, 200));
    }
    throw new Error("restart app never listened\n" + restartLog);
  };
  const stop = async () => {
    if (!app) return;
    const p = app;
    app = null;
    const gone = new Promise((r) => p.once("exit", r));
    p.kill("SIGKILL");
    await gone;
  };
  const ctxA = await browser.newContext();
  const ctxB = await browser.newContext();
  try {
    await start();
    const a = await ctxA.newPage();
    const b = await ctxB.newPage();
    await a.goto(base + "/", { waitUntil: "load" });
    await b.goto(base + "/", { waitUntil: "load" });
    await a.waitForTimeout(800);
    for (let i = 0; i < 3; i++) {
      await a.locator("#shout").click();
      await a.waitForTimeout(300);
    }
    await b.waitForTimeout(800);
    const heard0 = (await log(b)).filter((s) => s === "heard:hi").length;
    const epoch0 = await b.evaluate(() => __skyProcEpoch);
    const g0 = await b.evaluate(() => __skyLastGlobalSeq);
    check(heard0 === 3 && g0 >= 3, "L7 setup: B heard 3 broadcasts before the restart", `heard=${heard0} globalSeq=${g0}`);

    await stop();
    await start();
    // Both tabs reconnect their SSE and receive the new process's hello.
    let epoch1 = epoch0;
    for (let i = 0; i < 60 && epoch1 === epoch0; i++) {
      await b.waitForTimeout(250);
      epoch1 = await b.evaluate(() => __skyProcEpoch);
    }
    await a.waitForTimeout(1500);
    check(epoch1 && epoch1 !== epoch0, "L7 the SSE hello of the restarted process carries a new epoch",
      `before=${epoch0} after=${epoch1}`);

    await a.locator("#shout").click();
    await b.waitForTimeout(1500);
    const heard1 = (await log(b)).filter((s) => s === "heard:hi").length;
    check(heard1 === 4, "L7 after a restart a new broadcast applies (the guard reset on the new epoch)",
      `heard=${heard1}`);

    await b.locator("#page-btn").click();
    await b.waitForTimeout(800);
    const lb = await log(b);
    check(lb[lb.length - 1] === "inc-main", "L7 after a restart a local update applies", JSON.stringify(lb));
  } finally {
    await ctxA.close().catch(() => {});
    await ctxB.close().catch(() => {});
    await stop();
    for (const f of [db, db + "-wal", db + "-shm"]) rmSync(f, { force: true });
  }
}

// ── Desktop webview applier (webview.go) ──────────────────────────────
async function runWebview(browser) {
  const src = readFileSync(process.env.SKY_WEBVIEW_GO || join(ROOT, "runtime-go/rt/webview.go"), "utf8");
  const start = src.indexOf("const webviewSharedJS = `") + "const webviewSharedJS = `".length;
  const js = src.slice(start, src.indexOf("`", start));
  const page = await browser.newPage();
  await page.setContent(`<div id="sky-root">
    <textarea id="ta" sky-id="r.0#textarea" sky-enter="Send" sky-input="_" data-sky-hid="r.0#textarea.enter"></textarea>
    <div id="cm" sky-id="r.1#div" sky-contextmenu="Ctx" sky-dblclick="Dbl" data-sky-hid="r.1#div.contextmenu">x</div>
    <input id="v" sky-id="r.2#input" value="abc">
    <input id="rb" type="radio" sky-id="r.3#input" checked="checked">
    <input id="ime" sky-id="r.4#input" sky-input="SetIme">
  </div>`);
  await page.evaluate(() => {
    window.__calls = [];
    window.__args = [];
    window.__skyDispatch = (hid, args) => {
      window.__calls.push(hid);
      window.__args.push([hid, args]);
    };
  });
  await page.addScriptTag({ content: js });
  await page.locator("#ta").click();
  await page.keyboard.type("x");
  await page.keyboard.press("Enter");
  await page.locator("#cm").click({ button: "right" });
  await page.locator("#cm").dblclick();
  await page.locator("#v").fill("zzz");
  await page.evaluate(() => window.__skyApplyPatches([
    { id: "r.2#input", attrs: { value: "" } },
    { id: "r.3#input", attrs: { checked: "" } },
  ]));
  const r = await page.evaluate(() => ({
    calls: window.__calls,
    v: document.getElementById("v").value,
    rb: document.getElementById("rb").checked,
  }));
  check(r.calls.includes("r.0#textarea.input") && r.calls.includes("r.0#textarea.enter"),
    "webview F1 each event dispatches its own handler id", JSON.stringify(r.calls));
  check(r.calls.includes("r.1#div.contextmenu") && r.calls.includes("r.1#div.dblclick"),
    "webview F12 contextmenu + dblclick bind", JSON.stringify(r.calls));
  check(r.v === "" && r.rb === false, "webview F2/F3 attribute removal syncs the property", JSON.stringify(r));
  // UF-11 (webview): an IME pre-edit must not dispatch; the commit dispatches
  // once, with the committed text. Same CDP composition as the Live case.
  await page.locator("#ime").click();
  const cdp = await page.context().newCDPSession(page);
  await cdp.send("Input.imeSetComposition", { text: "k", selectionStart: 1, selectionEnd: 1 });
  await page.waitForTimeout(150);
  await cdp.send("Input.imeSetComposition", { text: "かな", selectionStart: 2, selectionEnd: 2 });
  await page.waitForTimeout(150);
  await cdp.send("Input.insertText", { text: "仮名" });
  await page.waitForTimeout(300);
  const ime = await page.evaluate(() =>
    window.__args.filter((c) => c[0] === "r.4#input.input").map((c) => c[1][0]));
  check(ime.length === 1 && ime[0] === "仮名", "webview UF-11 one dispatch with the committed text", JSON.stringify(ime));
  await page.close();
}

let browser;
try {
  await waitListening();
  browser = await chromium.launch({ headless: true });
  await run(browser);
  await runWebview(browser);
  await runRestart(browser);
  await browser.close();
  console.log(failures.length ? `VERDICT=FAIL ${failures.join("; ")}` : "VERDICT=PASS");
  process.exitCode = failures.length ? 2 : 0;
} catch (e) {
  console.error("HARNESS ERROR:", e.message);
  process.exitCode = 1;
} finally {
  // An error leaves Chromium open, and Node then never exits: the gate hangs
  // instead of failing. Close it, stop the app, and exit explicitly.
  try {
    await browser?.close();
  } catch (_) {}
  proc.kill("SIGKILL");
  process.exit(process.exitCode ?? 1);
}
