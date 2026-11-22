package i18n

import (
	"fmt"
	"strings"
)

// PluralCategory is a CLDR plural category.
type PluralCategory int

const (
	Zero PluralCategory = iota
	One
	Two
	Few
	Many
	Other
)

var categoryNames = [...]string{"zero", "one", "two", "few", "many", "other"}

func (c PluralCategory) String() string { return categoryNames[c] }

// CategoryFromName gives the category of a CLDR name ("one").
func CategoryFromName(s string) (PluralCategory, bool) {
	for i, n := range categoryNames {
		if n == s {
			return PluralCategory(i), true
		}
	}
	return Other, false
}

// pluralOperands are the CLDR plural operands of a number's absolute value.
// i is the integer part (its last 18 digits for a larger number), v the count
// of visible fraction digits, w the same without trailing zeros, f the
// visible fraction digits as an integer, t the same without trailing zeros,
// and e the compact exponent (always 0 here).
type pluralOperands struct {
	i, v, w, f, t, e uint64
}

// whole reports whether the number has no fraction (CLDR's n is then an
// integer). A method, not a field test, so a rule that names n twice does not
// read as a redundant condition to go vet.
func (o *pluralOperands) whole() bool { return o.t == 0 }

// operandsOf reads the operands from a decimal string such as "-12.50".
func operandsOf(s string) (pluralOperands, error) {
	var o pluralOperands
	s = strings.TrimPrefix(strings.TrimPrefix(s, "-"), "+")
	ip, fp, _ := strings.Cut(s, ".")
	if ip == "" && fp == "" {
		return o, fmt.Errorf("%q is not a number", s)
	}
	for _, r := range ip + fp {
		if r < '0' || r > '9' {
			return o, fmt.Errorf("%q is not a number", s)
		}
	}
	if len(ip) > 18 {
		ip = ip[len(ip)-18:]
	}
	for _, r := range ip {
		o.i = o.i*10 + uint64(r-'0')
	}
	if len(fp) > 18 {
		fp = fp[:18]
	}
	o.v = uint64(len(fp))
	trimmed := strings.TrimRight(fp, "0")
	o.w = uint64(len(trimmed))
	for _, r := range fp {
		o.f = o.f*10 + uint64(r-'0')
	}
	for _, r := range trimmed {
		o.t = o.t*10 + uint64(r-'0')
	}
	return o, nil
}

// ruleKeys gives the keys to try in a plural table for a locale: the chain,
// then the language, then root.
func ruleKeys(l Locale) []string {
	var keys []string
	for _, c := range Chain(l) {
		keys = append(keys, c.Tag)
	}
	return append(keys, l.Language, "root")
}

// Cardinal gives the CLDR cardinal plural category of a decimal string.
func Cardinal(l Locale, number string) (PluralCategory, error) {
	return pluralOf(l, number, pluralCardinalRules)
}

// Ordinal gives the CLDR ordinal plural category of a decimal string.
func Ordinal(l Locale, number string) (PluralCategory, error) {
	return pluralOf(l, number, pluralOrdinalRules)
}

func pluralOf(l Locale, number string, table map[string]func(*pluralOperands) PluralCategory) (PluralCategory, error) {
	o, err := operandsOf(number)
	if err != nil {
		return Other, err
	}
	for _, k := range ruleKeys(l) {
		if fn, ok := table[k]; ok {
			return fn(&o), nil
		}
	}
	return Other, nil
}

// Categories gives the cardinal (or ordinal) categories a locale uses, in
// CLDR order. Every locale uses "other".
func Categories(l Locale, ordinal bool) []PluralCategory {
	table := pluralCardinalCategories
	if ordinal {
		table = pluralOrdinalCategories
	}
	for _, k := range ruleKeys(l) {
		if s, ok := table[k]; ok {
			var out []PluralCategory
			for _, n := range strings.Fields(s) {
				if c, ok := CategoryFromName(n); ok {
					out = append(out, c)
				}
			}
			return out
		}
	}
	return []PluralCategory{Other}
}
