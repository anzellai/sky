package i18n

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The diagnostic codes of the catalogue check (docs/errors/i18n-catalogues.md).
const (
	CodeConfig        = "E5001" // the [i18n] section
	CodeUnknownLocale = "E5002" // a tag that does not parse or has no CLDR data
	CodeFile          = "E5003" // a catalogue file or folder: JSON, shape, duplicate key
	CodeSyntax        = "E5004" // a message that does not parse
	CodeFunction      = "E5005" // an unknown function or option
	CodeNotInDefault  = "E5006" // a key the default locale does not have
	CodePlaceholder   = "E5007" // a placeholder the default locale's message does not have
	CodePlural        = "E5008" // plural categories that do not fit the locale
)

// CheckConfig is the input of the catalogue check.
type CheckConfig struct {
	Root       string   `json:"root"`       // the project folder; paths in diagnostics are relative to it
	Catalogues string   `json:"catalogues"` // the catalogue folder, relative to Root
	Locales    []string `json:"locales"`
	Default    string   `json:"default"`
}

// Diagnostic is one problem the check found.
type Diagnostic struct {
	Code    string `json:"code"`
	File    string `json:"file,omitempty"` // "i18n/fr/shop.json:12"
	Message string `json:"message"`
}

// Coverage is the share of the default locale's keys a locale has.
type Coverage struct {
	Locale string `json:"locale"`
	Keys   int    `json:"keys"`
	Of     int    `json:"of"`
}

// CheckResult is the output of the check.
type CheckResult struct {
	Diagnostics []Diagnostic `json:"diagnostics"`
	Coverage    []Coverage   `json:"coverage"`
	Tags        []string     `json:"tags"`
	Catalogue   string       `json:"catalogue"` // CatalogueJSON text, when there is no diagnostic
	CLDR        string       `json:"cldr"`
}

type entry struct {
	src  string // message source (a plural object is converted to .match)
	file string // file:line
	msg  *Message
}

type checker struct {
	cfg   CheckConfig
	res   CheckResult
	files map[string]map[string]*entry // locale tag → key → entry
}

func (c *checker) diag(code, file, format string, args ...any) {
	c.res.Diagnostics = append(c.res.Diagnostics, Diagnostic{Code: code, File: file, Message: fmt.Sprintf(format, args...)})
}

