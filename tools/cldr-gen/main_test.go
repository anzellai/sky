package main

import (
	"os"
	"strings"
	"testing"
)

// TestRegenerationIsByteIdentical regenerates the CLDR tables from the pinned
// tarballs and compares them with the committed files in runtime-go/rt/i18n.
// It needs the tarballs: from SKY_CLDR_SRC, the cache ~/.cache/sky/cldr, or
// the network. Without them it fails, unless SKY_LIVE_TESTS=skip.
func TestRegenerationIsByteIdentical(t *testing.T) {
	src, err := load()
	if err != nil {
		if os.Getenv("SKY_LIVE_TESTS") == "skip" {
			t.Skipf("SKY_LIVE_TESTS=skip: %v", err)
		}
		t.Fatalf("the CLDR %s tarballs are not available: %v. Put them in SKY_CLDR_SRC or allow the download", Version, err)
	}
	files, err := generate(src)
	if err != nil {
		t.Fatal(err)
	}
	bad := compare("../../runtime-go/rt/i18n", files)
	if len(bad) > 0 {
		t.Fatalf("regenerate with `go run ./tools/cldr-gen` and commit:\n%s", strings.Join(bad, "\n"))
	}
	t.Logf("ASSERTIONS: %d", len(files))
}

func TestCompileRule(t *testing.T) {
	got, err := compileRule("n % 10 = 3..4,9 and n % 100 != 10..19")
	if err != nil {
		t.Fatal(err)
	}
	want := "((o.whole() && (((o.i % 10) >= 3 && (o.i % 10) <= 4) || (o.i % 10) == 9)) && !(o.whole() && (((o.i % 100) >= 10 && (o.i % 100) <= 19))))"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if _, err := compileRule("n within 1..2"); err == nil {
		t.Error("an unknown relation must be refused")
	}
}
