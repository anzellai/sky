#!/usr/bin/env node
// scripts/spa-click-routing-verify.mjs
//
// Browser e2e for the Sky.Spa (web:app) client's click dispatch and link
// router, and for per-visitor analytics state on the Spa backend. Drives the
// fixture rust/crates/sky/tests/fixtures/spa-click-routing:
//
//   one-click  a click on a card runs the card's Msg once. The card's Msg
//              renders the post page INSIDE the click's dispatch; the click
//              then bubbles through nodes the patch re-bound, and before the
//              fix a second element's Msg ran too (the post page's "Back", or
//              the header logo), so the page moved again. Checked with a DOM
//              click (`el.click()`, only a `click` event) and a real mouse click.
//   nested     an inner and an outer handler both run for one click, as on
//              Sky.Live and in Elm, and the outer one carries the Msg of the
//              view the click was delivered to (outer:0, not outer:1).
//   links      a link to an `App.api` route, to a path with no client route and
//              to /_sky/ is a full browser navigation (the server answers it);
//              a link to a client route stays in-app.
//   analytics  events from two visitors carry two anonymous ids, and one
//              visitor's `setConsent Denied` drops only that visitor's events.
//
// Usage: node scripts/spa-click-routing-verify.mjs <web:app-backend> [--port N]
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
const APP = process.argv[2];
if (!APP) {
  console.error("usage: spa-click-routing-verify.mjs <web:app-backend> [--port N]");
  process.exit(1);
}
const PORT = Number(arg("--port", "9365"));
const BROWSERS = (process.env.SKY_E2E_BROWSERS || "chromium").split(",").map((s) => s.trim()).filter(Boolean);
const HEADED = process.env.SKY_E2E_HEADED === "1";
const CHANNEL = process.env.SKY_E2E_CHANNEL || undefined;
const BASE = `http://127.0.0.1:${PORT}`;

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

const byTest = (id) => `[data-testid="${id}"]`;
const read = async (page, id) => (await page.locator(byTest(id)).innerText()).trim();

async function open(page, path) {
  await page.goto(BASE + path, { waitUntil: "load" });
  await page.waitForFunction(
    () => !document.documentElement.hasAttribute("data-sky-hydrating") && !!document.querySelector("#app [sky-id]"),
    null,
    { timeout: 30000 },
  );
  // A marker that only survives an in-app navigation (a full load drops it).
  await page.evaluate(() => {
    window.__skyE2eSamePage = true;
  });
}

async function settle(page) {
  await page.waitForTimeout(600);
}

const samePage = (page) => page.evaluate(() => window.__skyE2eSamePage === true).catch(() => false);
const pathname = (page) => new URL(page.url()).pathname;

async function oneClick(browser, tag) {
  const ctx = await browser.newContext();
  const page = await ctx.newPage();
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  try {
    // DOM click: only a `click` event, as the audit's `element.click()` probe.
    await open(page, "/list");
    await page.locator(byTest("card-more-a")).evaluate((e) => e.click());
    await settle(page);
    let log = await read(page, "log");
    check(`${tag} one-click: DOM click on a card runs one Msg`, log === "nav:post-a", `log=${log}`);
    check(`${tag} one-click: DOM click lands on the post`, (await read(page, "page")) === "post-a", await read(page, "page"));

    // Real mouse click on the "Back" element, then on the other card.
    await page.locator(byTest("back")).click();
    await settle(page);
    log = await read(page, "log");
    check(`${tag} one-click: Back runs one Msg`, log === "nav:post-a,nav:list", `log=${log}`);
    await page.locator(byTest("card-b")).click();
    await settle(page);
    log = await read(page, "log");
    check(`${tag} one-click: mouse click on a card runs one Msg`, log === "nav:post-a,nav:list,nav:post-b", `log=${log}`);
    check(`${tag} one-click: mouse click lands on the post`, (await read(page, "page")) === "post-b", await read(page, "page"));
    check(`${tag} one-click: in-app throughout`, await samePage(page), "no full reload");

    // Nested handlers: both run, the outer one with the Msg it was bound to.
    await open(page, "/");
    await page.locator(byTest("inner")).click();
    await settle(page);
    log = await read(page, "log");
    check(`${tag} nested: inner then outer, outer from the clicked view`, log.endsWith("inner,outer:0") && !log.includes("outer:1"), `log=${log}`);
    check(`${tag} no page errors`, errors.length === 0, errors.join(" | ") || "none");
  } finally {
    await ctx.close();
  }
}

