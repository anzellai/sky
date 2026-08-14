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
	// ErrUnknownReader: WatermarkRegistry.Advance was handed a token that is not live
	// (already Released, or never issued). Returned rather than ignored (C6b) — a silent
	// no-op tells the caller its new readTs is protected from GC when nothing pins it.
	ErrUnknownReader = errors.New("bluedb: unknown reader token")
	// ErrPebbleFatal: pebble reported a Logger.Fatalf (defect N3). Fatalf no longer
	// panics — it latched, and this is that latch surfacing at the next point the
	// engine would otherwise have reported success. It is unrollbackable by
	// construction, so it always accompanies a seal on the commit paths.
	ErrPebbleFatal = errors.New("bluedb: pebble reported a fatal fault")
	// ErrReadersLive: Close's bounded drain expired with readers still pinned (defect
	// N4). The engine is NOT closed in that case — the Pebble handle is deliberately
	// left OPEN, because closing it would panic those readers' next operation — and
	// Close is retryable once they are released.
	ErrReadersLive = errors.New("bluedb: readers still live at close")
)

// Engine is the L1 substrate. One Engine == one open file == one committer (§3.1).
type Engine interface {
	// Snapshot pins a lock-free consistent view (a Pebble snapshot seqnum) for an
	// ad-hoc, transaction-less read. In ONE critical section it picks
	// readTs := durableHi — the highest commitTs whose Apply(Sync) has RETURNED — pins
	// the snapshot after that read, and registers the reader token at that same readTs
	// (defect H1: the in-memory HLC high-water is bumped BEFORE the Apply, so it can
	// name an assigned-but-not-yet-durable commitTs that no snapshot contains).
	// There is NO caller-supplied readTs (grill 2a): a caller must not be able to name
	// a readTs below the GC threshold T. Reader.Close unregisters the token.
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
	// Get resolves userKey as of readTs. ok=false means absent or tombstoned — OR, if
	// Err() is non-nil, that the read FAILED and says nothing about the row (defect H3).
	Get(userKey []byte) (value []byte, commitTs HLC, ok bool)
	// Iterate returns an ordered, snapshot-consistent cursor over user-keys sharing
	// the given prefix (nil ⇒ the whole data keyspace), newest visible version per
	// distinct user-key, tombstones skipped.
	Iterate(prefix []byte) Cursor
	// Err reports the first I/O error a point read on this reader hit, latched, or nil.
	// It mirrors Cursor.Err(): Get's three-value shape has no error channel, so without
	// this an unreadable block is indistinguishable from an absent key. Consumers MUST
	// fail closed on a non-nil Err rather than treat any Get's ok=false as absence —
	// Txn.Commit does exactly that (an I/O error laundered into "absent" is how a
	// swallowed error becomes an unwanted INSERT). Iterate's errors are reported by the
	// returned Cursor's own Err(), not here.
	Err() error
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

	// ChangelogPayload is stored VERBATIM at 0x01‖commitTs and handed back unparsed on
	// tail-read. Empty ⇒ no changelog entry is written for this commit.
	//
	// It is NOT opaque, and the previous wording here ("OPAQUE to L1: L2 owns the
	// encode/decode") was already false when it was written: the committer decodes it on
	// every commit to build the SSI validation window (`pending`), the recent-changes ring
	// and the change feed. The format is EncodeChangelogPayload's, which lives in this
	// package. What IS true is that L1 never interprets a KeyChange's CONTENTS — only its
	// key and index coordinates, which are what conflict detection is defined over.
	//
	// A payload that does not decode therefore FAILS THE COMMIT CLOSED (defect N6): it
	// cannot contribute to the validation window, and a job that commits without
	// contributing is a job a later transaction validates against a hole for. See
	// decodePayload.
	ChangelogPayload []byte

	// Phase-2 fields (nil/empty in phase 1 → pure blind-write fast path). The engine
	// does not interpret their contents.
	ReadTs  HLC      // the body's read view = Reader.ReadTs() (never NowTs())
	ReadSet *ReadSet // nil ⇒ skip validation

	// Tenant is the Phase-4 TRANSIENT reactive routing tag (§3.4). It is stamped by the
	// writing (identity-stamped) session on the write, copied onto the in-RAM changefeed
	// ChangeBatch at the post-Apply emit sites, and used ONLY to fan a delta to the entitled
	// tenant's subscription bucket (the fail-closed reactive gate, §4.5). It is NEVER written
	// durably: it is not part of ChangelogPayload, never reaches EncodeChangelogPayload, and
	// the L1 store never sees it. "" ⇒ a no-verified-tenant write (background/CLI/unauth) that
	// routes ONLY to the "" bucket, never the union of tenants.
	Tenant string
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
