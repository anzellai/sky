//go:build !js && ((darwin && !ios) || linux)

package rt

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"golang.org/x/sys/unix"
)

// A SIGKILLed postmaster leaves its System V shared-memory segment behind,
// and only line 7 of its postmaster.pid says which one. PostgreSQL removes the
// segment itself on the next start, by reading that line, so clearing the pid
// file first used to leak one segment per crash. macOS allows 32 (shmmni), so
// after a few dozen crashes no PostgreSQL on the machine could start: initdb
// failed with "could not create shared memory segment: No space left on
// device".
func TestAStalePidfileReleasesTheSharedMemoryItNames(t *testing.T) {
	id := newTestShm(t)
	dir := t.TempDir()
	writePidfileWithShm(t, dir, 999999, id)

	if err := clearStalePidfile(dir); err != nil {
		t.Fatalf("a stale pid file must be cleared, not refused: %v", err)
	}
	if fileExists(filepath.Join(dir, "postmaster.pid")) {
		t.Fatal("the stale pid file is still there")
	}
	if shmExists(id) {
		t.Fatalf("segment %d named by the stale pid file was left behind", id)
	}
}

// A segment something is still attached to is in use, whatever the pid file
// says, and is never removed.
func TestAnAttachedSegmentIsNotReleased(t *testing.T) {
	id := newTestShm(t)
	mem, err := unix.SysvShmAttach(id, 0, 0)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	defer func() { _ = unix.SysvShmDetach(mem) }()
	dir := t.TempDir()
	writePidfileWithShm(t, dir, 999999, id)

	if err := clearStalePidfile(dir); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if !shmExists(id) {
		t.Fatal("an attached segment was removed")
	}
}

func TestPostmasterShmIDReadsLineSeven(t *testing.T) {
	body := "123\n/d\n1754000000\n5432\n/tmp/s\n127.0.0.1\n  5432001     65538\nready   \n"
	if id, ok := postmasterShmID(body); !ok || id != 65538 {
		t.Fatalf("got %d, %v", id, ok)
	}
	for _, bad := range []string{"123\n/d\n", "1\n2\n3\n4\n5\n6\n\n", "1\n2\n3\n4\n5\n6\n        0         7\n"} {
		if _, ok := postmasterShmID(bad); ok {
			t.Errorf("%q gave a segment id", bad)
		}
	}
}

func newTestShm(t *testing.T) int {
	t.Helper()
	id, err := unix.SysvShmGet(unix.IPC_PRIVATE, 64, unix.IPC_CREAT|0o600)
	if err != nil {
		t.Fatalf("shmget: %v", err)
	}
	t.Cleanup(func() { _, _ = unix.SysvShmCtl(id, unix.IPC_RMID, nil) })
	return id
}

func shmExists(id int) bool {
	var ds unix.SysvShmDesc
	_, err := unix.SysvShmCtl(id, unix.IPC_STAT, &ds)
	return err == nil
}

func writePidfileWithShm(t *testing.T, dir string, pid, shmid int) {
	t.Helper()
	body := strconv.Itoa(pid) + "\n" + dir + "\n1754000000\n5432\n/tmp/sky-x\n\n" +
		"  5432001 " + strconv.Itoa(shmid) + "\nready   \n"
	if err := os.WriteFile(filepath.Join(dir, "postmaster.pid"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
