package rt

// Gates for the generated postgresql.conf.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func settingsOf(m machine) map[string]string {
	out := map[string]string{}
	for _, s := range tuningFor(m) {
		out[s.key] = s.value
	}
	return out
}

func mbOf(t *testing.T, v string) uint64 {
	t.Helper()
	switch {
	case strings.HasSuffix(v, "GB"):
		n, err := strconv.ParseUint(strings.TrimSuffix(v, "GB"), 10, 64)
		if err != nil {
			t.Fatalf("%q: %v", v, err)
		}
		return n * 1024
	case strings.HasSuffix(v, "MB"):
		n, err := strconv.ParseUint(strings.TrimSuffix(v, "MB"), 10, 64)
		if err != nil {
			t.Fatalf("%q: %v", v, err)
		}
		return n
	}
	t.Fatalf("%q has no unit", v)
	return 0
}

// The app and the database share this machine. PostgreSQL's own default
// (128MB, allocated up front) is sized for neither a 512MB container nor a
// 64GB host, which is the entire reason this is derived rather than fixed.
func TestSharedBuffersScaleWithRAMAndLeaveRoomForTheApp(t *testing.T) {
	cases := []struct {
		name    string
		ram     uint64
		cpus    int
		wantMin uint64 // MB
		wantMax uint64 // MB
	}{
		{"512MB container", 512 * mb, 1, 32, 96},
		{"2GB VM", 2048 * mb, 2, 200, 400},
		{"16GB laptop", 16384 * mb, 8, 2000, 2600},
		{"512GB host", 512 * 1024 * mb, 64, 8192, 8192}, // clamped
		{"undetectable", 0, 4, 32, 200},
	}
	for _, c := range cases {
		s := settingsOf(machine{ramBytes: c.ram, cpus: c.cpus})
		got := mbOf(t, s["shared_buffers"])
		if got < c.wantMin || got > c.wantMax {
			t.Errorf("%s: shared_buffers = %s (%dMB), want %d–%dMB", c.name, s["shared_buffers"], got, c.wantMin, c.wantMax)
		}
		if c.ram > 0 && got*mb > c.ram/2 {
			t.Errorf("%s: shared_buffers took more than half the machine", c.name)
		}
	}
}

// The server's connection ceiling has to be above the app's own pool ceiling —
// P1 sizes that at 4 per CPU clamped 4–32 (db_pool.go) — or the app exhausts
// the database at exactly the moment it is busiest.
func TestMaxConnectionsClearsTheAppsOwnPoolCeiling(t *testing.T) {
	for _, cpus := range []int{1, 2, 4, 8, 16, 64} {
		s := settingsOf(machine{ramBytes: 8192 * mb, cpus: cpus})
		maxConn, err := strconv.Atoi(s["max_connections"])
		if err != nil {
			t.Fatal(err)
		}
		appPool := clampInt(4*cpus, 4, 32)
		if maxConn <= appPool {
			t.Errorf("%d CPUs: max_connections=%d does not clear the app's pool of %d", cpus, maxConn, appPool)
		}
		if maxConn > 200 {
			t.Errorf("%d CPUs: max_connections=%d is above the clamp", cpus, maxConn)
		}
	}
}

// work_mem is per sort/hash NODE. Multiplied by max_connections it must not
// promise memory the machine does not have.
func TestWorkMemTimesMaxConnectionsStaysWithinTheMachine(t *testing.T) {
	for _, ram := range []uint64{512 * mb, 2048 * mb, 16384 * mb, 131072 * mb} {
		s := settingsOf(machine{ramBytes: ram, cpus: 8})
		work := mbOf(t, s["work_mem"]) * mb
		conns, _ := strconv.Atoi(s["max_connections"])
		if total := work * uint64(conns); total > ram && ram > 1024*mb {
			t.Errorf("ram=%dMB: work_mem × max_connections = %dMB, over the machine", ram/mb, total/mb)
		}
	}
}

// Nothing in the managed block may change what a query MEANS. An embedded
// cluster that ran with fsync off, or at a different isolation level, would
// reintroduce in a subtler form exactly the divergence this feature removes.
func TestOnlyResourceKnobsAreManaged(t *testing.T) {
	forbidden := []string{
		"fsync", "synchronous_commit", "full_page_writes", "wal_level",
		"default_transaction_isolation", "datestyle", "timezone", "lc_",
		"standard_conforming_strings", "search_path", "bytea_output",
	}
	for _, s := range tuningFor(machine{ramBytes: 8192 * mb, cpus: 4}) {
		for _, f := range forbidden {
			if strings.HasPrefix(s.key, f) {
				t.Errorf("%s is a semantic knob and must not be managed", s.key)
			}
		}
		if s.reason == "" {
			t.Errorf("%s has no stated reason; the next reader has to guess", s.key)
		}
	}
}

