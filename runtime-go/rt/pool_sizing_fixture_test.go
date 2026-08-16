package rt

// The Go half of the cross-language tie between this file's pool arithmetic
// and `rust/crates/sky/src/db_pool_sizing.rs`.
//
// # Why a fixture rather than a comment
//
// Two implementations of one number is one too many, and here it is
// unavoidable: the Rust CLI writes a cluster's `postgresql.conf` before any Go
// has run, so it cannot ask the runtime what the pools will be. The Rust module
// opened by asserting, in prose, that it "MIRRORS" this file "exactly". Nothing
// checked the claim. The Go side then grew a shared-pool size the Rust side
// never learned about, and every cluster the CLI sized was short.
//
// So the two are tied by a file both can read. This gate regenerates the table
// from the Go arithmetic and fails when the checked-in one differs; the Rust
// gate `the_fixture_matches_the_go_arithmetic` asserts the Rust functions
// reproduce the same rows (via `include_str!`, so it cannot go stale). Changing
// either language without the other turns one of them red.

// # The env axis, and why the table has one
//
// The first version of this fixture swept exactly one axis: `cpus`. Every
// property that is NOT a function of `cpus` was therefore outside its frame,
// and one of the four terms was: the app's pool follows the documented
// `<PREFIX>_DB_MAX_OPEN_CONNS` / `sky.toml [database] maxOpenConns` knob, and
// the demand arithmetic read the DEFAULTS instead. Both languages reproduced
// each other's `f(cpus)` faithfully and both were wrong together, which is
// exactly the outcome a cross-language fixture is supposed to make impossible.
//
// So the table now sweeps `cpus` × the pool knob, including the values that are
// not plain positive integers — `0` and a negative (both "unlimited"), an
// unparseable one, an empty one, and one with surrounding whitespace — because
// the resolution rules for those are part of the arithmetic the Rust side must
// reproduce, and prose describing them is what the last frame error was made of.

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"testing"

	"sky-app/rt/telemetry"
)

var updateFixture = flag.Bool("update-pool-fixture", false,
	"rewrite runtime-go/rt/testdata/db_pool_sizing.tsv from the Go arithmetic")

const poolSizingFixturePath = "testdata/db_pool_sizing.tsv"

// poolFixtureOverrides is the env axis: every distinct SHAPE of value the pool
// knob can carry, not merely a couple of round numbers.
//
// `nil` is "unset". The rest are raw strings, passed through exactly as an
// operator's shell or `sky.toml` would deliver them.
var poolFixtureOverrides = []*string{
	nil,
	strptr(""),  // set but empty — suppresses a sky.toml default, then reads as unset
	strptr("1"), // below the floor the default would clamp to
	strptr("8"),
	strptr("23"), // no core count can produce it (the default is 4×CPU clamped 4..32)
	strptr("32"), // the top of the documented default range
	strptr("64"),
	strptr("200"),   // past the embedded cluster's own ceiling
	strptr("0"),     // database/sql for UNLIMITED
	strptr("-4"),    // folded to unlimited by the resolver
	strptr("lots"),  // unparseable — falls back to the default
	strptr("  12 "), // whitespace, as a .env line often carries
}

func strptr(s string) *string { return &s }

