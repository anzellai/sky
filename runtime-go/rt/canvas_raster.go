// Package rt — Std.Image.Canvas raster: replay a display list into pixels.
//
// canvasRender walks the op list from its base and draws each op once into
// one premultiplied RGBA raster. Shapes are anti-aliased paths
// (golang.org/x/image/vector), rasterised over their own bounding box only, so
// a shape costs the pixels it covers, not the whole canvas. A stroke is the
// band between the shape and the shape inset by the stroke width (drawn
// inside the shape's box, as Pillow draws an outline); the inner contour runs
// the other way round, so the rasteriser leaves it empty.
//
// Blur is three box passes per axis (a Gaussian to within a few percent),
// O(width x height) whatever the sigma. Resize uses golang.org/x/image/draw
// (Nearest, Bilinear, CatmullRom) and a Lanczos-3 kernel.
//
//go:build !js

package rt

import (
	"image"
	"image/color"
	"math"
	"strconv"

	"golang.org/x/image/draw"
	"golang.org/x/image/vector"
)

// canvasMaxNesting bounds pasteCanvas inside pasteCanvas.
const canvasMaxNesting = 64

func canvasCheckSize(op string, w, h int) any {
	if w < 1 || h < 1 {
		return Err[any, any](ErrInvalidInput(op + ": a canvas must be at least 1 x 1, not " +
			strconv.Itoa(w) + " x " + strconv.Itoa(h)))
	}
	if w > canvasMaxSide || h > canvasMaxSide {
		side := w
		if h > side {
			side = h
		}
		return imageDomainErr(imageCodeCanvasTooLarge,
			op+": a canvas side is "+strconv.Itoa(side)+" pixels, over the limit of "+
				strconv.Itoa(canvasMaxSide)+" ("+imageCodeCanvasTooLarge+")",
			[][2]string{{"limit", strconv.Itoa(canvasMaxSide)}, {"value", strconv.Itoa(side)}})
	}
	return imageCheckPixels(op, w, h)
}

// canvasColour unpacks 0xRRGGBBAA; -1 (a channel outside 0..255) is an Err.
func canvasColour(op string, packed int64) (color.NRGBA, any) {
	if packed < 0 || packed > 0xFFFFFFFF {
		return color.NRGBA{}, imageDomainErr(imageCodeColourRange,
			op+": each Rgba channel must be 0..255 ("+imageCodeColourRange+")",
			[][2]string{{"limit", "255"}})
	}
	return color.NRGBA{uint8(packed >> 24), uint8(packed >> 16), uint8(packed >> 8), uint8(packed)}, nil
}

// canvasRender replays the list that ends at top.
func canvasRender(op string, top *canvasNode, nesting int) (*image.RGBA, any) {
	if nesting > canvasMaxNesting {
		return nil, Err[any, any](ErrInvalidInput(op + ": pasteCanvas is nested too deep"))
	}
	ops := make([]*canvasNode, top.depth+1)
	for n := top; n != nil; n = n.prev {
		ops[n.depth] = n
	}
	var dst *image.RGBA
	for _, n := range ops {
		var bad any
		switch n.kind {
		case canvasOpBlank:
			col, e := canvasColour(op, n.colour)
			if e != nil {
				return nil, e
			}
			if e := canvasCheckSize(op, n.ints[0], n.ints[1]); e != nil {
				return nil, e
			}
			dst = image.NewRGBA(image.Rect(0, 0, n.ints[0], n.ints[1]))
			if col.A != 0 {
				draw.Draw(dst, dst.Rect, image.NewUniform(col), image.Point{}, draw.Src)
			}
		case canvasOpFromImage:
			src, _, e := imageDecodeLimited(op, n.bytes)
			if e != nil {
				return nil, e
			}
			b := src.Bounds()
			if e := canvasCheckSize(op, b.Dx(), b.Dy()); e != nil {
				return nil, e
			}
			dst = image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
			draw.Draw(dst, dst.Rect, src, b.Min, draw.Src)
		case canvasOpRect, canvasOpRoundedRect, canvasOpEllipse, canvasOpArc, canvasOpLine:
			bad = canvasShape(op, dst, n)
		case canvasOpPaste:
			src, _, e := imageDecodeLimited(op, n.bytes)
			if e != nil {
				return nil, e
			}
			bad = canvasPasteImage(op, dst, src, n)
		case canvasOpPasteCanvas:
			src, e := canvasRender(op, n.src, nesting+1)
			if e != nil {
				return nil, e
			}
			bad = canvasPasteImage(op, dst, src, n)
		case canvasOpBlur:
			canvasBlur(dst, n.floats[0])
		case canvasOpResize:
			if e := canvasCheckSize(op, n.ints[0], n.ints[1]); e != nil {
				return nil, e
			}
			dst = canvasResize(dst, n.ints[2], n.ints[0], n.ints[1])
		case canvasOpText:
			col, e := canvasColour(op, n.colour)
			if e != nil {
				return nil, e
			}
			canvasDrawText(dst, n.font, n.floats[0], col, n.ints[0], n.ints[1], n.text)
		}
		if bad != nil {
			return nil, bad
		}
	}
	return dst, nil
}

