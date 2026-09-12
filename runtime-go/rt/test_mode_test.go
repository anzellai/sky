package rt

import (
	"os"
	"sync"
	"testing"
)

// Determinism (phase 3a): with SKY_TEST_MODE on, the non-deterministic kernels
// (Time, Random, Uuid) become a fixed clock + a seeded stream. `force` (shared
// test helper) drives a Task thunk and returns its Ok value.

// resetTestModeForTest re-arms the lazy env read so a test can change the env and
// see it take effect. Test-only; the production path reads the env exactly once.
func resetTestModeForTest() {
	testModeOnce = sync.Once{}
	testModeOn = false
	testRng = nil
}

func TestTestModeClockIsFixedAndAdvanceable(t *testing.T) {
	t.Setenv("SKY_TEST_MODE", "1")
	t.Setenv("SKY_TEST_CLOCK_MS", "1700000000000")
	resetTestModeForTest()

	if force(t, Time_now(struct{}{})).(int) != 1700000000000 {
		t.Fatalf("Time.now under test mode is not the fixed clock")
	}
	if force(t, Time_unixMillis(struct{}{})).(int) != 1700000000000 {
		t.Fatalf("Time.unixMillis diverged from the fixed clock")
	}
	// Advanceable, not merely frozen.
	TestAdvanceClockMillis(5000)
	if force(t, Time_now(struct{}{})).(int) != 1700000005000 {
		t.Fatalf("clock did not advance by 5000ms")
	}
}

func TestTestModeRandomAndUuidAreReproducible(t *testing.T) {
	run := func() ([]int, []string) {
		os.Setenv("SKY_TEST_MODE", "1")
		os.Setenv("SKY_TEST_SEED", "424242")
		resetTestModeForTest()
		var ints []int
		for i := 0; i < 8; i++ {
			ints = append(ints, force(t, Random_int(0, 1000000)).(int))
		}
		var ids []string
		for i := 0; i < 4; i++ {
			ids = append(ids, force(t, Uuid_v4()).(string))
		}
		return ints, ids
	}
	i1, u1 := run()
	i2, u2 := run()
	for k := range i1 {
		if i1[k] != i2[k] {
			t.Fatalf("Random.int not reproducible at %d: %d vs %d", k, i1[k], i2[k])
		}
	}
	for k := range u1 {
		if u1[k] != u2[k] {
			t.Fatalf("Uuid.v4 not reproducible at %d: %s vs %s", k, u1[k], u2[k])
		}
		if len(u1[k]) != 36 || u1[k][14] != '4' {
			t.Fatalf("Uuid.v4 not a canonical v4: %q", u1[k])
		}
	}
	// A distinct seed must give a distinct stream (the seed actually threads).
	os.Setenv("SKY_TEST_SEED", "999")
	resetTestModeForTest()
	if force(t, Random_int(0, 1000000)).(int) == i1[0] {
		t.Fatalf("a different seed produced the same first value")
	}
	os.Unsetenv("SKY_TEST_MODE")
	os.Unsetenv("SKY_TEST_SEED")
}

func TestTestModeOffByDefault(t *testing.T) {
	os.Unsetenv("SKY_TEST_MODE")
	resetTestModeForTest()
	if testModeActive() {
		t.Fatalf("test mode must be OFF without SKY_TEST_MODE")
	}
}
