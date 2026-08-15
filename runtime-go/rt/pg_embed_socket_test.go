package rt

// The socket-directory derivation, pinned.
//
// Its twin is `the_socket_directory_for_a_pinned_project_is_a_pinned_constant`
// in `rust/crates/sky/src/db_cluster.rs`. Both assert the same LITERAL rather
// than comparing the two implementations to each other: two implementations
// compared only to each other can drift together, and drifting together is
// exactly what happened. Until P5b the Rust side hashed the PROJECT path while
// this side hashed `<dataRoot>/pg`, so a `./app --embed` run in a project whose
// cluster `sky db start` had already brought up adopted the live postmaster and
// then waited a minute on a socket directory nobody was listening in.
//
// This file is separate from pg_embed_test.go on purpose: the constant and the
// reason for it belong together, and the pinned value must be conspicuous
// enough that changing it is a decision rather than a rebase artefact.

import (
	"os"
	"path/filepath"
	"testing"
)

// The pinned coordinates. `pinnedProject` is deliberately a path that does not
// exist, so `resolvedPath` is an identity here and the test is hermetic on any
// machine.
const (
	pinnedProject   = "/sky/pinned/project"
	pinnedSocketDir = "/tmp/sky-3b7c436bcb7e1ee0"
)

func TestTheSocketDirectoryForAPinnedProjectIsAPinnedConstant(t *testing.T) {
	cfg, err := resolveEmbedConfig(
		[]string{"app", "--embed"},
		fakeEnv(map[string]string{}),
		pinnedProject,
	)
	if err != nil {
		t.Fatalf("resolveEmbedConfig: %v", err)
	}
	if cfg.socketDir != pinnedSocketDir {
		t.Errorf("socket dir for project %s = %s, want the pinned %s\n"+
			"If this changed deliberately, the Rust twin in db_cluster.rs must change "+
			"in the same commit — the two name one directory for one cluster.",
			pinnedProject, cfg.socketDir, pinnedSocketDir)
	}
	// And the input really is the data directory, not the project: this is the
	// assertion the two sides disagreed on.
	if want := socketDirFor(filepath.Join(pinnedProject, ".skydata", "pg"), "", "/tmp"); cfg.socketDir != want {
		t.Errorf("socket dir is not derived from the data directory: %s vs %s", cfg.socketDir, want)
	}
}

// A project reached through a symlink is one project. On macOS `/tmp` IS a
// symlink to `/private/tmp`, and the Rust side canonicalises its project path
// before hashing — so without symlink resolution here the two sides name
// different sockets for one data directory on the platform most of this repo's
// development happens on.
func TestASymlinkedDataDirectoryHashesAsItsRealPath(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}
	viaLink := socketDirFor(filepath.Join(link, "pg"), "", "/tmp")
	viaReal := socketDirFor(filepath.Join(real, "pg"), "", "/tmp")
	if viaLink != viaReal {
		t.Errorf("symlinked data dir hashed differently:\n  via link: %s\n  via real: %s", viaLink, viaReal)
	}
}

// The component that does not exist yet must survive resolution — `.skydata/pg`
// is absent until the first initdb, and a resolver that gave up on a missing
// leaf would hash the parent and put every project in one socket directory.
func TestResolvedPathKeepsComponentsThatDoNotExistYet(t *testing.T) {
	root := t.TempDir()
	got := resolvedPath(filepath.Join(root, "nope", "deeper"))
	if filepath.Base(got) != "deeper" || filepath.Base(filepath.Dir(got)) != "nope" {
		t.Errorf("resolvedPath dropped the missing tail: %s", got)
	}
	a := socketDirFor(filepath.Join(root, "one", "pg"), "", "/tmp")
	b := socketDirFor(filepath.Join(root, "two", "pg"), "", "/tmp")
	if a == b {
		t.Errorf("two missing data directories collapsed to one socket dir: %s", a)
	}
}
