//go:build darwin && !js

package rt

// process_tree_darwin.go — the macOS half of the tree sweep
// (process_tree.go): sysctl kern.proc for the snapshot, kern.procargs2 for
// the environment, and a stop / check / kill for the verified kill.

import (
	"bytes"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func darwinStart(k *unix.KinfoProc) uint64 {
	tv := k.Proc.P_starttime
	return uint64(tv.Sec)*1_000_000 + uint64(tv.Usec)
}

// procSnapshot lists the processes this user runs.
func procSnapshot() ([]procInfo, error) {
	all, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	uid := uint32(os.Getuid())
	out := make([]procInfo, 0, len(all))
	for i := range all {
		k := &all[i]
		pid := int(k.Proc.P_pid)
		if pid <= 0 || k.Eproc.Pcred.P_ruid != uid {
			continue
		}
		if k.Proc.P_stat == 5 { // SZOMB
			continue
		}
		sid, err := unix.Getsid(pid)
		if err != nil {
			sid = -1
		}
		out = append(out, procInfo{
			pid:   pid,
			ppid:  int(k.Eproc.Ppid),
			pgid:  int(k.Eproc.Pgid),
			sid:   sid,
			start: darwinStart(k),
		})
	}
	return out, nil
}

// procEnvValue reads one variable from a process's environment through
// kern.procargs2 (what `ps -E` reads): an int32 argc, the executable path,
// NUL padding, the arguments, then the environment. The strings are NOT
// parsed by position: a process that rewrites its argument area (perl does,
// for $0) leaves extra NULs between the arguments and the environment, so
// every NUL-separated string after the header is checked for `name=`. The
// cookie is a random value only a descendant carries, so a match elsewhere
// cannot be forged. Any failure is "not found" (fail closed).
func procEnvValue(pid int, name string) (string, bool) {
	b, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil || len(b) < 4 {
		return "", false
	}
	prefix := []byte(name + "=")
	for _, s := range bytes.Split(b[4:], []byte{0}) {
		if bytes.HasPrefix(s, prefix) {
			return string(s[len(prefix):]), true
		}
	}
	return "", false
}

// procStartTime is a process's start time (for the root of a sweep).
func procStartTime(pid int) uint64 {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || int(k.Proc.P_pid) != pid {
		return 0
	}
	return darwinStart(k)
}

// procKillVerified kills p only if it is still the process the snapshot saw.
// macOS has no pidfd, so the process is stopped first (a stopped process
// cannot exit and have its pid reused), its start time is read again, and it
// is killed on a match or continued on a mismatch.
func procKillVerified(p procInfo) {
	if syscall.Kill(p.pid, syscall.SIGSTOP) != nil {
		return // gone, or not ours
	}
	if procStartTime(p.pid) != p.start {
		_ = syscall.Kill(p.pid, syscall.SIGCONT)
		return
	}
	_ = syscall.Kill(p.pid, procKillSignal)
}
