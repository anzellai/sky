// Sky.Tui — Element-shape variant.
//
// Tui.app accepts a `view : Model -> Element Msg` (typed Std.Ui tree)
// and renders it to character cells, instead of Tui.program's
// `view : Model -> String` (raw frame the user assembles).
//
// This is the "write once, render anywhere" path: the same `view`
// function that produces an HTML rendering under Sky.Live can produce
// a TUI rendering under Tui.app, with explicit lossy fallbacks for
// visual decoration that doesn't carry to a character grid (font size,
// background images, drop shadows — see docs/design/std-ui-cross-
// platform.md).
//
// Logical-pixel canvas (Px N is a 1280×720-canvas pixel by default,
// configurable via cfg.canvas). Each renderer converts to native
// units; for TUI:
//
//   pxPerCellX = canvas_width  / term_cols
//   pxPerCellY = canvas_height / term_rows
//
// Recomputed on SIGWINCH so the layout reflows on terminal resize.

package rt

import (
	"fmt"
	"math"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"golang.org/x/term"
)

// Resource caps protect the host from runaway views.
//
//   - tuiMaxCanvas{Width,Height}: cap user-supplied logical-pixel
//     canvas dimensions. Anything larger is silently clamped — the
//     ratios that matter are the cell-per-px ratios, and a 1M ×
//     1M canvas is just bad input.
//   - tuiMaxContentH: hard cap on the laid-out view height. The
//     paint-grid allocation is cols × contentH cells (each ~64
//     bytes), so 50,000 rows × 200 cols ≈ 640 MB worst case.
//     Beyond this the view is truncated and a tuiWarn fires.
//   - tuiSoftWarnH: warn at 10,000 rows so users notice they're
//     building a pathological view long before they hit the cap.
const (
	tuiMaxCanvasWidth  = 100_000
	tuiMaxCanvasHeight = 100_000
	tuiMaxContentH     = 50_000
	tuiSoftWarnH       = 10_000
)

// tuiNoColor is set at app entry from $NO_COLOR. cellStyleSGR reads
// it when emitting SGR sequences and skips the fg/bg colour codes
// while keeping bold / underline / reverse so the user can still
// distinguish focus + emphasis on monochrome output.
var tuiNoColor bool

// ─── Public entry point ──────────────────────────────────────────────

func Tui_app(cfg any) any {
	return func() any {
		return tuiAppRun(cfg)
	}
}

// ─── Renderer state ──────────────────────────────────────────────────

type tuiCanvas struct{ width, height int }

type tuiCell struct {
	ch        string
	fg, bg    tuiColor
	bold      bool
	italic    bool
	underline bool
	strike    bool // text-decoration: line-through
	overline  bool // text-decoration: overline (SGR 53)
	reverse   bool
}

type tuiColor struct {
	set     bool
	r, g, b uint8
}

// focusable is one element the user can navigate to with Tab. The
// runtime tracks them in tab order so Enter activates by index, and
// editing keys (chars, backspace, etc.) flow into the focused input.
//
// `events` holds the full list of AttrEvent payloads on this element
// (eventPair values produced by Std.Live.Events.on*). We classify by
// the event's name field at activation time — "click" fires on Enter
// (and mouse click later), "input" fires per-keystroke for inputs,
// "change" fires when an input loses focus / receives Enter.
type focusable struct {
	events       []any
	isInput      bool   // tag=="input" — needs editor handling
	inputType    string // "text" | "password" | "checkbox" | "radio" | "range" | "textarea" | …
	initialValue string // from AttrAttribute "value", first-render only
	placeholder  string // from AttrAttribute "placeholder", shown on empty buffer
	row, col     int    // top-left corner of the focused element's box
	w, h         int
}

// isMultilineInput returns true if this focusable is a textarea-typed
// input. Used by the editor to decide whether Enter inserts a newline
// (multiline) or fires onChange (single-line submit).
func isMultilineInput(f focusable) bool {
	return f.isInput && f.inputType == "textarea"
}

// isCheckboxOrRadio returns true if this focusable is a checkbox or
// radio. Used by the main loop to translate Space presses into
// toggle/select Msgs (vs char insertion in text inputs).
func isCheckboxOrRadio(f focusable) bool {
	return f.isInput && (f.inputType == "checkbox" || f.inputType == "radio")
}

// tuiInput is per-input editor state, persisted across renders so the
// buffer survives even when the user's view doesn't carry value back
// (uncontrolled inputs work) and the cursor position survives when
// the model changes for unrelated reasons.
type tuiInput struct {
	buffer string
	cursor int    // rune index 0..len([]rune(buffer))
	lastValueAttr string // detect user-driven resets (model.draft = "")
}

// inputRegistry maps focus index → input state. Keyed by tab-order
// position, which is stable as long as the user doesn't add/remove
// focusable elements between renders. For dynamic forms, users should
// (one day) provide stable IDs via AttrAttribute "id" — for v1 this is
// good enough for the demo.
type inputRegistry struct {
	inputs map[int]*tuiInput
}

func newInputRegistry() *inputRegistry {
	return &inputRegistry{inputs: map[int]*tuiInput{}}
}

func (r *inputRegistry) get(idx int) *tuiInput {
	if r.inputs[idx] == nil {
		r.inputs[idx] = &tuiInput{}
	}
	return r.inputs[idx]
}

// tuiScroll is per-scroll-region offset state, persisted across renders.
type tuiScroll struct {
	offsetY int // rows scrolled down
	offsetX int // cols scrolled right
}

// scrollRegistry: same key strategy as inputRegistry (focus index in
// the tab order). Persistent so scrollY survives re-renders.
type scrollRegistry struct {
	regions map[int]*tuiScroll
}

func newScrollRegistry() *scrollRegistry {
	return &scrollRegistry{regions: map[int]*tuiScroll{}}
}

func (r *scrollRegistry) get(idx int) *tuiScroll {
	if r.regions[idx] == nil {
		r.regions[idx] = &tuiScroll{}
	}
	return r.regions[idx]
}

// ─── Main loop ──────────────────────────────────────────────────────

// tuiApplyUpdate runs the user's guard (if defined) before dispatch.
// Mirrors Sky.Live's guard semantics so the same auth-check function
// works under both runtimes:
//
//   guard : Msg -> Model -> Result Error ()
//
//   Ok ()       → dispatch the msg through update normally
//   Err reason  → SKIP update and stamp model.Notification = reason
//                 + model.NotificationType = "error" (if those
//                 fields exist on the user's record). The view
//                 inspects the notification field to render an
//                 in-app banner / toast / status line.
//
// Use case: auth-gated screens. e.g. user model has session field,
// guard rejects every Msg except Login until session is Just _.
//
// If guard isn't defined, the msg goes straight to update — zero
// overhead for the common case.
func tuiApplyUpdate(guardFn, updateFn, msg, model any, msgCh chan<- any) any {
	if guardFn != nil && isFunc(guardFn) {
		g := sky_call2(guardFn, msg, model)
		if isErrResult(g) {
			reason := extractErrResultValue(g)
			// RecordUpdate is a no-op when the field doesn't exist on
			// the model (graceful degradation — user opts in by adding
			// notification fields). When the user model lacks both,
			// guard rejection just silently drops the msg.
			return RecordUpdate(model, map[string]any{
				"Notification":     reason,
				"NotificationType": "error",
			})
		}
	}
	return cliApplyUpdate(updateFn, msg, model, msgCh)
}

