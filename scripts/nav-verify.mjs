#!/usr/bin/env node
// scripts/nav-verify.mjs
//
// Browser e2e for Std.Nav (runtime-go/rt/nav.go). Drives the fixture
// rust/crates/sky/tests/fixtures/nav-cmds on BOTH targets (the web:app split
// and Sky.Live), in each browser, under SKY_CSP=strict. See scripts/nav-e2e.sh
// for the scenarios. A page marker set after load must survive every step:
// no step reloads the page.
//
// Usage: node scripts/nav-verify.mjs <web:app-backend> <live-app> [--port N]
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
const LIVE = process.argv[3];
if (!SPA || !LIVE) {
  console.error("usage: nav-verify.mjs <web:app-backend> <live-app> [--port N]");
  process.exit(1);
}
const PORT = Number(arg("--port", "9381"));
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
async function waitUrl(page, pred, ms) {
  const deadline = Date.now() + ms;
  let u = await page.evaluate(() => location.pathname + location.search + location.hash);
  while (!pred(u) && Date.now() < deadline) {
    await page.waitForTimeout(50);
    u = await page.evaluate(() => location.pathname + location.search + location.hash);
  }
  return u;
}
const field = (s, k) => (s.match(new RegExp(k + "=(\\S*)")) || [])[1];
const loc = (page) => page.evaluate(() => ({ href: location.href, path: location.pathname + location.search + location.hash, len: history.length, marker: window.__navMarker === 1 }));

async function scenario(browserName, target, url, app) {
  const browser = await launch(browserName);
  const tag = `${target}/${browserName}`;
  const page = await browser.newPage();
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  page.on("console", (m) => {
    if (m.type() !== "error") return;
    if (/favicon/.test(m.location()?.url || "")) return;
    // The refusal the `evil` step expects on the Sky.Spa client.
    if (/NavRejectedUrl/.test(m.text())) return;
    errors.push(m.text());
  });
  const out = {};
  try {
    await page.goto(url, { waitUntil: "load" });
    await page.waitForTimeout(1500);
    await page.evaluate(() => { window.__navMarker = 1; });
    const s0 = await state(page);
    const l0 = await loc(page);
    out.load = s0;

    // ---- go: pushUrl to another path --------------------------------------
    await page.click("#go");
    const u1 = await waitUrl(page, (u) => u === "/about?x=1", 10000);
    const s1 = await waitState(page, (v) => v.includes("PAGE=about"), 10000);
    const l1 = await loc(page);
    check(`${tag} go: the URL moves`, u1 === "/about?x=1", u1);
    check(`${tag} go: the route runs`, field(s1, "PAGE") === "about", s1);
    check(`${tag} go: onNavigate runs once`, +field(s1, "NAVS") === +field(s0, "NAVS") + 1, `${s0} -> ${s1}`);
    check(`${tag} go: one new history entry`, l1.len === l0.len + 1, `${l0.len} -> ${l1.len}`);
    check(`${tag} go: no reload`, l1.marker, String(l1.marker));
    out.go = s1;

    // ---- Back returns to the page before ----------------------------------
    await page.goBack();
    const ub = await waitUrl(page, (u) => u === "/", 10000);
    const sb = await waitState(page, (v) => v.includes("PAGE=home"), 10000);
    check(`${tag} back: returns to /`, ub === "/" && field(sb, "PAGE") === "home", `${ub} ${sb}`);
    await page.goForward();
    await waitState(page, (v) => v.includes("PAGE=about"), 10000);

    // ---- home: replaceUrl ---------------------------------------------------
    const lh0 = await loc(page);
    await page.click("#home");
    const uh = await waitUrl(page, (u) => u === "/", 10000);
    const sh = await waitState(page, (v) => v.includes("PAGE=home"), 10000);
    const lh = await loc(page);
    check(`${tag} home: the URL is replaced`, uh === "/" && field(sh, "PAGE") === "home", `${uh} ${sh}`);
    check(`${tag} home: no new history entry`, lh.len === lh0.len, `${lh0.len} -> ${lh.len}`);
    check(`${tag} home: no reload`, lh.marker, String(lh.marker));

    // ---- frag: pushUrl "#sec" ----------------------------------------------
    const lf0 = await loc(page);
    const sf0 = await state(page);
    await page.click("#frag");
    const uf = await waitUrl(page, (u) => u === "/#sec", 10000);
    const sf = await waitState(page, (v) => v.includes("FRAG=sec"), 10000);
    const lf = await loc(page);
    check(`${tag} frag: the fragment moves`, uf === "/#sec", uf);
    check(`${tag} frag: Sub.onFragment receives it`, field(sf, "FRAG") === "sec", sf);
    check(`${tag} frag: no page moves`, field(sf, "NAVS") === field(sf0, "NAVS") && field(sf, "PAGE") === "home", `${sf0} -> ${sf}`);
    check(`${tag} frag: one new history entry`, lf.len === lf0.len + 1, `${lf0.len} -> ${lf.len}`);

    // ---- clear: clearFragment ------------------------------------------------
    await page.click("#clear");
    const uc = await waitUrl(page, (u) => u === "/", 10000);
    const sc = await waitState(page, (v) => /FRAG= /.test(v), 10000);
    const lc = await loc(page);
    check(`${tag} clear: the fragment leaves the address bar`, uc === "/" && !lc.href.includes("#"), lc.href);
    check(`${tag} clear: Sub.onFragment receives ""`, /FRAG= /.test(sc), sc);
    check(`${tag} clear: no new history entry`, lc.len === lf.len, `${lf.len} -> ${lc.len}`);
    check(`${tag} clear: no reload`, lc.marker, String(lc.marker));

    // ---- save: a server arm's navigation ------------------------------------
    await page.click("#save");
    const us = await waitUrl(page, (u) => u === "/about", 10000);
    const ss = await waitState(page, (v) => v.includes("SAVED=7") && v.includes("PAGE=about"), 10000);
    check(`${tag} save: the navigation runs`, us === "/about" && field(ss, "PAGE") === "about", `${us} ${ss}`);
    check(`${tag} save: the server result arrives`, field(ss, "SAVED") === "7", ss);
    out.save = ss;

    // ---- evil: a URL off the site is refused ---------------------------------
    const le0 = await loc(page);
    await page.click("#evil");
    await page.waitForTimeout(1500);
    const le = await loc(page);
    check(`${tag} evil: the address bar does not move`, le.href === le0.href && le.len === le0.len && le.marker, `${le0.href} -> ${le.href}`);
    if (target === "live") {
      check(`${tag} evil: the refusal is logged`, /NavRejectedUrl/.test(app.log()), "server log");
    }
    check(`${tag} no page errors`, errors.length === 0, errors.join(" | ") || "none");
    return out;
  } finally {
    await browser.close();
  }
}

