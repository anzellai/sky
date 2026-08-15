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

	// max_connections must cover the app's own pool with room to spare. P1
	// sizes that pool at 4 connections per CPU clamped to 4–32 on a VM
	// (db_pool.go), so 4×CPU+20 keeps the app's ceiling below the server's
	// even before autovacuum workers and a maintenance session are counted.
	maxConn := clampInt(4*cpus+20, 25, 200)

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
		{"max_connections", strconv.Itoa(maxConn), "above the app's own pool ceiling (4 per CPU, clamped 4-32) with room for maintenance"},
		{"max_worker_processes", strconv.Itoa(workers), "one per CPU"},
		{"max_parallel_workers", strconv.Itoa(workers), "cannot exceed max_worker_processes"},
		{"max_parallel_workers_per_gather", strconv.Itoa(parallel), "half the CPUs: the app needs the other half"},
		{"autovacuum_max_workers", strconv.Itoa(autovac), "autovacuum competes with the app for the same cores"},
		{"max_wal_size", memUnit(clampBytes(ram/8, 512*mb, 4096*mb)), "checkpoint spacing; larger means fewer, bigger checkpoints"},
		{"min_wal_size", memUnit(clampBytes(ram/32, 128*mb, 1024*mb)), "keeps recycled WAL segments rather than re-creating them"},
	}
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
	b.WriteString("# Edits below this marker are preserved; sky only appends the block once.\n")
	for _, s := range tuningFor(m) {
		fmt.Fprintf(&b, "%s = %s  # %s\n", s.key, s.value, s.reason)
	}
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

// ensureSkyConf appends the managed block unless it is already present.
// Returns the new contents and whether anything changed, so a restart neither
// duplicates settings nor grows the file without bound.
func ensureSkyConf(conf, block string) (string, bool) {
	if strings.Contains(conf, skyConfMarker) {
		return conf, false
	}
	return conf + block, true
}

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
