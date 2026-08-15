package rt

// postgresql.conf for a cluster that shares a machine with the app it serves.
//
// P2's development tuning (rust/crates/sky/src/db_cluster.rs) is fixed and
// small — 32MB of shared buffers, because several idle project clusters should
// cost tens of megabytes, not hundreds. `--embed` is the other end of the same
// problem: this is production, the machine is whatever the operator gave it,
// and PostgreSQL's own defaults (128MB shared_buffers, 100 connections) are
// sized for a machine it has no way to see.
//
// So the settings are derived from detected RAM and CPU. Every one of them is a
// RESOURCE knob. Nothing here changes what a query MEANS — not fsync, not
// synchronous_commit, not wal_level — because an embedded cluster that behaved
// differently from a managed one would reintroduce, in a subtler form, exactly
// the divergence this feature exists to remove.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

func runCapture(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	return string(out), err
}

// skyConfMarker opens the managed block. Its presence is what makes writing it
// idempotent across restarts.
const skyConfMarker = "# --- sky --embed: cluster tuning (managed by sky) ---"

// skyConfEndMarker closes the managed block, so it can be REPLACED on a later
// boot rather than only appended once. Clusters initialised before this marker
// existed have an un-delimited block; see replaceManagedBlock for how its
// extent is recovered without eating an operator's own settings.
const skyConfEndMarker = "# --- end sky --embed ---"

type machine struct {
	ramBytes uint64 // 0 when RAM could not be detected
	cpus     int
}

func isDarwin() bool { return runtime.GOOS == "darwin" }

func detectMachine() machine {
	return machine{ramBytes: detectRAMBytes(), cpus: runtime.NumCPU()}
}

// fileRead is the filesystem seam for machine detection, and it exists for the
// same reason tuningFor takes a `machine` rather than reading one: the ORDER of
// the sources below is the part with consequences, and a function that reads
// ambient files can only be tested on the machine the test happens to run on.
//
// Detection had no seam at all, and the whole half was therefore untested —
// which is how the ordering below could have been reversed without anything
// noticing. See TestACgroupLimitOutranksTheHostsMemTotal.
type fileRead func(string) ([]byte, error)

// detectRAMBytes reads the memory this process may actually use, returning 0
// when it cannot.
//
// Zero is a real answer and it is handled: tuningFor falls back to a
// deliberately small profile rather than guessing large. Guessing large on a
// machine whose size is unknown is how a container with a 512MB limit gets an
// OOM kill at the first checkpoint.
func detectRAMBytes() uint64 {
	return detectRAMBytesFrom(os.ReadFile, isDarwin(), sysctlMemsize)
}

// detectRAMBytesFrom is detectRAMBytes with its three sources injected.
//
// The cgroup limit is consulted FIRST and that is the whole point of the
// function. `/proc/meminfo` is NOT namespaced: inside a memory-limited container
// it reports the HOST's total, so a 512MB container on a 64GB node reads 64GB,
// sizes shared_buffers to the 8GB clamp, and is OOM-killed at the first
// checkpoint. Reversing these two lines is a one-line edit that no cluster-level
// gate can see, because every setting it produces is individually plausible.
func detectRAMBytesFrom(read fileRead, darwin bool, sysctl func() (uint64, bool)) uint64 {
	if n := cgroupMemoryLimitFrom(read); n > 0 {
		return n
	}
	if b, err := read("/proc/meminfo"); err == nil {
		if n := parseMemTotal(string(b)); n > 0 {
			return n
		}
	}
	if darwin {
		if n, ok := sysctl(); ok {
			return n
		}
	}
	return 0
}

// sysctlMemsize is the macOS source: there is no /proc, and no cgroup either.
func sysctlMemsize() (uint64, bool) {
	out, err := runCapture("sysctl", "-n", "hw.memsize")
	if err != nil {
		return 0, false
	}
	n, err := strconv.ParseUint(strings.TrimSpace(out), 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// parseMemTotal reads `MemTotal:  16316948 kB` out of /proc/meminfo.
func parseMemTotal(meminfo string) uint64 {
	for _, line := range strings.Split(meminfo, "\n") {
		rest, ok := strings.CutPrefix(line, "MemTotal:")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			return 0
		}
		n, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			return 0
		}
		if len(fields) > 1 && strings.EqualFold(fields[1], "kB") {
			return n * 1024
		}
		return n
	}
	return 0
}

