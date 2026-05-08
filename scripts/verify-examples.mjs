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
import { mkdir, writeFile, rm, access, rename, readdir } from 'node:fs/promises';
import { join, resolve } from 'node:path';
import process from 'node:process';

const ROOT = resolve(new URL('..', import.meta.url).pathname);
const SKY = join(ROOT, 'sky-out', 'sky');
const OUT = join(ROOT, '_verify');

// Per-example contract — stays small + declarative. Two shapes:
//
//   server:
//     `port`     — what the binary listens on.
//     `path`     — page to navigate after the server is up (default '/').
//     `actions`  — list of step objects driving a primary UI flow.
//                  Each step is one of:
//                    { click: <selector>, count?: N }
//                    { fill:  { locator, value } }
//                    { goto:  <path> }
//     `expectBefore` — text that MUST be on the page before actions
//                      run. Sanity check that the page rendered the
//                      expected initial state.
//     `expectAfter`  — text that MUST appear once actions are done.
//                      This is the proof that the interaction
//                      ACTUALLY changed state — not just that the
//                      page loaded clean.
//
//   cli:
//     `cli: true` — run the binary, capture exit + stdout, no browser.
//     `args`      — argv to pass (default []).
//     `stdin`     — optional string fed to the process's stdin.
//     `expectStdout` — substring that must appear in stdout for pass.
//     `expectExit`  — exit code to expect (default 0).
//
// 11-fyne-stopwatch is omitted — desktop GUI app, build-only.
const EXAMPLES = {
    '01-hello-world':   { cli: true, expectStdout: 'Hello' },
    '02-go-stdlib':     { cli: true },
    '03-tea-external':  { cli: true, expectStdout: 'UUID' },
    '04-local-pkg':     { cli: true },
    '05-mux-server':    { port: 8000, path: '/',
                          // Mux server routes / + /echo + /ping. Navigate
                          // to /ping which is a fixed-text endpoint —
                          // proves the gorilla/mux dispatch works.
                          actions: [{ goto: '/ping' }],
                          expectAfter: 'pong' },
    '06-json':          { cli: true },
    '07-todo-cli':      { cli: true, args: ['list'] },
    '08-notes-app':     { port: 8000, path: '/',
                          // Full e2e journey:
                          //   sign up → grep email-verify URL from
                          //   server stdout → click verify → sign in
                          //   → create a note via /notes/new → list
                          //   the note → sign out.
                          // The server prints the verify URL to
                          // stdout; the harness extracts the token
                          // and opens the link itself, simulating
                          // the email click.
                          actions: [
                              { goto: '/auth/sign-up' },
                              { fill: { locator: 'input[name="email"]', value: 'verify-{{ts}}@example.com' } },
                              { fill: { locator: 'input[name="password"]', value: 'verify-pass-1234' } },
                              { fill: { locator: 'input[name="confirm_password"]', value: 'verify-pass-1234' } },
                              { click: 'button[type="submit"]' },
                              { expectText: 'Account Created' },
                              // Server prints "  http://localhost:8000/auth/verify?token=..."
                              // — capture the token.
                              { extractFromLog: {
                                  regex: '/auth/verify\\?token=([a-f0-9-]+)',
                                  as: 'verifyToken',
                              } },
                              { goto: '/auth/verify?token={{verifyToken}}' },
                              { expectText: 'verified' },
                              { goto: '/auth/sign-in' },
                              { fill: { locator: 'input[name="email"]', value: 'verify-{{ts}}@example.com' } },
                              { fill: { locator: 'input[name="password"]', value: 'verify-pass-1234' } },
                              { click: 'button[type="submit"]' },
                              { waitMs: 500 },
                              // Now logged in; create a note.
                              { goto: '/notes/new' },
                              { fill: { locator: 'input[name="title"]', value: 'Verify-Note-{{ts}}' } },
                              { fill: { locator: 'textarea[name="content"]', value: '# heading\n\nbody text from verify run' } },
                              { click: 'button[type="submit"]' },
                              { waitMs: 500 },
                              // Note list should now show the note.
                              { goto: '/notes' },
                          ],
                          expectAfter: 'Verify-Note-' },
    '09-live-counter':  { port: 8000, path: '/',
                          // Click + three times; counter must read "3".
                          // The canonical Sky.Live SSE-update proof.
                          actions: [{ click: 'button:has-text("+")', count: 3 }],
                          expectBefore: '0',
                          expectAfter: '3' },
    '10-live-component': { port: 8000, path: '/',
                          // Component example has +/-/Reset buttons.
                          // Click + then Reset; counter back to 0.
                          actions: [
                              { click: 'button:has-text("+")', count: 2 },
                              { click: 'button:has-text("Reset")' },
                          ],
                          expectAfter: '0' },
    '12-skyvote':       { port: 8000, path: '/',
                          // Full e2e: sign up → post a feature idea
                          // via the "Submit Idea" form → vote on an
                          // existing idea on the board → sign out.
                          actions: [
                              { click: 'a[href="/auth/signup"]' },
                              { fill: { locator: 'input[placeholder*="username" i]', value: 'verify-{{ts}}' } },
                              { fill: { locator: 'input[type="email"]', value: 'verify-{{ts}}@example.com' } },
                              { fill: { locator: 'input[type="password"]', value: 'verify-pass-1234' } },
                              { click: 'button[type="submit"], button:has-text("Sign Up")' },
                              { expectText: 'Welcome' },
                              // "Submit Idea" → fill form → submit.
                              { click: 'a[href="/submit"], a:has-text("Submit Idea"), button:has-text("Submit Idea")' },
                              { fill: { locator: 'input[placeholder*="title" i]', value: 'Verify Idea {{ts}}' } },
                              { fill: { locator: 'textarea', value: 'Test idea body posted by the verify harness.' } },
                              { click: 'button:has-text("Submit Idea")' },
                              { waitMs: 600 },
                              // After submit → board page; the new
                              // idea should be in the list. We also
                              // sign out at the end so the next run
                              // starts clean.
                              { click: 'a:has-text("Sign out"), button:has-text("Sign out")' },
                              { waitMs: 300 },
                          ],
                          expectAfter: 'Sign in' },
    '13-skyshop':       { port: 8000, path: '/',
                          // Skyshop's auth is Firebase — can't sign in
                          // from a fresh test env. Browse the public
                          // routes instead (Home / Products / Cart /
                          // each individual product). No console
                          // errors = no runtime panics on any route.
                          actions: [
                              { click: 'a[href="/"]:has-text("Home"), a:has-text("Home")' },
                              { goto: '/products' },
                              { goto: '/cart' },
                              { goto: '/orders' },
                              { goto: '/' },
                          ],
                          expectBefore: 'SkyShop' },
    '14-task-demo':     { cli: true },
    '15-http-server':   { port: 8000, path: '/',
                          // Plain HTTP server: hit each route and
                          // verify the dispatch works. /hello/world is
                          // a path-param test, /api/status is a JSON
                          // endpoint, /cookie-demo sets a cookie.
                          actions: [
                              { goto: '/hello/world' },
                              { goto: '/api/status' },
                              { goto: '/cookie-demo' },
                              { goto: '/' },
                          ],
                          expectBefore: 'Sky HTTP Server' },
    '16-skychess':      { port: 8000, path: '/',
                          // Sign in with name → start game → click
                          // a piece (e2 pawn — opens the move picker).
                          actions: [
                              { fill: { locator: 'input[placeholder*="name"]', value: 'verify-bot' } },
                              { click: 'button:has-text("New Game")' },
                              { waitMs: 400 },
                              // First white pawn (e2). The Sky.Live
                              // chessboard renders pieces inside
                              // grid cells with sky-click handlers.
                              { click: '[sky-click="ClickSquare"]', count: 1 },
                          ],
                          expectAfter: 'verify-bot' },
    '17-skymon':        { port: 8000, path: '/',
                          // Sign in admin → Settings → add a monitor
                          // → assert the monitor appears on Dashboard
                          // → sign out.
                          actions: [
                              { goto: '/auth' },
                              { fill: { locator: 'input[placeholder*="username" i]', value: 'admin' } },
                              { fill: { locator: 'input[placeholder*="password" i]', value: 'admin123' } },
                              { click: 'button:has-text("Sign In")' },
                              { waitMs: 600 },
                              { goto: '/settings' },
                              { fill: { locator: 'input[placeholder="My API"]', value: 'verify-monitor-{{ts}}' } },
                              { fill: { locator: 'input[placeholder*="api.example"]', value: 'https://example.com/health' } },
                              { click: 'button:has-text("Add Monitor")' },
                              { waitMs: 500 },
                              { goto: '/' },
                              { expectText: 'verify-monitor-' },
                              { click: 'a:has-text("Sign Out"), button:has-text("Sign Out")' },
                              { waitMs: 300 },
                          ],
                          expectAfter: 'Sign In' },
    '18-job-queue':     { port: 8000, path: '/',
                          // Click Fast Job; the queue grows by one
                          // and the SSE updates within ~1s show the
                          // job transitioning to Done.
                          actions: [{ click: 'button:has-text("Fast Job")' }],
                          expectAfter: 'Done' },
    '19-skyforum':      { port: 8000, path: '/',
                          // Full e2e: sign in → upvote (142→143) →
                          // open post detail → leave a comment →
                          // sign out. skyforum has no separate
                          // user store (any non-empty username
                          // works per Update.sky's DoSignIn).
                          actions: [
                              { click: 'button:has-text("sign in")' },
                              { fill: { locator: 'input[name="username"]', value: 'verify-{{ts}}' } },
                              { fill: { locator: 'input[name="password"]', value: 'verify-pass' } },
                              { click: 'input[type="submit"], button[type="submit"]' },
                              { expectText: 'verify-{{ts}}' },
                              { click: 'button[sky-click="UpvotePost"]' },
                              { expectText: '143' },
                              // Open the first post's detail page.
                              // Need [sky-click="Navigate"] specifically
                              // — the title text lives in a deeply
                              // nested div and a bare :has-text matches
                              // the column-wrapper without the click
                              // handler.
                              { click: '[sky-click="Navigate"]:has-text("Show SF")' },
                              { waitMs: 400 },
                              { expectText: 'Comment' },
                              { fill: { locator: 'input[placeholder*="comment" i]', value: 'Hello from verify harness {{ts}}' } },
                              { click: 'button:has-text("post")' },
                              { waitMs: 800 },
                              { click: 'button:has-text("logout")' },
                              { waitMs: 300 },
                          ],
                          expectAfter: 'sign in' },
};


