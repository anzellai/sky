# Std.Ui.Canvas — SVG against a batched `<canvas>`

Which backend should draw a `Std.Ui.Canvas` scene on each target, measured.
Before this run every web backend drew the scene as inline SVG, and the
decision not to build a canvas backend rested on reasoning alone. This run
measures the SVG path on Sky.Live and Sky.Spa and a batched canvas renderer on
Sky.Spa, then records the decision.

- **Tree**: `fix/v027-judge2-canvas` off `release/v0.27.0` (85e86831), with
  the canvas backend (`runtime-go/rt/scene_canvas.go`,
  `scene_canvas_wasm.go`, `scenePainterJS` in `scene_client.go`) and the
  Sky.Live parse fix (section 5). Both backends run from the same build: the
  bench sets `window.Sky.sceneBackend = "svg" | "canvas"` before the client
  boots, so the SVG figures are the SVG path of the same binary.
- **App**: `bench-app/` (built with `sky build --target web` for Sky.Live and
  `--target web:app` for Sky.Spa, from a copy outside the repo).

## Conditions

| | |
|---|---|
| Host | Apple M1, 8 cores, 16 GB, macOS 27.0, arm64; other agents' work on the machine at the same time |
| Node / Playwright | 26.3.1 / 1.59.1 |
| Browsers | **headed** Google Chrome 154 (`--channel chrome`) and Playwright WebKit (Safari 26.4 engine, the engine of the macOS desktop window), one at a time |
| Page | viewport 1000 x 800, devicePixelRatio 2, `SKY_CSP=strict` (`script-src 'self' 'wasm-unsafe-eval'`), `ENV=development` |

Headed WebKit on this machine opens a 640 px wide window whatever viewport is
asked for, so the right fifth of the 800 px scene is off screen there. The hit
points (below) stay in the left 75% of the scene for that reason. WebKit's
`performance.now()` has 1 ms resolution, and a headed WebKit frame is
vsync-paced, so WebKit figures under about 12 ms read as one frame.

## Workload (`bench-app/src/Main.sky`)

A scene of N shapes on an 800 x 600 grid, in turn a rectangle, a circle, a
path (a stroked triangle) and a text, every shape with `onClick (Hit i)`, the
scene with `onPointerMove`. N is 10, 100, 1,000, 5,000 and 20,000. `step`
advances the frame counter; in `few` mode shapes 0 to 4 move with it (an
animated scene where a few shapes change per frame), in `all` mode every shape
moves (a particle or plot scene).

## Method (`bench.mjs`, driven by `run.sh`)

For each target, backend, browser and N:

1. **Static render**: click `n0`, then `n<N>`; the time from the click to the
   page showing N, then a `requestAnimationFrame` callback, then a macrotask
   (so the frame's script, the canvas painter's pass, style, layout and paint
   have run). Median of 3.
2. **Memory** after the static render: DOM elements in the page, the JS heap
   (Chrome, CDP `Performance.getMetrics` `JSHeapUsedSize`, one sample, no
   forced GC) and the wasm memory (Sky.Spa).
3. **Hit test**: one hit test at 200 points over the scene, the browser's
   (`document.elementFromPoint`) for SVG and `Sky.sceneCanvas.hitTest` for the
   canvas; mean µs per point.
4. **Pointer latency**: a click on a static rectangle in the middle row,
   dispatched on the element `document.elementFromPoint` returns there (the
   target the browser would pick), to the page showing its index and the next
   frame painted. Median of 3. On Sky.Spa this includes the re-render the new
   model causes.
5. **Per frame**: in `few` and then `all` mode, 20 `step` clicks, each to the
   page showing the new frame and the next frame painted: median and p95; on
   Chrome, main-thread TaskDuration / ScriptDuration / Layout + RecalcStyle
   per frame (CDP). A case stops after 60 s once it has 3 frames ("capped").
   For the canvas, the painter's own counters: paints, partial paints, shapes
   drawn in the last paint, the last paint's time.

Sky.Live runs the same clicks; the time includes the round trip to the server
on loopback.

Raw results: `raw/<target>-<backend>-<browser>.json` (the table figures are
those files). `run.sh <live-app-dir> <spa-backend-dir> <out-dir>` reruns the
matrix.

## Results

### 1. Sky.Spa, Chrome (`raw/spa-svg-chromium.json`, `raw/spa-canvas-chromium.json`)

