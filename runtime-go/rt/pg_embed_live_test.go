package rt

// Live-cluster gates: a real initdb, a real postmaster, a real stop.
//
// These skip when there is no PostgreSQL to run. That is a real weakness of a
// suite — a gate that skips is a gate that proves nothing — so it is stated
// loudly rather than hidden: run them with
//
//	SKY_POSTGRES_BIN=/opt/homebrew/opt/postgresql@14/bin go test ./rt/ -run Live
//
// The data directory is deliberately NOT t.TempDir(). On macOS that is under
// /var/folders, which the supervisor refuses for the same reason a production
// deploy should: the system is entitled to empty it.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// livePgBinDir finds a PostgreSQL to test against, or "" .
func livePgBinDir() string {
	if d := strings.TrimSpace(os.Getenv("SKY_POSTGRES_BIN")); d != "" {
		if len(missingPgBins(d)) == 0 {
			return d
		}
		return ""
	}
	if p, err := exec.LookPath("postgres"); err == nil {
		if d := filepath.Dir(p); len(missingPgBins(d)) == 0 {
			return d
		}
	}
	matches, _ := filepath.Glob("/opt/homebrew/opt/postgresql@*/bin")
	for _, d := range matches {
		if len(missingPgBins(d)) == 0 {
			return d
		}
	}
	return ""
}