func TestListenAddressesIsEmptySoNothingIsExposed(t *testing.T) {
	s := settingsOf(machine{ramBytes: 8192 * mb, cpus: 4})
	if s["listen_addresses"] != "''" {
		t.Fatalf("listen_addresses = %q — the database is reachable over TCP", s["listen_addresses"])
	}
}

func TestMemUnitRoundsDown(t *testing.T) {
	cases := map[uint64]string{
		32 * mb:      "32MB",
		1024 * mb:    "1GB",
		2048 * mb:    "2GB",
		1536 * mb:    "1536MB",
		76*mb + mb/2: "76MB", // rounding UP here would hand PostgreSQL memory the app is using
		mb - 1:       "0MB",
	}
	for in, want := range cases {
		if got := memUnit(in); got != want {
			t.Errorf("memUnit(%d) = %s, want %s", in, got, want)
		}
	}
}

func TestParseMemTotal(t *testing.T) {
	const meminfo = "MemTotal:       16316948 kB\nMemFree:         1234 kB\n"
	if got, want := parseMemTotal(meminfo), uint64(16316948)*1024; got != want {
		t.Errorf("parseMemTotal = %d, want %d", got, want)
	}
	for _, bad := range []string{"", "MemFree: 12 kB\n", "MemTotal:\n", "MemTotal: lots kB\n"} {
		if got := parseMemTotal(bad); got != 0 {
			t.Errorf("parseMemTotal(%q) = %d, want 0", bad, got)
		}
	}
	// A bare byte count (no unit) is not multiplied.
	if got := parseMemTotal("MemTotal: 4096\n"); got != 4096 {
		t.Errorf("unitless MemTotal = %d, want 4096", got)
	}
}

// Appending the block on every restart would grow postgresql.conf without
// bound and leave PostgreSQL applying the last of N copies.
func TestTheManagedBlockIsWrittenOnceAndPreservesEdits(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "postgresql.conf")
	pgWriteFile(t, conf, "# PostgreSQL's own generated file\nport = 5432\n")

	m := machine{ramBytes: 8192 * mb, cpus: 4}
	if err := writeTunedConf(dir, m); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(conf)
	if !strings.Contains(string(first), "shared_buffers") {
		t.Fatal("the block was not written")
	}

	// An operator's override below the marker must survive a restart.
	pgWriteFile(t, conf, string(first)+"\n# operator override\nshared_buffers = 1GB\n")
	if err := writeTunedConf(dir, m); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(conf)
	if strings.Count(string(second), skyConfMarker) != 1 {
		t.Errorf("the managed block was appended twice")
	}
	if !strings.Contains(string(second), "# operator override") {
		t.Error("the operator's edit was lost")
	}
	if !strings.Contains(string(second), "port = 5432") {
		t.Error("PostgreSQL's own settings were lost")
	}
}

// ---------------------------------------------------------------------------
// Detection — the half the conf gates above never reach
// ---------------------------------------------------------------------------

