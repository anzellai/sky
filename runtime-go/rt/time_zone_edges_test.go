package rt

// Regression fence for Std.Time at the edges: invalid calendar input,
// local times in a gap or an overlap with each Disambiguation, toParts,
// isZone / zones, and nextTransition / previousTransition.

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func utcMs(y int, mo time.Month, d, h, mi, s int) int {
	return int(time.Date(y, mo, d, h, mi, s, 0, time.UTC).UnixMilli())
}

func timeOk(t *testing.T, r any) any {
	t.Helper()
	sr, ok := r.(SkyResult[any, any])
	if !ok {
		t.Fatalf("not a Result: %T %v", r, r)
	}
	if sr.Tag != 0 {
		t.Fatalf("want Ok, got Err %s", extractErrMsg(Err[any, any](sr.ErrValue)))
	}
	return sr.OkValue
}

// timeErr checks that r is an Err with the domain "time" and the code, and
// returns the message.
func timeErr(t *testing.T, r any, code string) string {
	t.Helper()
	var ev any
	switch v := r.(type) {
	case SkyResult[any, any]:
		if v.Tag == 0 {
			t.Fatalf("want Err %s, got Ok %v", code, v.OkValue)
		}
		ev = v.ErrValue
	default:
		ev = r
	}
	df, ok := DomainOf(ev)
	if !ok {
		t.Fatalf("want a domain failure %s, got %v", code, ev)
	}
	if df.Domain != "time" || df.Code != code {
		t.Fatalf("want time/%s, got %s/%s", code, df.Domain, df.Code)
	}
	return extractErrMsg(Err[any, any](ev))
}

func fromPartsWith(t *testing.T, mode int, zone string, y, mo, d, h, mi, s int) any {
	t.Helper()
	return invokeTime(t, "Time_fromPartsWith", mode, zone, y, mo, d, h, mi, s)
}

func TestTime_FromParts_CalendarValidation(t *testing.T) {
	ok := []struct{ y, m, d int }{
		{2024, 2, 29}, {2000, 2, 29}, {2023, 2, 28}, {2026, 4, 30}, {2026, 12, 31}, {1, 1, 1}, {9999, 12, 31},
	}
	for _, c := range ok {
		got := timeOk(t, invokeTime(t, "Time_fromParts", "UTC", c.y, c.m, c.d, 0, 0, 0)).(int)
		if want := utcMs(c.y, time.Month(c.m), c.d, 0, 0, 0); got != want {
			t.Errorf("%d-%d-%d: got %d want %d", c.y, c.m, c.d, got, want)
		}
	}
	bad := []struct{ y, m, d, h, mi, s int }{
		{2023, 2, 29, 0, 0, 0}, // not a leap year
		{2100, 2, 29, 0, 0, 0}, // century, not a leap year
		{2026, 2, 31, 0, 0, 0}, // the original defect: gave 3 March
		{2026, 4, 31, 0, 0, 0},
		{2026, 6, 31, 0, 0, 0},
		{2026, 1, 0, 0, 0, 0},
		{2026, 0, 1, 0, 0, 0},
		{2026, 13, 1, 0, 0, 0},
		{0, 1, 1, 0, 0, 0},
		{10000, 1, 1, 0, 0, 0},
		{2026, 1, 1, 24, 0, 0},
		{2026, 1, 1, -1, 0, 0},
		{2026, 1, 1, 0, 60, 0},
		{2026, 1, 1, 0, 0, 60},
		{2026, 1, 1, 0, 0, -1},
	}
	for _, c := range bad {
		for _, zone := range []string{"UTC", "Europe/London"} {
			timeErr(t, invokeTime(t, "Time_fromParts", zone, c.y, c.m, c.d, c.h, c.mi, c.s), timeCodeInvalidDate)
			for mode := disambCompatible; mode <= disambReject; mode++ {
				timeErr(t, fromPartsWith(t, mode, zone, c.y, c.m, c.d, c.h, c.mi, c.s), timeCodeInvalidDate)
			}
		}
	}
	msg := timeErr(t, invokeTime(t, "Time_fromParts", "UTC", 2026, 2, 31, 0, 0, 0), timeCodeInvalidDate)
	if !strings.Contains(msg, "1 to 28") {
		t.Errorf("31 February message should give the month's length: %s", msg)
	}
}

