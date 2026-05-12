# Sky.Gui on gio — Plan

Status: Experimenting (branch `exp/sky-gui-gio`)
Started: 2026-05-12

## Goal

Establish **gio** as Sky's cross-platform native rendering foundation.
Build `Sky.Gui` as a sibling backend to `Sky.Live` (HTML/SSE) and
`Sky.Tui` (ANSI cells), interpreting the same `Std.Ui` source.

The win: one Sky codebase compiles to:
- **Web** via `Sky.Live` (existing)
- **Terminal** via `Sky.Tui` (existing)
- **Desktop** (Linux/Mac/Windows) via `Sky.Gui` (new — this plan)
- **Mobile** (iOS/Android) via `Sky.Gui` (later — gio's native targets)

## Why gio (vs alternatives surveyed)

See [docs/design/tea-backends.md](./tea-backends.md) for the full survey.
Key findings:

- **gio** — pure-Go layout/text/widget library on top of native GPU
  APIs (Vulkan/Metal/OpenGL via syscalls). Used in production by
  Tailscale. ~95% of Std.Ui primitives map cleanly. Active (v0.9.0,
  pushed 2026-05-06). License: `Unlicense OR MIT`.
- **purego-sdl3** — gives a window + GPU surface, but NO layout, text,
  or widgets. We'd write those ourselves (months of work). Defer.
- **Fyne** — framework-shaped, retained widget tree fights TEA. Skip.
- **shiny / Slint / Skia / Qt** — abandoned, Rust-dep, drawing-only,
  or framework-shaped respectively. Skip.

gio's cgo footprint is **bounded** — only window/event interop on
desktop platforms (no cgo for rendering). End users don't install
anything; OS frameworks are always present.

## Out of scope (this plan)

- LSP hover/completion gap fixes → separate v0.12.2 stream
- Mobile (iOS/Android) compilation → after v0.13 desktop ships and
  stabilises
- Sky.Webview (PWA / localhost+browser packaging) → consider after
  Sky.Gui ships; web users already have Sky.Live

## Approach (staged)

### Stage 0 — Std.Ui portability audit
File: `docs/design/std-ui-portability-audit.md`

Classify every Std.Ui primitive across THREE backends. Three tiers:

- **Portable** — works on Sky.Live + Sky.Tui + Sky.Gui without
  primitive loss. The user can use freely.
- **Degradable** — works fully on Sky.Live; Sky.Tui + Sky.Gui
  approximate or warn. User code keeps working; rendering is best-
  effort on non-web targets.
- **Web-only** — Sky.Live only; non-web backends emit `tuiWarn`-style
  warnings and a placeholder. Examples: `Ui.html` escape hatch,
  certain gradient/shadow shapes.

Add a `Std.Ui.Portability` marker (compile-time hint or simple type
tag) so the user can see at a glance which tier a primitive is in.

### Stage 1 — gio spike
- Add `gioui.org v0.9.0` as a Go dep (vendored in `runtime-go/go.mod`)
- Write `runtime-go/rt/gui.go` — `Gui_app cfg` entry point mirroring
  `Live_app` / `Tui_app` shape (init/update/view/subscriptions)
- Smallest viable test: render `Ui.text "Hello"` in a 600×400 window
- Cross-compile probe: build for Linux from macOS via gio's cgo,
  confirm size + dependencies

### Stage 2 — Std.Ui → gio interpreter
- Port the portable subset (row/column/padding/spacing/alignment/
  Length/Background.color/Border.color+width+rounded/Font.size+
  weight+color/text/paragraph)
- Render to gio's `layout.Flex` / `layout.Inset` / `paint.FillShape` /
  `widget.Border` / `material.Label`
- Event handling: pointer (mouse) + key — wire `onClick` / `onInput`
  / `onKeyDown` to gio's input system
- One end-to-end example: `examples/25-gui-counter` (port of
  09-live-counter or 22-tui-stopwatch-ui)

### Stage 3 — Polish
- Test matrix: cabal + go test + a `scripts/gui-verify.sh` that builds
  the example and screenshots it (if possible — gio supports
  off-screen rendering via headless backend)
- Document supported subset + degradation rules
- Update `docs/skyui/overview.md` with Sky.Gui notes
- Update `templates/CLAUDE.md` with cross-backend example
- CI: add Sky.Gui build job to `.github/workflows/ci.yml`

### Stage 4 — Release as v0.13.0
- Cross-platform binaries (Linux/Mac/Windows)
- Migration notes for existing apps wanting desktop builds
- One marketing example: the canonical "your Sky.Live app, also a
  desktop app" demo

## Self-grill: risks + what I'd cut

1. **Retained widget state** — gio's `widget.Editor`, `widget.Clickable`
   etc. are stateful, fight TEA's pure-view model. Mitigation: use
   gio's lower-level `pointer.InputOp` / `key.InputOp` and rebuild
   state from Sky's model per frame. Tailscale does this; doable but
   ~30% more runtime code per widget type.

2. **Text shaping quality** — gio's `text.Shaper` is good but not
   as rich as a browser's. RTL / complex scripts / emoji might
   differ from Sky.Live's HTML rendering. Mitigation: document the
   difference; same code, slightly different appearance.

3. **Cmd.perform threading** — gio drives its own event loop; Sky's
   `Cmd.perform` goroutines need to invalidate the gio frame
   (`op.InvalidateOp`). Doable but not free.

4. **Mobile (iOS) toolchain** — even gio needs Xcode + Apple
   Developer account for iOS app bundles. Out of scope for v0.13;
   noted for v0.14+.

5. **What I'd cut if time pressed**: Stage 2 widget set could ship
   with `text` + `row/column` + `padding` + `onClick` only, deferring
   `input` / `form` / `image` to v0.13.1. Sub-MVP but clear path.

6. **Sky.Tui re-audit** — the portability audit might surface
   Std.Ui primitives that Sky.Tui currently warns about silently;
   if the audit declares those degradable, Tui's warnings should
   align with the same taxonomy.

## Concrete next actions

1. Branch `exp/sky-gui-gio` created (done)
2. Write `docs/design/std-ui-portability-audit.md` — exhaustive
   classification of every Std.Ui primitive
3. Add `gioui.org` to `runtime-go/go.mod`; write minimal `gui.go`
4. Render `Ui.text "Hello"` in a gio window
5. Pause + show to user for direction before Stage 2

## Open questions for the user

- **Cross-backend backend dispatch**: should the same binary be able
  to run as Sky.Live OR Sky.Gui at runtime (env var / CLI flag), or
  do we compile separate binaries per target?
- **PWA / `Sky.Webview`**: deprecated by this plan? Or still ship
  as a "quick desktop launcher" alternative for users who don't want
  the gio bundle weight?
- **`Std.Ui.Portability` marker shape**: warning vs hard type error
  on Web-only-in-non-web-target? Lean toward warning for now.