// durableTestDir hands back a directory the supervisor will accept — i.e. one
// the operating system is not entitled to empty.
//
// # Why the path carries the pid
//
// It used to be exactly `~/.sky/p5-live-test/<name>`, which is the same path in
// every checkout on the machine. Two test binaries running these gates at once
// — two worktrees, two agents, a rerun started before the last one finished —
// therefore shared a data directory, and the first line of this function is
// `os.RemoveAll`. The loser sees its cluster deleted from under `initdb`:
//
//	FATAL: could not open file "global/2676": No such file or directory
//	PANIC: could not open file ".../global/pg_control": No such file or directory
//	initdb: error: failed to remove data directory
//
// which reads as a broken embedded-Postgres path and is nothing of the kind.
// That is the worst shape a flake can have: it accuses the code under test, and
// it is the victim who investigates. It cost this branch one contaminated
// mutation-matrix run (`docs/history/embedded-postgres/mutation-matrix.md`),
// where two gates failed for reasons that had nothing to do with the defect
// being injected.
//
// The pid keeps it unique per test binary while staying SHORT, which is the
// reason this lives under `$HOME` rather than in `t.TempDir()`: macOS caps a
// unix socket path at 104 bytes and the per-test `TMPDIR` blows straight
// through it. `SKY_LIVE_TEST_ROOT` overrides the parent for a harness that
// wants to place it somewhere explicit.
func durableTestDir(t *testing.T, name string) string {
	t.Helper()
	root := os.Getenv("SKY_LIVE_TEST_ROOT")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skip("no home directory to put a durable data directory in")
		}
		root = filepath.Join(home, ".sky", "p5-live-test")
	}
	dir := filepath.Join(root, fmt.Sprintf("%s-%d", name, os.Getpid()))
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func liveSupervisor(t *testing.T, root string) *pgSupervisor {
	t.Helper()
	cfg, err := resolveEmbedConfig([]string{"app", "--data-dir", root}, fakeEnv(nil), root)
	if err != nil {
		t.Fatal(err)
	}
	bins, err := discoverPgBins(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return &pgSupervisor{cfg: cfg, bins: bins, dsn: dsnForSocketDir(cfg.socketDir)}
}

// The whole lifecycle on one cluster, in the order the failure modes actually
// occur in production.
func TestLiveEmbeddedClusterLifecycle(t *testing.T) {
	binDir := livePgBinDir()
	if binDir == "" {
		t.Skip("no PostgreSQL binaries (set SKY_POSTGRES_BIN)")
	}
	t.Setenv("SKY_POSTGRES_BIN", binDir)
	root := durableTestDir(t, "lifecycle")

	s := liveSupervisor(t, root)
	t.Cleanup(func() {
		_ = os.RemoveAll(s.cfg.socketDir)
	})

	// In production, "the app was SIGKILLed" means the watcher died with it.
	// In-process the closest equivalent is detaching a supervisor before its
	// postmaster is killed — otherwise its (correct) reaction, exiting the
	// process non-zero, takes the test runner with it.
	detach := func(sup *pgSupervisor) { sup.stopping.Store(true) }

	// --- first boot: initdb, tuned conf, a postmaster on a unix socket ------
	if err := s.boot(); err != nil {
		t.Fatalf("first boot: %v", err)
	}
	defer func() {
		detach(s)
		if pid, ok := runningPostmaster(s.cfg.dataDir); ok {
			_ = syscall.Kill(pid, syscall.SIGQUIT)
		}
	}()

	if !fileExists(filepath.Join(s.cfg.dataDir, "PG_VERSION")) {
		t.Fatal("initdb did not produce a data directory")
	}
	conf, err := os.ReadFile(filepath.Join(s.cfg.dataDir, "postgresql.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(conf), skyConfMarker) {
		t.Error("the tuned block was not written into postgresql.conf")
	}
	pid, ok := runningPostmaster(s.cfg.dataDir)
	if !ok {
		t.Fatal("no postmaster is serving the data directory after boot")
	}

	// The postmaster must be in its own process group, so a Ctrl-C delivered to
	// the app's group does not reach it.
	if pgid, err := syscall.Getpgid(pid); err != nil {
		t.Errorf("cannot read the postmaster's process group: %v", err)
	} else if pgid == syscall.Getpgrp() {
		t.Error("the postmaster shares the app's process group")
	}

	// --- the mode of the socket directory of the RUNNING cluster ------------
	//
	// TestPrepareSocketDirIsPrivateToThisUser asserts what prepareSocketDir
	// leaves behind, which is a property of that function and not of the
	// cluster. One `os.Chmod(s.cfg.socketDir, 0o711)` anywhere AFTER it returns
	// — in boot, in spawn, in a future "make the socket reachable from a
	// sidecar" change — leaves that gate green and opens the database: at 0711
	// the directory is world-traversable, `unix_socket_permissions` defaults to
	// 0777, and local auth is `trust`, so any local user runs
	// `psql -h <dir> -d postgres` as a SUPERUSER.
	//
	// So the assertion is made here instead, on the directory a live postmaster
	// is actually listening in.
	assertSocketDirIsPrivate(t, s.cfg.socketDir, "live cluster's")

	// …and the socket file inside it really is the thing that would be reached.
	// This is the vacuity guard: if PostgreSQL had created a private socket the
	// directory's mode would be belt-and-braces rather than the only lock, and
	// the assertion above would prove nothing about access.
	socks, _ := filepath.Glob(filepath.Join(s.cfg.socketDir, ".s.PGSQL.*"))
	if len(socks) == 0 {
		t.Fatalf("no socket in %s — the mode assertion above is about an empty directory",
			s.cfg.socketDir)
	}
	if st, err := os.Stat(socks[0]); err != nil {
		t.Errorf("cannot stat the socket: %v", err)
	} else if st.Mode().Perm()&0o077 == 0 {
		t.Logf("note: the socket itself is %04o, tighter than PostgreSQL's 0777 default; "+
			"the directory mode is still the documented control", st.Mode().Perm())
	}

	// --- readiness means a query answers, not that a socket exists ---------
	db, err := sql.Open("pgx", s.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var one int
	if err := db.QueryRow("select 1").Scan(&one); err != nil || one != 1 {
		t.Fatalf("the DSN handed to the app does not work: %v", err)
	}

	// --- a second boot adopts rather than starting a second postmaster -----
	// This is the orphan a SIGKILLed app leaves behind.
	second := liveSupervisor(t, root)
	if err := second.boot(); err != nil {
		t.Fatalf("second boot did not adopt the running cluster: %v", err)
	}
	if !second.adopted {
		t.Error("the second boot started something instead of adopting")
	}
	if second.cmd != nil {
		t.Error("the second boot spawned a child")
	}
	if pid2, _ := runningPostmaster(s.cfg.dataDir); pid2 != pid {
		t.Errorf("the data directory is now served by pid %d, was %d", pid2, pid)
	}

	// --- a stale postmaster.pid is cleared, not fatal -----------------------
	// SIGKILL the whole group, exactly as a `kill -9` on the tree would.
	detach(s)
	detach(second)
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		t.Fatalf("cannot kill the cluster: %v", err)
	}
	waitGone(t, pid)
	if !fileExists(filepath.Join(s.cfg.dataDir, "postmaster.pid")) {
		t.Fatal("a SIGKILLed postmaster left no pid file — the rest of this test is vacuous")
	}
	// …and the pid in it has been RECYCLED. This is the case that needs Sky:
	// PostgreSQL clears a pid file naming a plainly-dead process by itself, but
	// when the kernel has handed that number to something unrelated it sees a
	// live pid, concludes another postmaster is running, and refuses to start —
	// forever, and with a message that accuses a process that has nothing to do
	// with it. The two-legged check is what tells the two apart.
	stand := exec.Command("/bin/sh", "-c", "sleep 60")
	if err := stand.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stand.Process.Kill(); _, _ = stand.Process.Wait() }()
	pidfile := filepath.Join(s.cfg.dataDir, "postmaster.pid")
	body, err := os.ReadFile(pidfile)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitN(string(body), "\n", 2)
	if len(lines) != 2 {
		t.Fatalf("postmaster.pid does not look like one:\n%s", body)
	}
	recycled := stand.Process.Pid
	if err := os.WriteFile(pidfile, []byte(strconv.Itoa(recycled)+"\n"+lines[1]), 0o600); err != nil {
		t.Fatal(err)
	}
	if !processAlive(recycled) {
		t.Fatal("the stand-in process died; the rest of this test is vacuous")
	}

	third := liveSupervisor(t, root)
	if err := third.boot(); err != nil {
		t.Fatalf("a stale pid file naming a recycled pid must be cleared, not fatal: %v", err)
	}
	if third.adopted {
		t.Error("a dead cluster was adopted")
	}
	if _, ok := runningPostmaster(third.cfg.dataDir); !ok {
		t.Fatal("no postmaster after recovering from a stale pid file")
	}

	// --- shutdown, in order, with a real pg_ctl stop ------------------------
	resetShutdownHooksForTesting()
	resetAcceptStoppersForTesting()
	var mu sync.Mutex
	var seq []string
	note := func(s string) { mu.Lock(); seq = append(seq, s); mu.Unlock() }
	RegisterAcceptStopper("listener", func() { note("stop-accepting") })
	RegisterShutdownHook("drain", func(context.Context) { time.Sleep(100 * time.Millisecond); note("drain") })
	go func() {
		<-time.After(30 * time.Millisecond)
		note("query-during-drain-ok")
		var n int
		if err := db.QueryRow("select 2").Scan(&n); err != nil {
			note("QUERY FAILED: " + err.Error())
		}
	}()

	// Through the REAL signal path, not a direct s.shutdown call: the goroutine
	// installSignalHandler starts is what a `kubectl rollout`, a Cloud Run
	// revision swap and a `systemctl restart` all go through, and a phase
	// mistake made there rather than in shutdown is invisible to a test that
	// calls shutdown itself.
	exited := make(chan int, 1)
	third.exitFn = func(code int) { exited <- code }
	third.installSignalHandler()
	defer third.detachSignalHandler()
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("cannot signal this process: %v", err)
	}
	select {
	case <-exited:
	case <-time.After(60 * time.Second):
		t.Fatal("SIGTERM never completed the supervisor's shutdown sequence")
	}

	mu.Lock()
	got := strings.Join(seq, " → ")
	mu.Unlock()
	if !strings.HasPrefix(got, "stop-accepting → ") {
		t.Errorf("the listener was not closed first: %s", got)
	}
	if !strings.HasSuffix(got, "drain") && !strings.Contains(got, "drain") {
		t.Errorf("nothing drained: %s", got)
	}
	if strings.Contains(got, "QUERY FAILED") {
		t.Errorf("a query in flight during the drain failed — the database went away too early:\n  %s", got)
	}
	if _, ok := runningPostmaster(third.cfg.dataDir); ok {
		t.Error("the postmaster is still running after shutdown")
	}
	if fileExists(filepath.Join(third.cfg.dataDir, "postmaster.pid")) {
		t.Error("pg_ctl stop left a pid file behind")
	}

	// --- and the stop was CLEAN, which only the next boot can say ------------
	//
	// "the postmaster is gone" is true of `pg_ctl stop -m immediate`, of
	// SIGQUIT, and of SIGKILL. All three skip the shutdown checkpoint, and
	// PostgreSQL then replays WAL on the next start — so switching `-m fast` to
	// `-m immediate` leaves every assertion above green while turning every
	// deploy restart into crash recovery. On a large cluster that is the
	// difference between a restart and an outage, and the only place it is
	// visible is the server's own log the FOLLOWING time it starts.
	//
	// The log is truncated first: this test has already SIGKILLed one postmaster
	// on purpose, and the recovery line that produced is still in the file.
	if err := os.Truncate(third.cfg.logPath, 0); err != nil {
		t.Fatalf("cannot truncate the server log: %v", err)
	}
	fourth := liveSupervisor(t, root)
	if err := fourth.boot(); err != nil {
		t.Fatalf("boot after a clean stop: %v", err)
	}
	t.Cleanup(func() {
		detach(fourth)
		fourth.stopPostgres()
		if pid, ok := runningPostmaster(fourth.cfg.dataDir); ok {
			_ = syscall.Kill(pid, syscall.SIGQUIT)
		}
	})
	logAfter, err := os.ReadFile(fourth.cfg.logPath)
	if err != nil {
		t.Fatalf("cannot read the server log: %v", err)
	}
	if len(strings.TrimSpace(string(logAfter))) == 0 {
		t.Fatal("the server logged nothing on the boot after the stop — this gate cannot " +
			"tell a clean shutdown from a crash and is vacuous")
	}
	for _, crash := range []string{
		"was not properly shut down",
		"automatic recovery in progress",
		"database system was interrupted",
	} {
		if strings.Contains(string(logAfter), crash) {
			t.Errorf("the previous shutdown was not clean — the next boot logged %q.\n"+
				"`pg_ctl stop -m fast` runs a shutdown checkpoint; `-m immediate`, SIGQUIT and\n"+
				"SIGKILL do not, and PostgreSQL replays WAL instead. Every deploy restart pays\n"+
				"for that, and on a large cluster it is the difference between a restart and an\n"+
				"outage.\nlog:\n%s", crash, logAfter)
		}
	}
}