// gapCase is a local time that does not exist. later is the instant that
// Compatible and Later give (shift forward by the gap), earlier is the
// instant that Earlier gives.
type edgeCase struct {
	zone                string
	y, mo, d, h, mi, s  int
	earlier, later      int
	wantInMessage       []string
	compatibleIsEarlier bool
}

func checkEdge(t *testing.T, c edgeCase, code string) {
	t.Helper()
	want := map[int]int{disambEarlier: c.earlier, disambLater: c.later}
	if c.compatibleIsEarlier {
		want[disambCompatible] = c.earlier
	} else {
		want[disambCompatible] = c.later
	}
	for mode, w := range want {
		got := timeOk(t, fromPartsWith(t, mode, c.zone, c.y, c.mo, c.d, c.h, c.mi, c.s)).(int)
		if got != w {
			t.Errorf("%s %04d-%02d-%02d %02d:%02d mode %d: got %s want %s", c.zone, c.y, c.mo, c.d, c.h, c.mi, mode,
				time.UnixMilli(int64(got)).UTC(), time.UnixMilli(int64(w)).UTC())
		}
	}
	for _, r := range []any{
		fromPartsWith(t, disambReject, c.zone, c.y, c.mo, c.d, c.h, c.mi, c.s),
		invokeTime(t, "Time_fromParts", c.zone, c.y, c.mo, c.d, c.h, c.mi, c.s),
	} {
		msg := timeErr(t, r, code)
		for _, s := range append([]string{c.zone, "fromPartsWith"}, c.wantInMessage...) {
			if !strings.Contains(msg, s) {
				t.Errorf("message lacks %q: %s", s, msg)
			}
		}
	}
}

func TestTime_FromParts_Gaps(t *testing.T) {
	cases := []edgeCase{
		// Europe/London, 29 March 2026: 01:00 GMT -> 02:00 BST.
		{zone: "Europe/London", y: 2026, mo: 3, d: 29, h: 1, mi: 30,
			earlier: utcMs(2026, 3, 29, 0, 30, 0), later: utcMs(2026, 3, 29, 1, 30, 0),
			wantInMessage: []string{"2026-03-29 01:30:00", "does not exist"}},
		// America/New_York, 8 March 2026: 02:00 EST -> 03:00 EDT.
		{zone: "America/New_York", y: 2026, mo: 3, d: 8, h: 2, mi: 30,
			earlier: utcMs(2026, 3, 8, 6, 30, 0), later: utcMs(2026, 3, 8, 7, 30, 0),
			wantInMessage: []string{"2026-03-08 02:30:00"}},
		// Australia/Lord_Howe, 4 October 2026: 02:00 +10:30 -> 02:30 +11.
		{zone: "Australia/Lord_Howe", y: 2026, mo: 10, d: 4, h: 2, mi: 15,
			earlier: utcMs(2026, 10, 3, 15, 15, 0), later: utcMs(2026, 10, 3, 15, 45, 0)},
		// Pacific/Apia skipped 30 December 2011: the whole day is a gap of
		// 24 hours (-10 -> +14 at midnight).
		{zone: "Pacific/Apia", y: 2011, mo: 12, d: 30, h: 12,
			earlier: utcMs(2011, 12, 29, 22, 0, 0), later: utcMs(2011, 12, 30, 22, 0, 0),
			wantInMessage: []string{"2011-12-30 12:00:00"}},
		// Before 1970: Europe/London, 10 April 1960, 02:00 GMT -> 03:00 BST.
		{zone: "Europe/London", y: 1960, mo: 4, d: 10, h: 2, mi: 30,
			earlier: utcMs(1960, 4, 10, 1, 30, 0), later: utcMs(1960, 4, 10, 2, 30, 0)},
	}
	for _, c := range cases {
		checkEdge(t, c, timeCodeNonexistent)
	}
	// The Compatible answer for a gap reads, in the zone, as the wall clock
	// moved forward by the gap.
	got := timeOk(t, fromPartsWith(t, disambCompatible, "Europe/London", 2026, 3, 29, 1, 30, 0)).(int)
	s := timeOk(t, invokeTime(t, "Time_formatInZone", "Europe/London", "15:04", got)).(string)
	if s != "02:30" {
		t.Errorf("Compatible in the London gap reads %s, want 02:30", s)
	}
	// Apia: the day before and the day after exist.
	timeOk(t, invokeTime(t, "Time_fromParts", "Pacific/Apia", 2011, 12, 29, 23, 59, 59))
	timeOk(t, invokeTime(t, "Time_fromParts", "Pacific/Apia", 2011, 12, 31, 0, 0, 0))
}

