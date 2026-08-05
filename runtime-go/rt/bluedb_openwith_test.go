package rt

import (
	"path/filepath"
	"testing"
)

// openWithID forces an open task, asserts Ok, and returns the store handle id.
func openWithID(t *testing.T, task any) int {
	t.Helper()
	return AsInt(runOK(t, task))
}

// expectErr forces a task and asserts it returned Err (the negative assertions:
// ErrFull / ErrTooLarge surfaced as a Sky Err through the kernel).
func expectErr(t *testing.T, task any) {
	t.Helper()
	res := task.(func() any)()
	r, ok := res.(SkyResult[any, any])
	if !ok || r.Tag == 0 {
		t.Fatalf("expected Err, got %#v", res)
	}
}

// closeStore forces BlueDB_close so the global registry doesn't leak between
// tests (each test uses its own temp path, but the handle must be released too).
func closeStore(t *testing.T, id int) {
	t.Helper()
	runOK(t, BlueDB_close(id))
}

// TestOpenWithMaxKeysPlumbs proves maxKeys reaches the engine: 2 new keys land,
// the 3rd NEW key is refused (ErrFull), and an overwrite of an existing key is
// still allowed.
func TestOpenWithMaxKeysPlumbs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "maxkeys.blue")
	id := openWithID(t, BlueDB_openWith(path, true, 0, 0, 2))
	defer closeStore(t, id)

	runOK(t, BlueDB_put(id, "k1", "v1")) // new key #1 — ok
	runOK(t, BlueDB_put(id, "k2", "v2")) // new key #2 — ok
	expectErr(t, BlueDB_put(id, "k3", "v3")) // new key #3 over the ceiling — ErrFull

	// Overwriting an existing key is always allowed (no new key created).
	runOK(t, BlueDB_put(id, "k1", "v1b"))
	if got, ok := getVal(t, id, "k1"); !ok || got != "v1b" {
		t.Fatalf("overwrite of existing key must succeed: got %q (present=%v)", got, ok)
	}
}

// TestOpenWithMaxValueBytesPlumbs proves maxValueBytes reaches the engine. The
// engine bounds key+value bytes, so with single-char keys and a limit of 5 a
// 4-byte value fits (1+4=5) while a 5-byte value overflows (1+5=6 → ErrTooLarge).
func TestOpenWithMaxValueBytesPlumbs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "maxval.blue")
	id := openWithID(t, BlueDB_openWith(path, true, 0, 5, 0))
	defer closeStore(t, id)

	runOK(t, BlueDB_put(id, "a", "abcd"))     // 1 + 4 = 5 ≤ 5 — ok
	expectErr(t, BlueDB_put(id, "b", "abcde")) // 1 + 5 = 6 > 5 — ErrTooLarge

	if got, ok := getVal(t, id, "a"); !ok || got != "abcd" {
		t.Fatalf("under-limit value must land: got %q (present=%v)", got, ok)
	}
	if _, ok := getVal(t, id, "b"); ok {
		t.Fatalf("over-limit value must not land")
	}
}

// TestOpenWithSyncFalseRoundTrips proves the relaxed tier opens and serves.
// (Power-loss durability isn't unit-testable; this is the behavioural smoke that
// sync=false is a valid open that reads and writes.)
func TestOpenWithSyncFalseRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync-false.blue")
	id := openWithID(t, BlueDB_openWith(path, false, 0, 0, 0))
	defer closeStore(t, id)

	runOK(t, BlueDB_put(id, "hello", "world"))
	if got, ok := getVal(t, id, "hello"); !ok || got != "world" {
		t.Fatalf("relaxed-tier round-trip failed: got %q (present=%v)", got, ok)
	}
}

// TestOpenWithReuseIgnoresOptions proves the open-once contract: a path already
// open via BlueDB_open returns the SAME handle when re-opened via openWith, and
// the openWith options do NOT take effect (the live handle keeps its defaults) —
// verified by a value far larger than the openWith's maxValueBytes=5 still
// succeeding (the default 64 MiB limit is live).
func TestOpenWithReuseIgnoresOptions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reuse.blue")
	id1 := openWithID(t, BlueDB_open(path))
	defer closeStore(t, id1)

	// Second open of the same path with tight, relaxed options.
	id2 := openWithID(t, BlueDB_openWith(path, false, 5, 5, 5))
	if id2 != id1 {
		t.Fatalf("reuse must return the same handle: id1=%d id2=%d", id1, id2)
	}

	// If openWith's maxValueBytes=5 had taken effect this 20-byte value would be
	// refused; it succeeds → the live handle kept the default limit.
	runOK(t, BlueDB_put(id1, "big", "0123456789ABCDEFGHIJ"))
	if got, ok := getVal(t, id1, "big"); !ok || got != "0123456789ABCDEFGHIJ" {
		t.Fatalf("reused handle must keep default limits: got %q (present=%v)", got, ok)
	}

	// And maxKeys=5 didn't take effect either — many new keys still land.
	for i := 0; i < 12; i++ {
		runOK(t, BlueDB_put(id1, "k"+string(rune('a'+i)), "v"))
	}
}

// TestBlueDBOpenUnchangedDefaultLimits proves BlueDB_open still opens a working
// handle with the default (generous) limits — a 100-byte value and many keys
// both land (the refactor to bluedbRegisterOpen kept BlueDB_open byte-identical).
func TestBlueDBOpenUnchangedDefaultLimits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "default.blue")
	id := openWithID(t, BlueDB_open(path))
	defer closeStore(t, id)

	big := make([]byte, 100)
	for i := range big {
		big[i] = 'x'
	}
	runOK(t, BlueDB_put(id, "k", string(big)))
	if got, ok := getVal(t, id, "k"); !ok || len(got) != 100 {
		t.Fatalf("default limits must accept a 100-byte value: len=%d present=%v", len(got), ok)
	}
	for i := 0; i < 50; i++ {
		runOK(t, BlueDB_put(id, "key"+string(rune('a'+i%26))+string(rune('0'+i/26)), "v"))
	}
}
