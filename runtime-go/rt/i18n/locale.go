package i18n

import (
	"fmt"
	"strings"

	"golang.org/x/text/language"
)

// PseudoLocale is the pseudo-locale with accented, expanded text, for layout
// tests. Its catalogue is made from the default locale at build time.
const PseudoLocale = "en-XA"

// Locale is a parsed BCP 47 language tag in canonical form. Extensions
// (-u-, -t-, -x-) are not kept.
type Locale struct {
	Tag      string // canonical: "zh-Hant-HK"
	Language string // "zh"
	Script   string // as written, "" when absent
	Region   string // "" when absent
	Variants []string
}

// InvalidLocaleError says that a string is not a well-formed BCP 47 tag.
type InvalidLocaleError struct {
	Input  string
	Reason string
}

func (e *InvalidLocaleError) Error() string {
	return fmt.Sprintf("%q is not a BCP 47 language tag: %s", e.Input, e.Reason)
}

// ParseLocale parses and canonicalises a BCP 47 tag: "zh-hant-hk" gives
// "zh-Hant-HK", "iw" gives "he". It refuses an empty or malformed tag, the
// root tag "und" and a private-use tag.
func ParseLocale(s string) (Locale, error) {
	in := strings.TrimSpace(strings.ReplaceAll(s, "_", "-"))
	if in == "" {
		return Locale{}, &InvalidLocaleError{Input: s, Reason: "the tag is empty"}
	}
	t, err := language.Parse(in)
	if err != nil {
		return Locale{}, &InvalidLocaleError{Input: s, Reason: err.Error()}
	}
	base, script, region := t.Raw()
	l := Locale{Language: base.String()}
	if l.Language == "und" {
		return Locale{}, &InvalidLocaleError{Input: s, Reason: "the tag has no language"}
	}
	if script.String() != "Zzzz" {
		l.Script = script.String()
	}
	if region.String() != "ZZ" {
		l.Region = region.String()
	}
	for _, v := range t.Variants() {
		l.Variants = append(l.Variants, v.String())
	}
	l.Tag = join(l.Language, l.Script, l.Region)
	for _, v := range l.Variants {
		l.Tag += "-" + v
	}
	return l, nil
}

// MustLocale parses a tag that is known to be valid (tests, constants).
func MustLocale(s string) Locale {
	l, err := ParseLocale(s)
	if err != nil {
		panic(err)
	}
	return l
}

// LikelyScript gives the likely script of a language, or of a language in a
// region when it differs (zh-HK is Hant, sr-ME is Latn). "" when unknown.
func LikelyScript(lang, region string) string {
	if region != "" {
		if s, ok := cldrLikelyScript[lang+"-"+region]; ok {
			return s
		}
	}
	return cldrLikelyScript[lang]
}

// ScriptOf gives the script of a locale: as written, else the likely script.
func ScriptOf(l Locale) string {
	if l.Script != "" {
		return l.Script
	}
	return LikelyScript(l.Language, l.Region)
}

// RightToLeft reports whether a locale's script is written right to left.
func RightToLeft(l Locale) bool {
	if l.Tag == PseudoLocale {
		return false
	}
	return cldrRtlScripts[ScriptOf(l)]
}

// Maximised gives language-script-region with the likely subtags filled in
// as far as the tables know them: "zh" → "zh-Hans", "zh-HK" → "zh-Hant-HK".
// Two locales that maximise to the same value are the same for message
// lookup: a catalogue folder "zh-Hans" serves a reader whose tag is "zh".
func Maximised(l Locale) string {
	s := ScriptOf(l)
	tag := join(l.Language, s, l.Region)
	if l.Region == "" {
		// Without a region the likely region of the language (or script) is
		// not needed for equality: "zh" and "zh-Hans" both give "zh-Hans".
		tag = join(l.Language, s, "")
	}
	for _, v := range l.Variants {
		tag += "-" + v
	}
	return tag
}

// parentTag is the CLDR parent of a tag: the explicit parent of a locale
// with data, else truncation, with the rule that a language-script tag whose
// script is not the likely one has root as parent. "" after root.
func parentTag(tag string) string {
	if p, ok := cldrParents[tag]; ok {
		if p == "und" {
			return ""
		}
		return p
	}
	parts := strings.Split(tag, "-")
	if len(parts) == 1 {
		return ""
	}
	if len(parts) == 2 && len(parts[1]) == 4 && parts[1] != cldrLikelyScript[parts[0]] {
		return ""
	}
	return strings.Join(parts[:len(parts)-1], "-")
}

// Chain gives the lookup chain of a locale without the app default: the tag,
// its form with the likely script when that differs from the language's,
// then its CLDR parents. zh-CN gives zh-CN, zh. zh-HK gives zh-HK,
// zh-Hant-HK, zh-Hant. zh-Hant-HK gives zh-Hant-HK, zh-Hant (CLDR does not
// fall back from Traditional to Simplified Chinese).
func Chain(l Locale) []Locale {
	var out []Locale
	seen := map[string]bool{}
	add := func(tag string) bool {
		if seen[tag] {
			return false
		}
		seen[tag] = true
		pl, err := ParseLocale(tag)
		if err != nil {
			return false
		}
		out = append(out, pl)
		return true
	}
	add(l.Tag)
	start := l.Tag
	// A region whose likely script is not the language's (zh-HK is Hant, zh
	// is Hans) goes through the language-script-region form, so zh-HK reaches
	// zh-Hant and not zh.
	if _, known := cldrParents[l.Tag]; !known && l.Script == "" && l.Region != "" && len(l.Variants) == 0 {
		if s := LikelyScript(l.Language, l.Region); s != "" && s != LikelyScript(l.Language, "") {
			start = join(l.Language, s, l.Region)
			add(start)
		}
	}
	for t := parentTag(start); t != ""; t = parentTag(t) {
		add(t)
	}
	return out
}

// Fallbacks gives the chain of a locale, then the default locale.
func Fallbacks(l Locale, def Locale) []Locale {
	out := Chain(l)
	for _, c := range out {
		if c.Tag == def.Tag {
			return out
		}
	}
	return append(out, def)
}

// Negotiate picks the supported locale that best fits an Accept-Language
// header. With no fit it gives the first supported locale. With no
// supported locale it gives def.
func Negotiate(supported []Locale, header string, def Locale) Locale {
	if len(supported) == 0 {
		return def
	}
	tags := make([]language.Tag, 0, len(supported))
	for _, s := range supported {
		t, err := language.Parse(s.Tag)
		if err != nil {
			t = language.Und
		}
		tags = append(tags, t)
	}
	want, _, err := language.ParseAcceptLanguage(header)
	if err != nil || len(want) == 0 {
		return supported[0]
	}
	m := language.NewMatcher(tags)
	_, idx, conf := m.Match(want...)
	if conf == language.No || idx < 0 || idx >= len(supported) {
		return supported[0]
	}
	return supported[idx]
}
