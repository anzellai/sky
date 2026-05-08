#!/usr/bin/env node
// scripts/verify-examples.mjs
//
// Local-only end-to-end smoke for examples — drives a real Chromium
// via Playwright, takes a screenshot of every server example, and
// captures any console errors / failed-request log lines. The aim
// is to catch regressions where the example BUILDS clean but
// renders wrong (blank page, JS exception in __sky*, dead Cmd.perform
// dispatch, etc.) — `sky verify` only checks HTTP 200 on /, which
// the symptom would slip past.
//
// Output: _verify/<example>/{screenshot.png,console.log,page.html}
// Gitignored. Local per-developer; not source.
//
// Usage:
//   node scripts/verify-examples.mjs              # every server example
//   node scripts/verify-examples.mjs 09 12 19     # specific examples (prefix-match)
//
// Requires: playwright + chromium installed (`npx playwright install chromium`).
// The harness installs neither — same philosophy as scripts/mem-guard.sh
// (a developer-tooling script you run yourself, not part of CI).

import { chromium } from 'playwright';
import { spawn } from 'node:child_process';
import { mkdir, writeFile, rm, access } from 'node:fs/promises';
import { join, resolve } from 'node:path';
import process from 'node:process';

const ROOT = resolve(new URL('..', import.meta.url).pathname);
const SKY = join(ROOT, 'sky-out', 'sky');
const OUT = join(ROOT, '_verify');

// Per-example contract — stays small + declarative. Two shapes:
//
//   server:
//     `port`     — what the binary listens on (sky.toml [live] port or
//                  hardcoded for raw http servers).
//     `path`     — page to navigate after the server is up (default '/').
//     `actions`  — optional list of click/type pairs to drive a primary
//                  UI flow before screenshotting.
//
//   cli:
//     `cli: true` — run the binary, capture exit + stdout, no browser.
//     `args`      — argv to pass (default []).
//     `stdin`     — optional string fed to the process's stdin.
//     `expectStdout` — substring that must appear in stdout for pass.
//     `expectExit`  — exit code to expect (default 0).
//
// The 11-fyne-stopwatch entry is omitted — it's a desktop GUI app
// that needs a windowing system; verified only via build.
const EXAMPLES = {
    '01-hello-world':   { cli: true, expectStdout: 'Hello' },
    '02-go-stdlib':     { cli: true },
    '03-tea-external':  { cli: true, expectStdout: 'UUID' },
    '04-local-pkg':     { cli: true },
    '05-mux-server':    { port: 8000, path: '/' },
    '06-json':          { cli: true },
    '07-todo-cli':      { cli: true, args: ['list'] },
    '08-notes-app':     { port: 8000, path: '/' },
    '09-live-counter':  { port: 8000, path: '/',
                          actions: [{ click: 'button:has-text("+")', count: 3 }] },
    '10-live-component': { port: 8000, path: '/' },
    '12-skyvote':       { port: 8000, path: '/' },
    '13-skyshop':       { port: 8000, path: '/',
                          actions: [
                              { click: 'a:has-text("Browse Products"), button:has-text("Browse Products")' },
                              { click: 'a:has-text("test product"), [class*="product"] a' },
                              { click: 'button:has-text("Add to Cart"), button:has-text("Add"):not(:has-text("admin"))' },
                          ] },
    '14-task-demo':     { cli: true },
    '15-http-server':   { port: 8000, path: '/' },
    '16-skychess':      { port: 8000, path: '/' },
    '17-skymon':        { port: 8000, path: '/' },
    '18-job-queue':     { port: 8000, path: '/' },
    '19-skyforum':      { port: 8000, path: '/' },
};


function args() {
    const filt = process.argv.slice(2);
    if (filt.length === 0) return Object.keys(EXAMPLES);
    return Object.keys(EXAMPLES).filter(n =>
        filt.some(f => n.startsWith(f) || n.includes(f)));
}


async function exists(p) {
    try { await access(p); return true; } catch { return false; }
}


