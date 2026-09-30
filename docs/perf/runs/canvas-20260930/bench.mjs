#!/usr/bin/env node
// docs/perf/runs/canvas-20260930/bench.mjs
//
// Std.Ui.Canvas: the SVG backend against the batched <canvas> backend.
//
// Runs the bench app (bench-app/, built by the compiler under test) with
// SKY_CSP=strict and drives it in a browser (headed by default, Chromium or
// WebKit, devicePixelRatio 2). For each scene size N (--sizes):
//
//   static      "n0" then "n<N>": the click to the scene painted, --reps runs
//   memory      after the static render: DOM elements, the JS heap and DOM
//               node counts (Chromium, CDP Performance.getMetrics), the wasm
//               memory (Sky.Spa)
//   hit_us      one hit test, the browser's own (document.elementFromPoint)
//               for SVG, Sky.sceneCanvas.hitTest for canvas; mean of 200
//               points over the scene
//   hit_ms      a click on shape i (a static rectangle in the middle of the
//               scene) to "#hit" showing i and the next frame painted,
//               dispatched where document.elementFromPoint says (the target
//               the browser would pick); median of --reps
//   few         "few" mode (shapes 0-4 move), --frames steps: each "step"
//               click to the frame painted
//   all         "all" mode (every shape moves), the same
//
// "Painted" is: the page shows the new state (the model's #n / #t / #hit),
// then a requestAnimationFrame callback, then a macrotask: the frame's
// script, the canvas painter's pass (a rAF callback queued before ours),
// style, layout and paint have run. On Sky.Live the click goes to the server
// and the patch comes back; the time includes that round trip on loopback.
//
// Per-frame figures: median, p95 and mean ms, and on Chromium the main
// thread's TaskDuration / ScriptDuration / Layout+RecalcStyle per frame (CDP).
// A case stops early after --budget-s seconds (the frames it measured are
// recorded, with "capped").
//
// Usage: node bench.mjs <app-binary> --cwd DIR --port N --target live|spa
//          [--backend svg|canvas|auto] [--browser chromium|webkit]
//          [--channel chrome] [--headless] [--sizes 100,1000,5000,20000]
//          [--frames 20] [--reps 3] [--budget-s 60] [--out file.json]
import pw from "playwright";
import { spawn } from "node:child_process";
import { writeFileSync } from "node:fs";

const argv = process.argv.slice(2);
const APP = argv[0];
const arg = (n, d) => {
  const i = argv.indexOf(n);
  return i >= 0 && i + 1 < argv.length ? argv[i + 1] : d;
};
const has = (n) => argv.includes(n);
const CWD = arg("--cwd", ".");
const PORT = Number(arg("--port", "9640"));
const TARGET = arg("--target", "spa");
const BACKEND = arg("--backend", "auto");
const BROWSER = arg("--browser", "chromium");
const CHANNEL = arg("--channel", "");
const HEADLESS = has("--headless");
const SIZES = arg("--sizes", "100,1000,5000,20000").split(",").map(Number);
const FRAMES = Number(arg("--frames", "20"));
const REPS = Number(arg("--reps", "3"));
const BUDGET = Number(arg("--budget-s", "60")) * 1000;
const OUT = arg("--out", "");
const ORIGIN = `http://127.0.0.1:${PORT}`;

const env = { ...process.env, PORT: String(PORT), SKY_LIVE_PORT: String(PORT), ENV: "development", SKY_CSP: "strict" };
const proc = spawn(APP, [], { cwd: CWD, env, stdio: ["ignore", "pipe", "pipe"] });
let log = "";
proc.stdout.on("data", (d) => (log += d));
proc.stderr.on("data", (d) => (log += d));
const stop = () => {
  try {
    proc.kill("SIGTERM");
  } catch (_) {}
};
process.on("exit", stop);

async function listening() {
  for (let i = 0; i < 240; i++) {
    try {
      const r = await fetch(ORIGIN + "/");
      if (r.status < 500) return;
    } catch (_) {}
    await new Promise((r) => setTimeout(r, 250));
  }
  throw new Error("app never listened\n" + log);
}

const q = (xs, p) => {
  if (!xs.length) return null;
  const s = [...xs].sort((a, b) => a - b);
  return +s[Math.min(s.length - 1, Math.floor(p * (s.length - 1) + 0.5))].toFixed(2);
};
const mean = (xs) => (xs.length ? +(xs.reduce((a, b) => a + b, 0) / xs.length).toFixed(2) : null);

