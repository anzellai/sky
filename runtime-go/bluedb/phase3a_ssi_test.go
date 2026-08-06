package bluedb

// phase3a_ssi_test.go — the Phase-3a Go SSI-soundness conformance suite. Every test exercises the
// embedded adapter's read-set wiring (§2.1/§2.3/§2.6/§2.7) end-to-end and proves the SSI stays
// SOUND at the L3 boundary: no phantom hole, no under-reject, no unenforced unique, and each
// multi-collection write attributed to its OWN collection.

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// TestPhase3a_TxnQueryPhantomRejected — THE phantom hole (§2.6). A transaction body queries
// WHERE status='open' (a declared range-optimized index) via the adapter, then (once) a concurrent
// insert of a matching row commits out-of-band. The querying txn's first attempt MUST abort (its
// scanned range now contains the concurrent NewIndex coord) and retry — so the "insert if none
// open" invariant holds and NO second open row is created. Under Snapshot Isolation (the hole)
// both would commit → two open rows.
func TestPhase3a_TxnQueryPhantomRejected(t *testing.T) {
	e := newSSIEngine(t)
	b := NewEmbeddedBackend(e)
	orders := ordersSchema()
	b.Register(orders)

	openPlan := QueryPlan{Where: CondNode{Op: CondEq, Col: "status", Type: ColText, Val: TextVal("open")}, Limit: -1}

	var attempts int32
	var once atomic.Bool
	err := b.Transaction(func(tx TxHandle) error {
		atomic.AddInt32(&attempts, 1)
		rows, qerr := tx.Query(orders, openPlan) // records the precise status='open' index range
		if qerr != nil {
			return qerr
		}
		if once.CompareAndSwap(false, true) {
			// A concurrent insert of a matching row commits FIRST (out-of-band blind write).
			if perr := b.Put(orders, "concurrent", jsonRow(`{"id":"concurrent","status":"open","age":30}`), nil); perr != nil {
				return perr
			}
		}
		if len(rows) == 0 {
			// "insert one open row if none exist" — the phantom would let this create a SECOND.
			return tx.Put(orders, "mine", jsonRow(`{"id":"mine","status":"open","age":40}`), nil)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("txn: %v", err)
	}
	if atomic.LoadInt32(&attempts) < 2 {
		t.Fatalf("expected the first attempt to ABORT on the predicate phantom then retry; attempts=%d", attempts)
	}
	// Invariant: exactly ONE open row, and it is the concurrent one — "mine" must NOT exist.
	got, _ := b.Query(orders, openPlan)
	if len(got) != 1 {
		t.Fatalf("phantom NOT rejected: expected exactly 1 open row, got %d", len(got))
	}
	if _, ok, _ := b.Get(orders, "mine"); ok {
		t.Fatal("phantom NOT rejected: a second open row 'mine' was committed (Snapshot-Isolation behaviour)")
	}
}

// TestPhase3a_MultiCollectionStamping — a transaction writes `orders` then `inventory`; each
// KeyChange must be stamped with its OWN collection (§2.1). Part (a) asserts the per-change Coll
// directly off buildReq; part (b) proves a concurrent WitnessCollection(orders) reader CATCHES the
// order-write while a witness on an UNRELATED collection does NOT (stamping is precise, not a
// last-collection catch-all — the pre-fix bug).
func TestPhase3a_MultiCollectionStamping(t *testing.T) {
	e := newSSIEngine(t)
	b := NewEmbeddedBackend(e)
	orders := CollSchema{Name: "orders", ID: 1, Key: "id", Cols: []ColSpec{{Name: "id", Type: ColText}}}
	inventory := CollSchema{Name: "inventory", ID: 2, Key: "sku", Cols: []ColSpec{{Name: "sku", Type: ColText}}}
	b.Register(orders)
	b.Register(inventory)

	// (a) direct per-change Coll assertion off buildReq.
	tx, _ := e.Begin()
	b.installTxn(tx)
	_ = tx.Put(dataUserKey("orders", "o1"), jsonRow(`{"id":"o1"}`))
	_ = tx.Put(dataUserKey("inventory", "i1"), jsonRow(`{"sku":"i1"}`))
	req := tx.buildReq()
	changes, err := DecodeChangelogPayload(req.ChangelogPayload)
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	collByPk := map[string]CollID{}
	for _, c := range changes {
		collByPk[string(c.Pk)] = c.Coll
	}
	if got := collByPk[string(dataUserKey("orders", "o1"))]; got != 1 {
		t.Fatalf("orders write mis-stamped: Coll=%d want 1", got)
	}
	if got := collByPk[string(dataUserKey("inventory", "i1"))]; got != 2 {
		t.Fatalf("inventory write mis-stamped: Coll=%d want 2", got)
	}
	tx.Abort()

	// (b) a concurrent order+inventory transaction; a witness on `orders` conflicts, a witness on
	// an unrelated collection does not.
	txR, _ := e.Begin()
	txR.WitnessCollection(orders.ID) // witnesses orders (CollID 1)
	txU, _ := e.Begin()
	txU.WitnessCollection(CollID(99)) // unrelated collection

	txW, _ := e.Begin()
	b.installTxn(txW)
	_ = txW.Put(dataUserKey("orders", "o2"), jsonRow(`{"id":"o2"}`))
	_ = txW.Put(dataUserKey("inventory", "i2"), jsonRow(`{"sku":"i2"}`))
	if err := txW.Commit(); err != nil {
		t.Fatalf("writer commit: %v", err)
	}

	_ = txR.Put([]byte("marker-r"), []byte("x"))
	if err := txR.Commit(); !errors.Is(err, ErrConflict) {
		t.Fatalf("orders witness must catch the per-change orders write (Coll=1), got %v", err)
	}
	_ = txU.Put([]byte("marker-u"), []byte("x"))
	if err := txU.Commit(); err != nil {
		t.Fatalf("unrelated-collection witness must NOT conflict with the orders/inventory writes, got %v", err)
	}
}

// TestPhase3a_NotOrderableMoneyFallback — a query filtering a not-order-preserving Money column
// (§2.3) must NEVER record a byte-range (lexical order ≠ numeric order → under-reject) and instead
// routes to the collection witness, which OVER-rejects: a concurrent change to the collection
// conflicts.
func TestPhase3a_NotOrderableMoneyFallback(t *testing.T) {
	e := newSSIEngine(t)
	b := NewEmbeddedBackend(e)
	products := CollSchema{
		Name: "products", ID: 3, Key: "id",
		Cols:    []ColSpec{{Name: "id", Type: ColText}, {Name: "price", Type: ColMoney}},
		Indexes: []IndexSpec{{ID: 30, Name: "price", Col: "price", Type: ColMoney}},
	}
	b.Register(products)
	pricePlan := QueryPlan{Where: CondNode{Op: CondGte, Col: "price", Type: ColMoney, Val: MoneyVal("USD 10.00")}, Limit: -1}

	tx, _ := e.Begin()
	b.installTxn(tx)
	et := &embeddedTx{b: b, tx: tx}
	if _, err := et.Query(products, pricePlan); err != nil {
		t.Fatal(err)
	}
	if len(tx.ranges) != 0 {
		t.Fatalf("a Money (not-orderable) query must NEVER record a byte-range; recorded %d", len(tx.ranges))
	}
	if !tx.collWitness[products.ID] {
		t.Fatal("a Money query must record the collection witness (§2.3)")
	}

	// A concurrent insert of ANY product (even one OUT of the numeric predicate) conflicts via the
	// witness — over-reject, never the under-reject a bogus lexical range would cause.
	txW, _ := e.Begin()
	b.installTxn(txW)
	_ = txW.Put(dataUserKey("products", "p1"), jsonRow(`{"id":"p1","price":"USD 5.00"}`))
	if err := txW.Commit(); err != nil {
		t.Fatalf("writer commit: %v", err)
	}
	_ = tx.Put(dataUserKey("products", "marker"), jsonRow(`{"id":"marker","price":"USD 1.00"}`))
	if err := tx.Commit(); !errors.Is(err, ErrConflict) {
		t.Fatalf("Money query witness must over-reject a concurrent collection change, got %v", err)
	}
}

// TestPhase3a_IsNullFallback — an IS-NULL predicate routes to the collection witness, never an
// index range (§2.3): a NULL value emits NO index coordinate, so a range read-set would MISS a
// concurrent insert of a row with that field NULL. The witness catches it.
func TestPhase3a_IsNullFallback(t *testing.T) {
	e := newSSIEngine(t)
	b := NewEmbeddedBackend(e)
	users := CollSchema{
		Name: "users", ID: 4, Key: "id",
		Cols:    []ColSpec{{Name: "id", Type: ColText}, {Name: "nick", Type: ColText}},
		Indexes: []IndexSpec{{ID: 40, Name: "nick", Col: "nick", Type: ColText}},
	}
	b.Register(users)
	nullPlan := QueryPlan{Where: CondNode{Op: CondIsNull, Col: "nick", Type: ColText}, Limit: -1}

	tx, _ := e.Begin()
	b.installTxn(tx)
	et := &embeddedTx{b: b, tx: tx}
	if _, err := et.Query(users, nullPlan); err != nil {
		t.Fatal(err)
	}
	if len(tx.ranges) != 0 {
		t.Fatalf("IS-NULL must NOT record a byte-range; recorded %d", len(tx.ranges))
	}
	if !tx.collWitness[users.ID] {
		t.Fatal("IS-NULL must record the collection witness (§2.3)")
	}

	// A concurrent insert of a nick=NULL row (which emits NO coord) must still conflict via the
	// witness — the under-reject a range would cause is prevented.
	txW, _ := e.Begin()
	b.installTxn(txW)
	_ = txW.Put(dataUserKey("users", "u1"), jsonRow(`{"id":"u1","nick":null}`))
	if err := txW.Commit(); err != nil {
		t.Fatalf("writer commit: %v", err)
	}
	_ = tx.Put(dataUserKey("users", "marker"), jsonRow(`{"id":"marker","nick":"x"}`))
	if err := tx.Commit(); !errors.Is(err, ErrConflict) {
		t.Fatalf("IS-NULL witness must catch a concurrent nick=NULL insert, got %v", err)
	}
}

// TestPhase3a_UniqueViaSSI — two concurrent inserts of email='x' (with different PKs) must result
// in exactly ONE commit; the other gets a deterministic ErrUniqueViolation (§2.7). The duplicate
// is prevented — exactly one row with that email exists.
func TestPhase3a_UniqueViaSSI(t *testing.T) {
	e := newSSIEngine(t)
	b := NewEmbeddedBackend(e)
	accounts := CollSchema{
		Name: "accounts", ID: 5, Key: "id",
		Cols: []ColSpec{{Name: "id", Type: ColText}, {Name: "email", Type: ColText, Unique: true}},
	}
	b.Register(accounts)

	row := func(id string) []byte {
		return jsonRow(fmt.Sprintf(`{"id":%q,"email":"x@example.com"}`, id))
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); _, errs[0] = b.Insert(accounts, row("a"), nil) }()
	go func() { defer wg.Done(); _, errs[1] = b.Insert(accounts, row("b"), nil) }()
	wg.Wait()

	ok0, ok1 := errs[0] == nil, errs[1] == nil
	if ok0 == ok1 {
		t.Fatalf("exactly one insert must commit; got err0=%v err1=%v", errs[0], errs[1])
	}
	loser := errs[0]
	if ok0 {
		loser = errs[1]
	}
	if !errors.Is(loser, ErrUniqueViolation) {
		t.Fatalf("the losing insert must get ErrUniqueViolation (duplicate prevented), got %v", loser)
	}

	got, _ := b.Query(accounts, QueryPlan{
		Where: CondNode{Op: CondEq, Col: "email", Type: ColText, Val: TextVal("x@example.com")}, Limit: -1,
	})
	if len(got) != 1 {
		t.Fatalf("duplicate NOT prevented: %d accounts with email=x", len(got))
	}
}

// TestPhase3a_RangeOptimizedPrecise — a declared int index range query records a PRECISE range and
// validates precisely (not coarse, §2.6): a concurrent IN-range insert conflicts, an OUT-of-range
// insert does NOT. This is what turns a coarse collection-witness into a tight range-witness (the
// mechanism that makes real write concurrency possible under transactions).
func TestPhase3a_RangeOptimizedPrecise(t *testing.T) {
	e := newSSIEngine(t)
	b := NewEmbeddedBackend(e)
	orders := ordersSchema()
	b.Register(orders)

	// age in [20, 40] — a bounded AND range on the declared int "age" index.
	agePlan := QueryPlan{Where: CondNode{Op: CondAnd, Kids: []CondNode{
		{Op: CondGte, Col: "age", Type: ColInt, Val: IntVal(20)},
		{Op: CondLte, Col: "age", Type: ColInt, Val: IntVal(40)},
	}}, Limit: -1}

	// (a) IN-range insert conflicts.
	tx, _ := e.Begin()
	b.installTxn(tx)
	et := &embeddedTx{b: b, tx: tx}
	if _, err := et.Query(orders, agePlan); err != nil {
		t.Fatal(err)
	}
	if len(tx.ranges) != 1 {
		t.Fatalf("a bounded int range must record exactly 1 PRECISE range; recorded %d", len(tx.ranges))
	}
	if tx.collWitness[orders.ID] {
		t.Fatal("a precise indexed range must NOT fall back to the coarse collection witness")
	}
	txIn, _ := e.Begin()
	b.installTxn(txIn)
	_ = txIn.Put(dataUserKey("orders", "in"), jsonRow(`{"id":"in","status":"open","age":30}`))
	if err := txIn.Commit(); err != nil {
		t.Fatalf("in-range writer commit: %v", err)
	}
	_ = tx.Put(dataUserKey("orders", "m1"), jsonRow(`{"id":"m1","status":"open","age":30}`))
	if err := tx.Commit(); !errors.Is(err, ErrConflict) {
		t.Fatalf("in-range (age=30) insert must conflict with the [20,40] scanner, got %v", err)
	}

	// (b) OUT-of-range insert does NOT conflict (precise, not coarse).
	tx2, _ := e.Begin()
	b.installTxn(tx2)
	et2 := &embeddedTx{b: b, tx: tx2}
	if _, err := et2.Query(orders, agePlan); err != nil {
		t.Fatal(err)
	}
	txOut, _ := e.Begin()
	b.installTxn(txOut)
	_ = txOut.Put(dataUserKey("orders", "out"), jsonRow(`{"id":"out","status":"open","age":50}`))
	if err := txOut.Commit(); err != nil {
		t.Fatalf("out-of-range writer commit: %v", err)
	}
	_ = tx2.Put(dataUserKey("orders", "m2"), jsonRow(`{"id":"m2","status":"closed","age":100}`))
	if err := tx2.Commit(); err != nil {
		t.Fatalf("out-of-range (age=50) insert must NOT conflict with the [20,40] scanner (precise, not coarse), got %v", err)
	}
}