func tuiAppRun(cfg any) any {
	initFn := Field(cfg, "Init")
	updateFn := Field(cfg, "Update")
	viewFn := Field(cfg, "View")
	subsFn := Field(cfg, "Subscriptions")
	onKeyFn := Field(cfg, "OnKey") // optional — for global hotkeys
	guardFn := Field(cfg, "Guard") // optional — Msg -> Model -> Result Error ()
	if initFn == nil || updateFn == nil || viewFn == nil {
		return Err[any, any](ErrInvalidInput(
			"Tui.app: cfg must define init / update / view"))
	}

	canvas := tuiCanvas{width: 1280, height: 720}
	if cw := Field(cfg, "CanvasWidth"); cw != nil {
		if v := AsInt(cw); v > 0 {
			if v > tuiMaxCanvasWidth {
				tuiWarn("canvas", fmt.Sprintf("width capped at %d (was %d)", tuiMaxCanvasWidth, v))
				v = tuiMaxCanvasWidth
			}
			canvas.width = v
		}
	}
	if ch := Field(cfg, "CanvasHeight"); ch != nil {
		if v := AsInt(ch); v > 0 {
			if v > tuiMaxCanvasHeight {
				tuiWarn("canvas", fmt.Sprintf("height capped at %d (was %d)", tuiMaxCanvasHeight, v))
				v = tuiMaxCanvasHeight
			}
			canvas.height = v
		}
	}

	stdin := os.Stdin
	fd := int(stdin.Fd())
	if !term.IsTerminal(fd) {
		msg := "Tui.app: stdin is not a terminal — use a real TTY"
		fmt.Fprintln(os.Stderr, msg)
		return Err[any, any](ErrIo(msg))
	}
	// Refuse to enter raw mode on TERM=dumb — we'd just emit ANSI
	// codes the terminal can't interpret, leaving garbage on screen.
	// Same goes for empty TERM (some CI environments). Better to
	// fail loudly with a useful message than render incoherent output.
	if termEnv := os.Getenv("TERM"); termEnv == "dumb" || termEnv == "" {
		msg := "Tui.app: terminal does not support ANSI rendering (TERM=" + termEnv + ") — use a modern terminal emulator (TERM=xterm-256color or similar)"
		fmt.Fprintln(os.Stderr, msg)
		return Err[any, any](ErrIo(msg))
	}
	// NO_COLOR (https://no-color.org) — honour by suppressing fg/bg
	// SGR colour codes during emission. We still apply bold /
	// underline / reverse so focus + emphasis are legible. See
	// cellStyleSGR for the application.
	if os.Getenv("NO_COLOR") != "" {
		tuiNoColor = true
	} else {
		tuiNoColor = false
	}
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		msg := "Tui.app: cannot enter raw mode: " + err.Error()
		fmt.Fprintln(os.Stderr, msg)
		return Err[any, any](ErrIo(msg))
	}

	// Publish the modification state so safeGo's panic recovery and
	// the signal handler can restore the terminal from any goroutine.
	// Without this, a panic in a Cmd.perform task or a SIGTERM from
	// outside would leave the user's shell stuck in raw mode.
	state := &tuiState{fd: fd, raw: true, oldState: oldState}
	tuiInstallState(state)
	cleanShutdown := installCleanShutdown()

	defer func() {
		tuiTeardown()
		tuiUninstallState()
		close(cleanShutdown)
		// After terminal state is fully restored, surface the warning
		// summary (if any) so users know what Std.Ui features were
		// skipped under TUI rendering.
		tuiFlushWarnings()
	}()

	fmt.Print(tuiAltScreenEnter)
	state.altScreen = true
	fmt.Print(tuiHideCursor)
	state.cursorHidden = true
	// Enable SGR mouse mode (button presses + releases, no drag for v1)
	// + bracketed paste so multi-line paste arrives as a single event
	// instead of N separate Enter keystrokes.
	fmt.Print("\x1b[?1000h\x1b[?1006h")
	state.mouseEnabled = true
	fmt.Print("\x1b[?2004h")
	state.bracketedPaste = true

	msgCh := make(chan any, 32)
	doneCh := make(chan struct{})

	// Initial state.
	initRes := SkyCall(initFn, struct{}{})
	model := tupleFirst(initRes)
	if cmd := tupleSecond(initRes); cmd != nil {
		cliRunCmd(cmd, msgCh)
	}

	subMgr := newSubManager(msgCh)
	subMgr.update(subsFn, model)

	// Focus state — runtime-managed, hidden from user code.
	focusIdx := 0
	inputs := newInputRegistry()

	// First render. Track the cell grid as `prev` so subsequent renders
	// can diff against it and emit only changed cells. nil prev signals
	// "first frame, paint everything".
	cols, rows := tuiTermSize(fd)
	var prev [][]tuiCell
	scrollY := 0
	grid, focusables, contentH := renderElementFrameScroll(viewFn, model, cols, rows, canvas, focusIdx, inputs, scrollY)
	tuiPaint(paintDiff(prev, grid))
	prev = grid
	focusIdx = clampFocus(focusIdx, len(focusables))
	scrollY = ensureFocusVisible(focusables, focusIdx, scrollY, rows, contentH)

	// Key reader goroutine. Categories of keys:
	//   - Tab / Shift-Tab    → focus navigation (handled by runtime)
	//   - Enter on focused   → dispatch focused element's onClick
	//   - paste-start..end   → aggregated into a single "paste" event
	//                          so multi-line paste into a single-line
	//                          input doesn't fire N spurious submits
	//   - Anything else      → forward to user's onKey if defined
	safeGo("Tui key reader", func() {
		buf := make([]byte, 4096) // larger buffer — bracketed paste of
		// large text snippets fits in fewer reads
		var pasting bool
		var pasteBuf []rune
		for {
			n, err := stdin.Read(buf)
			if err != nil {
				close(doneCh)
				return
			}
			if n == 0 {
				continue
			}
			i := 0
			for i < n {
				ev, consumed := tuiDecodeKey(buf[i:n])
				if consumed == 0 {
					break
				}
				i += consumed
				// Bracketed paste aggregation. While in paste mode every
				// decoded char goes into pasteBuf; on paste-end we flush
				// as a single event. \r and \n inside paste become
				// literal characters (not Enter keypresses) so a
				// multi-line paste into a text input lands as text,
				// not as N submits.
				if pasting {
					if ev.kind == "paste-end" {
						pasting = false
						msg := tuiKeyMsg{ev: keyEvent{kind: "paste", value: string(pasteBuf)}}
						pasteBuf = pasteBuf[:0]
						select {
						case msgCh <- msg:
						case <-doneCh:
							return
						}
						continue
					}
					switch ev.kind {
					case "char":
						for _, r := range ev.value {
							pasteBuf = append(pasteBuf, r)
						}
					case "enter":
						pasteBuf = append(pasteBuf, '\n')
					case "tab":
						pasteBuf = append(pasteBuf, '\t')
					case "space":
						pasteBuf = append(pasteBuf, ' ')
					}
					// Cap paste size to prevent memory exhaustion from a
					// runaway paste (a malicious or accidental flood).
					if len(pasteBuf) > 1<<20 { // 1 MiB of runes
						pasting = false
						msg := tuiKeyMsg{ev: keyEvent{kind: "paste", value: string(pasteBuf)}}
						pasteBuf = pasteBuf[:0]
						select {
						case msgCh <- msg:
						case <-doneCh:
							return
						}
					}
					continue
				}
				if ev.kind == "paste-start" {
					pasting = true
					continue
				}
				select {
				case msgCh <- tuiKeyMsg{ev: ev}:
				case <-doneCh:
					return
				}
			}
		}
	})

	// SIGWINCH watcher — push a tuiResizeMsg into the same Msg pipe so
	// the main loop sees it serialised with everything else (no race
	// with in-flight key/Tick handling). On non-Unix platforms where
	// SIGWINCH isn't a thing, signal.Notify silently never fires —
	// the main loop's per-render tuiTermSize() catches resize on the
	// NEXT Msg that comes through.
	winchCh := make(chan os.Signal, 1)
	signal.Notify(winchCh, syscall.SIGWINCH)
	safeGo("SIGWINCH watcher", func() {
		for {
			select {
			case <-doneCh:
				signal.Stop(winchCh)
				return
			case <-winchCh:
				select {
				case msgCh <- tuiResizeMsg{}:
				case <-doneCh:
					return
				}
			}
		}
	})

	for {
		var msg any
		select {
		case msg = <-msgCh:
		case <-doneCh:
			subMgr.stopAll()
			return Ok[any, any](struct{}{})
		}

		// Intercept tuiResizeMsg — terminal was resized. Re-query the
		// terminal size, invalidate prev so paintDiff does a full
		// paint at the new dims, re-render. Doesn't go through the
		// user's update — pure runtime concern.
		if _, ok := msg.(tuiResizeMsg); ok {
			cols, rows = tuiTermSize(fd)
			prev = nil
			grid, focusables, contentH = renderElementFrameScroll(viewFn, model, cols, rows, canvas, focusIdx, inputs, scrollY)
			tuiPaint(paintDiff(prev, grid))
			prev = grid
			focusIdx = clampFocus(focusIdx, len(focusables))
			scrollY = ensureFocusVisible(focusables, focusIdx, scrollY, rows, contentH)
			continue
		}

		// Intercept tuiKeyMsg before dispatching to update — Tab /
		// Shift-Tab handle focus locally, Enter activates focused
		// element, mouse clicks find a focusable + activate it,
		// editing keys flow into focused inputs, anything else falls
		// through to user's onKey.
		if km, ok := msg.(tuiKeyMsg); ok {
			// Hard exit on Ctrl-C if the user hasn't wired an onKey
			// handler. Without this, an app that doesn't define
			// onKey leaves the user stuck — raw mode swallows the
			// terminal's normal SIGINT delivery, so Ctrl-C is just
			// a 0x03 byte the runtime has to act on. With onKey
			// defined, fall through and let user code handle it
			// (the focused-input editor used to swallow ctrl keys
			// before this fix; the bypass below ensures onKey
			// always sees them).
			if km.ev.kind == "ctrl" && km.ev.value == "c" && onKeyFn == nil {
				close(doneCh)
				subMgr.stopAll()
				// defer in Tui_app restores TTY + alt-screen.
				return Ok[any, any](struct{}{})
			}
			// Mouse: SGR encoded as "<button>;<col>;<row>:<M|m>".
			//
			// v0.12 surface:
			//   * Left press (button==0, isPress=true) → focus
			//     change + onClick dispatch (the v1 path).
			//   * Wheel up   (button==64) → scroll viewport up.
			//   * Wheel down (button==65) → scroll viewport down.
			//
			// Deliberately NOT yet wired:
			//   * Release events (`m` suffix) — v1 callers only care
			//     about press; release would require splitting
			//     onMouseDown / onMouseUp surface area, which we
			//     don't expose.
			//   * Drag (button>=32 with `M` suffix) — slider drag
			//     is on the roadmap; for now sliders take values
			//     via keyboard arrows.
			//   * Middle / right click (button==1 / 2) — uncommon
			//     in TUI; user `onKey` can still dispatch on them
			//     via `kind == "mouse"` if they extend this code.
			if km.ev.kind == "mouse" {
				button, col1, row1, isPress, ok := parseMouseEvent(km.ev.value)
				if !ok {
					continue
				}
				// Wheel events: SGR 1006 encodes scroll-up as
				// button 64, scroll-down as 65 (with the `M`
				// suffix; SGR doesn't emit a release for wheel).
				// Scroll the viewport by 3 lines per notch — same
				// step PgUp / PgDn use, feels natural with a
				// trackpad's two-finger scroll.
				if isPress && (button == 64 || button == 65) {
					step := 3
					if button == 64 {
						scrollY = max(0, scrollY-step)
					} else {
						scrollY = min(max(0, contentH-rows), scrollY+step)
					}
					grid, focusables, contentH = renderElementFrameScroll(viewFn, model, cols, rows, canvas, focusIdx, inputs, scrollY)
					tuiPaint(paintDiff(prev, grid))
					prev = grid
					continue
				}
				if isPress && button == 0 {
					if hit := hitTestFocusables(focusables, col1-1, row1-1); hit >= 0 {
						oldFocus := focusIdx
						focusIdx = hit
						if oldFocus != focusIdx {
							tuiDispatchFocusChange(focusables, oldFocus, focusIdx, msgCh)
						}
						if !focusables[hit].isInput {
							if clickEvt := focusableEvent(focusables[hit], "click"); clickEvt != nil {
								if clickMsg := tuiExtractClickMsg(clickEvt); clickMsg != nil {
									msg = clickMsg
									goto applyMsg
								}
							}
						}
						// Either focus-changed or input-clicked: re-render.
						grid, focusables, contentH = renderElementFrameScroll(viewFn, model, cols, rows, canvas, focusIdx, inputs, scrollY)
						scrollY = ensureFocusVisible(focusables, focusIdx, scrollY, rows, contentH)
						grid, focusables, contentH = renderElementFrameScroll(viewFn, model, cols, rows, canvas, focusIdx, inputs, scrollY)
						tuiPaint(paintDiff(prev, grid))
						prev = grid
					}
				}
				continue
			}

			// Tab navigation always handled locally. Arrow keys on a
			// non-input focus also navigate the focus order — Down is
			// Tab semantics, Up is Shift-Tab. This makes the keyboard
			// feel native: you can walk the focusables with the arrow
			// keys without first reaching for Tab. ensureFocusVisible
			// (called below) auto-scrolls to keep the new focus on
			// screen, so a long page just scrolls naturally as you
			// navigate. PgUp / PgDn / Home / End remain pure viewport
			// scrolls (handled by the viewport block below) for
			// reading content with no focusable in reach.
			//
			// On an input we let arrows fall through to the editor
			// (cursor movement, multi-line up/down) — focus only
			// changes via Tab / Shift-Tab there.
			focusedInputForArrow := focusIdx >= 0 && focusIdx < len(focusables) && focusables[focusIdx].isInput
			handled := false
			oldFocus := focusIdx
			switch km.ev.kind {
			case "tab":
				if len(focusables) > 0 {
					focusIdx = (focusIdx + 1) % len(focusables)
					handled = true
				}
			case "down":
				if !focusedInputForArrow && len(focusables) > 0 {
					focusIdx = (focusIdx + 1) % len(focusables)
					handled = true
				}
			case "up":
				if !focusedInputForArrow && len(focusables) > 0 {
					focusIdx = (focusIdx - 1 + len(focusables)) % len(focusables)
					handled = true
				}
			case "other":
				if km.ev.value == "\x1b[Z" { // Shift-Tab
					if len(focusables) > 0 {
						focusIdx = (focusIdx - 1 + len(focusables)) % len(focusables)
						handled = true
					}
				}
			}
			if handled && oldFocus != focusIdx {
				tuiDispatchFocusChange(focusables, oldFocus, focusIdx, msgCh)
			}
			if handled {
				grid, focusables, contentH = renderElementFrameScroll(viewFn, model, cols, rows, canvas, focusIdx, inputs, scrollY)
				focusIdx = clampFocus(focusIdx, len(focusables))
				scrollY = ensureFocusVisible(focusables, focusIdx, scrollY, rows, contentH)
				grid, focusables, contentH = renderElementFrameScroll(viewFn, model, cols, rows, canvas, focusIdx, inputs, scrollY)
				tuiPaint(paintDiff(prev, grid))
				prev = grid
				continue
			}

			// Viewport scroll keys when focus is NOT on an input. Lets
			// users navigate content taller than the terminal viewport
			// (the kitchen sink hits this — many sections, mosh / SSH
			// has no native scrollback). Up / Down move by one row,
			// PgUp / PgDn by a viewport, Home / End jump to extremes.
			focusedInput := focusIdx >= 0 && focusIdx < len(focusables) && focusables[focusIdx].isInput
			if !focusedInput {
				maxScroll := contentH - rows
				if maxScroll < 0 {
					maxScroll = 0
				}
				scrolled := false
				switch km.ev.kind {
				case "up":
					if scrollY > 0 {
						scrollY--
						scrolled = true
					}
				case "down":
					if scrollY < maxScroll {
						scrollY++
						scrolled = true
					}
				case "pageup":
					scrollY -= rows
					if scrollY < 0 {
						scrollY = 0
					}
					scrolled = true
				case "pagedown":
					scrollY += rows
					if scrollY > maxScroll {
						scrollY = maxScroll
					}
					scrolled = true
				case "home":
					if scrollY != 0 {
						scrollY = 0
						scrolled = true
					}
				case "end":
					if scrollY != maxScroll {
						scrollY = maxScroll
						scrolled = true
					}
				}
				if scrolled {
					grid, focusables, contentH = renderElementFrameScroll(viewFn, model, cols, rows, canvas, focusIdx, inputs, scrollY)
					tuiPaint(paintDiff(prev, grid))
					prev = grid
					continue
				}
			}

			// Focused-input editor path. Checkbox / radio respond to
			// Space and Enter by firing their onClick (same Msg the
			// HTML side dispatches when the box is clicked) instead
			// of acting as a text editor.
			//
			// Ctrl-<letter> events bypass the editor entirely so an
			// app's global hotkeys (Ctrl-C / Ctrl-D / Ctrl-Q to quit,
			// Ctrl-S to save, etc.) reach the user's onKey even
			// when an input has focus. Without this bypass,
			// tuiEditInput's switch silently swallows ctrl events
			// and apps look "frozen" while editing a field.
			if km.ev.kind != "ctrl" && focusIdx >= 0 && focusIdx < len(focusables) && focusables[focusIdx].isInput {
				if isCheckboxOrRadio(focusables[focusIdx]) && (km.ev.kind == "space" || km.ev.kind == "enter") {
					if clickEvt := focusableEvent(focusables[focusIdx], "click"); clickEvt != nil {
						if clickMsg := tuiExtractClickMsg(clickEvt); clickMsg != nil {
							msg = clickMsg
							goto applyMsg
						}
					}
					continue
				}
				st := inputs.get(focusIdx)
				editorChanged, dispatchMsg := tuiEditInput(st, km.ev, focusables[focusIdx])
				if dispatchMsg != nil {
					msg = dispatchMsg
					goto applyMsg
				}
				if editorChanged {
					// Sync lastValueAttr so the next render's "did
					// the model reset?" check doesn't undo the edit.
					st.lastValueAttr = st.buffer
					grid, focusables, contentH = renderElementFrameScroll(viewFn, model, cols, rows, canvas, focusIdx, inputs, scrollY)
					tuiPaint(paintDiff(prev, grid))
					prev = grid
					continue
				}
				// Editor swallowed but didn't change state — drop the key.
				continue
			}

			// Enter or Space on a focusable button activates its
			// onClick. Matches the browser convention (<button>
			// activates on both keys) and prevents a global "space →
			// toggle" hotkey from firing the wrong msg when focus is
			// on a different button — the keypress is consumed here
			// before reaching the user's onKey.
			if (km.ev.kind == "enter" || km.ev.kind == "space") && focusIdx >= 0 && focusIdx < len(focusables) {
				if clickEvt := focusableEvent(focusables[focusIdx], "click"); clickEvt != nil {
					if clickMsg := tuiExtractClickMsg(clickEvt); clickMsg != nil {
						msg = clickMsg
						goto applyMsg
					}
				}
			}

			// Otherwise, forward to user's onKey if any.
			if onKeyFn != nil {
				key := tuiKeyToSky(onKeyFn, km.ev)
				if key != nil {
					if userMsg := SkyCall(onKeyFn, key); userMsg != nil {
						msg = userMsg
						goto applyMsg
					}
				}
			}
			continue
		}

	applyMsg:
		model = tuiApplyUpdate(guardFn, updateFn, msg, model, msgCh)
		subMgr.update(subsFn, model)

		// On resize, recompute terminal dims; the grid-size mismatch
		// against prev forces a full repaint inside paintDiff.
		newCols, newRows := tuiTermSize(fd)
		if newCols != cols || newRows != rows {
			cols, rows = newCols, newRows
			prev = nil // trigger full repaint
		}

		grid, focusables, contentH = renderElementFrameScroll(viewFn, model, cols, rows, canvas, focusIdx, inputs, scrollY)
		focusIdx = clampFocus(focusIdx, len(focusables))
		scrollY = ensureFocusVisible(focusables, focusIdx, scrollY, rows, contentH)
		grid, focusables, contentH = renderElementFrameScroll(viewFn, model, cols, rows, canvas, focusIdx, inputs, scrollY)
		tuiPaint(paintDiff(prev, grid))
		prev = grid
	}
}


// ensureFocusVisible adjusts scrollY so the focused element is within
// the visible viewport [scrollY .. scrollY+rows). Called after Tab /
// Shift-Tab so the user sees the just-focused element even when it
// was below the fold; also after focus-changes from mouse clicks.
//
// Returns the (possibly clamped) new scrollY. Doesn't move the
// viewport when the focused element is already in view — preserves
// the user's manual scroll position if they were already looking at
// the right area.
func ensureFocusVisible(focusables []focusable, focusIdx, scrollY, rows, contentH int) int {
	if focusIdx < 0 || focusIdx >= len(focusables) {
		return scrollY
	}
	maxScroll := contentH - rows
	if maxScroll < 0 {
		maxScroll = 0
	}
	f := focusables[focusIdx]
	top := f.row
	bottom := f.row + f.h - 1
	if top < scrollY {
		scrollY = top
	} else if bottom >= scrollY+rows {
		// Snap so bottom of element is at the bottom row of viewport,
		// with a small padding so it's not literally clipped at the
		// edge.
		scrollY = bottom - rows + 1
	}
	if scrollY < 0 {
		scrollY = 0
	}
	if scrollY > maxScroll {
		scrollY = maxScroll
	}
	return scrollY
}

// tuiKeyMsg is a private message type the runtime uses to ferry
// keypresses from the reader goroutine to the main loop. User code
// never sees this.
type tuiKeyMsg struct {
	ev keyEvent
}

// tuiResizeMsg signals that the terminal was resized (SIGWINCH). The
// main loop responds by re-querying terminal dims, invalidating prev
// (full repaint), and re-rendering.
type tuiResizeMsg struct{}

