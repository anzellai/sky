package i18n

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/shopspring/decimal"
)

// affix part kinds of a number pattern.
const (
	affLiteral = iota
	affCurrency
	affPercent
	affMinus
	affPlus
)

type affix struct {
	kind int
	text string
}

// numPattern is a parsed CLDR number pattern.
type numPattern struct {
	posPrefix, posSuffix []affix
	negPrefix, negSuffix []affix
	hasNeg               bool
	minInt               int
	minFrac, maxFrac     int
	primary, secondary   int // grouping sizes; 0 = no grouping
}

// parseNumPattern reads a pattern such as "#,##,##0.###" or "¤#,##0.00;-¤#,##0.00".
func parseNumPattern(p string) (numPattern, error) {
	var np numPattern
	pos, neg, hasNeg := splitPattern(p)
	pre, body, suf, err := splitAffixes(pos)
	if err != nil {
		return np, fmt.Errorf("number pattern %q: %v", p, err)
	}
	np.posPrefix, np.posSuffix = pre, suf
	if hasNeg {
		npre, _, nsuf, err := splitAffixes(neg)
		if err != nil {
			return np, fmt.Errorf("number pattern %q: %v", p, err)
		}
		np.negPrefix, np.negSuffix, np.hasNeg = npre, nsuf, true
	}
	ip, fp, _ := strings.Cut(body, ".")
	np.minInt = strings.Count(ip, "0")
	if np.minInt == 0 {
		np.minInt = 1
	}
	np.minFrac = strings.Count(fp, "0")
	np.maxFrac = len(fp)
	if i := strings.LastIndex(ip, ","); i >= 0 {
		np.primary = len(ip) - i - 1
		rest := ip[:i]
		if j := strings.LastIndex(rest, ","); j >= 0 {
			np.secondary = len(rest) - j - 1
		} else {
			np.secondary = np.primary
		}
	}
	return np, nil
}

// splitPattern splits at the ";" that is not inside quotes.
func splitPattern(p string) (string, string, bool) {
	inQ := false
	for i, r := range p {
		switch {
		case r == '\'':
			inQ = !inQ
		case r == ';' && !inQ:
			return p[:i], p[i+1:], true
		}
	}
	return p, "", false
}

func isBodyRune(r rune) bool {
	return r == '#' || r == '0' || r == ',' || r == '.' || (r >= '1' && r <= '9') || r == '@'
}

// splitAffixes separates the prefix, the number body and the suffix.
func splitAffixes(p string) ([]affix, string, []affix, error) {
	var pre, suf []affix
	var body strings.Builder
	state := 0 // 0 prefix, 1 body, 2 suffix
	inQ := false
	var lit strings.Builder
	flush := func(into *[]affix) {
		if lit.Len() > 0 {
			*into = append(*into, affix{affLiteral, lit.String()})
			lit.Reset()
		}
	}
	cur := func() *[]affix {
		if state == 0 {
			return &pre
		}
		return &suf
	}
	runes := []rune(p)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == '\'' {
			if i+1 < len(runes) && runes[i+1] == '\'' {
				lit.WriteRune('\'')
				i++
				continue
			}
			inQ = !inQ
			continue
		}
		if inQ {
			lit.WriteRune(r)
			continue
		}
		if isBodyRune(r) && state != 2 {
			if state == 0 {
				flush(&pre)
				state = 1
			}
			body.WriteRune(r)
			continue
		}
		if state == 1 {
			state = 2
		}
		switch r {
		case '¤':
			flush(cur())
			*cur() = append(*cur(), affix{affCurrency, ""})
		case '%':
			flush(cur())
			*cur() = append(*cur(), affix{affPercent, ""})
		case '-':
			flush(cur())
			*cur() = append(*cur(), affix{affMinus, ""})
		case '+':
			flush(cur())
			*cur() = append(*cur(), affix{affPlus, ""})
		default:
			lit.WriteRune(r)
		}
	}
	flush(cur())
	if body.Len() == 0 {
		return nil, "", nil, fmt.Errorf("no number in the pattern")
	}
	return pre, body.String(), suf, nil
}