// --- paths -------------------------------------------------------------------

type cvPt struct{ x, y float64 }

type cvSeg struct {
	cubic  bool
	c1, c2 cvPt
	p      cvPt
}

// cvContour is one closed contour.
type cvContour struct {
	start cvPt
	segs  []cvSeg
}

func (c *cvContour) lineTo(x, y float64) { c.segs = append(c.segs, cvSeg{p: cvPt{x, y}}) }
func (c *cvContour) cubicTo(c1, c2, p cvPt) {
	c.segs = append(c.segs, cvSeg{cubic: true, c1: c1, c2: c2, p: p})
}

func (c cvContour) last() cvPt {
	if len(c.segs) == 0 {
		return c.start
	}
	return c.segs[len(c.segs)-1].p
}

// reversed runs the same contour the other way round.
func (c cvContour) reversed() cvContour {
	pts := make([]cvPt, 0, len(c.segs)+1)
	pts = append(pts, c.start)
	for _, s := range c.segs {
		pts = append(pts, s.p)
	}
	out := cvContour{start: pts[len(pts)-1]}
	for i := len(c.segs) - 1; i >= 0; i-- {
		s := c.segs[i]
		out.segs = append(out.segs, cvSeg{cubic: s.cubic, c1: s.c2, c2: s.c1, p: pts[i]})
	}
	return out
}

func (c cvContour) bounds() (minX, minY, maxX, maxY float64) {
	minX, minY, maxX, maxY = c.start.x, c.start.y, c.start.x, c.start.y
	grow := func(p cvPt) {
		minX, minY = math.Min(minX, p.x), math.Min(minY, p.y)
		maxX, maxY = math.Max(maxX, p.x), math.Max(maxY, p.y)
	}
	for _, s := range c.segs {
		if s.cubic {
			grow(s.c1)
			grow(s.c2)
		}
		grow(s.p)
	}
	return
}

// cvFill draws the contours (non-zero coverage, so an inner contour run the
// other way is a hole) in `src` over dst, rasterising their bounding box only.
func cvFill(dst draw.Image, src image.Image, contours ...cvContour) {
	if len(contours) == 0 {
		return
	}
	minX, minY, maxX, maxY := contours[0].bounds()
	for _, c := range contours[1:] {
		a, b, cc, d := c.bounds()
		minX, minY, maxX, maxY = math.Min(minX, a), math.Min(minY, b), math.Max(maxX, cc), math.Max(maxY, d)
	}
	box := image.Rect(int(math.Floor(minX)), int(math.Floor(minY)),
		int(math.Ceil(maxX)), int(math.Ceil(maxY))).Intersect(dst.Bounds())
	if box.Empty() {
		return
	}
	// The rasteriser's own coordinates start at box.Min; points outside the
	// box are clipped by it.
	z := vector.NewRasterizer(box.Dx(), box.Dy())
	ox, oy := float64(box.Min.X), float64(box.Min.Y)
	f := func(p cvPt) (float32, float32) { return float32(p.x - ox), float32(p.y - oy) }
	for _, c := range contours {
		z.MoveTo(f(c.start))
		for _, s := range c.segs {
			if s.cubic {
				x1, y1 := f(s.c1)
				x2, y2 := f(s.c2)
				x3, y3 := f(s.p)
				z.CubeTo(x1, y1, x2, y2, x3, y3)
			} else {
				z.LineTo(f(s.p))
			}
		}
		z.ClosePath()
	}
	z.Draw(dst, box, src, box.Min)
}

