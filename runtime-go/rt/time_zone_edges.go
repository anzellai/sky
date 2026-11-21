package rt

// Std.Time at the edges: construction from local parts with an explicit
// rule for local times that do not exist (a gap, when the clocks go
// forward) or that exist twice (an overlap, when the clocks go back), all
// parts of a local time in one call, the list of zone names, and the
// instants where a zone's offset changes.
//
// The zone data is the IANA database that time_zones.go embeds
// (`import _ "time/tzdata"`). It is in every Sky binary, the Sky.Spa wasm
// client included. Measured on js/wasm with Go 1.26.0: the embedded copy
// adds 390,698 bytes raw (105,560 after gzip -9, 59,893 after brotli 11),
// and the name list in time_zone_names.go adds 25,181 bytes raw (8,133 after
// gzip, 3,720 after brotli). The name list comes from tools/gen-time-zones.
//
// Failures are InvalidInput with the domain "time" and one of the codes
// below, so a program can react to the code (Error.domain) and not to the
// message text.

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	timeCodeUnknownZone  = "UnknownZone"
	timeCodeInvalidDate  = "InvalidDate"
	timeCodeNonexistent  = "NonexistentLocalTime"
	timeCodeAmbiguous    = "AmbiguousLocalTime"
	timeMinYear          = 1
	timeMaxYear          = 9999
	timeDayMs            = int64(24 * 3600 * 1000)
	timeScanStepMs       = timeDayMs
	timeTransitionWindow = 50 * 3652425 * timeDayMs / 10000 // 50 years of 365.2425 days
)

// The Disambiguation constructors of Std.Time, in declaration order. The
// Sky side passes the index.
const (
	disambCompatible = 0
	disambEarlier    = 1
	disambLater      = 2
	disambReject     = 3
)

