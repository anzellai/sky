//go:build !js

package rt

import (
	"bytes"
	"flag"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/webp"
)

// -canvas-regen rewrites the committed reference image of the phone frame
// (only when the drawing is changed on purpose).
var canvasRegen = flag.Bool("canvas-regen", false, "rewrite testdata/canvas_phone_frame.png")

func pack(r, g, b, a int) int { return ((r*256+g)*256+b)*256 + a }

func canvasPNG(t *testing.T, c any) image.Image {
	t.Helper()
	out := okBytes(t, forceImageTask(t, Image_canvasEncode("png", 0, c)))
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("canvas png: %v", err)
	}
	return img
}

func nrgbaAt(img image.Image, x, y int) color.NRGBA {
	return color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
}

// The phone frame: a body with round corners and an outline, a screenshot
// pasted through a rounded mask, a camera pill, a status text and a home bar.
// A quarter of the 952 x 1812 frame keeps the committed reference small.
func phoneFrame(screenshot string) any {
	w, h := 238, 453
	c := Image_canvasBlank(w, h, pack(0, 0, 0, 0))
	c = Image_canvasRoundedRect(0, 0, w, h, 40, 0, pack(24, 24, 28, 255), c)
	c = Image_canvasRoundedRect(0, 0, w, h, 40, 3, pack(90, 90, 100, 255), c)
	c = Image_canvasPaste(10, 10, true, Image_canvasRoundedMask(w-20, h-20, 30), screenshot, c)
	c = Image_canvasRoundedRect(w/2-30, 18, 60, 14, 7, 0, pack(0, 0, 0, 255), c)
	c = Image_canvasEllipse(w/2+16, 21, 8, 8, 0, pack(30, 40, 70, 255), c)
	c = Image_canvasText(Image_canvasDefaultFont(nil), 12.0, pack(255, 255, 255, 255), 24, 16, "9:41", c)
	c = Image_canvasRoundedRect(w/2-40, h-22, 80, 5, 3, 0, pack(255, 255, 255, 230), c)
	return c
}

func phoneScreenshot(t *testing.T) string {
	img := image.NewNRGBA(image.Rect(0, 0, 218, 433))
	for y := 0; y < 433; y++ {
		for x := 0; x < 218; x++ {
			img.SetNRGBA(x, y, color.NRGBA{uint8(40 + x/2), uint8(80 + y/4), 200, 255})
		}
	}
	return encPNG(t, img)
}

