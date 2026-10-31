//go:build !js

package rt

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"
	"time"

	"golang.org/x/image/webp"
)

var (
	gRed   = color.RGBA{255, 0, 0, 255}
	gBlue  = color.RGBA{0, 0, 255, 255}
	gGreen = color.RGBA{0, 255, 0, 255}
	gWhite = color.RGBA{255, 255, 255, 255}
)

// gifTestPalette has a transparent entry at index 0.
var gifTestPalette = color.Palette{color.RGBA{}, gRed, gBlue, gGreen, gWhite}

func gifFrame(r image.Rectangle, idx uint8) *image.Paletted {
	p := image.NewPaletted(r, gifTestPalette)
	for i := range p.Pix {
		p.Pix[i] = idx
	}
	return p
}

func encGIF(t *testing.T, g *gif.GIF) string {
	t.Helper()
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, g); err != nil {
		t.Fatalf("encode source gif: %v", err)
	}
	return buf.String()
}

// stripedGIF is an n-frame w x h animation: frame i is a vertical bar moving
// across a white field, each frame 4 + i hundredths long.
func stripedGIF(t *testing.T, n, w, h int) (string, []int) {
	t.Helper()
	g := &gif.GIF{LoopCount: 0}
	for i := 0; i < n; i++ {
		f := gifFrame(image.Rect(0, 0, w, h), 4)
		x := i % w
		for y := 0; y < h; y++ {
			f.SetColorIndex(x, y, 1)
		}
		g.Image = append(g.Image, f)
		g.Delay = append(g.Delay, 4+i)
		g.Disposal = append(g.Disposal, gif.DisposalNone)
	}
	return encGIF(t, g), g.Delay
}

func decodeAllGIF(t *testing.T, b []byte) *gif.GIF {
	t.Helper()
	g, err := gif.DecodeAll(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("output is not a GIF: %v", err)
	}
	return g
}

// refCompose is an independent composer (the GIF89a rules) for the tests.
func refCompose(g *gif.GIF) []*image.NRGBA {
	screen := image.NewNRGBA(image.Rect(0, 0, g.Config.Width, g.Config.Height))
	var out []*image.NRGBA
	for i, f := range g.Image {
		before := image.NewNRGBA(screen.Rect)
		copy(before.Pix, screen.Pix)
		for y := f.Rect.Min.Y; y < f.Rect.Max.Y; y++ {
			for x := f.Rect.Min.X; x < f.Rect.Max.X; x++ {
				c := color.NRGBAModel.Convert(f.At(x, y)).(color.NRGBA)
				if c.A != 0 {
					screen.SetNRGBA(x, y, c)
				}
			}
		}
		shot := image.NewNRGBA(screen.Rect)
		copy(shot.Pix, screen.Pix)
		out = append(out, shot)
		switch g.Disposal[i] {
		case gif.DisposalBackground:
			for y := f.Rect.Min.Y; y < f.Rect.Max.Y; y++ {
				for x := f.Rect.Min.X; x < f.Rect.Max.X; x++ {
					screen.SetNRGBA(x, y, color.NRGBA{})
				}
			}
		case gif.DisposalPrevious:
			screen = before
		}
	}
	return out
}

func imageDomainCode(t *testing.T, res any) string {
	t.Helper()
	m, ok := res.(SkyResult[any, any])
	if !ok || m.Tag != 1 {
		t.Fatalf("expected Err, got %#v", res)
	}
	df, ok := DomainOf(m.ErrValue)
	if !ok || df.Domain != "image" {
		t.Fatalf("expected an Error in the domain \"image\", got %#v", m.ErrValue)
	}
	if kind := errKindOf(m.ErrValue); kind != "InvalidInput" {
		t.Fatalf("kind %s, want InvalidInput", kind)
	}
	if _, ok := df.Attr("limit"); !ok {
		t.Fatalf("the error does not name its limit: %#v", df)
	}
	return df.Code
}

func checkTiming(t *testing.T, name string, out *gif.GIF, n int, delays []int, loop int) {
	t.Helper()
	if len(out.Image) != n {
		t.Fatalf("%s: %d frames, want %d", name, len(out.Image), n)
	}
	for i := range delays {
		if out.Delay[i] != delays[i] {
			t.Fatalf("%s: frame %d delay %d, want %d", name, i, out.Delay[i], delays[i])
		}
	}
	if out.LoopCount != loop {
		t.Fatalf("%s: loop count %d, want %d", name, out.LoopCount, loop)
	}
}

