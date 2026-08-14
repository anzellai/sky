package bluedb

import (
	"errors"
	"fmt"

	"github.com/cockroachdb/pebble/v2"
)

// GCStats reports the outcome of one GC pass (§5).
type GCStats struct {
	Threshold        HLC  // the GC floor T this pass ran at (post-advance)
	Advanced         bool // whether T moved up this pass
	KeysScanned      int  // distinct user-keys visited
	VersionsDeleted  int  // stale data versions physically deleted (strictly below T)
	ChangelogTrimmed bool // whether a below-T changelog range-tombstone was issued

	// CorruptKeys counts data keys in the scanned range that decodeDataVersion could NOT
	// parse (Stage-1 made it (HLC, bool); see the delete loop below). Such a key is SKIPPED,
	// never deleted — GC must not destroy evidence it cannot read. But skipping alone leaks
	// the key permanently and INVISIBLY: GC's bounds are the whole data keyspace, so every
	// later pass re-visits it and no counter records that it did. This field is that record,
	// and maxCorruptKeysPerPass is the point at which the pass stops looping and errors.
	CorruptKeys int
}

// maxCorruptKeysPerPass is the documented threshold at which a GC pass STOPS and returns an
// error rather than skipping onward. One or two unparseable keys is a datum worth surfacing
// in stats; thousands means the data keyspace is damaged (or written by a format this build
// does not understand), and continuing to sweep it every pass — forever, silently — is worse
// than refusing. The pass is aborted BEFORE its batch is applied, so nothing is deleted.
const maxCorruptKeysPerPass = 1024

// ErrCorruptDataKeys is returned by GC when a pass exceeds maxCorruptKeysPerPass unparseable
// data keys. It is a diagnosis, not a repair: the keys are left on disk untouched.
var ErrCorruptDataKeys = errors.New("bluedb: GC found unparseable data keys")