async function buildExample(dir) {
    return new Promise((resolveProc, rejectProc) => {
        const ps = spawn(SKY, ['build', 'src/Main.sky'], {
            cwd: dir,
            env: { ...process.env, PATH: process.env.PATH },
        });
        let out = '';
        ps.stdout.on('data', c => { out += c.toString(); });
        ps.stderr.on('data', c => { out += c.toString(); });
        ps.on('close', code => code === 0
            ? resolveProc(out)
            : rejectProc(new Error('sky build failed:\n' + out)));
    });
}


async function waitForPort(port, timeoutMs = 15000) {
    const deadline = Date.now() + timeoutMs;
    while (Date.now() < deadline) {
        try {
            const ctrl = new AbortController();
            const t = setTimeout(() => ctrl.abort(), 1000);
            const res = await fetch(`http://localhost:${port}/`, {
                signal: ctrl.signal,
            }).catch(() => null);
            clearTimeout(t);
            if (res && res.status < 500) return true;
        } catch { /* loop */ }
        await new Promise(r => setTimeout(r, 250));
    }
    throw new Error(`port ${port} never opened`);
}


// CLI verification — spawn the binary, gather stdout/stderr, check
// exit code + optional stdout substring. No browser; no port wait.
async function verifyCli(name, cfg, dir, bin, out) {
    return new Promise((resolveCli) => {
        const ps = spawn(bin, cfg.args ?? [], { cwd: dir });
        let buf = '';
        ps.stdout.on('data', c => { buf += c.toString(); });
        ps.stderr.on('data', c => { buf += c.toString(); });
        if (cfg.stdin !== undefined) {
            ps.stdin.write(cfg.stdin);
            ps.stdin.end();
        }
        // Cap CLI runtime at 15s — anything that doesn't terminate
        // by then is treated as a hang. Sky CLI examples all
        // complete in well under 1s.
        const killer = setTimeout(() => {
            try { ps.kill('SIGKILL'); } catch {}
        }, 15000);
        ps.on('close', async (code) => {
            clearTimeout(killer);
            await writeFile(join(out, 'cli.log'), buf);
            const expectExit = cfg.expectExit ?? 0;
            const exitOk = code === expectExit;
            const substrOk = !cfg.expectStdout || buf.includes(cfg.expectStdout);
            if (exitOk && substrOk) {
                resolveCli({ name, ok: true, stage: 'done' });
            } else {
                const why = !exitOk
                    ? `exit ${code} (expected ${expectExit})`
                    : `stdout missing "${cfg.expectStdout}"`;
                resolveCli({ name, ok: false, stage: 'cli', err: why });
            }
        });
    });
}


async function killTree(child) {
    if (!child || child.killed) return;
    try { child.kill('SIGTERM'); } catch {}
    // Give it a graceful second, then SIGKILL.
    await new Promise(r => setTimeout(r, 800));
    try { child.kill('SIGKILL'); } catch {}
}


