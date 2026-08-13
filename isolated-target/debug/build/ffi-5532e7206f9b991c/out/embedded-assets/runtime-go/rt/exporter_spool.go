package rt

// HubExporter spool — durability layer behind the in-memory ring.
//
// PR 5 of v0.16.1. Two implementations sit behind a single `spool`
// interface:
//
//   1. fileSpool — SQLite WAL on disk. Used on VMs. Persists batches
//      that haven't been acked by the hub across process restarts.
//   2. memorySpool — bounded RAM ring with eviction. Used on
//      serverless (Cloud Run / Lambda) where disk is ephemeral and
//      cold-start churn makes persistence pointless.
//
// Auto-detect: SKY_CONSOLE_SPOOL_MODE=auto (default) picks file on
// VMs, memory on serverless via IsServerless(). Explicit override
// honoured.
//
// Hot path: NEVER touches the spool. Submit writes to the in-memory
// channel only. The drainer goroutine is responsible for:
//
//   1. Pulling a batch off the channel.
//   2. Calling spool.Persist(batch) BEFORE attempting OTLP push
//      (async write — the in-memory channel-pull is the only "ack"
//      we owe the hot path).
//   3. Calling pushBatch.
//   4. On push success, calling spool.Ack(batchID) so the row drops.
//   5. On push failure, leaving the spool row for retry on the next
//      cycle.
//
// On Start(), the drainer first drains any unacked rows the spool
// holds from a previous process. This is the crash-resilience
// mechanism — if we exited without successful push, the next boot
// replays.
//
// Reliability invariants:
//   - spool writes are best-effort. A spool failure NEVER blocks the
//     drainer or the hot path. Failed writes count under
//     sky_telemetry_spool_write_failures_total and fall through to
//     the pure-RAM ring.
//   - retention sweep + circular truncation run on a separate
//     goroutine every 5 min so the drainer hot loop stays clean.
//   - file-mode spool is single-writer (one HubExporter per process)
//     under WAL mode; the SQLite handle is held for the process
//     lifetime.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"sky-app/rt/telemetry"

	_ "modernc.org/sqlite"
)

// SpoolMode is the resolved mode after auto-detect + override.
type SpoolMode int

const (
	// SpoolFile — SQLite-backed disk persistence (VM default).
	SpoolFile SpoolMode = iota
	// SpoolMemory — RAM-only bounded ring (serverless default).
	SpoolMemory
	// SpoolNone — disabled entirely (testing / synchronous mode).
	SpoolNone
)

func (m SpoolMode) String() string {
	switch m {
	case SpoolFile:
		return "file"
	case SpoolMemory:
		return "memory"
	case SpoolNone:
		return "none"
	}
	return "unknown"
}

// spool is the internal interface implemented by fileSpool and
// memorySpool. HubExporter calls Persist before push, Ack after push
// success, Drain on boot.
type spool interface {
	// Persist writes a batch into the spool and returns a token that
	// uniquely identifies this batch for later Ack. Returns an error
	// if the write failed; caller treats this as best-effort (the
	// in-memory ring still has the data until pushBatch attempt
	// resolves).
	Persist(ctx context.Context, batch []telemetryItem) (token int64, err error)
	// Ack marks a previously-persisted batch as successfully pushed
	// to the hub, allowing the spool to free the row.
	Ack(ctx context.Context, token int64) error
	// Drain returns up to limit pending (unacked) batches in
	// insertion order. Used on Start() to replay anything left
	// from the previous process. Each returned item carries the
	// token so the drainer can Ack it after the push succeeds.
	Drain(ctx context.Context, limit int) ([]spoolEntry, error)
	// Size returns the current spool size in bytes. Used by the
	// retention + truncation sweep.
	Size(ctx context.Context) (int64, error)
	// PruneOlderThan deletes rows older than the cutoff. Returns
	// the number of rows deleted.
	PruneOlderThan(ctx context.Context, cutoff time.Time) (int64, error)
	// TruncateToSize deletes oldest rows until the spool is below
	// maxBytes. Returns the number of rows deleted.
	TruncateToSize(ctx context.Context, maxBytes int64) (int64, error)
	// Mode reports which backend this spool is.
	Mode() SpoolMode
	// Close releases any resources (file handle, etc).
	Close() error
}

