// Package rt — Std.Image runtime kernels.
//
// Backend (Go) image resize + thumbnail, so a Sky app downsizes an uploaded
// image on the server rather than in client JS. Modelled on Std.Compression
// (compression.go): operate on raw bytes (Sky.Core.Bytes alias = String), read
// the input as a Go string, return a Task thunk yielding Ok[string] / Err[Error].
//
// Decode uses the Go stdlib (image + the jpeg/png/gif decoders registered by
// the imports below) and golang.org/x/image/webp; scaling uses
// golang.org/x/image/draw (CatmullRom, a high-quality resampling kernel).
// Output formats: "jpeg" (at a quality), "png", "gif", "webp" (lossless,
// webp_vp8l_encode.go) and "preserve" (jpeg -> jpeg at 85, gif -> gif with
// every frame, webp -> lossless webp, anything else -> png). A non-image /
// corrupt / unsupported input is a classified InvalidInput error, never a
// panic; an image over the decode limits is refused before it is decoded
// (image_limits.go).
//
// An animated GIF kept as a GIF ("gif" or "preserve") is processed frame by
// frame (image_gif.go): each operation runs on every composed frame and the
// delays, disposals and loop count are kept. A GIF written as a still format
// gives its first composed frame.
//
// Classified SERVER in the Sky.Spa split (spa_partition EFFECT_KERNELS "Image"),
// so this codec and golang.org/x/image stay in the native backend and never ship
// in the wasm client. The `!js` build tag makes that physical: the wasm client
// never references these kernels (a server branch reaches them behind RPC), so
// excluding the file from the js build keeps the image codec + x/image out of
// the client binary entirely.
//
//go:build !js

package rt

import (
	"bytes"
	"image"
	"image/jpeg"
	"image/png"

	_ "golang.org/x/image/webp" // register the WebP decoder (lossy and lossless)

	"golang.org/x/image/draw"
)

// defaultJpegQuality is the JPEG quality for the "preserve" format and the
// fallback when a caller passes a non-positive quality.
const defaultJpegQuality = 85

// Image_resize is the kernel behind Std.Image.resizeToFit / .thumbnail:
//
//	Image_resize : String -> Int -> Int -> Int -> Bytes -> Task Error Bytes
//	               (format  quality maxW  maxH  bytes)
//
// The Sky-level `Format` option is translated to the `format` string +
// `quality` int by the Std.Image wrappers, so the FFI boundary stays
// primitive. Scale to fit within maxW x maxH, preserving aspect ratio and
// NEVER upscaling.
func Image_resize(formatArg, qualityArg, maxWArg, maxHArg, inputArg any) any {
	return func() any {
		return imageGuard("image.resize", func() any {
			return resizeBytes(
				asBytesString(inputArg),
				AsString(formatArg),
				AsInt(qualityArg),
				AsInt(maxWArg),
				AsInt(maxHArg),
			)
		})
	}
}

// Image_dimensions implements:
//
//	Std.Image.dimensions : Bytes -> Task Error { width : Int, height : Int }
//
// Read only the image header (cheap, no full decode, no size limit: it is how
// an app looks at an image before it decides to process it). Returns a record
// as a map[string]any, narrowed to the caller's `{ width, height }` record at
// the Sky boundary (the same map->record path Csv/Cache kernels use).
func Image_dimensions(inputArg any) any {
	return func() any {
		cfg, _, err := image.DecodeConfig(bytes.NewReader([]byte(asBytesString(inputArg))))
		if err != nil {
			return Err[any, any](ErrInvalidInput("image.dimensions: not a decodable image: " + err.Error()))
		}
		return Ok[any, any](map[string]any{"width": cfg.Width, "height": cfg.Height})
	}
}

// Image_frames implements:
//
//	Std.Image.frames : Bytes -> Task Error Int
//
// The number of frames: 1 for a still image, the frame count of a GIF. It
// scans the GIF's blocks (no decode, no frame limit), so an app can show
// "a GIF of N frames" or refuse it before any work.
func Image_frames(inputArg any) any {
	return func() any {
		return imageGuard("image.frames", func() any {
			in := asBytesString(inputArg)
			_, format, err := image.DecodeConfig(bytes.NewReader([]byte(in)))
			if err != nil {
				return Err[any, any](ErrInvalidInput("image.frames: not a decodable image: " + err.Error()))
			}
			if format != "gif" {
				return Ok[any, any](1)
			}
			s, bad := gifScan("image.frames", []byte(in), gifScanLimits{})
			if bad != nil {
				return bad
			}
			return Ok[any, any](len(s.frames))
		})
	}
}

// Image_reduceFrames implements:
//
//	Std.Image.reduceFrames : Int -> Bytes -> Task Error Bytes
//
// Keep frame 0, n, 2n, ... of a GIF; a kept frame shows for its own delay plus
// the delays of the frames dropped after it, so the animation keeps its
// length. Every kept frame is written whole, with disposal "background". An
// input that is not a GIF comes back unchanged.
func Image_reduceFrames(nArg, inputArg any) any {
	return func() any {
		return imageGuard("image.reduceFrames", func() any {
			return reduceFramesBytes(asBytesString(inputArg), AsInt(nArg))
		})
	}
}

