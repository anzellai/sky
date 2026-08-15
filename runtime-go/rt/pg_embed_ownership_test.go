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
	"strings"
	"syscall"
	"testing"
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
func TestOwnershipAnAdoptedClusterIsNotStopped(t *testing.T) {
	for _, tc := range []struct {
		name     string
		adopted  bool
		wantStop bool
	}{
		{"started by this process", false, true},
		{"adopted from another", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stopped := false
			s := &pgSupervisor{
				adopted: tc.adopted,
				stopFn:  func() error { stopped = true; return nil },
			}
			s.stopPostgres()
			if stopped != tc.wantStop {
				if tc.adopted {
					t.Fatal("the app stopped a cluster it did not start.\n" +
						"`sky db start` is contracted as persistent — it stays up until " +
						"`sky db stop` — and `sky run` honours that by ref-counting. An " +
						"`--embed` binary that stops what it adopted takes a developer's " +
						"cluster away every time they run their own build.")
				}
				t.Fatal("the app did not stop the cluster it started — the postmaster is orphaned")
			}
			// Whether or not the database went down, the supervisor is out of
			// service: watchAdopted must not go on to announce that a postmaster
			// it is no longer responsible for has "gone" and exit the process.
			if !s.stopping.Load() {
				t.Error("stopPostgres left the supervisor un-stopped; its watcher is still armed")
			}
		})
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