// spoolEntry is one batch pulled from the spool on Drain.
type spoolEntry struct {
	token int64
	items []telemetryItem
}

// spoolConfig is the resolved spool configuration.
type spoolConfig struct {
	mode       SpoolMode
	path       string        // file mode only
	retention  time.Duration // default 7d (168h)
	maxBytes   int64         // default 100 MB
	sweepEvery time.Duration // default 5m
}

// ─── env + config resolution ─────────────────────────────────────

const (
	envSpoolMode      = "SKY_CONSOLE_SPOOL_MODE"
	envSpoolPath      = "SKY_CONSOLE_SPOOL_PATH"
	envSpoolRetention = "SKY_CONSOLE_SPOOL_RETENTION"
	envSpoolMaxBytes  = "SKY_CONSOLE_SPOOL_MAX_BYTES"
)

// resolveSpoolMode picks the right backend per SKY_CONSOLE_SPOOL_MODE
// (auto by default → IsServerless decides). Explicit override always
// wins.
func resolveSpoolMode() SpoolMode {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(envSpoolMode))) {
	case "file":
		return SpoolFile
	case "memory", "mem":
		return SpoolMemory
	case "none", "off", "disabled":
		return SpoolNone
	case "", "auto":
		// fallthrough
	default:
		// Unrecognised value — auto-detect path.
	}
	if IsServerless() {
		return SpoolMemory
	}
	return SpoolFile
}

// defaultSpoolPath returns the platform-specific default spool path
// when SKY_CONSOLE_SPOOL_PATH is unset:
//
//	linux   /var/lib/sky/console-spool.db
//	darwin  ~/Library/Application Support/sky/console-spool.db
//	other   <cwd>/.sky-console-spool.db
//
// Caller is responsible for creating parent dirs.
func defaultSpoolPath() string {
	if explicit := strings.TrimSpace(os.Getenv(envSpoolPath)); explicit != "" {
		return explicit
	}
	switch runtime.GOOS {
	case "linux":
		return "/var/lib/sky/console-spool.db"
	case "darwin":
		home, err := os.UserHomeDir()
		if err == nil && home != "" {
			return filepath.Join(home, "Library", "Application Support",
				"sky", "console-spool.db")
		}
	}
	// Fallback: current working directory. Better than crashing on a
	// platform we don't recognise.
	return ".sky-console-spool.db"
}

// resolveSpoolConfig reads every spool-related env var and returns
// the merged configuration. Caller decides whether to actually open
// the backend (see openSpool).
func resolveSpoolConfig() spoolConfig {
	cfg := spoolConfig{
		mode:       resolveSpoolMode(),
		path:       defaultSpoolPath(),
		retention:  168 * time.Hour, // 7 days
		maxBytes:   100 * 1024 * 1024,
		sweepEvery: 5 * time.Minute,
	}
	if r := strings.TrimSpace(os.Getenv(envSpoolRetention)); r != "" {
		if d, err := time.ParseDuration(r); err == nil && d > 0 {
			cfg.retention = d
		}
	}
	if mb := strings.TrimSpace(os.Getenv(envSpoolMaxBytes)); mb != "" {
		if n, err := strconv.ParseInt(mb, 10, 64); err == nil && n > 0 {
			cfg.maxBytes = n
		}
	}
	return cfg
}

// openSpool constructs the backend for cfg.mode. Returns nil + nil
// for SpoolNone (caller treats nil as "no spool, hot-path only").
// Returns the configured backend plus any error from the backend's
// open / migrate. A non-nil error means the caller should fall back
// to SpoolNone (memory ring only) — the exporter remains functional
// even when the spool is unavailable.
func openSpool(cfg spoolConfig) (spool, error) {
	switch cfg.mode {
	case SpoolNone:
		return nil, nil
	case SpoolMemory:
		return newMemorySpool(cfg)
	case SpoolFile:
		return newFileSpool(cfg)
	}
	return nil, fmt.Errorf("unknown spool mode %v", cfg.mode)
}

