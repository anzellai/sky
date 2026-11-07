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
import { dirname, resolve, relative } from "node:path";
import { writeFile, unlink, lstat, readFile } from "node:fs/promises";
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

const ownedFiles = [];
async function startApp(bin, port) {
  const fixtureRoot = resolve(arg('--fixture-root', ''));
  const cwd = resolve(dirname(dirname(bin)));
  const known = ['.skyapp/web-app/.split/backend/sky-out/app', '.skyapp/web/sky-out/app'];
  if (!known.some(p => resolve(fixtureRoot, p) === resolve(bin))) throw new Error('UnexpectedFixtureBinary');
  if (!arg('--fixture-root', '') || relative(fixtureRoot, cwd).startsWith('..') || !relative(fixtureRoot, cwd)) throw new Error('InvalidOwnedFixtureRoot');
  for (const [name, value] of [['nav-scroll-target.txt', 'part-'], ['nav-scroll-rpc.txt', 'rpc-target']]) {
    const path = resolve(cwd, name);
    await writeFile(path, value, {flag:'wx', mode:0o600});
    const stat = await lstat(path);
    ownedFiles.push({path, value, ino:stat.ino, dev:stat.dev});
  }
  const env = { PATH: "/usr/bin:/bin", LANG: "C", PORT: String(port), SKY_LIVE_PORT: String(port), SKY_CSP: "strict" };
  const proc = guardChild(spawn(bin, [], { cwd: dirname(dirname(bin)), env }));
  let log = "";
  proc.stdout.on("data", (d) => (log += d));
  proc.stderr.on("data", (d) => (log += d));
  const deadline = Date.now() + 30000;
  while (!/listening/i.test(log)) {
    if (Date.now() > deadline || proc.exitCode !== null) throw new Error("FixtureDidNotListen");
    await new Promise((r) => setTimeout(r, 100));
  }
  return { proc, log: () => log };
}