// renderPoolSizingFixture emits the table: the consumer list, then one row per
// (core count × pool knob) with the app pool, the unshared aux ceiling, the
// shared pool, the resulting process demand, and the demand sky DERIVES from
// the machine alone (which is what the cluster sizings are allowed to clamp).
func renderPoolSizingFixture(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("# db_pool_sizing.tsv — the connection-pool arithmetic, shared between\n")
	b.WriteString("# runtime-go/rt/db_pool.go (the original) and\n")
	b.WriteString("# rust/crates/sky/src/db_pool_sizing.rs (which sizes clusters before any Go runs).\n")
	b.WriteString("#\n")
	b.WriteString("# Regenerate with:\n")
	b.WriteString("#   cd runtime-go && go test ./rt/ -run TestThePoolSizingFixture -update-pool-fixture\n")
	b.WriteString("# Both languages assert they reproduce it, so regenerating without following\n")
	b.WriteString("# the change on the other side turns that side red.\n")
	b.WriteString("#\n")
	b.WriteString("# The second column is the raw <PREFIX>_DB_MAX_OPEN_CONNS the process will\n")
	b.WriteString("# read — Go-quoted, or a bare `-` when the knob is unset. It is an AXIS of\n")
	b.WriteString("# this table, not a footnote: the app's pool follows that knob, three of the\n")
	b.WriteString("# four terms already did, and a table over `cpus` alone could not see the\n")
	b.WriteString("# fourth being wrong.\n")
	b.WriteString("#\n")
	b.WriteString("# consumers <tab> comma-separated names, in demand order\n")
	b.WriteString("# <cpus> <tab> <knob> <tab> <app pool> <tab> <unshared aux> <tab> <shared aux>\n")
	b.WriteString("#   <tab> <telemetry> <tab> <process demand> <tab> <machine-derived demand>\n")
	b.WriteString("#   <tab> <unlimited: yes|no>\n")
	fmt.Fprintf(&b, "consumers\t%s\n", strings.Join(dbAuxPoolConsumerNames(), ","))
	for _, raw := range poolFixtureOverrides {
		if raw == nil {
			os.Unsetenv(skyEnvName("DB_MAX_OPEN_CONNS"))
		} else {
			os.Setenv(skyEnvName("DB_MAX_OPEN_CONNS"), *raw)
		}
		col := "-"
		if raw != nil {
			col = fmt.Sprintf("%q", *raw)
		}
		for _, cpus := range coreCountsToCheck() {
			app, unlimited := dbAppPoolMaxOpenFor(cpus, false)
			unlim := "no"
			if unlimited {
				unlim = "yes"
			}
			fmt.Fprintf(&b, "%d\t%s\t%d\t%d\t%d\t%d\t%d\t%d\t%s\n",
				cpus,
				col,
				app,
				dbAuxPoolMaxOpenFor(cpus, false),
				dbSharedAuxPoolMaxOpenFor(cpus, false),
				telemetry.PoolMaxConns,
				dbProcessConnectionDemand(cpus, false),
				dbDerivedProcessConnectionDemand(cpus, false),
				unlim,
			)
		}
	}
	return b.String()
}

// TestThePoolSizingFixtureMatchesTheGoArithmetic keeps the shared table honest
// on this side.
func TestThePoolSizingFixtureMatchesTheGoArithmetic(t *testing.T) {
	withServerlessEnv(t, nil)
	// Registers the restore; the loop below then moves the value per row.
	t.Setenv(skyEnvName("DB_MAX_OPEN_CONNS"), "")
	want := renderPoolSizingFixture(t)
	if *updateFixture {
		if err := os.WriteFile(poolSizingFixturePath, []byte(want), 0o644); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		t.Logf("wrote %s", poolSizingFixturePath)
		return
	}
	got, err := os.ReadFile(poolSizingFixturePath)
	if err != nil {
		t.Fatalf("read %s: %v — the Rust cluster sizing reads this file, so it must exist",
			poolSizingFixturePath, err)
	}
	if string(got) != want {
		t.Fatalf("%s no longer matches the Go pool arithmetic.\n\n"+
			"This file is what ties runtime-go/rt/db_pool.go to\n"+
			"rust/crates/sky/src/db_pool_sizing.rs, which sizes every cluster Sky\n"+
			"generates. Regenerate it AND follow the change in the Rust module:\n"+
			"  cd runtime-go && go test ./rt/ -run TestThePoolSizingFixture -update-pool-fixture\n\n"+
			"--- on disk ---\n%s\n--- from the Go arithmetic ---\n%s",
			poolSizingFixturePath, got, want)
	}
}
