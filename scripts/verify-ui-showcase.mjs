#!/usr/bin/env node
// scripts/verify-ui-showcase.mjs
//
// Std.Ui regression gates — Cycle 5 renderer churn (mediaQuery /
// pseudo-classes / transitions / aspectRatio) gets a comprehensive
// computed-style + screenshot baseline BEFORE the renderer changes
// land. Issue #63's 5-attempt v0.14.7-v0.14.16 cycle is the
// "whack-a-mole risk" we close here.
//
// Two parallel checks per fixture:
//   1. Computed-style assertions (rock-solid, no anti-aliasing
//      flake) — flex-grow / width / height / display, etc.
//   2. PNG snapshots at fixed viewport sizes (desktop 1280×720
//      and mobile 375×667). Baselines in
//      examples/26-ui-showcase/snapshots/. Tolerance: ±3 px pixel
//      diff, 1 % colour delta — matches CLAUDE.md §"Critical
//      constraints" for cross-platform Chromium renders.
//
// Bounded by callers via `timeout 120 scripts/verify-ui-showcase.sh`.

import { chromium } from "playwright";
import { spawn } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { resolve, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);
const REPO_ROOT = resolve(__dirname, "..");

const APP_DIR = resolve(REPO_ROOT, "examples/26-ui-showcase");
const SNAP_DIR = resolve(APP_DIR, "snapshots");
const DIFF_DIR = resolve(REPO_ROOT, ".skycache/ui-showcase-diffs");
const PORT = parseInt(process.env.SKY_UI_SHOWCASE_PORT || "8826", 10);
const BIN = resolve(APP_DIR, "sky-out", "app");

const UPDATE_BASELINE = process.env.UPDATE_BASELINE === "1";
const SAVE_DIFFS = process.env.SAVE_DIFFS !== "0";

// CLAUDE.md §"Critical constraints" #3 — cross-platform Chromium
// renders text 1-2 px differently. ±3 px + 1 % colour delta.
const PIXEL_TOLERANCE = 3;
const COLOUR_DELTA = 0.01;
// Max fraction of pixels allowed to exceed the tolerance.
const PIXEL_DIFF_BUDGET = 0.01;

if (!existsSync(BIN)) {
    console.error(`FAIL — missing binary ${BIN}`);
    console.error(`  build first: cd ${APP_DIR} && TMPDIR=/tmp sky build src/Main.sky`);
    process.exit(2);
}
mkdirSync(SNAP_DIR, { recursive: true });
mkdirSync(DIFF_DIR, { recursive: true });

let serverLog = "";
const child = spawn(BIN, [], {
    cwd: APP_DIR,
    stdio: ["ignore", "pipe", "pipe"],
    env: { ...process.env, SKY_LIVE_PORT: String(PORT), SKY_DEV_BANNER: "off" },
});
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

// ─── PNG snapshot comparison ───────────────────────────────────
// Tiny self-contained PNG reader (8-bit RGBA only) so we don't
// pull a heavy image-diff lib for what is a deliberate "few-pixel"
// budget check. Uses zlib for IDAT decompression.
import { inflateSync } from "node:zlib";

function readPng(buf) {
    // PNG signature: 137 80 78 71 13 10 26 10
    if (buf[0] !== 0x89 || buf[1] !== 0x50) {
        throw new Error("not a PNG");
    }
    let offset = 8;
    let width = 0, height = 0, depth = 0, colourType = 0;
    let dataChunks = [];
    while (offset < buf.length) {
        const length = buf.readUInt32BE(offset);
        const type = buf.toString("ascii", offset + 4, offset + 8);
        const data = buf.subarray(offset + 8, offset + 8 + length);
        if (type === "IHDR") {
            width = data.readUInt32BE(0);
            height = data.readUInt32BE(4);
            depth = data.readUInt8(8);
            colourType = data.readUInt8(9);
        } else if (type === "IDAT") {
            dataChunks.push(data);
        } else if (type === "IEND") {
            break;
        }
        offset += 8 + length + 4; // chunk + crc
    }
    if (depth !== 8 || (colourType !== 6 && colourType !== 2)) {
        throw new Error(`unsupported PNG: depth=${depth} colourType=${colourType}`);
    }
    const channels = colourType === 6 ? 4 : 3;
    const raw = inflateSync(Buffer.concat(dataChunks));
    // Apply per-scanline filter (None / Sub / Up / Average / Paeth).
    const stride = width * channels;
    const pixels = Buffer.alloc(stride * height);
    let inPos = 0, outPos = 0;
    const prevRow = Buffer.alloc(stride);
    for (let y = 0; y < height; y++) {
        const filter = raw[inPos++];
        const row = pixels.subarray(outPos, outPos + stride);
        raw.copy(row, 0, inPos, inPos + stride);
        inPos += stride;
        if (filter === 1) {
            for (let i = channels; i < stride; i++) row[i] = (row[i] + row[i - channels]) & 0xff;
        } else if (filter === 2) {
            for (let i = 0; i < stride; i++) row[i] = (row[i] + prevRow[i]) & 0xff;
        } else if (filter === 3) {
            for (let i = 0; i < stride; i++) {
                const left = i < channels ? 0 : row[i - channels];
                row[i] = (row[i] + ((left + prevRow[i]) >> 1)) & 0xff;
            }
        } else if (filter === 4) {
            for (let i = 0; i < stride; i++) {
                const left = i < channels ? 0 : row[i - channels];
                const up = prevRow[i];
                const upLeft = i < channels ? 0 : prevRow[i - channels];
                const p = left + up - upLeft;
                const pa = Math.abs(p - left), pb = Math.abs(p - up), pc = Math.abs(p - upLeft);
                const paeth = pa <= pb && pa <= pc ? left : pb <= pc ? up : upLeft;
                row[i] = (row[i] + paeth) & 0xff;
            }
        }
        row.copy(prevRow);
        outPos += stride;
    }
    return { width, height, channels, pixels };
}