| N | | static ms | `few` median / p95 ms | `all` median / p95 ms | click to painted ms | hit test µs | DOM elements | JS heap MB |
|---|---|---|---|---|---|---|---|---|
| 10 | SVG | 5.6 | 5.2 / 5.5 | 5.2 / 5.4 | 5.6 | 2.4 | 56 | 2.5 |
| | canvas | 5.4 | 5.0 / 5.7 | 5.4 / 5.7 | 5.5 | 0.9 | 45 | 2.8 |
| 100 | SVG | 11.7 | 4.9 / 6.9 | 6.9 / 10.1 | 4.4 | 3.5 | 146 | 4.1 |
| | canvas | 6.1 | 4.6 / 7.4 | 5.2 / 7.3 | 4.4 | 1.0 | 45 | 3.2 |
| 1,000 | SVG | 110.2 | 41.3 / 43.2 | 73.5 / 78.5 | 40.6 | 20.6 | 1,046 | 4.3 |
| | canvas | **52.9** | 39.5 / 42.8 | **47.0 / 51.4** | 40.9 | 6.7 | 45 | 4.0 |
| 5,000 | SVG | 558.9 | 196.3 / 208.6 | 439.0 / 458.2 | 196.8 | 94.4 | 5,046 | 13.5 |
| | canvas | **250.0** | 190.7 / 196.6 | **224.9 / 232.2** | 184.0 | 27.3 | 45 | 10.2 |
| 20,000 | SVG | 2,160.6 | 806.7 / 831.2 | 3,714.2 / 3,835.5 (capped, 17 frames) | 801.4 | 603.5 | 20,046 | 11.0 |
| | canvas | **959.3** | 763.7 / 788.5 | **896.3 / 922.4** | 754.3 | 241.7 | 45 | 34.2 |

Main thread per `all` frame at 20,000: SVG 3,728 ms (layout + style 40 ms),
canvas 902 ms (layout + style 0.1 ms; the painter's decode 25.6 ms, its full
paint 17.3 ms).

### 2. Sky.Spa, WebKit (`raw/spa-svg-webkit.json`, `raw/spa-canvas-webkit.json`)

| N | | static ms | `few` median / p95 ms | `all` median / p95 ms | click to painted ms | hit test µs |
|---|---|---|---|---|---|---|
| 10 | SVG | 13 | 12 / 13 | 12 / 13 | 11 | 2 |
| | canvas | 12 | 12 / 13 | 12 / 13 | 12 | 1 |
| 100 | SVG | 13 | 13 / 13 | 12 / 13 | 10 | 7 |
| | canvas | 14 | 12 / 13 | 12 / 13 | 11 | 2 |
| 1,000 | SVG | 107 | 35 / 35 | 74 / 78 | 37 | 52 |
| | canvas | **48** | 34 / 37 | **41 / 43** | 35 | 6 |
| 5,000 | SVG | 564 | 162 / 171 | 458 / 473 | 160 | 244 |
| | canvas | **215** | 160 / 168 | **193 / 198** | 161 | 21 |
| 20,000 | SVG | 2,261 | 655 / 689 | 4,578 / 4,691 (capped, 14 frames) | 704 | 1,656 |
| | canvas | **886** | 634 / 656 | **775 / 793** | 637 | 113 |

The canvas's last full paint at 20,000: 49 ms in WebKit, 17.3 ms in Chrome.

### 3. What the canvas does per frame (painter counters, both browsers)

- `few`: 19 of 20 frames are partial paints (the first after the mode change
  is full), drawing 6 to 13 shapes (the moved ones and those under their old
  and new boxes), 0 to 1 ms.
- `all`: every frame is one full paint of all N shapes (the region is the
  whole scene).
- Never more than one paint per frame: 20 steps, 20 paints.
- The backing store is 1600 x 1200 for the 800 x 600 scene at
  devicePixelRatio 2 (crisp), `role="img"`, the label as `aria-label`, and a
  text alternative through `aria-describedby`, in both browsers, with zero
  CSP violations. The one console error in the Chrome runs is the headed
  browser's `/favicon.ico` 404 (the app serves none); WebKit logs none.

### 4. Sky.Live, SVG (`raw/live-svg-chromium.json`, `raw/live-svg-webkit.json`)

