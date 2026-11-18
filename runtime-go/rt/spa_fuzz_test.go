package rt

import (
	"strings"
	"testing"
)

func resetCov() {
	covTake(false)
	for i := range covSeen {
		covSeen[i] = 0
	}
	for i := range covHits {
		covHits[i] = 0
	}
}

func TestCovTakeCountsOnlyNewIDs(t *testing.T) {
	resetCov()
	defer resetCov()
	CovHit(3)
	CovHit(3)
	CovHit(70)
	if n := len(covTake(false)); n != 2 {
		t.Fatalf("dropped take = %d, want 2", n)
	}
	CovHit(3)
	if n := len(covTake(true)); n != 1 {
		t.Fatalf("take after a drop = %d, want 1", n)
	}
	CovHit(3)
	CovHit(-1)
	CovHit(covIDs)
	if n := len(covTake(true)); n != 0 {
		t.Fatalf("a seen id counted again: %d", n)
	}
	if got := covSeenIDs(); len(got) != 1 || got[0] != 3 {
		t.Fatalf("seen = %v", got)
	}
}

// A model whose panic needs Msg 1, 2, 3 in a row, behind nested branches that
// each count a marker (the shape of `case model.stage of` over `case msg of`),
// from a generator of 20 Msgs. The guided loop keeps the prefixes that reach a
// new branch and extends them; the unguided loop, with the same seed and
// budget, does not reach the panic.
func greyboxProbe(guided bool) (summary string, panicked any) {
	resetCov()
	gen := func(s any) any {
		n := AsInt(s)
		next := (n*1103515245 + 12345) & 0x7fffffff
		return T2[any, any]{V0: (next >> 16) % 20, V1: next}
	}
	update := func(msg, model any) any {
		m, stage := AsInt(msg), AsInt(model)
		next := 0
		switch {
		case stage == 0 && m == 1:
			CovHit(1)
			next = 1
		case stage == 0:
			CovHit(11)
		case stage == 1 && m == 2:
			CovHit(2)
			next = 2
		case stage == 1:
			CovHit(12)
		case stage == 2 && m == 3:
			panic("reached the third step")
		default:
			CovHit(13)
		}
		return T2[any, any]{V0: next, V1: nil}
	}
	defer func() { panicked = recover() }()
	return Spa_fuzzGreybox(1000, 1, 0, gen, update, guided).(string), nil
}

func TestGuidedFuzzReachesADeepOrderTheUnguidedMisses(t *testing.T) {
	defer resetCov()
	sum, p := greyboxProbe(false)
	if p != nil {
		t.Fatalf("the unguided run found it (pick another seed): %v", p)
	}
	if !strings.Contains(sum, "unguided, 0 kept") {
		t.Fatalf("summary = %q", sum)
	}
	_, p = greyboxProbe(true)
	if p != "reached the third step" {
		t.Fatalf("the guided run did not reach the panic: %v", p)
	}
}
