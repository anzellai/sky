// Command gen-time-zones writes runtime-go/rt/time_zone_names.go: the IANA
// zone names that Std.Time lists, the link names it also accepts, and the
// scan step that Std.Time.nextTransition and previousTransition use.
//
// Go has no API that lists the zones in its embedded database (time/tzdata).
// That package is made from the toolchain's lib/time/zoneinfo.zip, so this
// generator reads the same file. The zip holds one file for each name and
// does not say which names are links. The generator reads that from the IANA
// tzdata source release whose version the toolchain's lib/time/update.bash
// names ("Zone" lines are canonical names, "Link" lines are aliases). The
// release is checked against a pinned SHA-256.
//
// Run it with the Go version that the repository builds with (the
// go-version of the CI workflows):
//
//	cd tools/gen-time-zones
//	GOTOOLCHAIN=go1.26.0 go run . -out ../../runtime-go/rt/time_zone_names.go
//
// Use -tzdata <file> to give a tzdata release that is already on disk. The
// generator refuses a toolchain of another minor version and a tzdata
// release with an unknown digest. When the repository moves to a new Go
// minor version, change pinnedGoMinor, add the digest of the new tzdata
// release to pinnedTzdata and run the generator again. The test
// rt/time_zone_names_test.go fails when the committed file and the
// toolchain's zone data do not agree.
package main

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"go/format"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

// pinnedGoMinor is the Go minor version that the repository builds with.
const pinnedGoMinor = "go1.26"

// pinnedTzdata maps a tzdata release to the SHA-256 of its tarball.
var pinnedTzdata = map[string]string{
	"2025c": "4aa79e4effee53fc4029ffe5f6ebe97937282ebcdf386d5d2da91ce84142f957",
}

// The tzdata source files that hold Zone and Link lines. "backzone" is
// left out on purpose: it holds older data for names that are links in
// the main data, and those names stay links.
var tzdataSources = []string{
	"africa", "antarctica", "asia", "australasia", "europe",
	"northamerica", "southamerica", "etcetera", "backward", "factory",
}

func main() {
	out := flag.String("out", "", "the Go file to write (required)")
	tzdataPath := flag.String("tzdata", "", "a tzdata<version>.tar.gz on disk (else it is downloaded from data.iana.org)")
	flag.Parse()
	if *out == "" {
		fail(errors.New("-out is required"))
	}
	if err := run(*out, *tzdataPath); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "gen-time-zones:", err)
	os.Exit(1)
}

func run(out, tzdataPath string) error {
	goVersion := runtime.Version()
	if goVersion != pinnedGoMinor && !strings.HasPrefix(goVersion, pinnedGoMinor+".") {
		return fmt.Errorf("the toolchain is %s, but the generator is pinned to %s: run it with GOTOOLCHAIN=%s.0", goVersion, pinnedGoMinor, pinnedGoMinor)
	}
	goroot := runtime.GOROOT()
	dataVersion, err := toolchainTzdataVersion(goroot)
	if err != nil {
		return err
	}
	want, ok := pinnedTzdata[dataVersion]
	if !ok {
		return fmt.Errorf("the toolchain's tzdata is %s, which has no pinned digest: add it to pinnedTzdata", dataVersion)
	}
	tarball, err := readTzdata(tzdataPath, dataVersion)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(tarball)
	if got := hex.EncodeToString(sum[:]); got != want {
		return fmt.Errorf("tzdata%s.tar.gz has SHA-256 %s, want %s", dataVersion, got, want)
	}
	zones, links, err := parseTzdata(tarball, dataVersion)
	if err != nil {
		return err
	}
	files, err := readZip(filepath.Join(goroot, "lib", "time", "zoneinfo.zip"))
	if err != nil {
		return err
	}

	// Every name in the zip is a Zone or a Link, and every Zone and Link
	// is in the zip.
	for name := range files {
		_, z := zones[name]
		_, l := links[name]
		if !z && !l {
			return fmt.Errorf("zoneinfo.zip holds %q, which tzdata%s does not name", name, dataVersion)
		}
	}
	for name := range zones {
		if _, ok := files[name]; !ok {
			return fmt.Errorf("tzdata%s has Zone %q, which zoneinfo.zip does not hold", dataVersion, name)
		}
	}
	for name := range links {
		if _, ok := files[name]; !ok {
			return fmt.Errorf("tzdata%s has Link %q, which zoneinfo.zip does not hold", dataVersion, name)
		}
	}

	// "Factory" is a Zone for machines with no zone set. It is not a place.
	var canonical []string
	for name := range zones {
		if name != "Factory" {
			canonical = append(canonical, name)
		}
	}
	sort.Strings(canonical)
	var aliases []string
	for name := range links {
		aliases = append(aliases, name)
	}
	sort.Strings(aliases)

	gap, gapZone, gapAt, err := minTransitionGap(files)
	if err != nil {
		return err
	}
	src, err := render(goVersion, dataVersion, canonical, aliases, links, gap, gapZone, gapAt)
	if err != nil {
		return err
	}
	if err := os.WriteFile(out, src, 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s: %d zones, %d links, tzdata %s, %s; shortest interval between transitions %d s (%s at %s)\n",
		out, len(canonical), len(aliases), dataVersion, goVersion, gap, gapZone, time.Unix(gapAt, 0).UTC().Format(time.RFC3339))
	return nil
}

