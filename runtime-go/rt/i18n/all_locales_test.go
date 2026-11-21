//go:build sky_cldr_all

package i18n

import (
	"strings"
	"testing"
)

// TestEveryLocaleFormats formats with every generated locale: each pattern
// must parse and fill every placeholder, and each locale must be its own
// data locale.
func TestEveryLocaleFormats(t *testing.T) {
	n := 0
	for tag := range cldrParents {
		if tag == "und" {
			continue
		}
		l, err := ParseLocale(tag)
		if err != nil {
			t.Errorf("%s: %v", tag, err)
			continue
		}
		if DataLocale(l) != tag {
			t.Errorf("%s: data locale %q", tag, DataLocale(l))
		}
		for _, f := range []func() (string, error){
			func() (string, error) { return FormatDecimal(l, dec("-1234567.891")) },
			func() (string, error) { return FormatPercent(l, dec("0.125")) },
			func() (string, error) { return FormatMoney(l, dec("1234.5"), "EUR") },
			func() (string, error) { return FormatList(l, ListOr, []string{"a", "b", "c", "d"}) },
			func() (string, error) { return FormatRelative(l, "day", -3, true) },
		} {
			s, err := f()
			if err != nil || s == "" || strings.Contains(s, "{0}") {
				t.Errorf("%s: %q %v", tag, s, err)
			}
			n++
		}
		for _, st := range []Style{Short, Medium, Long, Full} {
			s, err := FormatDateTime(l, st, "Asia/Kolkata", ms)
			if err != nil || s == "" || strings.Contains(s, "{0}") || strings.Contains(s, "{1}") {
				t.Errorf("%s %s: %q %v", tag, st, s, err)
			}
			n++
		}
	}
	t.Logf("ASSERTIONS: %d", n)
}
