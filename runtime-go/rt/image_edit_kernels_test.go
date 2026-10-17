//go:build !js

package rt

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func encPNG(t *testing.T, img image.Image) string {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.String()
}

func encJPEG(t *testing.T, img image.Image) string {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.String()
}

func fill(img *image.NRGBA, r image.Rectangle, c color.NRGBA) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
}

func decodeNRGBA(t *testing.T, b []byte) (*image.NRGBA, string) {
	t.Helper()
	img, format, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("result does not decode: %v", err)
	}
	n := image.NewNRGBA(image.Rect(0, 0, img.Bounds().Dx(), img.Bounds().Dy()))
	for y := 0; y < n.Rect.Dy(); y++ {
		for x := 0; x < n.Rect.Dx(); x++ {
			n.SetNRGBA(x, y, color.NRGBAModel.Convert(img.At(img.Bounds().Min.X+x, img.Bounds().Min.Y+y)).(color.NRGBA))
		}
	}
	return n, format
}

func imageErrKind(t *testing.T, res any) string {
	t.Helper()
	m, ok := res.(SkyResult[any, any])
	if !ok || m.Tag != 1 {
		t.Fatalf("expected Err, got %#v", res)
	}
	return errKindOf(m.ErrValue)
}

var (
	white = color.NRGBA{255, 255, 255, 255}
	black = color.NRGBA{0, 0, 0, 255}
)

func whiteWithRect(w, h int, r image.Rectangle) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	fill(img, img.Rect, white)
	fill(img, r, black)
	return img
}

// 1. White margins around a black 20x10 rectangle at (30,40) on a 100x100
// JPEG: trim to the rectangle, then with 4 px of padding around it.
func TestImageTrim_WhiteMargins(t *testing.T) {
	src := encJPEG(t, whiteWithRect(100, 100, image.Rect(30, 40, 50, 50)))
	w, h, _ := decodeDims(t, okBytes(t, forceImageTask(t, Image_trim("png", 0, 245, 0, src))))
	if w != 20 || h != 10 {
		t.Fatalf("trim gave %dx%d, want 20x10", w, h)
	}
	w, h, _ = decodeDims(t, okBytes(t, forceImageTask(t, Image_trim("png", 0, 245, 4, src))))
	if w != 28 || h != 18 {
		t.Fatalf("trim with padding 4 gave %dx%d, want 28x18", w, h)
	}
	// Padding is clamped to the image.
	w, h, _ = decodeDims(t, okBytes(t, forceImageTask(t, Image_trim("png", 0, 245, 500, src))))
	if w != 100 || h != 100 {
		t.Fatalf("a large padding must clamp to the image, got %dx%d", w, h)
	}
}

// 2. Transparent margins: the opaque 10x10 square is kept, with its alpha.
func TestImageTrim_TransparentMarginsKeepAlpha(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 100, 100)) // all transparent
	fill(img, image.Rect(45, 45, 55, 55), color.NRGBA{255, 0, 0, 255})
	img.SetNRGBA(46, 46, color.NRGBA{255, 0, 0, 128}) // a partly transparent pixel inside
	out := okBytes(t, forceImageTask(t, Image_trim("png", 0, 245, 0, encPNG(t, img))))
	n, format := decodeNRGBA(t, out)
	if format != "png" || n.Rect.Dx() != 10 || n.Rect.Dy() != 10 {
		t.Fatalf("got %s %dx%d, want png 10x10", format, n.Rect.Dx(), n.Rect.Dy())
	}
	if a := n.NRGBAAt(1, 1).A; a != 128 {
		t.Fatalf("alpha was not kept: %d, want 128", a)
	}
}

// 3. Nothing to trim: a dark edge row keeps the dimensions.
func TestImageTrim_NothingToTrimKeepsDimensions(t *testing.T) {
	img := whiteWithRect(40, 30, image.Rect(0, 0, 40, 1))
	fill(img, image.Rect(0, 29, 40, 30), black)
	fill(img, image.Rect(0, 0, 1, 30), black)
	fill(img, image.Rect(39, 0, 40, 30), black)
	w, h, format := decodeDims(t, okBytes(t, forceImageTask(t, Image_trim("preserve", 0, 245, 0, encPNG(t, img)))))
	if w != 40 || h != 30 || format != "png" {
		t.Fatalf("got %s %dx%d, want png 40x30", format, w, h)
	}
}

