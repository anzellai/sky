// Sky.Tui — full-screen terminal UI backend.
//
// A Sky.Tui program follows the same TEA shape as Sky.Live / Sky.Cli
// (init / update / view / subscriptions), with two TUI-specific tweaks:
//
//   - view : Model -> String          — full-screen render, repainted
//                                       on every Msg
//   - onKey : Key -> Msg              — translate a keypress to a Msg
//
// Compared to Sky.Cli (line-oriented, blocking ReadLine, view = prompt),
// Sky.Tui uses raw mode + alt-screen so:
//
//   - Every keystroke fires immediately (no Enter required)
//   - Arrow keys / function keys decode to typed Key constructors
//   - The view OWNS the screen — clear-and-redraw between frames
//   - Sub.every actually drives visible animation (clocks, spinners)
//
// On exit (Quit Msg → user-initiated, or Ctrl-C from outside the
// program), terminal state is restored and the alt-screen is dropped
// so the user's shell prompt comes back as if the program were never
// there. Panics inside dispatch are recovered so a Sky-side bug can't
// leave the terminal in raw mode.
//
// Sky-side surface:
//
//   type alias KeyEvent =
//       { kind : String       -- "char" | "enter" | "escape" | ...
//       , value : String      -- for "char": the char; "ctrl": the letter
//       }
//
//   main =
//       Tui.program
//           { init = init
//           , update = update
//           , view = view
//           , subscriptions = subscriptions
//           , onKey = onKey
//           }
//           |> Task.run
//
// Why a record instead of a Sky ADT (KeyChar / KeyEnter / KeyCtrl)?
// The runtime would need to look up user-defined constructors by name
// to synthesise Sky-side values, and the codegen doesn't yet expose a
// "construct ADT value by source-name + args" hook. A record is dead
// simple from Go (`map[string]any{"Kind": k, "Value": v}` lands as
// a Sky record literal at the boundary), and pattern-matching on
// `(k.kind, k.value)` is ergonomic enough.
//
// If/when codegen gains a ctor registry, the surface can graduate to a
// typed `Key` ADT without changing the runtime's decoding logic — only
// the `tuiKeyToSky` shim swaps record-build for ctor-call.
//
// Key kind strings (the runtime constants):
//
//   "char"      — printable rune: value is the character ("a", "?", "é")
//   "enter"     — Enter / Return key
//   "escape"    — Esc key, alone (not start of a CSI sequence)
//   "backspace" — Backspace / Delete-back
//   "tab"       — Tab key
//   "space"     — Space bar
//   "up"        — Arrow Up (CSI A)
//   "down"      — Arrow Down (CSI B)
//   "right"     — Arrow Right (CSI C)
//   "left"      — Arrow Left (CSI D)
//   "ctrl"      — Ctrl-<letter>: value is the lowercase letter ("c", "z")
//   "other"     — escape sequence we didn't decode: value is the raw
//                 byte string

package rt

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"unicode/utf8"

	"golang.org/x/term"
)

const (
	tuiAltScreenEnter = "\x1b[?1049h"
	tuiAltScreenExit  = "\x1b[?1049l"
	tuiCursorHome     = "\x1b[H"
	tuiClearScreen    = "\x1b[2J"
	tuiHideCursor     = "\x1b[?25l"
	tuiShowCursor     = "\x1b[?25h"
)

// keyEvent is the Go-side representation of a decoded keypress.
// It maps to a Sky Key value via tuiKeyToSky.
//
// Modifier bits are populated by the decoder for sequences that
// carry them (CSI 1;<mod><letter> for modified arrows / F-keys,
// CSI <num>;<mod>~ for modified Home/End/etc.). For unmodified
// keys all three modifier flags are false.
type keyEvent struct {
	kind  string // "char", "enter", "escape", "backspace", ...
	value string // KeyChar string, KeyCtrl letter, KeyOther escape seq
	shift bool
	alt   bool
	ctrl  bool
}

// Tui_program is the Task-shaped entry point. Calling it returns a
// thunk; Task.run forces it and the program runs until the user
// dispatches a Msg that exits (typically via Cmd.perform System.exit).
func Tui_program(cfg any) any {
	return func() any {
		return tuiProgramRun(cfg)
	}
}

