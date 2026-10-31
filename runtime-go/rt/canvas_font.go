// Package rt — Std.Image.Canvas fonts and text.
//
// Fonts are parsed with golang.org/x/image/font/sfnt (TrueType and OpenType
// outlines, and TTC / OTC collections by index) and drawn with
// golang.org/x/image/font/opentype at 72 DPI, so a size is in pixels. There
// is no hinting, pair kerning comes from the `kern` table (GPOS kerning and
// complex shaping are not applied), and a line break moves down one line
// height. `defaultFont` is Go Regular (golang.org/x/image/font/gofont, a
// BSD-licensed font shipped in the module), so text works with no font file;
// the linker leaves it out of a program that never names it.
//
//go:build !js

package rt

import (
	"errors"
	"image"
	"image/color"
	"io/fs"
	"math"
	"os"
	"strconv"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// canvasMaxFontSize bounds a text size in pixels.
const canvasMaxFontSize = 4096

type canvasFont struct {
	f *sfnt.Font
}

func fontBox(f *canvasFont) SkyADT { return SkyADT{Tag: 0, SkyName: fontADTName, Fields: []any{f}} }

// fontOf returns the font a Font carries; another shape reads as Go Regular.
func fontOf(v any) *canvasFont {
	v = unwrapAny(v)
	if adt, ok := v.(SkyADT); ok && adt.SkyName == fontADTName && len(adt.Fields) == 1 {
		if f, ok := adt.Fields[0].(*canvasFont); ok && f != nil {
			return f
		}
	}
	f, _ := canvasParseFont("", goregular.TTF, 0)
	return f
}

// canvasParseFont reads one font: index 0 of a plain font file, or the
// index-th font of a collection (a file that starts with "ttcf").
func canvasParseFont(op string, b []byte, index int) (*canvasFont, any) {
	if len(b) >= 4 && string(b[:4]) == "ttcf" {
		c, err := sfnt.ParseCollection(b)
		if err != nil {
			return nil, imageDomainErr(imageCodeFontParse, op+": not a font collection: "+err.Error(), nil)
		}
		if index < 0 || index >= c.NumFonts() {
			return nil, imageDomainErr(imageCodeFontIndex,
				op+": the collection has no font at that index ("+imageCodeFontIndex+")",
				[][2]string{{"limit", strconv.Itoa(c.NumFonts() - 1)}, {"value", strconv.Itoa(index)}})
		}
		f, err := c.Font(index)
		if err != nil {
			return nil, imageDomainErr(imageCodeFontParse, op+": not a font: "+err.Error(), nil)
		}
		return &canvasFont{f: f}, nil
	}
	if index != 0 {
		return nil, imageDomainErr(imageCodeFontIndex,
			op+": a single font has only index 0 ("+imageCodeFontIndex+")",
			[][2]string{{"limit", "0"}, {"value", strconv.Itoa(index)}})
	}
	f, err := sfnt.Parse(b)
	if err != nil {
		return nil, imageDomainErr(imageCodeFontParse, op+": not a font: "+err.Error(), nil)
	}
	return &canvasFont{f: f}, nil
}

// Image_canvasLoadFont is `loadFont : String -> Int -> Task Error Font`.
func Image_canvasLoadFont(path, index any) any {
	return func() any {
		return imageGuard("canvas.loadFont", func() any {
			p := AsString(path)
			b, err := os.ReadFile(p)
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return Err[any, any](makeError(5, "NotFound", "canvas.loadFont: no font file at "+p))
				}
				return Err[any, any](ErrIo("canvas.loadFont: " + err.Error()))
			}
			f, bad := canvasParseFont("canvas.loadFont", b, AsInt(index))
			if bad != nil {
				return bad
			}
			return Ok[any, any](fontBox(f))
		})
	}
}

// Image_canvasFontFromBytes is `fontFromBytes : Bytes -> Int -> Result Error Font`.
func Image_canvasFontFromBytes(b, index any) any {
	return imageGuard("canvas.fontFromBytes", func() any {
		f, bad := canvasParseFont("canvas.fontFromBytes", []byte(asBytesString(b)), AsInt(index))
		if bad != nil {
			return bad
		}
		return Ok[any, any](fontBox(f))
	})
}

// Image_canvasDefaultFont is `defaultFont_ : () -> Font` (Go Regular).
func Image_canvasDefaultFont(_ any) any {
	f, _ := canvasParseFont("", goregular.TTF, 0)
	return fontBox(f)
}

func canvasFace(f *canvasFont, size float64) (font.Face, bool) {
	if f == nil || !(size > 0) || size > canvasMaxFontSize {
		return nil, false
	}
	face, err := opentype.NewFace(f.f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingNone})
	if err != nil {
		return nil, false
	}
	return face, true
}

// canvasDrawText draws s with its first line's left edge at x and the top of
// its ascent at y (Pillow's anchor "la").
func canvasDrawText(dst *image.RGBA, f *canvasFont, size float64, col color.NRGBA, x, y int, s string) {
	face, ok := canvasFace(f, size)
	if !ok || col.A == 0 || s == "" {
		return
	}
	defer face.Close()
	m := face.Metrics()
	d := &font.Drawer{Dst: dst, Src: image.NewUniform(col), Face: face}
	for i, line := range strings.Split(s, "\n") {
		d.Dot = fixed.Point26_6{X: fixed.I(x), Y: fixed.I(y) + m.Ascent + m.Height.Mul(fixed.I(i))}
		d.DrawString(line)
	}
}

func ceil26(v fixed.Int26_6) int { return int(math.Ceil(float64(v) / 64)) }

// Image_canvasMeasure is `measure : Font -> Float -> String -> { width,
// height, ascent, descent }` in whole pixels (rounded up). width is the
// widest line's advance; height is ascent + descent, plus one line height for
// each line break.
func Image_canvasMeasure(f, size, s any) any {
	face, ok := canvasFace(fontOf(f), AsFloat(size))
	if !ok {
		return map[string]any{"width": 0, "height": 0, "ascent": 0, "descent": 0}
	}
	defer face.Close()
	m := face.Metrics()
	lines := strings.Split(AsString(s), "\n")
	width := 0
	for _, line := range lines {
		if w := ceil26(font.MeasureString(face, line)); w > width {
			width = w
		}
	}
	ascent, descent := ceil26(m.Ascent), ceil26(m.Descent)
	return map[string]any{
		"width":   width,
		"height":  ascent + descent + (len(lines)-1)*ceil26(m.Height),
		"ascent":  ascent,
		"descent": descent,
	}
}
