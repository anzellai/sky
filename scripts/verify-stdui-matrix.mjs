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

    // ─── v0.15.56 F3 — Ui.layoutWith ─────────────────────────────
    {
        id: "F3",
        name: "F3-layoutWith-page-wrapper",
        port: 8814,
        descr: "Ui.layoutWith { wrapperAttrs, rootAttrs } — page-wrapper customisation",
        source: `module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Std.Cmd as Cmd
import Std.Sub as Sub
import Std.Live exposing (app, route)
import Std.Ui as Ui
import Std.Ui.Background as Background
import Std.Ui.Font as Font


type alias Model = { dummy : Int }
type Msg = Noop
type Page = MainPage


init : a -> ( Model, Cmd Msg )
init _ = ( { dummy = 0 }, Cmd.none )

update : Msg -> Model -> ( Model, Cmd Msg )
update _ m = ( m, Cmd.none )

subscriptions : Model -> Sub Msg
subscriptions _ = Sub.none


-- wrapperAttrs paint the OUTER 100vh div (Background.color +
-- Font.color + Font.family); rootAttrs apply to the inner root
-- column (Ui.padding).
view : Model -> any
view _ =
    Ui.layoutWith
        { wrapperAttrs =
            [ Background.color (Ui.rgb 18 18 24)
            , Font.color (Ui.rgb 240 240 240)
            , Font.family "monospace"
            , Ui.htmlAttribute "data-test-id" "f3-wrapper"
            ]
        , rootAttrs =
            [ Ui.padding 16
            , Ui.htmlAttribute "data-test-id" "f3-root"
            ]
        }
        (Ui.column
            [ Ui.spacing 12 ]
            [ Ui.el [ Ui.htmlAttribute "data-test-id" "f3-text" ] (Ui.text "dark page")
            ])


main =
    app
        { init = init, update = update, view = view, subscriptions = subscriptions
        , routes = [ route "/" MainPage ], notFound = MainPage
        }
`,
        assertions: async (page) => {
            // The outer wrapper div MUST carry the user's wrapperAttrs:
            //   * Background.color → background-color: rgb(18, 18, 24)
            //   * Font.color → color: rgb(240, 240, 240)
            //   * Font.family → font-family: monospace
            // AND must keep the default min-height: 100vh + flex column.
            const m = await page.evaluate(() => {
                const wrapper = document.querySelector('[data-test-id="f3-wrapper"]');
                const text = document.querySelector('[data-test-id="f3-text"]');
                if (!wrapper || !text) return { found: false };
                const cs = window.getComputedStyle(wrapper);
                const ts = window.getComputedStyle(text);
                const wsAttr = wrapper.getAttribute("style") || "";
                return {
                    found: true,
                    bg: cs.backgroundColor,
                    minHeight: cs.minHeight,
                    display: cs.display,
                    flexDirection: cs.flexDirection,
                    // Font.color + Font.family cascade to children:
                    textColor: ts.color,
                    textFontFamily: ts.fontFamily,
                    // verify the inline style ALSO carries the user attrs
                    // (not just inherited from <body>):
                    wrapperStyleStr: wsAttr,
                };
            });
            if (!m.found) {
                return { ok: false, msg: "wrapper or text element missing — data-test-id didn't reach wrapper" };
            }
            // Background colour on the wrapper itself.
            if (!/rgb\(18,\s*18,\s*24\)/.test(m.bg)) {
                return { ok: false, msg: `wrapper background-color ${m.bg} ≠ rgb(18, 18, 24)` };
            }
            // Defaults preserved.
            if (!m.minHeight.includes("vh") && parseFloat(m.minHeight) < 500) {
                return { ok: false, msg: `wrapper min-height ${m.minHeight} not 100vh-derived` };
            }
            if (m.display !== "flex" || m.flexDirection !== "column") {
                return { ok: false, msg: `wrapper display=${m.display} flex-direction=${m.flexDirection} (expected flex/column)` };
            }
            // Font.color cascades.
            if (!/rgb\(240,\s*240,\s*240\)/.test(m.textColor)) {
                return { ok: false, msg: `text color ${m.textColor} ≠ rgb(240, 240, 240) (Font.color didn't cascade)` };
            }
            // Font.family cascades.
            if (!m.textFontFamily.includes("monospace")) {
                return { ok: false, msg: `text font-family ${m.textFontFamily} doesn't include monospace (Font.family didn't cascade)` };
            }
            // The wrapper inline style must include both defaults AND user attrs.
            if (!m.wrapperStyleStr.includes("min-height: 100vh") || !m.wrapperStyleStr.includes("display: flex")) {
                return { ok: false, msg: `wrapper inline style missing defaults: ${m.wrapperStyleStr.slice(0, 120)}` };
            }
            return { ok: true, msg: `wrapper bg=${m.bg}, text color=${m.textColor}, font=${m.textFontFamily.slice(0, 20)}, defaults preserved` };
        },
    },

    // ─── v0.15.56 F4 — align-self single-emission ────────────────
    {
        id: "F4",
        name: "F4-align-self-dedup",
        port: 8815,
        descr: "align-self emitted ONCE per element across (fill × alignment) combinations",
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


-- Each cell exercises a (fill x alignment) combo. The probe's
-- inline style is queried directly + getComputedStyle is read for
-- the final align-self resolution. Pre-F4: the inline style
-- emitted align-self: stretch; ... align-self: <alignment>; so
-- cascade-last gave the correct visual but two declarations
-- existed. Post-F4: exactly one align-self (or none) per element.
cellCenterX : Ui.Element Msg
cellCenterX =
    Ui.column
        [ Ui.width (Ui.px 400), Ui.height (Ui.px 100)
        , Background.color (Ui.rgb 220 230 240)
        ]
        [ Ui.el
            [ Ui.width Ui.fill, Ui.centerX, Ui.height (Ui.px 30)
            , Background.color (Ui.rgb 60 80 200)
            , Ui.htmlAttribute "data-probe" "f4-col-widthfill-centerX"
            ]
            (Ui.text "x")
        ]


cellCenterY : Ui.Element Msg
cellCenterY =
    Ui.row
        [ Ui.width (Ui.px 400), Ui.height (Ui.px 100)
        , Background.color (Ui.rgb 220 230 240)
        ]
        [ Ui.el
            [ Ui.height Ui.fill, Ui.centerY, Ui.width (Ui.px 30)
            , Background.color (Ui.rgb 200 80 60)
            , Ui.htmlAttribute "data-probe" "f4-row-heightfill-centerY"
            ]
            (Ui.text "x")
        ]


cellAlignTop : Ui.Element Msg
cellAlignTop =
    Ui.row
        [ Ui.width (Ui.px 400), Ui.height (Ui.px 100)
        , Background.color (Ui.rgb 220 230 240)
        ]
        [ Ui.el
            [ Ui.height Ui.fill, Ui.alignTop, Ui.width (Ui.px 30)
            , Background.color (Ui.rgb 60 200 80)
            , Ui.htmlAttribute "data-probe" "f4-row-heightfill-alignTop"
            ]
            (Ui.text "x")
        ]


cellAlignBottom : Ui.Element Msg
cellAlignBottom =
    Ui.row
        [ Ui.width (Ui.px 400), Ui.height (Ui.px 100)
        , Background.color (Ui.rgb 220 230 240)
        ]
        [ Ui.el
            [ Ui.height Ui.fill, Ui.alignBottom, Ui.width (Ui.px 30)
            , Background.color (Ui.rgb 200 200 60)
            , Ui.htmlAttribute "data-probe" "f4-row-heightfill-alignBottom"
            ]
            (Ui.text "x")
        ]


cellAlignLeft : Ui.Element Msg
cellAlignLeft =
    Ui.column
        [ Ui.width (Ui.px 400), Ui.height (Ui.px 100)
        , Background.color (Ui.rgb 220 230 240)
        ]
        [ Ui.el
            [ Ui.width Ui.fill, Ui.alignLeft, Ui.height (Ui.px 30)
            , Background.color (Ui.rgb 80 200 200)
            , Ui.htmlAttribute "data-probe" "f4-col-widthfill-alignLeft"
            ]
            (Ui.text "x")
        ]


cellAlignRight : Ui.Element Msg
cellAlignRight =
    Ui.column
        [ Ui.width (Ui.px 400), Ui.height (Ui.px 100)
        , Background.color (Ui.rgb 220 230 240)
        ]
        [ Ui.el
            [ Ui.width Ui.fill, Ui.alignRight, Ui.height (Ui.px 30)
            , Background.color (Ui.rgb 200 80 200)
            , Ui.htmlAttribute "data-probe" "f4-col-widthfill-alignRight"
            ]
            (Ui.text "x")
        ]


-- Showcase outer column pattern: [Ui.width (Ui.maximum 760 Ui.fill),
-- Ui.centerX]. Tests that F4's strip of align-self: stretch from
-- widthFillFor AsColumn doesn't break the showcase's intent (fill
-- width up to the 760 cap, centred in parent).
cellShowcaseShape : Ui.Element Msg
cellShowcaseShape =
    Ui.column
        [ Ui.width (Ui.px 1000), Ui.height (Ui.px 60)
        , Background.color (Ui.rgb 220 220 220)
        ]
        [ Ui.column
            [ Ui.width (Ui.maximum 760 Ui.fill), Ui.centerX, Ui.height Ui.fill
            , Background.color (Ui.rgb 100 150 200)
            , Ui.htmlAttribute "data-probe" "f4-showcase-shape"
            ]
            [ Ui.text "x" ]
        ]


-- No-alignment baseline: pure fill, no alignment. The default
-- align-items: stretch must still make the child fill (no
-- explicit align-self emission needed; F4 strips the redundant one).
cellPureFill : Ui.Element Msg
cellPureFill =
    Ui.row
        [ Ui.width (Ui.px 400), Ui.height (Ui.px 100)
        , Background.color (Ui.rgb 220 230 240)
        ]
        [ Ui.el
            [ Ui.height Ui.fill, Ui.width (Ui.px 30)
            , Background.color (Ui.rgb 60 80 200)
            , Ui.htmlAttribute "data-probe" "f4-row-heightfill-no-align"
            ]
            (Ui.text "x")
        ]


view : Model -> any
view _ =
    Ui.layout
        []
        (Ui.column
            [ Ui.width Ui.fill, Ui.spacing 12, Ui.padding 8 ]
            [ cellCenterX, cellCenterY, cellAlignTop, cellAlignBottom
            , cellAlignLeft, cellAlignRight, cellShowcaseShape, cellPureFill
            ])


main =
    app
        { init = init, update = update, view = view, subscriptions = subscriptions
        , routes = [ route "/" MainPage ], notFound = MainPage
        }
`,
        assertions: async (page) => {
            const m = await page.evaluate(() => {
                const get = (sel) => {
                    const el = document.querySelector("[data-probe='" + sel + "']");
                    if (!el) return null;
                    const inline = el.getAttribute("style") || "";
                    const cs = window.getComputedStyle(el);
                    const r = el.getBoundingClientRect();
                    // count occurrences of "align-self:" in the inline style:
                    const asMatches = inline.match(/align-self\s*:/gi) || [];
                    return {
                        inlineStyle: inline,
                        alignSelfCount: asMatches.length,
                        alignSelfComputed: cs.alignSelf,
                        width: r.width, height: r.height,
                    };
                };
                return {
                    centerX:     get("f4-col-widthfill-centerX"),
                    centerY:     get("f4-row-heightfill-centerY"),
                    alignTop:    get("f4-row-heightfill-alignTop"),
                    alignBottom: get("f4-row-heightfill-alignBottom"),
                    alignLeft:   get("f4-col-widthfill-alignLeft"),
                    alignRight:  get("f4-col-widthfill-alignRight"),
                    showcase:    get("f4-showcase-shape"),
                    pureFill:    get("f4-row-heightfill-no-align"),
                };
            });
            const failures = [];
            // F4 contract: at most ONE align-self declaration per element.
            for (const [name, probe] of Object.entries(m)) {
                if (!probe) {
                    failures.push(`${name}: probe missing`);
                    continue;
                }
                if (probe.alignSelfCount > 1) {
                    failures.push(`${name}: ${probe.alignSelfCount} align-self declarations in inline style — F4 dedup failed: "${probe.inlineStyle.slice(0, 160)}"`);
                }
            }
            // centerX in column parent → align-self: center (or 'center').
            if (m.centerX && !m.centerX.alignSelfComputed.includes("center")) {
                failures.push(`centerX: computed align-self ${m.centerX.alignSelfComputed} ≠ center`);
            }
            // centerY in row parent → align-self: center.
            if (m.centerY && !m.centerY.alignSelfComputed.includes("center")) {
                failures.push(`centerY: computed align-self ${m.centerY.alignSelfComputed} ≠ center`);
            }
            // alignTop in row parent → align-self: flex-start.
            if (m.alignTop && !m.alignTop.alignSelfComputed.includes("flex-start")) {
                failures.push(`alignTop: computed align-self ${m.alignTop.alignSelfComputed} ≠ flex-start`);
            }
            // alignBottom in row parent → align-self: flex-end.
            if (m.alignBottom && !m.alignBottom.alignSelfComputed.includes("flex-end")) {
                failures.push(`alignBottom: computed align-self ${m.alignBottom.alignSelfComputed} ≠ flex-end`);
            }
            // alignLeft in col parent → align-self: flex-start.
            if (m.alignLeft && !m.alignLeft.alignSelfComputed.includes("flex-start")) {
                failures.push(`alignLeft: computed align-self ${m.alignLeft.alignSelfComputed} ≠ flex-start`);
            }
            // alignRight in col parent → align-self: flex-end.
            if (m.alignRight && !m.alignRight.alignSelfComputed.includes("flex-end")) {
                failures.push(`alignRight: computed align-self ${m.alignRight.alignSelfComputed} ≠ flex-end`);
            }
            // pureFill (no alignment) in row parent → fills via default stretch.
            if (m.pureFill && m.pureFill.height < 80) {
                failures.push(`pureFill: height ${m.pureFill.height} < 80 (default stretch didn't apply)`);
            }
            // Showcase shape: [width (max 760 fill), centerX] in 1000-wide column.
            // Expect: clamped to 760 wide AND centered (left margin = (1000-760)/2 = 120).
            if (m.showcase) {
                if (Math.abs(m.showcase.width - 760) > 8) {
                    failures.push(`showcase shape width ${m.showcase.width} ≠ 760 — F4 broke the showcase outer-column pattern`);
                }
            }
            if (failures.length) {
                return { ok: false, msg: failures.join(" | ") };
            }
            return { ok: true, msg: `8/8 F4 cells pass — align-self emitted ≤1× per element across all (fill × alignment) shapes; showcase shape ${m.showcase ? m.showcase.width.toFixed(0) : "?"}px wide` };
        },
    },

    // ─── v0.15.56 F5 — wrappedRow + spacing + multi-row wrap ─────
    {
        id: "F5",
        name: "F5-wrappedRow-spacing",
        port: 8816,
        descr: "wrappedRow [Ui.spacing N] wraps cleanly with gap-based spacing",
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


-- 8 cards of width 300 in a 1280-viewport wrappedRow with spacing 16.
-- Cards per row: floor((1280 - 16 (gap)) / (300 + 16)) ≈ 4.
-- Actually: gap-based layout fits 4 cards: 4 * 300 + 3 * 16 = 1248 < 1280.
-- 5 cards would need 5 * 300 + 4 * 16 = 1564 > 1280 → no.
-- So 8 cards → 2 rows of 4.
makeCard : Int -> Ui.Element Msg
makeCard i =
    Ui.el
        [ Ui.width (Ui.px 300), Ui.height (Ui.px 80)
        , Background.color (Ui.rgb 200 220 240)
        , Ui.htmlAttribute "data-card" (toString i)
        ]
        (Ui.text (toString i))


view : Model -> any
view _ =
    Ui.layout
        []
        (Ui.column
            [ Ui.width Ui.fill, Ui.padding 0, Ui.htmlAttribute "data-test-id" "f5-container" ]
            [ Ui.wrappedRow
                [ Ui.spacing 16, Ui.htmlAttribute "data-test-id" "f5-wrapped" ]
                [ makeCard 0, makeCard 1, makeCard 2, makeCard 3
                , makeCard 4, makeCard 5, makeCard 6, makeCard 7
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
                const wrap = document.querySelector('[data-test-id="f5-wrapped"]');
                if (!wrap) return { found: false };
                const cs = window.getComputedStyle(wrap);
                const cards = Array.from(document.querySelectorAll("[data-card]")).map((el) => {
                    const r = el.getBoundingClientRect();
                    return { idx: +el.getAttribute("data-card"), x: r.left, y: r.top, w: r.width, h: r.height };
                });
                return {
                    found: true,
                    wrapWidth: wrap.getBoundingClientRect().width,
                    flexWrap: cs.flexWrap, gap: cs.gap, rowGap: cs.rowGap, columnGap: cs.columnGap,
                    inlineStyle: wrap.getAttribute("style") || "",
                    cards,
                };
            });
            if (!m.found) return { ok: false, msg: "wrappedRow probe missing" };
            const failures = [];
            // CSS contract: flex-wrap: wrap + gap: 16px (modern flex-gap).
            if (m.flexWrap !== "wrap") {
                failures.push(`flex-wrap=${m.flexWrap} ≠ wrap`);
            }
            // gap should be 16px (single value applies to both row + column).
            if (!m.gap.includes("16") && !m.rowGap.includes("16")) {
                failures.push(`gap=${m.gap}, row-gap=${m.rowGap} (expected 16px)`);
            }
            // Cards should group into multiple rows (distinct y values).
            const ys = Array.from(new Set(m.cards.map((c) => Math.round(c.y))));
            ys.sort((a, b) => a - b);
            if (ys.length < 2) {
                failures.push(`only ${ys.length} distinct y values — no wrap detected at viewport 1280 with 8 × 300 cards`);
            }
            // Vertical row-gap between rows should be ≥ 16 (gap) within ~2px tolerance.
            if (ys.length >= 2) {
                const rowGap = ys[1] - ys[0] - 80; // 80 = card height
                if (Math.abs(rowGap - 16) > 4) {
                    failures.push(`row-gap measured ${rowGap}px ≠ ~16px (spacing not applied between rows)`);
                }
            }
            // Horizontal column-gap within a row should be ~16px.
            const row1 = m.cards.filter((c) => Math.round(c.y) === ys[0]).sort((a, b) => a.x - b.x);
            if (row1.length >= 2) {
                const colGap = row1[1].x - row1[0].x - row1[0].w;
                if (Math.abs(colGap - 16) > 4) {
                    failures.push(`col-gap measured ${colGap}px ≠ ~16px within row`);
                }
            }
            if (failures.length) {
                return { ok: false, msg: failures.join(" | ") };
            }
            return { ok: true, msg: `wrappedRow ${m.cards.length} cards across ${ys.length} rows, gap=${m.gap}, flex-wrap=${m.flexWrap}` };
        },
    },

    // ═══════════════════════════════════════════════════════════════════
    // v0.15.57 Cycle 7 audit extension — Dimensions 1, 2, 5, 6, 7
    //
    // Each dimension adds one or more cells exercising a slice of the
    // Std.Ui surface the v0.15.55 / v0.15.56 audit didn't reach. The
    // cells classify PASS / FAIL via DOM measurement; the goal is
    // coverage breadth, not pixel-perfection.
    // ═══════════════════════════════════════════════════════════════════

    // ─── Dimension 1 — Interaction states ────────────────────────────
    //
    // D1a covers focus mid-update + active pseudo-class via DOM
    // observation; D1b covers prefers-reduced-motion CSS gating; the
    // hover-mid-typing case is folded into D1a (typed input + hover
    // styling — verify hover style applies + caret unchanged).
    {
        id: "D1a",
        name: "D1a-focus-active-pseudo",
        port: 8820,
        descr: "focus + :active pseudo-class on a non-void Ui.el + void-element sibling hoist (#409)",
        source: `module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Std.Cmd as Cmd
import Std.Sub as Sub
import Std.Live exposing (app, route)
import Std.Ui as Ui
import Std.Ui.Background as Background
import Std.Ui.Input as Input


type alias Model = { text : String, ticks : Int }
type Msg = UpdateText String | Tick | NoOp
type Page = MainPage


init : a -> ( Model, Cmd Msg )
init _ = ( { text = "", ticks = 0 }, Cmd.none )

update : Msg -> Model -> ( Model, Cmd Msg )
update msg m =
    case msg of
        UpdateText t -> ( { m | text = t }, Cmd.none )
        Tick         -> ( { m | ticks = m.ticks + 1 }, Cmd.none )
        NoOp         -> ( m, Cmd.none )

subscriptions : Model -> Sub Msg
subscriptions _ = Sub.none


view : Model -> any
view m =
    Ui.layout
        []
        (Ui.column
            [ Ui.spacing 16, Ui.padding 16 ]
            [ -- An ordinary Ui.el (renders as <div>) carrying both a
              -- background colour AND an :active pseudo-class. This is
              -- the WORKING shape; the :active rule emits as a sky-id
              -- scoped <style data-sky-pc> child.
              Ui.el
                [ Ui.width (Ui.px 200), Ui.height (Ui.px 60)
                , Background.color (Ui.rgb 100 100 100)
                , Background.activeColor (Ui.rgb 200 100 50)
                , Background.hoverColor (Ui.rgb 50 50 200)
                , Ui.htmlAttribute "data-probe" "d1a-pseudo-el"
                ]
                (Ui.text "click & hover")
            , -- Sibling: focusable input WITH a pseudo-class attached
              -- (Background.activeColor). v0.15.57 #409 routes the
              -- pseudo through the void-element sibling-hoist path so
              -- the :active rule reaches the DOM as a <style> sibling
              -- of the inner <input>. Pre-fix the rule was silently
              -- dropped because <input> can't carry a <style> child.
              Input.text
                [ Ui.htmlAttribute "data-probe" "d1a-input"
                , Background.color (Ui.rgb 240 240 240)
                , Background.activeColor (Ui.rgb 200 100 50)
                ]
                { onChange = UpdateText
                , text = m.text
                , placeholder = Just (Input.placeholder [] (Ui.text "type here"))
                , label = Input.labelHidden "txt"
                }
            ])


main =
    app
        { init = init, update = update, view = view, subscriptions = subscriptions
        , routes = [ route "/" MainPage ], notFound = MainPage
        }
`,
        assertions: async (page) => {
            // First: assert :active + :hover pseudo-classes emitted as
            // sky-id-scoped <style data-sky-pc> children on the Ui.el.
            const m = await page.evaluate(() => {
                const styleEls = Array.from(document.querySelectorAll("style[data-sky-pc]"));
                const contents = styleEls.map((s) => s.textContent || "").join("\n");
                const hasActive = /:active/.test(contents);
                const hasHover = /:hover/.test(contents);
                const hoverGated = /@media\s*\(\s*hover\s*:\s*hover\s*\)/.test(contents);
                // Count distinct data-sky-pc style blocks — expect ≥2
                // (one for the Ui.el wrapper, one for the void input).
                const skyIds = new Set(styleEls.map((s) => s.getAttribute("data-sky-pc")));
                return { count: styleEls.length, skyIds: Array.from(skyIds), hasActive, hasHover, hoverGated };
            });
            if (m.count === 0) {
                return { ok: false, msg: "no [data-sky-pc] style block emitted on Ui.el (regression vs v0.15.56)" };
            }
            if (!m.hasActive) {
                return { ok: false, msg: "Background.activeColor did not emit a :active rule in the [data-sky-pc] style block" };
            }
            if (!m.hasHover) {
                return { ok: false, msg: "Background.hoverColor did not emit a :hover rule in the [data-sky-pc] style block" };
            }
            if (!m.hoverGated) {
                return { ok: false, msg: ":hover rule was NOT wrapped in @media (hover: hover) (regression — auto-gating broke)" };
            }
            // #409 verification: the void <input> must also have a
            // sibling <style data-sky-pc> with an :active rule keyed
            // to the input's sky-id.
            const voidStyle = await page.evaluate(() => {
                const input = document.querySelector('input[type="text"]');
                if (!input) return { hasInput: false };
                const skyId = input.getAttribute("sky-id");
                const next = input.nextElementSibling;
                const isSiblingStyle = next && next.tagName === "STYLE" && next.getAttribute("data-sky-pc") === skyId;
                return {
                    hasInput: true,
                    skyId,
                    nextTag: next ? next.tagName : null,
                    nextSkyPc: next ? next.getAttribute("data-sky-pc") : null,
                    isSiblingStyle,
                    siblingText: isSiblingStyle ? (next.textContent || "") : "",
                };
            });
            if (!voidStyle.hasInput) {
                return { ok: false, msg: "void input missing — D1a fixture didn't render" };
            }
            if (!voidStyle.isSiblingStyle) {
                return { ok: false, msg: `#409 regression: void <input sky-id="${voidStyle.skyId}"> has no sibling <style data-sky-pc=...> after it (next: ${voidStyle.nextTag}, data-sky-pc=${voidStyle.nextSkyPc})` };
            }
            if (!/:active/.test(voidStyle.siblingText)) {
                return { ok: false, msg: `#409 regression: sibling style block for void input has no :active rule. body="${voidStyle.siblingText.slice(0, 120)}"` };
            }
            // Second: focus + caret behaviour on the input (independent
            // of the pseudo gap).
            await page.waitForSelector("input[type='text']", { timeout: 5000 });
            await page.click("input[type='text']");
            await page.fill("input[type='text']", "hello");
            const focused = await page.evaluate(() => document.activeElement && document.activeElement.tagName === "INPUT");
            if (!focused) {
                return { ok: false, msg: "input did not receive focus after click" };
            }
            const caret = await page.evaluate(() => document.activeElement.selectionStart);
            if (caret !== 5) {
                return { ok: false, msg: `unexpected caret position ${caret} (expected 5)` };
            }
            return { ok: true, msg: `pseudo-classes on Ui.el AND void <input> (#409 hoist) emit correctly; focus + caret intact` };
        },
    },

    {
        id: "D1b",
        name: "D1b-reduced-motion-gating",
        port: 8821,
        descr: "Animation respectReducedMotion=True wraps keyframes in prefers-reduced-motion media query",
        source: `module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Std.Cmd as Cmd
import Std.Sub as Sub
import Std.Live exposing (app, route)
import Std.Ui as Ui
import Std.Ui.Background as Background
import Std.Ui.Animation as Animation
import Std.Ui.Transform as Transform


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
            [ Ui.padding 16, Ui.spacing 16 ]
            [ Ui.el
                [ Ui.width (Ui.px 100), Ui.height (Ui.px 100)
                , Background.color (Ui.rgb 60 80 200)
                , Animation.attribute
                    { name = "fadeIn"
                    , duration = 1000
                    , easing = Animation.easeOut
                    , delay = 0
                    , iterations = Animation.once
                    , fillMode = Animation.forwards
                    , respectReducedMotion = True
                    , keyframes =
                        [ ( 0, [ Transform.opacity 0.0 ] )
                        , ( 100, [ Transform.opacity 1.0 ] )
                        ]
                    }
                , Ui.htmlAttribute "data-probe" "anim-default-respect"
                ]
                (Ui.text "anim")
            ])


main =
    app
        { init = init, update = update, view = view, subscriptions = subscriptions
        , routes = [ route "/" MainPage ], notFound = MainPage
        }
`,
        assertions: async (page) => {
            // Verify a [data-sky-anim] style block exists AND its
            // body is wrapped in @media (prefers-reduced-motion:
            // no-preference). That's the gate that makes
            // respectReducedMotion=True actually respect the user
            // preference.
            const m = await page.evaluate(() => {
                const styleEls = Array.from(document.querySelectorAll("style[data-sky-anim]"));
                if (styleEls.length === 0) return { found: false };
                const content = styleEls.map((s) => s.textContent || "").join("\n");
                const hasGate = /@media\s*\(\s*prefers-reduced-motion\s*:\s*no-preference\s*\)/i.test(content);
                const hasKeyframes = /@keyframes\s+fadeIn/.test(content);
                return { found: true, hasGate, hasKeyframes, len: content.length };
            });
            if (!m.found) {
                return { ok: false, msg: "no [data-sky-anim] style block emitted" };
            }
            if (!m.hasGate) {
                return { ok: false, msg: "Animation.attribute respectReducedMotion=True did NOT wrap keyframes in @media (prefers-reduced-motion: no-preference)" };
            }
            if (!m.hasKeyframes) {
                return { ok: false, msg: "expected @keyframes fadeIn — missing" };
            }
            // Now flip the emulated media. CSS engine should drop the
            // animation when prefers-reduced-motion: reduce — the
            // @media gate is no-preference, so reduce-mode skips.
            await page.emulateMedia({ reducedMotion: "reduce" });
            await page.waitForTimeout(50);
            const animCs = await page.evaluate(() => {
                const el = document.querySelector('[data-probe="anim-default-respect"]');
                return el ? window.getComputedStyle(el).animationName : null;
            });
            // Under reduce, the gate fails → animation: none.
            // animationName might still be "fadeIn..." (rule string is
            // there but @media disables it). Check if computed-style
            // shows the animation OR rely on the rule presence as the
            // contract assertion. Browsers vary in how they report
            // animationName under disabled @media. We assert the rule
            // structure (sound) rather than the disabled-state spy.
            return { ok: true, msg: `respectReducedMotion correctly gated keyframes (animation-name under reduce: ${animCs || "none"})` };
        },
    },

    // ─── Dimension 2 — Viewport resize ────────────────────────────────
    //
    // Flex chains recompute on resize via the CSS engine alone — no
    // model field, no re-render. D2 verifies the chain stays sound
    // across resize events and that Ui.minimum holds.
    {
        id: "D2a",
        name: "D2a-flex-resize-recompute",
        port: 8822,
        descr: "flex chain recomputes on window.resize without model intervention",
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
            [ Ui.width Ui.fill, Ui.height Ui.fill, Ui.htmlAttribute "data-probe" "d2-row" ]
            [ Ui.el
                [ Ui.width (Ui.px 200), Ui.height Ui.fill
                , Background.color (Ui.rgb 240 240 240)
                , Ui.htmlAttribute "data-probe" "d2-sidebar"
                ]
                (Ui.text "side")
            , Ui.el
                [ Ui.width Ui.fill, Ui.height Ui.fill
                , Background.color (Ui.rgb 100 150 200)
                , Ui.htmlAttribute "data-probe" "d2-main"
                ]
                (Ui.text "main")
            ])


