package rt

// Who owns the cluster, and when does the migration run.
//
// Both gates here are about a boundary the rest of the `--embed` suite does not
// cross: what one PROCESS does to a cluster ANOTHER process is responsible for,
// and what `main` does in what order. Neither can be observed from inside a
// single test goroutine, so both drive a real child process — the test binary
// re-execed with a mode variable, exactly as
// `TestDeadPostmasterExitsTheAppNonZero` does — and inspect what it left behind.
//
// Live gates skip without PostgreSQL:
//
//	SKY_POSTGRES_BIN=/opt/homebrew/opt/postgresql@14/bin go test ./rt/ -run Ownership

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Ownership — an app must not stop a cluster it merely adopted
// ---------------------------------------------------------------------------

// The unit leg. No PostgreSQL required, so it runs everywhere and is the gate
// that will actually catch a regression in CI.
//
// `stopFn` is the leaf action of the last shutdown phase, so "was the database
// stopped" is exactly "was stopFn called". An adopted supervisor must reach the
// end of stopPostgres without touching it.
//
// The table is over the ENTRY POINTS as well as the ownership flag, and that is
// the point of it. Ownership is not a property of stopPostgres; it is a property
// of every route that ends a process holding a cluster. Driving only the leaf
// leaves the two routes that actually run in production — `s.shutdown`, and the
// goroutine `installSignalHandler` starts, which is what a `kubectl rollout`, a
// Cloud Run revision swap and a `systemctl restart` all deliver into — proven by
// nothing. A phase 3 that called `pgCtlStop` / `signalPostmaster` directly
// instead of delegating to `stopPostgres` would bypass the ownership check
// entirely and leave a leaf-only gate green, while `sky db start` → `./app
// --embed` → any rollout took the developer's persistent cluster away.
func TestOwnershipAnAdoptedClusterIsNotStopped(t *testing.T) {
	// via drives one supervisor to its stop, by one of the three routes that
	// reach it. Each returns once the stop has either happened or been refused.
	type via struct {
		name string
		run  func(t *testing.T, s *pgSupervisor)
	}
	routes := []via{
		{"stopPostgres", func(_ *testing.T, s *pgSupervisor) { s.stopPostgres() }},
		{"shutdown", func(_ *testing.T, s *pgSupervisor) { s.shutdown(5 * time.Second) }},
		{"SIGTERM", func(t *testing.T, s *pgSupervisor) {
			exited := make(chan int, 1)
			s.exitFn = func(code int) { exited <- code }
			s.installSignalHandler()
			// Detach before returning: signal.Notify is process-wide, and a
			// registration left live would also catch the NEXT subtest's signal.
			defer s.detachSignalHandler()
			if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
				t.Fatalf("cannot signal this process: %v", err)
			}
			select {
			case <-exited:
			case <-time.After(15 * time.Second):
				t.Fatal("SIGTERM never reached the supervisor's handler")
			}
		}},
	}

	for _, r := range routes {
		for _, tc := range []struct {
			name     string
			adopted  bool
			wantStop bool
		}{
			{"started by this process", false, true},
			{"adopted from another", true, false},
		} {
			t.Run(r.name+"/"+tc.name, func(t *testing.T) {
				resetShutdownHooksForTesting()
				resetAcceptStoppersForTesting()
				t.Cleanup(func() {
					resetShutdownHooksForTesting()
					resetAcceptStoppersForTesting()
				})

				var mu sync.Mutex
				stopped := false
				s := &pgSupervisor{
					adopted: tc.adopted,
					stopFn: func() error {
						mu.Lock()
						stopped = true
						mu.Unlock()
						return nil
					},
				}
				r.run(t, s)

				mu.Lock()
				got := stopped
				mu.Unlock()
				if got != tc.wantStop {
					if tc.adopted {
						t.Fatal("the app stopped a cluster it did not start.\n" +
							"`sky db start` is contracted as persistent — it stays up until " +
							"`sky db stop` — and `sky run` honours that by ref-counting. An " +
							"`--embed` binary that stops what it adopted takes a developer's " +
							"cluster away every time they run their own build.")
					}
					t.Fatal("the app did not stop the cluster it started — the postmaster is " +
						"orphaned.\nIf this route stops PostgreSQL by some path OTHER than " +
						"stopPostgres (pgCtlStop or signalPostmaster called directly), it has " +
						"also bypassed the adopted check, and the adopted case above is passing " +
						"for the wrong reason.")
				}
				// Whether or not the database went down, the supervisor is out of
				// service: watchAdopted must not go on to announce that a postmaster
				// it is no longer responsible for has "gone" and exit the process.
				if !s.stopping.Load() {
					t.Error("the stop left the supervisor un-stopped; its watcher is still armed")
				}
			})
		}
	}
}

