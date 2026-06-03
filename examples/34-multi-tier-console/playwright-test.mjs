#!/usr/bin/env node
// Playwright e2e — examples/34-multi-tier-console (v0.16.1 PR10-J).
//
// Verifies:
//   1. All four tier pages render with HTTP 200 + expected body markers.
//   2. Clicking nav buttons updates the active tier (Sky.Live click → SSE patch).
//   3. Emitting tier-specific log buttons increments the in-Sky counter.
//   4. The /_sky/console aggregation page is reachable post-login.
//   5. Telemetry from each tier (host nav, billing charge, jobs enqueue,
//      auth signin) appears in the console's metrics surface — we
//      assert via /_sky/console/api/logs JSON which the inline UI
//      reads.

import { chromium } from 'playwright';

const BASE = process.env.BASE || 'http://127.0.0.1:18834';
const TOKEN = process.env.TOKEN || 'cb8e7d6a4f2103e95f6a8d3b1c4e7f9a8d2b5c8e1f4a7d0c3b6e9f2a5d8c1b4e';

const fail = (msg) => { console.error('FAIL:', msg); process.exit(1); };
const ok = (msg) => console.log('PASS:', msg);

const browser = await chromium.launch({ headless: true });
const ctx = await browser.newContext({ ignoreHTTPSErrors: true });
const page = await ctx.newPage();
page.on('console', m => {
    if (m.type() === 'error') console.log('  browser:', m.type(), m.text());
});

// ─── 1. Each tier page renders ──────────────────────────────────
const tiers = [
    { path: '/', name: 'host' },
    { path: '/billing', name: 'billing' },
    { path: '/jobs', name: 'jobs' },
    { path: '/auth', name: 'auth' },
];
for (const t of tiers) {
    const resp = await ctx.request.get(`${BASE}${t.path}`);
    if (resp.status() !== 200) fail(`tier ${t.name} ${t.path} → ${resp.status()}`);
}
ok(`all 4 tier pages return HTTP 200`);

// ─── 2. Visit each tier (drives navigation logs) ────────────────
await page.goto(`${BASE}/`, { waitUntil: 'domcontentloaded' });
await page.waitForTimeout(300);

// Click Billing
await page.click('button:has-text("Billing")');
await page.waitForTimeout(500);
const billingVisible = await page.evaluate(() => document.body.textContent.includes('Stripe-style charge events'));
if (!billingVisible) fail('clicking Billing did not update view to billing tier');
ok('Billing tier becomes active after nav click');

// Click Jobs
await page.click('button:has-text("Jobs")');
await page.waitForTimeout(500);
const jobsVisible = await page.evaluate(() => document.body.textContent.includes('Background-task events'));
if (!jobsVisible) fail('clicking Jobs did not update view to jobs tier');
ok('Jobs tier becomes active after nav click');

// Click Auth
await page.click('button:has-text("Auth")');
await page.waitForTimeout(500);
const authVisible = await page.evaluate(() => document.body.textContent.includes('Sign-in / token events'));
if (!authVisible) fail('clicking Auth did not update view to auth tier');
ok('Auth tier becomes active after nav click');

// ─── 3. Emit one log from each tier ──────────────────────────────
// Navigate to billing, click emit button
await page.click('button:has-text("Billing")');
await page.waitForTimeout(500);
await page.click('button:has-text("Emit charge.success")');
await page.waitForTimeout(300);
await page.click('button:has-text("Emit charge.failed")');
await page.waitForTimeout(300);
const billingHits = await page.evaluate(() => {
    const m = document.body.textContent.match(/(\d+) hits this session/);
    return m ? parseInt(m[1], 10) : 0;
});
if (billingHits < 2) fail(`billing should have >=2 hits, got ${billingHits}`);
ok(`billing tier emitted ${billingHits} logs`);

