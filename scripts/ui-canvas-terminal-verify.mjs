#!/usr/bin/env node
// scripts/ui-canvas-terminal-verify.mjs
//
// Browser e2e for Std.Ui.Canvas, the Ui.text wrapping and Std.Ui.Terminal,
// driven by scripts/ui-canvas-terminal-e2e.sh. Every app runs with
// SKY_CSP=strict (script-src 'self' 'wasm-unsafe-eval', no inline script).
//
//   --mode canvas-live | canvas-spa  (fixture rust/crates/sky/tests/fixtures/ui-canvas)
//     * the scene is an SVG in the SVG namespace (an SVGRectElement, drawn);
//     * a pointer move over the scene arrives as the typed Msg with the
//       position in SCENE units (moved=x,y);
//     * a pointer down adds a dot: a NEW <circle> patched into the live scene
//       is an SVGCircleElement with a real bounding box (the Sky.Spa client
//       used to create it with createElement, an HTMLUnknownElement that
//       draws nothing);
//     * a click on a shape arrives as its Msg, and does not also fire the
//       backdrop's pointer handler;
//     * two Ui.text in a column are two lines; a long Ui.text in a narrow box
//       wraps inside it and does not overflow.
//
//   --mode terminal  (fixture ui-terminal, Sky.Live)
//     * the terminal widget mounts, reports a size and draws on a canvas;
//     * typing `echo hi` + Enter shows the line `hi` in the text layer, and
//       the canvas has lit pixels on that row;
//     * a mouse drag over the row selects `hi`, and copy puts it on the
//       clipboard;
//     * a full-screen redraw loop (150 x `clear; ls -la /`) finishes within
//       the frame budget: p95 animation-frame gap <= 50 ms, worst <= 250 ms,
//       at most 25% of frames missed;
//     * a narrower window resizes the widget, the PTY follows (`stty size`
//       prints the widget's rows and columns);
//     * output written while the SSE connection is down shows once it is
//       back, and typing still works;
//     * a reload (a remount: the widget starts empty) is repainted from the
//       server's screen by a repaint frame (base -1), no byte replay: `hi`
//       and `again` are back, each once.
//
// In every mode: zero securitypolicyviolation events, zero page errors, zero
// console errors.
//
// Usage: node scripts/ui-canvas-terminal-verify.mjs <app-binary> --port N
//          --mode canvas-live|canvas-spa|terminal [--cwd DIR]
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
  console.error("usage: ui-canvas-terminal-verify.mjs <app-binary> --port N --mode canvas-live|canvas-spa|terminal [--cwd DIR]");
  process.exit(2);
}
const PORT = Number(arg("--port", "9570"));
const MODE = arg("--mode", "canvas-live");
const CWD = arg("--cwd", dirname(dirname(APP)));
const ORIGIN = `http://127.0.0.1:${PORT}`;
const TAG = `ui-canvas-terminal/${MODE}`;

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

async function waitFor(page, fn, a, ms = 10000) {
  try {
    await page.waitForFunction(fn, a, { timeout: ms });
    return true;
  } catch (_) {
    return false;
  }
}

const text = (page, sel) => page.evaluate((s) => (document.querySelector(s) || {}).innerText || "", sel);

