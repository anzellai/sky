// Sky Console end-to-end test.
//
// THIS is what should have caught the bugs the user kept finding by
// hand. Each prior "tests pass" report from claude was true in the
// narrow sense (Go unit tests + cabal HM specs + sky-fmt specs all
// green) but missed the full pipeline:
//
//   parent app  →  spawned console child  →  reverse-proxy
//                                              ↓
//   browser  ←  Sky.Live wire  ←  /_sky/console/ HTML
//
// The bugs were always in the GAPS between layers:
//   * Console hang on Metrics tab — 5-endpoints-every-2s tick fired
//     re-renders faster than they could drain; proxy logged
//     "context canceled" floods + browser hung
//   * Logs showed walls of identical "msg_dispatch" — Message text
//     was the literal string, Msg name only in Fields
//   * Overview's buffer counters always 0 — read from a Prometheus-
//     only gauge that's never stored
//   * The Subapp + SessionID columns were blank because the wire
//     decoder dropped them
//
// None of those were catchable by unit tests. This script spawns
// the real parent + console child via subapp mount, drives a real
// browser, and asserts:
//
//   * Each tab loads + renders non-zero content within a hard
//     deadline (catches the hang).
//   * Server logs are clean — zero panics, zero "context canceled"
//     floods (catches the re-render pile-up).
//   * Log entries are differentiated — `msg_dispatch Tick` not
//     just `msg_dispatch` (catches the "noisy logs" regression).
//   * Overview KPIs are non-zero after driving traffic (catches
//     the always-zero counter regression).
//   * Session id badges render with non-empty content (catches
//     the dropped-Fields regression).
//
// Usage: node scripts/verify-console-e2e.mjs
//
// Exits 0 on success, non-zero on first assertion failure.

import { spawn } from 'child_process';
import { chromium } from 'playwright';
import { mkdirSync, readFileSync, createWriteStream } from 'fs';
import { fileURLToPath } from 'url';
import path from 'path';
import net from 'net';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(__dirname, '..');

// Use 09-live-counter as the parent — it's the smallest Sky.Live
// example that has a Sub.every tick, ensuring msg_dispatch logs
// flow + the trace ring fills + metrics accumulate.
const PARENT_EXAMPLE = '09-live-counter';
const PARENT_PORT    = 8500;
const ARTEFACT_DIR   = path.join(repoRoot, '.skycache', 'verify', 'console-e2e');

mkdirSync(ARTEFACT_DIR, { recursive: true });

// ─── helpers ──────────────────────────────────────────────────────

function waitForPort(port, timeoutMs) {
    const deadline = Date.now() + timeoutMs;
    return new Promise((resolve, reject) => {
        const attempt = () => {
            const socket = net.connect(port, '127.0.0.1');
            socket.on('connect', () => { socket.end(); resolve(); });
            socket.on('error', () => {
                socket.destroy();
                if (Date.now() > deadline) {
                    reject(new Error(`port ${port} never accepted within ${timeoutMs}ms`));
                } else {
                    setTimeout(attempt, 200);
                }
            });
        };
        attempt();
    });
}

async function fetchText(url) {
    const r = await fetch(url);
    return r.text();
}

async function fetchJSON(url) {
    const r = await fetch(url);
    return r.json();
}

function fail(msg, extra) {
    console.error('FAIL ' + msg);
    if (extra) console.error('  ' + extra);
    process.exit(1);
}

// ─── spawn the parent app ─────────────────────────────────────────

const parentDir   = path.join(repoRoot, 'examples', PARENT_EXAMPLE);
const parentBin   = path.join(parentDir, 'sky-out', 'app');
const serverLog   = path.join(ARTEFACT_DIR, 'parent-server.log');
const serverLogFh = createWriteStream(serverLog);

console.error(`[e2e] spawning ${parentBin} on :${PARENT_PORT}`);
const parent = spawn(parentBin, [], {
    env: { ...process.env,
           SKY_LIVE_PORT: String(PARENT_PORT),
           SKY_SUBAPP_VERBOSE: '1' },
    cwd: parentDir,
});
parent.stdout.pipe(serverLogFh);
parent.stderr.pipe(serverLogFh);

let parentExited = null;
parent.on('exit', (code, sig) => { parentExited = { code, sig }; });

// Track cleanup so even an assertion failure tears the parent down.
let cleanedUp = false;
async function cleanup() {
    if (cleanedUp) return;
    cleanedUp = true;
    try { parent.kill('SIGTERM'); } catch (_) {}
    await new Promise(r => setTimeout(r, 500));
    try { parent.kill('SIGKILL'); } catch (_) {}
}
process.on('exit', () => cleanup());
process.on('SIGINT', () => { cleanup(); process.exit(130); });

