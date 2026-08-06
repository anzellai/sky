package bluedb

// phase3a_test.go — Phase-3a embedded-adapter tests: the codec-driven-indexer encode-identity
// property (§2.5, the R-2.1 guarantee at the L3 boundary), CRUD round-trip incl. generated-field
// fill, and the blind-path-unchanged evidence (an index-less/unique-less Put stays on the engine
// blind fast path → zero validations → the ~50k/s firehose is untaxed). The SSI-soundness
// conformance suite is in phase3a_ssi_test.go.

import (
	"bytes"
	"fmt"
	"testing"
)

func jsonRow(s string) []byte { return []byte(s) }

// ordersSchema: a text PK + a text "status" index + an int "age" index — the shared fixture.
func ordersSchema() CollSchema {
	return CollSchema{
		Name: "orders", ID: 1, Key: "id",
		Cols: []ColSpec{
			{Name: "id", Type: ColText},
			{Name: "status", Type: ColText},
			{Name: "age", Type: ColInt},
		},
		Indexes: []IndexSpec{
			{ID: 10, Name: "status", Col: "status", Type: ColText},
			{ID: 11, Name: "age", Col: "age", Type: ColInt},
		},
	}
}

// TestPhase3a_IndexerEncodeIdentity — the codec-driven indexer emits coord bytes that BYTE-MATCH
// the scan-bound bytes Query builds for the same value, for every v1 index shape (single
// int/text/bool + the real/money/blob fallback classes). Because buildIndexer AND the scan-bound
// builder both go through the ONE encodeIndexKey, drift is structurally impossible (§2.5, R-2.1).
func TestPhase3a_IndexerEncodeIdentity(t *testing.T) {
	cases := []struct {
		name    string
		ct      ColType
		jsonVal string
		cv      ColValue
	}{
		{"int", ColInt, `42`, IntVal(42)},
		{"int-neg", ColInt, `-42`, IntVal(-42)},
		{"text", ColText, `"open"`, TextVal("open")},
		{"bool-true", ColBool, `true`, BoolVal(true)},
		{"bool-false", ColBool, `false`, BoolVal(false)},
		{"real-fallback", ColReal, `3.14`, RealVal(3.14)},
		{"money-fallback", ColMoney, `"USD 100.00"`, MoneyVal("USD 100.00")},
		{"blob-fallback", ColBlob, `"YWJj"`, ColValue{Type: ColBlob, Bytes: []byte("YWJj")}},
	}
	for _, c := range cases {
		cs := &CollSchema{
			Name: "t", ID: 1, Key: "id",
			Cols:    []ColSpec{{Name: "id", Type: ColText}, {Name: "v", Type: c.ct}},
			Indexes: []IndexSpec{{ID: 7, Name: "v", Col: "v", Type: c.ct}},
		}
		row := []byte(fmt.Sprintf(`{"id":"x","v":%s}`, c.jsonVal))
		coords := buildIndexer(cs)(dataUserKey("t", "x"), row)
		if len(coords) != 1 {
			t.Fatalf("%s: expected 1 coord, got %d", c.name, len(coords))
		}
		// The scan-bound (point range) through the SAME encoder must byte-match the coord.
		lo, hi := encodeScanRange(7, c.ct, c.cv.Bytes, c.cv.Bytes)
		if !bytes.Equal(coords[0].Key, lo) || !bytes.Equal(coords[0].Key, hi) {
			t.Fatalf("%s: indexer coord %x != scan-bound lo %x / hi %x (R-2.1 drift)", c.name, coords[0].Key, lo, hi)
		}
		// Fallback classes are NEVER range-optimized (§2.5) — validated by the witness, not a range.
		isFallback := c.ct == ColReal || c.ct == ColMoney || c.ct == ColBlob
		if isFallback && rangeOptimized(c.ct) {
			t.Fatalf("%s must be a fallback colType (not range-optimized)", c.name)
		}
		if !isFallback && !rangeOptimized(c.ct) {
			t.Fatalf("%s must be range-optimized", c.name)
		}
	}
}

// TestPhase3a_IndexerNullEmitsNoCoord — a NULL/absent indexed field emits NO coordinate (§2.3):
// there is nothing to encode, and an IS-NULL predicate is validated via the collection witness,
// never a missed range.
func TestPhase3a_IndexerNullEmitsNoCoord(t *testing.T) {
	cs := &CollSchema{
		Name: "u", ID: 1, Key: "id",
		Cols:    []ColSpec{{Name: "id", Type: ColText}, {Name: "nick", Type: ColText}},
		Indexes: []IndexSpec{{ID: 5, Name: "nick", Col: "nick", Type: ColText}},
	}
	if got := buildIndexer(cs)(dataUserKey("u", "x"), jsonRow(`{"id":"x","nick":null}`)); len(got) != 0 {
		t.Fatalf("explicit JSON null must emit 0 coords, got %d", len(got))
	}
	if got := buildIndexer(cs)(dataUserKey("u", "x"), jsonRow(`{"id":"x"}`)); len(got) != 0 {
		t.Fatalf("absent indexed field must emit 0 coords, got %d", len(got))
	}
	if got := buildIndexer(cs)(dataUserKey("u", "x"), jsonRow(`{"id":"x","nick":"bob"}`)); len(got) != 1 {
		t.Fatalf("present indexed field must emit 1 coord, got %d", len(got))
	}
}