func tuiProgramRun(cfg any) any {
	initFn := Field(cfg, "Init")
	updateFn := Field(cfg, "Update")
	viewFn := Field(cfg, "View")
	onKeyFn := Field(cfg, "OnKey")
	subsFn := Field(cfg, "Subscriptions")
	if initFn == nil || updateFn == nil || viewFn == nil || onKeyFn == nil {
		return Err[any, any](ErrInvalidInput(
			"Tui.program: cfg must define init / update / view / onKey"))
	}

	// Stdin must be a TTY for raw mode. Piped stdin (e.g. testing) is
	// rejected with a useful error rather than mysteriously hanging.
	stdin := os.Stdin
	fd := int(stdin.Fd())
	if !term.IsTerminal(fd) {
		msg := "Tui.program: stdin is not a terminal — use a real TTY (or test the program shape via Sky.Cli)"
		fmt.Fprintln(os.Stderr, msg)
		return Err[any, any](ErrIo(msg))
	}

	oldState, err := term.MakeRaw(fd)
	if err != nil {
		msg := "Tui.program: cannot enter raw mode: " + err.Error()
		fmt.Fprintln(os.Stderr, msg)
		return Err[any, any](ErrIo(msg))
	}

	// Publish state for safeGo's panic recovery + the signal handler.
	state := &tuiState{fd: fd, raw: true, oldState: oldState}
	tuiInstallState(state)
	cleanShutdown := installCleanShutdown()
	defer func() {
		tuiTeardown()
		tuiUninstallState()
		close(cleanShutdown)
	}()

	fmt.Print(tuiAltScreenEnter)
	state.altScreen = true
	fmt.Print(tuiHideCursor)
	state.cursorHidden = true
	fmt.Print(tuiClearScreen)
	fmt.Print(tuiCursorHome)

	// Channels: msgCh is the unified Msg pipe (keys, sub ticks,
	// Cmd.perform results). doneCh signals stdin EOF / fatal error.
	msgCh := make(chan any, 32)
	doneCh := make(chan struct{})

	// Key reader goroutine. Reads bytes, decodes into keyEvents, and
	// translates to Sky Key values pushed to msgCh via onKey.
	safeGo("Tui.program key reader", func() {
		tuiReadKeys(stdin, onKeyFn, msgCh, doneCh)
	})

	// Initial state.
	initRes := SkyCall(initFn, struct{}{})
	model := tupleFirst(initRes)
	if cmd := tupleSecond(initRes); cmd != nil {
		cliRunCmd(cmd, msgCh)
	}

	subMgr := newSubManager(msgCh)
	subMgr.update(subsFn, model)

	tuiRender(viewFn, model)

	for {
		select {
		case msg := <-msgCh:
			model = cliApplyUpdate(updateFn, msg, model, msgCh)
			subMgr.update(subsFn, model)
			tuiRender(viewFn, model)
			continue
		default:
		}
		select {
		case msg := <-msgCh:
			model = cliApplyUpdate(updateFn, msg, model, msgCh)
			subMgr.update(subsFn, model)
			tuiRender(viewFn, model)
		case <-doneCh:
			subMgr.stopAll()
			return Ok[any, any](struct{}{})
		}
	}
}

// tuiRender clears the alt-screen and writes the user's view(model)
// to the top-left. We re-paint the whole screen each frame; ANSI's
// alt-screen + clear keeps the user's real shell scrollback untouched.
//
// Future work: keyed VTree-style diffing to update only changed
// regions. Worth doing for very wide / busy views; not for stopwatch-
// class apps (the redraw cost is tiny compared to the terminal's own
// frame rate).
func tuiRender(viewFn, model any) {
	out := SkyCall(viewFn, model)
	s := ""
	switch v := out.(type) {
	case string:
		s = v
	default:
		s = fmt.Sprintf("%v", out)
	}
	fmt.Print(tuiCursorHome)
	fmt.Print(tuiClearScreen)
	fmt.Print(tuiCursorHome)
	fmt.Print(s)
}

// tuiReadKeys is the key reader goroutine. Reads raw bytes from the
// terminal in raw mode, decodes ANSI escape sequences for arrow / fn
// keys, and dispatches each decoded keypress as a Msg via onKey(key).
//
// Decoding is intentionally minimal — covers the keys most TUI apps
// need. Anything we don't recognise lands as KeyOther <raw> so the
// program can decide how to handle it (typically: ignore).
func tuiReadKeys(stdin *os.File, onKeyFn any, msgCh chan<- any, doneCh chan<- struct{}) {
	buf := make([]byte, 64)
	for {
		n, err := stdin.Read(buf)
		if err != nil {
			close(doneCh)
			return
		}
		if n == 0 {
			continue
		}
		// Decode buf[:n] into one or more keyEvents and dispatch each.
		i := 0
		for i < n {
			ev, consumed := tuiDecodeKey(buf[i:n])
			if consumed == 0 {
				// shouldn't happen — defensive
				break
			}
			i += consumed
			key := tuiKeyToSky(onKeyFn, ev)
			if key == nil {
				continue
			}
			msg := SkyCall(onKeyFn, key)
			if msg != nil {
				msgCh <- msg
			}
		}
	}
}