// ─── memorySpool ─────────────────────────────────────────────────

// memorySpool keeps a bounded slice of unacked batches in RAM. Used
// on serverless (Cloud Run / Lambda) where disk persistence makes no
// sense (ephemeral filesystems, cold-start churn).
//
// Semantics:
//   - Persist appends an entry, generating a monotonically-rising
//     token. If size > maxBytes after the append, the oldest entry
//     gets evicted (FIFO truncation; sky_telemetry_spool_truncated_total
//     incremented).
//   - Ack removes the entry by token.
//   - Drain returns the oldest N pending entries.
//   - PruneOlderThan deletes entries older than cutoff.
//
// All operations protected by a single mutex — contention is low
// because the drainer is the only Persist/Ack caller, and the
// retention sweep runs every 5 min.
type memorySpool struct {
	mu        sync.Mutex
	nextToken int64
	entries   []*memorySpoolEntry
	bytes     int64
	maxBytes  int64
}

type memorySpoolEntry struct {
	token int64
	items []telemetryItem
	bytes int64
	at    time.Time
}

func newMemorySpool(cfg spoolConfig) (*memorySpool, error) {
	// Per SERVERLESS.md: memory-mode cap is 5 MB, NOT the file cap.
	// If the caller didn't override the env, the cfg has the file
	// default (100 MB). Override here.
	maxBytes := int64(5 * 1024 * 1024)
	// If the user explicitly set SKY_CONSOLE_SPOOL_MAX_BYTES we
	// honour their value (cfg.maxBytes was overridden in
	// resolveSpoolConfig).
	if os.Getenv(envSpoolMaxBytes) != "" {
		maxBytes = cfg.maxBytes
	}
	return &memorySpool{
		maxBytes: maxBytes,
	}, nil
}

func (s *memorySpool) Persist(ctx context.Context, batch []telemetryItem) (int64, error) {
	if len(batch) == 0 {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextToken++
	tok := s.nextToken
	var b int64
	itemsCopy := make([]telemetryItem, len(batch))
	for i, it := range batch {
		pcopy := make([]byte, len(it.payload))
		copy(pcopy, it.payload)
		itemsCopy[i] = telemetryItem{
			kind: it.kind, severity: it.severity, payload: pcopy,
		}
		b += int64(len(it.payload))
	}
	s.entries = append(s.entries, &memorySpoolEntry{
		token: tok, items: itemsCopy, bytes: b, at: time.Now(),
	})
	s.bytes += b
	// Circular truncation — evict oldest until under cap.
	for s.bytes > s.maxBytes && len(s.entries) > 0 {
		first := s.entries[0]
		s.entries = s.entries[1:]
		s.bytes -= first.bytes
		telemetry.Default().Add("sky_telemetry_spool_truncated_total",
			map[string]string{"mode": "memory"}, 1)
	}
	return tok, nil
}

func (s *memorySpool) Ack(ctx context.Context, token int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, e := range s.entries {
		if e.token == token {
			s.entries = append(s.entries[:i], s.entries[i+1:]...)
			s.bytes -= e.bytes
			return nil
		}
	}
	return nil // already acked / pruned / never-spooled
}

func (s *memorySpool) Drain(ctx context.Context, limit int) ([]spoolEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 || limit > len(s.entries) {
		limit = len(s.entries)
	}
	out := make([]spoolEntry, 0, limit)
	for _, e := range s.entries[:limit] {
		itemsCopy := make([]telemetryItem, len(e.items))
		for i, it := range e.items {
			pcopy := make([]byte, len(it.payload))
			copy(pcopy, it.payload)
			itemsCopy[i] = telemetryItem{
				kind: it.kind, severity: it.severity, payload: pcopy,
			}
		}
		out = append(out, spoolEntry{token: e.token, items: itemsCopy})
	}
	return out, nil
}

func (s *memorySpool) Size(ctx context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bytes, nil
}

func (s *memorySpool) PruneOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var deleted int64
	keep := s.entries[:0]
	for _, e := range s.entries {
		if e.at.Before(cutoff) {
			s.bytes -= e.bytes
			deleted++
			continue
		}
		keep = append(keep, e)
	}
	s.entries = keep
	return deleted, nil
}

