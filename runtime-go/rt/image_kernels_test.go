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

// forceTask runs a Task-thunk kernel result (func() any), as the runtime does.
func forceImageTask(t *testing.T, v any) any {
	t.Helper()
	thunk, ok := v.(func() any)
	if !ok {
		t.Fatalf("expected a Task thunk (func() any), got %T", v)
	}
	return thunk()
}

// okBytes asserts an Ok[string] result and returns the bytes.
func okBytes(t *testing.T, res any) []byte {
	t.Helper()
	m, ok := res.(SkyResult[any, any])
	if !ok {
		t.Fatalf("expected SkyResult[any, any], got %T (%v)", res, res)
	}
	if m.Tag != 0 { // 0 = Ok
		t.Fatalf("expected Ok, got Err: %v", m.ErrValue)
	}
	s, ok := m.OkValue.(string)
	if !ok {
		t.Fatalf("Ok value is %T, want string", m.OkValue)
	}
	return []byte(s)
}

func genJPEG(t *testing.T, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x % 256), uint8(y % 256), 128, 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode source jpeg: %v", err)
	}
	return buf.String()
}

func genPNG(t *testing.T, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode source png: %v", err)
	}
	return buf.String()
}

func decodeDims(t *testing.T, b []byte) (int, int, string) {
	t.Helper()
	cfg, format, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("result is not a decodable image: %v", err)
	}
	return cfg.Width, cfg.Height, format
}

// resize downscales within the box, preserves aspect; "preserve" keeps jpeg.
func TestImageResizeDownscalesPreservingAspect(t *testing.T) {
	src := genJPEG(t, 2000, 1000) // 2:1
	out := okBytes(t, forceImageTask(t, Image_resize("preserve", 0, 800, 800, src)))
	w, h, format := decodeDims(t, out)
	if format != "jpeg" {
		t.Fatalf("format = %q, want jpeg (preserve source format)", format)
	}
	// 2:1 within 800x800 -> width is the binding side: 800x400.
	if w != 800 || h != 400 {
		t.Fatalf("result %dx%d, want 800x400 (aspect preserved, width-bound)", w, h)
	}
}

// An explicit Jpeg format re-encodes a PNG source AS jpeg (the darraghstudio
// policy: photos -> jpeg regardless of upload format).
func TestImageResizePngToJpeg(t *testing.T) {
	src := genPNG(t, 1200, 600)
	out := okBytes(t, forceImageTask(t, Image_resize("jpeg", 85, 400, 400, src)))
	w, h, format := decodeDims(t, out)
	if format != "jpeg" {
		t.Fatalf("format = %q, want jpeg (explicit format wins over the png source)", format)
	}
	if w != 400 || h != 200 {
		t.Fatalf("result %dx%d, want 400x200", w, h)
	}
}

// An explicit Png format keeps PNG (transparency-preserving path).
func TestImageResizeToPng(t *testing.T) {
	src := genJPEG(t, 800, 400)
	out := okBytes(t, forceImageTask(t, Image_resize("png", 0, 400, 400, src)))
	_, _, format := decodeDims(t, out)
	if format != "png" {
		t.Fatalf("format = %q, want png (explicit)", format)
	}
}

// An image already within the box is NOT upscaled.
func TestImageResizeNeverUpscales(t *testing.T) {
	src := genJPEG(t, 300, 200)
	out := okBytes(t, forceImageTask(t, Image_resize("preserve", 0, 4000, 4000, src)))
	w, h, _ := decodeDims(t, out)
	if w != 300 || h != 200 {
		t.Fatalf("result %dx%d, want the source 300x200 (no upscaling)", w, h)
	}
}

// A non-image / corrupt input is a classified InvalidInput error, not a panic.
func TestImageResizeRejectsNonImage(t *testing.T) {
	res := forceImageTask(t, Image_resize("jpeg", 85, 100, 100, "this is not an image"))
	m, ok := res.(SkyResult[any, any])
	if !ok {
		t.Fatalf("expected SkyResult, got %T", res)
	}
	if m.Tag != 1 { // 1 = Err
		t.Fatalf("expected Err for non-image input, got Ok")
	}
}

// dimensions is a Task thunk (like the resize entries) returning a record.
func TestImageDimensions(t *testing.T) {
	src := genPNG(t, 640, 480)
	res := forceImageTask(t, Image_dimensions(src))
	m, ok := res.(SkyResult[any, any])
	if !ok || m.Tag != 0 {
		t.Fatalf("expected Ok, got %v", res)
	}
	d, ok := m.OkValue.(map[string]any)
	if !ok {
		t.Fatalf("dimensions value is %T, want map[string]any", m.OkValue)
	}
	if d["width"] != 640 || d["height"] != 480 {
		t.Fatalf("dimensions = %v, want width=640 height=480", d)
	}
	if bad, ok := forceImageTask(t, Image_dimensions("nope")).(SkyResult[any, any]); !ok || bad.Tag != 1 {
		t.Fatalf("expected Err for a non-image input")
	}
}

// A non-positive bound is rejected rather than producing a zero-size image.
func TestImageResizeRejectsNonPositiveBound(t *testing.T) {
	src := genJPEG(t, 100, 100)
	res := forceImageTask(t, Image_resize("jpeg", 85, 0, 100, src))
	if m, ok := res.(SkyResult[any, any]); !ok || m.Tag != 1 {
		t.Fatalf("expected Err for a zero max width, got %v", res)
	}
}