// tuiDecodeKey reads one keypress from buf. Returns the keyEvent and
// the number of bytes consumed. Recognises:
//
//   - Plain UTF-8 char        (KeyChar)
//   - 0x0d / 0x0a             (KeyEnter)
//   - 0x1b                    (KeyEscape — when alone)
//   - 0x7f / 0x08             (KeyBackspace)
//   - 0x09                    (KeyTab)
//   - 0x20                    (KeySpace)
//   - 0x01..0x1a (except 9/10/13/27)  (KeyCtrl <letter>)
//   - ESC [ A/B/C/D           (Up / Down / Right / Left)
//   - ESC O P/Q/R/S           (F1..F4)         — emitted as KeyOther for now
//   - ESC [ <num> ~           (Home/End/Insert/Delete) — KeyOther for now
//
// Anything else: KeyOther of the raw bytes.
func tuiDecodeKey(buf []byte) (keyEvent, int) {
	if len(buf) == 0 {
		return keyEvent{kind: "other", value: ""}, 0
	}
	b := buf[0]
	switch {
	case b == 0x0d || b == 0x0a:
		return keyEvent{kind: "enter"}, 1
	case b == 0x09:
		return keyEvent{kind: "tab"}, 1
	case b == 0x20:
		return keyEvent{kind: "space"}, 1
	case b == 0x7f || b == 0x08:
		return keyEvent{kind: "backspace"}, 1
	case b == 0x1b:
		// Escape sequence or solo Esc.
		if len(buf) == 1 {
			return keyEvent{kind: "escape"}, 1
		}
		// CSI: ESC [
		if buf[1] == '[' && len(buf) >= 3 {
			// SGR mouse: ESC [ < button ; col ; row M (press) or m (release)
			// Encoded as kind="mouse" value="<button>:<col>:<row>:<M|m>".
			if buf[2] == '<' {
				end := 3
				for end < len(buf) {
					c := buf[end]
					end++
					if c == 'M' || c == 'm' {
						body := string(buf[3 : end-1]) // "button;col;row"
						kind := byte('M')
						if c == 'm' {
							kind = 'm'
						}
						return keyEvent{kind: "mouse", value: body + ":" + string(kind)}, end
					}
				}
				return keyEvent{kind: "other", value: string(buf[:end])}, end
			}
			// CSI 1;<mod><letter> form: modifier-prefixed arrow / Home /
			// End / F-keys. e.g. `\x1b[1;5C` = Ctrl-Right (used for
			// word-jump in input editors), `\x1b[1;2H` = Shift-Home.
			// The modifier byte's bits encode: 1=base, +1=Shift,
			// +2=Alt, +4=Ctrl, +8=Meta. We map the common cases.
			if buf[2] == '1' && len(buf) >= 6 && buf[3] == ';' {
				mod := buf[4]
				final := buf[5]
				ev := keyEvent{}
				switch final {
				case 'A':
					ev.kind = "up"
				case 'B':
					ev.kind = "down"
				case 'C':
					ev.kind = "right"
				case 'D':
					ev.kind = "left"
				case 'H':
					ev.kind = "home"
				case 'F':
					ev.kind = "end"
				case 'P':
					ev.kind = "fn"
					ev.value = "1"
				case 'Q':
					ev.kind = "fn"
					ev.value = "2"
				case 'R':
					ev.kind = "fn"
					ev.value = "3"
				case 'S':
					ev.kind = "fn"
					ev.value = "4"
				}
				if ev.kind != "" {
					switch mod {
					case '2':
						ev.shift = true
					case '3':
						ev.alt = true
					case '4':
						ev.shift = true
						ev.alt = true
					case '5':
						ev.ctrl = true
					case '6':
						ev.shift = true
						ev.ctrl = true
					case '7':
						ev.alt = true
						ev.ctrl = true
					case '8':
						ev.shift = true
						ev.alt = true
						ev.ctrl = true
					}
					return ev, 6
				}
			}
			// CSI ~ form: ESC [ <num> ~ → Home/End/Insert/Delete/PageUp/PageDown/F-keys.
			if buf[2] >= '0' && buf[2] <= '9' {
				end := 3
				for end < len(buf) {
					c := buf[end]
					end++
					if c == '~' {
						num := string(buf[2 : end-1])
						switch num {
						case "1", "7":
							return keyEvent{kind: "home"}, end
						case "4", "8":
							return keyEvent{kind: "end"}, end
						case "3":
							return keyEvent{kind: "delete"}, end
						case "5":
							return keyEvent{kind: "pageup"}, end
						case "6":
							return keyEvent{kind: "pagedown"}, end
						case "11", "12", "13", "14", "15":
							return keyEvent{kind: "fn", value: num}, end
						case "17", "18", "19", "20", "21", "23", "24":
							return keyEvent{kind: "fn", value: num}, end
						case "200":
							// Bracketed paste START. The reader-goroutine
							// state-machine in tui_ui.go aggregates bytes
							// until the matching 201~ end marker. We
							// emit a marker event here so the goroutine
							// can switch into paste-aggregation mode.
							return keyEvent{kind: "paste-start"}, end
						case "201":
							return keyEvent{kind: "paste-end"}, end
						}
						return keyEvent{kind: "other", value: string(buf[:end])}, end
					}
					if c >= 0x40 && c <= 0x7e {
						break
					}
				}
				return keyEvent{kind: "other", value: string(buf[:end])}, end
			}
			switch buf[2] {
			case 'A':
				return keyEvent{kind: "up"}, 3
			case 'B':
				return keyEvent{kind: "down"}, 3
			case 'C':
				return keyEvent{kind: "right"}, 3
			case 'D':
				return keyEvent{kind: "left"}, 3
			case 'H':
				return keyEvent{kind: "home"}, 3
			case 'F':
				return keyEvent{kind: "end"}, 3
			}
			// Unrecognised CSI — consume up to the final byte (in
			// 0x40..0x7e) so we don't leave dangling bytes for the
			// next read to mistake as separate keys.
			end := 2
			for end < len(buf) {
				c := buf[end]
				end++
				if c >= 0x40 && c <= 0x7e {
					break
				}
			}
			return keyEvent{kind: "other", value: string(buf[:end])}, end
		}
		// SS3: ESC O — F-keys on some terminals (xterm-style F1-F4).
		// Maps the 4 main function keys; everything else stays
		// "other" for the user's onKey to pattern-match if needed.
		if buf[1] == 'O' && len(buf) >= 3 {
			switch buf[2] {
			case 'P':
				return keyEvent{kind: "fn", value: "1"}, 3
			case 'Q':
				return keyEvent{kind: "fn", value: "2"}, 3
			case 'R':
				return keyEvent{kind: "fn", value: "3"}, 3
			case 'S':
				return keyEvent{kind: "fn", value: "4"}, 3
			}
			return keyEvent{kind: "other", value: string(buf[:3])}, 3
		}
		// Just an Esc with stray bytes — treat as solo Esc, leave
		// the rest for the next decode.
		return keyEvent{kind: "escape"}, 1
	case b >= 0x01 && b <= 0x1a:
		// Ctrl-A .. Ctrl-Z (excluding Tab, LF, CR which we handled
		// above as named keys, and ESC which is 0x1b).
		letter := string(rune('a' + b - 1))
		return keyEvent{kind: "ctrl", value: letter}, 1
	}
	// Plain UTF-8 char.
	r, size := utf8.DecodeRune(buf)
	if r == utf8.RuneError && size == 1 {
		return keyEvent{kind: "other", value: string(buf[:1])}, 1
	}
	return keyEvent{kind: "char", value: string(r)}, size
}