// A 2-frame and a 60-frame GIF keep every frame, the delays and the loop
// count through resizeToFit Preserve, at the requested size.
func TestImageGif_ResizeKeepsAnimation(t *testing.T) {
	for _, n := range []int{2, 60} {
		src, delays := stripedGIF(t, n, 80, 40)
		out := okBytes(t, forceImageTask(t, Image_resize("preserve", 0, 40, 40, src)))
		if !bytes.HasPrefix(out, []byte("GIF89a")) {
			t.Fatalf("%d frames: output is not a GIF: %q", n, out[:6])
		}
		g := decodeAllGIF(t, out)
		checkTiming(t, "resize", g, n, delays, 0)
		if g.Config.Width != 40 || g.Config.Height != 20 {
			t.Fatalf("%d frames: size %dx%d, want 40x20", n, g.Config.Width, g.Config.Height)
		}
		// The explicit Gif format gives the same.
		g2 := decodeAllGIF(t, okBytes(t, forceImageTask(t, Image_resize("gif", 0, 40, 40, src))))
		checkTiming(t, "resize gif", g2, n, delays, 0)
		// frames counts them.
		if got := okInt(t, forceImageTask(t, Image_frames(src))); got != n {
			t.Fatalf("frames = %d, want %d", got, n)
		}
	}
}

func okInt(t *testing.T, res any) int {
	t.Helper()
	m, ok := res.(SkyResult[any, any])
	if !ok || m.Tag != 0 {
		t.Fatalf("expected Ok, got %#v", res)
	}
	return m.OkValue.(int)
}

// Partial frames with offsets and "restore previous" disposal: the output's
// composed frames equal the source's composed frames, pixel for pixel.
func TestImageGif_PartialFramesRestorePrevious(t *testing.T) {
	src := &gif.GIF{
		Image: []*image.Paletted{
			gifFrame(image.Rect(0, 0, 12, 10), 1), // red field
			gifFrame(image.Rect(2, 2, 6, 6), 2),   // blue square, then restore
			gifFrame(image.Rect(5, 5, 9, 8), 3),   // green, kept
			gifFrame(image.Rect(1, 6, 4, 9), 2),   // blue, background
			gifFrame(image.Rect(8, 1, 11, 4), 0),  // all transparent
			gifFrame(image.Rect(0, 0, 3, 3), 4),   // white corner
		},
		Delay:    []int{10, 20, 30, 40, 50, 60},
		Disposal: []byte{gif.DisposalNone, gif.DisposalPrevious, gif.DisposalNone, gif.DisposalBackground, gif.DisposalNone, gif.DisposalPrevious},
		Config:   image.Config{Width: 12, Height: 10, ColorModel: gifTestPalette},
	}
	in := encGIF(t, src)
	want := refCompose(decodeAllGIF(t, []byte(in)))
	for _, name := range []string{"crop", "resize"} {
		var res any
		if name == "crop" {
			res = forceImageTask(t, Image_crop("preserve", 0, 0, 0, 12, 10, in))
		} else {
			res = forceImageTask(t, Image_resize("gif", 0, 100, 100, in))
		}
		out := decodeAllGIF(t, okBytes(t, res))
		checkTiming(t, name, out, 6, src.Delay, 0)
		for i, d := range []byte{gif.DisposalNone, gif.DisposalPrevious, gif.DisposalNone, gif.DisposalBackground, gif.DisposalNone, gif.DisposalPrevious} {
			if out.Disposal[i] != d {
				t.Fatalf("%s: frame %d disposal %d, want %d", name, i, out.Disposal[i], d)
			}
		}
		got := refCompose(out)
		for i := range want {
			if !bytes.Equal(got[i].Pix, want[i].Pix) {
				t.Fatalf("%s: composed frame %d differs from the source's", name, i)
			}
		}
	}
	// Spot checks of the composition itself: frame 2 shows red where the
	// blue square was (restored) and green at (6,6).
	at := func(i, x, y int) color.NRGBA { return want[i].NRGBAAt(x, y) }
	if at(1, 3, 3) != (color.NRGBA{0, 0, 255, 255}) || at(2, 3, 3) != (color.NRGBA{255, 0, 0, 255}) ||
		at(2, 6, 6) != (color.NRGBA{0, 255, 0, 255}) || at(4, 2, 7) != (color.NRGBA{}) {
		t.Fatalf("reference composition is wrong")
	}
}

