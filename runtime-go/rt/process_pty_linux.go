//go:build linux && !js

package rt

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// procOpenPTY allocates a pseudo-terminal pair with the standard library only
// (posix_openpt is open("/dev/ptmx"); unlockpt is TIOCSPTLCK; ptsname is
// TIOCGPTN). Returns the master (kept by the runtime) and the slave (given to
// the child as stdin/stdout/stderr, then closed in the parent).
func procOpenPTY(cols, rows int) (master, slave *os.File, err error) {
	// O_NONBLOCK puts the master in the Go poller (os.NewFile sees a
	// non-blocking descriptor): close then interrupts the pump's read even
	// while another process holds the slave (D-5). Linux epoll reports PTY
	// readiness; macOS kqueue does not, which is why darwin stays blocking.
	master, err = procOpenFD("/dev/ptmx", syscall.O_RDWR|syscall.O_NOCTTY|syscall.O_NONBLOCK)
	if err != nil {
		return nil, nil, err
	}
	unlock := int32(0)
	if err = ptyFileIoctl(master, syscall.TIOCSPTLCK, unsafe.Pointer(&unlock)); err != nil {
		master.Close()
		return nil, nil, fmt.Errorf("unlockpt: %w", err)
	}
	var n uint32
	if err = ptyFileIoctl(master, syscall.TIOCGPTN, unsafe.Pointer(&n)); err != nil {
		master.Close()
		return nil, nil, fmt.Errorf("ptsname: %w", err)
	}
	slave, err = procOpenFD(fmt.Sprintf("/dev/pts/%d", n), syscall.O_RDWR|syscall.O_NOCTTY)
	if err != nil {
		master.Close()
		return nil, nil, err
	}
	if cols > 0 && rows > 0 {
		if err = procSetWinsize(slave, cols, rows); err != nil {
			master.Close()
			slave.Close()
			return nil, nil, fmt.Errorf("set window size: %w", err)
		}
	}
	return master, slave, nil
}
