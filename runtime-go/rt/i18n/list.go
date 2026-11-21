package i18n

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"
)

// ListStyle is a CLDR list pattern type.
type ListStyle string

const (
	ListAnd  ListStyle = "standard"
	ListOr   ListStyle = "or"
	ListUnit ListStyle = "unit"
)

// FormatList joins items with the locale's list pattern: "a, b and c".
func FormatList(l Locale, style ListStyle, items []string) (string, error) {
	d, err := dataFor(l)
	if err != nil {
		return "", err
	}
	get := func(part string) string { return d.get("list." + string(style) + "." + part) }
	sub := func(p, a, b string) string { return strings.NewReplacer("{0}", a, "{1}", b).Replace(p) }
	// The two-item and end patterns join the last item: their conjunction
	// can depend on how that item starts (contextualPattern).
	last := func(p, a, b string) string { return sub(contextualPattern(l.Language, p, b), a, b) }
	switch len(items) {
	case 0:
		return "", nil
	case 1:
		return items[0], nil
	case 2:
		return last(get("2"), items[0], items[1]), nil
	case 3:
		if p := get("3"); p != "" {
			return strings.NewReplacer("{0}", items[0], "{1}", items[1], "{2}", items[2]).Replace(p), nil
		}
	}
	// end over the last two, middle inward, start over the first.
	n := len(items)
	acc := last(get("end"), items[n-2], items[n-1])
	for i := n - 3; i >= 1; i-- {
		acc = sub(get("middle"), items[i], acc)
	}
	return sub(get("start"), items[0], acc), nil
}

// The contextual list rules of ICU's ListFormatter (listformatter.cpp), the
// same set and the same conditions (ASCII letters in either case), tested on
// the item that follows the conjunction:
//
//   - Spanish "y" is "e" before an /i/ sound: an item that starts with "i",
//     or is "hi", or starts with "hi" and a letter other than "a" or "e"
//     ("hielo" and "hiato" keep "y").
//   - Spanish "o" is "u" before an /o/ sound: an item that starts with "o",
//     "ho" or "8", or is "11" or starts with "11 ".
//   - Hebrew "ו" is "ו-" before an item that does not start with a Hebrew
//     letter.
//
// ICU applies a rule only where the locale's pattern is exactly the plain
// form, so a pattern CLDR changes is never rewritten.
var (
	spanishECondition   = regexp.MustCompile(`(?s)^([iI].*|[hH][iI]|[hH][iI][^aAeE].*)$`)
	spanishUCondition   = regexp.MustCompile(`(?s)^(([oO]|[hH][oO]|8).*|11( .*)?)$`)
	hebrewDashCondition = regexp.MustCompile(`(?s)^[^\p{Hebrew}].*$`)
)

func contextualPattern(lang, pattern, next string) string {
	switch lang {
	case "es":
		switch {
		case pattern == "{0} y {1}" && spanishECondition.MatchString(next):
			return "{0} e {1}"
		case pattern == "{0} o {1}" && spanishUCondition.MatchString(next):
			return "{0} u {1}"
		}
	case "he":
		if pattern == "{0} \u05d5{1}" && hebrewDashCondition.MatchString(next) {
			return "{0} \u05d5-{1}"
		}
	}
	return pattern
}

// RelUnit is a relative-time unit.
type RelUnit string

// ParseRelUnit reads a CLDR field name: year, month, week, day, hour, minute, second.
func ParseRelUnit(s string) (RelUnit, error) {
	switch s {
	case "year", "month", "week", "day", "hour", "minute", "second":
		return RelUnit(s), nil
	}
	return "", fmt.Errorf("unknown relative-time unit %q", s)
}

// FormatRelative formats a count of units from now: -2 days gives
// "2 days ago", 3 hours gives "in 3 hours". With auto, -1, 0 and 1 use the
// locale's words when it has them ("yesterday", "today", "tomorrow").
func FormatRelative(l Locale, unit RelUnit, value int64, auto bool) (string, error) {
	d, err := dataFor(l)
	if err != nil {
		return "", err
	}
	if auto && value >= -1 && value <= 1 {
		if s := d.get(fmt.Sprintf("rel.%s.%d", unit, value)); s != "" {
			return s, nil
		}
	}
	dir := "f"
	abs := value
	if value < 0 {
		dir, abs = "p", -value
	}
	cat, err := Cardinal(l, strconv.FormatInt(abs, 10))
	if err != nil {
		return "", err
	}
	pat := d.get(fmt.Sprintf("rel.%s.%s.%s", unit, dir, cat))
	if pat == "" {
		pat = d.get(fmt.Sprintf("rel.%s.%s.other", unit, dir))
	}
	num, err := FormatDecimal(l, decimal.NewFromInt(abs))
	if err != nil {
		return "", err
	}
	return strings.Replace(pat, "{0}", num, 1), nil
}
