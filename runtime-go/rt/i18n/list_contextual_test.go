//go:build sky_cldr_all

package i18n

import "testing"

// TestListContextualRulesMatchICU runs the cases of ICU's
// ListFormatterTest::TestContextual (icu4c/source/test/intltest/
// listformattertest.cpp), at the wide width, for the same locales.
func TestListContextualRulesMatchICU(t *testing.T) {
	es := []string{"es", "es-419", "es-PY", "es-DO"}
	he := []string{"he", "he-IL", "iw", "iw-IL"}
	cases := []struct {
		locales  []string
		style    ListStyle
		expected string
		items    []string
	}{
		{es, ListAnd, "fascinante e increíblemente", []string{"fascinante", "increíblemente"}},
		{es, ListAnd, "Comunicaciones Industriales e IIoT", []string{"Comunicaciones Industriales", "IIoT"}},
		{es, ListAnd, "España e Italia", []string{"España", "Italia"}},
		{es, ListAnd, "hijas intrépidas e hijos solidarios", []string{"hijas intrépidas", "hijos solidarios"}},
		{es, ListAnd, "a un hombre e hirieron a otro", []string{"a un hombre", "hirieron a otro"}},
		{es, ListAnd, "hija e hijo", []string{"hija", "hijo"}},
		{es, ListAnd, "esposa, hija e hijo", []string{"esposa", "hija", "hijo"}},
		// The "y" exceptions.
		{es, ListAnd, "oro y hierro", []string{"oro", "hierro"}},
		{es, ListAnd, "agua y hielo", []string{"agua", "hielo"}},
		{es, ListAnd, "colágeno y hialurónico", []string{"colágeno", "hialurónico"}},

		{es, ListOr, "desierto u oasis", []string{"desierto", "oasis"}},
		{es, ListOr, "oasis, desierto u océano", []string{"oasis", "desierto", "océano"}},
		{es, ListOr, "7 u 8", []string{"7", "8"}},
		{es, ListOr, "7 u 80", []string{"7", "80"}},
		{es, ListOr, "7 u 800", []string{"7", "800"}},
		{es, ListOr, "6, 7 u 8", []string{"6", "7", "8"}},
		{es, ListOr, "10 u 11", []string{"10", "11"}},
		{es, ListOr, "10 o 111", []string{"10", "111"}},
		{es, ListOr, "10 o 11.2", []string{"10", "11.2"}},
		{es, ListOr, "9, 10 u 11", []string{"9", "10", "11"}},

		{he, ListAnd, "a, b ו-c", []string{"a", "b", "c"}},
		{he, ListAnd, "a ו-b", []string{"a", "b"}},
		{he, ListAnd, "1, 2 ו-3", []string{"1", "2", "3"}},
		{he, ListAnd, "1 ו-2", []string{"1", "2"}},
		{he, ListAnd, "אהבה ומקווה", []string{"אהבה", "מקווה"}},
		{he, ListAnd, "אהבה, מקווה ואמונה", []string{"אהבה", "מקווה", "אמונה"}},
	}
	n := 0
	for _, c := range cases {
		for _, loc := range c.locales {
			got, err := FormatList(MustLocale(loc), c.style, c.items)
			if err != nil || got != c.expected {
				t.Errorf("%s %s %q: got %q (%v), want %q", loc, c.style, c.items, got, err, c.expected)
			}
			n++
		}
	}
	// Other locales keep their patterns: no rule rewrites them.
	if got, _ := FormatList(MustLocale("en"), ListAnd, []string{"a", "Italia"}); got != "a and Italia" {
		t.Errorf("en: %q", got)
	}
	if got, _ := FormatList(MustLocale("pt"), ListAnd, []string{"a", "Italia"}); got != "a e Italia" {
		t.Errorf("pt: %q", got)
	}
	t.Logf("ASSERTIONS: %d", n+2)
}
