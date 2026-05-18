#!/usr/bin/env node
// v0.13.x runtime verification driver for Sky.Live / Sky.Http.Server apps.
//
// Usage:
//   node scripts/verify-live-app.mjs <example-name> [port] [scenario-name]
//
//   example-name   directory under examples/ (e.g. 09-live-counter)
//   port           HTTP port (default: 8000)
//   scenario-name  optional named scenario (default: "smoke" — load home + screenshot)
//
// Pipeline:
//   1. Spawn `examples/<name>/sky-out/app` with PORT=<port> in env.
//   2. Wait for the server to accept connections on <port>.
//   3. Launch Playwright (headless Chromium), open the home page.
//   4. Run the scenario (default: load + verify no console errors).
//   5. Capture a screenshot (and a trace, if `SKY_TRACE=1`).
//   6. Tail server stderr for `panic` / `runtime error`.
//   7. Kill server, report PASS/FAIL.
//
// Exits 0 on pass, non-zero on any failure.

import { chromium } from 'playwright';
import { spawn } from 'child_process';
import fs from 'fs';
import path from 'path';
import net from 'net';
import { fileURLToPath, pathToFileURL } from 'url';

const __filename = fileURLToPath(import.meta.url);
const __dirname  = path.dirname(__filename);
const repoRoot   = path.resolve(__dirname, '..');

const exampleName  = process.argv[2];
const port         = parseInt(process.argv[3] || '8000', 10);
const scenarioName = process.argv[4] || 'smoke';

if (!exampleName) {
    console.error('usage: node verify-live-app.mjs <example-name> [port] [scenario]');
    process.exit(2);
}

const exampleDir = path.join(repoRoot, 'examples', exampleName);
const binary     = path.join(exampleDir, 'sky-out', 'app');

if (!fs.existsSync(binary)) {
    console.error(`binary missing: ${binary}`);
    console.error('build first: cd examples/' + exampleName + ' && sky build src/Main.sky');
    process.exit(2);
}

// Output artefacts
const artefactDir = path.join(repoRoot, '.skycache', 'verify', exampleName);
fs.mkdirSync(artefactDir, { recursive: true });

// ─── Helpers ────────────────────────────────────────────────────────

function waitForPort(p, timeoutMs) {
    const deadline = Date.now() + timeoutMs;
    return new Promise((resolve, reject) => {
        const tick = () => {
            if (Date.now() > deadline) {
                reject(new Error(`port ${p} never accepted within ${timeoutMs}ms`));
                return;
            }
            const sock = net.connect(p, '127.0.0.1');
            sock.on('connect', () => { sock.end(); resolve(); });
            sock.on('error', () => { setTimeout(tick, 200); });
        };
        tick();
    });
}