// Check validates the catalogues of a project. It never panics on bad input:
// every problem is a diagnostic.
func Check(cfg CheckConfig) CheckResult {
	c := &checker{cfg: cfg, files: map[string]map[string]*entry{}}
	c.res.CLDR = CLDRVersion
	c.res.Diagnostics = []Diagnostic{}
	c.res.Coverage = []Coverage{}
	c.res.Tags = []string{}
	if cfg.Catalogues == "" {
		cfg.Catalogues = "i18n"
		c.cfg.Catalogues = "i18n"
	}

	// Locales.
	if len(cfg.Locales) == 0 {
		c.diag(CodeConfig, "sky.toml", "[i18n] locales is empty: list the locales the app supports, for example locales = [\"en-GB\"]")
		return c.res
	}
	var locales []Locale
	seen := map[string]bool{}
	tagSet := map[string]bool{"sky_i18n": true}
	for _, s := range cfg.Locales {
		l, err := ParseLocale(s)
		if err != nil {
			c.diag(CodeUnknownLocale, "sky.toml", "[i18n] locales: %v", err)
			continue
		}
		if l.Tag != s {
			c.diag(CodeConfig, "sky.toml", "[i18n] locales: write %q as %q (the canonical form)", s, l.Tag)
			continue
		}
		if DataLocale(l) == "" {
			c.diag(CodeUnknownLocale, "sky.toml", "[i18n] locales: CLDR %s has no locale data for %q", CLDRVersion, s)
			continue
		}
		if seen[Maximised(l)] {
			c.diag(CodeConfig, "sky.toml", "[i18n] locales: %q is listed twice (or is the same locale as an earlier entry)", s)
			continue
		}
		seen[Maximised(l)] = true
		locales = append(locales, l)
		for _, t := range BuildTags(l) {
			tagSet[t] = true
		}
	}
	for t := range tagSet {
		c.res.Tags = append(c.res.Tags, t)
	}
	sort.Strings(c.res.Tags)
	if len(c.res.Diagnostics) > 0 {
		return c.res
	}
	def, err := ParseLocale(cfg.Default)
	switch {
	case cfg.Default == "":
		c.diag(CodeConfig, "sky.toml", "[i18n] default is missing: name the locale every chain ends in, for example default = %q", cfg.Locales[0])
	case err != nil:
		c.diag(CodeUnknownLocale, "sky.toml", "[i18n] default: %v", err)
	case def.Tag == PseudoLocale:
		c.diag(CodeConfig, "sky.toml", "[i18n] default may not be the pseudo-locale %s", PseudoLocale)
	default:
		found := false
		for _, l := range locales {
			found = found || l.Tag == def.Tag
		}
		if !found {
			c.diag(CodeConfig, "sky.toml", "[i18n] default %q is not in locales", cfg.Default)
		}
	}
	if len(c.res.Diagnostics) > 0 {
		return c.res
	}

	// Folders and files.
	// A generated Sky.Spa half names the project's folder by an absolute path.
	root := cfg.Catalogues
	if !filepath.IsAbs(root) {
		root = filepath.Join(cfg.Root, root)
	}
	dirs, err := os.ReadDir(root)
	if err != nil {
		c.diag(CodeFile, cfg.Catalogues, "the catalogue folder %s cannot be read: %v (create it with one folder per locale, or set [i18n] catalogues)", cfg.Catalogues, err)
		return c.res
	}
	byTag := map[string]Locale{}
	for _, l := range locales {
		byTag[l.Tag] = l
	}
	for _, d := range dirs {
		name := d.Name()
		rel := filepath.ToSlash(filepath.Join(cfg.Catalogues, name))
		if strings.HasPrefix(name, ".") {
			continue
		}
		if !d.IsDir() {
			if strings.HasSuffix(name, ".json") {
				c.diag(CodeFile, rel, "a catalogue file must be in a locale folder: move it to %s/<locale>/%s", cfg.Catalogues, name)
			}
			continue
		}
		l, err := ParseLocale(name)
		if err != nil {
			c.diag(CodeUnknownLocale, rel, "the folder name is not a locale: %v", err)
			continue
		}
		if l.Tag != name {
			c.diag(CodeFile, rel, "rename the folder to %q (the canonical form)", l.Tag)
			continue
		}
		if _, ok := byTag[l.Tag]; !ok {
			c.diag(CodeFile, rel, "locale %s has a catalogue folder but is not in [i18n] locales", l.Tag)
			continue
		}
		if l.Tag == PseudoLocale {
			c.diag(CodeFile, rel, "%s is made from the default locale at build time: remove the folder", PseudoLocale)
			continue
		}
		c.readLocale(l, filepath.Join(root, name), rel)
	}

	// Messages: parse each and check its plural keys against its locale.
	for _, l := range locales {
		for key, e := range c.files[l.Tag] {
			m, err := ParseMessage(e.src)
			if err != nil {
				code := CodeSyntax
				if _, ok := err.(*FunctionError); ok {
					code = CodeFunction
				}
				c.diag(code, e.file, "%s: message %q: %v", l.Tag, key, err)
				continue
			}
			e.msg = m
			c.checkPlural(l, key, e)
		}
	}

	// Keys and placeholders against the default locale.
	defEntries := c.files[def.Tag]
	for _, l := range locales {
		if l.Tag == def.Tag {
			continue
		}
		n := 0
		for key, e := range c.files[l.Tag] {
			de, ok := defEntries[key]
			if !ok {
				c.diag(CodeNotInDefault, e.file, "%s: key %q is not in the default locale %s (add it there first, or remove it)", l.Tag, key, def.Tag)
				continue
			}
			n++
			if e.msg == nil || de.msg == nil {
				continue
			}
			have := map[string]bool{}
			for _, v := range de.msg.Variables() {
				have[v] = true
			}
			for _, v := range e.msg.Variables() {
				if !have[v] {
					c.diag(CodePlaceholder, e.file, "%s: message %q uses {%s}, which the %s message does not have (%s)", l.Tag, key, v, def.Tag, strings.Join(de.msg.Variables(), ", "))
				}
			}
		}
		if l.Tag != PseudoLocale {
			c.res.Coverage = append(c.res.Coverage, Coverage{Locale: l.Tag, Keys: n, Of: len(defEntries)})
		}
	}
	if len(c.res.Diagnostics) > 0 {
		return c.res
	}

	// The catalogue the program embeds.
	cj := CatalogueJSON{Default: def.Tag, Messages: map[string]map[string]string{}}
	for _, l := range locales {
		cj.Locales = append(cj.Locales, l.Tag)
		out := map[string]string{}
		if l.Tag == PseudoLocale {
			for key, e := range defEntries {
				out[key] = Pseudo(e.msg).String()
			}
		} else {
			for key, e := range c.files[l.Tag] {
				out[key] = e.src
			}
		}
		cj.Messages[l.Tag] = out
	}
	text, err := json.Marshal(cj)
	if err != nil {
		c.diag(CodeFile, cfg.Catalogues, "the catalogue does not encode: %v", err)
		return c.res
	}
	c.res.Catalogue = string(text)
	return c.res
}

