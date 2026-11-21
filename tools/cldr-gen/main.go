// Command cldr-gen writes the CLDR tables of Std.I18n into runtime-go/rt/i18n.
//
// It reads the cldr-json packages of one pinned CLDR release, checks each
// tarball against its pinned SHA-256, and writes Go source:
//
//   - cldr_index_gen.go: the list of locales and their CLDR parents, the
//     default-content aliases, the likely scripts, the right-to-left scripts,
//     the currency digits and the day-period rules. Always compiled.
//   - cldr_plural_gen.go: the CLDR cardinal and ordinal plural rules of every
//     language, compiled to Go functions. Always compiled.
//   - cldr_<tag>_data.go: the locale data of one locale, stored as the
//     difference from its CLDR parent. Compiled only under the build tag
//     sky_cldr_<tag> (root and en have no tag and are always present).
//   - plural_samples_gen_test.go: the CLDR sample numbers of every plural
//     rule, for the test that checks the compiled rules against them.
//
// Usage (from the repository root):
//
//	go run ./tools/cldr-gen            # write the files
//	go run ./tools/cldr-gen -check     # fail when the files differ
//
// The tarballs come from SKY_CLDR_SRC (a directory that holds them), else from
// the cache ~/.cache/sky/cldr/<version>, else from registry.npmjs.org. A
// tarball whose SHA-256 differs from the pin is refused.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"go/format"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Version is the pinned CLDR release (cldr-json on npm).
const Version = "49.0.0"

// pins holds the SHA-256 of each cldr-json tarball of Version.
var pins = map[string]string{
	"cldr-core":         "085b1c7d83f00692505c79d575c21eea89534466eb3e70b455688851a45c87bc",
	"cldr-numbers-full": "753da9481ac5c0d0566a23ab89c3945d7e4cb1dae6f6d5a87c0329a8640e7593",
	"cldr-dates-full":   "3e194fae3317cfceb1548a223f849407d4f97c827015c1280ddd981eca4214a6",
	"cldr-misc-full":    "d0129e972402b1d6b32ab44c82c5a058c3f721bf61bf07b2b652c9444eba8821",
}

// moneyCodes are the ISO 4217 codes of Std.Money's Currency type. The tables
// carry symbols for these codes only. BTC, ETH, USDT and USDC have no CLDR
// data and format with their code.
var moneyCodes = strings.Fields(`USD EUR GBP JPY CHF AUD CAD NZD SEK NOK DKK CNY HKD SGD KRW TWD INR THB
MYR IDR PHP VND BRL MXN ARS CLP ZAR TRY RUB UAH PLN CZK HUF RON BGN AED SAR QAR KWD BHD OMR JOD ILS
EGP NGN KES GHS MAD TND DZD PKR BDT LKR NPR`)

var relUnits = []string{"year", "month", "week", "day", "hour", "minute", "second"}
var pluralCats = []string{"zero", "one", "two", "few", "many", "other"}
var dayKeys = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

type source struct {
	files map[string][]byte // "cldr-core/package/..." → bytes
}

func main() {
	check := flag.Bool("check", false, "compare the generated files with the files on disk; write nothing")
	out := flag.String("out", "runtime-go/rt/i18n", "output directory")
	flag.Parse()
	src, err := load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "cldr-gen:", err)
		os.Exit(1)
	}
	files, err := generate(src)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cldr-gen:", err)
		os.Exit(1)
	}
	if *check {
		bad := compare(*out, files)
		if len(bad) > 0 {
			fmt.Fprintln(os.Stderr, "cldr-gen: the generated files differ from the files on disk:")
			for _, b := range bad {
				fmt.Fprintln(os.Stderr, "  "+b)
			}
			fmt.Fprintln(os.Stderr, "run `go run ./tools/cldr-gen` from the repository root and commit the result")
			os.Exit(1)
		}
		fmt.Printf("cldr-gen: %d files match CLDR %s\n", len(files), Version)
		return
	}
	if err := write(*out, files); err != nil {
		fmt.Fprintln(os.Stderr, "cldr-gen:", err)
		os.Exit(1)
	}
	fmt.Printf("cldr-gen: wrote %d files for CLDR %s into %s\n", len(files), Version, *out)
}

