package rt

// Unit gates for the embedded-PostgreSQL supervisor. The live-cluster gates
// (which need real PostgreSQL binaries) are in pg_embed_live_test.go.

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// fakeEnv builds an envFunc over a map, distinguishing unset from set-to-empty
// the same way os.LookupEnv does.
func fakeEnv(kv map[string]string) envFunc {
	return func(name string) (string, bool) {
		v, ok := kv[name]
		return v, ok
	}
}

// ---------------------------------------------------------------------------
// Shutdown ordering — the property with real consequences
// ---------------------------------------------------------------------------

// The sequence is observed from the LEAVES: a real accept-stopper, a real
// shutdown hook, and the supervisor's real stop seam. Nothing here asks the
// sequencer what it thinks it did.
func TestShutdownStopsPostgresAfterAcceptingStopsAndDrainCompletes(t *testing.T) {
	resetShutdownHooksForTesting()
	resetAcceptStoppersForTesting()
	t.Cleanup(func() {
		resetShutdownHooksForTesting()
		resetAcceptStoppersForTesting()
	})

	var mu sync.Mutex
	var seq []string
	note := func(s string) {
		mu.Lock()
		seq = append(seq, s)
		mu.Unlock()
	}

	RegisterAcceptStopper("listener", func() { note("stop-accepting") })
	RegisterShutdownHook("drain", func(context.Context) {
		// A drain that takes time is the whole point: an implementation that
		// merely *calls* the drain and moves on passes a zero-cost hook.
		time.Sleep(150 * time.Millisecond)
		note("drain")
	})

	s := &pgSupervisor{stopFn: func() error { note("stop-postgres"); return nil }}
	s.shutdown(5 * time.Second)

	mu.Lock()
	got := strings.Join(seq, " → ")
	mu.Unlock()
	const want = "stop-accepting → drain → stop-postgres"
	if got != want {
		t.Fatalf("shutdown ran in the wrong order\n  got:  %s\n  want: %s", got, want)
	}
}

// The app shapes install their own signal handlers and call RunShutdownHooks
// too. Whichever goroutine arrives second finds the chain already claimed and
// returns AT ONCE — with the drain still in flight. The supervisor must still
// not take the database away underneath it.
func TestShutdownWaitsForADrainStartedByTheAppsOwnHandler(t *testing.T) {
	resetShutdownHooksForTesting()
	resetAcceptStoppersForTesting()
	t.Cleanup(func() {
		resetShutdownHooksForTesting()
		resetAcceptStoppersForTesting()
	})

	var mu sync.Mutex
	var seq []string
	note := func(s string) {
		mu.Lock()
		seq = append(seq, s)
		mu.Unlock()
	}

	drainStarted := make(chan struct{})
	RegisterShutdownHook("slow-drain", func(context.Context) {
		close(drainStarted)
		time.Sleep(250 * time.Millisecond)
		note("drain")
	})

	// The app's own handler gets there first.
	go RunShutdownHooks(5 * time.Second)
	<-drainStarted

	s := &pgSupervisor{stopFn: func() error { note("stop-postgres"); return nil }}
	s.shutdown(5 * time.Second)

	mu.Lock()
	got := strings.Join(seq, " → ")
	mu.Unlock()
	if got != "drain → stop-postgres" {
		t.Fatalf("the database was stopped while the app was still draining\n  got: %s", got)
	}
}