// kappa: the cubic Bezier control distance for a quarter circle.
const cvKappa = 0.5522847498307936

func cvRect(x0, y0, x1, y1 float64) cvContour {
	c := cvContour{start: cvPt{x0, y0}}
	c.lineTo(x1, y0)
	c.lineTo(x1, y1)
	c.lineTo(x0, y1)
	c.lineTo(x0, y0)
	return c
}

func cvRounded(x0, y0, x1, y1, r float64) cvContour {
	r = math.Max(0, math.Min(r, math.Min(x1-x0, y1-y0)/2))
	if r == 0 {
		return cvRect(x0, y0, x1, y1)
	}
	k := r * cvKappa
	c := cvContour{start: cvPt{x0 + r, y0}}
	c.lineTo(x1-r, y0)
	c.cubicTo(cvPt{x1 - r + k, y0}, cvPt{x1, y0 + r - k}, cvPt{x1, y0 + r})
	c.lineTo(x1, y1-r)
	c.cubicTo(cvPt{x1, y1 - r + k}, cvPt{x1 - r + k, y1}, cvPt{x1 - r, y1})
	c.lineTo(x0+r, y1)
	c.cubicTo(cvPt{x0 + r - k, y1}, cvPt{x0, y1 - r + k}, cvPt{x0, y1 - r})
	c.lineTo(x0, y0+r)
	c.cubicTo(cvPt{x0, y0 + r - k}, cvPt{x0 + r - k, y0}, cvPt{x0 + r, y0})
	return c
}

func cvEllipse(x0, y0, x1, y1 float64) cvContour {
	cx, cy := (x0+x1)/2, (y0+y1)/2
	rx, ry := (x1-x0)/2, (y1-y0)/2
	kx, ky := rx*cvKappa, ry*cvKappa
	c := cvContour{start: cvPt{x1, cy}}
	c.cubicTo(cvPt{x1, cy + ky}, cvPt{cx + kx, y1}, cvPt{cx, y1})
	c.cubicTo(cvPt{cx - kx, y1}, cvPt{x0, cy + ky}, cvPt{x0, cy})
	c.cubicTo(cvPt{x0, cy - ky}, cvPt{cx - kx, y0}, cvPt{cx, y0})
	c.cubicTo(cvPt{cx + kx, y0}, cvPt{x1, cy - ky}, cvPt{x1, cy})
	return c
}

// cvArcTo appends a circular arc (centre, radius) from angle a to b (radians,
// clockwise on screen when b > a) in pieces of at most 90 degrees.
func cvArcTo(c *cvContour, cx, cy, r, a, b float64) {
	n := int(math.Ceil(math.Abs(b-a) / (math.Pi / 2)))
	if n < 1 {
		n = 1
	}
	step := (b - a) / float64(n)
	k := 4.0 / 3.0 * math.Tan(step/4)
	for i := 0; i < n; i++ {
		t0, t1 := a+step*float64(i), a+step*float64(i+1)
		p0 := cvPt{cx + r*math.Cos(t0), cy + r*math.Sin(t0)}
		p3 := cvPt{cx + r*math.Cos(t1), cy + r*math.Sin(t1)}
		c1 := cvPt{p0.x - k*r*math.Sin(t0), p0.y + k*r*math.Cos(t0)}
		c2 := cvPt{p3.x + k*r*math.Sin(t1), p3.y - k*r*math.Cos(t1)}
		c.cubicTo(c1, c2, p3)
	}
}