// 4. All margin: an all-white image has nothing left to trim to.
func TestImageTrim_AllMarginIsErr(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 20, 20))
	fill(img, img.Rect, white)
	if k := imageErrKind(t, forceImageTask(t, Image_trim("png", 0, 245, 0, encPNG(t, img)))); k != "InvalidInput" {
		t.Fatalf("kind %s, want InvalidInput", k)
	}
	if k := imageErrKind(t, forceImageTask(t, Image_trim("png", 0, 300, 0, encPNG(t, img)))); k != "InvalidInput" {
		t.Fatalf("a threshold above 255: kind %s", k)
	}
	if k := imageErrKind(t, forceImageTask(t, Image_trim("png", 0, 200, -1, encPNG(t, img)))); k != "InvalidInput" {
		t.Fatalf("a negative padding: kind %s", k)
	}
}

func toAlphaOf(t *testing.T, r, g, b int, img image.Image) *image.NRGBA {
	t.Helper()
	out := okBytes(t, forceImageTask(t, Image_toAlpha(r, g, b, encPNG(t, img))))
	n, format := decodeNRGBA(t, out)
	if format != "png" {
		t.Fatalf("toAlpha must return PNG, got %s", format)
	}
	return n
}

func near(a, b uint8, d int) bool {
	x := int(a) - int(b)
	return x >= -d && x <= d
}

// 5. White, black and grey 128 against white.
func TestImageWhiteToAlpha_Pixels(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 3, 1))
	img.SetNRGBA(0, 0, white)
	img.SetNRGBA(1, 0, black)
	img.SetNRGBA(2, 0, color.NRGBA{128, 128, 128, 255})
	n := toAlphaOf(t, 255, 255, 255, img)
	if a := n.NRGBAAt(0, 0).A; a != 0 {
		t.Fatalf("white: alpha %d, want 0", a)
	}
	if c := n.NRGBAAt(1, 0); c != black {
		t.Fatalf("black: %v, want (0,0,0,255)", c)
	}
	c := n.NRGBAAt(2, 0)
	if c.R != 0 || c.G != 0 || c.B != 0 || (c.A != 127 && c.A != 128) {
		t.Fatalf("grey 128: %v, want (0,0,0,127 or 128)", c)
	}
}

// 6. A colour keeps its look: laid back over white it gives the original
// within one step, and a 50% blend with white has about half its coverage
// and the same colour. With the white-key formula a = 255 - min(r,g,b), navy
// (0x0b,0x24,0x47) itself has alpha 244, not 255: its lightest channel is
// 0x0b above black, so that much of it is white.
func TestImageWhiteToAlpha_ColourKept(t *testing.T) {
	navy := color.NRGBA{0x0b, 0x24, 0x47, 255}
	blend := color.NRGBA{(0x0b + 255 + 1) / 2, (0x24 + 255 + 1) / 2, (0x47 + 255 + 1) / 2, 255}
	img := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	img.SetNRGBA(0, 0, navy)
	img.SetNRGBA(1, 0, blend)
	n := toAlphaOf(t, 255, 255, 255, img)
	over := func(c color.NRGBA) [3]uint8 {
		a := float64(c.A) / 255
		f := func(v uint8) uint8 { return uint8(float64(v)*a + 255*(1-a) + 0.5) }
		return [3]uint8{f(c.R), f(c.G), f(c.B)}
	}
	got := over(n.NRGBAAt(0, 0))
	for i, want := range []uint8{navy.R, navy.G, navy.B} {
		if !near(got[i], want, 1) {
			t.Fatalf("navy over white is %v, want %v", got, navy)
		}
	}
	if a := n.NRGBAAt(0, 0).A; a != 244 {
		t.Fatalf("navy alpha %d, want 244 (255 - 0x0b)", a)
	}
	nb, nn := n.NRGBAAt(1, 0), n.NRGBAAt(0, 0)
	if !near(nb.A, 122, 1) {
		t.Fatalf("the 50%% blend has alpha %d, want about 122 (half of 244)", nb.A)
	}
	if !near(nb.R, nn.R, 2) || !near(nb.G, nn.G, 2) || !near(nb.B, nn.B, 2) {
		t.Fatalf("the blend's colour %v differs from navy's %v", nb, nn)
	}
}

