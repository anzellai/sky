//go:build !js && !unix

package rt

// process_nounix.go — Sky.Core.Process on a non-Unix OS (Windows). Plain
// pipes work; a PTY and process-group signalling do not, and say so with an
// Err rather than a panic.

import (
	"errors"
	"os"
	"syscall"
)

func procSysAttr(pty bool) *syscall.SysProcAttr { return nil }

// sysSignalPid sends sig to one process. Signal 0 probes existence (finding
// the process succeeds only while it exists); Kill ends it; any other signal
// is what the OS refuses, reported as an error.
func sysSignalPid(pid int, sig syscall.Signal) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	switch sig {
	case 0:
		return nil
	case syscall.SIGKILL:
		return p.Kill()
	}
	return p.Signal(sig)
}

// notifyWindowResize: there is no resize signal on this OS.
func notifyWindowResize(ch chan<- os.Signal) {}

// procSignalNumber maps the Sky `Signal` tag. Windows delivers only Kill.
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

// procSignalGroup: there are no process groups here; Kill ends the child,
// every other signal is refused by the OS and reported as an error.
func procSignalGroup(p *os.Process, sig syscall.Signal) error {
	if p == nil {
		return nil
	}
	var err error
	if sig == syscall.SIGKILL {
		err = p.Kill()
	} else {
		err = p.Signal(sig)
	}
	if err == nil || errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

func procExitInfo(ps *os.ProcessState) (code int, signal int, signalled bool) {
	if ps == nil {
		return -1, 0, false
	}
	return ps.ExitCode(), 0, false
}

func procOpenPTY(cols, rows int) (master, slave *os.File, err error) {
	return nil, nil, errors.New("a PTY is supported on Linux and macOS only")
}

func procSetWinsize(f *os.File, cols, rows int) error {
	return errors.New("a PTY is supported on Linux and macOS only")
}

func ptyIsEOF(err error) bool { return false }
