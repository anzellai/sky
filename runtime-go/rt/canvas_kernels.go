// Package rt — Std.Image.Canvas kernels: a persistent display list.
//
// A Canvas is the stdlib ADT `Canvas__Internal` holding a *canvasNode. Each
// drawing function returns a new node that points at the canvas it was given,
// in O(1): no pixel is touched and nothing is copied, so a pipeline of a
// hundred drawing calls on a 952 x 1812 canvas costs a hundred small structs,
// not a hundred 6.9 MB copies. A canvas is never changed, so one canvas may be
// the base of many (a template card drawn once, then a name written on each
// copy).
//
// `Image_canvasEncode` replays the list once (canvas_raster.go) into one RGBA
// raster and writes it with the Std.Image encoders. Every error that depends
// on the input appears there, as the Task's Err: a colour channel outside
// 0..255, a canvas over 16384 on a side or over 100 megapixels, a pasted
// image that does not decode or is over the pixel limit.
//
// Server only (`!js`, EFFECT_KERNELS "Image"), like the rest of Std.Image.
//
//go:build !js

package rt

const (
	canvasADTName = "Canvas__Internal"
	maskADTName   = "Mask__Internal"
	fontADTName   = "Font__Internal"

	// canvasMaxSide bounds one side of a canvas (the WebP limit as well).
	canvasMaxSide = 16384

	imageCodeCanvasTooLarge = "canvas_too_large"
	imageCodeColourRange    = "colour_range"
	imageCodeFontParse      = "font_parse"
	imageCodeFontIndex      = "font_index"
	imageCodeSizeMismatch   = "size_mismatch"
)

type canvasOpKind int

const (
	canvasOpBlank canvasOpKind = iota
	canvasOpFromImage
	canvasOpRect
	canvasOpRoundedRect
	canvasOpEllipse
	canvasOpArc
	canvasOpLine
	canvasOpPaste
	canvasOpPasteCanvas
	canvasOpBlur
	canvasOpResize
	canvasOpText
)

// canvasNode is one recorded operation. `prev` is the canvas it draws on
// (nil for the two bases, blank and fromImage).
type canvasNode struct {
	kind   canvasOpKind
	prev   *canvasNode
	depth  int // ops from the base, for the replay slice
	ints   [6]int
	floats [2]float64
	colour int64 // packed 0xRRGGBBAA, or -1 for a channel outside 0..255
	bytes  string
	mask   *canvasMask
	src    *canvasNode // pasteCanvas: the canvas pasted
	font   *canvasFont
	text   string
}

func canvasBox(n *canvasNode) SkyADT {
	return SkyADT{Tag: 0, SkyName: canvasADTName, Fields: []any{n}}
}

// canvasOf returns the node a Canvas carries. Every Canvas is made by a
// kernel below, so another shape cannot reach here from well-typed code; it
// reads as a 1 x 1 transparent canvas rather than a panic.
func canvasOf(v any) *canvasNode {
	v = unwrapAny(v)
	if adt, ok := v.(SkyADT); ok && adt.SkyName == canvasADTName && len(adt.Fields) == 1 {
		if n, ok := adt.Fields[0].(*canvasNode); ok && n != nil {
			return n
		}
	}
	return &canvasNode{kind: canvasOpBlank, ints: [6]int{1, 1}}
}

func canvasPush(prev any, n *canvasNode) any {
	p := canvasOf(prev)
	n.prev = p
	n.depth = p.depth + 1
	return canvasBox(n)
}

// Image_canvasBlank is `blank_ : Int -> Int -> Int -> Canvas` (width, height,
// packed colour).
func Image_canvasBlank(w, h, colour any) any {
	return canvasBox(&canvasNode{kind: canvasOpBlank, ints: [6]int{AsInt(w), AsInt(h)}, colour: int64(AsInt(colour))})
}

// Image_canvasFromImage is `fromImage : Bytes -> Canvas`; decoded at encode.
func Image_canvasFromImage(b any) any {
	return canvasBox(&canvasNode{kind: canvasOpFromImage, bytes: asBytesString(b)})
}

