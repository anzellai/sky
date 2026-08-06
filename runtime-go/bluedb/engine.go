package bluedb

import "errors"

// Op discriminates a versioned write.
type Op uint8

const (
	OpPut Op = iota
	OpDelete
)

// Sentinel errors (frozen contract surface).
var (
	// ErrSnapshotTooOld: a picked readTs would sit below the GC threshold T.
	// Defensive only — under the register-before-advance barrier a freshly-picked
	// readTs is always >= T (§5.2).
	ErrSnapshotTooOld = errors.New("bluedb: snapshot too old")
	// ErrSealed: the engine hit an unrollbackable fault and refuses further writes.
	ErrSealed = errors.New("bluedb: engine sealed")
	// ErrMissingCommitMetadata: a logical-commit batch reached Apply without its
	// hlc_hi metadata key — a compiler-bug-class fault, never a silent write (§3.4).
	ErrMissingCommitMetadata = errors.New("bluedb: logical-commit batch missing hlc_hi metadata")
	// ErrClosed: the engine has been closed.
	ErrClosed = errors.New("bluedb: engine closed")
	// ErrConflict: a transaction's read-set failed commit-time validation (a concurrent
	// commit touched a read point key or fell into a scanned index range). Retried by
	// Transact; returned typed after maxTxnAttempts. errors.Is-friendly so Phase 3 can
	// branch on it (§5.2).
	ErrConflict = errors.New("bluedb: transaction conflict")
)

// Engine is the L1 substrate. One Engine == one open file == one committer (§3.1).
type Engine interface {
	// Snapshot atomically picks readTs := current HLC high-water AND registers its
	// reader token in ONE critical section, then pins a lock-free consistent view
	// (a Pebble snapshot seqnum). There is NO caller-supplied readTs (grill 2a):
	// a caller must not be able to name a readTs below the GC threshold T.
	// Reader.Close unregisters the token.
	Snapshot() (Reader, error)

	// NowTs returns the current HLC high-water. Informational only (metadata,
	// metrics); NOT a way to derive a readTs for a later Snapshot (that reintroduces
	// the 2a TOCTOU). Cheap, no fsync.
	NowTs() HLC

	// Commit is the ONLY write path. It enqueues req to the single committer, which
	// assigns commitTs, writes data + changelog + metadata in ONE atomic batch,
	// Apply(Sync), and only then acks. Blocks until durable-or-error.
	Commit(req CommitReq) CommitResult

	// Changelog exposes the post-commit stream ordered by commitTs (§4).
	Changelog() Changelog

	// Readers advances/queries the GC watermark registry (§5). The registry pins
	// reader readTs for snapshot consistency; GC advances the threshold behind it.
	Readers() WatermarkRegistry

	// GC runs one watermark version-GC pass (§5): advances the persisted, monotone
	// threshold T behind the register barrier, then issues PHYSICAL-ONLY deletes of
	// stale versions strictly below T (no commitTs, no changelog, no hlc_hi bump) and
	// trims the below-T changelog. Safe to call concurrently with Commit. Idempotent
	// when nothing is collectible.
	GC() (GCStats, error)

	// Close drains the committer and closes Pebble.
	Close() error

	// Begin opens a single-attempt transaction pinned at a fresh begin-snapshot (§3.4):
	// readTs = durableHi (the durably-applied high-water), the Pebble snapshot pinned
	// atomically with that choice, and the readTs registered in the watermark — the
	// retention invariant AND the window-boundary soundness (R-2.8). The body reads through
	// the Txn (recording the read-set) and buffers writes; txn.Commit() funnels one
	// CommitReq to the committer (§1.1).
	Begin() (*Txn, error)

	// Transact runs the optimistic loop: Begin → body → Commit; on ErrConflict it re-runs
	// body against a FRESH snapshot (bounded retry + backoff), returning nil on durable
	// commit or a typed ErrConflict after the bound. The body MUST be pure / re-runnable
	// (§5.3) — the API only exposes effect-free Txn ops.
	Transact(body func(tx *Txn) error) error
}

