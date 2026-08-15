//go:build integration
// +build integration

package rt

import (
	"os"
	"reflect"
	"testing"
	"time"
)

// Real-Postgres session-store integration tests. The per-push unit suite only
// covers memory/sqlite round-trip and the postgres FAIL-LOUD branch — the
// happy-path postgresStore (Get/Set/Delete/TTL against a live engine) was never
// tested, yet Postgres is the store production Sky.Live shops actually run on.
//
// Gated on SKY_TEST_POSTGRES_DSN so the default offline `go test` stays green;
// run with e.g.
//
//	SKY_TEST_POSTGRES_DSN='postgres://sky:sky@localhost:5432/sky?sslmode=disable' \
//	  go test -tags integration ./rt/ -run Postgres
//
// CI supplies the DSN via a `postgres:16` service container.

func requirePostgresDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("SKY_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("SKY_TEST_POSTGRES_DSN unset — skipping real-Postgres integration test")
	}
	return dsn
}

// The CROSS-INSTANCE round-trip is the meaningful test: postgresStore.Get hits
// an in-process memCache first, so writing and reading through the SAME instance
// never exercises the DB decode path. A second instance has an empty cache, so
// its Get must load + decode from Postgres — exactly what a restarted process or
// a second replica does. A serialization/DDL/decode regression here ships green
// under the unit tests.
func TestPostgresStore_CrossInstanceRoundTrip(t *testing.T) {
	dsn := requirePostgresDSN(t)
	ttl := 30 * time.Minute
	sid := "sky-test-crossinstance"

	// Model carrying a nested concrete type in an `any` field — the L10a
	// persistence class (a value that only ever lives behind interface{}).
	RegisterSkyGobTypes([]any{gobRegInner{}})
	model := map[string]any{
		"count": 42,
		"name":  "darragh",
		"inner": gobRegInner{Label: "hi"},
	}

	writer, err := newPostgresStore(dsn, ttl, 0)
	if err != nil {
		t.Fatalf("newPostgresStore(writer): %v", err)
	}
	defer writer.Close()
	writer.Delete(sid) // idempotent start
	writer.Set(sid, buildSess(model))

	reader, err := newPostgresStore(dsn, ttl, 0)
	if err != nil {
		t.Fatalf("newPostgresStore(reader): %v", err)
	}
	defer reader.Close()

	got, ok := reader.Get(sid)
	if !ok {
		t.Fatal("reader instance could not load the session from Postgres — cross-instance/restart persistence is broken")
	}
	if !reflect.DeepEqual(got.model, model) {
		t.Fatalf("model did not round-trip through Postgres:\n want %#v\n got  %#v", model, got.model)
	}

	// Delete must reach Postgres, not just the writer's cache: a THIRD fresh
	// instance must not find the session.
	writer.Delete(sid)
	reaper, err := newPostgresStore(dsn, ttl, 0)
	if err != nil {
		t.Fatalf("newPostgresStore(reaper): %v", err)
	}
	defer reaper.Close()
	if _, ok := reaper.Get(sid); ok {
		t.Fatal("session still present after Delete — delete did not propagate to Postgres")
	}
}

// Ping reports health for /_sky/readyz: a connected store returns nil so the
// orchestrator keeps routing to a healthy replica.
func TestPostgresStore_PingHealthy(t *testing.T) {
	dsn := requirePostgresDSN(t)
	s, err := newPostgresStore(dsn, 30*time.Minute, 0)
	if err != nil {
		t.Fatalf("newPostgresStore: %v", err)
	}
	defer s.Close()
	if err := s.Ping(); err != nil {
		t.Fatalf("Ping() on a connected Postgres store should be nil, got: %v", err)
	}
}

// The offline gate (TestPostgresSessionPoolHasACeiling) asserts the ceiling on
// the pool `openPostgresSessionPool` hands back. This asserts it on the store a
// real engine actually produced, which is the half the offline one cannot see:
// a newPostgresStore rewritten to call `sql.Open` for itself would satisfy the
// offline gate and still ship an unlimited pool.
func TestPostgresStore_PoolHasACeiling(t *testing.T) {
	dsn := requirePostgresDSN(t)
	// The SHARED sizing, not the bare auxiliary one: the session store draws
	// on a pool it may share with analytics and telemetry, sized as its own
	// former pool plus their caps so that sharing costs the request path
	// nothing. See dbSharedAuxPoolSizeFor.
	want := dbSharedAuxPoolConfig().MaxOpenConns
	if want <= 0 {
		t.Fatalf("dbSharedAuxPoolConfig().MaxOpenConns is %d — this gate would prove nothing", want)
	}
	s, err := newPostgresStore(dsn, 30*time.Minute, 0)
	if err != nil {
		t.Fatalf("newPostgresStore: %v", err)
	}
	defer s.Close()
	if got := s.db.Stats().MaxOpenConnections; got != want {
		t.Errorf("session-store pool MaxOpenConnections = %d, want %d (0 means UNLIMITED, "+
			"and this is the hottest pool in a Sky.Live app)", got, want)
	}
}

// A read slides the TTL (L4): last_seen must advance on Get so an idle-but-active
// session isn't reaped. Verified across instances (the reader updates the row).
func TestPostgresStore_GetTouchesLastSeen(t *testing.T) {
	dsn := requirePostgresDSN(t)
	ttl := 30 * time.Minute
	sid := "sky-test-lastseen"

	writer, err := newPostgresStore(dsn, ttl, 0)
	if err != nil {
		t.Fatalf("newPostgresStore(writer): %v", err)
	}
	defer writer.Close()
	defer writer.Delete(sid)
	writer.Delete(sid)
	writer.Set(sid, buildSess(map[string]any{"k": "v"}))

	var before int64
	if err := writer.db.QueryRow(`SELECT last_seen FROM sky_sessions WHERE sid = $1`, sid).Scan(&before); err != nil {
		t.Fatalf("read initial last_seen: %v", err)
	}

	time.Sleep(1100 * time.Millisecond)

	reader, err := newPostgresStore(dsn, ttl, 0)
	if err != nil {
		t.Fatalf("newPostgresStore(reader): %v", err)
	}
	defer reader.Close()
	if _, ok := reader.Get(sid); !ok {
		t.Fatal("reader could not load session")
	}

	var after int64
	if err := writer.db.QueryRow(`SELECT last_seen FROM sky_sessions WHERE sid = $1`, sid).Scan(&after); err != nil {
		t.Fatalf("read updated last_seen: %v", err)
	}
	if after <= before {
		t.Fatalf("Get did not slide last_seen: before=%d after=%d (idle-active session would be wrongly reaped)", before, after)
	}
}
