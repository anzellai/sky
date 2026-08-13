// Sky terminal runtime safety net — guarantees the user's shell is
// returned to a usable state regardless of how the program ends.
//
// Why this exists:
//
// Sky.Tui modifies global terminal state (raw mode, alt-screen, hidden
// cursor, mouse tracking, bracketed paste). Without coordination,
// any of the following bypasses the deferred restore in tuiAppRun:
//
//   - panic in a goroutine spawned by Cmd.perform, Sub.every, the
//     SIGWINCH watcher, or the key reader (Go runtime tears down the
//     whole process without running other goroutines' defers)
//   - external SIGTERM / SIGHUP / SIGQUIT (defers DO run on these,
//     but only if the receiving goroutine is the one calling defer)
//   - external SIGINT (raw mode swallows local Ctrl-C as 0x03, but
//     a SIGINT delivered by another shell still terminates us)
//
// All of these used to leave the user's shell stuck in raw mode +
// alt-screen, requiring `reset` typed blind. Confidence-killer.
//
// The fix is two parts:
//
//   1. tuiTeardown() — idempotent restore-everything function. Tracks
//      what we've enabled so it disables them in the right order
//      (mouse tracking off → raw mode restored → cursor shown →
//      alt-screen exited). Safe to call from any goroutine, signal
//      handler, or recover().
//
//   2. safeGo() — wraps `go func()` with defer-recover that runs
//      tuiTeardown before printing the panic + stack and exiting.
//      Replaces every bare `go func() { ... }()` in the runtime so
//      a panic anywhere always lands the user back on a usable shell.
//
// Plus: a single signal-handler goroutine that catches SIGTERM /
// SIGHUP / SIGQUIT / SIGINT and runs tuiTeardown before re-raising
// (via os.Exit with the conventional 128+signum code).
//
// Sky.Cli (line-oriented, no raw mode) registers a no-op tuiState so
// safeGo + signal handler still give it panic recovery + clean
// shutdown, without any TTY modifications to undo.

package rt

import (
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"sync"
	"syscall"

	"golang.org/x/term"
)

// tuiState tracks every terminal modification the runtime has applied
// so tuiTeardown can undo them in the correct order. Fields are set
// when the corresponding ANSI sequence is emitted; tuiTeardown reads
// them all under tuiStateMu.
type tuiState struct {
	fd             int
	raw            bool        // term.MakeRaw was called
	oldState       *term.State // for term.Restore
	altScreen      bool
	cursorHidden   bool
	mouseEnabled   bool
	bracketedPaste bool
}

var (
	tuiStateMu  sync.Mutex
	tuiActive   *tuiState
	tuiTearMu   sync.Mutex
	tuiTornDown bool
)

// tuiInstallState publishes the runtime's terminal-modification state
// so the central teardown + signal handler can find it. Called once,
// at the top of tuiAppRun / tuiProgramRun / cliProgramRun, before any
// goroutines spawn.
func tuiInstallState(s *tuiState) {
	tuiStateMu.Lock()
	tuiActive = s
	tuiTornDown = false // re-arm for sequential test runs
	tuiStateMu.Unlock()
}

// tuiUninstallState clears the published state — called from the
// program's deferred teardown after tuiTeardown has run, so a
// subsequent invocation in the same process (mostly tests) starts
// from a clean slate.
func tuiUninstallState() {
	tuiStateMu.Lock()
	tuiActive = nil
	tuiStateMu.Unlock()
}

