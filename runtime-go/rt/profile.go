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
// The headline is REPORT.md: a PLAIN-LANGUAGE, SKY-NAMED summary — no raw Go
// symbols. Every frame is demangled back to `Module.function` (`Main_update` →
// `Main.update`), reflection/coercion/GC frames are collapsed into a single
// "runtime overhead" bucket, and it renders verdicts a dev acts on directly:
// where the CPU went, whether the heap looks like a leak, and where a hang is
// stuck. The raw pprof files stay for power users.
//
// Files written to SKY_PROFILE_DIR:
//   - REPORT.md         — the plain-language summary (start here)
//   - cpu.pprof         — CPU profile (go tool pprof -http=: cpu.pprof)
//   - heap.pprof        — heap profile
//   - goroutines.txt    — full raw goroutine stack dump
//
// Three ways it stops, whichever fires first (sync.Once → written exactly once):
//  1. main returns / panics  → LogPanicAndExit calls stopProfiling.
//  2. SIGINT/SIGTERM/SIGQUIT  → watchdog writes profiles, exits 130.
//  3. SKY_PROFILE_TIMEOUT elapses with the app still running → watchdog writes a
//     hang report + the (demangled) blocked stacks, exits 1. Opt-in.
package rt

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/pprof"
	"sort"
	"strconv"
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

	memMu       sync.Mutex
	memSamples  []memSample
	memInitial  memSample
	memHasFirst bool
)

type memSample struct {
	heapAlloc uint64
	numGC     uint32
}

// init starts CPU profiling + the memory sampler + the stop watchdog when
// SKY_PROFILE_DIR is set. A no-op otherwise. Runs before main (Go package
// initialisation), so the CPU profile + memory trend span the whole run.
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

	// Memory trend sampler: cheap ReadMemStats every 250ms → leak detection.
	go func() {
		t := time.NewTicker(250 * time.Millisecond)
		defer t.Stop()
		var ms runtime.MemStats
		for range t.C {
			runtime.ReadMemStats(&ms)
			s := memSample{heapAlloc: ms.HeapAlloc, numGC: ms.NumGC}
			memMu.Lock()
			if !memHasFirst {
				memInitial = s
				memHasFirst = true
			}
			if len(memSamples) < 12000 { // ~50 min cap; then stop growing
				memSamples = append(memSamples, s)
			}
			memMu.Unlock()
		}
	}()

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
		// ExitProcess, not os.Exit: this goroutine ends the process from
		// underneath main's `defer rt.StopEmbeddedPostgres()`, and a profiling
		// run that orphaned the app's database would leave a postmaster the
		// next run adopts forever.
		case s := <-sigCh:
			writeProfilesOnce(fmt.Sprintf("signal %v", s))
			ExitProcess(130)
		case <-timer:
			writeProfilesOnce(fmt.Sprintf("hang — no exit after %s", timeout))
			ExitProcess(1)
		}
	}()
}

// stopProfiling flushes on the normal main-exit / panic path. Called from
// LogPanicAndExit. No-op when profiling is inactive; sync.Once makes it safe
// against a racing watchdog.
func stopProfiling(reason string) {
	if !profileActive {
		return
	}
	writeProfilesOnce(reason)
}

func writeProfilesOnce(reason string) {
	profileOnce.Do(func() { writeProfiles(reason) })
}

func writeProfiles(reason string) {
	elapsed := time.Since(profileStart)
	if profileCPUFile != nil {
		pprof.StopCPUProfile()
		_ = profileCPUFile.Close()
	}
	if f, err := os.Create(filepath.Join(profileDir, "heap.pprof")); err == nil {
		runtime.GC()
		_ = pprof.WriteHeapProfile(f)
		_ = f.Close()
	}

	var dumpB strings.Builder
	if p := pprof.Lookup("goroutine"); p != nil {
		_ = p.WriteTo(&dumpB, 2)
	}
	dump := dumpB.String()
	_ = os.WriteFile(filepath.Join(profileDir, "goroutines.txt"), []byte(dump), 0o644)

	report := renderReport(reason, elapsed, dump)
	_ = os.WriteFile(filepath.Join(profileDir, "REPORT.md"), []byte(report), 0o644)

	fmt.Fprintf(os.Stderr, "\nsky profile: %s — see %s/REPORT.md\n", reason, profileDir)
}

