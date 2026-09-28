//go:build darwin && !js

package rt

import (
	"bytes"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// procOpenPTY allocates a pseudo-terminal pair with the standard library only
// (posix_openpt is open("/dev/ptmx"); grantpt / unlockpt / ptsname are the
// TIOCPTYGRANT / TIOCPTYUNLK / TIOCPTYGNAME ioctls, which is what libc does).
func procOpenPTY(cols, rows int) (master, slave *os.File, err error) {
	master, err = procOpenFD("/dev/ptmx", syscall.O_RDWR|syscall.O_NOCTTY)
	if err != nil {
		return nil, nil, err
	}
	if err = ptyIoctl(master.Fd(), syscall.TIOCPTYGRANT, 0); err != nil {
		master.Close()
		return nil, nil, fmt.Errorf("grantpt: %w", err)
	}
	if err = ptyIoctl(master.Fd(), syscall.TIOCPTYUNLK, 0); err != nil {
		master.Close()
		return nil, nil, fmt.Errorf("unlockpt: %w", err)
	}
	var name [128]byte
	if err = ptyIoctl(master.Fd(), syscall.TIOCPTYGNAME, uintptr(unsafe.Pointer(&name[0]))); err != nil {
		master.Close()
		return nil, nil, fmt.Errorf("ptsname: %w", err)
	}
	path := string(name[:bytes.IndexByte(name[:], 0)])
	slave, err = procOpenFD(path, syscall.O_RDWR|syscall.O_NOCTTY)
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