// A boot that fails AFTER the spawn is the one that leaves something behind.
//
// Every failure gate so far fails BEFORE a postmaster exists — a bad data
// directory, a major-version mismatch, an over-long socket path, no binaries —
// so "the start failed" and "nothing is running" have never had to be two
// different facts. They are: `waitReady` can time out on a cluster still
// replaying WAL, on a postgresql.conf edited into refusing connections, or on a
// disk that filled during startup, and by then the postmaster is up and in its
// own process group.
//
// What follows is not one leaked process. `MaybeStartEmbeddedPostgres` exits
// non-zero, so nothing is registered for `StopEmbeddedPostgres` to find and
// generated main's defer has nothing to do; the operator retries; the retry
// ADOPTS the postmaster the first attempt left, and — correctly, per the
// ownership rule — never stops it either. The cluster outlives every run after
// the one that failed.
func TestLiveABootThatFailsAfterTheSpawnStopsWhatItStarted(t *testing.T) {
	binDir := livePgBinDir()
	if binDir == "" {
		t.Skip("no PostgreSQL binaries (set SKY_POSTGRES_BIN)")
	}
	t.Setenv("SKY_POSTGRES_BIN", binDir)
	root := durableTestDir(t, "boot-fails-after-spawn")

	s := liveSupervisor(t, root)
	t.Cleanup(func() {
		s.stopping.Store(true)
		if pid, ok := runningPostmaster(s.cfg.dataDir); ok {
			_ = syscall.Kill(pid, syscall.SIGQUIT)
		}
		_ = os.RemoveAll(s.cfg.socketDir)
	})
	// The real initdb, the real spawn, and then the one thing that goes wrong.
	s.readyFn = func(time.Duration) error {
		return errors.New("sky --embed: PostgreSQL did not accept connections in time")
	}

	err := s.boot()
	if err == nil {
		t.Fatal("boot reported success although readiness failed")
	}
	// Vacuity guard: a boot that failed before spawning would satisfy the
	// assertion below without proving anything. The postmaster must have EXISTED.
	if s.cmd == nil || s.cmd.Process == nil {
		t.Fatalf("no postmaster was ever spawned, so 'nothing was left running' is "+
			"true for the wrong reason: %v", err)
	}
	spawned := s.cmd.Process.Pid

	if pid, ok := runningPostmaster(s.cfg.dataDir); ok {
		t.Errorf("the failed boot left a postmaster running (spawned %d, still serving %d).\n"+
			"MaybeStartEmbeddedPostgres exits non-zero from here and nothing is registered\n"+
			"for StopEmbeddedPostgres to find, so this process is the last one that could\n"+
			"have stopped it. The operator's retry adopts it and never stops it either.",
			spawned, pid)
	}
	if processAlive(spawned) && isPostgresProcess(spawned) {
		t.Errorf("pid %d is still a live postgres after the failed boot", spawned)
	}
}

