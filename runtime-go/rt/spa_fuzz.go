package rt

// Coverage-guided `sky fuzz` (docs/tooling/testing.md, "Property-based
// fuzzing").
//
// A guided fuzz harness is built with in-process counters: the compiler
// renders each coverage marker (one per top-level Sky definition and one per
// `case` arm, rust/crates/lower/src/coverage.rs) as `rt.CovHit(<id>)`. CovHit
// counts the id. Spa_fuzzGreybox runs the fuzz loop in this process: after
// each Msg sequence it reads which ids the sequence reached for the first
// time, keeps the sequence when there is one, and builds later sequences by
// mutating the kept ones, the ones that first reached a rarely reached id
// most often. Every random choice comes from the one seed, so a run is
// reproducible.

import (
	"fmt"
	"math/rand"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// covIDs bounds the ids CovHit counts (a program has a few thousand). An id
// past it is ignored.
const covIDs = 1 << 18

var (
	// covHits counts every hit of every id.
	covHits [covIDs]uint32
	// covSeen marks the ids a kept count has reached.
	covSeen [covIDs / 64]uint64
	// covFresh marks the ids reached since the last covTake that are not in
	// covSeen. covFreshIDs lists them in the order they were reached.
	covFresh    [covIDs / 64]uint64
	covFreshMu  sync.Mutex
	covFreshIDs []int
)

// CovHit counts coverage marker `id`. Emitted only into a guided `sky fuzz`
// harness (`codegen::emit_program_instrumented`). An id already seen costs
// one atomic add and one atomic load.
func CovHit(id int) {
	if id < 0 || id >= covIDs {
		return
	}
	atomic.AddUint32(&covHits[id], 1)
	w, b := id>>6, uint64(1)<<(uint(id)&63)
	if atomic.LoadUint64(&covSeen[w])&b != 0 {
		return
	}
	if !casOr(&covFresh[w], b) {
		return
	}
	covFreshMu.Lock()
	covFreshIDs = append(covFreshIDs, id)
	covFreshMu.Unlock()
}

// casOr sets bit b in *p. False when it was already set.
func casOr(p *uint64, b uint64) bool {
	for {
		old := atomic.LoadUint64(p)
		if old&b != 0 {
			return false
		}
		if atomic.CompareAndSwapUint64(p, old, old|b) {
			return true
		}
	}
}

// casAndNot clears bit b in *p.
func casAndNot(p *uint64, b uint64) {
	for {
		old := atomic.LoadUint64(p)
		if atomic.CompareAndSwapUint64(p, old, old&^b) {
			return
		}
	}
}

// covTake clears the fresh ids and returns them. With keep they join covSeen
// (they count as reached); without, they are dropped and can count again.
func covTake(keep bool) []int {
	covFreshMu.Lock()
	ids := covFreshIDs
	covFreshIDs = nil
	covFreshMu.Unlock()
	for _, id := range ids {
		w, b := id>>6, uint64(1)<<(uint(id)&63)
		casAndNot(&covFresh[w], b)
		if keep {
			casOr(&covSeen[w], b)
		}
	}
	return ids
}

// covFreshCount is how many fresh ids are waiting for covTake.
func covFreshCount() int {
	covFreshMu.Lock()
	defer covFreshMu.Unlock()
	return len(covFreshIDs)
}

// covSeenIDs lists every id in covSeen, ascending.
func covSeenIDs() []int {
	var out []int
	for w := range covSeen {
		v := atomic.LoadUint64(&covSeen[w])
		for bit := 0; v != 0; bit++ {
			if v&1 != 0 {
				out = append(out, w*64+bit)
			}
			v >>= 1
		}
	}
	return out
}

// covWriteSeen writes the reached ids, one per line, to the file
// SKY_FUZZ_COV_OUT names (`sky fuzz` reads it for "arms reached"). No-op when
// the variable is unset.
func covWriteSeen() {
	path := os.Getenv("SKY_FUZZ_COV_OUT")
	if path == "" {
		return
	}
	var b strings.Builder
	for _, id := range covSeenIDs() {
		b.WriteString(strconv.Itoa(id))
		b.WriteByte('\n')
	}
	_ = os.WriteFile(path, []byte(b.String()), 0o644)
}

// fuzzPair splits a Sky 2-tuple of any instantiation (`T2[Model, Cmd]`,
// `T2[Msg, Seed]`) into its two fields.
func fuzzPair(pair any) (any, any) {
	rv := reflect.ValueOf(pair)
	if rv.Kind() == reflect.Struct {
		a, b := rv.FieldByName("V0"), rv.FieldByName("V1")
		if a.IsValid() && b.IsValid() {
			return a.Interface(), b.Interface()
		}
	}
	panic(fmt.Sprintf("sky fuzz: expected a pair, got %T", pair))
}

// The longest fresh sequence, and the longest a mutation may grow one to.
const (
	fuzzFreshMax  = 8
	fuzzMutateMax = 32
)

// fuzzEntry is a kept sequence and the ids it reached first.
type fuzzEntry struct {
	seq []any
	ids []int
	// picked counts the mutations of this entry so far.
	picked int
}

// Spa_fuzzGreybox runs the model no-panic net for `budget` Msgs, from the
// model `model0`, with Msgs from `gen` (`Seed -> ( Msg, Seed )`, threaded from
// `seed`) folded through `update`. Each sequence starts from model0.
//
// Guided (`guided` True, a harness built with CovHit counters): a sequence
// that reaches a marker no earlier sequence reached is kept: for each step
// that reached one, the prefix that ends at that step. Seven of eight later
// sequences mutate a kept one: append one or two fresh Msgs, replace one Msg,
// or splice the start of one kept sequence onto the end of another. A kept
// sequence is picked with a weight of 1 / ((1 + h) * (1 + p)), where h is how
// often the rarest marker it reached first has been hit and p how often it
// was mutated already: the sequence that reached a rare state is extended
// most, and one whose mutations find nothing gives way to the others.
// Unguided: every sequence is fresh. The generator's own counts are dropped
// before each sequence runs, so only `update` and what it calls guide the
// loop.
//
// A sequence that panics is shrunk to the fewest Msgs from model0 that still
// panic (the prefix up to the panic, then single Msgs removed while it still
// panics). Its steps go to stderr, and the panic is raised again, so the
// program's panic net reports and classifies it as before. Returns the
// summary line.
func Spa_fuzzGreybox(budget, seed, model0, gen, update, guided any) any {
	steps := AsInt(budget)
	g := AsBool(guided)
	rng := rand.New(rand.NewSource(int64(AsInt(seed))))
	s := seed
	fresh := func(n int) []any {
		out := make([]any, 0, n)
		for i := 0; i < n; i++ {
			msg, next := fuzzPair(SkyCall(gen, s))
			s = next
			out = append(out, msg)
		}
		return out
	}
	// run folds seq from model0. `at` is the step that panicked (-1: none).
	// `marks[i]` is how many fresh ids had been counted after step i.
	run := func(seq []any) (at int, marks []int, r any) {
		m := model0
		for i, msg := range seq {
			failed := func() (failed bool) {
				defer func() {
					if x := recover(); x != nil {
						r, failed = x, true
					}
				}()
				m, _ = fuzzPair(SkyCall(update, msg, m))
				return false
			}()
			if failed {
				return i, marks, r
			}
			marks = append(marks, covFreshCount())
		}
		return -1, marks, nil
	}
	var pool []fuzzEntry
	pick := func() []any {
		weights := make([]float64, len(pool))
		total := 0.0
		for i, e := range pool {
			least := uint32(1<<32 - 1)
			for _, id := range e.ids {
				if h := atomic.LoadUint32(&covHits[id]); h < least {
					least = h
				}
			}
			weights[i] = 1 / (float64(1+least) * float64(1+e.picked))
			total += weights[i]
		}
		x := rng.Float64() * total
		for i, w := range weights {
			if x < w {
				pool[i].picked++
				return pool[i].seq
			}
			x -= w
		}
		pool[len(pool)-1].picked++
		return pool[len(pool)-1].seq
	}
	mutate := func() []any {
		seq := append([]any(nil), pick()...)
		switch op := rng.Intn(6); {
		case op <= 2:
			seq = append(seq, fresh(1)...)
		case op == 3:
			seq = append(seq, fresh(2)...)
		case op == 4:
			seq[rng.Intn(len(seq))] = fresh(1)[0]
		default:
			other := pool[rng.Intn(len(pool))].seq
			seq = append(seq[:1+rng.Intn(len(seq))], other[rng.Intn(len(other)):]...)
		}
		if len(seq) > fuzzMutateMax {
			seq = seq[:fuzzMutateMax]
		}
		return seq
	}
	covTake(false) // init's counts are not a sequence's
	used, seqs := 0, 0
	for used < steps {
		var seq []any
		if g && len(pool) > 0 && rng.Intn(8) != 0 {
			seq = mutate()
		} else {
			seq = fresh(1 + rng.Intn(fuzzFreshMax))
		}
		if len(seq) > steps-used {
			seq = seq[:steps-used]
		}
		covTake(false)
		at, marks, r := run(seq)
		seqs++
		if at >= 0 {
			covTake(true)
			fuzzReportPanic(seq[:at+1], func(c []any) int { a, _, _ := run(c); return a })
			covTake(true)
			covWriteSeen()
			panic(r)
		}
		used += len(seq)
		// Each step that reached a new marker keeps the prefix that ends at
		// it, so a Msg appended to that prefix runs in the state that reached
		// the marker.
		ids := covTake(true)
		if g {
			from := 0
			for i, upto := range marks {
				if upto > from && upto <= len(ids) {
					pool = append(pool, fuzzEntry{seq: seq[:i+1], ids: ids[from:upto]})
					from = upto
				}
			}
		}
	}
	covWriteSeen()
	mode := "guided"
	if !g {
		mode = "unguided"
	}
	return fmt.Sprintf("%d steps in %d sequences (%s, %d kept), no unclassified panic", used, seqs, mode, len(pool))
}

// fuzzReportPanic shrinks a panicking sequence and prints its steps.
func fuzzReportPanic(seq []any, run func([]any) int) {
	cur := seq
	for i := 0; i < len(cur)-1; {
		cand := append(append([]any(nil), cur[:i]...), cur[i+1:]...)
		if at := run(cand); at >= 0 {
			cur = cand[:at+1]
		} else {
			i++
		}
	}
	names := make([]string, len(cur))
	for i, m := range cur {
		names[i] = Basics_toString(m)
	}
	fmt.Fprintf(os.Stderr, "sky fuzz: panicking sequence (%d Msg(s) from init): %s\n", len(cur), strings.Join(names, ", "))
}