// canvasShape draws one rect, roundedRect, ellipse, arc or line op.
func canvasShape(op string, dst *image.RGBA, n *canvasNode) any {
	col, bad := canvasColour(op, n.colour)
	if bad != nil {
		return bad
	}
	if col.A == 0 {
		return nil
	}
	src := image.NewUniform(col)
	x, y, w, h, stroke := float64(n.ints[0]), float64(n.ints[1]), float64(n.ints[2]), float64(n.ints[3]), float64(n.ints[4])
	if stroke < 0 {
		return Err[any, any](ErrInvalidInput(op + ": a stroke width must not be negative"))
	}
	switch n.kind {
	case canvasOpLine:
		// Through pixel centres, `stroke` wide, square ends at the points.
		x1, y1, x2, y2 := x+0.5, y+0.5, w+0.5, h+0.5
		width := math.Max(stroke, 1)
		dx, dy := x2-x1, y2-y1
		l := math.Hypot(dx, dy)
		if l == 0 {
			cvFill(dst, src, cvRect(x1-width/2, y1-width/2, x1+width/2, y1+width/2))
			return nil
		}
		nx, ny := -dy/l*width/2, dx/l*width/2
		c := cvContour{start: cvPt{x1 + nx, y1 + ny}}
		c.lineTo(x2+nx, y2+ny)
		c.lineTo(x2-nx, y2-ny)
		c.lineTo(x1-nx, y1-ny)
		c.lineTo(x1+nx, y1+ny)
		cvFill(dst, src, c)
		return nil
	case canvasOpArc:
		cx, cy, r := x, y, float64(n.ints[2])
		if r <= 0 {
			return nil
		}
		a0, a1 := n.floats[0], n.floats[1]
		for a1 < a0 {
			a1 += 360
		}
		if a1-a0 > 360 {
			a1 = a0 + 360
		}
		ra, rb := a0*math.Pi/180, a1*math.Pi/180
		if stroke == 0 || stroke >= r {
			c := cvContour{start: cvPt{cx, cy}}
			c.lineTo(cx+r*math.Cos(ra), cy+r*math.Sin(ra))
			cvArcTo(&c, cx, cy, r, ra, rb)
			c.lineTo(cx, cy)
			cvFill(dst, src, c)
			return nil
		}
		ri := r - stroke
		c := cvContour{start: cvPt{cx + r*math.Cos(ra), cy + r*math.Sin(ra)}}
		cvArcTo(&c, cx, cy, r, ra, rb)
		c.lineTo(cx+ri*math.Cos(rb), cy+ri*math.Sin(rb))
		cvArcTo(&c, cx, cy, ri, rb, ra)
		c.lineTo(c.start.x, c.start.y)
		cvFill(dst, src, c)
		return nil
	}
	if w <= 0 || h <= 0 {
		return nil
	}
	shape := func(x0, y0, x1, y1 float64, inset float64) cvContour {
		switch n.kind {
		case canvasOpRoundedRect:
			return cvRounded(x0, y0, x1, y1, float64(n.ints[5])-inset)
		case canvasOpEllipse:
			return cvEllipse(x0, y0, x1, y1)
		default:
			return cvRect(x0, y0, x1, y1)
		}
	}
	outer := shape(x, y, x+w, y+h, 0)
	if stroke == 0 || 2*stroke >= math.Min(w, h) {
		cvFill(dst, src, outer)
		return nil
	}
	inner := shape(x+stroke, y+stroke, x+w-stroke, y+h-stroke, stroke).reversed()
	cvFill(dst, src, outer, inner)
	return nil
}

// --- masks and paste ------------------------------------------------------------

type canvasMaskKind int

const (
	canvasMaskRounded canvasMaskKind = iota
	canvasMaskEllipse
	canvasMaskImage
)

// canvasMask is recorded like a canvas op and rendered when it is used.
type canvasMask struct {
	kind   canvasMaskKind
	w, h   int
	radius int
	bytes  string
}

func maskBox(m *canvasMask) SkyADT { return SkyADT{Tag: 0, SkyName: maskADTName, Fields: []any{m}} }

