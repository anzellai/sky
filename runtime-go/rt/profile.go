// profile.go — opt-in runtime profiling for `sky run --profile`.
//
// A Sky app hanging or spiking CPU/memory is otherwise opaque to a dev without
// reaching for external Go tooling. `sky run --profile` sets SKY_PROFILE_DIR (and
// optionally SKY_PROFILE_TIMEOUT) in the app's environment. This file's `init`
// starts profiling when that env is set; the stop is folded into the
// `rt.LogPanicAndExit` that every generated `func main()` already defers, so the
// EMITTED Go is byte-identical whether or not profiling is used (no codegen
// change → the Rust-vs-oracle output parity gates are untouched). When the env is
// unset, `init` returns immediately — zero overhead off the profiling path.
//
// What it captures, written to SKY_PROFILE_DIR:
//   - cpu.pprof         — CPU profile for the whole run (go tool pprof)
//   - heap.pprof        — heap profile at stop
//   - goroutines.txt    — full goroutine stack dump (pprof debug=2)
//   - REPORT.md         — human-readable summary: reason (exit / panic / signal /
//     hang), wall time, goroutine count + state breakdown, and
//     a HANG verdict when goroutines sit blocked.
//
// Three ways it stops, whichever fires first (guarded by sync.Once so profiles
// are written exactly once):
//  1. main returns / panics  → LogPanicAndExit calls stopProfiling (reason
//     "exit" / "panic").
//  2. SIGINT/SIGTERM/SIGQUIT  → watchdog writes profiles, exits 130.
//  3. SKY_PROFILE_TIMEOUT elapses with the app still running → watchdog writes
//     profiles with a HANG verdict + the goroutine dump (where it's stuck),
//     exits 1. Opt-in: an unset timeout never fires (a server "hangs" by design).
package rt

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/pprof"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

var (
	profileDir     string
	profileCPUFile *os.File
	profileStart   time.Time
	profileOnce    sync.Once
	profileActive  bool
)

// init starts CPU profiling + the stop watchdog when SKY_PROFILE_DIR is set.
// A no-op otherwise. Runs before main (Go package initialisation), so the CPU
// profile spans the whole run.
func init() {
	dir := os.Getenv("SKY_PROFILE_DIR")
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "sky profile: cannot create %s: %v\n", dir, err)
		return
	}
	profileDir = dir
	profileActive = true
	profileStart = time.Now()

	if f, err := os.Create(filepath.Join(dir, "cpu.pprof")); err == nil {
		if perr := pprof.StartCPUProfile(f); perr == nil {
			profileCPUFile = f
		} else {
			_ = f.Close()
		}
	}

	var timeout time.Duration
	if s := os.Getenv("SKY_PROFILE_TIMEOUT"); s != "" {
		if d, err := time.ParseDuration(s); err == nil {
			timeout = d
		} else {
			fmt.Fprintf(os.Stderr, "sky profile: bad SKY_PROFILE_TIMEOUT %q: %v\n", s, err)
		}
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT)
	go func() {
		var timer <-chan time.Time
		if timeout > 0 {
			timer = time.After(timeout)
		}
		select {
		case s := <-sigCh:
			writeProfilesOnce(fmt.Sprintf("signal %v", s))
			os.Exit(130)
		case <-timer:
			writeProfilesOnce(fmt.Sprintf("hang — no exit after %s", timeout))
			os.Exit(1)
		}
	}()
}

// stopProfiling flushes the profiles on the normal main-exit / panic path. Called
// from LogPanicAndExit (which every generated main defers). No-op when profiling
// is inactive; the sync.Once makes it safe against a racing watchdog.
func stopProfiling(reason string) {
	if !profileActive {
		return
	}
	writeProfilesOnce(reason)
}

func writeProfilesOnce(reason string) {
	profileOnce.Do(func() { writeProfiles(reason) })
}