// ─── demangling: Go symbol → Sky name ──────────────────────────────────────

// frameKind classifies a demangled frame so the summary can bucket it.
type frameKind int

const (
	kindUser    frameKind = iota // the dev's own Sky code (main.Mod_fn)
	kindStdlib                   // a Sky stdlib call (rt.Db_query → Db.query)
	kindRuntime                  // Sky runtime plumbing (rt.SkyCall, rt.Coerce, …)
	kindGo                       // Go runtime / reflect / stdlib
)

// demangleSkyName turns `Lib_AuthHandlers_handleSignIn` into
// `Lib.AuthHandlers.handleSignIn`: leading uppercase-initial segments are the
// module path, the first lowercase-initial segment onward is the binding
// (Sky values are lowercase, module segments uppercase — the lowering scheme).
func demangleSkyName(s string) string {
	parts := strings.Split(s, "_")
	i := 0
	for i < len(parts)-1 {
		p := parts[i]
		if p == "" || p[0] < 'A' || p[0] > 'Z' {
			break
		}
		i++
	}
	if i == 0 {
		return s // no module prefix — leave as-is
	}
	return strings.Join(parts[:i], ".") + "." + strings.Join(parts[i:], "_")
}

// closureSuffixRe strips Go's closure markers (`.func1`, `.func1.2`) so a lambda
// inside a Sky function reports as that function, not `…​.func3`.
var closureSuffixRe = regexp.MustCompile(`(\.func\d+)+$`)

// demangleFrame maps a raw pprof/goroutine function symbol to a Sky-friendly
// name + its kind.
func demangleFrame(fn string) (string, frameKind) {
	fn = closureSuffixRe.ReplaceAllString(fn, "")
	if s, ok := strings.CutPrefix(fn, "main."); ok {
		// A closure emitted inside `main` shows as `main.main.X` — strip the
		// second `main.` too.
		s = strings.TrimPrefix(s, "main.")
		if s == "" || s == "main" {
			return "main (entry point)", kindUser
		}
		return demangleSkyName(s), kindUser
	}
	if s, ok := strings.CutPrefix(fn, "sky-app/rt."); ok {
		// A kernel binding (`Db_query`, `Log_println`) demangles to a real Sky
		// stdlib call; single-segment plumbing (`SkyCall`, `Coerce`, `AsListT`,
		// `AnyTaskRun`) is runtime overhead.
		if d := demangleSkyName(s); d != s && strings.Contains(d, ".") {
			return d, kindStdlib
		}
		return "sky runtime (" + s + ")", kindRuntime
	}
	switch {
	case strings.HasPrefix(fn, "reflect."):
		return "reflection dispatch", kindRuntime
	case strings.HasPrefix(fn, "runtime."), strings.HasPrefix(fn, "internal/"),
		strings.HasPrefix(fn, "sync."), strings.HasPrefix(fn, "syscall"):
		return fn, kindGo
	}
	return fn, kindGo
}

// ─── the report ────────────────────────────────────────────────────────────

func renderReport(reason string, elapsed time.Duration, dump string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Sky app profile\n\n")
	fmt.Fprintf(&b, "- **Stopped because:** %s\n", humanReason(reason))
	fmt.Fprintf(&b, "- **Ran for:** %s\n\n", elapsed.Round(time.Millisecond))

	renderMemory(&b)
	renderCPU(&b, elapsed)
	renderGoroutines(&b, reason, dump)

	fmt.Fprintf(&b, "---\n\n_Raw profiles for deeper digging: `go tool pprof -http=: %s/cpu.pprof` (or `heap.pprof`); full stacks in `goroutines.txt`._\n",
		profileDir)
	return b.String()
}

