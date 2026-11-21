//go:build sky_cldr_all

package i18n

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

// The expected strings are CLDR's (ICU gives the same). Run with
// `go test -tags sky_cldr_all ./rt/i18n/`: the tag compiles every locale.
//   is a no-break space,   a narrow no-break space, ؜ the
// Arabic letter mark.

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestFormatNumbers(t *testing.T) {
	cases := []struct{ loc, in, want string }{
		{"en-GB", "1234.5", "1,234.5"},
		{"en-US", "1234567.891", "1,234,567.891"},
		{"en", "0.0005", "0"},
		{"en", "2.0005", "2"},
		{"en", "2.0015", "2.002"},
		{"en", "-1234.5", "-1,234.5"},
		{"de", "1234.5", "1.234,5"},
		{"fr", "1234.5", "1 234,5"},
		{"hi", "1234567.891", "12,34,567.891"},
		{"hi", "123", "123"},
		{"ja", "1234.5", "1,234.5"},
		{"zh-Hans", "1234.5", "1,234.5"},
		{"zh-Hant-HK", "1234.5", "1,234.5"},
		{"ar", "1234.5", "1,234.5"},
		{"ar", "-7", "‎-7"},
		{"pl", "1234", "1234"},
		{"pl", "12345", "12 345"},
		{"es", "1234", "1234"},
		{"de-CH", "1234.5", "1'234.5"},
		{"bn", "1234", "১,২৩৪"},
	}
	for _, c := range cases {
		got, err := FormatDecimal(MustLocale(c.loc), dec(c.in))
		if err != nil || got != c.want {
			t.Errorf("decimal %s %s: got %q (%v), want %q", c.loc, c.in, got, err, c.want)
		}
	}
	if got, _ := FormatInt(MustLocale("en-GB"), -1234567); got != "-1,234,567" {
		t.Errorf("int: %q", got)
	}
}

func TestFormatPercent(t *testing.T) {
	cases := []struct{ loc, in, want string }{
		{"en-GB", "0.125", "12.5%"},
		{"en", "1", "100%"},
		{"zh-Hans", "0.125", "12.5%"},
		{"de", "0.125", "12,5 %"},
		{"fr", "0.125", "12,5 %"},
		{"hi", "123.45", "12,345%"},
	}
	for _, c := range cases {
		got, err := FormatPercent(MustLocale(c.loc), dec(c.in))
		if err != nil || got != c.want {
			t.Errorf("percent %s %s: got %q (%v), want %q", c.loc, c.in, got, err, c.want)
		}
	}
}

func TestFormatMoney(t *testing.T) {
	cases := []struct{ loc, amount, code, want string }{
		{"en-GB", "1234.5", "GBP", "£1,234.50"},
		{"en-GB", "1234.5", "USD", "US$1,234.50"},
		{"en-US", "1234.5", "USD", "$1,234.50"},
		{"en-GB", "1200", "JPY", "JP¥1,200"},
		{"en-GB", "120", "AED", "AED 120.00"},
		{"en-GB", "-5", "GBP", "-£5.00"},
		{"en-GB", "2.005", "GBP", "£2.00"},
		{"en-GB", "2.015", "GBP", "£2.02"},
		{"en-GB", "1.5", "KWD", "KWD 1.500"},
		{"zh-Hans", "88", "CNY", "¥88.00"},
		{"zh-Hans", "1234.5", "GBP", "£1,234.50"},
		{"zh-Hant-HK", "1234.5", "HKD", "HK$1,234.50"},
		{"de", "1234.5", "EUR", "1.234,50 €"},
		{"fr", "1234.5", "EUR", "1 234,50 €"},
		{"ja", "1200", "JPY", "￥1,200"},
		{"hi", "1234567", "INR", "₹12,34,567.00"},
		{"en", "1", "BTC", "BTC 1.00"},
	}
	for _, c := range cases {
		got, err := FormatMoney(MustLocale(c.loc), dec(c.amount), c.code)
		if err != nil || got != c.want {
			t.Errorf("money %s %s %s: got %q (%v), want %q", c.loc, c.amount, c.code, got, err, c.want)
		}
	}
}