func (s *memorySpool) TruncateToSize(ctx context.Context, maxBytes int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var deleted int64
	for s.bytes > maxBytes && len(s.entries) > 0 {
		first := s.entries[0]
		s.entries = s.entries[1:]
		s.bytes -= first.bytes
		deleted++
	}
	return deleted, nil
}

func (s *memorySpool) Mode() SpoolMode { return SpoolMemory }
func (s *memorySpool) Close() error    { return nil }

// ─── fileSpool ───────────────────────────────────────────────────

// fileSpool persists unacked batches to a SQLite WAL file. One row
// per (token, telemetry item) — the token groups items into batches
// for atomic ack.
//
// Schema:
//
//	CREATE TABLE spool_batches (
//	    token       INTEGER PRIMARY KEY AUTOINCREMENT,
//	    created_at  INTEGER NOT NULL,      -- unix nanos
//	    bytes       INTEGER NOT NULL       -- approx serialised size
//	);
//	CREATE TABLE spool_items (
//	    id          INTEGER PRIMARY KEY AUTOINCREMENT,
//	    token       INTEGER NOT NULL REFERENCES spool_batches(token),
//	    kind        INTEGER NOT NULL,
//	    severity    INTEGER NOT NULL,
//	    payload     BLOB NOT NULL
//	);
//	CREATE INDEX idx_spool_items_token ON spool_items(token);
//	CREATE INDEX idx_spool_batches_at  ON spool_batches(created_at);
//
// PRAGMAs at open:
//
//	journal_mode = WAL          (concurrent reads, lower fsync cost)
//	synchronous  = NORMAL       (one-fsync-per-checkpoint; safe within WAL)
//	busy_timeout = 2000         (tolerate transient contention)
//	foreign_keys = ON           (referential integrity for items↔batches)
type fileSpool struct {
	db   *sql.DB
	path string
}

const fileSpoolSchema = `
CREATE TABLE IF NOT EXISTS spool_batches (
    token       INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at  INTEGER NOT NULL,
    bytes       INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS spool_items (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    token       INTEGER NOT NULL,
    kind        INTEGER NOT NULL,
    severity    INTEGER NOT NULL,
    payload     BLOB NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_spool_items_token
    ON spool_items(token);

CREATE INDEX IF NOT EXISTS idx_spool_batches_at
    ON spool_batches(created_at);
`

func newFileSpool(cfg spoolConfig) (*fileSpool, error) {
	if cfg.path == "" {
		return nil, errors.New("fileSpool: empty path")
	}
	if dir := filepath.Dir(cfg.path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("fileSpool: mkdir %s: %w", dir, err)
		}
	}
	db, err := sql.Open("sqlite", cfg.path)
	if err != nil {
		return nil, fmt.Errorf("fileSpool: open %s: %w", cfg.path, err)
	}
	// Single-writer use; SQLite is happiest with a small pool.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	for _, pragma := range []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA synchronous=NORMAL`,
		`PRAGMA busy_timeout=2000`,
		`PRAGMA foreign_keys=ON`,
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("fileSpool: %s: %w", pragma, err)
		}
	}
	if _, err := db.Exec(fileSpoolSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("fileSpool: migrate: %w", err)
	}
	return &fileSpool{db: db, path: cfg.path}, nil
}

