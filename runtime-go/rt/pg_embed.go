package rt

// Embedded PostgreSQL — the runtime supervisor behind `./app --embed`.
// Phase 5 of docs/skydb/embedded-postgres.md; the compiler half
// (`sky build --embed`, the `go:embed` of the bundle) is a separate,
// deliberately small follow-up that only has to set the two variables at the
// top of pg_embed_bundle.go and call two functions from the generated main.
//
// What this is
// ------------
// A built Sky app that was compiled with `--embed` carries a PostgreSQL
// distribution inside it. On start it extracts that distribution once,
// initialises a data directory, starts a postmaster as its own child on a unix
// socket, waits for it to accept connections, and hands the resulting DSN to
// the app's ordinary connection path (`Db_connect` reads it from the
// environment — the app never learns which tier provisioned it, which is the
// whole point of the design).
//
// What it deliberately is NOT
// ---------------------------
//   - It never falls back to SQLite. A silent fallback would reintroduce the
//     dialect drift the feature exists to remove.
//   - It never restarts a dead postmaster in place. A postmaster that exits is
//     a failing disk, an OOM kill or a corrupt cluster; retrying in the app
//     hides all three until they are an outage. The app exits non-zero and
//     lets whatever supervises the app restart the tree.
//   - It never resolves `--embed` against an operator-supplied DSN. See
//     embeddedDSNConflict below: there is no defensible precedence.
//
// Three of the failure modes here (double start, a stale `postmaster.pid`, a
// major-version mismatch) are properties of pointing any postmaster at a data
// directory rather than anything to do with `--embed`, and P2 already closed
// them for `sky db start` in rust/crates/sky/src/db_cluster.rs. The logic below
// mirrors that implementation — including its two-legged liveness check —
// because two subtly different answers to "is this cluster running" is how a
// second postmaster ends up opening the same data directory.

import (
	"database/sql"
	"fmt"
	"log"

	"sky-app/rt/periodic"

	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// ---------------------------------------------------------------------------
// The entry point — what generated code calls
// ---------------------------------------------------------------------------

// MaybeStartEmbeddedPostgres brings up the app's own PostgreSQL when the binary
// was asked for one, and returns immediately when it was not.
//
// Generated `main` calls it before the app boots and defers StopEmbeddedPostgres:
//
//	func main() {
//	    rt.MaybeStartEmbeddedPostgres()
//	    defer rt.StopEmbeddedPostgres()
//	    ...
//	}
//
// From `main`, NOT from an `init()`, and that is load-bearing rather than
// stylistic. `sky.toml`'s `[database] path` and `[database] url` do not reach
// the runtime as configuration — the compiler emits them as
// `rt.SetSkyDefault("DB_PATH", …)` inside the generated prologue `init()`
// (`rust/crates/lower/src/lower.rs`, `prologue_init`). Go runs every `init()`
// before `main`, so calling from `main` is what makes those two keys visible to
// the ambiguity check below, and it is why checking two environment variables
// here covers all four sources `sky run` checks in `db_cluster.rs`. Called from
// a second `init()`, the two would run in filename order and an app configured
// with `[database] path` alongside `--embed` could start the cluster anyway.
//
// The not-requested path costs one scan of os.Args and one getenv: no
// filesystem probe, no bundle read, no process spawn. A binary built without
// `--embed` has no bundle set at all and reports that fact rather than
// half-working.
//
// On any failure it prints the reason and exits non-zero. There is no degraded
// mode: an app that was told to run its own database and could not has nothing
// useful left to do, and continuing would mean connecting somewhere the
// operator did not choose.
func MaybeStartEmbeddedPostgres() {
	if !embedRequested(os.Args, osEnv) {
		return
	}
	if err := startEmbeddedPostgres(); err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		os.Exit(1)
	}
}

// StopEmbeddedPostgres shuts the cluster down and is safe to call any number of
// times, from any goroutine, whether or not a cluster was ever started.
//
// It exists for the ordinary exit path (generated `main`'s defer). The SIGTERM
// path does not need it: the supervisor installs its own handler so it can run
// the phases in the required order, which a defer cannot express.
//
// A cluster this process ADOPTED is not stopped — see stopPostgres.
func StopEmbeddedPostgres() {
	if s := activeSupervisor(); s != nil {
		s.stopPostgres()
	}
}

// ExitProcess ends the process the way generated `main` would have, for the code
// paths that exit from underneath its defers.
//
// `os.Exit` does not run deferred functions, so any `os.Exit` reached from
// inside `main` skips `defer rt.StopEmbeddedPostgres()` and leaves the embedded
// postmaster running with nothing left to stop it. That is not hypothetical:
// `SKY_DB_OP=migrate ./app --embed` is a one-shot deploy step whose whole job is
// to exit, and Db_migrateApply exits the process itself. Without this, the
// deploy sequence "migrate, then serve" would orphan a postmaster on the first
// half and adopt it forever on the second.
//
// A no-op-plus-exit when no cluster was ever started, which is every ordinary
// build.
func ExitProcess(code int) {
	StopEmbeddedPostgres()
	os.Exit(code)
}

