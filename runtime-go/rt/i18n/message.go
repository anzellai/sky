package i18n

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The message syntax is a subset of Unicode MessageFormat 2.0 (stable since
// CLDR 47), with one addition for the form apps already write:
//
//   - Text with placeholders: "Hello {$name}", "{$n :number maximumFractionDigits=1}".
//   - {name} with no "$" and no function is the variable $name. (In MF2 it is
//     the literal "name". Write {|name|} for that literal.)
//   - Functions: :string, :number, :integer. Any other function is an error.
//   - A complex message starts with .input, .local, .match or {{.
//     .match selects a variant by plural category or exact number
//     (:number, :integer) or by string (:string). A catch-all variant (all
//     keys *) is required.
//   - Escapes in text and quoted patterns: \\ \{ \} \|. In a quoted literal
//     |...|: \\ and \|.
//   - Markup ({#b}, {/b}) and attributes (@x) are not supported.

// Message is a parsed message.
type Message struct {
	Decls     []Decl
	Pattern   Pattern // when there is no matcher
	Selectors []string
	Variants  []Variant
}

// Decl is a .input or .local declaration.
type Decl struct {
	Input bool
	Name  string
	Expr  Expr
}

// Variant is one variant of a matcher.
type Variant struct {
	Keys    []Key
	Pattern Pattern
}

// Key is a variant key: * or a literal.
type Key struct {
	Star  bool
	Value string
}

// Pattern is text and placeholders.
type Pattern []Part

// Part is text (Expr == nil) or a placeholder.
type Part struct {
	Text string
	Expr *Expr
}

// Expr is a placeholder or a declaration's value.
type Expr struct {
	Var  string  // operand variable, "" when none
	Lit  *string // literal operand
	Func string  // "", "string", "number", "integer"
	Opts []Opt
	Bare bool // written {name}
}

// Opt is a function option.
type Opt struct {
	Name  string
	Value string // literal value, or the variable name when IsVar
	IsVar bool
}

// SyntaxError is a parse error at a byte offset of the source.
type SyntaxError struct {
	Pos int
	Msg string
}

func (e *SyntaxError) Error() string { return fmt.Sprintf("at character %d: %s", e.Pos+1, e.Msg) }

// FunctionError is an unknown function or option, or a bad option value.
type FunctionError struct{ Msg string }

func (e *FunctionError) Error() string { return e.Msg }

// knownOptions gives the options each function takes.
var knownOptions = map[string]map[string]bool{
	"string":  {},
	"number":  {"minimumFractionDigits": true, "maximumFractionDigits": true, "minimumIntegerDigits": true, "useGrouping": true, "select": true},
	"integer": {"minimumIntegerDigits": true, "useGrouping": true, "select": true},
}

// ParseMessage parses and validates a message.
func ParseMessage(src string) (*Message, error) {
	p := &parser{src: src}
	m, err := p.message()
	if err != nil {
		return nil, err
	}
	if err := m.validate(); err != nil {
		return nil, err
	}
	return m, nil
}

type parser struct {
	src string
	pos int
}

func (p *parser) eof() bool { return p.pos >= len(p.src) }

func (p *parser) peek() rune {
	if p.eof() {
		return 0
	}
	r, _ := utf8.DecodeRuneInString(p.src[p.pos:])
	return r
}

func (p *parser) next() rune {
	r, n := utf8.DecodeRuneInString(p.src[p.pos:])
	p.pos += n
	return r
}

func (p *parser) errf(format string, args ...any) error {
	return &SyntaxError{Pos: utf8.RuneCountInString(p.src[:min(p.pos, len(p.src))]), Msg: fmt.Sprintf(format, args...)}
}

func isSpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '　'
}

func (p *parser) skipSpace() bool {
	start := p.pos
	for !p.eof() && isSpace(p.peek()) {
		p.next()
	}
	return p.pos > start
}

func (p *parser) hasPrefix(s string) bool { return strings.HasPrefix(p.src[p.pos:], s) }

