package rt

import (
	"os"
	"os/exec"
	"testing"
)

// The Sky.Live client runtime is a large JS program embedded as a Go string, so
// `go build` never parses it — a syntax error (a stray brace in a hand-edited
// handler) ships silently and breaks every page at load. This gate assembles
// the real client JS and runs `node --check` on it. Skips when node is absent
// (e.g. minimal CI images) rather than failing.
func TestLiveJSSyntaxValid(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not available; skipping embedded-JS syntax check")
	}
	js := liveJSWithCfgAndCsrfWithBase("sid-test", liveBannerConfig{}, "csrf-test", "")
	f, err := os.CreateTemp("", "skylive-*.js")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(js); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if out, err := exec.Command(node, "--check", f.Name()).CombinedOutput(); err != nil {
		t.Fatalf("embedded Sky.Live client JS failed `node --check`:\n%s", out)
	}
}
