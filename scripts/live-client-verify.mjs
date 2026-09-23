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
//   webview: per-event handler ids, generic binding, property sync
//
// Usage: node scripts/live-client-verify.mjs <app-binary> [--port N]
// Exit: 0 PASS · 2 FAIL · 1 harness error.
import pw from "playwright";
import { spawn } from "node:child_process";
import { readFileSync } from "node:fs";
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
  </div>`);
  await page.evaluate(() => { window.__calls = []; window.__skyDispatch = (hid) => window.__calls.push(hid); });
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
  await page.close();
}

try {
  await waitListening();
  const browser = await chromium.launch({ headless: true });
  await run(browser);
  await runWebview(browser);
  await browser.close();
  console.log(failures.length ? `VERDICT=FAIL ${failures.join("; ")}` : "VERDICT=PASS");
  process.exitCode = failures.length ? 2 : 0;
} catch (e) {
  console.error("HARNESS ERROR:", e.message);
  process.exitCode = 1;
} finally {
  proc.kill("SIGKILL");
}
