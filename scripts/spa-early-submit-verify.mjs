#!/usr/bin/env node
// scripts/spa-early-submit-verify.mjs
//
// Browser e2e: a form submitted before the page is live never ends on the
// CSRF 403 JSON page (v0.29.1). Before the fix, a phone that pressed "Go"
// while the Sky.Spa wasm client hydrated made a native POST to the page's
// own URL, and the server answered
// {"status":"csrf_missing",...} in place of the app.
//
// Drives rust/crates/sky/tests/fixtures/spa-early-submit, built twice
// (--target web:app and --target web), in each browser, under SKY_CSP=strict.
//
// Sky.Spa (web:app):
//   (a) the SSR page carries a `__sky_csrf` form field on a first visit and
//       on a second visit
//   (b) with the wasm held, typed text and Enter are held: no navigation and
//       no 403. Once the wasm is released, the model shows the typed text and
//       `update` ran the Save Msg exactly once
//   (c) with JavaScript off, a native submit answers 303 to the same URL, then
//       200 with the page, never JSON
//   (d) the same with the CSRF cookie cleared: 303
//   (e) an App.api "POST /unsub" route still receives its form body
//   (f) a form POST to /_rpc/<Msg> is still refused with 403, not redirected
//   (g) no "SSR hydrate skipped" warning
//
// Sky.Live (web):
//   (b) with the client script held, Enter ends on the page (303, then 200),
//       never on JSON
//   (c), (d), (e) as above
//
// Usage: node scripts/spa-early-submit-verify.mjs <web:app-backend> <live-app> [--port N]
//   The Sky.Live app listens on N + 1.
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
if (!SPA || !LIVE || SPA.startsWith("--") || LIVE.startsWith("--")) {
  console.error("usage: spa-early-submit-verify.mjs <web:app-backend> <live-app> [--port N]");
  process.exit(1);
}
const PORT = Number(arg("--port", "9393"));
const BROWSERS = (process.env.SKY_E2E_BROWSERS || "chromium").split(",").map((s) => s.trim()).filter(Boolean);
const HEADED = process.env.SKY_E2E_HEADED === "1";
const CHANNEL = process.env.SKY_E2E_CHANNEL || undefined;
const CSRF_FIELD = 'name="__sky_csrf"';

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

const byId = (page, id) => page.locator(`[data-testid="${id}"]`);
async function waitText(page, id, want, ms) {
  const deadline = Date.now() + ms;
  let v = "";
  while (Date.now() < deadline) {
    v = ((await byId(page, id).innerText().catch(() => "")) || "").trim();
    if (v === want) return v;
    await page.waitForTimeout(50);
  }
  return v;
}

// Every main-frame document response: its status and content type.
function trackDocuments(page) {
  const docs = [];
  page.on("response", (r) => {
    if (r.request().resourceType() !== "document" || r.frame() !== page.mainFrame()) return;
    docs.push({ method: r.request().method(), status: r.status(), type: r.headers()["content-type"] || "", url: r.url() });
  });
  return docs;
}

const describe = (docs) => docs.map((d) => `${d.method} ${d.status} ${d.type.split(";")[0]}`).join(" -> ") || "none";

// (c)/(d): JavaScript off, a native submit of the form.
async function nativeSubmit(browser, base, tag, clearCookie) {
  const ctx = await browser.newContext({ javaScriptEnabled: false });
  try {
    const page = await ctx.newPage();
    await page.goto(base + "/form?from=test", { waitUntil: "load" });
    if (clearCookie) await ctx.clearCookies();
    const docs = trackDocuments(page);
    const seen = [];
    page.on("response", (r) => {
      if (r.request().method() === "POST") seen.push({ status: r.status(), location: r.headers()["location"] || "" });
    });
    await byId(page, "name").fill("native");
    await Promise.all([page.waitForURL(/\/form/, { waitUntil: "load" }), byId(page, "go").click()]);
    await page.waitForTimeout(300);
    const post = seen[0] || { status: 0, location: "" };
    const label = clearCookie ? "(d) cookie cleared" : "(c) no JavaScript";
    check(`${tag} ${label}: the POST answers 303 to the same URL`,
      post.status === 303 && post.location === "/form?from=test", `${post.status} ${post.location}`);
    const last = docs[docs.length - 1] || { status: 0, type: "", url: "" };
    check(`${tag} ${label}: the browser ends on the page (200 HTML)`,
      last.status === 200 && last.type.startsWith("text/html") && new URL(last.url).pathname === "/form",
      describe(docs));
    check(`${tag} ${label}: no JSON page`, !docs.some((d) => d.type.startsWith("application/json")), describe(docs));
  } finally {
    await ctx.close();
  }
}

async function requests(browser, base, tag, spa) {
  const ctx = await browser.newContext();
  try {
    const unsub = await ctx.request.post(base + "/unsub", { form: { list: "news" }, maxRedirects: 0 });
    const ubody = await unsub.text();
    check(`${tag} (e) App.api POST with a form body is reached`,
      unsub.status() === 200 && ubody === "unsubscribed:list=news", `${unsub.status()} ${ubody}`);
    if (spa) {
      const rpc = await ctx.request.post(base + "/_rpc/Stamp", { form: { a: "b" }, maxRedirects: 0 });
      check(`${tag} (f) a form POST to /_rpc is still refused`, rpc.status() === 403, String(rpc.status()));
    }
  } finally {
    await ctx.close();
  }
}