func (s *fileSpool) Persist(ctx context.Context, batch []telemetryItem) (int64, error) {
	if len(batch) == 0 {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var totalBytes int64
	for _, it := range batch {
		totalBytes += int64(len(it.payload))
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO spool_batches(created_at, bytes) VALUES (?, ?)`,
		time.Now().UnixNano(), totalBytes)
	if err != nil {
		return 0, err
	}
	token, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO spool_items(token, kind, severity, payload) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	for _, it := range batch {
		if _, err := stmt.ExecContext(ctx, token, int(it.kind), int(it.severity), it.payload); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return token, nil
}

func (s *fileSpool) Ack(ctx context.Context, token int64) error {
	if token == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM spool_items WHERE token = ?`, token); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM spool_batches WHERE token = ?`, token); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *fileSpool) Drain(ctx context.Context, limit int) ([]spoolEntry, error) {
	if limit <= 0 {
		limit = 1024
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT token FROM spool_batches ORDER BY token ASC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tokens []int64
	for rows.Next() {
		var tok int64
		if err := rows.Scan(&tok); err != nil {
			return nil, err
		}
		tokens = append(tokens, tok)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]spoolEntry, 0, len(tokens))
	for _, tok := range tokens {
		itemRows, err := s.db.QueryContext(ctx,
			`SELECT kind, severity, payload FROM spool_items WHERE token = ? ORDER BY id ASC`, tok)
		if err != nil {
			return nil, err
		}
		var items []telemetryItem
		for itemRows.Next() {
			var kind, sev int
			var payload []byte
			if err := itemRows.Scan(&kind, &sev, &payload); err != nil {
				itemRows.Close()
				return nil, err
			}
			items = append(items, telemetryItem{
				kind:     TelemetryKind(kind),
				severity: Severity(sev),
				payload:  payload,
			})
		}
		itemRows.Close()
		if err := itemRows.Err(); err != nil {
			return nil, err
		}
		out = append(out, spoolEntry{token: tok, items: items})
	}
	return out, nil
}

func (s *fileSpool) Size(ctx context.Context) (int64, error) {
	var total sql.NullInt64
	row := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(bytes), 0) FROM spool_batches`)
	if err := row.Scan(&total); err != nil {
		return 0, err
	}
	return total.Int64, nil
}