// tuiKeyToSky converts a keyEvent into the type onKey expects as its
// first parameter. Sky's typed codegen emits a real Go struct
// (e.g. `Main_KeyEvent_R{Kind: ..., Value: ...}`) for record type
// aliases, so we can't pass a generic `map[string]any` — SkyCall's
// reflect.Call would reject it.
//
// Mirrors live.go's decodeMsgArg trick: marshal the keyEvent as JSON,
// then json.Unmarshal into a freshly-allocated instance of the
// function's expected parameter type. Go's json.Unmarshal is case-
// insensitive for struct field matching, so Sky-side `{kind,value}`
// lands correctly in the codegen's `Kind`/`Value` fields without us
// needing to know the exact type name.
//
// Fallback: if onKey's first param is `interface{}` (untyped curried
// lambda — happens when the user inlines `\\k -> ...` without a type
// alias), we pass a `map[string]any` with both casings.
func tuiKeyToSky(onKeyFn any, ev keyEvent) any {
	payload, _ := json.Marshal(map[string]any{
		"kind":  ev.kind,
		"value": ev.value,
		"shift": ev.shift,
		"alt":   ev.alt,
		"ctrl":  ev.ctrl,
	})

	rv := reflect.ValueOf(onKeyFn)
	if rv.Kind() == reflect.Func && rv.Type().NumIn() > 0 {
		paramT := rv.Type().In(0)
		if paramT.Kind() != reflect.Interface {
			ptr := reflect.New(paramT)
			if err := json.Unmarshal(payload, ptr.Interface()); err == nil {
				return ptr.Elem().Interface()
			}
		}
	}
	return map[string]any{
		"Kind":  ev.kind,
		"Value": ev.value,
		"Shift": ev.shift,
		"Alt":   ev.alt,
		"Ctrl":  ev.ctrl,
		"kind":  ev.kind,
		"value": ev.value,
		"shift": ev.shift,
		"alt":   ev.alt,
		"ctrl":  ev.ctrl,
	}
}