// The DSN handoff is the whole point of `--embed`, and nothing called
// startEmbeddedPostgres: the unit gates stop at embeddedDSNConflict and the
// lifecycle test above drives s.boot() directly. Deleting either os.Setenv left
// the entire suite green.
//
// Losing `DATABASE_URL` in particular does not look like an `--embed` bug from
// the outside. `Db.connect ()` reads `<PREFIX>_DB_PATH` first, so the app's own
// queries keep working; it is the Sky.Live session store, the analytics store
// and Std.Jobs that fall back to `DATABASE_URL`, find nothing, and SILENTLY
// degrade to their non-Postgres defaults. Sessions land in memory, a restart
// loses every one of them, and the embedded cluster sits there running
// perfectly — which reads as a Sky.Live bug for as long as it takes someone to
// check what the session store actually opened.
func TestLiveEmbeddedStartHandsTheDSNToBothNamesTheRuntimeReads(t *testing.T) {
	binDir := livePgBinDir()
	if binDir == "" {
		t.Skip("no PostgreSQL binaries (set SKY_POSTGRES_BIN)")
	}
	t.Setenv("SKY_POSTGRES_BIN", binDir)
	root := durableTestDir(t, "dsn-handoff")
	t.Setenv("SKY_DATA_DIR", root)
	// startEmbeddedPostgres refuses to run alongside either name, so both are
	// cleared here — through t.Setenv, which also puts the caller's values back
	// after the os.Setenv the function under test performs.
	t.Setenv(skyEnvName("DB_PATH"), "")
	t.Setenv("DATABASE_URL", "")

	prev := activeSupervisor()
	t.Cleanup(func() { setActiveSupervisor(prev) })

	if err := startEmbeddedPostgres(); err != nil {
		t.Fatalf("startEmbeddedPostgres: %v", err)
	}
	s := activeSupervisor()
	if s == nil {
		t.Fatal("startEmbeddedPostgres registered no supervisor")
	}
	t.Cleanup(func() {
		// Detach FIRST: installSignalHandler's registration is process-wide, and
		// a handler left live here would also catch the signal a later test
		// raises — and take the test binary down with os.Exit.
		s.detachSignalHandler()
		s.stopPostgres()
		_ = os.RemoveAll(s.cfg.socketDir)
	})

	want := s.dsn
	if !strings.Contains(want, "host="+s.cfg.socketDir) {
		t.Fatalf("the supervisor's own DSN does not name its socket directory: %s", want)
	}
	for _, name := range []string{skyEnvName("DB_PATH"), "DATABASE_URL"} {
		if got := os.Getenv(name); got != want {
			t.Errorf("%s = %q, want the embedded cluster's DSN %q.\n"+
				"Everything that reads this name — Db.connect for the first, and the "+
				"Sky.Live session store, Std.Analytics and Std.Jobs for the second — "+
				"silently falls back to a non-Postgres default when it is missing.",
				name, got, want)
		}
	}

	// …and the value is a working DSN, not merely a matching string.
	db, err := sql.Open("pgx", os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatalf("the exported DSN does not open: %v", err)
	}
	defer db.Close()
	var one int
	if err := db.QueryRow("select 1").Scan(&one); err != nil || one != 1 {
		t.Fatalf("the exported DSN does not answer a query: %v", err)
	}
}

func waitGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("pid %d did not die", pid)
}

// ---------------------------------------------------------------------------
// A dead postmaster must exit the app non-zero
// ---------------------------------------------------------------------------

// The exit is a process-level effect, so it is observed from a parent: this
// test re-execs the test binary with SKY_PG_CHILD_MODE set and inspects the
// child's status. `fake` runs everywhere; `live` kills a real postmaster and is
// the one that proves the wiring rather than the function.
func TestDeadPostmasterExitsTheAppNonZero(t *testing.T) {
	switch os.Getenv("SKY_PG_CHILD_MODE") {
	case "fake":
		childFakeDeadPostmaster()
		return
	case "live":
		childLiveDeadPostmaster()
		return
	}

	modes := []string{"fake"}
	if livePgBinDir() != "" {
		modes = append(modes, "live")
	} else {
		t.Log("no PostgreSQL binaries: running the fake-child mode only")
	}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestDeadPostmasterExitsTheAppNonZero$", "-test.v")
			cmd.Env = append(os.Environ(), "SKY_PG_CHILD_MODE="+mode)
			if d := livePgBinDir(); d != "" {
				cmd.Env = append(cmd.Env, "SKY_POSTGRES_BIN="+d)
			}
			out, err := cmd.CombinedOutput()
			code := cmd.ProcessState.ExitCode()
			if err == nil || code == 0 {
				t.Fatalf("the app survived its database dying (exit %d):\n%s", code, out)
			}
			if code != 1 {
				t.Errorf("exit code %d, want 1:\n%s", code, out)
			}
			if !strings.Contains(string(out), "exited unexpectedly") &&
				!strings.Contains(string(out), "is gone") {
				t.Errorf("the app did not say why it exited:\n%s", out)
			}
		})
	}
}