func TestFormatDates(t *testing.T) {
	cases := []struct {
		loc   string
		style Style
		want  string
	}{
		{"en-GB", Short, "17/11/2026"},
		{"en-GB", Medium, "17 Nov 2026"},
		{"en-GB", Long, "17 November 2026"},
		{"en-GB", Full, "Tuesday, 17 November 2026"},
		{"en-US", Short, "11/17/26"},
		{"en-US", Medium, "Nov 17, 2026"},
		{"en-US", Full, "Tuesday, November 17, 2026"},
		{"de", Medium, "17.11.2026"},
		{"de", Long, "17. November 2026"},
		{"fr", Medium, "17 nov. 2026"},
		{"fr", Long, "17 novembre 2026"},
		{"ja", Medium, "2026/11/17"},
		{"ja", Long, "2026年11月17日"},
		{"ja", Full, "2026年11月17日火曜日"},
		{"zh-Hans", Medium, "2026年11月17日"},
		{"zh-Hans", Short, "2026/11/17"},
		{"zh-Hans", Full, "2026年11月17日星期二"},
		{"zh-Hant-HK", Medium, "2026年11月17日"},
		{"zh-Hant-HK", Short, "17/11/2026"},
	}
	for _, c := range cases {
		got, err := FormatDate(MustLocale(c.loc), c.style, "2026-11-17")
		if err != nil || got != c.want {
			t.Errorf("date %s %s: got %q (%v), want %q", c.loc, c.style, got, err, c.want)
		}
	}
	if _, err := FormatDate(MustLocale("en"), Short, "2026-13-01"); err == nil {
		t.Error("a bad ISO date must be an error")
	}
}

// 2026-11-17T14:05:09Z
var ms = time.Date(2026, 11, 17, 14, 5, 9, 0, time.UTC).UnixMilli()

func TestFormatTimes(t *testing.T) {
	cases := []struct {
		loc, zone string
		style     Style
		dt        bool
		want      string
	}{
		{"en-GB", "Europe/London", Short, false, "14:05"},
		{"en-GB", "Europe/London", Long, false, "14:05:09 GMT"},
		{"en-GB", "Asia/Shanghai", Long, false, "22:05:09 GMT+8"},
		{"en-GB", "Asia/Kolkata", Full, false, "19:35:09 GMT+05:30"},
		{"en-GB", "Asia/Kolkata", Long, false, "19:35:09 GMT+5:30"},
		{"en-US", "America/New_York", Short, false, "9:05 AM"},
		{"zh-Hans", "Asia/Shanghai", Short, false, "22:05"},
		{"zh-Hant-HK", "Asia/Hong_Kong", Short, false, "下午10:05"},
		{"ja", "Asia/Tokyo", Short, false, "23:05"},
		{"en-GB", "Europe/London", Medium, true, "17 Nov 2026, 14:05:09"},
		{"zh-Hans", "Asia/Shanghai", Short, true, "2026/11/17 22:05"},
	}
	for _, c := range cases {
		var got string
		var err error
		if c.dt {
			got, err = FormatDateTime(MustLocale(c.loc), c.style, c.zone, ms)
		} else {
			got, err = FormatTime(MustLocale(c.loc), c.style, c.zone, ms)
		}
		if err != nil || got != c.want {
			t.Errorf("time %s %s %s: got %q (%v), want %q", c.loc, c.zone, c.style, got, err, c.want)
		}
	}
	if _, err := FormatTime(MustLocale("en"), Short, "Mars/Base", ms); err == nil {
		t.Error("an unknown zone must be an error")
	}
}

func TestFormatLists(t *testing.T) {
	abc := []string{"a", "b", "c"}
	cases := []struct {
		loc   string
		style ListStyle
		items []string
		want  string
	}{
		{"en-GB", ListAnd, abc, "a, b and c"},
		{"en-US", ListAnd, abc, "a, b, and c"},
		{"en-US", ListOr, abc, "a, b, or c"},
		{"en-US", ListAnd, []string{"a", "b"}, "a and b"},
		{"en-US", ListAnd, []string{"a", "b", "c", "d"}, "a, b, c, and d"},
		{"de", ListAnd, abc, "a, b und c"},
		{"fr", ListAnd, abc, "a, b et c"},
		{"zh-Hans", ListAnd, abc, "a、b和c"},
		{"ja", ListAnd, abc, "a、b、c"},
		{"en", ListAnd, nil, ""},
		{"en", ListAnd, []string{"x"}, "x"},
	}
	for _, c := range cases {
		got, err := FormatList(MustLocale(c.loc), c.style, c.items)
		if err != nil || got != c.want {
			t.Errorf("list %s %s: got %q (%v), want %q", c.loc, c.style, got, err, c.want)
		}
	}
}

func TestFormatRelative(t *testing.T) {
	cases := []struct {
		loc  string
		unit RelUnit
		v    int64
		auto bool
		want string
	}{
		{"en", "day", -1, true, "yesterday"},
		{"en", "day", -1, false, "1 day ago"},
		{"en", "day", -2, true, "2 days ago"},
		{"en", "hour", 3, false, "in 3 hours"},
		{"en", "day", 0, true, "today"},
		{"en", "year", 1, true, "next year"},
		{"fr", "day", -2, false, "il y a 2 jours"},
		{"de", "day", -2, false, "vor 2 Tagen"},
		{"zh-Hans", "day", -2, false, "2天前"},
		{"ru", "day", 5, false, "через 5 дней"},
		{"ru", "day", 21, false, "через 21 день"},
	}
	for _, c := range cases {
		got, err := FormatRelative(MustLocale(c.loc), c.unit, c.v, c.auto)
		if err != nil || got != c.want {
			t.Errorf("relative %s %s %d: got %q (%v), want %q", c.loc, c.unit, c.v, got, err, c.want)
		}
	}
}

