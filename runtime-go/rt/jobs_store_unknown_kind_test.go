package rt

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// Silent-degrade regression for the JOBS store — the same defect class already
// closed for the SESSION store in live_store_unknown_kind_test.go, never applied
// here because nothing in the repo imported Std.Jobs and so nothing ever ran
// this code path.
//
// `chooseJobsStore` degraded to an in-process memory queue on FOUR paths:
//
//	 1. an unrecognised store kind (a typo: "postgress" / "psql", or a kind the
//	    docs list but this switch has no branch for)
//	 2. `store = "sqlite"` whose file cannot be opened
//	 3. `store = "postgres"` with NO url configured
//	 4. `store = "postgres"` whose url cannot be connected
//
// Every one of them wrote a line to stderr and kept serving. In production that
// means enqueued jobs live only in the RAM of one replica: they are lost on
// every restart and never shared across instances, while the app reports
// healthy. An operator who configured a durable queue gets a volatile one and
// is told only by a line in the log.
//
// This is the exact reasoning of the session-store fix (#8) — fail loud in
// production, warn + fall back in dev — applied to the surface that shipped
// without it.

// An explicitly-configured but unrecognised jobs store kind must not silently
// become an in-memory queue in production.
func TestChooseJobsStore_UnknownKind_FailsLoudInProd(t *testing.T) {
	t.Setenv("ENV", "production")
	t.Setenv("SKY_JOBS_STORE_PATH", "")

	oldFatal := jobsStoreFatalf
	var fatalMsg string
	jobsStoreFatalf = func(format string, args ...any) { fatalMsg = fmt.Sprintf(format, args...) }
	defer func() { jobsStoreFatalf = oldFatal }()

	for _, kind := range []string{"postgress", "psql", "redis", "firestore"} {
		fatalMsg = ""
		t.Setenv("SKY_JOBS_STORE", kind)
		_ = chooseJobsStore()
		if fatalMsg == "" {
			t.Fatalf("chooseJobsStore() with SKY_JOBS_STORE=%q in production must FAIL LOUD, "+
				"but jobsStoreFatalf did not fire (a silent in-memory queue loses every "+
				"enqueued job on restart and never shares across replicas)", kind)
		}
		if !strings.Contains(fatalMsg, kind) {
			t.Errorf("the fatal message must name the offending kind %q; got: %s", kind, fatalMsg)
		}
	}
}

// `store = "postgres"` with no URL is a CONFIG error, not a connect failure —
// the operator asked for a durable shared queue and named no server. Production
// must refuse rather than hand back RAM.
func TestChooseJobsStore_PostgresWithoutURL_FailsLoudInProd(t *testing.T) {
	t.Setenv("ENV", "production")
	t.Setenv("SKY_JOBS_STORE", "postgres")
	t.Setenv("SKY_JOBS_STORE_PATH", "")
	t.Setenv("DATABASE_URL", "")

	oldFatal := jobsStoreFatalf
	var fatalMsg string
	jobsStoreFatalf = func(format string, args ...any) { fatalMsg = fmt.Sprintf(format, args...) }
	defer func() { jobsStoreFatalf = oldFatal }()

	_ = chooseJobsStore()
	if fatalMsg == "" {
		t.Fatal("chooseJobsStore() with store=postgres and NO url in production must FAIL " +
			"LOUD — falling back to memory gives the operator a volatile single-replica " +
			"queue while reporting healthy")
	}
}

// An unreachable Postgres is a connect failure, and in production it must also
// refuse: degrading to memory converts a durability guarantee into RAM without
// the operator ever asking for it.
func TestChooseJobsStore_PostgresUnreachable_FailsLoudInProd(t *testing.T) {
	t.Setenv("ENV", "production")
	t.Setenv("SKY_JOBS_STORE", "postgres")
	// Port 1 is never a Postgres.
	t.Setenv("SKY_JOBS_STORE_PATH", "postgres://skytest@127.0.0.1:1/nope?sslmode=disable")

	oldFatal := jobsStoreFatalf
	var fatalMsg string
	jobsStoreFatalf = func(format string, args ...any) { fatalMsg = fmt.Sprintf(format, args...) }
	defer func() { jobsStoreFatalf = oldFatal }()

	_ = chooseJobsStore()
	if fatalMsg == "" {
		t.Fatal("chooseJobsStore() with an UNREACHABLE postgres in production must FAIL LOUD")
	}
}

// A `sqlite` store whose file cannot be opened must refuse in production for the
// same reason. The path below is inside a file, so it can never be a directory.
func TestChooseJobsStore_SqliteUnopenable_FailsLoudInProd(t *testing.T) {
	bad := t.TempDir() + "/not-a-dir"
	if err := os.WriteFile(bad, []byte("i am a file, not a directory"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	t.Setenv("ENV", "production")
	t.Setenv("SKY_JOBS_STORE", "sqlite")
	t.Setenv("SKY_JOBS_STORE_PATH", bad+"/jobs.db")

	oldFatal := jobsStoreFatalf
	var fatalMsg string
	jobsStoreFatalf = func(format string, args ...any) { fatalMsg = fmt.Sprintf(format, args...) }
	defer func() { jobsStoreFatalf = oldFatal }()

	_ = chooseJobsStore()
	if fatalMsg == "" {
		t.Fatal("chooseJobsStore() with an UNOPENABLE sqlite path in production must FAIL LOUD")
	}
}

// In DEV the same misconfiguration warns and falls back, so local iteration is
// never blocked. The hard failure is production-only — identical to the session
// store's contract.
func TestChooseJobsStore_UnknownKind_WarnsAndMemoryInDev(t *testing.T) {
	t.Setenv("ENV", "development")
	t.Setenv("SKY_JOBS_STORE", "firestore")
	t.Setenv("SKY_JOBS_STORE_PATH", "")

	oldFatal := jobsStoreFatalf
	fataled := false
	jobsStoreFatalf = func(string, ...any) { fataled = true }
	defer func() { jobsStoreFatalf = oldFatal }()

	s := chooseJobsStore()
	if fataled {
		t.Fatal("an unknown jobs store kind in DEV must NOT fatal — warn + memory fallback")
	}
	if got := fmt.Sprintf("%T", s); got != "*jobs.memoryStore" {
		t.Fatalf("dev fallback should be the memory store, got %s", got)
	}
}

// The intended memory paths — unset and an explicit "memory" — must NEVER fatal,
// even in production. `SKY_JOBS_STORE=memory` is a deliberate opt-in to a
// volatile queue and is allowed to stay exactly that.
func TestChooseJobsStore_MemoryAndEmpty_NeverFatal(t *testing.T) {
	t.Setenv("ENV", "production")
	t.Setenv("SKY_JOBS_STORE_PATH", "")

	oldFatal := jobsStoreFatalf
	fataled := false
	jobsStoreFatalf = func(string, ...any) { fataled = true }
	defer func() { jobsStoreFatalf = oldFatal }()

	for _, kind := range []string{"", "memory"} {
		t.Setenv("SKY_JOBS_STORE", kind)
		s := chooseJobsStore()
		if fataled {
			t.Fatalf("chooseJobsStore() with SKY_JOBS_STORE=%q must never fatal "+
				"(the deliberate in-memory opt-in)", kind)
		}
		if got := fmt.Sprintf("%T", s); got != "*jobs.memoryStore" {
			t.Fatalf("SKY_JOBS_STORE=%q should be the memory store, got %s", kind, got)
		}
	}
}