// The live leg, and the one that reproduces the reported symptom: a real
// cluster, a real second process that adopts it, and a real exit.
//
// This is what `sky db start` followed by `./sky-out/app --embed` does, and
// before the fix the cluster was `stopped` by the time the binary returned to
// the prompt.
func TestOwnershipLiveAdoptedClusterSurvivesTheAppThatAdoptedIt(t *testing.T) {
	if mode := os.Getenv("SKY_PG_OWNERSHIP_CHILD"); mode != "" {
		if mode == "adopt" {
			childAdoptThenExit()
		}
		return
	}
	binDir := livePgBinDir()
	if binDir == "" {
		t.Skip("no PostgreSQL binaries (set SKY_POSTGRES_BIN)")
	}
	t.Setenv("SKY_POSTGRES_BIN", binDir)
	root := durableTestDir(t, "ownership-adopt")

	// Stand in for `sky db start`: a cluster this process is responsible for.
	s := liveSupervisor(t, root)
	if err := s.boot(); err != nil {
		t.Fatalf("could not start the stand-in persistent cluster: %v", err)
	}
	t.Cleanup(func() {
		s.stopPostgres()
		if pid, ok := runningPostmaster(s.cfg.dataDir); ok {
			_ = syscall.Kill(pid, syscall.SIGQUIT)
		}
		_ = os.RemoveAll(s.cfg.socketDir)
	})
	before, ok := runningPostmaster(s.cfg.dataDir)
	if !ok {
		t.Fatal("no postmaster after the stand-in start — the rest of this test is vacuous")
	}

	// …and now the developer runs their own built binary.
	out, err := runEmbedChild(t, "adopt", root, binDir, nil)
	if err != nil {
		t.Fatalf("the --embed child failed (%v):\n%s", err, out)
	}

	after, stillUp := runningPostmaster(s.cfg.dataDir)
	if !stillUp {
		t.Fatalf("the cluster is gone: an `--embed` run stopped a cluster it adopted.\n"+
			"`sky db start` promises the cluster stays up until `sky db stop`; a "+
			"developer who runs their own binary against it must get it back.\nchild output:\n%s",
			out)
	}
	if after != before {
		t.Errorf("the postmaster changed pid across the child run (%d → %d): it was "+
			"restarted rather than adopted", before, after)
	}
	if !strings.Contains(out, "adopted") {
		t.Errorf("the child did not report adopting the running cluster:\n%s", out)
	}
}

// childAdoptThenExit is one `--embed` app, whole: start (which adopts), then the
// exit path generated `main` takes via `defer rt.StopEmbeddedPostgres()`.
func childAdoptThenExit() {
	defer LogPanicAndExit()
	MaybeStartEmbeddedPostgres()
	s := activeSupervisor()
	if s == nil {
		os.Stderr.WriteString("child: no supervisor was registered\n")
		os.Exit(9)
	}
	if !s.adopted {
		os.Stderr.WriteString("child: started its own cluster instead of adopting the live one\n")
		os.Exit(9)
	}
	s.detachSignalHandler()
	StopEmbeddedPostgres() // the defer generated `main` carries
	os.Exit(0)
}

// ---------------------------------------------------------------------------
// A self-migrating --embed binary
// ---------------------------------------------------------------------------

