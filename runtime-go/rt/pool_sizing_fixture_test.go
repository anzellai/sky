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

// renderPoolSizingFixture emits the table: the consumer list, then one row per
// core count with the app pool, the unshared aux ceiling, the shared pool, and
// the resulting process demand.
func renderPoolSizingFixture() string {
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
	b.WriteString("# consumers <tab> comma-separated names, in demand order\n")
	b.WriteString("# <cpus> <tab> <app pool> <tab> <unshared aux> <tab> <shared aux> <tab> <telemetry> <tab> <process demand>\n")
	fmt.Fprintf(&b, "consumers\t%s\n", strings.Join(dbAuxPoolConsumerNames(), ","))
	for _, cpus := range coreCountsToCheck() {
		fmt.Fprintf(&b, "%d\t%d\t%d\t%d\t%d\t%d\n",
			cpus,
			defaultPostgresPoolConfigFor(cpus, false).MaxOpenConns,
			dbAuxPoolMaxOpenFor(cpus, false),
			dbSharedAuxPoolMaxOpenFor(cpus, false),
			telemetry.PoolMaxConns,
			dbProcessConnectionDemand(cpus, false),
		)
	}
	return b.String()
}

// TestThePoolSizingFixtureMatchesTheGoArithmetic keeps the shared table honest
// on this side.
func TestThePoolSizingFixtureMatchesTheGoArithmetic(t *testing.T) {
	withServerlessEnv(t, nil)
	want := renderPoolSizingFixture()
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
