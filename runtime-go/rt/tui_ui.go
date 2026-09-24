//go:build !js

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
	"io"
	"math"
	"os"
	"strconv"
	"strings"

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
	isInput      bool   // an editable control (tag input / textarea)
	inputType    string // "text" | "password" | "checkbox" | "radio" | "range" | "textarea" | …
	initialValue string // from AttrAttribute "value"
	placeholder  string // from AttrAttribute "placeholder", shown on empty buffer
	row, col     int    // top-left corner of the focused element's box
	w, h         int
	// key is the element's stable identity across renders (tuiFocusKey):
	// focus and editor state follow the ELEMENT, not its position in the
	// tab order, so a list that shrinks in the background does not move
	// focus onto a different item.
	key      string
	tag      string
	name     string   // AttrAttribute "name" — the form field it feeds
	min, max string   // range inputs
	step     string   // range inputs
	form     *tuiForm // enclosing Ui.form with an onSubmit, if any
}

// tuiForm is one Ui.form with an onSubmit handler, collected during paint.
// fields lists its named controls in document order.
type tuiForm struct {
	submit any // eventPair{name: "submit", msg: handler}
	fields []tuiFormField
}

type tuiFormField struct {
	name      string
	key       string
	inputType string
	valueAttr string
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
	buffer        string
	cursor        int    // rune index 0..len([]rune(buffer))
	lastValueAttr string // detect user-driven resets (model.draft = "")
}

// inputRegistry maps a focusable's stable identity (focusable.key) to its
// editor state, so a buffer stays with its input when other elements are
// added or removed. It also carries the per-frame paint context: the
// identity occurrence counter and the stack of enclosing forms.
type inputRegistry struct {
	inputs   map[string]*tuiInput
	keyCount map[string]int
	forms    []*tuiForm
}

func newInputRegistry() *inputRegistry {
	return &inputRegistry{inputs: map[string]*tuiInput{}, keyCount: map[string]int{}}
}

func (r *inputRegistry) get(key string) *tuiInput {
	if r.inputs[key] == nil {
		r.inputs[key] = &tuiInput{}
	}
	return r.inputs[key]
}

// beginFrame resets the per-frame paint context.
func (r *inputRegistry) beginFrame() {
	r.keyCount = map[string]int{}
	r.forms = nil
}

// nextKey returns identity + an occurrence suffix, so two identical
// elements in one frame still get distinct, order-stable keys.
func (r *inputRegistry) nextKey(identity string) string {
	n := r.keyCount[identity]
	r.keyCount[identity] = n + 1
	return fmt.Sprintf("%s#%d", identity, n)
}

func (r *inputRegistry) currentForm() *tuiForm {
	if len(r.forms) == 0 {
		return nil
	}
	return r.forms[len(r.forms)-1]
}