// ---- plain: a page with no Sub.onFragment, opened at a URL with a fragment --
// The client reports the fragment at load. With no subscriber the report is a
// no-op: the page keeps its first paint (it used to be patched with an empty
// body, which blanked the page).
async function plainScenario(browserName, target, port) {
  const browser = await launch(browserName);
  const tag = `${target}/${browserName}`;
  const page = await browser.newPage();
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  try {
    await page.goto(`http://127.0.0.1:${port}/plain#x`, { waitUntil: "load" });
    await page.waitForTimeout(2000);
    const n = await page.locator("#state").count();
    const s = n > 0 ? await state(page) : "";
    const btns = await page.locator("#go").count();
    check(`${tag} plain#x: the page is not blanked`, n === 1 && btns === 1, `#state=${n} #go=${btns}`);
    check(`${tag} plain#x: the route ran, no fragment delivered`, field(s, "PAGE") === "plain" && /FRAG= /.test(s), s || "(empty)");
    check(`${tag} plain#x: no page errors`, errors.length === 0, errors.join(" | ") || "none");
  } finally {
    await browser.close();
  }
}

let apps = [];
try {
  const results = {};
  for (const [target, bin, port] of [
    ["web:app", SPA, PORT],
    ["live", LIVE, PORT + 1],
  ]) {
    const app = await startApp(bin, port);
    apps.push(app);
    for (const b of BROWSERS) {
      results[`${target}/${b}`] = await scenario(b, target, `http://127.0.0.1:${port}/`, app);
      await plainScenario(b, target, port);
    }
    app.proc.kill("SIGKILL");
  }
  for (const b of BROWSERS) {
    const s = results[`web:app/${b}`];
    const l = results[`live/${b}`];
    for (const k of ["go", "save"]) {
      const sp = s[k].replace(/NAVS=\d+ /, "");
      const lp = l[k].replace(/NAVS=\d+ /, "");
      check(`${b}: ${k} matches Sky.Live`, sp === lp, `web:app "${s[k]}" / live "${l[k]}"`);
    }
  }
  console.log(failures.length ? `VERDICT=FAIL ${failures.join("; ")}` : "VERDICT=PASS Std.Nav behaves the same on web:app and Sky.Live");
  process.exitCode = failures.length ? 2 : 0;
} catch (e) {
  console.error("HARNESS ERROR:", e.message);
  for (const a of apps) console.error(a.log().slice(-2000));
  process.exitCode = 1;
} finally {
  for (const a of apps) a.proc.kill("SIGKILL");
  process.exit(process.exitCode ?? 1);
}
