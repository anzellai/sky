package rt

import (
	"bufio"
	"encoding/hex"
	"os"
	"strconv"
	"strings"
	"testing"
)

// The reference matrices in testdata/qr_reference.txt were produced by an
// independent encoder, rsc.io/qr v0.2.0 (package coding), with the mask
// forced, so the comparison covers the data encoding, Reed–Solomon blocks,
// interleaving, function patterns, format and version information, and all
// eight mask patterns, from version 1 to version 40 (the file header names
// the exact calls).
func TestQrMatchesReferenceMatrices(t *testing.T) {
	f, err := os.Open("testdata/qr_reference.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<16), 1<<16)
	cases := 0
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "case ") {
			continue
		}
		fs := strings.Fields(line)
		text := ""
		if fs[1] != "-" {
			b, _ := hex.DecodeString(fs[1])
			text = string(b)
		}
		lvl, _ := strconv.Atoi(fs[2])
		ver, _ := strconv.Atoi(fs[3])
		mask, _ := strconv.Atoi(fs[4])
		got, err := qrEncodeBytes([]byte(text), lvl, mask)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		if got.size != ver*4+17 {
			t.Fatalf("case %d: chose version %d, reference used %d", cases, (got.size-17)/4, ver)
		}
		for y := 0; y < got.size; y++ {
			if !sc.Scan() {
				t.Fatalf("truncated fixture")
			}
			row, _ := hex.DecodeString(sc.Text())
			for x := 0; x < got.size; x++ {
				want := row[x/8]&(1<<uint(7-x%8)) != 0
				if got.mods[y*got.size+x] != want {
					t.Fatalf("case %d (v%d level %d mask %d): module (%d,%d) differs", cases, ver, lvl, mask, x, y)
				}
			}
		}
		cases++
	}
	if cases != 8 {
		t.Fatalf("expected 8 reference cases, read %d", cases)
	}
}

// readFormat reads the first copy of the format information back out of the
// symbol and decodes level and mask, so a test can check the symbol says
// what the encoder chose.
func readFormat(c QrCode) (lvl, mask int, ok bool) {
	at := func(x, y int) int {
		if c.mods[y*c.size+x] {
			return 1
		}
		return 0
	}
	bits := 0
	set := func(i, v int) { bits |= v << uint(i) }
	for i := 0; i <= 5; i++ {
		set(i, at(8, i))
	}
	set(6, at(8, 7))
	set(7, at(8, 8))
	set(8, at(7, 8))
	for i := 9; i < 15; i++ {
		set(i, at(14-i, 8))
	}
	bits ^= 0x5412
	data := bits >> 10
	for l := 0; l < 4; l++ {
		if qrFormatBits[l] == data>>3 {
			return l, data & 7, true
		}
	}
	return 0, 0, false
}

func TestQrMaskChoiceMinimisesPenalty(t *testing.T) {
	for _, lvl := range []int{qrLow, qrMedium, qrQuartile, qrHigh} {
		for _, text := range []string{"HELLO WORLD", "https://sky-lang.org/", strings.Repeat("ab", 90)} {
			chosen, err := qrEncodeBytes([]byte(text), lvl, -1)
			if err != nil {
				t.Fatal(err)
			}
			l, m, ok := readFormat(chosen)
			if !ok || l != lvl {
				t.Fatalf("format information does not decode to level %d", lvl)
			}
			best := -1
			for mask := 0; mask < 8; mask++ {
				c, _ := qrEncodeBytes([]byte(text), lvl, mask)
				g := &qrGrid{size: c.size, mods: c.mods}
				if p := g.penalty(); best < 0 || p < best {
					best = p
				}
			}
			forced, _ := qrEncodeBytes([]byte(text), lvl, m)
			g := &qrGrid{size: forced.size, mods: forced.mods}
			if g.penalty() != best {
				t.Fatalf("%q level %d: chosen mask %d has penalty %d, the minimum is %d", text, lvl, m, g.penalty(), best)
			}
		}
	}
}

func TestQrKernels(t *testing.T) {
	c := cryOk(t, Qr_encodeWith(1, "HELLO WORLD"))
	if Qr_size(c).(int) != 21 {
		t.Fatalf("HELLO WORLD at level M should be version 1 (21 modules)")
	}
	// The top-left finder pattern: a dark 7×7 ring.
	if !Qr_isDark(0, 0, c).(bool) || Qr_isDark(1, 1, c).(bool) || !Qr_isDark(3, 3, c).(bool) {
		t.Fatalf("finder pattern not where it should be")
	}
	// Outside the symbol is light, never a panic.
	for _, xy := range [][2]int{{-1, 0}, {0, -1}, {21, 0}, {0, 21}, {1000, 1000}} {
		if Qr_isDark(xy[0], xy[1], c).(bool) {
			t.Fatalf("isDark outside the symbol returned True")
		}
	}
	rows := Qr_rows(c).([]any)
	if len(rows) != 21 || len(rows[0].([]any)) != 21 || rows[0].([]any)[0] != true {
		t.Fatalf("rows has the wrong shape")
	}
	// Byte-mode capacity at level L, version 40, is 2953 bytes.
	cryOk(t, Qr_encodeWith(0, strings.Repeat("z", 2953)))
	cryErr(t, Qr_encodeWith(0, strings.Repeat("z", 2954)), "too long")
	cryErr(t, Qr_encodeWith(3, strings.Repeat("z", 1274)), "too long")
	cryErr(t, Qr_encodeWith(9, "x"), "level")
}

// The encoder is deterministic: the same input is the same symbol.
func TestQrDeterministic(t *testing.T) {
	a, _ := qrEncodeBytes([]byte("same"), qrQuartile, -1)
	b, _ := qrEncodeBytes([]byte("same"), qrQuartile, -1)
	for i := range a.mods {
		if a.mods[i] != b.mods[i] {
			t.Fatalf("two encodes of the same text differ")
		}
	}
}