// The two tests above call s.shutdown directly, which leaves the goroutine
// installSignalHandler starts — the one EVERY production SIGTERM goes through —
// covered by nothing. That is not a theoretical hole: a `s.stopPostgres()`
// inserted into the handler before it delegates makes stopOnce turn shutdown's
// third phase into a no-op, so the real order becomes
// `stop-postgres → stop-accepting → drain` while both tests above stay green.
// Every rollout would then take the database away with requests in flight.
//
// So this drives the REAL handler with a REAL signal: install it, raise
// SIGTERM at this process, and assert the same leaf sequence.
func TestTheSignalHandlerRunsThePhasesInOrder(t *testing.T) {
	resetShutdownHooksForTesting()
	resetAcceptStoppersForTesting()
	t.Cleanup(func() {
		resetShutdownHooksForTesting()
		resetAcceptStoppersForTesting()
	})

	var mu sync.Mutex
	var seq []string
	note := func(s string) {
		mu.Lock()
		seq = append(seq, s)
		mu.Unlock()
	}

	RegisterAcceptStopper("listener", func() { note("stop-accepting") })
	RegisterShutdownHook("drain", func(context.Context) {
		time.Sleep(150 * time.Millisecond)
		note("drain")
	})

	exited := make(chan int, 1)
	s := &pgSupervisor{
		stopFn: func() error { note("stop-postgres"); return nil },
		exitFn: func(code int) { exited <- code },
	}
	s.installSignalHandler()
	t.Cleanup(s.detachSignalHandler)

	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("cannot signal this process: %v", err)
	}
	select {
	case code := <-exited:
		if code != 0 {
			t.Errorf("the handler exited %d, want 0", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("SIGTERM did not reach the supervisor's handler")
	}

	mu.Lock()
	got := strings.Join(seq, " → ")
	mu.Unlock()
	const want = "stop-accepting → drain → stop-postgres"
	if got != want {
		t.Fatalf("the SIGTERM path ran in the wrong order\n  got:  %s\n  want: %s", got, want)
	}
}

func TestStopPostgresIsIdempotent(t *testing.T) {
	n := 0
	s := &pgSupervisor{stopFn: func() error { n++; return nil }}
	s.stopPostgres()
	s.stopPostgres()
	StopEmbeddedPostgres() // no supervisor registered — must not panic
	if n != 1 {
		t.Fatalf("stop ran %d times, want 1", n)
	}
}

// ---------------------------------------------------------------------------
// --embed with an explicit DSN
// ---------------------------------------------------------------------------

func TestEmbedWithAnExplicitDSNIsRefused(t *testing.T) {
	for _, name := range []string{skyEnvName("DB_PATH"), "DATABASE_URL"} {
		t.Run(name, func(t *testing.T) {
			env := fakeEnv(map[string]string{name: "postgres://ops@db.internal:5432/app"})
			err := embeddedDSNConflict(embeddedDSNSources(env))
			if err == nil {
				t.Fatalf("--embed alongside %s was accepted; it must be refused", name)
			}
			for _, want := range []string{name, "db.internal", "will not choose"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal does not mention %q:\n%s", want, err)
				}
			}
		})
	}
}

// `sky.toml`'s `[database] path` / `url` never reach the runtime as
// configuration: the compiler compiles them into a `rt.SetSkyDefault("DB_PATH",
// …)` in the generated prologue `init()`. So the conflict `sky run` reports for
// those two keys has to be reported here through the environment variable they
// become — otherwise a project with `[database] path` in its sky.toml would be
// refused by `sky run` and quietly accepted by the binary it produced.
func TestASkyTomlDatabasePathIsSeenAsAConflict(t *testing.T) {
	t.Setenv(skyEnvName("DB_PATH"), "") // snapshot, restored by t.Cleanup
	t.Setenv("DATABASE_URL", "")
	// SetSkyDefault is set-if-UNSET, so the variable has to be genuinely absent
	// for this to reproduce a real boot.
	if err := os.Unsetenv(skyEnvName("DB_PATH")); err != nil {
		t.Fatal(err)
	}
	// Exactly what the generated init() does with `[database] path = "app.db"`.
	SetSkyDefault("DB_PATH", "app.db")

	err := embeddedDSNConflict(embeddedDSNSources(osEnv))
	if err == nil {
		t.Fatal("a sky.toml [database] path was not seen as a conflict with --embed")
	}
	if !strings.Contains(err.Error(), "sky.toml") {
		t.Errorf("the refusal does not tell the reader where the value can come from:\n%s", err)
	}
}

func TestEmbedWithoutADSNIsAccepted(t *testing.T) {
	env := fakeEnv(map[string]string{"DATABASE_URL": "   "})
	if err := embeddedDSNConflict(embeddedDSNSources(env)); err != nil {
		t.Fatalf("a blank DATABASE_URL must not count as an explicit DSN: %v", err)
	}
}

