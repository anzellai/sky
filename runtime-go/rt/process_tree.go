//go:build unix && !js

package rt

// process_tree.go — ending a child's WHOLE tree (D-1).
//
// The runtime used to end a child with one signal to its process group. With
// a PTY the child is a session leader, and an interactive shell puts every
// job in a group of its own: when the shell dies the kernel hangs up only the
// FOREGROUND group, so `tail -f log &` or `npm run dev &` typed into a
// Std.Ui.Terminal outlived Process.close, the session and the program, holding
// its files and ports with nothing left able to reach it. Any descendant that
// called setsid / setpgid escaped the same way, PTY or not.
//
// A sweep finds a child's descendants three ways, in a snapshot of the
// processes this user runs taken BEFORE anything is killed:
//
//  1. the parent links (ppid) from the child down: valid only in that first
//     snapshot, because once the child dies its children are re-parented;
//  2. the child's session and group (sid / pgid == the child's pid): the jobs
//     of an interactive shell;
//  3. an environment cookie (procTreeCookieEnv): every child started without
//     withClearEnv carries a random value in its environment, which its
//     descendants inherit, so a descendant that left the session and was
//     re-parented is still found.
//
// Later rounds (a process may fork while the first round kills) match only by
// session and cookie, never by parent link: once the child is dead its pid
// can be reused, and "ppid == X" would name strangers.
//
// Every kill is verified against PID reuse between the snapshot and the
// signal: Linux signals through a pidfd opened on the process and checks its
// start time (process_tree_linux.go); macOS stops the process, re-reads its
// start time, and continues it again on a mismatch (process_tree_darwin.go).
// A platform that cannot list processes kills nothing beyond the group (fail
// closed). There is no subreaper: the runtime never waits on a process it did
// not start, so the exit status of every direct child (the embedded
// PostgreSQL postmaster included) still reaches its own Wait.
//
// Residual, stated in AGENTS.md: a descendant that clears its environment,
// leaves the session and double-forks (a deliberate daemon) cannot be told
// apart from any other process of the same user, and is not killed.
//
// Sweeps are batched (procSweep): the shutdowns that App.stop, a session's
// end and the exit drain start concurrently share one snapshot.

import (
	cryptorand "crypto/rand"
	"encoding/hex"
	"os"
	"sync"
	"syscall"
	"time"
)

// procTreeCookieEnv is the environment variable that carries a child's tree
// cookie. It has no SKY_ prefix on purpose: it is not a setting.
const procTreeCookieEnv = "SKYPROC_TREE"

// procTreeRounds bounds the sweep: round 1 plus the rounds that catch a
// process forked while round 1 killed.
const procTreeRounds = 4

// procInfo is one process in a snapshot.
type procInfo struct {
	pid, ppid, pgid, sid int
	start                uint64 // platform start time; 0 when unknown
}

// procTreeRoot is one child whose tree a sweep ends.
type procTreeRoot struct {
	pid    int
	start  uint64 // the child's start time, recorded at spawn
	cookie string // "" for a withClearEnv child
}

func newProcTreeCookie() string {
	var b [16]byte
	if _, err := cryptorand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}