// Image_canvasRect is `rect_ : x y w h strokeWidth colour canvas`; a stroke
// width of 0 fills.
func Image_canvasRect(x, y, w, h, stroke, colour, c any) any {
	return canvasPush(c, &canvasNode{kind: canvasOpRect,
		ints: [6]int{AsInt(x), AsInt(y), AsInt(w), AsInt(h), AsInt(stroke)}, colour: int64(AsInt(colour))})
}

// Image_canvasRoundedRect is `roundedRect_ : x y w h radius stroke colour canvas`.
func Image_canvasRoundedRect(x, y, w, h, radius, stroke, colour, c any) any {
	return canvasPush(c, &canvasNode{kind: canvasOpRoundedRect,
		ints:   [6]int{AsInt(x), AsInt(y), AsInt(w), AsInt(h), AsInt(stroke), AsInt(radius)},
		colour: int64(AsInt(colour))})
}

// Image_canvasEllipse is `ellipse_ : x y w h stroke colour canvas` (the
// ellipse inside the box).
func Image_canvasEllipse(x, y, w, h, stroke, colour, c any) any {
	return canvasPush(c, &canvasNode{kind: canvasOpEllipse,
		ints: [6]int{AsInt(x), AsInt(y), AsInt(w), AsInt(h), AsInt(stroke)}, colour: int64(AsInt(colour))})
}

// Image_canvasArc is `arc_ : cx cy radius start end stroke colour canvas`
// (angles in degrees, clockwise from 3 o'clock).
func Image_canvasArc(cx, cy, r, start, end, stroke, colour, c any) any {
	return canvasPush(c, &canvasNode{kind: canvasOpArc,
		ints:   [6]int{AsInt(cx), AsInt(cy), AsInt(r), 0, AsInt(stroke)},
		floats: [2]float64{AsFloat(start), AsFloat(end)}, colour: int64(AsInt(colour))})
}

// Image_canvasLine is `line_ : x1 y1 x2 y2 width colour canvas`.
func Image_canvasLine(x1, y1, x2, y2, width, colour, c any) any {
	return canvasPush(c, &canvasNode{kind: canvasOpLine,
		ints: [6]int{AsInt(x1), AsInt(y1), AsInt(x2), AsInt(y2), AsInt(width)}, colour: int64(AsInt(colour))})
}

// Image_canvasPaste is `paste_ : x y hasMask mask bytes canvas`.
func Image_canvasPaste(x, y, hasMask, mask, b, c any) any {
	n := &canvasNode{kind: canvasOpPaste, ints: [6]int{AsInt(x), AsInt(y)}, bytes: asBytesString(b)}
	if AsBool(hasMask) {
		n.mask = maskOf(mask)
	}
	return canvasPush(c, n)
}

// Image_canvasPasteCanvas is `pasteCanvas_ : x y hasMask mask src canvas`.
func Image_canvasPasteCanvas(x, y, hasMask, mask, src, c any) any {
	n := &canvasNode{kind: canvasOpPasteCanvas, ints: [6]int{AsInt(x), AsInt(y)}, src: canvasOf(src)}
	if AsBool(hasMask) {
		n.mask = maskOf(mask)
	}
	return canvasPush(c, n)
}

// Image_canvasBlur is `blur : Float -> Canvas -> Canvas` (Gaussian sigma).
func Image_canvasBlur(sigma, c any) any {
	return canvasPush(c, &canvasNode{kind: canvasOpBlur, floats: [2]float64{AsFloat(sigma)}})
}

// Image_canvasResize is `resize_ : filter w h canvas` (filter 0 Nearest,
// 1 Bilinear, 2 CatmullRom, 3 Lanczos).
func Image_canvasResize(filter, w, h, c any) any {
	return canvasPush(c, &canvasNode{kind: canvasOpResize, ints: [6]int{AsInt(w), AsInt(h), AsInt(filter)}})
}

// Image_canvasText is `text_ : font size colour x y string canvas`.
func Image_canvasText(f, size, colour, x, y, s, c any) any {
	return canvasPush(c, &canvasNode{kind: canvasOpText, font: fontOf(f),
		floats: [2]float64{AsFloat(size)}, colour: int64(AsInt(colour)),
		ints: [6]int{AsInt(x), AsInt(y)}, text: AsString(s)})
}