// ─── Main loop ──────────────────────────────────────────────────────

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

	// Wire tracing so a Tui app's Db / Http / File / Msg spans are
	// captured (OTLP export when OTEL_EXPORTER_OTLP_ENDPOINT is set;
	// otherwise the in-process ring). Non-fatal on failure — same as
	// the Sky.Live / Sky.Http.Server startup path.
	if err := InitTracingFromEnv(); err != nil {
		fmt.Fprintf(os.Stderr,
			"[sky.tui] OTel init failed (continuing without trace export): %v\n", err)
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

	return tuiAppLoop(cfg, fd, canvas, initFn, updateFn, viewFn, subsFn, onKeyFn, guardFn)
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

// tuiFocusChangeMsgs returns the onBlur Msg of the old focused element and
// the onFocus Msg of the new one (when bound), in that order. The loop
// queues them locally, so a busy Msg channel can never drop them.
func tuiFocusChangeMsgs(focusables []focusable, oldIdx, newIdx int) []any {
	if oldIdx == newIdx {
		return nil
	}
	var out []any
	if oldIdx >= 0 && oldIdx < len(focusables) {
		if blurEvt := focusableEvent(focusables[oldIdx], "blur"); blurEvt != nil {
			if msg := tuiExtractClickMsg(blurEvt); msg != nil {
				out = append(out, msg)
			}
		}
	}
	if newIdx >= 0 && newIdx < len(focusables) {
		if focusEvt := focusableEvent(focusables[newIdx], "focus"); focusEvt != nil {
			if msg := tuiExtractClickMsg(focusEvt); msg != nil {
				out = append(out, msg)
			}
		}
	}
	return out
}

// focusableEvent returns the eventPair on `f` matching the given name
// ("click", "input", "change", ...). Std.Html.Events' on* builders
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
	// No lastValueAttr sync here: paintBox only adopts a value attr that
	// CHANGED and differs from the buffer (see the input branch there),
	// so the edit survives both the model's echo and an uncontrolled
	// input whose value attr never changes.
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

// tuiExtractClickMsg pulls the Msg out of a Std.Html.Events event
// value. onClick produces an `eventPair{name, msg}` (see live.go);
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
		// The view scrolls, so the root's height is content-sized; a
		// root `height fill` fills the viewport (rootFillH).
		indefH:    true,
		rootFillH: rows,
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
	inputs.beginFrame()
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
	// indefH marks the available height as NOT definite: the parent is
	// sized by its content (no explicit height), or this is the root of a
	// scrollable view. A vertical `fill` then resolves to the content
	// height, as elm-ui / CSS resolve fill inside an auto-height parent.
	// Pre-fix a fill height claimed the whole 50,000-row layout budget
	// (Ui.width on an Input hoists an implicit fill to the control).
	indefH bool
	// rootFillH is the definite height a `fill` height resolves to at the
	// ROOT element only: the terminal viewport, so a full-screen layout
	// (`column [height fill] [header, body fill, footer]`) fills the
	// screen. It is zero for every element below the root.
	rootFillH int
}

// lengthIsFill reports a Fill length, bare or wrapped in Min / Max.
func lengthIsFill(v any) bool {
	_, tag, fields, ok := unwrapADTShape(v)
	if !ok {
		return false
	}
	switch tag {
	case 2:
		return true
	case 3, 4:
		if len(fields) >= 2 {
			return lengthIsFill(fields[1])
		}
	}
	return false
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
	idAttr      string // AttrAttribute "id" — a stable focus identity
	minAttr     string // range inputs: AttrAttribute "min"
	maxAttr     string // range inputs: AttrAttribute "max"
	stepAttr    string // range inputs: AttrAttribute "step"
	inputType   string // "text" | "password" | "checkbox" | "radio" | "range" | "textarea" | …
	children    []layoutBox
	wrapped     bool      // wrappedRow flag — children break into rows
	paragraph   bool      // paragraph flag — text children word-wrap
	textColumn  bool      // textColumn flag — reading-width column
	gridLayout  bool      // grid flag — children flow into auto NxM grid
	gridColumns int       // columns for grid (0 = auto)
	clip        [2]bool   // [clipX, clipY]
	overflow    [2]string // [x, y] — "clip", "scrollbars", ""
	nearby      []nearbyEntry
	borderWidth [4]int // top, right, bottom, left — 1 cell each if border present
	borderColor tuiColor
	borderStyle string // "solid" | "dashed" | "dotted"
}

