package i18n

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Style is a CLDR date or time format length.
type Style string

const (
	Short  Style = "short"
	Medium Style = "medium"
	Long   Style = "long"
	Full   Style = "full"
)

// ParseStyle reads a style name.
func ParseStyle(s string) (Style, error) {
	switch Style(s) {
	case Short, Medium, Long, Full:
		return Style(s), nil
	}
	return "", fmt.Errorf("unknown date style %q (short, medium, long, full)", s)
}

// DateError says that a date or a zone is not valid.
type DateError struct{ Reason string }

func (e *DateError) Error() string { return e.Reason }

// ParseISODate reads "2026-11-17".
func ParseISODate(s string) (time.Time, error) {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, &DateError{Reason: fmt.Sprintf("%q is not an ISO date (YYYY-MM-DD)", s)}
	}
	return t, nil
}

// zoneTime gives the time of a Unix millisecond in an IANA zone.
func zoneTime(ms int64, zone string) (time.Time, error) {
	if zone == "" {
		zone = "UTC"
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return time.Time{}, &DateError{Reason: fmt.Sprintf("unknown IANA zone %q", zone)}
	}
	return time.UnixMilli(ms).In(loc), nil
}

// FormatDate formats an ISO date with the locale's date pattern of a style.
func FormatDate(l Locale, style Style, iso string) (string, error) {
	d, err := dataFor(l)
	if err != nil {
		return "", err
	}
	t, err := ParseISODate(iso)
	if err != nil {
		return "", err
	}
	return dateFormatter{d, l}.format(d.get("cal.date."+string(style)), t), nil
}

// FormatTime formats the time of day of a Unix millisecond in a zone.
func FormatTime(l Locale, style Style, zone string, ms int64) (string, error) {
	d, err := dataFor(l)
	if err != nil {
		return "", err
	}
	t, err := zoneTime(ms, zone)
	if err != nil {
		return "", err
	}
	return dateFormatter{d, l}.format(d.get("cal.time."+string(style)), t), nil
}

// FormatDateTime formats the date and the time of a Unix millisecond in a
// zone, joined by the locale's date-time pattern of the same style.
func FormatDateTime(l Locale, style Style, zone string, ms int64) (string, error) {
	d, err := dataFor(l)
	if err != nil {
		return "", err
	}
	t, err := zoneTime(ms, zone)
	if err != nil {
		return "", err
	}
	f := dateFormatter{d, l}
	date := f.format(d.get("cal.date."+string(style)), t)
	tm := f.format(d.get("cal.time."+string(style)), t)
	glue := d.get("cal.dt." + string(style))
	return strings.NewReplacer("{1}", date, "{0}", tm).Replace(glue), nil
}

type dateFormatter struct {
	d *data
	l Locale
}

func (f dateFormatter) num(n, width int) string {
	s := strconv.Itoa(n)
	for len(s) < width {
		s = "0" + s
	}
	return numberFormatter{f.d}.digits(s)
}

// format applies a CLDR date pattern. Quoted text ('at') is literal and ”
// is an apostrophe. Letters the four standard styles of the CLDR modern
// locales do not use are written as they are.
func (f dateFormatter) format(pat string, t time.Time) string {
	var b strings.Builder
	rs := []rune(pat)
	for i := 0; i < len(rs); {
		r := rs[i]
		if r == '\'' {
			if i+1 < len(rs) && rs[i+1] == '\'' {
				b.WriteRune('\'')
				i += 2
				continue
			}
			j := i + 1
			for j < len(rs) && rs[j] != '\'' {
				b.WriteRune(rs[j])
				j++
			}
			i = j + 1
			continue
		}
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			b.WriteRune(r)
			i++
			continue
		}
		n := 1
		for i+n < len(rs) && rs[i+n] == r {
			n++
		}
		b.WriteString(f.field(r, n, t))
		i += n
	}
	return b.String()
}