// cgroupMemoryLimitFrom reads the container memory limit, v2 then v1. "max" (v2)
// and the v1 sentinel (a number close to 2^63) both mean unlimited.
func cgroupMemoryLimitFrom(read fileRead) uint64 {
	for _, p := range []string{"/sys/fs/cgroup/memory.max", "/sys/fs/cgroup/memory/memory.limit_in_bytes"} {
		b, err := read(p)
		if err != nil {
			continue
		}
		s := strings.TrimSpace(string(b))
		if s == "max" {
			continue
		}
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil || n == 0 || n > 1<<62 {
			continue
		}
		return n
	}
	return 0
}

type confSetting struct {
	key    string
	value  string
	reason string
}

const mb = uint64(1) << 20

// tuningFor derives the managed settings. Pure, so the arithmetic — including
// the clamps that matter on very small and very large machines — is testable
// without a machine of that size.
//
// The shape of the reasoning: the app and the database share this box, so
// PostgreSQL does NOT get the 25% of RAM a dedicated server is usually given.
// It gets 15%, clamped so that a 512MB container is not handed 76MB of shared
// buffers it needs for the app, and a 512GB host is not handed 76GB it cannot
// usefully fill.
func tuningFor(m machine) []confSetting {
	cpus := m.cpus
	if cpus < 1 {
		cpus = 1
	}
	ram := m.ramBytes
	if ram == 0 {
		// Undetectable RAM → the small profile. Under-configuring costs
		// throughput; over-configuring costs the process.
		ram = 1024 * mb
	}

	shared := clampBytes(ram*15/100, 32*mb, 8192*mb)
	effectiveCache := clampBytes(ram*40/100, 128*mb, 32768*mb)
	maintenance := clampBytes(ram*5/100, 32*mb, 1024*mb)

	maxConn := embeddedMaxConnections(cpus)

	// work_mem is per sort/hash NODE, not per connection: the worst case is
	// several nodes in each of max_connections sessions. Budgeting a quarter of
	// RAM across that ceiling keeps the worst case survivable.
	work := clampBytes(ram/4/uint64(maxConn), 4*mb, 64*mb)

	workers := clampInt(cpus, 2, 16)
	parallel := clampInt(cpus/2, 1, 8)
	autovac := clampInt(cpus/4, 1, 4)

	return []confSetting{
		{"listen_addresses", "''", "no TCP listener at all: the app talks over a 0700 unix socket, and a port would expose the database to the network by accident"},
		{"shared_buffers", memUnit(shared), "15% of RAM, not the 25% a dedicated server gets — the app is on this machine too"},
		{"effective_cache_size", memUnit(effectiveCache), "a planner hint about the OS page cache; costs no memory"},
		{"maintenance_work_mem", memUnit(maintenance), "vacuum and index builds; one or two at a time, so it can be larger than work_mem"},
		{"work_mem", memUnit(work), "per sort/hash node, so budgeted across max_connections rather than per machine"},
		{"max_connections", strconv.Itoa(maxConn), "covers every pool this process opens (app + analytics + sessions + telemetry), twice over for restart overlap, plus the reserved superuser slots"},
		{"max_worker_processes", strconv.Itoa(workers), "one per CPU"},
		{"max_parallel_workers", strconv.Itoa(workers), "cannot exceed max_worker_processes"},
		{"max_parallel_workers_per_gather", strconv.Itoa(parallel), "half the CPUs: the app needs the other half"},
		{"autovacuum_max_workers", strconv.Itoa(autovac), "autovacuum competes with the app for the same cores"},
		{"max_wal_size", memUnit(clampBytes(ram/8, 512*mb, 4096*mb)), "checkpoint spacing; larger means fewer, bigger checkpoints"},
		{"min_wal_size", memUnit(clampBytes(ram/32, 128*mb, 1024*mb)), "keeps recycled WAL segments rather than re-creating them"},
	}
}