func reduceFramesBytes(in string, n int) any {
	const op = "image.reduceFrames"
	if n < 1 {
		return Err[any, any](ErrInvalidInput(op + ": keep 1 frame in n needs n >= 1"))
	}
	_, format, bad := imageConfig(op, in)
	if bad != nil {
		return bad
	}
	if format != "gif" {
		return Ok[any, any](in)
	}
	data := []byte(in)
	s, bad := gifScan(op, data, gifDefaultLimits)
	if bad != nil {
		return bad
	}
	return gifAnimate(op, data, s,
		func(img image.Image) (image.Image, any) { return img, nil },
		func(i int) bool { return i%n == 0 }, true)
}

// imageApply runs `fn` on a decoded image and writes the result.
//
// When the source is a GIF and the output is "gif" or "preserve" (or
// keepAnimation is set and the GIF has more than one frame), every composed
// frame goes through `fn` and the output is a GIF with the source's timing.
// Otherwise a still image (a GIF's first composed frame) goes through `fn`,
// and the output format is `outFormat`.
//
// prepass, when not nil, sees every image `fn` will see, first (trim uses it
// to find one box that holds every frame).
func imageApply(op, in, outFormat string, quality int, keepAnimation, forceBackground bool,
	prepass func(image.Image), fn func(image.Image) (image.Image, any)) any {
	_, srcFormat, bad := imageConfig(op, in)
	if bad != nil {
		return bad
	}
	if srcFormat == "gif" {
		data := []byte(in)
		s, bad := gifScan(op, data, gifDefaultLimits)
		if bad != nil {
			return bad
		}
		if outFormat == "gif" || outFormat == "preserve" || outFormat == "" ||
			(keepAnimation && len(s.frames) > 1) {
			if prepass != nil {
				if bad := gifEachFrame(op, data, s, func(_ int, img *image.NRGBA) any {
					prepass(img)
					return nil
				}); bad != nil {
					return bad
				}
			}
			return gifAnimate(op, data, s, fn, nil, forceBackground)
		}
	}
	src, srcFormat, bad := imageDecodeLimited(op, in)
	if bad != nil {
		return bad
	}
	if prepass != nil {
		prepass(src)
	}
	out, bad := fn(src)
	if bad != nil {
		return bad
	}
	return encodeImage(op, out, outFormat, srcFormat, quality)
}

// resizeBytes decodes, scales-to-fit (no upscale), and re-encodes in the chosen
// output format.
func resizeBytes(in, outFormat string, quality, maxW, maxH int) any {
	const op = "image.resize"
	if maxW <= 0 || maxH <= 0 {
		return Err[any, any](ErrInvalidInput("image.resize: max width and height must be positive"))
	}
	return imageApply(op, in, outFormat, quality, false, false, nil,
		func(src image.Image) (image.Image, any) {
			b := src.Bounds()
			sw, sh := b.Dx(), b.Dy()
			if sw <= 0 || sh <= 0 {
				return nil, Err[any, any](ErrInvalidInput("image.resize: image has zero dimension"))
			}
			tw, th := fitWithin(sw, sh, maxW, maxH)
			if tw == sw && th == sh {
				return src, nil
			}
			rgba := image.NewRGBA(image.Rect(0, 0, tw, th))
			draw.CatmullRom.Scale(rgba, rgba.Bounds(), src, b, draw.Over, nil)
			return rgba, nil
		})
}

// encodeImage writes `dst` in the chosen output format and wraps the bytes as
// the kernel's Result. "preserve" maps the source format to its own encoder
// (jpeg, gif and webp keep theirs; anything else becomes png); a JPEG quality
// outside 1..100 uses defaultJpegQuality. `op` names the Sky entry point in
// the error.
func encodeImage(op string, dst image.Image, outFormat, srcFormat string, quality int) any {
	enc := outFormat
	if enc == "" || enc == "preserve" {
		switch srcFormat {
		case "jpeg", "gif", "webp":
			enc = srcFormat
		default:
			enc = "png"
		}
	}
	q := quality
	if q <= 0 || q > 100 {
		q = defaultJpegQuality
	}
	var out bytes.Buffer
	switch enc {
	case "jpeg":
		if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: q}); err != nil {
			return Err[any, any](ErrFfi(op + ": jpeg encode: " + err.Error()))
		}
	case "png":
		if err := png.Encode(&out, dst); err != nil {
			return Err[any, any](ErrFfi(op + ": png encode: " + err.Error()))
		}
	case "gif":
		s, err := gifStill(dst)
		if err != nil {
			return Err[any, any](ErrFfi(op + ": gif encode: " + err.Error()))
		}
		return Ok[any, any](s)
	case "webp":
		if err := webpLosslessEncode(&out, dst); err != nil {
			return Err[any, any](ErrFfi(op + ": webp encode: " + err.Error()))
		}
	default:
		return Err[any, any](ErrInvalidInput(op + ": unsupported output format " + enc))
	}
	return Ok[any, any](out.String())
}

// fitWithin returns the largest (w,h) with the same aspect ratio as (sw,sh) that
// fits inside (maxW,maxH). It never enlarges: a source already within the box
// returns its own dimensions.
func fitWithin(sw, sh, maxW, maxH int) (int, int) {
	if sw <= maxW && sh <= maxH {
		return sw, sh
	}
	// Scale by the tighter of the two ratios (float to avoid integer-truncation
	// bias), then round and clamp to >= 1 so a very wide/tall image never yields
	// a zero side.
	rw := float64(maxW) / float64(sw)
	rh := float64(maxH) / float64(sh)
	r := rw
	if rh < rw {
		r = rh
	}
	tw := int(float64(sw)*r + 0.5)
	th := int(float64(sh)*r + 0.5)
	if tw < 1 {
		tw = 1
	}
	if th < 1 {
		th = 1
	}
	return tw, th
}
