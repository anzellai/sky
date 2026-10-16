#!/usr/bin/env node
// scripts/spa-async-arms-verify.mjs
//
// Browser e2e for Sky.Spa async arms (v0.27.9): a server arm whose own model
// write the client can compute runs ASYNC through `case` / `if` / `let` and
// helpers, so the page shows that write while the server call runs, as Sky.Live
// does. Before v0.27.9 only a direct `( model, cmd )` tuple did; a support desk's
// `Select` froze the page for the whole model call.
//
// Drives rust/crates/sky/tests/fixtures/spa-async-arms, built for --target
// web:app and for Sky.Live, in each browser, under SKY_CSP=strict. The app's
// "model" is an HTTP endpoint THIS script serves and holds open until it
// releases it, so every check is ORDERED, never timed: a check reads the page
// while the model call is known to be held.
//
//   select  `Select 1` (a helper's command): while the model call is held,
//           the page shows SELECTED=1 and BUSY=drafting, and typed text
//           applies; the /_rpc/Select request is still pending (web:app).
//           Released, the draft arrives.
//   saved   `Select 2` (the helper's `Cmd.none` branch): the draft shows,
//           no /_rpc request is sent and the model is never called.
//   hold    `HoldPick` writes a server value, so it holds (the negative
//           control): while its call is held, web:app shows HOLD=- and Sky.Live
//           shows HOLD=held; both end at HOLD=held done.
//
// Usage: node scripts/spa-async-arms-verify.mjs <web:app-backend> <live-app> [--port N]
//   SKY_E2E_BROWSERS  comma list of chromium,webkit (default chromium)
//   SKY_E2E_CHANNEL   a Playwright Chromium channel, e.g. "chrome"
//   SKY_E2E_HEADED=1  run the browsers headed
// Exit: 0 PASS · 2 FAIL · 1 harness error.
import pw from "playwright";
import http from "node:http";
import { spawn } from "node:child_process";
import { dirname } from "node:path";
import { guardChild } from "./lib/child-guard.mjs";

function arg(name, def) {
  const i = process.argv.indexOf(name);
  return i >= 0 && process.argv[i + 1] ? process.argv[i + 1] : def;
}
const SPA = process.argv[2];
const LIVE = process.argv[3];
if (!SPA || !LIVE) {
  console.error("usage: spa-async-arms-verify.mjs <web:app-backend> <live-app> [--port N]");
  process.exit(1);
}
const PORT = Number(arg("--port", "9385"));
const MODEL_PORT = PORT + 2;
const BROWSERS = (process.env.SKY_E2E_BROWSERS || "chromium").split(",").map((s) => s.trim()).filter(Boolean);
const HEADED = process.env.SKY_E2E_HEADED === "1";
const CHANNEL = process.env.SKY_E2E_CHANNEL || undefined;

const failures = [];
function check(step, ok, detail) {
  console.log(`${ok ? "ok  " : "FAIL"} ${step}: ${detail}`);
  if (!ok) failures.push(step);
}

// ---- the held "model" -------------------------------------------------------
// Every request waits until `release()` answers it.
const held = [];
let calls = 0;
const model = http.createServer((req, res) => {
  calls++;
  held.push(res);
});
function release(body) {
  const res = held.shift();
  if (!res) throw new Error("release: no held model call");
  res.writeHead(200, { "content-type": "text/plain" });
  res.end(body);
}
async function until(what, pred, ms = 15000) {
  const deadline = Date.now() + ms;
  while (!(await pred())) {
    if (Date.now() > deadline) throw new Error(`timed out waiting for ${what}`);
    await new Promise((r) => setTimeout(r, 25));
  }
}

async function startApp(bin, port) {
  const env = {
    ...process.env,
    PORT: String(port),
    SKY_LIVE_PORT: String(port),
    SKY_CSP: "strict",
    SPA_ASYNC_MODEL_URL: `http://127.0.0.1:${MODEL_PORT}/draft`,
  };
  const proc = guardChild(spawn(bin, [], { cwd: dirname(dirname(bin)), env }));
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
// Two animation frames: the page has rendered every Msg it has run.
const settled = (page) =>
  page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));

