//go:build !js

package rt

import (
	"os"
	"strings"
	"testing"
)

// A test file named `*_js_test.go` or `*_wasm_test.go` carries an implicit
// GOOS=js / GOARCH=wasm constraint from its NAME. With a `//go:build !js`
// line as well it builds for no target, and its tests never run: v0.27.0
// found TestIslandJS_DeliveryContract (island_delivery_js_test.go, a node
// harness meant for the host) silently dead this way. A host test must not
// end in a GOOS or GOARCH suffix.
func TestNoHostTestIsHiddenByAFileNameSuffix(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, "_test.go") {
			continue
		}
		stem := strings.TrimSuffix(name, "_test.go")
		if !strings.HasSuffix(stem, "_js") && !strings.HasSuffix(stem, "_wasm") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.SplitN(string(src), "\n", 5) {
			if strings.HasPrefix(line, "//go:build") && strings.Contains(line, "!js") {
				t.Errorf("%s: the `_js`/`_wasm` file-name suffix limits it to js/wasm, and `%s` excludes js, so it never builds; rename it", name, line)
			}
		}
	}
}
