//go:build linux && !js

package rt

// process_tree_linux.go — the Linux half of the tree sweep (process_tree.go):
// /proc for the snapshot and the environment, and a pidfd for the kill.

import (
	"bytes"
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// procSnapshot lists the processes this user runs, from /proc/<pid>/stat.
func procSnapshot() ([]procInfo, error) {
	d, err := os.Open("/proc")
	if err != nil {
		return nil, err
	}
	defer d.Close()
	names, err := d.Readdirnames(-1)
	if err != nil {
		return nil, err
	}
	uid := uint32(os.Getuid())
	out := make([]procInfo, 0, len(names))
	for _, n := range names {
		pid, err := strconv.Atoi(n)
		if err != nil || pid <= 0 {
			continue
		}
		var st syscall.Stat_t
		if syscall.Stat("/proc/"+n, &st) != nil || st.Uid != uid {
			continue
		}
		if p, ok := procStatInfo(pid); ok {
			out = append(out, p)
		}
	}
	return out, nil
}

// procStatInfo parses /proc/<pid>/stat: after the ")" that ends the command
// name, field 3 is the state, 4 ppid, 5 pgrp, 6 session and 22 starttime.
func procStatInfo(pid int) (procInfo, bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return procInfo{}, false
	}
	i := bytes.LastIndexByte(b, ')')
	if i < 0 || i+2 >= len(b) {
		return procInfo{}, false
	}
	f := strings.Fields(string(b[i+2:]))
	// f[0] is field 3 (state).
	if len(f) < 20 || f[0] == "Z" || f[0] == "X" {
		return procInfo{}, false
	}
	ppid, _ := strconv.Atoi(f[1])
	pgid, _ := strconv.Atoi(f[2])
	sid, _ := strconv.Atoi(f[3])
	start, _ := strconv.ParseUint(f[19], 10, 64)
	return procInfo{pid: pid, ppid: ppid, pgid: pgid, sid: sid, start: start}, true
}

// procEnvValue reads one variable from /proc/<pid>/environ. Any failure is
// "not found" (fail closed).
func procEnvValue(pid int, name string) (string, bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
	if err != nil {
		return "", false
	}
	prefix := []byte(name + "=")
	for _, kv := range bytes.Split(b, []byte{0}) {
		if bytes.HasPrefix(kv, prefix) {
			return string(kv[len(prefix):]), true
		}
	}
	return "", false
}

// procKillVerified kills p only if it is still the process the snapshot saw:
// a pidfd pins the process, then its start time is compared. Without pidfd
// support (a kernel before 5.3) the start-time check is made just before a
// plain kill.
func procKillVerified(p procInfo) {
	fd, err := unix.PidfdOpen(p.pid, 0)
	if err != nil {
		if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EPERM) {
			if now, ok := procStatInfo(p.pid); ok && now.start == p.start {
				_ = syscall.Kill(p.pid, procKillSignal)
			}
		}
		return // ESRCH: already gone
	}
	defer unix.Close(fd)
	if now, ok := procStatInfo(p.pid); !ok || now.start != p.start {
		return // the pid names another process now
	}
	_ = unix.PidfdSendSignal(fd, procKillSignal, nil, 0)
}

// procStartTime is a process's start time (for the root of a sweep).
func procStartTime(pid int) uint64 {
	if p, ok := procStatInfo(pid); ok {
		return p.start
	}
	return 0
}
