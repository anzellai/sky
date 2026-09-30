#!/usr/bin/env node
// scripts/ui-canvas-terminal-verify.mjs
//
// Browser e2e for Std.Ui.Canvas, the Ui.text wrapping and Std.Ui.Terminal,
// driven by scripts/ui-canvas-terminal-e2e.sh. Every app runs with
// SKY_CSP=strict (script-src 'self' 'wasm-unsafe-eval', no inline script).
//
//   --mode canvas-live  (fixture rust/crates/sky/tests/fixtures/ui-canvas)
//     * the scene is an SVG in the SVG namespace (an SVGRectElement, drawn);
//     * a pointer move over the scene arrives as the typed Msg with the
//       position in SCENE units (moved=x,y);
//     * a pointer down adds a dot: a NEW <circle> patched into the live scene
//       is an SVGCircleElement with a real bounding box (the Sky.Spa client
//       used to create it with createElement, an HTMLUnknownElement that
//       draws nothing);
//     * a click on a shape arrives as its Msg, and does not also fire the
//       backdrop's pointer handler;
//     * a children patch adding 3,000 shapes to an SVG applies in under 2 s
//       as SVG elements (--browser webkit too: a Range per new child made it
//       quadratic in WebKit);
//     * two Ui.text in a column are two lines; a long Ui.text in a narrow box
//       wraps inside it and does not overflow.
//
//   --mode canvas-spa  (the same fixture, Sky.Spa; --browser chromium |
//   webkit; the page at devicePixelRatio 2)
//     * the scene is a <canvas> the wasm client draws (scene_canvas.go), with
//       role="img", the label as aria-label and a text alternative
//       (aria-describedby: the label and the scene's text); no SVG is left;
//     * crisp: the backing store is the CSS size times devicePixelRatio;
//     * pixels drawn: the red square, the black line and the text;
//     * the painter's hit test finds the square, the backdrop and, once it
//       is added, the new dot;
//     * a pointer move arrives as Moved in scene units; a pointer down on
//       the backdrop adds a dot, drawn (blue pixels where it went down) in
//       exactly one more paint; a model change outside the scene paints
//       nothing; a click on the square is Square and adds no dot;
//     * the Ui.text cases as above.
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
//   --mode terminal-race  (fixture ui-terminal, Sky.Live; --browser chromium
//   | webkit, --iterations N, default 16)
//     N times: "new shell" spawns a fresh `sh` at 80x24 and attaches it to the
//     mounted widget, whose size differs (the first screen read and the
//     resize to the widget's size run at once), then:
//     * the server's screen has the widget's size (the text layer has
//       exactly the widget's rows);
//     * in `vim -u NONE -N -n -c 'set showcmd'`, typing `ihello from vim`
//       and Escape keeps row 0 as the typed line. The Escape draws "^[" in
//       the showcmd column (10 from the right); on a screen left at 80
//       columns under a wider PTY that write wraps at the bottom row and
//       scrolls row 0 away, which is how the race used to show (about 3 runs
//       in 10). 16 clean runs leave a 30% failure rate a 0.3% chance.
//
// In every mode: zero securitypolicyviolation events, zero page errors, zero
// console errors.
//
// Usage: node scripts/ui-canvas-terminal-verify.mjs <app-binary> --port N
//          --mode canvas-live|canvas-spa|terminal|terminal-race [--cwd DIR]
//          [--browser chromium|webkit] [--iterations N]
import pw from "playwright";
import { spawn } from "node:child_process";
import { guardChild } from "./lib/child-guard.mjs";
import { dirname } from "node:path";

const { chromium, webkit } = pw;
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
const BROWSER = arg("--browser", "chromium");
const ITERATIONS = Number(arg("--iterations", "16"));
const TAG = `ui-canvas-terminal/${MODE}` + (BROWSER === "chromium" ? "" : `/${BROWSER}`);
if (BROWSER !== "chromium" && BROWSER !== "webkit") {
  console.error(`${TAG}: --browser must be chromium or webkit`);
  process.exit(2);
}

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

const text = (page, sel) => page.evaluate((s) => ((document.querySelector(s) || {}).innerText || "").trim(), sel);