async function scenario(browserName, target, url) {
  const browser = await launch(browserName);
  const tag = `${target}/${browserName}`;
  const page = await browser.newPage();
  const errors = [];
  const rpc = { sent: [], done: new Set() };
  page.on("pageerror", (e) => errors.push(e.message));
  page.on("console", (m) => {
    const where = m.location()?.url || "";
    if (m.type() === "error" && !/favicon/.test(where)) errors.push(m.text());
  });
  page.on("request", (r) => {
    if (r.url().includes("/_rpc/")) rpc.sent.push(r);
  });
  page.on("requestfinished", (r) => rpc.done.add(r));
  page.on("requestfailed", (r) => rpc.done.add(r));
  const pendingSelect = () => rpc.sent.some((r) => r.url().includes("/_rpc/Select") && !rpc.done.has(r));
  try {
    await page.goto(url, { waitUntil: "load" });
    await until(`${tag} first paint`, async () => (await text(page, "selected")) === "SELECTED=0");
    await settled(page);
    const rpcsAtBoot = rpc.sent.length;

    // ---- select: the helper's command runs on the server --------------------
    await page.click("#t1");
    await until(`${tag} the model call`, async () => held.length === 1);
    await until(
      `${tag} the selection while the model call is held`,
      async () => (await text(page, "selected")) === "SELECTED=1" && (await text(page, "busy")) === "BUSY=drafting",
    ).catch(() => {});
    const sel = await text(page, "selected");
    const busy = await text(page, "busy");
    check(`${tag} select: the selection shows while the model call is held`, sel === "SELECTED=1", sel);
    check(`${tag} select: the busy label shows while the model call is held`, busy === "BUSY=drafting", busy);
    if (target === "web:app") {
      check(`${tag} select: /_rpc/Select is still pending`, pendingSelect(), `sent=${rpc.sent.length - rpcsAtBoot}`);
    }
    await page.fill("#type", "hello");
    await until(`${tag} typed text`, async () => (await text(page, "typed")) === "TYPED=hello").catch(() => {});
    const typed = await text(page, "typed");
    check(`${tag} select: typed text applies while the model call is held`, typed === "TYPED=hello", typed);
    check(`${tag} select: the model call is still held`, held.length === 1, `held=${held.length}`);
    release("drafted");
    await until(`${tag} the draft`, async () => (await text(page, "draft")) === "DRAFT=drafted").catch(() => {});
    const draft = await text(page, "draft");
    check(`${tag} select: the draft arrives when the call answers`, draft === "DRAFT=drafted", draft);
    check(`${tag} select: the busy label clears`, (await text(page, "busy")) === "BUSY=-", await text(page, "busy"));

    // ---- saved: the helper's Cmd.none branch sends nothing -------------------
    await settled(page);
    const rpcsBefore = rpc.sent.length;
    const callsBefore = calls;
    await page.click("#t2");
    await until(`${tag} the saved reply`, async () => (await text(page, "draft")) === "DRAFT=saved reply").catch(() => {});
    await settled(page);
    const saved = await text(page, "draft");
    check(`${tag} saved: the saved reply shows`, saved === "DRAFT=saved reply", saved);
    check(`${tag} saved: no /_rpc request`, rpc.sent.length === rpcsBefore, `sent=${rpc.sent.length - rpcsBefore}`);
    check(`${tag} saved: the model is not called`, calls === callsBefore, `calls=${calls - callsBefore}`);

    // ---- hold: the negative control ------------------------------------------
    await page.click("#holdbtn");
    await until(`${tag} the hold call`, async () => held.length === 1);
    let hold;
    if (target === "live") {
      await until(`${tag} the hold write`, async () => (await text(page, "hold")) === "HOLD=held").catch(() => {});
      hold = await text(page, "hold");
      check(`${tag} hold: Sky.Live shows the write while the call is held`, hold === "HOLD=held", hold);
    } else {
      await settled(page);
      hold = await text(page, "hold");
      check(`${tag} hold: web:app shows no write while the held request runs`, hold === "HOLD=-", hold);
    }
    release("ok");
    await until(`${tag} the hold result`, async () => (await text(page, "hold")) === "HOLD=held done").catch(() => {});
    const done = await text(page, "hold");
    check(`${tag} hold: the write and its result land`, done === "HOLD=held done", done);
    check(`${tag} no page errors`, errors.length === 0, errors.join(" | ") || "none");
    return { sel, busy, typed, draft, saved, done };
  } finally {
    while (held.length) release("late");
    await browser.close();
  }
}

let apps = [];
try {
  await new Promise((r) => model.listen(MODEL_PORT, "127.0.0.1", r));
  const results = {};
  for (const [target, bin, port] of [
    ["web:app", SPA, PORT],
    ["live", LIVE, PORT + 1],
  ]) {
    const app = await startApp(bin, port);
    apps.push(app);
    for (const b of BROWSERS) {
      results[`${target}/${b}`] = await scenario(b, target, `http://127.0.0.1:${port}/`);
    }
    app.proc.kill("SIGKILL");
  }
  for (const b of BROWSERS) {
    const s = results[`web:app/${b}`];
    const l = results[`live/${b}`];
    for (const k of ["sel", "busy", "typed", "draft", "saved", "done"]) {
      check(`${b}: ${k} matches Sky.Live`, s[k] === l[k], `web:app "${s[k]}" / live "${l[k]}"`);
    }
  }
  console.log(
    failures.length
      ? `VERDICT=FAIL ${failures.join("; ")}`
      : "VERDICT=PASS an async arm shows its write while its server call runs, as Sky.Live does",
  );
  process.exitCode = failures.length ? 2 : 0;
} catch (e) {
  console.error("HARNESS ERROR:", e.message);
  for (const a of apps) console.error(a.log().slice(-2000));
  process.exitCode = 1;
} finally {
  for (const a of apps) a.proc.kill("SIGKILL");
  model.close();
  process.exit(process.exitCode ?? 1);
}
