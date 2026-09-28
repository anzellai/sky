# Std.Ui.Terminal — DOM byte stream vs. server screen + canvas

What moving the terminal emulation to the server, drawing on a canvas and
sending screen-diff frames bought, against the first cut of the module, and
why binary frames were not built.

- **Before**: `f4e98f10` (release/v0.27.0). The widget parses the PTY output
  itself (a JS VT) and renders DOM `<span>` rows. The output goes to the page
  as base64 `"output"` island commands, one per `Process.readWithin` read
  (at most 64 KiB), over SSE.
- **After**: `fix/v027-terminal`. `Process.screen` runs every output byte
  through a Go VT screen (`runtime-go/rt/term_screen.go`) and returns
  screen-diff frames (`term_frame.go`), at least 16 ms apart. The widget
  (`island_terminal.go`) applies them and draws on a `<canvas>`, one draw
  pass per animation frame over the changed rows.

## Conditions

| | |
|---|---|
| Host | Apple M1, 8 cores, 16 GB, macOS 27.0, arm64; other agents' work on the machine at the same time |
| Go / Node / Playwright | 1.26.1 / 26.3.1 / 1.59.1, headless Chromium |
| App | `rust/crates/sky/tests/fixtures/ui-terminal` (Sky.Live, `sh` on a PTY), built by each tree's compiler |

## Workloads (`workloads.mjs`)

Deterministic byte streams, the bytes a PTY hands the server:

| name | bytes | what |
|---|---|---|
| `yes` | 4,194,303 | `y\r\n` to 4 MiB |
| `seq` | 1,488,895 | `seq 1 200000` |
| `redraw` | 686,100 | 300 x (`clear`, a 38-line coloured `ls -la`) |
| `colour` | 5,816,273 | 60 full 120 x 40 screens, every cell its own 256-colour fg and bg |

## Method

1. **Widget alone** (`bench-widget.mjs`, median of 5). Headless Chromium, the
   island sized to exactly 120 x 40 cells, fed the commands one per task
   (`MessageChannel`, the way SSE messages arrive) as fast as the page takes
   them. Before: the stream cut into 64 KiB `"output"` commands. After: the
   frames the Go screen makes from the same 64 KiB cuts, one per cut, no
   pacing (`TERM_BENCH_DIR=<dir> go test ./rt -run TestTerminalWireCost`
   writes them) — the same number of messages as before. Measured: first
   command to last paint, time inside the widget's command handler and
   paint, widget work per animation frame, missed animation frames (a gap of
   k x 16.7 ms counts k-1), and Chromium's `Performance.getMetrics`
   TaskDuration / ScriptDuration / Layout + RecalcStyle deltas.
2. **Wire cost** (`TestTerminalWireCost` in `runtime-go/rt/term_bench_test.go`,
   output in `wire-cost.txt`). For each workload in 64 KiB reads: the
   base64 `"output"` messages, the JSON frames, and the same frames in a
   compact binary encoding (varints, a tag byte per op, length-prefixed
   UTF-8) base64-encoded for SSE. Each as SSE island event text, raw and
   through one gzip stream flushed after every message (what a compressing
   proxy such as Caddy `encode gzip` does to an SSE stream).
3. **End to end** (`bench-app.mjs`, median of 3). The fixture app; the test
   types `cat <workload>.bin; echo; echo DONE$((40+2))` and waits (60 s cap)
   for the line `DONE42`. Measured: Enter to `DONE42`, SSE island bytes and
   messages (CDP `Network.eventSourceMessageReceived`), Chromium
   TaskDuration, missed animation frames, and whether the last line before
   `DONE42` is the workload's last line. The fixture's terminal is 300 px
   tall (about 15 rows): every workload scrolls.
4. **Slow client.** `bench-app.mjs --throttle-kbps 2000` (CDP network
   emulation) and the Go test `TestTerminalScreenIsExactWhenTheRingOverflows`
   (a 4 KiB ring, 3,000 lines, the reader behind by far more than the ring).
5. **Server cost.** `go test ./rt -bench BenchmarkVTScreenFeed` (the screen
   consuming built-in versions of `yes`, `seq`, `colour` at 120 x 40).

## Results

### 1. Widget alone, 120 x 40 (`before-widget.json`, `after-widget.json`)

| workload | | total ms | command ms | paint ms | work / frame p95 ms | missed frames | Chromium task ms | layout + style ms |
|---|---|---|---|---|---|---|---|---|
| `yes` | before | 2,208.6 | 2,151.9 | 26.0 | 35.3 | 64 of 66 | 2,252.5 | 7.1 |
| | after | **33.5** | 6.4 | 8.3 | 6.0 | 0 of 5 | 157.7 | 0.7 |
| `seq` | before | 388.8 | 338.7 | 24.4 | 25.0 | 0 of 25 | 400.1 | 8.4 |
| | after | **26.1** | 5.6 | 5.3 | 5.6 | 0 of 4 | 105.0 | 0.9 |
| `redraw` | before | 50.1 | 28.8 | 4.0 | 8.8 | 0 of 6 | 48.4 | 2.6 |
| | after | **16.6** | 1.6 | 5.0 | 5.0 | 0 of 2 | 13.8 | 0.4 |
| `colour` | before | 2,935.4 | 265.9 | 552.1 | 11.8 | 83 of 91 | 3,018.2 | 679.8 |
| | after | **1,335.7** | 11.1 | 376.2 | 6.0 | 6 of 92 | 1,996.9 | 33.4 |