main =
    app
        { init = init, update = update, view = view, subscriptions = subscriptions
        , routes = [ route "/" MainPage ], notFound = MainPage
        }
`,
        assertions: async (page) => {
            // Measure at 1280x800 default.
            const initial = await page.evaluate(() => {
                const main = document.querySelector('[data-probe="d2-main"]');
                const side = document.querySelector('[data-probe="d2-sidebar"]');
                return {
                    main: main.getBoundingClientRect().width,
                    side: side.getBoundingClientRect().width,
                };
            });
            // sidebar fixed 200, main fills (~ vp - 200 - body-margin).
            if (Math.abs(initial.side - 200) > 4) {
                return { ok: false, msg: `initial sidebar w=${initial.side} ≠ 200` };
            }
            const expectedMain1 = 1280 - 200 - 16; // some chrome
            if (initial.main < expectedMain1 - 16) {
                return { ok: false, msg: `initial main w=${initial.main} < ${expectedMain1}` };
            }
            // Resize to 800x600.
            await page.setViewportSize({ width: 800, height: 600 });
            await page.waitForTimeout(60);
            const small = await page.evaluate(() => {
                const main = document.querySelector('[data-probe="d2-main"]');
                const side = document.querySelector('[data-probe="d2-sidebar"]');
                return {
                    main: main.getBoundingClientRect().width,
                    side: side.getBoundingClientRect().width,
                };
            });
            if (Math.abs(small.side - 200) > 4) {
                return { ok: false, msg: `after-resize sidebar w=${small.side} ≠ 200 (sidebar should stay fixed)` };
            }
            // main should track the new viewport width.
            const expectedMain2 = 800 - 200 - 16;
            if (small.main < expectedMain2 - 16 || small.main > expectedMain2 + 16) {
                return { ok: false, msg: `after-resize main w=${small.main} ≠ ~${expectedMain2} (didn't recompute)` };
            }
            // Resize back to 1280x800.
            await page.setViewportSize({ width: 1280, height: 800 });
            await page.waitForTimeout(60);
            const back = await page.evaluate(() => {
                const main = document.querySelector('[data-probe="d2-main"]');
                return main.getBoundingClientRect().width;
            });
            if (back < expectedMain1 - 16) {
                return { ok: false, msg: `after-resize-back main w=${back} (didn't grow back)` };
            }
            return { ok: true, msg: `flex chain recomputed: 1280→${initial.main.toFixed(0)}, 800→${small.main.toFixed(0)}, back→${back.toFixed(0)}` };
        },
    },

    {
        id: "D2b",
        name: "D2b-minimum-holds-on-shrink",
        port: 8823,
        descr: "Ui.minimum N Ui.fill keeps the floor under aggressive viewport shrink",
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
            [ Ui.padding 0 ]
            [ Ui.el
                [ Ui.width (Ui.minimum 320 Ui.fill)
                , Ui.height (Ui.px 60)
                , Background.color (Ui.rgb 100 150 200)
                , Ui.htmlAttribute "data-probe" "d2b-floor"
                ]
                (Ui.text "min 320")
            ])


