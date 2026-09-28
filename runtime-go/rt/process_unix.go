//go:build !js && unix

package rt

// process_unix.go — the Unix half of Sky.Core.Process: process groups, signal
// delivery to the whole group, exit-status decoding, and PTY resize. PTY
// allocation itself is per-OS (process_pty_linux.go, process_pty_darwin.go,
// process_pty_other.go).

import (
	"errors"
	"os"
	"os/signal"
	"syscall"
	"unsafe"
)

// sysSignalPid sends sig to one process (sig 0 probes that it exists).
func sysSignalPid(pid int, sig syscall.Signal) error { return syscall.Kill(pid, sig) }

// notifyWindowResize relays terminal resizes (SIGWINCH) to ch.
func notifyWindowResize(ch chan<- os.Signal) { signal.Notify(ch, syscall.SIGWINCH) }

// procSysAttr puts the child in a process group of its own, so a signal sent
// to the group reaches every grandchild the child starts (a shell pipeline, a
// build tool's workers). With a PTY the child also becomes a session leader
// with the PTY as its controlling terminal (Setsid implies a new group).
func procSysAttr(pty bool) *syscall.SysProcAttr {
	if pty {
		return &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	}
	return &syscall.SysProcAttr{Setpgid: true}
}

// procSignalNumber maps the Sky `Signal` tag to the OS signal.
func procSignalNumber(tag int) (syscall.Signal, bool) {
	switch tag {
	case 0:
		return syscall.SIGINT, true
	case 1:
		return syscall.SIGTERM, true
	case 2:
		return syscall.SIGKILL, true
	case 3:
		return syscall.SIGHUP, true
	}
	return 0, false
}

// procSignalGroup sends sig to the child's whole process group. The group id
// is the child's pid (Setpgid / Setsid make it so). A group that is already
// gone is not an error: the caller wanted it dead and it is.
func procSignalGroup(p *os.Process, sig syscall.Signal) error {
	if p == nil {
		return nil
	}
	err := syscall.Kill(-p.Pid, sig)
	if err == nil || errors.Is(err, syscall.ESRCH) {
		return nil
	}
	// EPERM on the group can happen on some systems when every member has
	// already exited but the leader is a zombie; fall back to the leader.
	if err2 := p.Signal(sig); err2 == nil || errors.Is(err2, os.ErrProcessDone) {
		return nil
	}
	return err
}

// procExitInfo decodes a finished child's status: an exit code, or the
// signal that terminated it (signalled = true).
func procExitInfo(ps *os.ProcessState) (code int, signal int, signalled bool) {
	if ps == nil {
		return -1, 0, false
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return -1, int(ws.Signal()), true
	}
	return ps.ExitCode(), 0, false
}

type ptyWinsize struct {
	Row    uint16
	Col    uint16
	Xpixel uint16
	Ypixel uint16
}

// procSetWinsize sets a PTY's window size (TIOCSWINSZ). The kernel sends
// SIGWINCH to the foreground process group of the terminal.
func procSetWinsize(f *os.File, cols, rows int) error {
	ws := ptyWinsize{Row: uint16(rows), Col: uint16(cols)}
	return ptyIoctl(f.Fd(), syscall.TIOCSWINSZ, uintptr(unsafe.Pointer(&ws)))
}

func ptyIoctl(fd uintptr, req uint, arg uintptr) error {
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(req), arg)
	if e != 0 {
		return e
	}
	return nil
}

// ptyIsEOF reports whether a read error on a PTY master means "the other side
// closed": Linux answers EIO once every slave descriptor is closed.
func ptyIsEOF(err error) bool {
	return errors.Is(err, syscall.EIO)
}

// procOpenFD opens path as a BLOCKING descriptor wrapped in *os.File. A PTY is
// deliberately kept out of the Go poller: macOS kqueue does not report
// readiness on PTY devices, so a poller-registered master would never wake.
// Blocking reads run on their own thread; a read returns when the child side
// closes, which the kill in close() guarantees.
func procOpenFD(path string, flags int) (*os.File, error) {
	fd, err := syscall.Open(path, flags|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(fd), path), nil
}