func childFakeDeadPostmaster() {
	cmd := exec.Command("/bin/sh", "-c", "exit 7")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		os.Stderr.WriteString("child: cannot start the stand-in: " + err.Error() + "\n")
		os.Exit(9)
	}
	s := &pgSupervisor{cmd: cmd, cfg: embedConfig{logPath: filepath.Join(os.TempDir(), "nonexistent.log")}}
	s.watchChild() // must os.Exit(1)
	os.Stderr.WriteString("child: watchChild returned; the app would have kept serving\n")
	os.Exit(9)
}

func childLiveDeadPostmaster() {
	// The same isolation durableTestDir provides, spelled out because this runs
	// in a re-exec'd CHILD with no *testing.T to hand.
	//
	// It is the last place that built this path by hand, and it survived the fix
	// to durableTestDir for exactly that reason — one helper corrected, one
	// open-coded copy left behind, and the copy is what collided. It cost a
	// mutation-matrix run whose baseline came back with 19 failures. Route new
	// live-cluster paths through durableTestDir, or through this env var when
	// there is no test context.
	root := os.Getenv("SKY_LIVE_TEST_ROOT")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			os.Exit(9)
		}
		root = filepath.Join(home, ".sky", "p5-live-test")
	}
	root = filepath.Join(root, fmt.Sprintf("deadchild-%d", os.Getpid()))
	_ = os.RemoveAll(root)
	if err := os.MkdirAll(root, 0o700); err != nil {
		os.Exit(9)
	}
	cfg, err := resolveEmbedConfig([]string{"app", "--data-dir", root}, fakeEnv(nil), root)
	if err != nil {
		os.Stderr.WriteString("child: " + err.Error() + "\n")
		os.Exit(9)
	}
	bins, err := discoverPgBins(cfg)
	if err != nil {
		os.Stderr.WriteString("child: " + err.Error() + "\n")
		os.Exit(9)
	}
	s := &pgSupervisor{cfg: cfg, bins: bins, dsn: dsnForSocketDir(cfg.socketDir)}
	defer os.RemoveAll(root)
	defer os.RemoveAll(cfg.socketDir)
	if err := s.boot(); err != nil {
		os.Stderr.WriteString("child: " + err.Error() + "\n")
		os.Exit(9)
	}
	// The disk fails, the OOM killer arrives, the cluster is corrupt: the
	// postmaster goes away and the app did not ask it to.
	_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
	time.Sleep(30 * time.Second) // watchChild should exit(1) long before this
	os.Stderr.WriteString("child: still serving 30s after the database died\n")
	os.Exit(9)
}
