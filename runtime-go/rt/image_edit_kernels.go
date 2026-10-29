// Package rt — Std.Image edit kernels: trim, crop and colour to alpha.
//
// Like the resize kernels (image_kernels.go) these run in the backend only
// (the `!js` tag keeps them out of the wasm client) and use the Go standard
// library alone: image, image/draw, image/color and the jpeg/png codecs.
// Every entry decodes its input, so a non-image or corrupt input is an
// `Err InvalidInput`, and a panic inside the codec is recovered into an
// `Err` as well: bad bytes never take the process down.
//
//go:build !js

package rt

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
)

// Image_trim implements Std.Image.trim:
//
//	Image_trim : String -> Int -> Int -> Int -> Bytes -> Task Error Bytes
//	             (format  quality threshold padding bytes)
//
// It removes the edge rows and columns in which every pixel is margin: fully
// transparent, or at least `threshold` on each of red, green and blue. It
// keeps `padding` pixels around what is left, clamped to the image.
func Image_trim(formatArg, qualityArg, thresholdArg, paddingArg, inputArg any) any {
	return func() any {
		return imageGuard("image.trim", func() any {
			return trimBytes(asBytesString(inputArg), AsString(formatArg), AsInt(qualityArg),
				AsInt(thresholdArg), AsInt(paddingArg))
		})
	}
}

// Image_crop implements Std.Image.crop:
//
//	Image_crop : String -> Int -> Int -> Int -> Int -> Int -> Bytes -> Task Error Bytes
//	             (format  quality x     y     width height bytes)
func Image_crop(formatArg, qualityArg, xArg, yArg, wArg, hArg, inputArg any) any {
	return func() any {
		return imageGuard("image.crop", func() any {
			return cropBytes(asBytesString(inputArg), AsString(formatArg), AsInt(qualityArg),
				AsInt(xArg), AsInt(yArg), AsInt(wArg), AsInt(hArg))
		})
	}
}

// Image_toAlpha implements Std.Image.toAlpha (and whiteToAlpha, which calls
// it with white):
//
//	Image_toAlpha : Int -> Int -> Int -> Bytes -> Task Error Bytes
//	                (red   green blue  bytes)
//
// The output is always PNG.
func Image_toAlpha(rArg, gArg, bArg, inputArg any) any {
	return func() any {
		return imageGuard("image.toAlpha", func() any {
			return toAlphaBytes(asBytesString(inputArg), AsInt(rArg), AsInt(gArg), AsInt(bArg))
		})
	}
}

// imageGuard turns a panic inside a codec or a pixel loop into an `Err`.
func imageGuard(op string, f func() any) (res any) {
	defer func() {
		if r := recover(); r != nil {
			res = Err[any, any](ErrInvalidInput(fmt.Sprintf("%s: could not process the image: %v", op, r)))
		}
	}()
	return f()
}

// copyRect returns the pixels of `r` (in src coordinates) as a new NRGBA image
// whose bounds start at (0, 0), so the alpha channel survives a PNG encode.
func copyRect(src image.Image, r image.Rectangle) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	draw.Draw(dst, dst.Bounds(), src, r.Min, draw.Src)
	return dst
}

// trimBox grows to hold the non-margin pixels of every image it sees.
type trimBox struct {
	t                      uint8
	minX, minY, maxX, maxY int
	seen                   bool
}

func (tb *trimBox) add(src image.Image) {
	b := src.Bounds()
	if !tb.seen {
		tb.minX, tb.minY, tb.maxX, tb.maxY = b.Max.X, b.Max.Y, b.Min.X-1, b.Min.Y-1
		tb.seen = true
	}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := color.NRGBAModel.Convert(src.At(x, y)).(color.NRGBA)
			if c.A == 0 || (c.R >= tb.t && c.G >= tb.t && c.B >= tb.t) {
				continue
			}
			if x < tb.minX {
				tb.minX = x
			}
			if x > tb.maxX {
				tb.maxX = x
			}
			if y < tb.minY {
				tb.minY = y
			}
			if y > tb.maxY {
				tb.maxY = y
			}
		}
	}
}