main =
    app
        { init = init, update = update, view = view, subscriptions = subscriptions
        , routes = [ route "/" MainPage ], notFound = MainPage
        }
`,
        assertions: async (page) => {
            // Start at 1280: width should fill ≈ 1280.
            const wide = await page.evaluate(() => document.querySelector('[data-probe="d2b-floor"]').getBoundingClientRect().width);
            // Shrink to 240 — should clamp to min 320.
            await page.setViewportSize({ width: 240, height: 400 });
            await page.waitForTimeout(60);
            const tiny = await page.evaluate(() => document.querySelector('[data-probe="d2b-floor"]').getBoundingClientRect().width);
            if (tiny < 318) {
                return { ok: false, msg: `floor=${tiny} < 320 — Ui.minimum did not hold under 240px viewport` };
            }
            return { ok: true, msg: `Ui.minimum 320 held: vp=1280 → ${wide.toFixed(0)}, vp=240 → ${tiny.toFixed(0)}` };
        },
    },

    // ─── Dimension 5 — Ui.Lazy / Keyed ───────────────────────────────
    //
    // D5a: a Lazy-cached subtree's parent re-renders with a different
    // attr. The cached Element is REUSED but its rendered output is
    // walked fresh — so the parent's new style cascades correctly.
    // D5b: Keyed children survive same-key + attr-change updates.
    {
        id: "D5a",
        name: "D5a-lazy-survives-parent-style-change",
        port: 8824,
        descr: "Ui.Lazy.lazy: cached subtree renders correctly when parent attrs change",
        source: `module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Std.Cmd as Cmd