// The Sky.Spa client draws a scene on a canvas (scene_canvas.go).
async function canvasSpaCases(page) {
  check(
    "the wasm client booted",
    await waitFor(page, () => !document.documentElement.hasAttribute("data-sky-hydrating") && !!document.querySelector("#app [sky-id]"), null, 30000)
  );
  const S = "canvas[data-sky-scene]";
  check(
    "the scene is a canvas drawn by the client, role img, labelled",
    await waitFor(
      page,
      (s) => {
        const c = document.querySelector(s);
        return !!(c && c.getAttribute("role") === "img" && c.getAttribute("aria-label") === "Test scene" && c.getAttribute("data-sky-scene-backend") === "canvas");
      },
      S,
      10000
    )
  );
  check("no SVG scene is left in the page", await page.evaluate(() => !document.querySelector("svg[data-sky-scene]")));
  const alt = await page.evaluate((s) => {
    const c = document.querySelector(s);
    const d = c && document.getElementById(c.getAttribute("aria-describedby") || "");
    return d ? d.textContent : "";
  }, S);
  check("the text alternative names the scene and its text", alt === "Test scene. Text in the scene: scene.", alt);
  const geo = await page.evaluate((s) => {
    const c = document.querySelector(s);
    const r = c.getBoundingClientRect();
    return { x: r.left, y: r.top, w: r.width, h: r.height, bw: c.width, bh: c.height, dpr: devicePixelRatio };
  }, S);
  check("the scene is drawn at 400x200 CSS px", Math.round(geo.w) === 400 && Math.round(geo.h) === 200, JSON.stringify(geo));
  check(
    "crisp: the backing store is 400x200 times devicePixelRatio 2",
    geo.dpr === 2 && geo.bw === 800 && geo.bh === 400,
    JSON.stringify(geo)
  );
  // Pixels at scene points (after the painter's frame).
  const pixel = (x, y) =>
    page.evaluate(
      ([s, x, y]) => {
        const c = document.querySelector(s);
        const k = c.width / 400;
        return Array.from(c.getContext("2d").getImageData(Math.round(x * k), Math.round(y * k), 1, 1).data);
      },
      [S, x, y]
    );
  const isRed = (p) => p[0] > 180 && p[1] < 80 && p[2] < 80 && p[3] > 200;
  check(
    "pixels drawn: the red square",
    await waitFor(
      page,
      (s) => {
        const c = document.querySelector(s);
        const k = c.width / 400;
        const p = c.getContext("2d").getImageData(Math.round(330 * k), Math.round(50 * k), 1, 1).data;
        return p[0] > 180 && p[1] < 80 && p[2] < 80 && p[3] > 200;
      },
      S,
      10000
    ),
    JSON.stringify(await pixel(330, 50))
  );
  const line = await pixel(200, 190);
  check("pixels drawn: the black line", line[3] > 200 && line[0] < 80 && line[1] < 80 && line[2] < 80, JSON.stringify(line));
  const inked = await page.evaluate((s) => {
    const c = document.querySelector(s);
    const k = c.width / 400;
    const d = c.getContext("2d").getImageData(Math.round(8 * k), Math.round(6 * k), Math.round(50 * k), Math.round(18 * k)).data;
    let n = 0;
    for (let i = 3; i < d.length; i += 4) if (d[i] > 128) n++;
    return n;
  }, S);
  check("pixels drawn: the text", inked > 20, `${inked} inked pixels`);
  const hits = await page.evaluate((s) => {
    const c = document.querySelector(s);
    return [window.Sky.sceneCanvas.hitTest(c, 330, 50), window.Sky.sceneCanvas.hitTest(c, 200, 100)];
  }, S);
  check("the hit test finds the square (record 1) and the backdrop (record 0)", hits[0] === 1 && hits[1] === 0, JSON.stringify(hits));
  const paints = () => page.evaluate((s) => window.Sky.sceneCanvas.stats(document.querySelector(s)).paints, S);

  const box = { x: geo.x, y: geo.y };
  const p0 = await paints();
  await page.mouse.move(box.x + 100, box.y + 50);
  await page.mouse.move(box.x + 120, box.y + 70, { steps: 4 });
  check(
    "a pointer move arrives as Moved with scene coordinates",
    await waitFor(page, () => /moved=12[01],(69|70|71)/.test(document.body.innerText), null, 10000),
    await text(page, "#moved")
  );
  await page.waitForTimeout(200);
  check("a model change outside the scene paints nothing", (await paints()) === p0, `${p0} -> ${await paints()}`);

  await page.mouse.move(box.x + 60, box.y + 120);
  await page.mouse.down();
  await page.mouse.up();
  check("a pointer down arrives as Down", await waitFor(page, () => document.body.innerText.includes("dots=1"), null, 10000), await text(page, "#dots"));
  check(
    "the new dot is drawn where the pointer went down",
    await waitFor(
      page,
      (s) => {
        const c = document.querySelector(s);
        const k = c.width / 400;
        const p = c.getContext("2d").getImageData(Math.round(60 * k), Math.round(120 * k), 1, 1).data;
        return p[2] > 150 && p[0] < 80 && p[3] > 200;
      },
      S,
      10000
    ),
    JSON.stringify(await pixel(60, 120))
  );
  await page.waitForTimeout(200);
  check("the dot cost exactly one paint", (await paints()) === p0 + 1, `${p0} -> ${await paints()}`);
  check(
    "the hit test finds the new dot",
    (await page.evaluate((s) => window.Sky.sceneCanvas.hitTest(document.querySelector(s), 60, 120), S)) === 4
  );

  await page.mouse.click(box.x + 330, box.y + 50);
  check("a click on the square arrives as Square", await waitFor(page, () => document.body.innerText.includes("clicks=1"), null, 10000), await text(page, "#clicks"));
  await page.waitForTimeout(400);
  check("the click on a shape did not also add a dot", (await text(page, "#dots")) === "dots=1", await text(page, "#dots"));
  check("the square is still red", isRed(await pixel(330, 50)), JSON.stringify(await pixel(330, 50)));
  await textCases(page);
}