func clampFocus(idx, n int) int {
	if n == 0 {
		return 0
	}
	if idx < 0 {
		return 0
	}
	if idx >= n {
		return n - 1
	}
	return idx
}

func tuiTermSize(fd int) (int, int) {
	w, h, err := term.GetSize(fd)
	if err != nil || w <= 0 || h <= 0 {
		return 80, 24
	}
	return w, h
}

// parseMouseEvent decodes a mouse keyEvent.value of the form
// "<button>;<col>;<row>:<M|m>" (set by tuiDecodeKey). Returns
// (button, col1based, row1based, isPress, ok).
func parseMouseEvent(s string) (int, int, int, bool, bool) {
	// Split on ":" — the trailing ":M" or ":m" tells us press/release.
	last := strings.LastIndex(s, ":")
	if last < 0 || last == len(s)-1 {
		return 0, 0, 0, false, false
	}
	suffix := s[last+1:]
	body := s[:last]
	parts := strings.Split(body, ";")
	if len(parts) != 3 {
		return 0, 0, 0, false, false
	}
	var bn, cn, rn int
	if _, err := fmt.Sscanf(parts[0], "%d", &bn); err != nil {
		return 0, 0, 0, false, false
	}
	if _, err := fmt.Sscanf(parts[1], "%d", &cn); err != nil {
		return 0, 0, 0, false, false
	}
	if _, err := fmt.Sscanf(parts[2], "%d", &rn); err != nil {
		return 0, 0, 0, false, false
	}
	return bn, cn, rn, suffix == "M", true
}

// hitTestFocusables returns the index of the topmost focusable whose
// bounding box contains (col, row) — both 0-based. Returns -1 if no
// focusable is hit.
//
// "Topmost" = last in tab order, on the assumption that later-rendered
// focusables overlay earlier ones in nested layouts. For flat layouts
// (most cases) only one focusable contains a given cell, so the order
// doesn't matter.
func hitTestFocusables(focusables []focusable, col, row int) int {
	for i := len(focusables) - 1; i >= 0; i-- {
		f := focusables[i]
		if col >= f.col && col < f.col+f.w && row >= f.row && row < f.row+f.h {
			return i
		}
	}
	return -1
}

// tuiDispatchFocusChange fires onBlur for the old focused element + onFocus
// for the new one (when those events are bound). Both Msgs land on msgCh
// so they flow through the same update sequence as everything else.
func tuiDispatchFocusChange(focusables []focusable, oldIdx, newIdx int, msgCh chan<- any) {
	if oldIdx == newIdx {
		return
	}
	if oldIdx >= 0 && oldIdx < len(focusables) {
		if blurEvt := focusableEvent(focusables[oldIdx], "blur"); blurEvt != nil {
			if msg := tuiExtractClickMsg(blurEvt); msg != nil {
				select {
				case msgCh <- msg:
				default: // channel full — drop rather than block the focus path
				}
			}
		}
	}
	if newIdx >= 0 && newIdx < len(focusables) {
		if focusEvt := focusableEvent(focusables[newIdx], "focus"); focusEvt != nil {
			if msg := tuiExtractClickMsg(focusEvt); msg != nil {
				select {
				case msgCh <- msg:
				default:
				}
			}
		}
	}
}

// focusableEvent returns the eventPair on `f` matching the given name
// ("click", "input", "change", ...). Sky.Live's Event_onClick et al.
// produce eventPair{name, msg} values; we filter the focusable's
// events list by name for activation routing.
func focusableEvent(f focusable, name string) any {
	for _, ev := range f.events {
		if ep, ok := ev.(eventPair); ok && ep.name == name {
			return ev
		}
	}
	return nil
}

// tuiEditInput applies a key event to an input's editor state. Returns
// (editorChanged, dispatchMsg). If editorChanged, the runtime should
// re-render and (if there's an onInput handler) the dispatchMsg will
// be set to a Msg representing "user typed; new buffer is X". If the
// key is Enter and there's an onChange handler, dispatchMsg fires that.
//
// v1 supports: chars (insert at cursor), backspace (delete left),
// delete (delete right), Enter (fire onChange). Cursor movement
// (Left/Right/Home/End) lands in C3 with extended keys.
// isSpaceRune classifies a rune as a "word boundary" for cursor
// word-jump (Ctrl-Left / Ctrl-Right). Includes whitespace and the
// common punctuation that splits words in editors. Wide / CJK runes
// are NOT word-boundaries — they belong to the same word as the
// surrounding text in the absence of an explicit space.
func isSpaceRune(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r', '.', ',', ';', ':', '!', '?',
		'(', ')', '[', ']', '{', '}', '<', '>', '/', '\\', '|',
		'"', '\'', '`', '@', '#', '$', '%', '^', '&', '*', '+', '=', '-':
		return true
	}
	return false
}

func tuiEditInput(st *tuiInput, ev keyEvent, f focusable) (bool, any) {
	runes := []rune(st.buffer)
	changed := false
	switch ev.kind {
	case "char":
		// Insert the (possibly multi-byte) char at cursor. ev.value is
		// already a single grapheme as decoded by tuiDecodeKey.
		ins := []rune(ev.value)
		newRunes := make([]rune, 0, len(runes)+len(ins))
		newRunes = append(newRunes, runes[:st.cursor]...)
		newRunes = append(newRunes, ins...)
		newRunes = append(newRunes, runes[st.cursor:]...)
		st.buffer = string(newRunes)
		st.cursor += len(ins)
		changed = true
	case "space":
		newRunes := make([]rune, 0, len(runes)+1)
		newRunes = append(newRunes, runes[:st.cursor]...)
		newRunes = append(newRunes, ' ')
		newRunes = append(newRunes, runes[st.cursor:]...)
		st.buffer = string(newRunes)
		st.cursor++
		changed = true
	case "paste":
		// Bracketed-paste payload — insert the entire buffer at the
		// cursor as one operation. For single-line inputs we strip
		// embedded newlines so a paste of "user@example.com\n" doesn't
		// fire a phantom Enter (= submit) at the end. For multi-line
		// inputs (textarea) the newlines are preserved as line breaks.
		body := ev.value
		if !isMultilineInput(f) {
			// Replace \r\n and \n with space so the paste stays on
			// one line. Tab also becomes space — single-line inputs
			// shouldn't render tab anyway.
			body = strings.ReplaceAll(body, "\r\n", " ")
			body = strings.ReplaceAll(body, "\n", " ")
			body = strings.ReplaceAll(body, "\t", " ")
		} else {
			body = strings.ReplaceAll(body, "\r\n", "\n")
		}
		// Sanitise control bytes — paste content is untrusted.
		body = sanitiseString(body)
		ins := []rune(body)
		newRunes := make([]rune, 0, len(runes)+len(ins))
		newRunes = append(newRunes, runes[:st.cursor]...)
		newRunes = append(newRunes, ins...)
		newRunes = append(newRunes, runes[st.cursor:]...)
		st.buffer = string(newRunes)
		st.cursor += len(ins)
		changed = true
	case "backspace":
		if st.cursor > 0 {
			newRunes := make([]rune, 0, len(runes)-1)
			newRunes = append(newRunes, runes[:st.cursor-1]...)
			newRunes = append(newRunes, runes[st.cursor:]...)
			st.buffer = string(newRunes)
			st.cursor--
			changed = true
		}
	case "delete":
		if st.cursor < len(runes) {
			newRunes := make([]rune, 0, len(runes)-1)
			newRunes = append(newRunes, runes[:st.cursor]...)
			newRunes = append(newRunes, runes[st.cursor+1:]...)
			st.buffer = string(newRunes)
			changed = true
		}
	case "left":
		if ev.ctrl {
			// Word jump: skip back over whitespace then back over a
			// run of non-whitespace, landing the cursor at the start
			// of the word to the left.
			pos := st.cursor
			for pos > 0 && isSpaceRune(runes[pos-1]) {
				pos--
			}
			for pos > 0 && !isSpaceRune(runes[pos-1]) {
				pos--
			}
			if pos != st.cursor {
				st.cursor = pos
				return true, nil
			}
		} else if st.cursor > 0 {
			st.cursor--
			return true, nil // cursor-only change; re-render but no Msg
		}
	case "right":
		if ev.ctrl {
			// Word jump forward: skip current word, then skip
			// whitespace, landing at start of next word.
			pos := st.cursor
			for pos < len(runes) && !isSpaceRune(runes[pos]) {
				pos++
			}
			for pos < len(runes) && isSpaceRune(runes[pos]) {
				pos++
			}
			if pos != st.cursor {
				st.cursor = pos
				return true, nil
			}
		} else if st.cursor < len(runes) {
			st.cursor++
			return true, nil
		}
	case "home":
		if st.cursor != 0 {
			st.cursor = 0
			return true, nil
		}
	case "end":
		if st.cursor != len(runes) {
			st.cursor = len(runes)
			return true, nil
		}
	case "enter":
		if isMultilineInput(f) {
			// Insert newline at cursor.
			runes := []rune(st.buffer)
			newRunes := make([]rune, 0, len(runes)+1)
			newRunes = append(newRunes, runes[:st.cursor]...)
			newRunes = append(newRunes, '\n')
			newRunes = append(newRunes, runes[st.cursor:]...)
			st.buffer = string(newRunes)
			st.cursor++
			changed = true
		} else {
			// Fire onChange (and any "submit" event the form bound).
			if changeEvt := focusableEvent(f, "change"); changeEvt != nil {
				if msg := tuiExtractInputMsg(changeEvt, st.buffer); msg != nil {
					return false, msg
				}
			}
			return false, nil
		}
	case "up":
		// Multiline: move cursor up one line preserving column.
		if !isMultilineInput(f) {
			return false, nil
		}
		runes := []rune(st.buffer)
		line, col := cursorLocate(st.buffer, st.cursor)
		if line == 0 {
			return false, nil
		}
		// Find line start of previous line.
		prevStart := 0
		curLine := 0
		for i := 0; i < len(runes); i++ {
			if curLine == line-1 {
				prevStart = i
				break
			}
			if runes[i] == '\n' {
				curLine++
				prevStart = i + 1
			}
		}
		// Find length of previous line.
		prevEnd := prevStart
		for prevEnd < len(runes) && runes[prevEnd] != '\n' {
			prevEnd++
		}
		newCol := col
		if newCol > prevEnd-prevStart {
			newCol = prevEnd - prevStart
		}
		st.cursor = prevStart + newCol
		return true, nil
	case "down":
		if !isMultilineInput(f) {
			return false, nil
		}
		runes := []rune(st.buffer)
		_, col := cursorLocate(st.buffer, st.cursor)
		// Find next line's start.
		i := st.cursor
		for i < len(runes) && runes[i] != '\n' {
			i++
		}
		if i >= len(runes) {
			return false, nil // already on last line
		}
		nextStart := i + 1
		nextEnd := nextStart
		for nextEnd < len(runes) && runes[nextEnd] != '\n' {
			nextEnd++
		}
		newCol := col
		if newCol > nextEnd-nextStart {
			newCol = nextEnd - nextStart
		}
		st.cursor = nextStart + newCol
		return true, nil
	}
	if !changed {
		return false, nil
	}
	// Sync lastValueAttr to the new buffer so the next render's
	// "did the model reset?" check (in paintInputBufferAdvanced)
	// doesn't undo the local edit by snapping the cursor to the end
	// of buffer. Without this sync, mid-string edits (eg typing
	// between two existing lines of a multiline input) get
	// reset every keystroke because the dispatch Msg path bypasses
	// the post-edit sync that the editorChanged-no-dispatch path
	// already does.
	st.lastValueAttr = st.buffer
	// Dispatch onInput Msg with the new buffer.
	if inputEvt := focusableEvent(f, "input"); inputEvt != nil {
		if msg := tuiExtractInputMsg(inputEvt, st.buffer); msg != nil {
			return true, msg
		}
	}
	return true, nil
}

// tuiExtractInputMsg unwraps an eventPair{name, msg} where msg is a
// Sky `String -> Msg` constructor, and applies it to the new buffer
// string to produce the actual Msg to dispatch.
func tuiExtractInputMsg(evt any, buffer string) any {
	ep, ok := evt.(eventPair)
	if !ok {
		return nil
	}
	if ep.msg == nil {
		return nil
	}
	// onInput / onChange in Std.Live.Events take String -> Msg, so
	// applying the captured fn to the buffer gives us the user's Msg.
	return sky_call(ep.msg, buffer)
}

// tuiExtractClickMsg pulls the Msg out of a Std.Live.Events event
// value. Event_onClick returns an `eventPair{name, msg}` (see live.go);
// we just read its msg field. We also tolerate tuple-shaped values for
// forward compatibility with future event payload shapes.
func tuiExtractClickMsg(evt any) any {
	if evt == nil {
		return nil
	}
	if ep, ok := evt.(eventPair); ok {
		return ep.msg
	}
	if t, ok := evt.(SkyTuple2); ok {
		return t.V1
	}
	if pair, ok := evt.([]any); ok && len(pair) == 2 {
		return pair[1]
	}
	return nil
}

// ─── Rendering ───────────────────────────────────────────────────────

// renderElementFrame is the top-level render. Walks the Element ADT,
// computes layout for the available terminal size + logical canvas,
// produces a 2D cell grid + focusable list (in tab order). The input
// registry persists across renders so editor state (buffer + cursor)
// survives even when the model doesn't carry it back.
//
// Returns the grid (not yet ANSI-encoded) so the caller can diff
// against the previous frame and emit only changed cells. See
// paintDiff for the minimal-write emission.
func renderElementFrame(viewFn, model any, cols, rows int, canvas tuiCanvas, focusIdx int, inputs *inputRegistry) ([][]tuiCell, []focusable) {
	grid, focusables, _ := renderElementFrameScroll(viewFn, model, cols, rows, canvas, focusIdx, inputs, 0)
	return grid, focusables
}

