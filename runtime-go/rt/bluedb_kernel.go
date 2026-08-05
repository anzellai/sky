// bluedb_kernel.go — the Std.BlueDB kernel surface: an embedded, durable,
// group-committed key-value store a Sky app can use for its OWN data (beyond the
// Sky.Live session store). Backed by the runtime-go/bluedb engine. Follows the
// same opaque-handle-registry pattern as Std.Cache: the Sky `Store` wraps an int
// handle; this registry maps the handle to the open *bluedb.DB.
package rt

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"sky-app/bluedb"
)

// The working set is RAM-resident (docs/bluedb/capacity.md), so both bounds are
// ON by default: a single pathological value, and a key-count safety ceiling
// that returns ErrFull instead of OOM-killing the process. The ceiling is
// generous (a v1 embedded store past it should move to the distributed v2 tier);
// it's a guard, not a tight quota.
const (
	bluedbMaxValueBytes = 64 << 20   // 64 MiB
	bluedbMaxKeys       = 20_000_000 // ~20M keys before ErrFull (OOM guard)
)

type bluedbEntry struct {
	db   *bluedb.DB
	path string
}

var (
	bluedbRegistry = map[int64]*bluedbEntry{} // handle id → open store
	bluedbByPath   = map[string]int64{}       // path → handle id (dedup)
	bluedbRegMu    sync.Mutex
	bluedbNextID   atomic.Int64
)

func bluedbLookup(idArg any) *bluedb.DB {
	id := int64(AsInt(idArg))
	bluedbRegMu.Lock()
	e := bluedbRegistry[id]
	bluedbRegMu.Unlock()
	if e == nil {
		return nil
	}
	return e.db
}

// bluedbRegisterOpen opens `path` with `opts` and registers the handle, honouring
// the open-once-per-path contract: a path already open returns the SAME handle
// (never a second engine on one WAL, which would corrupt it) and `opts` is
// IGNORED for that handle — the live handle keeps its original options. A racing
// concurrent open is collapsed under the lock (the duplicate engine is closed).
// Shared by BlueDB_open (default options) and BlueDB_openWith (explicit options).
func bluedbRegisterOpen(path string, opts bluedb.Options) any {
	bluedbRegMu.Lock()
	if id, ok := bluedbByPath[path]; ok {
		bluedbRegMu.Unlock()
		return Ok[any, any](int(id)) // reuse the existing handle
	}
	bluedbRegMu.Unlock()

	if dir := filepath.Dir(path); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o755) // create the parent dir so "data/app.blue" just works
	}
	db, err := bluedb.Open(path, opts)
	if err != nil {
		return Err[any, any](ErrFfi("BlueDB.open: " + err.Error()))
	}
	id := bluedbNextID.Add(1)
	bluedbRegMu.Lock()
	// Re-check under the lock in case of a concurrent open of the same path.
	if existing, ok := bluedbByPath[path]; ok {
		bluedbRegMu.Unlock()
		_ = db.Close() // discard the racing duplicate
		return Ok[any, any](int(existing))
	}
	bluedbRegistry[id] = &bluedbEntry{db: db, path: path}
	bluedbByPath[path] = id
	bluedbRegMu.Unlock()
	bluedbMaybeStartPump(id, db)
	return Ok[any, any](int(id))
}

// BlueDB_open : String -> Task Error Int
//
// Idempotent per path within a process: a second open of the same path returns
// the SAME handle (never a second engine on one WAL, which would corrupt it).
// Across processes the engine's advisory file lock refuses the second open
// (ErrLocked).
func BlueDB_open(pathArg any) any {
	return func() any {
		path := AsString(pathArg)
		if path == "" {
			return Err[any, any](ErrInvalidInput("BlueDB.open: empty path"))
		}
		return bluedbRegisterOpen(path, bluedb.Options{
			Sync:            true,
			CheckpointEvery: 10000,
			MaxValueBytes:   bluedbMaxValueBytes,
			MaxKeys:         bluedbMaxKeys,
		})
	}
}

