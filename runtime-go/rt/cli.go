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
	if initFn == nil || updateFn == nil || viewFn == nil || onLineFn == nil {
		return Err[any, any](ErrInvalidInput(
			"Cli.program: cfg must define init / update / view / onLine"))
	}

	// Single dispatch channel for both stdin lines (turned into Msgs via
	// onLine) and Cmd.perform results (already Msgs). The main loop
	// reads from this channel and serialises updates.
	msgCh := make(chan any, 16)
	doneCh := make(chan struct{})

	// Sky.Cli doesn't modify terminal state (no raw mode, no alt-
	// screen) so there's nothing to teardown — but we still install
	// the empty state + signal handler so a SIGTERM / SIGHUP runs
	// our normal cleanup path (subMgr.stopAll, stdout flush) instead
	// of crashing without running any defer at all.
	tuiInstallState(&tuiState{})
	cleanShutdown := installCleanShutdown()
	defer func() {
		tuiUninstallState()
		close(cleanShutdown)
	}()

	// Stdin reader goroutine. EOF closes doneCh which terminates the loop.
	safeGo("Cli stdin reader", func() {
		reader := bufio.NewReader(os.Stdin)
		for {
			line, err := reader.ReadString('\n')
			line = strings.TrimRight(line, "\r\n")
			if line != "" || err == nil {
				msg := SkyCall(onLineFn, line)
				if msg != nil {
					msgCh <- msg
				}
			}
			if err != nil {
				close(doneCh)
				return
			}
		}
	})

	// Initial state — call init () and fire startup cmd if any.
	initRes := SkyCall(initFn, struct{}{})
	model := tupleFirst(initRes)
	if cmd := tupleSecond(initRes); cmd != nil {
		cliRunCmd(cmd, msgCh)
	}

	// Subscription manager — tracks the active ticker(s) so we can
	// tear them down when subscriptions(model) returns a different
	// shape. nil-tolerant: a program without `subscriptions` keyword
	// in cfg just gets an empty list every tick.
	subMgr := newSubManager(msgCh)
	subMgr.update(subsFn, model)

	// Render the initial prompt before waiting for input.
	cliPrintView(viewFn, model)

	// Main update loop. Each Msg → update → maybe Cmd → re-render prompt.
	// Always drain pending msgs BEFORE honouring an EOF signal — Go's
	// select picks ready cases at random, so a piped stdin can close
	// doneCh while the channel still holds queued msgs. We do a
	// non-blocking msgCh peek first; only when there's nothing to
	// process do we wait for either source.
	for {
		select {
		case msg := <-msgCh:
			model = cliApplyUpdate(updateFn, msg, model, msgCh)
			subMgr.update(subsFn, model)
			cliPrintView(viewFn, model)
			continue
		default:
		}
		select {
		case msg := <-msgCh:
			model = cliApplyUpdate(updateFn, msg, model, msgCh)
			subMgr.update(subsFn, model)
			cliPrintView(viewFn, model)
		case <-doneCh:
			subMgr.stopAll()
			fmt.Println()
			return Ok[any, any](struct{}{})
		}
	}
}

// cliApplyUpdate calls update(msg, model), runs any resulting cmd,
// and returns the new model. update is expected to be a curried
// 2-arg Sky function returning a tuple (newModel, cmd).
func cliApplyUpdate(updateFn, msg, model any, msgCh chan<- any) any {
	res := SkyCall(updateFn, msg, model)
	newModel := tupleFirst(res)
	if cmd := tupleSecond(res); cmd != nil {
		cliRunCmd(cmd, msgCh)
	}
	return newModel
}

// cliPrintView calls the user's view(model) → String and writes the
// result to stdout without a trailing newline (the user's prompt
// formatting decides whether to add one).
func cliPrintView(viewFn, model any) {
	out := SkyCall(viewFn, model)
	if s, ok := out.(string); ok {
		fmt.Print(s)
	} else if out != nil {
		fmt.Print(out)
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
			return Ok[any, any](strings.TrimRight(line, "\r\n"))
		}
		bytes, err := term.ReadPassword(fd)
		// term.ReadPassword does NOT echo a newline on Enter — the
		// prompt and the next output would otherwise glue together.
		// Print one explicit newline so the user's screen advances.
		fmt.Println()
		if err != nil {
			return Err[any, any](ErrIo("readPassword: " + err.Error()))
		}
		return Ok[any, any](string(bytes))
	}
}

// cliRunCmd processes a Cmd value, spawning goroutines for Cmd.perform.
// Each goroutine pushes its (toMsg result) into msgCh so the main loop
// can fold it into the next update.
func cliRunCmd(cmd any, msgCh chan<- any) {
	c, ok := cmd.(cmdT)
	if !ok {
		return
	}
	switch c.kind {
	case "none":
		return
	case "batch":
		for _, sub := range c.batch {
			cliRunCmd(sub, msgCh)
		}
	case "perform":
		// safeGo: a panic inside the user's Task or its toMsg handler
		// won't bypass the deferred terminal restore. Without this,
		// any Cmd.perform that crashes leaves Tui's terminal stuck
		// in raw mode + alt-screen forever. Sky.Cli has no such
		// state to undo, but the recover still gives users a useful
		// error message instead of a bare goroutine stack-dump.
		safeGo("Cmd.perform task", func() {
			result := sky_call(c.task, nil)
			msg := sky_call(c.toMsg, result)
			if msg != nil {
				// Defensive: msgCh may be closed if the main loop
				// exited between the task spawning and finishing.
				// recover-on-closed-send catches the panic via
				// safeGo's wrapper.
				defer func() { _ = recover() }()
				msgCh <- msg
			}
		})
	}
}