// A crop of an offset region of the composed frames.
func TestImageGif_CropOffsetsComposedFrames(t *testing.T) {
	in, _ := stripedGIF(t, 5, 20, 10)
	out := decodeAllGIF(t, okBytes(t, forceImageTask(t, Image_crop("gif", 0, 2, 0, 3, 10, in))))
	if len(out.Image) != 5 || out.Config.Width != 3 || out.Config.Height != 10 {
		t.Fatalf("crop: %d frames %dx%d", len(out.Image), out.Config.Width, out.Config.Height)
	}
	frames := refCompose(out)
	// The bar is at x = i; columns 2..4 are cropped, so frame 2 has red at 0.
	if frames[2].NRGBAAt(0, 5) != (color.NRGBA{255, 0, 0, 255}) || frames[0].NRGBAAt(0, 5) != (color.NRGBA{255, 255, 255, 255}) {
		t.Fatalf("crop: wrong pixels")
	}
}

// gifBlocks builds a GIF by hand: a screen of w x h and n frames of fw x fh,
// each a few bytes of LZW. No decoder runs on it before the refusal.
func gifBlocks(w, h, fw, fh, n int) string {
	var b bytes.Buffer
	b.WriteString("GIF89a")
	b.Write([]byte{byte(w), byte(w >> 8), byte(h), byte(h >> 8), 0x80, 0, 0})
	b.Write([]byte{0, 0, 0, 255, 255, 255}) // 2-colour global table
	b.Write([]byte{0x21, 0xFF, 11})
	b.WriteString("NETSCAPE2.0")
	b.Write([]byte{3, 1, 0, 0, 0})
	frame := []byte{0x21, 0xF9, 4, 0, 2, 0, 0, 0,
		0x2C, 0, 0, 0, 0, byte(fw), byte(fw >> 8), byte(fh), byte(fh >> 8), 0,
		2, 2, 0x4C, 0x01, 0}
	for i := 0; i < n; i++ {
		b.Write(frame)
	}
	b.WriteByte(0x3B)
	return b.String()
}

// The 1x1 GIF of 100,000 frames and the 5000x5000 GIF of 50 frames are
// refused with the limit error, quickly, and nothing is decoded.
func TestImageGif_BombsRefusedBeforeDecode(t *testing.T) {
	cases := []struct {
		name, in, code string
	}{
		{"1x1x100000", gifBlocks(1, 1, 1, 1, 100000), imageCodeTooManyFrames},
		{"5000x5000x50", gifBlocks(5000, 5000, 5000, 5000, 50), imageCodeTooManyFramePixels},
	}
	for _, c := range cases {
		for op, res := range map[string]func() any{
			"resize":       func() any { return forceImageTask(t, Image_resize("preserve", 0, 100, 100, c.in)) },
			"crop":         func() any { return forceImageTask(t, Image_crop("gif", 0, 0, 0, 10, 10, c.in)) },
			"trim":         func() any { return forceImageTask(t, Image_trim("preserve", 0, 245, 0, c.in)) },
			"toAlpha":      func() any { return forceImageTask(t, Image_toAlpha(255, 255, 255, c.in)) },
			"reduceFrames": func() any { return forceImageTask(t, Image_reduceFrames(2, c.in)) },
		} {
			start := time.Now()
			if code := imageDomainCode(t, res()); code != c.code {
				t.Fatalf("%s %s: code %s, want %s", c.name, op, code, c.code)
			}
			if d := time.Since(start); d > 2*time.Second {
				t.Fatalf("%s %s: took %v", c.name, op, d)
			}
		}
	}
	// frames still counts the 1x1 bomb (a scan, no decode).
	if got := okInt(t, forceImageTask(t, Image_frames(cases[0].in))); got != 100000 {
		t.Fatalf("frames = %d, want 100000", got)
	}
}