// fatalfAndExit is `log.Fatalf` with the database stopped first: log the line,
// then leave through ExitProcess.
//
// It exists because `log.Fatalf` is `log.Output` followed by `os.Exit(1)`, and
// the second half is the one that matters here — it runs no defers, so it skips
// generated main's `defer rt.StopEmbeddedPostgres()` exactly as a bare os.Exit
// does. The two callers are the fail-loud branches that refuse to start when a
// configured session or jobs store is unreachable in production, and their
// timing is the worst possible: `MaybeStartEmbeddedPostgres` has already run, so
// the postmaster this process just started is orphaned on the way out. The next
// boot adopts it and, by the ownership rule in stopPostgres, never stops it
// either — so a production `--embed` app pointed at an unreachable store leaked
// a cluster on EVERY boot attempt, which is to say on every restart of a crash
// loop.
//
// Never returns.
func fatalfAndExit(format string, v ...any) {
	log.Printf(format, v...)
	ExitProcess(1)
}

// EmbeddedPostgresActive reports whether this process is supervising a cluster.
// Used by the app-shape signal handlers to decide whether the supervisor owns
// the shutdown sequence.
func EmbeddedPostgresActive() bool { return activeSupervisor() != nil }

var (
	supervisorMu sync.Mutex
	supervisor   *pgSupervisor
)

func activeSupervisor() *pgSupervisor {
	supervisorMu.Lock()
	defer supervisorMu.Unlock()
	return supervisor
}

func setActiveSupervisor(s *pgSupervisor) {
	supervisorMu.Lock()
	supervisor = s
	supervisorMu.Unlock()
}

// ---------------------------------------------------------------------------
// Request detection and configuration
// ---------------------------------------------------------------------------

