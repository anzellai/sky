package rt

// Regression test for adaptFuncValue's CURRIED-to-flat path.
//
// A Sky function defined with fewer parameters than its type has
// (`withRank : Int -> Job -> Job`, `withRank n = update (…)`) is a Go
// `func(int) func(Job) Job`. Coerced to the flat slot shape
// `func(int, Job) Job`, the adapter cut the argument list down to the
// source's one parameter, called it, got a `func(Job) Job` back, could
// not narrow that func to a `Job`, and returned the zero `Job`: a silent
// wrong value that panicked at the next pattern match.
//
// The adapter now applies the remaining arguments group by group to the
// funcs each call returns, and fails loudly (a classified CoerceFailure)
// when a group cannot be applied, never with a zero value.

import (
	"strings"
	"testing"
)

type adaptJob struct {
	Name string
	Rank int
}

func TestAdaptFuncValue_CurriedToFlat(t *testing.T) {
	withRank := func(n int) func(adaptJob) adaptJob {
		return func(j adaptJob) adaptJob {
			j.Rank = n
			return j
		}
	}
	flat := Coerce[func(int, adaptJob) adaptJob](withRank)
	got := flat(4, adaptJob{Name: "a", Rank: 1})
	if got != (adaptJob{Name: "a", Rank: 4}) {
		t.Fatalf("curried→flat adapter: got %+v, want {a 4}", got)
	}
}

func TestAdaptFuncValue_CurriedGroupsToFlat(t *testing.T) {
	// Two declared params, the third through the returned func, and a
	// source boxed as the canonical `func(any) any` nest one level down.
	withBoth := func(name string, n int) func(any) any {
		return func(j any) any {
			r := j.(adaptJob)
			r.Name, r.Rank = name, n
			return r
		}
	}
	flat := Coerce[func(string, int, adaptJob) adaptJob](withBoth)
	got := flat("b", 7, adaptJob{Name: "a", Rank: 1})
	if got != (adaptJob{Name: "b", Rank: 7}) {
		t.Fatalf("curried groups→flat adapter: got %+v, want {b 7}", got)
	}
}

func TestAdaptFuncValue_CurriedNonFuncResultFailsLoudly(t *testing.T) {
	// The source returns a value, not a func, while arguments remain: no
	// adaptation exists, and a zero value would be a silent wrong answer.
	notCurried := func(n int) int { return n }
	flat := Coerce[func(int, int) int](notCurried)
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("an unadaptable curried shape must panic, not return a zero value")
		}
		msg, _ := r.(string)
		if kind, _ := classifyPanic(msg); kind != "CoerceFailure" {
			t.Fatalf("panic must classify as CoerceFailure, got %q (%v)", kind, r)
		}
		if !strings.Contains(msg, "func(int, int) int") {
			t.Fatalf("panic must name the slot shape: %v", r)
		}
	}()
	_ = flat(1, 2)
}