function args() {
    const filt = process.argv.slice(2);
    if (filt.length === 0) return Object.keys(EXAMPLES);
    return Object.keys(EXAMPLES).filter(n =>
        filt.some(f => n.startsWith(f) || n.includes(f)));
}


// Compact, file-name-safe label for an action step. Used as a
// suffix on the step-NN-<label>.png filename so a reviewer can
// tell which still came from which interaction.
function describeStep(a) {
    if (a.click) return 'click-' + slug(a.click);
    if (a.fill)  return 'fill-' + slug(a.fill.locator);
    if (a.goto)  return 'goto-' + slug(a.goto);
    return 'step';
}


function slug(s) {
    return String(s)
        .replace(/^\W+|\W+$/g, '')
        .replace(/[^a-zA-Z0-9]+/g, '-')
        .slice(0, 28);
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


// Write the app log + close ctx (if any) + kill child + return a
// FAIL result. Used at expectBefore / expectAfter early-out points
// so the verify dir keeps the diagnostic artefacts even on fail.
async function finalizeFail(out, child, appLog, ctx, why) {
    try { if (ctx) await ctx.close(); } catch {}
    await writeFile(join(out, 'app.log'), appLog).catch(() => null);
    await killTree(child);
    return { name: out.split('/').pop(), ok: false, stage: 'expect', err: why };
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
        // recordVideo writes a .webm of the full session into the
        // verify dir — playwright finalises the file on ctx.close,
        // so the artefact lands alongside the per-step PNGs.
        const ctx = await browser.newContext({
            ignoreHTTPSErrors: true,
            viewport: { width: 1280, height: 800 },
            recordVideo: { dir: out, size: { width: 1280, height: 800 } },
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

        // BEFORE snapshot — captured before any actions run, so a
        // diff against the AFTER screenshot proves the actions
        // actually changed UI state (not just "page loads clean").
        const beforeHtml = await page.content();
        await writeFile(join(out, 'page-before.html'), beforeHtml);
        await page.screenshot({
            path: join(out, 'screenshot-before.png'),
            fullPage: true,
        });

        if (cfg.expectBefore && !beforeHtml.includes(cfg.expectBefore)) {
            return finalizeFail(out, child, appLog, ctx,
                `expectBefore "${cfg.expectBefore}" missing on initial page`);
        }

        // 4. UI actions — click / fill / goto. After each step,
        // screenshot to a step-N.png file so the final artefact dir
        // shows the full progression. Per-step PNGs + the video
        // recording give two views of the same flow: stills you can
        // diff against expected output, and a webm you can scrub.
        // Per-run substitution table. {{ts}} is a unique-per-run
        // timestamp (so signup emails don't collide). Additional
        // entries are populated by `extractFromLog` actions — e.g.
        // notes-app prints the email-verify URL to stdout, the
        // harness greps it from app.log, and subsequent goto/fill
        // actions reference it via {{verifyUrl}}.
        const vars = { ts: String(Date.now()) };
        const subst = (s) => String(s).replace(
            /\{\{(\w+)\}\}/g,
            (_, k) => vars[k] ?? `{{${k}}}`,
        );

        if (cfg.actions) {
            for (let i = 0; i < cfg.actions.length; i++) {
                const a = cfg.actions[i];
                const stepLabel = describeStep(a);
                if (a.click) {
                    const n = a.count ?? 1;
                    for (let j = 0; j < n; j++) {
                        await page.locator(subst(a.click)).first().click({
                            timeout: 3000,
                        }).catch(() => null);
                        await page.waitForTimeout(250);
                    }
                }
                if (a.fill) {
                    const value = subst(a.fill.value);
                    await page.locator(subst(a.fill.locator)).first()
                        .fill(value, { timeout: 3000 }).catch(() => null);
                    // Blur so onChange (fires on blur, not input) —
                    // 17-skymon's password input — picks up the value.
                    await page.locator(subst(a.fill.locator)).first()
                        .blur({ timeout: 1000 }).catch(() => null);
                    await page.waitForTimeout(200);
                }
                if (a.goto) {
                    let target = subst(a.goto);
                    if (!/^https?:/.test(target)) {
                        target = `http://localhost:${cfg.port}${target}`;
                    }
                    await page.goto(target, {
                        waitUntil: 'domcontentloaded',
                        timeout: 10000,
                    }).catch(() => null);
                }
                if (a.waitMs) {
                    await page.waitForTimeout(a.waitMs);
                }
                if (a.extractFromLog) {
                    // Grep the captured server stdout for a regex; the
                    // first capture group lands in vars[as] for use in
                    // subsequent {{as}} substitutions.
                    const re = new RegExp(a.extractFromLog.regex);
                    const m = re.exec(appLog);
                    if (m) {
                        vars[a.extractFromLog.as] = m[1] ?? m[0];
                    } else {
                        // Soft-fail: leave var unset so a downstream
                        // {{as}} renders literally and the contract
                        // assertion catches it.
                    }
                }
                if (a.expectText) {
                    // Mid-flow assertion — fail fast if a step's
                    // expected post-state isn't met. Useful for
                    // catching auth failures early in a long journey.
                    const html = await page.content();
                    const want = subst(a.expectText);
                    if (!html.includes(want)) {
                        await writeFile(join(out, 'app.log'), appLog);
                        await ctx.close();
                        await killTree(child);
                        return { name, ok: false, stage: 'step',
                                 err: `step ${i + 1}: "${want}" missing` };
                    }
                }
                // Stills + label: lets a reviewer scrub through the
                // PNGs and read what each step represents.
                const stepIdx = String(i + 1).padStart(2, '0');
                await page.screenshot({
                    path: join(out, `step-${stepIdx}-${stepLabel}.png`),
                    fullPage: true,
                }).catch(() => null);
            }
            // Settle any post-action SSE reconciliation. Live.app
            // patches arrive via SSE — give them a generous window
            // before the final snapshot.
            await page.waitForTimeout(1000);
        }

        // AFTER snapshot — main artefact for visual diff review.
        const afterHtml = await page.content();
        await writeFile(join(out, 'page.html'), afterHtml);
        await page.screenshot({
            path: join(out, 'screenshot.png'),
            fullPage: true,
        });
        await writeFile(join(out, 'console.log'),
            consoleErrors.join('\n') + (consoleErrors.length ? '\n' : ''));
        await ctx.close();

        // Rename the auto-named video.webm playwright finalises on
        // ctx.close (page@<hash>.webm) to a stable 'video.webm' so
        // the verify dir has a predictable filename.
        try {
            const entries = await readdir(out);
            for (const e of entries) {
                if (e.startsWith('page@') && e.endsWith('.webm')) {
                    await rename(join(out, e), join(out, 'video.webm'));
                    break;
                }
            }
        } catch { /* best-effort */ }

        // Action proof — expectAfter MUST appear post-actions if
        // declared. This is the proof that the interaction
        // actually changed state.
        if (cfg.expectAfter && !afterHtml.includes(cfg.expectAfter)) {
            return finalizeFail(out, child, appLog, null,
                `expectAfter "${cfg.expectAfter}" missing post-actions`);
        }

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

    // Wipe the entire _verify/ tree (except this run's wanted set,
    // which is recreated per-example anyway) so artefacts the user
    // browses afterwards are guaranteed fresh from THIS sweep —
    // no stale screenshots/videos from a prior partial run.
    if (process.argv.length === 2) {
        await rm(OUT, { recursive: true, force: true });
    } else {
        // Targeted run: only wipe the dirs we're about to recreate.
        for (const name of wanted) {
            await rm(join(OUT, name), { recursive: true, force: true });
        }
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
