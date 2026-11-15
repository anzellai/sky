package rt

import (
	"os"
	"path/filepath"
	"testing"
)

// The dev console token is written to .sky/console-token in the working
// directory. In a git work tree whose .gitignore does not list `.sky/` it
// reaches the next commit, and it is bare hex that no secret scanner
// flags (a real portal pushed one). The runtime must say so at the moment
// it writes the file, and stay quiet when the file is ignored or the
// directory is not under git.
func TestConsoleTokenCommitRisk(t *testing.T) {
	dir := t.TempDir()
	if got := consoleTokenCommitRisk(dir); got != "" {
		t.Fatalf("not a git work tree: want no risk, got %q", got)
	}
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := consoleTokenCommitRisk(dir); got == "" {
		t.Fatal("git work tree with no .gitignore: want a risk")
	}
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("sky-out/\n.env\n")
	if got := consoleTokenCommitRisk(dir); got == "" {
		t.Fatal(".gitignore without .sky/: want a risk")
	}
	for _, ok := range []string{".sky/", ".sky", "/.sky/", ".sky/console-token", "# local state\n  .sky/  \n"} {
		write("sky-out/\n" + ok + "\n")
		if got := consoleTokenCommitRisk(dir); got != "" {
			t.Fatalf(".gitignore with %q: want no risk, got %q", ok, got)
		}
	}
	// A nested project folder inside a work tree is still under git, and the
	// repository's top-level .gitignore counts for it.
	sub := filepath.Join(dir, "apps", "one")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	write("sky-out/\n")
	if got := consoleTokenCommitRisk(sub); got == "" {
		t.Fatal("nested folder, top-level .gitignore without .sky/: want a risk")
	}
	write("sky-out/\n.sky/\n")
	if got := consoleTokenCommitRisk(sub); got != "" {
		t.Fatalf("nested folder covered by the top-level .gitignore: want no risk, got %q", got)
	}
}
