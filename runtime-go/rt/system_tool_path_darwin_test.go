//go:build darwin && !js

package rt

import "testing"

// TestMacMemoryIsDetectedWithAnEmptyPath: on macOS the machine's memory comes
// from `sysctl -n hw.memsize`. A bare `sysctl` is not on an empty PATH (it is
// in /usr/sbin), so detection returned 0 and the startup line said "machine
// memory could not be detected; left on the Go defaults".
func TestMacMemoryIsDetectedWithAnEmptyPath(t *testing.T) {
	t.Setenv("PATH", "")
	n, ok := sysctlMemsize()
	if !ok || n == 0 {
		t.Fatalf("sysctlMemsize with PATH=\"\" = (%d, %v), want the machine's memory", n, ok)
	}
	if got := detectRAMBytes(); got == 0 {
		t.Fatal("detectRAMBytes with PATH=\"\" = 0, want the machine's memory")
	}
}
