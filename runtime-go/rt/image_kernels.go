// Package rt — Std.Image runtime kernels.
//
// Backend (Go) image resize + thumbnail, so a Sky app downsizes an uploaded
// image on the server rather than in client JS. Modelled on Std.Compression
// (compression.go): operate on raw bytes (Sky.Core.Bytes alias = String), read
// the input as a Go string, return a Task thunk yielding Ok[string] / Err[Error].
//
// Decode uses the Go stdlib (image + the jpeg/png decoders registered by the
// blank imports below); scaling uses golang.org/x/image/draw (CatmullRom, a
// high-quality resampling kernel). The source format is preserved on re-encode
// (jpeg->jpeg at quality 85, png->png). A non-image / corrupt / unsupported
// input is a classified InvalidInput error, never a panic.
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

	_ "image/gif" // register the GIF decoder (a GIF re-encodes as PNG below)

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
// The Sky-level `Format` option (Jpeg q / Png / Preserve) is translated to the
// `format` string + `quality` int by the Std.Image wrappers, so the FFI boundary
// stays primitive. Scale to fit within maxW x maxH, preserving aspect ratio and
// NEVER upscaling. `format`: "jpeg" (at `quality`, 1..100, else 85), "png"
// (lossless; keeps transparency), or "preserve" (jpeg->jpeg q85, png/gif->png).
func Image_resize(formatArg, qualityArg, maxWArg, maxHArg, inputArg any) any {
	return func() any {
		return resizeBytes(
			asBytesString(inputArg),
			AsString(formatArg),
			AsInt(qualityArg),
			AsInt(maxWArg),
			AsInt(maxHArg),
		)
	}
}

// Image_dimensions implements:
//
//	Std.Image.dimensions : Bytes -> Task Error { width : Int, height : Int }
//
// Read only the image header (cheap, no full decode). Returns a record as a
// map[string]any, narrowed to the caller's `{ width, height }` record at the Sky
// boundary (the same map->record path Csv/Cache kernels use). A Task (thunk) like
// the other Std.Image entries: the whole module is a backend capability (the
// codec is server-only), so every function is an effect — uniform to compose and
// honest about running server-side in the Sky.Spa split.
func Image_dimensions(inputArg any) any {
	return func() any {
		cfg, _, err := image.DecodeConfig(bytes.NewReader([]byte(asBytesString(inputArg))))
		if err != nil {
			return Err[any, any](ErrInvalidInput("image.dimensions: not a decodable image: " + err.Error()))
		}
		return Ok[any, any](map[string]any{"width": cfg.Width, "height": cfg.Height})
	}
}

// resizeBytes decodes, scales-to-fit (no upscale), and re-encodes in the chosen
// output format.
func resizeBytes(in, outFormat string, quality, maxW, maxH int) any {
	if maxW <= 0 || maxH <= 0 {
		return Err[any, any](ErrInvalidInput("image.resize: max width and height must be positive"))
	}
	src, srcFormat, err := image.Decode(bytes.NewReader([]byte(in)))
	if err != nil {
		return Err[any, any](ErrInvalidInput("image.resize: not a decodable image: " + err.Error()))
	}
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw <= 0 || sh <= 0 {
		return Err[any, any](ErrInvalidInput("image.resize: image has zero dimension"))
	}
	tw, th := fitWithin(sw, sh, maxW, maxH)
	var dst image.Image
	if tw == sw && th == sh {
		dst = src
	} else {
		rgba := image.NewRGBA(image.Rect(0, 0, tw, th))
		draw.CatmullRom.Scale(rgba, rgba.Bounds(), src, b, draw.Over, nil)
		dst = rgba
	}
	// Resolve the output encoder. "preserve" maps the source format to its own
	// encoder (gif has no encoder here, so it becomes png).
	enc := outFormat
	if enc == "" || enc == "preserve" {
		if srcFormat == "jpeg" {
			enc = "jpeg"
		} else {
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
			return Err[any, any](ErrFfi("image.resize: jpeg encode: " + err.Error()))
		}
	case "png":
		if err := png.Encode(&out, dst); err != nil {
			return Err[any, any](ErrFfi("image.resize: png encode: " + err.Error()))
		}
	default:
		return Err[any, any](ErrInvalidInput("image.resize: unsupported output format " + enc))
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
