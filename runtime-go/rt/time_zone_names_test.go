package rt

// The generated zone list (time_zone_names.go) against the zone data of the
// toolchain that runs the test. time/tzdata, which time_zones.go embeds, is
// made from the toolchain's lib/time/zoneinfo.zip, so the test reads that
// file. When a Go upgrade adds or removes a name, this test fails: run
// tools/gen-time-zones again (see its package comment).

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"
)

func toolchainZoneFiles(t *testing.T) map[string][]byte {
	t.Helper()
	path := filepath.Join(runtime.GOROOT(), "lib", "time", "zoneinfo.zip")
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("the Go toolchain has no %s: %v", path, err)
	}
	defer zr.Close()
	files := map[string][]byte{}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		files[f.Name] = b
	}
	return files
}

// tzifTransitionsMs reads the 64-bit transitions of a TZif file and returns
// the instants (ms) where the offset or the abbreviation changes. It is the
// same reading as tools/gen-time-zones.
func tzifTransitionsMs(b []byte) ([]int64, error) {
	hdr := func(b []byte) (c [6]int, rest []byte, v byte, err error) {
		if len(b) < 44 || string(b[:4]) != "TZif" {
			return c, nil, 0, errors.New("not a TZif file")
		}
		for i := range c {
			c[i] = int(binary.BigEndian.Uint32(b[20+4*i:]))
		}
		return c, b[44:], b[4], nil
	}
	c, rest, v, err := hdr(b)
	if err != nil {
		return nil, err
	}
	if v < '2' {
		return nil, errors.New("TZif version 1")
	}
	skip := c[3]*4 + c[3] + c[4]*6 + c[5] + c[2]*8 + c[1] + c[0]
	if len(rest) < skip {
		return nil, errors.New("short TZif")
	}
	if c, rest, _, err = hdr(rest[skip:]); err != nil {
		return nil, err
	}
	timecnt, typecnt, charcnt := c[3], c[4], c[5]
	if len(rest) < timecnt*9+typecnt*6+charcnt || c[2] != 0 || typecnt == 0 {
		return nil, errors.New("unexpected TZif version 2 block")
	}
	idx := rest[8*timecnt : 9*timecnt]
	types := rest[9*timecnt : 9*timecnt+6*typecnt]
	chars := rest[9*timecnt+6*typecnt : 9*timecnt+6*typecnt+charcnt]
	state := func(i int) zoneState {
		tt := types[6*i:]
		ai := int(tt[5])
		return zoneState{offset: int(int32(binary.BigEndian.Uint32(tt))), abbr: string(chars[ai : ai+bytes.IndexByte(chars[ai:], 0)])}
	}
	var out []int64
	prev := state(0)
	for i := 0; i < timecnt; i++ {
		s := state(int(idx[i]))
		// zic may write a first transition at -2^59 s. Keep the years
		// that Std.Time scans, so the ms value cannot overflow.
		if sec := int64(binary.BigEndian.Uint64(rest[8*i:])); s != prev && sec < timeScanMaxMs/1000 && sec > timeScanMinMs/1000 {
			out = append(out, sec*1000)
		}
		prev = s
	}
	return out, nil
}

