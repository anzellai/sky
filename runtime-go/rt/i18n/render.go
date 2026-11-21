package i18n

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/shopspring/decimal"
)

// ArgKind is the kind of a message argument. The letters are the wire form
// Std.I18n uses for Arg.
type ArgKind byte

const (
	ArgString  ArgKind = 's'
	ArgInt     ArgKind = 'i'
	ArgDecimal ArgKind = 'd'
	ArgMoney   ArgKind = 'm' // payload "CODE amount"
	ArgPercent ArgKind = 'p' // payload: the ratio (0.125 is 12.5%)
	ArgTime    ArgKind = 't' // payload "ms zone"
	ArgDate    ArgKind = 'D' // payload "YYYY-MM-DD"
	ArgList    ArgKind = 'l' // payload: items joined by \x1f
)

// Arg is one typed message argument.
type Arg struct {
	Kind    ArgKind
	Payload string
}

// Args are named arguments.
type Args map[string]Arg

// numeric gives the decimal value of a numeric argument.
func (a Arg) numeric() (decimal.Decimal, bool) {
	switch a.Kind {
	case ArgInt, ArgDecimal, ArgPercent:
		d, err := decimal.NewFromString(a.Payload)
		return d, err == nil
	case ArgString:
		d, err := decimal.NewFromString(a.Payload)
		return d, err == nil
	}
	return decimal.Zero, false
}

// formatArg formats an argument by its kind, with no function.
func formatArg(l Locale, a Arg) (string, error) {
	switch a.Kind {
	case ArgString:
		return a.Payload, nil
	case ArgInt, ArgDecimal:
		d, ok := a.numeric()
		if !ok {
			return "", fmt.Errorf("%q is not a number", a.Payload)
		}
		return FormatDecimal(l, d)
	case ArgPercent:
		d, ok := a.numeric()
		if !ok {
			return "", fmt.Errorf("%q is not a number", a.Payload)
		}
		return FormatPercent(l, d)
	case ArgMoney:
		code, amt, _ := strings.Cut(a.Payload, " ")
		d, err := decimal.NewFromString(amt)
		if err != nil {
			return "", fmt.Errorf("%q is not an amount", amt)
		}
		return FormatMoney(l, d, code)
	case ArgTime:
		msS, zone, _ := strings.Cut(a.Payload, " ")
		ms, err := strconv.ParseInt(msS, 10, 64)
		if err != nil {
			return "", fmt.Errorf("%q is not a time", msS)
		}
		return FormatDateTime(l, Medium, zone, ms)
	case ArgDate:
		return FormatDate(l, Medium, a.Payload)
	case ArgList:
		var items []string
		if a.Payload != "" {
			items = strings.Split(a.Payload, "\x1f")
		}
		return FormatList(l, ListAnd, items)
	}
	return "", fmt.Errorf("unknown argument kind %q", a.Kind)
}

// value is a resolved operand: an argument with the function that annotates it.
type value struct {
	arg  Arg
	fn   string
	opts map[string]string
	ok   bool
}

// renderer renders one message for one locale.
type renderer struct {
	fmtLocale  Locale // the locale whose CLDR data formats values
	plurLocale Locale
	args       Args
	vars       map[string]value
	problems   []string
}

func (r *renderer) optsOf(e *Expr) map[string]string {
	if len(e.Opts) == 0 {
		return nil
	}
	m := map[string]string{}
	for _, o := range e.Opts {
		if o.IsVar {
			if v, ok := r.lookup(o.Value); ok {
				m[o.Name] = v.arg.Payload
			}
			continue
		}
		m[o.Name] = o.Value
	}
	return m
}

func (r *renderer) lookup(name string) (value, bool) {
	if v, ok := r.vars[name]; ok {
		return v, v.ok
	}
	a, ok := r.args[name]
	if !ok {
		return value{}, false
	}
	return value{arg: a, ok: true}, true
}

// resolve evaluates an expression to a value. A function on the expression
// replaces the annotation an earlier declaration gave the variable.
func (r *renderer) resolve(e *Expr) value {
	var v value
	switch {
	case e.Var != "":
		got, ok := r.lookup(e.Var)
		if !ok {
			return value{}
		}
		v = got
	case e.Lit != nil:
		v = value{arg: Arg{Kind: ArgString, Payload: *e.Lit}, ok: true}
	default:
		v = value{ok: true}
	}
	if e.Func != "" {
		v.fn = e.Func
		v.opts = r.optsOf(e)
	}
	return v
}