// renderElementFrameScroll lays out the view at its natural full height
// (uncapped by terminal rows), paints the full content into a virtual
// grid, then returns the windowed slice [scrollY .. scrollY+rows]. Lets
// the user scroll content taller than the terminal viewport via
// Up/Down/PgUp/PgDn arrow keys when no input has focus.
//
// The third return value (contentH) is the total laid-out height in
// terminal-cell rows; the caller uses it to clamp scrollY to
// [0, max(0, contentH-rows)].
func renderElementFrameScroll(viewFn, model any, cols, rows int, canvas tuiCanvas, focusIdx int, inputs *inputRegistry, scrollY int) ([][]tuiCell, []focusable, int) {
	elem := SkyCall(viewFn, model)
	pxPerCellX := float64(canvas.width) / float64(cols)
	pxPerCellY := float64(canvas.height) / float64(rows)
	if pxPerCellX <= 0 {
		pxPerCellX = 1
	}
	if pxPerCellY <= 0 {
		pxPerCellY = 1
	}
	ctx := tuiLayoutCtx{
		cols:       cols,
		rows:       rows,
		pxPerCellX: pxPerCellX,
		pxPerCellY: pxPerCellY,
	}
	// Layout with generous maxH so content can grow taller than the
	// terminal viewport. We discover the actual content height via
	// the root box's height field afterwards.
	//
	// Hard cap on generousH AND the post-layout contentH protects
	// the host from `List.repeat 1_000_000 _ |> Ui.column` style
	// misuse — without a cap, a runaway view allocates a 1000×N
	// cell grid with no upper bound. tuiMaxContentH = 50,000 rows
	// is ~10× more than any realistic user-facing screen and still
	// caps the worst-case cell-grid allocation at ~1 GB. Beyond this
	// the grid is truncated and a once-per-session warning surfaces
	// on exit so the developer knows their view is over-tall.
	box := layoutElement(elem, ctx, cols, tuiMaxContentH, layoutAxisColumn)
	contentH := box.height
	if contentH < rows {
		contentH = rows
	}
	if contentH > tuiMaxContentH {
		tuiWarn("layout", fmt.Sprintf("view height capped at %d rows (was %d)", tuiMaxContentH, contentH))
		contentH = tuiMaxContentH
	} else if contentH > tuiSoftWarnH {
		tuiWarn("layout", fmt.Sprintf("very tall view: %d rows (consider Std.Ui.Lazy / pagination)", contentH))
	}
	fullGrid := newCellGrid(cols, contentH)
	var focusables []focusable
	paintBox(fullGrid, box, 0, 0, cols, contentH, focusIdx, &focusables, inputs, textStyle{}, layoutAxisColumn, 0)

	// Window the full grid down to the visible viewport. When
	// scrollY+rows > contentH the trailing rows are blank.
	if scrollY < 0 {
		scrollY = 0
	}
	max := contentH - rows
	if max < 0 {
		max = 0
	}
	if scrollY > max {
		scrollY = max
	}
	visible := newCellGrid(cols, rows)
	for r := 0; r < rows && r+scrollY < contentH; r++ {
		copy(visible[r], fullGrid[r+scrollY])
	}
	return visible, focusables, contentH
}

type tuiLayoutCtx struct {
	cols, rows             int
	pxPerCellX, pxPerCellY float64
}

type layoutAxis int

const (
	layoutAxisColumn layoutAxis = iota
	layoutAxisRow
)

// layoutBox is the result of measuring an Element. It carries enough
// information for the paint pass to actually emit cells.
type layoutBox struct {
	kind        string // "empty" | "text" | "node"
	text        string // for "text"
	tag         string // for tagged nodes ("h1", "button", "a", "input"…) — empty for default
	width       int
	height      int
	axis        layoutAxis // for "node" — children are laid out in this direction
	padding     [4]int     // top, right, bottom, left in cells
	spacing     int        // cells between siblings
	fg, bg      tuiColor
	bold        bool
	italic      bool
	underline   bool
	strike      bool
	overline    bool
	textAlign   string // "left" | "center" | "right" — text painting alignment
	alignX      string
	alignY      string
	events      []any  // for focusables: all AttrEvent payloads
	valueAttr   string // for inputs: initial value from AttrAttribute "value"
	placeholder string // for inputs: shown when buffer is empty
	nameAttr    string // for inputs in forms: form field name
	inputType   string // "text" | "password" | "checkbox" | "radio" | "range" | "textarea" | …
	children    []layoutBox
	wrapped     bool   // wrappedRow flag — children break into rows
	paragraph   bool   // paragraph flag — text children word-wrap
	textColumn  bool   // textColumn flag — reading-width column
	gridLayout  bool   // grid flag — children flow into auto NxM grid
	gridColumns int    // columns for grid (0 = auto)
	clip        [2]bool // [clipX, clipY]
	overflow    [2]string // [x, y] — "clip", "scrollbars", ""
	nearby      []nearbyEntry
	borderWidth [4]int // top, right, bottom, left — 1 cell each if border present
	borderColor tuiColor
	borderStyle string // "solid" | "dashed" | "dotted"
}

// layoutElement walks one Element node + computes its box for the
// given parent constraints. Recursive.
//
//   maxW, maxH: parent-imposed upper bounds in cells
//   parentAxis: how this element is being laid out by its parent
func layoutElement(elem any, ctx tuiLayoutCtx, maxW, maxH int, parentAxis layoutAxis) layoutBox {
	adt, ok := elem.(SkyADT)
	if !ok {
		return layoutBox{kind: "empty"}
	}
	switch adt.Tag {
	case 0: // Empty
		return layoutBox{kind: "empty"}
	case 1: // Text s
		s := ""
		if len(adt.Fields) > 0 {
			if str, ok := adt.Fields[0].(string); ok {
				s = str
			}
		}
		return layoutBox{kind: "text", text: s, width: runeLen(s), height: 1}
	case 2: // Node desc attrs children
		return layoutNode("", adt.Fields, ctx, maxW, maxH, parentAxis)
	case 3: // TaggedNode tag desc attrs children
		tag := ""
		if len(adt.Fields) > 0 {
			if s, ok := adt.Fields[0].(string); ok {
				tag = s
			}
		}
		// Skip first field (tag); the rest mirror Node's layout.
		return layoutNode(tag, adt.Fields[1:], ctx, maxW, maxH, parentAxis)
	case 4: // Raw _
		return layoutBox{kind: "text", text: "[raw]", width: 5, height: 1}
	}
	return layoutBox{kind: "empty"}
}

// layoutNode handles both Node and TaggedNode (after stripping the tag).
// Fields layout: [desc, attrsList, childrenList]
func layoutNode(tag string, fields []any, ctx tuiLayoutCtx, maxW, maxH int, parentAxis layoutAxis) layoutBox {
	if len(fields) < 3 {
		return layoutBox{kind: "empty"}
	}
	attrsList := asList(fields[1])
	childrenList := asList(fields[2])

	// Walk attrs to extract layout-relevant values.
	la := walkAttrs(attrsList, ctx)

	// Determine axis (row vs column from sentinel attrs).
	axis := layoutAxisColumn
	if la.isRow {
		axis = layoutAxisRow
	}

	// Apply tag-specific styling defaults. Headings get bold + a
	// trailing underline row in the paint pass; the height bump here
	// reserves space for the underline.
	headingUnderline := false
	switch tag {
	case "h1":
		la.bold = true
		headingUnderline = true
	case "h2":
		la.bold = true
		headingUnderline = true
	case "h3", "h4", "h5", "h6":
		la.bold = true
	}

	// Compute available space inside padding + border. Border eats
	// 1 cell per side that has it (TUI cells are atomic; CSS Npx
	// becomes a single Unicode box-drawing cell).
	innerMaxW := maxW - la.padding[1] - la.padding[3] - la.borderWidth[1] - la.borderWidth[3]
	innerMaxH := maxH - la.padding[0] - la.padding[2] - la.borderWidth[0] - la.borderWidth[2]
	if innerMaxW < 0 {
		innerMaxW = 0
	}
	if innerMaxH < 0 {
		innerMaxH = 0
	}

	// Resolve explicit width/height first.
	width, hasExplicitW := resolveLengthCells(la.width, "x", innerMaxW, ctx)
	height, hasExplicitH := resolveLengthCells(la.height, "y", innerMaxH, ctx)
	if !hasExplicitW {
		width = innerMaxW
	}
	if !hasExplicitH {
		height = innerMaxH
	}

	// Paragraph / textColumn: collapse children's text content into
	// word-wrapped text lines fitting `width`. v1 simplification: we
	// flatten all child text into a single buffer, lose inline styled
	// spans (each child becomes plain text). Inline styling is a
	// future polish pass.
	if la.isParagraph || la.isTextColumn {
		// Determine wrap width — fall back to innerMaxW when width is
		// unspecified at this node.
		wrapW := width
		if !hasExplicitW || wrapW <= 0 {
			wrapW = innerMaxW
		}
		if wrapW <= 0 {
			wrapW = ctx.cols
		}
		texts := []string{}
		for _, c := range childrenList {
			texts = append(texts, extractTextContent(c))
		}
		joined := strings.Join(texts, " ")
		// textColumn handles paragraph BOUNDARIES — each child is a
		// separate paragraph (own line break). For paragraph itself,
		// we wrap the joined text continuously.
		var lines []string
		if la.isTextColumn {
			for i, t := range texts {
				if i > 0 {
					lines = append(lines, "")
				}
				lines = append(lines, wrapText(t, wrapW)...)
			}
		} else {
			lines = wrapText(joined, wrapW)
		}
		// Replace children with one Text box per line.
		var paraBoxes []layoutBox
		for _, line := range lines {
			paraBoxes = append(paraBoxes, layoutBox{kind: "text", text: line, width: runeLen(line), height: 1})
		}
		childBoxes := paraBoxes
		// Force vertical stack inside a paragraph/textColumn.
		axis = layoutAxisColumn
		// Box dimensions: width = wrapW (or content), height = line count.
		finalW := wrapW + la.padding[1] + la.padding[3] + la.borderWidth[1] + la.borderWidth[3]
		finalH := len(lines) + la.padding[0] + la.padding[2] + la.borderWidth[0] + la.borderWidth[2]
		if finalW > maxW {
			finalW = maxW
		}
		if finalH > maxH {
			finalH = maxH
		}
		return layoutBox{
			kind:        "node",
			tag:         tag,
			width:       finalW,
			height:      finalH,
			axis:        axis,
			padding:     la.padding,
			spacing:     0, // line spacing handled by stacking text boxes directly
			fg:          la.fg,
			bg:          la.bg,
			bold:        la.bold,
			italic:      la.italic,
			underline:   la.underline,
			strike:      la.strike,
			overline:    la.overline,
			textAlign:   la.textAlign,
			alignX:      la.alignX,
			alignY:      la.alignY,
			events:      la.events,
			nameAttr:    la.nameAttr,
		inputType:   la.inputType,
			paragraph:   la.isParagraph,
			textColumn:  la.isTextColumn,
			clip:        la.clip,
			overflow:    la.overflow,
			nearby:      la.nearby,
			children:    childBoxes,
			borderWidth: la.borderWidth,
			borderColor: la.borderColor,
			borderStyle: la.borderStyle,
		}
	}

	// Grid layout: distribute children into auto-flow columns based
	// on minColumnPx (gridColumns attr → __gridMin). Children flow
	// row-major; each row's height is the max of its children.
	if la.isGrid {
		minColCells := pxToCellsX(la.gridColumns, ctx)
		if minColCells <= 0 {
			minColCells = 10 // sensible default if user didn't specify
		}
		availW := innerMaxW
		if hasExplicitW && width > 0 {
			availW = width
		}
		numCols := availW / minColCells
		if numCols < 1 {
			numCols = 1
		}
		colWidth := availW / numCols
		// Lay out each child within colWidth.
		var childBoxes []layoutBox
		for _, c := range childrenList {
			cb := layoutElement(c, ctx, colWidth, innerMaxH, layoutAxisColumn)
			cb.width = colWidth
			childBoxes = append(childBoxes, cb)
		}
		// Compute total height: sum of row max heights + spacing.
		nRows := (len(childBoxes) + numCols - 1) / numCols
		rowHeights := make([]int, nRows)
		for i, c := range childBoxes {
			r := i / numCols
			if c.height > rowHeights[r] {
				rowHeights[r] = c.height
			}
		}
		totalH := 0
		for _, rh := range rowHeights {
			totalH += rh
		}
		if nRows > 1 {
			totalH += la.spacing * (nRows - 1)
		}
		finalW := availW + la.padding[1] + la.padding[3] + la.borderWidth[1] + la.borderWidth[3]
		finalH := totalH + la.padding[0] + la.padding[2] + la.borderWidth[0] + la.borderWidth[2]
		if finalW > maxW {
			finalW = maxW
		}
		if finalH > maxH {
			finalH = maxH
		}
		return layoutBox{
			kind:        "node",
			tag:         tag,
			width:       finalW,
			height:      finalH,
			padding:     la.padding,
			spacing:     la.spacing,
			fg:          la.fg,
			bg:          la.bg,
			bold:        la.bold,
			italic:      la.italic,
			underline:   la.underline,
			strike:      la.strike,
			overline:    la.overline,
			textAlign:   la.textAlign,
			alignX:      la.alignX,
			alignY:      la.alignY,
			events:      la.events,
			nameAttr:    la.nameAttr,
		inputType:   la.inputType,
			gridLayout:  true,
			gridColumns: numCols,
			clip:        la.clip,
			overflow:    la.overflow,
			nearby:      la.nearby,
			children:    childBoxes,
			borderWidth: la.borderWidth,
			borderColor: la.borderColor,
			borderStyle: la.borderStyle,
		}
	}

	// Lay out children.
	childBoxes := layoutChildren(childrenList, ctx, width, height, axis, la.spacing)

	// If width or height was unspecified (Content-like fallback), shrink
	// to children's intrinsic size.
	if !hasExplicitW {
		intrinsic := 0
		if axis == layoutAxisRow {
			for i, c := range childBoxes {
				intrinsic += c.width
				if i > 0 {
					intrinsic += la.spacing
				}
			}
		} else {
			for _, c := range childBoxes {
				if c.width > intrinsic {
					intrinsic = c.width
				}
			}
		}
		// Inputs have no children, so the intrinsic-from-children
		// pass collapses them to width=0. Give each input type a
		// sensible default that fits its rendered glyph(s) plus a
		// pad. Without this, a `Ui.input` with no explicit width
		// renders as 0 cells and the checkbox / radio glyph is
		// invisible.
		if tag == "input" {
			switch la.inputType {
			case "checkbox", "radio":
				if intrinsic < 1 { // single-glyph render: ☐/☑/○/●
					intrinsic = 1
				}
			case "range":
				if intrinsic < 12 { // "├──●──────┤"
					intrinsic = 12
				}
			default:
				if intrinsic < 16 { // text/password/email/etc.
					intrinsic = 16
				}
			}
		}
		if intrinsic < width {
			width = intrinsic
		}
	}
	if !hasExplicitH {
		intrinsic := 0
		if axis == layoutAxisColumn {
			for i, c := range childBoxes {
				intrinsic += c.height
				if i > 0 {
					intrinsic += la.spacing
				}
			}
		} else {
			for _, c := range childBoxes {
				if c.height > intrinsic {
					intrinsic = c.height
				}
			}
		}
		// Inputs need at least 1 cell of height to render their
		// glyph; textarea defaults to 3 rows for a useful editor.
		if tag == "input" {
			if la.inputType == "textarea" {
				if intrinsic < 3 {
					intrinsic = 3
				}
			} else if intrinsic < 1 {
				intrinsic = 1
			}
		}
		if intrinsic < height {
			height = intrinsic
		}
	}

	// Final box dimensions include padding + border.
	finalW := width + la.padding[1] + la.padding[3] + la.borderWidth[1] + la.borderWidth[3]
	finalH := height + la.padding[0] + la.padding[2] + la.borderWidth[0] + la.borderWidth[2]
	if headingUnderline {
		finalH++ // reserve a row for the heading's underline
	}
	if finalW > maxW {
		finalW = maxW
	}
	if finalH > maxH {
		finalH = maxH
	}

	return layoutBox{
		kind:        "node",
		tag:         tag,
		width:       finalW,
		height:      finalH,
		axis:        axis,
		padding:     la.padding,
		spacing:     la.spacing,
		fg:          la.fg,
		bg:          la.bg,
		bold:        la.bold,
		italic:      la.italic,
		underline:   la.underline,
		strike:      la.strike,
		overline:    la.overline,
		textAlign:   la.textAlign,
		alignX:      la.alignX,
		alignY:      la.alignY,
		events:      la.events,
		valueAttr:   la.valueAttr,
		placeholder: la.placeholder,
		nameAttr:    la.nameAttr,
		inputType:   la.inputType,
		wrapped:     la.isWrappedRow,
		paragraph:   la.isParagraph,
		textColumn:  la.isTextColumn,
		gridLayout:  la.isGrid,
		gridColumns: la.gridColumns,
		clip:        la.clip,
		overflow:    la.overflow,
		nearby:      la.nearby,
		children:    childBoxes,
		borderWidth: la.borderWidth,
		borderColor: la.borderColor,
		borderStyle: la.borderStyle,
	}
}

