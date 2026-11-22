package i18n

import "testing"

type pluralSample struct {
	kind, locale, category, number string
}

// TestPluralRulesMatchCLDRSamples checks every compiled rule against the
// sample numbers CLDR lists for it (both ends of each sample range).
func TestPluralRulesMatchCLDRSamples(t *testing.T) {
	if len(pluralSamples) < 1000 {
		t.Fatalf("only %d samples: the generated table looks truncated", len(pluralSamples))
	}
	n := 0
	for _, s := range pluralSamples {
		table := pluralCardinalRules
		if s.kind == "Ordinal" {
			table = pluralOrdinalRules
		}
		fn, ok := table[s.locale]
		if !ok {
			t.Fatalf("no %s rule for %s", s.kind, s.locale)
		}
		o, err := operandsOf(s.number)
		if err != nil {
			t.Fatalf("%s %s %s: %v", s.kind, s.locale, s.number, err)
		}
		if got := fn(&o).String(); got != s.category {
			t.Errorf("%s %s %s: got %s, CLDR says %s", s.kind, s.locale, s.number, got, s.category)
		}
		n++
	}
	t.Logf("ASSERTIONS: %d", n)
}

func TestPluralSpotChecks(t *testing.T) {
	cases := []struct {
		loc, num string
		ordinal  bool
		want     PluralCategory
	}{
		{"en", "1", false, One}, {"en", "0", false, Other}, {"en", "1.0", false, Other}, {"en", "2", false, Other},
		{"en-GB", "1", false, One},
		{"en", "1", true, One}, {"en", "2", true, Two}, {"en", "3", true, Few}, {"en", "11", true, Other},
		{"en", "22", true, Two}, {"en", "113", true, Other},
		{"fr", "0", false, One}, {"fr", "1", false, One}, {"fr", "1.5", false, One}, {"fr", "2", false, Other},
		{"fr", "1000000", false, Many}, {"fr", "1", true, One}, {"fr", "2", true, Other},
		{"ar", "0", false, Zero}, {"ar", "1", false, One}, {"ar", "2", false, Two}, {"ar", "3", false, Few},
		{"ar", "11", false, Many}, {"ar", "100", false, Other}, {"ar", "103", false, Few},
		{"ru", "1", false, One}, {"ru", "2", false, Few}, {"ru", "5", false, Many}, {"ru", "21", false, One},
		{"ru", "11", false, Many}, {"ru", "1.5", false, Other},
		{"pl", "1", false, One}, {"pl", "2", false, Few}, {"pl", "5", false, Many}, {"pl", "22", false, Few},
		{"pl", "12", false, Many}, {"pl", "1.5", false, Other},
		{"zh", "1", false, Other}, {"zh-Hans", "2", false, Other}, {"zh-Hant-HK", "1", false, Other},
		{"ja", "1", false, Other},
		{"cy", "0", false, Zero}, {"cy", "1", false, One}, {"cy", "2", false, Two}, {"cy", "3", false, Few},
		{"cy", "6", false, Many}, {"cy", "4", false, Other},
		{"cy", "0", true, Zero}, {"cy", "1", true, One}, {"cy", "2", true, Two}, {"cy", "3", true, Few},
		{"cy", "5", true, Many}, {"cy", "10", true, Other},
		{"pt-PT", "0", false, Other}, {"pt", "0", false, One},
	}
	for _, c := range cases {
		var got PluralCategory
		var err error
		if c.ordinal {
			got, err = Ordinal(MustLocale(c.loc), c.num)
		} else {
			got, err = Cardinal(MustLocale(c.loc), c.num)
		}
		if err != nil || got != c.want {
			t.Errorf("%s %s ordinal=%v: got %v (%v), want %v", c.loc, c.num, c.ordinal, got, err, c.want)
		}
	}
}

func TestCategories(t *testing.T) {
	want := map[string]string{"ar": "zero one two few many other", "zh-Hans": "other", "en": "one other",
		"fr": "one many other", "ru": "one few many other", "ja": "other"}
	for loc, w := range want {
		var s string
		for i, c := range Categories(MustLocale(loc), false) {
			if i > 0 {
				s += " "
			}
			s += c.String()
		}
		if s != w {
			t.Errorf("%s: got %q, want %q", loc, s, w)
		}
	}
}