// TestPhase3a_CRUDRoundTrip — Get/Put/Insert(serial fill)/Query/Count/Delete on the embedded arm.
func TestPhase3a_CRUDRoundTrip(t *testing.T) {
	e := newSSIEngine(t)
	b := NewEmbeddedBackend(e)
	orders := ordersSchema()
	b.Register(orders)

	if err := b.Put(orders, "o1", jsonRow(`{"id":"o1","status":"open","age":30}`), nil); err != nil {
		t.Fatalf("put: %v", err)
	}
	v, ok, err := b.Get(orders, "o1")
	if err != nil || !ok || !bytes.Contains(v, []byte(`"open"`)) {
		t.Fatalf("get o1: %q ok=%v err=%v", v, ok, err)
	}

	// Insert with a serial int PK filled by the adapter.
	logs := CollSchema{
		Name: "logs", ID: 6, Key: "id",
		Cols:      []ColSpec{{Name: "id", Type: ColInt, Generated: true}, {Name: "msg", Type: ColText}},
		Generated: map[string]bool{"id": true},
	}
	b.Register(logs)
	f1, err := b.Insert(logs, jsonRow(`{"msg":"a"}`), nil)
	if err != nil {
		t.Fatalf("insert 1: %v", err)
	}
	f2, err := b.Insert(logs, jsonRow(`{"msg":"b"}`), nil)
	if err != nil {
		t.Fatalf("insert 2: %v", err)
	}
	if !bytes.Contains(f1, []byte(`"id":1`)) || !bytes.Contains(f2, []byte(`"id":2`)) {
		t.Fatalf("serial fill wrong: f1=%s f2=%s", f1, f2)
	}

	got, err := b.Query(orders, QueryPlan{
		Where: CondNode{Op: CondEq, Col: "status", Type: ColText, Val: TextVal("open")}, Limit: -1,
	})
	if err != nil || len(got) != 1 {
		t.Fatalf("query open: %d (err %v)", len(got), err)
	}
	n, err := b.Count(orders, QueryPlan{Where: CondNode{Op: CondTrue}, Limit: -1})
	if err != nil || n != 1 {
		t.Fatalf("count all: %d (err %v)", n, err)
	}

	if err := b.Delete(orders, "o1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok, _ := b.Get(orders, "o1"); ok {
		t.Fatal("delete o1 failed — still present")
	}
}

// TestPhase3a_QueryOrderingAndPaging — orderAsc/orderDesc + limit/offset over the ordered engine.
func TestPhase3a_QueryOrderingAndPaging(t *testing.T) {
	e := newSSIEngine(t)
	b := NewEmbeddedBackend(e)
	orders := ordersSchema()
	b.Register(orders)
	for i, age := range []int{50, 10, 30, 20, 40} {
		id := fmt.Sprintf("o%d", i)
		if err := b.Put(orders, id, jsonRow(fmt.Sprintf(`{"id":%q,"status":"open","age":%d}`, id, age)), nil); err != nil {
			t.Fatal(err)
		}
	}
	// order by age asc, limit 3, offset 1 → ages [20,30,40]
	rows, err := b.Query(orders, QueryPlan{
		Where: CondNode{Op: CondTrue}, Orders: []OrderSpec{{Col: "age"}}, Limit: 3, Offset: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{`"age":20`, `"age":30`, `"age":40`}
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}
	for i, w := range want {
		if !bytes.Contains(rows[i], []byte(w)) {
			t.Fatalf("row %d = %s, want %s", i, rows[i], w)
		}
	}
}

// TestPhase3a_BlindPutStaysOnFastPath — the "must not tax the blind path" evidence: an
// index-less, unique-less Put is a pure blind commit (ReadSet == nil → the engine's
// processBlindPhase1 → ZERO validations). The adapter does not push the OLTP firehose off the
// fast path the ~50k/s benchmark measures.
func TestPhase3a_BlindPutStaysOnFastPath(t *testing.T) {
	e := newSSIEngine(t)
	b := NewEmbeddedBackend(e)
	kv := CollSchema{
		Name: "kv", ID: 7, Key: "k",
		Cols: []ColSpec{{Name: "k", Type: ColText}, {Name: "v", Type: ColText}},
	}
	b.Register(kv)
	before := validateCalls.Load()
	for i := 0; i < 50; i++ {
		if err := b.Put(kv, fmt.Sprintf("k%d", i), jsonRow(fmt.Sprintf(`{"k":"k%d","v":"x"}`, i)), nil); err != nil {
			t.Fatal(err)
		}
	}
	if got := validateCalls.Load() - before; got != 0 {
		t.Fatalf("index-less blind Put drove %d validate() calls, want 0 (fast path preserved)", got)
	}
}

// TestPhase3a_Capabilities + seam surface — the embedded backend reports SSI + in-process
// reactivity + cross-instance reactivity (commit-path seam), and SelectRaw SQL text is SQL-only.
func TestPhase3a_CapabilitiesAndSeams(t *testing.T) {
	e := newSSIEngine(t)
	b := NewEmbeddedBackend(e)
	caps := b.Capabilities()
	if !caps.InProcessReactive || !caps.SerializableTxn || !caps.CrossInstanceReactive || !caps.DeterministicTxn {
		t.Fatalf("embedded caps wrong: %+v", caps)
	}
	if _, err := b.SelectRaw("SELECT 1", nil); err != ErrSelectRawSQLOnly {
		t.Fatalf("embedded SelectRaw of SQL text should be SQL-only, got %v", err)
	}
	// Phase 4a WIRES the reactive seam: Watch now registers a live subscription (no longer
	// ErrReactiveSeamPhase4). It must return a usable, closeable Subscription.
	b.Register(ordersSchema())
	sub, err := b.Watch(ordersSchema(), openPlan())
	if err != nil {
		t.Fatalf("Phase-4a Watch should register a subscription, got err %v", err)
	}
	if sub == nil {
		t.Fatal("Phase-4a Watch returned a nil subscription")
	}
	sub.Close()
}