func TestOnlyTheFirstConflictingSourceIsReported(t *testing.T) {
	env := fakeEnv(map[string]string{
		skyEnvName("DB_PATH"): "postgres://a/1",
		"DATABASE_URL":        "postgres://b/2",
	})
	err := embeddedDSNConflict(embeddedDSNSources(env))
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if strings.Contains(err.Error(), "postgres://b/2") {
		t.Errorf("reported more than one source; one mistake deserves one complaint:\n%s", err)
	}
}

// ---------------------------------------------------------------------------
// Request detection and data-directory resolution
// ---------------------------------------------------------------------------

func TestEmbedRequested(t *testing.T) {
	none := fakeEnv(nil)
	cases := []struct {
		name string
		args []string
		env  envFunc
		want bool
	}{
		{"flag", []string{"app", "--embed"}, none, true},
		{"absent", []string{"app", "serve"}, none, false},
		{"env", []string{"app"}, fakeEnv(map[string]string{"SKY_EMBED_POSTGRES": "true"}), true},
		{"env off", []string{"app"}, fakeEnv(map[string]string{"SKY_EMBED_POSTGRES": "0"}), false},
		{"after --", []string{"app", "--", "--embed"}, none, false},
		{"argv0 only", []string{"--embed"}, none, false},
	}
	for _, c := range cases {
		if got := embedRequested(c.args, c.env); got != c.want {
			t.Errorf("%s: embedRequested(%q) = %v, want %v", c.name, c.args, got, c.want)
		}
	}
}

