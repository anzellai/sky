#!/usr/bin/env node
// scripts/session-revocation-verify.mjs
//
// Browser e2e for the Sky.Spa sign-out (runtime-go/rt/spa_session_revocation.go,
// docs/skyspa/auto-split.md §25). Drives the web:app backend built from
// rust/crates/sky/tests/fixtures/spa-session-revocation, in each browser:
//
//   control   a copy of a signed-in cookie, put in a second browser context:
//             an admin RPC sent from that context's page (a same-origin fetch,
//             the browser attaches the cookie) and one sent directly both run
//             as the user, so the replay method works
//   signout   sign in, copy the cookie, click "Sign out" (the client clears its
//             session and calls /_rpc/__spaSignOut); the copy, replayed in a
//             second context the same two ways, does not run as the user
//   server    sign in, copy the cookie, click "Server sign out" (a server
//             branch clears the session); the copy is refused the same way
//   again     a fresh sign-in after the sign-outs works
//
// Usage: node scripts/session-revocation-verify.mjs <web:app-backend> [--port N]
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
if (!SPA) {
  console.error("usage: session-revocation-verify.mjs <web:app-backend> [--port N]");
  process.exit(1);
}
const PORT = Number(arg("--port", "9391"));
const BROWSERS = (process.env.SKY_E2E_BROWSERS || "chromium").split(",").map((s) => s.trim()).filter(Boolean);
const HEADED = process.env.SKY_E2E_HEADED === "1";
const CHANNEL = process.env.SKY_E2E_CHANNEL || undefined;
const BACKEND_DIR = dirname(dirname(SPA));
const URL = `http://127.0.0.1:${PORT}/`;

const failures = [];
function check(step, ok, detail) {
  console.log(`${ok ? "ok  " : "FAIL"} ${step}: ${detail}`);
  if (!ok) failures.push(step);
}

async function startApp(bin, port) {
  const env = { ...process.env, PORT: String(port), SKY_CSP: "strict" };
  delete env.SKY_LIVE_STORE;
  delete env.SKY_LIVE_STORE_PATH;
  const proc = guardChild(spawn(bin, [], { cwd: BACKEND_DIR, env }));
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

const state = async (page) => (await page.locator("#state").innerText()).trim();
async function waitState(page, pred, ms) {
  const deadline = Date.now() + ms;
  let v = await state(page);
  while (!pred(v) && Date.now() < deadline) {
    await page.waitForTimeout(50);
    v = await state(page);
  }
  return v;
}
const adminFile = () => {
  try {
    return readFileSync(join(BACKEND_DIR, "admin.txt"), "utf8");
  } catch (_) {
    return "";
  }
};
async function sidCookie(ctx) {
  // v0.27.0: a Sky.Spa backend keeps its session in `sky_spa` (A-2b).
  const c = (await ctx.cookies(URL)).find((k) => k.name === "sky_spa" || k.name === "__Host-sky_spa");
  return c ? c.value : "";
}

const clearAdmin = () => rmSync(join(BACKEND_DIR, "admin.txt"), { force: true });

// A second browser (the holder of a copied cookie): a fresh context with only
// that cookie. Its page sends an admin RPC as a same-origin fetch (the browser
// attaches the cookie, as it would for the app's own client), and the context
// sends one directly. `pageRan` / `rpcRan`: whether each ran the admin effect
// (wrote admin.txt). The page's own Save button is not used: the signed-out
// client model runs the `Nothing` arm locally and sends no RPC.
async function replay(browser, value, content) {
  const ctx = await browser.newContext();
  try {
    await ctx.addCookies([{ name: "sky_spa", value, url: URL }]);
    const page = await ctx.newPage();
    await page.goto(URL, { waitUntil: "load" });
    await waitState(page, (v) => v.startsWith("signed"), 15000);
    clearAdmin();
    const fromPage = await page.evaluate(async (c) => {
      const r = await fetch("/_rpc/SaveAdmin", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ session: null, content: c, note: "" }),
      });
      return { status: r.status, body: await r.text() };
    }, `${content}-page`);
    const pageRan = adminFile() === `${content}-page`;
    clearAdmin();
    const r = await ctx.request.post(`${URL}_rpc/SaveAdmin`, {
      headers: { "Content-Type": "application/json" },
      data: JSON.stringify({ session: null, content, note: "" }),
    });
    const body = await r.text();
    return { shown: fromPage.body, pageStatus: fromPage.status, pageRan, status: r.status(), body, rpcRan: adminFile() === content };
  } finally {
    await ctx.close();
  }
}