// compare returns the names of the files that differ, are missing, or are
// generated files on disk that the generator no longer writes.
func compare(dir string, files map[string][]byte) []string {
	var bad []string
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			bad = append(bad, name+" (missing)")
		} else if !bytes.Equal(got, want) {
			bad = append(bad, name+" (differs)")
		}
	}
	for _, name := range generatedOnDisk(dir) {
		if _, ok := files[name]; !ok {
			bad = append(bad, name+" (stale)")
		}
	}
	sort.Strings(bad)
	return bad
}

func generatedOnDisk(dir string) []string {
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, "cldr_") && strings.HasSuffix(n, ".go") || n == "plural_samples_gen_test.go" {
			names = append(names, n)
		}
	}
	return names
}

func write(dir string, files map[string][]byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, name := range generatedOnDisk(dir) {
		if _, ok := files[name]; !ok {
			if err := os.Remove(filepath.Join(dir, name)); err != nil {
				return err
			}
		}
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// ─── Loading ────────────────────────────────────────────────────────────────

func load() (*source, error) {
	src := &source{files: map[string][]byte{}}
	for name, pin := range pins {
		data, err := tarball(name)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); got != pin {
			return nil, fmt.Errorf("%s-%s.tgz has SHA-256 %s, the pin is %s", name, Version, got, pin)
		}
		if err := untar(name, data, src.files); err != nil {
			return nil, fmt.Errorf("%s: %v", name, err)
		}
	}
	return src, nil
}

func tarball(name string) ([]byte, error) {
	file := fmt.Sprintf("%s-%s.tgz", name, Version)
	var dirs []string
	if d := os.Getenv("SKY_CLDR_SRC"); d != "" {
		dirs = append(dirs, d)
	}
	cache := ""
	if home, err := os.UserHomeDir(); err == nil {
		cache = filepath.Join(home, ".cache", "sky", "cldr", Version)
		dirs = append(dirs, cache)
	}
	for _, d := range dirs {
		if data, err := os.ReadFile(filepath.Join(d, file)); err == nil {
			return data, nil
		}
	}
	url := fmt.Sprintf("https://registry.npmjs.org/%s/-/%s", name, file)
	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("download %s: %v (or put the tarball in SKY_CLDR_SRC)", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if cache != "" {
		if os.MkdirAll(cache, 0o755) == nil {
			_ = os.WriteFile(filepath.Join(cache, file), data, 0o644)
		}
	}
	return data, nil
}

func untar(name string, data []byte, into map[string][]byte) error {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg || !strings.HasSuffix(h.Name, ".json") {
			continue
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			return err
		}
		into[name+"/"+h.Name] = b
	}
}

func (s *source) json(path string) (map[string]any, error) {
	b, ok := s.files[path]
	if !ok {
		return nil, fmt.Errorf("missing %s", path)
	}
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	return v, nil
}

func (s *source) has(path string) bool { _, ok := s.files[path]; return ok }

// dig walks a JSON object by keys. A missing key gives nil.
func dig(v any, keys ...string) any {
	for _, k := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[k]
	}
	return v
}

// str gives the string at the path. A CLDR value with attributes is an object
// with "_value", which is read too.
func str(v any, keys ...string) string {
	x := dig(v, keys...)
	switch t := x.(type) {
	case string:
		return t
	case map[string]any:
		if s, ok := t["_value"].(string); ok {
			return s
		}
	}
	return ""
}

// ─── Locale set and parents ─────────────────────────────────────────────────

type world struct {
	src           *source
	parentLocale  map[string]string
	likely        map[string]string // likelySubtags
	defaultCont   map[string]bool
	locales       []string // sorted, with "und"
	set           map[string]bool
	resolved      map[string]map[string]string
	moneyDigits   map[string]int
	numberSystems map[string]string
}

func subtags(tag string) []string { return strings.Split(tag, "-") }

func isScript(s string) bool {
	return len(s) == 4 && s[0] >= 'A' && s[0] <= 'Z'
}

// likelyScript gives the likely script of a bare language.
func (w *world) likelyScript(lang string) string {
	full := w.likely[lang]
	for _, p := range subtags(full)[1:] {
		if isScript(p) {
			return p
		}
	}
	return ""
}