func TestLocales(t *testing.T) {
	canon := map[string]string{"zh-hant-hk": "zh-Hant-HK", "EN_gb": "en-GB", "iw": "he", "zh-CN": "zh-CN", "sr-latn-me": "sr-Latn-ME"}
	for in, want := range canon {
		l, err := ParseLocale(in)
		if err != nil || l.Tag != want {
			t.Errorf("parse %q: got %q (%v), want %q", in, l.Tag, err, want)
		}
	}
	for _, bad := range []string{"", "und", "x-private", "en--GB", "123"} {
		if _, err := ParseLocale(bad); err == nil {
			t.Errorf("parse %q: want an error", bad)
		}
	}
	scripts := map[string]string{"zh-CN": "Hans", "zh-HK": "Hant", "zh-TW": "Hant", "zh": "Hans", "sr-ME": "Latn", "ar": "Arab", "en": "Latn"}
	for in, want := range scripts {
		if got := ScriptOf(MustLocale(in)); got != want {
			t.Errorf("script %s: got %q, want %q", in, got, want)
		}
	}
	if !RightToLeft(MustLocale("ar")) || !RightToLeft(MustLocale("he")) || RightToLeft(MustLocale("en-GB")) || RightToLeft(MustLocale("en-XA")) {
		t.Error("direction")
	}
	chains := map[string]string{
		"zh-Hant-HK": "zh-Hant-HK zh-Hant",
		"zh-CN":      "zh-CN zh",
		"zh-HK":      "zh-HK zh-Hant-HK zh-Hant",
		"en-AU":      "en-AU en-001 en",
		"en-GB":      "en-GB en-001 en",
		"fr-CA":      "fr-CA fr",
	}
	for in, want := range chains {
		var got string
		for i, c := range Chain(MustLocale(in)) {
			if i > 0 {
				got += " "
			}
			got += c.Tag
		}
		if got != want {
			t.Errorf("chain %s: got %q, want %q", in, got, want)
		}
	}
	data := map[string]string{"zh-Hans": "zh-Hans", "zh-CN": "zh-Hans", "zh-HK": "zh-Hant-HK", "zh-TW": "zh-Hant", "en-US": "en",
		"en-GB": "en-GB", "pt-BR": "pt", "en-XA": "en", "tlh": "", "fr-CA": "fr-CA"}
	for in, want := range data {
		if got := DataLocale(MustLocale(in)); got != want {
			t.Errorf("data locale %s: got %q, want %q", in, got, want)
		}
	}
	sup := []Locale{MustLocale("en-GB"), MustLocale("zh-Hans"), MustLocale("fr")}
	neg := map[string]string{
		"zh-CN,zh;q=0.9,en;q=0.8": "zh-Hans",
		"fr-CA,fr;q=0.9":          "fr",
		"en-US,en;q=0.9":          "en-GB",
		"de-DE":                   "en-GB",
		"":                        "en-GB",
		"garbage;;;":              "en-GB",
	}
	for h, want := range neg {
		if got := Negotiate(sup, h, MustLocale("en")); got.Tag != want {
			t.Errorf("negotiate %q: got %s, want %s", h, got.Tag, want)
		}
	}
}

func TestBuildTags(t *testing.T) {
	got := BuildTags(MustLocale("en-GB"))
	if len(got) != 2 || got[0] != "sky_cldr_en_GB" || got[1] != "sky_cldr_en_001" {
		t.Errorf("en-GB tags: %v", got)
	}
	got = BuildTags(MustLocale("zh-Hans"))
	if len(got) != 2 || got[0] != "sky_cldr_zh_Hans" || got[1] != "sky_cldr_zh" {
		t.Errorf("zh-Hans tags: %v", got)
	}
	got = BuildTags(MustLocale("zh-Hant-HK"))
	if len(got) != 2 || got[0] != "sky_cldr_zh_Hant_HK" || got[1] != "sky_cldr_zh_Hant" {
		t.Errorf("zh-Hant-HK tags: %v", got)
	}
}

func TestUnknownLocaleIsAnError(t *testing.T) {
	_, err := FormatInt(MustLocale("tlh"), 1)
	var ue *UnknownLocaleError
	if !errors.As(err, &ue) {
		t.Errorf("want UnknownLocaleError, got %v", err)
	}
}
