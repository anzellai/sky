// Light prod canary smoke against sky-lang.org with the new PR10 runtime.
// Asserts:
//   1. home renders + sky-id namespace prefix appears as "r" (host)
//   2. /_sky/console serves the 401 auth-gated response
//   3. /_sky/healthz returns 200 (proves observability mount survived
//      the PR10-D middleware wrap)
//   4. /_sky/metrics has Bearer-protected access (production gate live)
import { chromium } from 'playwright';

const BASE = 'https://sky-lang.org';
const fail = m => { console.error('FAIL:', m); process.exit(1); };
const ok = m => console.log('PASS:', m);

const browser = await chromium.launch({ headless: true });
const ctx = await browser.newContext();
const page = await ctx.newPage();

// 1. home
const homeResp = await ctx.request.get(`${BASE}/`);
if (homeResp.status() !== 200) fail(`home ${homeResp.status()}`);
const homeBody = await homeResp.text();
if (!homeBody.includes('Sky')) fail('home body missing "Sky"');
ok('sky-lang.org / renders HTTP 200');

// Verify sky-id prefix is still "r" (host default)
const hostIdMatch = homeBody.match(/sky-id="(r\.[^"]+)"/);
if (!hostIdMatch) fail('host page lacks sky-id="r.*" — PR10-A skyIDPrefix scaffolding broken');
ok(`host sky-id namespace = "r.*" (PR10-A scaffolding live)`);

// 2. console gate
const consoleResp = await ctx.request.get(`${BASE}/_sky/console`);
if (consoleResp.status() !== 401) fail(`console got ${consoleResp.status()}, want 401`);
ok('/_sky/console returns 401 (auth gate live)');

// 3. healthz
const healthResp = await ctx.request.get(`${BASE}/_sky/healthz`);
if (healthResp.status() !== 200) fail(`healthz ${healthResp.status()}`);
ok('/_sky/healthz HTTP 200 (PR10-D middleware wrap did not break observability)');

// 4. metrics gate
const metricsResp = await ctx.request.get(`${BASE}/_sky/metrics`);
if (metricsResp.status() !== 401 && metricsResp.status() !== 403) {
    fail(`metrics ungated: got ${metricsResp.status()}`);
}
ok(`/_sky/metrics gated (HTTP ${metricsResp.status()})`);

// 5. /_sky/buildinfo
const biResp = await ctx.request.get(`${BASE}/_sky/buildinfo`);
if (biResp.status() !== 200) fail(`buildinfo ${biResp.status()}`);
const bi = await biResp.json();
console.log('  buildinfo:', JSON.stringify(bi));
ok('/_sky/buildinfo HTTP 200');

await browser.close();
console.log('\nProduction canary GREEN — PR10 runtime alive on sky-lang.org');