// A still GIF: Preserve keeps it a GIF of one frame; Jpeg and Png give the
// same pixels as the plain first-frame decode always did.
func TestImageGif_StillGif(t *testing.T) {
	still := image.NewPaletted(image.Rect(0, 0, 30, 20), gifTestPalette)
	for y := 0; y < 20; y++ {
		for x := 0; x < 30; x++ {
			still.Pix[y*still.Stride+x] = uint8(1 + (x+y)%4)
		}
	}
	var buf bytes.Buffer
	if err := gif.Encode(&buf, still, nil); err != nil {
		t.Fatal(err)
	}
	in := buf.String()
	g := decodeAllGIF(t, okBytes(t, forceImageTask(t, Image_resize("preserve", 0, 100, 100, in))))
	if len(g.Image) != 1 || g.Config.Width != 30 {
		t.Fatalf("still gif: %d frames %dx%d", len(g.Image), g.Config.Width, g.Config.Height)
	}
	if got := refCompose(g)[0]; !bytes.Equal(got.Pix, imageToTestNRGBA(still).Pix) {
		t.Fatalf("still gif: pixels changed")
	}
	pngOut := okBytes(t, forceImageTask(t, Image_resize("png", 0, 100, 100, in)))
	dec, err := png.Decode(bytes.NewReader(pngOut))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(imageToTestNRGBA(dec).Pix, imageToTestNRGBA(still).Pix) {
		t.Fatalf("png output of a still gif differs from its frame")
	}
	jpgOut := okBytes(t, forceImageTask(t, Image_resize("jpeg", 90, 15, 15, in)))
	if _, err := jpeg.Decode(bytes.NewReader(jpgOut)); err != nil {
		t.Fatalf("jpeg output: %v", err)
	}
	if got := okInt(t, forceImageTask(t, Image_frames(in))); got != 1 {
		t.Fatalf("frames of a still gif = %d", got)
	}
}

func imageToTestNRGBA(img image.Image) *image.NRGBA {
	b := img.Bounds()
	n := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			n.SetNRGBA(x, y, color.NRGBAModel.Convert(img.At(b.Min.X+x, b.Min.Y+y)).(color.NRGBA))
		}
	}
	return n
}

// gifExtensionLabels walks a GIF's blocks and lists its extension labels and
// application identifiers.
func gifExtensionLabels(t *testing.T, b []byte) []string {
	t.Helper()
	var labels []string
	p := 13
	if b[10]&0x80 != 0 {
		p += 3 * (1 << (int(b[10]&7) + 1))
	}
	skip := func(p int) int {
		for b[p] != 0 {
			p += int(b[p]) + 1
		}
		return p + 1
	}
	for p < len(b) {
		switch b[p] {
		case 0x3B:
			return labels
		case 0x21:
			l := b[p+1]
			name := string([]byte{'x', "0123456789abcdef"[l>>4], "0123456789abcdef"[l&15]})
			if l == 0xFF {
				name = string(b[p+3 : p+14])
			}
			labels = append(labels, name)
			p = skip(p + 2)
		case 0x2C:
			packed := b[p+9]
			p += 10
			if packed&0x80 != 0 {
				p += 3 * (1 << (int(packed&7) + 1))
			}
			p = skip(p + 1)
		default:
			t.Fatalf("bad block 0x%x at %d", b[p], p)
		}
	}
	return labels
}

// A GIF with a comment extension and an unknown application extension: the
// output has neither, only the NETSCAPE loop and the graphic controls.
func TestImageGif_CommentExtensionDropped(t *testing.T) {
	in, _ := stripedGIF(t, 3, 10, 10)
	// Insert a comment and an application extension after the logical screen.
	b := []byte(in)
	hdr := 13
	if b[10]&0x80 != 0 {
		hdr += 3 * (1 << (int(b[10]&7) + 1))
	}
	var ext bytes.Buffer
	ext.Write([]byte{0x21, 0xFE, 12})
	ext.WriteString("secret notes")
	ext.Write([]byte{0, 0x21, 0xFF, 11})
	ext.WriteString("XMP DataXMP")
	ext.Write([]byte{4, 'g', 'p', 's', '!', 0})
	withExt := append(append(append([]byte{}, b[:hdr]...), ext.Bytes()...), b[hdr:]...)
	labels := gifExtensionLabels(t, withExt)
	if labels[0] != "xfe" {
		t.Fatalf("fixture has no comment: %v", labels)
	}
	out := okBytes(t, forceImageTask(t, Image_resize("preserve", 0, 10, 10, string(withExt))))
	for _, l := range gifExtensionLabels(t, out) {
		if l != "xf9" && l != "NETSCAPE2.0" {
			t.Fatalf("output keeps the extension %q", l)
		}
	}
	if bytes.Contains(out, []byte("secret notes")) || bytes.Contains(out, []byte("XMP")) {
		t.Fatalf("output keeps the extension text")
	}
	if g := decodeAllGIF(t, out); len(g.Image) != 3 {
		t.Fatalf("frames %d", len(g.Image))
	}
}

