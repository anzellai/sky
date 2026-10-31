//go:build !js

package rt

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"math"
	"math/rand"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/image/webp"
)

// vp8lTestImage builds a deterministic test image of the given kind.
func vp8lTestImage(rng *rand.Rand, kind string, w, h int, offset bool) *image.NRGBA {
	ox, oy := 0, 0
	if offset {
		ox, oy = rng.Intn(50)-25, rng.Intn(50)-25
	}
	img := image.NewNRGBA(image.Rect(ox, oy, ox+w, oy+h))
	set := func(x, y int, c [4]byte) {
		i := img.PixOffset(ox+x, oy+y)
		copy(img.Pix[i:i+4], c[:])
	}
	randColour := func() [4]byte {
		return [4]byte{byte(rng.Intn(256)), byte(rng.Intn(256)), byte(rng.Intn(256)), byte(rng.Intn(256))}
	}
	palette := func(n int) [][4]byte {
		p := make([][4]byte, n)
		for i := range p {
			p[i] = randColour()
		}
		return p
	}
	switch {
	case kind == "noise":
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				set(x, y, randColour())
			}
		}
	case strings.HasPrefix(kind, "colours"):
		n, _ := strconv.Atoi(strings.TrimPrefix(kind, "colours"))
		p := palette(n)
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				set(x, y, p[rng.Intn(n)])
			}
		}
	case kind == "gradient":
		a := rng.Intn(2) == 0
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				c := [4]byte{byte(x * 255 / max(w, 1)), byte(y * 255 / max(h, 1)), byte((x + y) * 2), 255}
				if a {
					c[3] = byte(255 - x)
				}
				set(x, y, c)
			}
		}
	case kind == "transparent":
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				c := randColour()
				c[3] = 0
				set(x, y, c)
			}
		}
	case kind == "pattern":
		tw, th := 1+rng.Intn(9), 1+rng.Intn(5)
		tile := make([][4]byte, tw*th)
		for i := range tile {
			tile[i] = randColour()
		}
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				c := tile[(y%th)*tw+x%tw]
				if rng.Intn(50) == 0 {
					c = randColour()
				}
				set(x, y, c)
			}
		}
	case kind == "single":
		c := randColour()
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				set(x, y, c)
			}
		}
	default:
		panic("unknown kind " + kind)
	}
	return img
}

// vp8lRoundTrip encodes img, decodes it with x/image/webp and compares every
// channel of every pixel. It returns the encoded bytes.
func vp8lRoundTrip(t *testing.T, name string, img *image.NRGBA) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := webpLosslessEncode(&buf, img); err != nil {
		t.Fatalf("%s: encode: %v", name, err)
	}
	dec, err := webp.Decode(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("%s: decode: %v", name, err)
	}
	got, ok := dec.(*image.NRGBA)
	if !ok {
		t.Fatalf("%s: decoded %T, want *image.NRGBA", name, dec)
	}
	vp8lComparePix(t, name, img, got.Pix, got.Stride, got.Bounds().Dx(), got.Bounds().Dy())
	return buf.Bytes()
}

func vp8lComparePix(t *testing.T, name string, want *image.NRGBA, pix []byte, stride, w, h int) {
	t.Helper()
	b := want.Bounds()
	if w != b.Dx() || h != b.Dy() {
		t.Fatalf("%s: size %dx%d, want %dx%d", name, w, h, b.Dx(), b.Dy())
	}
	for y := 0; y < h; y++ {
		wi := want.PixOffset(b.Min.X, b.Min.Y+y)
		if !bytes.Equal(want.Pix[wi:wi+4*w], pix[y*stride:y*stride+4*w]) {
			for x := 0; x < w; x++ {
				if !bytes.Equal(want.Pix[wi+4*x:wi+4*x+4], pix[y*stride+4*x:y*stride+4*x+4]) {
					t.Fatalf("%s: pixel (%d,%d) = %v, want %v", name, x, y,
						pix[y*stride+4*x:y*stride+4*x+4], want.Pix[wi+4*x:wi+4*x+4])
				}
			}
		}
	}
}

func vp8lKinds() []string {
	return []string{"noise", "colours2", "colours3", "colours16", "colours200",
		"colours256", "colours257", "gradient", "transparent", "pattern", "single"}
}