// tuiTeardown is idempotent. It restores the terminal to a usable
// state regardless of whether it's called from the main goroutine's
// deferred cleanup, a goroutine's recover() block, or the signal
// handler.
//
// Order matters — and the ordering here is the result of debugging
// "readline messed up after exiting a Sky.Tui app on mosh":
//
//   1. (raw mode, alt-screen)  Disable mouse + bracketed paste —
//      the codes need raw mode so the terminal driver consumes
//      them rather than echoing them to the user.
//   2. (raw mode, alt-screen)  SGR reset (\x1b[m) so any sticky
//      colour / bold / underline doesn't carry across the screen
//      transition.
//   3. (raw mode, alt-screen)  Show cursor — must be before alt-
//      screen exit so the cursor is visible the moment the user's
//      primary screen comes back.
//   4. (raw mode, alt-screen)  Exit alt-screen → user's primary
//      screen content + cursor position are restored.
//   5. (raw mode, PRIMARY screen)  NOW send DECSTR + charset reset
//      + DECCKM/DECPAM resets. These reset state the user's actual
//      shell will inherit. Sending them in step 1-3 instead would
//      affect the alt-screen (which we're about to discard) AND
//      the alt-screen exit on some terminals (notably mosh) does
//      not propagate the resets to the primary screen.
//   6. Restore TTY → cooked mode.
//
// The codes in step 5:
//   \x1b[m       — reset SGR (belt-and-braces)
//   \x0f         — Shift-In: select G0 character set (cancels any
//                  prior \x0e Shift-Out that left G1 active — DEC
//                  special graphics for box drawing)
//   \x1b(B       — Designate G0 = ASCII
//   \x1b[?1l     — DECCKM normal (cursor keys send CSI A/B/C/D, not
//                  SS3 OA/OB/OC/OD which break shell history recall)
//   \x1b>        — DECPAM normal (numeric keypad mode — application
//                  keypad mode breaks number-row in some shells)
//   \x1b[!p      — DECSTR soft reset (insert mode, origin mode,
//                  scroll region, ~12 other modes. Does NOT clear
//                  screen, so user's primary screen content stays.)
//   \x1b[r       — Reset scroll region to full screen (belt-and-
//                  braces — DECSTR should cover this)
//
// Writes go to os.Stdout via WriteString (not fmt.Print which routes
// through a Println-aware buffered formatter that may not flush
// reliably during signal teardown). Errors on write are ignored —
// best-effort, never panic from teardown itself.
func tuiTeardown() {
	tuiTearMu.Lock()
	defer tuiTearMu.Unlock()
	if tuiTornDown {
		return
	}
	tuiStateMu.Lock()
	s := tuiActive
	tuiStateMu.Unlock()
	if s == nil {
		tuiTornDown = true
		return
	}
	// Step 1: disable every input-tracking mode while still in raw +
	// alt-screen. We deliberately disable ALL mouse-tracking modes
	// (1000/1002/1003/1004/1005/1006/1015) regardless of which we
	// enabled — some terminals remember earlier modes that other
	// software set. Same for paste mode.
	_, _ = os.Stdout.WriteString(
		"\x1b[?1006l" + // SGR mouse off
			"\x1b[?1015l" + // urxvt mouse off
			"\x1b[?1005l" + // UTF-8 mouse off
			"\x1b[?1003l" + // any-event mouse off
			"\x1b[?1002l" + // button-event mouse off
			"\x1b[?1001l" + // hilite-tracking off
			"\x1b[?1000l" + // normal mouse off
			"\x1b[?1004l" + // focus events off
			"\x1b[?2004l" + // bracketed paste off
			"\x1b[m" + // SGR reset
			"\x1b[?25h") // show cursor
	// Step 2: exit alt-screen. User's primary shell screen returns,
	// with whatever modes that screen had before alt-screen entry.
	_, _ = os.Stdout.WriteString(tuiAltScreenExit)
	// Step 3: reset terminal modes that the alt-screen exit doesn't
	// propagate. mosh in particular keeps a synthetic emulator state
	// that needs each mode reset emitted explicitly on the primary
	// screen. Order matters here too — DECSTR last so prior mode-
	// resets aren't overwritten by anything DECSTR's own state
	// machine does.
	_, _ = os.Stdout.WriteString(
		"\x1b[m" + // SGR reset (primary screen)
			"\x0f" + // SI — back to G0 charset
			"\x1b(B" + // G0 = ASCII
			"\x1b)B" + // G1 = ASCII (in case it was switched)
			"\x1b[?1l" + // DECCKM normal: arrow keys send CSI A/B/C/D
			"\x1b[?7h" + // DECAWM autowrap on (default)
			"\x1b>" + // DECPAM normal: keypad sends digits
			"\x1b[!p" + // DECSTR soft reset
			"\x1b[r" + // reset scroll region (full screen)
			"\x1b[?25h") // show cursor again (DECSTR may toggle)
	// Step 4: force-flush stdout. macOS terminal drivers buffer
	// stdout in line-discipline mode (and even in raw mode some
	// terminals batch). Without an explicit flush, a fast process
	// exit can race the shell's prompt redraw and the resets land
	// AFTER the new prompt is rendered — defeating the whole reset.
	_ = os.Stdout.Sync()
	// Step 5: restore TTY to cooked mode.
	if s.raw && s.oldState != nil {
		_ = term.Restore(s.fd, s.oldState)
	}
	// Step 6: one more SGR reset + newline AFTER cooked-mode
	// restore. This pushes any buffered prediction state in mosh
	// past the stale-mode boundary so the next prompt renders
	// cleanly. The newline ensures the shell's prompt starts on a
	// fresh row even if the alt-screen exit landed mid-line.
	_, _ = os.Stdout.WriteString("\x1b[m\r\n")
	_ = os.Stdout.Sync()
	tuiTornDown = true
}