// layoutChildren distributes the available main-axis space (width for
// row, height for column) using flex-style portion division. Children
// with explicit sizes get them; remaining space is split among Fill
// children proportional to their portions.
func layoutChildren(children []any, ctx tuiLayoutCtx, availW, availH int, axis layoutAxis, spacing int) []layoutBox {
	n := len(children)
	if n == 0 {
		return nil
	}

	mainAxis := availW
	if axis == layoutAxisColumn {
		mainAxis = availH
	}

	// First pass — measure non-Fill children at intrinsic / explicit size.
	totalSpacing := spacing * (n - 1)
	if totalSpacing < 0 {
		totalSpacing = 0
	}

	type entry struct {
		idx      int
		fillN    int // 0 if not fill
		measured int // main-axis size before fill expansion
		box      layoutBox
	}
	entries := make([]entry, n)
	used := 0
	totalFill := 0

	for i, c := range children {
		// Measure with potentially generous bounds; we'll adjust if needed.
		var box layoutBox
		if axis == layoutAxisRow {
			box = layoutElement(c, ctx, availW, availH, axis)
		} else {
			box = layoutElement(c, ctx, availW, availH, axis)
		}
		// Detect Fill via the resolved Length on the main axis. We need
		// to peek into the Element's attrs to know — simpler heuristic:
		// re-walk attrs for Fill-on-main-axis. For now we treat any
		// child whose intrinsic main-axis size hits availW/availH as
		// having claimed it; finer grain comes when AttrFill is wired
		// via a flag in walkAttrs (see TODO in walkAttrs).
		fillN := childFillPortion(c, axis)
		entries[i] = entry{idx: i, fillN: fillN, box: box}
		if fillN > 0 {
			totalFill += fillN
			entries[i].measured = 0
		} else {
			if axis == layoutAxisRow {
				entries[i].measured = box.width
			} else {
				entries[i].measured = box.height
			}
			used += entries[i].measured
		}
	}

	remaining := mainAxis - used - totalSpacing
	if remaining < 0 {
		remaining = 0
	}

	// Distribute remaining among Fill children.
	if totalFill > 0 {
		distributed := 0
		for i, e := range entries {
			if e.fillN <= 0 {
				continue
			}
			share := remaining * e.fillN / totalFill
			if i == n-1 {
				// Last fill child claims remainder to avoid losing
				// cells to integer division.
				share = remaining - distributed
			}
			distributed += share
			entries[i].measured = share
			// Re-layout the child with the allocated main-axis size.
			if axis == layoutAxisRow {
				entries[i].box = layoutElement(children[i], ctx, share, availH, axis)
				entries[i].box.width = share
			} else {
				entries[i].box = layoutElement(children[i], ctx, availW, share, axis)
				entries[i].box.height = share
			}
		}
	}

	out := make([]layoutBox, n)
	for i, e := range entries {
		out[i] = e.box
	}
	return out
}

// childFillPortion peeks inside an Element's attrs for Fill on the main
// axis. Returns the portion (1 for `Fill 1`, N for `Fill N`), or 0 if
// the child doesn't claim Fill on this axis.
//
// A v0 simplification: we look for AttrWidth/AttrHeight = Fill N. The
// finer-grained "Fill is mediated via Length" walk would integrate
// with walkAttrs.
func childFillPortion(child any, axis layoutAxis) int {
	adt, ok := child.(SkyADT)
	if !ok {
		return 0
	}
	if adt.Tag != 2 && adt.Tag != 3 {
		return 0
	}
	var attrs []any
	switch adt.Tag {
	case 2:
		if len(adt.Fields) >= 2 {
			attrs = asList(adt.Fields[1])
		}
	case 3:
		if len(adt.Fields) >= 3 {
			attrs = asList(adt.Fields[2])
		}
	}
	for _, a := range attrs {
		ad, ok := a.(SkyADT)
		if !ok {
			continue
		}
		// AttrWidth = tag 1, AttrHeight = tag 2 (per Std.Ui.Attribute order)
		switch {
		case axis == layoutAxisRow && ad.Tag == 1:
			if p := lengthFillPortion(ad.Fields); p > 0 {
				return p
			}
		case axis == layoutAxisColumn && ad.Tag == 2:
			if p := lengthFillPortion(ad.Fields); p > 0 {
				return p
			}
		}
	}
	return 0
}

func lengthFillPortion(fields []any) int {
	if len(fields) == 0 {
		return 0
	}
	l, ok := fields[0].(SkyADT)
	if !ok {
		return 0
	}
	// Length.Fill = tag 2 (per Length ADT order: Px=0, Content=1, Fill=2, Min=3, Max=4, Vh=5, Vw=6)
	if l.Tag == 2 && len(l.Fields) > 0 {
		if n, ok := l.Fields[0].(int); ok {
			return n
		}
	}
	return 0
}

// walkAttrs extracts layout-relevant values from a Std.Ui attribute list.
// isInternalMarker — Std.Ui uses AttrStyle keys prefixed with __ as
// sentinels that the renderer interprets specially (see Std.Ui's
// rowMarker / colMarker / wrapMarker / gridMarker / paragraphMarker /
// textColumnMarker definitions). Non-prefixed style keys are user-
// supplied raw CSS, which we can't render.
func isInternalMarker(k string) bool {
	return len(k) >= 2 && k[0] == '_' && k[1] == '_'
}

type walkedAttrs struct {
	width       any    // raw Length value
	height      any
	padding     [4]int // top, right, bottom, left in cells
	spacing     int
	fg, bg      tuiColor
	bold        bool
	italic      bool
	underline   bool
	strike      bool   // text-decoration: line-through
	overline    bool   // text-decoration: overline (SGR 53)
	textAlign   string // "left" | "center" | "right" — for text painting within box
	alignX       string // "" (unset, default left/main-axis), "left", "center", "right"
	alignY       string // "" (unset), "top", "center", "bottom"
	isRow       bool
	isWrappedRow bool
	isParagraph bool
	isTextColumn bool
	isGrid       bool
	gridColumns  int
	clip         [2]bool // [clipX, clipY]
	overflow     [2]string // [x, y] — "", "clip", "scrollbars"
	nameAttr     string  // AttrAttribute "name" — for form-submit collection
	inputType    string  // AttrAttribute "type" — text/password/checkbox/radio/range/textarea/etc.
	nearby       []nearbyEntry // captured AttrNearby items
	events       []any  // every AttrEvent payload
	valueAttr    string // AttrAttribute "value" — initial value for inputs
	placeholder  string // AttrAttribute "placeholder" — shown on empty input
	borderWidth  [4]int // top, right, bottom, left — 1 if border present, 0 otherwise
	borderColor  tuiColor
	borderStyle  string // "solid" (default), "dashed", "dotted"
}

// nearbyEntry pairs a Location with the Element to render at that
// offset relative to its host. The renderer realises these AFTER
// painting the host so they sit on top.
type nearbyEntry struct {
	location int // 0=Above 1=Below 2=OnRight 3=OnLeft 4=InFront 5=Behind
	elem     any
}

func walkAttrs(attrs []any, ctx tuiLayoutCtx) walkedAttrs {
	out := walkedAttrs{}
	for _, a := range attrs {
		adt, ok := a.(SkyADT)
		if !ok {
			continue
		}
		// Tag numbers from Std.Ui's Attribute ADT order (verified
		// against the codegen — see sky-stdlib/Std/Ui.sky).
		// 0:NoAttribute 1:Width 2:Height 3:AlignX 4:AlignY 5:Nearby
		// 6:Padding 7:Spacing 8:Style 9:Describe 10:Class 11:Event
		// 12:Attribute 13:FontSize 14:FontColor 15:FontFamily
		// 16:FontWeight 17:FontItalic 18:FontUnderline 19:FontDecoration
		// 20:FontLetterSpacing 21:FontWordSpacing 22:FontAlign
		// 23:BgColor 24:BgImage 25:BgGradient 26:BorderWidth
		// 27:BorderWidthEach 28:BorderColor 29:BorderRounded
		// 30:BorderStyle 31:BorderShadow 32:BorderInsetShadow
		// 33:Pointer 34:Overflow
		switch adt.Tag {
		case 0: // NoAttribute
			continue
		case 1: // AttrWidth Length
			if len(adt.Fields) > 0 {
				out.width = adt.Fields[0]
			}
		case 2: // AttrHeight Length
			if len(adt.Fields) > 0 {
				out.height = adt.Fields[0]
			}
		case 3: // AttrAlignX HAlign — HAlign tags: 0=Left, 1=CenterX, 2=Right
			if len(adt.Fields) > 0 {
				if ah, ok := adt.Fields[0].(SkyADT); ok {
					switch ah.Tag {
					case 0:
						out.alignX = "left"
					case 1:
						out.alignX = "center"
					case 2:
						out.alignX = "right"
					}
				}
			}
		case 4: // AttrAlignY VAlign — VAlign tags: 0=Top, 1=CenterY, 2=Bottom
			if len(adt.Fields) > 0 {
				if av, ok := adt.Fields[0].(SkyADT); ok {
					switch av.Tag {
					case 0:
						out.alignY = "top"
					case 1:
						out.alignY = "center"
					case 2:
						out.alignY = "bottom"
					}
				}
			}
		case 5: // AttrNearby Location (Element msg) — Location tags: 0=Above 1=Below 2=OnRight 3=OnLeft 4=InFront 5=Behind
			if len(adt.Fields) >= 2 {
				if loc, ok := adt.Fields[0].(SkyADT); ok {
					out.nearby = append(out.nearby, nearbyEntry{location: loc.Tag, elem: adt.Fields[1]})
				}
			}
		case 6: // AttrPadding T R B L
			if len(adt.Fields) >= 4 {
				out.padding[0] = pxToCellsY(intOf(adt.Fields[0]), ctx)
				out.padding[1] = pxToCellsX(intOf(adt.Fields[1]), ctx)
				out.padding[2] = pxToCellsY(intOf(adt.Fields[2]), ctx)
				out.padding[3] = pxToCellsX(intOf(adt.Fields[3]), ctx)
			}
		case 7: // AttrSpacing N
			if len(adt.Fields) > 0 {
				out.spacing = pxToCellsX(intOf(adt.Fields[0]), ctx)
			}
		case 8: // AttrStyle "k" "v" — sentinel for row/col/wrap/grid + raw CSS escape
			if len(adt.Fields) >= 2 {
				k, _ := adt.Fields[0].(string)
				switch k {
				case "__row":
					out.isRow = true
				case "__col":
					// default (column); no flag needed
				case "__wrap":
					out.isWrappedRow = true
				case "__grid":
					out.isGrid = true
				case "__paragraph":
					out.isParagraph = true
				case "__textcolumn":
					out.isTextColumn = true
				case "__gridMin":
					// gridColumns N → AttrStyle "__gridMin" (encoded value)
					if v, ok := adt.Fields[1].(string); ok {
						fmt.Sscanf(v, "%d", &out.gridColumns)
					}
				default:
					// User-supplied raw CSS — TUI can't render, warn once.
					if !isInternalMarker(k) {
						tuiWarn("style", "raw CSS attribute "+k)
					}
				}
			}
		case 9: // AttrDescribe Description — accessibility hints; tag-specific styling handled in layoutNode
			// Description content is consumed by tagForDescription / pickSemanticTag
			// at layoutElement time. No attr-level work here.
		case 10: // AttrClass — CSS class, ignored in TUI by design
			tuiWarn("style", "AttrClass (CSS classes don't apply in terminal)")
		case 11: // AttrEvent — pre-built event payload (eventPair{name, msg})
			if len(adt.Fields) > 0 {
				out.events = append(out.events, adt.Fields[0])
			}
		case 12: // AttrAttribute "k" "v" — raw HTML attr; we read "value"/"placeholder"/"name"
			if len(adt.Fields) >= 2 {
				k, _ := adt.Fields[0].(string)
				v, _ := adt.Fields[1].(string)
				switch k {
				case "value":
					out.valueAttr = v
				case "placeholder":
					out.placeholder = v
				case "name":
					out.nameAttr = v
				case "type":
					out.inputType = v
				case "id", "for", "rows", "cols", "min", "max", "step",
					"required", "disabled", "checked", "readonly", "autofocus",
					"autocomplete", "minlength", "maxlength", "pattern",
					"href", "target", "src", "alt", "accept", "multiple", "selected",
					"sky-nav":
					// Known HTML attrs that don't need TUI rendering — silent skip.
				default:
					tuiWarn("attribute", "raw HTML attribute "+k)
				}
			}
		case 13: // AttrFontSize — terminal has one cell size
			tuiWarn("font", "size (terminal cells are uniform)")
		case 14: // AttrFontColor Color
			if len(adt.Fields) > 0 {
				out.fg = colorOf(adt.Fields[0])
			}
		case 15: // AttrFontFamily — terminal font is set by emulator
			tuiWarn("font", "family (terminal font is fixed)")
		case 16: // AttrFontWeight
			if len(adt.Fields) > 0 {
				if w, ok := adt.Fields[0].(int); ok && w >= 600 {
					out.bold = true
				}
			}
		case 17: // AttrFontItalic
			out.italic = true
		case 18: // AttrFontUnderline
			out.underline = true
		case 19: // AttrFontDecoration String
			if len(adt.Fields) > 0 {
				if s, ok := adt.Fields[0].(string); ok {
					switch s {
					case "underline":
						out.underline = true
					case "line-through":
						out.strike = true
					case "overline":
						out.overline = true
					case "none":
						out.underline = false
						out.strike = false
						out.overline = false
					default:
						tuiWarn("font", "decoration "+s)
					}
				}
			}
		case 20: // AttrFontLetterSpacing
			tuiWarn("font", "letter-spacing (terminal cells are atomic)")
		case 21: // AttrFontWordSpacing
			tuiWarn("font", "word-spacing (terminal cells are atomic)")
		case 22: // AttrFontAlign
			if len(adt.Fields) > 0 {
				if s, ok := adt.Fields[0].(string); ok {
					out.textAlign = s
				}
			}
		case 23: // AttrBgColor Color
			if len(adt.Fields) > 0 {
				out.bg = colorOf(adt.Fields[0])
			}
		case 24: // AttrBgImage
			tuiWarn("background", "image (terminals can't render image fills)")
		case 25: // AttrBgGradient
			tuiWarn("background", "gradient (terminals can't render gradient fills)")
		case 26: // AttrBorderWidth Int — uniform border on all sides
			if len(adt.Fields) > 0 {
				if w, ok := adt.Fields[0].(int); ok && w > 0 {
					out.borderWidth = [4]int{1, 1, 1, 1}
				}
			}
		case 27: // AttrBorderWidthEach T R B L
			if len(adt.Fields) >= 4 {
				for i := 0; i < 4; i++ {
					if w, ok := adt.Fields[i].(int); ok && w > 0 {
						out.borderWidth[i] = 1
					}
				}
			}
		case 28: // AttrBorderColor Color
			if len(adt.Fields) > 0 {
				out.borderColor = colorOf(adt.Fields[0])
			}
		case 29: // AttrBorderRounded — no rounded box-drawing in standard Unicode
			tuiWarn("border", "rounded corners (Unicode box-drawing has no rounded chars)")
		case 30: // AttrBorderStyle String — "solid" | "dashed" | "dotted"
			if len(adt.Fields) > 0 {
				if s, ok := adt.Fields[0].(string); ok {
					out.borderStyle = s
				}
			}
		case 31: // AttrBorderShadow
			tuiWarn("border", "shadow (terminals can't render drop shadows)")
		case 32: // AttrBorderInsetShadow
			tuiWarn("border", "inner shadow (terminals can't render shadows)")
		case 33: // AttrPointer — cursor: pointer; TUI has no cursor concept
			// Silent skip — pointer is purely a mouse cursor hint, the
			// focus indicator already telegraphs interactivity.
		case 34: // AttrOverflow String String — overflow-x, overflow-y
			if len(adt.Fields) >= 2 {
				x, _ := adt.Fields[0].(string)
				y, _ := adt.Fields[1].(string)
				out.overflow[0] = x
				out.overflow[1] = y
				if x == "clip" {
					out.clip[0] = true
				}
				if y == "clip" {
					out.clip[1] = true
				}
			}
		default:
			// Unknown tag — likely a Std.Ui addition we haven't ported.
			// Warn so users see a hint rather than silent breakage.
			tuiWarn("attribute", fmt.Sprintf("unknown attribute tag %d (%s)", adt.Tag, adt.SkyName))
		}
	}
	return out
}

