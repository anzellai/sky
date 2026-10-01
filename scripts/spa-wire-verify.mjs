#!/usr/bin/env node
// scripts/spa-wire-verify.mjs
//
// Browser e2e for the client half of the Sky.Spa wire handshake
// (runtime-go/rt/spa_wire.go, spa_wire_wasm.go, http_wasm.go). Drives the
// web:app backend of rust/crates/sky/tests/fixtures/spa-rpc-order under
// SKY_CSP=strict, in each browser:
//
//   header  every `/_rpc/` request the v0.27 client sends carries
//           `X-Sky-Wire`, equal to the page's `<meta name="sky-wire">`
//           (if the client stops sending it, every RPC silently takes the
//           legacy path; this check is what goes red)
//   legacy  the server side: the same request replayed WITHOUT the header
//           is answered on the legacy path (200; the server ran the
//           follow-ups inline, so the body carries none: `spaFollow_` is
//           absent or empty, where the current path lists `Tracked`); with a different hash it is refused 409 with
//           `X-Sky-Status: reload`
//   reload  when every `/_rpc/` answer is a 409 + `X-Sky-Status: reload`,
//           the tab reloads exactly once, then shows the "not sent" notice;
//           a second refused RPC inside the 30 s guard does NOT reload again
//           and shows the notice with a Reload button
//
// Usage: node scripts/spa-wire-verify.mjs <web:app-backend> [--port N]
//   SKY_E2E_BROWSERS  comma list of chromium,webkit (default chromium)
//   SKY_E2E_CHANNEL   a Playwright Chromium channel, e.g. "chrome"
//   SKY_E2E_HEADED=1  run the browsers headed
// Exit: 0 PASS · 2 FAIL · 1 harness error.
import pw from "playwright";
import { spawn } from "node:child_process";
import { dirname } from "node:path";
import { guardChild } from "./lib/child-guard.mjs";

function arg(name, def) {
  const i = process.argv.indexOf(name);
  return i >= 0 && process.argv[i + 1] ? process.argv[i + 1] : def;
}
const SPA = process.argv[2];
if (!SPA) {
  console.error("usage: spa-wire-verify.mjs <web:app-backend> [--port N]");
  process.exit(1);
}
const PORT = Number(arg("--port", "9367"));
const BROWSERS = (process.env.SKY_E2E_BROWSERS || "chromium").split(",").map((s) => s.trim()).filter(Boolean);
const HEADED = process.env.SKY_E2E_HEADED === "1";
const CHANNEL = process.env.SKY_E2E_CHANNEL || undefined;

const failures = [];
function check(step, ok, detail) {
  console.log(`${ok ? "ok  " : "FAIL"} ${step}: ${detail}`);
  if (!ok) failures.push(step);
}