// Reader is a lock-free, snapshot-consistent view as of a fixed readTs.
type Reader interface {
	// Get resolves userKey as of readTs. ok=false means absent or tombstoned.
	Get(userKey []byte) (value []byte, commitTs HLC, ok bool)
	// Iterate returns an ordered, snapshot-consistent cursor over user-keys sharing
	// the given prefix (nil ⇒ the whole data keyspace), newest visible version per
	// distinct user-key, tombstones skipped.
	Iterate(prefix []byte) Cursor
	ReadTs() HLC
	// Close unregisters this reader's watermark token and releases the pinned view.
	Close()
}

// Cursor is an ordered scan over distinct visible user-keys.
type Cursor interface {
	Next() bool
	Key() []byte // the user-key (version stripped)
	Value() []byte
	CommitTs() HLC
	Err() error
	Close()
}

// CommitReq is one atomic unit funneled to the committer.
type CommitReq struct {
	// Writes is the buffered write-set (put/delete per user-key).
	Writes []VersionedWrite

	// ChangelogPayload is OPAQUE to L1 (grill 1b): L2 owns the encode/decode of the
	// KeyChange list; the engine stores these bytes verbatim at 0x01‖commitTs and
	// hands them back unparsed on tail-read. Keeps bluedb.Engine a clean KV/MVCC
	// substrate. Empty ⇒ no changelog entry is written for this commit.
	ChangelogPayload []byte

	// Phase-2 fields (nil/empty in phase 1 → pure blind-write fast path). The engine
	// does not interpret their contents.
	ReadTs  HLC      // the body's read view = Reader.ReadTs() (never NowTs())
	ReadSet *ReadSet // nil ⇒ skip validation
}

// VersionedWrite is one put/delete against a user-key.
type VersionedWrite struct {
	UserKey []byte
	Op      Op
	Value   []byte // nil for delete
}

// CommitResult is the outcome of a Commit.
type CommitResult struct {
	CommitTs HLC
	Err      error // nil ⇒ durable
}

// ReadSet is the phase-2 validation input (opaque to L1 in phase 1). Declared here so
// CommitReq's shape stays frozen; its fields are L2's (§2). The committer runs validate()
// over these against the (readTs, commitTs] window. Supporting types (pointRead, indexRange)
// live in readset.go — same package (L2-embedded is more Go in bluedb).
type ReadSet struct {
	points       map[string]pointRead // point-key dependencies (§2.1)
	ranges       []indexRange         // scanned index intervals (§2.2 — the SSI crux)
	collWitness  map[CollID]bool      // conservative collection-level fallback witness (§2.2)
	indexWitness map[IndexID]bool     // conservative index-level fallback witness (§2.2)
}

// Changelog exposes the commitTs-ordered post-commit stream (§4). Phase 1a
// provides the tail READ; L2 validation / L4 reactivity consume it in later phases.
type Changelog interface {
	// Tail returns every (commitTs, opaque payload) entry with commitTs in
	// (after, +inf), ascending, up to the current high-water. after.IsZero() reads
	// from the beginning.
	Tail(after HLC) ([]ChangelogEntry, error)
}

// ChangelogEntry is one commitTs-keyed opaque changelog record.
type ChangelogEntry struct {
	CommitTs HLC
	Payload  []byte // opaque L1 bytes (L2-owned encoding)
}

// WatermarkRegistry tracks live reader readTs tokens so GC never drops a version a
// live reader still needs (§5.2). The GC-threshold ADVANCE pass is phase 1b; phase
// 1a implements Register/Release (snapshot readTs pinning) + a persisted Threshold.
type WatermarkRegistry interface {
	// Register ATOMICALLY picks readTs := current high-water AND records the token in
	// ONE critical section (closes 2a). No caller-supplied readTs.
	Register() (tok ReaderToken, readTs HLC, err error)
	// Advance moves a reactive binding's token forward; must land >= Threshold().
	Advance(tok ReaderToken, readTs HLC) error
	Release(tok ReaderToken)
	// Threshold returns T — the persisted, monotone GC floor.
	Threshold() HLC
}

// ReaderToken identifies a registered reader in the watermark registry.
type ReaderToken uint64
