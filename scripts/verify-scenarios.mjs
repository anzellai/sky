// Per-app end-to-end interaction scenarios used by verify-live-app.mjs.
// Each scenario is async (page, opts) => void. Throw to fail.
// `opts.baseUrl` available. `opts.pause(ms)` helps with video pacing.
// `opts.skyEventPosts` is a live array the driver pushes to whenever a
// POST /_sky/event fires — use `expectSkyEventAfter(opts, fn, msg)` to
// assert a click really round-tripped to the server.  Pre-v0.13.2 a
// regression silently dropped Std.Ui events at render time, so the
// click became a no-op; the page still rendered and Playwright still
// "succeeded", masking the bug. The expectSkyEvent assertion forces
// the test to fail loudly on that class.
//
// Goal: cover every meaningful page + every user-facing action (auth,
// CRUD, navigation, form submit) for v0.13 regression verification.

const wait = (page, ms) => page.waitForTimeout(ms);

async function clickIfPresent(page, sel, pauseMs = 400) {
    const el = page.locator(sel).first();
    if (await el.count() > 0) {
        await el.click().catch(() => {});
        await wait(page, pauseMs);
        return true;
    }
    return false;
}

async function fillByName(page, name, value, pauseMs = 200) {
    const el = page.locator(`[name="${name}"]`).first();
    if (await el.count() > 0) {
        await el.fill(value).catch(() => {});
        await wait(page, pauseMs);
        return true;
    }
    return false;
}

async function gotoAndSettle(page, url, pauseMs = 800) {
    await page.goto(url, { waitUntil: 'domcontentloaded', timeout: 15_000 });
    await page.waitForLoadState('networkidle', { timeout: 5_000 }).catch(() => {});
    await wait(page, pauseMs);
}

// Run fn, then assert AT LEAST ONE new POST /_sky/event fired during
// fn or in the 500ms after. Throws when none fired — that's the
// silent-event-drop regression class. `opts.skyEventPosts` is the
// driver's live event log.
async function expectSkyEventAfter(opts, fn, label) {
    if (!opts || !Array.isArray(opts.skyEventPosts)) {
        // Driver may not have wired up the event log (older driver
        // version). Skip the assertion — falling back to the
        // structural HTML check in verify-live-app.mjs.
        await fn();
        return;
    }
    const before = opts.skyEventPosts.length;
    await fn();
    // Give the SSE round-trip a moment to land.
    //
    // This was `await opts.pause ? opts.pause(500) : new Promise(...)`.
    // `await` binds tighter than `?:`, so it awaited the FUNCTION VALUE
    // (truthy, resolves immediately), then called `opts.pause(500)` and threw
    // the returned promise away unawaited — no settle happened at all, and the
    // /_sky/event POST count below was read before the round-trip landed.
    // Parenthesise the conditional.
    await (opts.pause ? opts.pause(500) : new Promise(r => setTimeout(r, 500)));
    const fired = opts.skyEventPosts.length - before;
    if (fired === 0) {
        throw new Error(`expected /_sky/event POST after "${label}" but none fired `
            + `(event-emission pipeline broken? button/form may be missing sky-* attrs)`);
    }
}

// ─── Scenario definitions ───────────────────────────────────────────