async function startApp(bin, port) {
  const env = { ...process.env, PORT: String(port), SKY_LIVE_PORT: String(port), SKY_CSP: "strict" };
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
async function waitFor(page, id, pred, ms) {
  const deadline = Date.now() + ms;
  let v = await text(page, id);
  while (!pred(v) && Date.now() < deadline) {
    await page.waitForTimeout(50);
    v = await text(page, id);
  }
  return v;
}
const isRpc = (url) => new URL(url).pathname.startsWith("/_rpc/");

async function scenario(browserName, url) {
  const browser = await launch(browserName);
  const tag = `web:app/${browserName}`;
  try {
    // ---- header + legacy ---------------------------------------------------
    const ctx = await browser.newContext();
    const page = await ctx.newPage();
    const rpcs = [];
    page.on("request", (r) => {
      if (isRpc(r.url())) rpcs.push(r);
    });
    await page.goto(url, { waitUntil: "load" });
    await page.waitForTimeout(1500);
    const meta = await page.evaluate(() => document.querySelector('meta[name="sky-wire"]')?.content || "");
    check(`${tag} page carries a wire hash`, meta.length > 0, JSON.stringify(meta));

    await page.click("#pairbtn");
    await waitFor(page, "pair", (v) => v === "A=a B=b", 15000);
    await page.click("#addbtn");
    await waitFor(page, "basket", (v) => v.includes("TRACKS=1"), 15000);
    const hdrs = await Promise.all(rpcs.map((r) => r.allHeaders()));
    const wires = hdrs.map((h) => h["x-sky-wire"] || "");
    check(`${tag} the client sent RPCs`, rpcs.length >= 3, `${rpcs.length} /_rpc/ requests`);
    check(
      `${tag} every /_rpc/ request carries X-Sky-Wire = the page's hash`,
      rpcs.length > 0 && wires.every((w) => w === meta),
      JSON.stringify(rpcs.map((r, i) => `${new URL(r.url()).pathname} wire=${wires[i] || "(none)"}`)),
    );

    // Replay the add (it has a server follow-up) from the page, same origin.
    const add = rpcs.find((r) => /add/i.test(new URL(r.url()).pathname)) || rpcs[rpcs.length - 1];
    const addHdrs = { ...(await add.allHeaders()) };
    const body = add.postData() || "";
    const path = new URL(add.url()).pathname;
    const replay = (wire) =>
      page.evaluate(
        async ({ path, body, ct, wire }) => {
          const h = { "Content-Type": ct };
          if (wire !== null) h["X-Sky-Wire"] = wire;
          const r = await fetch(path, { method: "POST", headers: h, body, credentials: "same-origin" });
          return { status: r.status, sky: r.headers.get("X-Sky-Status") || "", body: await r.text() };
        },
        { path, body, ct: addHdrs["content-type"] || "application/json", wire },
      );
    const current = await replay(meta);
    const legacy = await replay(null);
    const other = await replay("0000-not-this-schema");
    check(`${tag} replay with the hash is answered`, current.status === 200, `${current.status} ${current.body.slice(0, 160)}`);
    const follows = (b) => {
      try {
        const f = JSON.parse(b).spaFollow_;
        return f === undefined ? [] : JSON.parse(f);
      } catch {
        return null;
      }
    };
    check(
      `${tag} replay with NO header takes the legacy path (200, no follow-ups sent back)`,
      legacy.status === 200 && Array.isArray(follows(legacy.body)) && follows(legacy.body).length === 0,
      `${legacy.status} ${legacy.body.slice(0, 160)}`,
    );
    check(
      `${tag} the legacy path differs from the current one`,
      (follows(current.body) || []).length > 0,
      `current follow-ups: ${JSON.stringify(follows(current.body))}`,
    );
    check(
      `${tag} replay with another hash is refused 409 + X-Sky-Status: reload`,
      other.status === 409 && other.sky === "reload",
      `${other.status} X-Sky-Status=${other.sky}`,
    );
    await ctx.close();

    // ---- reload guard + notice ---------------------------------------------
    const ctx2 = await browser.newContext();
    const p2 = await ctx2.newPage();
    const errors = [];
    p2.on("pageerror", (e) => errors.push(e.message));
    await p2.goto(url, { waitUntil: "load" });
    await p2.waitForTimeout(1500);
    let refused = 0;
    await p2.route(
      (u) => isRpc(u.href),
      (route) => {
        refused++;
        return route.fulfill({ status: 409, headers: { "X-Sky-Status": "reload" }, body: "" });
      },
    );
    let loads = 0;
    p2.on("load", () => loads++);
    await p2.click("#pairbtn");
    const deadline = Date.now() + 15000;
    while (loads < 1 && Date.now() < deadline) await p2.waitForTimeout(100);
    check(`${tag} a 409 reload reloads the tab`, loads === 1, `${loads} reload(s), ${refused} refused RPC(s)`);
    await p2.waitForTimeout(1500);
    const notice1 = (await p2.locator("#sky-spa-wire").innerText().catch(() => "")).trim();
    check(`${tag} after the reload the "not sent" notice shows`, /not sent/.test(notice1), JSON.stringify(notice1));

    // A second refusal inside the 30 s guard: no reload, a notice with Reload.
    const before = refused;
    await p2.click("#pairbtn");
    const d2 = Date.now() + 10000;
    while (refused === before && Date.now() < d2) await p2.waitForTimeout(100);
    await p2.waitForTimeout(3000);
    check(`${tag} the guard stops a second reload within 30 s`, loads === 1, `${loads} reload(s), ${refused} refused`);
    const notice2 = (await p2.locator("#sky-spa-wire").innerText().catch(() => "")).trim();
    check(
      `${tag} the guarded notice says the action was not sent and offers Reload`,
      /not sent/.test(notice2) && /Reload/.test(notice2),
      JSON.stringify(notice2),
    );
    check(`${tag} no page errors`, errors.length === 0, errors.join(" | ") || "none");
    await ctx2.close();
  } finally {
    await browser.close();
  }
}

let app;
try {
  app = await startApp(SPA, PORT);
  for (const b of BROWSERS) await scenario(b, `http://127.0.0.1:${PORT}/`);
  console.log(failures.length ? `VERDICT=FAIL ${failures.join("; ")}` : "VERDICT=PASS the wire handshake holds in every browser");
  process.exitCode = failures.length ? 2 : 0;
} catch (e) {
  console.error("HARNESS ERROR:", e.message);
  if (app) console.error(app.log().slice(-2000));
  process.exitCode = 1;
} finally {
  if (app) app.proc.kill("SIGKILL");
  process.exit(process.exitCode ?? 1);
}