func intOf(v any) int {
	if v == nil {
		return 0
	}
	if n, ok := v.(int); ok {
		return n
	}
	return AsInt(v)
}

// colorOf reads a Std.Ui.Color value — Color = Rgba Int Int Int Float.
func colorOf(v any) tuiColor {
	adt, ok := v.(SkyADT)
	if !ok {
		return tuiColor{}
	}
	if adt.Tag != 0 || len(adt.Fields) < 3 {
		return tuiColor{}
	}
	r, _ := adt.Fields[0].(int)
	g, _ := adt.Fields[1].(int)
	b, _ := adt.Fields[2].(int)
	return tuiColor{set: true, r: uint8(r & 0xff), g: uint8(g & 0xff), b: uint8(b & 0xff)}
}

// resolveLengthCells maps a Std.Ui Length value to character cells on
// the given axis, given the available cells in the parent. Returns
// (cells, hasExplicitSize). If the input isn't a recognised Length,
// returns (0, false) and the caller falls back to Content / parent-fill.
func resolveLengthCells(v any, axis string, available int, ctx tuiLayoutCtx) (int, bool) {
	if v == nil {
		return 0, false
	}
	adt, ok := v.(SkyADT)
	if !ok {
		return 0, false
	}
	switch adt.Tag {
	case 0: // Px Int
		if len(adt.Fields) == 0 {
			return 0, false
		}
		px, _ := adt.Fields[0].(int)
		if axis == "x" {
			return pxToCellsX(px, ctx), true
		}
		return pxToCellsY(px, ctx), true
	case 1: // Content
		return 0, false // caller measures children
	case 2: // Fill _
		// Fill is handled by the parent's distribution pass; here it
		// claims "as much as possible" if asked directly.
		return available, true
	case 3: // Min N Length
		if len(adt.Fields) >= 2 {
			minN, _ := adt.Fields[0].(int)
			inner, hasExpl := resolveLengthCells(adt.Fields[1], axis, available, ctx)
			if !hasExpl {
				return minN, true
			}
			if inner < minN {
				return minN, true
			}
			return inner, true
		}
	case 4: // Max N Length
		if len(adt.Fields) >= 2 {
			maxN, _ := adt.Fields[0].(int)
			inner, hasExpl := resolveLengthCells(adt.Fields[1], axis, available, ctx)
			if !hasExpl {
				return available, true
			}
			if inner > maxN {
				return maxN, true
			}
			return inner, true
		}
	case 5: // Vh N (viewport-height percent)
		if len(adt.Fields) > 0 {
			pct, _ := adt.Fields[0].(int)
			return ctx.rows * pct / 100, true
		}
	case 6: // Vw N (viewport-width percent)
		if len(adt.Fields) > 0 {
			pct, _ := adt.Fields[0].(int)
			return ctx.cols * pct / 100, true
		}
	}
	return 0, false
}

// pxToCellsX / pxToCellsY — logical-pixel canvas conversion.
//
// Round-half-to-even by default, but POSITIVE px values smaller than
// half a cell round UP to 1 (rather than down to 0) so user intent
// like `Ui.spacing 4` produces visible separation in a typical 80-col
// terminal where pxPerCellX is ~16. Without this, every paddingXY/
// spacing under half a cell width silently disappears.
func pxToCellsX(px int, ctx tuiLayoutCtx) int {
	if ctx.pxPerCellX <= 0 {
		return px
	}
	cells := math.Round(float64(px) / ctx.pxPerCellX)
	if cells == 0 && px > 0 {
		return 1
	}
	return int(cells)
}

func pxToCellsY(px int, ctx tuiLayoutCtx) int {
	if ctx.pxPerCellY <= 0 {
		return px
	}
	cells := math.Round(float64(px) / ctx.pxPerCellY)
	if cells == 0 && px > 0 {
		return 1
	}
	return int(cells)
}

// runeLen returns the DISPLAY WIDTH of s in terminal cells — not the
// rune count. CJK / emoji / wide chars each contribute 2; combining
// marks contribute 0; printable ASCII contributes 1. Used by layout
// to size text-shaped boxes correctly so a row containing "日本"
// reserves 4 cells, not 2.
//
// The function name is a historical artefact (it used to count
// runes). All callers want display width; renaming is mechanical
// and tracked separately to keep this commit minimal.
func runeLen(s string) int {
	return displayWidth(s)
}

// ─── Paint pass ──────────────────────────────────────────────────────

func newCellGrid(cols, rows int) [][]tuiCell {
	g := make([][]tuiCell, rows)
	for i := range g {
		row := make([]tuiCell, cols)
		for j := range row {
			row[j].ch = " "
		}
		g[i] = row
	}
	return g
}

// paintBox writes a layoutBox into the grid starting at (col0, row0).
// Recurses through children, applying axis + spacing for sibling
// placement. Collects focusable elements in tab order. Inputs read
// their buffer from the persistent inputRegistry so typing carries
// across re-renders.
//
// inherited carries the parent chain's effective text style so font
// attributes (color, bold, italic, etc.) cascade to text leaves the
// way CSS inheritance does on the web side. Each Node merges its own
// style on top, and propagates the merged style down.
func paintBox(grid [][]tuiCell, box layoutBox, col0, row0, maxW, maxH, focusIdx int, focusables *[]focusable, inputs *inputRegistry, inherited textStyle, parentAxis layoutAxis, idxInParent int) {
	w := box.width
	if w > maxW {
		w = maxW
	}
	h := box.height
	if h > maxH {
		h = maxH
	}

	// Background fill.
	if box.bg.set {
		fillRect(grid, col0, row0, w, h, box.bg)
	}

	// Behind overlays — paint before children so they sit underneath.
	for _, n := range box.nearby {
		if n.location == 5 { // Behind
			paintNearby(grid, n, col0, row0, w, h, focusIdx, focusables, inputs, inherited)
		}
	}

	// Border draw (under children/text but over background fill).
	// Inputs deliberately suppress border rendering — Unicode
	// box-drawing on a 1-row input looks chunky and misaligns
	// against neighbouring text. The track shading inside the
	// input (paintInputBufferAdvanced) communicates bounds; the
	// reverse-cursor + ☑ / ● glyphs communicate state.
	if box.tag != "input" && box.borderWidth[0]+box.borderWidth[1]+box.borderWidth[2]+box.borderWidth[3] > 0 {
		drawBorder(grid, col0, row0, w, h, box.borderWidth, box.borderColor, box.borderStyle)
	}

	// If this box is focusable (has any event handler), is an input
	// (always focusable for editing), or is an `<a>` link (matches
	// HTML's intrinsic tab-stop behaviour — `Ui.link` carries no
	// events but should still join the focus order so users can
	// reach it by arrow / Tab and see the focused-link underline).
	isInput := box.tag == "input"
	isLink := box.tag == "a"
	thisFocusIdx := -1
	if len(box.events) > 0 || isInput || isLink {
		thisFocusIdx = len(*focusables)
		*focusables = append(*focusables, focusable{
			events:       box.events,
			isInput:      isInput,
			inputType:    box.inputType,
			initialValue: box.valueAttr,
			placeholder:  box.placeholder,
			row:          row0, col: col0, w: w, h: h,
		})
	}

	// Recurse into content area (after padding + border). Inputs
	// suppress border rendering (see drawBorder skip above), so
	// the inner area for inputs is also computed without the
	// border inset — otherwise an input with `Border.width 1`
	// would still consume cells for an invisible border.
	bw := box.borderWidth
	if box.tag == "input" {
		bw = [4]int{}
	}
	innerCol := col0 + box.padding[3] + bw[3]
	innerRow := row0 + box.padding[0] + bw[0]
	innerW := w - box.padding[1] - box.padding[3] - bw[1] - bw[3]
	innerH := h - box.padding[0] - box.padding[2] - bw[0] - bw[2]
	if innerW < 0 {
		innerW = 0
	}
	if innerH < 0 {
		innerH = 0
	}

	// Effective style for this node = parent inherited + own.
	style := mergeStyle(inherited, boxOwnStyle(box))

	switch box.kind {
	case "text":
		paintText(grid, box.text, innerCol, innerRow, innerW, style)
	case "node":
		// Inputs render their persistent buffer + cursor instead of
		// recursing into children (Std.Ui's input creates a TaggedNode
		// with no children). The render varies by inputType:
		//   text / email / search / "" → standard text editor
		//   password → buffer rendered as ●●●
		//   checkbox → [✓] when value=="true", [ ] otherwise
		//   radio    → ◉ / ○  same way
		//   range    → ──●── slider
		//   textarea → multi-line editor
		if box.tag == "input" {
			focIdx := len(*focusables) - 1
			focused := focIdx == focusIdx
			switch box.inputType {
			case "checkbox":
				paintCheckbox(grid, box, innerCol, innerRow, innerW, style, focused)
			case "radio":
				paintRadio(grid, box, innerCol, innerRow, innerW, style, focused)
			case "range":
				paintSlider(grid, box, innerCol, innerRow, innerW, style, focused)
			default:
				st := inputs.get(focIdx)
				if box.valueAttr != st.lastValueAttr {
					st.buffer = box.valueAttr
					st.cursor = runeLen(st.buffer)
					st.lastValueAttr = box.valueAttr
				}
				masked := box.inputType == "password"
				multiline := box.inputType == "textarea"
				paintInputBufferAdvanced(grid, st, innerCol, innerRow, innerW, innerH, style, box.placeholder, focused, masked, multiline)
			}
			break
		}
		if box.gridLayout && box.gridColumns > 0 {
			// Flow children row-major into NxM cells.
			ncols := box.gridColumns
			colWidth := 0
			if ncols > 0 {
				colWidth = innerW / ncols
			}
			// Compute row heights.
			nrows := (len(box.children) + ncols - 1) / ncols
			rowHeights := make([]int, nrows)
			for i, c := range box.children {
				r := i / ncols
				if c.height > rowHeights[r] {
					rowHeights[r] = c.height
				}
			}
			y := innerRow
			for r := 0; r < nrows; r++ {
				if r > 0 {
					y += box.spacing
				}
				for col := 0; col < ncols; col++ {
					i := r*ncols + col
					if i >= len(box.children) {
						break
					}
					c := box.children[i]
					x := innerCol + col*colWidth
					paintBox(grid, c, x, y, colWidth, rowHeights[r], focusIdx, focusables, inputs, style, layoutAxisRow, i)
				}
				y += rowHeights[r]
			}
		} else if box.wrapped && box.axis == layoutAxisRow {
			// wrappedRow: lay children in horizontal rows; when next
			// child wouldn't fit, break to a new row beneath.
			x := innerCol
			y := innerRow
			rowHeight := 0
			for _, c := range box.children {
				if x+c.width > innerCol+innerW && x > innerCol {
					// Wrap to next "row".
					x = innerCol
					y += rowHeight + box.spacing
					rowHeight = 0
				}
				paintBox(grid, c, x, y, innerW, innerH-(y-innerRow), focusIdx, focusables, inputs, style, layoutAxisRow, 0)
				x += c.width + box.spacing
				if c.height > rowHeight {
					rowHeight = c.height
				}
			}
		} else if box.axis == layoutAxisRow {
			x := innerCol
			for i, c := range box.children {
				if i > 0 {
					x += box.spacing
				}
				// Cross-axis (vertical) alignment per child.
				yOffset := alignOffset(c.alignY, innerH-c.height, false)
				paintBox(grid, c, x, innerRow+yOffset, innerW-(x-innerCol), innerH, focusIdx, focusables, inputs, style, layoutAxisRow, i)
				x += c.width
			}
		} else {
			y := innerRow
			for i, c := range box.children {
				if i > 0 {
					y += box.spacing
				}
				// Cross-axis (horizontal) alignment per child.
				xOffset := alignOffset(c.alignX, innerW-c.width, true)
				paintBox(grid, c, innerCol+xOffset, y, innerW, innerH-(y-innerRow), focusIdx, focusables, inputs, style, layoutAxisColumn, i)
				y += c.height
			}
		}
	}

	// Heading underline rows + level markers. Paint AFTER children so
	// the underline sits below the heading's text. Each level gets a
	// distinct visual treatment so users can distinguish hierarchy at
	// a glance even without font-size differences:
	//   h1: ═══ double-line under title
	//   h2: ─── single-line under title
	//   h3: ▌ heavy left bar prefix
	//   h4: ▎ medium left bar
	//   h5: ▏ thin left bar
	//   h6: · dot prefix
	switch box.tag {
	case "h1":
		paintHeadingUnderline(grid, innerCol, innerRow+1, innerW, "═", style.fg)
	case "h2":
		paintHeadingUnderline(grid, innerCol, innerRow+1, innerW, "─", style.fg)
	case "h3":
		paintHeadingMarker(grid, col0, row0, "▌", style.fg)
	case "h4":
		paintHeadingMarker(grid, col0, row0, "▎", style.fg)
	case "h5":
		paintHeadingMarker(grid, col0, row0, "▏", style.fg)
	case "h6":
		paintHeadingMarker(grid, col0, row0, "·", style.fg)
	}

	// Focus indicator AFTER children so markers (e.g. ▸ ◂ for
	// buttons) aren't overwritten by the label paint.
	if thisFocusIdx == focusIdx && thisFocusIdx >= 0 && !isInput {
		applyFocusIndicator(grid, box, col0, row0, w, h)
	}

	// Above / Below / OnLeft / OnRight / InFront overlays.
	// Behind was already painted before children at the top of paintBox.
	for _, n := range box.nearby {
		if n.location == 5 { // Behind already handled
			continue
		}
		paintNearby(grid, n, col0, row0, w, h, focusIdx, focusables, inputs, style)
	}
}