// checkPlural checks the keys of each plural selector against the CLDR
// categories of the locale. Every category but other must have a variant
// (* stands for other), and no variant may name a category the locale does
// not use.
func (c *checker) checkPlural(l Locale, key string, e *entry) {
	for _, ps := range e.msg.PluralSelectors() {
		cats := Categories(l, ps.Ordinal)
		uses := map[string]bool{}
		for _, cat := range cats {
			uses[cat.String()] = true
		}
		have := map[string]bool{}
		for _, k := range ps.Keys {
			if _, err := operandsOf(k); err == nil {
				continue // an exact number
			}
			if _, ok := CategoryFromName(k); !ok {
				c.diag(CodePlural, e.file, "%s: message %q: %q is not a plural category (zero, one, two, few, many, other) or a number", l.Tag, key, k)
				continue
			}
			if !uses[k] {
				hint := ""
				if k == "zero" {
					hint = " (for the number 0 use the key \"=0\", or 0 in .match)"
				}
				c.diag(CodePlural, e.file, "%s: message %q: %s does not use the category %q%s. It uses: %s", l.Tag, key, l.Tag, k, hint, catList(cats))
			}
			have[k] = true
		}
		var missing []string
		for _, cat := range cats {
			if cat != Other && !have[cat.String()] {
				missing = append(missing, cat.String())
			}
		}
		if len(missing) > 0 {
			c.diag(CodePlural, e.file, "%s: message %q selects on {%s} but has no variant for %s. %s uses: %s", l.Tag, key, ps.Var, strings.Join(missing, ", "), l.Tag, catList(cats))
		}
	}
}

func catList(cats []PluralCategory) string {
	var s []string
	for _, c := range cats {
		s = append(s, c.String())
	}
	return strings.Join(s, ", ")
}

func (c *checker) readLocale(l Locale, dir, rel string) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		c.diag(CodeFile, rel, "cannot read the folder: %v", err)
		return
	}
	if c.files[l.Tag] == nil {
		c.files[l.Tag] = map[string]*entry{}
	}
	for _, f := range ents {
		name := f.Name()
		frel := rel + "/" + name
		if strings.HasPrefix(name, ".") {
			continue
		}
		if f.IsDir() {
			c.diag(CodeFile, frel, "a locale folder holds .json files only, not folders")
			continue
		}
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			c.diag(CodeFile, frel, "cannot read: %v", err)
			continue
		}
		c.readFile(l, frel, data)
	}
}