// trimBytes cuts the margin. Every frame of an animated GIF is cut to one
// box: the one that holds what is left of every frame.
func trimBytes(in, outFormat string, quality, threshold, padding int) any {
	if threshold < 0 || threshold > 255 {
		return Err[any, any](ErrInvalidInput("image.trim: threshold must be 0..255"))
	}
	if padding < 0 {
		return Err[any, any](ErrInvalidInput("image.trim: padding must not be negative"))
	}
	tb := &trimBox{t: uint8(threshold)}
	return imageApply("image.trim", in, outFormat, quality, false, false, tb.add,
		func(src image.Image) (image.Image, any) {
			if tb.maxX < tb.minX || tb.maxY < tb.minY {
				return nil, Err[any, any](ErrInvalidInput("nothing left to trim to"))
			}
			b := src.Bounds()
			keep := image.Rect(tb.minX-padding, tb.minY-padding, tb.maxX+1+padding, tb.maxY+1+padding).Intersect(b)
			return copyRect(src, keep), nil
		})
}

func cropBytes(in, outFormat string, quality, x, y, w, h int) any {
	if w < 0 || h < 0 {
		return Err[any, any](ErrInvalidInput("image.crop: width and height must not be negative"))
	}
	return imageApply("image.crop", in, outFormat, quality, false, false, nil,
		func(src image.Image) (image.Image, any) {
			b := src.Bounds()
			// x and y are relative to the image's top-left corner.
			r := image.Rect(b.Min.X+x, b.Min.Y+y, b.Min.X+x+w, b.Min.Y+y+h).Intersect(b)
			if r.Empty() {
				return nil, Err[any, any](ErrInvalidInput("image.crop: the area is outside the image or empty"))
			}
			return copyRect(src, r), nil
		})
}

// toAlphaBytes writes PNG. An animated GIF stays an animated GIF (each frame
// keyed, with one-bit transparency), every frame written whole with disposal
// "background", because keying makes pixels transparent that an earlier
// frame covered.
func toAlphaBytes(in string, kr, kg, kb int) any {
	for _, k := range []int{kr, kg, kb} {
		if k < 0 || k > 255 {
			return Err[any, any](ErrInvalidInput("image.toAlpha: each colour channel must be 0..255"))
		}
	}
	key := [3]float64{float64(kr), float64(kg), float64(kb)}
	return imageApply("image.toAlpha", in, "png", 0, true, true, nil,
		func(src image.Image) (image.Image, any) {
			b := src.Bounds()
			dst := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
			for y := b.Min.Y; y < b.Max.Y; y++ {
				for x := b.Min.X; x < b.Max.X; x++ {
					c := color.NRGBAModel.Convert(src.At(x, y)).(color.NRGBA)
					dst.SetNRGBA(x-b.Min.X, y-b.Min.Y, colourToAlpha(c, key))
				}
			}
			return dst, nil
		})
}

// colourToAlpha is GIMP's "colour to alpha" for one pixel against key k. The
// pixel is first flattened onto k (if it is partly transparent). Per channel
// the coverage needed to reach c from k is (c-k)/(255-k) above k and
// (k-c)/k below it; the pixel's alpha is the largest, and its colour is the
// one that, laid over k at that alpha, gives c back. Computed in floats, then
// clamped to 0..255 and rounded to the nearest value (clampByte).
func colourToAlpha(c color.NRGBA, k [3]float64) color.NRGBA {
	a0 := float64(c.A) / 255
	ch := [3]float64{
		float64(c.R)*a0 + k[0]*(1-a0),
		float64(c.G)*a0 + k[1]*(1-a0),
		float64(c.B)*a0 + k[2]*(1-a0),
	}
	alpha := 0.0
	for i := 0; i < 3; i++ {
		var ai float64
		switch {
		case ch[i] > k[i]:
			ai = (ch[i] - k[i]) / (255 - k[i]) // k < 255 here
		case ch[i] < k[i]:
			ai = (k[i] - ch[i]) / k[i] // k > 0 here
		}
		if ai > alpha {
			alpha = ai
		}
	}
	if alpha <= 0 {
		return color.NRGBA{}
	}
	var out [3]uint8
	for i := 0; i < 3; i++ {
		out[i] = clampByte(k[i] + (ch[i]-k[i])/alpha)
	}
	return color.NRGBA{R: out[0], G: out[1], B: out[2], A: clampByte(alpha * 255)}
}
