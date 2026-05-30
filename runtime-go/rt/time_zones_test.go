package rt

// Regression fence for Std.Time IANA-zone primitives. Phase 2.4.

import (
	"strings"
	"testing"
)

func invokeTime(t *testing.T, name string, args ...any) any {
	t.Helper()
	ffiRegistryMu.RLock()
	fn, ok := ffiPureRegistry[name]
	ffiRegistryMu.RUnlock()
	if !ok {
		t.Fatalf("time kernel %q not registered", name)
	}
	return fn(args)
}

// 2026-04-12 20:00 UTC = Unix ms 1776024000000 — a Sunday.
const refTs = 1776024000000

func TestTime_InZone(t *testing.T) {
	r := invokeTime(t, "Time_inZone", "America/New_York", refTs)
	sr := r.(SkyResult[any, any])
	if sr.Tag != 0 {
		t.Fatalf("inZone NYC failed: %v", sr.ErrValue)
	}
	s := sr.OkValue.(string)
	// 20:00 UTC = 16:00 EDT (April → DST active)
	if !strings.HasPrefix(s, "2026-04-12T16:00") {
		t.Errorf("NYC formatted: got %q want prefix 2026-04-12T16:00", s)
	}

	// Tokyo: +9 → 05:00 next day
	r2 := invokeTime(t, "Time_inZone", "Asia/Tokyo", refTs)
	s2 := r2.(SkyResult[any, any]).OkValue.(string)
	if !strings.HasPrefix(s2, "2026-04-13T05:00") {
		t.Errorf("Tokyo formatted: got %q", s2)
	}
}

func TestTime_InZone_BadZone(t *testing.T) {
	r := invokeTime(t, "Time_inZone", "Pluto/CapitalCity", refTs)
	sr := r.(SkyResult[any, any])
	if sr.Tag != 1 {
		t.Fatal("bad zone should return Err")
	}
}

func TestTime_FormatInZone(t *testing.T) {
	r := invokeTime(t, "Time_formatInZone", "America/New_York", "2006-01-02 15:04", refTs)
	sr := r.(SkyResult[any, any])
	if sr.Tag != 0 {
		t.Fatalf("formatInZone failed: %v", sr.ErrValue)
	}
	if sr.OkValue.(string) != "2026-04-12 16:00" {
		t.Errorf("custom format: got %q", sr.OkValue)
	}
}

func TestTime_CalendarAdd(t *testing.T) {
	// addMonths handles month-length correctly: Jan 31 + 1 month = Feb 28 / 29.
	// 2026-01-31 00:00 UTC = 1769817600000
	jan31 := int64(1769817600000)
	added := invokeTime(t, "Time_addMonths", 1, int(jan31))
	// Int-declared Time kernels box a Go int (canonical Sky Int → Go int).
	// See kernel_return_width_test.go for the contract — int64 would
	// break the typed SkyResult / SkyTask boundary via coerceInner.
	addedMs, ok := added.(int)
	if !ok {
		t.Fatalf("addMonths returned %T, want int", added)
	}
	// Should be 2026-02-28 (not 2026-03-03).
	zone := invokeTime(t, "Time_formatInZone", "UTC", "2006-01-02", addedMs)
	s := zone.(SkyResult[any, any]).OkValue.(string)
	if s != "2026-02-28" {
		t.Errorf("Jan 31 + 1 month: want 2026-02-28 got %q", s)
	}
}

func TestTime_StartOfDay(t *testing.T) {
	// In NYC the start of the day for refTs is 2026-04-12 00:00 EDT (which
	// is 04:00 UTC).
	r := invokeTime(t, "Time_startOfDay", "America/New_York", refTs)
	sr := r.(SkyResult[any, any])
	if sr.Tag != 0 {
		t.Fatalf("startOfDay failed: %v", sr.ErrValue)
	}
	// Int-declared Time kernels box a Go int — see TestTime_CalendarAdd
	// note + kernel_return_width_test.go for the contract.
	startMs, ok := sr.OkValue.(int)
	if !ok {
		t.Fatalf("startOfDay returned %T, want int", sr.OkValue)
	}
	// Format the result back as NYC date — should be 2026-04-12 00:00
	f := invokeTime(t, "Time_formatInZone", "America/New_York", "2006-01-02 15:04", startMs)
	if got := f.(SkyResult[any, any]).OkValue.(string); got != "2026-04-12 00:00" {
		t.Errorf("startOfDay NYC: want '2026-04-12 00:00' got %q", got)
	}
}