async function stopApp(app) {
  if (app.proc.exitCode !== null || app.proc.signalCode !== null) return;
  const exited = new Promise(resolve => app.proc.once('exit', resolve));
  app.proc.kill('SIGTERM');
  let timer;
  const completed = await Promise.race([exited.then(() => true), new Promise(resolve => { timer = setTimeout(() => resolve(false), 5000); })]);
  clearTimeout(timer);
  if (!completed) { app.proc.kill('SIGKILL'); await exited; }
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

async function installScrollWitness(context) {
  await context.addInitScript(() => {
          const traces = [];
          Object.defineProperty(window, '__navScrollTraces', { value: traces });
          let hydrated;
          Object.defineProperty(window, '__skyHydrated', {
            configurable: true,
            get() { return hydrated; },
            set(fn) {
              hydrated = function() {
                window.__navHydrated = true;
                return fn.apply(this, arguments);
              };
            },
          });
          let implementation;
          Object.defineProperty(window, '__skyScrollApply', {
            configurable: true,
            get() { return implementation; },
            set(fn) {
              implementation = function(command) {
                const snapshot = () => {
                  const sheet = document.getElementById('sheet');
                  const outer = document.getElementById('outer');
                  const node = document.getElementById(command?.target);
                  const rect = node?.getBoundingClientRect();
                  const sr = sheet?.getBoundingClientRect();
                  return {
                    x: scrollX, y: scrollY, bodyX: document.body?.scrollLeft, bodyY: document.body?.scrollTop,
                    sheetX: sheet?.scrollLeft, sheetY: sheet?.scrollTop,
                    outerX: outer?.scrollLeft, outerY: outer?.scrollTop,
                    targetTop: rect?.top ?? null,
                    portTop: sr ? sr.top + sheet.clientTop : null,
                    portHeight: sheet?.clientHeight,
                    state: document.getElementById('state')?.textContent ?? '',
                  };
                };
                if (typeof window.__navScrollSetup === 'function') { const setup = window.__navScrollSetup; window.__navScrollSetup = null; setup(); }
                const before = snapshot();
                const result = fn.apply(this, arguments);
                if (traces.length >= 128) throw new Error('ScrollTraceOverflow');
                traces.push({ target: command?.target ?? null, top: command?.top === true, before, after: snapshot() });
                return result;
              };
            },
          });
        });
}

// Scroll witnesses are captured synchronously at the actual command boundary.
// Waiting for a trace NEVER turns an eventual smooth animation into a pass.
async function scrollScenario(browserName, target, url) {
  const browser = await launch(browserName);
  const { defaultBrowserType, ...phone } = pw.devices['iPhone 13'];
  try {
    for (const reducedMotion of ['no-preference', 'reduce']) {
      const context = await browser.newContext({ ...phone, reducedMotion });
      try {
        await installScrollWitness(context);
        const page = await context.newPage();
        const errors = [];
        page.on('pageerror', () => errors.push(1));
        page.on('console', message => {
          if(message.type() !== 'error') return;
          // Fixed public classes only; never retain or print raw browser text.
          if(/(?:^|[^A-Za-z0-9_])(ScrollFailure|ScrollPayloadRejected|ScrollViewMissing|SpaScrollWire|ScrollQueueOverflow)(?:$|[^A-Za-z0-9_])/.test(message.text())) errors.push(1);
        });
        await page.goto(url, { waitUntil: 'load' });
        await page.locator('#next-part').waitFor();
        if (target === 'web:app') await page.waitForFunction(() => window.__navHydrated === true);
        await page.evaluate(() => {
          document.documentElement.style.cssText += ';overflow-anchor:none;scroll-behavior:smooth;overflow-x:auto;width:100vw;';
          document.body.style.overflowAnchor = 'none';
          document.body.style.minWidth = '2000px';
          document.body.style.width = '2000px';
          document.body.style.overflowX = 'visible';
          for (const el of document.querySelectorAll('#sheet *,#outer *')) el.style.flexShrink = '0';
          window.__navMarker = 1;
        });
        const tag = `${target}/${browserName}/${reducedMotion}`;
        const original = await loc(page);
        async function fire(id, expected) {
          const count = await page.evaluate(() => __navScrollTraces.length);
          await page.evaluate(id => document.getElementById(id).click(), id);
          await page.waitForFunction(n => __navScrollTraces.length > n, count);
          const trace = await page.evaluate(n => __navScrollTraces[n], count);
          check(`${tag} command target ${id}`, expected(trace), 'synchronous command witness');
          return trace;
        }
        const unchanged = t => ['x','y','outerX','outerY','sheetX','sheetY'].every(k => t.before[k] === t.after[k]);
        const isolated = t => ['x','y','outerX','outerY','sheetX'].every(k => t.before[k] === t.after[k]);
        const aligned = t => t.after.targetTop !== null && Math.abs(t.after.targetTop - t.after.portTop - 18 - (t.after.portHeight * .1 + 5)) <= 1.5;
        await page.evaluate(() => {
          const s = document.getElementById('sheet'), o = document.getElementById('outer');
          o.scrollTo({left: 35, top: 35, behavior: 'instant'});
          s.scrollTo({left: 25, top: 0, behavior: 'instant'});
          scrollTo({left: 45, top: 40, behavior: 'instant'});
        });
        check(`${tag} new target absent`, await page.locator('#part-1').count() === 0, 'before update');
        const horizontal = await page.evaluate(() => ({ x:scrollX, innerWidth, clientWidth:document.documentElement.clientWidth, scrollWidth:document.documentElement.scrollWidth, visualWidth:visualViewport?.width }));
        check(`${tag} nonzero window horizontal setup`, horizontal.x > 0, JSON.stringify(horizontal));
        if (!(horizontal.x > 0)) throw new Error('WindowHorizontalPremiseFailed');
        const first = await fire('next-part', t => t.target === 'part-1' && /PART=1/.test(t.after.state));
        check(`${tag} immediate margin/calc-padding alignment`, aligned(first), 'no animation wait');
        check(`${tag} nearest box preserves outer/window/x`, isolated(first), 'exact offsets');
        const after = await loc(page);
        check(`${tag} same-address new render`, after.href === original.href && after.len === original.len && after.marker, 'history and realm unchanged');
        await fire('needed', t => t.target === 'part-1' && unchanged(t));
        await fire('missing', t => t.target === 'absent-target' && unchanged(t));
        await fire('hidden', t => t.target === 'hidden-target' && unchanged(t));
        const server = await fire('server-part', t => t.target === 'part-2' && /PART=2/.test(t.after.state));
        check(`${tag} server-only delayed File.read scroll is post-render`, aligned(server) && isolated(server), 'new server-derived target');
        const n = await page.evaluate(() => __navScrollTraces.length);
        await page.evaluate(() => {
          document.getElementById('scroll-rpc').click();
          document.getElementById('move-target').click();
        });
        await page.waitForFunction(n => __navScrollTraces.length > n, n);
        const rpc = await page.evaluate(n => __navScrollTraces[n], n);
        check(`${tag} no-write RPC scroll precedes later pure movement`, rpc.target === 'rpc-target' && /MOVED=0/.test(rpc.after.state) && aligned(rpc), 'same physical id, strict command boundary');
        await page.waitForFunction(() => /MOVED=1/.test(document.getElementById('state').textContent));
        check(`${tag} native anchoring disabled`, await page.evaluate(y => document.getElementById('sheet').scrollTop === y, rpc.after.sheetY), 'later layout cannot repair order');
        if (target === 'web:app') {
          const held = await page.evaluate(() => {
            const n = __navScrollTraces.length;
            document.getElementById('held-select').focus();
            document.getElementById('next-part').click();
            document.getElementById('next-part').click();
            return n;
          });
          // Two messages are processed while the select holds ancestor patches.
          await waitState(page, s => /PART=4/.test(s), 10000);
          check(`${tag} held select defers target commit`, await page.locator('#part-4').count() === 0 && await page.evaluate(n => __navScrollTraces.length === n, held), 'no precommit scroll');
          await page.evaluate(() => document.getElementById('held-select').blur());
          await page.waitForFunction(n => __navScrollTraces.length > n, held);
          const heldTraces = await page.evaluate(n => __navScrollTraces.slice(n), held);
          check(`${tag} select supersession scrolls only committed latest target`, heldTraces.length === 1 && heldTraces[0].target === 'part-4' && aligned(heldTraces[0]), 'latest frame only');
          const serverHeld = await page.evaluate(() => {
            const n = __navScrollTraces.length;
            document.getElementById('held-select').focus();
            document.getElementById('server-part').click();
            document.getElementById('next-part').click();
            return n;
          });
          const heldState = await waitState(page, s => /PART=5/.test(s), 10000);
          check(`${tag} RPC hold retains later message until actual DOM commit`, /PART=5/.test(heldState) && await page.evaluate(n => __navScrollTraces.length === n, serverHeld), 'server result staged, next not released');
          await page.evaluate(() => document.getElementById('held-select').blur());
          await page.waitForFunction(n => __navScrollTraces.length >= n + 2, serverHeld);
          const committed = await page.evaluate(n => __navScrollTraces.slice(n), serverHeld);
          check(`${tag} RPC select release commits then processes next`, committed.length === 2 && committed[0].target === 'part-5' && committed[1].target === 'part-6' && committed.every(aligned), 'ordered postcommit commands');
        }
        const top = await fire('top', t => t.top && t.after.y === 0 && t.after.x === t.before.x && t.after.sheetY === t.before.sheetY && t.after.outerY === t.before.outerY);
        check(`${tag} explicit top is instant with CSS smooth`, top.after.y === 0, 'synchronous result');
        check(`${tag} no scroll errors`, errors.length === 0, 'classified public fixture');
      } finally { await context.close(); }
    }
  } finally { await browser.close(); }
}

// Each static body/viewport case gets a fresh document. Its ownership CSS is
// present in <head> before the body receives its first layout, not mutated at
// the command boundary. The actual target is still created by typed Next.
async function installStaticRootCSS(page, url, mode) {
  const viewport = mode === 'viewport-propagated';
  const rootOverflow = mode === 'root-clip' ? 'clip' : 'visible';
  const x = mode === 'root-x-clip' ? 'clip' : rootOverflow;
  const rootContain = mode === 'root-containment' ? 'layout' : 'none';
  const bodyContain = mode === 'body-containment' ? 'layout' : 'none';
  const css = `html{width:100vw!important;overflow-x:${x}!important;overflow-y:${rootOverflow}!important;contain:${rootContain}!important;overflow-anchor:none!important;scroll-behavior:smooth!important}body{margin:0!important;padding:0!important;height:240px!important;min-height:0!important;width:${viewport?'2000':'340'}px!important;min-width:0!important;overflow:auto!important;contain:${bodyContain}!important;scroll-padding:0!important;scroll-behavior:smooth!important;overflow-anchor:none!important}#sky-root,#sky-root>*,#app,#app>*,#outer,#outer>*,#sheet{height:auto!important;max-height:none!important}#sky-root,#sky-root *,#app,#app *{overflow:visible!important;overflow-anchor:none!important;flex-shrink:0!important}`;
  await page.route(url, async route => {
    if (route.request().method() !== 'GET' || route.request().resourceType() !== 'document') return route.continue();
    const response = await route.fetch();
    const html = await response.text();
    const nonce = html.match(/<style\b[^>]*\bnonce=(["'])([^"']+)\1[^>]*>/i);
    if (nonce && !/^[A-Za-z0-9+/_=-]+$/.test(nonce[2])) throw new Error('FixtureNonceShape');
    const csp = response.headers()['content-security-policy'] || '';
    if (/style-src[^;]*'nonce-/i.test(csp) && !nonce) throw new Error('FixtureStyleNonceMissing');
    let inserted = 0;
    const body = html.replace(/<head\b[^>]*>/i, head => { inserted++; return head + `<style${nonce?' nonce="'+nonce[2]+'"':''}>${css}</style>`; });
    if (inserted !== 1) throw new Error('FixtureHeadMissing');
    await route.fulfill({response,body});
  });
}

async function rootPortScenarios(browserName, target, url) {
  const browser = await launch(browserName);
  const {defaultBrowserType,...phone}=pw.devices['iPhone 13'];
  const modes=['viewport-propagated','root-clip','root-x-clip','body-containment','root-containment'];
  try {
    for(const reducedMotion of ['no-preference','reduce']) for(const mode of modes) {
      const context=await browser.newContext({...phone,reducedMotion});
      try {
        await installScrollWitness(context);const page=await context.newPage();const errors=[];
        page.on('pageerror',()=>errors.push(1));
        page.on('console',message=>{if(message.type()==='error'&&/(?:^|[^A-Za-z0-9_])(ScrollFailure|ScrollPayloadRejected|ScrollViewMissing|SpaScrollWire|ScrollQueueOverflow)(?:$|[^A-Za-z0-9_])/.test(message.text()))errors.push(1)});
        await installStaticRootCSS(page,url,mode);await page.goto(url,{waitUntil:'load'});await page.locator('#next-part').waitFor();
        if(target==='web:app')await page.waitForFunction(()=>window.__navHydrated===true);
        const absent=await page.locator('#part-1').count()===0;
        await page.evaluate(viewport=>{if(viewport)scrollTo({left:45,top:0,behavior:'instant'});else document.body.scrollTo({left:31,top:0,behavior:'instant'});window.__navMarker=1},mode==='viewport-propagated');
        const before=await loc(page);const n=await page.evaluate(()=>__navScrollTraces.length);
        await page.evaluate(()=>document.getElementById('next-part').click());await page.waitForFunction(n=>__navScrollTraces.length>n,n);
        const trace=await page.evaluate(n=>__navScrollTraces[n],n);const after=await loc(page);
        const viewport=mode==='viewport-propagated';const tag=`${target}/${browserName}/${reducedMotion}/static-${mode}`;
        check(`${tag} typed Next creates target after render`,absent&&trace.target==='part-1'&&/PART=1/.test(trace.after.state),'new target, not preexisting DOM');
        check(`${tag} immediate alignment in intended port`,trace.after.targetTop!==null&&Math.abs(trace.after.targetTop-18)<=1.5&&(viewport?trace.after.y>0&&trace.after.bodyY===0:trace.after.bodyY>0&&trace.after.y===trace.before.y),'synchronous helper-return geometry');
        check(`${tag} nonzero horizontal and outer isolation`,(viewport?trace.before.x>0:trace.before.bodyX>0)&&trace.after.x===trace.before.x&&trace.after.bodyX===trace.before.bodyX&&trace.after.sheetX===trace.before.sheetX&&trace.after.outerX===trace.before.outerX&&trace.after.sheetY===trace.before.sheetY&&trace.after.outerY===trace.before.outerY,'exact preserved offsets');
        check(`${tag} no navigation or scroll errors`,after.href===before.href&&after.len===before.len&&after.marker&&errors.length===0,'strict normal/static behavior');
      }finally{await context.close()}
    }
    if(browserName==='webkit')for(const mode of ['root-clip','root-x-clip']) {
      const context=await browser.newContext({...phone,reducedMotion:'reduce'});
      try {
        await installScrollWitness(context);const page=await context.newPage();const errors=[];
        page.on('pageerror',()=>errors.push('PageError'));
        page.on('console',message=>{if(message.type()!=='error')return;const match=message.text().match(/(?:^|[^A-Za-z0-9_])(ScrollFailure|ScrollPayloadRejected|ScrollViewMissing|SpaScrollWire|ScrollQueueOverflow)(?:$|[^A-Za-z0-9_])/);if(match)errors.push(match[1])});
        await installStaticRootCSS(page,url,'viewport-propagated');await page.goto(url,{waitUntil:'load'});await page.locator('#next-part').waitFor();if(target==='web:app')await page.waitForFunction(()=>window.__navHydrated===true);
        const n=await page.evaluate(()=>__navScrollTraces.length);
        await page.evaluate(mode=>{window.__navScrollSetup=()=>{const style=document.documentElement.style;style.setProperty('overflow-x','clip','important');style.setProperty('overflow-y',mode==='root-clip'?'clip':'visible','important');};document.getElementById('next-part').click()},mode);
        await page.waitForFunction(n=>__navScrollTraces.length>n,n);const trace=await page.evaluate(n=>__navScrollTraces[n],n);
        const isolated=['x','y','bodyX','sheetX','sheetY','outerX','outerY'].every(k=>trace.before[k]===trace.after[k]);
        const fulfilled=isolated&&trace.after.bodyY>trace.before.bodyY&&Math.abs(trace.after.targetTop-18)<=1.5&&errors.length===0;
        const refused=isolated&&trace.after.bodyY===trace.before.bodyY&&trace.after.targetTop===trace.before.targetTop&&errors.length===1&&errors[0]==='ScrollFailure';
        check(`${target}/webkit/${mode} native ownership-transition outcome is explicit`,trace.target==='part-1'&&(fulfilled||refused),'immediate fulfillment or unchanged geometry plus exactly one fixed failure');
      }finally{await context.close()}
    }
  }finally{await browser.close()}
}

// Same browser context intentionally shares the Live session cookie. Shared
// server-model delivery is proved by a new Part appearing in BOTH documents;
// only the issuing document may receive the accompanying scroll command.
async function liveOriginScenario(browserName, url) {
  const browser = await launch(browserName);
  const { defaultBrowserType, ...phone } = pw.devices['iPhone 13'];
  const context = await browser.newContext({ ...phone, reducedMotion:'reduce' });
  try {
    await installScrollWitness(context);
    const first=await context.newPage();
    await first.goto(url,{waitUntil:'load'});await first.locator('#next-part').waitFor();
    const second=await context.newPage();
    await second.goto(url,{waitUntil:'load'});await second.locator('#next-part').waitFor();
    const originErrors=[];
    for(const page of [first,second]) {
      page.on('pageerror',()=>originErrors.push(1));
      page.on('console',message=>{if(message.type()==='error'&&/(?:^|[^A-Za-z0-9_])(ScrollFailure|ScrollPayloadRejected|ScrollViewMissing|SpaScrollWire|ScrollQueueOverflow)(?:$|[^A-Za-z0-9_])/.test(message.text()))originErrors.push(1)});
    }
    for(const page of [first,second]) await page.evaluate(() => {
      document.documentElement.style.overflowAnchor='none';document.body.style.overflowAnchor='none';
      for(const el of document.querySelectorAll('#outer,#sheet,#sheet *'))el.style.overflowAnchor='none';
    });
    const initial=await state(first);const next=Number(field(initial,'PART'))+1;
    const beforeFirst=await first.evaluate(()=>__navScrollTraces.length);
    const beforeSecond=await second.evaluate(()=>__navScrollTraces.length);
    await first.evaluate(()=>document.getElementById('next-part').click());
    await first.waitForFunction(n=>__navScrollTraces.length>n,beforeFirst);
    await waitState(second,s=>Number(field(s,'PART'))===next,10000);
    check(`live/${browserName} two tabs share actual server model`, Number(field(await state(second),'PART'))===next,'same new Part delivered by SSE');
    check(`live/${browserName} model broadcast does not broadcast scroll`,await second.evaluate(n=>__navScrollTraces.length===n,beforeSecond),'other document receives no scroll');
    const firstCount=await first.evaluate(()=>__navScrollTraces.length);
    const secondCount=await second.evaluate(()=>__navScrollTraces.length);
    await second.evaluate(()=>document.getElementById('scroll-rpc').click());
    await second.waitForFunction(n=>__navScrollTraces.length>n,secondCount);
    const own=await second.evaluate(n=>__navScrollTraces[n],secondCount);
    check(`live/${browserName} scroll-only RPC returns to origin tab`,own.target==='rpc-target','issuing document receives delayed command');
    // A later model publication is a delivery fence for the observer tab.
    // This avoids treating a briefly absent, delayed wrong-origin command as a pass.
    await second.evaluate(()=>document.getElementById('next-part').click());
    await waitState(first,s=>Number(field(s,'PART'))===next+1,10000);
    check(`live/${browserName} origin exclusion survives later SSE publication`,Number(field(await state(first),'PART'))===next+1&&await first.evaluate(n=>__navScrollTraces.length===n,firstCount),'shared model delivery fence, no foreign scroll');
    check(`live/${browserName} origin cases have no scroll errors`,originErrors.length===0,'fixed classes and page errors only');
  } finally { await context.close();await browser.close(); }
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
      await scrollScenario(b, target, `http://127.0.0.1:${port}/`);
      await rootPortScenarios(b, target, `http://127.0.0.1:${port}/`);
      if(target==='live')await liveOriginScenario(b, `http://127.0.0.1:${port}/`);
    }
    await stopApp(app);
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
  console.error("Public fixture failure; child output retained in memory only.");
  process.exitCode = 1;
} finally {
  for (const a of apps) await stopApp(a);
  for (const f of ownedFiles) {
    const stat = await lstat(f.path);
    if (stat.ino !== f.ino || stat.dev !== f.dev || await readFile(f.path, 'utf8') !== f.value) throw new Error('OwnedFixtureFileChanged');
    await unlink(f.path);
  }
  process.exit(process.exitCode ?? 1);
}