// Every gate above drives `tuningFor`, which takes a `machine`. None of them
// says where that machine comes from, and until this file a search for
// `cgroup`, `detectRAMBytes` or `detectMachine` across the whole test suite
// returned nothing: the arithmetic was pinned and its INPUT was not.
//
// The input is the half that fails silently. `/proc/meminfo` is not namespaced —
// a container reads the HOST's MemTotal — so in a 512MB container on a 64GB node
// the meminfo path answers 64GB. shared_buffers is then sized to the 8GB clamp,
// PostgreSQL allocates it up front, and the container is OOM-killed at the first
// checkpoint. Every setting in the file looks reasonable; the machine it was
// sized for simply is not this one.
//
// So the ORDER is the assertion, and it is made on both the raw byte count and
// on the setting the byte count produces — the second is what an operator would
// actually notice.
func TestACgroupLimitOutranksTheHostsMemTotal(t *testing.T) {
	const (
		containerLimit = 512 * mb
		hostTotal      = 64 * 1024 * mb
	)
	// A reader for a memory-limited container on a very large host: cgroup v2
	// says 512MB, and /proc/meminfo — unnamespaced — says 64GB.
	inContainer := func(path string) ([]byte, error) {
		switch path {
		case "/sys/fs/cgroup/memory.max":
			return []byte("536870912\n"), nil
		case "/proc/meminfo":
			return []byte("MemTotal:       67108864 kB\nMemFree:  100 kB\n"), nil
		}
		return nil, os.ErrNotExist
	}
	noSysctl := func() (uint64, bool) { return 0, false }

	// Vacuity guard: the two sources must genuinely disagree, or "the cgroup
	// won" is indistinguishable from "either would have done".
	if got := parseMemTotal("MemTotal:       67108864 kB\n"); got != hostTotal {
		t.Fatalf("the fixture's MemTotal parses to %d, not the intended %d", got, hostTotal)
	}

	got := detectRAMBytesFrom(inContainer, false, noSysctl)
	if got != containerLimit {
		t.Fatalf("detected %d bytes, want the cgroup limit %d.\n"+
			"/proc/meminfo is NOT namespaced: in a memory-limited container it reports the\n"+
			"HOST's total, so reading it first sizes this 512MB container from a 64GB\n"+
			"number. The cgroup limit is the only figure the kernel will honour.",
			got, containerLimit)
	}

	// …and the consequence, which is what actually kills the container.
	sized := settingsOf(machine{ramBytes: got, cpus: 2})
	if sb := mbOf(t, sized["shared_buffers"]); sb > 128 {
		t.Errorf("shared_buffers = %s in a 512MB container — PostgreSQL allocates that up "+
			"front and the container is OOM-killed at the first checkpoint", sized["shared_buffers"])
	}
	unlimited := settingsOf(machine{ramBytes: hostTotal, cpus: 2})
	if mbOf(t, unlimited["shared_buffers"]) <= 128 {
		t.Fatal("the 64GB profile is not distinguishable from the 512MB one — this gate " +
			"cannot tell the two sources apart and is vacuous")
	}
}

// The sources, one at a time. Each of these is a real deployment: cgroup v1 is
// still what a lot of Kubernetes runs on, "max" is an unlimited v2 container,
// the v1 sentinel is unlimited v1, and macOS has neither /proc nor cgroups.
func TestRAMDetectionFallsThroughItsSourcesInOrder(t *testing.T) {
	reader := func(files map[string]string) fileRead {
		return func(path string) ([]byte, error) {
			if v, ok := files[path]; ok {
				return []byte(v), nil
			}
			return nil, os.ErrNotExist
		}
	}
	noSysctl := func() (uint64, bool) { return 0, false }
	sysctl16G := func() (uint64, bool) { return 16 * 1024 * mb, true }

	cases := []struct {
		name   string
		files  map[string]string
		darwin bool
		sysctl func() (uint64, bool)
		want   uint64
	}{
		{"cgroup v2", map[string]string{"/sys/fs/cgroup/memory.max": "536870912"}, false, noSysctl, 512 * mb},
		{"cgroup v1", map[string]string{
			"/sys/fs/cgroup/memory/memory.limit_in_bytes": "268435456"}, false, noSysctl, 256 * mb},
		{"v2 unlimited falls through to meminfo", map[string]string{
			"/sys/fs/cgroup/memory.max": "max",
			"/proc/meminfo":             "MemTotal:       1048576 kB\n"}, false, noSysctl, 1024 * mb},
		{"v1 sentinel falls through to meminfo", map[string]string{
			"/sys/fs/cgroup/memory/memory.limit_in_bytes": "9223372036854771712",
			"/proc/meminfo": "MemTotal:       1048576 kB\n"}, false, noSysctl, 1024 * mb},
		{"macOS: no proc, no cgroup", nil, true, sysctl16G, 16 * 1024 * mb},
		{"nothing answers", nil, false, noSysctl, 0},
		// Undetectable RAM must stay 0 rather than becoming a guess: tuningFor
		// turns 0 into the deliberately small profile, and under-configuring
		// costs throughput while over-configuring costs the process.
		{"unparseable meminfo is not a guess", map[string]string{
			"/proc/meminfo": "MemTotal: not-a-number kB\n"}, false, noSysctl, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := detectRAMBytesFrom(reader(c.files), c.darwin, c.sysctl); got != c.want {
				t.Errorf("detected %d bytes, want %d", got, c.want)
			}
		})
	}
}

func TestEnsureSkyConfIsIdempotent(t *testing.T) {
	block := renderConfBlock(machine{ramBytes: 2048 * mb, cpus: 2})
	out, changed := ensureSkyConf("port = 5432\n", block)
	if !changed || !strings.Contains(out, skyConfMarker) {
		t.Fatal("the first call must append the block")
	}
	again, changed := ensureSkyConf(out, block)
	if changed || again != out {
		t.Fatal("the second call must be a no-op")
	}
}
