package rt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// packageGoSources concatenates this package's non-test Go sources, so a test
// can ask what the production code actually reads rather than restating it.
func packageGoSources(t *testing.T) string {
	t.Helper()
	paths, err := filepath.Glob("*.go")
	if err != nil || len(paths) == 0 {
		t.Fatalf("cannot list package sources: %v", err)
	}
	var b strings.Builder
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		by, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("cannot read %s: %v", p, err)
		}
		b.Write(by)
	}
	return b.String()
}

// devReport is the block a developer sees on a bare `sky run`: dev bind is
// loopback ("127.0.0.1"), so the exposure note never fires here.
func devReport(gc gcTuning) []string {
	return startupReportLines("http://localhost:8000/_sky/console", "127.0.0.1", false, gc, false)
}

func joinReport(lines []string) string { return strings.Join(lines, "\n") }

// TestNoAddedStartupLineLooksLikeAListeningLine is the compatibility gate, and
// it is the reason this block is additive rather than a reformat.
//
// Three consumers key on the existing line:
//   - `apps/fieldbook/verify.sh` greps `Sky.Live listening on :$PORT` literally.
//   - `xtask build_run_gate` reads stdout and, for any line whose LOWERCASE form
//     contains "listening", lifts the port from its last `:PORT` / last number.
//   - `sky run`'s supervisor (`rust/crates/sky/src/main.rs`) does the same.
//
// So any line this block adds that contained "listening" would be parsed as a
// second, competing port announcement — and the console line ends in a URL with
// a port in it, which is exactly the shape that would be misread.
func TestNoAddedStartupLineLooksLikeAListeningLine(t *testing.T) {
	cases := [][]string{
		devReport(gcTuning{reason: "GOMEMLIMIT 996MB, GOGC 400 — derived from 1.9GB detected"}),
		startupReportLines("http://localhost:8000/_sky/console", "", true, gcTuning{reason: "x"}, false),
		startupReportLines("", "127.0.0.1", false, gcTuning{reason: "Go defaults — 512MB detected is too little"}, false),
	}
	for _, lines := range cases {
		for _, l := range lines {
			if strings.Contains(strings.ToLower(l), "listening") {
				t.Fatalf("added startup line would be parsed as a port announcement: %q", l)
			}
		}
	}
}

// TestProductionPrintsNoConsoleLineAndNoScolding. Under `ENV=production` the
// console block DISAPPEARS. It is not replaced by a warning: the user has done
// the thing the checklist asked for, and being told off for it is how a banner
// becomes something people silence.
func TestProductionPrintsNoConsoleLineAndNoScolding(t *testing.T) {
	got := joinReport(startupReportLines("http://localhost:8000/_sky/console", "", true,
		gcTuning{reason: "GOMEMLIMIT 996MB, GOGC 400 — derived from 1.9GB detected"}, false))

	if strings.Contains(got, "console") {
		t.Fatalf("production block mentions the console:\n%s", got)
	}
	for _, scold := range []string{"WARN", "warning", "insecure", "should", "must", "!"} {
		if strings.Contains(got, scold) {
			t.Fatalf("production block scolds (%q):\n%s", scold, got)
		}
	}
	if !strings.Contains(got, "GOMEMLIMIT") {
		t.Fatalf("production block dropped the GC line, which is not a dev-only fact:\n%s", got)
	}
}

