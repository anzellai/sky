package rt

import "testing"

// Benchmarks for the Task interpreter. Each builds its task the way generated
// code does (`rt.TaskCoerceT[E, A](rt.AnyTaskAndThen(...))`), so the numbers
// measure the shapes a Sky program actually runs.
//
//	go test ./rt/ -run '^$' -bench 'BenchmarkTask' -benchmem

// benchStep mirrors the emitted Go of
//
//	step limit n = if n >= limit then Task.succeed n
//	               else Task.succeed n |> Task.andThen (\_ -> step limit (n + 1))
func benchStep(limit, n int) SkyTask[any, int] {
	if n >= limit {
		return TaskCoerceT[any, int](AnyTaskSucceed(any(n)))
	}
	return TaskCoerceT[any, int](AnyTaskAndThen(any(func(_w0 any) any {
		return any(benchStep(limit, n+1))
	}), any(TaskCoerceT[any, int](AnyTaskSucceed(any(n))))))
}

// Before the trampoline, 1e6 steps of this recursion died with a fatal
// "stack overflow" (Go's 1 GB stack limit), so the pre-change baseline is
// taken at 1e5, which it survived.
func BenchmarkTaskAndThenRecursion1e5(b *testing.B) {
	const steps = 100_000
	for i := 0; i < b.N; i++ {
		r := anyTaskInvoke(benchStep(steps, 0))
		if r.Tag != 0 || r.OkValue.(int) != steps {
			b.Fatalf("got %+v", r)
		}
	}
}

func BenchmarkTaskAndThenRecursion1e6(b *testing.B) {
	const steps = 1_000_000
	for i := 0; i < b.N; i++ {
		r := anyTaskInvoke(benchStep(steps, 0))
		if r.Tag != 0 || r.OkValue.(int) != steps {
			b.Fatalf("got %+v", r)
		}
	}
}

func BenchmarkTaskLoop1e6(b *testing.B) {
	const steps = 1_000_000
	for i := 0; i < b.N; i++ {
		r := anyTaskInvoke(Task_loop(countdown, SkyTuple2{V0: steps, V1: 0}))
		if r.Tag != 0 || r.OkValue.(int) != steps {
			b.Fatalf("got %+v", r)
		}
	}
}

func BenchmarkTaskSequence1e5(b *testing.B) {
	const n = 100_000
	tasks := make([]any, n)
	for i := range tasks {
		tasks[i] = AnyTaskSucceed(i)
	}
	for i := 0; i < b.N; i++ {
		r := anyTaskInvoke(Task_sequence(tasks))
		if r.Tag != 0 || len(r.OkValue.([]any)) != n {
			b.Fatalf("got tag %d", r.Tag)
		}
	}
}
