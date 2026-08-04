package rt

import (
	"encoding/json"
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

func parseCondNode(t *testing.T, s string) map[string]any {
	t.Helper()
	if s == "" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("parse cond: %v", err)
	}
	return m
}

// P-R3: the query-overlap engine re-runs iff a change could change the result —
// row enters (match), row leaves (was in result), delete of a result pk — and
// skips provably-irrelevant changes.
func TestChangeAffectsQuery(t *testing.T) {
	// query: status == "active", currently showing {u1}
	cond := parseCondNode(t, `{"t":"op","col":"status","op":"=","v":{"k":"str","s":"active"}}`)
	sub := newBluedbQuerySub("users", cond)
	sub.setResultPks([]string{"u1"})

	put := func(coll, pk, rec string) bluedbRecordChange {
		return bluedbRecordChange{Coll: coll, Pk: pk, Record: rec}
	}
	del := func(coll, pk string) bluedbRecordChange {
		return bluedbRecordChange{Coll: coll, Pk: pk, IsDelete: true}
	}

	cases := []struct {
		name string
		rc   bluedbRecordChange
		want bool
	}{
		{"row enters (new active)", put("users", "u2", `{"status":"active"}`), true},
		{"unrelated put (idle, not in result)", put("users", "u3", `{"status":"idle"}`), false},
		{"result row updated (u1 → idle: may leave)", put("users", "u1", `{"status":"idle"}`), true},
		{"result row deleted", del("users", "u1"), true},
		{"delete of non-result row", del("users", "u3"), false},
		{"other collection", put("orders", "o1", `{"status":"active"}`), false},
		{"resync", bluedbRecordChange{Resync: true}, true},
		{"undecodable record → re-run", put("users", "u4", `{not json`), true},
	}
	for _, c := range cases {
		if got := bluedbChangeAffectsQuery(c.rc, sub); got != c.want {
			t.Errorf("%s: affects=%v want %v", c.name, got, c.want)
		}
	}

	// match-all query (no where_): any put to its collection affects it; a delete
	// only if the pk was in the result.
	all := newBluedbQuerySub("users", parseCondNode(t, ""))
	all.setResultPks([]string{"z"})
	if !bluedbChangeAffectsQuery(put("users", "new", `{"x":1}`), all) {
		t.Fatal("match-all: a put must affect it")
	}
	if bluedbChangeAffectsQuery(del("users", "other"), all) {
		t.Fatal("match-all: deleting a non-result pk must not affect it")
	}
	if !bluedbChangeAffectsQuery(del("users", "z"), all) {
		t.Fatal("match-all: deleting a result pk must affect it")
	}
}

// P-R4a: opening a data store while a Live app is running starts the pump, and a
// write publishes a change to the collection topic on the app broker.
func TestReactivePumpPublishesToBroker(t *testing.T) {
	unregisterProcessBroker() // clear any leftover from a prior test
	reg := newTopicRegistry(16)
	app := &liveApp{topics: reg}
	registerProcessBroker(app)
	defer unregisterProcessBroker()

	ch, cancelSub := reg.Subscribe(bluedbCollTopic("users"))
	defer cancelSub()

	dir := t.TempDir()
	id := runOK(t, BlueDB_open(dir+"/data.blue")).(int)
	defer func() { BlueDB_close(id).(func() any)() }()

	collPut(t, id, "users", "u1", `{"id":"u1","name":"Ada"}`)

	select {
	case ev := <-ch:
		s, ok := ev.Payload.(string)
		if !ok {
			t.Fatalf("payload not a string: %T", ev.Payload)
		}
		var p bluedbChangePayload
		if err := json.Unmarshal([]byte(s), &p); err != nil {
			t.Fatalf("payload not JSON: %v (%q)", err, s)
		}
		if p.Op != "put" || p.Coll != "users" || p.Pk != "u1" || p.Record == "" {
			t.Fatalf("payload = %+v", p)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no change event delivered to the broker topic")
	}

	// No Live app → no publish (a store opened without a broker isn't reactive).
	unregisterProcessBroker()
	id2 := runOK(t, BlueDB_open(dir+"/data2.blue")).(int)
	defer func() { BlueDB_close(id2).(func() any)() }()
	// (pump not started for id2; nothing to assert beyond no panic / clean write)
	collPut(t, id2, "users", "x", `{"id":"x"}`)
}

// A feed-overflow resync is published as an op="resync" change on the collection
// topic (subscribers re-read rather than silently diverge).
func TestReactiveResyncPublish(t *testing.T) {
	unregisterProcessBroker()
	reg := newTopicRegistry(16)
	app := &liveApp{topics: reg}
	registerProcessBroker(app)
	defer unregisterProcessBroker()

	ch, cancel := reg.Subscribe(bluedbCollTopic("users"))
	defer cancel()

	bluedbPublishChange(bluedbRecordChange{Coll: "users", Resync: true})
	select {
	case ev := <-ch:
		var p bluedbChangePayload
		if err := json.Unmarshal([]byte(ev.Payload.(string)), &p); err != nil {
			t.Fatalf("payload not JSON: %v", err)
		}
		if p.Op != "resync" || p.Coll != "users" {
			t.Fatalf("resync payload = %+v want op=resync coll=users", p)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no resync change published to the collection topic")
	}
}