// parent is the CLDR parent locale of a locale ("und" for root).
func (w *world) parent(tag string) string {
	if tag == "und" {
		return ""
	}
	if p, ok := w.parentLocale[tag]; ok {
		if p == "root" {
			return "und"
		}
		return p
	}
	parts := subtags(tag)
	if len(parts) == 1 {
		return "und"
	}
	// _localeRules nonlikelyScript: a language-script locale whose script is
	// not the language's likely script has the root as its parent.
	if len(parts) == 2 && isScript(parts[1]) && parts[1] != w.likelyScript(parts[0]) {
		return "und"
	}
	p := strings.Join(parts[:len(parts)-1], "-")
	// A default-content locale (ca-ES) has no data of its own: its data is
	// its own parent's (ca), so the chain goes there.
	if w.defaultCont[p] && !w.src.has("cldr-numbers-full/package/main/"+p+"/numbers.json") {
		return w.parent(p)
	}
	return p
}

func newWorld(src *source) (*world, error) {
	w := &world{src: src, parentLocale: map[string]string{}, likely: map[string]string{},
		defaultCont: map[string]bool{}, set: map[string]bool{}, resolved: map[string]map[string]string{},
		moneyDigits: map[string]int{}, numberSystems: map[string]string{}}
	pl, err := src.json("cldr-core/package/supplemental/parentLocales.json")
	if err != nil {
		return nil, err
	}
	if rule := str(pl, "supplemental", "parentLocales", "_localeRules", "parentLocale", "nonlikelyScript"); rule != "root" {
		return nil, fmt.Errorf("parentLocales _localeRules nonlikelyScript is %q, the generator knows only \"root\"", rule)
	}
	for k, v := range dig(pl, "supplemental", "parentLocales", "parentLocale").(map[string]any) {
		w.parentLocale[k] = v.(string)
	}
	ls, err := src.json("cldr-core/package/supplemental/likelySubtags.json")
	if err != nil {
		return nil, err
	}
	for k, v := range dig(ls, "supplemental", "likelySubtags").(map[string]any) {
		w.likely[k] = v.(string)
	}
	dc, err := src.json("cldr-core/package/defaultContent.json")
	if err != nil {
		return nil, err
	}
	for _, v := range dc["defaultContent"].([]any) {
		w.defaultCont[v.(string)] = true
	}
	cov, err := src.json("cldr-core/package/coverageLevels.json")
	if err != nil {
		return nil, err
	}
	var queue []string
	for k, v := range cov["effectiveCoverageLevels"].(map[string]any) {
		if v == "modern" {
			queue = append(queue, k)
		}
	}
	queue = append(queue, "und", "en")
	for len(queue) > 0 {
		t := queue[0]
		queue = queue[1:]
		if t == "" || w.set[t] {
			continue
		}
		if !src.has("cldr-numbers-full/package/main/" + t + "/numbers.json") {
			return nil, fmt.Errorf("locale %s has no numbers.json", t)
		}
		w.set[t] = true
		queue = append(queue, w.parent(t))
	}
	for t := range w.set {
		w.locales = append(w.locales, t)
	}
	sort.Strings(w.locales)
	cd, err := src.json("cldr-core/package/supplemental/currencyData.json")
	if err != nil {
		return nil, err
	}
	fr := dig(cd, "supplemental", "currencyData", "fractions").(map[string]any)
	def, _ := strconv.Atoi(str(fr, "DEFAULT", "_digits"))
	for _, c := range moneyCodes {
		d := def
		if s := str(fr, c, "_digits"); s != "" {
			d, _ = strconv.Atoi(s)
		}
		w.moneyDigits[c] = d
	}
	ns, err := src.json("cldr-core/package/supplemental/numberingSystems.json")
	if err != nil {
		return nil, err
	}
	for k, v := range dig(ns, "supplemental", "numberingSystems").(map[string]any) {
		if d := str(v, "_digits"); d != "" && str(v, "_type") == "numeric" {
			w.numberSystems[k] = d
		}
	}
	return w, nil
}

// ─── Locale data ────────────────────────────────────────────────────────────

