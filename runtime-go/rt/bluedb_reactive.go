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
	"encoding/json"
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

// ── Query-overlap engine (P-R3 / decision A2) ────────────────────────────────
//
// A reactive query subscription tracks its collection, its Cond (the P5 plan node,
// evaluated by bluedbEvalCond), and the pk set of its current result. Given a
// record change, bluedbChangeAffectsQuery decides whether the query MUST re-run —
// a precise, correct-by-construction narrowing of the always-re-run fallback.

type bluedbQuerySub struct {
	coll      string
	cond      map[string]any  // P5 plan Cond node; nil/empty ⇒ match-all
	resultPks map[string]bool // pks currently in the query's result set
}

func newBluedbQuerySub(coll string, cond map[string]any) *bluedbQuerySub {
	return &bluedbQuerySub{coll: coll, cond: cond, resultPks: map[string]bool{}}
}

// setResultPks replaces the tracked result-pk set (called after each (re-)run).
func (s *bluedbQuerySub) setResultPks(pks []string) {
	m := make(map[string]bool, len(pks))
	for _, pk := range pks {
		m[pk] = true
	}
	s.resultPks = m
}

// bluedbChangeAffectsQuery reports whether a record change could change the query's
// result set, so the query must re-run. Reasoning (never misses a transition —
// enter, leave, or in-place change — while skipping provably-irrelevant changes):
//   - resync → always (we missed deltas).
//   - other collection → never.
//   - pk already IN the result → any change to it (update or delete) can change the
//     result → re-run.
//   - pk NOT in the result + delete → deleting a row that wasn't shown can't change
//     the result → skip.
//   - pk NOT in the result + put/update → re-run IFF the new record now matches the
//     Cond (the row just entered the result).
func bluedbChangeAffectsQuery(rc bluedbRecordChange, s *bluedbQuerySub) bool {
	if rc.Resync {
		return true
	}
	if rc.Coll != s.coll {
		return false
	}
	if s.resultPks[rc.Pk] {
		return true
	}
	if rc.IsDelete {
		return false
	}
	var m map[string]any
	if json.Unmarshal([]byte(rc.Record), &m) != nil {
		return true // undecodable record → re-run rather than risk missing a change
	}
	return bluedbEvalCond(s.cond, m)
}

// ── Broker publish (P-R4a) ───────────────────────────────────────────────────

// bluedbChangePayload is the JSON payload delivered to Persist.watch* subscribers.
type bluedbChangePayload struct {
	Op     string `json:"op"`     // "put" | "delete"
	Coll   string `json:"coll"`   // collection name
	Pk     string `json:"pk"`     // primary key
	Record string `json:"record"` // record JSON for a put; "" for a delete
}

// bluedbPublishChange routes one decoded record change to the running Sky.Live
// app's broker (the process-global handle), on the change's collection topic
// (bluedbCollTopic). A no-op when no Live app is registered (a CLI / BlueDB-only
// process), and for a resync marker (not collection-targeted — the declarative
// layer re-runs all queries on resync).
func bluedbPublishChange(rc bluedbRecordChange) {
	app := processBroker.Load()
	if app == nil || rc.Resync {
		return
	}
	op := "put"
	if rc.IsDelete {
		op = "delete"
	}
	payload, err := json.Marshal(bluedbChangePayload{Op: op, Coll: rc.Coll, Pk: rc.Pk, Record: rc.Record})
	if err != nil {
		return
	}
	topic := bluedbCollTopic(rc.Coll)
	app.Publish(topic, SessionEvent{Topic: topic, Payload: string(payload)})
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