// procTreeSweepNow ends every descendant of the roots (and the roots). See
// the file comment for the rules.
func procTreeSweepNow(roots []procTreeRoot) {
	if len(roots) == 0 {
		return
	}
	self := os.Getpid()
	snap, err := procSnapshot()
	if err != nil {
		return // cannot list processes: fail closed
	}
	rootPid := map[int]procTreeRoot{}
	cookies := map[string]bool{}
	for _, r := range roots {
		rootPid[r.pid] = r
		if r.cookie != "" {
			cookies[r.cookie] = true
		}
	}
	children := map[int][]procInfo{}
	for _, p := range snap {
		children[p.ppid] = append(children[p.ppid], p)
	}
	targets := map[int]procInfo{}
	add := func(p procInfo) {
		if p.pid > 1 && p.pid != self {
			targets[p.pid] = p
		}
	}
	for _, p := range snap {
		if r, ok := rootPid[p.pid]; ok && (r.start == 0 || p.start == 0 || r.start == p.start) {
			add(p)
			// 1. parent links, first snapshot only.
			stack := []int{p.pid}
			for len(stack) > 0 {
				n := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				for _, c := range children[n] {
					if _, seen := targets[c.pid]; !seen && c.pid != self {
						add(c)
						stack = append(stack, c.pid)
					}
				}
			}
		}
	}
	// 2 and 3. session / group, and cookie.
	for _, p := range snap {
		if _, done := targets[p.pid]; done || p.pid <= 1 || p.pid == self {
			continue
		}
		if procTreeMember(p, rootPid, snap, cookies) {
			add(p)
		}
	}
	for _, p := range targets {
		procKillVerified(p)
	}
	for round := 2; round <= procTreeRounds; round++ {
		time.Sleep(10 * time.Millisecond)
		snap, err = procSnapshot()
		if err != nil {
			return
		}
		found := 0
		for _, p := range snap {
			if p.pid <= 1 || p.pid == self {
				continue
			}
			if procTreeMember(p, rootPid, snap, cookies) {
				procKillVerified(p)
				found++
			}
		}
		if found == 0 {
			return
		}
	}
}

// procTreeMember reports whether p belongs to a root's tree by session,
// group or cookie. A session / group match is used only while no OTHER
// process holds the root's pid (if one does, the pid was reused and the
// session with that id is not the child's).
func procTreeMember(p procInfo, roots map[int]procTreeRoot, snap []procInfo, cookies map[string]bool) bool {
	for _, id := range [2]int{p.sid, p.pgid} {
		r, ok := roots[id]
		if !ok {
			continue
		}
		if procPidReused(r, snap) {
			continue
		}
		return true
	}
	if len(cookies) == 0 {
		return false
	}
	// A process that started before every root cannot descend from one:
	// skip reading its environment (the common case, and the costly one).
	if p.start != 0 && procRootsMinStart(roots) != 0 && p.start < procRootsMinStart(roots) {
		return false
	}
	c, ok := procEnvValue(p.pid, procTreeCookieEnv)
	return ok && cookies[c]
}

// procRootsMinStart is the earliest known start time among the roots (0 when
// none is known).
func procRootsMinStart(roots map[int]procTreeRoot) uint64 {
	var min uint64
	for _, r := range roots {
		if r.start != 0 && (min == 0 || r.start < min) {
			min = r.start
		}
	}
	return min
}

// procPidReused reports whether the root's pid now names a process that
// started at another time.
func procPidReused(r procTreeRoot, snap []procInfo) bool {
	if r.start == 0 {
		return false
	}
	for _, p := range snap {
		if p.pid == r.pid {
			return p.start != 0 && p.start != r.start
		}
	}
	return false
}

// ── batching ──────────────────────────────────────────────────────────────

var procSweeper struct {
	mu      sync.Mutex
	running bool
	pending []procTreeRoot
	waiters []chan struct{}
}

// procSweep ends the tree of `root` and returns when a sweep that included it
// has finished. Concurrent callers share one sweep.
func procSweep(root procTreeRoot) {
	done := make(chan struct{})
	procSweeper.mu.Lock()
	procSweeper.pending = append(procSweeper.pending, root)
	procSweeper.waiters = append(procSweeper.waiters, done)
	if !procSweeper.running {
		procSweeper.running = true
		go procSweepLoop()
	}
	procSweeper.mu.Unlock()
	<-done
}

func procSweepLoop() {
	// A short gather window, so the shutdowns one App.stop or one exit drain
	// starts together land in the same sweep.
	time.Sleep(5 * time.Millisecond)
	for {
		procSweeper.mu.Lock()
		roots, waiters := procSweeper.pending, procSweeper.waiters
		procSweeper.pending, procSweeper.waiters = nil, nil
		if len(roots) == 0 {
			procSweeper.running = false
			procSweeper.mu.Unlock()
			return
		}
		procSweeper.mu.Unlock()
		func() {
			defer func() {
				if r := recover(); r != nil {
					LogRecoveredPanic("sky.process", "process tree sweep", r)
				}
			}()
			procTreeSweepNow(roots)
		}()
		for _, w := range waiters {
			close(w)
		}
	}
}

// procKillSignal is what a sweep sends.
const procKillSignal = syscall.SIGKILL
