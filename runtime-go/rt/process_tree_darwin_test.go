//go:build darwin

package rt

import "golang.org/x/sys/unix"

// procStopped reports whether pid is stopped (SSTOP).
func procStopped(pid int) bool {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	return err == nil && k.Proc.P_stat == 4
}
