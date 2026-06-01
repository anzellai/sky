#!/usr/bin/env node
// scripts/verify-issue-63-input.mjs
//
// Regression guard for the v0.15.55 follow-up to GitHub #63:
// `Std.Ui.Input.*` user attrs must split between the
// wrapWithLabel wrapper (layout / size / alignment) and the
// inner form control (form / event / visual style).
//
// Pre-fix shape inherited from the long #63 cycle:
//
//   Ui.layout [] (Ui.el [Ui.height Ui.fill, Ui.padding 16]
//       (Input.multiline [Ui.height Ui.fill, Ui.width Ui.fill]
//           { ... }))
//
// The wrapWithLabel-emitted wrapper Ui.el had no layout attrs at
// all, so even though both the outer Ui.el AND the inner textarea
// carried fill, the wrapper sat at `flex: 0 0 auto` and broke the
// chain — the textarea had nothing to grow into.
//
// Post-fix: the user's layout attrs on Input.multiline (Ui.width /
// Ui.height / Ui.padding / Ui.spacing / Ui.alignX / Ui.alignY /
// Ui.nearby / Ui.pointer / Ui.overflow) hoist to the wrapper.
// The inner textarea gains implicit `Ui.width Ui.fill + Ui.height
// Ui.fill` so the cascade flows through. Form / event / visual
// attrs (Background.color, Font.color, htmlAttribute, onInput)
// stay on the textarea.
//
// This script boots /tmp/sky-issue-63-input (built by the caller),
// opens the page, and asserts:
//   1. The wrapper carries the hoisted layout intent (flex-grow > 0
//      OR align-self stretch).
//   2. The textarea fills the viewport minus the outer padding
//      (chain intact end-to-end).

import { chromium } from "playwright";
import { spawn } from "node:child_process";
import { existsSync } from "node:fs";
import { resolve } from "node:path";

const APP_DIR = process.env.SKY_ISSUE63_INPUT_DIR || "/tmp/sky-issue-63-input";
const PORT = parseInt(process.env.SKY_ISSUE63_INPUT_PORT || "8764", 10);
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

    await page.goto(`http://localhost:${PORT}/`, {
        waitUntil: "domcontentloaded",
        timeout: 15_000,
    });
    await page.waitForSelector("textarea", { timeout: 5_000 });
    await page.waitForTimeout(150);

    const m = await page.evaluate(() => {
        const ta = document.querySelector("textarea");
        if (!ta) return { error: "no textarea" };
        const wrapper = ta.parentElement;
        if (!wrapper) return { error: "missing wrapper" };
        const taR = ta.getBoundingClientRect();
        const wR = wrapper.getBoundingClientRect();
        const wCs = window.getComputedStyle(wrapper);
        return {
            ta: { w: taR.width, h: taR.height },
            wrapper: {
                w: wR.width,
                h: wR.height,
                flexGrow: wCs.flexGrow,
                alignSelf: wCs.alignSelf,
                display: wCs.display,
                style: wrapper.getAttribute("style") || "",
            },
            viewport: {
                w: window.innerWidth,
                h: window.innerHeight,
            },
        };
    });

    console.log("  measurements:", JSON.stringify(m, null, 2));

    if (m.error) {
        throw new Error(m.error);
    }

    // Gate 1: wrapper carries layout intent. Pre-fix the wrapper
    // had zero layout attrs — no flex-grow, no align-self stretch.
    // The fix's `splitLayoutAttrs` hoists `Ui.height Ui.fill` (and
    // friends) to the wrapper. We check for either:
    //   * computed `flex-grow > 0` (column-flex parent — main axis), OR
    //   * inline `align-self: stretch` (any flex parent — cross axis),
    //     OR
    //   * inline `flex-grow: 1` literal (typed lowering surface).
    const wGrow = parseFloat(m.wrapper.flexGrow);
    const hasFlex = wGrow > 0 || m.wrapper.style.includes("flex-grow:");
    const hasStretch = m.wrapper.alignSelf === "stretch"
        || m.wrapper.style.includes("align-self: stretch");
    if (!hasFlex && !hasStretch) {
        console.error(`FAIL — wrapper missing layout intent. style="${m.wrapper.style}"`);
        console.error("  Pre-fix: wrapper had no flex-grow / align-self at all.");
        console.error("  See sky-stdlib/Std/Ui/Input.sky § splitLayoutAttrs.");
        exitCode = 1;
    } else {
        console.log(`PASS — wrapper carries layout intent (flex-grow=${m.wrapper.flexGrow}, align-self=${m.wrapper.alignSelf})`);
    }

    // Gate 2: textarea fills the viewport minus the outer Ui.el's
    // `Ui.padding 16` (16 top + 16 bottom = 32 px chrome). Allow
    // 4 px tolerance for sub-pixel rounding.
    const VP = m.viewport.h;
    const PAD = 16;
    const TOL = 4;
    const expectedMin = VP - 2 * PAD - TOL;
    if (m.ta.h < expectedMin) {
        console.error(`FAIL — textarea height ${m.ta.h}px < expected min ${expectedMin}px (viewport ${VP}px).`);
        console.error("  This is the v0.15.55 follow-up regression to issue #63.");
        exitCode = 1;
    } else {
        console.log(`PASS — textarea fills viewport (height ${m.ta.h}px ≥ ${expectedMin}px)`);
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
