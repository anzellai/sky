#!/usr/bin/env node
// Deep-interaction sweep for all Sky.Live apps + HTTP server example.
// Hammers every button, link, and form in each app. Captures any
// server panic / 500 / pageerror. Output: pass/fail per app + per-app
// log file under /tmp/sweep-<app>.log.

import { chromium } from "playwright";
import { spawn, execSync } from "node:child_process";
import { writeFileSync, existsSync } from "node:fs";
import { join } from "node:path";

const ROOT = "/Users/anzel/works/playground/sky";

// (name, port). Set port=null to skip boot (we'll just check it builds).
const APPS = [
    { name: "09-live-counter", port: 8000 },
    { name: "10-live-component", port: 8000 },
    { name: "12-skyvote", port: 8000 },
    { name: "13-skyshop", port: 8000 },
    { name: "15-http-server", port: 8000 },
    { name: "16-skychess", port: 8000 },
    { name: "17-skymon", port: 8000 },
    { name: "18-job-queue", port: 8000 },
    { name: "19-skyforum", port: 8000 },
];

async function bootApp(name, port) {
    const dir = join(ROOT, "examples", name);
    if (!existsSync(join(dir, "sky-out/app"))) throw new Error(`no app binary in ${dir}`);
    // Clean residual DBs that hold WAL locks
    for (const fn of [".db", ".db-wal", ".db-shm"]) {
        try {
            execSync(`find ${dir} -maxdepth 1 -name "*${fn}" -delete 2>/dev/null`, { stdio: "ignore" });
        } catch {}
    }
    const proc = spawn("./sky-out/app", [], {
        cwd: dir,
        env: { ...process.env, SKY_LIVE_PORT: String(port), PORT: String(port) },
        stdio: ["ignore", "pipe", "pipe"],
    });
    let out = "", err = "";
    proc.stdout.on("data", (b) => out += b.toString());
    proc.stderr.on("data", (b) => err += b.toString());
    // Wait up to 10s for port to bind
    const start = Date.now();
    while (Date.now() - start < 10000) {
        try {
            const r = await fetch(`http://localhost:${port}/`, { signal: AbortSignal.timeout(800) });
            if (r.status >= 200 && r.status < 600) return { proc, getOutput: () => ({ out, err }) };
        } catch {}
        await new Promise(r => setTimeout(r, 200));
    }
    throw new Error("server never bound");
}

async function killApp(server) {
    try { server.proc.kill("SIGTERM"); await new Promise(r => setTimeout(r, 400)); if (!server.proc.killed) server.proc.kill("SIGKILL"); } catch {}
}

async function deepExercise(page) {
    // Click every link that doesn't navigate away from localhost
    const links = await page.locator('a[href^="/"]').all();
    const visited = new Set(["/"]);
    for (let i = 0; i < Math.min(links.length, 8); i++) {
        try {
            const href = await links[i].getAttribute("href");
            if (!href || visited.has(href)) continue;
            visited.add(href);
            await links[i].click({ timeout: 2000 });
            await page.waitForTimeout(400);
        } catch {}
    }
    // Click every button on the current page (up to 12) — these dispatch via Sky.Live SSE
    const btns = await page.locator("button:not([disabled])").all();
    for (let i = 0; i < Math.min(btns.length, 12); i++) {
        try {
            await btns[i].click({ timeout: 1500 });
            await page.waitForTimeout(250);
        } catch {}
    }
    // Fill every text/email/password/number input with sensible value
    const fills = [
        ['input[type="text"], input:not([type])', "test"],
        ['input[type="email"]', "test@example.com"],
        ['input[type="password"]', "secret123"],
        ['input[type="number"]', "42"],
        ['input[type="search"]', "test"],
        ['textarea', "Test content."],
    ];
    for (const [sel, val] of fills) {
        try {
            const xs = await page.locator(sel).all();
            for (const e of xs) {
                try { await e.fill(val, { timeout: 800 }); } catch {}
            }
        } catch {}
    }
    // Submit any form
    try {
        const submits = await page.locator('button[type="submit"]').all();
        for (let i = 0; i < Math.min(submits.length, 3); i++) {
            try {
                await submits[i].click({ timeout: 1500 });
                await page.waitForTimeout(700);
            } catch {}
        }
    } catch {}
}

async function runOne(name, port) {
    let server, browser;
    const log = [];
    const errors = [];
    try {
        // ensure no leftover
        try { execSync(`pkill -f "examples/${name}/sky-out/app" 2>/dev/null`); await new Promise(r => setTimeout(r, 300)); } catch {}
        server = await bootApp(name, port);
        log.push(`booted ${name} on :${port}`);
        browser = await chromium.launch({ headless: true });
        const ctx = await browser.newContext({ viewport: { width: 1280, height: 720 } });
        const page = await ctx.newPage();
        page.on("pageerror", (e) => errors.push("PAGEERROR: " + e.message));
        page.on("response", (r) => { if (r.status() >= 500) errors.push(`HTTP ${r.status()} ${r.url()}`); });
        page.on("requestfailed", (req) => {
            const err = req.failure()?.errorText || "";
            // SSE long-polls and POST events get aborted at page close —
            // not a server panic. Only flag real network failures.
            if (err === "net::ERR_ABORTED" || err === "net::ERR_FAILED") return;
            errors.push(`REQ FAIL ${req.url()}: ${err}`);
        });

        await page.goto(`http://localhost:${port}/`, { waitUntil: "domcontentloaded", timeout: 8000 });
        await page.waitForTimeout(500);
        log.push("page loaded; deep exercise...");
        await deepExercise(page);
        await page.waitForTimeout(500);
        // Capture final state
        const body = await page.locator("body").innerText().catch(() => "");
        log.push(`final body len: ${body.length}`);
        await page.screenshot({ path: `/tmp/sweep-${name}.png`, fullPage: true }).catch(() => {});

        await ctx.close();
        await browser.close();
        browser = null;

        const so = server.getOutput();
        log.push(`stdout tail: ${so.out.split("\n").slice(-4).join(" | ")}`);
        log.push(`stderr tail: ${so.err.split("\n").slice(-4).join(" | ")}`);
        // Check server output for panics
        const combined = so.out + so.err;
        const panicMatch = combined.match(/panic[^\n]*|Unreachable[^\n]*|coerceInner[^\n]*|interface conversion[^\n]*/g);
        if (panicMatch) {
            errors.push(...panicMatch.slice(0, 6));
        }
    } catch (e) {
        errors.push(`exception: ${e.message}`);
    } finally {
        if (browser) try { await browser.close(); } catch {}
        if (server) await killApp(server);
    }
    writeFileSync(`/tmp/sweep-${name}.log`, log.concat(["---errors---"], errors).join("\n"));
    return { name, pass: errors.length === 0, errors };
}

async function main() {
    const filter = process.argv[2];
    const targets = filter ? APPS.filter(a => a.name.includes(filter)) : APPS;
    const results = [];
    for (const { name, port } of targets) {
        process.stdout.write(`▶  ${name}  …  `);
        const r = await runOne(name, port);
        if (r.pass) console.log("PASS");
        else {
            console.log("FAIL");
            r.errors.slice(0, 5).forEach(e => console.log("    " + e));
        }
        results.push(r);
    }
    const failed = results.filter(r => !r.pass);
    console.log(`\n${results.length - failed.length}/${results.length} pass`);
    process.exit(failed.length === 0 ? 0 : 1);
}

await main();