async function canvasCases(page) {
  if (MODE === "canvas-spa") {
    check(
      "the wasm client booted",
      await waitFor(page, () => !document.documentElement.hasAttribute("data-sky-hydrating") && !!document.querySelector("#app [sky-id]"), null, 30000)
    );
  } else {
    await page.waitForTimeout(1200); // the SSE handshake
  }
  check(
    "the scene is an SVG in the SVG namespace, labelled",
    await page.evaluate(() => {
      const svg = document.querySelector("svg[data-sky-scene]");
      const rect = svg && svg.querySelector("rect[fill^='rgba(220']");
      return !!(svg && svg.getAttribute("aria-label") === "Test scene" && rect instanceof SVGRectElement && rect.getBBox().width > 0);
    })
  );
  const box = await page.evaluate(() => {
    const r = document.querySelector("svg[data-sky-scene]").getBoundingClientRect();
    return { x: r.left, y: r.top, w: r.width, h: r.height };
  });
  check("the scene is drawn at 400x200 CSS px", Math.round(box.w) === 400 && Math.round(box.h) === 200, JSON.stringify(box));

  // Pointer move in scene units.
  await page.mouse.move(box.x + 100, box.y + 50);
  await page.mouse.move(box.x + 120, box.y + 70, { steps: 4 });
  check(
    "a pointer move arrives as Moved with scene coordinates",
    await waitFor(page, () => /moved=12[01],(69|70|71)/.test(document.body.innerText), null, 10000),
    await text(page, "#moved")
  );

  // Pointer down on the backdrop adds a dot: a new shape patched into the scene.
  await page.mouse.move(box.x + 60, box.y + 120);
  await page.mouse.down();
  await page.mouse.up();
  check("a pointer down arrives as Down", await waitFor(page, () => document.body.innerText.includes("dots=1"), null, 10000), await text(page, "#dots"));
  check(
    "the new dot is an SVGCircleElement with a real box, where the pointer went down",
    await waitFor(
      page,
      () => {
        const c = document.querySelector("svg[data-sky-scene] circle");
        if (!(c instanceof SVGCircleElement)) return false;
        const b = c.getBBox();
        return b.width > 10 && Math.abs(b.x + b.width / 2 - 60) < 2 && Math.abs(b.y + b.height / 2 - 120) < 2;
      },
      null,
      10000
    ),
    await page.evaluate(() => {
      const c = document.querySelector("svg[data-sky-scene] circle");
      return c ? `${c.constructor.name} ${c.namespaceURI}` : "no circle";
    })
  );

  // A click on the square is its own Msg; the backdrop handler does not fire.
  await page.mouse.click(box.x + 330, box.y + 50);
  check("a click on the square arrives as Square", await waitFor(page, () => document.body.innerText.includes("clicks=1"), null, 10000), await text(page, "#clicks"));
  await page.waitForTimeout(400);
  check("the click on a shape did not also add a dot", (await text(page, "#dots")) === "dots=1", await text(page, "#dots"));

  // Ui.text wrapping.
  const two = await page.evaluate(() => {
    const spans = Array.from(document.querySelectorAll("#two-texts > span"));
    return spans.map((s) => ({ t: s.textContent, top: Math.round(s.getBoundingClientRect().top) }));
  });
  check(
    "two Ui.text in a column are two lines",
    two.length === 2 && two[0].t === "first line" && two[1].t === "second line" && two[1].top > two[0].top,
    JSON.stringify(two)
  );
  const narrow = await page.evaluate(() => {
    const el = document.querySelector("#narrow");
    const s = el.querySelector("span");
    const lh = parseFloat(getComputedStyle(s).lineHeight) || 18;
    const r = s.getBoundingClientRect();
    return { w: Math.round(r.width), h: Math.round(r.height), lh, box: el.clientWidth, sw: el.scrollWidth };
  });
  check(
    "a long Ui.text wraps inside a 120 px box and does not overflow it",
    narrow.h >= 2 * narrow.lh - 1 && narrow.w <= 120 && narrow.sw <= narrow.box,
    JSON.stringify(narrow)
  );
}

// The terminal's text: the widget's text layer (what a screen reader reads
// and a selection copies), without its blank rows.
const TL = '[data-sky-island="sky-terminal"] .sky-term-text';
const termText = (page) =>
  page.evaluate((sel) => {
    const el = document.querySelector(sel);
    return el ? el.innerText.split("\n").map((l) => l.trimEnd()).filter((l) => l).join("\n") : "";
  }, TL);
const lineShown = (page, re, ms) =>
  waitFor(page, ([sel, src]) => new RegExp(src, "m").test((document.querySelector(sel) || {}).innerText || ""), [TL, re], ms);

// animationGaps records requestAnimationFrame gaps (ms) until stopped.
async function startFrames(page) {
  await page.evaluate(() => {
    window.__gaps = [];
    window.__rafOn = true;
    let last = 0;
    const f = (ts) => {
      if (last) window.__gaps.push(ts - last);
      last = ts;
      if (window.__rafOn) requestAnimationFrame(f);
    };
    requestAnimationFrame(f);
  });
}
async function stopFrames(page) {
  return page.evaluate(() => {
    window.__rafOn = false;
    return window.__gaps;
  });
}