// PostgreSQL's own reservations, which come off the top of max_connections
// before an app gets any.
const (
	// pgSuperuserReserved is `superuser_reserved_connections`, whose default
	// is 3. Those slots are NOT available to an ordinary role, so a cluster
	// with `max_connections = 52` can serve 49 app connections. Leaving them
	// out of the arithmetic is a three-connection error in the direction that
	// produces an outage.
	pgSuperuserReserved = 3

	// pgOperatorHeadroom keeps a few slots for the human: a psql session, a
	// backup, a migration, a monitoring agent. Without it the first thing an
	// operator does when the app is struggling — connect and look — is the
	// thing that cannot be done.
	pgOperatorHeadroom = 5

	// pgRestartOverlapFactor covers the window in which TWO copies of the app
	// hold pools against this cluster.
	//
	// That window is not exotic, it is every restart: `sky watch` rebuilding
	// and relaunching, a rolling deploy bringing the new process up before
	// the old one has finished draining, a supervisor restarting a crashed
	// app while its connections are still being reaped. Sizing for exactly
	// one process means every restart under load is a `too many clients`
	// incident, and the arithmetic that produced it looks correct in
	// isolation.
	pgRestartOverlapFactor = 2

	// pgMaxConnectionsFloor / Ceiling bound the result. The floor keeps a
	// tiny machine usable at all; the ceiling stops a very large host from
	// being handed a number whose per-backend memory is no longer a rounding
	// error.
	pgMaxConnectionsFloor   = 25
	pgMaxConnectionsCeiling = 200
)

// embeddedMaxConnections sizes the embedded cluster's `max_connections` from
// what the process this cluster serves will actually demand.
//
// # The defect this replaces
//
// It used to be `clampInt(4*cpus+20, 25, 200)`, under a comment reasoning
// about "the app's own pool". The process opens FOUR pools, not one — the
// app's plus analytics, sessions and telemetry — and the aux pools are a
// quarter-share each, clamped 2–8. Counting only the app's pool left the
// cluster short across a whole band of ordinary machines:
//
//	cpus  old max_conn  demand  usable (−3 reserved)  verdict
//	   6            44      42                    41  EXHAUSTS ITS OWN DB
//	   7            48      49                    45  EXHAUSTS ITS OWN DB
//	   8            52      56                    49  EXHAUSTS ITS OWN DB
//	   9            56      56                    53  EXHAUSTS ITS OWN DB
//
// Eight cores is the most common instance size there is. Under load the app
// exhausted the database it had just started, and the user had configured
// nothing to deserve it.
//
// The demand is now read from `dbProcessConnectionDemand`, which is the same
// function the pools themselves are sized by, so the two cannot drift. If a
// fifth pool is added, `dbAuxPoolConsumers` grows and this number follows.
func embeddedMaxConnections(cpus int) int {
	// The embedded cluster serves a long-lived process on this machine; the
	// serverless sizing is for a platform that runs many small instances and
	// does not use an embedded cluster at all.
	demand := dbProcessConnectionDemand(cpus, false)
	n := demand*pgRestartOverlapFactor + pgSuperuserReserved + pgOperatorHeadroom
	return clampInt(n, pgMaxConnectionsFloor, pgMaxConnectionsCeiling)
}

func clampBytes(v, lo, hi uint64) uint64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// clampInt is db_pool.go's — the same clamp the app's own pool sizing uses.

// memUnit renders bytes as PostgreSQL's own MB/GB units, rounded DOWN to a
// whole unit. Rounding down matters: rounding 15% of a 512MB container up to
// the next unit hands PostgreSQL memory the app is already using.
func memUnit(b uint64) string {
	if b >= 1024*mb && b%(1024*mb) == 0 {
		return fmt.Sprintf("%dGB", b/(1024*mb))
	}
	return fmt.Sprintf("%dMB", b/mb)
}

// renderConfBlock is the managed block appended to postgresql.conf. Each line
// carries its reason, because the next person to read this file will be
// deciding whether to override it.
func renderConfBlock(m machine) string {
	var b strings.Builder
	b.WriteString("\n" + skyConfMarker + "\n")
	fmt.Fprintf(&b, "# Sized for %s of RAM and %d CPUs, shared with the app this database serves.\n",
		humanRAM(m.ramBytes), max(m.cpus, 1))
	b.WriteString("# Resource sizing only — nothing here changes what a query means, so an\n")
	b.WriteString("# embedded cluster stays a faithful rehearsal of a managed one.\n")
	b.WriteString("# This block is REGENERATED on every start, from the machine detected on\n")
	b.WriteString("# that start, so the cluster follows the host when it is resized. Edits\n")
	b.WriteString("# INSIDE the block are overwritten; put your own settings OUTSIDE it and\n")
	b.WriteString("# they are preserved. PostgreSQL takes the last occurrence of a setting,\n")
	b.WriteString("# so anything you write after the end marker wins.\n")
	for _, s := range tuningFor(m) {
		fmt.Fprintf(&b, "%s = %s  # %s\n", s.key, s.value, s.reason)
	}
	b.WriteString(skyConfEndMarker + "\n")
	return b.String()
}