// extract gives the flat, resolved data of one locale.
func (w *world) extract(tag string) (map[string]string, error) {
	m := map[string]string{}
	put := func(k, v string) {
		if v != "" {
			m[k] = v
		}
	}
	main := func(pkg, file string) (any, error) {
		v, err := w.src.json(fmt.Sprintf("%s/package/main/%s/%s", pkg, tag, file))
		if err != nil {
			return nil, err
		}
		return dig(v, "main", tag), nil
	}

	// Numbers.
	nm, err := main("cldr-numbers-full", "numbers.json")
	if err != nil {
		return nil, err
	}
	n := dig(nm, "numbers")
	ns := str(n, "defaultNumberingSystem")
	digits, ok := w.numberSystems[ns]
	if !ok {
		return nil, fmt.Errorf("%s: numbering system %q has no digits", tag, ns)
	}
	put("num.ns", ns)
	put("num.digits", digits)
	symNs := ns
	if dig(n, "symbols-numberSystem-"+ns) == nil {
		symNs = "latn"
	}
	sym := dig(n, "symbols-numberSystem-"+symNs)
	put("num.dec", str(sym, "decimal"))
	put("num.grp", str(sym, "group"))
	put("num.pct", str(sym, "percentSign"))
	put("num.minus", str(sym, "minusSign"))
	put("num.plus", str(sym, "plusSign"))
	put("num.pat.dec", str(n, "decimalFormats-numberSystem-"+symNs, "standard"))
	put("num.pat.pct", str(n, "percentFormats-numberSystem-"+symNs, "standard"))
	cf := dig(n, "currencyFormats-numberSystem-"+symNs)
	put("num.pat.cur", str(cf, "standard"))
	put("num.pat.curalpha", str(cf, "standard-alphaNextToNumber"))
	put("cur.sp.before", str(cf, "currencySpacing", "beforeCurrency", "insertBetween"))
	put("cur.sp.after", str(cf, "currencySpacing", "afterCurrency", "insertBetween"))
	put("num.mingroup", str(n, "minimumGroupingDigits"))

	cm, err := main("cldr-numbers-full", "currencies.json")
	if err != nil {
		return nil, err
	}
	for _, c := range moneyCodes {
		put("cur.sym."+c, str(cm, "numbers", "currencies", c, "symbol"))
		put("cur.nar."+c, str(cm, "numbers", "currencies", c, "symbol-alt-narrow"))
	}

	// Gregorian calendar.
	gm, err := main("cldr-dates-full", "ca-gregorian.json")
	if err != nil {
		return nil, err
	}
	g := dig(gm, "dates", "calendars", "gregorian")
	for _, ctx := range [][2]string{{"format", "f"}, {"stand-alone", "s"}} {
		for _, wd := range [][2]string{{"abbreviated", "a"}, {"wide", "w"}} {
			for i := 1; i <= 12; i++ {
				put(fmt.Sprintf("cal.m.%s.%s.%d", ctx[1], wd[1], i), str(g, "months", ctx[0], wd[0], strconv.Itoa(i)))
			}
			for i, d := range dayKeys {
				put(fmt.Sprintf("cal.d.%s.%s.%d", ctx[1], wd[1], i), str(g, "days", ctx[0], wd[0], d))
			}
		}
	}
	dp := dig(g, "dayPeriods", "format", "abbreviated")
	if dpm, ok := dp.(map[string]any); ok {
		for k := range dpm {
			if strings.Contains(k, "-alt-") {
				continue
			}
			put("cal.dp."+k, str(dpm, k))
		}
	}
	put("cal.era.0", str(g, "eras", "eraAbbr", "0"))
	put("cal.era.1", str(g, "eras", "eraAbbr", "1"))
	for _, s := range []string{"full", "long", "medium", "short"} {
		put("cal.date."+s, str(g, "dateFormats", s))
		put("cal.time."+s, str(g, "timeFormats", s))
		put("cal.dt."+s, str(g, "dateTimeFormats", s))
	}

	tzm, err := main("cldr-dates-full", "timeZoneNames.json")
	if err != nil {
		return nil, err
	}
	tz := dig(tzm, "dates", "timeZoneNames")
	put("tz.gmt", str(tz, "gmtFormat"))
	put("tz.gmtzero", str(tz, "gmtZeroFormat"))
	put("tz.hour", str(tz, "hourFormat"))

	// Relative time.
	dfm, err := main("cldr-dates-full", "dateFields.json")
	if err != nil {
		return nil, err
	}
	fields := dig(dfm, "dates", "fields")
	for _, u := range relUnits {
		f := dig(fields, u)
		for _, r := range []string{"-1", "0", "1"} {
			put(fmt.Sprintf("rel.%s.%s", u, r), str(f, "relative-type-"+r))
		}
		for _, dir := range [][2]string{{"future", "f"}, {"past", "p"}} {
			for _, c := range pluralCats {
				put(fmt.Sprintf("rel.%s.%s.%s", u, dir[1], c), str(f, "relativeTime-type-"+dir[0], "relativeTimePattern-count-"+c))
			}
		}
	}

	// Lists.
	lm, err := main("cldr-misc-full", "listPatterns.json")
	if err != nil {
		return nil, err
	}
	for _, typ := range []string{"standard", "or", "unit"} {
		p := dig(lm, "listPatterns", "listPattern-type-"+typ)
		for _, part := range []string{"start", "middle", "end", "2", "3"} {
			put(fmt.Sprintf("list.%s.%s", typ, part), str(p, part))
		}
	}
	return m, nil
}