function diffPng(a, b) {
    if (a.width !== b.width || a.height !== b.height) {
        return { match: false, reason: `size mismatch ${a.width}x${a.height} vs ${b.width}x${b.height}`, ratio: 1 };
    }
    if (a.channels !== b.channels) {
        return { match: false, reason: `channel mismatch ${a.channels} vs ${b.channels}`, ratio: 1 };
    }
    let bad = 0;
    const total = a.width * a.height;
    const ch = a.channels;
    for (let i = 0; i < a.pixels.length; i += ch) {
        // RGB delta — ignore alpha for the colour-delta check.
        const dr = Math.abs(a.pixels[i] - b.pixels[i]);
        const dg = Math.abs(a.pixels[i + 1] - b.pixels[i + 1]);
        const db = Math.abs(a.pixels[i + 2] - b.pixels[i + 2]);
        // Normalised mean delta in [0, 1].
        const delta = (dr + dg + db) / (3 * 255);
        if (delta > COLOUR_DELTA) bad++;
    }
    const ratio = bad / total;
    return { match: ratio <= PIXEL_DIFF_BUDGET, ratio, badCount: bad, total };
}

let failures = [];

function ok(label) {
    console.log(`  PASS  ${label}`);
}
function fail(label, msg) {
    console.error(`  FAIL  ${label} — ${msg}`);
    failures.push(`${label}: ${msg}`);
}

async function snapshot(page, name, sel, viewport) {
    const baselinePath = resolve(SNAP_DIR, `${name}-${viewport}.png`);
    const target = sel ? await page.locator(`[data-test-id="${sel}"]`) : page;
    const buf = await target.screenshot({ animations: "disabled" });
    if (UPDATE_BASELINE || !existsSync(baselinePath)) {
        writeFileSync(baselinePath, buf);
        console.log(`  WRITE ${name}-${viewport}.png (baseline)`);
        return;
    }
    const baseline = readPng(readFileSync(baselinePath));
    let current;
    try {
        current = readPng(buf);
    } catch (e) {
        fail(`${name}-${viewport}`, `current PNG parse: ${e.message}`);
        return;
    }
    const diff = diffPng(baseline, current);
    if (diff.match) {
        ok(`snapshot ${name}-${viewport} (${(diff.ratio * 100).toFixed(2)}% diff)`);
    } else if (diff.reason) {
        const diffPath = resolve(DIFF_DIR, `${name}-${viewport}.current.png`);
        if (SAVE_DIFFS) writeFileSync(diffPath, buf);
        fail(`snapshot ${name}-${viewport}`, `${diff.reason}; saved current → ${diffPath}`);
    } else {
        const diffPath = resolve(DIFF_DIR, `${name}-${viewport}.current.png`);
        if (SAVE_DIFFS) writeFileSync(diffPath, buf);
        fail(`snapshot ${name}-${viewport}`,
            `${(diff.ratio * 100).toFixed(2)}% pixels exceed Δ=${COLOUR_DELTA} (budget ${(PIXEL_DIFF_BUDGET * 100).toFixed(2)}%); ` +
            `saved current → ${diffPath}`);
    }
}

async function measure(page, sel) {
    return await page.evaluate((s) => {
        const el = document.querySelector(`[data-test-id="${s}"]`);
        if (!el) return null;
        const r = el.getBoundingClientRect();
        const cs = window.getComputedStyle(el);
        return {
            x: r.x, y: r.y,
            width: r.width, height: r.height,
            display: cs.display,
            flexDirection: cs.flexDirection,
            flexGrow: parseFloat(cs.flexGrow) || 0,
            minWidth: cs.minWidth,
            minHeight: cs.minHeight,
            gridTemplateColumns: cs.gridTemplateColumns,
            gridTemplateRows: cs.gridTemplateRows,
            aspectRatio: cs.aspectRatio,
        };
    }, sel);
}