The before widget's time goes to parsing (the JS VT: `yes` is 1.4 M
scrolls) and, on `colour`, to style and layout of up to 4,800 styled spans
per paint. After, parsing is on the server and a paint is a few `fillRect` /
`fillText` calls per changed row.

### 2. Wire cost, 64 KiB reads (`wire-cost.txt`)

| workload | base64 bytes raw / gzip | JSON frames raw / gzip | binary frames raw / gzip |
|---|---|---|---|
| `yes` | 5,599,958 / 14,006 | 562,984 / 4,131 | 544,768 / 3,948 |
| `seq` | 1,987,882 / 519,643 | 311,465 / 53,163 | 341,248 / 64,179 |
| `redraw` | 916,075 / 66,165 | 17,624 / 2,569 | 18,248 / 3,737 |
| `colour` | 7,765,554 / 1,558,567 | 5,450,137 / 1,821,005 | 3,546,908 / 1,848,616 |

Frames against bytes, raw: 10x (`yes`), 6.4x (`seq`), 52x (`redraw`), 1.4x
(`colour`) fewer. Binary against JSON frames: raw -3% / +10% / +4% / -35%;
through gzip -4% / +21% / +45% / +2%.

### 3. End to end (`before-app.json`, `after-app.json`)

| workload | | Enter to DONE42 | SSE bytes | SSE messages | Chromium task ms | missed frames | right last line |
|---|---|---|---|---|---|---|---|
| `yes` | before | **never (60 s)** | 3,914,384 | 136 | 1,613 | 59 | 0 of 3 |
| | after | 766 ms | 325,678 | 41 | 42 | 0 | 3 of 3 |
| `seq` | before | 428 ms | 2,255,146 | 30 | 367 | 19 | 3 of 3 |
| | after | 205 ms | 104,607 | 10 | 19 | 0 | 3 of 3 |
| `redraw` | before | 117 ms | 931,387 | 14 | 84 | 4 | 3 of 3 |
| | after | 106 ms | 195,049 | 5 | 17 | 0 | 3 of 3 |
| `colour` | before | 425 ms | 7,193,065 | 98 | 391 | 16 | 2 of 3 |
| | after | 416 ms | 4,872,489 | 19 | 151 | 0 | 3 of 3 |

The before `yes` never shows `DONE42`: the output outruns the page, the
session's SSE buffer (16 frames) fills, and the island commands that do not
fit are dropped. The widget only noticed a gap when a LATER command arrived,
so the dropped commands at the end of the output left the terminal stopped
short of the last line until the next output (confirmed by reading the
widget's offset: it stopped at 3,033,573 of 4,194,303 bytes, with no gap
flagged). One `colour` run lost its end the same way. After, frames are
paced (16 ms), one per paint at most, so the buffer does not fill; if a frame
is lost anyway, the next frame or the check frame (0.75 s after a burst)
shows the gap and the widget asks for one repaint.

`colour` stays large after (4.9 MB): on a 15-row terminal every screen of
the stress scrolls 40 rows of per-cell colours into the scrollback, and the
scrollback lines are sent with their colours.

### 4. Slow client

`--throttle-kbps 2000` did not bind the SSE stream in headless Chromium:
the before `seq` run moved 2.26 MB in 1.9 s (1.2 MB/s, over the 250 KB/s
cap). The figures are in `before-slow-*.json` / `after-slow-*.json` for the
record (before `yes`: 1 of 2 runs right, 1 repaint; after: 2 of 2 right for
every workload) but the slow-client argument rests on:

- `TestTerminalScreenIsExactWhenTheRingOverflows`: the screen, fed by the
  pump as the process writes, ends with the exact screen, scrollback and
  exit line of 3,000 lines through a 4 KiB ring. The byte replay could only
  start from the ring's oldest byte, so its screen and scrollback were those
  of the tail.
- the flood result above: before, the page falling behind lost frames with
  no recovery; after, frames coalesce and a loss is repaired.

### 5. Server cost (`BenchmarkVTScreenFeed`, 120 x 40)

| workload | throughput |
|---|---|
| `yes` (3-byte lines: one scroll each) | 8.4-8.7 MB/s (about 2.9 M lines/s) |
| `seq` | 15.5-16.0 MB/s |
| `colour` | 66-67 MB/s |

The screen runs on the process's output pump, so a process that writes
faster than this is slowed to it (the flow control every terminal has); a
slow page is not.

## Decisions

1. **Canvas renderer: built. The DOM span renderer: removed.** The canvas was
   faster on every workload (table 1), and most on the ones the span renderer
   was worst at. The text layer over the canvas (transparent, one row per
   screen row) keeps what the spans gave: mouse selection, copy, a screen
   reader. Without a 2D canvas it is shown as a monochrome fallback.
2. **Server-side VT with a screen-diff op stream: built.** Scroll ops, row
   spans with styles, cursor, title, modes, bell. A remount is one repaint
   frame from the screen plus the last 1000 scrollback lines.
3. **Raw-byte replay: removed.** Nothing needs the bytes once the screen is
   the source of truth, and the replay was what could not survive a ring
   overflow. `Terminal.encodeOutput` / `encodeExit` (unreleased) went with it.
4. **Binary frames: not built.** Through gzip they save 4% on `yes` and cost
   more on the other three (table 2), under the 10% bar; uncompressed they
   save 35% only on the per-cell colour stress. A binary path would need a
   second transport (a WebSocket for terminal islands) proven against the
   strict CSP, proxies, reconnect and the header-session transport, for no
   measured saving. Frames stay JSON on the island SSE channel.
