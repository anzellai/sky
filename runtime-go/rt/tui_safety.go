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
// handler. Sequence matters: mouse tracking and bracketed paste
// must be disabled BEFORE raw mode is restored (otherwise the
// disable-codes go to the wrong sink), and the cursor / alt-screen
// codes go AFTER restore so they reach the user's shell directly.
//
// We also emit a charset reset (`\x0f\x1b(B`) and DECSTR soft reset
// (`\x1b[!p`) before exiting the alt-screen. Without these, common
// readline corruption symptoms appear after a Sky.Tui run on some
// terminals (notably mosh): "multi-tab looking" lines (alternate
// charset stuck on G0), Backspace echoing wrong (insert mode left
// on), arrow keys printing escape codes (application cursor keys
// left on). DECSTR resets all of those without clearing the screen
// the user just exited to.
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
	// Order: ANSI mode-disables while raw mode is still active so the
	// codes reach the terminal driver, not the cooked-mode line buffer.
	if s.mouseEnabled {
		_, _ = os.Stdout.WriteString("\x1b[?1006l\x1b[?1000l")
	}
	if s.bracketedPaste {
		_, _ = os.Stdout.WriteString("\x1b[?2004l")
	}
	// Character set + soft reset. Issued before raw mode is restored
	// so the bytes reach the terminal driver directly. Sequence:
	//   \x0f       — Shift-In: select G0 character set (cancels
	//                any prior \x0e Shift-Out that left G1 active)
	//   \x1b(B     — Designate G0 = ASCII (cancels DEC special
	//                graphics that some apps switch to for borders)
	//   \x1b[!p    — DECSTR: soft terminal reset. Resets insert
	//                mode, application cursor keys, origin mode,
	//                scrolling region, and ~12 other modes that
	//                user apps commonly leave dirty. Doesn't clear
	//                the screen.
	//   \x1b[r     — Reset scroll region to full screen (belt-and-
	//                braces — DECSTR should cover this but some
	//                terminals are quirky)
	_, _ = os.Stdout.WriteString("\x0f\x1b(B\x1b[!p\x1b[r")
	if s.raw && s.oldState != nil {
		_ = term.Restore(s.fd, s.oldState)
	}
	if s.cursorHidden {
		_, _ = os.Stdout.WriteString(tuiShowCursor)
	}
	if s.altScreen {
		_, _ = os.Stdout.WriteString(tuiAltScreenExit)
	}
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
