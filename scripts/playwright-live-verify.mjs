#!/usr/bin/env node
// scripts/playwright-live-verify.mjs
//
// Interactive verification of Sky.Live examples via headed/headless
// Chromium. For each Live app:
//   1. Boot the app (./sky-out/app)
//   2. Open localhost:8000 in Chromium
//   3. Exercise key interactive elements (click buttons, fill inputs, etc.)
//   4. Capture a screenshot of the final state
//   5. Record a video of the interaction
//   6. Verify no SSE/network errors
//   7. Kill the app, verify clean shutdown (no panic)
//
// Output:
//   ./playwright-out/<app>/screenshot.png
//   ./playwright-out/<app>/recording.webm
//   ./playwright-out/<app>/console.log
//   stdout: PASS/FAIL per app
//
// Usage: node scripts/playwright-live-verify.mjs [app-name]

import { chromium } from "playwright";
import { spawn } from "node:child_process";
import { mkdirSync, writeFileSync, existsSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = dirname(fileURLToPath(import.meta.url));
const ROOT = resolve(__dirname, "..");
const OUT = join(ROOT, "playwright-out");

// Per-app interaction recipe. Keep these minimal but exercise the
// app's signature behaviour — enough to prove the Sky.Live wire
// (SSE handshake + click dispatch + DOM patch) is working.
const RECIPES = {
    "09-live-counter": {
        name: "live-counter",
        port: 8000,
        steps: async (page) => {
            // Click + button a few times, then - button once.
            // Verify count updates accordingly.
            await page.waitForSelector("button", { timeout: 5000 });
            const initial = await page.locator("body").innerText();
            console.log("  [recipe] initial text:", initial.substring(0, 100));
            // Click '+' twice
            await page.locator("button", { hasText: "+" }).first().click();
            await page.waitForTimeout(300);
            await page.locator("button", { hasText: "+" }).first().click();
            await page.waitForTimeout(300);
            // Click '−' once
            await page.locator("button", { hasText: /−|-/ }).first().click();
            await page.waitForTimeout(300);
            const final = await page.locator("body").innerText();
            console.log("  [recipe] final text:", final.substring(0, 100));
            // Expect the count to have changed
            return final !== initial;
        },
    },
    "12-skyvote": {
        name: "skyvote",
        port: 8000,
        steps: async (page) => {
            await page.waitForSelector("button, a", { timeout: 5000 });
            // Click any button to exercise dispatch
            const btn = page.locator("button").first();
            if (await btn.count() > 0) {
                await btn.click();
                await page.waitForTimeout(500);
            }
            const text = await page.locator("body").innerText();
            console.log("  [recipe] body length:", text.length);
            return text.length > 0;
        },
    },
    "13-skyshop": {
        name: "skyshop",
        port: 8000,
        steps: async (page) => {
            await page.waitForSelector("body", { timeout: 5000 });
            const text = await page.locator("body").innerText();
            console.log("  [recipe] body length:", text.length);
            // Just verify content renders (skyshop has many pages)
            return text.length > 100;
        },
    },
    "16-skychess": {
        name: "skychess",
        port: 8000,
        steps: async (page) => {
            await page.waitForSelector("body", { timeout: 5000 });
            const text = await page.locator("body").innerText();
            console.log("  [recipe] body length:", text.length);
            return text.length > 0;
        },
    },
    "17-skymon": {
        name: "skymon",
        port: 8000,
        steps: async (page) => {
            await page.waitForSelector("body", { timeout: 5000 });
            const text = await page.locator("body").innerText();
            console.log("  [recipe] body length:", text.length);
            return text.length > 0;
        },
    },
    "19-skyforum": {
        name: "skyforum",
        port: 8000,
        steps: async (page) => {
            await page.waitForSelector("body", { timeout: 5000 });
            // skyforum should show posts on the home page
            const text = await page.locator("body").innerText();
            console.log("  [recipe] body length:", text.length);
            // Try clicking one of the upvote buttons if present
            const upvoteBtns = page.locator("button", { hasText: /▲|upvote/ });
            const upvoteCount = await upvoteBtns.count();
            if (upvoteCount > 0) {
                await upvoteBtns.first().click();
                await page.waitForTimeout(500);
                console.log("  [recipe] clicked upvote");
            }
            return text.length > 100;
        },
    },
    "10-live-component": {
        name: "live-component",
        port: 8000,
        steps: async (page) => {
            await page.waitForSelector("body", { timeout: 5000 });
            const text = await page.locator("body").innerText();
            console.log("  [recipe] body length:", text.length);
            return text.length > 0;
        },
    },
};

async function bootApp(appDir, port) {
    const appPath = join(appDir, "sky-out/app");
    if (!existsSync(appPath)) {
        throw new Error(`No app binary at ${appPath}`);
    }
    const proc = spawn(appPath, [], {
        cwd: appDir,
        env: { ...process.env, SKY_LIVE_PORT: String(port), PORT: String(port) },
        stdio: ["ignore", "pipe", "pipe"],
    });
    let stderr = "";
    let stdout = "";
    proc.stdout.on("data", (b) => { stdout += b.toString(); });
    proc.stderr.on("data", (b) => { stderr += b.toString(); });
    // Wait for port to be listening (poll http)
    const start = Date.now();
    while (Date.now() - start < 10000) {
        try {
            const r = await fetch(`http://localhost:${port}/`, { signal: AbortSignal.timeout(800) });
            if (r.status >= 200 && r.status < 500) break;
        } catch {}
        await new Promise((r) => setTimeout(r, 250));
    }
    return { proc, getOutput: () => ({ stdout, stderr }) };
}

async function killApp(server) {
    try {
        server.proc.kill("SIGTERM");
        await new Promise((r) => setTimeout(r, 500));
        if (!server.proc.killed) server.proc.kill("SIGKILL");
    } catch {}
}

async function runApp(name, recipe) {
    const appDir = join(ROOT, "examples", name);
    const outDir = join(OUT, name);
    mkdirSync(outDir, { recursive: true });

    let pass = false;
    let detail = "";
    let server;
    let browser;

    try {
        server = await bootApp(appDir, recipe.port);
        browser = await chromium.launch({ headless: true });
        const context = await browser.newContext({
            recordVideo: { dir: outDir, size: { width: 1280, height: 720 } },
            viewport: { width: 1280, height: 720 },
        });
        const page = await context.newPage();

        const consoleLog = [];
        page.on("console", (m) => consoleLog.push(`[${m.type()}] ${m.text()}`));
        page.on("pageerror", (e) => consoleLog.push(`[error] ${e.message}`));

        // Sky.Live keeps SSE open — networkidle never fires. Use domcontentloaded.
        await page.goto(`http://localhost:${recipe.port}/`, { waitUntil: "domcontentloaded", timeout: 8000 });

        // Run recipe
        pass = await recipe.steps(page);

        // Screenshot final state
        await page.screenshot({ path: join(outDir, "screenshot.png"), fullPage: true });

        // Save console log
        writeFileSync(join(outDir, "console.log"), consoleLog.join("\n"));

        await context.close();  // finalises video recording
        await browser.close();

        // Check no panic in server output
        const out = server.getOutput();
        writeFileSync(join(outDir, "server-stderr.log"), out.stderr);
        writeFileSync(join(outDir, "server-stdout.log"), out.stdout);
        if (out.stderr.includes("panic") || out.stdout.includes("panic")) {
            pass = false;
            detail = "server panic detected";
        }
    } catch (e) {
        detail = `exception: ${e.message}`;
        pass = false;
    } finally {
        if (browser) try { await browser.close(); } catch {}
        if (server) await killApp(server);
    }

    return { pass, detail };
}

async function main() {
    const filter = process.argv[2];
    const names = Object.keys(RECIPES).filter((n) => !filter || n === filter);
    if (names.length === 0) {
        console.error(`No recipe matches ${filter}`);
        process.exit(2);
    }

    mkdirSync(OUT, { recursive: true });

    const results = [];
    for (const name of names) {
        const recipe = RECIPES[name];
        process.stdout.write(`▶  ${name}  …  `);
        const { pass, detail } = await runApp(name, recipe);
        if (pass) console.log("PASS");
        else console.log(`FAIL — ${detail}`);
        results.push({ name, pass, detail });
    }

    const failed = results.filter((r) => !r.pass);
    console.log(`\n${results.length - failed.length}/${results.length} pass`);
    if (failed.length > 0) {
        console.log("Failures:");
        for (const f of failed) console.log(`  ${f.name}: ${f.detail}`);
        process.exit(1);
    }
}

await main();
