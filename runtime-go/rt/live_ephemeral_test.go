package rt

// Persist.ephemeral — a reactive-derived Model field marked ephemeral is NOT
// persisted in the session blob (re-derived by the reactive loop on reopen), the
// huge-Model optimization. These tests prove the two runtime-grill invariants:
//
//   Finding 1 — encodeSession zeroes the ephemeral field on a SHALLOW COPY, never
//   in place: the persisted blob loses the data but the LIVE sess.model keeps it.
//
//   Finding 2 — persistSession's dirty-check compares the ZEROED PROJECTION on
//   both sides, so a pure ephemeral-field re-derive is a NO-OP persist (not a
//   write storm) while a real input-field change still persists.

import (
	"reflect"
	"testing"
)

// ephemTestModel: Count is an input (persisted) field; Todos is the
// reactively-derived ephemeral field.
//
// (countingStore — a SessionStore that counts Set calls — is declared in
// live_store_r2_race_test.go and reused here.)
type ephemTestModel struct {
	Count int
	Todos []string
}

// (a) encodeSession zeroes the ephemeral field in the BLOB but leaves the live
// sess.model untouched (copy-not-in-place proof).
func TestEncodeSession_EphemeralFieldZeroedInBlobLiveModelUntouched(t *testing.T) {
	model := ephemTestModel{Count: 3, Todos: []string{"a", "b", "c"}}
	sess := buildSess(model)
	sess.ephemeralFields = []string{"Todos"}

	blob, err := encodeSession(sess)
	if err != nil {
		t.Fatalf("encodeSession errored: %v", err)
	}

	// The LIVE model still has its data — the projection copied, it did not zero
	// in place.
	live := sess.model.(ephemTestModel)
	if len(live.Todos) != 3 {
		t.Fatalf("live sess.model.Todos was mutated: want 3 rows, got %d", len(live.Todos))
	}
	if live.Count != 3 {
		t.Fatalf("live sess.model.Count changed: got %d", live.Count)
	}

	// The DECODED blob has the ephemeral field emptied (to be re-derived on
	// reopen) but keeps the input field.
	sess2, err := decodeSession(blob)
	if err != nil {
		t.Fatalf("decodeSession errored: %v", err)
	}
	decoded := sess2.model.(ephemTestModel)
	if len(decoded.Todos) != 0 {
		t.Fatalf("blob should have zeroed the ephemeral Todos, got %d rows", len(decoded.Todos))
	}
	if decoded.Count != 3 {
		t.Fatalf("blob should keep the input Count=3, got %d", decoded.Count)
	}
}

// No ephemeral fields → encodeSession persists the whole model (byte-identical to
// pre-ephemeral behaviour).
func TestEncodeSession_NoEphemeralFieldsPersistsWholeModel(t *testing.T) {
	model := ephemTestModel{Count: 3, Todos: []string{"a", "b", "c"}}
	sess := buildSess(model) // ephemeralFields left nil
	blob, err := encodeSession(sess)
	if err != nil {
		t.Fatalf("encodeSession errored: %v", err)
	}
	sess2, err := decodeSession(blob)
	if err != nil {
		t.Fatalf("decodeSession errored: %v", err)
	}
	decoded := sess2.model.(ephemTestModel)
	if len(decoded.Todos) != 3 {
		t.Fatalf("non-ephemeral model should persist all rows, got %d", len(decoded.Todos))
	}
}

// (b) persistSession dirty-check: an ephemeral-only change is a no-op persist; an
// input-field change persists.
func TestPersistSession_EphemeralOnlyChangeIsNoOp(t *testing.T) {
	cs := &countingStore{}
	app := &liveApp{store: cs}

	sess := buildSess(ephemTestModel{Count: 1, Todos: []string{"a"}})
	sess.sid = "s1"
	sess.ephemeralFields = []string{"Todos"}

	// First persist: lastPersistedModel is nil → always writes.
	app.persistSession(sess)
	if cs.sets != 1 {
		t.Fatalf("first persist should write once, got %d", cs.sets)
	}

	// ONLY the ephemeral field changes (a reactive re-derive) → NO-OP persist.
	sess.model = ephemTestModel{Count: 1, Todos: []string{"a", "b", "c"}}
	app.persistSession(sess)
	if cs.sets != 1 {
		t.Fatalf("ephemeral-only change must not persist (write storm), got %d writes", cs.sets)
	}

	// A real INPUT-field change → persists.
	sess.model = ephemTestModel{Count: 2, Todos: []string{"a", "b", "c"}}
	app.persistSession(sess)
	if cs.sets != 2 {
		t.Fatalf("input-field change must persist, got %d writes", cs.sets)
	}
}

