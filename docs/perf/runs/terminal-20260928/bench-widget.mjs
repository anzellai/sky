#!/usr/bin/env node
// docs/perf/runs/terminal-20260928/bench-widget.mjs
//
// The widget half of the terminal run: the "sky-terminal" island alone, in
// headless Chromium, fed one workload as the island commands the server
// would push, as fast as the page takes them (one command per task, the way
// SSE messages arrive). No server and no network: this isolates the
// browser main thread.
//
//   node bench-widget.mjs --rev <git-rev> --workdir <dir>          (DOM widget, "output" commands)
//   node bench-widget.mjs --tree <repo-root> --workdir <dir>       (current tree; "frame" commands
//                                                                   from <dir>/<name>.frames.json)
//
// <dir>/<name>.bin must exist (node workloads.mjs <dir>). For the byte path
// the commands are the stream cut into 64 KiB chunks (maxProcessChunkBytes,
// the most one Process.readWithin returns), each a base64 "output" payload.
// For the frame path they are the frames the Go screen model produced from
// the same 64 KiB cuts (one frame per cut: no server-side pacing, so the
// widget sees as many messages as on the byte path).
//
// Measured per workload, median of --reps runs (default 5):
//   total_ms      first command to the last paint
//   cmd_ms        main-thread time inside the widget's command handler
//   render_ms     main-thread time inside the widget's paint
//   frame_p50/p95/max_ms   widget work (command + paint) per animation frame
//   dropped       animation frames missed (a rAF gap of k*16.7 ms counts k-1)
//   frames        animation frames seen
//   main_thread_task_ms / script_ms / layout_style_ms   Chromium's own
//                 Performance.getMetrics deltas over the run (TaskDuration,
//                 ScriptDuration, LayoutDuration + RecalcStyleDuration): the
//                 style, layout and paint the DOM rows cost show here, not in
//                 render_ms
import pw from "playwright";
import { readFileSync, existsSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { COLS, ROWS } from "./workloads.mjs";

const argv = process.argv.slice(2);
const arg = (n, d) => {
  const i = argv.indexOf(n);
  return i >= 0 && i + 1 < argv.length ? argv[i + 1] : d;
};
const REV = arg("--rev", null);
const TREE = arg("--tree", null);
const DIR = arg("--workdir", null);
const REPS = Number(arg("--reps", "5"));
const ONLY = arg("--only", null);
if ((!REV && !TREE) || !DIR) {
  console.error("usage: bench-widget.mjs (--rev REV | --tree ROOT) --workdir DIR [--reps N] [--only name]");
  process.exit(2);
}

function goConst(src, name) {
  const start = src.indexOf("const " + name + " = `");
  if (start < 0) throw new Error("no const " + name);
  const from = src.indexOf("`", start) + 1;
  return src.slice(from, src.indexOf("`", from));
}
function readGo(file) {
  if (REV) return execFileSync("git", ["show", REV + ":runtime-go/rt/" + file], { encoding: "utf8", maxBuffer: 1 << 26 });
  return readFileSync(TREE + "/runtime-go/rt/" + file, "utf8");
}
const js = goConst(readGo("island_client.go"), "islandClientJS") + "\n" + goConst(readGo("island_terminal.go"), "terminalWidgetJS");

function commandsFor(name) {
  const frames = DIR + "/" + name + ".frames.json";
  if (TREE && existsSync(frames) && !REV) {
    return JSON.parse(readFileSync(frames, "utf8")).map((p) => ["frame", p]);
  }
  const bytes = readFileSync(DIR + "/" + name + ".bin");
  const cmds = [];
  for (let off = 0; off < bytes.length; off += 64 << 10) {
    const part = bytes.subarray(off, Math.min(bytes.length, off + (64 << 10)));
    cmds.push(["output", { data: part.toString("base64"), from: off, next: off + part.length, dropped: false }]);
  }
  return cmds;
}

const median = (a) => {
  const s = [...a].sort((x, y) => x - y);
  return s.length ? s[Math.floor(s.length / 2)] : 0;
};

const browser = await pw.chromium.launch({ headless: true });
const results = {};
try {
  const names = ONLY ? [ONLY] : ["yes", "seq", "redraw", "colour"];
  for (const name of names) {
    const cmds = commandsFor(name);
    const wire = cmds.reduce((n, c) => n + JSON.stringify({ id: "t", name: c[0], payload: c[1] }).length, 0);
    const runs = [];
    for (let r = 0; r < REPS; r++) {
      const page = await browser.newPage({ viewport: { width: 1400, height: 1000 } });
      await page.setContent("<!doctype html><html><body style='margin:0'><div id='t' data-sky-island='sky-terminal' data-sky-island-id='t'></div></body></html>");
      // Size the island to exactly COLS x ROWS cells of the widget's font.
      await page.evaluate(
        ([C, R]) => {
          const p = document.createElement("span");
          p.style.cssText = "position:absolute;visibility:hidden;white-space:pre;font-family:ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;line-height:1.2";
          p.textContent = "MMMMMMMMMM";
          document.body.appendChild(p);
          const b = p.getBoundingClientRect();
          p.remove();
          const el = document.getElementById("t");
          el.style.width = Math.ceil((b.width / 10) * C + 1) + "px";
          el.style.height = Math.ceil(b.height * R + 1) + "px";
        },
        [COLS, ROWS]
      );
      await page.addScriptTag({ content: js });
      await page.evaluate(() => window.Sky.__islandHostReady && window.Sky.__islandHostReady());
      await page.waitForFunction(() => document.getElementById("t").getAttribute("data-term-ready") === "1");
      const cdp = await page.context().newCDPSession(page);
      await cdp.send("Performance.enable");
      const metrics = async () => Object.fromEntries((await cdp.send("Performance.getMetrics")).metrics.map((x) => [x.name, x.value]));
      const size = await page.evaluate(() => [document.getElementById("t").getAttribute("data-term-cols"), document.getElementById("t").getAttribute("data-term-rows")].join("x"));
      const m0 = await metrics();
      const res = await page.evaluate(async (cmds) => {
        const el = document.getElementById("t");
        const inst = el.__skyIsland.inst;
        let busy = 0, cmdMs = 0, renderMs = 0;
        const oc = inst.command, or = inst.render;
        inst.command = function () {
          const t = performance.now();
          oc.apply(this, arguments);
          const d = performance.now() - t;
          busy += d;
          cmdMs += d;
        };
        inst.render = function () {
          const t = performance.now();
          or.apply(this, arguments);
          const d = performance.now() - t;
          busy += d;
          renderMs += d;
        };
        const perFrame = [], stamps = [];
        let done = false;
        const tick = (ts) => {
          stamps.push(ts);
          perFrame.push(busy);
          busy = 0;
          if (!done) requestAnimationFrame(tick);
        };
        await new Promise((r) => requestAnimationFrame((ts) => { stamps.push(ts); r(); }));
        requestAnimationFrame(tick);
        const t0 = performance.now();
        await new Promise((resolve) => {
          const ch = new MessageChannel();
          let i = 0;
          ch.port1.onmessage = () => {
            if (i < cmds.length) {
              const c = cmds[i++];
              window.Sky.__islandCommand("t", c[0], c[1]);
              ch.port2.postMessage(0);
            } else resolve();
          };
          ch.port2.postMessage(0);
        });
        // The last paint: two frames after the last command.
        await new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)));
        const total = performance.now() - t0;
        done = true;
        let dropped = 0;
        for (let k = 1; k < stamps.length; k++) {
          const gap = stamps[k] - stamps[k - 1];
          if (gap > 16.7 * 1.5) dropped += Math.round(gap / 16.7) - 1;
        }
        const pf = perFrame.filter((x) => x > 0).sort((a, b) => a - b);
        const q = (p) => (pf.length ? pf[Math.min(pf.length - 1, Math.floor(pf.length * p))] : 0);
        return { total, cmdMs, renderMs, p50: q(0.5), p95: q(0.95), max: pf.length ? pf[pf.length - 1] : 0, dropped, frames: stamps.length - 1 };
      }, cmds);
      const m1 = await metrics();
      const dm = (k) => (m1[k] - m0[k]) * 1000;
      res.task = dm("TaskDuration");
      res.script = dm("ScriptDuration");
      res.layoutStyle = dm("LayoutDuration") + dm("RecalcStyleDuration");
      res.size = size;
      runs.push(res);
      await page.close();
    }
    const m = (k) => Math.round(median(runs.map((x) => x[k])) * 10) / 10;
    results[name] = {
      size: runs[0].size,
      commands: cmds.length,
      wire_bytes: wire,
      total_ms: m("total"),
      cmd_ms: m("cmdMs"),
      render_ms: m("renderMs"),
      frame_p50_ms: m("p50"),
      frame_p95_ms: m("p95"),
      frame_max_ms: m("max"),
      dropped: m("dropped"),
      frames: m("frames"),
      main_thread_task_ms: m("task"),
      script_ms: m("script"),
      layout_style_ms: m("layoutStyle"),
      reps: REPS,
    };
    console.log(name, JSON.stringify(results[name]));
  }
} finally {
  await browser.close();
}
console.log(JSON.stringify(results));
