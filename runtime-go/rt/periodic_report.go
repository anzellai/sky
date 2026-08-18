package rt

import (
	"fmt"

	"sky-app/rt/periodic"
)

// periodicReport routes every panic and every error from `rt`'s background
// loops into the structured log — the same ring the console's Logs tab reads,
// so "was it reported" means "could an operator have seen it".
//
// It exists because periodic.Reporter deliberately does no logging of its own:
// `rt/telemetry`, `rt/hub` and `rt/jobs` cannot import `rt`, so the shared
// mechanism cannot know what any one caller's operators read. Each package
// supplies its own adapter; this is `rt`'s.
//
// The event names are `<loop>.cycle_panicked` / `<loop>.cycle_failed`, so a
// dashboard filter for `.cycle_` catches the whole class at once.
func periodicReport(r periodic.Report) {
	switch {
	case r.Panic != nil:
		logStructured("warn", r.Loop+".cycle_panicked",
			"detail", "this cycle is lost; the loop continues and the next tick will retry",
			"panic", fmt.Sprintf("%v", r.Panic),
			"stack", string(r.Stack))
	case r.Err != nil:
		logStructured("warn", r.Loop+".cycle_failed",
			"detail", "the cycle did not complete its work",
			"error", r.Err.Error())
	}
}