async function terminalCases(page, context, cdp) {
  const T = '[data-sky-island="sky-terminal"]';
  check(
    "the terminal widget mounted and measured a size",
    await waitFor(
      page,
      (s) => {
        const el = document.querySelector(s);
        return el && el.getAttribute("data-term-ready") === "1" && Number(el.getAttribute("data-term-cols")) > 20;
      },
      T,
      15000
    )
  );
  check("the widget draws on a canvas", (await page.evaluate((s) => document.querySelector(s).getAttribute("data-term-renderer"), T)) === "canvas");
  check("the process is attached", await waitFor(page, () => document.body.innerText.includes("status=running"), null, 15000), await text(page, "#status"));
  check("the shell prompt shows", await lineShown(page, "\\$\\s*$", 15000), JSON.stringify((await termText(page)).slice(-200)));

  await page.click(T);
  await page.keyboard.type("echo hi", { delay: 20 });
  await page.keyboard.press("Enter");
  check("`echo hi` prints the line hi", await lineShown(page, "^hi\\s*$", 10000), JSON.stringify((await termText(page)).slice(-300)));
  // The canvas drew it: the row of "hi" has pixels in the text colour.
  const drawn = await page.evaluate((s) => {
    const el = document.querySelector(s);
    const rows = Array.from(el.querySelectorAll(".sky-term-text > div"));
    const y = rows.findIndex((r) => r.textContent.trimEnd() === "hi");
    if (y < 0) return "no hi row";
    const cv = el.querySelector("canvas"), ctx = cv.getContext("2d");
    const dpr = cv.width / cv.clientWidth, rh = rows[y].getBoundingClientRect().height;
    const img = ctx.getImageData(0, Math.floor(y * rh * dpr), Math.ceil(rh * 2 * dpr), Math.ceil(rh * dpr)).data;
    let lit = 0;
    for (let i = 0; i < img.length; i += 4) if (img[i] > 120 && img[i + 1] > 120 && img[i + 2] > 120) lit++;
    return lit;
  }, T);
  check("the canvas drew the glyphs of hi", typeof drawn === "number" && drawn > 10, String(drawn));

  // Copy: a mouse selection over the text layer selects the text, and copy
  // puts it on the clipboard.
  const box = await page.evaluate((s) => {
    const rows = Array.from(document.querySelectorAll(s + " .sky-term-text > div"));
    const r = rows.find((x) => x.textContent.trimEnd() === "hi");
    if (!r) return null;
    const b = r.getBoundingClientRect();
    return { x: b.left, y: b.top + b.height / 2, h: b.height };
  }, T);
  if (box) {
    await page.mouse.move(box.x + 1, box.y);
    await page.mouse.down();
    await page.mouse.move(box.x + box.h * 3, box.y, { steps: 5 });
    await page.mouse.up();
  }
  const selected = await page.evaluate(() => String(window.getSelection()));
  check("a mouse drag over the row selects its text", /^hi\s*$/.test(selected), JSON.stringify(selected));
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  await page.evaluate(() => document.execCommand("copy"));
  const copied = await page.evaluate(() => navigator.clipboard.readText().catch((e) => "ERR " + e));
  check("copy puts the selected text on the clipboard", /^hi\s*$/.test(copied), JSON.stringify(copied));

  // A full-screen redraw loop: the page keeps painting.
  await page.click(T);
  await startFrames(page);
  await page.keyboard.type("i=0; while [ $i -lt 150 ]; do clear; ls -la /; i=$((i+1)); done; echo DONE$((40+2))", { delay: 2 });
  await page.keyboard.press("Enter");
  const redrawn = await lineShown(page, "^DONE42\\s*$", 60000);
  const gaps = await stopFrames(page);
  gaps.sort((a, b) => a - b);
  const p95 = gaps.length ? gaps[Math.floor(gaps.length * 0.95)] : 0;
  const worst = gaps.length ? gaps[gaps.length - 1] : 0;
  const dropped = gaps.reduce((n, g) => n + (g > 25 ? Math.round(g / 16.7) - 1 : 0), 0);
  const share = gaps.length ? dropped / (gaps.length + dropped) : 1;
  check("a full-screen redraw loop finishes", redrawn, JSON.stringify((await termText(page)).slice(-200)));
  // The budget: 95% of animation frames within 50 ms, no gap over 250 ms,
  // and at most a quarter of frames missed.
  check(
    "the redraw loop stays within the frame budget (p95 gap <= 50 ms, worst <= 250 ms, <= 25% frames missed)",
    p95 <= 50 && worst <= 250 && share <= 0.25,
    `frames ${gaps.length}, p95 ${p95.toFixed(1)} ms, worst ${worst.toFixed(1)} ms, missed ${(share * 100).toFixed(1)}%`
  );
  console.log(`info [${TAG}] redraw loop: frames ${gaps.length}, p95 gap ${p95.toFixed(1)} ms, worst ${worst.toFixed(1)} ms, missed ${(share * 100).toFixed(1)}%`);
  await page.keyboard.type("clear", { delay: 10 });
  await page.keyboard.press("Enter");
  await page.waitForTimeout(500);
  await page.keyboard.type("echo hi", { delay: 10 });
  await page.keyboard.press("Enter");
  await lineShown(page, "^hi\\s*$", 10000);

  // Resize: a narrower window, a narrower widget, and the PTY follows.
  const cols0 = await page.evaluate((s) => Number(document.querySelector(s).getAttribute("data-term-cols")), T);
  await page.setViewportSize({ width: 600, height: 900 });
  check(
    "a narrower window resizes the widget",
    await waitFor(page, ([s, c]) => Number(document.querySelector(s).getAttribute("data-term-cols")) < c, [T, cols0], 10000),
    `cols ${cols0} -> ${await page.evaluate((s) => document.querySelector(s).getAttribute("data-term-cols"), T)}`
  );
  await page.waitForTimeout(500); // the resize event reaches Process.resize
  const size = await page.evaluate((s) => {
    const el = document.querySelector(s);
    return el.getAttribute("data-term-rows") + " " + el.getAttribute("data-term-cols");
  }, T);
  await page.click(T);
  await page.keyboard.type("stty size", { delay: 20 });
  await page.keyboard.press("Enter");
  check("the PTY has the widget's size (stty size)", await lineShown(page, "^" + size + "\\s*$", 10000), `want "${size}"; ` + JSON.stringify((await termText(page)).slice(-300)));

  // Drop the SSE connection while output arrives, bring it back: the screen
  // catches up (a lost frame is repainted from the server's screen).
  await page.keyboard.type("sleep 1; echo later", { delay: 10 });
  await page.keyboard.press("Enter");
  await context.setOffline(true);
  await page.waitForTimeout(3000);
  await context.setOffline(false);
  check("output written while the connection was down shows after it returns", await lineShown(page, "^later\\s*$", 20000), JSON.stringify((await termText(page)).slice(-300)));
  await page.waitForTimeout(500);
  await page.click(T);
  await page.keyboard.type("echo again", { delay: 20 });
  await page.keyboard.press("Enter");
  check("after the connection drops and returns, the terminal still works", await lineShown(page, "^again\\s*$", 20000), JSON.stringify((await termText(page)).slice(-300)));

  // A reload remounts the widget empty; it is repainted from the server's
  // screen with ONE repaint frame (no byte replay).
  let frames = [];
  const onMsg = (e) => {
    if (e.eventName === "island") frames.push(e.data);
  };
  cdp.on("Network.eventSourceMessageReceived", onMsg);
  await page.reload({ waitUntil: "load" });
  check(
    "after a reload the screen is repainted (hi and again are back)",
    await waitFor(
      page,
      (sel) => {
        const t = (document.querySelector(sel) || {}).innerText || "";
        return /^hi\s*$/m.test(t) && /^again\s*$/m.test(t);
      },
      TL,
      20000
    ),
    JSON.stringify((await termText(page)).slice(-400))
  );
  await page.waitForTimeout(300);
  cdp.off("Network.eventSourceMessageReceived", onMsg);
  const repaints = frames.filter((d) => /"base":-1/.test(d)).length;
  check("the reload was one repaint frame from the screen state", repaints >= 1 && frames.every((d) => !/"name":"output"/.test(d)), `${frames.length} island messages, ${repaints} repaints`);
  const all = await termText(page);
  check("the repaint shows each line once", (all.match(/^again\s*$/gm) || []).length === 1, JSON.stringify(all.slice(-400)));
}

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
  const context = await browser.newContext({ viewport: { width: 1000, height: 900 } });
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
  const cdp = await context.newCDPSession(page);
  await cdp.send("Network.enable");
  let offline = false;
  page.on("console", (m) => {
    // A request that fails while the test holds the browser offline is the
    // point of that step, not an error of the page.
    if (m.type() === "error" && !(offline || /ERR_INTERNET_DISCONNECTED|net::ERR_/.test(m.text()))) consoleErrors.push(m.text());
  });
  page.on("pageerror", (e) => consoleErrors.push("[pageerror] " + e.message));
  const origSetOffline = context.setOffline.bind(context);
  context.setOffline = async (v) => {
    offline = v;
    await origSetOffline(v);
  };

  await page.goto(ORIGIN + "/", { waitUntil: "load" });
  if (MODE === "terminal") await terminalCases(page, context, cdp);
  else await canvasCases(page);

  check("zero securitypolicyviolation events", violations.length === 0, violations.slice(0, 3).join(" | "));
  check("zero console errors", consoleErrors.length === 0, consoleErrors.slice(0, 3).join(" | "));
} catch (e) {
  failures.push(String(e && e.stack ? e.stack : e));
  console.log(`FAIL [${TAG}] ${e && e.stack ? e.stack : e}`);
} finally {
  if (browser) await browser.close().catch(() => {});
  proc.kill("SIGTERM");
}

if (failures.length) {
  console.log(`${TAG}: FAIL (${failures.length})`);
  if (process.env.UI_E2E_VERBOSE) console.log(serverLog);
  process.exit(1);
}
console.log(`${TAG}: PASS`);