func TestCanvas_PhoneFrameMatchesReference(t *testing.T) {
	out := okBytes(t, forceImageTask(t, Image_canvasEncode("png", 0, phoneFrame(phoneScreenshot(t)))))
	ref := filepath.Join("testdata", "canvas_phone_frame.png")
	if *canvasRegen {
		if err := os.WriteFile(ref, out, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(ref)
	if err != nil {
		t.Fatalf("reference: %v", err)
	}
	diff := okInt(t, forceImageTask(t, Image_canvasPixelDiff(2, string(out), string(want))))
	if diff > 20 {
		t.Fatalf("the phone frame differs from the reference in %d pixels", diff)
	}
	// Independent spot checks: transparent outside the round corner, the
	// body colour inside it, the outline, the screenshot, the camera pill.
	img, _ := png.Decode(bytes.NewReader(out))
	for _, c := range []struct {
		x, y int
		want color.NRGBA
	}{
		{0, 0, color.NRGBA{}},
		{237, 452, color.NRGBA{}},
		{1, 226, color.NRGBA{90, 90, 100, 255}},
		{6, 226, color.NRGBA{24, 24, 28, 255}},
		{100, 200, color.NRGBA{40 + 90/2, 80 + 190/4, 200, 255}},
		{119, 24, color.NRGBA{0, 0, 0, 255}},
	} {
		if got := nrgbaAt(img, c.x, c.y); got != c.want {
			t.Fatalf("pixel (%d,%d) = %v, want %v", c.x, c.y, got, c.want)
		}
	}
	// The screenshot's corner is cut by its rounded mask: transparent there,
	// so the body colour shows.
	if got := nrgbaAt(img, 15, 15); got != (color.NRGBA{24, 24, 28, 255}) {
		t.Fatalf("masked corner = %v", got)
	}
}

// measure against the font's own tables (advances summed in font units with
// the kern table, then scaled), within 1 px.
func TestCanvas_MeasureMatchesFontTables(t *testing.T) {
	f, err := sfnt.Parse(goregular.TTF)
	if err != nil {
		t.Fatal(err)
	}
	var buf sfnt.Buffer
	upem := fixed.I(int(f.UnitsPerEm()))
	for _, c := range []struct {
		s    string
		size float64
	}{{"Hello, Sky", 32}, {"AVATAR Wy", 48}, {"9:41", 12}} {
		var units fixed.Int26_6
		prev := sfnt.GlyphIndex(0)
		for i, r := range c.s {
			g, _ := f.GlyphIndex(&buf, r)
			if i > 0 {
				k, _ := f.Kern(&buf, prev, g, upem, font.HintingNone)
				units += k
			}
			a, _ := f.GlyphAdvance(&buf, g, upem, font.HintingNone)
			units += a
			prev = g
		}
		wantW := float64(units) / 64 * c.size / float64(f.UnitsPerEm())
		m := Image_canvasMeasure(Image_canvasDefaultFont(nil), c.size, c.s).(map[string]any)
		if math.Abs(float64(m["width"].(int))-wantW) > 1 {
			t.Fatalf("%q at %v: width %v, font tables give %.2f", c.s, c.size, m["width"], wantW)
		}
		met, _ := f.Metrics(&buf, upem, font.HintingNone)
		wantA := float64(met.Ascent) / 64 * c.size / float64(f.UnitsPerEm())
		if math.Abs(float64(m["ascent"].(int))-wantA) > 1 {
			t.Fatalf("%q: ascent %v, font tables give %.2f", c.s, m["ascent"], wantA)
		}
		if m["height"].(int) != m["ascent"].(int)+m["descent"].(int) {
			t.Fatalf("height is not ascent + descent: %v", m)
		}
	}
	two := Image_canvasMeasure(Image_canvasDefaultFont(nil), 20.0, "a\nb").(map[string]any)
	one := Image_canvasMeasure(Image_canvasDefaultFont(nil), 20.0, "a").(map[string]any)
	if two["height"].(int) <= one["height"].(int) {
		t.Fatalf("two lines are not taller: %v %v", two, one)
	}
}

// PNG, JPEG 92, GIF and lossless WebP outputs decode back at the canvas size.
func TestCanvas_EncodeFormats(t *testing.T) {
	c := Image_canvasEllipse(10, 10, 60, 30, 0, pack(200, 30, 30, 255), Image_canvasBlank(97, 53, pack(255, 255, 255, 255)))
	for _, f := range []string{"png", "jpeg", "gif", "webp", "preserve"} {
		out := okBytes(t, forceImageTask(t, Image_canvasEncode(f, 92, c)))
		cfg, format, err := image.DecodeConfig(bytes.NewReader(out))
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		want := f
		if f == "preserve" {
			want = "png"
		}
		if format != want || cfg.Width != 97 || cfg.Height != 53 {
			t.Fatalf("%s: %s %dx%d", f, format, cfg.Width, cfg.Height)
		}
	}
	if _, err := jpeg.Decode(bytes.NewReader(okBytes(t, forceImageTask(t, Image_canvasEncode("jpeg", 92, c))))); err != nil {
		t.Fatal(err)
	}
}

// WebpLossless output decodes with x/image/webp to the same pixels as the
// PNG of the same canvas: a property over random images and drawings.
func TestCanvas_WebpLosslessPixelExactProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(29))
	for i := 0; i < 40; i++ {
		w, h := 1+rng.Intn(90), 1+rng.Intn(90)
		src := image.NewNRGBA(image.Rect(0, 0, w, h))
		for p := range src.Pix {
			src.Pix[p] = uint8(rng.Intn(256))
		}
		for p := 3; p < len(src.Pix); p += 4 {
			if i%3 == 0 {
				src.Pix[p] = 255
			}
		}
		c := Image_canvasFromImage(encPNG(t, src))
		if i%2 == 0 {
			c = Image_canvasEllipse(0, 0, w, h, 2, pack(rng.Intn(256), rng.Intn(256), rng.Intn(256), rng.Intn(256)), c)
		}
		pngOut := okBytes(t, forceImageTask(t, Image_canvasEncode("png", 0, c)))
		webpOut := okBytes(t, forceImageTask(t, Image_canvasEncode("webp", 0, c)))
		a, err := webp.Decode(bytes.NewReader(webpOut))
		if err != nil {
			t.Fatalf("#%d: webp decode: %v", i, err)
		}
		b, _ := png.Decode(bytes.NewReader(pngOut))
		if !bytes.Equal(imageToTestNRGBA(a).Pix, imageToTestNRGBA(b).Pix) {
			t.Fatalf("#%d %dx%d: webp pixels differ from png", i, w, h)
		}
	}
}

