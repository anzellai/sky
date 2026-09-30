// Real-app verification harness: one app, one browser, fresh context per check.
// usage: node harness.mjs <appConfig.mjs> <chrome|webkit> <out.json>
import { createRequire } from 'module';
import fs from 'fs';
const require = createRequire('/Users/anzel/works/playground/sky/package.json');
const { chromium, webkit } = require('playwright');

const [, , cfgPath, browserName, outPath] = process.argv;
const cfg = (await import(cfgPath)).default;
const BASE = cfg.base;

const launch = () =>
  browserName === 'chrome'
    ? chromium.launch({ channel: 'chrome', headless: false })
    : webkit.launch({ headless: false });

const browser = await launch();
browser.on('disconnected', () => process.stderr.write('BROWSER DISCONNECTED\n'));
const out = {
  app: cfg.name,
  browser: browserName,
  browserVersion: browser.version(),
  base: BASE,
  startedAt: new Date().toISOString(),
  pages: [],
  checks: {},
};

async function freshContext() {
  const ctx = await browser.newContext({ ignoreHTTPSErrors: true });
  await ctx.addInitScript(() => {
    window.__cspv = [];
    document.addEventListener('securitypolicyviolation', (e) => {
      window.__cspv.push({ directive: e.violatedDirective, blocked: e.blockedURI, source: e.sourceFile });
    });
  });
  return ctx;
}

function watch(page) {
  const w = { consoleErrors: [], pageErrors: [], cspConsole: [], rpc: [], wasm: [], sub: [], failed: [] };
  page.on('console', (m) => {
    const t = m.text();
    if (/Content Security Policy|Content-Security-Policy|Refused to (load|execute|apply|connect|frame)/i.test(t)) w.cspConsole.push(t.slice(0, 300));
    if (m.type() === 'error') w.consoleErrors.push(t.slice(0, 300));
  });
  page.on('pageerror', (e) => w.pageErrors.push(String(e).slice(0, 300)));
  page.on('requestfailed', (r) => w.failed.push(`${r.method()} ${r.url()} ${r.failure()?.errorText}`));
  page.on('response', async (r) => {
    const u = r.url();
    if (u.includes('/_rpc/')) w.rpc.push({ method: r.request().method(), path: new URL(u).pathname, status: r.status() });
    if (u.endsWith('.wasm')) {
      const h = await r.allHeaders().catch(() => ({}));
      w.wasm.push({ path: new URL(u).pathname, status: r.status(), contentEncoding: h['content-encoding'] || null, cacheControl: h['cache-control'] || null });
    }
    if (u.includes('/_sky/sub')) w.sub.push({ path: new URL(u).pathname + new URL(u).search, status: r.status() });
  });
  return w;
}

const withTimeout = (p, ms, what) => Promise.race([p, new Promise((_, rej) => setTimeout(() => rej(new Error('timeout: ' + what)), ms))]);

async function hydrated(page, ms = 30000) {
  try {
    await page.waitForFunction(() => !document.documentElement.hasAttribute('data-sky-hydrating') && !!document.querySelector('#app'), null, { timeout: ms });
    return true;
  } catch { return false; }
}

async function cspViolations(page) {
  return page.evaluate(() => window.__cspv || []).catch(() => ['<eval failed>']);
}

const cookieView = (cs) => cs.map((c) => ({ name: c.name, secure: c.secure, httpOnly: c.httpOnly, sameSite: c.sameSite, path: c.path, domain: c.domain, session: c.expires === -1 }));

async function step(name, fn) {
  try { out.checks[name] = await fn(); }
  catch (e) { out.checks[name] = { error: String(e).slice(0, 500) }; }
  process.stderr.write(`[${cfg.name}/${browserName}] ${name}: ${JSON.stringify(out.checks[name]).slice(0, 400)}\n`);
}

// 1. Every main page: https load, zero console errors, zero CSP violations, wasm boot.
for (const p of cfg.pages) {
  const ctx = await freshContext();
  const page = await ctx.newPage();
  const w = watch(page);
  let status = null, cspHeader = null;
  try {
    const resp = await page.goto(BASE + p, { waitUntil: 'load', timeout: 45000 });
    status = resp?.status() ?? null;
    const h = resp ? await resp.allHeaders() : {};
    cspHeader = h['content-security-policy'] || null;
  } catch (e) { w.pageErrors.push('goto: ' + String(e).slice(0, 200)); }
  const boot = await hydrated(page);
  await page.waitForTimeout(1500);
  const v = await cspViolations(page);
  out.pages.push({
    path: p, status, protocol: new URL(page.url()).protocol, finalPath: new URL(page.url()).pathname,
    wasmBooted: boot, wasm: w.wasm, consoleErrors: w.consoleErrors, pageErrors: w.pageErrors,
    cspViolations: v.length + w.cspConsole.length, cspDetail: [...v, ...w.cspConsole].slice(0, 5),
    cspHeader: cspHeader ? cspHeader.slice(0, 160) : null, rpc: w.rpc, failedRequests: w.failed.slice(0, 5),
  });
  process.stderr.write(`[${cfg.name}/${browserName}] page ${p}: ${status} boot=${boot} errs=${w.consoleErrors.length + w.pageErrors.length} csp=${v.length + w.cspConsole.length}\n`);
  await ctx.close();
}

// 2. Cookies set by an anonymous visit.
await step('anonCookies', async () => {
  const ctx = await freshContext();
  const page = await ctx.newPage();
  await page.goto(BASE + cfg.pages[0], { waitUntil: 'load' });
  await hydrated(page);
  await page.waitForTimeout(1000);
  const r = cookieView(await ctx.cookies(BASE));
  await ctx.close();
  return r;
});

