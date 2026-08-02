// Sky.Live RESILIENCE end-to-end test (browser / Playwright).
//
// These are the paths hardened in v0.19.4-7 that were only UNIT-tested
// — never verified in a real browser against the real runtime wire.
// They reproduce two production incidents:
//
//   1. IDLE-SURVIVAL — the darraghstudio "idle 20-30min → reconnecting /
//      disconnected → refresh fixes it" incident. A stateful session held
//      idle PAST its TTL under a live SSE connection must SURVIVE: the SSE
//      heartbeat slides the server-side TTL (touchLastSeen) and the
//      sliding session cookie keeps the browser's cookie window open, so
//      the next interaction still dispatches against the same Model.
//      If the Model is lost after idle, the client hard-reloads (init →
//      state reset) — that is the bug.
//
//   2. DESYNC-RECOVERY — the handler-map-drift-after-redeploy strand.
//      When the client's DOM references a handler id the current server
//      view no longer has (a deploy changed the view, or the DOM went
//      stale after an SSE drop), the server must reply
//      `X-Sky-Status: desync` + a fresh re-render of the CURRENT view.
//      The client soft-resyncs the DOM inline (NO full reload), drops the
//      un-dispatchable action, and the NEXT interaction round-trips
//      normally. If the client strands (spinner / no-op clicks) instead
//      of healing, that is the bug.
//
// Both drive the REAL browser client against the REAL runtime, so they
// exercise the CSRF double-submit, the SSE lifecycle, the desync
// classification header, and the client's response handler end-to-end —
// none of which the Go unit tests cover.
//
// Usage:  node scripts/verify-live-resilience.mjs [scenario]
//           scenario: "idle" | "desync" | "all" (default "all")
//
// Env knobs:
//   SKY_RESILIENCE_IDLE_MS   idle hold for scenario 1 (default 72000).
//                            MUST exceed the memory-store cleanup cadence
//                            (60s) so the idle window crosses at least one
//                            eviction tick — otherwise the test can't
//                            actually fail on a broken heartbeat.
//   SKY_RESILIENCE_TTL       session TTL for scenario 1 (default "25s").
//                            Chosen > the 15s SSE heartbeat interval so a
//                            working heartbeat keeps lastSeen younger than
//                            the TTL at every cleanup tick; a BROKEN
//                            heartbeat lets it age past TTL → eviction.
//   SKY_SKY_BIN              override the sky compiler binary used to build
//                            the fixture (default: release binary, then PATH).
//
// Exits 0 on success, non-zero on the first failed scenario.

import { spawn, spawnSync } from 'child_process';
import { chromium } from 'playwright';
import { mkdirSync, existsSync, createWriteStream, readFileSync } from 'fs';
import { fileURLToPath } from 'url';
import path from 'path';
import net from 'net';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(__dirname, '..');

const FIXTURE_DIR = path.join(repoRoot, 'scripts', 'fixtures', 'live-resilience');
const FIXTURE_BIN = path.join(FIXTURE_DIR, 'sky-out', 'app');
const ARTEFACT_DIR = path.join(repoRoot, '.skycache', 'verify', 'live-resilience');
mkdirSync(ARTEFACT_DIR, { recursive: true });

const IDLE_MS = parseInt(process.env.SKY_RESILIENCE_IDLE_MS || '72000', 10);
const TTL = process.env.SKY_RESILIENCE_TTL || '25s';

const which = process.argv[2] || 'all';

// ─── helpers ──────────────────────────────────────────────────────

function log(m) { console.error('[resilience] ' + m); }

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

function killPort(port) {
    try {
        const r = spawnSync('lsof', ['-ti', ':' + port], { encoding: 'utf8' });
        const pids = (r.stdout || '').split('\n').map(s => s.trim()).filter(Boolean);
        for (const pid of pids) { try { process.kill(parseInt(pid, 10), 'SIGKILL'); } catch (_) {} }
    } catch (_) {}
}