// BlueDB_openWith : String -> Bool -> Int -> Int -> Int -> Task Error Int
//
// The options-carrying open: exposes the engine's Options (sync / checkpointEvery
// / maxValueBytes / maxKeys) to Sky. `sync=false` selects the relaxed durability
// tier (higher throughput; survives a process crash, NOT power loss). Idempotent
// per path exactly like BlueDB_open — and because a path already open keeps its
// ORIGINAL options, these options are IGNORED when the handle is reused. That
// reuse is logged (bluedb.open.options-ignored) so a second openWith with
// different options isn't a silent no-op for the operator.
func BlueDB_openWith(pathArg, syncArg, checkpointEveryArg, maxValueBytesArg, maxKeysArg any) any {
	return func() any {
		path := AsString(pathArg)
		if path == "" {
			return Err[any, any](ErrInvalidInput("BlueDB.openWith: empty path"))
		}
		opts := bluedb.Options{
			Sync:            AsBool(syncArg),
			CheckpointEvery: AsInt(checkpointEveryArg),
			MaxValueBytes:   AsInt(maxValueBytesArg),
			MaxKeys:         AsInt(maxKeysArg),
		}
		// Open-once contract: a path already open keeps its original options; the
		// ones passed here won't take effect. Surface that to the operator.
		bluedbRegMu.Lock()
		_, reused := bluedbByPath[path]
		bluedbRegMu.Unlock()
		if reused {
			logStructured("warn", "bluedb.open.options-ignored", "path", path, "reason", "already-open")
		}
		return bluedbRegisterOpen(path, opts)
	}
}

// Reactive change-feed pumps, one per open data store (P-R4a). Keyed by the same
// store handle as bluedbRegistry; guarded by its own mutex to avoid coupling to
// bluedbRegMu (the pump start calls into the engine's own locks).
var (
	bluedbPumpMu sync.Mutex
	bluedbPumps  = map[int64]func(){}
)

// bluedbMaybeStartPump starts the reactive change-feed pump for a newly-opened
// store IFF a Sky.Live app is running (so its writes can drive reactive UI
// updates). No Live app (CLI / BlueDB-only) → no pump and no per-write overhead.
// (The normal flow opens data stores after the Live app boots, so the app handle
// is present; a store opened before boot simply isn't reactive.)
func bluedbMaybeStartPump(id int64, db *bluedb.DB) {
	if processBroker.Load() == nil {
		return
	}
	bluedbPumpMu.Lock()
	defer bluedbPumpMu.Unlock()
	if _, running := bluedbPumps[id]; running {
		return
	}
	bluedbPumps[id] = bluedbStartReactivePump(db, bluedbPublishChange)
}

// bluedbStopPump stops and unregisters a store's reactive pump (called on close).
func bluedbStopPump(id int64) {
	bluedbPumpMu.Lock()
	stop := bluedbPumps[id]
	delete(bluedbPumps, id)
	bluedbPumpMu.Unlock()
	if stop != nil {
		stop()
	}
}

// BlueDB_put : Int -> String -> String -> Task Error ()
func BlueDB_put(idArg, keyArg, valueArg any) any {
	return func() any {
		db := bluedbLookup(idArg)
		if db == nil {
			return Err[any, any](ErrInvalidInput("BlueDB.put: store not found (closed?)"))
		}
		if strings.ContainsRune(AsString(keyArg), 0) {
			return Err[any, any](ErrInvalidInput("BlueDB.put: key must not contain NUL (reserved for the index keyspace)"))
		}
		if err := db.Put([]byte(AsString(keyArg)), []byte(AsString(valueArg))); err != nil {
			return Err[any, any](ErrFfi("BlueDB.put: " + err.Error()))
		}
		return Ok[any, any](nil)
	}
}

// BlueDB_batch : Int -> List (String, String, String) -> Task Error ()
//
// Each triple is (tag, key, value) with tag ∈ {"put","del"} (value ignored for
// "del"). Commits every op ATOMICALLY as ONE group-commit (all-or-nothing, one
// fsync) via the engine's WriteBatch — the multi-key atomic write. This is the
// RAW kv layer (like BlueDB.put): it does NOT maintain secondary indexes. That
// is intended — use collPut/putIndexed for indexed collections.
func BlueDB_batch(idArg, opsArg any) any {
	return func() any {
		db := bluedbLookup(idArg)
		if db == nil {
			return Err[any, any](ErrInvalidInput("BlueDB.batch: store not found (closed?)"))
		}
		// Reuse the proven triple decode (SkyTuple3 / T3, read reflectively):
		// here the triple is (tag, key, value) rather than (field, value, colType).
		b := bluedb.NewBatch()
		for _, t := range bluedbParseFVT(opsArg) {
			tag, key, value := t.field, t.value, t.colType
			if strings.ContainsRune(key, 0) {
				return Err[any, any](ErrInvalidInput("BlueDB.batch: key must not contain NUL (reserved for the index keyspace)"))
			}
			switch tag {
			case "put":
				b.Put([]byte(key), []byte(value))
			case "del":
				b.Delete([]byte(key))
			default:
				return Err[any, any](ErrInvalidInput("BlueDB.batch: unknown op \"" + tag + "\" (expected put|del)"))
			}
		}
		if b.Len() == 0 {
			return Ok[any, any](nil) // empty batch = no-op; never commit an empty batch
		}
		if err := db.WriteBatch(b); err != nil {
			return Err[any, any](ErrFfi("BlueDB.batch: " + err.Error()))
		}
		return Ok[any, any](nil)
	}
}