// reduceFrames keeps 1 frame in n and adds the dropped delays to the kept one.
func TestImageGif_ReduceFrames(t *testing.T) {
	in, _ := stripedGIF(t, 7, 10, 4) // delays 4..10
	g := decodeAllGIF(t, okBytes(t, forceImageTask(t, Image_reduceFrames(3, in))))
	checkTiming(t, "reduceFrames 3", g, 3, []int{4 + 5 + 6, 7 + 8 + 9, 10}, 0)
	src := refCompose(decodeAllGIF(t, []byte(in)))
	got := refCompose(g)
	for i, j := range []int{0, 3, 6} {
		if !bytes.Equal(got[i].Pix, src[j].Pix) {
			t.Fatalf("kept frame %d is not source frame %d", i, j)
		}
	}
	// n = 1 keeps every frame; a still image comes back unchanged; n = 0 is Err.
	if g := decodeAllGIF(t, okBytes(t, forceImageTask(t, Image_reduceFrames(1, in)))); len(g.Image) != 7 {
		t.Fatalf("n=1: %d frames", len(g.Image))
	}
	png1 := genPNG(t, 4, 4)
	if out := okBytes(t, forceImageTask(t, Image_reduceFrames(2, png1))); string(out) != png1 {
		t.Fatalf("a png was changed")
	}
	if kind := imageErrKind(t, forceImageTask(t, Image_reduceFrames(0, in))); kind != "InvalidInput" {
		t.Fatalf("n=0: %s", kind)
	}
}

// toAlpha and whiteToAlpha of an animated GIF key every frame and keep the
// animation; trim cuts every frame to one box.
func TestImageGif_ToAlphaAndTrimEveryFrame(t *testing.T) {
	in, delays := stripedGIF(t, 4, 12, 6)
	g := decodeAllGIF(t, okBytes(t, forceImageTask(t, Image_toAlpha(255, 255, 255, in))))
	checkTiming(t, "toAlpha", g, 4, delays, 0)
	frames := refCompose(g)
	for i, f := range frames {
		if f.NRGBAAt(i, 2) != (color.NRGBA{255, 0, 0, 255}) || f.NRGBAAt((i+5)%12, 2).A != 0 {
			t.Fatalf("toAlpha frame %d: bar %v, field %v", i, f.NRGBAAt(i, 2), f.NRGBAAt((i+5)%12, 2))
		}
	}
	// Trim: the bars cover x = 0..3, so every frame is cut to 4 x 6.
	tg := decodeAllGIF(t, okBytes(t, forceImageTask(t, Image_trim("preserve", 0, 245, 0, in))))
	if len(tg.Image) != 4 || tg.Config.Width != 4 || tg.Config.Height != 6 {
		t.Fatalf("trim: %d frames %dx%d", len(tg.Image), tg.Config.Width, tg.Config.Height)
	}
}

// A GIF with more colours than a palette holds is reduced with dithering,
// and a JPEG written as Gif becomes a still GIF.
func TestImageGif_GifOutputOfAPhoto(t *testing.T) {
	src := genJPEG(t, 64, 48)
	out := okBytes(t, forceImageTask(t, Image_resize("gif", 0, 32, 32, src)))
	g := decodeAllGIF(t, out)
	if len(g.Image) != 1 || g.Config.Width != 32 || g.Config.Height != 24 || len(g.Image[0].Palette) > 256 {
		t.Fatalf("gif of a photo: %d frames %dx%d", len(g.Image), g.Config.Width, g.Config.Height)
	}
}

// pngWithSize is a valid 1x1 PNG whose IHDR claims w x h (CRC fixed), a
// tiny file that would decode to a huge image.
func pngWithSize(t *testing.T, w, h uint32) string {
	t.Helper()
	b := []byte(genPNG(t, 1, 1))
	binary.BigEndian.PutUint32(b[16:], w)
	binary.BigEndian.PutUint32(b[20:], h)
	binary.BigEndian.PutUint32(b[29:], crc32.ChecksumIEEE(b[12:29]))
	return string(b)
}

// jpegWithSize is an 8x8 JPEG whose SOF claims w x h.
func jpegWithSize(t *testing.T, w, h uint16) string {
	t.Helper()
	b := []byte(genJPEG(t, 8, 8))
	for i := 2; i+9 < len(b); i++ {
		if b[i] == 0xFF && (b[i+1] == 0xC0 || b[i+1] == 0xC2) {
			binary.BigEndian.PutUint16(b[i+5:], h)
			binary.BigEndian.PutUint16(b[i+7:], w)
			return string(b)
		}
	}
	t.Fatal("no SOF marker")
	return ""
}