func humanReason(reason string) string {
	switch {
	case reason == "exit":
		return "the app finished normally"
	case reason == "panic":
		return "the app panicked (see the error log above)"
	case strings.HasPrefix(reason, "hang"):
		return reason + " — it was still running when the timeout fired"
	case strings.HasPrefix(reason, "signal"):
		return "interrupted (" + reason + ")"
	default:
		return reason
	}
}

func humanBytes(n uint64) string {
	const u = 1024
	if n < u {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(u), 0
	for m := n / u; m >= u; m /= u {
		div *= u
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

// renderMemory: peak / final heap, GC count, and a leak-or-not verdict from the
// sampled trend.
func renderMemory(b *strings.Builder) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	memMu.Lock()
	samples := append([]memSample(nil), memSamples...)
	initial := memInitial
	hasFirst := memHasFirst
	memMu.Unlock()

	final := ms.HeapAlloc
	var peak uint64 = final
	for _, s := range samples {
		if s.heapAlloc > peak {
			peak = s.heapAlloc
		}
	}
	if !hasFirst {
		initial = memSample{heapAlloc: final, numGC: ms.NumGC}
	}
	gcs := int(ms.NumGC) - int(initial.numGC)
	if gcs < 0 {
		gcs = 0
	}

	fmt.Fprintf(b, "## Memory\n\n")
	fmt.Fprintf(b, "- Heap now: **%s**, peak: **%s**, GC runs: **%d**\n",
		humanBytes(final), humanBytes(peak), gcs)

	grew := final > initial.heapAlloc && (final-initial.heapAlloc) > 16<<20
	stayedHigh := peak > 0 && final*10 >= peak*8 // final ≥ 80% of peak
	switch {
	case grew && stayedHigh && gcs >= 2:
		fmt.Fprintf(b, "- ⚠️ **Possible memory leak.** The heap climbed from %s to %s and stayed there across %d GCs — memory is being held, not reclaimed. Look for a growing `List`/`Dict`/cache in your Model or a subscription that never unsubscribes. `go tool pprof -http=: %s/heap.pprof` shows the allocation sites.\n",
			humanBytes(initial.heapAlloc), humanBytes(final), gcs, profileDir)
	case peak > final*2 && peak > 32<<20:
		fmt.Fprintf(b, "- Heap spiked to %s then came back down to %s — a transient burst, not a leak.\n",
			humanBytes(peak), humanBytes(final))
	default:
		fmt.Fprintf(b, "- Memory looks steady — no leak signal.\n")
	}
	fmt.Fprintf(b, "\n")
}

// cpuLine is one parsed row of `go tool pprof -top`.
type cpuLine struct {
	flatPct float64
	cumPct  float64
	fn      string
}

var pprofTopRe = regexp.MustCompile(`^\s*\S+\s+(\d+(?:\.\d+)?)%\s+\d+(?:\.\d+)?%\s+\S+\s+(\d+(?:\.\d+)?)%\s+(.+?)\s*$`)

// renderCPU: shells `go tool pprof -top` (Sky devs have Go), demangles the
// functions, and reports the dev's hot Sky functions + a single runtime-overhead
// bucket + a verdict. Degrades gracefully when go / samples are unavailable.
func renderCPU(b *strings.Builder, elapsed time.Duration) {
	fmt.Fprintf(b, "## CPU\n\n")

	bin, err := os.Executable()
	if err != nil || bin == "" {
		bin = os.Args[0]
	}
	cpuPath := filepath.Join(profileDir, "cpu.pprof")
	out, err := runPprofTop(bin, cpuPath)
	if err != nil {
		fmt.Fprintf(b, "- CPU breakdown needs the Go toolchain (`go tool pprof`); it wasn't available here. Open it yourself: `go tool pprof -http=: %s`.\n\n", cpuPath)
		return
	}

	var user []cpuLine
	var overhead, goRt float64
	sampled := false
	for _, ln := range strings.Split(out, "\n") {
		m := pprofTopRe.FindStringSubmatch(ln)
		if m == nil {
			continue
		}
		sampled = true
		flat, _ := strconv.ParseFloat(m[1], 64)
		cum, _ := strconv.ParseFloat(m[2], 64)
		name, kind := demangleFrame(m[3])
		switch kind {
		case kindUser, kindStdlib:
			user = append(user, cpuLine{flat, cum, name})
		case kindRuntime:
			overhead += flat
		case kindGo:
			goRt += flat
		}
	}
	if !sampled {
		fmt.Fprintf(b, "- Ran too briefly (%s) to collect CPU samples — nothing hot to report.\n\n", elapsed.Round(time.Millisecond))
		return
	}

	sort.SliceStable(user, func(i, j int) bool { return user[i].flatPct > user[j].flatPct })
	fmt.Fprintf(b, "Where the CPU went (your code, by self-time):\n\n")
	if len(user) == 0 {
		fmt.Fprintf(b, "- None of the hot frames are your code — almost all time is runtime overhead (below).\n")
	} else {
		fmt.Fprintf(b, "| Function | Self | Total (incl. callees) |\n|---|---|---|\n")
		for i, l := range user {
			if i >= 8 {
				break
			}
			fmt.Fprintf(b, "| `%s` | %.1f%% | %.1f%% |\n", l.fn, l.flatPct, l.cumPct)
		}
	}
	fmt.Fprintf(b, "\n- Runtime overhead (reflection / coercion / list-op dispatch): **%.0f%%**; Go runtime + GC: **%.0f%%**.\n", overhead, goRt)

	// Verdict.
	switch {
	case len(user) > 0 && user[0].flatPct >= 30:
		fmt.Fprintf(b, "- **CPU is concentrated in `%s` (%.0f%% of self-time).** That's your hot path — optimise there first.\n",
			user[0].fn, user[0].flatPct)
	case overhead >= 40:
		fmt.Fprintf(b, "- **Most CPU is runtime overhead**, not your logic — typically heavy higher-order/list ops (`map`/`filter`/`foldl`) over large data going through reflective dispatch. Reducing the data size or the number of passes helps most.\n")
	default:
		fmt.Fprintf(b, "- CPU is spread out — no single hot spot.\n")
	}
	fmt.Fprintf(b, "\n")
}

func runPprofTop(bin, cpuPath string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "tool", "pprof", "-top", "-nodecount=40", bin, cpuPath)
	out, err := cmd.Output()
	return string(out), err
}

// renderGoroutines: count states, and for a hang show WHERE each blocked
// goroutine is stuck — in Sky names, the first frame of the dev's own code.
func renderGoroutines(b *strings.Builder, reason, dump string) {
	blocks := splitGoroutineBlocks(dump)
	states := map[string]int{}
	type stuck struct {
		state string
		where string
	}
	var stuckList []stuck
	longBlocked := 0
	for _, blk := range blocks {
		state := blk.state
		short := strings.SplitN(state, ",", 2)[0]
		states[short]++
		if strings.Contains(state, "minutes") {
			longBlocked++
		}
		// Surface a goroutine only if it runs the dev's OWN code — that skips the
		// profiler's own watchdog/sampler goroutines and pure Go-runtime workers,
		// which aren't what a dev's hang is about. Report whatever state it's in
		// (a stdin/network read parks in `syscall`/`IO wait`, a stuck Task in
		// `chan receive`), with the dev's top frame.
		if where, ok := userFrame(blk.frames); ok {
			stuckList = append(stuckList, stuck{state: state, where: where})
		}
	}

	hang := strings.HasPrefix(reason, "hang") || longBlocked > 0
	fmt.Fprintf(b, "## Goroutines & hangs\n\n")
	fmt.Fprintf(b, "- %d goroutine(s):", len(blocks))
	keys := make([]string, 0, len(states))
	for s := range states {
		keys = append(keys, s)
	}
	sort.Slice(keys, func(i, j int) bool {
		if states[keys[i]] != states[keys[j]] {
			return states[keys[i]] > states[keys[j]]
		}
		return keys[i] < keys[j]
	})
	parts := make([]string, 0, len(keys))
	for _, s := range keys {
		parts = append(parts, fmt.Sprintf("%d %s", states[s], s))
	}
	fmt.Fprintf(b, " %s.\n", strings.Join(parts, ", "))

	if hang {
		fmt.Fprintf(b, "\n### ⚠️ Where it's stuck\n\n")
		if len(stuckList) == 0 {
			fmt.Fprintf(b, "The app didn't finish in time, but nothing is blocked — it's likely stuck in a tight loop (CPU-bound). See the CPU section above.\n")
		} else {
			shown := 0
			for _, s := range stuckList {
				if shown >= 6 {
					break
				}
				fmt.Fprintf(b, "- **%s** — waiting on `%s`\n", s.where, s.state)
				shown++
			}
			fmt.Fprintf(b, "\nA goroutine parked on `chan receive` / `select` / `semacquire` is waiting for something that never comes (a Task that never completes, a lock held elsewhere); on `IO wait` it's blocked on the network or a file.\n")
		}
	}
	fmt.Fprintf(b, "\n")
}

type goroutineBlock struct {
	state  string
	frames []string
}

var grHeaderRe = regexp.MustCompile(`^goroutine \d+ \[([^\]]+)\]:`)

func splitGoroutineBlocks(dump string) []goroutineBlock {
	var out []goroutineBlock
	var cur *goroutineBlock
	for _, line := range strings.Split(dump, "\n") {
		if m := grHeaderRe.FindStringSubmatch(line); m != nil {
			if cur != nil {
				out = append(out, *cur)
			}
			cur = &goroutineBlock{state: strings.TrimSpace(m[1])}
			continue
		}
		if cur == nil {
			continue
		}
		// Function lines are un-indented `pkg.Func(...)`; file lines start with a tab.
		if strings.HasPrefix(line, "\t") || line == "" {
			continue
		}
		if i := strings.LastIndex(line, "("); i > 0 {
			cur.frames = append(cur.frames, line[:i])
		}
	}
	if cur != nil {
		out = append(out, *cur)
	}
	return out
}

func isBlockedState(s string) bool {
	switch s {
	case "chan receive", "chan send", "select", "IO wait", "semacquire",
		"sync.Mutex.Lock", "sync.WaitGroup.Wait", "sleep":
		return true
	}
	return false
}

// userFrame returns the top frame that is the dev's OWN Sky code (demangled) and
// true, or ("", false) if the goroutine runs no user code (a runtime/profiler
// worker). Used to pick which goroutines a hang report is actually about.
func userFrame(frames []string) (string, bool) {
	for _, f := range frames {
		if name, kind := demangleFrame(f); kind == kindUser {
			return name, true
		}
	}
	return "", false
}

// topSkyFrame returns the first frame that is the dev's own Sky code (demangled),
// falling back to the first stdlib call, then the raw top frame.
func topSkyFrame(frames []string) string {
	var stdlib string
	for _, f := range frames {
		name, kind := demangleFrame(f)
		if kind == kindUser {
			return name
		}
		if kind == kindStdlib && stdlib == "" {
			stdlib = name
		}
	}
	if stdlib != "" {
		return stdlib
	}
	if len(frames) > 0 {
		name, _ := demangleFrame(frames[0])
		return name
	}
	return "unknown"
}