func humanRAM(b uint64) string {
	if b == 0 {
		return "an undetectable amount"
	}
	if b >= 1024*mb {
		return fmt.Sprintf("%.1fGB", float64(b)/float64(1024*mb))
	}
	return fmt.Sprintf("%dMB", b/mb)
}

// ensureSkyConf REPLACES the managed block, or appends it when the file has
// none. Returns the new contents and whether anything changed, so a restart
// that needs no retune does not rewrite the file at all.
//
// # Why this replaces rather than appends-once
//
// It used to return `(conf, false)` the moment the marker was present, and it
// was called only from `initCluster`. The managed block was therefore written
// ONCE, at initdb, and frozen at whatever the machine was on the day the data
// directory was created — while the connection pools call `runtime.NumCPU()`
// on every boot.
//
// The two diverge on exactly the event a user expects to help. Resize a host
// from 2 vCPU to 8: pool demand goes from 14 to 56 at the next start, and
// `max_connections` stays sized for the 2-vCPU machine. The app strangles
// itself on the upgrade. Restoring a data directory onto a different host does
// the same thing with no warning at all, and a changed container memory limit
// leaves `shared_buffers` sized for the old one.
//
// Vertical scaling on one server is a first-class use of an embedded cluster,
// so this is the main path, not an edge.
func ensureSkyConf(conf, block string) (string, bool) {
	out := replaceManagedBlock(conf, block)
	return out, out != conf
}

// replaceManagedBlock swaps the managed block for a freshly rendered one,
// leaving everything an operator wrote outside it untouched.
//
// # Finding the end of the block
//
// New blocks are delimited (`skyConfMarker` … `skyConfEndMarker`). Blocks
// written before the end marker existed are not, and they ran to the end of
// the file — so for those the extent is inferred instead: from the start
// marker, consume comments, blanks and assignments to keys this file MANAGES,
// and stop at the first line that is none of those.
//
// That inference matters. Treating an un-delimited block as "everything to
// EOF" would silently delete any setting an operator appended after it, which
// is the one edit the header comment invites them to make.
func replaceManagedBlock(conf, block string) string {
	start := strings.Index(conf, skyConfMarker)
	if start < 0 {
		if conf != "" && !strings.HasSuffix(conf, "\n") {
			conf += "\n"
		}
		return conf + block
	}
	head := conf[:start]
	rest := conf[start:]

	if i := strings.Index(rest, skyConfEndMarker); i >= 0 {
		tail := rest[i+len(skyConfEndMarker):]
		return head + strings.TrimPrefix(block, "\n") + strings.TrimPrefix(tail, "\n")
	}

	// Legacy, un-delimited block: consume only what we recognise as ours.
	managed := map[string]bool{}
	for _, s := range tuningFor(machine{}) {
		managed[s.key] = true
	}
	lines := strings.Split(rest, "\n")
	end := 0
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			end = i + 1
			continue
		}
		key := strings.TrimSpace(strings.SplitN(t, "=", 2)[0])
		if managed[key] {
			end = i + 1
			continue
		}
		break
	}
	tail := strings.Join(lines[end:], "\n")
	return head + strings.TrimPrefix(block, "\n") + tail
}

// writeTunedConf renders the managed block for the machine it is given and
// writes it, when it differs from what is already there.
func writeTunedConf(dataDir string, m machine) error {
	path := filepath.Join(dataDir, "postgresql.conf")
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("sky --embed: cannot read %s: %w", path, err)
	}
	tuned, changed := ensureSkyConf(string(b), renderConfBlock(m))
	if !changed {
		return nil
	}
	if err := os.WriteFile(path, []byte(tuned), 0o600); err != nil {
		return fmt.Errorf("sky --embed: cannot write %s: %w", path, err)
	}
	return nil
}
