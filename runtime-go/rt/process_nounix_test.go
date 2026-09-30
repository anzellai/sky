//go:build !js && !unix

package rt

import (
	"strings"
	"syscall"
	"testing"
)

// Sky.Core.Process on Windows (process_nounix.go). The documented contract
// (sky-stdlib/Sky/Core/Process.sky): plain pipes work; a PTY spawn returns
// `Err Unavailable`; only `Kill` is delivered, every other signal is an Err.
// Compiled on every change by TestProcessWindowsPathCompiles (a GOOS=windows
// vet from a Unix host), run on a Windows host.

func TestProcessWindows_PtySpawnIsUnavailable(t *testing.T) {
	h, e := spawnProcess(procSpec{program: "cmd", pty: true, cols: 80, rows: 24})
	if h != nil {
		t.Fatal("a PTY spawn must not start a process on Windows")
	}
	if e == nil || errorKindName(e) != "Unavailable" {
		t.Fatalf("a PTY spawn must be Err Unavailable, got %v", e)
	}
	if !strings.Contains(errorMessage(e), "Linux and macOS only") {
		t.Fatalf("the Err must say where a PTY is supported, got %v", e)
	}
	if _, _, err := procOpenPTY(80, 24); err == nil {
		t.Fatal("procOpenPTY must fail on Windows")
	}
	if err := procSetWinsize(nil, 80, 24); err == nil {
		t.Fatal("procSetWinsize must fail on Windows")
	}
}

func TestProcessWindows_SignalsMapAndNoGroupIsNotAnError(t *testing.T) {
	for tag, want := range map[int]syscall.Signal{0: syscall.SIGINT, 1: syscall.SIGTERM, 2: syscall.SIGKILL, 3: syscall.SIGHUP} {
		got, ok := procSignalNumber(tag)
		if !ok || got != want {
			t.Fatalf("procSignalNumber(%d) = %v, %v", tag, got, ok)
		}
	}
	if _, ok := procSignalNumber(9); ok {
		t.Fatal("an unknown signal tag must be refused")
	}
	if err := procSignalGroup(nil, syscall.SIGTERM); err != nil {
		t.Fatalf("signalling no process is not an error: %v", err)
	}
	if procSysAttr(true) != nil {
		t.Fatal("there are no process groups or PTY attributes on Windows")
	}
}