async function main() {
    // Spawn server
    const env = { ...process.env, PORT: String(port), SKY_LIVE_PORT: String(port) };
    const serverLogPath = path.join(artefactDir, 'server.log');
    const serverLog = fs.createWriteStream(serverLogPath);
    const child = spawn(binary, [], { env, cwd: exampleDir });
    child.stdout.pipe(serverLog);
    child.stderr.pipe(serverLog);

    let serverExitedEarly = null;
    child.on('exit', (code, signal) => {
        if (signal !== 'SIGTERM' && signal !== 'SIGKILL') {
            serverExitedEarly = { code, signal };
        }
    });

    try {
        await waitForPort(port, 10_000);
    } catch (err) {
        child.kill('SIGKILL');
        // Wait for logs to flush
        await new Promise(r => setTimeout(r, 200));
        const log = fs.readFileSync(serverLogPath, 'utf8');
        console.error(`FAIL ${exampleName} — server failed to listen: ${err.message}`);
        console.error('--- server log ---');
        console.error(log.split('\n').slice(0, 40).join('\n'));
        process.exit(1);
    }

    if (serverExitedEarly) {
        console.error(`FAIL ${exampleName} — server exited early: code=${serverExitedEarly.code} signal=${serverExitedEarly.signal}`);
        console.error(fs.readFileSync(serverLogPath, 'utf8'));
        process.exit(1);
    }

    // Playwright
    const browser = await chromium.launch({ headless: true });
    const context = await browser.newContext({
        viewport: { width: 1280, height: 720 },
        recordVideo: process.env.SKY_RECORD ? { dir: artefactDir } : undefined,
    });
    if (process.env.SKY_TRACE) {
        await context.tracing.start({ screenshots: true, snapshots: true });
    }
    const page = await context.newPage();

    const consoleErrors = [];
    page.on('console', msg => {
        if (msg.type() === 'error') {
            const loc = msg.location();
            const where = loc && loc.url ? ` (${loc.url})` : '';
            consoleErrors.push(msg.text() + where);
        }
    });
    page.on('pageerror', err => consoleErrors.push(`pageerror: ${err.message}`));

    // Track failed network requests to distinguish benign 404s (favicon,
    // service-worker probes) from real app failures.
    const networkFailures = [];
    page.on('response', res => {
        const status = res.status();
        if (status >= 400) {
            networkFailures.push(`${status} ${res.url()}`);
        }
    });

    // v0.13.2: hard-fail probe to catch the "click is a no-op" class
    // (Std.Ui events stripped at render time → button has no
    // sky-click attr → DOM click never POSTs /_sky/event).  Watch
    // every /_sky/event POST; the scenario runner asserts at least
    // one round-trip on apps with interactive scenarios so a silent
    // regression in the event-emission pipeline can't ship.
    const skyEventPosts = [];
    page.on('request', req => {
        if (req.method() === 'POST' && req.url().includes('/_sky/event')) {
            skyEventPosts.push({
                url: req.url(),
                postData: req.postData() || '',
                ts: Date.now(),
            });
        }
    });

    let outcome = 'PASS';
    let detail = '';
    try {
        const baseUrl = `http://127.0.0.1:${port}`;

        // Default smoke scenario: load home, screenshot, ensure no console error.
        await page.goto(baseUrl, { waitUntil: 'domcontentloaded', timeout: 10_000 });
        await page.waitForLoadState('networkidle', { timeout: 5_000 }).catch(() => {});

        // Helper for animated walk-through interactions when recording.
        const pause = (ms) => page.waitForTimeout(ms);

        // Load the per-app scenario module (defined in verify-scenarios.mjs).
        // Scenario name maps directly to the exported function name.
        const scenarioMod = await import(pathToFileURL(path.join(__dirname, 'verify-scenarios.mjs')).href);
        const scenarios = scenarioMod.scenarios || scenarioMod.default;
        const scenarioFn = scenarios[scenarioName];
        if (typeof scenarioFn !== 'function') {
            throw new Error(`unknown scenario: ${scenarioName} (known: ${Object.keys(scenarios).join(', ')})`);
        }
        await scenarioFn(page, { baseUrl, pause, skyEventPosts });

        await page.screenshot({ path: path.join(artefactDir, 'home.png'), fullPage: false });

        // v0.13.2: structural HTML assertion — if the rendered home
        // page has <button> or <form>, it MUST have at least one
        // `sky-(click|input|change|submit)=` attribute. A silent
        // event-dropping regression (Std.Ui types coerced to []any
        // returning nil from AsListT[any] pre-v0.13.2) would render
        // buttons without any sky-* event marker.  Scenario-runner
        // probes confirm the round-trip; this check catches even
        // scenarios that don't exercise a click but still have an
        // event-bearing UI.
        const homeHtml = await page.content();
        const hasInteractive = /<button|<form/i.test(homeHtml);
        const hasSkyEvent = /sky-(click|input|change|submit)="/i.test(homeHtml);
        if (hasInteractive && !hasSkyEvent && outcome === 'PASS') {
            outcome = 'FAIL';
            detail = 'rendered HTML has <button>/<form> but ZERO sky-event '
                + 'attributes — events stripped at render time (probable '
                + 'Std.Ui → []any coercion regression)';
        }

        // Console-error filter — treat 404s on common static-asset paths
        // (favicon.ico, robots.txt, apple-touch-icon, manifest.json) as
        // BENIGN. They're browser auto-probes, not app failures. Any
        // other console error counts.
        const benignAssetRe = /(favicon\.ico|robots\.txt|apple-touch-icon|manifest\.json|sitemap\.xml)/i;
        const realConsoleErrors = consoleErrors.filter(e => !benignAssetRe.test(e));
        if (realConsoleErrors.length > 0) {
            outcome = 'FAIL';
            detail = `console errors: ${realConsoleErrors.slice(0, 5).join('; ')}`;
        }

        // Write network log for forensic review (didn't fail on this
        // alone; the app-level outcome is the structural + console-error
        // signal).
        if (networkFailures.length > 0) {
            fs.writeFileSync(
                path.join(artefactDir, 'network-failures.log'),
                networkFailures.join('\n')
            );
        }
    } catch (err) {
        outcome = 'FAIL';
        detail = `playwright: ${err.message}`;
        await page.screenshot({ path: path.join(artefactDir, 'error.png'), fullPage: false }).catch(() => {});
    }

    if (process.env.SKY_TRACE) {
        await context.tracing.stop({ path: path.join(artefactDir, 'trace.zip') });
    }
    await browser.close();

    // Kill server gracefully
    child.kill('SIGTERM');
    await new Promise(r => setTimeout(r, 500));
    if (!child.killed) child.kill('SIGKILL');

    // Tail server log for panic / runtime error
    const log = fs.readFileSync(serverLogPath, 'utf8');
    const panicPatterns = [
        /panic:/i,
        /runtime error:/i,
        /goroutine \d+ \[/,  // any goroutine stack trace
        /interface conversion:/,
    ];
    const panics = panicPatterns.flatMap(re => {
        const m = log.match(re);
        return m ? [m[0]] : [];
    });
    if (panics.length > 0) {
        outcome = 'FAIL';
        detail = (detail ? detail + '; ' : '') + 'server panics: ' + panics.join(', ');
    }

    if (outcome === 'PASS') {
        console.log(`PASS ${exampleName} (port ${port}, scenario ${scenarioName})`);
        process.exit(0);
    } else {
        console.error(`FAIL ${exampleName} — ${detail}`);
        console.error('artefacts in ' + artefactDir);
        console.error('--- last 30 lines of server.log ---');
        console.error(log.split('\n').slice(-30).join('\n'));
        process.exit(1);
    }
}

main().catch(err => {
    console.error('FAIL ' + exampleName + ' — driver error: ' + err.message);
    console.error(err.stack);
    process.exit(1);
});