func TestTime_FromParts_Overlaps(t *testing.T) {
	cases := []edgeCase{
		// Europe/London, 25 October 2026: 02:00 BST -> 01:00 GMT.
		{zone: "Europe/London", y: 2026, mo: 10, d: 25, h: 1, mi: 30, compatibleIsEarlier: true,
			earlier: utcMs(2026, 10, 25, 0, 30, 0), later: utcMs(2026, 10, 25, 1, 30, 0),
			wantInMessage: []string{"2026-10-25 01:30:00", "occurs twice", "2026-10-25T01:30:00+01:00", "2026-10-25T01:30:00Z"}},
		// America/New_York, 1 November 2026: 02:00 EDT -> 01:00 EST.
		{zone: "America/New_York", y: 2026, mo: 11, d: 1, h: 1, mi: 30, compatibleIsEarlier: true,
			earlier: utcMs(2026, 11, 1, 5, 30, 0), later: utcMs(2026, 11, 1, 6, 30, 0),
			wantInMessage: []string{"2026-11-01T01:30:00-04:00", "2026-11-01T01:30:00-05:00"}},
		// Australia/Lord_Howe, 5 April 2026: 02:00 +11 -> 01:30 +10:30, a
		// 30-minute overlap.
		{zone: "Australia/Lord_Howe", y: 2026, mo: 4, d: 5, h: 1, mi: 45, compatibleIsEarlier: true,
			earlier: utcMs(2026, 4, 4, 14, 45, 0), later: utcMs(2026, 4, 4, 15, 15, 0),
			wantInMessage: []string{"+11:00", "+10:30"}},
		// Before 1970: Europe/London, 2 October 1960, 03:00 BST -> 02:00 GMT.
		{zone: "Europe/London", y: 1960, mo: 10, d: 2, h: 2, mi: 30, compatibleIsEarlier: true,
			earlier: utcMs(1960, 10, 2, 1, 30, 0), later: utcMs(1960, 10, 2, 2, 30, 0)},
	}
	for _, c := range cases {
		checkEdge(t, c, timeCodeAmbiguous)
	}
	// Lord_Howe 01:29 is before the overlap and 02:00 after it.
	for _, hm := range [][2]int{{1, 29}, {2, 0}} {
		timeOk(t, invokeTime(t, "Time_fromParts", "Australia/Lord_Howe", 2026, 4, 5, hm[0], hm[1], 0))
	}
}

func TestTime_FromParts_UniqueTimes(t *testing.T) {
	cases := []struct {
		zone               string
		y, mo, d, h, mi, s int
		want               int
	}{
		{"Europe/London", 2026, 7, 1, 12, 0, 0, utcMs(2026, 7, 1, 11, 0, 0)},
		{"Europe/London", 2026, 3, 29, 2, 0, 0, utcMs(2026, 3, 29, 1, 0, 0)},     // first time after the gap
		{"Europe/London", 2026, 3, 29, 0, 59, 59, utcMs(2026, 3, 29, 0, 59, 59)}, // last time before it
		{"Europe/London", 2026, 10, 25, 2, 0, 0, utcMs(2026, 10, 25, 2, 0, 0)},   // first time after the overlap
		{"America/New_York", 1969, 7, 20, 22, 56, 0, utcMs(1969, 7, 21, 2, 56, 0)},
		{"Asia/Kolkata", 1950, 1, 26, 10, 0, 0, utcMs(1950, 1, 26, 4, 30, 0)},
		{"UTC", 1900, 1, 1, 0, 0, 0, utcMs(1900, 1, 1, 0, 0, 0)},
		{"", 2021, 1, 1, 0, 0, 0, utcMs(2021, 1, 1, 0, 0, 0)},
	}
	for _, c := range cases {
		for mode := disambCompatible; mode <= disambReject; mode++ {
			got := timeOk(t, fromPartsWith(t, mode, c.zone, c.y, c.mo, c.d, c.h, c.mi, c.s)).(int)
			if got != c.want {
				t.Errorf("%s %d-%d-%d %d:%d mode %d: got %d want %d", c.zone, c.y, c.mo, c.d, c.h, c.mi, mode, got, c.want)
			}
		}
	}
	if r := invokeTime(t, "Time_fromPartsWith", 4, "UTC", 2026, 1, 1, 0, 0, 0); r.(SkyResult[any, any]).Tag != 1 {
		t.Error("an unknown Disambiguation index must be an Err")
	}
}