// TestTheDevBlockNamesOnlyVariablesTheRuntimeReads.
//
// The failure this prevents: a banner printed on every dev run that names an
// environment variable nothing consults. Every name asserted here is checked
// against a reader in this package by
// `TestEveryVariableTheDevBlockNamesHasAReaderInThisPackage`.
func TestTheDevBlockNamesOnlyVariablesTheRuntimeReads(t *testing.T) {
	got := joinReport(devReport(gcTuning{reason: "GOMEMLIMIT 996MB, GOGC 400 — derived from 1.9GB detected"}))

	for _, want := range []string{"ENV=production", "SKY_CONSOLE_AUTH=token", "SKY_CONSOLE_TOKEN=", "SKY_ADMIN_TOKEN"} {
		if !strings.Contains(got, want) {
			t.Fatalf("dev block does not name %s:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "/_sky/console") {
		t.Fatalf("dev block does not give the console URL:\n%s", got)
	}

	// `SKY_AUTH_TOKEN_SECRET` must NOT appear. Nothing in the runtime reads it
	// — `Auth.signToken` takes its secret as a Sky-level argument — so telling
	// every user of every no-auth app to set it is a false instruction printed
	// on every dev run. `AGENTS.md` carried exactly this claim in its
	// production gate; it is corrected in the same commit.
	if strings.Contains(got, "SKY_AUTH_TOKEN_SECRET") {
		t.Fatalf("dev block names SKY_AUTH_TOKEN_SECRET, which no runtime code reads:\n%s", got)
	}
	// Likewise the legacy aliases: naming them teaches the wrong one.
	for _, legacy := range []string{"SKY_CONSOLE_TOKEN_SECRET", "SKY_METRICS_TOKEN"} {
		if strings.Contains(got, legacy) {
			t.Fatalf("dev block names the back-compat alias %s instead of the canonical variable:\n%s", legacy, got)
		}
	}
}

// TestEveryVariableTheDevBlockNamesHasAReaderInThisPackage is the gate that
// makes the test above more than a restatement of the implementation.
//
// It lifts every `SKY_*` token out of the RENDERED block and requires a real
// `Getenv` for it in this package's non-test sources. Rename the variable in
// the reader and the banner goes stale silently; this fails instead. It is the
// same shape of defect as the twenty-one false doc claims corrected today,
// except printed to every user on every run.
func TestEveryVariableTheDevBlockNamesHasAReaderInThisPackage(t *testing.T) {
	block := joinReport(devReport(gcTuning{reason: "GOMEMLIMIT 996MB, GOGC 400"}))

	names := map[string]bool{}
	for _, f := range strings.FieldsFunc(block, func(r rune) bool {
		return !(r == '_' || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'))
	}) {
		if strings.HasPrefix(f, "SKY_") {
			names[f] = true
		}
	}
	if len(names) == 0 {
		t.Fatal("no SKY_ variable found in the block — this gate would pass vacuously")
	}

	sources := packageGoSources(t)
	for name := range names {
		// Either the plain reader, or the prefix-stripped form the runtime's
		// own `skyGetenv` helper takes (`skyGetenv("CONSOLE_AUTH")`).
		plain := `Getenv("` + name + `")`
		stripped := `skyGetenv("` + strings.TrimPrefix(name, "SKY_") + `")`
		if !strings.Contains(sources, plain) && !strings.Contains(sources, stripped) {
			t.Fatalf("the startup block names %s but no file in this package reads it (looked for %s and %s)",
				name, plain, stripped)
		}
	}
}

// TestTheDevBlockStaysShort. This prints on every start, including in tests and
// scripts. A checklist nobody reads is worse than no checklist, and length is
// the usual reason.
// The GC line is taken from the REAL derivation across the machines an app is
// deployed on, not from a literal in this file — a literal goes stale the
// moment the wording changes, which is how a length gate comes to pass while
// the thing it guards wraps.
func TestTheDevBlockStaysShort(t *testing.T) {
	for _, ram := range machinesToCheck() {
		for _, env := range []gcEnvironment{{}, {embeddedPostgres: true}, {gogc: "150"}, {gomemlimit: "2GiB"}} {
			lines := devReport(gcTuningFor(machine{ramBytes: ram, cpus: 4}, env))
			if len(lines) > 6 {
				t.Fatalf("RAM %s: dev block is %d lines:\n%s", humanRAM(ram), len(lines), joinReport(lines))
			}
			for _, l := range lines {
				if len([]rune(l)) > 110 {
					t.Fatalf("RAM %s: startup line is %d chars, it will wrap in a normal terminal: %q",
						humanRAM(ram), len([]rune(l)), l)
				}
			}
		}
	}
}

// TestAnOperatorsOwnGCSettingIsVisiblyHonoured. A deliberate `GOGC` that the
// runtime respected must SAY so, or the operator cannot tell the difference
// between "respected" and "silently overridden" without reading the source.
func TestAnOperatorsOwnGCSettingIsVisiblyHonoured(t *testing.T) {
	tun := gcTuningFor(machine{ramBytes: 16384 * mb, cpus: 8}, gcEnvironment{gogc: "150"})
	got := joinReport(devReport(tun))
	if !strings.Contains(got, "150") || !strings.Contains(got, "set by you") {
		t.Fatalf("an explicit GOGC=150 is not visibly honoured:\n%s", got)
	}
}

// TestNoConsoleMountedMeansNoConsoleLine. The block must not advertise a
// surface this binary does not serve — `printStartupReport` passes an empty URL
// when neither console mounted.
func TestNoConsoleMountedMeansNoConsoleLine(t *testing.T) {
	got := joinReport(startupReportLines("", "127.0.0.1", false, gcTuning{reason: "GOMEMLIMIT 1GB"}, false))
	if strings.Contains(got, "console") {
		t.Fatalf("advertised a console that is not mounted:\n%s", got)
	}
	if !strings.Contains(got, "GOMEMLIMIT") {
		t.Fatalf("dropped the GC line when no console was mounted:\n%s", got)
	}
}

// TestSkyGcQuietDropsOnlyTheGcLine. It suppresses output, it does not change
// what was derived — and it must not take the console checklist with it.
func TestSkyGcQuietDropsOnlyTheGcLine(t *testing.T) {
	got := joinReport(startupReportLines("http://localhost:8000/_sky/console", "127.0.0.1", false,
		gcTuning{reason: "GOMEMLIMIT 996MB, GOGC 400"}, true))
	if strings.Contains(got, "GOMEMLIMIT") {
		t.Fatalf("SKY_GC_QUIET did not drop the GC line:\n%s", got)
	}
	if !strings.Contains(got, "/_sky/console") {
		t.Fatalf("SKY_GC_QUIET also dropped the console line:\n%s", got)
	}
}
