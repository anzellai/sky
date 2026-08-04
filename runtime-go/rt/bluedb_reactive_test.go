package rt

import (
	"testing"
	"time"
)

func recvChange(t *testing.T, ch chan bluedbRecordChange) bluedbRecordChange {
	t.Helper()
	select {
	case rc := <-ch:
		return rc
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a record change")
		return bluedbRecordChange{}
	}
}

func assertNoMoreChange(t *testing.T, ch chan bluedbRecordChange) {
	t.Helper()
	select {
	case rc := <-ch:
		t.Fatalf("unexpected extra change: %+v", rc)
	case <-time.After(200 * time.Millisecond):
	}
}

// Only record keys (\x00x\x00d\x00<coll>\x00<pk>) decode; the sibling index/unique/
// manifest/seq keys and bare keys do not.
func TestReactiveDecodeRecordKey(t *testing.T) {
	coll, pk, ok := bluedbDecodeRecordKey(string(bluedbCollRecordKey("users", "u1")))
	if !ok || coll != "users" || pk != "u1" {
		t.Fatalf("record decode = %q,%q,%v", coll, pk, ok)
	}
	for name, k := range map[string]string{
		"index":    bluedbReserved + "i\x00users\x00email\x00val\x00u1",
		"unique":   bluedbReserved + "u\x00users\x00email\x00val",
		"manifest": bluedbReserved + "m\x00users",
		"seq":      bluedbReserved + "s\x00users",
		"bare":     "sessionid123",
		"empty-pk": bluedbRecordKeyTag + "users\x00",
		"no-sep":   bluedbRecordKeyTag + "users",
	} {
		if _, _, ok := bluedbDecodeRecordKey(k); ok {
			t.Fatalf("%s key must not decode as a record", name)
		}
	}
}

// End-to-end: the pump turns committed writes into exactly one record change each
// (sibling index/manifest keys filtered), across collections, for puts and deletes.
func TestReactivePump(t *testing.T) {
	id := registerIdxStore(t)
	db := idxDB(id)
	ch := make(chan bluedbRecordChange, 64)
	stop := bluedbStartReactivePump(db, func(rc bluedbRecordChange) { ch <- rc })
	defer stop()

	// put with an index triple → record + index (+ manifest) keys in one batch;
	// the pump must yield exactly ONE record change.
	collPut(t, id, "users", "u1", `{"id":"u1","email":"a@x"}`, [3]string{"email", "a@x", "text"})
	rc := recvChange(t, ch)
	// The change's Record is the exact stored bytes (codec may reorder keys), so
	// compare it to what a read returns rather than to the input literal.
	stored, _ := collGet(t, id, "users", "u1")
	if rc.Coll != "users" || rc.Pk != "u1" || rc.IsDelete || rc.Record != stored {
		t.Fatalf("put change = %+v (stored=%q)", rc, stored)
	}
	assertNoMoreChange(t, ch) // siblings filtered — no extra change

	// a different collection is isolated
	collPut(t, id, "orders", "o9", `{"id":"o9"}`)
	rc = recvChange(t, ch)
	if rc.Coll != "orders" || rc.Pk != "o9" || rc.Record != `{"id":"o9"}` {
		t.Fatalf("orders change = %+v", rc)
	}

	// delete → IsDelete change (record + index deletes → one record change)
	runOK(t, BlueDB_collDelete(id, "users", "u1", ftPairs([2]string{"email", "text"}), []any{}))
	rc = recvChange(t, ch)
	if rc.Coll != "users" || rc.Pk != "u1" || !rc.IsDelete {
		t.Fatalf("delete change = %+v", rc)
	}
}

func TestReactiveCollTopic(t *testing.T) {
	if got := bluedbCollTopic("todos"); got != "__bluedb:todos" {
		t.Fatalf("topic = %q", got)
	}
}