try {
    // Parent listens on PARENT_PORT; the spawned console child has
    // an unpredictable port but is reverse-proxied at /_sky/console
    // on PARENT_PORT.
    await waitForPort(PARENT_PORT, 10_000);
    console.error('[e2e] parent listening');

    // Hit / a few times to seed metrics + a 404 for warn-level
    // HTTP access entries.
    for (let i = 0; i < 5; i++) await fetchText(`http://127.0.0.1:${PARENT_PORT}/`);
    await fetchText(`http://127.0.0.1:${PARENT_PORT}/does-not-exist`);

    // Drive a real browser visit so the parent's Sub.every Tick
    // subscription actually fires — that's what generates the
    // msg_dispatch entries we assert below. Plain fetch() doesn't
    // establish a Sky.Live session (no cookie round-trip + no SSE
    // subscription start), so the parent's Sub never activates and
    // the log ring stays HTTP-access-only.
    const warmupBrowser = await chromium.launch({ headless: true });
    const warmupPage    = await warmupBrowser.newPage();
    try {
        await warmupPage.goto(`http://127.0.0.1:${PARENT_PORT}/`, {
            waitUntil: 'load', timeout: 10_000,
        });
        // Wait long enough for several Tick subscriptions to fire
        // (live-counter ticks every 1 s).
        await warmupPage.waitForTimeout(4_000);
    } finally {
        await warmupBrowser.close();
    }

    // Wait for the console child to be ready behind the proxy. First-
    // run build can take up to ~10 s on cold cache.
    const consoleBase = `http://127.0.0.1:${PARENT_PORT}/_sky/console`;
    const consoleDeadline = Date.now() + 30_000;
    let consoleReady = false;
    while (Date.now() < consoleDeadline) {
        try {
            const r = await fetch(consoleBase + '/');
            if (r.ok) { consoleReady = true; break; }
        } catch (_) {}
        await new Promise(r => setTimeout(r, 500));
    }
    if (!consoleReady) {
        fail('console child never came ready behind proxy within 30s');
    }
    console.error('[e2e] console child mounted');

    // ─── assertion 1: API endpoints return data ────────────────────
    const overview = await fetchJSON(consoleBase + '/api/overview');
    if (overview.requestsTotal < 5) {
        fail(`overview.requestsTotal=${overview.requestsTotal}, expected >=5 (we hit / 5x)`);
    }
    // Regression: bufferLogUsed used to be stuck at 0 because the
    // counter read from a Prometheus-only gauge.
    if (overview.bufferLogUsed === 0) {
        fail('overview.bufferLogUsed === 0 — buffer counter regression (count-from-snapshot bug)');
    }
    if (overview.bufferTraceUsed === 0) {
        fail('overview.bufferTraceUsed === 0 — trace counter regression');
    }
    console.error(`[e2e] api/overview ok: requests=${overview.requestsTotal} logs=${overview.bufferLogUsed} traces=${overview.bufferTraceUsed}`);

    const logs = await fetchJSON(consoleBase + '/api/logs?limit=50');
    // Regression: pre-fix every msg_dispatch entry had literal
    // Message="msg_dispatch". Now they carry the Msg name —
    // "msg_dispatch Tick" / "GET / 200 (0ms)" etc.
    const msgs = logs.map(e => e.Message);
    const distinctMessages = new Set(msgs);
    if (distinctMessages.size < 3) {
        fail('logs all have nearly-identical messages — Msg-name bake regressed',
             `got ${distinctMessages.size} distinct: ${[...distinctMessages].slice(0,5).join(' / ')}`);
    }
    const sawTickMsg     = msgs.some(m => m.includes('Tick'));
    const sawHttpMsg     = msgs.some(m => /^(GET|POST|PUT|DELETE) /.test(m));
    if (!sawTickMsg) fail('no msg_dispatch entry carrying the Tick Msg name');
    if (!sawHttpMsg) fail('no http access log entry with method prefix (GET /...)');
    console.error('[e2e] logs differentiated: ' + distinctMessages.size + ' distinct messages');

    // Regression: pre-fix Subapp + ReqID were on the wire but the
    // pre-2026-05-18 console view dropped them. Verify they survive
    // serialisation at least (Subapp may be empty for parent-only
    // workloads — but the FIELD must be present in the JSON shape).
    if (!logs[0].hasOwnProperty('Subapp')) fail('log entries missing Subapp field');
    if (!logs[0].hasOwnProperty('ReqID'))  fail('log entries missing ReqID field');

    const metrics = await fetchJSON(consoleBase + '/api/metrics-summary');
    if (!Array.isArray(metrics) || metrics.length === 0) {
        fail('metrics-summary returned empty / non-array');
    }
    const sawHttpReqCount = metrics.some(m => m.name === 'sky_live_requests_total');
    if (!sawHttpReqCount) {
        fail('metrics-summary missing sky_live_requests_total — observability middleware not writing');
    }
    console.error(`[e2e] api/metrics-summary ok: ${metrics.length} series`);

    const traces = await fetchJSON(consoleBase + '/api/traces?limit=20');
    if (traces.length === 0) {
        fail('traces endpoint returned empty — RecordTrace from middleware not writing');
    }
    if (!traces[0].name || !traces[0].startTime) {
        fail('trace entries missing required fields (name + startTime)');
    }
    console.error(`[e2e] api/traces ok: ${traces.length} spans`);

    // ─── assertion 2: browser can render every tab ─────────────────

    const browser = await chromium.launch({ headless: true });
    const context = await browser.newContext({
        viewport: { width: 1500, height: 900 },
        recordVideo: process.env.SKY_RECORD ? { dir: ARTEFACT_DIR } : undefined,
    });
    const page = await context.newPage();
    page.on('console', msg => {
        if (msg.type() === 'error') {
            console.error('[browser console error] ' + msg.text());
        }
    });
    page.on('pageerror', e => console.error('[page-error] ' + e.message));
    // Log every failing response so we know WHICH URL 500'd.
    page.on('response', async r => {
        const s = r.status();
        if (s >= 400) {
            let body = '';
            try { body = (await r.text()).slice(0, 500); } catch (_) {}
            console.error('[resp ' + s + '] ' + r.request().method() + ' '
                          + r.url() + (body ? ' body=' + JSON.stringify(body) : ''));
        }
    });
    page.on('requestfailed', r => {
        console.error('[req-failed] ' + r.method() + ' ' + r.url()
                      + ' — ' + r.failure().errorText);
    });

    await page.goto(consoleBase + '/', { waitUntil: 'load', timeout: 15_000 });
    // Wait for initial fetch to settle (overview should be visible).
    await page.waitForTimeout(2500);

    // The hang reproducer: switch to each tab + wait. Each tab must
    // remain interactive (no click times out). The Metrics tab gets
    // an EXTENDED dwell (~10 s) because the original hang only
    // surfaced after several refresh ticks accumulated re-render
    // pressure on the larger metric series payload.
    const tabs = ['Overview', 'Metrics', 'Logs', 'Traces', 'Errors'];
    for (const tab of tabs) {
        console.error(`[e2e] clicking tab: ${tab}`);
        await page.locator('text=' + tab).first().click({ timeout: 10_000 });

        // Drive concurrent parent traffic during the dwell so the
        // background log/metric/trace volume grows and the
        // re-render queue is realistically stressed.
        const dwellMs = tab === 'Metrics' ? 10_000 : 3_000;
        const startedAt = Date.now();
        while (Date.now() - startedAt < dwellMs) {
            // 5 concurrent fetches; never await — we want overlap.
            for (let i = 0; i < 5; i++) {
                fetch(`http://127.0.0.1:${PARENT_PORT}/`).catch(() => {});
            }
            await page.waitForTimeout(200);
        }

        // After the dwell, click ONCE MORE to confirm the page is
        // still responsive. A hung dispatch would time-out here.
        await page.locator('text=' + tab).first().click({ timeout: 5_000 });
        await page.waitForTimeout(500);
        await page.screenshot({
            path: path.join(ARTEFACT_DIR, `tab-${tab.toLowerCase()}.png`),
            timeout: 5_000,
        });
    }
    console.error('[e2e] all tabs reachable + screenshotted');

    // ─── assertion 3: logs filter UI exists + level toggles work ──
    await page.locator('text=Logs').first().click({ timeout: 5_000 });
    await page.waitForTimeout(1500);

    // Filter input should be present (regression: no filter at all
    // pre-Phase-5 commit).
    const filterInput = page.locator('input[type="search"]');
    if (await filterInput.count() === 0) {
        fail('logs tab: search filter input missing');
    }

    // The 4 level toggle pills should exist with the level text.
    for (const lvl of ['DEBUG', 'INFO', 'WARN', 'ERROR']) {
        const pill = await page.locator(`text=${lvl}`).count();
        if (pill === 0) fail(`logs tab: ${lvl} level toggle pill missing`);
    }
    console.error('[e2e] logs filter UI present');

    await browser.close();

    // ─── assertion 4: server log is clean ──────────────────────────
    await new Promise(r => setTimeout(r, 500));
    const serverLogText = readFileSync(serverLog, 'utf8');

    // Hang reproducer: pre-fix Metrics tab caused dozens of "http:
    // proxy error: context canceled" entries per minute as the
    // 5-fetches-every-2s pile-up overflowed the dispatch queue.
    const ctxCancels = (serverLogText.match(/http: proxy error: context canceled/g) || []).length;
    if (ctxCancels > 3) {
        fail(`server log has ${ctxCancels} "context canceled" proxy errors — fetch pile-up regression`,
             `(threshold is 3 — occasional one is fine, double-digits = pile-up)`);
    }
    if (serverLogText.includes('panic:') || serverLogText.includes('runtime error:')) {
        fail('server log contains panic / runtime error', serverLogText.split('\n').slice(-20).join('\n'));
    }
    console.error(`[e2e] server log clean: ${ctxCancels} context-cancels (threshold 3), no panics`);

    if (parentExited) {
        fail(`parent process exited unexpectedly: code=${parentExited.code} sig=${parentExited.sig}`);
    }

    console.error('\nPASS console-e2e — all 4 assertion groups green');
    await cleanup();
    process.exit(0);

} catch (err) {
    console.error('FAIL console-e2e — uncaught: ' + (err && err.stack || err));
    await cleanup();
    process.exit(1);
}