func TestTimeZoneNames_MatchToolchainData(t *testing.T) {
	files := toolchainZoneFiles(t)
	if !sort.StringsAreSorted(timeZoneNames) {
		t.Fatal("timeZoneNames is not sorted")
	}
	listed := map[string]bool{"Factory": true}
	for i, n := range timeZoneNames {
		if i > 0 && timeZoneNames[i-1] == n {
			t.Fatalf("timeZoneNames holds %q twice", n)
		}
		listed[n] = true
	}
	for l, target := range timeZoneLinks {
		if listed[l] {
			t.Errorf("%q is both a zone and a link", l)
		}
		listed[l] = true
		if i := sort.SearchStrings(timeZoneNames, target); i == len(timeZoneNames) || timeZoneNames[i] != target {
			t.Errorf("link %q points to %q, which is not a listed zone", l, target)
		}
	}
	for n := range files {
		if !listed[n] {
			t.Errorf("the toolchain's zone data holds %q, which the generated list lacks: run tools/gen-time-zones", n)
		}
	}
	for n := range listed {
		if _, ok := files[n]; !ok {
			t.Errorf("the generated list holds %q, which the toolchain's zone data lacks: run tools/gen-time-zones", n)
		}
	}
	// Every listed name and link loads, through the same path as the
	// Std.Time functions.
	for n := range listed {
		if n == "Factory" {
			continue
		}
		if _, err := loadZone(n); err != nil {
			t.Errorf("loadZone %q: %v", n, err)
		}
		if invokeTime(t, "Time_isZone", n) != true {
			t.Errorf("isZone %q is False", n)
		}
	}
}

// The scan in nextZoneTransition steps one day at a time. That is correct
// only while no zone has two transitions closer than one step. This checks
// the toolchain's data, both the listed transitions and the ones that each
// zone's rule gives after them.
func TestTimeZoneNames_StepIsBelowShortestGap(t *testing.T) {
	if timeZoneMinGapMs < timeScanStepMs {
		t.Fatalf("the generated shortest gap %d ms is below the scan step %d ms", timeZoneMinGapMs, timeScanStepMs)
	}
	files := toolchainZoneFiles(t)
	for name, b := range files {
		tx, err := tzifTransitionsMs(b)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		loc, err := time.LoadLocationFromTZData(name, b)
		if err != nil {
			t.Fatal(err)
		}
		// The rule after the last listed transition, hour by hour for one
		// year.
		start := int64(0)
		if len(tx) > 0 && tx[len(tx)-1] > 0 {
			start = tx[len(tx)-1]
		}
		prev := zoneStateAt(loc, start)
		for ms := start + 3600_000; ms <= start+366*timeDayMs; ms += 3600_000 {
			if s := zoneStateAt(loc, ms); s != prev {
				tx = append(tx, ms)
				prev = s
			}
		}
		for i := 1; i < len(tx); i++ {
			if d := tx[i] - tx[i-1]; d < timeScanStepMs+3600_000 {
				t.Errorf("%s: transitions %s and %s are %d ms apart, below the scan step", name,
					time.UnixMilli(tx[i-1]).UTC(), time.UnixMilli(tx[i]).UTC(), d)
			}
		}
	}
}

// nextZoneTransition and previousZoneTransition against the transitions
// that the TZif files list, for every zone file: each listed transition is
// found from the one before it (so the scan steps over none) and from just
// after it backwards.
func TestTimeZoneTransitions_MatchTZif(t *testing.T) {
	files := toolchainZoneFiles(t)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	checked := 0
	for _, name := range names {
		tx, err := tzifTransitionsMs(files[name])
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		loc, err := time.LoadLocationFromTZData(name, files[name])
		if err != nil {
			t.Fatal(err)
		}
		for i, at := range tx {
			if at <= timeScanMinMs || at >= timeScanMaxMs {
				continue
			}
			from := at - 30*timeDayMs
			if i > 0 && tx[i-1] > from {
				from = tx[i-1]
			}
			got, ok := nextZoneTransition(loc, from, from+timeTransitionWindow)
			if !ok || got != at {
				t.Errorf("%s: next from %s = %v %s, want %s", name, time.UnixMilli(from).UTC(), ok,
					time.UnixMilli(got).UTC(), time.UnixMilli(at).UTC())
				continue
			}
			back, ok := previousZoneTransition(loc, at+1, at+1-timeTransitionWindow)
			if !ok || back != at {
				t.Errorf("%s: previous from just after %s = %v %s", name, time.UnixMilli(at).UTC(), ok, time.UnixMilli(back).UTC())
			}
			checked++
		}
	}
	if checked < 10000 {
		t.Fatalf("checked only %d transitions", checked)
	}
}
