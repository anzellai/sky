//go:build sky_cldr_all

package i18n

import (
	"fmt"
	"strings"
	"testing"

	// The Sky runtime links the zone data; the test binary needs it too,
	// above all under js/wasm, which has no zoneinfo files.
	_ "time/tzdata"
)

// parityCorpus formats a fixed set of values in several locales. The server
// and a Sky.Spa client must give the same lines (TestWasmFormatsLikeNative).
func parityCorpus() []string {
	var out []string
	add := func(what string, s string, err error) {
		if err != nil {
			s = "ERR " + err.Error()
		}
		out = append(out, what+"\t"+s)
	}
	at := int64(1794924309000) // 2026-11-17T14:05:09Z
	for _, tag := range []string{"en-GB", "en-US", "zh-Hans", "zh-Hant-HK", "ar", "de", "fr", "hi", "ja", "ru", "pl", "cy", "bn", "fa"} {
		l := MustLocale(tag)
		s, err := FormatDecimal(l, dec("-1234567.8915"))
		add(tag+" decimal", s, err)
		s, err = FormatPercent(l, dec("0.12345"))
		add(tag+" percent", s, err)
		for _, code := range []string{"GBP", "USD", "JPY", "EUR", "AED", "KWD"} {
			s, err = FormatMoney(l, dec("-1234.505"), code)
			add(tag+" money "+code, s, err)
		}
		for _, st := range []Style{Short, Medium, Long, Full} {
			s, err = FormatDate(l, st, "2026-02-03")
			add(tag+" date "+string(st), s, err)
			s, err = FormatDateTime(l, st, "Asia/Kolkata", at)
			add(tag+" datetime "+string(st), s, err)
			s, err = FormatTime(l, st, "America/New_York", at)
			add(tag+" time "+string(st), s, err)
		}
		for _, ls := range []ListStyle{ListAnd, ListOr, ListUnit} {
			s, err = FormatList(l, ls, []string{"α", "b", "c", "d"})
			add(tag+" list "+string(ls), s, err)
		}
		for _, v := range []int64{-2, -1, 0, 1, 5, 21} {
			s, err = FormatRelative(l, "day", v, true)
			add(fmt.Sprintf("%s relative %d", tag, v), s, err)
		}
		for _, n := range []string{"0", "1", "2", "3", "5", "11", "21", "100", "1.5", "1.0"} {
			c, _ := Cardinal(l, n)
			o, _ := Ordinal(l, n)
			out = append(out, fmt.Sprintf("%s plural %s\t%s %s", tag, n, c, o))
		}
		m, perr := ParseMessage(".input {$n :number}\n.match $n\n0 {{none}}\none {{{$n} one {who}}}\n* {{{$n :number minimumFractionDigits=1} other {who}}}")
		if perr != nil {
			out = append(out, "ERR "+perr.Error())
			continue
		}
		for _, n := range []string{"0", "1", "2", "1234.5"} {
			text, _ := m.Render(l, l, Args{"n": {ArgDecimal, n}, "who": {ArgString, "Ada"}})
			out = append(out, tag+" message "+n+"\t"+text)
		}
	}
	return out
}

// TestParityCorpus prints the corpus. The native test reads it from the
// js/wasm build of this package.
func TestParityCorpus(t *testing.T) {
	for _, line := range parityCorpus() {
		fmt.Println("PARITY\t" + strings.ReplaceAll(line, "\n", "\\n"))
	}
}