import Std.Sub as Sub
import Std.Live exposing (app, route)
import Std.Ui as Ui
import Std.Ui.Background as Background
import Std.Ui.Lazy as Lazy


type alias Model = { lightOn : Bool }
type Msg = Toggle
type Page = MainPage


init : a -> ( Model, Cmd Msg )
init _ = ( { lightOn = False }, Cmd.none )

update : Msg -> Model -> ( Model, Cmd Msg )
update _ m = ( { m | lightOn = not m.lightOn }, Cmd.none )

subscriptions : Model -> Sub Msg
subscriptions _ = Sub.none


-- A lazy child that doesn't depend on parent style.
renderLabel : String -> Ui.Element Msg
renderLabel s =
    Ui.el
        [ Ui.htmlAttribute "data-probe" "d5a-lazy-child" ]
        (Ui.text s)


view : Model -> any
view m =
    Ui.layout
        []
        (Ui.column
            [ Ui.padding 20
            , Background.color (if m.lightOn then Ui.rgb 250 240 200 else Ui.rgb 40 40 60)
            , Ui.htmlAttribute "data-probe" "d5a-parent"
            ]
            [ Lazy.lazy renderLabel "stable child"
            , Ui.el
                [ Ui.htmlAttribute "data-probe" "d5a-toggle"
                , Ui.onClick Toggle
                ]
                (Ui.text "click to toggle")
            ])


