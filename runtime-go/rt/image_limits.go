// Package rt — Std.Image decode limits (decompression bombs).
//
// A small file can declare a huge image: a 50 kB PNG may say it is
// 100,000 x 100,000 pixels, and decoding it allocates 40 GB. A GIF of
// 1 x 1 pixels may carry 100,000 frames. Every Std.Image and
// Std.Image.Canvas entry that decodes reads the header first
// (image.DecodeConfig: the IHDR of a PNG, the SOF of a JPEG, the logical
// screen of a GIF, the VP8/VP8L/VP8X header of a WebP) and refuses an image
// over the limits below BEFORE the full decode allocates anything. A GIF's
// frames are counted by a scan of its block structure (gifScan, no LZW
// decode), and counted again while the frames are decoded.
//
// A refusal is an `Error` of kind InvalidInput in the domain "image" with a
// code that names the limit (too_many_pixels, too_many_frames,
// too_many_frame_pixels) and the attributes `limit` and `value`.
//
//go:build !js

package rt

import (
	"bytes"
	"image"
	"strconv"
)

const (
	// imageMaxPixels bounds width x height of one image, one GIF logical
	// screen and one canvas: 100 megapixels (400 MB as RGBA).
	imageMaxPixels = 100_000_000
	// imageMaxFrames bounds the frames of one animated GIF.
	imageMaxFrames = 1000
	// imageMaxFramePixels bounds width x height x frames of one animated GIF,
	// the work an animation costs: 250 million pixels in all.
	imageMaxFramePixels = 250_000_000

	imageCodeTooManyPixels      = "too_many_pixels"
	imageCodeTooManyFrames      = "too_many_frames"
	imageCodeTooManyFramePixels = "too_many_frame_pixels"
)

// imageDomainErr builds `Err InvalidInput` in the domain "image".
func imageDomainErr(code, msg string, attrs [][2]string) any {
	df := DomainFailure{Domain: "image", Code: code, RetryAfterMs: -1, Attrs: attrs}
	return Err[any, any](ErrDomain(errKindInvalidInput, msg, df))
}

func imageLimitErr(op, code, what string, limit, value int64) any {
	return imageDomainErr(code,
		op+": the image has "+strconv.FormatInt(value, 10)+" "+what+", over the limit of "+
			strconv.FormatInt(limit, 10)+" ("+code+")",
		[][2]string{
			{"limit", strconv.FormatInt(limit, 10)},
			{"value", strconv.FormatInt(value, 10)},
		})
}

// imageCheckPixels refuses width x height over imageMaxPixels.
func imageCheckPixels(op string, w, h int) any {
	if w < 0 || h < 0 {
		return Err[any, any](ErrInvalidInput(op + ": the image has a negative dimension"))
	}
	if n := int64(w) * int64(h); n > imageMaxPixels {
		return imageLimitErr(op, imageCodeTooManyPixels, "pixels", imageMaxPixels, n)
	}
	return nil
}

// imageConfig reads the header and applies the pixel limit. It never decodes
// the pixel data.
func imageConfig(op, in string) (image.Config, string, any) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader([]byte(in)))
	if err != nil {
		return image.Config{}, "", Err[any, any](ErrInvalidInput(op + ": not a decodable image: " + err.Error()))
	}
	if bad := imageCheckPixels(op, cfg.Width, cfg.Height); bad != nil {
		return image.Config{}, "", bad
	}
	return cfg, format, nil
}

// imageDecodeLimited decodes a still image under the limits. A GIF gives its
// first frame composed onto the logical screen (frames are not counted: one
// frame is decoded).
func imageDecodeLimited(op, in string) (image.Image, string, any) {
	_, format, bad := imageConfig(op, in)
	if bad != nil {
		return nil, "", bad
	}
	if format == "gif" {
		img, gbad := gifFirstFrame(op, []byte(in))
		if gbad != nil {
			return nil, "", gbad
		}
		return img, format, nil
	}
	src, format, err := image.Decode(bytes.NewReader([]byte(in)))
	if err != nil {
		return nil, "", Err[any, any](ErrInvalidInput(op + ": not a decodable image: " + err.Error()))
	}
	b := src.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 {
		return nil, "", Err[any, any](ErrInvalidInput(op + ": image has zero dimension"))
	}
	return src, format, nil
}