// lighten clamps a single 8-bit colour channel + delta to [0, 255].
// Used by paintInputBufferAdvanced to derive a track-shade colour
// from the input's bg colour without dragging in a colour-space
// library.
func lighten(c uint8, delta int) uint8 {
	v := int(c) + delta
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}


// paintInputBufferAdvanced renders an input's buffer + cursor.
// `masked` (password type) replaces each char with ● in the visual
// rendering (the underlying buffer keeps real chars). `multiline`
// flows the buffer's embedded \n into separate rows.
//
// Empty cells in the input range get a light shaded "track" (░)
// painted first so the user can see the input's bounds even before
// typing. Real characters paint over the track. The track uses a
// dim grey foreground so it's visibly subordinate to typed text;
// when the parent has set an explicit background colour, the track
// inherits the parent's bg without the dim fg overlay (the bg
// already defines the field shape).
func paintInputBufferAdvanced(grid [][]tuiCell, st *tuiInput, col, row, w, h int, style textStyle, placeholder string, focused, masked, multiline bool) {
	if row < 0 || row >= len(grid) || w <= 0 {
		return
	}
	display := st.buffer
	usePlaceholder := false
	if display == "" && placeholder != "" && !focused {
		display = placeholder
		usePlaceholder = true
	}

	// Paint the input track first — every cell in the input's range
	// gets a light shaded ░ so the field shape is visible even when
	// the buffer is empty. Real characters paint over the track in
	// paintInputLine. Two shading rules:
	//
	//   * No bg colour set on the input: track is dim grey ░ on the
	//     terminal's default bg. Gives the input a visible "groove"
	//     even when it has no explicit styling.
	//   * Bg colour set: track is a 15%-lighter shade of the bg, so
	//     the input has a subtle textured fill that differs from
	//     the surrounding solid bg fill. Lets the user see input
	//     bounds + cursor location without harsh contrast.
	rowsToPaint := h
	if !multiline {
		rowsToPaint = 1
	}
	trackFg := tuiColor{set: true, r: 110, g: 110, b: 110}
	if style.bg.set {
		// Lighten the bg by 15% (clamped) for the track fg.
		trackFg = tuiColor{set: true,
			r: lighten(style.bg.r, 38),
			g: lighten(style.bg.g, 38),
			b: lighten(style.bg.b, 38)}
	}
	for li := 0; li < rowsToPaint; li++ {
		rr := row + li
		if rr < 0 || rr >= len(grid) {
			continue
		}
		rowCells := grid[rr]
		for cx := col; cx < col+w && cx >= 0 && cx < len(rowCells); cx++ {
			cell := &rowCells[cx]
			if cell.ch == "" || cell.ch == " " {
				cell.ch = "░"
				cell.fg = trackFg
				if style.bg.set {
					cell.bg = style.bg
				}
			}
		}
	}

	// Multi-line: split into lines + place each on consecutive rows.
	if multiline {
		lines := strings.Split(display, "\n")
		// Place each line.
		for li, line := range lines {
			if row+li >= len(grid) || row+li-row >= h {
				break
			}
			paintInputLine(grid, line, col, row+li, w, style, usePlaceholder)
		}
		// Cursor: locate (lineIdx, colInLine) from cursor rune index.
		if focused && !usePlaceholder {
			lineIdx, colInLine := cursorLocate(display, st.cursor)
			cy := row + lineIdx
			cx := col + colInLine
			if cy >= 0 && cy < len(grid) && cy-row < h && cx >= 0 && cx < col+w && cx < len(grid[cy]) {
				grid[cy][cx].reverse = true
				if grid[cy][cx].ch == "" {
					grid[cy][cx].ch = " "
				}
			}
		}
		return
	}

	// Single-line path. mask if password.
	rendered := display
	if masked && !usePlaceholder {
		rendered = strings.Repeat("●", runeLen(display))
	}
	paintInputLine(grid, rendered, col, row, w, style, usePlaceholder)

	// Cursor (single-line): position st.cursor (rune index) within the
	// rendered string. Mask doesn't change cursor positioning since
	// rune count is preserved.
	if focused && !usePlaceholder {
		cursorCol := col + st.cursor
		if cursorCol >= col+w {
			cursorCol = col + w - 1
		}
		if cursorCol >= 0 && cursorCol < col+w && row < len(grid) && cursorCol < len(grid[row]) {
			grid[row][cursorCol].reverse = true
			if grid[row][cursorCol].ch == " " || grid[row][cursorCol].ch == "" {
				grid[row][cursorCol].ch = " "
			}
		}
	}
}

// paintInputLine paints one line of input text into the grid.
// Resets fg per-cell when style.fg is unset so the parent's track
// colour (set by paintInputBufferAdvanced's pre-pass) doesn't leak
// through onto the typed character — without this clear, real text
// inherits the dim track colour and looks identical to the track.
func paintInputLine(grid [][]tuiCell, text string, col, row, w int, style textStyle, isPlaceholder bool) {
	if row < 0 || row >= len(grid) {
		return
	}
	rowCells := grid[row]
	clean := sanitiseString(text)
	x := col
	iterGraphemes(clean, func(cluster string, gw int) bool {
		if x >= col+w || x >= len(rowCells) {
			return false
		}
		// Wide cluster at the right edge → substitute space.
		if gw >= 2 && (x+1 >= col+w || x+1 >= len(rowCells)) {
			cluster = " "
			gw = 1
		}
		if gw <= 0 {
			// Combining mark — attach to the cell on the left.
			if x > 0 && x-1 < len(rowCells) {
				rowCells[x-1].ch += cluster
			}
			return true
		}
		if x < 0 {
			x += gw
			return true
		}
		c := &rowCells[x]
		c.ch = cluster
		if style.fg.set {
			c.fg = style.fg
		} else {
			// Promote to default fg (terminal's text colour) so the
			// dim ░ track colour painted underneath doesn't leak.
			c.fg = tuiColor{}
		}
		if style.bg.set {
			c.bg = style.bg
		}
		if isPlaceholder {
			c.italic = true
		}
		// Wide cluster: continuation cell stays empty so paintDiff
		// doesn't double-emit. Inherit style so overlays like the
		// reverse-cursor render evenly across both halves.
		if gw >= 2 && x+1 < len(rowCells) {
			next := &rowCells[x+1]
			next.ch = ""
			next.fg = c.fg
			next.bg = c.bg
			next.italic = c.italic
		}
		x += gw
		return true
	})
}

// cursorLocate returns (lineIdx, colInLine) for a cursor rune index
// within text containing embedded \n. `colInLine` is in DISPLAY
// COLUMNS — for ASCII the same as the rune offset, for CJK / emoji
// each wide char counts 2 cells. Painters use the column as a cell
// position in the grid, so a cursor sitting after `日` lands on
// cell 2, not cell 1.
func cursorLocate(text string, cursor int) (int, int) {
	runes := []rune(text)
	if cursor > len(runes) {
		cursor = len(runes)
	}
	line := 0
	colStart := 0 // rune index where current line starts
	for i := 0; i < cursor; i++ {
		if runes[i] == '\n' {
			line++
			colStart = i + 1
		}
	}
	// Convert the rune-offset (cursor - colStart) into a display
	// column by summing widths of the preceding runes ON THIS LINE.
	col := 0
	for i := colStart; i < cursor && i < len(runes); i++ {
		col += displayWidthRune(runes[i])
	}
	return line, col
}

// paintCheckbox renders a single-cell ☐ / ☑ glyph based on the
// element's value attr. Focused state inverts fg/bg via the
// reverse SGR — keeps the visual minimal (one cell) and aligns
// cleanly with neighbouring text rather than a chunky `[ ]`.
func paintCheckbox(grid [][]tuiCell, box layoutBox, col, row, w int, style textStyle, focused bool) {
	checked := box.valueAttr == "true"
	glyph := "☐"
	if checked {
		glyph = "☑"
	}
	paintInputLine(grid, glyph, col, row, w, style, false)
	if focused && row >= 0 && row < len(grid) && col >= 0 && col < len(grid[row]) {
		grid[row][col].reverse = true
	}
}

// paintRadio renders ○ / ● for unselected / selected. Focus is
// shown via the reverse SGR (inverts fg/bg) so the focused radio
// stands out without consuming an extra cell for a focus arrow.
func paintRadio(grid [][]tuiCell, box layoutBox, col, row, w int, style textStyle, focused bool) {
	selected := box.valueAttr != "" && box.valueAttr != "false"
	glyph := "○"
	if selected {
		glyph = "●"
	}
	paintInputLine(grid, glyph, col, row, w, style, false)
	if focused && row >= 0 && row < len(grid) && col >= 0 && col < len(grid[row]) {
		grid[row][col].reverse = true
	}
}

// paintSlider renders ──●── with the thumb (●) positioned proportional
// to value within [min, max]. Min/max/step are in box.* (read from
// AttrAttribute "min"/"max"/"step"; not yet wired — for v1 the slider
// renders at midpoint).
func paintSlider(grid [][]tuiCell, box layoutBox, col, row, w int, style textStyle, focused bool) {
	// v1: render a fixed midpoint thumb. min/max parsing is a polish
	// pass; the user sees a slider widget that responds to focus and
	// arrow keys (handled in main loop) without precise value mapping.
	if w < 3 {
		paintInputLine(grid, "●", col, row, w, style, false)
		return
	}
	thumb := w / 2
	for i := 0; i < w; i++ {
		ch := "─"
		if i == thumb {
			ch = "●"
		}
		if i == 0 {
			ch = "├"
		} else if i == w-1 {
			ch = "┤"
		}
		paintInputLine(grid, ch, col+i, row, 1, style, false)
	}
	if focused {
		paintInputLine(grid, "▸", col, row, 1, style, false)
	}
}

// mergeStyle layers a node's own style on top of inherited parent style.
// CSS-like cascading: child's explicit style wins, otherwise inherits.
// `align` and `bg` don't inherit (they're per-element); fg, bold, italic,
// underline, strike, overline DO inherit.
func mergeStyle(parent, own textStyle) textStyle {
	out := own
	if !out.fg.set {
		out.fg = parent.fg
	}
	if !out.bold {
		out.bold = parent.bold
	}
	if !out.italic {
		out.italic = parent.italic
	}
	if !out.underline {
		out.underline = parent.underline
	}
	if !out.strike {
		out.strike = parent.strike
	}
	if !out.overline {
		out.overline = parent.overline
	}
	// align doesn't inherit by default in CSS; leave own.
	// bg doesn't inherit (transparent is the default).
	return out
}

// boxOwnStyle extracts just the style fields from a layoutBox.
func boxOwnStyle(box layoutBox) textStyle {
	return textStyle{
		fg:        box.fg,
		bg:        box.bg,
		bold:      box.bold,
		italic:    box.italic,
		underline: box.underline,
		strike:    box.strike,
		overline:  box.overline,
		align:     box.textAlign,
	}
}

// paintNearby places a nearby Element relative to the host box's
// bounds. Location tags (from Std.Ui.Location):
//   0 Above   row = hostRow - childHeight
//   1 Below   row = hostRow + hostHeight
//   2 OnRight col = hostCol + hostWidth
//   3 OnLeft  col = hostCol - childWidth
//   4 InFront same coords as host (overlay)
//   5 Behind  same coords (handled in caller before children paint)
//
// The renderer measures the child against a generous bound (host's
// own size in the relevant axis) then offsets accordingly.
func paintNearby(grid [][]tuiCell, n nearbyEntry, hostCol, hostRow, hostW, hostH, focusIdx int, focusables *[]focusable, inputs *inputRegistry, inherited textStyle) {
	if len(grid) == 0 {
		return
	}
	maxCols := len(grid[0])
	maxRows := len(grid)
	// Choose layout context based on host's size — gives the child
	// generous bounds so it can size itself naturally.
	ctx := tuiLayoutCtx{
		cols:       maxCols,
		rows:       maxRows,
		pxPerCellX: 1, // pixels-per-cell mostly irrelevant for nearby; child uses its own attrs
		pxPerCellY: 1,
	}
	childBox := layoutElement(n.elem, ctx, hostW, hostH, layoutAxisColumn)
	col := hostCol
	row := hostRow
	switch n.location {
	case 0: // Above
		row = hostRow - childBox.height
	case 1: // Below
		row = hostRow + hostH
	case 2: // OnRight
		col = hostCol + hostW
	case 3: // OnLeft
		col = hostCol - childBox.width
	case 4, 5: // InFront / Behind — same coords as host
		col = hostCol
		row = hostRow
	}
	// Bounds-clip — don't paint past grid edges.
	if row < 0 {
		row = 0
	}
	if col < 0 {
		col = 0
	}
	maxW := maxCols - col
	maxH := maxRows - row
	if maxW <= 0 || maxH <= 0 {
		return
	}
	paintBox(grid, childBox, col, row, maxW, maxH, focusIdx, focusables, inputs, inherited, layoutAxisColumn, 0)
}