// readFile reads one catalogue file: a JSON object whose values are message
// strings, plural objects or nested objects (whose keys join with ".").
func (c *checker) readFile(l Locale, rel string, data []byte) {
	// lineOf gives file:line of the next token after an offset (the decoder's
	// offset is the end of the previous token).
	lineOf := func(off int64) string {
		i := min(int(off), len(data))
		for i < len(data) && strings.IndexByte(" \t\r\n,", data[i]) >= 0 {
			i++
		}
		return fmt.Sprintf("%s:%d", rel, bytes.Count(data[:i], []byte("\n"))+1)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		c.diag(CodeFile, rel, "not valid JSON: %v", err)
		return
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		c.diag(CodeFile, lineOf(dec.InputOffset()), "a catalogue file is a JSON object of key to message")
		return
	}
	if err := c.readObject(l, dec, "", lineOf); err != nil {
		c.diag(CodeFile, lineOf(dec.InputOffset()), "not valid JSON: %v", err)
		return
	}
	if _, err := dec.Token(); err != io.EOF {
		c.diag(CodeFile, lineOf(dec.InputOffset()), "text after the end of the JSON object")
	}
}

// readObject reads the members of an object after its "{".
func (c *checker) readObject(l Locale, dec *json.Decoder, prefix string, lineOf func(int64) string) error {
	for dec.More() {
		at := lineOf(dec.InputOffset())
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		// A key twice in one object (or in two files) is reported by add.
		name := tok.(string)
		if name == "" || strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".") || strings.Contains(name, "..") {
			c.diag(CodeFile, at, "%s: %q is not a key (keys are non-empty names joined by single dots)", l.Tag, prefix+name)
		}
		full := prefix + name
		vt, err := dec.Token()
		if err != nil {
			return err
		}
		switch v := vt.(type) {
		case string:
			c.add(l, full, v, at)
		case json.Delim:
			if v != '{' {
				c.diag(CodeFile, at, "%s: key %q: a value is a message string or an object, not an array", l.Tag, full)
				if err := skip(dec, v); err != nil {
					return err
				}
				continue
			}
			obj, isPlural, err := readMaybePlural(dec)
			if err != nil {
				return err
			}
			if isPlural {
				src, problem := pluralToMF2(obj)
				if problem != "" {
					c.diag(CodePlural, at, "%s: message %q: %s", l.Tag, full, problem)
					continue
				}
				c.add(l, full, src, at)
				continue
			}
			for _, kv := range obj {
				if kv.obj != nil {
					// Re-read the nested namespace from its saved tokens.
					c.readNested(l, kv, full+".", at)
					continue
				}
				if kv.isStr {
					c.add(l, full+"."+kv.key, kv.str, at)
				} else {
					c.diag(CodeFile, at, "%s: key %q: a value is a message string or an object", l.Tag, full+"."+kv.key)
				}
			}
		default:
			c.diag(CodeFile, at, "%s: key %q: a value is a message string or an object, not %v", l.Tag, full, v)
		}
	}
	_, err := dec.Token() // }
	return err
}

func (c *checker) readNested(l Locale, kv member, prefix, at string) {
	if kv.plural {
		src, problem := pluralToMF2(kv.obj)
		if problem != "" {
			c.diag(CodePlural, at, "%s: message %q: %s", l.Tag, strings.TrimSuffix(prefix, "."), problem)
			return
		}
		c.add(l, prefix+kv.key, src, at)
		return
	}
	for _, m := range kv.obj {
		switch {
		case m.obj != nil:
			c.readNested(l, m, prefix+kv.key+".", at)
		case m.isStr:
			c.add(l, prefix+kv.key+"."+m.key, m.str, at)
		default:
			c.diag(CodeFile, at, "%s: key %q: a value is a message string or an object", l.Tag, prefix+kv.key+"."+m.key)
		}
	}
}

