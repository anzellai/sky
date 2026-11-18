//go:build darwin && !js

package rt

// process_tree_darwin.go — the macOS half of the tree sweep
// (process_tree.go): sysctl kern.proc for the snapshot, kern.procargs2 for
// the environment, and a stop / check / kill for the verified kill.

import (
	"encoding/binary"
	"os"
	"syscall"
	"unsafe"

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
// cannot be forged. A process that cannot be read is procEnvAbsent. While a
// process is in execve its argument area can be briefly empty or cut off,
// which is procEnvUnsure (see procEnvScan).
func procEnvValue(pid int, name string) (string, procEnvState) {
	b, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return "", procEnvAbsent
	}
	if len(b) < 4 {
		return "", procEnvUnsure
	}
	return procEnvScan(b[4:], name)
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

// procIsZombie reports whether pid has exited but is not reaped yet (SZOMB).
func procIsZombie(pid int) bool {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	return err == nil && int(k.Proc.P_pid) == pid && k.Proc.P_stat == 5 // SZOMB
}

// ── the descriptor witness ────────────────────────────────────────────────
//
// macOS 27 shows another process's arguments through kern.procargs2 (and
// `ps -E`) but no environment when that process runs an Apple platform
// binary (/bin/sleep, /bin/sh, perl, …), so the tree cookie cannot be read
// there. Measured on macOS 27.2: a `/bin/sleep` child gives
// "\x02\x00\x00\x00/bin/sleep\x00…sleep\x0030\x00" and nothing after it,
// while a program that is not Apple's shows its whole environment. A
// double-forked descendant that ran such a program was then not found.
//
// Its descriptors stay readable (proc_info, what lsof reads). A descendant
// inherits the child's stdout and stderr pipes, or its PTY slave, and keeps
// them across fork, setsid and exec unless it closes them. Only the child's
// tree can hold the write end of those pipes (the runtime's own copies are
// close-on-exec) or the slave of that PTY, so a process that holds one is in
// the tree. The marks are taken at spawn and stay valid while the runtime
// holds the read ends and the master. shutdown closes those only after the
// sweep, so until then the kernel cannot give the pipe or the terminal to
// another process.

const (
	darwinProcInfoCallPidInfo   = 2 // PROC_INFO_CALL_PIDINFO
	darwinProcInfoCallPidFdInfo = 3 // PROC_INFO_CALL_PIDFDINFO
	darwinProcPidListFds        = 1 // PROC_PIDLISTFDS
	darwinProcPidFdVnodeInfo    = 1 // PROC_PIDFDVNODEINFO
	darwinProcPidFdPipeInfo     = 6 // PROC_PIDFDPIPEINFO
	darwinFdTypeVnode           = 1 // PROX_FDTYPE_VNODE
	darwinFdTypePipe            = 6 // PROX_FDTYPE_PIPE

	// Sizes and offsets from <sys/proc_info.h>: struct proc_fdinfo (8),
	// struct proc_fileinfo (24), struct vinfo_stat (136),
	// struct vnode_fdinfo = proc_fileinfo + vnode_info (24 + 152),
	// struct pipe_fdinfo = proc_fileinfo + pipe_info (24 + 160).
	darwinProcFdInfoSize   = 8
	darwinVnodeFdInfoSize  = 176
	darwinPipeFdInfoSize   = 184
	darwinVstModeOffset    = 24 + 4   // vinfo_stat.vst_mode
	darwinVstRdevOffset    = 24 + 116 // vinfo_stat.vst_rdev
	darwinPipeHandleOffset = 24 + 136 // pipe_info.pipe_handle
	darwinPipePeerOffset   = 24 + 144 // pipe_info.pipe_peerhandle
)

func darwinProcInfo(call, pid, flavor int, arg uint64, buf []byte) (int, bool) {
	var p unsafe.Pointer
	if len(buf) > 0 {
		p = unsafe.Pointer(&buf[0])
	}
	r, _, e := unix.Syscall6(unix.SYS_PROC_INFO, uintptr(call), uintptr(pid), uintptr(flavor), uintptr(arg), uintptr(p), uintptr(len(buf)))
	if e != 0 {
		return 0, false
	}
	return int(r), true
}

// darwinPipeHandles is the kernel's handle of one pipe end and of its peer.
func darwinPipeHandles(pid, fd int) (handle, peer uint64, ok bool) {
	var buf [darwinPipeFdInfoSize]byte
	n, ok := darwinProcInfo(darwinProcInfoCallPidFdInfo, pid, darwinProcPidFdPipeInfo, uint64(fd), buf[:])
	if !ok || n != len(buf) {
		return 0, 0, false
	}
	return binary.LittleEndian.Uint64(buf[darwinPipeHandleOffset:]), binary.LittleEndian.Uint64(buf[darwinPipePeerOffset:]), true
}

// darwinCharRdev is the device number of a character-device descriptor.
func darwinCharRdev(pid, fd int) (uint64, bool) {
	var buf [darwinVnodeFdInfoSize]byte
	n, ok := darwinProcInfo(darwinProcInfoCallPidFdInfo, pid, darwinProcPidFdVnodeInfo, uint64(fd), buf[:])
	if !ok || n != len(buf) {
		return 0, false
	}
	if binary.LittleEndian.Uint16(buf[darwinVstModeOffset:])&unix.S_IFMT != unix.S_IFCHR {
		return 0, false
	}
	return uint64(binary.LittleEndian.Uint32(buf[darwinVstRdevOffset:])), true
}

// procFdMarksOf records the descriptors only the child's tree holds: the
// child-side end (the peer) of each of our read pipes, and the PTY slave.
func procFdMarksOf(readEnds []*os.File, ptySlave *os.File) procFdMarks {
	var m procFdMarks
	self := os.Getpid()
	for _, f := range readEnds {
		if f == nil {
			continue
		}
		rc, err := f.SyscallConn()
		if err != nil {
			continue
		}
		_ = rc.Control(func(fd uintptr) {
			if _, peer, ok := darwinPipeHandles(self, int(fd)); ok && peer != 0 {
				m.pipes = append(m.pipes, peer)
			}
		})
	}
	if ptySlave != nil {
		if rc, err := ptySlave.SyscallConn(); err == nil {
			_ = rc.Control(func(fd uintptr) {
				var st unix.Stat_t
				if unix.Fstat(int(fd), &st) == nil && uint32(st.Mode)&unix.S_IFMT == unix.S_IFCHR {
					m.ttys = append(m.ttys, uint64(uint32(st.Rdev)))
				}
			})
		}
	}
	return m
}

// procHoldsFdMark reports whether pid holds one of the marked descriptors.
func procHoldsFdMark(pid int, pipes, ttys map[uint64]bool) bool {
	if len(pipes) == 0 && len(ttys) == 0 {
		return false
	}
	// A nil buffer asks for the size. The list can grow between the calls,
	// and a descriptor opened after the second call is not seen: the sweep's
	// later rounds read the process again.
	need, ok := darwinProcInfo(darwinProcInfoCallPidInfo, pid, darwinProcPidListFds, 0, nil)
	if !ok || need <= 0 {
		return false
	}
	buf := make([]byte, need+32*darwinProcFdInfoSize)
	n, ok := darwinProcInfo(darwinProcInfoCallPidInfo, pid, darwinProcPidListFds, 0, buf)
	if !ok {
		return false
	}
	for i := 0; i+darwinProcFdInfoSize <= n; i += darwinProcFdInfoSize {
		fd := int(int32(binary.LittleEndian.Uint32(buf[i:])))
		switch binary.LittleEndian.Uint32(buf[i+4:]) {
		case darwinFdTypePipe:
			if len(pipes) > 0 {
				if h, _, ok := darwinPipeHandles(pid, fd); ok && pipes[h] {
					return true
				}
			}
		case darwinFdTypeVnode:
			if len(ttys) > 0 {
				if rdev, ok := darwinCharRdev(pid, fd); ok && ttys[rdev] {
					return true
				}
			}
		}
	}
	return false
}
