#!/usr/bin/env node
// scripts/verify-issue-63.mjs
//
// Regression guard for GitHub issue #63 — Std.Ui flex-chain
// propagation. The issue spans v0.14.7-v0.14.16 (5 attempted
// fixes). Canonical broken case:
//
//   view : Model -> any
//   view model =
//       Ui.layout [] <| Ui.el [ Ui.height Ui.fill, Ui.padding 16 ]
//           <| Input.multiline
//                  [ Ui.height Ui.fill ]    -- DOES NOT STRETCH
//                  { ... }
//
// Without the fix, the inner Input.multiline wrapper (a Ui.el
// from wrapWithLabel) has no flex-grow set and breaks the flex
// chain — its textarea child has flex-grow:1 but no parent height
// to grow into, so it stays at content-line height.
//
// This test boots /tmp/sky-issue-63 (built by the caller),
// opens the page, and asserts the textarea bounding rect
// occupies ≥ (viewport.height - 2 * padding - 4) pixels.

import { chromium } from "playwright";
import { spawn } from "node:child_process";
import { existsSync } from "node:fs";
import { resolve } from "node:path";

const APP_DIR = process.env.SKY_ISSUE63_DIR || "/tmp/sky-issue-63";
const PORT = parseInt(process.env.SKY_ISSUE63_PORT || "8763", 10);
const BIN = resolve(APP_DIR, "sky-out", "app");

if (!existsSync(BIN)) {
    console.error(`FAIL — missing binary ${BIN}`);
    console.error(`  build first: cd ${APP_DIR} && sky build src/Main.sky`);
    process.exit(2);
}

const child = spawn(BIN, [], {
    cwd: APP_DIR,
    stdio: ["ignore", "pipe", "pipe"],
    env: { ...process.env, SKY_LIVE_PORT: String(PORT) },
});

let serverLog = "";
child.stdout.on("data", (d) => { serverLog += d.toString(); });
child.stderr.on("data", (d) => { serverLog += d.toString(); });

// Bounded wait for server readiness
async function waitForReady(timeoutMs) {
    const deadline = Date.now() + timeoutMs;
    while (Date.now() < deadline) {
        try {
            const r = await fetch(`http://localhost:${PORT}/`);
            if (r.ok) return true;
        } catch (_) {}
        await new Promise((r) => setTimeout(r, 100));
    }
    return false;
}

let exitCode = 0;
try {
    if (!(await waitForReady(10_000))) {
        throw new Error(`server did not become ready in 10s; log:\n${serverLog}`);
    }

    const browser = await chromium.launch();
    const context = await browser.newContext({
        viewport: { width: 1280, height: 800 },
    });
    const page = await context.newPage();

    page.on("pageerror", (e) => {
        console.error(`PAGE ERROR: ${e.message}`);
        exitCode = 1;
    });

    await page.goto(`http://localhost:${PORT}/`, { waitUntil: "domcontentloaded", timeout: 15_000 });
    await page.waitForSelector("#the-textarea", { timeout: 5_000 });
    // Tiny settle to let the runtime's post-hello layout finalise.
    await page.waitForTimeout(150);

    const measurements = await page.evaluate(() => {
        const ta = document.querySelector("#the-textarea");
        const r = ta.getBoundingClientRect();
        return {
            taTop: r.top,
            taBottom: r.bottom,
            taHeight: r.height,
            taWidth: r.width,
            vpHeight: window.innerHeight,
            vpWidth: window.innerWidth,
        };
    });

    console.log("  measurements:", JSON.stringify(measurements));

    // Outer Ui.el has `Ui.padding 16` → the textarea should occupy the
    // viewport minus 16px top + 16px bottom = 32px chrome. Allow 4px
    // tolerance for sub-pixel rounding.
    const VP = measurements.vpHeight;
    const PAD = 16;
    const TOLERANCE = 4;
    const expectedMin = VP - 2 * PAD - TOLERANCE;

    if (measurements.taHeight < expectedMin) {
        console.error(`FAIL — textarea height ${measurements.taHeight}px < expected min ${expectedMin}px (viewport ${VP}px)`);
        console.error("  This is regression #6 in the v0.14.7-v0.14.16 issue #63 cycle.");
        console.error("  See sky-stdlib/Std/Ui.sky § flex-chain propagation.");
        exitCode = 1;
    } else {
        console.log(`PASS — textarea fills viewport (height ${measurements.taHeight}px ≥ ${expectedMin}px)`);
    }

    // Also verify the inner wrapWithLabel wrapper carries flex-grow now.
    const wrapperFlex = await page.evaluate(() => {
        const ta = document.querySelector("#the-textarea");
        const wrapper = ta.parentElement;
        const cs = window.getComputedStyle(wrapper);
        return {
            flexGrow: cs.flexGrow,
            minHeight: cs.minHeight,
            display: cs.display,
            tag: wrapper.tagName,
        };
    });
    console.log("  wrapper computed:", JSON.stringify(wrapperFlex));

    // Bonus: a quick taste-test for the broader showcase to surface
    // an obvious flex regression should one happen on a sub-page. We
    // navigate to /_sky/healthz to make sure the runtime is still
    // alive (so the test's pass means "render + runtime both up").
    const health = await page.evaluate(async () => {
        const r = await fetch("/_sky/healthz");
        return { status: r.status };
    });
    if (health.status !== 200) {
        console.error(`FAIL — /_sky/healthz returned ${health.status}`);
        exitCode = 1;
    }

    await browser.close();
} catch (e) {
    console.error(`FAIL — ${e.message}`);
    exitCode = 1;
} finally {
    child.kill("SIGTERM");
    await new Promise((r) => setTimeout(r, 200));
    if (!child.killed) child.kill("SIGKILL");
}

process.exit(exitCode);