// numOptions changes a pattern's digits (MessageFormat options).
type numOptions struct {
	minFrac, maxFrac, minInt int // -1 = the pattern's
	grouping                 string
}

func defaultNumOptions() numOptions {
	return numOptions{minFrac: -1, maxFrac: -1, minInt: -1, grouping: "auto"}
}

// numberFormatter formats with one locale's symbols.
type numberFormatter struct {
	d *data
}

func (f numberFormatter) digits(s string) string {
	ds := []rune(f.d.get("num.digits"))
	if len(ds) != 10 || string(ds) == "0123456789" {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(ds[r-'0'])
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// body formats the absolute value of v with the pattern's digit rules. It
// rounds half to even, as CLDR and ICU do by default.
func (f numberFormatter) body(v decimal.Decimal, np numPattern, opt numOptions) (string, bool) {
	minFrac, maxFrac, minInt := np.minFrac, np.maxFrac, np.minInt
	if opt.maxFrac >= 0 {
		maxFrac = opt.maxFrac
		if minFrac > maxFrac {
			minFrac = maxFrac
		}
	}
	if opt.minFrac >= 0 {
		minFrac = opt.minFrac
		if maxFrac < minFrac {
			maxFrac = minFrac
		}
	}
	if opt.minInt >= 0 {
		minInt = opt.minInt
	}
	r := v.RoundBank(int32(maxFrac))
	neg := r.Sign() < 0
	s := r.Abs().StringFixed(int32(maxFrac))
	ip, fp, _ := strings.Cut(s, ".")
	for len(fp) > minFrac && strings.HasSuffix(fp, "0") {
		fp = fp[:len(fp)-1]
	}
	for len(ip) < minInt {
		ip = "0" + ip
	}
	minGroup := 1
	if f.d.get("num.mingroup") == "2" {
		minGroup = 2
	}
	if opt.grouping == "min2" {
		minGroup = 2
	}
	if np.primary > 0 && opt.grouping != "never" {
		if opt.grouping == "always" || len(ip) >= np.primary+minGroup {
			ip = group(ip, np.primary, np.secondary, f.d.get("num.grp"))
		}
	}
	out := f.digits(ip)
	if fp != "" {
		out += f.d.get("num.dec") + f.digits(fp)
	}
	return out, neg
}

// group inserts the group separator: the primary size from the right, then
// the secondary size (3,3 for "#,##0"; 3,2 for Indian "#,##,##0").
func group(ip string, primary, secondary int, sep string) string {
	if len(ip) <= primary {
		return ip
	}
	var parts []string
	head := ip[:len(ip)-primary]
	tail := ip[len(ip)-primary:]
	for len(head) > secondary {
		parts = append([]string{head[len(head)-secondary:]}, parts...)
		head = head[:len(head)-secondary]
	}
	if head != "" {
		parts = append([]string{head}, parts...)
	}
	parts = append(parts, tail)
	return strings.Join(parts, sep)
}

// render puts the body between the affixes of the pattern.
func (f numberFormatter) render(v decimal.Decimal, np numPattern, opt numOptions, currency string) string {
	body, neg := f.body(v, np, opt)
	prefix, suffix := np.posPrefix, np.posSuffix
	minusFront := false
	if neg {
		if np.hasNeg {
			prefix, suffix = np.negPrefix, np.negSuffix
		} else {
			minusFront = true
		}
	}
	var b strings.Builder
	if minusFront {
		b.WriteString(f.d.get("num.minus"))
	}
	f.affixes(&b, prefix, currency)
	// Currency spacing (CLDR currencySpacing): a symbol whose character next
	// to the number is not a symbol or a space gets the insert between.
	if len(prefix) > 0 && prefix[len(prefix)-1].kind == affCurrency {
		if r, _ := utf8.DecodeLastRuneInString(currency); spacingApplies(r) {
			b.WriteString(f.d.get("cur.sp.before"))
		}
	}
	b.WriteString(body)
	if len(suffix) > 0 && suffix[0].kind == affCurrency {
		if r, _ := utf8.DecodeRuneInString(currency); spacingApplies(r) {
			b.WriteString(f.d.get("cur.sp.after"))
		}
	}
	f.affixes(&b, suffix, currency)
	return b.String()
}

// spacingApplies is CLDR's currencyMatch [[:^S:]&[:^Z:]].
func spacingApplies(r rune) bool {
	if r == utf8.RuneError {
		return false
	}
	return !unicode.In(r, unicode.S, unicode.Z)
}

func (f numberFormatter) affixes(b *strings.Builder, as []affix, currency string) {
	for _, a := range as {
		switch a.kind {
		case affLiteral:
			b.WriteString(a.text)
		case affCurrency:
			b.WriteString(currency)
		case affPercent:
			b.WriteString(f.d.get("num.pct"))
		case affMinus:
			b.WriteString(f.d.get("num.minus"))
		case affPlus:
			b.WriteString(f.d.get("num.plus"))
		}
	}
}

func (f numberFormatter) pattern(key string) (numPattern, error) {
	return parseNumPattern(f.d.get(key))
}

// FormatDecimal formats a decimal with the locale's decimal pattern
// (up to three fraction digits, half-even rounding).
func FormatDecimal(l Locale, v decimal.Decimal) (string, error) {
	return formatDecimalWith(l, v, defaultNumOptions())
}

func formatDecimalWith(l Locale, v decimal.Decimal, opt numOptions) (string, error) {
	d, err := dataFor(l)
	if err != nil {
		return "", err
	}
	f := numberFormatter{d}
	np, err := f.pattern("num.pat.dec")
	if err != nil {
		return "", err
	}
	return f.render(v, np, opt, ""), nil
}

// FormatInt formats an integer with the locale's decimal pattern.
func FormatInt(l Locale, n int64) (string, error) {
	return FormatDecimal(l, decimal.NewFromInt(n))
}

// FormatPercent formats a ratio as a percentage: 0.125 gives "12.5%" in
// English. It keeps up to three fraction digits of the percentage.
func FormatPercent(l Locale, ratio decimal.Decimal) (string, error) {
	d, err := dataFor(l)
	if err != nil {
		return "", err
	}
	f := numberFormatter{d}
	np, err := f.pattern("num.pat.pct")
	if err != nil {
		return "", err
	}
	opt := defaultNumOptions()
	opt.maxFrac = 3
	return f.render(ratio.Mul(decimal.NewFromInt(100)), np, opt, ""), nil
}

// CurrencyDigits gives the fraction digits CLDR gives a currency (2 when the
// code is not one of Std.Money's codes).
func CurrencyDigits(code string) int {
	if d, ok := cldrCurrencyDigits[code]; ok {
		return d
	}
	return 2
}

// FormatMoney formats an amount in a currency with the locale's currency
// pattern, symbol and spacing, rounded to the currency's digits.
func FormatMoney(l Locale, amount decimal.Decimal, code string) (string, error) {
	d, err := dataFor(l)
	if err != nil {
		return "", err
	}
	code = strings.ToUpper(code)
	f := numberFormatter{d}
	sym := d.get("cur.sym." + code)
	if sym == "" {
		sym = code
	}
	key := "num.pat.cur"
	if alt := d.get("num.pat.curalpha"); alt != "" {
		np, err := parseNumPattern(d.get(key))
		if err != nil {
			return "", err
		}
		var r rune
		if len(np.posPrefix) > 0 && np.posPrefix[len(np.posPrefix)-1].kind == affCurrency {
			r, _ = utf8.DecodeLastRuneInString(sym)
		} else {
			r, _ = utf8.DecodeRuneInString(sym)
		}
		if unicode.IsLetter(r) {
			key = "num.pat.curalpha"
		}
	}
	np, err := f.pattern(key)
	if err != nil {
		return "", err
	}
	digits := CurrencyDigits(code)
	opt := defaultNumOptions()
	opt.minFrac, opt.maxFrac = digits, digits
	return f.render(amount, np, opt, sym), nil
}