// resolvedOf is the resolved data of a locale, memoised.
func (w *world) resolvedOf(tag string) (map[string]string, error) {
	if m, ok := w.resolved[tag]; ok {
		return m, nil
	}
	m, err := w.extract(tag)
	if err != nil {
		return nil, err
	}
	w.resolved[tag] = m
	return m, nil
}

// diff gives the entries of a locale that its parent chain does not already
// give. A key the parent has and the child lacks keeps the parent's value,
// which is what CLDR inheritance means.
func (w *world) diff(tag string) (map[string]string, error) {
	own, err := w.resolvedOf(tag)
	if err != nil {
		return nil, err
	}
	p := w.parent(tag)
	if p == "" {
		return own, nil
	}
	inherited := map[string]string{}
	for q := p; q != ""; q = w.parent(q) {
		pm, err := w.resolvedOf(q)
		if err != nil {
			return nil, err
		}
		for k, v := range pm {
			if _, ok := inherited[k]; !ok {
				inherited[k] = v
			}
		}
		// The nearest ancestor holds the complete resolved data already.
		break
	}
	out := map[string]string{}
	for k, v := range own {
		if inherited[k] != v {
			out[k] = v
		}
	}
	return out, nil
}

// ─── Output ─────────────────────────────────────────────────────────────────

const header = "// Code generated by tools/cldr-gen from CLDR %s (cldr-json). DO NOT EDIT.\n\n"

func goTag(tag string) string { return strings.ReplaceAll(tag, "-", "_") }

func fileFor(tag string) string {
	if tag == "und" {
		return "cldr_root_data.go"
	}
	return "cldr_" + goTag(tag) + "_data.go"
}

// encode packs a map into one string: key \x1f value \x1e, keys sorted.
func encode(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte(0x1f)
		b.WriteString(m[k])
		b.WriteByte(0x1e)
	}
	return b.String()
}

func gofmt(name string, src []byte) ([]byte, error) {
	out, err := format.Source(src)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", name, err)
	}
	return out, nil
}

func generate(src *source) (map[string][]byte, error) {
	w, err := newWorld(src)
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	for _, tag := range w.locales {
		d, err := w.diff(tag)
		if err != nil {
			return nil, err
		}
		var b bytes.Buffer
		if tag != "und" && tag != "en" {
			// sky_cldr_all compiles every locale (the runtime's own tests).
			fmt.Fprintf(&b, "//go:build sky_cldr_%s || sky_cldr_all\n\n", goTag(tag))
		}
		fmt.Fprintf(&b, header, Version)
		b.WriteString("package i18n\n\n")
		fmt.Fprintf(&b, "func init() {\n\tregisterCLDR(%q, %s)\n}\n", tag, strconv.Quote(encode(d)))
		name := fileFor(tag)
		if files[name], err = gofmt(name, b.Bytes()); err != nil {
			return nil, err
		}
	}
	idx, err := w.index()
	if err != nil {
		return nil, err
	}
	if files["cldr_index_gen.go"], err = gofmt("cldr_index_gen.go", idx); err != nil {
		return nil, err
	}
	pr, samples, err := w.plurals()
	if err != nil {
		return nil, err
	}
	if files["cldr_plural_gen.go"], err = gofmt("cldr_plural_gen.go", pr); err != nil {
		return nil, err
	}
	if files["plural_samples_gen_test.go"], err = gofmt("plural_samples_gen_test.go", samples); err != nil {
		return nil, err
	}
	return files, nil
}

