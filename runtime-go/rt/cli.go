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
//   2. Print view(model). Read one line from stdin.
//   3. Dispatch onLine(line) through update; fire any resulting cmd.
//   4. Loop until stdin EOF (Ctrl-D / closed pipe).
//
// Concurrency: Cmd.perform runs each Task in its own goroutine, then
// dispatches the result back into the loop via msgCh. The main loop
// selects between stdin lines AND msgCh, so async results merge into
// the same single-threaded update sequence — no shared-state hazards.
//
// Subscriptions (Time.every, etc.) are not yet wired. Next iteration.

package rt

import (
	"bufio"
	"fmt"
	"os"
	"strings"
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
	if initFn == nil || updateFn == nil || viewFn == nil || onLineFn == nil {
		return Err[any, any](ErrInvalidInput(
			"Cli.program: cfg must define init / update / view / onLine"))
	}

	// Single dispatch channel for both stdin lines (turned into Msgs via
	// onLine) and Cmd.perform results (already Msgs). The main loop
	// reads from this channel and serialises updates.
	msgCh := make(chan any, 16)
	doneCh := make(chan struct{})

	// Stdin reader goroutine. EOF closes doneCh which terminates the loop.
	go func() {
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
	}()

	// Initial state — call init () and fire startup cmd if any.
	initRes := SkyCall(initFn, struct{}{})
	model := tupleFirst(initRes)
	if cmd := tupleSecond(initRes); cmd != nil {
		cliRunCmd(cmd, msgCh)
	}

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
			cliPrintView(viewFn, model)
			continue
		default:
		}
		select {
		case msg := <-msgCh:
			model = cliApplyUpdate(updateFn, msg, model, msgCh)
			cliPrintView(viewFn, model)
		case <-doneCh:
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
		go func() {
			result := sky_call(c.task, nil)
			msg := sky_call(c.toMsg, result)
			if msg != nil {
				msgCh <- msg
			}
		}()
	}
}