await page.click('button:has-text("Jobs")');
await page.waitForTimeout(300);
await page.click('button:has-text("Emit enqueue")');
await page.waitForTimeout(300);
const jobsHits = await page.evaluate(() => {
    const m = document.body.textContent.match(/(\d+) hits this session/);
    return m ? parseInt(m[1], 10) : 0;
});
if (jobsHits < 1) fail(`jobs should have >=1 hits, got ${jobsHits}`);
ok(`jobs tier emitted ${jobsHits} logs`);

await page.click('button:has-text("Auth")');
await page.waitForTimeout(300);
await page.click('button:has-text("Emit sign-in")');
await page.waitForTimeout(300);
const authHits = await page.evaluate(() => {
    const m = document.body.textContent.match(/(\d+) hits this session/);
    return m ? parseInt(m[1], 10) : 0;
});
if (authHits < 1) fail(`auth should have >=1 hits, got ${authHits}`);
ok(`auth tier emitted ${authHits} logs`);

// ─── 4. Console reachable post-login ────────────────────────────
// Playwright's ctx.request auto-stores cookies for subsequent
// ctx.request calls (per the docs). Same pattern as the working
// console-click-test.mjs.
const loginResp = await ctx.request.post(`${BASE}/_sky/console/_login`, {
    form: { token: TOKEN },
    maxRedirects: 0,
    failOnStatusCode: false
});
if (loginResp.status() !== 303 && loginResp.status() !== 302) {
    fail(`console login expected 303/302, got ${loginResp.status()}`);
}
ok(`console login HTTP ${loginResp.status()}`);

// Use page.goto rather than ctx.request.get because the browser
// page cookie jar handles the `__Host-` Secure-only contract by
// trusting localhost. (We sync the API-request jar's cookie to the
// browser page jar via the explicit Cookie header below.)
const cookieHeader = (loginResp.headers()['set-cookie'] || '')
    .split(';')[0]; // "__Host-sky_console=<value>"
const consoleResp = await ctx.request.get(`${BASE}/_sky/console`, {
    headers: { Cookie: cookieHeader },
});
if (consoleResp.status() !== 200) fail(`/_sky/console got ${consoleResp.status()}`);
ok(`/_sky/console reachable post-login (HTTP 200)`);

// ─── 5. Console aggregates per-tier logs ────────────────────────
const logsResp = await ctx.request.get(`${BASE}/_sky/console/api/logs?limit=200`, {
    headers: { Cookie: cookieHeader },
});
if (logsResp.status() !== 200) fail(`/_sky/console/api/logs got ${logsResp.status()}`);
const logs = await logsResp.json();
ok(`console returned ${logs.length} log entries`);

// Verify we see logs emitted from each tier. Each tier's Log.infoWith
// emits a structured log line containing the tier's namespace name.
// The console aggregates ALL of them into one stream — that's the
// headline claim of "one pane of glass for every signal your app
// generates."
const allMessages = logs.map(l => l.Message || '').join(' | ');
const expectedMarkers = [
    { tier: 'billing', marker: 'billing.charge' },
    { tier: 'jobs', marker: 'jobs.enqueue' },
    { tier: 'auth', marker: 'auth.signin' },
    { tier: 'host', marker: 'navigate' }, // host nav events
];
const missing = expectedMarkers.filter(em => !allMessages.includes(em.marker));
if (missing.length > 0) {
    console.log('  log messages sample:', logs.slice(0, 5).map(l => l.Message));
    fail(`console missing logs from tier(s): ${missing.map(em => em.tier).join(', ')}`);
}
ok(`console aggregates logs from all 4 tiers (${expectedMarkers.map(em => em.tier).join(', ')})`);
// Bonus: assert the explicit namespace tag appears in at least one
// emitted log. This is what powers the UI filter chip.
const namespaceTaggedLogs = logs.filter(l => (l.Message || '').includes('service.namespace'));
if (namespaceTaggedLogs.length === 0) {
    fail('expected at least one log to carry the service.namespace tag');
}
ok(`${namespaceTaggedLogs.length} log lines carry service.namespace tag (drives the UI filter)`);

await browser.close();
console.log('\nALL GREEN — multi-tier console e2e verified');