func (w *world) index() ([]byte, error) {
	var b bytes.Buffer
	fmt.Fprintf(&b, header, Version)
	b.WriteString("package i18n\n\n")
	fmt.Fprintf(&b, "// CLDRVersion is the CLDR release the tables come from.\nconst CLDRVersion = %q\n\n", Version)

	b.WriteString("// cldrParents maps each locale with generated data to its CLDR parent (\"\" for root).\n")
	b.WriteString("var cldrParents = map[string]string{\n")
	for _, t := range w.locales {
		fmt.Fprintf(&b, "\t%q: %q,\n", t, w.parent(t))
	}
	b.WriteString("}\n\n")

	b.WriteString("// cldrDefaultContent maps a default-content locale to the locale that holds its data.\n")
	b.WriteString("var cldrDefaultContent = map[string]string{\n")
	var dcs []string
	for t := range w.defaultCont {
		dcs = append(dcs, t)
	}
	sort.Strings(dcs)
	for _, t := range dcs {
		// The data of a default-content locale is its truncation parent.
		parts := subtags(t)
		p := strings.Join(parts[:len(parts)-1], "-")
		for p != "" && !w.set[p] {
			if !w.defaultCont[p] {
				p = ""
				break
			}
			pp := subtags(p)
			p = strings.Join(pp[:len(pp)-1], "-")
		}
		if p != "" && !w.set[t] {
			fmt.Fprintf(&b, "\t%q: %q,\n", t, p)
		}
	}
	b.WriteString("}\n\n")

	// Likely scripts of the languages with data, and the language-region pairs
	// whose likely script differs from the language's.
	langs := map[string]bool{}
	for _, t := range w.locales {
		if t != "und" {
			langs[subtags(t)[0]] = true
		}
	}
	langs["en"] = true
	b.WriteString("// cldrLikelyScript gives the likely script of a language, or of a language-region pair\n// whose likely script differs from the language's (likelySubtags).\n")
	b.WriteString("var cldrLikelyScript = map[string]string{\n")
	var keys []string
	for k := range w.likely {
		parts := subtags(k)
		if !langs[parts[0]] {
			continue
		}
		if len(parts) == 1 || (len(parts) == 2 && !isScript(parts[1])) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts := subtags(k)
		s := ""
		for _, p := range subtags(w.likely[k])[1:] {
			if isScript(p) {
				s = p
			}
		}
		if len(parts) == 2 && s == w.likelyScript(parts[0]) {
			continue
		}
		fmt.Fprintf(&b, "\t%q: %q,\n", k, s)
	}
	b.WriteString("}\n\n")

	sm, err := w.src.json("cldr-core/package/scriptMetadata.json")
	if err != nil {
		return nil, err
	}
	var rtl []string
	for k, v := range sm["scriptMetadata"].(map[string]any) {
		if str(v, "rtl") == "YES" {
			rtl = append(rtl, k)
		}
	}
	sort.Strings(rtl)
	b.WriteString("// cldrRtlScripts are the scripts written right to left (scriptMetadata).\nvar cldrRtlScripts = map[string]bool{\n")
	for _, s := range rtl {
		fmt.Fprintf(&b, "\t%q: true,\n", s)
	}
	b.WriteString("}\n\n")

	b.WriteString("// cldrCurrencyDigits gives the fraction digits of each Std.Money currency (currencyData).\nvar cldrCurrencyDigits = map[string]int{\n")
	for _, c := range moneyCodes {
		fmt.Fprintf(&b, "\t%q: %d,\n", c, w.moneyDigits[c])
	}
	b.WriteString("}\n\n")

	dpd, err := w.src.json("cldr-core/package/supplemental/dayPeriods.json")
	if err != nil {
		return nil, err
	}
	sets := dig(dpd, "supplemental", "dayPeriodRuleSet").(map[string]any)
	var dl []string
	for l := range sets {
		if langs[l] {
			dl = append(dl, l)
		}
	}
	sort.Strings(dl)
	b.WriteString("// cldrDayPeriods gives the flexible day periods of a language: name, from minute, before minute.\n// A period with only an \"at\" time has from == before.\nvar cldrDayPeriods = map[string][]dayPeriodRule{\n")
	for _, l := range dl {
		rules := sets[l].(map[string]any)
		var names []string
		for k := range rules {
			names = append(names, k)
		}
		sort.Strings(names)
		fmt.Fprintf(&b, "\t%q: {", l)
		for _, nm := range names {
			r := rules[nm]
			from, before := str(r, "_from"), str(r, "_before")
			if at := str(r, "_at"); at != "" {
				from, before = at, at
			}
			fm, bm := minutes(from), minutes(before)
			fmt.Fprintf(&b, "{%q, %d, %d}, ", nm, fm, bm)
		}
		b.WriteString("},\n")
	}
	b.WriteString("}\n")
	return b.Bytes(), nil
}