func TestTime_EndOfMonth(t *testing.T) {
	r := invokeTime(t, "Time_endOfMonth", "UTC", refTs)
	endMs, ok := r.(SkyResult[any, any]).OkValue.(int)
	if !ok {
		t.Fatalf("endOfMonth returned %T, want int", r.(SkyResult[any, any]).OkValue)
	}
	// April 2026 has 30 days. End-of-month-UTC = 2026-04-30 23:59:59.999
	f := invokeTime(t, "Time_formatInZone", "UTC", "2006-01-02 15:04:05", endMs)
	if got := f.(SkyResult[any, any]).OkValue.(string); got != "2026-04-30 23:59:59" {
		t.Errorf("endOfMonth UTC: want '2026-04-30 23:59:59' got %q", got)
	}
}

func TestTime_DayOfWeek_ISO(t *testing.T) {
	// 2026-04-12 is a Sunday. ISO Sun=7.
	r := invokeTime(t, "Time_dayOfWeek", "UTC", refTs)
	d := r.(SkyResult[any, any]).OkValue.(int)
	if d != 7 {
		t.Errorf("Sunday in ISO: want 7 got %d", d)
	}
}

func TestTime_IsLeapYear(t *testing.T) {
	cases := []struct {
		y    int
		leap bool
	}{
		{2024, true}, {2025, false}, {2100, false}, {2000, true}, {1900, false},
	}
	for _, c := range cases {
		got := invokeTime(t, "Time_isLeapYear", c.y).(bool)
		if got != c.leap {
			t.Errorf("isLeapYear(%d): want %t got %t", c.y, c.leap, got)
		}
	}
}

func TestTime_DaysInMonth(t *testing.T) {
	cases := []struct {
		y, m int
		want int
	}{
		{2024, 1, 31}, {2024, 2, 29}, {2025, 2, 28}, {2024, 4, 30}, {2024, 12, 31},
	}
	for _, c := range cases {
		got := invokeTime(t, "Time_daysInMonth", c.y, c.m).(int)
		if got != c.want {
			t.Errorf("daysInMonth(%d,%d): want %d got %d", c.y, c.m, c.want, got)
		}
	}
}

func TestTime_DiffDays(t *testing.T) {
	a := refTs
	b := refTs - int64(7*24*3600*1000) // 7 days before
	d := invokeTime(t, "Time_diffDays", a, int(b)).(int)
	if d != 7 {
		t.Errorf("diffDays: want 7 got %d", d)
	}
}

func TestTime_FromParts(t *testing.T) {
	// Build NYC noon for the same day.
	r := invokeTime(t, "Time_fromParts", "America/New_York", 2026, 4, 12, 12, 0, 0)
	sr := r.(SkyResult[any, any])
	if sr.Tag != 0 {
		t.Fatalf("fromParts failed: %v", sr.ErrValue)
	}
	// Format back — should round-trip cleanly.
	// Int-declared Time kernels box a Go int — see kernel_return_width_test.go.
	ms, ok := sr.OkValue.(int)
	if !ok {
		t.Fatalf("fromParts returned %T, want int", sr.OkValue)
	}
	f := invokeTime(t, "Time_formatInZone", "America/New_York", "2006-01-02 15:04", ms)
	if got := f.(SkyResult[any, any]).OkValue.(string); got != "2026-04-12 12:00" {
		t.Errorf("fromParts round-trip: want '2026-04-12 12:00' got %q", got)
	}
}

func TestTime_FromParts_OutOfRange(t *testing.T) {
	r := invokeTime(t, "Time_fromParts", "UTC", 2026, 13, 1, 0, 0, 0) // month 13
	sr := r.(SkyResult[any, any])
	if sr.Tag != 1 {
		t.Fatal("month=13 should Err")
	}
}

func TestTime_ZoneOffset(t *testing.T) {
	// EDT = UTC-4 = -14400 seconds. (April 2026 — DST active.)
	r := invokeTime(t, "Time_zoneOffset", "America/New_York", refTs)
	off := r.(SkyResult[any, any]).OkValue.(int)
	if off != -4*3600 {
		t.Errorf("NYC zone offset: want -14400 got %d", off)
	}
}