// Image_canvasEncode is `encode_ : String -> Int -> Canvas -> Task Error Bytes`
// (format name and quality, as Std.Image passes them).
func Image_canvasEncode(format, quality, c any) any {
	return func() any {
		return imageGuard("canvas.encode", func() any {
			img, bad := canvasRender("canvas.encode", canvasOf(c), 0)
			if bad != nil {
				return bad
			}
			return encodeImage("canvas.encode", img, AsString(format), "png", AsInt(quality))
		})
	}
}

// Image_canvasPixelDiff is `pixelDiff : Int -> Bytes -> Bytes -> Task Error Int`:
// the number of pixels where some channel (red, green, blue or alpha, not
// premultiplied) differs by more than `tolerance`. Images of different sizes
// are an Err (code size_mismatch).
func Image_canvasPixelDiff(tolerance, a, b any) any {
	return func() any {
		return imageGuard("canvas.pixelDiff", func() any {
			return canvasPixelDiff(AsInt(tolerance), asBytesString(a), asBytesString(b))
		})
	}
}

// Typed twins (doc 14 R5, lever §5.3; lower.rs `kernel_typed_return_twin`):
// the same kernels returning the handle ADT itself, so a drawing step narrows
// nothing at the call site. canvas_kernels_test.go checks each against its
// erased kernel.

func Image_canvasBlankT(w, h, colour any) SkyADT { return Image_canvasBlank(w, h, colour).(SkyADT) }
func Image_canvasFromImageT(b any) SkyADT        { return Image_canvasFromImage(b).(SkyADT) }
func Image_canvasRectT(x, y, w, h, stroke, colour, c any) SkyADT {
	return Image_canvasRect(x, y, w, h, stroke, colour, c).(SkyADT)
}
func Image_canvasRoundedRectT(x, y, w, h, radius, stroke, colour, c any) SkyADT {
	return Image_canvasRoundedRect(x, y, w, h, radius, stroke, colour, c).(SkyADT)
}
func Image_canvasEllipseT(x, y, w, h, stroke, colour, c any) SkyADT {
	return Image_canvasEllipse(x, y, w, h, stroke, colour, c).(SkyADT)
}
func Image_canvasArcT(cx, cy, r, start, end, stroke, colour, c any) SkyADT {
	return Image_canvasArc(cx, cy, r, start, end, stroke, colour, c).(SkyADT)
}
func Image_canvasLineT(x1, y1, x2, y2, width, colour, c any) SkyADT {
	return Image_canvasLine(x1, y1, x2, y2, width, colour, c).(SkyADT)
}
func Image_canvasPasteT(x, y, hasMask, mask, b, c any) SkyADT {
	return Image_canvasPaste(x, y, hasMask, mask, b, c).(SkyADT)
}
func Image_canvasPasteCanvasT(x, y, hasMask, mask, src, c any) SkyADT {
	return Image_canvasPasteCanvas(x, y, hasMask, mask, src, c).(SkyADT)
}
func Image_canvasBlurT(sigma, c any) SkyADT { return Image_canvasBlur(sigma, c).(SkyADT) }
func Image_canvasResizeT(filter, w, h, c any) SkyADT {
	return Image_canvasResize(filter, w, h, c).(SkyADT)
}
func Image_canvasTextT(f, size, colour, x, y, s, c any) SkyADT {
	return Image_canvasText(f, size, colour, x, y, s, c).(SkyADT)
}
func Image_canvasRoundedMaskT(w, h, r any) SkyADT { return Image_canvasRoundedMask(w, h, r).(SkyADT) }
func Image_canvasEllipseMaskT(w, h any) SkyADT    { return Image_canvasEllipseMask(w, h).(SkyADT) }
func Image_canvasAlphaMaskT(b any) SkyADT         { return Image_canvasAlphaMask(b).(SkyADT) }
func Image_canvasDefaultFontT(u any) SkyADT       { return Image_canvasDefaultFont(u).(SkyADT) }