func maskOf(v any) *canvasMask {
	v = unwrapAny(v)
	if adt, ok := v.(SkyADT); ok && adt.SkyName == maskADTName && len(adt.Fields) == 1 {
		if m, ok := adt.Fields[0].(*canvasMask); ok && m != nil {
			return m
		}
	}
	return &canvasMask{kind: canvasMaskEllipse}
}

// Image_canvasRoundedMask is `roundedMask : Int -> Int -> Int -> Mask`.
func Image_canvasRoundedMask(w, h, r any) any {
	return maskBox(&canvasMask{kind: canvasMaskRounded, w: AsInt(w), h: AsInt(h), radius: AsInt(r)})
}

// Image_canvasEllipseMask is `ellipseMask : Int -> Int -> Mask`.
func Image_canvasEllipseMask(w, h any) any {
	return maskBox(&canvasMask{kind: canvasMaskEllipse, w: AsInt(w), h: AsInt(h)})
}

// Image_canvasAlphaMask is `alphaMask : Bytes -> Mask`.
func Image_canvasAlphaMask(b any) any {
	return maskBox(&canvasMask{kind: canvasMaskImage, bytes: asBytesString(b)})
}

// render makes the mask's coverage: an anti-aliased shape, or an image's
// alpha (when it has transparency) else its luminance (white shows).
func (m *canvasMask) render(op string) (*image.Alpha, any) {
	if m.kind == canvasMaskImage {
		src, _, bad := imageDecodeLimited(op, m.bytes)
		if bad != nil {
			return nil, bad
		}
		b := src.Bounds()
		out := image.NewAlpha(image.Rect(0, 0, b.Dx(), b.Dy()))
		opaque := true
		for y := b.Min.Y; y < b.Max.Y && opaque; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				if _, _, _, a := src.At(x, y).RGBA(); a != 0xFFFF {
					opaque = false
					break
				}
			}
		}
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				c := src.At(x, y)
				if opaque {
					out.Pix[(y-b.Min.Y)*out.Stride+x-b.Min.X] = color.GrayModel.Convert(c).(color.Gray).Y
				} else {
					out.Pix[(y-b.Min.Y)*out.Stride+x-b.Min.X] = color.AlphaModel.Convert(c).(color.Alpha).A
				}
			}
		}
		return out, nil
	}
	if bad := canvasCheckSize(op, m.w, m.h); bad != nil {
		return nil, bad
	}
	out := image.NewAlpha(image.Rect(0, 0, m.w, m.h))
	w, h := float64(m.w), float64(m.h)
	if m.kind == canvasMaskRounded {
		cvFill(out, image.Opaque, cvRounded(0, 0, w, h, float64(m.radius)))
	} else {
		cvFill(out, image.Opaque, cvEllipse(0, 0, w, h))
	}
	return out, nil
}

// canvasPasteImage draws src with its top-left at (x, y), over what is there,
// through the mask when one is given (the mask's top-left at (x, y) too).
func canvasPasteImage(op string, dst *image.RGBA, src image.Image, n *canvasNode) any {
	b := src.Bounds()
	r := image.Rect(n.ints[0], n.ints[1], n.ints[0]+b.Dx(), n.ints[1]+b.Dy())
	if n.mask == nil {
		draw.Draw(dst, r, src, b.Min, draw.Over)
		return nil
	}
	m, bad := n.mask.render(op)
	if bad != nil {
		return bad
	}
	draw.DrawMask(dst, r, src, b.Min, m, image.Point{}, draw.Over)
	return nil
}

// --- blur --------------------------------------------------------------------

// canvasBoxSizes: three box widths whose passes approximate a Gaussian of
// sigma (Kutskir's method).
func canvasBoxSizes(sigma float64) [3]int {
	const n = 3.0
	wIdeal := math.Sqrt(12*sigma*sigma/n + 1)
	wl := int(math.Floor(wIdeal))
	if wl%2 == 0 {
		wl--
	}
	wu := wl + 2
	mIdeal := (12*sigma*sigma - n*float64(wl*wl) - 4*n*float64(wl) - 3*n) / (-4*float64(wl) - 4)
	m := int(math.Round(mIdeal))
	var out [3]int
	for i := 0; i < 3; i++ {
		if i < m {
			out[i] = wl
		} else {
			out[i] = wu
		}
	}
	return out
}