// GC runs one watermark version-GC pass (§5.1/§5.2). It is safe to call
// concurrently with the committer: every write it issues lands on PHYSICAL keys
// disjoint from anything the committer writes (already-committed, provably-dead data
// versions strictly below T; the gc_threshold metadata key; and the below-T changelog
// range — the committer only ever writes changelog keys at commitTs >= high-water >= T).
// So its side Apply is key-safe under Pebble's batch semantics (C1 amendment, §8.2).
//
// The three grill-critical properties (do NOT deviate):
//   - GC advances T only behind the register barrier (advanceThreshold), so no
//     in-flight registration can sit below the new T (closes grill 2a).
//   - GC deletes are PHYSICAL ONLY — raw db.Delete on the exact version key via a side
//     Apply(NoSync): NO commitTs, NO changelog entry, NO hlc_hi bump (closes grill 2b).
//   - The newest version < T per user-key is KEPT (a reader at exactly T must still
//     resolve it); only strictly-older versions below T are dropped; a key's sole
//     remaining version is never dropped.
func (e *pebbleEngine) GC() (GCStats, error) {
	if e.isClosed() {
		return GCStats{}, ErrClosed
	}
	if e.sealed.Load() {
		return GCStats{}, ErrSealed
	}

	// (1) Advance T behind the register barrier (§5.2 part iii). This is the ONLY
	// place T moves; it moves up only. The candidate is min-over-live, or the
	// high-water when the live set is empty (the load-bearing empty-set rule).
	T, advanced := e.reg.advanceThreshold()

	// Marshal the ring trim onto the committer goroutine (Fix-3/R-2.9): GC NEVER mutates the
	// recent-changes ring directly (that would race the committer's append). It enqueues the
	// new T; the committer drains it (drainTrimRequests) at the top of its next drain and
	// applies recent.trim(T) on its own goroutine → the ring stays single-writer.
	if advanced {
		e.enqueueTrim(T)
	}

	// (2) Persist T durably BEFORE issuing any physical delete. If we crash between
	// this Sync and the (NoSync) deletes, T is durable-high and the versions are still
	// present → a later pass re-collects them. The reverse order could leave a durable
	// delete under a regressed T. gc_threshold (tag 0x02) is disjoint from the
	// committer's hlc_hi/changelog_cursor metadata → key-safe concurrent Apply.
	if advanced {
		if err := e.persistThreshold(T); err != nil {
			return GCStats{Threshold: T, Advanced: advanced}, err
		}
	}

	stats := GCStats{Threshold: T, Advanced: advanced}
	if T.IsZero() {
		// Nothing is strictly below {0,0}; no versions are collectible yet.
		return stats, nil
	}

	// (3) The delete pass. Scan the whole data keyspace ascending. Keys sort by
	// (user-key asc, version DESC) because the version suffix is bit-inverted, so within
	// each user-key prefix the NEWEST version comes first. For each prefix: keep every
	// version >= T; keep the first (newest) version < T; delete every strictly-older
	// version < T. GC deletes are point db.Delete on the exact version key.
	batch := e.db.NewBatch()
	defer batch.Close()

	lo := []byte{tagData}
	hi := []byte{tagChangelog}
	iter, err := e.db.NewIter(&pebble.IterOptions{LowerBound: lo, UpperBound: hi})
	if err != nil {
		return stats, err
	}

	var curPrefix []byte
	var keptBelowT bool
	for ok := iter.First(); ok; ok = iter.Next() {
		k := iter.Key()
		prefix := k[:skydbSplit(k)]
		if curPrefix == nil || !bytesEqual(prefix, curPrefix) {
			curPrefix = append([]byte(nil), prefix...)
			keptBelowT = false
			stats.KeysScanned++
		}
		ts, ok := decodeDataVersion(k)
		if !ok {
			// NEVER delete a key you cannot parse. A key whose version suffix does not
			// decode is either corrupt or written by a format this build does not know;
			// either way GC has no basis to call it dead, and destroying it destroys the
			// only evidence of the fault. Skip it, COUNT it (GCStats.CorruptKeys), and
			// abort the pass once the count says the keyspace — not a stray key — is the
			// problem. `_`-ing the ok flag here would silently resurrect the pre-Stage-1
			// behaviour of deleting on a {0,0} misparse.
			stats.CorruptKeys++
			if stats.CorruptKeys > maxCorruptKeysPerPass {
				_ = iter.Close()
				return stats, fmt.Errorf("%w: %d unparseable keys in one pass (threshold %d); nothing deleted",
					ErrCorruptDataKeys, stats.CorruptKeys, maxCorruptKeysPerPass)
			}
			continue
		}
		if !ts.Less(T) {
			continue // >= T: some reader may need it → keep
		}
		// ts < T
		if !keptBelowT {
			keptBelowT = true // the newest version < T → keep (a reader at exactly T needs it)
			continue
		}
		// strictly older than the kept newest-<T version → physically drop
		dead := append([]byte(nil), k...)
		if err := batch.Delete(dead, nil); err != nil {
			_ = iter.Close()
			return stats, err
		}
		stats.VersionsDeleted++
	}
	if err := iter.Error(); err != nil {
		_ = iter.Close()
		return stats, err
	}
	if err := iter.Close(); err != nil {
		return stats, err
	}

	// (4) Changelog retention: trim 0x01 ‖ [0, T) via a range tombstone. Below T no
	// validator (L2) or binding (L4) needs the changelog. The committer never writes a
	// changelog key below T (its commitTs is always >= high-water >= T), so this range
	// is disjoint from any concurrent commit.
	clLo := []byte{tagChangelog}
	clHi := encodeChangelogKey(T) // exclusive upper: keys strictly below commitTs T
	if skydbCompare(clLo, clHi) < 0 {
		if err := batch.DeleteRange(clLo, clHi, nil); err != nil {
			return stats, err
		}
		stats.ChangelogTrimmed = true
	}

	if batch.Empty() {
		return stats, nil
	}
	// PHYSICAL-ONLY side Apply, NoSync (grill 2b): dead versions need no individual
	// fsync — a later real commit's Sync (or a background compaction) flushes them.
	if err := e.db.Apply(batch, pebble.NoSync); err != nil {
		return stats, err
	}
	return stats, nil
}

// persistThreshold durably writes the GC threshold T to the gc_threshold metadata
// key (tag 0x02). Sync so T is monotone across a crash. This is GC bookkeeping, not a
// logical commit: it carries NO commitTs, writes NO data version, appends NO changelog
// entry, and does NOT touch hlc_hi — so it never perturbs the commit total order or
// reactivity (grill 2b).
func (e *pebbleEngine) persistThreshold(t HLC) error {
	b := e.db.NewBatch()
	defer b.Close()
	if err := b.Set(encodeMetaKey(metaGCThreshold), encodeHLC(t), nil); err != nil {
		return err
	}
	return e.db.Apply(b, pebble.Sync)
}

// bytesEqual is a tiny local equality (avoids importing bytes solely for this).
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
