// Sky.Tui — runtime warning sink for Std.Ui features the terminal
// can't faithfully render.
//
// Design goal: keep the terminal clean during the run (no surprise
// stderr writes corrupting the alt-screen) AND give the developer a
// clear summary at exit of what was skipped.
//
// Mechanics:
//   - tuiWarn(category, detail) records a (category, detail) pair the
//     first time it's seen. Subsequent calls with the same key are
//     silently dropped — we don't want to spam the same warning on
//     every render.
//   - On Tui.app exit, the deferred teardown calls tuiFlushWarnings()
//     which prints a one-shot summary to stderr unless SKY_TUI_QUIET=1
//     is set. The summary lists each unique (category, detail) once
//     with a count, so users see e.g. "fontSize ignored × 47 frames".
//   - The full ledger is also written to ./sky-tui-warnings.log when
//     SKY_TUI_LOG=1 is set, for reviewing across sessions.
//
// This is the "skipped/ignore that element/attribute is fine —
// ideally should have warning" requirement landed in code form.

package rt

import (
	"fmt"
	"os"
	"sort"
	"sync"
)

type tuiWarning struct {
	category string
	detail   string
	count    int
}

var (
	tuiWarnMu      sync.Mutex
	tuiWarnSeen    = map[string]*tuiWarning{}
	tuiWarnOrdered []string // insertion order so output is deterministic
)

// tuiWarn records a single skipped feature. Safe to call from any
// goroutine. Only the FIRST occurrence per (category, detail) emits
// any work beyond a counter increment, so it's cheap to call from
// hot paths (e.g. inside the layout pass, per attribute, per frame).
//
// `category` is the broad area (e.g. "background", "font", "border")
// and `detail` is the specific feature (e.g. "gradient", "size",
// "shadow"). Both are short kebab-case-ish strings.
func tuiWarn(category, detail string) {
	tuiWarnMu.Lock()
	defer tuiWarnMu.Unlock()
	key := category + ":" + detail
	if w, ok := tuiWarnSeen[key]; ok {
		w.count++
		return
	}
	tuiWarnSeen[key] = &tuiWarning{category: category, detail: detail, count: 1}
	tuiWarnOrdered = append(tuiWarnOrdered, key)
}

// tuiFlushWarnings prints a summary to stderr (unless SKY_TUI_QUIET=1)
// and resets the ledger. Called once from Tui.app's deferred teardown
// after the terminal has been restored — at this point stderr is
// safe to write to without corrupting the user's view.
func tuiFlushWarnings() {
	tuiWarnMu.Lock()
	defer tuiWarnMu.Unlock()
	if len(tuiWarnSeen) == 0 {
		return
	}
	logFile := ""
	if os.Getenv("SKY_TUI_LOG") != "" {
		logFile = "sky-tui-warnings.log"
	}
	quiet := os.Getenv("SKY_TUI_QUIET") != ""

	// Group by category for readable output.
	categories := map[string][]*tuiWarning{}
	for _, key := range tuiWarnOrdered {
		w := tuiWarnSeen[key]
		categories[w.category] = append(categories[w.category], w)
	}

	cats := make([]string, 0, len(categories))
	for c := range categories {
		cats = append(cats, c)
	}
	sort.Strings(cats)

	var stderr, file *os.File
	if !quiet {
		stderr = os.Stderr
	}
	if logFile != "" {
		f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err == nil {
			file = f
			defer file.Close()
		}
	}

	if stderr != nil {
		fmt.Fprintf(stderr, "\nSky.Tui: %d feature(s) were skipped (not supported in terminal)\n", len(tuiWarnSeen))
	}
	if file != nil {
		fmt.Fprintf(file, "Sky.Tui — feature ledger from this session\n\n")
	}

	for _, cat := range cats {
		ws := categories[cat]
		if stderr != nil {
			fmt.Fprintf(stderr, "  %s:\n", cat)
			for _, w := range ws {
				suffix := ""
				if w.count > 1 {
					suffix = fmt.Sprintf(" (×%d)", w.count)
				}
				fmt.Fprintf(stderr, "    - %s%s\n", w.detail, suffix)
			}
		}
		if file != nil {
			fmt.Fprintf(file, "%s:\n", cat)
			for _, w := range ws {
				fmt.Fprintf(file, "  - %s (count=%d)\n", w.detail, w.count)
			}
		}
	}

	if stderr != nil {
		fmt.Fprintf(stderr, "  (set SKY_TUI_QUIET=1 to suppress this summary, SKY_TUI_LOG=1 to write a full ledger to ./sky-tui-warnings.log)\n")
	}

	// Reset for the next run (matters mostly for tests).
	tuiWarnSeen = map[string]*tuiWarning{}
	tuiWarnOrdered = nil
}

// tuiResetWarnings clears the ledger without printing. Used by tests
// to isolate per-test counts.
func tuiResetWarnings() {
	tuiWarnMu.Lock()
	defer tuiWarnMu.Unlock()
	tuiWarnSeen = map[string]*tuiWarning{}
	tuiWarnOrdered = nil
}