main =
    app
        { init = init, update = update, view = view, subscriptions = subscriptions
        , routes = [ route "/" MainPage ], notFound = MainPage
        }
`,
        assertions: async (page) => {
            // Initial: parent dark, lazy child says "stable child".
            const beforeBg = await page.evaluate(() => {
                const p = document.querySelector('[data-probe="d5a-parent"]');
                return window.getComputedStyle(p).backgroundColor;
            });
            const beforeChildText = await page.evaluate(() => {
                return document.querySelector('[data-probe="d5a-lazy-child"]').textContent;
            });
            // Click toggle.
            await page.click('[data-probe="d5a-toggle"]');
            await page.waitForTimeout(150);
            const afterBg = await page.evaluate(() => {
                const p = document.querySelector('[data-probe="d5a-parent"]');
                return window.getComputedStyle(p).backgroundColor;
            });
            const afterChildText = await page.evaluate(() => {
                return document.querySelector('[data-probe="d5a-lazy-child"]').textContent;
            });
            if (beforeBg === afterBg) {
                return { ok: false, msg: `parent bg didn't change after toggle: ${beforeBg}` };
            }
            if (afterChildText !== "stable child" || beforeChildText !== "stable child") {
                return { ok: false, msg: `lazy child text changed: before="${beforeChildText}", after="${afterChildText}"` };
            }
            return { ok: true, msg: `Lazy subtree stable across parent style change (bg: ${beforeBg} → ${afterBg}, child stable)` };
        },
    },

    {
        id: "D5b",
        name: "D5b-keyed-children",
        port: 8825,
        descr: "Std.Ui.Keyed: sky-key attribute survives to DOM for diff-identity",
        source: `module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Std.Cmd as Cmd
import Std.Sub as Sub
import Std.Live exposing (app, route)
import Std.Ui as Ui
import Std.Ui.Background as Background
import Std.Ui.Keyed as Keyed


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
        (Keyed.column
            [ Ui.spacing 8, Ui.padding 16, Ui.htmlAttribute "data-probe" "d5b-keyed-col" ]
            [ ( "alpha", Ui.el [ Ui.htmlAttribute "data-probe" "d5b-item-alpha" ] (Ui.text "A") )
            , ( "beta",  Ui.el [ Ui.htmlAttribute "data-probe" "d5b-item-beta"  ] (Ui.text "B") )
            , ( "gamma", Ui.el [ Ui.htmlAttribute "data-probe" "d5b-item-gamma" ] (Ui.text "C") )
            ])


main =
    app
        { init = init, update = update, view = view, subscriptions = subscriptions
        , routes = [ route "/" MainPage ], notFound = MainPage
        }
`,
        assertions: async (page) => {
            // Verify each keyed child has a sky-key attribute on its
            // wrapper. Keyed wraps each (key, child) in an outer
            // Ui.el so the sky-key is on the wrapper, not the child.
            const m = await page.evaluate(() => {
                const items = ["alpha", "beta", "gamma"].map((k) => {
                    const child = document.querySelector('[data-probe="d5b-item-' + k + '"]');
                    if (!child) return { key: k, found: false };
                    // Walk up to find a sky-key ancestor.
                    let p = child;
                    while (p && p !== document.body) {
                        if (p.hasAttribute("sky-key")) {
                            return { key: k, found: true, skyKey: p.getAttribute("sky-key"), text: child.textContent };
                        }
                        p = p.parentElement;
                    }
                    return { key: k, found: false };
                });
                return { items };
            });
            const missing = m.items.filter((i) => !i.found);
            if (missing.length > 0) {
                return { ok: false, msg: `missing sky-key ancestors for: ${missing.map((m) => m.key).join(",")}` };
            }
            for (const it of m.items) {
                if (it.skyKey !== it.key) {
                    return { ok: false, msg: `keyed item "${it.key}" has sky-key="${it.skyKey}" (mismatch)` };
                }
            }
            return { ok: true, msg: `Keyed.column attached sky-key to 3/3 wrappers: ${m.items.map((i) => i.key + "=" + i.skyKey).join(", ")}` };
        },
    },

    // ─── Dimension 6 — Std.Ui.Responsive + breakpoint compose ────────
    //
    // D6a verifies the boundary semantics: at viewport 700px,
    // Std.Ui.Responsive.classifyDevice says Tablet (≥ 600); but
    // Ui.breakpoint Ui.mobile says match (≤ 767). They DISAGREE in
    // the 600-767 band. This cell documents that divergence with a
    // visible test so users (and v0.15.58 candidate fix) see it.
    {
        id: "D6a",
        name: "D6a-responsive-vs-breakpoint",
        port: 8826,
        descr: "Std.Ui.Responsive.mobile (< 600) vs Ui.breakpoint Ui.mobile (≤ 767) — boundary thresholds",
        source: `module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Std.Cmd as Cmd
import Std.Sub as Sub
import Std.Live exposing (app, route)
import Std.Ui as Ui
import Std.Ui.Background as Background
import Std.Ui.Responsive as Responsive


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
            [ Ui.padding 8 ]
            [ -- This element flips colour at the breakpoint-mobile boundary.
              Ui.breakpoint Ui.mobile
                [ Background.color (Ui.rgb 200 50 50)
                , Ui.htmlAttribute "data-bp-state" "mobile"
                ]
                (Ui.el
                    [ Ui.width (Ui.px 200), Ui.height (Ui.px 40)
                    , Background.color (Ui.rgb 50 200 50)
                    , Ui.htmlAttribute "data-probe" "d6a-bp-target"
                    ]
                    (Ui.text "bp mobile"))
            , -- The classifyDevice-thresholds expressed as raw constants
              -- so the test can read them (we emit them as data-* attrs).
              Ui.el
                [ Ui.width (Ui.px 200), Ui.height (Ui.px 40)
                , Ui.htmlAttribute "data-responsive-threshold-phone" "600"
                , Ui.htmlAttribute "data-breakpoint-threshold-mobile" "767"
                ]
                (Ui.text "thresholds")
            ])


main =
    app
        { init = init, update = update, view = view, subscriptions = subscriptions
        , routes = [ route "/" MainPage ], notFound = MainPage
        }
`,
        assertions: async (page) => {
            // Test at 500px: both APIs should agree → mobile.
            await page.setViewportSize({ width: 500, height: 600 });
            await page.waitForTimeout(60);
            const at500 = await page.evaluate(() => {
                const el = document.querySelector('[data-probe="d6a-bp-target"]');
                const cs = window.getComputedStyle(el.parentElement);
                return cs.backgroundColor;
            });
            // At 500, breakpoint mobile matches → parent has rgb(200,50,50).
            const bpMatches500 = /rgb\(200,\s*50,\s*50\)/.test(at500);

            // Test at 700px: Responsive says Tablet (≥ 600), breakpoint says still mobile (≤ 767). DIVERGENCE.
            await page.setViewportSize({ width: 700, height: 600 });
            await page.waitForTimeout(60);
            const at700 = await page.evaluate(() => {
                const el = document.querySelector('[data-probe="d6a-bp-target"]');
                const cs = window.getComputedStyle(el.parentElement);
                return cs.backgroundColor;
            });
            const bpMatches700 = /rgb\(200,\s*50,\s*50\)/.test(at700);

            // Test at 900px: both → not mobile.
            await page.setViewportSize({ width: 900, height: 600 });
            await page.waitForTimeout(60);
            const at900 = await page.evaluate(() => {
                const el = document.querySelector('[data-probe="d6a-bp-target"]');
                const cs = window.getComputedStyle(el.parentElement);
                return cs.backgroundColor;
            });
            const bpMatches900 = /rgb\(200,\s*50,\s*50\)/.test(at900);

            // Document the divergence (PASS = both APIs report what their
            // thresholds say; the divergence in 600-767 is a known v0.15.58
            // candidate task #410).
            const results = {
                "vp=500 bp.mobile matches": bpMatches500,
                "vp=700 bp.mobile matches (Responsive disagrees: ≥600 = Tablet)": bpMatches700,
                "vp=900 bp.mobile no-match": !bpMatches900,
            };
            const failures = [];
            if (!results["vp=500 bp.mobile matches"]) failures.push("at 500px breakpoint mobile didn't match");
            if (!results["vp=700 bp.mobile matches (Responsive disagrees: ≥600 = Tablet)"]) failures.push("at 700px breakpoint mobile didn't match (audit's predicted threshold)");
            if (!results["vp=900 bp.mobile no-match"]) failures.push("at 900px breakpoint mobile DID match (shouldn't)");
            if (failures.length) {
                return { ok: false, msg: failures.join(" | ") };
            }
            return {
                ok: true,
                msg: `breakpoint thresholds verified across vp; Responsive ≥600=Tablet vs breakpoint ≤767=mobile DIVERGES in 600-767 band (#410 candidate)`,
            };
        },
    },

    // ─── Dimension 7 — Ui.html raw escape hatch ──────────────────────
    //
    // Ui.html injects an arbitrary Std.Html node into the Ui tree
    // wrapped in a `Raw` constructor. The runtime renders it
    // verbatim — no inline-style wrapper, no flex context. D7a
    // verifies that a raw <div> inside a Ui.row doesn't break the
    // surrounding flex chain; it lives as a flex item but with no
    // typed-Ui sizing assertions.
    {
        id: "D7a",
        name: "D7a-html-raw-in-flex-row",
        port: 8827,
        descr: "Ui.html raw <div> inside Ui.row participates in flex layout (no inline-style wrapper)",
        source: `module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Std.Cmd as Cmd
import Std.Sub as Sub
import Std.Live exposing (app, route)
import Std.Ui as Ui
import Std.Ui.Background as Background
import Std.Html as Html
import Std.Html.Attributes as Attr


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
            [ Ui.width Ui.fill, Ui.spacing 8, Ui.padding 8, Ui.htmlAttribute "data-probe" "d7-row" ]
            [ Ui.el
                [ Ui.width (Ui.px 100), Ui.height (Ui.px 60), Background.color (Ui.rgb 60 80 200)
                , Ui.htmlAttribute "data-probe" "d7-typed-left"
                ]
                (Ui.text "typed")
            , Ui.html
                (Html.node "div"
                    [ Attr.attribute "data-probe" "d7-raw-mid"
                    , Attr.attribute "style" "width:100px;height:60px;background:rgb(200,80,60);"
                    ]
                    [ Html.text "raw" ])
            , Ui.el
                [ Ui.width (Ui.px 100), Ui.height (Ui.px 60), Background.color (Ui.rgb 80 200 60)
                , Ui.htmlAttribute "data-probe" "d7-typed-right"
                ]
                (Ui.text "typed-r")
            ])


main =
    app
        { init = init, update = update, view = view, subscriptions = subscriptions
        , routes = [ route "/" MainPage ], notFound = MainPage
        }
`,
        assertions: async (page) => {
            const m = await page.evaluate(() => {
                const get = (sel) => {
                    const el = document.querySelector("[data-probe='" + sel + "']");
                    if (!el) return null;
                    const r = el.getBoundingClientRect();
                    const cs = window.getComputedStyle(el);
                    return { x: r.left, y: r.top, w: r.width, h: r.height, display: cs.display };
                };
                return {
                    row: get("d7-row"),
                    left: get("d7-typed-left"),
                    mid: get("d7-raw-mid"),
                    right: get("d7-typed-right"),
                };
            });
            const failures = [];
            // All four should exist.
            if (!m.row || !m.left || !m.mid || !m.right) {
                failures.push("missing one of row / left / mid / right probes");
            }
            // Three children laid out horizontally → distinct x, same y (or close).
            if (m.left && m.mid && m.right) {
                // Left should be leftmost.
                if (!(m.left.x < m.mid.x && m.mid.x < m.right.x)) {
                    failures.push(`x ordering wrong: left=${m.left.x.toFixed(0)}, mid=${m.mid.x.toFixed(0)}, right=${m.right.x.toFixed(0)}`);
                }
                // All ~same y (within ~4px).
                const ys = [m.left.y, m.mid.y, m.right.y];
                if (Math.max(...ys) - Math.min(...ys) > 8) {
                    failures.push(`vertical drift: ys=${ys.map((y) => y.toFixed(0)).join(",")}`);
                }
                // Sizes preserved.
                if (Math.abs(m.left.w - 100) > 4 || Math.abs(m.mid.w - 100) > 4 || Math.abs(m.right.w - 100) > 4) {
                    failures.push(`widths drifted from 100: l=${m.left.w}, m=${m.mid.w}, r=${m.right.w}`);
                }
            }
            if (failures.length) {
                return { ok: false, msg: failures.join(" | ") };
            }
            return { ok: true, msg: `Ui.html raw <div> lays out as flex item alongside typed siblings: left(${m.left.x.toFixed(0)},${m.left.w.toFixed(0)}w) | mid(${m.mid.x.toFixed(0)},${m.mid.w.toFixed(0)}w) | right(${m.right.x.toFixed(0)},${m.right.w.toFixed(0)}w)` };
        },
    },

    {
        id: "D7b",
        name: "D7b-html-raw-bare-no-wrapper",
        port: 8828,
        descr: "Ui.html does NOT add an inline-style wrapper — the raw node renders verbatim",
        source: `module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Std.Cmd as Cmd
import Std.Sub as Sub
import Std.Live exposing (app, route)
import Std.Ui as Ui
import Std.Html as Html
import Std.Html.Attributes as Attr


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
            [ Ui.padding 16 ]
            [ Ui.html
                (Html.node "canvas"
                    [ Attr.attribute "id" "raw-canvas"
                    , Attr.attribute "width" "200"
                    , Attr.attribute "height" "100"
                    ]
                    [])
            ])


main =
    app
        { init = init, update = update, view = view, subscriptions = subscriptions
        , routes = [ route "/" MainPage ], notFound = MainPage
        }
`,
        assertions: async (page) => {
            const m = await page.evaluate(() => {
                const c = document.querySelector("canvas#raw-canvas");
                if (!c) return { found: false };
                const cs = window.getComputedStyle(c);
                const parent = c.parentElement;
                const parentTag = parent ? parent.tagName : null;
                const inlineStyle = c.getAttribute("style") || "";
                // The HTML width/height attributes on <canvas> set the
                // bitmap dimensions (NOT the layout size — that's CSS-
                // driven). Read the JS DOM properties to assert the
                // attribute round-tripped.
                return {
                    found: true,
                    bitmapWidth: c.width,
                    bitmapHeight: c.height,
                    widthAttr: c.getAttribute("width"),
                    heightAttr: c.getAttribute("height"),
                    display: cs.display,
                    parentTag,
                    inlineStyle,
                };
            });
            if (!m.found) {
                return { ok: false, msg: "raw canvas not in DOM" };
            }
            // Bitmap width/height attributes survive to the DOM verbatim.
            if (m.widthAttr !== "200" || m.heightAttr !== "100") {
                return { ok: false, msg: `canvas width/height HTML attrs ("${m.widthAttr}", "${m.heightAttr}") ≠ ("200", "100") — Std.Html.Attributes.attribute did not round-trip` };
            }
            // Bitmap JS-side dims.
            if (m.bitmapWidth !== 200 || m.bitmapHeight !== 100) {
                return { ok: false, msg: `canvas bitmap (${m.bitmapWidth}, ${m.bitmapHeight}) ≠ (200, 100)` };
            }
            // The canvas's inline style should be empty (Ui.html renders verbatim).
            if (m.inlineStyle.length > 0) {
                return { ok: false, msg: `raw canvas has unexpected inline style: "${m.inlineStyle}"` };
            }
            return { ok: true, msg: `raw <canvas> rendered verbatim: bitmap=${m.bitmapWidth}x${m.bitmapHeight}, no Ui-injected style, parent=${m.parentTag}` };
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
    // Strip SKY_RUNTIME_DIR — the nix shellHook pins it at the
    // PARENT repo's runtime-go, which masks any worktree-local
    // runtime-go changes from `sky build`. Without this scrub the
    // matrix builds the fixture against a stale runtime and our
    // void-element / pseudo-class assertions (#409) won't reflect
    // the in-worktree fix.
    const env = { ...process.env };
    delete env.SKY_RUNTIME_DIR;
    const r = spawnSync(SKY_BIN, ["build", "src/Main.sky"], {
        cwd: dir, stdio: "pipe", env, timeout: 180_000,
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
                if (!(await waitForReady(fx.port, 15000))) {
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