func TestCanvas_Errors(t *testing.T) {
	// A missing font is NotFound.
	res := forceImageTask(t, Image_canvasLoadFont(filepath.Join(t.TempDir(), "nope.ttf"), 0))
	if kind := imageErrKind(t, res); kind != "NotFound" {
		t.Fatalf("missing font: %s", kind)
	}
	// A font file loads; a bad one is font_parse; a wrong index is font_index.
	p := filepath.Join(t.TempDir(), "go.ttf")
	if err := os.WriteFile(p, goregular.TTF, 0o644); err != nil {
		t.Fatal(err)
	}
	if m := forceImageTask(t, Image_canvasLoadFont(p, 0)).(SkyResult[any, any]); m.Tag != 0 {
		t.Fatalf("loadFont: %#v", m)
	}
	if code := imageCodeOf(t, Image_canvasFontFromBytes("not a font", 0)); code != imageCodeFontParse {
		t.Fatalf("bad font: %s", code)
	}
	if code := imageCodeOf(t, Image_canvasFontFromBytes(string(goregular.TTF), 1)); code != imageCodeFontIndex {
		t.Fatalf("bad index: %s", code)
	}
	blankW := Image_canvasBlank(10, 10, pack(255, 255, 255, 255))
	// A 200-megapixel paste (a tiny PNG claiming 20000 x 10000) is refused.
	big := Image_canvasPaste(0, 0, false, nil, pngWithSize(t, 20000, 10000), blankW)
	if code := imageDomainCode(t, forceImageTask(t, Image_canvasEncode("png", 0, big))); code != imageCodeTooManyPixels {
		t.Fatalf("200 MP paste: %s", code)
	}
	// A corrupt pasted image.
	if kind := imageErrKind(t, forceImageTask(t, Image_canvasEncode("png", 0,
		Image_canvasPaste(0, 0, false, nil, "junk", blankW)))); kind != "InvalidInput" {
		t.Fatalf("corrupt paste: %s", kind)
	}
	// A colour channel outside 0..255 (packed as -1 by the Sky wrapper).
	if code := imageCodeOf(t, forceImageTask(t, Image_canvasEncode("png", 0,
		Image_canvasRect(0, 0, 5, 5, 0, -1, blankW)))); code != imageCodeColourRange {
		t.Fatalf("colour: %s", code)
	}
	// Canvas size limits.
	if code := imageCodeOf(t, forceImageTask(t, Image_canvasEncode("png", 0,
		Image_canvasBlank(16385, 1, 0)))); code != imageCodeCanvasTooLarge {
		t.Fatalf("side: %s", code)
	}
	if code := imageCodeOf(t, forceImageTask(t, Image_canvasEncode("png", 0,
		Image_canvasResize(2, 12000, 12000, blankW)))); code != imageCodeTooManyPixels {
		t.Fatalf("resize over 100 MP: %s", code)
	}
	// Images of different sizes in pixelDiff.
	if code := imageCodeOf(t, forceImageTask(t, Image_canvasPixelDiff(0, genPNG(t, 2, 2), genPNG(t, 3, 2)))); code != imageCodeSizeMismatch {
		t.Fatalf("pixelDiff sizes: %s", code)
	}
}

