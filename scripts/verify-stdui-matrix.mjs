#!/usr/bin/env node
// scripts/verify-stdui-matrix.mjs
//
// Std.Ui correctness matrix — the permanent regression set folded
// out of the v0.15.55 Cycle 7 audit rig
// (`docs/v0.15.x-hardening/CYCLE-07-STDUI-AUDIT.md`). Single runnable
// script: it bootstraps four fixtures into a scratch dir, runs
// `sky build` on each, boots them as Sky.Live apps, opens Playwright,
// and asserts the F1 + F2 invariants.
//
// Four fixtures:
//
//   * Z1-row-fill-in-layout — `Ui.layout [] (Ui.row [Ui.height fill]
//     [text])`. Direct flex child of the layout wrapper. Pre-fix
//     this passed at HEAD (the row's main-axis fill resolved cleanly
//     against the wrapper's `min-height: 100vh`), so the gate
//     fences against future regression.
//
//   * Z2-input-multiline-in-row-fill — `Ui.row [w fill, h fill]
//     [Input.multiline [w fill, h fill] cfg]`. F1+F2 canonical case.
//     Pre-F1 the textarea collapsed to 51 px (the wrapWithLabel
//     `<div>` carried `align-self: stretch; height: 100%;`, the
//     `100%` resolved against an indefinite parent → fell back to
//     content size). With F1+F2 the textarea fills the viewport
//     minus padding.
//
//   * Z3-three-pane-app-shell — classic header + sidebar + main
//     layout. Pre-F1 every child of the inner row that asked for
//     `Ui.height Ui.fill` collapsed to 22 px. Post-F1 sidebar +
//     main column + content all stretch to the row's flex-derived
//     height (~740 px after the 60 px header).
//
//   * M-in-page-matrix — a small in-page matrix exercising the
//     B-flex-chain + D-input + E-align groups from the audit rig.
//     Every row has a definite (`px 320`) frame so the matrix
//     measures with explicit-parent definiteness — same coverage
//     net as the audit rig (which was wall-to-wall PASS at HEAD).
//
// Usage:
//   node scripts/verify-stdui-matrix.mjs
//   SKY_MATRIX_KEEP=1 node scripts/verify-stdui-matrix.mjs   # keep scratch dir
//   SKY_MATRIX_BIN=./sky-out/sky node scripts/verify-stdui-matrix.mjs