| N | | static ms | `few` median ms | `all` median ms | click to painted ms | hit test µs |
|---|---|---|---|---|---|---|
| 1,000 | Chrome | 21.3 | 9.5 | 17.8 | 14.3 | 20.5 |
| | WebKit | 31 | 12 | 26 | 11 | 53 |
| 5,000 | Chrome | 99.6 | 35.0 | 161.1 | 36.2 | 85.8 |
| | WebKit | 138 | 40 | 227 | 39 | 250 |
| 20,000 | Chrome | 394.5 | 127.7 | 3,371.7 (capped) | 130.2 | 488.6 |
| | WebKit | 575 | 144 | 3,612 (capped) | 162 | 1,795 |

Sky.Live runs `view` and the diff natively on the server, so a `few` frame at
5,000 shapes costs 35 ms against 191 ms on Sky.Spa, whose `view` and diff run
in wasm in the page.

### 5. A defect this run found: Sky.Live in WebKit (`before-range-fix.txt`, `range-parse.mjs`, `range-parse.txt`)

The first matrix run measured the Sky.Live static render of 5,000 shapes in
WebKit at **93,258 ms** (1,000 shapes: 315 ms), and the 20,000 case did not
finish in 25 minutes. The Sky.Live client (`live_client_asset.go`) and the
desktop window client (`webview.go`) parsed each new child of an SVG through
its own `Range` (`createContextualFragment`, so the child stays in the SVG
namespace). WebKit keeps every `Range` live until it is collected and updates
each one on every later DOM change, so adding N children cost O(N²). Measured
alone (`range-parse.txt`, one page per case): 5,000 children took 49,199 ms in
WebKit with a Range per child, 17 ms with one Range, and 24 ms with a detached
`<svg>` whose `innerHTML` is set (Chrome: 123 / 17 / 20 ms). Both clients now
parse through a detached element of the container's own kind (no Range):
5,000 shapes render in 138 ms in WebKit, and in Chrome the 20,000-shape static
render went from 3,096 ms to 395 ms (first run against section 4).

## Decisions

1. **Sky.Spa draws every scene on a canvas; there is no size threshold.** No
   size measured SVG faster. At 10 and 100 shapes the two are within a
   millisecond in Chrome and in the same frame in WebKit; from 1,000 shapes
   the canvas wins every measure: the static render 2.1 to 2.3 x (Chrome) and
   2.2 to 2.6 x (WebKit); the every-shape-moves frame 1.6 x (Chrome) and
   1.8 x (WebKit) at 1,000, 2.0 x and 2.4 x at 5,000, 4.1 x and 5.9 x at
   20,000; the hit test 2.5 to 3.5 x (Chrome) and 8.7 to 14.7 x (WebKit); and
   45 DOM elements whatever N, against N + 46. The `few` frame and the click
   are the same on both (item 4). `sceneCanvasMin` is 0.
   The desktop, tablet and mobile native shells run the same client
   (`desktop:<os>` in WKWebView, measured here as WebKit), so they draw on
   the canvas too.
2. **Sky.Live stays SVG.** It is server-rendered: the first paint needs no
   script, and the diff sends attribute patches that are already small (a
   `few` frame at 5,000 shapes is 35 ms end to end). A canvas there would need
   the scene sent to the page as data on every change, for a gain only in the
   every-shape-moves case. `--target desktop` (Sky.Live in a native window)
   stays SVG with it.
3. **Partial repaint: built.** A changed shape redraws only the region its old
   and new boxes cover (19 of 20 `few` frames draw at most 13 shapes); when
   the changed region passes half the canvas, or more than a quarter of the
   shapes changed, the frame redraws everything, which is what the
   every-shape-moves case does.
4. **Where the Sky.Spa frame goes.** From 1,000 shapes the `few` frame is the
   same on both backends (5,000: 191 against 196 ms in Chrome): it is `view`
   and the diff running in wasm, not drawing. The canvas removes the drawing
   cost; it does not make a 5,000-shape scene animate at 60 frames a second
   on Sky.Spa. Sky.Live, which does that work natively, is the faster target
   for large animated scenes with few changes per frame.
5. **The Sky.Live SVG parse fix: built** (section 5), in the Sky.Live and the
   desktop window clients, with a WebKit regression case in
   `scripts/ui-canvas-terminal-e2e.sh`.