// 7. JPEG in, PNG out, and dimensions agree.
func TestImageWhiteToAlpha_JpegInPngOut(t *testing.T) {
	src := encJPEG(t, whiteWithRect(64, 48, image.Rect(10, 10, 30, 20)))
	out := forceImageTask(t, Image_toAlpha(255, 255, 255, src))
	w, h, format := decodeDims(t, okBytes(t, out))
	dims := forceImageTask(t, Image_dimensions(string(okBytes(t, out)))).(SkyResult[any, any]).OkValue.(map[string]any)
	if format != "png" || w != 64 || h != 48 || dims["width"] != 64 || dims["height"] != 48 {
		t.Fatalf("got %s %dx%d (dimensions %v), want png 64x48", format, w, h, dims)
	}
}

// 8. trim, then whiteToAlpha, then resizeToFit Png on a 1024x1024 JPEG logo
// with wide margins: a PNG within bounds, with alpha, at the logo's aspect.
func TestImageChain_TrimWhiteToAlphaResize(t *testing.T) {
	logo := whiteWithRect(1024, 1024, image.Rect(212, 362, 812, 662)) // 600x300
	trimmed := okBytes(t, forceImageTask(t, Image_trim("png", 0, 245, 0, encJPEG(t, logo))))
	clear := okBytes(t, forceImageTask(t, Image_toAlpha(255, 255, 255, string(trimmed))))
	out := okBytes(t, forceImageTask(t, Image_resize("png", 0, 200, 200, string(clear))))
	n, format := decodeNRGBA(t, out)
	w, h := n.Rect.Dx(), n.Rect.Dy()
	if format != "png" || w > 200 || h > 200 {
		t.Fatalf("got %s %dx%d, want png within 200x200", format, w, h)
	}
	if want := w / 2; h < want-1 || h > want+1 {
		t.Fatalf("aspect lost: %dx%d, want 2:1 within 1 px", w, h)
	}
	if n.NRGBAAt(w/2, h/2).A == 0 {
		t.Fatalf("the logo's centre became transparent")
	}
}

// 9. Bad bytes are an Err for every entry; crop clamps, refuses an empty
// area and refuses negative sizes.
func TestImageEdit_BadBytesAndCrop(t *testing.T) {
	for name, res := range map[string]any{
		"trim":    forceImageTask(t, Image_trim("png", 0, 245, 0, "not an image")),
		"toAlpha": forceImageTask(t, Image_toAlpha(255, 255, 255, "not an image")),
		"crop":    forceImageTask(t, Image_crop("png", 0, 0, 0, 1, 1, "not an image")),
	} {
		if k := imageErrKind(t, res); k != "InvalidInput" {
			t.Fatalf("%s on bad bytes: kind %s", name, k)
		}
	}
	src := encPNG(t, whiteWithRect(50, 40, image.Rect(0, 0, 1, 1)))
	w, h, _ := decodeDims(t, okBytes(t, forceImageTask(t, Image_crop("png", 0, 10, 5, 20, 10, src))))
	if w != 20 || h != 10 {
		t.Fatalf("crop gave %dx%d, want 20x10", w, h)
	}
	w, h, _ = decodeDims(t, okBytes(t, forceImageTask(t, Image_crop("png", 0, 40, 30, 100, 100, src))))
	if w != 10 || h != 10 {
		t.Fatalf("crop past the edge must clamp: %dx%d, want 10x10", w, h)
	}
	if k := imageErrKind(t, forceImageTask(t, Image_crop("png", 0, 60, 0, 10, 10, src))); k != "InvalidInput" {
		t.Fatalf("crop outside the image: kind %s", k)
	}
	if k := imageErrKind(t, forceImageTask(t, Image_crop("png", 0, 0, 0, 0, 10, src))); k != "InvalidInput" {
		t.Fatalf("an empty crop: kind %s", k)
	}
	if k := imageErrKind(t, forceImageTask(t, Image_crop("png", 0, 0, 0, -5, 10, src))); k != "InvalidInput" {
		t.Fatalf("a negative width: kind %s", k)
	}
}