var dataLine = regexp.MustCompile(`(?m)^DATA=([0-9]{4}[a-z])\s*$`)

func toolchainTzdataVersion(goroot string) (string, error) {
	b, err := os.ReadFile(filepath.Join(goroot, "lib", "time", "update.bash"))
	if err != nil {
		return "", err
	}
	m := dataLine.FindSubmatch(b)
	if m == nil {
		return "", errors.New("lib/time/update.bash has no DATA= line")
	}
	return string(m[1]), nil
}

func readTzdata(path, version string) ([]byte, error) {
	if path != "" {
		return os.ReadFile(path)
	}
	url := "https://data.iana.org/time-zones/releases/tzdata" + version + ".tar.gz"
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 16<<20))
}

func parseTzdata(tarball []byte, version string) (zones map[string]bool, links map[string]string, err error) {
	gz, err := gzip.NewReader(bytes.NewReader(tarball))
	if err != nil {
		return nil, nil, err
	}
	tr := tar.NewReader(gz)
	want := map[string]bool{}
	for _, s := range tzdataSources {
		want[s] = true
	}
	zones = map[string]bool{}
	links = map[string]string{}
	seen := map[string]bool{}
	gotVersion := ""
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		if h.Name == "version" {
			b, err := io.ReadAll(tr)
			if err != nil {
				return nil, nil, err
			}
			gotVersion = strings.TrimSpace(string(b))
			continue
		}
		if !want[h.Name] {
			continue
		}
		seen[h.Name] = true
		sc := bufio.NewScanner(tr)
		for sc.Scan() {
			line := sc.Text()
			if i := strings.IndexByte(line, '#'); i >= 0 {
				line = line[:i]
			}
			f := strings.Fields(line)
			if len(f) == 0 {
				continue
			}
			switch f[0] {
			case "Zone":
				if len(f) < 2 {
					return nil, nil, fmt.Errorf("%s: short Zone line %q", h.Name, line)
				}
				zones[f[1]] = true
			case "Link":
				if len(f) < 3 {
					return nil, nil, fmt.Errorf("%s: short Link line %q", h.Name, line)
				}
				links[f[2]] = f[1]
			}
		}
		if err := sc.Err(); err != nil {
			return nil, nil, err
		}
	}
	if gotVersion != version {
		return nil, nil, fmt.Errorf("the tarball says version %q, want %q", gotVersion, version)
	}
	for _, s := range tzdataSources {
		if !seen[s] {
			return nil, nil, fmt.Errorf("the tarball has no %q file", s)
		}
	}
	for l := range links {
		if zones[l] {
			return nil, nil, fmt.Errorf("%q is both a Zone and a Link", l)
		}
	}
	return zones, links, nil
}

func readZip(path string) (map[string][]byte, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	files := map[string][]byte{}
	for _, f := range zr.File {
		if strings.HasSuffix(f.Name, "/") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, err
		}
		files[f.Name] = b
	}
	return files, nil
}

// zoneState is what Std.Time treats as the zone's state: the UTC offset
// and the abbreviation. A change of the DST flag alone is not a transition.
type zoneState struct {
	offset int
	abbr   string
}

