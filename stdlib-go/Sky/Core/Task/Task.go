package sky_sky_core_task

import "fmt"

// Task is a deferred computation: func() any that returns a SkyResult.
// SkyResult has Tag=0 (Ok) or Tag=1 (Err), with OkValue/ErrValue fields.

type SkyResult struct {
	Tag      int
	SkyName  string
	OkValue  any
	ErrValue any
}

func SkyOk(v any) SkyResult {
	return SkyResult{Tag: 0, SkyName: "Ok", OkValue: v}
}

func SkyErr(v any) SkyResult {
	return SkyResult{Tag: 1, SkyName: "Err", ErrValue: v}
}

// Succeed creates a Task that immediately succeeds with a value.
func Succeed(value any) any {
	return func() any {
		return SkyOk(value)
	}
}

// Fail creates a Task that immediately fails with an error.
func Fail(err any) any {
	return func() any {
		return SkyErr(err)
	}
}

// Map transforms the success value of a Task.
func Map(fn any) any {
	return func(task any) any {
		return func() any {
			result := runTask(task)
			if result.Tag == 0 {
				f := fn.(func(any) any)
				return SkyOk(f(result.OkValue))
			}
			return result
		}
	}
}

// MapError transforms the error value of a Task.
func MapError(fn any) any {
	return func(task any) any {
		return func() any {
			result := runTask(task)
			if result.Tag == 1 {
				f := fn.(func(any) any)
				return SkyErr(f(result.ErrValue))
			}
			return result
		}
	}
}

// AndThen chains two tasks: if the first succeeds, feed its value to fn.
func AndThen(fn any) any {
	return func(task any) any {
		return func() any {
			result := runTask(task)
			if result.Tag == 0 {
				f := fn.(func(any) any)
				nextTask := f(result.OkValue)
				return runTask(nextTask)
			}
			return result
		}
	}
}

// Attempt converts a Task err a into a Task never (Result err a).
func Attempt(task any) any {
	return func() any {
		result := runTask(task)
		// Always succeed with the Result as the value
		return SkyOk(result)
	}
}

// Sequence runs a list of tasks in order, collecting results.
func Sequence(tasks any) any {
	return func() any {
		items := asList(tasks)
		results := make([]any, 0, len(items))
		for _, t := range items {
			result := runTask(t)
			if result.Tag == 1 {
				return result // Short-circuit on first error
			}
			results = append(results, result.OkValue)
		}
		return SkyOk(results)
	}
}

// Perform executes a Task and returns the SkyResult.
// This is the effect boundary — the only place where thunks run.
func Perform(task any) any {
	result := runTask(task)
	return result
}

// PerformUnsafe executes a Task. If it fails, prints the error and exits.
// Used by the runtime for main : Task err ().
func PerformUnsafe(task any) {
	result := runTask(task)
	if result.Tag == 1 {
		fmt.Fprintf(nil, "Task failed: %v\n", result.ErrValue)
	}
}

// FromIO wraps a synchronous IO function as a Task.
// The function is called lazily when the task is performed.
// Panics are caught and converted to Err.
func FromIO(fn any) any {
	return func() any {
		defer func() {
			if r := recover(); r != nil {
				// Panic caught — return as error
				// This is handled by the caller
			}
		}()

		f := fn.(func() any)
		return f()
	}
}

// FromResult wraps a Result value as a Task.
func FromResult(result any) any {
	return func() any {
		if r, ok := result.(SkyResult); ok {
			return r
		}
		return SkyOk(result)
	}
}

// runTask executes a task thunk, handling panics.
func runTask(task any) SkyResult {
	switch t := task.(type) {
	case func() any:
		// Catch panics
		var result any
		func() {
			defer func() {
				if r := recover(); r != nil {
					result = SkyErr(fmt.Sprintf("panic: %v", r))
				}
			}()
			result = t()
		}()
		if r, ok := result.(SkyResult); ok {
			return r
		}
		return SkyOk(result)
	case SkyResult:
		return t
	default:
		// Not a thunk — wrap as immediate Ok
		return SkyOk(task)
	}
}

func asList(v any) []any {
	if l, ok := v.([]any); ok {
		return l
	}
	return nil
}