func (p *parser) message() (*Message, error) {
	trimmed := strings.TrimLeftFunc(p.src, isSpace)
	complex := false
	for _, kw := range []string{".input", ".local", ".match", "{{"} {
		if strings.HasPrefix(trimmed, kw) {
			complex = true
		}
	}
	if !complex {
		pat, err := p.pattern(false)
		if err != nil {
			return nil, err
		}
		return &Message{Pattern: pat}, nil
	}
	m := &Message{}
	for {
		p.skipSpace()
		switch {
		case p.hasPrefix(".input"):
			p.pos += len(".input")
			p.skipSpace()
			if p.peek() != '{' {
				return nil, p.errf(".input needs an expression {$name ...}")
			}
			e, err := p.expression()
			if err != nil {
				return nil, err
			}
			if e.Var == "" {
				return nil, p.errf(".input needs a variable: {$name ...}")
			}
			m.Decls = append(m.Decls, Decl{Input: true, Name: e.Var, Expr: *e})
		case p.hasPrefix(".local"):
			p.pos += len(".local")
			if !p.skipSpace() {
				return nil, p.errf("a space must follow .local")
			}
			if p.peek() != '$' {
				return nil, p.errf(".local needs a variable: .local $name = {...}")
			}
			p.next()
			name, err := p.name()
			if err != nil {
				return nil, err
			}
			p.skipSpace()
			if p.peek() != '=' {
				return nil, p.errf("= must follow .local $%s", name)
			}
			p.next()
			p.skipSpace()
			if p.peek() != '{' {
				return nil, p.errf(".local $%s needs an expression", name)
			}
			e, err := p.expression()
			if err != nil {
				return nil, err
			}
			m.Decls = append(m.Decls, Decl{Name: name, Expr: *e})
		case p.hasPrefix(".match"):
			p.pos += len(".match")
			if err := p.matcher(m); err != nil {
				return nil, err
			}
			p.skipSpace()
			if !p.eof() {
				return nil, p.errf("unexpected text after the last variant")
			}
			return m, nil
		case p.hasPrefix("{{"):
			pat, err := p.quotedPattern()
			if err != nil {
				return nil, err
			}
			m.Pattern = pat
			p.skipSpace()
			if !p.eof() {
				return nil, p.errf("unexpected text after }}")
			}
			return m, nil
		case p.hasPrefix("."):
			return nil, p.errf("unknown keyword (use .input, .local or .match)")
		default:
			return nil, p.errf("a complex message needs a body: {{...}} or .match")
		}
	}
}

func (p *parser) matcher(m *Message) error {
	for {
		if !p.skipSpace() {
			break
		}
		if p.peek() != '$' {
			break
		}
		p.next()
		n, err := p.name()
		if err != nil {
			return err
		}
		m.Selectors = append(m.Selectors, n)
	}
	if len(m.Selectors) == 0 {
		return p.errf(".match needs at least one selector variable: .match $count")
	}
	for {
		p.skipSpace()
		if p.eof() {
			break
		}
		var keys []Key
		for !p.hasPrefix("{{") {
			if p.eof() {
				return p.errf("a variant needs a pattern {{...}}")
			}
			if p.peek() == '*' {
				p.next()
				keys = append(keys, Key{Star: true})
			} else {
				lit, _, err := p.literal()
				if err != nil {
					return err
				}
				keys = append(keys, Key{Value: lit})
			}
			p.skipSpace()
		}
		pat, err := p.quotedPattern()
		if err != nil {
			return err
		}
		if len(keys) != len(m.Selectors) {
			return p.errf("a variant has %d keys, the matcher has %d selectors", len(keys), len(m.Selectors))
		}
		m.Variants = append(m.Variants, Variant{Keys: keys, Pattern: pat})
	}
	if len(m.Variants) == 0 {
		return p.errf(".match needs variants")
	}
	return nil
}

func (p *parser) quotedPattern() (Pattern, error) {
	if !p.hasPrefix("{{") {
		return nil, p.errf("expected {{")
	}
	p.pos += 2
	pat, err := p.pattern(true)
	if err != nil {
		return nil, err
	}
	if !p.hasPrefix("}}") {
		return nil, p.errf("expected }} to end the pattern")
	}
	p.pos += 2
	return pat, nil
}

// pattern reads text and placeholders up to the end (or "}}" when quoted).
func (p *parser) pattern(quoted bool) (Pattern, error) {
	var pat Pattern
	var text strings.Builder
	flush := func() {
		if text.Len() > 0 {
			pat = append(pat, Part{Text: text.String()})
			text.Reset()
		}
	}
	for !p.eof() {
		if quoted && p.hasPrefix("}}") {
			break
		}
		r := p.peek()
		switch r {
		case '\\':
			p.next()
			if p.eof() {
				return nil, p.errf("\\ at the end of the message")
			}
			e := p.next()
			if e != '\\' && e != '{' && e != '}' && e != '|' {
				return nil, p.errf("unknown escape \\%c (use \\\\, \\{, \\} or \\|)", e)
			}
			text.WriteRune(e)
		case '{':
			flush()
			e, err := p.expression()
			if err != nil {
				return nil, err
			}
			pat = append(pat, Part{Expr: e})
		case '}':
			return nil, p.errf("} with no { (write \\} for the character)")
		default:
			text.WriteRune(p.next())
		}
	}
	flush()
	return pat, nil
}