// embedRequested reports whether this invocation asked for an embedded cluster.
//
// Both spellings exist because both callers exist: a human types `--embed`, and
// a container image or a systemd unit sets an environment variable rather than
// rewriting an ENTRYPOINT. Argument scanning stops at `--` so an app that
// forwards its own arguments is not second-guessed.
func embedRequested(args []string, env envFunc) bool {
	for i := 1; i < len(args); i++ {
		if args[i] == "--" {
			break
		}
		if args[i] == "--embed" {
			return true
		}
	}
	switch strings.ToLower(strings.TrimSpace(env.get("SKY_EMBED_POSTGRES"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// envFunc is the environment seam. It reports "set" separately from "value"
// because the two cases differ in what they mean: `SKY_DATA_DIR=` is a deploy
// script that failed to interpolate a variable, and defaulting it to
// `$PWD/.skydata` would put production data wherever the process happened to
// be started from.
type envFunc func(name string) (string, bool)

func (e envFunc) get(name string) string {
	v, _ := e(name)
	return v
}

// osEnv is the production environment seam.
func osEnv(name string) (string, bool) { return os.LookupEnv(name) }

// embedConfig is everything the supervisor needs, resolved before anything is
// spawned so a bad configuration fails at startup rather than half-way through
// an initdb.
type embedConfig struct {
	dataRoot  string // --data-dir / SKY_DATA_DIR / <cwd>/.skydata
	dataDir   string // dataRoot/pg — the PostgreSQL data directory proper
	runtimeIn string // dataRoot/runtime — where the bundle is extracted
	socketDir string // short hashed path OUTSIDE dataRoot (sockaddr_un is 107 bytes)
	logPath   string // dataRoot/pg.log
}

// resolveEmbedConfig derives the configuration from arguments and environment.
//
// `cwd` and `env` are parameters rather than ambient reads so every rule here —
// including the temp-directory refusal, which is the one with real consequences
// — is testable without touching process state.
func resolveEmbedConfig(args []string, env envFunc, cwd string) (embedConfig, error) {
	root, err := dataRootFrom(args, env, cwd)
	if err != nil {
		return embedConfig{}, err
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return embedConfig{}, fmt.Errorf("sky --embed: cannot resolve the data directory %q: %w", root, err)
	}
	abs = filepath.Clean(abs)
	if err := rejectTempDataDir(abs, env); err != nil {
		return embedConfig{}, err
	}
	return embedConfig{
		dataRoot:  abs,
		dataDir:   filepath.Join(abs, "pg"),
		runtimeIn: filepath.Join(abs, "runtime"),
		socketDir: socketDirFor(filepath.Join(abs, "pg"), env.get("XDG_RUNTIME_DIR"), "/tmp"),
		logPath:   filepath.Join(abs, "pg.log"),
	}, nil
}

// dataRootFrom reads `--data-dir <path>` / `--data-dir=<path>`, then
// SKY_DATA_DIR, then defaults to `.skydata` beside the working directory.
//
// An explicitly empty value is an error rather than a fall-through to the
// default: `--data-dir ""` and `SKY_DATA_DIR=` are what a broken deploy script
// produces, and silently writing production data to `$PWD/.skydata` because a
// shell variable was unset is precisely the outcome this feature must not have.
func dataRootFrom(args []string, env envFunc, cwd string) (string, error) {
	for i := 1; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		if a == "--data-dir" {
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" {
				return "", fmt.Errorf("sky --embed: --data-dir needs a path")
			}
			return args[i+1], nil
		}
		if v, ok := strings.CutPrefix(a, "--data-dir="); ok {
			if strings.TrimSpace(v) == "" {
				return "", fmt.Errorf("sky --embed: --data-dir needs a path")
			}
			return v, nil
		}
	}
	if v, set := env("SKY_DATA_DIR"); set {
		if strings.TrimSpace(v) == "" {
			return "", fmt.Errorf("sky --embed: SKY_DATA_DIR is set but empty; " +
				"give it a path or unset it")
		}
		return v, nil
	}
	return filepath.Join(cwd, ".skydata"), nil
}

// tempDirRoots are the directories an operating system is entitled to empty.
var tempDirRoots = []string{"/tmp", "/var/tmp", "/private/tmp", "/private/var/tmp", "/dev/shm", "/var/folders", "/private/var/folders"}

// rejectTempDataDir refuses a data directory the system may delete.
//
// This is not fussiness. `--embed` means the app's only copy of its data lives
// in this directory; macOS empties `/var/folders` on its own schedule, Linux
// distributions clean `/tmp` on boot or on a timer, and a container's
// `PrivateTmp` is gone with the container. A cluster that silently reinitialises
// itself looks exactly like an app that lost every row, and by then the
// evidence has been deleted too.
func rejectTempDataDir(dir string, env envFunc) error {
	roots := append([]string(nil), tempDirRoots...)
	if t := strings.TrimSpace(env.get("TMPDIR")); t != "" {
		if abs, err := filepath.Abs(t); err == nil {
			roots = append(roots, filepath.Clean(abs))
		}
	}
	for _, r := range roots {
		if dir == r || strings.HasPrefix(dir, r+string(filepath.Separator)) {
			return fmt.Errorf(
				"sky --embed: refusing to keep a database in a temporary directory.\n"+
					"\n"+
					"  data directory: %s\n"+
					"  which is under: %s\n"+
					"\n"+
					"Under --embed this directory holds the app's only copy of its data, and\n"+
					"the system is entitled to empty it — on reboot, on a timer, or when the\n"+
					"container goes away. The app would come back up with an empty database\n"+
					"and no error to explain it.\n"+
					"\n"+
					"Point it somewhere durable:\n"+
					"  ./app --embed --data-dir /var/lib/myapp\n"+
					"  SKY_DATA_DIR=/var/lib/myapp ./app --embed", dir, r)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// `--embed` with an explicit DSN
// ---------------------------------------------------------------------------

type dsnSource struct {
	name  string
	value string
}

// embeddedDSNSources lists the environment variables that would send the app to
// a database other than the one `--embed` is about to start.
//
// These are the names the runtime ACTUALLY reads — `Db_connect` consults
// `<PREFIX>_DB_PATH` and then `DATABASE_URL` (db_auth.go), and the session,
// analytics and jobs stores consult `DATABASE_URL` too. The design document
// calls this variable `SKY_DB_URL`, which nothing in the runtime or the
// compiler has ever read; checking that name and not these two would have
// produced a guard that passes while the app quietly connects elsewhere.
//
// Two names here cover the FOUR sources `sky run` checks in P2/P4's
// `embedded_dsn_conflict`, because `sky.toml`'s `[database] path` and
// `[database] url` are both compiled into a `rt.SetSkyDefault("DB_PATH", …)`
// in the prologue `init()` (see MaybeStartEmbeddedPostgres). By the time this
// runs they ARE `<PREFIX>_DB_PATH`. The two paths therefore agree on what
// counts as a conflict, which matters more than either rule does alone.
func embeddedDSNSources(env envFunc) []dsnSource {
	return []dsnSource{
		{skyEnvName("DB_PATH"), env.get(skyEnvName("DB_PATH"))},
		{"DATABASE_URL", env.get("DATABASE_URL")},
	}
}

// embeddedDSNConflict refuses `--embed` alongside an operator-set DSN.
//
// There is no defensible precedence. Preferring the cluster means the app
// writes to local disk while its operator believes it is talking to the server
// they named — the failure that only surfaces once the data is in the wrong
// place, and by then both copies are wrong. Preferring the DSN means `--embed`
// is a flag that does nothing, on a binary that carries 77MB of PostgreSQL for
// the privilege. Stopping is the only outcome that cannot lose data.
//
// Only the first source is reported: four complaints about one mistake are
// harder to act on than one.
func embeddedDSNConflict(sources []dsnSource) error {
	for _, s := range sources {
		if strings.TrimSpace(s.value) == "" {
			continue
		}
		return fmt.Errorf(
			"sky --embed: this app was started with --embed and also carries an explicit\n"+
				"connection string:\n"+
				"\n"+
				"  %s = %s\n"+
				"\n"+
				"Sky will not choose between them. Starting the embedded cluster anyway would\n"+
				"write to a local data directory while you believed the app was talking to the\n"+
				"database you named.\n"+
				"\n"+
				"Pick one:\n"+
				"  • use the connection string: drop --embed (and SKY_EMBED_POSTGRES)\n"+
				"  • use the embedded cluster:  unset %s — and if it came from the\n"+
				"    project's sky.toml rather than the environment, remove\n"+
				"    [database] path / [database] url, which are compiled into it",
			s.name, s.value, s.name)
	}
	return nil
}

// ---------------------------------------------------------------------------
// The supervisor
// ---------------------------------------------------------------------------

type pgSupervisor struct {
	cfg  embedConfig
	bins pgBins
	dsn  string

	cmd *exec.Cmd
	// adopted is set when a postmaster was already serving the data directory —
	// the orphan a SIGKILLed app leaves behind. It is adopted rather than
	// refused, because refusing to boot is a worse answer than continuing with
	// the database that is demonstrably up and serving the right data
	// directory.
	adopted bool

	stopping atomic.Bool
	stopOnce sync.Once

	// childState is how the postmaster's exit reaches the boot goroutine.
	//
	// waitReady used to consult `s.cmd.ProcessState` directly, which is a data
	// race and an `os/exec` contract violation in one: `Cmd.Wait` WRITES that
	// field, watchChild is the goroutine that calls Wait, and the package
	// documents ProcessState as invalid until Wait returns. `go test -race`
	// reports it on every live boot. Publishing through an atomic makes the
	// value both safe to read and actually valid when it is read.
	childState atomic.Pointer[os.ProcessState]

	// stopFn replaces the leaf action of the last shutdown phase. It exists so
	// a test can observe WHEN the database is stopped relative to the other
	// phases without a live cluster. The sequencer narrating its own phases
	// would make an ordering test tautological, so the test observes the real
	// leaves instead (a registered accept-stopper, a registered shutdown hook,
	// and this).
	stopFn func() error

	// exitFn replaces the OTHER leaf of the signal path: the os.Exit that ends
	// the process once the phases are done.
	//
	// Without it the ordering gate could only ever call s.shutdown directly,
	// leaving installSignalHandler — the goroutine every real SIGTERM actually
	// goes through — covered by nothing. A mistake made in the handler rather
	// than in shutdown (stopping the database before delegating, say) is
	// invisible to a test that calls shutdown itself, and it is the handler
	// that runs on every `kubectl rollout`, Cloud Run revision swap and
	// `systemctl restart`. With this seam the gate sends a real signal and
	// watches the real goroutine run.
	exitFn func(int)

	// readyFn replaces the last leaf of the START path: "did the cluster begin
	// accepting connections". It exists so the boot-failed-after-spawn case can
	// be driven with a REAL postmaster running.
	//
	// That case cannot be reached any other way in a test. It needs a live
	// postmaster that does not become ready inside pgReadyTimeout — a minute of
	// waiting, and then only if the machine can be made slow enough on cue —
	// and the property under test (nothing is left running) is precisely about
	// the process that IS running. Shortening the budget instead would make the
	// gate a race against how fast this machine starts PostgreSQL.
	readyFn func(time.Duration) error

	// sigCh is the handler's notify channel, kept so a test can detach the
	// handler again — signal.Notify is process-wide, and a live registration
	// left behind would make the NEXT test's signal reach a supervisor that is
	// no longer under test.
	sigCh chan os.Signal
}

func (s *pgSupervisor) ready(budget time.Duration) error {
	if s.readyFn != nil {
		return s.readyFn(budget)
	}
	return s.waitReady(budget)
}

func (s *pgSupervisor) exit(code int) {
	if s.exitFn != nil {
		s.exitFn(code)
		return
	}
	os.Exit(code)
}

func startEmbeddedPostgres() error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("sky --embed: cannot determine the working directory: %w", err)
	}
	if err := embeddedDSNConflict(embeddedDSNSources(osEnv)); err != nil {
		return err
	}
	cfg, err := resolveEmbedConfig(os.Args, osEnv, cwd)
	if err != nil {
		return err
	}
	bins, err := discoverPgBins(cfg)
	if err != nil {
		return err
	}
	s := &pgSupervisor{cfg: cfg, bins: bins, dsn: dsnForSocketDir(cfg.socketDir)}
	if err := s.boot(); err != nil {
		return err
	}
	setActiveSupervisor(s)
	s.installSignalHandler()

	// Hand the DSN to the app's ordinary connection path. Both names are set:
	// `<PREFIX>_DB_PATH` is what `Db.connect ()` reads, and `DATABASE_URL` is
	// what the session store, the analytics store and Std.Jobs fall back to, so
	// one embedded cluster serves the whole app. Neither can have been set
	// already — embeddedDSNConflict refused that above.
	_ = os.Setenv(skyEnvName("DB_PATH"), s.dsn)
	_ = os.Setenv("DATABASE_URL", s.dsn)

	verb := "started"
	if s.adopted {
		verb = "adopted (already running)"
	}
	fmt.Fprintf(os.Stderr, "[sky.pg] embedded PostgreSQL %s %s — data %s, socket %s\n",
		s.bins.version, verb, s.cfg.dataDir, s.cfg.socketDir)
	return nil
}

// boot brings the cluster to "accepting connections", and takes back down
// anything it started if it cannot.
//
// The failure that matters happens AFTER spawn: the postmaster is running and
// then readiness never arrives — a cluster replaying a long WAL past the
// budget, a postgresql.conf an operator edited into refusing connections, a
// full disk. The postmaster is in its own process group, so it survives the
// non-zero exit MaybeStartEmbeddedPostgres is about to take; nothing has been
// registered yet, so StopEmbeddedPostgres finds no supervisor and main's defer
// has nothing to do. The process that could NOT talk to its database therefore
// leaves it running — and the operator's retry adopts that postmaster and,
// correctly per the ownership rule in stopPostgres, never stops it either. One
// failed start becomes a postmaster that outlives every subsequent run.
//
// Adopted clusters are exempt, because stopPostgres owns that distinction: a
// boot that failed while adopting must leave the other process's cluster alone.
func (s *pgSupervisor) boot() error {
	if err := s.bringUp(); err != nil {
		s.stopPostgres()
		return err
	}
	return nil
}

// bringUp is boot without the failure cleanup — the sequence itself.
func (s *pgSupervisor) bringUp() error {
	// Already serving? Adopt it. This is the SIGKILL-of-the-app case: the
	// postmaster is in its own process group and outlived its parent, and it is
	// the correct database with the correct data directory.
	if pid, ok := runningPostmaster(s.cfg.dataDir); ok {
		s.adopted = true
		s.watchAdopted(pid)
		return s.ready(pgReadyTimeout)
	}

	switch st, err := inspectDataDir(s.cfg.dataDir); {
	case err != nil:
		return err
	case st == dataDirInitialised:
		if err := checkMajorMatches(s.cfg.dataDir, s.bins); err != nil {
			return err
		}
		// Initialised, nothing serving it, and a pid file still present → the
		// postmaster was killed rather than stopped. Clearing it is what turns
		// the next start from a refusal into a start.
		if err := clearStalePidfile(s.cfg.dataDir); err != nil {
			return err
		}
	case st == dataDirRubble:
		return fmt.Errorf(
			"sky --embed: %s exists but is not a PostgreSQL data directory (no PG_VERSION).\n"+
				"A previous initdb probably failed part-way. Remove it and retry:\n"+
				"  rm -rf %s", s.cfg.dataDir, s.cfg.dataDir)
	default:
		if err := s.initCluster(); err != nil {
			return err
		}
	}

	if err := prepareSocketDir(s.cfg.socketDir); err != nil {
		return err
	}
	// Re-tune for the machine THIS boot is running on, immediately before the
	// postmaster starts.
	//
	// The managed block used to be written only by initCluster, i.e. once, at
	// initdb — while the connection pools re-read the CPU count on every
	// start. Resize a host from 2 vCPU to 8 and pool demand goes 14 → 56
	// while `max_connections` stays sized for the old machine; restore a data
	// directory onto a different host and the same divergence happens with no
	// warning at all.
	//
	// Here is the right place for it because `max_connections` and
	// `shared_buffers` need a RESTART rather than a reload, and a restart is
	// exactly what is about to happen — so the new values take effect on this
	// very boot with no second restart and no reload plumbing. The block is
	// marked and idempotent, so when nothing has changed this rewrites
	// nothing (see ensureSkyConf).
	if err := writeTunedConf(s.cfg.dataDir, detectMachine()); err != nil {
		return err
	}
	if err := s.spawn(); err != nil {
		return err
	}
	return s.ready(pgReadyTimeout)
}

const pgReadyTimeout = 60 * time.Second

// initCluster runs initdb and writes the tuned configuration.
func (s *pgSupervisor) initCluster() error {
	if err := os.MkdirAll(filepath.Dir(s.cfg.dataDir), 0o700); err != nil {
		return fmt.Errorf("sky --embed: cannot create %s: %w", filepath.Dir(s.cfg.dataDir), err)
	}
	fmt.Fprintf(os.Stderr, "[sky.pg] initialising a PostgreSQL %d cluster in %s\n",
		s.bins.major, s.cfg.dataDir)
	cmd := exec.Command(s.bins.tool("initdb"),
		"-D", s.cfg.dataDir,
		"--encoding=UTF8", "--locale=C",
		// The socket is the access control: a 0700 directory owned by this
		// user. Host authentication is rejected outright as a second lock on a
		// door `listen_addresses = ''` has already bricked up.
		"--auth-local=trust", "--auth-host=reject",
	)
	cmd.Env = s.bins.env()
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Leave no half-written data directory: the next run would diagnose it
		// as "not a PostgreSQL data directory" and send the reader after the
		// wrong bug.
		_ = os.RemoveAll(s.cfg.dataDir)
		return fmt.Errorf("sky --embed: initdb failed:\n%s", strings.TrimSpace(string(out)))
	}
	return writeTunedConf(s.cfg.dataDir, detectMachine())
}

// spawn starts the postmaster as a direct child in its own process group.
//
// Directly, not through `pg_ctl start`, for two reasons. The app must be able
// to observe the postmaster dying — `pg_ctl` daemonises, leaving the app with a
// pid to poll instead of a child to wait for, and polling cannot distinguish a
// dead postmaster from a recycled pid. And `pg_ctl start` interpolates its
// arguments into a string it hands to `/bin/sh` (`start_postmaster` in
// pg_ctl.c); exec'ing the postmaster directly removes the shell from the start
// path entirely.
//
// Its own process group so that a Ctrl-C in a terminal — which the tty delivers
// to the whole foreground group — reaches the app's handler and not the
// postmaster, and so the group can be signalled as a unit if the postmaster
// ever has to be taken down hard.
func (s *pgSupervisor) spawn() error {
	log, err := os.OpenFile(s.cfg.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("sky --embed: cannot open the PostgreSQL log %s: %w", s.cfg.logPath, err)
	}
	defer log.Close()

	cmd := exec.Command(s.bins.tool("postgres"),
		"-D", s.cfg.dataDir,
		// -k is unix_socket_directories, passed per start rather than frozen
		// into postgresql.conf: the hashed path is re-derived from the
		// environment every boot, and a value written to the file would
		// silently go stale when XDG_RUNTIME_DIR moved.
		"-k", s.cfg.socketDir,
		"-c", "listen_addresses=",
	)
	cmd.Env = s.bins.env()
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("sky --embed: cannot start the postmaster: %w", err)
	}
	s.cmd = cmd
	go s.watchChild()
	return nil
}

// watchChild is the dead-postmaster rule: if PostgreSQL exits and we did not
// ask it to, the app exits non-zero.
//
// Restarting it here would be worse than useless. A postmaster exits because
// the disk is full, the kernel killed it, or the cluster is damaged; every one
// of those gets worse with a retry loop, and every one of them is hidden by
// one. Exiting hands the decision to whatever supervises the app, which is the
// component that can actually escalate.
func (s *pgSupervisor) watchChild() {
	if s.cmd == nil {
		return
	}
	err := s.cmd.Wait()
	// Publish the verdict for waitReady, which runs on the boot goroutine and
	// must not touch `s.cmd.ProcessState` itself — see childState.
	s.childState.Store(s.cmd.ProcessState)
	if s.stopping.Load() {
		return
	}
	fmt.Fprintf(os.Stderr,
		"[sky.pg] the embedded PostgreSQL server exited unexpectedly (%v).\n"+
			"[sky.pg] the app cannot serve without it and is exiting so its supervisor can\n"+
			"[sky.pg] restart the whole tree. The server's own log is at %s\n%s",
		err, s.cfg.logPath, logTail(s.cfg.logPath, 20))
	os.Exit(1)
}

// watchAdopted is watchChild for a postmaster this process did not spawn: there
// is no child to wait for, so its liveness is polled. The same rule applies —
// the database going away is fatal to the app.
func (s *pgSupervisor) watchAdopted(pid int) {
	// The recover is per cycle. This is a watchdog, so a silently dead
	// watchdog is the worst possible failure: the adopted postmaster could
	// then vanish and the app would carry on against a database that is gone,
	// which is exactly the state this loop exists to turn into a restart.
	stopped := make(chan struct{})
	go periodic.Every(periodic.Config{
		Name:     "pg.adopted-watchdog",
		Interval: 2 * time.Second,
		Stop:     stopped,
		Report:   periodicReport,
		Work: func(time.Time) error {
			if s.stopping.Load() {
				close(stopped)
				return nil
			}
			if !isPostgresProcess(pid) {
				fmt.Fprintf(os.Stderr,
					"[sky.pg] the adopted PostgreSQL server (pid %d) is gone; exiting so the\n"+
						"[sky.pg] app's supervisor can restart the tree.\n", pid)
				os.Exit(1)
			}
			return nil
		},
	})
}

// waitReady blocks until the cluster accepts connections, the deadline passes,
// or the postmaster dies — whichever comes first.
//
// Readiness is a connection, not a file. The socket appears before the
// postmaster finishes crash recovery, and `postmaster.pid`'s status line lags
// as well, so both would report a database that immediately refuses queries.
// `pg_isready` is preferred when the distribution has it and a real connection
// is the fallback; `psql` is not consulted, because the bundle deliberately
// does not ship it (it links GPL readline).
func (s *pgSupervisor) waitReady(budget time.Duration) error {
	deadline := time.Now().Add(budget)
	delay := 25 * time.Millisecond
	var last error
	for {
		if st := s.childState.Load(); st != nil {
			return fmt.Errorf(
				"sky --embed: the PostgreSQL server exited during startup (%s).\n%s",
				st, logTail(s.cfg.logPath, 30))
		}
		if err := s.probeReady(); err == nil {
			return nil
		} else {
			last = err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf(
				"sky --embed: PostgreSQL did not accept connections within %s (%v).\n%s",
				budget, last, logTail(s.cfg.logPath, 30))
		}
		time.Sleep(delay)
		if delay < 250*time.Millisecond {
			delay *= 2
		}
	}
}

func (s *pgSupervisor) probeReady() error {
	if ready := s.bins.tool("pg_isready"); fileExists(ready) {
		// `-d postgres` is not decoration. Without it pg_isready defaults the
		// database name to the OS user, and every single boot logs a pair of
		// `FATAL: database "<user>" does not exist` lines into the server log
		// before the app has run a query — the first thing anyone debugging a
		// real problem would find, and a red herring.
		cmd := exec.Command(ready, "-h", s.cfg.socketDir, "-d", "postgres", "-q")
		cmd.Env = s.bins.env()
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("pg_isready: %w", err)
		}
		return nil
	}
	db, err := sql.Open("pgx", s.dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	return db.Ping()
}

// ---------------------------------------------------------------------------
// Shutdown — the ordering IS the feature
// ---------------------------------------------------------------------------

// acceptStoppers is the "stop accepting new work" phase: listeners, and
// anything else whose job is to refuse arrivals. Registered LIFO for the same
// reason the shutdown hooks are — the most recently started subsystem is the
// one that may depend on the others still being up.
var (
	acceptMu       sync.Mutex
	acceptStoppers []namedStopper
)

type namedStopper struct {
	name string
	fn   func()
}

// RegisterAcceptStopper records something that stops accepting new work.
// Called by the app shapes (Sky.Http.Server, Sky.Live) with their listener.
func RegisterAcceptStopper(name string, fn func()) {
	if fn == nil {
		return
	}
	acceptMu.Lock()
	acceptStoppers = append(acceptStoppers, namedStopper{name, fn})
	acceptMu.Unlock()
}

func runAcceptStoppers() {
	acceptMu.Lock()
	list := append([]namedStopper(nil), acceptStoppers...)
	acceptStoppers = nil
	acceptMu.Unlock()
	for i := len(list) - 1; i >= 0; i-- {
		func(s namedStopper) {
			defer func() {
				if r := recover(); r != nil {
					fmt.Fprintf(os.Stderr, "[sky.pg] accept-stopper %q panicked: %v\n", s.name, r)
				}
			}()
			s.fn()
		}(list[i])
	}
}

func resetAcceptStoppersForTesting() {
	acceptMu.Lock()
	acceptStoppers = nil
	acceptMu.Unlock()
}

const embeddedDrainBudget = 8 * time.Second

// installSignalHandler makes the supervisor the owner of the termination
// sequence.
func (s *pgSupervisor) installSignalHandler() {
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	s.sigCh = ch
	go func() {
		<-ch
		s.shutdown(embeddedDrainBudget)
		s.exit(0)
	}()
}

// detachSignalHandler — TEST-ONLY. Stops delivery to this supervisor's channel
// and parks its goroutine, so a test that raised a signal does not leave a
// handler that the next one's signal would also reach.
func (s *pgSupervisor) detachSignalHandler() {
	if s.sigCh != nil {
		signal.Stop(s.sigCh)
	}
}

// shutdown runs the three phases in the one order that does not turn a routine
// deploy into a page of errors:
//
//  1. stop accepting — the listeners close, so nothing new arrives
//  2. drain          — in-flight work finishes and telemetry flushes
//  3. stop PostgreSQL — pg_ctl stop -m fast
//
// Reversing 2 and 3 is the interesting mistake, and it is an easy one to make:
// the database is "just another subsystem" and the shutdown-hook registry is
// right there. But every in-flight request holds a connection, and taking the
// database away underneath them converts a clean rollout into a burst of
// query-level errors in exactly the window an operator is watching.
//
// Phase 2 deliberately does not just call RunShutdownHooks and continue. The
// app shapes install their own signal handlers and call it too, so whichever
// goroutine arrives second finds the registry already closed and returns
// immediately — with the hooks still running. Waiting on the completion barrier
// is what makes "drain finished" true rather than merely called.
func (s *pgSupervisor) shutdown(budget time.Duration) {
	s.stopping.Store(true)

	// Phase 1 — stop accepting.
	runAcceptStoppers()

	// Phase 2 — drain, and WAIT for it, whoever started it. Then release the
	// resources the drain was using (the session store's pooled handle among
	// them) — still BEFORE the database, since those handles point AT it and a
	// pool released cleanly is a pool `pg_ctl -m fast` has nothing to roll back.
	// The listener is already closed by phase 1, so no closeListener here.
	drainAndRelease(budget, nil)

	// Phase 3 — and only now, the database.
	s.stopPostgres()
}

// stopPostgres asks the postmaster for a fast shutdown and waits for it — but
// only for a cluster this process actually started.
//
// `-m fast` (refuse new connections, roll back what is in flight, exit) rather
// than `-m smart`, which waits for every client to disconnect and would turn a
// stop into a hang behind one idle connection. Idempotent: an app that stops
// via its normal exit path and an app that stops via SIGTERM both end up here.
//
// The `adopted` early return is the ownership rule, and it is the rule the rest
// of the toolchain already keeps. `sky db start` is contracted as EXPLICIT and
// PERSISTENT — it stays up until `sky db stop` — and `sky run` honours that by
// ref-counting (`ClusterEntry.explicit` / `RunRef` in
// `rust/crates/sky/src/db_cluster.rs`): a `sky run` exit never takes down a
// cluster it did not start. Without this, `./app --embed` was the one path that
// broke the contract: it found the live postmaster, adopted it, connected, and
// then stopped it on the way out — so a developer who ran `sky db start` and
// then their own built binary got their persistent cluster taken away, silently
// and once per run.
//
// "Stop only what you started" is the whole rule, deliberately, rather than a Go
// re-implementation of the registry: see the note on ExitProcess and
// `docs/skydb/embedded-postgres.md`. It costs one orphaned postmaster in the
// case where an app is SIGKILLed and its successor adopts the leftover — which
// is exactly the state `sky db ps` / `sky db stop` exist to resolve, and a
// running database is the cheaper of the two mistakes.
func (s *pgSupervisor) stopPostgres() {
	s.stopOnce.Do(func() {
		s.stopping.Store(true)
		if s.adopted {
			fmt.Fprintf(os.Stderr,
				"[sky.pg] leaving the adopted PostgreSQL server running — this process did not\n"+
					"[sky.pg] start it. Stop it with `sky db stop`, or `pg_ctl -D %s stop`.\n",
				s.cfg.dataDir)
			return
		}
		if s.stopFn != nil {
			if err := s.stopFn(); err != nil {
				fmt.Fprintf(os.Stderr, "[sky.pg] stop failed: %v\n", err)
			}
			return
		}
		if err := s.pgCtlStop(); err != nil {
			fmt.Fprintf(os.Stderr, "[sky.pg] %v\n", err)
			s.signalPostmaster()
		}
	})
}

func (s *pgSupervisor) pgCtlStop() error {
	ctl := s.bins.tool("pg_ctl")
	if !fileExists(ctl) {
		return fmt.Errorf("pg_ctl is not available at %s", ctl)
	}
	cmd := exec.Command(ctl, "-D", s.cfg.dataDir, "-m", "fast", "-w", "-t", "30", "stop")
	cmd.Env = s.bins.env()
	out, err := cmd.CombinedOutput()
	if err != nil {
		// "server is not running" is a success for an idempotent stop.
		if strings.Contains(string(out), "not running") {
			return nil
		}
		return fmt.Errorf("pg_ctl stop failed: %v\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// signalPostmaster is the fallback when pg_ctl is unavailable or failed.
// SIGINT is PostgreSQL's own fast-shutdown signal, so this is the same request
// by a different route rather than a kill.
func (s *pgSupervisor) signalPostmaster() {
	pid := 0
	if s.cmd != nil && s.cmd.Process != nil {
		pid = s.cmd.Process.Pid
	} else if p, ok := readPostmasterPid(s.cfg.dataDir); ok {
		pid = p
	}
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(pid, syscall.SIGINT)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	// Still there: SIGQUIT is PostgreSQL's immediate shutdown (no checkpoint;
	// recovery on next start). Preferred over SIGKILL, which leaves shared
	// memory and semaphores behind.
	_ = syscall.Kill(pid, syscall.SIGQUIT)
}

// BlockIfEmbeddedShuttingDown parks a caller that has just had its listener
// closed by the supervisor's first phase.
//
// Without it the sequence has a race it cannot win: closing the listener makes
// `ListenAndServe` return, the app's main returns, and the process exits —
// taking the goroutine that was going to stop PostgreSQL with it, and leaving
// an orphaned postmaster behind. The supervisor calls os.Exit itself once the
// database is down, so parking here is not a leak: it is the process waiting
// for its own shutdown to finish.
func BlockIfEmbeddedShuttingDown() {
	s := activeSupervisor()
	if s == nil || !s.stopping.Load() {
		return
	}
	select {}
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func logTail(path string, lines int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	all := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	return fmt.Sprintf("--- %s ---\n%s\n", path, strings.Join(all, "\n"))
}