// toAlpha with black as the key: white text on black comes back white, with
// its coverage as alpha; black itself becomes clear.
func TestImageToAlpha_BlackKey(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 3, 1))
	img.SetNRGBA(0, 0, black)
	img.SetNRGBA(1, 0, white)
	img.SetNRGBA(2, 0, color.NRGBA{128, 128, 128, 255}) // half-covered white
	n := toAlphaOf(t, 0, 0, 0, img)
	if a := n.NRGBAAt(0, 0).A; a != 0 {
		t.Fatalf("black key: black has alpha %d, want 0", a)
	}
	if c := n.NRGBAAt(1, 0); c != white {
		t.Fatalf("white text on black: %v, want white at 255", c)
	}
	c := n.NRGBAAt(2, 0)
	if c.R != 255 || c.G != 255 || c.B != 255 || !near(c.A, 128, 1) {
		t.Fatalf("grey on black: %v, want white at about 128", c)
	}
}

// toAlpha with a mid colour key: the key is clear, a colour far from it is
// opaque, and a 50% blend toward white has about half coverage.
func TestImageToAlpha_MidKey(t *testing.T) {
	key := color.NRGBA{0, 128, 255, 255}
	far := color.NRGBA{255, 128, 255, 255}  // red channel at the far end
	half := color.NRGBA{128, 128, 255, 255} // half way from the key to `far`
	img := image.NewNRGBA(image.Rect(0, 0, 3, 1))
	img.SetNRGBA(0, 0, key)
	img.SetNRGBA(1, 0, far)
	img.SetNRGBA(2, 0, half)
	n := toAlphaOf(t, 0, 128, 255, img)
	if a := n.NRGBAAt(0, 0).A; a != 0 {
		t.Fatalf("the key pixel has alpha %d, want 0", a)
	}
	if c := n.NRGBAAt(1, 0); c.A != 255 || c.R != 255 {
		t.Fatalf("a pixel far from the key: %v, want opaque", c)
	}
	if c := n.NRGBAAt(2, 0); !near(c.A, 128, 1) || c.R != 255 {
		t.Fatalf("a 50%% blend: %v, want red 255 at about alpha 128", c)
	}
}

func TestImageToAlpha_ChannelOutOfRange(t *testing.T) {
	src := encPNG(t, whiteWithRect(4, 4, image.Rect(0, 0, 1, 1)))
	for _, k := range [][3]int{{256, 0, 0}, {0, -1, 0}, {0, 0, 300}} {
		if kind := imageErrKind(t, forceImageTask(t, Image_toAlpha(k[0], k[1], k[2], src))); kind != "InvalidInput" {
			t.Fatalf("key %v: kind %s, want InvalidInput", k, kind)
		}
	}
}

// The white key and the brief's closed form agree on every grey level and
// on colours: a = 255 - min(r,g,b), c' = 255 - (255 - c) * 255 / a.
func TestImageWhiteToAlpha_MatchesClosedForm(t *testing.T) {
	k := [3]float64{255, 255, 255}
	for _, c := range []color.NRGBA{{0x0b, 0x24, 0x47, 255}, {200, 10, 90, 255}, {17, 17, 17, 255}, {254, 255, 253, 255}} {
		got := colourToAlpha(c, k)
		mn := c.R
		if c.G < mn {
			mn = c.G
		}
		if c.B < mn {
			mn = c.B
		}
		a := 255 - int(mn)
		f := func(v uint8) uint8 { return clampByte(255 - float64(255-int(v))*255/float64(a)) }
		want := color.NRGBA{f(c.R), f(c.G), f(c.B), uint8(a)}
		if got != want {
			t.Fatalf("%v: got %v, want %v", c, got, want)
		}
	}
}
