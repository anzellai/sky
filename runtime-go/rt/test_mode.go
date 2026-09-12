package rt

// Deterministic TEST MODE for Sky's own effect kernels (auto-testing phase 3a).
//
// There is no runtime effect-dispatch table to swap (Ffi.kernel is a build-time
// sentinel; kernels lower to direct Go calls). So determinism is injected INSIDE
// the few non-deterministic kernels, keyed on a process-global flag read from the
// environment. The `sky test` runner (or a scenario harness) sets the env BEFORE
// spawning the app — same app code, swapped behaviour, the app never knows.
//
// Activated by `SKY_TEST_MODE` (any value but ""/"0"/"false"). Then:
//   * Time.now / Time.unixMillis  -> a fixed, ADVANCEABLE clock (SKY_TEST_CLOCK_MS)
//   * Random.int / Random.float   -> a seeded stream (SKY_TEST_SEED)
//   * Uuid.v4 / Uuid.v7           -> a deterministic stream from the same seed
//
// The clock is advanceable (not merely frozen) so a test can drive TTL / expiry
// transitions; the seed advances per call so two "random" values never collide.
// Env is read once, lazily, on first use — which is after the runner has set it
// (and after the CAF-connect footgun's env-before-first-force requirement).

import (
	"encoding/binary"
	mrand "math/rand"
	"os"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/google/uuid"
)

// The default fixed clock: 2024-01-01T00:00:00Z in ms. Arbitrary but stable, so
// a test that does not pin SKY_TEST_CLOCK_MS still reproduces byte-for-byte.
const testDefaultClockMs int64 = 1704067200000

// The default seed, matching the differential fuzzer's default (spa-diff-fuzz).
const testDefaultSeed int64 = 20260912

var (
	testModeOnce sync.Once
	testModeOn   bool
	testClockMs  atomic.Int64
	testClockSet bool
	testSeedSet  bool
	testRngMu    sync.Mutex
	testRng      *mrand.Rand
)

func initTestMode() {
	testModeOnce.Do(func() {
		v := os.Getenv("SKY_TEST_MODE")
		if v == "" || v == "0" || v == "false" {
			return
		}
		testModeOn = true

		// Determinism is OPT-IN within test mode: a fixed clock / seeded RNG only
		// when the specific var is set. This decouples "offline" (the HTTP mock,
		// always on in test mode) from "deterministic" (fixed clock + seeded
		// Random/Uuid) — so turning test mode on for a project does NOT silently
		// freeze `Data.newId ()`, which would make cross-run-unique ids collide
		// and break DB integration tests. A scenario that WANTS reproducibility
		// sets SKY_TEST_SEED / SKY_TEST_CLOCK_MS (typically alongside an ephemeral
		// DB, where a fixed id is fine because the store is fresh each run).
		ms := testDefaultClockMs
		if s := os.Getenv("SKY_TEST_CLOCK_MS"); s != "" {
			if n, err := strconv.ParseInt(s, 10, 64); err == nil {
				ms = n
				testClockSet = true
			}
		}
		testClockMs.Store(ms)

		seed := testDefaultSeed
		if s := os.Getenv("SKY_TEST_SEED"); s != "" {
			if n, err := strconv.ParseInt(s, 10, 64); err == nil {
				seed = n
				testSeedSet = true
			}
		}
		testRng = mrand.New(mrand.NewSource(seed))
	})
}

// testModeActive reports whether test mode is on (lazy env read). It gates the
// offline HTTP mock — always safe to have on in a test, since no test should
// reach the real network.
func testModeActive() bool {
	initTestMode()
	return testModeOn
}

// testClockActive reports whether the FIXED test clock should replace wall time —
// only when a scenario opted in with SKY_TEST_CLOCK_MS. Otherwise the real clock
// runs even in test mode.
func testClockActive() bool {
	initTestMode()
	return testModeOn && testClockSet
}

// testSeedActive reports whether Random/Uuid should draw from the SEEDED stream —
// only when a scenario opted in with SKY_TEST_SEED. Otherwise real entropy runs
// even in test mode, so `Data.newId ()` stays unique across runs.
func testSeedActive() bool {
	initTestMode()
	return testModeOn && testSeedSet
}

// testNowMillis returns the current test clock in ms (does not advance it).
func testNowMillis() int64 {
	return testClockMs.Load()
}

// TestAdvanceClockMillis moves the test clock forward by `delta` ms and returns
// the new value. Exposed for a scenario harness / a `Test.advanceClock` kernel;
// a no-op sink when test mode is off.
func TestAdvanceClockMillis(delta int64) int64 {
	if !testClockActive() {
		return 0
	}
	return testClockMs.Add(delta)
}

// testRandIntn returns a deterministic Intn(n) from the seeded stream.
func testRandIntn(n int) int {
	if n <= 0 {
		return 0
	}
	testRngMu.Lock()
	defer testRngMu.Unlock()
	return testRng.Intn(n)
}

// testRandFloat returns a deterministic Float64() from the seeded stream.
func testRandFloat() float64 {
	testRngMu.Lock()
	defer testRngMu.Unlock()
	return testRng.Float64()
}

// testUuidV4 builds a deterministic RFC-4122 v4 UUID from the seeded stream.
func testUuidV4() string {
	var b [16]byte
	testRngMu.Lock()
	_, _ = testRng.Read(b[:])
	testRngMu.Unlock()
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC-4122 variant
	u, _ := uuid.FromBytes(b[:])
	return u.String()
}

// testUuidV7 builds a deterministic v7 UUID: the 48-bit millisecond timestamp
// from the (advanceable) test clock, the rest from the seeded stream. Stays
// time-ordered like a real v7, but reproducible.
func testUuidV7() string {
	var b [16]byte
	testRngMu.Lock()
	_, _ = testRng.Read(b[:])
	testRngMu.Unlock()
	ms := uint64(testNowMillis())
	var ts [8]byte
	binary.BigEndian.PutUint64(ts[:], ms)
	// The 48-bit big-endian timestamp occupies bytes 0..5.
	copy(b[0:6], ts[2:8])
	b[6] = (b[6] & 0x0f) | 0x70 // version 7
	b[8] = (b[8] & 0x3f) | 0x80 // RFC-4122 variant
	u, _ := uuid.FromBytes(b[:])
	return u.String()
}