func canvasBlur(dst *image.RGBA, sigma float64) {
	if sigma <= 0 || dst == nil {
		return
	}
	w, h := dst.Rect.Dx(), dst.Rect.Dy()
	tmp := make([]uint8, len(dst.Pix))
	for _, size := range canvasBoxSizes(sigma) {
		r := (size - 1) / 2
		if r < 1 {
			continue
		}
		canvasBoxPass(dst.Pix, tmp, w, h, 4, dst.Stride, r) // along x
		canvasBoxPass(tmp, dst.Pix, h, w, dst.Stride, 4, r) // along y
	}
}

// canvasBoxPass blurs `lines` lines of `n` pixels: pixel i of line l is at
// l*lineStep + i*step. Edges repeat the edge pixel.
func canvasBoxPass(src, dst []uint8, n, lines, step, lineStep, r int) {
	div := 2*r + 1
	for l := 0; l < lines; l++ {
		base := l * lineStep
		at := func(i int) int {
			if i < 0 {
				i = 0
			} else if i >= n {
				i = n - 1
			}
			return base + i*step
		}
		for c := 0; c < 4; c++ {
			sum := 0
			for i := -r; i <= r; i++ {
				sum += int(src[at(i)+c])
			}
			for i := 0; i < n; i++ {
				dst[base+i*step+c] = uint8((sum + div/2) / div)
				sum += int(src[at(i+r+1)+c]) - int(src[at(i-r)+c])
			}
		}
	}
}

// --- resize ------------------------------------------------------------------

func lanczos3(t float64) float64 {
	if t == 0 {
		return 1
	}
	if t < -3 || t > 3 {
		return 0
	}
	pt := math.Pi * t
	return 3 * math.Sin(pt) * math.Sin(pt/3) / (pt * pt)
}

var canvasLanczos = &draw.Kernel{Support: 3, At: lanczos3}

func canvasResize(src *image.RGBA, filter, w, h int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	var s draw.Scaler
	switch filter {
	case 0:
		s = draw.NearestNeighbor
	case 1:
		s = draw.BiLinear
	case 3:
		s = canvasLanczos
	default:
		s = draw.CatmullRom
	}
	s.Scale(dst, dst.Rect, src, src.Rect, draw.Src, nil)
	return dst
}

// --- pixelDiff -----------------------------------------------------------------

func canvasPixelDiff(tolerance int, a, b string) any {
	const op = "canvas.pixelDiff"
	if tolerance < 0 {
		return Err[any, any](ErrInvalidInput(op + ": the tolerance must not be negative"))
	}
	ia, _, bad := imageDecodeLimited(op, a)
	if bad != nil {
		return bad
	}
	ib, _, bad := imageDecodeLimited(op, b)
	if bad != nil {
		return bad
	}
	ba, bb := ia.Bounds(), ib.Bounds()
	if ba.Dx() != bb.Dx() || ba.Dy() != bb.Dy() {
		return imageDomainErr(imageCodeSizeMismatch,
			op+": the images are "+strconv.Itoa(ba.Dx())+"x"+strconv.Itoa(ba.Dy())+" and "+
				strconv.Itoa(bb.Dx())+"x"+strconv.Itoa(bb.Dy())+" ("+imageCodeSizeMismatch+")", nil)
	}
	count := 0
	for y := 0; y < ba.Dy(); y++ {
		for x := 0; x < ba.Dx(); x++ {
			ca := color.NRGBAModel.Convert(ia.At(ba.Min.X+x, ba.Min.Y+y)).(color.NRGBA)
			cb := color.NRGBAModel.Convert(ib.At(bb.Min.X+x, bb.Min.Y+y)).(color.NRGBA)
			if canvasChanDiff(ca.R, cb.R) > tolerance || canvasChanDiff(ca.G, cb.G) > tolerance ||
				canvasChanDiff(ca.B, cb.B) > tolerance || canvasChanDiff(ca.A, cb.A) > tolerance {
				count++
			}
		}
	}
	return Ok[any, any](count)
}

func canvasChanDiff(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}
