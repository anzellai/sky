package rt

// Std.Time zone helpers — IANA timezone-aware formatting, calendar
// arithmetic, period-bucketing, ISO 8601 week numbering. Production-
// grade surface for any app that schedules, bills monthly, reports
// daily, or shows users their local time.
//
// Sky-side surface lives in sky-stdlib/Std/Time.sky. This file is
// the FFI boundary — registers Time_* primitives via RegisterPure
// so the Sky source can invoke them through Sky.Ffi.callPure.
//
// Zones are passed as IANA strings ("UTC", "America/New_York",
// "Asia/Tokyo"). Invalid zone names return Err so the user's
// downstream pattern-match flags the configuration problem.

import (
	"fmt"
	"time"

	// Embedded IANA tzdata so LoadLocation works without
	// /usr/share/zoneinfo on the host. Adds ~450 KB to binary;
	// worth it for containers / scratch images / Windows.
	_ "time/tzdata"
)

// daysInMonthFor returns the calendar last-day of the given year+month.
// Used by Time.addMonths / Time.addYears to clamp day-of-month so
// "Jan 31 + 1 month" yields end-of-Feb rather than Mar 3.
func daysInMonthFor(year int, m time.Month) int {
	// First-of-next-month minus one day.
	return time.Date(year, m+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// loadZone wraps time.LoadLocation with a clearer error message
// when the IANA name is unknown. Empty zone falls back to UTC.
func loadZone(name string) (*time.Location, error) {
	if name == "" {
		return time.UTC, nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("unknown IANA zone %q: %v", name, err)
	}
	return loc, nil
}

// withZone is the common pattern: parse zone, build time, hand to a
// callback. Centralises the load-and-err shape.
func withZone(zone any, ms any, fn func(t time.Time, loc *time.Location) any) any {
	loc, err := loadZone(fmt.Sprintf("%v", zone))
	if err != nil {
		return Err[any, any](ErrInvalidInput(err.Error()))
	}
	t := time.UnixMilli(int64(AsInt(ms))).In(loc)
	return fn(t, loc)
}

func init() {
	// ── Formatting ──────────────────────────────────────────────

	// Time.inZone : String -> Int -> Result Error String
	// RFC 3339 with zone offset.
	RegisterPure("Time_inZone", func(args []any) any {
		if len(args) < 2 {
			return Err[any, any](ErrInvalidInput("Time.inZone: missing args"))
		}
		return withZone(args[0], args[1], func(t time.Time, _ *time.Location) any {
			return Ok[any, any](t.Format(time.RFC3339))
		})
	})

	// Time.formatInZone : String -> String -> Int -> Result Error String
	// Custom Go-layout format in the named zone.
	RegisterPure("Time_formatInZone", func(args []any) any {
		if len(args) < 3 {
			return Err[any, any](ErrInvalidInput("Time.formatInZone: missing args"))
		}
		return withZone(args[0], args[2], func(t time.Time, _ *time.Location) any {
			return Ok[any, any](t.Format(fmt.Sprintf("%v", args[1])))
		})
	})

	// ── Calendar arithmetic ─────────────────────────────────────
	// All add* return Unix-ms. Calendar-aware (handles month-length
	// + leap years correctly via Go's time.AddDate / Add).

	// addMonths CLAMPS to the last day of the target month rather
	// than letting Go's AddDate overflow (Jan 31 + 1 month = Mar 3
	// is the stdlib behaviour but emphatically NOT what users
	// expect for billing / invoicing). Jan 31 + 1 → Feb 28/29.
	RegisterPure("Time_addMonths", func(args []any) any {
		if len(args) < 2 {
			return 0
		}
		t := time.UnixMilli(int64(AsInt(args[1]))).UTC()
		y, m, d := t.Date()
		newM := int(m) + AsInt(args[0])
		// Normalise into 1..12 + year carry.
		ny := y + (newM-1)/12
		nm := time.Month(((newM-1)%12+12)%12 + 1)
		// Clamp day to last day of target month.
		lastDay := daysInMonthFor(ny, nm)
		if d > lastDay {
			d = lastDay
		}
		return int(time.Date(ny, nm, d, t.Hour(), t.Minute(), t.Second(),
			t.Nanosecond(), t.Location()).UnixMilli())
	})

	RegisterPure("Time_addYears", func(args []any) any {
		if len(args) < 2 {
			return 0
		}
		t := time.UnixMilli(int64(AsInt(args[1]))).UTC()
		y, m, d := t.Date()
		ny := y + AsInt(args[0])
		// Feb 29 + 1 year (non-leap) → Feb 28.
		lastDay := daysInMonthFor(ny, m)
		if d > lastDay {
			d = lastDay
		}
		return int(time.Date(ny, m, d, t.Hour(), t.Minute(), t.Second(),
			t.Nanosecond(), t.Location()).UnixMilli())
	})

	RegisterPure("Time_addDays", func(args []any) any {
		if len(args) < 2 {
			return 0
		}
		t := time.UnixMilli(int64(AsInt(args[1]))).UTC()
		return int(t.AddDate(0, 0, AsInt(args[0])).UnixMilli())
	})

	RegisterPure("Time_addHours", func(args []any) any {
		if len(args) < 2 {
			return 0
		}
		ms := int64(AsInt(args[1]))
		return int(ms + int64(AsInt(args[0]))*3600*1000)
	})

	RegisterPure("Time_addMinutes", func(args []any) any {
		if len(args) < 2 {
			return 0
		}
		ms := int64(AsInt(args[1]))
		return int(ms + int64(AsInt(args[0]))*60*1000)
	})

	RegisterPure("Time_addSeconds", func(args []any) any {
		if len(args) < 2 {
			return 0
		}
		ms := int64(AsInt(args[1]))
		return int(ms + int64(AsInt(args[0]))*1000)
	})

	// ── Period boundaries ───────────────────────────────────────
	// All start*/end* take a zone — local-day starts at midnight
	// LOCAL, not midnight UTC. Without zone-awareness, daily reports
	// in any non-Greenwich zone bucket wrong.

	RegisterPure("Time_startOfDay", func(args []any) any {
		if len(args) < 2 {
			return Err[any, any](ErrInvalidInput("Time.startOfDay: missing args"))
		}
		return withZone(args[0], args[1], func(t time.Time, loc *time.Location) any {
			start := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
			return Ok[any, any](int(start.UnixMilli()))
		})
	})

	RegisterPure("Time_endOfDay", func(args []any) any {
		if len(args) < 2 {
			return Err[any, any](ErrInvalidInput("Time.endOfDay: missing args"))
		}
		return withZone(args[0], args[1], func(t time.Time, loc *time.Location) any {
			start := time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, loc)
			return Ok[any, any](int(start.Add(-time.Millisecond).UnixMilli()))
		})
	})

	// startOfWeek — Monday as week start (ISO 8601). For Sunday-start
	// (US convention), use startOfDay + addDays.
	RegisterPure("Time_startOfWeek", func(args []any) any {
		if len(args) < 2 {
			return Err[any, any](ErrInvalidInput("Time.startOfWeek: missing args"))
		}
		return withZone(args[0], args[1], func(t time.Time, loc *time.Location) any {
			wd := int(t.Weekday()) // Sunday=0, Monday=1, ...
			daysFromMonday := (wd + 6) % 7
			start := time.Date(t.Year(), t.Month(), t.Day()-daysFromMonday,
				0, 0, 0, 0, loc)
			return Ok[any, any](int(start.UnixMilli()))
		})
	})

	RegisterPure("Time_startOfMonth", func(args []any) any {
		if len(args) < 2 {
			return Err[any, any](ErrInvalidInput("Time.startOfMonth: missing args"))
		}
		return withZone(args[0], args[1], func(t time.Time, loc *time.Location) any {
			start := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, loc)
			return Ok[any, any](int(start.UnixMilli()))
		})
	})

	RegisterPure("Time_endOfMonth", func(args []any) any {
		if len(args) < 2 {
			return Err[any, any](ErrInvalidInput("Time.endOfMonth: missing args"))
		}
		return withZone(args[0], args[1], func(t time.Time, loc *time.Location) any {
			firstOfNext := time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, loc)
			return Ok[any, any](int(firstOfNext.Add(-time.Millisecond).UnixMilli()))
		})
	})

	RegisterPure("Time_startOfYear", func(args []any) any {
		if len(args) < 2 {
			return Err[any, any](ErrInvalidInput("Time.startOfYear: missing args"))
		}
		return withZone(args[0], args[1], func(t time.Time, loc *time.Location) any {
			start := time.Date(t.Year(), 1, 1, 0, 0, 0, 0, loc)
			return Ok[any, any](int(start.UnixMilli()))
		})
	})

	RegisterPure("Time_endOfYear", func(args []any) any {
		if len(args) < 2 {
			return Err[any, any](ErrInvalidInput("Time.endOfYear: missing args"))
		}
		return withZone(args[0], args[1], func(t time.Time, loc *time.Location) any {
			start := time.Date(t.Year()+1, 1, 1, 0, 0, 0, 0, loc)
			return Ok[any, any](int(start.Add(-time.Millisecond).UnixMilli()))
		})
	})

	// ── Calendar queries ────────────────────────────────────────

	// year / month / day in a zone (UTC zone gives raw UTC fields).
	RegisterPure("Time_year", func(args []any) any {
		if len(args) < 2 {
			return Err[any, any](ErrInvalidInput("Time.year: missing args"))
		}
		return withZone(args[0], args[1], func(t time.Time, _ *time.Location) any {
			return Ok[any, any](t.Year())
		})
	})

	RegisterPure("Time_month", func(args []any) any {
		if len(args) < 2 {
			return Err[any, any](ErrInvalidInput("Time.month: missing args"))
		}
		return withZone(args[0], args[1], func(t time.Time, _ *time.Location) any {
			return Ok[any, any](int(t.Month()))
		})
	})

	RegisterPure("Time_day", func(args []any) any {
		if len(args) < 2 {
			return Err[any, any](ErrInvalidInput("Time.day: missing args"))
		}
		return withZone(args[0], args[1], func(t time.Time, _ *time.Location) any {
			return Ok[any, any](t.Day())
		})
	})

	// dayOfWeek : String -> Int -> Result Error Int
	// ISO 8601: Monday=1 ... Sunday=7
	RegisterPure("Time_dayOfWeek", func(args []any) any {
		if len(args) < 2 {
			return Err[any, any](ErrInvalidInput("Time.dayOfWeek: missing args"))
		}
		return withZone(args[0], args[1], func(t time.Time, _ *time.Location) any {
			wd := int(t.Weekday())
			if wd == 0 {
				wd = 7 // Sunday → 7 (ISO)
			}
			return Ok[any, any](wd)
		})
	})

	// dayOfYear : String -> Int -> Result Error Int  (1..366)
	RegisterPure("Time_dayOfYear", func(args []any) any {
		if len(args) < 2 {
			return Err[any, any](ErrInvalidInput("Time.dayOfYear: missing args"))
		}
		return withZone(args[0], args[1], func(t time.Time, _ *time.Location) any {
			return Ok[any, any](t.YearDay())
		})
	})

	// weekOfYear : String -> Int -> Result Error Int (ISO 8601)
	RegisterPure("Time_weekOfYear", func(args []any) any {
		if len(args) < 2 {
			return Err[any, any](ErrInvalidInput("Time.weekOfYear: missing args"))
		}
		return withZone(args[0], args[1], func(t time.Time, _ *time.Location) any {
			_, week := t.ISOWeek()
			return Ok[any, any](week)
		})
	})

	// isWeekend : String -> Int -> Result Error Bool
	RegisterPure("Time_isWeekend", func(args []any) any {
		if len(args) < 2 {
			return Err[any, any](ErrInvalidInput("Time.isWeekend: missing args"))
		}
		return withZone(args[0], args[1], func(t time.Time, _ *time.Location) any {
			wd := t.Weekday()
			return Ok[any, any](wd == time.Saturday || wd == time.Sunday)
		})
	})

	// daysInMonth : Int -> Int -> Int — `daysInMonth 2024 2` = 29
	// Pure calendar lookup; no zone needed.
	RegisterPure("Time_daysInMonth", func(args []any) any {
		if len(args) < 2 {
			return 30
		}
		year := AsInt(args[0])
		month := AsInt(args[1])
		if month < 1 || month > 12 {
			return 30
		}
		// Last day of month = first of next month minus 1 day.
		first := time.Date(year, time.Month(month+1), 1, 0, 0, 0, 0, time.UTC)
		return first.AddDate(0, 0, -1).Day()
	})

	// isLeapYear : Int -> Bool
	RegisterPure("Time_isLeapYear", func(args []any) any {
		if len(args) < 1 {
			return false
		}
		y := AsInt(args[0])
		return (y%4 == 0 && y%100 != 0) || y%400 == 0
	})

	// ── Differences ─────────────────────────────────────────────

	// diffDays : Int -> Int -> Int — calendar days (a - b)
	RegisterPure("Time_diffDays", func(args []any) any {
		if len(args) < 2 {
			return 0
		}
		ms := int64(AsInt(args[0])) - int64(AsInt(args[1]))
		return int(ms / (24 * 3600 * 1000))
	})

	RegisterPure("Time_diffHours", func(args []any) any {
		if len(args) < 2 {
			return 0
		}
		ms := int64(AsInt(args[0])) - int64(AsInt(args[1]))
		return int(ms / (3600 * 1000))
	})

	RegisterPure("Time_diffMinutes", func(args []any) any {
		if len(args) < 2 {
			return 0
		}
		ms := int64(AsInt(args[0])) - int64(AsInt(args[1]))
		return int(ms / (60 * 1000))
	})

	RegisterPure("Time_diffSeconds", func(args []any) any {
		if len(args) < 2 {
			return 0
		}
		ms := int64(AsInt(args[0])) - int64(AsInt(args[1]))
		return int(ms / 1000)
	})

	// ── Construction from parts ─────────────────────────────────

	// fromParts : String -> Int -> Int -> Int -> Int -> Int -> Int -> Result Error Int
	// zone year month day hour minute second → Unix-ms
	// All ranges validated; out-of-range returns Err.
	RegisterPure("Time_fromParts", func(args []any) any {
		if len(args) < 7 {
			return Err[any, any](ErrInvalidInput("Time.fromParts: missing args"))
		}
		loc, err := loadZone(fmt.Sprintf("%v", args[0]))
		if err != nil {
			return Err[any, any](ErrInvalidInput(err.Error()))
		}
		year := AsInt(args[1])
		month := AsInt(args[2])
		day := AsInt(args[3])
		hour := AsInt(args[4])
		minute := AsInt(args[5])
		second := AsInt(args[6])
		if month < 1 || month > 12 || day < 1 || day > 31 ||
			hour < 0 || hour > 23 || minute < 0 || minute > 59 ||
			second < 0 || second > 59 {
			return Err[any, any](ErrInvalidInput("Time.fromParts: out-of-range field"))
		}
		t := time.Date(year, time.Month(month), day, hour, minute, second, 0, loc)
		return Ok[any, any](int(t.UnixMilli()))
	})

	// ── Zone discovery ──────────────────────────────────────────

	// zoneOffset : String -> Int -> Result Error Int
	// Offset from UTC in seconds, for the given ms (accounts for DST).
	RegisterPure("Time_zoneOffset", func(args []any) any {
		if len(args) < 2 {
			return Err[any, any](ErrInvalidInput("Time.zoneOffset: missing args"))
		}
		return withZone(args[0], args[1], func(t time.Time, _ *time.Location) any {
			_, off := t.Zone()
			return Ok[any, any](off)
		})
	})

	// zoneName : String -> Int -> Result Error String
	// e.g. "EDT", "PST", "JST" — the short name for the moment.
	RegisterPure("Time_zoneName", func(args []any) any {
		if len(args) < 2 {
			return Err[any, any](ErrInvalidInput("Time.zoneName: missing args"))
		}
		return withZone(args[0], args[1], func(t time.Time, _ *time.Location) any {
			name, _ := t.Zone()
			return Ok[any, any](name)
		})
	})
}