async function spaScenario(browserName, base) {
  const browser = await launch(browserName);
  const tag = `spa ${browserName}`;
  try {
    // (a) the CSRF field on a first and a second visit.
    {
      const ctx = await browser.newContext();
      const first = await (await ctx.request.get(base + "/form")).text();
      const second = await (await ctx.request.get(base + "/form")).text();
      check(`${tag} (a) the first visit carries the CSRF field`, first.includes(CSRF_FIELD), String(first.includes(CSRF_FIELD)));
      check(`${tag} (a) a second visit carries the CSRF field`, second.includes(CSRF_FIELD), String(second.includes(CSRF_FIELD)));
      await ctx.close();
    }

    // (b) + (g) a submit made while the wasm client hydrates.
    {
      const ctx = await browser.newContext();
      const page = await ctx.newPage();
      const warnings = [];
      page.on("console", (m) => {
        if (/hydrate skipped/.test(m.text())) warnings.push(m.text());
      });
      let release;
      const held = new Promise((r) => (release = r));
      await page.route(/\.wasm(\?|$)/, async (route) => {
        await held;
        await route.continue();
      });
      await page.goto(base + "/form", { waitUntil: "load" });
      const docs = trackDocuments(page);
      const hydrating = await page.evaluate(() => document.documentElement.hasAttribute("data-sky-hydrating"));
      check(`${tag} (b) the page is still hydrating`, hydrating, String(hydrating));
      await byId(page, "name").fill("early");
      await byId(page, "name").press("Enter");
      await page.waitForTimeout(800);
      check(`${tag} (b) Enter before hydration does not navigate`, docs.length === 0, describe(docs));
      release();
      const saves = await waitText(page, "saves", "saves:1", 15000);
      check(`${tag} (b) the held submit ran update once`, saves === "saves:1", saves);
      const saved = await waitText(page, "saved", "saved:early", 5000);
      check(`${tag} (b) the submit carried the typed text`, saved === "saved:early", saved);
      const draft = await waitText(page, "draft", "draft:early", 5000);
      check(`${tag} (b) the typed text reached the model`, draft === "draft:early", draft);
      const value = await byId(page, "name").inputValue({ timeout: 2000 }).catch(() => "<no field>");
      check(`${tag} (b) the field keeps the typed text`, value === "early", value);
      await page.waitForTimeout(800);
      const again = ((await byId(page, "saves").innerText({ timeout: 2000 }).catch(() => "")) || "").trim();
      check(`${tag} (b) the submit ran exactly once`, again === "saves:1", again);
      check(`${tag} (b) no page navigation at all`, docs.length === 0, describe(docs));
      check(`${tag} (g) no hydrate-skipped warning`, warnings.length === 0, warnings.join(" | ") || "none");
      await ctx.close();
    }

    await nativeSubmit(browser, base, tag, false);
    await nativeSubmit(browser, base, tag, true);
    await requests(browser, base, tag, true);
  } finally {
    await browser.close();
  }
}

async function liveScenario(browserName, base) {
  const browser = await launch(browserName);
  const tag = `live ${browserName}`;
  try {
    // (b) the client script held: Enter is a native POST, which must end on
    // the page, not on JSON.
    {
      const ctx = await browser.newContext();
      const page = await ctx.newPage();
      let release;
      const held = new Promise((r) => (release = r));
      await page.route(/\/_sky\/live[^/]*\.js(\?|$)/, async (route) => {
        await held;
        await route.continue();
      });
      // The client script is a parser-blocking script at the end of <body>:
      // held, the document never finishes loading, but the form above it is
      // already parsed and shown, as on a slow phone.
      await page.goto(base + "/form", { waitUntil: "commit" });
      await byId(page, "name").waitFor({ timeout: 10000 });
      const docs = trackDocuments(page);
      await byId(page, "name").fill("early");
      await Promise.all([
        page.waitForResponse((r) => r.request().resourceType() === "document" && r.request().method() === "GET", { timeout: 10000 }).catch(() => null),
        byId(page, "name").press("Enter"),
      ]);
      release();
      await page.waitForTimeout(500);
      const post = docs.find((d) => d.method === "POST") || { status: 0 };
      check(`${tag} (b) an early Enter answers 303`, post.status === 303, describe(docs));
      const last = docs[docs.length - 1] || { status: 0, type: "" };
      check(`${tag} (b) the browser ends on the page`, last.status === 200 && last.type.startsWith("text/html"), describe(docs));
      check(`${tag} (b) no JSON page`, !docs.some((d) => d.type.startsWith("application/json")), describe(docs));
      await ctx.close();
    }
    await nativeSubmit(browser, base, tag, false);
    await nativeSubmit(browser, base, tag, true);
    await requests(browser, base, tag, false);
  } finally {
    await browser.close();
  }
}

let spa;
let live;
try {
  spa = await startApp(SPA, PORT);
  live = await startApp(LIVE, PORT + 1);
  for (const b of BROWSERS) {
    await spaScenario(b, `http://127.0.0.1:${PORT}`);
    await liveScenario(b, `http://127.0.0.1:${PORT + 1}`);
  }
  console.log(
    failures.length
      ? `VERDICT=FAIL ${failures.join("; ")}`
      : "VERDICT=PASS an early or script-less form submit is held, replayed or redirected, never a 403 JSON page",
  );
  process.exitCode = failures.length ? 2 : 0;
} catch (e) {
  console.error("HARNESS ERROR:", e.message);
  if (spa) console.error(spa.log().slice(-2000));
  if (live) console.error(live.log().slice(-2000));
  process.exitCode = 1;
} finally {
  if (spa) spa.proc.kill("SIGKILL");
  if (live) live.proc.kill("SIGKILL");
  process.exit(process.exitCode ?? 1);
}