async function scenario(browserName) {
  const browser = await launch(browserName);
  const tag = browserName + (CHANNEL && browserName === "chromium" ? `(${CHANNEL})` : "");
  const ctx = await browser.newContext();
  const page = await ctx.newPage();
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  try {
    await page.goto(URL, { waitUntil: "load" });
    const s0 = await waitState(page, (v) => v.startsWith("signed out"), 15000);
    check(`${tag} load: signed out`, s0.startsWith("signed out"), s0);

    // ---- control: a copy of a live cookie works in another context ---------
    await page.click("#signin");
    const s1 = await waitState(page, (v) => v.startsWith("signed in as u1"), 15000);
    check(`${tag} sign in`, s1.startsWith("signed in as u1"), s1);
    const live = await sidCookie(ctx);
    check(`${tag} sign in sets sky_spa`, live !== "", live ? "set" : "missing");
    const ctl = await replay(browser, live, `control-${browserName}`);
    check(`${tag} control: an RPC from the copy's page runs as the user`, ctl.pageStatus === 200 && ctl.pageRan, `${ctl.pageStatus} ${ctl.shown}`);
    check(`${tag} control: a direct RPC with the copy runs as the user`, ctl.status === 200 && ctl.rpcRan, `${ctl.status} ${ctl.body}`);

    // ---- signout: the client-side sign-out ends the session ----------------
    await page.click("#save");
    await waitState(page, (v) => v.includes("note: saved"), 15000);
    const copied = await sidCookie(ctx);
    const signedOut = page.waitForResponse((r) => r.url().endsWith("/_rpc/__spaSignOut"), { timeout: 15000 });
    await page.click("#signout");
    const so = await signedOut;
    const s2 = await waitState(page, (v) => v.startsWith("signed out"), 15000);
    check(`${tag} signout: the page is signed out`, s2.startsWith("signed out"), s2);
    check(`${tag} signout: __spaSignOut answers 200`, so.status() === 200, String(so.status()));
    check(`${tag} signout: the cookie is gone from the browser`, (await sidCookie(ctx)) === "", "cleared");
    const r1 = await replay(browser, copied, `replay-${browserName}`);
    check(`${tag} signout: an RPC from the pre-sign-out copy's page does not run as the user`, r1.pageStatus === 200 && !r1.pageRan, `${r1.pageStatus} ${r1.shown}`);
    check(`${tag} signout: a direct RPC with the copy does not run as the user`, r1.status === 200 && !r1.rpcRan, `${r1.status} ${r1.body}`);

    // ---- server: a server branch that clears the session -------------------
    await page.click("#signin");
    await waitState(page, (v) => v.startsWith("signed in as u1"), 15000);
    const copied2 = await sidCookie(ctx);
    await page.click("#serversignout");
    const s3 = await waitState(page, (v) => v.startsWith("signed out"), 15000);
    check(`${tag} server sign-out: the page is signed out`, s3.startsWith("signed out"), s3);
    const r2 = await replay(browser, copied2, `replay2-${browserName}`);
    check(`${tag} server sign-out: an RPC from the copy's page does not run as the user`, r2.pageStatus === 200 && !r2.pageRan, `${r2.pageStatus} ${r2.shown}`);
    check(`${tag} server sign-out: a direct RPC with the copy does not run as the user`, r2.status === 200 && !r2.rpcRan, `${r2.status} ${r2.body}`);

    // ---- again: a fresh sign-in works --------------------------------------
    await page.click("#signin");
    const s4 = await waitState(page, (v) => v.startsWith("signed in as u1"), 15000);
    check(`${tag} again: a fresh sign-in works`, s4.startsWith("signed in as u1"), s4);
    clearAdmin();
    await page.click("#save");
    const s5 = await waitState(page, (v) => v.includes("note: saved"), 15000);
    check(`${tag} again: the admin RPC runs`, s5.includes("note: saved") && adminFile() === "saved", s5);

    check(`${tag} no page errors`, errors.length === 0, errors.join(" | ") || "none");
  } finally {
    await browser.close();
  }
}

let app;
try {
  app = await startApp(SPA, PORT);
  for (const b of BROWSERS) await scenario(b);
} catch (e) {
  console.error(e);
  if (app) console.error(app.log());
  process.exit(1);
} finally {
  if (app) app.proc.kill("SIGKILL");
}
if (failures.length) {
  console.error(`session-revocation-verify: FAIL (${failures.length}): ${failures.join(", ")}`);
  process.exit(2);
}
console.log("session-revocation-verify: PASS");