async function verify(name, cfg, browser) {
    const dir = join(ROOT, 'examples', name);
    const out = join(OUT, name);
    await rm(out, { recursive: true, force: true });
    await mkdir(out, { recursive: true });

    const log = (s) => writeFile(join(out, 'verify.log'),
        s, { flag: 'a' });

    // 1. Build
    try {
        const buildOut = await buildExample(dir);
        await log(`# build\n${buildOut}\n`);
    } catch (e) {
        await log(`# build FAILED\n${e.message}\n`);
        return { name, ok: false, stage: 'build', err: e.message };
    }

    const bin = join(dir, 'sky-out', 'app');
    if (!(await exists(bin))) {
        await log(`# missing binary: ${bin}\n`);
        return { name, ok: false, stage: 'spawn', err: 'no binary' };
    }

    // CLI examples: run binary, capture stdout, check exit + substring.
    if (cfg.cli) {
        return verifyCli(name, cfg, dir, bin, out);
    }
    const child = spawn(bin, [], {
        cwd: dir,
        env: { ...process.env, SKY_LIVE_PORT: String(cfg.port) },
    });
    let appLog = '';
    child.stdout.on('data', c => { appLog += c.toString(); });
    child.stderr.on('data', c => { appLog += c.toString(); });

    let result = { name, ok: false, stage: 'unknown' };
    try {
        await waitForPort(cfg.port);

        // 3. Navigate + capture
        const ctx = await browser.newContext({
            ignoreHTTPSErrors: true,
            viewport: { width: 1280, height: 800 },
        });
        const page = await ctx.newPage();

        const consoleErrors = [];
        page.on('console', m => {
            if (m.type() === 'error') consoleErrors.push(m.text());
        });
        page.on('pageerror', e => consoleErrors.push('pageerror: ' + e.message));

        const url = `http://localhost:${cfg.port}${cfg.path || '/'}`;
        await page.goto(url, { waitUntil: 'domcontentloaded', timeout: 10000 });

        // Sky.Live runtime takes a brief moment to wire the SSE
        // hello. Wait for the connection-status banner (if injected)
        // OR a 1s settle, whichever first.
        await Promise.race([
            page.waitForFunction(
                () => window.__skySid !== undefined,
                { timeout: 4000 }
            ).catch(() => null),
            new Promise(r => setTimeout(r, 1000)),
        ]);

        // 4. Optional UI actions
        if (cfg.actions) {
            for (const a of cfg.actions) {
                if (a.click) {
                    const n = a.count ?? 1;
                    for (let i = 0; i < n; i++) {
                        await page.locator(a.click).first().click({
                            timeout: 3000,
                        }).catch(() => null);
                        await page.waitForTimeout(150);
                    }
                }
                if (a.fill) {
                    await page.locator(a.fill.locator).first()
                        .fill(a.fill.value, { timeout: 3000 }).catch(() => null);
                }
            }
            // Settle any post-action SSE reconciliation.
            await page.waitForTimeout(500);
        }

        // 5. Snapshot
        const html = await page.content();
        await writeFile(join(out, 'page.html'), html);
        await page.screenshot({
            path: join(out, 'screenshot.png'),
            fullPage: true,
        });
        await writeFile(join(out, 'console.log'),
            consoleErrors.join('\n') + (consoleErrors.length ? '\n' : ''));
        await ctx.close();

        // 6. Pass criteria — ignore noisy resource-loading errors
        // (broken image URLs in dev fixtures, third-party trackers,
        // favicon, cookie warnings). Treat real JS exceptions and
        // pageerror events as fatal.
        const failed = consoleErrors.filter(e =>
            !/Cookie .* will be soon rejected/i.test(e) &&
            !/favicon/i.test(e) &&
            !/Failed to load resource/i.test(e));
        result = failed.length === 0
            ? { name, ok: true, stage: 'done' }
            : { name, ok: false, stage: 'console', err: failed[0] };
    } catch (e) {
        result = { name, ok: false, stage: 'navigate', err: e.message };
    } finally {
        await writeFile(join(out, 'app.log'), appLog);
        await killTree(child);
    }
    return result;
}


async function main() {
    const wanted = args();
    if (wanted.length === 0) {
        console.error('No matching examples.');
        process.exit(2);
    }

    await mkdir(OUT, { recursive: true });
    const browser = await chromium.launch({ headless: true });

    const results = [];
    for (const name of wanted) {
        process.stdout.write(`[verify] ${name} … `);
        const r = await verify(name, EXAMPLES[name], browser);
        results.push(r);
        process.stdout.write(r.ok
            ? 'ok\n'
            : `FAIL (${r.stage}: ${r.err ?? ''})\n`);
    }

    await browser.close();

    const ok = results.filter(r => r.ok).length;
    const fail = results.length - ok;
    console.log('');
    console.log(`# ${ok}/${results.length} passed`);
    if (fail) {
        console.log('Failed:');
        for (const r of results.filter(x => !x.ok)) {
            console.log(`  - ${r.name}: ${r.stage}: ${r.err ?? ''}`);
        }
        process.exit(1);
    }
}


main().catch(e => {
    console.error(e);
    process.exit(2);
});
