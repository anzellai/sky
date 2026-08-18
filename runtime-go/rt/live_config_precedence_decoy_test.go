package rt

// live_config_precedence.go exists to hold ONE precedence rule. A function in
// it that nothing calls is a second, unreachable copy of that rule — and a
// reader who edits the unreachable one changes nothing while believing they
// changed the behaviour of every Sky.Live setting.
//
// That is not hypothetical. `resolveLivePortLayers` sat here with ZERO callers
// in production and in tests, while `resolveLivePort` (live.go:3866) called
// `configLayers` directly. It was found because inverting its precedence left
// `xtask config-matrix --check: OK` — no gate in the repository could see the
// difference, because there was no difference to see.
//
// Go does not report an unused package-level function, so nothing else can
// catch this. This test does.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var precedenceFuncDecl = regexp.MustCompile(`(?m)^func ([A-Za-z_][A-Za-z0-9_]*)\(`)

// TestNoDecoyInThePrecedenceFile — every function declared in
// live_config_precedence.go is called from somewhere else in the package.
func TestNoDecoyInThePrecedenceFile(t *testing.T) {
	const owner = "live_config_precedence.go"

	src, err := os.ReadFile(owner)
	if err != nil {
		t.Fatalf("cannot read %s: %v", owner, err)
	}
	decls := precedenceFuncDecl.FindAllStringSubmatch(string(src), -1)
	if len(decls) < 5 {
		t.Fatalf("found only %d function declarations in %s — the scan has broken, "+
			"and a broken scan would pass vacuously", len(decls), owner)
	}

	// Every other .go file in the package, test files included: a helper that
	// only tests call is still not production code, but it is at least reached,
	// and this test is about UNREACHABLE, not about layering.
	others, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	var corpus []string
	for _, p := range others {
		if filepath.Base(p) == owner {
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("cannot read %s: %v", p, err)
		}
		corpus = append(corpus, string(b))
	}
	if len(corpus) < 50 {
		t.Fatalf("only %d sibling files read — the corpus has broken", len(corpus))
	}

	// Callers inside the owning file count too: `resolveTTL` calling
	// `configLayers` is a real call site, and `configLayers` is genuinely
	// reachable through it. What must not happen is a function no file calls.
	corpus = append(corpus, stripDecls(string(src)))

	checked := 0
	for _, d := range decls {
		name := d[1]
		checked++
		called := false
		for _, body := range corpus {
			if strings.Contains(body, name+"(") {
				called = true
				break
			}
		}
		if !called {
			t.Errorf("%s declares %s and NOTHING calls it. This file holds one "+
				"precedence rule; an uncalled resolver in it is a second, "+
				"unreachable copy that a reader will edit believing it is live. "+
				"Wire it, or delete it.", owner, name)
		}
	}
	if checked != len(decls) {
		t.Fatalf("checked %d of %d declarations", checked, len(decls))
	}
}

// stripDecls removes the `func name(` headers so a function does not count as
// its own caller.
func stripDecls(src string) string {
	return precedenceFuncDecl.ReplaceAllString(src, "func __decl__(")
}
