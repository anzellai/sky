//go:build !js

package rt

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Std.Time in the Sky.Spa wasm client. The runtime embeds the IANA zone data
// (`import _ "time/tzdata"`), and a browser has no zone files, so the client
// must find every zone in that copy. This builds rt/timewasm for js/wasm and
// runs it under Node.js through Go's wasm_exec.js with no `fs` module: that
// is the browser's setup, in which a file read fails. The test in the wasm
// binary checks first that it cannot read the host's zone files.
func TestStdTimeRunsInWasmWithEmbeddedZones(t *testing.T) {
	node := requireNode(t)
	goroot := runtime.GOROOT()
	wasmExec := filepath.Join(goroot, "lib", "wasm", "wasm_exec.js")
	if _, err := os.Stat(wasmExec); err != nil {
		t.Fatalf("the Go toolchain has no lib/wasm/wasm_exec.js: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	dir := t.TempDir()
	bin := filepath.Join(dir, "timewasm.test.wasm")
	build := exec.CommandContext(ctx, filepath.Join(goroot, "bin", "go"), "test", "-c", "-o", bin, "./timewasm/")
	build.Env = append(os.Environ(), "GOOS=js", "GOARCH=wasm", "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the wasm test failed: %v\n%s", err, out)
	}
	runner := filepath.Join(dir, "run.js")
	script := `"use strict";
const nodefs = require("fs");
require(process.argv[2]);
const go = new Go();
go.argv = [process.argv[3], "-test.v", "-test.count=1"];
go.env = {};
go.exit = process.exit;
WebAssembly.instantiate(nodefs.readFileSync(process.argv[3]), go.importObject)
	.then((r) => go.run(r.instance))
	.catch((e) => { console.error(e); process.exit(1); });
`
	if err := os.WriteFile(runner, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, node, runner, wasmExec, bin)
	cmd.Env = []string{"PATH=" + filepath.Dir(node)}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the wasm test failed: %v\n%s", err, out)
	}
	for _, name := range []string{"TestWasmHasNoZoneFiles", "TestWasmTimeZones"} {
		if !strings.Contains(string(out), "--- PASS: "+name) {
			t.Fatalf("the wasm test %s did not pass:\n%s", name, out)
		}
	}
}