import { chromium } from "playwright";
import { spawn, spawnSync } from "node:child_process";
import { existsSync, mkdtempSync, writeFileSync, mkdirSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { resolve, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = resolve(__dirname, "..");
const SKY_BIN = resolve(process.env.SKY_MATRIX_BIN || `${REPO_ROOT}/sky-out/sky`);
const VIEWPORT = { width: 1280, height: 800 };
const KEEP_DIR = !!process.env.SKY_MATRIX_KEEP;

if (!existsSync(SKY_BIN)) {
    console.error(`FAIL — missing sky binary at ${SKY_BIN}`);
    console.error("  build first: cabal install --overwrite-policy=always --installdir=./sky-out --install-method=copy exe:sky");
    process.exit(2);
}

// ─── Fixtures ────────────────────────────────────────────────────

const FIXTURES = [
    {
        id: "Z1",
        name: "Z1-row-fill-in-layout",
        port: 8810,
        descr: "Ui.layout [] (Ui.row [Ui.height fill] [...]) — fence",
        source: `module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Std.Cmd as Cmd
import Std.Sub as Sub
import Std.Live exposing (app, route)
import Std.Ui as Ui
import Std.Ui.Background as Background


type alias Model = { dummy : Int }
type Msg = Noop
type Page = MainPage


init : a -> ( Model, Cmd Msg )
init _ = ( { dummy = 0 }, Cmd.none )

update : Msg -> Model -> ( Model, Cmd Msg )
update _ m = ( m, Cmd.none )

subscriptions : Model -> Sub Msg
subscriptions _ = Sub.none


view : Model -> any
view _ =
    Ui.layout
        []
        (Ui.row
            [ Ui.height Ui.fill
            , Ui.width Ui.fill
            , Background.color (Ui.rgb 60 80 200)
            , Ui.htmlAttribute "data-probe" "Z1-row"
            ]
            [ Ui.text "x" ])


main =
    app
        { init = init, update = update, view = view, subscriptions = subscriptions
        , routes = [ route "/" MainPage ], notFound = MainPage
        }
`,
        assertions: async (page) => {
            const m = await page.evaluate(() => {
                const r = document.querySelector("[data-probe='Z1-row']");
                const rect = r.getBoundingClientRect();
                return { h: rect.height, vp: window.innerHeight };
            });
            // The row's main-axis fill resolves against the wrapper's
            // 100vh min-height — should be ≥ vp - 40 (browser margins,
            // browser chrome variance).
            const min = m.vp - 40;
            if (m.h < min) {
                return { ok: false, msg: `row height ${m.h.toFixed(0)}px < expected ${min}px (vp ${m.vp}px)` };
            }
            return { ok: true, msg: `row fills viewport (h=${m.h.toFixed(0)}px, vp=${m.vp}px)` };
        },
    },

    {
        id: "Z2",
        name: "Z2-input-multiline-in-row-fill",
        port: 8811,
        descr: "Ui.row [w/h fill] [Input.multiline [w/h fill]] — F1+F2 canonical",
        source: `module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Std.Cmd as Cmd
import Std.Sub as Sub
import Std.Live exposing (app, route)
import Std.Ui as Ui
import Std.Ui.Input as Input


type alias Model = { dummy : Int }
type Msg = Noop | UpdateText String
type Page = MainPage


init : a -> ( Model, Cmd Msg )
init _ = ( { dummy = 0 }, Cmd.none )

update : Msg -> Model -> ( Model, Cmd Msg )
update _ m = ( m, Cmd.none )

subscriptions : Model -> Sub Msg
subscriptions _ = Sub.none


view : Model -> any
view _ =
    Ui.layout
        []
        (Ui.row
            [ Ui.width Ui.fill, Ui.height Ui.fill, Ui.padding 16 ]
            [ Input.multiline
                [ Ui.width Ui.fill, Ui.height Ui.fill, Ui.htmlAttribute "data-probe" "z2-ta" ]
                { onChange = UpdateText
                , text = ""
                , placeholder = Nothing
                , label = Input.labelHidden "txt"
                , spellcheck = False
                }
            ])


main =
    app
        { init = init, update = update, view = view, subscriptions = subscriptions
        , routes = [ route "/" MainPage ], notFound = MainPage
        }
`,
        assertions: async (page) => {
            await page.waitForSelector("textarea", { timeout: 5000 });
            const m = await page.evaluate(() => {
                const ta = document.querySelector("textarea");
                const r = ta.getBoundingClientRect();
                return { h: r.height, w: r.width, vp: window.innerHeight };
            });
            // viewport (800) - 2 * padding (16) - tolerance.
            const min = m.vp - 2 * 16 - 8;
            if (m.h < min) {
                return { ok: false, msg: `textarea height ${m.h.toFixed(0)}px < expected ${min}px (vp ${m.vp}px). Pre-F1 was 51px — F1 not in effect.` };
            }
            return { ok: true, msg: `textarea fills viewport (h=${m.h.toFixed(0)}px, w=${m.w.toFixed(0)}px, vp=${m.vp}px)` };
        },
    },

    {
        id: "Z3",
        name: "Z3-three-pane-app-shell",
        port: 8812,
        descr: "header + sidebar + main app shell — Z3, F1 cross-axis test",
        source: `module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Std.Cmd as Cmd
import Std.Sub as Sub
import Std.Live exposing (app, route)
import Std.Ui as Ui
import Std.Ui.Background as Background


type alias Model = { dummy : Int }
type Msg = Noop
type Page = MainPage


init : a -> ( Model, Cmd Msg )
init _ = ( { dummy = 0 }, Cmd.none )

update : Msg -> Model -> ( Model, Cmd Msg )
update _ m = ( m, Cmd.none )

subscriptions : Model -> Sub Msg
subscriptions _ = Sub.none


view : Model -> any
view _ =
    Ui.layout
        []
        (Ui.column
            [ Ui.width Ui.fill, Ui.height Ui.fill, Ui.htmlAttribute "data-probe" "z3-outer" ]
            [ Ui.el
                [ Ui.width Ui.fill, Ui.height (Ui.px 60), Background.color (Ui.rgb 200 200 200) ]
                (Ui.text "header")
            , Ui.row
                [ Ui.width Ui.fill, Ui.height Ui.fill, Ui.htmlAttribute "data-probe" "z3-row" ]
                [ Ui.el
                    [ Ui.width (Ui.px 200), Ui.height Ui.fill, Background.color (Ui.rgb 240 240 240), Ui.htmlAttribute "data-probe" "z3-sidebar" ]
                    (Ui.text "side")
                , Ui.column
                    [ Ui.width Ui.fill, Ui.height Ui.fill, Background.color (Ui.rgb 250 250 250), Ui.htmlAttribute "data-probe" "z3-main" ]
                    [ Ui.el
                        [ Ui.width Ui.fill, Ui.height Ui.fill, Background.color (Ui.rgb 100 150 200), Ui.htmlAttribute "data-probe" "z3-content" ]
                        (Ui.text "content")
                    ]
                ]
            ])


main =
    app
        { init = init, update = update, view = view, subscriptions = subscriptions
        , routes = [ route "/" MainPage ], notFound = MainPage
        }
`,
        assertions: async (page) => {
            const m = await page.evaluate(() => {
                const out = (sel) => {
                    const el = document.querySelector("[data-probe='" + sel + "']");
                    if (!el) return null;
                    const r = el.getBoundingClientRect();
                    return { h: r.height, w: r.width };
                };
                return {
                    outer: out("z3-outer"),
                    row: out("z3-row"),
                    sidebar: out("z3-sidebar"),
                    main: out("z3-main"),
                    content: out("z3-content"),
                    vp: window.innerHeight,
                };
            });
            // Expected: vp - 60 (header) - margin ≈ 740. Pre-F1 these
            // were 22 px each. Gate: all three should be ≥ 700.
            const min = m.vp - 60 - 40; // vp - header - some chrome
            const checks = [
                { name: "sidebar", h: m.sidebar.h },
                { name: "main",    h: m.main.h    },
                { name: "content", h: m.content.h },
            ];
            for (const c of checks) {
                if (c.h < min) {
                    return { ok: false, msg: `${c.name} h=${c.h.toFixed(0)}px < expected ${min}px. Pre-F1 was 22px — F1 not in effect.` };
                }
            }
            return { ok: true, msg: `all three panes stretch (sidebar=${m.sidebar.h.toFixed(0)}px, main=${m.main.h.toFixed(0)}px, content=${m.content.h.toFixed(0)}px)` };
        },
    },

    {
        id: "M",
        name: "M-in-page-matrix",
        port: 8813,
        descr: "in-page matrix — flex-chain + input + align (definite frame)",
        source: `module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Std.Cmd as Cmd
import Std.Sub as Sub
import Std.Live exposing (app, route)
import Std.Ui as Ui
import Std.Ui.Background as Background
import Std.Ui.Input as Input


type alias Model = { dummy : Int }
type Msg = Noop | UpdateText String
type Page = MainPage


init : a -> ( Model, Cmd Msg )
init _ = ( { dummy = 0 }, Cmd.none )

update : Msg -> Model -> ( Model, Cmd Msg )
update _ m = ( m, Cmd.none )

subscriptions : Model -> Sub Msg
subscriptions _ = Sub.none


-- Each cell wraps its target in a frame [width fill, height (px 200)]
-- giving the inner content a DEFINITE-px parent. The matrix exercises
-- the audit rig's surviving classes:
--   B-flex-chain: row in row, col → row → fill child
--   D-input:      Input.text / multiline / checkbox with fill attrs
--   E-align:      centerX / centerY in row + col parents
--   F1-postfix:   cross-axis fill in a flex-grow-derived parent (the
--                 frame itself is a flex child of the outer column)
frame : String -> Ui.Element Msg -> Ui.Element Msg
frame caseId el_ =
    Ui.column
        [ Ui.width Ui.fill, Ui.height (Ui.px 200)
        , Background.color (Ui.rgb 245 248 255)
        , Ui.htmlAttribute "data-case" caseId
        ]
        [ el_ ]


-- B-row-childfillH-cross
cellB1 : Ui.Element Msg
cellB1 =
    frame "B-row-childfillH-cross"
        (Ui.row
            [ Ui.width Ui.fill, Ui.height Ui.fill ]
            [ Ui.el
                [ Ui.width (Ui.px 50), Ui.height Ui.fill
                , Background.color (Ui.rgb 60 80 200)
                , Ui.htmlAttribute "data-probe" "B-row-childfillH-cross"
                ]
                (Ui.text "x")
            ])


-- B-row-in-row
cellB2 : Ui.Element Msg
cellB2 =
    frame "B-rowInRow"
        (Ui.row
            [ Ui.width Ui.fill, Ui.height Ui.fill ]
            [ Ui.row
                [ Ui.width Ui.fill, Ui.height Ui.fill ]
                [ Ui.el
                    [ Ui.width Ui.fill, Ui.height Ui.fill
                    , Background.color (Ui.rgb 60 200 80)
                    , Ui.htmlAttribute "data-probe" "B-rowInRow"
                    ]
                    (Ui.text "x")
                ]
            ])


-- B-col-row mixed
cellB3 : Ui.Element Msg
cellB3 =
    frame "B-colRow"
        (Ui.column
            [ Ui.width Ui.fill, Ui.height Ui.fill ]
            [ Ui.row
                [ Ui.width Ui.fill, Ui.height Ui.fill ]
                [ Ui.el
                    [ Ui.width Ui.fill, Ui.height Ui.fill
                    , Background.color (Ui.rgb 200 80 60)
                    , Ui.htmlAttribute "data-probe" "B-colRow"
                    ]
                    (Ui.text "x")
                ]
            ])


-- D-input-text-fill (#403)
cellD1 : Ui.Element Msg
cellD1 =
    frame "D-text-fill"
        (Input.text
            [ Ui.width Ui.fill, Ui.htmlAttribute "data-probe" "D-text-fill" ]
            { onChange = UpdateText, text = "", placeholder = Nothing, label = Input.labelHidden "x" })


-- D-input-multiline-fill (#403 + #63 canonical)
cellD2 : Ui.Element Msg
cellD2 =
    frame "D-multiline-fill"
        (Input.multiline
            [ Ui.width Ui.fill, Ui.height Ui.fill, Ui.htmlAttribute "data-probe" "D-multiline-fill" ]
            { onChange = UpdateText, text = "", placeholder = Nothing, label = Input.labelHidden "x", spellcheck = False })


-- E-row-centerX
cellE1 : Ui.Element Msg
cellE1 =
    frame "E-row-centerX"
        (Ui.row
            [ Ui.width Ui.fill, Ui.height Ui.fill, Ui.centerX ]
            [ Ui.el
                [ Ui.width (Ui.px 80), Ui.height (Ui.px 30)
                , Background.color (Ui.rgb 80 60 200)
                , Ui.htmlAttribute "data-probe" "E-row-centerX"
                ]
                (Ui.text "x")
            ])


-- E-col-centerY
cellE2 : Ui.Element Msg
cellE2 =
    frame "E-col-centerY"
        (Ui.column
            [ Ui.width Ui.fill, Ui.height Ui.fill, Ui.centerY ]
            [ Ui.el
                [ Ui.width (Ui.px 80), Ui.height (Ui.px 30)
                , Background.color (Ui.rgb 200 80 60)
                , Ui.htmlAttribute "data-probe" "E-col-centerY"
                ]
                (Ui.text "x")
            ])


view : Model -> any
view _ =
    Ui.layout
        []
        (Ui.column
            [ Ui.width Ui.fill, Ui.spacing 12, Ui.padding 8 ]
            [ cellB1, cellB2, cellB3, cellD1, cellD2, cellE1, cellE2 ])


main =
    app
        { init = init, update = update, view = view, subscriptions = subscriptions
        , routes = [ route "/" MainPage ], notFound = MainPage
        }
`,
        // Per-cell expectations against the 200 px frame.
        assertions: async (page) => {
            const m = await page.evaluate(() => {
                const get = (sel) => {
                    const el = document.querySelector("[data-probe='" + sel + "']");
                    if (!el) return null;
                    const r = el.getBoundingClientRect();
                    const cs = window.getComputedStyle(el);
                    return {
                        h: r.height, w: r.width,
                        flexGrow: cs.flexGrow, alignSelf: cs.alignSelf,
                    };
                };
                const getCase = (caseId) => {
                    const el = document.querySelector("[data-case='" + caseId + "']");
                    if (!el) return null;
                    const r = el.getBoundingClientRect();
                    return { h: r.height, w: r.width };
                };
                return {
                    B1: { probe: get("B-row-childfillH-cross"), frame: getCase("B-row-childfillH-cross") },
                    B2: { probe: get("B-rowInRow"),             frame: getCase("B-rowInRow")             },
                    B3: { probe: get("B-colRow"),               frame: getCase("B-colRow")               },
                    D1: { probe: get("D-text-fill"),            frame: getCase("D-text-fill")            },
                    D2: { probe: get("D-multiline-fill"),       frame: getCase("D-multiline-fill")       },
                    E1: { probe: get("E-row-centerX"),          frame: getCase("E-row-centerX")          },
                    E2: { probe: get("E-col-centerY"),          frame: getCase("E-col-centerY")          },
                };
            });
            const failures = [];
            const FRAME_H = 200, TOL = 6;
            // B1: row-cross, child height fill → child fills frame.
            if (m.B1.probe.h < FRAME_H - TOL) {
                failures.push(`B1 (row → child h fill cross): probe h=${m.B1.probe.h} < ${FRAME_H - TOL}`);
            }
            // B2: row in row, inner child fills both axes.
            if (m.B2.probe.h < FRAME_H - TOL) {
                failures.push(`B2 (row in row, inner h fill): probe h=${m.B2.probe.h} < ${FRAME_H - TOL}`);
            }
            // B3: col → row → child fill main+cross.
            if (m.B3.probe.h < FRAME_H - TOL) {
                failures.push(`B3 (col → row → child fill): probe h=${m.B3.probe.h} < ${FRAME_H - TOL}`);
            }
            // D1: Input.text wrapper width-fill → wrapper width ≥ frame width - chrome.
            if (m.D1.probe.w < 200) {
                failures.push(`D1 (Input.text wrapper width fill): probe w=${m.D1.probe.w} < 200`);
            }
            // D2: Input.multiline both fill → textarea fills frame.
            //   The textarea itself isn't [data-probe], it's the wrapper.
            //   The wrapper holding the textarea should be ≥ FRAME_H - chrome.
            if (m.D2.probe.h < FRAME_H - TOL) {
                failures.push(`D2 (Input.multiline w/h fill, F1+F2 canonical): probe h=${m.D2.probe.h} < ${FRAME_H - TOL}`);
            }
            // E1: row centerX → child h=30, frame h=200, child shouldn't fill.
            if (Math.abs(m.E1.probe.h - 30) > TOL) {
                failures.push(`E1 (row centerX): probe h=${m.E1.probe.h} ≠ 30 (centerX shouldn't stretch height)`);
            }
            // E2: col centerY → child shouldn't fill height.
            if (Math.abs(m.E2.probe.h - 30) > TOL) {
                failures.push(`E2 (col centerY): probe h=${m.E2.probe.h} ≠ 30 (centerY shouldn't stretch height)`);
            }
            if (failures.length) {
                return { ok: false, msg: failures.join(" | ") };
            }
            return { ok: true, msg: `7/7 cells pass — B1=${m.B1.probe.h.toFixed(0)}, B2=${m.B2.probe.h.toFixed(0)}, B3=${m.B3.probe.h.toFixed(0)}, D1=${m.D1.probe.w.toFixed(0)}w, D2=${m.D2.probe.h.toFixed(0)}, E1=${m.E1.probe.h.toFixed(0)}, E2=${m.E2.probe.h.toFixed(0)}` };
        },
    },
];

// ─── Driver ──────────────────────────────────────────────────────

const scratch = mkdtempSync(`${tmpdir()}/sky-stdui-matrix-`);
console.log(`scratch dir: ${scratch}`);

async function buildFixture(fx) {
    const dir = resolve(scratch, fx.name);
    mkdirSync(`${dir}/src`, { recursive: true });
    writeFileSync(`${dir}/src/Main.sky`, fx.source);
    writeFileSync(`${dir}/sky.toml`, `name = "${fx.name}"\nversion = "0.0.0"\n`);
    const r = spawnSync(SKY_BIN, ["build", "src/Main.sky"], {
        cwd: dir, stdio: "pipe", env: process.env, timeout: 180_000,
    });
    if (r.status !== 0) {
        const out = (r.stdout?.toString() ?? "") + (r.stderr?.toString() ?? "");
        throw new Error(`build failed for ${fx.name}:\n${out}`);
    }
    return resolve(dir, "sky-out", "app");
}

async function waitForReady(port, timeoutMs) {
    const deadline = Date.now() + timeoutMs;
    while (Date.now() < deadline) {
        try {
            const r = await fetch(`http://localhost:${port}/`);
            if (r.ok) return true;
        } catch (_) { /* not ready yet */ }
        await new Promise((r) => setTimeout(r, 100));
    }
    return false;
}

let totalPass = 0, totalFail = 0;
const failures = [];

try {
    const browser = await chromium.launch();
    try {
        for (const fx of FIXTURES) {
            console.log(`\n=== ${fx.id} ${fx.name} ===`);
            console.log(`    ${fx.descr}`);
            let bin;
            try {
                bin = await buildFixture(fx);
            } catch (e) {
                console.error(`FAIL — ${e.message.split("\n").slice(0, 5).join("\n")}`);
                totalFail++;
                failures.push(`${fx.id}: build failed`);
                continue;
            }
            const child = spawn(bin, [], {
                cwd: dirname(dirname(bin)),
                stdio: ["ignore", "pipe", "pipe"],
                env: { ...process.env, SKY_LIVE_PORT: String(fx.port), SKY_DEV_BANNER: "off" },
            });
            let serverLog = "";
            child.stdout.on("data", (d) => { serverLog += d.toString(); });
            child.stderr.on("data", (d) => { serverLog += d.toString(); });
            try {
                if (!(await waitForReady(fx.port, 8000))) {
                    throw new Error(`server not ready on :${fx.port}:\n${serverLog.slice(0, 400)}`);
                }
                const context = await browser.newContext({ viewport: VIEWPORT });
                const page = await context.newPage();
                let pageErr = null;
                page.on("pageerror", (e) => { pageErr = e.message; });
                await page.goto(`http://localhost:${fx.port}/`, { waitUntil: "domcontentloaded", timeout: 10_000 });
                await page.waitForTimeout(200);
                if (pageErr) throw new Error(`page error: ${pageErr}`);

                const result = await fx.assertions(page);
                if (result.ok) {
                    console.log(`PASS — ${result.msg}`);
                    totalPass++;
                } else {
                    console.error(`FAIL — ${result.msg}`);
                    totalFail++;
                    failures.push(`${fx.id}: ${result.msg}`);
                }
                await context.close();
            } catch (e) {
                console.error(`FAIL — ${e.message}`);
                totalFail++;
                failures.push(`${fx.id}: ${e.message}`);
            } finally {
                child.kill("SIGTERM");
                await new Promise((r) => setTimeout(r, 150));
                if (!child.killed) child.kill("SIGKILL");
            }
        }
    } finally {
        await browser.close();
    }
} finally {
    if (!KEEP_DIR) {
        try { rmSync(scratch, { recursive: true, force: true }); }
        catch (_) { /* best-effort */ }
    } else {
        console.log(`\n(keeping scratch dir: ${scratch})`);
    }
}

console.log(`\n=== summary ===`);
console.log(`PASS: ${totalPass} / ${FIXTURES.length}`);
console.log(`FAIL: ${totalFail} / ${FIXTURES.length}`);
if (failures.length) {
    console.log("\nfailures:");
    for (const f of failures) console.log(`  - ${f}`);
    process.exit(1);
}
process.exit(0);