func imageCodeOf(t *testing.T, res any) string {
	t.Helper()
	m, ok := res.(SkyResult[any, any])
	if !ok || m.Tag != 1 {
		t.Fatalf("expected Err, got %#v", res)
	}
	df, ok := DomainOf(m.ErrValue)
	if !ok {
		t.Fatalf("no domain: %#v", m.ErrValue)
	}
	return df.Code
}

// Shapes: a fill, an outline with an empty middle, a pie, a line, a circle.
func TestCanvas_Shapes(t *testing.T) {
	red := pack(255, 0, 0, 255)
	c := Image_canvasBlank(100, 100, pack(255, 255, 255, 255))
	c = Image_canvasRect(10, 10, 20, 20, 0, red, c)
	c = Image_canvasRect(50, 10, 30, 30, 3, red, c)
	c = Image_canvasArc(30, 70, 20, 0, 90, 0, red, c)
	c = Image_canvasLine(50, 60, 90, 60, 1, red, c)
	c = Image_canvasEllipse(60, 70, 20, 20, 0, pack(0, 0, 255, 255), c)
	img := canvasPNG(t, c)
	is := func(x, y int, want color.NRGBA) {
		t.Helper()
		if got := nrgbaAt(img, x, y); got != want {
			t.Fatalf("(%d,%d) = %v, want %v", x, y, got, want)
		}
	}
	r, w, b := color.NRGBA{255, 0, 0, 255}, color.NRGBA{255, 255, 255, 255}, color.NRGBA{0, 0, 255, 255}
	is(10, 10, r)
	is(29, 29, r)
	is(30, 30, w)
	is(9, 9, w)
	is(50, 10, r)
	is(52, 25, r)
	is(53, 25, w)
	is(65, 25, w)
	is(79, 39, r)
	is(40, 80, r) // inside the pie (0..90 degrees is the lower right quarter)
	is(20, 60, w) // the upper left quarter is not
	is(70, 60, r)
	is(70, 59, w)
	is(70, 80, b)
	is(61, 71, w) // outside the circle, inside its box
}

// The display list is persistent: two drawings on one base do not see each
// other, and the base is unchanged.
func TestCanvas_PersistentDisplayList(t *testing.T) {
	base := Image_canvasBlank(20, 20, pack(255, 255, 255, 255))
	a := Image_canvasRect(0, 0, 10, 10, 0, pack(255, 0, 0, 255), base)
	b := Image_canvasRect(10, 10, 10, 10, 0, pack(0, 0, 255, 255), base)
	ia, ib, i0 := canvasPNG(t, a), canvasPNG(t, b), canvasPNG(t, base)
	if nrgbaAt(ia, 15, 15) != (color.NRGBA{255, 255, 255, 255}) || nrgbaAt(ib, 5, 5) != (color.NRGBA{255, 255, 255, 255}) ||
		nrgbaAt(i0, 5, 5) != (color.NRGBA{255, 255, 255, 255}) || nrgbaAt(ia, 5, 5) != (color.NRGBA{255, 0, 0, 255}) {
		t.Fatal("drawings share state")
	}
}