// extractTextContent walks an Element ADT and returns the plain text
// content (concatenated). Used by paragraph / textColumn to flatten
// styled inline children for word-wrap.
func extractTextContent(elem any) string {
	adt, ok := elem.(SkyADT)
	if !ok {
		return ""
	}
	switch adt.Tag {
	case 0: // Empty
		return ""
	case 1: // Text s
		if len(adt.Fields) > 0 {
			if s, ok := adt.Fields[0].(string); ok {
				return s
			}
		}
	case 2, 3: // Node / TaggedNode — recurse into children
		var fields []any = adt.Fields
		if adt.Tag == 3 && len(fields) > 0 {
			fields = fields[1:] // skip tag
		}
		if len(fields) >= 3 {
			children := asList(fields[2])
			parts := make([]string, 0, len(children))
			for _, c := range children {
				parts = append(parts, extractTextContent(c))
			}
			return strings.Join(parts, " ")
		}
	}
	return ""
}

// alignOffset returns the cell offset for a child along its cross axis
// given (align, slack). Slack = parent_size - child_size; negative
// slack means the child is bigger than the parent and we just place
// at zero offset. axisX flag exists for symmetry with future hooks
// (handed to readers for clarity even though current logic is the
// same for both axes).
func alignOffset(align string, slack int, axisX bool) int {
	if slack <= 0 {
		return 0
	}
	switch align {
	case "center":
		return slack / 2
	case "right", "bottom":
		return slack
	default:
		// "left", "top", or "" (unset) → no offset.
		return 0
	}
}

// paintHeadingMarker writes a single character at (col, row) with the
// given foreground colour. Used for h3-h6 level indicators.
func paintHeadingMarker(grid [][]tuiCell, col, row int, ch string, fg tuiColor) {
	if row < 0 || row >= len(grid) {
		return
	}
	rowCells := grid[row]
	if col < 0 || col >= len(rowCells) {
		return
	}
	rowCells[col].ch = ch
	if fg.set {
		rowCells[col].fg = fg
	}
	rowCells[col].bold = true
}

func paintHeadingUnderline(grid [][]tuiCell, col, row, w int, ch string, fg tuiColor) {
	if row < 0 || row >= len(grid) || w <= 0 {
		return
	}
	rowCells := grid[row]
	for c := col; c < col+w && c < len(rowCells); c++ {
		if c < 0 {
			continue
		}
		rowCells[c].ch = ch
		if fg.set {
			rowCells[c].fg = fg
		}
	}
}

// textStyle bundles all the typographic flags so paintText callers
// don't pass ten booleans positionally. Keep zero-valued for "no style".
type textStyle struct {
	fg, bg    tuiColor
	bold      bool
	italic    bool
	underline bool
	strike    bool
	overline  bool
	align     string // "" | "left" | "center" | "right"
}

func paintText(grid [][]tuiCell, text string, col, row, maxW int, st textStyle) {
	if row < 0 || row >= len(grid) || maxW <= 0 {
		return
	}
	rowCells := grid[row]
	// Sanitise control bytes BEFORE clustering — escape codes between
	// graphemes would otherwise corrupt cluster boundaries.
	clean := sanitiseString(text)
	// Compute starting column based on display-width alignment within
	// the slot. CJK / emoji push the alignment maths through display
	// width, not rune count, so a centred "日本" lands centred not
	// shifted half-a-character left.
	textW := displayWidth(clean)
	startCol := col
	if st.align != "" && textW < maxW {
		slack := maxW - textW
		switch st.align {
		case "center":
			startCol = col + slack/2
		case "right":
			startCol = col + slack
		}
	}
	x := startCol
	iterGraphemes(clean, func(cluster string, w int) bool {
		if x >= col+maxW || x >= len(rowCells) {
			return false
		}
		// A wide cluster (w==2) needs both the current cell AND the
		// next cell to render correctly. If the next cell would be
		// past our slot (x+1 >= col+maxW), substitute a single
		// space rather than truncating mid-glyph.
		if w >= 2 && (x+1 >= col+maxW || x+1 >= len(rowCells)) {
			cluster = " "
			w = 1
		}
		if w <= 0 {
			// Combining mark / zero-width — attach to the previous
			// cell's content rather than allocating its own cell.
			if x > 0 && x-1 < len(rowCells) {
				rowCells[x-1].ch += cluster
			}
			return true
		}
		if x < 0 {
			x += w
			return true
		}
		c := &rowCells[x]
		c.ch = cluster
		if st.fg.set {
			c.fg = st.fg
		}
		if st.bg.set {
			c.bg = st.bg
		}
		if st.bold {
			c.bold = true
		}
		if st.italic {
			c.italic = true
		}
		if st.underline {
			c.underline = true
		}
		if st.strike {
			c.strike = true
		}
		if st.overline {
			c.overline = true
		}
		// For wide clusters, mark the next cell as a continuation.
		// Leaving ch="" tells paintDiff "don't emit anything for this
		// cell — the wide glyph in the previous cell already covered
		// it on the terminal side". Style fields are inherited so a
		// later overlay (e.g. focus reverse) stays consistent across
		// both cells of the wide character.
		if w >= 2 && x+1 < len(rowCells) {
			next := &rowCells[x+1]
			next.ch = ""
			next.fg = c.fg
			next.bg = c.bg
			next.bold = c.bold
			next.italic = c.italic
			next.underline = c.underline
			next.strike = c.strike
			next.overline = c.overline
		}
		x += w
		return true
	})
}

func fillRect(grid [][]tuiCell, col, row, w, h int, bg tuiColor) {
	for r := row; r < row+h && r < len(grid); r++ {
		if r < 0 {
			continue
		}
		rowCells := grid[r]
		for c := col; c < col+w && c < len(rowCells); c++ {
			if c < 0 {
				continue
			}
			rowCells[c].bg = bg
		}
	}
}

// drawBorder paints Unicode box-drawing characters around a box.
// `width` is [top, right, bottom, left]; non-zero entries get drawn.
// Corners only render when their two adjoining sides are both present.
//
// v1: solid (─│┌┐└┘), dashed (┄┆), dotted (┈┊). Rounded is documented
// as ignored (no rounded box-drawing chars in standard Unicode without
// pulling in extended sets that aren't universally rendered).
func drawBorder(grid [][]tuiCell, col, row, w, h int, width [4]int, color tuiColor, style string) {
	if w < 2 || h < 2 {
		return
	}
	hor, vert, tl, tr, bl, br := borderGlyphs(style)
	put := func(c, r int, ch string) {
		if r < 0 || r >= len(grid) || c < 0 || c >= len(grid[r]) {
			return
		}
		cell := &grid[r][c]
		cell.ch = ch
		if color.set {
			cell.fg = color
		}
	}
	// Top edge.
	if width[0] > 0 {
		for c := col + 1; c < col+w-1; c++ {
			put(c, row, hor)
		}
	}
	// Bottom edge.
	if width[2] > 0 {
		for c := col + 1; c < col+w-1; c++ {
			put(c, row+h-1, hor)
		}
	}
	// Left edge.
	if width[3] > 0 {
		for r := row + 1; r < row+h-1; r++ {
			put(col, r, vert)
		}
	}
	// Right edge.
	if width[1] > 0 {
		for r := row + 1; r < row+h-1; r++ {
			put(col+w-1, r, vert)
		}
	}
	// Corners — only draw where both adjoining sides exist.
	if width[0] > 0 && width[3] > 0 {
		put(col, row, tl)
	}
	if width[0] > 0 && width[1] > 0 {
		put(col+w-1, row, tr)
	}
	if width[2] > 0 && width[3] > 0 {
		put(col, row+h-1, bl)
	}
	if width[2] > 0 && width[1] > 0 {
		put(col+w-1, row+h-1, br)
	}
}

// borderGlyphs returns the (horizontal, vertical, topLeft, topRight,
// bottomLeft, bottomRight) box-drawing chars for the requested style.
// Defaults to "solid".
func borderGlyphs(style string) (string, string, string, string, string, string) {
	switch style {
	case "dashed":
		return "┄", "┆", "┌", "┐", "└", "┘"
	case "dotted":
		return "┈", "┊", "┌", "┐", "└", "┘"
	default:
		// solid (and unknown styles fall back here)
		return "─", "│", "┌", "┐", "└", "┘"
	}
}

// applyFocusIndicator draws a per-element-kind focus cue.
//
// Buttons get triangular markers (▸ ... ◂) framing the label so the
// indicator is legible against any button background. Links get a
// full-text underline. Other focusables fall back to a thin reverse-
// video band on top + bottom edges.
func applyFocusIndicator(grid [][]tuiCell, box layoutBox, col, row, w, h int) {
	if w <= 0 || h <= 0 {
		return
	}
	switch box.tag {
	case "button":
		// Place ▸ at the first inner column, ◂ at the last inner column.
		// Inner area is offset by padding + border.
		innerCol := col + box.padding[3] + box.borderWidth[3]
		innerRow := row + box.padding[0] + box.borderWidth[0]
		innerW := w - box.padding[1] - box.padding[3] - box.borderWidth[1] - box.borderWidth[3]
		if innerW < 2 || innerRow < 0 || innerRow >= len(grid) {
			applyReverse(grid, col, row, w, h)
			return
		}
		rowCells := grid[innerRow]
		if innerCol >= 0 && innerCol < len(rowCells) {
			rowCells[innerCol].ch = "▸"
			rowCells[innerCol].bold = true
		}
		if innerCol+innerW-1 >= 0 && innerCol+innerW-1 < len(rowCells) {
			rowCells[innerCol+innerW-1].ch = "◂"
			rowCells[innerCol+innerW-1].bold = true
		}
	case "a":
		// Underline the entire content row (links already use underline
		// semantically, this just makes focus state extra-clear).
		applyUnderline(grid, col, row, w, h)
	default:
		applyReverse(grid, col, row, w, h)
	}
}

func applyUnderline(grid [][]tuiCell, col, row, w, h int) {
	for r := row; r < row+h && r < len(grid); r++ {
		if r < 0 {
			continue
		}
		rowCells := grid[r]
		for c := col; c < col+w && c < len(rowCells); c++ {
			if c < 0 {
				continue
			}
			rowCells[c].underline = true
		}
	}
}

func applyReverse(grid [][]tuiCell, col, row, w, h int) {
	for r := row; r < row+h && r < len(grid); r++ {
		if r < 0 {
			continue
		}
		rowCells := grid[r]
		for c := col; c < col+w && c < len(rowCells); c++ {
			if c < 0 {
				continue
			}
			rowCells[c].reverse = true
		}
	}
}

// ─── ANSI emission ───────────────────────────────────────────────────

// cellEqual returns true iff two cells render identically. The diff
// emitter uses this to decide whether a cell needs to be repainted.
func cellEqual(a, b tuiCell) bool {
	return a.ch == b.ch &&
		a.fg == b.fg && a.bg == b.bg &&
		a.bold == b.bold && a.italic == b.italic &&
		a.underline == b.underline && a.strike == b.strike &&
		a.overline == b.overline && a.reverse == b.reverse
}

// paintDiff emits the minimum ANSI sequence to transform `prev` into
// `next`. First frame (prev == nil) does a full paint. Resize (size
// mismatch) also triggers a full paint plus a leading clear so the
// terminal state can't show stale cells around the new frame's edges.
//
// Algorithm: walk row by row, find runs of consecutive changed cells,
// emit `\e[r;cH<sgr>cells\e[0m` per run. Adjacent unchanged cells
// don't get repainted. Cursor positioning is 1-based per ANSI spec.
//
// The returned string is meant to be fmt.Print'd.
func paintDiff(prev, next [][]tuiCell) string {
	var sb strings.Builder
	full := prev == nil ||
		len(prev) != len(next) ||
		(len(prev) > 0 && len(prev[0]) != len(next[0]))
	if full {
		sb.WriteString(tuiClearScreen)
		sb.WriteString(tuiCursorHome)
	}
	for r := 0; r < len(next); r++ {
		row := next[r]
		var prevRow []tuiCell
		if !full && r < len(prev) {
			prevRow = prev[r]
		}
		c := 0
		for c < len(row) {
			// Skip unchanged cells when we have a prev to compare.
			if !full && c < len(prevRow) && cellEqual(prevRow[c], row[c]) {
				c++
				continue
			}
			// Start of a changed run — find its end.
			runStart := c
			for c < len(row) {
				if !full && c < len(prevRow) && cellEqual(prevRow[c], row[c]) {
					break
				}
				c++
			}
			runEnd := c // exclusive
			// Emit cursor positioning + the run.
			fmt.Fprintf(&sb, "\x1b[%d;%dH", r+1, runStart+1)
			lastStyle := ""
			for i := runStart; i < runEnd; i++ {
				// Continuation cell from a wide character (paintText /
				// paintInputLine set ch="" for the second half of CJK /
				// emoji glyphs). The terminal's cursor was advanced
				// 2 columns by the wide char, so we must NOT emit
				// anything for the next cell — emitting even an
				// empty SGR or the prior cell's style would re-trigger
				// cursor activity and desync the row layout.
				if row[i].ch == "" {
					continue
				}
				s := cellStyleSGR(row[i])
				if s != lastStyle {
					sb.WriteString("\x1b[0m")
					if s != "" {
						sb.WriteString(s)
					}
					lastStyle = s
				}
				sb.WriteString(row[i].ch)
			}
			sb.WriteString("\x1b[0m")
		}
	}
	return sb.String()
}

func cellStyleSGR(c tuiCell) string {
	if c.ch == " " && !c.fg.set && !c.bg.set && !c.bold && !c.italic &&
		!c.underline && !c.strike && !c.overline && !c.reverse {
		return ""
	}
	var parts []string
	if c.bold {
		parts = append(parts, "1")
	}
	if c.italic {
		parts = append(parts, "3")
	}
	if c.underline {
		parts = append(parts, "4")
	}
	if c.reverse {
		parts = append(parts, "7")
	}
	if c.strike {
		parts = append(parts, "9")
	}
	if c.overline {
		parts = append(parts, "53")
	}
	// NO_COLOR support (https://no-color.org). When enabled by env
	// var, suppress fg/bg colour codes but keep bold / underline /
	// reverse / italic / strike — those convey emphasis and focus
	// state, which the spec explicitly considers separate from
	// "colour" output.
	if !tuiNoColor {
		if c.fg.set {
			parts = append(parts, fmt.Sprintf("38;2;%d;%d;%d", c.fg.r, c.fg.g, c.fg.b))
		}
		if c.bg.set {
			parts = append(parts, fmt.Sprintf("48;2;%d;%d;%d", c.bg.r, c.bg.g, c.bg.b))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "\x1b[" + strings.Join(parts, ";") + "m"
}

func tuiPaint(frame string) {
	fmt.Print(frame)
}
