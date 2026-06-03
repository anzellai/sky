#!/usr/bin/env node
// Playwright e2e — verify Sky Console tab clicks work end-to-end (v0.16.1 PR10-F+).
//
// Post-PR10-F the inline console is a canonical Sky.Live sub-app
// mounted at /_sky/console. Endpoints:
//
//   GET  /_sky/console            — initial render (handleInitial)
//   POST /_sky/console/_login     — token-mode login (sets __Host-sky_console)
//   GET  /_sky/console/_sky/sse   — SSE patch channel
//   POST /_sky/console/_sky/event — Msg dispatch
//
// (Pre-PR10-F endpoints were /_sky/console/_sse + /_sky/console/_event
// driven by the now-deleted console_loop.go. The redirect of the test
// to the new URLs is the verification that PR10-G's deletion didn't
// break interactivity.)

import { chromium } from 'playwright';

const BASE = process.env.BASE || 'http://127.0.0.1:18814';
const TOKEN = process.env.TOKEN || 'cb8e7d6a4f2103e95f6a8d3b1c4e7f9a8d2b5c8e1f4a7d0c3b6e9f2a5d8c1b4e';

const fail = (msg) => { console.error('FAIL:', msg); process.exit(1); };
const ok = (msg) => console.log('PASS:', msg);

const browser = await chromium.launch({ headless: true });
const ctx = await browser.newContext({ ignoreHTTPSErrors: true });
const page = await ctx.newPage();

// Capture network events for diagnosis
const events = [];
page.on('request', r => {
    if (r.url().includes('/_sky/console/')) events.push({ type: 'req', url: r.url(), method: r.method(), body: r.postData() });
});
page.on('response', async r => {
    if (r.url().includes('/_sky/console/_sky/event') || r.url().includes('/_sky/console/_sky/sse')) {
        events.push({ type: 'resp', url: r.url(), status: r.status() });
    }
});
page.on('console', m => console.log('  browser:', m.type(), m.text()));

// Generate telemetry
for (let i = 0; i < 10; i++) {
    await fetch(`${BASE}/`).catch(()=>{});
}

// Login via Playwright's context so cookies are stored natively
const loginResp = await ctx.request.post(`${BASE}/_sky/console/_login`, {
    form: { token: TOKEN },
    maxRedirects: 0,
    failOnStatusCode: false
});
if (loginResp.status() !== 303 && loginResp.status() !== 302) fail(`login expected 303/302, got ${loginResp.status()}`);
ok(`login HTTP ${loginResp.status()} + cookie auto-stored`);

// Go to the console
await page.goto(`${BASE}/_sky/console/`, { waitUntil: 'domcontentloaded' });
ok('navigated to /_sky/console/');

// Wait for SSE handshake
await page.waitForTimeout(800);

// Capture the Overview tab's color (the "active" indicator)
const overviewColor = await page.evaluate(() => {
    const tabs = Array.from(document.querySelectorAll('[sky-click="SelectTab"]'));
    return tabs.find(t => t.textContent.trim() === 'Overview')?.style.color || '';
});
const logsColorBefore = await page.evaluate(() => {
    const tabs = Array.from(document.querySelectorAll('[sky-click="SelectTab"]'));
    return tabs.find(t => t.textContent.trim() === 'Logs')?.style.color || '';
});
console.log('  Overview color (before):', overviewColor);
console.log('  Logs color (before):', logsColorBefore);

// Verify hid attribute on tabs
const logsHid = await page.evaluate(() => {
    const tabs = Array.from(document.querySelectorAll('[sky-click="SelectTab"]'));
    return tabs.find(t => t.textContent.trim() === 'Logs')?.getAttribute('data-sky-hid');
});
if (!logsHid || logsHid === '.click') fail(`Logs tab hid is broken: "${logsHid}"`);
ok(`Logs tab data-sky-hid = "${logsHid}"`);

// CLICK the Logs tab
console.log('  → clicking Logs tab');
await page.click('[sky-click="SelectTab"] >> text=Logs');
await page.waitForTimeout(1500);

// Verify the active indicator moved (color now on Logs, not Overview)
const overviewColorAfter = await page.evaluate(() => {
    const tabs = Array.from(document.querySelectorAll('[sky-click="SelectTab"]'));
    return tabs.find(t => t.textContent.trim() === 'Overview')?.style.color || '';
});
const logsColorAfter = await page.evaluate(() => {
    const tabs = Array.from(document.querySelectorAll('[sky-click="SelectTab"]'));
    return tabs.find(t => t.textContent.trim() === 'Logs')?.style.color || '';
});
console.log('  Overview color (after):', overviewColorAfter);
console.log('  Logs color (after):', logsColorAfter);

// Network capture
const eventPosts = events.filter(e => e.type === 'req' && e.url.includes('/_sky/event'));
console.log('  /_sky/console/_sky/event POSTs captured:', eventPosts.length);
for (const e of eventPosts) console.log('    body:', e.body);

if (logsColorAfter === logsColorBefore) {
    fail('Logs tab color did NOT change — click did not update model OR DOM patch did not apply');
}
ok('Logs tab color CHANGED — interactivity works!');

await browser.close();
console.log('\nALL GREEN');