function skyBin() {
    const cand = process.env.SKY_SKY_BIN
        || '/Users/anzel/.cargo/bin/release/sky';
    if (existsSync(cand)) return cand;
    return 'sky'; // PATH fallback
}

function ensureFixtureBuilt() {
    if (existsSync(FIXTURE_BIN)) {
        log('fixture binary present: ' + FIXTURE_BIN);
        return;
    }
    log('building fixture with ' + skyBin() + ' ...');
    const r = spawnSync(skyBin(), ['build', 'src/Main.sky'], {
        cwd: FIXTURE_DIR, encoding: 'utf8', timeout: 360_000,
    });
    if (r.status !== 0 || !existsSync(FIXTURE_BIN)) {
        throw new Error('fixture build failed:\n' + (r.stdout || '') + (r.stderr || ''));
    }
    log('fixture built.');
}

// Boot the fixture server, return { child, logPath, panics() }.
function bootServer(port, extraEnv, tag) {
    killPort(port);
    const logPath = path.join(ARTEFACT_DIR, `server-${tag}.log`);
    const logFh = createWriteStream(logPath);
    const child = spawn(FIXTURE_BIN, [], {
        cwd: FIXTURE_DIR,
        env: { ...process.env, SKY_LIVE_PORT: String(port), PORT: String(port), ...extraEnv },
    });
    child.stdout.pipe(logFh);
    child.stderr.pipe(logFh);
    return {
        child,
        logPath,
        panics() {
            let txt = '';
            try { txt = readFileSync(logPath, 'utf8'); } catch (_) {}
            const pats = [/panic:/i, /runtime error:/i, /goroutine \d+ \[/, /interface conversion:/];
            return pats.flatMap(re => { const m = txt.match(re); return m ? [m[0]] : []; });
        },
    };
}

async function stopServer(srv) {
    if (!srv || !srv.child) return;
    try { srv.child.kill('SIGTERM'); } catch (_) {}
    await new Promise(r => setTimeout(r, 400));
    try { if (!srv.child.killed) srv.child.kill('SIGKILL'); } catch (_) {}
}

// Attach console + response capture to a page. Returns live arrays.
function instrument(page) {
    const consoleMsgs = [];
    const pageErrors = [];
    const eventResponses = []; // { status, skyStatus }
    page.on('console', m => consoleMsgs.push({ type: m.type(), text: m.text() }));
    page.on('pageerror', e => pageErrors.push(e.message));
    page.on('response', res => {
        if (res.url().includes('/_sky/event')) {
            eventResponses.push({
                status: res.status(),
                skyStatus: res.headers()['x-sky-status'] || '',
            });
        }
    });
    return { consoleMsgs, pageErrors, eventResponses };
}

async function readCount(page) {
    const t = await page.locator('#count-value').innerText();
    return parseInt(t.trim(), 10);
}

// Wait until the client has an open SSE (connected). The status banner
// carries class sky-status--reconnecting / --offline while degraded; a
// connected client leaves it hidden/empty. We poll a short bounded loop.
async function waitConnected(page, timeoutMs) {
    const deadline = Date.now() + timeoutMs;
    while (Date.now() < deadline) {
        const state = await page.evaluate(() => {
            const el = document.getElementById('__sky-status');
            const cls = el ? el.className : '';
            const sseReady = (typeof window.__skySSE !== 'undefined' && window.__skySSE
                && window.__skySSE.readyState === 1);
            return { cls, sseReady };
        });
        // Not degraded and (SSE OPEN or banner simply not shown yet).
        if (!/reconnecting|offline/.test(state.cls)) return true;
        await page.waitForTimeout(300);
    }
    return false;
}

// ─── Scenario 1: idle survival ────────────────────────────────────

async function scenarioIdle(browser) {
    const port = 8781;
    log(`idle-survival: TTL=${TTL}, idle=${IDLE_MS}ms (must cross the ~60s store cleanup tick)`);
    const srv = bootServer(port, { SKY_LIVE_TTL: TTL, SKY_LIVE_STORE: 'memory' }, 'idle');
    let failure = null;
    let context, page;
    try {
        await waitForPort(port, 15_000);
        context = await browser.newContext({ viewport: { width: 1024, height: 768 } });
        page = await context.newPage();
        const cap = instrument(page);

        const base = `http://127.0.0.1:${port}`;
        await page.goto(base, { waitUntil: 'domcontentloaded', timeout: 15_000 });
        await page.waitForSelector('#count-value', { timeout: 10_000 });

        // Plant a sentinel so a client-driven reload is detectable.
        await page.evaluate(() => { window.__resilienceSentinel = 'alive'; });

        // Set some state: increment to 3 + type a note.
        for (let i = 0; i < 3; i++) {
            await page.click('#btn-inc');
            await page.waitForTimeout(150);
        }
        await page.fill('#note-input', 'survive-me');
        await page.waitForTimeout(400); // let onInput debounce round-trip
        // blur to flush any pending debounce
        await page.click('#count-value');
        await page.waitForTimeout(400);

        const beforeCount = await readCount(page);
        if (beforeCount !== 3) throw new Error(`pre-idle count expected 3, got ${beforeCount}`);

        const connected = await waitConnected(page, 8_000);
        if (!connected) throw new Error('SSE never reached a connected state before idle');

        log(`state set (count=3, note="survive-me"); holding idle ${IDLE_MS}ms ...`);
        // Bounded idle. No interaction — the ONLY thing keeping the
        // session alive is the server SSE heartbeat sliding the TTL and
        // the sliding cookie. Any auto-tick would invalidate the test;
        // the fixture has no subscriptions on purpose.
        await page.waitForTimeout(IDLE_MS);

        // Did the client silently reload during idle (session-lost)?
        const sentinel = await page.evaluate(() => window.__resilienceSentinel || null);
        if (sentinel !== 'alive') {
            failure = 'client RELOADED during idle (sentinel gone) — session was lost under a '
                + 'live SSE connection: the heartbeat/sliding-cookie keep-alive FAILED. '
                + 'This is the darraghstudio idle-disconnect bug.';
        }

        // Not stuck in a reconnecting/offline banner.
        const banner = await page.evaluate(() => {
            const el = document.getElementById('__sky-status');
            return el ? el.className + '|' + (el.textContent || '') : '';
        });
        if (!failure && /reconnecting|offline/.test(banner)) {
            failure = `after idle the client shows a degraded banner ("${banner}") — `
                + 'the connection did not stay alive across the idle window.';
        }

        // The decisive assertion: interact again. If the whole keep-alive
        // chain held, the count is still 3 and this increments to 4.
        // Distinguish the failure modes precisely — they point at DIFFERENT
        // mechanisms:
        //   * X-Sky-Status: session-lost  → server EVICTED the session
        //     (the SSE-heartbeat touchLastSeen / sliding sky_sid keep-alive
        //     failed).  count resets via reload.
        //   * HTTP 403                    → the __sky_csrf cookie EXPIRED
        //     during idle.  Its Max-Age is keyed to the session TTL and is
        //     only refreshed on GET / POST — the SSE heartbeat slides the
        //     server session + sky_sid cookie but NOT the CSRF cookie.  So
        //     the session is ALIVE server-side but the client can no longer
        //     POST events → the action is queued + retried (all 403) → the
        //     reconnecting/offline banner → stranded until a manual refresh
        //     re-issues the cookie via GET.  This is the darraghstudio
        //     "idle → disconnected → refresh fixes it" incident.
        //   * count unchanged, no 403/session-lost → some other drop.
        if (!failure) {
            const beforeResp = cap.eventResponses.length;
            await page.click('#btn-inc');
            await page.waitForTimeout(1500);
            const afterCount = await readCount(page);
            const newResps = cap.eventResponses.slice(beforeResp);
            const gotSessionLost = newResps.some(r => r.skyStatus === 'session-lost');
            const got403 = newResps.some(r => r.status === 403);
            const noteAfter = await page.locator('#note-echo').innerText().catch(() => '');

            if (gotSessionLost) {
                failure = 'post-idle interaction returned X-Sky-Status: session-lost — '
                    + 'the server EVICTED the session under a live SSE (heartbeat '
                    + 'touchLastSeen / sliding sky_sid keep-alive FAILED).';
            } else if (got403) {
                failure = 'post-idle event POST returned HTTP 403 (CSRF). The session is '
                    + 'ALIVE server-side (SSE stayed connected, no reload), but the '
                    + '__sky_csrf cookie EXPIRED during idle: its Max-Age is keyed to the '
                    + 'session TTL and it is only refreshed on GET/POST — the SSE heartbeat '
                    + 'slides the server session + sky_sid cookie but NOT the CSRF cookie. '
                    + 'The client can no longer POST → the click is queued/retried (all 403) '
                    + '→ reconnecting banner → stranded until a manual refresh. '
                    + '[darraghstudio idle-disconnect ROOT CAUSE — runtime fix needed: '
                    + 'slide __sky_csrf on the SSE heartbeat, or make its Max-Age outlive '
                    + 'the sliding session TTL. Verified sole cause: SKY_CSRF=off makes this '
                    + 'exact idle survive.]';
            } else if (afterCount !== 4) {
                failure = `post-idle count expected 4 (survived 3 + 1), got ${afterCount}. `
                    + (afterCount === 1
                        ? 'Reset to 0 then +1 → the Model was LOST across idle.'
                        : 'Model did not survive the idle intact (no 403/session-lost seen).');
            } else if (noteAfter.trim() !== 'survive-me') {
                failure = `note field lost across idle: expected "survive-me", got "${noteAfter}".`;
            }
        }

        const panics = srv.panics();
        if (!failure && panics.length) failure = 'server panic: ' + panics.join(', ');
        if (!failure && cap.pageErrors.length) failure = 'pageerror: ' + cap.pageErrors.join('; ');

        await page.screenshot({ path: path.join(ARTEFACT_DIR, 'idle.png') }).catch(() => {});
    } catch (err) {
        failure = 'driver error: ' + err.message;
    } finally {
        if (page) await page.screenshot({ path: path.join(ARTEFACT_DIR, 'idle-final.png') }).catch(() => {});
        if (context) await context.close().catch(() => {});
        await stopServer(srv);
    }
    return failure;
}

// ─── Scenario 2: desync recovery ──────────────────────────────────

async function scenarioDesync(browser) {
    const port = 8782;
    log('desync-recovery: driving a stale handler id through the real client');
    const srv = bootServer(port, { SKY_LIVE_STORE: 'memory' }, 'desync');
    let failure = null;
    let context, page;
    try {
        await waitForPort(port, 15_000);
        context = await browser.newContext({ viewport: { width: 1024, height: 768 } });
        page = await context.newPage();
        const cap = instrument(page);

        const base = `http://127.0.0.1:${port}`;
        await page.goto(base, { waitUntil: 'domcontentloaded', timeout: 15_000 });
        await page.waitForSelector('#count-value', { timeout: 10_000 });
        await page.evaluate(() => { window.__resilienceSentinel = 'alive'; });

        // Set state to 2.
        for (let i = 0; i < 2; i++) {
            await page.click('#btn-inc');
            await page.waitForTimeout(150);
        }
        const beforeCount = await readCount(page);
        if (beforeCount !== 2) throw new Error(`pre-desync count expected 2, got ${beforeCount}`);

        // Confirm the client exposes the wire hook (top-level script, so
        // window.__sky_send is global) — otherwise we can't drive a
        // desync through the real client + CSRF path.
        const hasHook = await page.evaluate(() => typeof window.__sky_send === 'function');
        if (!hasHook) throw new Error('window.__sky_send missing — cannot drive client dispatch');

        // Fire an event against a handler id the server view does NOT have.
        // The session is VALID (real sid + CSRF via __skyPostEvent), so the
        // server classifies this as a VIEW DESYNC: replies X-Sky-Status:
        // desync + a fresh re-render, and the client soft-resyncs inline.
        const respBefore = cap.eventResponses.length;
        await page.evaluate(() => {
            // __sky_send(msgName, args, handlerId) → __skySend → __skyPostEvent
            window.__sky_send('Increment', [], 'sky-999-9.click');
        });
        // Give the round-trip + client heal time to land.
        await page.waitForTimeout(1500);

        const desyncResps = cap.eventResponses.slice(respBefore);
        const sawDesync = desyncResps.some(r => r.status === 200 && r.skyStatus === 'desync');
        if (!sawDesync) {
            failure = 'stale-handler POST did NOT return X-Sky-Status: desync '
                + '(got: ' + JSON.stringify(desyncResps) + '). The server desync '
                + 'classification is not firing — the client would strand on a bare 404.';
        }

        // No full reload — the heal is INLINE.
        if (!failure) {
            const sentinel = await page.evaluate(() => window.__resilienceSentinel || null);
            if (sentinel !== 'alive') {
                failure = 'client RELOADED on desync (sentinel gone) — a desync must '
                    + 'soft-resync INLINE, not hard-reload.';
            }
        }

        // Client logged the desync heal (proves the client-side branch ran).
        if (!failure) {
            const warned = cap.consoleMsgs.some(m => /view desync/i.test(m.text));
            if (!warned) {
                failure = 'client did not log the "view desync" heal — the '
                    + 'X-Sky-Status: desync branch in the client did not execute.';
            }
        }

        // The dropped action did NOT mutate state (count still 2).
        if (!failure) {
            const midCount = await readCount(page);
            if (midCount !== 2) {
                failure = `desync action was not dropped: count moved to ${midCount} (expected 2).`;
            }
        }

        // The decisive assertion: the NEXT real interaction round-trips
        // normally — the user is NOT stranded. Click the real button; the
        // DOM was re-synced so its handler id now matches the server.
        if (!failure) {
            const respBefore2 = cap.eventResponses.length;
            await page.click('#btn-inc');
            await page.waitForTimeout(1200);
            const afterCount = await readCount(page);
            const newResps = cap.eventResponses.slice(respBefore2);
            const healthy = newResps.some(r => r.status === 200 && r.skyStatus !== 'desync' && r.skyStatus !== 'session-lost');
            if (!healthy) {
                failure = 'post-desync interaction did not round-trip cleanly '
                    + '(' + JSON.stringify(newResps) + ') — the client is STRANDED after a desync.';
            } else if (afterCount !== 3) {
                failure = `post-desync count expected 3, got ${afterCount} — the session `
                    + 'did not heal to a working dispatch state.';
            }
        }

        const panics = srv.panics();
        if (!failure && panics.length) failure = 'server panic: ' + panics.join(', ');
        if (!failure && cap.pageErrors.length) failure = 'pageerror: ' + cap.pageErrors.join('; ');

        await page.screenshot({ path: path.join(ARTEFACT_DIR, 'desync.png') }).catch(() => {});
    } catch (err) {
        failure = 'driver error: ' + err.message;
    } finally {
        if (context) await context.close().catch(() => {});
        await stopServer(srv);
    }
    return failure;
}

// ─── main ─────────────────────────────────────────────────────────

async function main() {
    ensureFixtureBuilt();
    const browser = await chromium.launch({ headless: true });
    const results = [];
    try {
        if (which === 'idle' || which === 'all') {
            const f = await scenarioIdle(browser);
            results.push(['idle-survival', f]);
        }
        if (which === 'desync' || which === 'all') {
            const f = await scenarioDesync(browser);
            results.push(['desync-recovery', f]);
        }
    } finally {
        await browser.close().catch(() => {});
    }

    let failed = 0;
    for (const [name, f] of results) {
        if (f) { failed++; console.log(`FAIL ${name} — ${f}`); }
        else { console.log(`PASS ${name}`); }
    }
    console.log(`\nRESILIENCE: ${results.length - failed} pass / ${failed} fail`);
    process.exit(failed === 0 ? 0 : 1);
}

main().catch(err => {
    console.error('FAIL resilience — driver error: ' + err.message);
    console.error(err.stack);
    process.exit(1);
});