// 3. The /_rpc origin guard, from the browser's own network stack and with explicit headers.
await step('rpcGuard', async () => {
  const ctx = await freshContext();
  const page = await ctx.newPage();
  await page.goto(BASE + cfg.pages[0], { waitUntil: 'load' });
  await hydrated(page);
  const target = BASE + cfg.rpcGuardPath;
  const body = cfg.rpcGuardBody ?? '{}';
  const origin = new URL(BASE).origin;
  const req = ctx.request;
  const r = {};
  const st = async (resp) => ({ status: resp.status(), body: (await resp.text()).slice(0, 120) });
  r.crossOriginJson = await st(await req.post(target, { headers: { 'Content-Type': 'application/json', Origin: 'https://evil.example', 'Sec-Fetch-Site': 'cross-site' }, data: body }));
  r.sameSiteOtherOriginJson = await st(await req.post(target, { headers: { 'Content-Type': 'application/json', Origin: 'https://other.localhost:9443', 'Sec-Fetch-Site': 'same-site' }, data: body }));
  r.originNullJson = await st(await req.post(target, { headers: { 'Content-Type': 'application/json', Origin: 'null' }, data: body }));
  r.sameOriginTextPlain = await st(await req.post(target, { headers: { 'Content-Type': 'text/plain', Origin: origin, 'Sec-Fetch-Site': 'same-origin' }, data: body }));
  r.sameOriginForm = await st(await req.post(target, { headers: { 'Content-Type': 'application/x-www-form-urlencoded', Origin: origin, 'Sec-Fetch-Site': 'same-origin' }, data: 'a=1' }));
  r.get = await st(await req.get(target));
  r.sameOriginJsonControl = await st(await req.post(target, { headers: { 'Content-Type': 'application/json', Origin: origin, 'Sec-Fetch-Site': 'same-origin' }, data: body }));
  // In-browser: a same-origin fetch the way the wasm client sends it.
  r.browserSameOriginFetch = await withTimeout(page.evaluate(async ([t, b]) => {
    const resp = await fetch(t, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: b, credentials: 'include' });
    return { status: resp.status };
  }, [target, body]), 20000, 'same-origin fetch');
  // In-browser: a page on a foreign origin POSTs text/plain without a preflight.
  const evil = await ctx.newPage();
  const seen = [];
  evil.on('response', (x) => { if (x.url().includes('/_rpc/')) seen.push({ status: x.status() }); });
  await evil.goto(cfg.evilOrigin + '/');
  r.evilOrigin = cfg.evilOrigin;
  await withTimeout(evil.evaluate(async ([t, b]) => {
    try { await fetch(t, { method: 'POST', mode: 'no-cors', credentials: 'include', headers: { 'Content-Type': 'text/plain' }, body: b }); } catch (e) {}
    try { await fetch(t, { method: 'POST', mode: 'cors', credentials: 'include', headers: { 'Content-Type': 'text/plain' }, body: b }); } catch (e) {}
  }, [target, body]), 20000, 'evil fetch');
  await evil.waitForTimeout(1500);
  r.browserCrossOriginNoCors = seen;
  await ctx.close();
  return r;
});

// 4. Precompressed wasm and loader through the proxy.
await step('precompressed', async () => {
  const ctx = await freshContext();
  const page = await ctx.newPage();
  const w = watch(page);
  await page.goto(BASE + cfg.pages[0], { waitUntil: 'load' });
  await hydrated(page);
  const wasmPath = w.wasm[0]?.path;
  const r = { browserWasm: w.wasm };
  if (wasmPath) {
    for (const enc of ['br', 'gzip', 'identity']) {
      const resp = await ctx.request.get(BASE + wasmPath, { headers: { 'Accept-Encoding': enc } });
      const h = resp.headers();
      r[enc] = { status: resp.status(), contentEncoding: h['content-encoding'] || null, contentLength: h['content-length'] || null, cacheControl: h['cache-control'] || null };
    }
    const le = await ctx.request.get(BASE + '/wasm_exec.js', { headers: { 'Accept-Encoding': 'br' } });
    r.wasmExecBr = { status: le.status(), contentEncoding: le.headers()['content-encoding'] || null };
  }
  await ctx.close();
  return r;
});

// 5. Console: refused anonymously, opens with the local credential.
await step('console', async () => {
  const ctx = await freshContext();
  const page = await ctx.newPage();
  const anon = await page.goto(BASE + '/_sky/console/', { waitUntil: 'load' });
  const r = { anonymousStatus: anon?.status() ?? null, anonymousApi: (await ctx.request.get(BASE + '/_sky/console/api/overview')).status() };
  if (cfg.consoleLogin) Object.assign(r, await cfg.consoleLogin({ ctx, page, BASE, hydrated, cookieView, watch }));
  await ctx.close();
  return r;
});

// 6. App-specific: sign-in + session-cookie rotation + an /_rpc interaction + subscriptions.
for (const [name, fn] of Object.entries(cfg.scenarios || {})) {
  await step(name, async () => {
    const ctx = await freshContext();
    const page = await ctx.newPage();
    const w = watch(page);
    const r = await fn({ ctx, page, w, BASE, hydrated, cookieView, cspViolations, browser });
    r.consoleErrors = w.consoleErrors; r.pageErrors = w.pageErrors; r.rpcSeen = w.rpc; r.subSeen = w.sub;
    r.cspViolations = (await cspViolations(page)).length + w.cspConsole.length;
    await ctx.close();
    return r;
  });
}

out.finishedAt = new Date().toISOString();
await browser.close();
fs.writeFileSync(outPath, JSON.stringify(out, null, 2));
console.log('wrote', outPath);
