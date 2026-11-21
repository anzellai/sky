// Package i18n is the engine of Std.I18n and Std.I18n.Format: BCP 47 locales,
// CLDR plural rules, number, date, list and relative-time formatting from
// CLDR data, the message syntax (a subset of Unicode MessageFormat 2.0), the
// message catalogue and its build-time check.
//
// The package has no dependency on package rt, so the build-time catalogue
// checker (skyi18ncheck) compiles only this package. The runtime imports it
// only under the build tag sky_i18n, which the compiler sets when a program
// uses the module or declares [i18n] in sky.toml.
//
// CLDR data: root and en are always compiled. Each other locale is in a file
// with the build tag sky_cldr_<tag>, which the compiler sets for the locales
// of [i18n] locales and their CLDR parents. The tables come from
// tools/cldr-gen (CLDR version: CLDRVersion).
package i18n

import (
	"fmt"
	"strings"
	"sync"
)

// cldrRaw holds the packed data of each compiled locale (registerCLDR).
var (
	cldrMu     sync.Mutex
	cldrRaw    = map[string]string{}
	cldrParsed = map[string]map[string]string{}
)

// registerCLDR is called by the init of each generated cldr_<tag>_data.go.
func registerCLDR(tag, packed string) {
	cldrMu.Lock()
	cldrRaw[tag] = packed
	cldrMu.Unlock()
}

// cldrTable gives the parsed own data of a compiled locale.
func cldrTable(tag string) (map[string]string, bool) {
	cldrMu.Lock()
	defer cldrMu.Unlock()
	if m, ok := cldrParsed[tag]; ok {
		return m, true
	}
	raw, ok := cldrRaw[tag]
	if !ok {
		return nil, false
	}
	m := map[string]string{}
	for _, rec := range strings.Split(raw, "\x1e") {
		if k, v, ok := strings.Cut(rec, "\x1f"); ok {
			m[k] = v
		}
	}
	cldrParsed[tag] = m
	return m, true
}

// Compiled reports whether the CLDR data of a data locale and of all its CLDR
// parents is in this binary.
func Compiled(dataLocale string) bool {
	for t := dataLocale; t != ""; t = cldrParents[t] {
		if _, ok := cldrTable(t); !ok {
			return false
		}
	}
	return true
}

// data is the resolved CLDR data of one data locale: a chain of tables.
type data struct {
	tag    string
	tables []map[string]string
}

// get gives the value of a key, inherited through the CLDR parents.
func (d *data) get(key string) string {
	for _, t := range d.tables {
		if v, ok := t[key]; ok {
			return v
		}
	}
	return ""
}

// NotCompiledError says that a locale's CLDR data is not in the binary.
type NotCompiledError struct {
	Locale     string
	DataLocale string
}

func (e *NotCompiledError) Error() string {
	return fmt.Sprintf("the CLDR data of locale %s (CLDR locale %s) is not compiled into this program. "+
		"Add %q to locales in the [i18n] section of sky.toml", e.Locale, e.DataLocale, e.Locale)
}

// UnknownLocaleError says that CLDR has no data for a locale.
type UnknownLocaleError struct{ Locale string }

func (e *UnknownLocaleError) Error() string {
	return fmt.Sprintf("CLDR %s has no locale data for %s", CLDRVersion, e.Locale)
}

// dataFor gives the resolved data of a locale, or an error when CLDR has no
// data for it or when the data is not compiled in.
func dataFor(l Locale) (*data, error) {
	dl := DataLocale(l)
	if dl == "" {
		return nil, &UnknownLocaleError{Locale: l.Tag}
	}
	d := &data{tag: dl}
	for t := dl; t != ""; t = cldrParents[t] {
		tab, ok := cldrTable(t)
		if !ok {
			return nil, &NotCompiledError{Locale: l.Tag, DataLocale: dl}
		}
		d.tables = append(d.tables, tab)
	}
	return d, nil
}

// DataLocale gives the CLDR locale whose data serves a locale ("" when CLDR
// has none). The pseudo-locale en-XA uses the data of en.
func DataLocale(l Locale) string {
	if l.Tag == PseudoLocale {
		return "en"
	}
	s := l.Script
	if s == "" {
		s = LikelyScript(l.Language, l.Region)
	}
	likely := LikelyScript(l.Language, "")
	var cands []string
	cands = append(cands, l.Tag)
	if l.Region != "" {
		cands = append(cands, join(l.Language, s, l.Region))
		if s == likely {
			cands = append(cands, join(l.Language, "", l.Region))
		}
	}
	cands = append(cands, join(l.Language, s, ""))
	if s == likely || s == "" {
		cands = append(cands, l.Language)
	}
	for _, c := range cands {
		if _, ok := cldrParents[c]; ok {
			return c
		}
		if p, ok := cldrDefaultContent[c]; ok {
			return p
		}
	}
	return ""
}

// BuildTags gives the Go build tags that compile the CLDR data of a locale:
// sky_cldr_<tag> for its data locale and each CLDR parent, less root and en,
// which are always compiled.
func BuildTags(l Locale) []string {
	var tags []string
	for t := DataLocale(l); t != ""; t = cldrParents[t] {
		if t == "und" || t == "en" {
			continue
		}
		tags = append(tags, "sky_cldr_"+strings.ReplaceAll(t, "-", "_"))
	}
	return tags
}

func join(lang, script, region string) string {
	s := lang
	if script != "" {
		s += "-" + script
	}
	if region != "" {
		s += "-" + region
	}
	return s
}

// dayPeriodRule is one flexible day period of a language (dayPeriods.json).
type dayPeriodRule struct {
	name         string
	from, before int // minutes after midnight
}
