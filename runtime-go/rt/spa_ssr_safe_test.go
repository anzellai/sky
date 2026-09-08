//go:build !js

package rt

import (
	"os"
	"path/filepath"
	"testing"
)

// Fix 2 soundness: a DESTRUCTIVE kernel run WHILE an SSR settle is in flight must
// self-suppress — it returns a classified Err and never touches the store, so a
// GET can never mutate even when the settled command batches a write alongside a
// read. Outside a settle the same kernel writes normally.
func TestSsrSettle_SuppressesDestructiveWrites(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "out.txt")
	if err := os.WriteFile(target, []byte("seed"), 0644); err != nil {
		t.Fatalf("stage: %v", err)
	}

	// The File.writeFile kernel yields a task thunk; running it performs the write.
	task, ok := File_writeFile(target, "mutated").(func() any)
	if !ok {
		t.Fatalf("File_writeFile did not return a task thunk")
	}

	// (1) Inside a settle: the write is suppressed to an Err, file unchanged.
	enterSsrSettle()
	if !InSsrSettle() {
		t.Fatal("InSsrSettle must be true after enterSsrSettle")
	}
	res := task()
	if !isErrResult(res) {
		t.Fatalf("a suppressed write must yield Err, got %#v", res)
	}
	exitSsrSettle()
	if got, _ := os.ReadFile(target); string(got) != "seed" {
		t.Fatalf("suppressed write must NOT touch the file, got %q", string(got))
	}

	// (2) Outside a settle: the same task writes normally.
	if InSsrSettle() {
		t.Fatal("InSsrSettle must be false after exitSsrSettle")
	}
	if res := task(); isErrResult(res) {
		t.Fatalf("outside a settle the write must succeed, got Err: %#v", res)
	}
	if got, _ := os.ReadFile(target); string(got) != "mutated" {
		t.Fatalf("outside a settle the write must land, got %q", string(got))
	}
}