func TestDataRootResolution(t *testing.T) {
	none := fakeEnv(nil)
	cases := []struct {
		name    string
		args    []string
		env     envFunc
		want    string
		wantErr string
	}{
		{name: "flag", args: []string{"app", "--data-dir", "/var/lib/app"}, env: none, want: "/var/lib/app"},
		{name: "flag=", args: []string{"app", "--data-dir=/var/lib/app"}, env: none, want: "/var/lib/app"},
		{name: "flag beats env", args: []string{"app", "--data-dir=/var/lib/app"},
			env: fakeEnv(map[string]string{"SKY_DATA_DIR": "/srv/other"}), want: "/var/lib/app"},
		{name: "env", args: []string{"app"},
			env: fakeEnv(map[string]string{"SKY_DATA_DIR": "/srv/data"}), want: "/srv/data"},
		{name: "default", args: []string{"app"}, env: none, want: filepath.Join("/work", ".skydata")},
		{name: "flag with no value", args: []string{"app", "--data-dir"}, env: none, wantErr: "needs a path"},
		{name: "empty env is not unset", args: []string{"app"},
			env: fakeEnv(map[string]string{"SKY_DATA_DIR": ""}), wantErr: "set but empty"},
	}
	for _, c := range cases {
		got, err := dataRootFrom(c.args, c.env, "/work")
		switch {
		case c.wantErr != "":
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("%s: want error containing %q, got (%q, %v)", c.name, c.wantErr, got, err)
			}
		case err != nil:
			t.Errorf("%s: unexpected error %v", c.name, err)
		case got != c.want:
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestATemporaryDataDirectoryIsRefused(t *testing.T) {
	env := fakeEnv(map[string]string{"TMPDIR": "/var/folders/zz/T"})
	refused := []string{
		"/tmp/app-data",
		"/private/var/folders/zz/T/x/pgdata",
		"/var/tmp/app",
		"/dev/shm/app",
		"/var/folders/zz/T/whatever",
	}
	for _, d := range refused {
		err := rejectTempDataDir(d, env)
		if err == nil {
			t.Errorf("%s was accepted as a data directory; the system may empty it", d)
			continue
		}
		if !strings.Contains(err.Error(), "--data-dir") {
			t.Errorf("%s: refusal gives the reader no way out:\n%s", d, err)
		}
	}
	for _, d := range []string{"/var/lib/app", "/srv/app/.skydata", "/home/me/proj/.skydata"} {
		if err := rejectTempDataDir(d, env); err != nil {
			t.Errorf("%s is durable and was refused: %v", d, err)
		}
	}
}

// A path that merely SHARES A PREFIX with a temp root is not in it.
func TestPrefixLookalikesAreNotTemporary(t *testing.T) {
	for _, d := range []string{"/tmpfiles/app", "/var/folders-of-mine/app"} {
		if err := rejectTempDataDir(d, fakeEnv(nil)); err != nil {
			t.Errorf("%s was refused on a prefix match: %v", d, err)
		}
	}
}

// ---------------------------------------------------------------------------
// Socket path derivation
// ---------------------------------------------------------------------------

// The budget is measured on the socket FILE. A directory-only check is short by
// the length of `.s.PGSQL.5432`, which is how this passes locally and fails on
// a host with a longer prefix.
func TestSocketBudgetIsMeasuredOnTheSocketFile(t *testing.T) {
	dir := "/run/user/1000/sky/0123456789abcdef"
	if got, want := socketPathLen(dir), len(dir)+1+len(pgSocketBasename); got != want {
		t.Fatalf("socketPathLen(%q) = %d, want %d", dir, got, want)
	}
	if socketPathLen(dir) <= len(dir) {
		t.Fatal("the socket file is not longer than its directory — the budget is being measured on the wrong thing")
	}
	// PostgreSQL also creates `<socket>.lock`, five bytes longer again; the
	// ceiling has to leave room for it under macOS's 103-byte limit.
	if maxSocketPath+len(".lock") >= 103 {
		t.Fatalf("maxSocketPath=%d leaves no room for the .lock file under macOS's 103-byte sun_path", maxSocketPath)
	}
}

func TestSocketDirFallsBackWhenXDGRuntimeDirIsTooLong(t *testing.T) {
	data := "/var/lib/an-application-with-a-long-name/pg"
	short := socketDirFor(data, "/run/user/1000", "/tmp")
	if !strings.HasPrefix(short, "/run/user/1000/sky/") {
		t.Fatalf("a short XDG_RUNTIME_DIR should be used: %s", short)
	}
	long := "/run/user/1000/" + strings.Repeat("deep/", 20)
	fell := socketDirFor(data, long, "/tmp")
	if !strings.HasPrefix(fell, "/tmp/sky-") {
		t.Fatalf("a long XDG_RUNTIME_DIR must degrade to /tmp, got %s", fell)
	}
	if socketPathLen(fell) > maxSocketPath {
		t.Fatalf("the fallback is itself over budget: %d bytes", socketPathLen(fell))
	}
	// A relative XDG_RUNTIME_DIR is not a runtime dir.
	if got := socketDirFor(data, "relative/path", "/tmp"); !strings.HasPrefix(got, "/tmp/sky-") {
		t.Fatalf("a relative XDG_RUNTIME_DIR must be ignored, got %s", got)
	}
}

// However deep the data directory, the socket path is a constant length: that
// is the entire reason the hash exists.
func TestSocketPathLengthIsIndependentOfDataDirDepth(t *testing.T) {
	shallow := socketDirFor("/a/pg", "", "/tmp")
	deep := socketDirFor("/"+strings.Repeat("a-fairly-long-directory-name/", 12)+"pg", "", "/tmp")
	if socketPathLen(shallow) != socketPathLen(deep) {
		t.Fatalf("socket length varies with project depth: %d vs %d", socketPathLen(shallow), socketPathLen(deep))
	}
	if socketPathLen(deep) > maxSocketPath {
		t.Fatalf("derived socket path is over budget: %d", socketPathLen(deep))
	}
	if shallow == deep {
		t.Fatal("two different data directories hashed to one socket directory")
	}
}

// The hash is PERSISTED — it names a directory a running postmaster is bound
// to — and P2 derives the same name in Rust. These vectors are FNV-1a/128
// truncated to its top 64 bits, computed independently; if this test goes red,
// every running cluster has just been orphaned.
func TestPathHashMatchesFNV1a128(t *testing.T) {
	cases := map[string]string{
		// The first three are the published FNV-1a/128 vectors (the full
		// 128-bit digests of "a" and "foobar" are d228cb696f1a8caf… and
		// 343e1662793c64bf…); the last two were computed with an independent
		// big.Int implementation.
		"":                    "6c62272e07bb0142",
		"a":                   "d228cb696f1a8caf",
		"foobar":              "343e1662793c64bf",
		"/var/lib/myapp/pg":   "9e1b97fae9a594c0",
		"/home/me/project/pg": "7299db18eece2939",
	}
	for in, want := range cases {
		if got := pathHash(in); got != want {
			t.Errorf("pathHash(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestSocketDirShellSafety(t *testing.T) {
	// pg_ctl hands its command line to /bin/sh; these cannot be made safe by
	// quoting, so they are refused with the reason.
	for _, bad := range []string{
		"/run/user/1000/sky dir",
		"/run/user/$USER/sky",
		"/run/user/it's/sky",
		"/run/user/`id`/sky",
		"/run/user/a;b/sky",
		"/run/user/a|b/sky",
	} {
		if socketDirIsShellSafe(bad) {
			t.Errorf("%q was accepted as shell-safe", bad)
		}
		if err := prepareSocketDir(bad); err == nil {
			t.Errorf("prepareSocketDir(%q) accepted an unquotable path", bad)
		} else if !strings.Contains(err.Error(), "/bin/sh") {
			t.Errorf("refusal for %q does not say why:\n%v", bad, err)
		}
	}
	if !socketDirIsShellSafe("/tmp/sky-0123456789abcdef") {
		t.Error("sky's own derived path was rejected")
	}
}

func TestPrepareSocketDirRefusesAnOverlongPath(t *testing.T) {
	dir := "/tmp/" + strings.Repeat("x", maxSocketPath)
	err := prepareSocketDir(dir)
	if err == nil {
		t.Fatal("an over-budget socket path was accepted")
	}
	if !strings.Contains(err.Error(), "sockaddr_un") {
		t.Errorf("the refusal does not name the limit:\n%v", err)
	}
	if _, statErr := os.Stat(dir); statErr == nil {
		t.Error("the directory was created before the length was checked")
	}
}

// The socket directory's mode is not hygiene — it is the ONLY access control
// the embedded cluster has.
//
// initdb is run with `--auth-local=trust` (initCluster), and PostgreSQL's
// `unix_socket_permissions` defaults to 0777, so the socket file itself is
// world-writable by design. What stops a second local user from connecting is
// that they cannot TRAVERSE the directory the socket sits in. At 0755 the
// directory is world-traversable and `psql -h <dir> -d postgres` connects as a
// SUPERUSER — no password, no prompt, full read/write over every table in the
// app's database.
//
// Both the mode on MkdirAll and the Chmod that follows it are asserted,
// because they close different halves: MkdirAll only sets the mode when it
// CREATES the directory, so an already-present 0777 directory — a re-boot into
// a socket path a previous run or another tool left behind — is tightened only
// by the Chmod.
func TestPrepareSocketDirIsPrivateToThisUser(t *testing.T) {
	// Not t.TempDir(): on macOS that is a long /var/folders path which
	// prepareSocketDir would (correctly) refuse for exceeding sun_path.
	base := filepath.Join("/tmp", "sky-mode-"+strconv.Itoa(os.Getpid()))
	t.Cleanup(func() { _ = os.RemoveAll(base) })

	// (a) a directory prepareSocketDir CREATES.
	fresh := filepath.Join(base, "fresh")
	if err := prepareSocketDir(fresh); err != nil {
		t.Fatalf("prepareSocketDir(%s): %v", fresh, err)
	}
	assertSocketDirIsPrivate(t, fresh, "created")

	// (b) a directory that ALREADY exists, world-writable. MkdirAll is a no-op
	// on it; only the Chmod tightens it.
	stale := filepath.Join(base, "stale")
	if err := os.MkdirAll(stale, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(stale, 0o777); err != nil {
		t.Fatal(err)
	}
	if got := statPerm(t, stale); got != 0o777 {
		t.Fatalf("the test could not create a 0777 directory (got %04o); the rest of "+
			"this case is vacuous", got)
	}
	if err := prepareSocketDir(stale); err != nil {
		t.Fatalf("prepareSocketDir(%s): %v", stale, err)
	}
	assertSocketDirIsPrivate(t, stale, "pre-existing")
}

func statPerm(t *testing.T, dir string) os.FileMode {
	t.Helper()
	st, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat %s: %v", dir, err)
	}
	if !st.IsDir() {
		t.Fatalf("%s is not a directory", dir)
	}
	return st.Mode().Perm()
}

func assertSocketDirIsPrivate(t *testing.T, dir, which string) {
	t.Helper()
	got := statPerm(t, dir)
	if got != 0o700 {
		t.Errorf("the %s socket directory %s is mode %04o, want 0700.\n"+
			"Local auth is `trust` and unix_socket_permissions defaults to 0777, so this\n"+
			"directory's mode is the only thing between any local user and superuser on\n"+
			"the app's database: `psql -h %s -d postgres` would connect.", which, dir, got, dir)
	}
	if got&0o077 != 0 {
		t.Errorf("the %s socket directory %s grants group/other %04o", which, dir, got&0o077)
	}
}

func TestDSNIsTheURLFormPointedAtTheSocketDirectory(t *testing.T) {
	dsn := dsnForSocketDir("/tmp/sky-abc")
	if driver, _ := detectDriver(dsn); driver != "pgx" {
		t.Fatalf("the app's own driver detection classifies %q as %q, not postgres", dsn, driver)
	}
	if !strings.Contains(dsn, "host=/tmp/sky-abc") {
		t.Fatalf("DSN does not name the socket directory: %s", dsn)
	}
}

// ---------------------------------------------------------------------------
// Liveness, stale pid files, data-directory state
// ---------------------------------------------------------------------------

func TestParsePostmasterPid(t *testing.T) {
	const real = "4242\n/var/lib/app/pg\n1754000000\n5432\n/tmp/sky-abc\n\n\n  ready   \n"
	if pid, ok := parsePostmasterPid(real); !ok || pid != 4242 {
		t.Fatalf("got (%d, %v), want (4242, true)", pid, ok)
	}
	for _, bad := range []string{"", "\n", "not-a-pid\n", "-1\n", "0\n"} {
		if _, ok := parsePostmasterPid(bad); ok {
			t.Errorf("%q was parsed as a pid", bad)
		}
	}
}

func TestCommandLooksLikePostgres(t *testing.T) {
	for _, yes := range []string{
		"/opt/sky/postgres/bin/postgres -D /var/lib/app/pg",
		"postgres: checkpointer",
		"/usr/lib/postgresql/16/bin/postmaster -D /data",
	} {
		if !commandLooksLikePostgres(yes) {
			t.Errorf("%q was not recognised as a postmaster", yes)
		}
	}
	// The pid-reuse case: after a SIGKILL the kernel is free to hand the
	// recorded number to something else, and kill(pid,0) says "alive" about it.
	for _, no := range []string{
		"-zsh", "/bin/sleep 100", "node server.js",
		// Both of these MENTION postgres and neither is one. The first is the
		// app itself under a plausible data directory; the second is this test
		// binary, which is how the substring version of this check was caught.
		"/srv/app --embed --data-dir /var/lib/postgres-data",
		"/var/folders/x/rt.test -test.run=TestStopPostgresIsIdempotent",
	} {
		if commandLooksLikePostgres(no) {
			t.Errorf("%q was mistaken for a postmaster", no)
		}
	}
}

func TestProcessAliveOnThisProcessAndOnNobody(t *testing.T) {
	if !processAlive(os.Getpid()) {
		t.Error("this process reports as dead")
	}
	if processAlive(0) || processAlive(-1) {
		t.Error("a non-pid reports as alive")
	}
}

func TestAStalePidfileIsClearedAndALiveOneIsNot(t *testing.T) {
	dir := t.TempDir()

	// A pid that is not a postgres: cleared, not fatal. This is what a SIGKILL
	// leaves behind, and refusing to boot on it would need a human every time.
	writePidfile(t, dir, 999999)
	if err := clearStalePidfile(dir); err != nil {
		t.Fatalf("a stale pid file must be cleared, not refused: %v", err)
	}
	if fileExists(filepath.Join(dir, "postmaster.pid")) {
		t.Fatal("the stale pid file is still there")
	}

	// Our own pid IS alive, and `ps` will not call it a postmaster — so it is
	// treated as stale. That is the two-legged check doing its job.
	writePidfile(t, dir, os.Getpid())
	if err := clearStalePidfile(dir); err != nil {
		t.Fatalf("a live NON-postgres pid must not block a start: %v", err)
	}

	// No pid file at all is not an error.
	if err := clearStalePidfile(dir); err != nil {
		t.Fatalf("absent pid file: %v", err)
	}
}

func writePidfile(t *testing.T, dir string, pid int) {
	t.Helper()
	body := strconv.Itoa(pid) + "\n" + dir + "\n1754000000\n5432\n/tmp/sky-x\n"
	if err := os.WriteFile(filepath.Join(dir, "postmaster.pid"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestInspectDataDir(t *testing.T) {
	root := t.TempDir()

	absent := filepath.Join(root, "nothing")
	if st, err := inspectDataDir(absent); err != nil || st != dataDirAbsent {
		t.Errorf("missing directory: got (%v, %v)", st, err)
	}

	empty := filepath.Join(root, "empty")
	pgMkdirAll(t, empty)
	if st, _ := inspectDataDir(empty); st != dataDirAbsent {
		t.Errorf("empty directory: got %v, want absent", st)
	}

	rubble := filepath.Join(root, "half-initdb")
	pgMkdirAll(t, rubble)
	pgWriteFile(t, filepath.Join(rubble, "base"), "")
	if st, _ := inspectDataDir(rubble); st != dataDirRubble {
		t.Errorf("half-finished initdb: got %v, want rubble", st)
	}

	good := filepath.Join(root, "cluster")
	pgMkdirAll(t, good)
	pgWriteFile(t, filepath.Join(good, "PG_VERSION"), "18\n")
	if st, _ := inspectDataDir(good); st != dataDirInitialised {
		t.Errorf("initialised cluster: got %v, want initialised", st)
	}
}

func TestAMajorVersionMismatchIsRefusedWithBothVersions(t *testing.T) {
	dir := t.TempDir()
	pgWriteFile(t, filepath.Join(dir, "PG_VERSION"), "14\n")
	err := checkMajorMatches(dir, pgBins{major: 18, binDir: "/opt/sky/pg/bin"})
	if err == nil {
		t.Fatal("an 18 server was allowed to open a 14 data directory")
	}
	for _, want := range []string{"14", "18", "pg_upgrade"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not mention %q:\n%s", want, err)
		}
	}
	pgWriteFile(t, filepath.Join(dir, "PG_VERSION"), "18\n")
	if err := checkMajorMatches(dir, pgBins{major: 18}); err != nil {
		t.Fatalf("matching majors refused: %v", err)
	}
}

func TestParsePgVersion(t *testing.T) {
	cases := map[string]struct {
		v string
		m int
	}{
		"postgres (PostgreSQL) 18.6":           {"18.6", 18},
		"pg_ctl (PostgreSQL) 14.21 (Homebrew)": {"14.21", 14},
		"postgres (PostgreSQL) 9.6.24":         {"9.6.24", 9},
		"postgres (PostgreSQL) 17rc1":          {"17", 17},
	}
	for in, want := range cases {
		v, m, ok := parsePgVersion(in)
		if !ok || v != want.v || m != want.m {
			t.Errorf("parsePgVersion(%q) = (%q, %d, %v), want (%q, %d, true)", in, v, m, ok, want.v, want.m)
		}
	}
	if _, _, ok := parsePgVersion("command not found"); ok {
		t.Error("a non-version was parsed as a version")
	}
}

func pgMkdirAll(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o700); err != nil {
		t.Fatal(err)
	}
}

func pgWriteFile(t *testing.T, p, body string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
