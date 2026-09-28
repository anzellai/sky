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
	master, err = procOpenFD("/dev/ptmx", syscall.O_RDWR|syscall.O_NOCTTY)
	if err != nil {
		return nil, nil, err
	}
	unlock := int32(0)
	if err = ptyIoctl(master.Fd(), syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock))); err != nil {
		master.Close()
		return nil, nil, fmt.Errorf("unlockpt: %w", err)
	}
	var n uint32
	if err = ptyIoctl(master.Fd(), syscall.TIOCGPTN, uintptr(unsafe.Pointer(&n))); err != nil {
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