async function canvasCases(page) {
  if (MODE === "canvas-spa") return canvasSpaCases(page);
  await page.waitForTimeout(1200); // the SSE handshake
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

  // A children patch that adds many shapes to an SVG. The client used to
  // parse each new child through its own Range, and WebKit updates every
  // Range it has made on each later DOM change: 3,000 new shapes took about
  // 10 s in WebKit (5,000 took 93 s). Applied to a detached copy, so the live
  // scene is not touched.
  const kidsMs = await page.evaluate(() => {
    const host = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    document.body.appendChild(host);
    const kids = [];
    for (let i = 0; i < 3000; i++) kids.push({ html: '<circle sky-id="bulk.' + i + '" cx="' + (i % 400) + '" cy="5" r="1"></circle>' });
    const t0 = performance.now();
    __skyApplyKids(host, kids);
    const ms = performance.now() - t0;
    const ok = host.childElementCount === 3000 && host.lastElementChild instanceof SVGCircleElement;
    host.remove();
    return ok ? ms : -1;
  });
  check("a patch adding 3,000 shapes to an SVG applies in under 2 s, as SVG elements", kidsMs >= 0 && kidsMs < 2000, `${Math.round(kidsMs)} ms`);
  await textCases(page);
}

async function textCases(page) {
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

// terminalRaceCases attaches a fresh shell ITERATIONS times and checks, each
// time, that the server's screen took the widget's size and that vim's Escape
// does not scroll row 0 away (see the header).
async function terminalRaceCases(page) {
  const T = '[data-sky-island="sky-terminal"]';
  check(
    "the terminal widget mounted and measured a size",
    await waitFor(page, (s) => {
      const el = document.querySelector(s);
      return el && el.getAttribute("data-term-ready") === "1" && Number(el.getAttribute("data-term-cols")) > 20;
    }, T, 15000)
  );
  check("the first shell is attached", await waitFor(page, () => /status=running 1\b/.test(document.body.innerText), null, 15000), await text(page, "#status"));
  const size = await page.evaluate((s) => {
    const el = document.querySelector(s);
    return [Number(el.getAttribute("data-term-cols")), Number(el.getAttribute("data-term-rows"))];
  }, T);
  check("the widget is not at the spawn size (80x24), so a lost resize shows", size[0] > 90 && size[1] !== 24, JSON.stringify(size));
  const rowsShown = () => page.evaluate((s) => document.querySelector(s).querySelectorAll(".sky-term-text > div").length, T);
  const row0 = () => page.evaluate((s) => {
    const r = document.querySelector(s).querySelector(".sky-term-text > div");
    return r ? r.textContent.trimEnd() : "";
  }, T);
  const bad = [];
  for (let i = 1; i <= ITERATIONS; i++) {
    const n = i + 1;
    await page.click("#new-shell");
    const attached = await waitFor(page, (want) => new RegExp("status=running " + want + "\\b").test(document.body.innerText), String(n), 15000);
    const prompt = attached && (await waitFor(page, (s) => {
      const r = document.querySelector(s).querySelector(".sky-term-text > div");
      return !!r && r.textContent.trimEnd() === "$";
    }, T, 15000));
    if (!prompt) {
      bad.push(`run ${i}: the new shell never showed its prompt (${JSON.stringify((await termText(page)).slice(0, 120))})`);
      continue;
    }
    await page.waitForTimeout(300); // the resize reaches the PTY
    const shown = await rowsShown();
    await page.click(T);
    await page.keyboard.type(`vim -u NONE -N -n -c 'set showcmd' /tmp/sky-term-race-${process.pid}-${i}.txt`, { delay: 5 });
    await page.keyboard.press("Enter");
    const inVim = await waitFor(page, (s) => /^~\s*$/m.test(document.querySelector(s).querySelector(".sky-term-text").innerText), T, 15000);
    await page.waitForTimeout(400);
    await page.keyboard.type("ihello from vim", { delay: 15 });
    const typed = await waitFor(page, (s) => {
      const r = document.querySelector(s).querySelector(".sky-term-text > div");
      return !!r && r.textContent.trimEnd() === "hello from vim";
    }, T, 10000);
    await page.keyboard.press("Escape");
    await page.waitForTimeout(1200);
    const after = await row0();
    const rowsAfter = await rowsShown();
    if (shown !== size[1] || rowsAfter !== size[1] || !inVim || !typed || after !== "hello from vim") {
      bad.push(`run ${i}: widget ${size[0]}x${size[1]}, screen rows ${shown}/${rowsAfter}, vim ${inVim}, typed ${typed}, row 0 after Escape ${JSON.stringify(after)}`);
    }
    await page.keyboard.type(":q!", { delay: 15 });
    await page.keyboard.press("Enter");
    await page.waitForTimeout(300);
  }
  check(
    `${ITERATIONS} fresh shells attached to the mounted widget: the screen has the widget's size and vim's Escape keeps row 0`,
    bad.length === 0,
    bad.length ? `${bad.length} of ${ITERATIONS} runs failed: ` + bad.slice(0, 4).join(" | ") : ""
  );
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

  // UI_E2E_HEADED=1 runs a visible browser (a local run on a desktop).
  browser = await (BROWSER === "webkit" ? webkit : chromium).launch({ headless: !process.env.UI_E2E_HEADED });
  const context = await browser.newContext({
    viewport: { width: 1000, height: 900 },
    ...(MODE === "canvas-spa" ? { deviceScaleFactor: 2 } : {}),
  });
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
  let cdp = null;
  if (MODE === "terminal") {
    cdp = await context.newCDPSession(page);
    await cdp.send("Network.enable");
  }
  let offline = false;
  page.on("console", (m) => {
    // A request that fails while the test holds the browser offline is the
    // point of that step, not an error of the page.
    if (m.type() !== "error" || offline || /ERR_INTERNET_DISCONNECTED|net::ERR_/.test(m.text())) return;
    // A headed Chromium asks for /favicon.ico, which the fixtures do not
    // serve; that 404 is the browser's, not the page's.
    const at = (m.location() || {}).url || "";
    if (/status of 404/.test(m.text()) && /\/favicon\.ico$/.test(at)) return;
    consoleErrors.push(m.text() + (at ? " (" + at + ")" : ""));
  });
  page.on("pageerror", (e) => consoleErrors.push("[pageerror] " + e.message));
  const origSetOffline = context.setOffline.bind(context);
  context.setOffline = async (v) => {
    offline = v;
    await origSetOffline(v);
  };

  await page.goto(ORIGIN + "/", { waitUntil: "load" });
  if (MODE === "terminal") await terminalCases(page, context, cdp);
  else if (MODE === "terminal-race") await terminalRaceCases(page);
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