// projectEphemeralModel is a pure shallow-copy-zero: original untouched, only the
// named fields zeroed on the copy; empty field list returns the original.
func TestProjectEphemeralModel_ShallowCopyZero(t *testing.T) {
	orig := ephemTestModel{Count: 7, Todos: []string{"x", "y"}}

	// Empty fields → identity (same value, byte-identical path).
	if got := projectEphemeralModel(orig, nil); !reflect.DeepEqual(got, orig) {
		t.Fatalf("empty fields should return the original unchanged")
	}

	got := projectEphemeralModel(orig, []string{"Todos"}).(ephemTestModel)
	if len(got.Todos) != 0 || got.Count != 7 {
		t.Fatalf("projection should zero Todos + keep Count, got %+v", got)
	}
	// Original untouched.
	if len(orig.Todos) != 2 {
		t.Fatalf("projection mutated the original: got %d rows", len(orig.Todos))
	}

	// Raw (lowercase) field name is capitalized to the Go field.
	got2 := projectEphemeralModel(orig, []string{"todos"}).(ephemTestModel)
	if len(got2.Todos) != 0 {
		t.Fatalf("lowercase accessor name should still zero Todos, got %d rows", len(got2.Todos))
	}
}

// Blob-size independence: the persisted blob of a session whose big derived
// field is ephemeral is IDENTICAL in size no matter how many rows the live Model
// holds (10 vs 5000) — the whole point of Persist.ephemeral. A non-ephemeral
// control blob grows with the row count.
func TestEncodeSession_EphemeralBlobSizeIndependentOfRowCount(t *testing.T) {
	rows := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = "todo-item-with-some-text"
		}
		return out
	}

	// Hold the input field constant so ONLY the ephemeral slice length varies
	// between the two encodes.
	const fixedCount = 42

	// Ephemeral: Todos zeroed out of the blob → size independent of n.
	encEph := func(n int) int {
		s := buildSess(ephemTestModel{Count: fixedCount, Todos: rows(n)})
		s.ephemeralFields = []string{"Todos"}
		blob, err := encodeSession(s)
		if err != nil {
			t.Fatalf("encodeSession errored: %v", err)
		}
		return len(blob)
	}
	small, big := encEph(10), encEph(5000)
	if small != big {
		t.Fatalf("ephemeral blob size must be row-count-independent: 10 rows=%d bytes, 5000 rows=%d bytes", small, big)
	}

	// Control: same model WITHOUT ephemeral marking → blob grows with rows.
	encPlain := func(n int) int {
		s := buildSess(ephemTestModel{Count: fixedCount, Todos: rows(n)})
		blob, err := encodeSession(s)
		if err != nil {
			t.Fatalf("encodeSession errored: %v", err)
		}
		return len(blob)
	}
	if encPlain(10) >= encPlain(5000) {
		t.Fatalf("non-ephemeral control should grow with rows: 10=%d 5000=%d", encPlain(10), encPlain(5000))
	}
}

// Map-shaped Model: shallow-clone + drop the ephemeral keys, original untouched.
func TestProjectEphemeralModel_MapModel(t *testing.T) {
	orig := map[string]any{"count": 3, "todos": []string{"a", "b"}}
	got := projectEphemeralModel(orig, []string{"todos"}).(map[string]any)
	if _, present := got["todos"]; present {
		t.Fatalf("map projection should drop the ephemeral key")
	}
	if got["count"] != 3 {
		t.Fatalf("map projection should keep the input key, got %v", got["count"])
	}
	if _, present := orig["todos"]; !present {
		t.Fatalf("map projection mutated the original map")
	}
}