// The found bug: a still image had no pixel limit. A tiny PNG or JPEG that
// claims 20000 x 20000 (400 megapixels) is refused from its header with
// too_many_pixels before any decode, by every entry that decodes; dimensions
// still reports it.
func TestImagePixelLimit_TinyFileHugeDimensions(t *testing.T) {
	for name, in := range map[string]string{
		"png":  pngWithSize(t, 20000, 20000),
		"jpeg": jpegWithSize(t, 20000, 20000),
		"gif":  gifBlocks(20000, 20000, 1, 1, 1),
	} {
		if len(in) > 2000 {
			t.Fatalf("%s fixture is not tiny: %d bytes", name, len(in))
		}
		for op, res := range map[string]any{
			"resize":       forceImageTask(t, Image_resize("png", 0, 100, 100, in)),
			"trim":         forceImageTask(t, Image_trim("png", 0, 245, 0, in)),
			"crop":         forceImageTask(t, Image_crop("png", 0, 0, 0, 10, 10, in)),
			"toAlpha":      forceImageTask(t, Image_toAlpha(255, 255, 255, in)),
			"reduceFrames": forceImageTask(t, Image_reduceFrames(2, in)),
		} {
			if code := imageDomainCode(t, res); code != imageCodeTooManyPixels {
				t.Fatalf("%s %s: code %s", name, op, code)
			}
		}
		res := forceImageTask(t, Image_dimensions(in))
		m := res.(SkyResult[any, any])
		if m.Tag != 0 || m.OkValue.(map[string]any)["width"] != 20000 {
			t.Fatalf("%s: dimensions = %#v", name, res)
		}
	}
	// Just under the limit is not refused by the limit (10000 x 10000).
	if res := forceImageTask(t, Image_resize("png", 0, 10, 10, pngWithSize(t, 10000, 10000))); res.(SkyResult[any, any]).Tag == 1 {
		if df, ok := DomainOf(res.(SkyResult[any, any]).ErrValue); ok && df.Domain == "image" {
			t.Fatalf("100 megapixels was refused by the limit")
		}
	}
}

// WebpLossless output decodes back pixel-exact, a WebP input is read, and
// Preserve keeps a WebP a WebP.
func TestImageWebpLossless_RoundTrip(t *testing.T) {
	src := genJPEG(t, 120, 80)
	pngOut := okBytes(t, forceImageTask(t, Image_resize("png", 0, 60, 60, src)))
	webpOut := okBytes(t, forceImageTask(t, Image_resize("webp", 0, 60, 60, src)))
	if !bytes.HasPrefix(webpOut, []byte("RIFF")) || string(webpOut[8:16]) != "WEBPVP8L" {
		t.Fatalf("not a lossless webp: %q", webpOut[:16])
	}
	wd, err := webp.Decode(bytes.NewReader(webpOut))
	if err != nil {
		t.Fatalf("webp decode: %v", err)
	}
	pd, _ := png.Decode(bytes.NewReader(pngOut))
	if !bytes.Equal(imageToTestNRGBA(wd).Pix, imageToTestNRGBA(pd).Pix) {
		t.Fatalf("webp pixels differ from the png of the same resize")
	}
	// A WebP input: Preserve keeps it WebP; Png reads it.
	again := okBytes(t, forceImageTask(t, Image_resize("preserve", 0, 30, 30, string(webpOut))))
	if string(again[8:16]) != "WEBPVP8L" {
		t.Fatalf("preserve of a webp is not webp")
	}
	w, h, _ := decodeDims(t, okBytes(t, forceImageTask(t, Image_resize("png", 0, 30, 30, string(webpOut)))))
	if w != 30 || h != 20 {
		t.Fatalf("webp input resized to %dx%d", w, h)
	}
}

// A GIF with a header and a trailer but no frame is an Err, not an empty
// image.
func TestImageGif_NoFrameIsErr(t *testing.T) {
	in := gifBlocks(4, 4, 1, 1, 0)
	for name, res := range map[string]any{
		"resize": forceImageTask(t, Image_resize("png", 0, 2, 2, in)),
		"gif":    forceImageTask(t, Image_resize("gif", 0, 2, 2, in)),
		"frames": forceImageTask(t, Image_frames(in)),
	} {
		if kind := imageErrKind(t, res); kind != "InvalidInput" {
			t.Fatalf("%s: %s", name, kind)
		}
	}
}