var (
	zoneCache sync.Map // name -> *time.Location

	zoneFoldOnce sync.Once
	zoneFold     map[string]string

	// The first and last instants Std.Time scans: 0001-01-01 and
	// 10000-01-01 UTC.
	timeScanMinMs = time.Date(timeMinYear, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	timeScanMaxMs = time.Date(timeMaxYear+1, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
)

// knownZone reports whether name is a canonical zone or a link in the
// generated list.
func knownZone(name string) bool {
	if _, ok := timeZoneLinks[name]; ok {
		return true
	}
	i := sort.SearchStrings(timeZoneNames, name)
	return i < len(timeZoneNames) && timeZoneNames[i] == name
}

// zoneFoldIndex maps the lower-case form of every known name to the name.
func zoneFoldIndex() map[string]string {
	zoneFoldOnce.Do(func() {
		zoneFold = make(map[string]string, len(timeZoneNames)+len(timeZoneLinks))
		for _, n := range timeZoneNames {
			zoneFold[strings.ToLower(n)] = n
		}
		for n := range timeZoneLinks {
			zoneFold[strings.ToLower(n)] = n
		}
	})
	return zoneFold
}

func timeFail(code, msg string, attrs ...[2]string) any {
	return Err[any, any](ErrDomain(errKindInvalidInput, msg, DomainFailure{
		Domain: "time", Code: code, RetryAfterMs: -1, Attrs: attrs,
	}))
}

// zoneState is the offset (seconds east of UTC) and the abbreviation in
// effect at an instant. A transition is an instant where it changes.
type zoneState struct {
	offset int
	abbr   string
}

func zoneStateAt(loc *time.Location, ms int64) zoneState {
	abbr, off := time.UnixMilli(ms).In(loc).Zone()
	return zoneState{offset: off, abbr: abbr}
}

// nextZoneTransition returns the first instant T with from < T <= until
// where the zone's state at T differs from the state at T-1 ms. It steps
// one day at a time. No zone in the data has two transitions closer than
// timeZoneMinGapMs (about four days; time_zone_names_test.go keeps that
// true for the toolchain's data), so a step cannot pass over two
// transitions and miss both. A changed state is then bisected to the
// millisecond.
func nextZoneTransition(loc *time.Location, from, until int64) (int64, bool) {
	if from < timeScanMinMs {
		from = timeScanMinMs
	}
	if until > timeScanMaxMs {
		until = timeScanMaxMs
	}
	if from >= until {
		return 0, false
	}
	lo := from
	s0 := zoneStateAt(loc, lo)
	for lo < until {
		hi := lo + timeScanStepMs
		if hi > until {
			hi = until
		}
		if zoneStateAt(loc, hi) != s0 {
			// state(lo) == s0 and state(hi) != s0: bisect.
			for hi-lo > 1 {
				mid := lo + (hi-lo)/2
				if zoneStateAt(loc, mid) == s0 {
					lo = mid
				} else {
					hi = mid
				}
			}
			return hi, true
		}
		lo = hi
	}
	return 0, false
}

// previousZoneTransition returns the last instant T with since < T < from
// where the zone's state at T differs from the state at T-1 ms.
func previousZoneTransition(loc *time.Location, from, since int64) (int64, bool) {
	if from > timeScanMaxMs {
		from = timeScanMaxMs
	}
	if since < timeScanMinMs {
		since = timeScanMinMs
	}
	hi := from - 1
	if hi <= since {
		return 0, false
	}
	s0 := zoneStateAt(loc, hi)
	for hi > since {
		lo := hi - timeScanStepMs
		if lo < since {
			lo = since
		}
		if zoneStateAt(loc, lo) != s0 {
			// state(lo) != s0 and state(hi) == s0: bisect.
			for hi-lo > 1 {
				mid := lo + (hi-lo)/2
				if zoneStateAt(loc, mid) == s0 {
					hi = mid
				} else {
					lo = mid
				}
			}
			return hi, true
		}
		hi = lo
	}
	return 0, false
}

// clampAddMs adds d to ms and keeps the sum within the scanned range, so
// an instant near the ends of the Int range does not wrap.
func clampAddMs(ms, d int64) int64 {
	if ms > timeScanMaxMs {
		ms = timeScanMaxMs
	}
	if ms < timeScanMinMs {
		ms = timeScanMinMs
	}
	return ms + d
}

func formatLocal(y, mo, d, h, mi, s int) string {
	return fmt.Sprintf("%04d-%02d-%02d %02d:%02d:%02d", y, mo, d, h, mi, s)
}

func formatInstant(loc *time.Location, ms int64) string {
	return time.UnixMilli(ms).In(loc).Format("2006-01-02T15:04:05Z07:00")
}

// timeFromParts implements fromParts and fromPartsWith. parts holds year,
// month, day, hour, minute, second.
func timeFromParts(fn string, mode int, zoneArg any, parts []any) any {
	zone := fmt.Sprintf("%v", zoneArg)
	loc, err := loadZone(zone)
	if err != nil {
		return zoneError(zone, err)
	}
	y, mo, d := AsInt(parts[0]), AsInt(parts[1]), AsInt(parts[2])
	h, mi, s := AsInt(parts[3]), AsInt(parts[4]), AsInt(parts[5])
	bad := func(why string) any {
		return timeFail(timeCodeInvalidDate,
			fmt.Sprintf("%s: invalid date or time (year %d, month %d, day %d, %02d:%02d:%02d): %s", fn, y, mo, d, h, mi, s, why),
			[2]string{"zone", zone})
	}
	switch {
	case y < timeMinYear || y > timeMaxYear:
		return bad(fmt.Sprintf("the year must be %d to %d", timeMinYear, timeMaxYear))
	case mo < 1 || mo > 12:
		return bad("the month must be 1 to 12")
	case d < 1 || d > daysInMonthFor(y, time.Month(mo)):
		return bad(fmt.Sprintf("the day must be 1 to %d in this month", daysInMonthFor(y, time.Month(mo))))
	case h < 0 || h > 23:
		return bad("the hour must be 0 to 23")
	case mi < 0 || mi > 59:
		return bad("the minute must be 0 to 59")
	case s < 0 || s > 59:
		return bad("the second must be 0 to 59")
	}
	local := formatLocal(y, mo, d, h, mi, s)
	// The wall clock read as if it were UTC. An instant t shows this wall
	// clock in the zone when t + offset(t) == wall.
	wall := time.Date(y, time.Month(mo), d, h, mi, s, 0, time.UTC).UnixMilli()

	// Every offset in effect within two days of the wall clock. No real
	// offset is more than 26 hours from UTC, so every instant that can
	// show this wall clock is in the window.
	lo, hi := wall-2*timeDayMs, wall+2*timeDayMs
	type change struct {
		at            int64
		before, after zoneState
	}
	var changes []change
	states := []zoneState{zoneStateAt(loc, lo)}
	for t := lo; ; {
		next, ok := nextZoneTransition(loc, t, hi)
		if !ok {
			break
		}
		changes = append(changes, change{at: next, before: zoneStateAt(loc, next-1), after: zoneStateAt(loc, next)})
		states = append(states, zoneStateAt(loc, next))
		t = next
	}
	seen := map[int64]bool{}
	var candidates []int64
	for _, st := range states {
		c := wall - int64(st.offset)*1000
		if seen[c] {
			continue
		}
		seen[c] = true
		if zoneStateAt(loc, c).offset == st.offset {
			candidates = append(candidates, c)
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i] < candidates[j] })

	switch {
	case len(candidates) == 1:
		return Ok[any, any](int(candidates[0]))
	case len(candidates) > 1:
		earlier, later := candidates[0], candidates[len(candidates)-1]
		switch mode {
		case disambCompatible, disambEarlier:
			return Ok[any, any](int(earlier))
		case disambLater:
			return Ok[any, any](int(later))
		}
		e, l := formatInstant(loc, earlier), formatInstant(loc, later)
		return timeFail(timeCodeAmbiguous,
			fmt.Sprintf("%s: %s occurs twice in %s, at %s (%d ms) and at %s (%d ms), because the clocks go back. Use Time.fromPartsWith Earlier or Later to choose one",
				fn, local, zone, e, earlier, l, later),
			[2]string{"zone", zone}, [2]string{"local", local},
			[2]string{"earlier", e}, [2]string{"later", l})
	}

	// A gap: find the transition that skips over the wall clock.
	for _, c := range changes {
		from := c.at + int64(c.before.offset)*1000
		to := c.at + int64(c.after.offset)*1000
		if !(from <= wall && wall < to) {
			continue
		}
		earlier := wall - int64(c.after.offset)*1000
		later := wall - int64(c.before.offset)*1000
		switch mode {
		case disambCompatible, disambLater:
			return Ok[any, any](int(later))
		case disambEarlier:
			return Ok[any, any](int(earlier))
		}
		fl := time.UnixMilli(from).UTC().Format("2006-01-02 15:04:05")
		tl := time.UnixMilli(to).UTC().Format("2006-01-02 15:04:05")
		return timeFail(timeCodeNonexistent,
			fmt.Sprintf("%s: %s does not exist in %s, because the clocks go forward from %s to %s at %s. Use Time.fromPartsWith to choose an instant (Compatible or Later gives %s, Earlier gives %s)",
				fn, local, zone, fl, tl, formatInstant(loc, c.at), formatInstant(loc, later), formatInstant(loc, earlier)),
			[2]string{"zone", zone}, [2]string{"local", local},
			[2]string{"gapStart", fl}, [2]string{"gapEnd", tl})
	}
	// Not reachable for data in which every offset is within 26 hours of
	// UTC. It is an Err and not a guess.
	return timeFail(timeCodeNonexistent,
		fmt.Sprintf("%s: %s has no instant in %s", fn, local, zone),
		[2]string{"zone", zone}, [2]string{"local", local})
}

// timeParts is the Std.Time.Parts record for an instant in a zone.
func timeParts(t time.Time) map[string]any {
	abbr, off := t.Zone()
	wd := int(t.Weekday())
	if wd == 0 {
		wd = 7
	}
	return map[string]any{
		"year":          t.Year(),
		"month":         int(t.Month()),
		"day":           t.Day(),
		"hour":          t.Hour(),
		"minute":        t.Minute(),
		"second":        t.Second(),
		"millisecond":   t.Nanosecond() / int(time.Millisecond),
		"weekday":       wd,
		"offsetMinutes": off / 60,
		"abbreviation":  abbr,
	}
}

func init() {
	// fromPartsWith : Disambiguation -> String -> Int -> Int -> Int -> Int -> Int -> Int -> Result Error Int
	RegisterPure("Time_fromPartsWith", func(args []any) any {
		if len(args) < 8 {
			return Err[any, any](ErrInvalidInput("Time.fromPartsWith: missing args"))
		}
		mode := AsInt(args[0])
		if mode < disambCompatible || mode > disambReject {
			return Err[any, any](ErrInvalidInput(fmt.Sprintf("Time.fromPartsWith: unknown Disambiguation %d", mode)))
		}
		return timeFromParts("Time.fromPartsWith", mode, args[1], args[2:8])
	})

	// toParts : String -> Int -> Result Error Parts
	RegisterPure("Time_toParts", func(args []any) any {
		if len(args) < 2 {
			return Err[any, any](ErrInvalidInput("Time.toParts: missing args"))
		}
		return withZone(args[0], args[1], func(t time.Time, _ *time.Location) any {
			return Ok[any, any](timeParts(t))
		})
	})

	// toPartsUtc : Int -> Parts
	RegisterPure("Time_toPartsUtc", func(args []any) any {
		var ms int64
		if len(args) > 0 {
			ms = int64(AsInt(args[0]))
		}
		return timeParts(time.UnixMilli(ms).UTC())
	})

	// isZone : String -> Bool. True when the other Std.Time functions
	// accept the name. The empty name (which they read as UTC) is not a
	// zone name.
	RegisterPure("Time_isZone", func(args []any) any {
		if len(args) < 1 {
			return false
		}
		name := fmt.Sprintf("%v", args[0])
		if name == "" {
			return false
		}
		_, err := loadZone(name)
		return err == nil
	})

	// zones : List String. The canonical names, sorted.
	RegisterPure("Time_zones", func(args []any) any {
		out := make([]any, len(timeZoneNames))
		for i, n := range timeZoneNames {
			out[i] = n
		}
		return out
	})

	// nextTransition / previousTransition : String -> Int -> Result Error (Maybe Int)
	RegisterPure("Time_nextTransition", func(args []any) any {
		if len(args) < 2 {
			return Err[any, any](ErrInvalidInput("Time.nextTransition: missing args"))
		}
		return withZone(args[0], args[1], func(t time.Time, loc *time.Location) any {
			from := t.UnixMilli()
			if at, ok := nextZoneTransition(loc, from, clampAddMs(from, timeTransitionWindow)); ok {
				return Ok[any, any](Just[any](int(at)))
			}
			return Ok[any, any](Nothing[any]())
		})
	})
	RegisterPure("Time_previousTransition", func(args []any) any {
		if len(args) < 2 {
			return Err[any, any](ErrInvalidInput("Time.previousTransition: missing args"))
		}
		return withZone(args[0], args[1], func(t time.Time, loc *time.Location) any {
			from := t.UnixMilli()
			if at, ok := previousZoneTransition(loc, from, clampAddMs(from, -timeTransitionWindow)); ok {
				return Ok[any, any](Just[any](int(at)))
			}
			return Ok[any, any](Nothing[any]())
		})
	})
}
