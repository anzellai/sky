// scripts/skyvote-hammer.mjs
// Hammers skyvote: visits every page, clicks every clickable, fills every form,
// tries signup/signin/signout, post idea, comment, vote, browse routes — both
// authenticated AND unauthenticated. Captures any panic / 500.

import { chromium } from "playwright";
const URL = "http://localhost:8000";
const browser = await chromium.launch({ headless: true });
const errors = [];
async function newCtx() {
    const ctx = await browser.newContext();
    const page = await ctx.newPage();
    page.on("pageerror", e => errors.push("PAGEERR: " + e.message));
    page.on("response", r => { if (r.status() >= 500) errors.push(`HTTP ${r.status()} ${r.url()}`); });
    return { ctx, page };
}

async function deepClick(page, label) {
    // Click every link/button visible (idempotent on most pages).
    const elems = await page.locator("button, a[href]").all();
    for (let i = 0; i < Math.min(elems.length, 20); i++) {
        try {
            const txt = (await elems[i].innerText({ timeout: 800 }))?.slice(0, 40) || "";
            await elems[i].click({ timeout: 1500 });
            await page.waitForTimeout(250);
        } catch {}
    }
    console.log(`  [${label}] post-click body len:`, (await page.locator("body").innerText().catch(() => "")).length);
}

console.log("=== UNAUTHED: visit every route + click everything ===");
const { ctx: c1, page: p1 } = await newCtx();
const routes = ["/", "/roadmap", "/about", "/auth/signin", "/auth/signup"];
for (const r of routes) {
    try {
        await p1.goto(URL + r, { waitUntil: "domcontentloaded", timeout: 8000 });
        await p1.waitForTimeout(400);
        await deepClick(p1, "unauthed " + r);
    } catch (e) { errors.push(`route ${r}: ${e.message}`); }
}

console.log("=== SIGN UP ===");
await p1.goto(URL + "/auth/signup", { waitUntil: "domcontentloaded" });
await p1.waitForTimeout(400);
// Forms by placeholder — skyvote uses placeholder, no name attrs
try {
    await p1.fill('input[placeholder*="username" i]', "alice");
    await p1.fill('input[placeholder*="example.com" i], input[type="email"]', "alice@test.com");
    await p1.fill('input[type="password"]', "secret123");
    await p1.click('button[type="submit"]');
    await p1.waitForTimeout(2000);
    console.log("  post-signup URL:", p1.url());
    console.log("  body 200 chars:", (await p1.locator("body").innerText()).slice(0, 200));
} catch (e) { errors.push("signup: " + e.message); }

console.log("=== AUTHED: visit + interact on every page ===");
for (const r of ["/", "/roadmap"]) {
    try {
        await p1.goto(URL + r, { waitUntil: "domcontentloaded" });
        await p1.waitForTimeout(400);
        await deepClick(p1, "authed " + r);
    } catch (e) { errors.push(`authed ${r}: ${e.message}`); }
}

console.log("=== SUBMIT idea (if form available) ===");
await p1.goto(URL + "/", { waitUntil: "domcontentloaded" });
await p1.waitForTimeout(500);
try {
    const submitBtns = p1.locator("button, a").filter({ hasText: /Submit.*Idea|New Idea|Create|Post/i });
    if (await submitBtns.count() > 0) {
        await submitBtns.first().click({ timeout: 1500 });
        await p1.waitForTimeout(500);
        const inputs = await p1.locator('input[type="text"], textarea, input:not([type])').count();
        console.log("  submit-form inputs:", inputs);
        if (inputs >= 1) {
            try { await p1.locator('input[type="text"], input:not([type])').first().fill("Hammer test idea"); } catch {}
            try { await p1.locator('textarea').first().fill("Description for the hammer test idea."); } catch {}
            await p1.locator('button[type="submit"]').click({ timeout: 1500 });
            await p1.waitForTimeout(1500);
            console.log("  post-submit body 200 chars:", (await p1.locator("body").innerText()).slice(0, 200));
        }
    }
} catch (e) { errors.push("submit: " + e.message); }

console.log("=== CLICK every idea card + interact ===");
await p1.goto(URL + "/", { waitUntil: "domcontentloaded" });
await p1.waitForTimeout(500);
// Find idea-card-like locators (heading inside a card)
const ideaTitles = await p1.locator("h2, h3, .idea-title, [class*=idea]").all();
console.log("  idea-title-ish:", ideaTitles.length);
if (ideaTitles.length > 0) {
    try {
        await ideaTitles[0].click({ timeout: 1500 });
        await p1.waitForTimeout(800);
        console.log("  detail page body 200:", (await p1.locator("body").innerText()).slice(0, 200));
        // Click everything on the detail page — vote, comment, back, etc.
        await deepClick(p1, "detail page");
    } catch (e) { errors.push("idea click: " + e.message); }
}

await c1.close();

console.log("=== FRESH BROWSER (no cookies): click idea ===");
const { ctx: c2, page: p2 } = await newCtx();
await p2.goto(URL + "/", { waitUntil: "domcontentloaded" });
await p2.waitForTimeout(400);
const ideas2 = await p2.locator("h2, h3").all();
if (ideas2.length > 0) {
    try {
        await ideas2[0].click({ timeout: 1500 });
        await p2.waitForTimeout(800);
        console.log("  unauthed detail body 200:", (await p2.locator("body").innerText()).slice(0, 200));
        await deepClick(p2, "unauthed-detail");
    } catch (e) { errors.push("unauthed idea click: " + e.message); }
}

await c2.close();
await browser.close();

console.log("\n=== ERRORS ===");
errors.forEach(e => console.log("  ", e));
process.exit(errors.length === 0 ? 0 : 1);