function approxEq(label, actual, expected, tol) {
    if (actual === null || actual === undefined) {
        fail(label, `value is ${actual}`);
        return;
    }
    if (Math.abs(actual - expected) <= tol) {
        ok(`${label} (${actual.toFixed(1)} ≈ ${expected}, ±${tol})`);
    } else {
        fail(label, `${actual.toFixed(1)} ≠ ${expected} (tol ±${tol})`);
    }
}

let exitCode = 0;
try {
    if (!(await waitForReady(15_000))) {
        throw new Error(`server did not become ready in 15s; log:\n${serverLog}`);
    }

    const browser = await chromium.launch();
    try {
        // ─── Desktop pass: 1280×720 ──────────────────────────────
        const context = await browser.newContext({
            viewport: { width: 1280, height: 720 },
            deviceScaleFactor: 1,
            reducedMotion: "reduce",
            colorScheme: "light",
        });
        // Disable animations + force a uniform fallback font stack
        // for deterministic text rendering across hosts.
        await context.addInitScript(() => {
            // Defer until <head> exists. addInitScript runs at "earliest"
            // — before document.head is parsed — so the immediate
            // appendChild blows up. Schedule on DOMContentLoaded
            // (or run immediately if already past).
            const inject = () => {
                if (!document.head) return;
                if (document.getElementById("__ui-showcase-overrides")) return;
                const css = document.createElement("style");
                css.id = "__ui-showcase-overrides";
                css.textContent =
                    `*, *::before, *::after {
                       transition: none !important;
                       animation: none !important;
                     }
                     body, html, #sky-root, [data-test-id] {
                       font-family: monospace !important;
                     }`;
                document.head.appendChild(css);
            };
            if (document.readyState === "loading") {
                document.addEventListener("DOMContentLoaded", inject, { once: true });
            } else {
                inject();
            }
        });
        const page = await context.newPage();
        page.on("pageerror", (e) => {
            fail("pageerror", e.message);
        });

        await page.goto(`http://localhost:${PORT}/`, {
            waitUntil: "domcontentloaded",
            timeout: 15_000,
        });
        await page.waitForSelector('[data-test-id="showcase-root"]', { timeout: 15_000 });
        await page.waitForTimeout(200);

        // ─── 1. Deep-nesting fill (#63 class) ───────────────────
        console.log("--- #63-class deep-nesting fill ---");
        // triple-outer is fixed height 180; inner should reach height 180 - 16 (padding).
        const tripleInner = await measure(page, "triple-inner");
        approxEq("triple-inner height", tripleInner?.height, 180 - 16, PIXEL_TOLERANCE);
        if (tripleInner && tripleInner.flexGrow !== 1) {
            fail("triple-inner flex-grow", `expected 1, got ${tripleInner.flexGrow}`);
        } else if (tripleInner) {
            ok(`triple-inner flex-grow = 1`);
        }

        // Input.multiline fill (#403 / v0.15.55 — the #63 follow-up).
        // The outer column is fixed-height 120; Input.multiline carries
        // `Ui.height fill` so the textarea should fill the column.
        // Pre-fix the wrapWithLabel-emitted wrapper carried no layout
        // attrs at all, so the textarea collapsed at content-line
        // height (~22 px) even though the user supplied fill.
        const inputTa = await page.evaluate(() => {
            const ta = document.querySelector('[data-test-id="input-multiline-textarea"]');
            if (!ta) return null;
            const r = ta.getBoundingClientRect();
            return { width: r.width, height: r.height };
        });
        if (inputTa) {
            // Outer is 120px tall; textarea should fill it within tolerance.
            approxEq("input-multiline-textarea height", inputTa.height, 120, PIXEL_TOLERANCE);
        } else {
            fail("input-multiline-textarea", "missing textarea[data-test-id=input-multiline-textarea]");
        }

        // F1 contract (v0.15.55) — the inner control's implicit
        // `Ui.height Ui.fill` (injected by `implicitFillIfHoisted`
        // when ≥1 layout attr was hoisted to the wrapper) must lower
        // through `heightFillFor` per its parent context. In a column
        // wrapper (LabelHidden = Ui.el = column parent) the inner
        // textarea's height-fill is MAIN axis → `flex-grow: 1; min-
        // height: 0;`. We assert the inline-style does NOT contain
        // the harmful `height: 100%;` substring on the cross-axis
        // child element. Pre-v0.15.55 had `align-self: stretch;
        // height: 100%;` on row-cross-axis children which collapsed
        // under indefinite parents.
        const taInline = await page.evaluate(() => {
            const ta = document.querySelector('[data-test-id="input-multiline-textarea"]');
            if (!ta) return null;
            return { style: ta.getAttribute("style") || "" };
        });
        if (taInline) {
            // Stylesheet shouldn't carry the legacy `height: 100%;`
            // signature in any cross-axis context. (Main-axis is
            // flex-grow + min-height: 0; no `100%` involvement.)
            if (taInline.style.includes("height: 100%;")) {
                fail("F1 input-multiline-textarea cross-axis",
                    `inline style still emits the pre-F1 height: 100%: ${taInline.style}`);
            } else {
                ok(`F1 input-multiline-textarea: no legacy height: 100% (style="${taInline.style.slice(0, 80)}...")`);
            }
        }

        // wrapWithLabel shape (AsRow propagation, the canonical
        // issue #63 reproducer for the horizontal axis).
        const wrapMid = await measure(page, "wrap-mid");
        const wrapInner = await measure(page, "wrap-inner");
        if (wrapMid && wrapInner) {
            // Outer is 360 px wide. Wrapper inherits fill via
            // propagation; inner stretches to wrapper's width.
            approxEq("wrap-inner width", wrapInner.width, 360, PIXEL_TOLERANCE);
            approxEq("wrap-inner height", wrapInner.height, 80, PIXEL_TOLERANCE);
            if (wrapMid.flexGrow !== 1) {
                fail("wrap-mid flex-grow", `expected 1 from propagation, got ${wrapMid.flexGrow}`);
            } else {
                ok(`wrap-mid flex-grow = 1 (propagated)`);
            }
        }

        // fillPortion 1:2:3 — widths in ratio. Outer card padding = 16 each side,
        // gap 8 × 2 = 16, so available = card-width - 32 - 16 = card-width - 48.
        const p1 = await measure(page, "portion-1");
        const p2 = await measure(page, "portion-2");
        const p3 = await measure(page, "portion-3");
        if (p1 && p2 && p3) {
            // 1:2:3 — p2 ≈ 2 × p1, p3 ≈ 3 × p1 (±3px tolerance per side).
            approxEq("portion-2 / portion-1 ratio", p2.width / p1.width, 2, 0.1);
            approxEq("portion-3 / portion-1 ratio", p3.width / p1.width, 3, 0.1);
        } else {
            fail("portion-row", "missing one of portion-1/2/3");
        }

        // Fixed-px siblings of fill child: left 80, right 80, mid fills.
        const fl = await measure(page, "fixed-left");
        const fm = await measure(page, "fill-mid");
        const fr = await measure(page, "fixed-right");
        if (fl && fm && fr) {
            approxEq("fixed-left width", fl.width, 80, PIXEL_TOLERANCE);
            approxEq("fixed-right width", fr.width, 80, PIXEL_TOLERANCE);
            if (fm.flexGrow !== 1) {
                fail("fill-mid flex-grow", `expected 1, got ${fm.flexGrow}`);
            } else {
                ok(`fill-mid flex-grow = 1`);
            }
            // Mid should be substantially wider than the 80px siblings.
            if (fm.width < 200) {
                fail("fill-mid width", `expected ≥200 (filling space), got ${fm.width}`);
            } else {
                ok(`fill-mid width ${fm.width.toFixed(0)} (filling)`);
            }
        } else {
            fail("fixed-fill-row", "missing fixed/fill children");
        }

        // ─── 2. Aspect ratio (#379) — typed `Ui.aspectRatio*` ────
        console.log("--- aspect ratio ---");
        const a320 = await measure(page, "aspect-320");
        const a160 = await measure(page, "aspect-160");
        const aSquare = await measure(page, "aspect-square");
        const aFillCine = await measure(page, "aspect-fill-cinema");
        if (a320) {
            // 320 wide → 16:9 → ~180 high
            approxEq("aspect-320 width", a320.width, 320, PIXEL_TOLERANCE);
            approxEq("aspect-320 ratio", a320.width / a320.height, 16 / 9, 0.05);
            // CSS aspect-ratio property reads as `auto N / M` or `N / M`
            // depending on the browser — verify it's present and matches 16/9.
            if (a320.aspectRatio && (a320.aspectRatio.includes("16 / 9") || a320.aspectRatio.includes("auto 16 / 9"))) {
                ok(`aspect-320 aspect-ratio = ${a320.aspectRatio}`);
            } else {
                fail("aspect-320 aspect-ratio", `expected '16 / 9', got '${a320.aspectRatio}'`);
            }
        }
        if (a160) {
            // 160 wide, 1.777 decimal → ~90 high
            approxEq("aspect-160 width", a160.width, 160, PIXEL_TOLERANCE);
            approxEq("aspect-160 ratio", a160.width / a160.height, 1.777, 0.05);
        }
        if (aSquare) {
            // 100×100 square via Ui.square
            approxEq("aspect-square width", aSquare.width, 100, PIXEL_TOLERANCE);
            approxEq("aspect-square height", aSquare.height, 100, PIXEL_TOLERANCE);
        }
        if (aFillCine) {
            // Cinemascope 2.35:1; width fills the card body
            approxEq("aspect-fill-cinema ratio", aFillCine.width / aFillCine.height, 2.35, 0.05);
        }

        // ─── 3. Grid (#379) — auto-fill default ─────────────────
        console.log("--- grid ---");
        const grid = await measure(page, "grid-row");
        if (grid) {
            const tracks = (grid.gridTemplateColumns || "").trim().split(/\s+/).filter(Boolean);
            if (tracks.length === 3) {
                ok(`grid-row has 3 tracks (${grid.gridTemplateColumns})`);
            } else {
                fail("grid-row tracks", `expected 3, got ${tracks.length}: ${grid.gridTemplateColumns}`);
            }
        }

        // ─── 3b. Grid tracks (#379) — typed Std.Ui.Grid ─────────
        console.log("--- grid tracks (typed) ---");
        const gtSidebar = await measure(page, "grid-tracks-sidebar");
        const gtAutoFit = await measure(page, "grid-tracks-autofit");
        const gtAuto = await measure(page, "grid-tracks-auto");
        if (gtSidebar) {
            // Sidebar: 1fr 200px 1fr → exactly 3 tracks; middle = 200px.
            const t = (gtSidebar.gridTemplateColumns || "").trim().split(/\s+/).filter(Boolean);
            if (t.length === 3 && t[1] === "200px") {
                ok(`grid-tracks-sidebar = ${gtSidebar.gridTemplateColumns}`);
            } else {
                fail("grid-tracks-sidebar layout", `expected 3 tracks middle=200px, got '${gtSidebar.gridTemplateColumns}'`);
            }
        }
        if (gtAutoFit) {
            // Auto-fit at desktop ≥ 400px → expect 2-3 tracks (depends on card body width).
            const t = (gtAutoFit.gridTemplateColumns || "").trim().split(/\s+/).filter(Boolean);
            if (t.length >= 2) {
                ok(`grid-tracks-autofit produced ${t.length} tracks (${gtAutoFit.gridTemplateColumns})`);
            } else {
                fail("grid-tracks-autofit", `expected ≥2 tracks, got ${t.length}: ${gtAutoFit.gridTemplateColumns}`);
            }
        }
        if (gtAuto) {
            // auto 1fr → 2 tracks, second is fractional (huge px value
            // when computed). Just confirm 2 tracks emitted.
            const t = (gtAuto.gridTemplateColumns || "").trim().split(/\s+/).filter(Boolean);
            if (t.length === 2) {
                ok(`grid-tracks-auto = ${gtAuto.gridTemplateColumns}`);
            } else {
                fail("grid-tracks-auto", `expected 2 tracks (auto + 1fr), got ${t.length}: ${gtAuto.gridTemplateColumns}`);
            }
        }

        // ─── 3c. Aspect ratio at multiple widths (#379) ──────────
        // Resize the page across three widths; the `aspect-fill-cinema`
        // box has `Ui.width Ui.fill + Ui.cinemascope` so its height MUST
        // track its width via the browser's `aspect-ratio` solver. This
        // is the load-bearing regression — the moment the browser stops
        // honouring `aspect-ratio` (or our CSS-emission drifts), the
        // height drops to 0 or matches some unrelated content height.
        console.log("--- aspect ratio @ multi-width ---");
        for (const w of [400, 800, 1200]) {
            await page.setViewportSize({ width: w, height: 720 });
            await page.waitForTimeout(120);
            const a = await measure(page, "aspect-fill-cinema");
            if (a && a.width > 0) {
                const r = a.width / a.height;
                if (Math.abs(r - 2.35) <= 0.05) {
                    ok(`aspect-fill-cinema @ viewport=${w} → ${a.width.toFixed(0)}×${a.height.toFixed(0)} (ratio ${r.toFixed(2)})`);
                } else {
                    fail(`aspect-fill-cinema @ viewport=${w}`, `ratio ${r.toFixed(2)} ≠ 2.35 (tol 0.05); box ${a.width.toFixed(0)}×${a.height.toFixed(0)}`);
                }
            } else {
                fail(`aspect-fill-cinema @ viewport=${w}`, "missing or zero-size element");
            }
        }
        // Reset to desktop viewport before snapshots.
        await page.setViewportSize({ width: 1280, height: 720 });
        await page.waitForTimeout(120);

        // ─── Media query / breakpoint (#376) ────────────────────
        // Desktop pass: viewport 1280 ≥ 768, so `Ui.breakpoint Ui.mobile`
        // overrides DO NOT fire — base styles (padding 8, blue) apply.
        // `Ui.mediaQuery "(min-width: 800px)"` DOES fire — overrides apply (green bg).
        console.log("--- media-query / breakpoint (desktop) ---");
        const mqMobileDesktop = await measure(page, "mq-mobile-box");
        const mqRawDesktop = await measure(page, "mq-raw-box");
        if (mqMobileDesktop) {
            // Inner box has base padding 8 + parent's default (no breakpoint
            // override on desktop). The CSS engine should NOT have applied
            // `padding: 24px` to the outer wrapper.
            const bg = await page.evaluate(
                (s) => window.getComputedStyle(document.querySelector(`[data-test-id="${s}"]`)).backgroundColor,
                "mq-mobile-box",
            );
            // Expect the BLUE base — `rgb(96, 128, 224)`. If the breakpoint
            // wrongly fired on desktop, the inner box would still be blue
            // (the override is on its parent), so we instead check the
            // PARENT'S background colour through the wrapper sky-id.
            if (!bg.includes("96") || !bg.includes("128") || !bg.includes("224")) {
                fail("mq-mobile-box desktop bg", `expected base blue, got ${bg}`);
            } else {
                ok(`mq-mobile-box desktop bg = blue (no mobile override)`);
            }
        }
        if (mqRawDesktop) {
            // The wrapper around mq-raw-box gets `background-color: green`
            // when viewport ≥ 800px. Check the wrapper's bg.
            const parentBg = await page.evaluate(() => {
                const el = document.querySelector('[data-test-id="mq-raw-box"]');
                if (!el || !el.parentElement) return "";
                return window.getComputedStyle(el.parentElement).backgroundColor;
            });
            // Expect green: rgb(96, 176, 104).
            if (parentBg.includes("96") && parentBg.includes("176") && parentBg.includes("104")) {
                ok(`mq-raw parent bg = green (≥800px override fires at desktop 1280)`);
            } else {
                fail("mq-raw parent bg", `expected green ≥800px override, got ${parentBg}`);
            }
        }
        // The breakpoint wrappers must expose their sky-id-scoped <style>
        // blocks — verify at least one is in the DOM.
        const mqStyleCount = await page.evaluate(
            () => document.querySelectorAll('style[data-sky-mq]').length,
        );
        if (mqStyleCount >= 2) {
            ok(`media-query <style> blocks present (count=${mqStyleCount})`);
        } else {
            fail("media-query <style>", `expected ≥2 data-sky-mq blocks, found ${mqStyleCount}`);
        }

        // ─── 4. Pseudo-class stubs (#377) ────────────────────────
        console.log("--- pseudo-class stubs ---");
        const hover = await measure(page, "hover-button");
        const focus = await measure(page, "focus-input");
        const active = await measure(page, "active-link");
        if (hover && hover.width > 0) ok("hover-button rendered");
        else fail("hover-button", "missing or zero-size");
        if (focus && focus.width > 0) ok("focus-input rendered");
        else fail("focus-input", "missing or zero-size");
        if (active && active.width > 0) ok("active-link rendered");
        else fail("active-link", "missing or zero-size");

        // The runtime injects a `<style data-sky-pc=...>` block as
        // the first child of any element carrying pseudo-class
        // rules. Confirm at least one such block exists.
        const pcStyleCount = await page.evaluate(() =>
            document.querySelectorAll("style[data-sky-pc]").length,
        );
        if (pcStyleCount >= 1) {
            ok(`pseudo-class <style> blocks present (count=${pcStyleCount})`);
        } else {
            fail("pseudo-class <style>", `expected ≥1 data-sky-pc blocks, found ${pcStyleCount}`);
        }

        // The `:hover` rule MUST be auto-wrapped in `@media (hover:
        // hover)` so it doesn't fire as sticky-hover on touch
        // devices. Inspect every emitted pseudo <style> block.
        const hoverGatePresent = await page.evaluate(() => {
            const blocks = document.querySelectorAll("style[data-sky-pc]");
            for (const b of blocks) {
                if (b.textContent.includes(":hover")
                    && b.textContent.includes("@media (hover: hover)")) {
                    return true;
                }
            }
            return false;
        });
        if (hoverGatePresent) {
            ok(":hover wrapped in @media (hover: hover) (touch-safe)");
        } else {
            fail(":hover hover-gate", "no @media (hover: hover) wrap found");
        }

        // Snapshot the hover-button in its hovered state. Playwright's
        // page.hover() triggers the pointer:over+CSS :hover state.
        // Need a desktop browser with `(hover: hover)` to fire the
        // gated rule — which is the default for chromium on a
        // 1280×720 viewport (mouse pointer, hover-capable).
        try {
            await page.hover('[data-test-id="hover-button"]');
            await page.waitForTimeout(80);
            await snapshot(
                page, "hover-button-state", "hover-button", "desktop",
            );
        } catch (e) {
            fail("hover-button :hover snapshot", String(e));
        }

        // Focus the input via `.focus()` AND `.click()` so
        // `:focus-visible` fires (in chromium, programmatic focus
        // doesn't always paint focus-visible; clicking the input
        // gives a stable visible-ring snapshot baseline).
        try {
            await page.focus('[data-test-id="focus-input"]');
            await page.waitForTimeout(80);
            await snapshot(
                page, "focus-input-state", "focus-input-wrap", "desktop",
            );
        } catch (e) {
            fail("focus-input :focus snapshot", String(e));
        }

        // Static disabled-button snapshot — confirms the disabled
        // styling renders cleanly. (We don't trigger :disabled at
        // runtime; the HTML `disabled=true` attribute already does.)
        try {
            await snapshot(
                page, "disabled-button-state", "disabled-wrap", "desktop",
            );
        } catch (e) {
            fail("disabled-button snapshot", String(e));
        }

        // ─── 5. Transitions + animations (#378) ──────────────────
        console.log("--- transitions + animations (#378) ---");
        // The Std.Ui transition / animation injection passes both
        // wrap their rules in `@media (prefers-reduced-motion:
        // no-preference)` by default. Inspect every emitted style
        // block to confirm the gate is present (the snapshot
        // baseline used `reducedMotion: "reduce"` so the actual
        // movement is suppressed, but the gated CSS is in the DOM).
        const trStyleCount = await page.evaluate(() =>
            document.querySelectorAll("style[data-sky-tr]").length,
        );
        if (trStyleCount >= 1) {
            ok(`transition <style> blocks present (count=${trStyleCount})`);
        } else {
            fail("transition <style>", `expected ≥1 data-sky-tr blocks, found ${trStyleCount}`);
        }
        const animStyleCount = await page.evaluate(() =>
            document.querySelectorAll("style[data-sky-anim]").length,
        );
        if (animStyleCount >= 2) {
            ok(`animation <style> blocks present (count=${animStyleCount})`);
        } else {
            fail("animation <style>", `expected ≥2 data-sky-anim blocks, found ${animStyleCount}`);
        }
        // Reduced-motion gate present on the gated animation
        // (animatedFadeIn carries respectReducedMotion = True; the
        // spinner opts out, so we look at the fade-in element).
        const fadeInGated = await page.evaluate(() => {
            const el = document.querySelector('[data-test-id="animated-fade-in"]');
            if (!el) return false;
            const style = el.querySelector("style[data-sky-anim]");
            if (!style) return false;
            return style.textContent.includes(
                "@media (prefers-reduced-motion: no-preference)",
            );
        });
        if (fadeInGated) {
            ok("animated-fade-in wrapped in @media (prefers-reduced-motion: no-preference)");
        } else {
            fail("fade-in reduced-motion gate", "expected gate missing");
        }
        // Spinner opts OUT — its style block MUST NOT have the gate.
        const spinnerUngated = await page.evaluate(() => {
            const el = document.querySelector('[data-test-id="animated-spinner"]');
            if (!el) return false;
            const style = el.querySelector("style[data-sky-anim]");
            if (!style) return false;
            // Animation rule (not just @keyframes) must NOT be gated.
            return !style.textContent
                .replace(/@keyframes[^}]*\{[^}]*\}/g, "")
                .includes("@media (prefers-reduced-motion: no-preference)");
        });
        if (spinnerUngated) {
            ok("animated-spinner correctly opts out of reduced-motion gate");
        } else {
            fail("spinner ungated", "spinner rule should bypass reduced-motion gate");
        }
        // Transition shorthand should be present on the
        // transition-button's style block.
        const trShorthand = await page.evaluate(() => {
            const el = document.querySelector('[data-test-id="transition-button"]');
            if (!el) return "";
            const style = el.querySelector("style[data-sky-tr]");
            return style ? style.textContent : "";
        });
        if (trShorthand.includes("transition: background-color 200ms ease-out")) {
            ok("transition-button shorthand = `background-color 200ms ease-out`");
        } else {
            fail("transition shorthand", `expected 'background-color 200ms ease-out', got: ${trShorthand}`);
        }
        // Animation @keyframes effective name MUST be auto-suffixed
        // with the element's sky-id-derived ident (prevents global
        // collisions across elements).
        const fadeInKeyframes = await page.evaluate(() => {
            const el = document.querySelector('[data-test-id="animated-fade-in"]');
            if (!el) return "";
            const style = el.querySelector("style[data-sky-anim]");
            return style ? style.textContent : "";
        });
        if (/@keyframes fadeInUp__\w+/.test(fadeInKeyframes)) {
            ok("animated-fade-in @keyframes name auto-suffixed with sky-id");
        } else {
            fail("fadeInUp suffix", `expected fadeInUp__<sky-id>, got: ${fadeInKeyframes}`);
        }

        // ─── 6. Snapshots ───────────────────────────────────────
        console.log("--- snapshots (desktop) ---");
        for (const section of [
            "triple-nested", "wrap-label", "portion",
            "fixed-fill", "aspect", "grid", "grid-tracks",
            "pseudo", "viewport", "mediaquery", "motion",
        ]) {
            await snapshot(page, section, `section-${section}`, "desktop");
        }
        // A "page-shape" snapshot too — full page screenshot for the
        // human reviewer to eyeball.
        const fullBuf = await page.screenshot({ fullPage: true, animations: "disabled" });
        writeFileSync(resolve(SNAP_DIR, "fullpage-desktop.png"), fullBuf);

        await context.close();

        // ─── Mobile pass: 375×667 ───────────────────────────────
        const mobileCtx = await browser.newContext({
            viewport: { width: 375, height: 667 },
            deviceScaleFactor: 1,
            reducedMotion: "reduce",
            colorScheme: "light",
        });
        await mobileCtx.addInitScript(() => {
            const inject = () => {
                if (!document.head) return;
                if (document.getElementById("__ui-showcase-overrides")) return;
                const css = document.createElement("style");
                css.id = "__ui-showcase-overrides";
                css.textContent =
                    `*, *::before, *::after { transition: none !important; animation: none !important; }
                     body, html, #sky-root, [data-test-id] { font-family: monospace !important; }`;
                document.head.appendChild(css);
            };
            if (document.readyState === "loading") {
                document.addEventListener("DOMContentLoaded", inject, { once: true });
            } else {
                inject();
            }
        });
        const mp = await mobileCtx.newPage();
        await mp.goto(`http://localhost:${PORT}/`, {
            waitUntil: "domcontentloaded",
            timeout: 15_000,
        });
        await mp.waitForSelector('[data-test-id="showcase-root"]', { timeout: 15_000 });
        await mp.waitForTimeout(200);

        console.log("--- snapshots (mobile) ---");
        for (const section of [
            "triple-nested", "wrap-label", "portion",
            "fixed-fill", "viewport", "mediaquery",
        ]) {
            await snapshot(mp, section, `section-${section}`, "mobile");
        }

        // ─── Media query — mobile assertions ─────────────────────
        // At 375 px the `Ui.breakpoint Ui.mobile` override IS active:
        // the wrapper around mq-mobile-box has bg = red, padding = 24.
        console.log("--- media-query (mobile assertions) ---");
        const mqMobileParentBg = await mp.evaluate(() => {
            const el = document.querySelector('[data-test-id="mq-mobile-box"]');
            if (!el || !el.parentElement) return "";
            return window.getComputedStyle(el.parentElement).backgroundColor;
        });
        // Expect rgb(224, 96, 96).
        if (mqMobileParentBg.includes("224") && mqMobileParentBg.includes("96")) {
            ok(`mq-mobile parent bg = red (mobile breakpoint fires at 375px)`);
        } else {
            fail("mq-mobile parent bg (mobile)", `expected red mobile override, got ${mqMobileParentBg}`);
        }
        // `(min-width: 800px)` should NOT fire at 375 — wrapper stays
        // its base colour (transparent, since the wrapper has no base bg).
        const mqRawParentBg = await mp.evaluate(() => {
            const el = document.querySelector('[data-test-id="mq-raw-box"]');
            if (!el || !el.parentElement) return "";
            return window.getComputedStyle(el.parentElement).backgroundColor;
        });
        if (mqRawParentBg.includes("96, 176, 104")) {
            fail("mq-raw parent bg (mobile)", `green override leaked into mobile: ${mqRawParentBg}`);
        } else {
            ok(`mq-raw parent bg ≠ green at 375px (≥800px override correctly suppressed)`);
        }
        const mobileFull = await mp.screenshot({ fullPage: true, animations: "disabled" });
        writeFileSync(resolve(SNAP_DIR, "fullpage-mobile.png"), mobileFull);

        await mobileCtx.close();
    } finally {
        await browser.close();
    }
} catch (e) {
    console.error(`FAIL — ${e.message}`);
    exitCode = 1;
} finally {
    child.kill("SIGTERM");
    await new Promise((r) => setTimeout(r, 200));
    if (!child.killed) child.kill("SIGKILL");
}

if (failures.length > 0) {
    console.error(`\n${failures.length} failure(s):`);
    for (const f of failures) console.error(`  ${f}`);
    exitCode = 1;
}

if (exitCode === 0) {
    console.log("\nPASS — ui-showcase regression gates green");
} else {
    console.error("\nFAIL — see above");
}
process.exit(exitCode);