// BlueDB_get : Int -> String -> Task Error (Maybe String)
func BlueDB_get(idArg, keyArg any) any {
	return func() any {
		db := bluedbLookup(idArg)
		if db == nil {
			return Err[any, any](ErrInvalidInput("BlueDB.get: store not found (closed?)"))
		}
		v, ok := db.Get([]byte(AsString(keyArg)))
		if !ok {
			return Ok[any, any](makeMaybeNothing())
		}
		// Copy out: db.Get returns the DB-owned slice.
		return Ok[any, any](makeMaybeJust(string(v)))
	}
}

// BlueDB_delete : Int -> String -> Task Error ()
func BlueDB_delete(idArg, keyArg any) any {
	return func() any {
		db := bluedbLookup(idArg)
		if db == nil {
			return Err[any, any](ErrInvalidInput("BlueDB.delete: store not found (closed?)"))
		}
		if err := db.Delete([]byte(AsString(keyArg))); err != nil {
			return Err[any, any](ErrFfi("BlueDB.delete: " + err.Error()))
		}
		return Ok[any, any](nil)
	}
}

// BlueDB_has : Int -> String -> Task Error Bool
func BlueDB_has(idArg, keyArg any) any {
	return func() any {
		db := bluedbLookup(idArg)
		if db == nil {
			return Err[any, any](ErrInvalidInput("BlueDB.has: store not found (closed?)"))
		}
		_, ok := db.Get([]byte(AsString(keyArg)))
		return Ok[any, any](ok)
	}
}

// BlueDB_keys : Int -> Task Error (List String)
func BlueDB_keys(idArg any) any {
	return func() any {
		db := bluedbLookup(idArg)
		if db == nil {
			return Err[any, any](ErrInvalidInput("BlueDB.keys: store not found (closed?)"))
		}
		keys := []any{}
		db.ForEach(func(k, _ []byte) bool {
			if bluedbIsReserved(string(k)) { // hide index/manifest entries from app keys
				return true
			}
			keys = append(keys, string(k))
			return true
		})
		return Ok[any, any](keys)
	}
}

// BlueDB_scan : Int -> String -> String -> Int -> Task Error (List (String, String))
// Prefix scan in ascending key order, starting strictly after `startAfter` ("" =
// from the beginning), up to `limit` pairs (limit <= 0 = no cap). Deterministic +
// paginable: pass the last key as the next startAfter. Admin/inspection path.
func BlueDB_scan(idArg, prefixArg, startAfterArg, limitArg any) any {
	return func() any {
		db := bluedbLookup(idArg)
		if db == nil {
			return Err[any, any](ErrInvalidInput("BlueDB.scan: store not found (closed?)"))
		}
		prefixS := AsString(prefixArg)
		prefix := []byte(prefixS)
		after := []byte(AsString(startAfterArg))
		limit := int(AsInt(limitArg))
		// Hide the reserved index/manifest keyspace UNLESS the caller explicitly
		// scans into it (the admin escape hatch: scan "\x00...").
		hideReserved := !strings.HasPrefix(prefixS, bluedbReserved)
		pairs := []any{}
		db.Scan(prefix, after, limit, func(k, v []byte) bool {
			if hideReserved && bluedbIsReserved(string(k)) {
				return true
			}
			pairs = append(pairs, SkyTuple2{V0: string(k), V1: string(v)})
			return true
		})
		return Ok[any, any](pairs)
	}
}

// BlueDB_close : Int -> Task Error () — idempotent.
func BlueDB_close(idArg any) any {
	return func() any {
		id := int64(AsInt(idArg))
		bluedbRegMu.Lock()
		e := bluedbRegistry[id]
		if e != nil {
			delete(bluedbRegistry, id)
			delete(bluedbByPath, e.path)
		}
		bluedbRegMu.Unlock()
		bluedbStopPump(id)
		if e == nil {
			return Ok[any, any](nil)
		}
		if err := e.db.Close(); err != nil {
			return Err[any, any](ErrFfi("BlueDB.close: " + err.Error()))
		}
		return Ok[any, any](nil)
	}
}