// Blur keeps a flat colour flat and spreads an edge; resize changes the size
// with each filter; text draws pixels where it is written.
func TestCanvas_BlurResizeText(t *testing.T) {
	c := Image_canvasBlank(60, 20, pack(255, 255, 255, 255))
	c = Image_canvasRect(30, 0, 30, 20, 0, pack(0, 0, 0, 255), c)
	img := canvasPNG(t, Image_canvasBlur(3.0, c))
	if got := nrgbaAt(img, 2, 10); got != (color.NRGBA{255, 255, 255, 255}) {
		t.Fatalf("flat area changed: %v", got)
	}
	if mid := nrgbaAt(img, 29, 10).R; mid < 60 || mid > 200 {
		t.Fatalf("edge not blurred: %d", mid)
	}
	for f := 0; f < 4; f++ {
		if b := canvasPNG(t, Image_canvasResize(f, 13, 7, c)).Bounds(); b.Dx() != 13 || b.Dy() != 7 {
			t.Fatalf("filter %d: %v", f, b)
		}
	}
	tx := canvasPNG(t, Image_canvasText(Image_canvasDefaultFont(nil), 16.0, pack(0, 0, 0, 255), 2, 2, "Hi",
		Image_canvasBlank(40, 30, pack(255, 255, 255, 255))))
	dark := 0
	for y := 0; y < 30; y++ {
		for x := 0; x < 40; x++ {
			if nrgbaAt(tx, x, y).R < 128 {
				dark++
				if x > 30 || y > 22 {
					t.Fatalf("text pixel at (%d,%d) is outside its measure", x, y)
				}
			}
		}
	}
	if dark < 10 {
		t.Fatalf("text drew %d pixels", dark)
	}
}

// A use case beyond the request: an Open Graph card (1200 x 630) with an
// initials avatar in a round mask and a QR code from Std.Qr, written as
// lossless WebP.
func TestCanvas_OpenGraphCardWithQr(t *testing.T) {
	font := Image_canvasDefaultFont(nil)
	avatar := Image_canvasBlank(160, 160, pack(70, 110, 220, 255))
	m := Image_canvasMeasure(font, 72.0, "AL").(map[string]any)
	avatar = Image_canvasText(font, 72.0, pack(255, 255, 255, 255), (160-m["width"].(int))/2, (160-m["height"].(int))/2, "AL", avatar)
	qrRes := Qr_encodeWith(1, "https://sky-lang.org/").(SkyResult[any, any])
	if qrRes.Tag != 0 {
		t.Fatalf("qr: %#v", qrRes)
	}
	size := Qr_size(qrRes.OkValue).(int)
	qr := Image_canvasBlank((size+8)*6, (size+8)*6, pack(255, 255, 255, 255))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			if Qr_isDark(x, y, qrRes.OkValue).(bool) {
				qr = Image_canvasRect((x+4)*6, (y+4)*6, 6, 6, 0, pack(0, 0, 0, 255), qr)
			}
		}
	}
	card := Image_canvasBlank(1200, 630, pack(250, 250, 252, 255))
	card = Image_canvasRect(0, 0, 1200, 12, 0, pack(70, 110, 220, 255), card)
	card = Image_canvasPasteCanvas(80, 80, true, Image_canvasEllipseMask(160, 160), avatar, card)
	card = Image_canvasText(font, 64.0, pack(20, 20, 30, 255), 80, 300, "Sky on the server", card)
	card = Image_canvasPasteCanvas(1200-80-(size+8)*6, 630-80-(size+8)*6, false, nil, qr, card)
	out := okBytes(t, forceImageTask(t, Image_canvasEncode("webp", 0, card)))
	img, err := webp.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 1200 || b.Dy() != 630 {
		t.Fatalf("card size %v", b)
	}
	// The avatar's corner is cut by the round mask; its centre is blue or white.
	if got := nrgbaAt(img, 82, 82); got != (color.NRGBA{250, 250, 252, 255}) {
		t.Fatalf("avatar corner %v", got)
	}
	if got := nrgbaAt(img, 160, 90); got != (color.NRGBA{70, 110, 220, 255}) {
		t.Fatalf("avatar top %v", got)
	}
	// The QR code's top-left finder pattern is dark at its first module.
	qx, qy := 1200-80-(size+8)*6+4*6, 630-80-(size+8)*6+4*6
	if got := nrgbaAt(img, qx+1, qy+1); got != (color.NRGBA{0, 0, 0, 255}) {
		t.Fatalf("qr finder %v", got)
	}
}

