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
//     * the terminal widget mounts and reports a size;
//     * typing `echo hi` + Enter shows the line `hi`;
//     * a narrower window resizes the widget, the PTY follows (`stty size`
//       prints the widget's rows and columns);
//     * after the SSE connection drops and comes back, typing still works;
//     * a reload (a remount: the widget starts empty) repaints the
//       scrollback: `hi` is back without typing it again.
//
// In every mode: zero securitypolicyviolation events, zero page errors, zero
// console errors.
//
// Usage: node scripts/ui-canvas-terminal-verify.mjs <app-binary> --port N
//          --mode canvas-live|canvas-spa|terminal [--cwd DIR]
import pw from "playwright";
import { spawn } from "node:child_process";
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
const proc = spawn(APP, [], { cwd: CWD, env });
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

// The terminal's text without its blank rows, for failure details.
const termText = (page) =>
  page.evaluate(() => {
    const el = document.querySelector('[data-sky-island="sky-terminal"]');
    return el ? el.innerText.split("\n").map((l) => l.trimEnd()).filter((l) => l).join("\n") : "";
  });

async function terminalCases(page, context) {
  check(
    "the terminal widget mounted and measured a size",
    await waitFor(
      page,
      () => {
        const el = document.querySelector('[data-sky-island="sky-terminal"]');
        return el && el.getAttribute("data-term-ready") === "1" && Number(el.getAttribute("data-term-cols")) > 20;
      },
      null,
      15000
    )
  );
  check("the process is attached", await waitFor(page, () => document.body.innerText.includes("status=running"), null, 15000), await text(page, "#status"));
  check("the shell prompt shows", await waitFor(page, () => /\$\s*$/m.test((document.querySelector('[data-sky-island="sky-terminal"]') || {}).innerText || ""), null, 15000), JSON.stringify((await termText(page)).slice(-200)));

  await page.click('[data-sky-island="sky-terminal"]');
  await page.keyboard.type("echo hi", { delay: 20 });
  await page.keyboard.press("Enter");
  check(
    "`echo hi` prints the line hi",
    await waitFor(page, () => /^hi\s*$/m.test(document.querySelector('[data-sky-island="sky-terminal"]').innerText), null, 10000),
    JSON.stringify((await termText(page)).slice(-300))
  );

  // Resize: a narrower window, a narrower widget, and the PTY follows.
  const cols0 = await page.evaluate(() => Number(document.querySelector('[data-sky-island="sky-terminal"]').getAttribute("data-term-cols")));
  await page.setViewportSize({ width: 600, height: 900 });
  check(
    "a narrower window resizes the widget",
    await waitFor(page, (c) => Number(document.querySelector('[data-sky-island="sky-terminal"]').getAttribute("data-term-cols")) < c, cols0, 10000),
    `cols ${cols0} -> ${await page.evaluate(() => document.querySelector('[data-sky-island="sky-terminal"]').getAttribute("data-term-cols"))}`
  );
  await page.waitForTimeout(500); // the resize event reaches Process.resize
  const size = await page.evaluate(() => {
    const el = document.querySelector('[data-sky-island="sky-terminal"]');
    return el.getAttribute("data-term-rows") + " " + el.getAttribute("data-term-cols");
  });
  await page.click('[data-sky-island="sky-terminal"]');
  await page.keyboard.type("stty size", { delay: 20 });
  await page.keyboard.press("Enter");
  check(
    "the PTY has the widget's size (stty size)",
    await waitFor(page, (s) => new RegExp("^" + s + "\\s*$", "m").test(document.querySelector('[data-sky-island="sky-terminal"]').innerText), size, 10000),
    `want "${size}"; ` + JSON.stringify((await termText(page)).slice(-300))
  );

  // Drop the SSE connection, bring it back, and keep typing.
  await context.setOffline(true);
  await page.waitForTimeout(2500);
  await context.setOffline(false);
  await page.waitForTimeout(3000);
  await page.click('[data-sky-island="sky-terminal"]');
  await page.keyboard.type("echo again", { delay: 20 });
  await page.keyboard.press("Enter");
  check(
    "after the connection drops and returns, the terminal still works",
    await waitFor(page, () => /^again\s*$/m.test(document.querySelector('[data-sky-island="sky-terminal"]').innerText), null, 20000),
    JSON.stringify((await termText(page)).slice(-300))
  );

  // A reload remounts the widget empty; the scrollback is replayed.
  await page.reload({ waitUntil: "load" });
  check(
    "after a reload the scrollback is repainted (hi and again are back)",
    await waitFor(
      page,
      () => {
        const el = document.querySelector('[data-sky-island="sky-terminal"]');
        if (!el) return false;
        const t = el.innerText;
        return /^hi\s*$/m.test(t) && /^again\s*$/m.test(t);
      },
      null,
      20000
    ),
    JSON.stringify((await termText(page)).slice(-400))
  );
  check("the replay did not print the output twice", ((await termText(page)).match(/^hi\s*$/gm) || []).length === 1, JSON.stringify((await termText(page)).slice(-400)));
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
  if (MODE === "terminal") await terminalCases(page, context);
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