func minutes(hhmm string) int {
	parts := strings.Split(hhmm, ":")
	if len(parts) != 2 {
		return 0
	}
	h, _ := strconv.Atoi(parts[0])
	m, _ := strconv.Atoi(parts[1])
	return h*60 + m
}

// ─── Plural rules ───────────────────────────────────────────────────────────

func (w *world) plurals() ([]byte, []byte, error) {
	var b, t bytes.Buffer
	fmt.Fprintf(&b, header, Version)
	b.WriteString("package i18n\n\n")
	fmt.Fprintf(&t, header, Version)
	t.WriteString("package i18n\n\n")
	t.WriteString("// pluralSamples are CLDR's own sample numbers for each rule: kind, locale, category, samples.\n")
	t.WriteString("var pluralSamples = []pluralSample{\n")
	for _, kind := range []struct{ file, key, name string }{
		{"plurals.json", "plurals-type-cardinal", "Cardinal"},
		{"ordinals.json", "plurals-type-ordinal", "Ordinal"},
	} {
		d, err := w.src.json("cldr-core/package/supplemental/" + kind.file)
		if err != nil {
			return nil, nil, err
		}
		rules := dig(d, "supplemental", kind.key).(map[string]any)
		var locs []string
		for l := range rules {
			locs = append(locs, l)
		}
		sort.Strings(locs)
		// One function per distinct rule set.
		fnOf := map[string]string{}
		var fnOrder []string
		bodies := map[string]string{}
		catsOf := map[string]string{}
		for _, l := range locs {
			rs := rules[l].(map[string]any)
			var conds []string
			var cats []string
			sig := ""
			for _, c := range pluralCats {
				raw, ok := rs["pluralRule-count-"+c].(string)
				if !ok {
					continue
				}
				cats = append(cats, c)
				cond, samples := splitRule(raw)
				sig += c + ":" + cond + ";"
				if c != "other" {
					g, err := compileRule(cond)
					if err != nil {
						return nil, nil, fmt.Errorf("%s %s %s: %v", kind.name, l, c, err)
					}
					conds = append(conds, fmt.Sprintf("\tif %s {\n\t\treturn %s\n\t}\n", g, catConst(c)))
				}
				for _, s := range samples {
					fmt.Fprintf(&t, "\t{%q, %q, %q, %q},\n", kind.name, l, c, s)
				}
			}
			name, ok := fnOf[sig]
			if !ok {
				name = fmt.Sprintf("plural%s%d", kind.name, len(fnOrder))
				fnOf[sig] = name
				fnOrder = append(fnOrder, name)
				bodies[name] = strings.Join(conds, "") + "\treturn Other\n"
			}
			fnOf["loc:"+l] = name
			catsOf[l] = strings.Join(cats, " ")
		}
		for _, name := range fnOrder {
			fmt.Fprintf(&b, "func %s(o *pluralOperands) PluralCategory {\n%s}\n\n", name, bodies[name])
		}
		fmt.Fprintf(&b, "var plural%sRules = map[string]func(*pluralOperands) PluralCategory{\n", kind.name)
		for _, l := range locs {
			fmt.Fprintf(&b, "\t%q: %s,\n", l, fnOf["loc:"+l])
		}
		b.WriteString("}\n\n")
		fmt.Fprintf(&b, "var plural%sCategories = map[string]string{\n", kind.name)
		for _, l := range locs {
			fmt.Fprintf(&b, "\t%q: %q,\n", l, catsOf[l])
		}
		b.WriteString("}\n\n")
	}
	t.WriteString("}\n")
	return b.Bytes(), t.Bytes(), nil
}