// layoutElement walks one Element node + computes its box for the
// given parent constraints. Recursive.
//
//	maxW, maxH: parent-imposed upper bounds in cells
//	parentAxis: how this element is being laid out by its parent
func layoutElement(elem any, ctx tuiLayoutCtx, maxW, maxH int, parentAxis layoutAxis) layoutBox {
	_, tag, fields, ok := unwrapADTShape(elem)
	if !ok {
		return layoutBox{kind: "empty"}
	}
	switch tag {
	case 0: // Empty
		return layoutBox{kind: "empty"}
	case 1: // Text s
		s := ""
		if len(fields) > 0 {
			if str, ok := fields[0].(string); ok {
				s = str
			}
		}
		return layoutBox{kind: "text", text: s, width: runeLen(s), height: 1}
	case 2: // Node desc attrs children
		return layoutNode("", fields, ctx, maxW, maxH, parentAxis)
	case 3: // TaggedNode tag desc attrs children
		ntag := ""
		if len(fields) > 0 {
			if s, ok := fields[0].(string); ok {
				ntag = s
			}
		}
		// Skip first field (tag); the rest mirror Node's layout.
		return layoutNode(ntag, fields[1:], ctx, maxW, maxH, parentAxis)
	case 4: // Raw node — the Std.Html node's text content (markup cannot draw)
		s := ""
		if len(fields) > 0 {
			s = tuiRawText(fields[0])
		}
		if s == "" {
			return layoutBox{kind: "empty"}
		}
		return layoutBox{kind: "text", text: s, width: runeLen(s), height: 1}
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
	if tag == "textarea" && la.inputType == "" {
		la.inputType = "textarea"
	}

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
	var height int
	var hasExplicitH bool
	if ctx.indefH && lengthIsFill(la.height) {
		if ctx.rootFillH > 0 {
			// Root: fill the terminal viewport.
			avail := ctx.rootFillH - la.padding[0] - la.padding[2] - la.borderWidth[0] - la.borderWidth[2]
			if avail > innerMaxH {
				avail = innerMaxH
			}
			if avail < 0 {
				avail = 0
			}
			height, hasExplicitH = resolveLengthCells(la.height, "y", avail, ctx)
		} else {
			// Fill inside a content-sized parent: size to content. A
			// `Min n fill` still honours its floor.
			if _, ltag, lfields, ok := unwrapADTShape(la.height); ok && ltag == 3 && len(lfields) >= 1 {
				height, hasExplicitH = intOf(lfields[0]), true
			}
		}
	} else {
		height, hasExplicitH = resolveLengthCells(la.height, "y", innerMaxH, ctx)
	}
	if !hasExplicitW {
		width = innerMaxW
	}
	if !hasExplicitH {
		height = innerMaxH
	}
	// Children see a definite height only when this node's height is
	// explicit (a length, or a fill resolved against a definite parent).
	ctx.indefH = !hasExplicitH
	ctx.rootFillH = 0

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
			idAttr:      la.idAttr,
			minAttr:     la.minAttr,
			maxAttr:     la.maxAttr,
			stepAttr:    la.stepAttr,
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
			idAttr:      la.idAttr,
			minAttr:     la.minAttr,
			maxAttr:     la.maxAttr,
			stepAttr:    la.stepAttr,
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
		if isInputTag(tag) {
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
		if isInputTag(tag) {
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
		idAttr:      la.idAttr,
		minAttr:     la.minAttr,
		maxAttr:     la.maxAttr,
		stepAttr:    la.stepAttr,
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
		if axis == layoutAxisColumn && ctx.indefH {
			// A content-sized column has no spare height to hand out:
			// its fill children are sized by their content.
			fillN = 0
		}
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
	_, tag, fields, ok := unwrapADTShape(child)
	if !ok {
		return 0
	}
	if tag != 2 && tag != 3 {
		return 0
	}
	var attrs []any
	switch tag {
	case 2:
		if len(fields) >= 2 {
			attrs = asList(fields[1])
		}
	case 3:
		if len(fields) >= 3 {
			attrs = asList(fields[2])
		}
	}
	for _, a := range attrs {
		_, atag, afields, ok := unwrapADTShape(a)
		if !ok {
			continue
		}
		// AttrWidth = tag 1, AttrHeight = tag 2 (per Std.Ui.Attribute order)
		switch {
		case axis == layoutAxisRow && atag == 1:
			if p := lengthFillPortion(afields); p > 0 {
				return p
			}
		case axis == layoutAxisColumn && atag == 2:
			if p := lengthFillPortion(afields); p > 0 {
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
	_, ltag, lfields, ok := unwrapADTShape(fields[0])
	if !ok {
		return 0
	}
	// Length.Fill = tag 2 (per Length ADT order: Px=0, Content=1, Fill=2, Min=3, Max=4, Vh=5, Vw=6)
	if ltag == 2 && len(lfields) > 0 {
		if n, ok := lfields[0].(int); ok {
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
	width        any // raw Length value
	height       any
	padding      [4]int // top, right, bottom, left in cells
	spacing      int
	fg, bg       tuiColor
	bold         bool
	italic       bool
	underline    bool
	strike       bool   // text-decoration: line-through
	overline     bool   // text-decoration: overline (SGR 53)
	textAlign    string // "left" | "center" | "right" — for text painting within box
	alignX       string // "" (unset, default left/main-axis), "left", "center", "right"
	alignY       string // "" (unset), "top", "center", "bottom"
	isRow        bool
	isWrappedRow bool
	isParagraph  bool
	isTextColumn bool
	isGrid       bool
	gridColumns  int
	clip         [2]bool       // [clipX, clipY]
	overflow     [2]string     // [x, y] — "", "clip", "scrollbars"
	nameAttr     string        // AttrAttribute "name" — for form-submit collection
	idAttr       string        // AttrAttribute "id"
	minAttr      string        // AttrAttribute "min" (range)
	maxAttr      string        // AttrAttribute "max" (range)
	stepAttr     string        // AttrAttribute "step" (range)
	inputType    string        // AttrAttribute "type" — text/password/checkbox/radio/range/textarea/etc.
	nearby       []nearbyEntry // captured AttrNearby items
	events       []any         // every AttrEvent payload
	valueAttr    string        // AttrAttribute "value" — initial value for inputs
	placeholder  string        // AttrAttribute "placeholder" — shown on empty input
	borderWidth  [4]int        // top, right, bottom, left — 1 if border present, 0 otherwise
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
		aname, atag, afields, ok := unwrapADTShape(a)
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
		switch atag {
		case 0: // NoAttribute
			continue
		case 1: // AttrWidth Length
			if len(afields) > 0 {
				out.width = afields[0]
			}
		case 2: // AttrHeight Length
			if len(afields) > 0 {
				out.height = afields[0]
			}
		case 3: // AttrAlignX HAlign — HAlign tags: 0=Left, 1=CenterX, 2=Right
			if len(afields) > 0 {
				if _, ahtag, _, ok := unwrapADTShape(afields[0]); ok {
					switch ahtag {
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
			if len(afields) > 0 {
				if _, avtag, _, ok := unwrapADTShape(afields[0]); ok {
					switch avtag {
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
			if len(afields) >= 2 {
				if _, loctag, _, ok := unwrapADTShape(afields[0]); ok {
					out.nearby = append(out.nearby, nearbyEntry{location: loctag, elem: afields[1]})
				}
			}
		case 6: // AttrPadding T R B L
			if len(afields) >= 4 {
				out.padding[0] = pxToCellsY(intOf(afields[0]), ctx)
				out.padding[1] = pxToCellsX(intOf(afields[1]), ctx)
				out.padding[2] = pxToCellsY(intOf(afields[2]), ctx)
				out.padding[3] = pxToCellsX(intOf(afields[3]), ctx)
			}
		case 7: // AttrSpacing N
			if len(afields) > 0 {
				out.spacing = pxToCellsX(intOf(afields[0]), ctx)
			}
		case 8: // AttrStyle "k" "v" — sentinel for row/col/wrap/grid + raw CSS escape
			if len(afields) >= 2 {
				k, _ := afields[0].(string)
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
					if v, ok := afields[1].(string); ok {
						fmt.Sscanf(v, "%d", &out.gridColumns)
					}
				case "__gridTracks":
					// Std.Ui.Grid.tracks / Grid.columns / Grid.rows emit
					// explicit CSS-grid track lists (`fr` / `px` / `auto`
					// / `minmax` / `repeatAutoFit`). The terminal has no
					// fractional / minmax cells — collapse to a flat
					// content-grid fallback and warn so users know the
					// proportions won't survive.
					tuiWarn("layout", "explicit grid tracks (terminal can't render fr/minmax/auto)")
				case "aspect-ratio":
					// Std.Ui.aspectRatio / aspectRatioWH / square /
					// widescreen / fullHd / cinemascope emit
					// `aspect-ratio: W / H`. The terminal grid is
					// driven by parent cell allocation, not CSS ratio.
					tuiWarn("layout", "aspect-ratio (terminal cells don't honour CSS aspect-ratio)")
				default:
					// User-supplied raw CSS — TUI can't render, warn once.
					if !isInternalMarker(k) {
						tuiWarn("style", "raw CSS attribute "+k)
					} else {
						// Unknown __-prefixed marker — Std.Ui added a
						// sentinel TUI hasn't ported. Warn so the gap is
						// visible (we don't want to silently drop a layout
						// directive).
						tuiWarn("layout", "unsupported Std.Ui marker "+k)
					}
				}
			}
		case 9: // AttrDescribe Description — accessibility hints; tag-specific styling handled in layoutNode
			// Description content is consumed by tagForDescription / pickSemanticTag
			// at layoutElement time. No attr-level work here.
		case 10: // AttrClass — CSS class, ignored in TUI by design
			tuiWarn("style", "AttrClass (CSS classes don't apply in terminal)")
		case 11: // AttrEvent — event payload. Two shapes accepted:
			//   * Sky-source Layer-3 form (v0.13+): Fields[0] is a
			//     `Std.Html.Attributes.Attribute_EventAttr` SkyADT
			//     whose Fields[0] in turn is an `Event` SkyADT
			//     (`OnMsg name msg`, `OnString name fn`, etc.).
			//     The Event's Fields[0]=name (string), Fields[1]=msg
			//     or handler.
			//   * Legacy Go-kernel form: Fields[0] is a raw
			//     `eventPair{name, msg}` struct (pre-Layer-3 kernel
			//     output).
			// The TUI's focusableEvent expects eventPair, so we
			// normalise Layer-3 SkyADTs into eventPair here. Without
			// this, typed Std.Ui apps using v0.13 Sky-source events
			// render correctly but fire NO key events — every
			// keystroke / mouse click silently drops because the
			// type assertion in focusableEvent silently rejects the
			// SkyADT shape.
			if len(afields) > 0 {
				payload := unwrapAny(afields[0])
				if ep, ok := payload.(eventPair); ok {
					out.events = append(out.events, ep)
				} else if _, _, evAttrFields, ok := unwrapADTShape(payload); ok && len(evAttrFields) >= 1 {
					// EventAttr wrapping Event — unwrap once more.
					if _, _, innerFields, ok := unwrapADTShape(unwrapAny(evAttrFields[0])); ok && len(innerFields) >= 2 {
						name, _ := innerFields[0].(string)
						out.events = append(out.events, eventPair{
							name: name,
							msg:  innerFields[1],
						})
					}
				}
			}
		case 12: // AttrAttribute "k" "v" — raw HTML attr; we read "value"/"placeholder"/"name"
			if len(afields) >= 2 {
				k, _ := afields[0].(string)
				v, _ := afields[1].(string)
				switch k {
				case "value":
					out.valueAttr = v
				case "placeholder":
					out.placeholder = v
				case "name":
					out.nameAttr = v
				case "type":
					out.inputType = v
				case "id":
					out.idAttr = v
				case "min":
					out.minAttr = v
				case "max":
					out.maxAttr = v
				case "step":
					out.stepAttr = v
				case "for", "rows", "cols", "spellcheck", "aria-label",
					"required", "disabled", "checked", "readonly", "autofocus",
					"autocomplete", "minlength", "maxlength", "pattern",
					"href", "target", "src", "alt", "accept", "multiple", "selected",
					"sky-nav":
					// Known HTML attrs that don't need TUI rendering — silent skip.
				case "data-sky-pc-rules":
					// Pseudo-class rule payload from
					// `Std.Ui.onPseudo` / `Background.hoverColor` /
					// `focusColor` / `activeColor` / `disabledColor`
					// / `Font.hoverSize` / `Border.hoverColor` etc.
					// The terminal has no :hover / :focus / :active /
					// :disabled CSS pseudo-class engine — every
					// reactive style is silently inert.
					tuiWarn("pseudo-class", ":hover / :focus / :active / :disabled (terminal has no CSS pseudo-class engine — base style still renders)")
				case "data-sky-mq-q":
					// Media query selector payload from
					// `Ui.mediaQuery` / `Ui.breakpoint` (Mobile /
					// Tablet / Desktop / DarkMode / TouchDevice /
					// Portrait / ReducedMotion / Custom). The
					// terminal's "viewport" is the terminal size
					// itself — these directives don't translate.
					tuiWarn("media-query", "@media rule (terminal viewport is fixed — base style still renders)")
				case "data-sky-mq-rules":
					// Companion payload to data-sky-mq-q (the actual
					// CSS rules). Silent — already warned above on
					// `data-sky-mq-q` per (category, detail) dedupe.
				case "data-sky-tr-rules":
					// Typed CSS transition payload from
					// `Std.Ui.Transition.attribute` (e.g.
					// "background-color 200ms ease-out"). The
					// terminal repaints discretely cell-by-cell;
					// there's no inter-frame easing.
					tuiWarn("transition", "CSS transition (terminal can't interpolate between frames)")
				case "data-sky-tr-respect":
					// Companion to data-sky-tr-rules (reduced-motion
					// gate flag). Silent — already warned above.
				case "data-sky-anim-rules":
					// Keyframe animation payload from
					// `Std.Ui.Animation.attribute` (translate /
					// opacity / scale / rotate / skew). The
					// terminal has no per-frame animation loop —
					// the element renders at its final keyframe
					// position only.
					tuiWarn("animation", "@keyframes animation (terminal renders final keyframe only — no per-frame loop)")
				case "data-sky-anim-respect":
					// Companion to data-sky-anim-rules. Silent.
				case "data-sky-path":
					// Sky.Live URL-sync sentinel for history
					// push/replace. Pure browser-history concept —
					// no terminal analogue; safe silent skip.
				case "data-sky-eval":
					// Legacy Sky.Live CSP-incompatible escape
					// hatch (post-patch JS eval). No terminal
					// analogue; safe silent skip.
				default:
					tuiWarn("attribute", "raw HTML attribute "+k)
				}
			}
		case 13: // AttrFontSize — terminal has one cell size
			tuiWarn("font", "size (terminal cells are uniform)")
		case 14: // AttrFontColor Color
			if len(afields) > 0 {
				out.fg = colorOf(afields[0])
			}
		case 15: // AttrFontFamily — terminal font is set by emulator
			tuiWarn("font", "family (terminal font is fixed)")
		case 16: // AttrFontWeight
			if len(afields) > 0 {
				if w, ok := afields[0].(int); ok && w >= 600 {
					out.bold = true
				}
			}
		case 17: // AttrFontItalic
			out.italic = true
		case 18: // AttrFontUnderline
			out.underline = true
		case 19: // AttrFontDecoration String
			if len(afields) > 0 {
				if s, ok := afields[0].(string); ok {
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
			if len(afields) > 0 {
				if s, ok := afields[0].(string); ok {
					out.textAlign = s
				}
			}
		case 23: // AttrBgColor Color
			if len(afields) > 0 {
				out.bg = colorOf(afields[0])
			}
		case 24: // AttrBgImage
			tuiWarn("background", "image (terminals can't render image fills)")
		case 25: // AttrBgGradient
			tuiWarn("background", "gradient (terminals can't render gradient fills)")
		case 26: // AttrBorderWidth Int — uniform border on all sides
			if len(afields) > 0 {
				if w, ok := afields[0].(int); ok && w > 0 {
					out.borderWidth = [4]int{1, 1, 1, 1}
				}
			}
		case 27: // AttrBorderWidthEach T R B L
			if len(afields) >= 4 {
				for i := 0; i < 4; i++ {
					if w, ok := afields[i].(int); ok && w > 0 {
						out.borderWidth[i] = 1
					}
				}
			}
		case 28: // AttrBorderColor Color
			if len(afields) > 0 {
				out.borderColor = colorOf(afields[0])
			}
		case 29: // AttrBorderRounded — no rounded box-drawing in standard Unicode
			tuiWarn("border", "rounded corners (Unicode box-drawing has no rounded chars)")
		case 30: // AttrBorderStyle String — "solid" | "dashed" | "dotted"
			if len(afields) > 0 {
				if s, ok := afields[0].(string); ok {
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
			if len(afields) >= 2 {
				x, _ := afields[0].(string)
				y, _ := afields[1].(string)
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
			tuiWarn("attribute", fmt.Sprintf("unknown attribute tag %d (%s)", atag, aname))
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
	_, tag, fields, ok := unwrapADTShape(v)
	if !ok {
		return tuiColor{}
	}
	if tag != 0 || len(fields) < 3 {
		return tuiColor{}
	}
	r, _ := fields[0].(int)
	g, _ := fields[1].(int)
	b, _ := fields[2].(int)
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
	_, tag, fields, ok := unwrapADTShape(v)
	if !ok {
		return 0, false
	}
	switch tag {
	case 0: // Px Int
		if len(fields) == 0 {
			return 0, false
		}
		px, _ := fields[0].(int)
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
		if len(fields) >= 2 {
			minN, _ := fields[0].(int)
			inner, hasExpl := resolveLengthCells(fields[1], axis, available, ctx)
			if !hasExpl {
				return minN, true
			}
			if inner < minN {
				return minN, true
			}
			return inner, true
		}
	case 4: // Max N Length
		if len(fields) >= 2 {
			maxN, _ := fields[0].(int)
			inner, hasExpl := resolveLengthCells(fields[1], axis, available, ctx)
			if !hasExpl {
				return available, true
			}
			if inner > maxN {
				return maxN, true
			}
			return inner, true
		}
	case 5: // Vh N (viewport-height percent)
		if len(fields) > 0 {
			pct, _ := fields[0].(int)
			return ctx.rows * pct / 100, true
		}
	case 6: // Vw N (viewport-width percent)
		if len(fields) > 0 {
			pct, _ := fields[0].(int)
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
	if !isInputTag(box.tag) && box.borderWidth[0]+box.borderWidth[1]+box.borderWidth[2]+box.borderWidth[3] > 0 {
		drawBorder(grid, col0, row0, w, h, box.borderWidth, box.borderColor, box.borderStyle)
	}

	// If this box is focusable (has any event handler), is an input
	// (always focusable for editing), or is an `<a>` link (matches
	// HTML's intrinsic tab-stop behaviour — `Ui.link` carries no
	// events but should still join the focus order so users can
	// reach it by arrow / Tab and see the focused-link underline).
	isInput := isInputTag(box.tag)
	isLink := box.tag == "a"
	// A Ui.form carrying onSubmit is NOT a tab stop: its submit fires
	// from Enter in one of its single-line inputs or from a
	// type="submit" button inside it (the browser's implicit
	// submission). It opens a form context its named controls join.
	if box.tag == "form" {
		if sub := tuiEventNamed(box.events, "submit"); sub != nil {
			inputs.forms = append(inputs.forms, &tuiForm{submit: sub})
			defer func() { inputs.forms = inputs.forms[:len(inputs.forms)-1] }()
		}
	}
	thisFocusIdx := -1
	if tuiHasFocusEvents(box) || isInput || isLink {
		thisFocusIdx = len(*focusables)
		f := focusable{
			events:       box.events,
			isInput:      isInput,
			inputType:    box.inputType,
			initialValue: box.valueAttr,
			placeholder:  box.placeholder,
			row:          row0, col: col0, w: w, h: h,
			key:  inputs.nextKey(tuiFocusIdentity(box)),
			tag:  box.tag,
			name: box.nameAttr,
			min:  box.minAttr,
			max:  box.maxAttr,
			step: box.stepAttr,
			form: inputs.currentForm(),
		}
		if f.form != nil && isInput && f.name != "" {
			f.form.fields = append(f.form.fields, tuiFormField{
				name: f.name, key: f.key, inputType: f.inputType, valueAttr: box.valueAttr,
			})
		}
		*focusables = append(*focusables, f)
	}

	// Recurse into content area (after padding + border). Inputs
	// suppress border rendering (see drawBorder skip above), so
	// the inner area for inputs is also computed without the
	// border inset — otherwise an input with `Border.width 1`
	// would still consume cells for an invisible border.
	bw := box.borderWidth
	if isInput {
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
		if isInput {
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
				st := inputs.get((*focusables)[focIdx].key)
				// The model changed the value attr. Adopt it unless it is
				// the model echoing the local edit (value == buffer), so
				// the cursor stays put while typing mid-string. An input
				// whose value attr never changes (uncontrolled, or a
				// frame painted before the edit's Msg is applied) keeps
				// the user's buffer.
				if box.valueAttr != st.lastValueAttr {
					if box.valueAttr != st.buffer {
						st.buffer = box.valueAttr
						st.cursor = runeLen(st.buffer)
					}
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

// paintSlider renders ├──●──┤ with the thumb (●) positioned proportional
// to value within [min, max] (AttrAttribute "value" / "min" / "max",
// defaults 0..100 as in HTML).
func paintSlider(grid [][]tuiCell, box layoutBox, col, row, w int, style textStyle, focused bool) {
	if w < 3 {
		paintInputLine(grid, "●", col, row, w, style, false)
		return
	}
	lo, hi, _ := tuiRangeBounds(box.minAttr, box.maxAttr, box.stepAttr)
	v := tuiRangeValue(box.valueAttr, lo, hi)
	thumb := 0
	if hi > lo {
		thumb = int(math.Round((v - lo) / (hi - lo) * float64(w-1)))
	}
	for i := 0; i < w; i++ {
		ch := "─"
		switch {
		case i == thumb:
			ch = "●" // the thumb wins over the end caps
		case i == 0:
			ch = "├"
		case i == w-1:
			ch = "┤"
		}
		paintInputLine(grid, ch, col+i, row, 1, style, false)
	}
	// Focus: reverse video on the thumb (the arrow keys move it).
	if focused && row >= 0 && row < len(grid) && col+thumb >= 0 && col+thumb < len(grid[row]) {
		grid[row][col+thumb].reverse = true
	}
}

// tuiRangeBounds parses a range input's min / max / step with the HTML
// defaults (0, 100, 1).
func tuiRangeBounds(minS, maxS, stepS string) (lo, hi, step float64) {
	lo, hi, step = 0, 100, 1
	if v, err := strconv.ParseFloat(strings.TrimSpace(minS), 64); err == nil {
		lo = v
	}
	if v, err := strconv.ParseFloat(strings.TrimSpace(maxS), 64); err == nil {
		hi = v
	}
	if v, err := strconv.ParseFloat(strings.TrimSpace(stepS), 64); err == nil && v > 0 {
		step = v
	}
	if hi < lo {
		hi = lo
	}
	return lo, hi, step
}

// tuiRangeValue parses a range value, clamped to [lo, hi]; an unparsable
// value sits at the midpoint (the HTML default).
func tuiRangeValue(s string, lo, hi float64) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		v = lo + (hi-lo)/2
	}
	return math.Max(lo, math.Min(hi, v))
}

// tuiFormatRange prints a range value the way the browser reports it:
// integers without a decimal point.
func tuiFormatRange(v float64) string {
	if v == math.Trunc(v) && math.Abs(v) < 1e15 {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
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
//
//	0 Above   row = hostRow - childHeight
//	1 Below   row = hostRow + hostHeight
//	2 OnRight col = hostCol + hostWidth
//	3 OnLeft  col = hostCol - childWidth
//	4 InFront same coords as host (overlay)
//	5 Behind  same coords (handled in caller before children paint)
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
	_, tag, fields, ok := unwrapADTShape(elem)
	if !ok {
		return ""
	}
	switch tag {
	case 0: // Empty
		return ""
	case 1: // Text s
		if len(fields) > 0 {
			if s, ok := fields[0].(string); ok {
				return s
			}
		}
	case 2, 3: // Node / TaggedNode — recurse into children
		childFields := fields
		if tag == 3 && len(childFields) > 0 {
			childFields = childFields[1:] // skip tag
		}
		if len(childFields) >= 3 {
			children := asList(childFields[2])
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
		left, right := innerCol, innerCol+innerW-1
		// The markers may only take BLANK cells. When the label reaches
		// the edge (a button without horizontal padding) a marker would
		// overwrite a label character — or half of a wide glyph, whose
		// continuation cell is "" — so fall back to reverse video, which
		// keeps every label cell intact.
		blank := func(c int) bool { return c >= 0 && c < len(rowCells) && rowCells[c].ch == " " }
		if !blank(left) || !blank(right) {
			applyReverse(grid, col, row, w, h)
			return
		}
		rowCells[left].ch = "▸"
		rowCells[left].bold = true
		rowCells[right].ch = "◂"
		rowCells[right].bold = true
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
	fmt.Fprint(tuiOut, frame)
}

// tuiOut is where frames are written (the terminal; tests discard it).
var tuiOut io.Writer = os.Stdout