func TestTime_ToParts(t *testing.T) {
	ms := int(time.Date(2026, 7, 1, 12, 34, 56, 789_000_000, time.UTC).UnixMilli())
	p := timeOk(t, invokeTime(t, "Time_toParts", "Europe/London", ms)).(map[string]any)
	want := map[string]any{
		"year": 2026, "month": 7, "day": 1, "hour": 13, "minute": 34, "second": 56,
		"millisecond": 789, "weekday": 3, "offsetMinutes": 60, "abbreviation": "BST",
	}
	for k, v := range want {
		if p[k] != v {
			t.Errorf("toParts London %s: got %v want %v", k, p[k], v)
		}
	}
	if len(p) != len(want) {
		t.Errorf("toParts has %d fields, want %d", len(p), len(want))
	}
	lh := timeOk(t, invokeTime(t, "Time_toParts", "Australia/Lord_Howe", utcMs(2026, 7, 1, 0, 0, 0))).(map[string]any)
	if lh["offsetMinutes"] != 630 || lh["hour"] != 10 || lh["minute"] != 30 {
		t.Errorf("toParts Lord_Howe winter: %v", lh)
	}
	// Sunday is 7 (ISO).
	sun := timeOk(t, invokeTime(t, "Time_toParts", "UTC", refTs)).(map[string]any)
	if sun["weekday"] != 7 {
		t.Errorf("2026-04-12 is a Sunday: weekday %v", sun["weekday"])
	}
	// Before 1970, with milliseconds: -1 ms is 1969-12-31 23:59:59.999.
	u := invokeTime(t, "Time_toPartsUtc", -1).(map[string]any)
	for k, v := range map[string]any{"year": 1969, "month": 12, "day": 31, "hour": 23, "minute": 59, "second": 59, "millisecond": 999, "weekday": 3, "offsetMinutes": 0, "abbreviation": "UTC"} {
		if u[k] != v {
			t.Errorf("toPartsUtc -1 %s: got %v want %v", k, u[k], v)
		}
	}
	timeErr(t, invokeTime(t, "Time_toParts", "Not/AZone", 0), timeCodeUnknownZone)
	// toParts and fromParts agree.
	for _, zone := range []string{"Europe/London", "America/New_York", "Australia/Lord_Howe", "Asia/Kathmandu"} {
		for _, at := range []int{utcMs(1955, 3, 1, 7, 0, 0), utcMs(2026, 1, 15, 7, 0, 0), utcMs(2026, 8, 15, 19, 0, 0)} {
			p := timeOk(t, invokeTime(t, "Time_toParts", zone, at)).(map[string]any)
			back := timeOk(t, invokeTime(t, "Time_fromParts", zone, p["year"], p["month"], p["day"], p["hour"], p["minute"], p["second"])).(int)
			if back != at {
				t.Errorf("%s: toParts then fromParts gave %d, want %d", zone, back, at)
			}
		}
	}
}