func catConst(c string) string { return strings.ToUpper(c[:1]) + c[1:] }

// splitRule separates the condition from the samples and expands the samples
// to concrete numbers: each listed number and both ends of each range. Samples
// in compact notation (1c6) and the ellipsis are skipped.
func splitRule(raw string) (string, []string) {
	cond := raw
	rest := ""
	if i := strings.Index(raw, "@"); i >= 0 {
		cond, rest = raw[:i], raw[i:]
	}
	var samples []string
	for _, part := range strings.Split(rest, "@") {
		part = strings.TrimSpace(part)
		part = strings.TrimPrefix(part, "integer")
		part = strings.TrimPrefix(part, "decimal")
		for _, s := range strings.Split(part, ",") {
			s = strings.TrimSpace(s)
			if s == "" || s == "…" || strings.ContainsAny(s, "ce") {
				continue
			}
			for _, e := range strings.Split(s, "~") {
				samples = append(samples, strings.TrimSpace(e))
			}
		}
	}
	return strings.TrimSpace(cond), samples
}

// compileRule turns a CLDR plural condition into a Go boolean expression over
// a *pluralOperands named o.
func compileRule(cond string) (string, error) {
	toks := strings.Fields(strings.NewReplacer("!=", " != ", "=", " = ", "%", " % ", ",", " , ").Replace(cond))
	// Re-join "! =" that the replacer split: "!=" becomes " !  = " — handle it.
	var tt []string
	for i := 0; i < len(toks); i++ {
		if toks[i] == "!" && i+1 < len(toks) && toks[i+1] == "=" {
			tt = append(tt, "!=")
			i++
			continue
		}
		tt = append(tt, toks[i])
	}
	toks = tt
	pos := 0
	peek := func() string {
		if pos < len(toks) {
			return toks[pos]
		}
		return ""
	}
	next := func() string { s := peek(); pos++; return s }
	relation := func() (string, error) {
		op := next()
		if !strings.Contains("niv wftce", op) || len(op) != 1 {
			return "", fmt.Errorf("unknown operand %q", op)
		}
		if op == "c" {
			op = "e"
		}
		expr := "o." + op
		if op == "n" {
			expr = "o.i"
		}
		if peek() == "%" {
			next()
			m := next()
			if _, err := strconv.ParseUint(m, 10, 64); err != nil {
				return "", fmt.Errorf("bad modulus %q", m)
			}
			expr = fmt.Sprintf("(%s %% %s)", expr, m)
		}
		rel := next()
		if rel != "=" && rel != "!=" {
			return "", fmt.Errorf("unknown relation %q", rel)
		}
		var alts []string
		for {
			v := next()
			if lo, hi, ok := strings.Cut(v, ".."); ok {
				alts = append(alts, fmt.Sprintf("(%s >= %s && %s <= %s)", expr, lo, expr, hi))
			} else {
				if _, err := strconv.ParseUint(v, 10, 64); err != nil {
					return "", fmt.Errorf("bad value %q", v)
				}
				alts = append(alts, fmt.Sprintf("%s == %s", expr, v))
			}
			if peek() != "," {
				break
			}
			next()
		}
		in := strings.Join(alts, " || ")
		if op == "n" {
			// n is the absolute value: it equals an integer only when it has no
			// fraction, and CLDR ranges on n hold integers only.
			in = fmt.Sprintf("o.whole() && (%s)", in)
		}
		if rel == "!=" {
			return fmt.Sprintf("!(%s)", in), nil
		}
		return fmt.Sprintf("(%s)", in), nil
	}
	var ors []string
	for {
		var ands []string
		for {
			r, err := relation()
			if err != nil {
				return "", err
			}
			ands = append(ands, r)
			if peek() != "and" {
				break
			}
			next()
		}
		ors = append(ors, "("+strings.Join(ands, " && ")+")")
		if peek() != "or" {
			break
		}
		next()
	}
	if pos != len(toks) {
		return "", fmt.Errorf("unexpected %q", peek())
	}
	return strings.Join(ors, " || "), nil
}
