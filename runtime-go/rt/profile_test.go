package rt

import (
	"strings"
	"testing"
)

func TestDemangleSkyName(t *testing.T) {
	cases := map[string]string{
		"Lib_AuthHandlers_handleSignIn": "Lib.AuthHandlers.handleSignIn",
		"Main_view":                     "Main.view",
		"Std_Ui_layout":                 "Std.Ui.layout",
		"Main_update":                   "Main.update",
		"foldl":                         "foldl", // no module prefix → unchanged
	}
	for in, want := range cases {
		if got := demangleSkyName(in); got != want {
			t.Errorf("demangleSkyName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDemangleFrameKinds(t *testing.T) {
	cases := []struct {
		in   string
		want string
		kind frameKind
	}{
		{"main.Main_update", "Main.update", kindUser},
		{"main.Lib_Products_listAllProducts", "Lib.Products.listAllProducts", kindUser},
		{"main.main", "main (entry point)", kindUser},
		{"sky-app/rt.Db_query", "Db.query", kindStdlib},
		{"sky-app/rt.Log_println", "Log.println", kindStdlib},
		{"sky-app/rt.SkyCall", "sky runtime (SkyCall)", kindRuntime},
		{"sky-app/rt.Coerce", "sky runtime (Coerce)", kindRuntime},
		{"reflect.Value.Index", "reflection dispatch", kindRuntime},
		{"runtime.gopark", "runtime.gopark", kindGo},
	}
	for _, c := range cases {
		got, kind := demangleFrame(c.in)
		if got != c.want || kind != c.kind {
			t.Errorf("demangleFrame(%q) = (%q, %d), want (%q, %d)", c.in, got, kind, c.want, c.kind)
		}
	}
}

const sampleDump = `goroutine 1 [chan receive, 3 minutes]:
sky-app/rt.SkyCall(...)
	/app/rt/rt.go:100 +0x20
main.Main_waitForThing(...)
	/app/main.go:42 +0x10
main.main()
	/app/main.go:10 +0x5
goroutine 7 [running]:
runtime.systemstack()
goroutine 9 [IO wait]:
internal/poll.runtime_pollWait(...)
`

func TestSplitGoroutineBlocksAndTopFrame(t *testing.T) {
	blocks := splitGoroutineBlocks(sampleDump)
	if len(blocks) != 3 {
		t.Fatalf("expected 3 goroutine blocks, got %d", len(blocks))
	}
	if blocks[0].state != "chan receive, 3 minutes" {
		t.Errorf("block 0 state = %q", blocks[0].state)
	}
	// topSkyFrame must skip the rt plumbing and surface the dev's own frame.
	if w := topSkyFrame(blocks[0].frames); w != "Main.waitForThing" {
		t.Errorf("topSkyFrame = %q, want Main.waitForThing", w)
	}
}

func TestRenderGoroutinesFlagsHang(t *testing.T) {
	var b strings.Builder
	renderGoroutines(&b, "hang — no exit after 30s", sampleDump)
	out := b.String()
	if !strings.Contains(out, "Where it's stuck") {
		t.Errorf("hang must render the stuck section:\n%s", out)
	}
	if !strings.Contains(out, "Main.waitForThing") {
		t.Errorf("stuck section must name the dev's Sky frame:\n%s", out)
	}
	if !strings.Contains(out, "chan receive") {
		t.Errorf("stuck section must show the wait state:\n%s", out)
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[uint64]string{
		512:                    "512 B",
		2048:                   "2.0 KB",
		5 * 1024 * 1024:        "5.0 MB",
		3 * 1024 * 1024 * 1024: "3.0 GB",
	}
	for in, want := range cases {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}