// Each typed twin returns what its erased kernel returns.
func TestCanvas_TypedTwinsMatchErasedKernels(t *testing.T) {
	c := Image_canvasBlank(4, 4, pack(1, 2, 3, 255))
	m := Image_canvasEllipseMask(4, 4)
	f := Image_canvasDefaultFont(nil)
	png1 := genPNG(t, 2, 2)
	pairs := []struct {
		name   string
		erased any
		typed  SkyADT
	}{
		{"blank", Image_canvasBlank(4, 4, 7), Image_canvasBlankT(4, 4, 7)},
		{"fromImage", Image_canvasFromImage(png1), Image_canvasFromImageT(png1)},
		{"rect", Image_canvasRect(0, 0, 2, 2, 0, 7, c), Image_canvasRectT(0, 0, 2, 2, 0, 7, c)},
		{"roundedRect", Image_canvasRoundedRect(0, 0, 2, 2, 1, 0, 7, c), Image_canvasRoundedRectT(0, 0, 2, 2, 1, 0, 7, c)},
		{"ellipse", Image_canvasEllipse(0, 0, 2, 2, 0, 7, c), Image_canvasEllipseT(0, 0, 2, 2, 0, 7, c)},
		{"arc", Image_canvasArc(1, 1, 1, 0.0, 90.0, 0, 7, c), Image_canvasArcT(1, 1, 1, 0.0, 90.0, 0, 7, c)},
		{"line", Image_canvasLine(0, 0, 3, 3, 1, 7, c), Image_canvasLineT(0, 0, 3, 3, 1, 7, c)},
		{"paste", Image_canvasPaste(0, 0, true, m, png1, c), Image_canvasPasteT(0, 0, true, m, png1, c)},
		{"pasteCanvas", Image_canvasPasteCanvas(0, 0, false, m, c, c), Image_canvasPasteCanvasT(0, 0, false, m, c, c)},
		{"blur", Image_canvasBlur(1.0, c), Image_canvasBlurT(1.0, c)},
		{"resize", Image_canvasResize(2, 3, 3, c), Image_canvasResizeT(2, 3, 3, c)},
		{"text", Image_canvasText(f, 8.0, 7, 0, 0, "a", c), Image_canvasTextT(f, 8.0, 7, 0, 0, "a", c)},
	}
	for _, p := range pairs {
		a := okBytes(t, forceImageTask(t, Image_canvasEncode("png", 0, p.erased)))
		b := okBytes(t, forceImageTask(t, Image_canvasEncode("png", 0, p.typed)))
		if !bytes.Equal(a, b) {
			t.Fatalf("%s: the typed twin draws something else", p.name)
		}
	}
	for name, pair := range map[string][2]any{
		"roundedMask": {Image_canvasRoundedMask(3, 3, 1), Image_canvasRoundedMaskT(3, 3, 1)},
		"ellipseMask": {Image_canvasEllipseMask(3, 3), Image_canvasEllipseMaskT(3, 3)},
		"alphaMask":   {Image_canvasAlphaMask(png1), Image_canvasAlphaMaskT(png1)},
		"defaultFont": {Image_canvasDefaultFont(nil), Image_canvasDefaultFontT(nil)},
	} {
		a, b := pair[0].(SkyADT), pair[1].(SkyADT)
		if a.SkyName != b.SkyName || len(a.Fields) != 1 || len(b.Fields) != 1 {
			t.Fatalf("%s: twin shape differs", name)
		}
	}
}