// `SKY_DB_OP=migrate ./app --embed` has to migrate against the cluster the same
// invocation just started. That is only true if the migration runs AFTER the
// start — which is why the call moved out of the generated
// `embedded_migrations.go` init() and into `main` (every init() runs before
// main, so from there the database did not exist yet).
//
// The child below is the generated `main` in the fixed order. Reversing its two
// calls is the mutation this gate exists to catch, and it fails with the same
// "no database" error the shipped binary produced.
func TestOwnershipLiveEmbedMigrateAppliesAgainstTheStartedCluster(t *testing.T) {
	if mode := os.Getenv("SKY_PG_OWNERSHIP_CHILD"); mode != "" {
		if mode == "migrate" {
			childEmbedMigrate()
		}
		return
	}
	binDir := livePgBinDir()
	if binDir == "" {
		t.Skip("no PostgreSQL binaries (set SKY_POSTGRES_BIN)")
	}
	t.Setenv("SKY_POSTGRES_BIN", binDir)
	root := durableTestDir(t, "ownership-migrate")

	out, err := runEmbedChild(t, "migrate", root, binDir, []string{"SKY_DB_OP=migrate"})
	if err != nil {
		t.Fatalf("`SKY_DB_OP=migrate ./app --embed` failed (%v):\n%s", err, out)
	}
	if !strings.Contains(out, "applied 1 migration") {
		t.Errorf("the child did not report applying the embedded migration:\n%s", out)
	}

	// The child is gone. Bring the same data directory back up and look at what
	// it left in the database — the only evidence that survives the process.
	s := liveSupervisor(t, root)
	if err := s.boot(); err != nil {
		t.Fatalf("cannot re-open the migrated cluster: %v", err)
	}
	t.Cleanup(func() {
		s.stopPostgres()
		if pid, ok := runningPostmaster(s.cfg.dataDir); ok {
			_ = syscall.Kill(pid, syscall.SIGQUIT)
		}
		_ = os.RemoveAll(s.cfg.socketDir)
	})
	// A one-shot migrate is a process that exits, and os.Exit skips main's
	// defers: if the exit did not route through ExitProcess the child's own
	// cluster would still be up and this boot would adopt it rather than start
	// it, leaving a postmaster running on the host forever.
	if s.adopted {
		t.Error("the migrate run left its cluster running — its exit skipped the stop")
	}

	db, err := sql.Open("pgx", s.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var applied string
	if err := db.QueryRow(`SELECT name FROM _sky_migrations`).Scan(&applied); err != nil {
		t.Fatalf("the migration ledger is empty — nothing was applied: %v\nchild output:\n%s", err, out)
	}
	if applied != "0001_widgets" {
		t.Errorf("_sky_migrations records %q, want 0001_widgets", applied)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM widgets`).Scan(&n); err != nil {
		t.Fatalf("the migrated table does not exist: %v\nchild output:\n%s", err, out)
	}
}

// ownershipTestMigrations is one committed `db/migrations/0001_widgets.json`, in
// the concatenated form `sky build` bakes into `rt.SkyEmbeddedMigrations`.
const ownershipTestMigrations = `[{"id":"0001_widgets","ops":[` +
	`{"kind":"createTable","table":"widgets","columns":[` +
	`{"name":"id","type":"text","pk":true},{"name":"label","type":"text"}]}]}]`

// childEmbedMigrate is the generated program for a project that has migrations,
// in the order `lower_main` + `write_embedded_migrations` produce:
//
//	init(): rt.SkyEmbeddedMigrations = "…"
//	main():	defer rt.LogPanicAndExit()
//	        rt.MaybeStartEmbeddedPostgres()
//	        defer rt.StopEmbeddedPostgres()
//	        rt.MaybeApplyEmbeddedMigrationsAndExit()
func childEmbedMigrate() {
	// init()
	SkyEmbeddedMigrations = ownershipTestMigrations

	// main()
	defer LogPanicAndExit()
	MaybeStartEmbeddedPostgres()
	defer StopEmbeddedPostgres()
	if s := activeSupervisor(); s != nil {
		// Not part of generated main: a live process-wide handler in a test
		// binary would outlive this child's purpose.
		s.detachSignalHandler()
	}
	MaybeApplyEmbeddedMigrationsAndExit()

	os.Stderr.WriteString("child: the migration returned instead of exiting\n")
	os.Exit(9)
}

// ---------------------------------------------------------------------------
// The FAILING exits — the ones a deploy actually hits
// ---------------------------------------------------------------------------

// A migration that fails is the NORMAL failure of a deploy step: a typo in a
// column name, a NOT NULL added to a table with rows, a constraint the data
// violates. The gate above proves only the SUCCESS exit, so reverting
// Db_migrateApply's `fail` path from ExitProcess back to os.Exit(1) leaves it
// green — and every failed deploy then leaves a postmaster running on the host.
// The retry adopts it and, by the ownership rule, never stops it; so does every
// run after that. The cluster the operator eventually finds belongs to a process
// that exited days ago.
//
// The whole property is "a non-zero exit stops the database too", so both halves
// are asserted: the child must fail, and it must leave nothing behind.
func TestOwnershipLiveEmbedMigrateFailureStopsItsClusterToo(t *testing.T) {
	if mode := os.Getenv("SKY_PG_OWNERSHIP_CHILD"); mode != "" {
		if mode == "migrate-fail" {
			childEmbedMigrateFailure()
		}
		return
	}
	binDir := livePgBinDir()
	if binDir == "" {
		t.Skip("no PostgreSQL binaries (set SKY_POSTGRES_BIN)")
	}
	t.Setenv("SKY_POSTGRES_BIN", binDir)
	root := durableTestDir(t, "ownership-migrate-fail")
	dataDir := filepath.Join(root, "pg")

	out, err := runEmbedChild(t, "migrate-fail", root, binDir, []string{"SKY_DB_OP=migrate"})
	t.Cleanup(func() {
		// Belt and braces: if the property under test is broken, the postmaster
		// this child left is still running and would outlive the whole suite.
		if pid, ok := runningPostmaster(dataDir); ok {
			_ = syscall.Kill(pid, syscall.SIGQUIT)
		}
	})

	// Vacuity guard: the child must actually have STARTED a cluster and then hit
	// the failure. A child that skipped the migration, or never started
	// PostgreSQL, would satisfy "no postmaster left" for the wrong reason.
	if !strings.Contains(out, "embedded PostgreSQL") {
		t.Fatalf("the child never started a cluster — this gate proves nothing:\n%s", out)
	}
	if err == nil {
		t.Fatalf("a migration against a table that does not exist exited 0:\n%s", out)
	}
	if !strings.Contains(out, "db: migration") {
		t.Errorf("the child did not report the migration failure:\n%s", out)
	}

	if pid, ok := runningPostmaster(dataDir); ok {
		t.Errorf("the FAILED migrate left its cluster running (pid %d).\n"+
			"os.Exit skips generated main's `defer rt.StopEmbeddedPostgres()`, so a failure\n"+
			"exit that does not route through rt.ExitProcess orphans the database. The next\n"+
			"deploy attempt adopts it and — correctly, per the ownership rule — never stops\n"+
			"it, so one bad migration leaves a postmaster on the host indefinitely.\n"+
			"child output:\n%s", pid, out)
	}
}

// childEmbedMigrateFailure is childEmbedMigrate with a migration that cannot
// apply. `raw` is used deliberately: the SQL reaches PostgreSQL verbatim, so the
// failure happens in the server rather than in the renderer, which is where a
// real bad migration fails.
func childEmbedMigrateFailure() {
	SkyEmbeddedMigrations = `[{"id":"0001_boom","ops":[` +
		`{"kind":"raw","sql":"ALTER TABLE a_table_that_does_not_exist ADD COLUMN x text"}]}]`

	defer LogPanicAndExit()
	MaybeStartEmbeddedPostgres()
	defer StopEmbeddedPostgres()
	if s := activeSupervisor(); s != nil {
		s.detachSignalHandler()
	}
	MaybeApplyEmbeddedMigrationsAndExit()

	os.Stderr.WriteString("child: the failing migration returned instead of exiting\n")
	os.Exit(9)
}

// `Std.System.exit` is the ordinary way a `Sky.Cli` job ends — and "background
// job / cron" is a first-class app shape, so `--embed` plus a one-shot job is
// exactly what ExitProcess exists for. It called os.Exit directly, which skips
// generated main's `defer rt.StopEmbeddedPostgres()`: the job printed its
// summary, exited 0, and left a PostgreSQL running behind it. Reproduced on an
// unmutated tree before this gate existed.
//
// The source audit in pg_embed_exit_audit_test.go keeps every OTHER exit honest.
// This one is behavioural because System.exit is the site the symptom was
// reported from, and a live cluster is the only thing that can say the database
// really went down rather than merely that the right function was called.
func TestOwnershipLiveSystemExitStopsTheEmbeddedCluster(t *testing.T) {
	if mode := os.Getenv("SKY_PG_OWNERSHIP_CHILD"); mode != "" {
		if mode == "sysexit" {
			childSystemExit()
		}
		return
	}
	binDir := livePgBinDir()
	if binDir == "" {
		t.Skip("no PostgreSQL binaries (set SKY_POSTGRES_BIN)")
	}
	t.Setenv("SKY_POSTGRES_BIN", binDir)
	root := durableTestDir(t, "ownership-system-exit")
	dataDir := filepath.Join(root, "pg")

	out, err := runEmbedChild(t, "sysexit", root, binDir, nil)
	t.Cleanup(func() {
		if pid, ok := runningPostmaster(dataDir); ok {
			_ = syscall.Kill(pid, syscall.SIGQUIT)
		}
	})
	if err != nil {
		t.Fatalf("the child failed (%v):\n%s", err, out)
	}
	if !strings.Contains(out, "embedded PostgreSQL") {
		t.Fatalf("the child never started a cluster — this gate proves nothing:\n%s", out)
	}
	if pid, ok := runningPostmaster(dataDir); ok {
		t.Errorf("`Std.System.exit` left the embedded cluster running (pid %d).\n"+
			"System.exit is how a Sky.Cli job ends; os.Exit skips main's\n"+
			"`defer rt.StopEmbeddedPostgres()`, so every cron run of an `--embed` job\n"+
			"orphans a postmaster and the next run adopts one it will never stop.\n"+
			"child output:\n%s", pid, out)
	}
}

// childSystemExit is a one-shot `--embed` job that ends the way Sky code ends
// one: `Std.System.exit 0`.
func childSystemExit() {
	defer LogPanicAndExit()
	MaybeStartEmbeddedPostgres()
	defer StopEmbeddedPostgres()
	if s := activeSupervisor(); s != nil {
		s.detachSignalHandler()
	}
	System_exit(0)

	os.Stderr.WriteString("child: System.exit returned instead of exiting\n")
	os.Exit(9)
}

// runEmbedChild re-execs the test binary as one `--embed` app run and returns
// its combined output.
//
// The environment is built from scratch rather than inherited: `--embed` refuses
// to start alongside `SKY_DB_PATH` or `DATABASE_URL` (there is no defensible
// precedence between a cluster and a DSN), and either one leaking in from the
// developer's shell would turn every gate here into a skip that looks like a
// pass.
func runEmbedChild(t *testing.T, mode, dataRoot, binDir string, extra []string) (string, error) {
	t.Helper()
	// The child runs ONLY the test it is the child of: every other test in this
	// binary would otherwise run as a parent inside it, starting clusters of its
	// own against the same data directory.
	cmd := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.v")
	cmd.Env = append([]string{
		"HOME=" + os.Getenv("HOME"),
		"PATH=" + os.Getenv("PATH"),
		"SKY_PG_OWNERSHIP_CHILD=" + mode,
		"SKY_POSTGRES_BIN=" + binDir,
		"SKY_DATA_DIR=" + dataRoot,
		// The child's os.Args are the test binary's, so `--embed` is asked for by
		// the environment spelling instead — the one a container image or a
		// systemd unit would use, and the same request either way.
		"SKY_EMBED_POSTGRES=1",
	}, extra...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