func isNameStart(r rune) bool {
	return r == '_' || unicode.IsLetter(r)
}

func isNameChar(r rune) bool {
	return isNameStart(r) || unicode.IsDigit(r) || r == '-' || r == '.' || unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Mc, r)
}

func (p *parser) name() (string, error) {
	start := p.pos
	if p.eof() || !isNameStart(p.peek()) {
		return "", p.errf("expected a name")
	}
	for !p.eof() && isNameChar(p.peek()) {
		p.next()
	}
	return p.src[start:p.pos], nil
}

// literal reads |quoted| or an unquoted name or number. quoted reports the
// |...| form.
func (p *parser) literal() (lit string, quoted bool, err error) {
	if p.peek() == '|' {
		p.next()
		var b strings.Builder
		for {
			if p.eof() {
				return "", true, p.errf("a quoted literal |...| has no closing |")
			}
			r := p.next()
			if r == '|' {
				return b.String(), true, nil
			}
			if r == '\\' {
				if p.eof() {
					return "", true, p.errf("\\ at the end of the message")
				}
				e := p.next()
				if e != '\\' && e != '|' && e != '{' && e != '}' {
					return "", true, p.errf("unknown escape \\%c in a quoted literal", e)
				}
				r = e
			}
			b.WriteRune(r)
		}
	}
	start := p.pos
	for !p.eof() {
		r := p.peek()
		if isNameChar(r) || r == '+' || (r == '-' && p.pos == start) {
			p.next()
			continue
		}
		break
	}
	if p.pos == start {
		return "", false, p.errf("expected a literal, * or {{")
	}
	return p.src[start:p.pos], false, nil
}

func (p *parser) expression() (*Expr, error) {
	p.next() // {
	p.skipSpace()
	e := &Expr{}
	litQuoted := false
	switch r := p.peek(); {
	case r == '$':
		p.next()
		n, err := p.name()
		if err != nil {
			return nil, err
		}
		e.Var = n
	case r == ':':
	case r == '#' || r == '/':
		return nil, p.errf("markup ({#...}, {/...}) is not supported")
	case r == '}':
		return nil, p.errf("an empty placeholder {}")
	default:
		lit, quoted, err := p.literal()
		if err != nil {
			return nil, err
		}
		e.Lit = &lit
		litQuoted = quoted
	}
	spaced := p.skipSpace()
	if p.peek() == ':' {
		if !spaced && (e.Var != "" || e.Lit != nil) {
			return nil, p.errf("a space must come before the function")
		}
		p.next()
		fn, err := p.name()
		if err != nil {
			return nil, err
		}
		e.Func = fn
		for {
			if !p.skipSpace() {
				break
			}
			if p.peek() == '}' || p.peek() == '@' {
				break
			}
			on, err := p.name()
			if err != nil {
				return nil, err
			}
			p.skipSpace()
			if p.peek() != '=' {
				return nil, p.errf("option %s needs = and a value", on)
			}
			p.next()
			p.skipSpace()
			o := Opt{Name: on}
			if p.peek() == '$' {
				p.next()
				v, err := p.name()
				if err != nil {
					return nil, err
				}
				o.Value, o.IsVar = v, true
			} else {
				v, _, err := p.literal()
				if err != nil {
					return nil, err
				}
				o.Value = v
			}
			e.Opts = append(e.Opts, o)
		}
	}
	p.skipSpace()
	if p.peek() == '@' {
		return nil, p.errf("attributes (@name) are not supported")
	}
	if p.peek() != '}' {
		return nil, p.errf("expected } to end the placeholder")
	}
	p.next()
	if e.Var == "" && e.Lit == nil && e.Func == "" {
		return nil, p.errf("an empty placeholder")
	}
	// Sky's simple hole: {name} is the variable $name.
	// A quoted |name| literal is never a hole.
	if e.Lit != nil && !litQuoted && e.Func == "" && len(*e.Lit) > 0 && isNameStart([]rune(*e.Lit)[0]) {
		e.Var, e.Lit, e.Bare = *e.Lit, nil, true
	}
	return e, nil
}