func TestVP8LRoundTripProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(20261029))
	for i := 0; i < 200; i++ {
		kinds := vp8lKinds()
		kind := kinds[i%len(kinds)]
		w, h := 1+rng.Intn(70), 1+rng.Intn(70)
		if i%50 == 7 {
			w, h = 300, 200
		}
		name := fmt.Sprintf("#%d %s %dx%d", i, kind, w, h)
		vp8lRoundTrip(t, name, vp8lTestImage(rng, kind, w, h, i%3 == 0))
	}
}

func TestVP8LSizes(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for _, s := range [][2]int{{1, 1}, {1, 37}, {1, 300}, {37, 1}, {300, 1}, {16384, 1}, {1, 16384}, {2, 2}} {
		for _, kind := range []string{"noise", "colours3", "pattern", "single", "gradient"} {
			name := fmt.Sprintf("%s %dx%d", kind, s[0], s[1])
			vp8lRoundTrip(t, name, vp8lTestImage(rng, kind, s[0], s[1], false))
		}
	}
	for _, s := range [][2]int{{0, 5}, {5, 0}, {16385, 1}, {1, 16385}} {
		img := image.NewNRGBA(image.Rect(0, 0, s[0], s[1]))
		if err := webpLosslessEncode(&bytes.Buffer{}, img); err == nil {
			t.Errorf("%dx%d: want an error", s[0], s[1])
		}
	}
	if err := webpLosslessEncode(&bytes.Buffer{}, nil); err == nil {
		t.Errorf("nil image: want an error")
	}
}

// TestVP8LNonNRGBA checks the conversion path for other image types.
func TestVP8LNonNRGBA(t *testing.T) {
	g := image.NewGray(image.Rect(3, 4, 40, 31))
	for i := range g.Pix {
		g.Pix[i] = byte(i * 7)
	}
	var buf bytes.Buffer
	if err := webpLosslessEncode(&buf, g); err != nil {
		t.Fatal(err)
	}
	dec, err := webp.Decode(&buf)
	if err != nil {
		t.Fatal(err)
	}
	b := g.Bounds()
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			r, gg, bb, a := dec.At(x, y).RGBA()
			want := uint32(g.GrayAt(b.Min.X+x, b.Min.Y+y).Y) * 0x101
			if r != want || gg != want || bb != want || a != 0xffff {
				t.Fatalf("(%d,%d) mismatch", x, y)
			}
		}
	}
}

func vp8lSmoothGradient(w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			fx, fy := float64(x)/float64(w), float64(y)/float64(h)
			i := img.PixOffset(x, y)
			img.Pix[i+0] = byte(255 * fx)
			img.Pix[i+1] = byte(255 * fy)
			img.Pix[i+2] = byte(127 + 127*math.Sin(3*fx+2*fy))
			img.Pix[i+3] = 255
		}
	}
	return img
}

func TestVP8LCompression(t *testing.T) {
	rng := rand.New(rand.NewSource(99))
	cases := []struct {
		name string
		img  *image.NRGBA
	}{
		{"smooth gradient 256x256", vp8lSmoothGradient(256, 256)},
		{"pattern 300x200", vp8lTestImage(rng, "pattern", 300, 200, false)},
		{"16 colours 300x200", vp8lTestImage(rng, "colours16", 300, 200, false)},
		{"noise 300x200", vp8lTestImage(rng, "noise", 300, 200, false)},
		{"single 300x200", vp8lTestImage(rng, "single", 300, 200, false)},
	}
	for _, c := range cases {
		out := vp8lRoundTrip(t, c.name, c.img)
		var p bytes.Buffer
		if err := png.Encode(&p, c.img); err != nil {
			t.Fatal(err)
		}
		raw := len(c.img.Pix)
		t.Logf("%-24s raw %7d  webp %7d (%5.1f%%)  png %7d", c.name, raw, len(out),
			100*float64(len(out))/float64(raw), p.Len())
		if c.name == "smooth gradient 256x256" && len(out)*100 >= raw*60 {
			t.Errorf("gradient: %d bytes is not under 60%% of raw %d", len(out), raw)
		}
	}
}
