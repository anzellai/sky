//go:build !js

package rt

// The Sky Console's query vocabulary: one time range and a set of search
// terms, shared by the embedded console's /_sky/console/api/* handlers and
// the hub's SQLite readers (runtime-go/rt/hub/bridge.go).
//
// The console used to fetch the newest 200 log lines (100 spans) and filter
// THEM in the browser by range and search text. With a busy app the newest
// 200 lines span a few seconds, so the 24h, 7d and All ranges showed the same
// rows, a search found nothing older than those rows, and a log row's trace
// badge jumped to a Traces tab that did not hold the trace. The range compare
// was also a string compare between a UTC threshold and a log time stamped in
// the server's local zone, wrong by the zone offset everywhere but UTC. The
// filter now runs here, on the server, over the whole store, BEFORE the row
// limit, and compares instants, not strings.

import (
	"net/url"
	"strings"
	"time"
)

// ConsoleRangeWindow maps a console range key ("15m", "1h", "24h", "7d",
// "all") to its duration. ok is false for "all", "" and an unknown key: no
// lower bound.
func ConsoleRangeWindow(key string) (time.Duration, bool) {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "15m":
		return 15 * time.Minute, true
	case "1h":
		return time.Hour, true
	case "24h":
		return 24 * time.Hour, true
	case "7d":
		return 7 * 24 * time.Hour, true
	}
	return 0, false
}

// ConsoleRangeSince is the lower bound of range key `key` at `now`, or the
// zero time when the range has none.
func ConsoleRangeSince(key string, now time.Time) time.Time {
	if d, ok := ConsoleRangeWindow(key); ok {
		return now.Add(-d)
	}
	return time.Time{}
}

// ConsoleSearchTerms splits the search parameters of a console request into
// lower-cased terms. Every `q` value is one term (the global search and a
// tab's own search box each send one); empty values are dropped.
func ConsoleSearchTerms(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.ToLower(strings.TrimSpace(v))
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// ConsoleTextMatch reports whether every term occurs (case-insensitively) in
// at least one of fields. No terms → true.
func ConsoleTextMatch(terms []string, fields ...string) bool {
	if len(terms) == 0 {
		return true
	}
	lowered := make([]string, len(fields))
	for i, f := range fields {
		lowered[i] = strings.ToLower(f)
	}
	for _, t := range terms {
		hit := false
		for _, f := range lowered {
			if strings.Contains(f, t) {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	return true
}

// consoleQuery is the parsed range + search of one console API request.
type consoleQuery struct {
	since time.Time // zero → no lower bound
	terms []string
}

func parseConsoleQuery(q url.Values, now time.Time) consoleQuery {
	return consoleQuery{
		since: ConsoleRangeSince(q.Get("range"), now),
		terms: ConsoleSearchTerms(q["q"]),
	}
}

// inRange reports whether t is inside the query's range. A zero t (an entry
// with no time) is inside only when the range has no lower bound.
func (cq consoleQuery) inRange(t time.Time) bool {
	if cq.since.IsZero() {
		return true
	}
	return !t.IsZero() && !t.Before(cq.since)
}