// In the page: click #id, wait until #sel reads want, then a rAF and a
// macrotask. Returns [ms to the state in the page, ms to painted].
async function timed(page, id, sel, want) {
  return page.evaluate(
    async ([id, sel, want]) => {
      const read = () => (document.getElementById(sel) || {}).textContent;
      const t0 = performance.now();
      document.getElementById(id).click();
      if (read() !== want) {
        await new Promise((res, rej) => {
          const mo = new MutationObserver(() => {
            if (read() === want) {
              mo.disconnect();
              res();
            }
          });
          mo.observe(document.body, { subtree: true, childList: true, characterData: true });
          setTimeout(() => {
            mo.disconnect();
            rej(new Error(`#${sel} never read ${want} (reads ${read()})`));
          }, 120000);
        });
      }
      const t1 = performance.now();
      await new Promise((res) => requestAnimationFrame(() => setTimeout(res, 0)));
      return [t1 - t0, performance.now() - t0];
    },
    [id, sel, want]
  );
}

async function metrics(cdp) {
  if (!cdp) return null;
  const { metrics } = await cdp.send("Performance.getMetrics");
  const m = {};
  for (const x of metrics) m[x.name] = x.value;
  return m;
}

const sceneSel = "[data-sky-scene]";

let browser = null;
async function run() {
  await listening();
  const bt = BROWSER === "webkit" ? pw.webkit : pw.chromium;
  browser = await bt.launch({ headless: HEADLESS, ...(CHANNEL ? { channel: CHANNEL } : {}) });
  const ctx = await browser.newContext({ viewport: { width: 1000, height: 800 }, deviceScaleFactor: 2 });
  await ctx.addInitScript((backend) => {
    window.Sky = window.Sky || {};
    if (backend !== "auto") window.Sky.sceneBackend = backend;
    // Time spent inside the painter's set / update / mount calls (decode,
    // derive: the JS half of a canvas update; the paint itself is counted by
    // the painter's own stats).
    window.__painterMs = 0;
    let painter;
    Object.defineProperty(window.Sky, "sceneCanvas", {
      configurable: true,
      get: () => painter,
      set: (v) => {
        const wrap = (f) =>
          function () {
            const t0 = performance.now();
            try {
              return f.apply(this, arguments);
            } finally {
              window.__painterMs += performance.now() - t0;
            }
          };
        painter = { ...v, mount: wrap(v.mount), set: wrap(v.set), update: wrap(v.update) };
      },
    });
    window.__violations = 0;
    document.addEventListener("securitypolicyviolation", () => window.__violations++, true);
    if (window.WebAssembly && WebAssembly.instantiateStreaming) {
      const orig = WebAssembly.instantiateStreaming;
      WebAssembly.instantiateStreaming = function () {
        return orig.apply(this, arguments).then((r) => {
          window.__wasm = r.instance;
          return r;
        });
      };
    }
  }, BACKEND);
  const page = await ctx.newPage();
  const errors = [];
  page.on("pageerror", (e) => errors.push(String(e.message)));
  page.on("console", (m) => {
    if (m.type() === "error") errors.push(m.text());
  });
  let cdp = null;
  if (BROWSER === "chromium") {
    cdp = await ctx.newCDPSession(page);
    await cdp.send("Performance.enable");
  }
  await page.goto(ORIGIN + "/", { waitUntil: "load" });
  if (TARGET === "spa") {
    await page.waitForFunction(() => !document.documentElement.hasAttribute("data-sky-hydrating") && !!document.querySelector("#app [sky-id]"), null, { timeout: 60000 });
  } else {
    await page.waitForTimeout(1500);
  }
  const info = await page.evaluate(() => ({ ua: navigator.userAgent, dpr: devicePixelRatio }));
  const out = { target: TARGET, backend: BACKEND, browser: BROWSER, channel: CHANNEL || null, headless: HEADLESS, ...info, sizes: {} };

  for (const n of SIZES) {
    const r = { static: [], static_state: [] };
    for (let k = 0; k < REPS; k++) {
      await timed(page, "n0", "n", "0");
      const [s, p] = await timed(page, `n${n}`, "n", String(n));
      r.static_state.push(+s.toFixed(2));
      r.static.push(+p.toFixed(2));
    }
    // What drew the scene.
    r.drawn_as = await page.evaluate((sel) => {
      const el = document.querySelector(sel);
      return el ? el.tagName.toLowerCase() : "none";
    }, sceneSel);
    const m = await metrics(cdp);
    r.memory = await page.evaluate((sel) => {
      const el = document.querySelector(sel);
      return {
        dom_elements: document.getElementsByTagName("*").length,
        scene_elements: el ? el.getElementsByTagName("*").length + 1 : 0,
        wasm_bytes: window.__wasm ? window.__wasm.exports.mem.buffer.byteLength : null,
      };
    }, sceneSel);
    if (m) {
      r.memory.js_heap_used_bytes = m.JSHeapUsedSize;
      r.memory.cdp_nodes = m.Nodes;
    }
    // The canvas: backing size and the painter's count of the last paint.
    r.canvas = await page.evaluate((sel) => {
      const el = document.querySelector(sel);
      if (!el || el.tagName !== "CANVAS") return null;
      const b = el.getBoundingClientRect();
      return {
        backing: [el.width, el.height],
        css: [Math.round(b.width), Math.round(b.height)],
        role: el.getAttribute("role"),
        label: el.getAttribute("aria-label"),
        described: !!(el.getAttribute("aria-describedby") && document.getElementById(el.getAttribute("aria-describedby"))),
        stats: window.Sky.sceneCanvas.stats(el),
      };
    }, sceneSel);

    // Hit tests: 200 points on a 20 x 10 grid over the left 75% of the scene
    // (on screen in every window this bench opens).
    r.hit_us = await page.evaluate((sel) => {
      const el = document.querySelector(sel);
      const b = el.getBoundingClientRect();
      const pts = [];
      for (let i = 0; i < 20; i++) for (let j = 0; j < 10; j++) pts.push([((i + 0.5) / 20) * 0.75, (j + 0.5) / 10]);
      const canvas = el.tagName === "CANVAS";
      const t0 = performance.now();
      let found = 0;
      for (let k = 0; k < 5; k++) {
        for (const [fx, fy] of pts) {
          if (canvas) found += window.Sky.sceneCanvas.hitTest(el, fx * 800, fy * 600) >= 0 ? 1 : 0;
          else found += document.elementFromPoint(b.left + fx * b.width, b.top + fy * b.height) ? 1 : 0;
        }
      }
      return +(((performance.now() - t0) * 1000) / (pts.length * 5)).toFixed(2);
    }, sceneSel);

    // A click on a static rectangle in the middle row of the scene, near its
    // left edge (headed WebKit on macOS opens a 640 px wide window whatever
    // the viewport asked for, so the right of the 800 px scene is off
    // screen, where elementFromPoint finds nothing).
    const cols = Math.round(Math.sqrt(n * 1.3333)) + 1;
    let i = Math.floor(Math.floor(n / cols + 1) / 2) * cols + 1;
    while (i % 4 !== 0 || i < 8) i++;
    if (i >= n) i = 8;
    r.hit_ms = [];
    r.hit_state_ms = [];
    for (let k = 0; k < REPS; k++) {
      const res = await page.evaluate(
        async ([sel, n, i, prev]) => {
          const c = Math.round(Math.sqrt(n * 1.3333)) + 1;
          const rows = Math.floor(n / c) + 1;
          const cw = 800 / c,
            ch = 600 / rows;
          const x = (i % c) * cw + cw * 0.5,
            y = Math.floor(i / c) * ch + ch * 0.5;
          const el = document.querySelector(sel);
          const b = el.getBoundingClientRect();
          const cx = b.left + (x * b.width) / 800,
            cy = b.top + (y * b.height) / 600;
          const read = () => (document.getElementById("hit") || {}).textContent;
          const want = String(i);
          if (read() === want) return [-1, -1];
          // WebKit can answer null for a point in a just-built 20,000-element
          // SVG until a frame has laid it out: wait for a target first.
          for (let k = 0; k < 30 && !document.elementFromPoint(cx, cy); k++) await new Promise((res) => requestAnimationFrame(res));
          const t0 = performance.now();
          const target = document.elementFromPoint(cx, cy);
          target.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, clientX: cx, clientY: cy, view: window }));
          if (read() !== want) {
            await new Promise((res, rej) => {
              const mo = new MutationObserver(() => {
                if (read() === want) {
                  mo.disconnect();
                  res();
                }
              });
              mo.observe(document.body, { subtree: true, childList: true, characterData: true });
              setTimeout(() => {
                mo.disconnect();
                rej(new Error(`the click at ${x},${y} did not hit ${i} (hit ${read()}, target ${target.tagName})`));
              }, 60000);
            });
          }
          const t1 = performance.now();
          await new Promise((res) => requestAnimationFrame(() => setTimeout(res, 0)));
          return [t1 - t0, performance.now() - t0];
        },
        [sceneSel, n, i, k]
      );
      if (res[0] >= 0) {
        r.hit_state_ms.push(+res[0].toFixed(2));
        r.hit_ms.push(+res[1].toFixed(2));
      }
      // Clear the hit so the next rep clicks again.
      await timed(page, "n0", "n", "0");
      await timed(page, `n${n}`, "n", String(n));
    }

    for (const mode of ["few", "all"]) {
      await timed(page, mode, "n", String(n));
      const t = Number(await page.evaluate(() => document.getElementById("t").textContent));
      const frames = [];
      const states = [];
      const drawn = [];
      const m0 = await metrics(cdp);
      await page.evaluate(() => (window.__painterMs = 0));
      const start = Date.now();
      let capped = false;
      for (let k = 1; k <= FRAMES; k++) {
        const [s, p] = await timed(page, "step", "t", String(t + k));
        states.push(s);
        frames.push(p);
        const st = await page.evaluate((sel) => {
          const el = document.querySelector(sel);
          return el && el.tagName === "CANVAS" ? { ...window.Sky.sceneCanvas.stats(el) } : null;
        }, sceneSel);
        if (st) drawn.push(st);
        if (Date.now() - start > BUDGET && k >= 3) {
          capped = k < FRAMES;
          break;
        }
      }
      const m1 = await metrics(cdp);
      const painterMs = await page.evaluate(() => window.__painterMs);
      const per = (key) => (m0 && m1 ? +(((m1[key] - m0[key]) * 1000) / frames.length).toFixed(2) : null);
      r[mode] = {
        frames: frames.length,
        capped,
        median_ms: q(frames, 0.5),
        p95_ms: q(frames, 0.95),
        mean_ms: mean(frames),
        state_median_ms: q(states, 0.5),
        task_ms_per_frame: per("TaskDuration"),
        script_ms_per_frame: per("ScriptDuration"),
        painter_calls_ms_per_frame: +(painterMs / frames.length).toFixed(2),
        layout_style_ms_per_frame: m0 && m1 ? +((((m1.LayoutDuration - m0.LayoutDuration) + (m1.RecalcStyleDuration - m0.RecalcStyleDuration)) * 1000) / frames.length).toFixed(2) : null,
      };
      if (drawn.length) {
        const last = drawn[drawn.length - 1],
          first = drawn[0];
        r[mode].canvas = {
          paints: last.paints - first.paints + 1,
          frames: drawn.length,
          partial_paints: last.partial - first.partial,
          last_paint_ms: +last.lastMs.toFixed(2),
          last_drawn: last.drawn,
        };
      }
      await timed(page, "few", "n", String(n));
    }
    await timed(page, "n0", "n", "0");
    out.sizes[n] = r;
    console.error(`[${TARGET}/${BACKEND}/${BROWSER}] n=${n} static=${q(r.static, 0.5)} hit=${q(r.hit_ms, 0.5)} few=${r.few.median_ms} all=${r.all.median_ms}${r.all.capped ? " (capped)" : ""} as=${r.drawn_as}`);
  }
  out.violations = await page.evaluate(() => window.__violations);
  out.errors = errors.slice(0, 5);
  await browser.close();
  return out;
}

try {
  const out = await run();
  const s = JSON.stringify(out, null, 1);
  if (OUT) writeFileSync(OUT, s + "\n");
  else console.log(s);
} catch (e) {
  console.error("bench failed:", e && e.stack ? e.stack : e);
  console.error(log.slice(-2000));
  process.exitCode = 1;
} finally {
  if (browser) await browser.close().catch(() => {});
  stop();
}