func (f dateFormatter) field(r rune, n int, t time.Time) string {
	d := f.d
	switch r {
	case 'G':
		if t.Year() <= 0 {
			return d.get("cal.era.0")
		}
		return d.get("cal.era.1")
	case 'y', 'u':
		y := t.Year()
		if n == 2 {
			return f.num(y%100, 2)
		}
		return f.num(y, n)
	case 'M', 'L':
		ctx := "f"
		if r == 'L' {
			ctx = "s"
		}
		m := int(t.Month())
		switch {
		case n <= 2:
			return f.num(m, n)
		case n == 3:
			return d.get(fmt.Sprintf("cal.m.%s.a.%d", ctx, m))
		default:
			return d.get(fmt.Sprintf("cal.m.%s.w.%d", ctx, m))
		}
	case 'd':
		return f.num(t.Day(), n)
	case 'E', 'c', 'e':
		ctx := "f"
		if r == 'c' {
			ctx = "s"
		}
		wd := int(t.Weekday())
		if r != 'E' && n <= 2 {
			return f.num(wd+1, n)
		}
		if n == 4 {
			return d.get(fmt.Sprintf("cal.d.%s.w.%d", ctx, wd))
		}
		return d.get(fmt.Sprintf("cal.d.%s.a.%d", ctx, wd))
	case 'a', 'b':
		if t.Hour() < 12 {
			return d.get("cal.dp.am")
		}
		return d.get("cal.dp.pm")
	case 'B':
		return f.flexiblePeriod(t)
	case 'h':
		h := t.Hour() % 12
		if h == 0 {
			h = 12
		}
		return f.num(h, n)
	case 'H':
		return f.num(t.Hour(), n)
	case 'K':
		return f.num(t.Hour()%12, n)
	case 'k':
		h := t.Hour()
		if h == 0 {
			h = 24
		}
		return f.num(h, n)
	case 'm':
		return f.num(t.Minute(), n)
	case 's':
		return f.num(t.Second(), n)
	case 'z', 'O', 'v', 'V':
		_, off := t.Zone()
		long := n >= 4
		return f.gmt(off, long)
	}
	return strings.Repeat(string(r), n)
}

// flexiblePeriod gives the CLDR flexible day period ("B") of the time.
func (f dateFormatter) flexiblePeriod(t time.Time) string {
	m := t.Hour()*60 + t.Minute()
	for _, r := range cldrDayPeriods[f.l.Language] {
		if r.from == r.before {
			continue // an "at" period (midnight, noon) is for "b" only
		}
		in := false
		if r.from < r.before {
			in = m >= r.from && m < r.before
		} else {
			in = m >= r.from || m < r.before
		}
		if in {
			if s := f.d.get("cal.dp." + r.name); s != "" {
				return s
			}
		}
	}
	if t.Hour() < 12 {
		return f.d.get("cal.dp.am")
	}
	return f.d.get("cal.dp.pm")
}

// gmt is the CLDR localized GMT format: "GMT", "GMT+1", "GMT+5:30" (short)
// or "GMT+01:00" (long). Sky shows every zone this way, so the text depends
// only on the offset and the locale.
func (f dateFormatter) gmt(offset int, long bool) string {
	d := f.d
	if offset == 0 {
		// CLDR 48 dropped gmtZeroFormat: the zero offset is gmtFormat with an
		// empty offset ("GMT").
		if z := d.get("tz.gmtzero"); z != "" {
			return z
		}
		return strings.Replace(d.get("tz.gmt"), "{0}", "", 1)
	}
	pos, neg, _ := strings.Cut(d.get("tz.hour"), ";")
	pat := pos
	if offset < 0 {
		pat = neg
		offset = -offset
	}
	h, m := offset/3600, (offset%3600)/60
	if !long {
		pat = strings.Replace(pat, "HH", "H", 1)
		if m == 0 {
			// Drop the separator and the minutes: "+H:mm" → "+H".
			if i := strings.Index(pat, "mm"); i > 0 {
				j := i - 1
				for j > 0 && pat[j] != 'H' {
					j--
				}
				pat = pat[:j+1] + pat[i+2:]
			}
		}
	}
	var b strings.Builder
	for i := 0; i < len(pat); {
		switch {
		case strings.HasPrefix(pat[i:], "HH"):
			b.WriteString(f.num(h, 2))
			i += 2
		case pat[i] == 'H':
			b.WriteString(f.num(h, 1))
			i++
		case strings.HasPrefix(pat[i:], "mm"):
			b.WriteString(f.num(m, 2))
			i += 2
		default:
			b.WriteByte(pat[i])
			i++
		}
	}
	return strings.Replace(d.get("tz.gmt"), "{0}", b.String(), 1)
}