async function links(browser, tag) {
  const ctx = await browser.newContext();
  const page = await ctx.newPage();
  try {
    await open(page, "/");
    await page.locator(byTest("client-link")).click();
    await settle(page);
    check(`${tag} links: a client route stays in-app`, (await samePage(page)) && (await read(page, "page")) === "list", `${pathname(page)} same=${await samePage(page)}`);

    await open(page, "/");
    await Promise.all([page.waitForURL(BASE + "/server/hello", { timeout: 10000 }).catch(() => {}), page.locator(byTest("server-link")).click()]);
    await settle(page);
    const served = await page.evaluate(() => document.body.innerText).catch(() => "");
    check(`${tag} links: an App.api route is served by the server`, served.includes("served-by-the-server") && !(await samePage(page)), `${pathname(page)} body=${served.slice(0, 60)}`);

    await open(page, "/");
    await Promise.all([page.waitForURL(BASE + "/post/export", { timeout: 10000 }).catch(() => {}), page.locator(byTest("overlap-link")).click()]);
    await settle(page);
    const exported = await page.evaluate(() => document.body.innerText).catch(() => "");
    check(`${tag} links: an App.api path a client route also matches is served by the server`, exported.includes("export-served-by-the-server") && !(await samePage(page)), `${pathname(page)} body=${exported.slice(0, 60)}`);

    await open(page, "/");
    await page.locator(byTest("unrouted-link")).click();
    await page.waitForLoadState("load");
    await settle(page);
    check(`${tag} links: a path with no client route is a full navigation`, !(await samePage(page)) && pathname(page) === "/no/client/route", `${pathname(page)} same=${await samePage(page)}`);

    await open(page, "/");
    await page.locator(byTest("sky-link")).click();
    await page.waitForLoadState("load");
    await settle(page);
    check(`${tag} links: a /_sky/ path is a full navigation`, !(await samePage(page)) && pathname(page) === "/_sky/healthz", `${pathname(page)} same=${await samePage(page)}`);
  } finally {
    await ctx.close();
  }
}

function analyticsEvents(log) {
  const out = [];
  for (const line of log.split("\n")) {
    const i = line.indexOf("[analytics] ");
    if (i < 0) continue;
    try {
      const ev = JSON.parse(line.slice(i + "[analytics] ".length));
      if (ev.event === "fixture_click") out.push(ev);
    } catch (_) {
      // not an event line
    }
  }
  return out;
}

async function analytics(browser, tag, app) {
  const a = await browser.newContext();
  const b = await browser.newContext();
  const pa = await a.newPage();
  const pb = await b.newPage();
  const track = async (page, n) => {
    await page.bringToFront();
    await page.locator(byTest("track")).click();
    await page.waitForFunction(
      ([sel, want]) => (document.querySelector(sel)?.innerText || "").trim().split(",").filter((x) => x === "tracked").length >= want,
      [byTest("log"), n],
      { timeout: 10000 },
    );
  };
  try {
    await open(pa, "/");
    await open(pb, "/");
    const before = analyticsEvents(app.log()).length;
    await track(pa, 1);
    await track(pa, 2);
    await track(pb, 1);
    await new Promise((r) => setTimeout(r, 300));
    const evs = analyticsEvents(app.log()).slice(before);
    check(`${tag} analytics: three events captured`, evs.length === 3, `${evs.length}`);
    if (evs.length === 3) {
      check(`${tag} analytics: one visitor keeps one anonymous id`, evs[0].anonymous_id === evs[1].anonymous_id, `${evs[0].anonymous_id} ${evs[1].anonymous_id}`);
      check(`${tag} analytics: two visitors get two anonymous ids`, evs[0].anonymous_id !== evs[2].anonymous_id, `${evs[0].anonymous_id} vs ${evs[2].anonymous_id}`);
    }
    // Visitor A denies consent: A's events stop, B's continue.
    await pa.bringToFront();
    await pa.locator(byTest("deny")).click();
    await pa.waitForFunction(
      (sel) => (document.querySelector(sel)?.innerText || "").trim().split(",").filter((x) => x === "tracked").length >= 3,
      byTest("log"),
      { timeout: 10000 },
    );
    const mid = analyticsEvents(app.log()).length;
    await track(pa, 4);
    await track(pb, 2);
    await new Promise((r) => setTimeout(r, 300));
    const after = analyticsEvents(app.log()).slice(mid);
    check(
      `${tag} analytics: consent is per visitor`,
      after.length === 1 && evs.length === 3 && after[0].anonymous_id === evs[2].anonymous_id,
      `after deny: ${after.map((e) => e.anonymous_id).join(",") || "none"}`,
    );
  } finally {
    await a.close();
    await b.close();
  }
}

let app;
try {
  app = await startApp(APP, PORT);
  for (const name of BROWSERS) {
    const browser = await launch(name);
    const tag = CHANNEL && name === "chromium" ? CHANNEL : name;
    try {
      await oneClick(browser, tag);
      await links(browser, tag);
      await analytics(browser, tag, app);
    } finally {
      await browser.close();
    }
  }
} catch (e) {
  console.error("spa-click-routing-verify: harness error:", e);
  if (app) console.error(app.log().slice(-4000));
  process.exit(1);
} finally {
  if (app) app.proc.kill("SIGTERM");
}
if (failures.length) {
  console.error(`spa-click-routing-verify: FAIL (${failures.length}): ${failures.join("; ")}`);
  process.exit(2);
}
console.log("spa-click-routing-verify: PASS");
process.exit(0);