// safeGo spawns fn in a goroutine guarded by defer-recover. On panic:
//   1. Run tuiTeardown so the terminal is usable.
//   2. Print the panic + stack to stderr (now safe — terminal restored).
//   3. Exit with code 2 (Go's conventional unhandled-panic code).
//
// `name` identifies the goroutine in the panic message (e.g.
// "Cmd.perform task", "key reader", "SIGWINCH watcher") so a user
// reporting a bug can tell us where it died.
//
// Use this instead of `go func() { ... }()` for every long-lived
// goroutine in the terminal runtime. Cmd.perform tasks, key readers,
// signal watchers, sub-tickers — all funnel through here.
func safeGo(name string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				tuiTeardown()
				fmt.Fprintf(os.Stderr, "\nSky runtime panic in %s: %v\n\n%s\n",
					name, r, debug.Stack())
				os.Exit(2)
			}
		}()
		fn()
	}()
}

// installCleanShutdown registers a signal handler that catches SIGTERM,
// SIGHUP, SIGQUIT, and SIGINT, runs tuiTeardown, and exits with the
// conventional 128+signum code. Returns a `done` channel the caller
// closes on normal exit so the goroutine doesn't leak.
//
// Why we trap SIGINT here: in raw mode the Ctrl-C keystroke arrives
// as 0x03 byte and is handled by the runtime's own key dispatch. But
// a SIGINT delivered from OUTSIDE the program (e.g. another shell
// running `kill -INT $pid`) still gets through and would otherwise
// terminate without running our defers.
//
// Why we trap SIGHUP: terminals send SIGHUP to all child processes
// when the window closes. Without trapping it, the process dies with
// raw mode still set on the (now-orphaned) tty — leaks into the
// next session that opens that tty.
func installCleanShutdown() chan struct{} {
	done := make(chan struct{})
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGINT)
	go func() {
		// Even the signal handler can panic (rare — but reflect calls
		// happen here in some paths). Recover so a panic during
		// teardown doesn't compound into "raw mode + Go stacktrace".
		defer func() {
			if r := recover(); r != nil {
				tuiTeardown()
				fmt.Fprintf(os.Stderr, "\nSky signal handler panic: %v\n", r)
				os.Exit(2)
			}
		}()
		select {
		case sig := <-sigCh:
			tuiTeardown()
			num := 1
			if s, ok := sig.(syscall.Signal); ok {
				num = int(s)
			}
			// 128 + signal-number is the POSIX convention. Lets the
			// parent shell see "killed by SIGTERM" via $?.
			os.Exit(128 + num)
		case <-done:
			signal.Stop(sigCh)
		}
	}()
	return done
}