// writeProfiles flushes CPU, heap, and a full goroutine dump, then renders
// REPORT.md. Best-effort: a failure on any single artefact still writes the rest.
func writeProfiles(reason string) {
	elapsed := time.Since(profileStart)
	if profileCPUFile != nil {
		pprof.StopCPUProfile()
		_ = profileCPUFile.Close()
	}
	if f, err := os.Create(filepath.Join(profileDir, "heap.pprof")); err == nil {
		runtime.GC() // up-to-date heap statistics
		_ = pprof.WriteHeapProfile(f)
		_ = f.Close()
	}

	var goroutineDump strings.Builder
	if p := pprof.Lookup("goroutine"); p != nil {
		_ = p.WriteTo(&goroutineDump, 2) // debug=2 → full stacks
	}
	dump := goroutineDump.String()
	_ = os.WriteFile(filepath.Join(profileDir, "goroutines.txt"), []byte(dump), 0o644)

	report := renderReport(reason, elapsed, dump)
	_ = os.WriteFile(filepath.Join(profileDir, "REPORT.md"), []byte(report), 0o644)

	fmt.Fprintf(os.Stderr, "\nsky profile: wrote %s (%s)\n", profileDir, reason)
}

var goroutineHeader = regexp.MustCompile(`^goroutine \d+ \[([^,\]]+)`)
var goroutineLongBlock = regexp.MustCompile(`, \d+ minutes\]`)

// renderReport builds the human-readable REPORT.md: reason, wall time, goroutine
// count + a state breakdown, a HANG verdict when goroutines sit blocked, and how
// to open the pprof files.
func renderReport(reason string, elapsed time.Duration, dump string) string {
	states := map[string]int{}
	total := 0
	longBlocked := 0
	for _, line := range strings.Split(dump, "\n") {
		m := goroutineHeader.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		total++
		states[strings.TrimSpace(m[1])]++
		if goroutineLongBlock.MatchString(line) {
			longBlocked++
		}
	}

	type kv struct {
		state string
		n     int
	}
	ordered := make([]kv, 0, len(states))
	for s, n := range states {
		ordered = append(ordered, kv{s, n})
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].n != ordered[j].n {
			return ordered[i].n > ordered[j].n
		}
		return ordered[i].state < ordered[j].state
	})

	var b strings.Builder
	fmt.Fprintf(&b, "# Sky runtime profile\n\n")
	fmt.Fprintf(&b, "- **Reason:** %s\n", reason)
	fmt.Fprintf(&b, "- **Wall time:** %s\n", elapsed.Round(time.Millisecond))
	fmt.Fprintf(&b, "- **Goroutines:** %d\n\n", total)

	// A hang verdict: the run stopped on the timeout watchdog, OR goroutines have
	// been blocked for whole minutes (Go annotates long waits in the dump).
	if strings.HasPrefix(reason, "hang") || longBlocked > 0 {
		fmt.Fprintf(&b, "## ⚠️ Likely hang\n\n")
		if strings.HasPrefix(reason, "hang") {
			fmt.Fprintf(&b, "The app did not exit within the profile timeout. ")
		}
		if longBlocked > 0 {
			fmt.Fprintf(&b, "%d goroutine(s) have been blocked for minutes. ", longBlocked)
		}
		fmt.Fprintf(&b, "See the blocked stacks in `goroutines.txt` — the top frame of each blocked goroutine is where it is stuck (channel receive, mutex, network read, …).\n\n")
	}

	fmt.Fprintf(&b, "## Goroutine states\n\n")
	fmt.Fprintf(&b, "| State | Count |\n|---|---|\n")
	for _, e := range ordered {
		fmt.Fprintf(&b, "| %s | %d |\n", e.state, e.n)
	}
	fmt.Fprintf(&b, "\n> States like `chan receive`, `select`, `IO wait`, `semacquire` dominating a hung app show where it is waiting. `running`/`runnable` dominating a CPU spike points at hot code — open `cpu.pprof`.\n\n")

	fmt.Fprintf(&b, "## Open the profiles\n\n")
	fmt.Fprintf(&b, "```sh\ngo tool pprof -http=: cpu.pprof     # CPU flame graph\ngo tool pprof -http=: heap.pprof    # heap allocations\n```\n\n")
	fmt.Fprintf(&b, "`goroutines.txt` is a plain-text full stack dump — grep it for your module (`Main_`, `Std_`) to find your own frames.\n")
	return b.String()
}
