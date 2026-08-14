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