// tzifTransitions reads the 64-bit transitions of a TZif file (version 2
// or later) and returns the instants (Unix seconds) where the zoneState
// changes.
func tzifTransitions(b []byte) ([]int64, error) {
	hdr := func(b []byte) (counts [6]int, rest []byte, version byte, err error) {
		if len(b) < 44 || string(b[:4]) != "TZif" {
			return counts, nil, 0, errors.New("not a TZif file")
		}
		version = b[4]
		for i := range counts {
			counts[i] = int(binary.BigEndian.Uint32(b[20+4*i:]))
		}
		return counts, b[44:], version, nil
	}
	c, rest, version, err := hdr(b)
	if err != nil {
		return nil, err
	}
	if version < '2' {
		return nil, errors.New("TZif version 1 has no 64-bit data")
	}
	isut, isstd, leap, timecnt, typecnt, charcnt := c[0], c[1], c[2], c[3], c[4], c[5]
	skip := timecnt*4 + timecnt + typecnt*6 + charcnt + leap*8 + isstd + isut
	if len(rest) < skip {
		return nil, errors.New("short TZif version 1 block")
	}
	c, rest, _, err = hdr(rest[skip:])
	if err != nil {
		return nil, err
	}
	_, _, leap, timecnt, typecnt, charcnt = c[0], c[1], c[2], c[3], c[4], c[5]
	need := timecnt*8 + timecnt + typecnt*6 + charcnt
	if len(rest) < need {
		return nil, errors.New("short TZif version 2 block")
	}
	if leap != 0 {
		return nil, errors.New("TZif with leap seconds")
	}
	times := make([]int64, timecnt)
	for i := range times {
		times[i] = int64(binary.BigEndian.Uint64(rest[8*i:]))
	}
	idx := rest[8*timecnt : 9*timecnt]
	types := rest[9*timecnt : 9*timecnt+6*typecnt]
	chars := rest[9*timecnt+6*typecnt : 9*timecnt+6*typecnt+charcnt]
	state := func(i int) zoneState {
		t := types[6*i:]
		off := int(int32(binary.BigEndian.Uint32(t)))
		ai := int(t[5])
		end := bytes.IndexByte(chars[ai:], 0)
		return zoneState{offset: off, abbr: string(chars[ai : ai+end])}
	}
	var out []int64
	if typecnt == 0 {
		return nil, nil
	}
	prev := state(0)
	for i, t := range times {
		s := state(int(idx[i]))
		if s != prev {
			out = append(out, t)
		}
		prev = s
	}
	return out, nil
}

// minTransitionGap returns the shortest interval, in seconds, between two
// transitions of one zone file. It reads the transitions that the files
// list, and it finds the ones that the files' rules give after the last
// listed transition by asking the time package hour by hour for two years.
func minTransitionGap(files map[string][]byte) (gap int64, zone string, at int64, err error) {
	gap = 1 << 62
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		tx, err := tzifTransitions(files[name])
		if err != nil {
			return 0, "", 0, fmt.Errorf("%s: %w", name, err)
		}
		loc, err := time.LoadLocationFromTZData(name, files[name])
		if err != nil {
			return 0, "", 0, err
		}
		start := int64(0)
		if len(tx) > 0 {
			start = tx[len(tx)-1]
		}
		if start < 0 {
			start = 0
		}
		prev := stateAt(loc, start)
		for t := start + 3600; t <= start+2*366*86400; t += 3600 {
			s := stateAt(loc, t)
			if s != prev {
				lo, hi := t-3600, t
				for hi-lo > 1 {
					mid := lo + (hi-lo)/2
					if stateAt(loc, mid) == prev {
						lo = mid
					} else {
						hi = mid
					}
				}
				if hi > start {
					tx = append(tx, hi)
				}
				prev = s
			}
		}
		for i := 1; i < len(tx); i++ {
			if d := tx[i] - tx[i-1]; d < gap {
				gap, zone, at = d, name, tx[i-1]
			}
		}
	}
	return gap, zone, at, nil
}

func stateAt(loc *time.Location, sec int64) zoneState {
	name, off := time.Unix(sec, 0).In(loc).Zone()
	return zoneState{offset: off, abbr: name}
}

func render(goVersion, dataVersion string, canonical, aliases []string, links map[string]string, gap int64, gapZone string, gapAt int64) ([]byte, error) {
	var b strings.Builder
	b.WriteString("// Code generated by tools/gen-time-zones. DO NOT EDIT.\n\n")
	b.WriteString("package rt\n\n")
	fmt.Fprintf(&b, "// timeZoneData is the source of this file: the IANA tzdata release and\n// the Go toolchain whose lib/time/zoneinfo.zip it was read from.\n")
	fmt.Fprintf(&b, "const timeZoneData = %q\n\n", "tzdata "+dataVersion+", "+goVersion)
	fmt.Fprintf(&b, "// timeZoneMinGapMs is the shortest interval between two transitions of\n// one zone in the data (%s at %s). Std.Time scans for a transition in\n// steps of this size or less, so a scan cannot step over two transitions.\n",
		gapZone, time.Unix(gapAt, 0).UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "const timeZoneMinGapMs = %d\n\n", gap*1000)
	b.WriteString("// timeZoneNames holds the canonical IANA zone names (tzdata Zone lines),\n// sorted. Std.Time.zones returns it.\n")
	b.WriteString("var timeZoneNames = []string{\n")
	for _, n := range canonical {
		fmt.Fprintf(&b, "\t%q,\n", n)
	}
	b.WriteString("}\n\n")
	b.WriteString("// timeZoneLinks maps each link name (tzdata Link lines) to its target.\n// Std.Time.isZone accepts these names, but Std.Time.zones does not list them.\n")
	b.WriteString("var timeZoneLinks = map[string]string{\n")
	for _, n := range aliases {
		fmt.Fprintf(&b, "\t%q: %q,\n", n, links[n])
	}
	b.WriteString("}\n")
	return format.Source([]byte(b.String()))
}