export const scenarios = {
    // Default — just verify body renders something.
    async smoke(page, { baseUrl }) {
        const body = await page.locator('body').innerText();
        if (!body || body.trim().length === 0) {
            throw new Error('home page rendered empty body');
        }
        await wait(page, 800);
    },

    // 09-live-counter — Increment / Decrement / Reset + About nav.
    async 'live-counter'(page, opts) {
        const incBtn = page.locator('button:has-text("+")').first();
        if (await incBtn.count() === 0) throw new Error('+ button not found');
        // First click MUST round-trip through /_sky/event — proves the
        // event-emission pipeline is intact end-to-end. Pre-v0.13.2
        // Std.Ui apps would silently skip this (no sky-click attr).
        await expectSkyEventAfter(opts, async () => {
            await incBtn.click();
        }, 'Increment click');
        await wait(page, 300);
        await incBtn.click();
        await wait(page, 300);
        await incBtn.click();
        await wait(page, 400);
        await clickIfPresent(page, 'button:has-text("-")', 400);
        await clickIfPresent(page, 'button:has-text("Reset")', 600);
        // Nav to About
        await clickIfPresent(page, 'button:has-text("About"), a:has-text("About")', 800);
        // Back to Counter
        await clickIfPresent(page, 'button:has-text("Counter"), a:has-text("Counter")', 600);
    },

    // 10-live-component — click any rendered buttons + check page.
    async 'live-component'(page, { baseUrl }) {
        // Component demo — click any button present
        const buttons = page.locator('button');
        const count = await buttons.count();
        for (let i = 0; i < Math.min(count, 4); i++) {
            await buttons.nth(i).click().catch(() => {});
            await wait(page, 300);
        }
        // Type into any input
        const input = page.locator('input').first();
        if (await input.count() > 0) {
            await input.fill('Hello v0.13').catch(() => {});
            await wait(page, 400);
        }
    },

    // 12-skyvote — full auth + CRUD + voting flow.
    async skyvote(page, { baseUrl }) {
        const stamp = Date.now();
        const username = 'verify_' + stamp;
        const email = `verify+${stamp}@v013.test`;
        const password = 'verify-pass-12345';

        // Visit each public page
        await gotoAndSettle(page, baseUrl + '/about', 600);
        await gotoAndSettle(page, baseUrl + '/roadmap', 600);

        // Sign up
        await gotoAndSettle(page, baseUrl + '/auth/signup', 600);
        await fillByName(page, 'username', username, 200);
        await fillByName(page, 'email', email, 200);
        await fillByName(page, 'password', password, 200);
        await clickIfPresent(page, 'button[type="submit"], button:has-text("Sign up"), button:has-text("Create")', 1500);

        // Submit an idea (auth required)
        await gotoAndSettle(page, baseUrl + '/submit', 600);
        await fillByName(page, 'title', 'v0.13 verification idea ' + stamp, 200);
        await fillByName(page, 'description', 'Auto-generated by verify-live-app.mjs to exercise full Sky.Live CRUD path.', 200);
        await clickIfPresent(page, 'button[type="submit"], button:has-text("Submit")', 1500);

        // Back to board — try to upvote
        await gotoAndSettle(page, baseUrl + '/', 800);
        await clickIfPresent(page, '.vote-btn, button:has-text("▲"), button:has-text("Upvote")', 800);

        // Sign out
        await clickIfPresent(page, 'a:has-text("Sign Out"), button:has-text("Sign Out"), a:has-text("Logout")', 800);
    },

    // 13-skyshop — navigate public pages only (Google OAuth blocked).
    async skyshop(page, { baseUrl }) {
        const pages = [
            '/',
            '/products',
            '/cart',
            '/privacy-policy',
            '/terms',
            '/auth/signin',  // Will render the Google sign-in button page
        ];
        for (const p of pages) {
            await gotoAndSettle(page, baseUrl + p, 800);
        }
        // Try clicking the first product if products page rendered any.
        await gotoAndSettle(page, baseUrl + '/products', 600);
        await clickIfPresent(page, 'a[href^="/product/"]', 1200);
    },

    // 16-skychess — start new game + click squares.
    async skychess(page, { baseUrl }) {
        // Try start new game from home
        await clickIfPresent(page, 'button:has-text("Start New Game"), button:has-text("New Game")', 1200);
        // Click some board squares (e2 to e4 attempt by pixel-position)
        const squares = page.locator('td.square, td[class*="sq"], [data-square]');
        const sqCount = await squares.count();
        if (sqCount > 0) {
            // Try a sequence — click two squares to attempt a move.
            await squares.nth(52).click().catch(() => {}); // ~e2
            await wait(page, 500);
            await squares.nth(36).click().catch(() => {}); // ~e4
            await wait(page, 600);
            await squares.nth(12).click().catch(() => {}); // black e7
            await wait(page, 500);
            await squares.nth(28).click().catch(() => {});
            await wait(page, 800);
        }
        // Try resign
        await clickIfPresent(page, 'button:has-text("Resign")', 800);
    },

    // 17-skymon — navigate every page (auth is GitHub OAuth, external).
    async skymon(page, { baseUrl }) {
        for (const p of ['/', '/status', '/settings', '/alerts', '/auth']) {
            await gotoAndSettle(page, baseUrl + p, 1000);
        }
    },

    // 18-job-queue — every job-type button + history controls.
    async 'job-queue'(page, { baseUrl }) {
        await clickIfPresent(page, 'button:has-text("Fast Job")', 800);
        await clickIfPresent(page, 'button:has-text("Slow Job")', 800);
        await clickIfPresent(page, 'button:has-text("Failing Job")', 800);
        await clickIfPresent(page, 'button:has-text("Batch")', 1000);
        await clickIfPresent(page, 'button:has-text("Save Snapshot")', 600);
        await clickIfPresent(page, 'button:has-text("Load History")', 600);
        await clickIfPresent(page, 'button:has-text("Clear Finished")', 600);
    },

    // 19-skyforum — login + new post + upvote + comment + logout.
    async skyforum(page, { baseUrl }) {
        const stamp = Date.now();
        // The forum is Navigate-Msg driven; look for a sign-in trigger
        // (form is shown on LoginPage, reached via clicking "Sign in").
        await clickIfPresent(page, 'a:has-text("Sign in"), button:has-text("Sign in")', 600);
        await fillByName(page, 'username', 'verify_' + stamp, 200);
        // Submit login
        const submitBtn = page.locator('button[type="submit"]').first();
        if (await submitBtn.count() > 0) {
            await submitBtn.click().catch(() => {});
            await wait(page, 1000);
        } else {
            await page.keyboard.press('Enter');
            await wait(page, 1000);
        }

        // Try create post — click "New Post" or compose link
        await clickIfPresent(page, 'a:has-text("New Post"), button:has-text("New Post"), a:has-text("Compose")', 800);
        await fillByName(page, 'title', 'v0.13 verification post ' + stamp, 200);
        await fillByName(page, 'body', 'Body text auto-generated for v0.13 verification.', 200);
        await fillByName(page, 'url', '', 200);
        // Submit
        const post = page.locator('button[type="submit"], button:has-text("Post"), button:has-text("Submit")').first();
        if (await post.count() > 0) {
            await post.click().catch(() => {});
            await wait(page, 1200);
        }

        // Upvote any visible post
        await clickIfPresent(page, 'button:has-text("▲"), .upvote, .vote-up, [aria-label*="upvote" i]', 600);

        // Click any post link to view detail
        await clickIfPresent(page, 'a[class*="post"], a[href*="post"]', 800);

        // Logout
        await clickIfPresent(page, 'a:has-text("Sign out"), button:has-text("Sign out"), a:has-text("Logout"), button:has-text("Logout")', 600);
    },

    // 08-notes-app — sign-up → sign-in → CRUD note → sign-out.
    // (Sky.Http.Server — traditional form-POST flow.)
    async 'notes-crud'(page, { baseUrl }) {
        const stamp = Date.now();
        const email = `verify+${stamp}@v013.test`;
        const password = 'verify-pass-12345';

        // 1. Landing
        await gotoAndSettle(page, baseUrl + '/', 600);

        // 2. Sign up
        await gotoAndSettle(page, baseUrl + '/auth/sign-up', 600);
        await fillByName(page, 'email', email, 200);
        await fillByName(page, 'password', password, 200);
        await fillByName(page, 'confirm_password', password, 200);
        await clickIfPresent(page, 'button[type="submit"]', 1500);

        // 3. Sign-up flow may need email verification — try sign-in regardless.
        await gotoAndSettle(page, baseUrl + '/auth/sign-in', 600);
        await fillByName(page, 'email', email, 200);
        await fillByName(page, 'password', password, 200);
        await clickIfPresent(page, 'button[type="submit"]', 1500);

        // 4. Notes list (may redirect back to sign-in if email verification required)
        await gotoAndSettle(page, baseUrl + '/notes', 800);

        // 5. Try New Note (if accessible)
        await clickIfPresent(page, 'a:has-text("New Note"), button:has-text("New Note")', 800);
        await fillByName(page, 'title', 'v0.13 verify note ' + stamp, 200);
        await fillByName(page, 'body', 'auto-generated body for end-to-end notes verification.', 200);
        await clickIfPresent(page, 'button[type="submit"]', 1500);

        // 6. Visit notes again
        await gotoAndSettle(page, baseUrl + '/notes', 800);

        // 7. Sign out
        await gotoAndSettle(page, baseUrl + '/auth/sign-out', 600);
    },

    // 05-mux-server — gorilla/mux example has /, /echo, /ping routes.
    async 'mux-routes'(page, { baseUrl }) {
        await gotoAndSettle(page, baseUrl + '/', 500);
        await gotoAndSettle(page, baseUrl + '/ping', 500);
        await gotoAndSettle(page, baseUrl + '/echo', 500);
    },

    // 15-http-server — Sky.Http.Server with home, hello, api/status,
    // cookie-demo, redirect routes.
    async 'http-routes'(page, { baseUrl }) {
        await gotoAndSettle(page, baseUrl + '/', 500);
        await gotoAndSettle(page, baseUrl + '/hello/sky', 500);
        await gotoAndSettle(page, baseUrl + '/api/status', 500);
        await gotoAndSettle(page, baseUrl + '/cookie-demo', 500);
    },
};

export default scenarios;
