// bluedb_reactive.go — P-R2 of the reactive scope-sync layer.
//
// Bridges the BlueDB engine change-feed (P-R1) to RECORD-level changes the higher
// layers reason about: it decodes each committed key back to its (collection, pk),
// keeps only real record writes (dropping the sibling index/unique/manifest/seq
// keys a single upsert also emits — "one record changed, not five"), and pumps
// those to a publish callback. A feed overflow surfaces as a Resync signal so a
// consumer that fell behind re-reads instead of trusting a partial delta.
package rt

import (
	"strings"

	"sky-app/bluedb"
)

// bluedbRecordChange is one record-level change derived from the engine change-feed.
type bluedbRecordChange struct {
	Coll     string // collection name
	Pk       string // primary key
	IsDelete bool   // true → the record was deleted
	Record   string // record JSON for a put; "" for a delete
	Resync   bool   // true → the feed overflowed; re-read everything (Coll/Pk empty)
}

// bluedbRecordKeyTag is the record-key discriminator prefix: \x00x\x00d\x00.
const bluedbRecordKeyTag = bluedbReserved + "d\x00"

// bluedbCollTopic is the broker topic a collection's changes publish to.
func bluedbCollTopic(coll string) string { return "__bluedb:" + coll }

// bluedbDecodeRecordKey decodes a committed key back to (collection, pk) IFF it is
// a record key (\x00x\x00d\x00<coll>\x00<pk>). Index/unique/manifest/seq keys
// (\x00x\x00{i,u,m,s}\x00…) and bare/unnamespaced keys return ok=false — they are
// internal churn, not user-visible record changes. coll and pk are NUL-free (the
// kernel enforces this on write), so the split on the first NUL is unambiguous.
func bluedbDecodeRecordKey(key string) (coll, pk string, ok bool) {
	if !strings.HasPrefix(key, bluedbRecordKeyTag) {
		return "", "", false
	}
	rest := key[len(bluedbRecordKeyTag):] // <coll>\x00<pk>
	i := strings.IndexByte(rest, 0)
	if i <= 0 || i == len(rest)-1 {
		return "", "", false // missing coll, missing separator, or empty pk
	}
	return rest[:i], rest[i+1:], true
}

// bluedbDecodeChanges turns one committed batch of ChangeEvents into record-level
// changes, keeping only record keys (so a record upsert that also wrote index +
// unique + seq keys yields exactly ONE change for that record).
func bluedbDecodeChanges(evs []bluedb.ChangeEvent) []bluedbRecordChange {
	out := make([]bluedbRecordChange, 0, len(evs))
	for _, e := range evs {
		coll, pk, ok := bluedbDecodeRecordKey(string(e.Key))
		if !ok {
			continue
		}
		rc := bluedbRecordChange{Coll: coll, Pk: pk, IsDelete: e.IsDelete()}
		if e.IsPut() {
			rc.Record = string(e.Value)
		}
		out = append(out, rc)
	}
	return out
}

// bluedbStartReactivePump subscribes to a store's change-feed and pumps decoded
// record changes to publish on a background goroutine. On a feed overflow it first
// emits a Resync change (the consumer must re-read, having missed deltas). Returns
// a stop func that unsubscribes and ends the goroutine. publish runs on the pump
// goroutine — it must be non-blocking (the broker publish is).
func bluedbStartReactivePump(db *bluedb.DB, publish func(bluedbRecordChange)) func() {
	sub, cancel := db.Subscribe(0)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				cancel()
				return
			case evs := <-sub.C:
				if sub.Overflowed() {
					publish(bluedbRecordChange{Resync: true})
				}
				for _, rc := range bluedbDecodeChanges(evs) {
					publish(rc)
				}
			}
		}
	}()
	var stopped bool
	return func() {
		if stopped {
			return
		}
		stopped = true
		close(done)
	}
}
