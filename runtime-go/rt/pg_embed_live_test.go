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
func durableTestDir(t *testing.T, name string) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory to put a durable data directory in")
	}
	dir := filepath.Join(home, ".sky", "p5-live-test", name)
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

	third.shutdown(10 * time.Second)

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
	home, err := os.UserHomeDir()
	if err != nil {
		os.Exit(9)
	}
	root := filepath.Join(home, ".sky", "p5-live-test", "deadchild")
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