// validate checks functions, options, declarations and the matcher.
func (m *Message) validate() error {
	declared := map[string]*Expr{}
	locals := map[string]bool{}
	for i := range m.Decls {
		d := &m.Decls[i]
		if err := d.Expr.checkFunc(); err != nil {
			return err
		}
		if _, dup := declared[d.Name]; dup {
			return &SyntaxError{Msg: fmt.Sprintf("$%s is declared twice", d.Name)}
		}
		// A .local may not use a variable a later declaration defines.
		declared[d.Name] = &d.Expr
		if !d.Input {
			locals[d.Name] = true
		}
	}
	check := func(pat Pattern) error {
		for _, part := range pat {
			if part.Expr != nil {
				if err := part.Expr.checkFunc(); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if len(m.Selectors) == 0 {
		return check(m.Pattern)
	}
	for _, s := range m.Selectors {
		e, ok := declared[s]
		if !ok || e.Func == "" {
			return &SyntaxError{Msg: fmt.Sprintf(".match $%s needs a declaration with a function, for example .input {$%s :number}", s, s)}
		}
	}
	seen := map[string]bool{}
	fallback := false
	for _, v := range m.Variants {
		if err := check(v.Pattern); err != nil {
			return err
		}
		var sig []string
		all := true
		for _, k := range v.Keys {
			if k.Star {
				sig = append(sig, "*")
			} else {
				all = false
				sig = append(sig, "="+k.Value)
			}
		}
		key := strings.Join(sig, " ")
		if seen[key] {
			return &SyntaxError{Msg: fmt.Sprintf("two variants have the keys %s", key)}
		}
		seen[key] = true
		fallback = fallback || all
	}
	if !fallback {
		return &SyntaxError{Msg: "a matcher needs a catch-all variant whose keys are all * (other)"}
	}
	return nil
}

func (e *Expr) checkFunc() error {
	if e.Func == "" {
		return nil
	}
	opts, ok := knownOptions[e.Func]
	if !ok {
		return &FunctionError{Msg: fmt.Sprintf("unknown function :%s (Sky supports :string, :number and :integer)", e.Func)}
	}
	seen := map[string]bool{}
	for _, o := range e.Opts {
		if !opts[o.Name] {
			var names []string
			for n := range opts {
				names = append(names, n)
			}
			sort.Strings(names)
			hint := "it takes no options"
			if len(names) > 0 {
				hint = "it takes " + strings.Join(names, ", ")
			}
			return &FunctionError{Msg: fmt.Sprintf("unknown option %s of :%s (%s)", o.Name, e.Func, hint)}
		}
		if seen[o.Name] {
			return &FunctionError{Msg: fmt.Sprintf("option %s is given twice", o.Name)}
		}
		seen[o.Name] = true
		if o.IsVar {
			continue
		}
		switch o.Name {
		case "minimumFractionDigits", "maximumFractionDigits", "minimumIntegerDigits":
			n, err := strconv.Atoi(o.Value)
			if err != nil || n < 0 || n > 20 {
				return &FunctionError{Msg: fmt.Sprintf("option %s must be a whole number from 0 to 20, not %q", o.Name, o.Value)}
			}
		case "useGrouping":
			if o.Value != "auto" && o.Value != "always" && o.Value != "never" && o.Value != "min2" {
				return &FunctionError{Msg: fmt.Sprintf("useGrouping must be auto, always, never or min2, not %q", o.Value)}
			}
		case "select":
			if o.Value != "plural" && o.Value != "ordinal" && o.Value != "exact" {
				return &FunctionError{Msg: fmt.Sprintf("select must be plural, ordinal or exact, not %q", o.Value)}
			}
		}
	}
	return nil
}

// Variables gives the variables a message reads from its arguments: every
// variable it names, less the ones a .local defines.
func (m *Message) Variables() []string {
	set := map[string]bool{}
	locals := map[string]bool{}
	addExpr := func(e *Expr) {
		if e.Var != "" && !locals[e.Var] {
			set[e.Var] = true
		}
		for _, o := range e.Opts {
			if o.IsVar && !locals[o.Value] {
				set[o.Value] = true
			}
		}
	}
	for i := range m.Decls {
		d := &m.Decls[i]
		addExpr(&d.Expr)
		if !d.Input {
			locals[d.Name] = true
		} else {
			set[d.Name] = true
		}
	}
	addPat := func(pat Pattern) {
		for _, part := range pat {
			if part.Expr != nil {
				addExpr(part.Expr)
			}
		}
	}
	addPat(m.Pattern)
	for _, s := range m.Selectors {
		if !locals[s] {
			set[s] = true
		}
	}
	for _, v := range m.Variants {
		addPat(v.Pattern)
	}
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// PluralSelector describes a matcher with one selector that selects by
// plural category: its variable, whether it is ordinal, and its keys.
type PluralSelector struct {
	Var     string
	Ordinal bool
	Keys    []string // the category and exact-number keys, not *
}

// PluralSelectors gives the matcher's plural selectors (select=exact is not
// one).
func (m *Message) PluralSelectors() []PluralSelector {
	var out []PluralSelector
	for si, s := range m.Selectors {
		var e *Expr
		for i := range m.Decls {
			if m.Decls[i].Name == s {
				e = &m.Decls[i].Expr
			}
		}
		if e == nil || (e.Func != "number" && e.Func != "integer") {
			continue
		}
		sel := "plural"
		for _, o := range e.Opts {
			if o.Name == "select" && !o.IsVar {
				sel = o.Value
			}
		}
		if sel == "exact" {
			continue
		}
		ps := PluralSelector{Var: s, Ordinal: sel == "ordinal"}
		seen := map[string]bool{}
		for _, v := range m.Variants {
			if k := v.Keys[si]; !k.Star && !seen[k.Value] {
				seen[k.Value] = true
				ps.Keys = append(ps.Keys, k.Value)
			}
		}
		out = append(out, ps)
	}
	return out
}

// String writes the message back in the syntax ParseMessage reads.
func (m *Message) String() string {
	var b strings.Builder
	if len(m.Decls) == 0 && len(m.Selectors) == 0 {
		writePattern(&b, m.Pattern, false)
		return b.String()
	}
	for _, d := range m.Decls {
		if d.Input {
			b.WriteString(".input ")
		} else {
			b.WriteString(".local $" + d.Name + " = ")
		}
		writeExpr(&b, &d.Expr)
		b.WriteString("\n")
	}
	if len(m.Selectors) == 0 {
		b.WriteString("{{")
		writePattern(&b, m.Pattern, true)
		b.WriteString("}}")
		return b.String()
	}
	b.WriteString(".match")
	for _, s := range m.Selectors {
		b.WriteString(" $" + s)
	}
	for _, v := range m.Variants {
		b.WriteString("\n")
		for i, k := range v.Keys {
			if i > 0 {
				b.WriteString(" ")
			}
			if k.Star {
				b.WriteString("*")
			} else {
				writeLiteral(&b, k.Value, false)
			}
		}
		b.WriteString(" {{")
		writePattern(&b, v.Pattern, true)
		b.WriteString("}}")
	}
	return b.String()
}

func writePattern(b *strings.Builder, pat Pattern, quoted bool) {
	for _, part := range pat {
		if part.Expr != nil {
			writeExpr(b, part.Expr)
			continue
		}
		for _, r := range part.Text {
			if r == '\\' || r == '{' || r == '}' || (quoted && r == '|') {
				b.WriteRune('\\')
			}
			b.WriteRune(r)
		}
	}
}

func writeExpr(b *strings.Builder, e *Expr) {
	b.WriteString("{")
	switch {
	case e.Var != "":
		b.WriteString("$" + e.Var)
	case e.Lit != nil:
		writeLiteral(b, *e.Lit, true)
	}
	if e.Func != "" {
		if e.Var != "" || e.Lit != nil {
			b.WriteString(" ")
		}
		b.WriteString(":" + e.Func)
		for _, o := range e.Opts {
			b.WriteString(" " + o.Name + "=")
			if o.IsVar {
				b.WriteString("$" + o.Value)
			} else {
				writeLiteral(b, o.Value, false)
			}
		}
	}
	b.WriteString("}")
}

// writeLiteral writes a literal unquoted when it can. An operand name is
// always quoted: unquoted it would read as a hole.
func writeLiteral(b *strings.Builder, s string, operand bool) {
	plain := s != ""
	for i, r := range s {
		if !(isNameChar(r) || r == '+' || (r == '-' && i == 0)) {
			plain = false
		}
	}
	if plain && operand && isNameStart([]rune(s)[0]) {
		plain = false
	}
	if plain {
		b.WriteString(s)
		return
	}
	b.WriteString("|")
	for _, r := range s {
		if r == '|' || r == '\\' {
			b.WriteRune('\\')
		}
		b.WriteRune(r)
	}
	b.WriteString("|")
}
