// bluedb_kernel.go — the Std.BlueDB kernel surface: an embedded, durable,
// group-committed key-value store a Sky app can use for its OWN data (beyond the
// Sky.Live session store). Backed by the runtime-go/bluedb engine. Follows the
// same opaque-handle-registry pattern as Std.Cache: the Sky `Store` wraps an int
// handle; this registry maps the handle to the open *bluedb.DB.
package rt

import (
	"os"
	"path/filepath"
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
		bluedbRegMu.Lock()
		if id, ok := bluedbByPath[path]; ok {
			bluedbRegMu.Unlock()
			return Ok[any, any](int(id)) // reuse the existing handle
		}
		bluedbRegMu.Unlock()

		if dir := filepath.Dir(path); dir != "" && dir != "." {
			_ = os.MkdirAll(dir, 0o755) // create the parent dir so "data/app.blue" just works
		}
		db, err := bluedb.Open(path, bluedb.Options{
			Sync:            true,
			CheckpointEvery: 10000,
			MaxValueBytes:   bluedbMaxValueBytes,
			MaxKeys:         bluedbMaxKeys,
		})
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
		return Ok[any, any](int(id))
	}
}

// BlueDB_put : Int -> String -> String -> Task Error ()
func BlueDB_put(idArg, keyArg, valueArg any) any {
	return func() any {
		db := bluedbLookup(idArg)
		if db == nil {
			return Err[any, any](ErrInvalidInput("BlueDB.put: store not found (closed?)"))
		}
		if err := db.Put([]byte(AsString(keyArg)), []byte(AsString(valueArg))); err != nil {
			return Err[any, any](ErrFfi("BlueDB.put: " + err.Error()))
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
		prefix := []byte(AsString(prefixArg))
		after := []byte(AsString(startAfterArg))
		limit := int(AsInt(limitArg))
		pairs := []any{}
		db.Scan(prefix, after, limit, func(k, v []byte) bool {
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
		if e == nil {
			return Ok[any, any](nil)
		}
		if err := e.db.Close(); err != nil {
			return Err[any, any](ErrFfi("BlueDB.close: " + err.Error()))
		}
		return Ok[any, any](nil)
	}
}