func atoiOr(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

// numberOf gives the rounded value of a :number / :integer value and its
// options, as a plain decimal string (the operand of plural selection and of
// exact keys).
func numberOf(v value) (decimal.Decimal, numOptions, bool) {
	d, ok := v.arg.numeric()
	if !ok {
		return d, numOptions{}, false
	}
	opt := defaultNumOptions()
	if v.fn == "integer" {
		opt.maxFrac, opt.minFrac = 0, 0
	}
	if s, ok := v.opts["minimumFractionDigits"]; ok && v.fn == "number" {
		opt.minFrac = atoiOr(s, -1)
	}
	if s, ok := v.opts["maximumFractionDigits"]; ok && v.fn == "number" {
		opt.maxFrac = atoiOr(s, -1)
	}
	if s, ok := v.opts["minimumIntegerDigits"]; ok {
		opt.minInt = atoiOr(s, -1)
	}
	if s, ok := v.opts["useGrouping"]; ok {
		opt.grouping = s
	}
	return d, opt, true
}

// plainDigits gives the digits plural rules and exact keys see: the value
// rounded and trimmed as the display would be, in ASCII.
func plainDigits(d decimal.Decimal, opt numOptions) string {
	maxF, minF := opt.maxFrac, opt.minFrac
	if maxF < 0 {
		maxF = 3
	}
	if minF < 0 {
		minF = 0
	}
	if minF > maxF {
		maxF = minF
	}
	s := d.RoundBank(int32(maxF)).StringFixed(int32(maxF))
	if i := strings.IndexByte(s, '.'); i >= 0 {
		for len(s)-i-1 > minF && strings.HasSuffix(s, "0") {
			s = s[:len(s)-1]
		}
		s = strings.TrimSuffix(s, ".")
	}
	return s
}

// format renders a value as text.
func (r *renderer) format(v value) (string, error) {
	switch v.fn {
	case "number", "integer":
		d, opt, ok := numberOf(v)
		if !ok {
			return "", fmt.Errorf(":%s needs a number, the argument is %q", v.fn, v.arg.Payload)
		}
		return formatDecimalWith(r.fmtLocale, d, opt)
	case "string":
		if v.arg.Kind == ArgString {
			return v.arg.Payload, nil
		}
	}
	return formatArg(r.fmtLocale, v.arg)
}

func (r *renderer) pattern(pat Pattern) string {
	var b strings.Builder
	for _, part := range pat {
		if part.Expr == nil {
			b.WriteString(part.Text)
			continue
		}
		v := r.resolve(part.Expr)
		if !v.ok {
			b.WriteString(fallbackText(part.Expr))
			r.problems = append(r.problems, fmt.Sprintf("no argument %q", part.Expr.Var))
			continue
		}
		s, err := r.format(v)
		if err != nil {
			b.WriteString(fallbackText(part.Expr))
			r.problems = append(r.problems, err.Error())
			continue
		}
		b.WriteString(s)
	}
	return b.String()
}

// fallbackText is what a placeholder shows when it has no value: its source.
func fallbackText(e *Expr) string {
	switch {
	case e.Bare:
		return "{" + e.Var + "}"
	case e.Var != "":
		return "{$" + e.Var + "}"
	case e.Lit != nil:
		return "{|" + *e.Lit + "|}"
	}
	return "{:" + e.Func + "}"
}

// match ranks how a key matches a value: 0 exact, 1 category, 2 *, -1 not.
func (r *renderer) match(k Key, v value) int {
	if k.Star {
		return 2
	}
	if !v.ok {
		return -1
	}
	switch v.fn {
	case "number", "integer":
		d, opt, ok := numberOf(v)
		if !ok {
			return -1
		}
		plain := plainDigits(d.Abs(), opt)
		if kd, err := decimal.NewFromString(k.Value); err == nil {
			if pd, err := decimal.NewFromString(plainDigits(d, opt)); err == nil && kd.Equal(pd) {
				return 0
			}
			return -1
		}
		sel := v.opts["select"]
		if sel == "exact" {
			return -1
		}
		var cat PluralCategory
		var err error
		if sel == "ordinal" {
			cat, err = Ordinal(r.plurLocale, plain)
		} else {
			cat, err = Cardinal(r.plurLocale, plain)
		}
		if err == nil && cat.String() == k.Value {
			return 1
		}
		return -1
	default:
		s := v.arg.Payload
		if v.arg.Kind != ArgString {
			if f, err := formatArg(r.fmtLocale, v.arg); err == nil {
				s = f
			}
		}
		if s == k.Value {
			return 0
		}
		return -1
	}
}

// Render renders a message with arguments. fmtLocale formats values;
// plurLocale selects plural categories (the catalogue locale of the message).
// problems lists what fell back (a missing argument, a value of the wrong kind).
func (m *Message) Render(fmtLocale, plurLocale Locale, args Args) (text string, problems []string) {
	r := &renderer{fmtLocale: fmtLocale, plurLocale: plurLocale, args: args, vars: map[string]value{}}
	for i := range m.Decls {
		d := &m.Decls[i]
		r.vars[d.Name] = r.resolve(&d.Expr)
	}
	if len(m.Selectors) == 0 {
		return r.pattern(m.Pattern), r.problems
	}
	vals := make([]value, len(m.Selectors))
	for i, s := range m.Selectors {
		vals[i], _ = r.lookup(s)
		if !vals[i].ok {
			r.problems = append(r.problems, fmt.Sprintf("no argument %q for .match", s))
		}
	}
	best := -1
	var bestRank []int
	for vi, v := range m.Variants {
		rank := make([]int, len(vals))
		okAll := true
		for i, k := range v.Keys {
			rank[i] = r.match(k, vals[i])
			if rank[i] < 0 {
				okAll = false
				break
			}
		}
		if !okAll {
			continue
		}
		if best < 0 || less(rank, bestRank) {
			best, bestRank = vi, rank
		}
	}
	if best < 0 {
		r.problems = append(r.problems, "no variant matches")
		return "", r.problems
	}
	return r.pattern(m.Variants[best].Pattern), r.problems
}

func less(a, b []int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// ─── Catalogue ──────────────────────────────────────────────────────────────

// Catalogue is the set of validated messages of a program.
type Catalogue struct {
	Default  Locale
	Locales  []Locale
	messages map[string]map[string]*Message // maximised locale → key → message
	byMax    map[string]Locale              // maximised → catalogue locale
}

// CatalogueJSON is the form the build-time check writes and Install reads.
type CatalogueJSON struct {
	Default  string                       `json:"default"`
	Locales  []string                     `json:"locales"`
	Messages map[string]map[string]string `json:"messages"`
}

var (
	installed  atomic.Pointer[Catalogue]
	production atomic.Bool
	logf       atomic.Pointer[func(string)]
	loggedMu   sync.Mutex
	logged     = map[string]bool{}
)

// SetLogger sets where the package reports a message problem (once each).
func SetLogger(f func(string)) { logf.Store(&f) }

// SetProduction sets whether a missing key shows as ⟦key⟧ (false) or as the
// key's plain text (true).
func SetProduction(p bool) { production.Store(p) }

func logOnce(key, msg string) {
	loggedMu.Lock()
	seen := logged[key]
	if !seen {
		if len(logged) > 10000 {
			logged = map[string]bool{}
		}
		logged[key] = true
	}
	loggedMu.Unlock()
	if seen {
		return
	}
	if f := logf.Load(); f != nil {
		(*f)(msg)
	}
}

// Install loads the catalogue the build embedded. The build validated it, so
// an error here means the embedded text is damaged.
func Install(text string) error {
	var cj CatalogueJSON
	if err := json.Unmarshal([]byte(text), &cj); err != nil {
		return fmt.Errorf("the embedded i18n catalogue does not read: %v", err)
	}
	c, err := NewCatalogue(cj)
	if err != nil {
		return err
	}
	installed.Store(c)
	return nil
}

// NewCatalogue parses every message of a catalogue.
func NewCatalogue(cj CatalogueJSON) (*Catalogue, error) {
	def, err := ParseLocale(cj.Default)
	if err != nil {
		return nil, err
	}
	c := &Catalogue{Default: def, messages: map[string]map[string]*Message{}, byMax: map[string]Locale{}}
	for _, t := range cj.Locales {
		l, err := ParseLocale(t)
		if err != nil {
			return nil, err
		}
		c.Locales = append(c.Locales, l)
		c.byMax[Maximised(l)] = l
		c.messages[Maximised(l)] = map[string]*Message{}
	}
	for t, msgs := range cj.Messages {
		l, err := ParseLocale(t)
		if err != nil {
			return nil, err
		}
		into := c.messages[Maximised(l)]
		if into == nil {
			return nil, fmt.Errorf("catalogue messages for %s, which is not a catalogue locale", t)
		}
		for k, src := range msgs {
			m, err := ParseMessage(src)
			if err != nil {
				return nil, fmt.Errorf("catalogue %s %s: %v", t, k, err)
			}
			into[k] = m
		}
	}
	return c, nil
}

// Installed gives the program's catalogue. A program with no [i18n] section
// has an empty catalogue whose default and only locale is en.
func Installed() *Catalogue {
	if c := installed.Load(); c != nil {
		return c
	}
	en := MustLocale("en")
	return &Catalogue{Default: en, Locales: []Locale{en}, messages: map[string]map[string]*Message{}, byMax: map[string]Locale{}}
}

// Overrides are extra messages that win over the catalogue: locale → key → message.
type Overrides struct {
	byMax map[string]map[string]*Message
}

// override is one parsed override message, cached by its source.
var overrideCache sync.Map

func parseCached(src string) (*Message, error) {
	if m, ok := overrideCache.Load(src); ok {
		return m.(*Message), nil
	}
	m, err := ParseMessage(src)
	if err != nil {
		return nil, err
	}
	overrideCache.Store(src, m)
	return m, nil
}

// OverrideError says that an override is not valid.
type OverrideError struct {
	Locale, Key string
	Err         error
}

func (e *OverrideError) Error() string {
	return fmt.Sprintf("override %s %s: %v", e.Locale, e.Key, e.Err)
}

// NewOverrides parses and checks overrides given as (locale, key, message).
func NewOverrides(entries [][3]string) (*Overrides, error) {
	o := &Overrides{byMax: map[string]map[string]*Message{}}
	for _, e := range entries {
		l, err := ParseLocale(e[0])
		if err != nil {
			return nil, &OverrideError{Locale: e[0], Key: e[1], Err: err}
		}
		m, err := parseCached(e[2])
		if err != nil {
			return nil, &OverrideError{Locale: l.Tag, Key: e[1], Err: err}
		}
		mx := Maximised(l)
		if o.byMax[mx] == nil {
			o.byMax[mx] = map[string]*Message{}
		}
		o.byMax[mx][e[1]] = m
	}
	return o, nil
}

// Lookup gives the message for a key, the locale it comes from, and whether
// one exists: for each locale of the chain, an override, then the catalogue.
func (c *Catalogue) Lookup(o *Overrides, l Locale, key string) (*Message, Locale, bool) {
	for _, step := range Fallbacks(l, c.Default) {
		mx := Maximised(step)
		if o != nil {
			if m, ok := o.byMax[mx][key]; ok {
				if cl, ok := c.byMax[mx]; ok {
					return m, cl, true
				}
				return m, step, true
			}
		}
		if m, ok := c.messages[mx][key]; ok {
			return m, c.byMax[mx], true
		}
	}
	return nil, Locale{}, false
}

// Render renders a key for a locale. A key with no message anywhere gives
// ⟦key⟧ outside production and the key itself in production, and is logged
// once. A problem inside a message (a missing argument) shows the
// placeholder's source and is logged once.
func (c *Catalogue) Render(o *Overrides, l Locale, key string, args Args) string {
	m, from, ok := c.Lookup(o, l, key)
	if !ok {
		logOnce("missing\x00"+key, fmt.Sprintf("i18n: no message for key %q (locale %s)", key, l.Tag))
		if production.Load() {
			return key
		}
		return "⟦" + key + "⟧"
	}
	// Values format with the CLDR data of the message's locale, which the
	// build compiled in. An override from a locale with no compiled data
	// formats with the default locale's.
	fmtLocale := from
	if _, err := dataFor(fmtLocale); err != nil {
		fmtLocale = c.Default
	}
	text, problems := m.Render(fmtLocale, from, args)
	for _, p := range problems {
		logOnce("problem\x00"+from.Tag+"\x00"+key+"\x00"+p, fmt.Sprintf("i18n: message %q (%s): %s", key, from.Tag, p))
	}
	return text
}
