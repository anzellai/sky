//go:build sky_cldr_all

package i18n

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// project writes catalogue files (path → content) under a temp folder.
func project(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for p, c := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func runCheck(t *testing.T, files map[string]string, locales []string, def string) CheckResult {
	t.Helper()
	return Check(CheckConfig{Root: project(t, files), Catalogues: "i18n", Locales: locales, Default: def})
}

func codes(r CheckResult) string {
	var s []string
	for _, d := range r.Diagnostics {
		s = append(s, d.Code)
	}
	return strings.Join(s, " ")
}

func TestCheckAcceptsAGoodCatalogue(t *testing.T) {
	r := runCheck(t, map[string]string{
		"i18n/en-GB/shop.json": `{"shop": {"title": "Shop", "basket": {"one": "{count} item", "other": "{count} items", "=0": "Empty"}},
		  "greet": "Hello {name}"}`,
		"i18n/zh-Hans/shop.json": `{"shop.title": "商店", "shop.basket": {"other": "{count} 件商品"}}`,
		"i18n/ar/shop.json":      `{"shop.basket": {"zero": "لا", "one": "واحد", "two": "اثنان", "few": "{count} قليل", "many": "{count} كثير", "other": "{count}"}}`,
	}, []string{"en-GB", "zh-Hans", "ar", "en-XA"}, "en-GB")
	if len(r.Diagnostics) > 0 {
		t.Fatalf("diagnostics: %+v", r.Diagnostics)
	}
	want := "sky_cldr_ar sky_cldr_en_001 sky_cldr_en_GB sky_cldr_zh sky_cldr_zh_Hans sky_i18n"
	if got := strings.Join(r.Tags, " "); got != want {
		t.Errorf("tags: %s", got)
	}
	var cj CatalogueJSON
	if err := json.Unmarshal([]byte(r.Catalogue), &cj); err != nil {
		t.Fatal(err)
	}
	if len(cj.Messages["en-XA"]) != 3 || !strings.HasPrefix(cj.Messages["en-XA"]["greet"], "[Ĥéļļö") {
		t.Errorf("pseudo: %v", cj.Messages["en-XA"])
	}
	cov := map[string]string{}
	for _, c := range r.Coverage {
		cov[c.Locale] = strings.Repeat("x", c.Keys) + "/" + strings.Repeat("x", c.Of)
	}
	if cov["zh-Hans"] != "xx/xxx" || cov["ar"] != "x/xxx" {
		t.Errorf("coverage: %v", r.Coverage)
	}
	c, err := NewCatalogue(cj)
	if err != nil {
		t.Fatal(err)
	}
	en := MustLocale("en-GB")
	if got := c.Render(nil, en, "shop.basket", Args{"count": {ArgInt, "0"}}); got != "Empty" {
		t.Errorf("=0: %q", got)
	}
	if got := c.Render(nil, en, "shop.basket", Args{"count": {ArgInt, "1"}}); got != "1 item" {
		t.Errorf("one: %q", got)
	}
	// A Chinese reader asking for zh-CN gets the zh-Hans catalogue.
	if got := c.Render(nil, MustLocale("zh-CN"), "shop.title", nil); got != "商店" {
		t.Errorf("zh-CN: %q", got)
	}
	// A key the zh-Hans folder lacks falls back to the default locale.
	if got := c.Render(nil, MustLocale("zh-Hans"), "greet", Args{"name": {ArgString, "A"}}); got != "Hello A" {
		t.Errorf("fallback: %q", got)
	}
}

func TestCheckDiagnostics(t *testing.T) {
	cases := []struct {
		name    string
		files   map[string]string
		locales []string
		def     string
		want    string // the codes, in order
		text    string // a part of the first message
	}{
		{"empty locales", nil, nil, "en", "E5001", "locales is empty"},
		{"default not listed", map[string]string{"i18n/en/a.json": `{}`}, []string{"en"}, "fr", "E5001", "not in locales"},
		{"non-canonical", nil, []string{"en-gb"}, "en-gb", "E5001", "canonical"},
		{"bad tag", nil, []string{"en--x"}, "en", "E5002", "not a BCP 47"},
		{"no CLDR data", nil, []string{"tlh"}, "tlh", "E5002", "no locale data"},
		{"no folder", nil, []string{"en"}, "en", "E5003", "cannot be read"},
		{"bad JSON", map[string]string{"i18n/en/a.json": `{"a": "x",}`}, []string{"en"}, "en", "E5003", "not valid JSON"},
		{"not an object", map[string]string{"i18n/en/a.json": `["a"]`}, []string{"en"}, "en", "E5003", "JSON object"},
		{"duplicate key", map[string]string{"i18n/en/a.json": `{"a": "x", "a": "y"}`}, []string{"en"}, "en", "E5003", "defined twice"},
		{"duplicate across files", map[string]string{"i18n/en/a.json": `{"a.b": "x"}`, "i18n/en/b.json": `{"a": {"b": "y"}}`}, []string{"en"}, "en", "E5003", "defined twice"},
		{"array value", map[string]string{"i18n/en/a.json": `{"a": ["x"]}`}, []string{"en"}, "en", "E5003", "not an array"},
		{"folder not listed", map[string]string{"i18n/en/a.json": `{}`, "i18n/fr/a.json": `{}`}, []string{"en"}, "en", "E5003", "not in [i18n] locales"},
		{"syntax", map[string]string{"i18n/en/a.json": `{"a": "Hello {name"}`}, []string{"en"}, "en", "E5004", "message \"a\""},
		{"function", map[string]string{"i18n/en/a.json": `{"a": "{$d :datetime}"}`}, []string{"en"}, "en", "E5005", "unknown function :datetime"},
		{"not in default", map[string]string{"i18n/en/a.json": `{"a": "x"}`, "i18n/fr/a.json": `{"a": "x", "b": "y"}`}, []string{"en", "fr"}, "en", "E5006", "not in the default locale"},
		{"placeholder", map[string]string{"i18n/en/a.json": `{"a": "Hi {name}"}`, "i18n/fr/a.json": `{"a": "Salut {nom}"}`}, []string{"en", "fr"}, "en", "E5007", "{nom}"},
		{"missing category", map[string]string{"i18n/en/a.json": `{"a": {"one": "1", "other": "n"}}`, "i18n/ar/a.json": `{"a": {"one": "1", "other": "n"}}`}, []string{"en", "ar"}, "en", "E5008", "zero, two, few, many"},
		{"unused category", map[string]string{"i18n/en/a.json": `{"a": {"one": "1", "few": "f", "other": "n"}}`}, []string{"en"}, "en", "E5008", "does not use the category \"few\""},
		{"no other", map[string]string{"i18n/en/a.json": `{"a": {"one": "1", "=0": "none"}}`}, []string{"en"}, "en", "E5008", "needs the key \"other\""},
		{"pseudo folder", map[string]string{"i18n/en/a.json": `{}`, "i18n/en-XA/a.json": `{}`}, []string{"en", "en-XA"}, "en", "E5003", "made from the default"},
		{"mf2 match categories", map[string]string{"i18n/en/a.json": `{"a": ".input {$n :number}\n.match $n\n* {{x}}"}`}, []string{"en"}, "en", "E5008", "no variant for one"},
	}
	for _, c := range cases {
		r := runCheck(t, c.files, c.locales, c.def)
		if got := codes(r); got != c.want {
			t.Errorf("%s: codes %q, want %q (%+v)", c.name, got, c.want, r.Diagnostics)
			continue
		}
		if !strings.Contains(r.Diagnostics[0].Message, c.text) {
			t.Errorf("%s: message %q lacks %q", c.name, r.Diagnostics[0].Message, c.text)
		}
		if r.Catalogue != "" {
			t.Errorf("%s: a failing check must not give a catalogue", c.name)
		}
	}
}

func TestCheckReportsTheLine(t *testing.T) {
	r := runCheck(t, map[string]string{"i18n/en/a.json": "{\n  \"a\": \"ok\",\n  \"b\": \"bad {\"\n}"}, []string{"en"}, "en")
	if len(r.Diagnostics) != 1 || r.Diagnostics[0].File != "i18n/en/a.json:3" {
		t.Errorf("%+v", r.Diagnostics)
	}
}
