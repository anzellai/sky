package rt

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestAiRandomFailureIsAnErrorNotAPanic: a failed read of the system
// randomness source reaches the caller as an error at every site that names
// a temporary file, an engine key or a registry entry. It used to panic with
// "crypto/rand failed", an unclassified panic in a well-typed program.
func TestAiRandomFailureIsAnErrorNotAPanic(t *testing.T) {
	saved := aiRandRead
	aiRandRead = func([]byte) (int, error) { return 0, errors.New("entropy source closed") }
	defer func() { aiRandRead = saved }()

	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.WriteFile(src, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := map[string]func() error{
		"aiRandomHex": func() error { _, err := aiRandomHex(4); return err },
		"aiWriteFileAtomic": func() error {
			return aiWriteFileAtomic(filepath.Join(dir, "a.json"), []byte("{}"))
		},
		"aiCopyFile":       func() error { return aiCopyFile(src, filepath.Join(dir, "copy")) },
		"aiEngineOwnsPort": func() error { _, err := aiEngineOwnsPort(1, "key"); return err },
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			var err error
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("%s panicked: %v", name, r)
					}
				}()
				err = run()
			}()
			if err == nil {
				t.Fatalf("%s: expected an error from a failed randomness source", name)
			}
			var f *aiFailure
			if name != "aiCopyFile" && name != "aiWriteFileAtomic" && !errors.As(err, &f) {
				t.Fatalf("%s: expected a typed AI failure, got %T: %v", name, err, err)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(dir, "a.json")); err == nil {
		t.Fatal("aiWriteFileAtomic wrote the file without a temporary name")
	}
}