func (s *fileSpool) PruneOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM spool_items WHERE token IN (
		    SELECT token FROM spool_batches WHERE created_at < ?
		 )`, cutoff.UnixNano()); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx,
		`DELETE FROM spool_batches WHERE created_at < ?`, cutoff.UnixNano())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

func (s *fileSpool) TruncateToSize(ctx context.Context, maxBytes int64) (int64, error) {
	var deleted int64
	for {
		cur, err := s.Size(ctx)
		if err != nil {
			return deleted, err
		}
		if cur <= maxBytes {
			return deleted, nil
		}
		var tok sql.NullInt64
		row := s.db.QueryRowContext(ctx,
			`SELECT token FROM spool_batches ORDER BY token ASC LIMIT 1`)
		if err := row.Scan(&tok); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return deleted, nil
			}
			return deleted, err
		}
		if !tok.Valid {
			return deleted, nil
		}
		if err := s.Ack(ctx, tok.Int64); err != nil {
			return deleted, err
		}
		deleted++
		telemetry.Default().Add("sky_telemetry_spool_truncated_total",
			map[string]string{"mode": "file"}, 1)
	}
}

func (s *fileSpool) Mode() SpoolMode { return SpoolFile }

func (s *fileSpool) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// ─── HubExporter spool integration ───────────────────────────────

// attachSpool wires the supplied spool to e. Idempotent; replaces
// any previously-attached spool. Set spool=nil to detach.
func (e *HubExporter) attachSpool(s spool) {
	if e == nil {
		return
	}
	e.spoolMu.Lock()
	defer e.spoolMu.Unlock()
	if e.spool != nil && e.spool != s {
		_ = e.spool.Close()
	}
	e.spool = s
	if s != nil {
		telemetry.Default().SetGauge("sky_telemetry_spool_mode",
			map[string]string{"mode": s.Mode().String()}, 1)
	}
}

// activeSpool returns the currently-attached spool or nil. Cheap
// read-locked accessor used by the drainer.
func (e *HubExporter) activeSpool() spool {
	if e == nil {
		return nil
	}
	e.spoolMu.RLock()
	defer e.spoolMu.RUnlock()
	return e.spool
}

// spoolRetentionSweep runs PruneOlderThan + TruncateToSize on a tick.
// Lives in its own goroutine so the drainer hot-loop stays clean.
// Started by Start(); exits on stopCh.
func (e *HubExporter) spoolRetentionSweep(ctx context.Context, cfg spoolConfig) {
	tick := time.NewTicker(cfg.sweepEvery)
	defer tick.Stop()
	for {
		select {
		case <-e.stopCh:
			return
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		sp := e.activeSpool()
		if sp == nil {
			continue
		}
		sweepCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		cutoff := time.Now().Add(-cfg.retention)
		if deleted, err := sp.PruneOlderThan(sweepCtx, cutoff); err == nil && deleted > 0 {
			telemetry.Default().Add("sky_telemetry_spool_pruned_total",
				map[string]string{"reason": "retention"}, float64(deleted))
		}
		if deleted, err := sp.TruncateToSize(sweepCtx, cfg.maxBytes); err == nil && deleted > 0 {
			telemetry.Default().Add("sky_telemetry_spool_pruned_total",
				map[string]string{"reason": "size-cap"}, float64(deleted))
		}
		if size, err := sp.Size(sweepCtx); err == nil {
			telemetry.Default().SetGauge("sky_telemetry_spool_size_bytes",
				map[string]string{"mode": sp.Mode().String()}, float64(size))
		}
		cancel()
	}
}

// replaySpoolOnBoot — at the start of the drainer, pull any batches
// the previous process left unacked and push them through. Runs
// once per Start; the drainer continues with normal channel
// processing afterwards.
//
// We DRAIN-AND-PUSH rather than re-enqueue to avoid bouncing through
// the in-memory ring (which has its own bounded capacity). Each
// replayed batch goes through the same pushBatch path as a fresh
// batch — circuit breaker + retry semantics identical.
func (e *HubExporter) replaySpoolOnBoot(ctx context.Context) {
	sp := e.activeSpool()
	if sp == nil {
		return
	}
	entries, err := sp.Drain(ctx, 1024)
	if err != nil {
		return
	}
	if len(entries) == 0 {
		return
	}
	var totalItems int
	for _, ent := range entries {
		totalItems += len(ent.items)
	}
	fmt.Fprintf(os.Stderr,
		"[sky.hub-exporter] replaying %d spooled events from previous process\n",
		totalItems)
	for _, ent := range entries {
		// Push the batch; on success the entry is acked. pushBatch
		// returns true iff every kind's POST succeeded.
		if e.pushBatch(ctx, ent.items) {
			if err := sp.Ack(ctx, ent.token); err == nil {
				telemetry.Default().Add("sky_telemetry_spool_replayed_total",
					map[string]string{"result": "ok"}, 1)
			}
		} else {
			telemetry.Default().Add("sky_telemetry_spool_replayed_total",
				map[string]string{"result": "fail"}, 1)
		}
	}
}

// spoolPersistAttempt — called by the drainer after pulling a batch
// off the channel + BEFORE pushBatch. Best-effort: a failure here
// counts a spool_write_failures_total but does NOT abort the push.
// Returns the token (0 if nothing was spooled, e.g. backend nil).
func (e *HubExporter) spoolPersistAttempt(ctx context.Context, batch []telemetryItem) int64 {
	sp := e.activeSpool()
	if sp == nil || len(batch) == 0 {
		return 0
	}
	persistCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	token, err := sp.Persist(persistCtx, batch)
	if err != nil {
		telemetry.Default().Add("sky_telemetry_spool_write_failures_total",
			map[string]string{"mode": sp.Mode().String()}, 1)
		atomic.AddInt64(&e.spoolWriteFails, 1)
		return 0
	}
	return token
}

// spoolAckAttempt — called by the drainer after pushBatch succeeds
// for a token previously returned by spoolPersistAttempt. Best-
// effort; a failure here means the row will be retried on the next
// boot (idempotent — the hub already has the data, the next push
// becomes a duplicate which the hub deduplicates by signal id).
func (e *HubExporter) spoolAckAttempt(ctx context.Context, token int64) {
	if token == 0 {
		return
	}
	sp := e.activeSpool()
	if sp == nil {
		return
	}
	ackCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_ = sp.Ack(ackCtx, token)
}
