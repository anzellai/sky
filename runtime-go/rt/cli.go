//go:build !js

// Sky.Cli — line-oriented TEA backend.
//
// A Sky.Cli program follows the same shape as Sky.Live (init / update /
// view / subscriptions), but with two CLI-specific tweaks:
//
//   - view : Model -> String          — the prompt printed before each read
//   - onLine : String -> Msg          — converts a stdin line into a Msg
//
// The runtime loop:
//   1. Call init () → (model, cmd) and fire startup cmd.
//   2. Set up subscriptions (Time.every tickers).
//   3. Print view(model). Read one line from stdin.
//   4. Dispatch onLine(line) through update; fire any resulting cmd.
//   5. Re-evaluate subscriptions for the new model.
//   6. Loop until stdin EOF (Ctrl-D / closed pipe).
//
// Concurrency: Cmd.perform runs each Task in its own goroutine, then
// dispatches the result back into the loop via msgCh. Sub.every spawns
// a ticker goroutine that pushes its Msg into the same channel each
// interval. The main loop selects between stdin lines AND msgCh, so
// async results merge into the same single-threaded update sequence —
// no shared-state hazards.

package rt

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// Cli_program is the Task-shaped entry point. Calling it returns a thunk;
// Task.run forces it and the loop blocks until stdin EOF.
//
// Sky-side surface:
//
//	main =
//	    Cli.program
//	        { init = init
//	        , update = update
//	        , view = view
//	        , subscriptions = subscriptions
//	        , onLine = onLine
//	        }
//	        |> Task.run
func Cli_program(cfg any) any {
	return func() any {
		return cliProgramRun(cfg)
	}
}

func cliProgramRun(cfg any) any {
	initFn := Field(cfg, "Init")
	updateFn := Field(cfg, "Update")
	viewFn := Field(cfg, "View")
	onLineFn := Field(cfg, "OnLine")
	subsFn := Field(cfg, "Subscriptions")
	guardFn := Field(cfg, "Guard")
	dur := durableCtxOf(Field(cfg, "Durable"))
	if initFn == nil || updateFn == nil || viewFn == nil {
		return Err[any, any](ErrInvalidInput(
			"Cli.program: cfg must define init / update / view"))
	}

	// Single dispatch channel for stdin lines (turned into Msgs via
	// onLine), Cmd.perform results, published payloads and Sub.every
	// ticks. The main loop serialises every update.
	msgCh := make(chan any, 64)
	loop := newTeaLoop(msgCh, updateFn, guardFn, dur)

	// Sky.Cli doesn't modify terminal state (no raw mode, no alt-
	// screen) so there's nothing to teardown — but we still install
	// the empty state + signal handler so a SIGTERM / SIGHUP runs
	// our normal cleanup path (subscriptions stopped, stdout flush)
	// instead of crashing without running any defer at all.
	tuiInstallState(&tuiState{})
	cleanShutdown := installCleanShutdown()
	defer func() {
		tuiUninstallState()
		close(cleanShutdown)
	}()

	// Input. With an onLine handler the program reads stdin line by
	// line until EOF. WITHOUT one (an App.app / App.cli that never
	// called withInput) there is no input source at all: the program
	// runs init, its Cmds and its subscriptions, and exits 0 once
	// nothing is left to happen (no queued Msg, no in-flight Cmd, no
	// Sub.every requested).
	var inputDone <-chan struct{}
	inputClosed := true
	if onLineFn != nil {
		doneCh := make(chan struct{})
		inputDone = doneCh
		inputClosed = false
		safeGo("Cli stdin reader", func() {
			reader := bufio.NewReader(os.Stdin)
			for {
				line, err := reader.ReadString('\n')
				line = strings.TrimRight(line, "\r\n")
				if line != "" || err == nil {
					if msg := SkyCall(onLineFn, line); msg != nil {
						// Sent BEFORE doneCh closes, so a line read
						// just ahead of EOF is always processed.
						loop.send(msg)
					}
				}
				if err != nil {
					close(doneCh)
					return
				}
			}
		})
	}

	// Initial state — call init () and fire startup cmd if any.
	initRes := SkyCall(initFn, struct{}{})
	model := tupleFirst(initRes)
	// Durable: restore the persisted model (if any) before the first render.
	model = dur.bootFixed(model)
	if cmd := tupleSecond(initRes); cmd != nil {
		loop.runCmd(cmd)
	}

	subMgr := loop.subs
	subMgr.update(subsFn, model)
	defer subMgr.stopAll()

	// Render the initial prompt before waiting for input.
	cliPrintView(viewFn, model)

	for {
		// Exit rule. Input is closed (EOF, or no input handler) and
		// nothing is queued or in flight: an in-flight Cmd.perform
		// still lands and renders before the program exits. A program
		// with no input handler additionally stays alive while it
		// requests a Sub.every (a timer-driven job runs until its
		// subscriptions return Sub.none).
		if inputClosed && loop.idle() && (onLineFn != nil || !subMgr.hasTimers()) {
			fmt.Fprintln(cliOut)
			return Ok[any, any](struct{}{})
		}
		select {
		case msg := <-msgCh:
			appMsg, ok := loop.resolve(msg)
			if !ok {
				continue
			}
			model = loop.apply(appMsg, model)
			subMgr.update(subsFn, model)
			cliPrintView(viewFn, model)
		case <-inputDone:
			inputClosed = true
			inputDone = nil
		case <-loop.wake:
		}
	}
}

// cliOut is where Sky.Cli writes its frames (stdout; tests capture it).
var cliOut io.Writer = os.Stdout

// cliPrintView calls the user's view(model) → String and writes the
// result to stdout without a trailing newline (the user's prompt
// formatting decides whether to add one).
func cliPrintView(viewFn, model any) {
	out := SkyCall(viewFn, model)
	if s, ok := out.(string); ok {
		fmt.Fprint(cliOut, s)
	} else if out != nil {
		fmt.Fprint(cliOut, out)
	}
}

// Cli_readPassword reads one line from stdin with terminal echo
// disabled. Wraps `golang.org/x/term`'s ReadPassword (already a
// dep). Returns a Task that produces the typed password (without
// the trailing newline) on success, or ErrIo on read failure.
//
// Use this for auth flows — the password is NEVER echoed on the
// user's screen and never lands in their terminal scrollback. The
// runtime momentarily flips the tty into raw mode for the duration
// of the read and restores it after.
//
// If stdin isn't a TTY (piped input, CI), we fall back to a normal
// line read so scripts that pipe a password through stdin still
// work — they just don't get the echo-suppression UX.
func Cli_readPassword(_ any) any {
	return func() any {
		fd := int(os.Stdin.Fd())
		if !term.IsTerminal(fd) {
			// Piped stdin — fall back to bufio line read. No echo
			// suppression, but we never controlled the tty anyway.
			reader := bufio.NewReader(os.Stdin)
			line, err := reader.ReadString('\n')
			if err != nil && line == "" {
				return Err[any, any](ErrIo("readPassword: " + err.Error()))
			}
			// The line is a password — hand back an opaque Secret, never a
			// String that could leak into a log or a `%v`.
			return Ok[any, any](Secret{v: strings.TrimRight(line, "\r\n")})
		}
		bytes, err := term.ReadPassword(fd)
		// term.ReadPassword does NOT echo a newline on Enter — the
		// prompt and the next output would otherwise glue together.
		// Print one explicit newline so the user's screen advances.
		fmt.Println()
		if err != nil {
			return Err[any, any](ErrIo("readPassword: " + err.Error()))
		}
		return Ok[any, any](Secret{v: string(bytes)})
	}
}