// member is one member of an object read ahead of time.
type member struct {
	key    string
	str    string
	isStr  bool
	obj    []member
	plural bool
}

// readMaybePlural reads a whole object after its "{". The object is a plural
// object when every key is a plural category or "=<number>".
func readMaybePlural(dec *json.Decoder) ([]member, bool, error) {
	var out []member
	seen := map[string]bool{}
	allPlural := true
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, false, err
		}
		k := tok.(string)
		if seen[k] {
			return nil, false, fmt.Errorf("key %q is in an object twice", k)
		}
		seen[k] = true
		_, isCat := CategoryFromName(k)
		isExact := false
		if strings.HasPrefix(k, "=") {
			if _, err := operandsOf(strings.TrimPrefix(k, "=")); err == nil {
				isExact = true
			}
		}
		allPlural = allPlural && (isCat || isExact)
		vt, err := dec.Token()
		if err != nil {
			return nil, false, err
		}
		m := member{key: k}
		switch v := vt.(type) {
		case string:
			m.str, m.isStr = v, true
		case json.Delim:
			if v == '{' {
				sub, pl, err := readMaybePlural(dec)
				if err != nil {
					return nil, false, err
				}
				m.obj, m.plural = sub, pl
				if m.obj == nil {
					m.obj = []member{}
				}
			} else if err := skip(dec, v); err != nil {
				return nil, false, err
			}
			allPlural = false
		default:
			allPlural = false
		}
		out = append(out, m)
	}
	if _, err := dec.Token(); err != nil {
		return nil, false, err
	}
	return out, allPlural && len(out) > 0, nil
}

func skip(dec *json.Decoder, open json.Delim) error {
	depth := 1
	for depth > 0 {
		t, err := dec.Token()
		if err != nil {
			return err
		}
		if d, ok := t.(json.Delim); ok {
			if d == '{' || d == '[' {
				depth++
			} else {
				depth--
			}
		}
	}
	_ = open
	return nil
}

// pluralToMF2 converts a plural object to a .match on $count. "other" is the
// catch-all; "=N" is the exact number N.
func pluralToMF2(obj []member) (string, string) {
	var b strings.Builder
	b.WriteString(".input {$count :number}\n.match $count")
	hasOther := false
	var rest []member
	for _, m := range obj {
		if m.key == "other" {
			hasOther = true
			continue
		}
		rest = append(rest, m)
	}
	if !hasOther {
		return "", "a plural object needs the key \"other\""
	}
	sort.SliceStable(rest, func(i, j int) bool {
		// exact numbers first, then CLDR order
		ei, ej := strings.HasPrefix(rest[i].key, "="), strings.HasPrefix(rest[j].key, "=")
		if ei != ej {
			return ei
		}
		ci, _ := CategoryFromName(rest[i].key)
		cj, _ := CategoryFromName(rest[j].key)
		return ci < cj
	})
	write := func(key, text string) {
		b.WriteString("\n" + key + " {{")
		b.WriteString(text)
		b.WriteString("}}")
	}
	for _, m := range rest {
		k := m.key
		if strings.HasPrefix(k, "=") {
			k = strings.TrimPrefix(k, "=")
		}
		write(k, m.str)
	}
	for _, m := range obj {
		if m.key == "other" {
			write("*", m.str)
		}
	}
	return b.String(), ""
}

func (c *checker) add(l Locale, key, src, at string) {
	if prev, ok := c.files[l.Tag][key]; ok {
		c.diag(CodeFile, at, "%s: key %q is defined twice (first at %s)", l.Tag, key, prev.file)
		return
	}
	c.files[l.Tag][key] = &entry{src: src, file: at}
}