func TestTime_IsZoneAndZones(t *testing.T) {
	yes := []string{"Europe/London", "America/New_York", "Asia/Calcutta", "Asia/Kolkata", "Etc/UTC", "UTC", "GMT", "US/Eastern", "Australia/Lord_Howe"}
	no := []string{"", "Local", "europe/london", "EUROPE/LONDON", "utc", "Mars/Olympus_Mons", "Europe/../Europe/London", "/etc/passwd"}
	for _, n := range yes {
		if invokeTime(t, "Time_isZone", n) != true {
			t.Errorf("isZone %q: want True", n)
		}
	}
	for _, n := range no {
		if invokeTime(t, "Time_isZone", n) != false {
			t.Errorf("isZone %q: want False", n)
		}
	}
	msg := timeErr(t, invokeTime(t, "Time_inZone", "europe/london", 0), timeCodeUnknownZone)
	if !strings.Contains(msg, `"Europe/London"`) {
		t.Errorf("a name in the wrong case should name the right one: %s", msg)
	}
	timeErr(t, invokeTime(t, "Time_fromParts", "Local", 2026, 1, 1, 0, 0, 0), timeCodeUnknownZone)

	zs := invokeTime(t, "Time_zones").([]any)
	if len(zs) != len(timeZoneNames) || len(zs) < 300 {
		t.Fatalf("zones has %d names", len(zs))
	}
	set := map[string]bool{}
	for i, z := range zs {
		s := z.(string)
		if i > 0 && zs[i-1].(string) >= s {
			t.Fatalf("zones is not sorted and unique at %q", s)
		}
		set[s] = true
	}
	for _, n := range []string{"Europe/London", "Etc/UTC", "Australia/Lord_Howe", "Pacific/Apia", "Asia/Kolkata"} {
		if !set[n] {
			t.Errorf("zones lacks %q", n)
		}
	}
	for _, n := range []string{"Asia/Calcutta", "UTC", "US/Eastern", "Factory"} {
		if set[n] {
			t.Errorf("zones holds %q, which is not a canonical zone", n)
		}
	}
	// The caller may not change the list that later calls get.
	zs[0] = "changed"
	if invokeTime(t, "Time_zones").([]any)[0] == "changed" {
		t.Error("zones returned shared storage")
	}
}

func transitionOf(t *testing.T, kernel, zone string, ms int) (int, bool) {
	t.Helper()
	m := timeOk(t, invokeTime(t, kernel, zone, ms)).(SkyMaybe[any])
	if m.Tag != 0 {
		return 0, false
	}
	return m.JustValue.(int), true
}

