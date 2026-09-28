// task_loop.go — stack-safe Task loops: Task.loop and Task.forever.
//
// Every `Task.andThen` step nests Go frames: `AnyTaskAndThen` forces the
// outer task, then forces the continuation's task INSIDE the same Go call.
// A Sky loop written as plain recursion —
//
//	count n =
//	    if n == 0 then Task.succeed 0
//	    else Task.succeed n |> Task.andThen (\_ -> count (n - 1))
//
// — therefore grows the goroutine stack by a few frames per step, and a
// long enough loop (two million steps) dies with Go's fatal "goroutine stack
// exceeds limit". That is not a recoverable panic: no recover site sees it and
// the process exits.
//
// `Task.loop` runs each step to completion in a Go `for` loop, so the stack
// depth is the depth of ONE step, whatever the number of steps. The step
// function returns a `Step state a` value:
//
//	type Step state a = Loop state | Done a
//
// `Loop s` runs the step again with the new state; `Done a` ends the loop
// with `a`. An `Err` from any step ends the loop with that error. A panic in a
// step propagates unchanged to the same recover sites as any other Task.
package rt

import "fmt"

// Task.loop : (state -> Task e (Step state a)) -> state -> Task e a
func Task_loop(step any, initial any) any {
	return SkyTask[any, any](func() SkyResult[any, any] {
		state := initial
		for {
			r := anyTaskInvoke(SkyCall(step, state))
			if r.Tag != 0 {
				return Err[any, any](r.ErrValue)
			}
			name, payload := readStep(r.OkValue)
			switch name {
			case "Loop":
				state = payload
			case "Done":
				return Ok[any, any](payload)
			default:
				panic(fmt.Sprintf("rt.Coerce: expected a Task.Step value (Loop or Done) from the Task.loop step function, got %T", r.OkValue))
			}
		}
	})
}

// Task.forever : Task e a -> Task e b
//
// Runs `task` again and again. Only an `Err` ends it, and the result is that
// error, so the success type is free (`b`): a forever task never produces a
// value. Constant stack for the same reason as Task_loop.
func Task_forever(task any) any {
	return SkyTask[any, any](func() SkyResult[any, any] {
		for {
			r := anyTaskInvoke(task)
			if r.Tag != 0 {
				return Err[any, any](r.ErrValue)
			}
		}
	})
}

// readStep reads the constructor NAME and the single payload off a
// `Task.Step` value. It never assumes a numeric tag: the generated
// constructor carries its Sky name (`SkyName`), and that is what this
// matches on. Two shapes are accepted, like readShouldRetry:
//  1. SkyADT — the representation of a stdlib ADT (Sky_Core_* types are
//     never sealed, so `Sky_Core_Task_Step` is an alias of rt.SkyADT).
//  2. Any struct exposing string `SkyName` and slice `Fields` fields.
//
// A value with no constructor name returns "", which Task_loop reports as a
// classified CoerceFailure panic rather than guessing a branch.
func readStep(v any) (string, any) {
	if a, ok := v.(SkyADT); ok {
		if len(a.Fields) != 1 {
			return "", nil
		}
		return a.SkyName, a.Fields[0]
	}
	name, fields := readShouldRetry(v)
	if len(fields) != 1 {
		return "", nil
	}
	// readShouldRetry falls back to the ShouldRetry tag table when a struct
	// carries no SkyName; that table does not describe Step, so only trust a
	// real constructor name here.
	if name != "Loop" && name != "Done" {
		return "", nil
	}
	return name, fields[0]
}