func TestTime_Transitions(t *testing.T) {
	cases := []struct {
		zone       string
		from, next int
	}{
		{"Europe/London", utcMs(2026, 1, 1, 0, 0, 0), utcMs(2026, 3, 29, 1, 0, 0)},
		{"Europe/London", utcMs(2026, 3, 29, 1, 0, 0), utcMs(2026, 10, 25, 1, 0, 0)},
		{"America/New_York", utcMs(2026, 1, 1, 0, 0, 0), utcMs(2026, 3, 8, 7, 0, 0)},
		{"America/New_York", utcMs(2026, 4, 1, 0, 0, 0), utcMs(2026, 11, 1, 6, 0, 0)},
		// The 2007 US rule change: the second Sunday of March, not the
		// first Sunday of April.
		{"America/New_York", utcMs(2007, 1, 1, 0, 0, 0), utcMs(2007, 3, 11, 7, 0, 0)},
		{"America/New_York", utcMs(2006, 1, 1, 0, 0, 0), utcMs(2006, 4, 2, 7, 0, 0)},
		// Lord_Howe: a 30-minute change.
		{"Australia/Lord_Howe", utcMs(2026, 1, 1, 0, 0, 0), utcMs(2026, 4, 4, 15, 0, 0)},
		{"Australia/Lord_Howe", utcMs(2026, 5, 1, 0, 0, 0), utcMs(2026, 10, 3, 15, 30, 0)},
		{"Pacific/Apia", utcMs(2011, 12, 1, 0, 0, 0), utcMs(2011, 12, 30, 10, 0, 0)},
		// Europe/Moscow moved to +03 all year on 26 October 2014.
		{"Europe/Moscow", utcMs(2014, 1, 1, 0, 0, 0), utcMs(2014, 10, 25, 22, 0, 0)},
		// Before 1970.
		{"Europe/London", utcMs(1960, 1, 1, 0, 0, 0), utcMs(1960, 4, 10, 2, 0, 0)},
	}
	for _, c := range cases {
		got, ok := transitionOf(t, "Time_nextTransition", c.zone, c.from)
		if !ok || got != c.next {
			t.Errorf("nextTransition %s from %s: got %v %s want %s", c.zone, time.UnixMilli(int64(c.from)).UTC(), ok,
				time.UnixMilli(int64(got)).UTC(), time.UnixMilli(int64(c.next)).UTC())
		}
		// The offset or the name changes exactly there.
		before, _ := transitionOf(t, "Time_previousTransition", c.zone, c.next+1)
		if before != c.next {
			t.Errorf("previousTransition %s from just after %s: got %s", c.zone, time.UnixMilli(int64(c.next)).UTC(), time.UnixMilli(int64(before)).UTC())
		}
		loc, _ := loadZone(c.zone)
		if zoneStateAt(loc, int64(c.next)-1) == zoneStateAt(loc, int64(c.next)) {
			t.Errorf("%s: no change at %d", c.zone, c.next)
		}
	}
	// Both directions are strict, so a loop moves on.
	n1, _ := transitionOf(t, "Time_nextTransition", "Europe/London", utcMs(2026, 1, 1, 0, 0, 0))
	if p, _ := transitionOf(t, "Time_previousTransition", "Europe/London", n1); p != utcMs(2025, 10, 26, 1, 0, 0) {
		t.Errorf("previousTransition at a transition gives the one before: got %s", time.UnixMilli(int64(p)).UTC())
	}
	// No transitions: Asia/Shanghai has had none since 1991, UTC none ever,
	// Moscow none after 2014.
	for _, c := range []struct {
		zone string
		from int
	}{{"Asia/Shanghai", utcMs(2026, 1, 1, 0, 0, 0)}, {"UTC", 0}, {"Etc/GMT+5", 0}, {"Europe/Moscow", utcMs(2015, 1, 1, 0, 0, 0)}} {
		if _, ok := transitionOf(t, "Time_nextTransition", c.zone, c.from); ok {
			t.Errorf("nextTransition %s: want Nothing", c.zone)
		}
	}
	if p, ok := transitionOf(t, "Time_previousTransition", "Asia/Shanghai", utcMs(2026, 1, 1, 0, 0, 0)); !ok || p != utcMs(1991, 9, 14, 17, 0, 0) {
		t.Errorf("previousTransition Shanghai: got %v %s, want 1991-09-14T17:00Z", ok, time.UnixMilli(int64(p)).UTC())
	}
	if _, ok := transitionOf(t, "Time_previousTransition", "Asia/Shanghai", utcMs(2050, 1, 1, 0, 0, 0)); ok {
		t.Error("previousTransition Shanghai from 2050: the last change (1991) is past the 50-year horizon, want Nothing")
	}
	timeErr(t, invokeTime(t, "Time_nextTransition", "Not/AZone", 0), timeCodeUnknownZone)
	// The ends of the Int range do not wrap.
	for _, ms := range []int{int(^uint(0) >> 1), -int(^uint(0)>>1) - 1} {
		transitionOf(t, "Time_nextTransition", "Europe/London", ms)
		transitionOf(t, "Time_previousTransition", "Europe/London", ms)
	}
}

// A scan with no transition reads 50 years. It must stay fast enough to
// call per request.
func TestTime_TransitionScanIsFast(t *testing.T) {
	start := time.Now()
	for i := 0; i < 10; i++ {
		transitionOf(t, "Time_nextTransition", "Asia/Shanghai", utcMs(2026, 1, 1, 0, 0, 0))
	}
	if per := time.Since(start) / 10; per > 200*time.Millisecond {
		t.Errorf("a 50-year scan took %s", per)
	}
}

func TestTime_LoadZoneConcurrent(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				name := timeZoneNames[(i*50+j)%len(timeZoneNames)]
				if _, err := loadZone(name); err != nil {
					t.Error(err)
					return
				}
				invokeTime(t, "Time_fromParts", name, 2026, 6, 1, 12, 0, 0)
			}
		}(i)
	}
	wg.Wait()
}
