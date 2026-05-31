// Package rt — Std.Compression runtime kernels.
//
// v0.15.47 stdlib batch (#380): gzip (compress/gzip) +
// zstd (klauspost/compress/zstd). Both operate on raw bytes
// (Sky.Core.Bytes alias = String), so kernels read the input as a
// Go string and return Ok[string] / Err[Error].
package rt

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"

	"github.com/klauspost/compress/zstd"
)

// Compression_gzip implements:
//
//	Std.Compression.gzip : Bytes -> Task Error Bytes
func Compression_gzip(inputArg any) any {
	return func() any {
		in := asBytesString(inputArg)
		var buf bytes.Buffer
		w := gzip.NewWriter(&buf)
		if _, err := w.Write([]byte(in)); err != nil {
			_ = w.Close()
			return Err[any, any](ErrFfi(fmt.Sprintf("compression.gzip: %v", err)))
		}
		if err := w.Close(); err != nil {
			return Err[any, any](ErrFfi(fmt.Sprintf("compression.gzip close: %v", err)))
		}
		return Ok[any, any](buf.String())
	}
}

// Compression_gunzip implements:
//
//	Std.Compression.gunzip : Bytes -> Task Error Bytes
func Compression_gunzip(inputArg any) any {
	return func() any {
		in := asBytesString(inputArg)
		r, err := gzip.NewReader(bytes.NewReader([]byte(in)))
		if err != nil {
			return Err[any, any](ErrInvalidInput("compression.gunzip: " + err.Error()))
		}
		defer r.Close()
		out, err := io.ReadAll(r)
		if err != nil {
			return Err[any, any](ErrInvalidInput("compression.gunzip: " + err.Error()))
		}
		return Ok[any, any](string(out))
	}
}

// Compression_zstdCompress implements:
//
//	Std.Compression.zstdCompress : Bytes -> Task Error Bytes
func Compression_zstdCompress(inputArg any) any {
	return func() any {
		in := asBytesString(inputArg)
		enc, err := zstd.NewWriter(nil)
		if err != nil {
			return Err[any, any](ErrFfi("compression.zstd encoder: " + err.Error()))
		}
		out := enc.EncodeAll([]byte(in), make([]byte, 0, len(in)/2+128))
		_ = enc.Close()
		return Ok[any, any](string(out))
	}
}

// Compression_zstdDecompress implements:
//
//	Std.Compression.zstdDecompress : Bytes -> Task Error Bytes
func Compression_zstdDecompress(inputArg any) any {
	return func() any {
		in := asBytesString(inputArg)
		dec, err := zstd.NewReader(nil)
		if err != nil {
			return Err[any, any](ErrFfi("compression.zstd decoder: " + err.Error()))
		}
		defer dec.Close()
		out, err := dec.DecodeAll([]byte(in), nil)
		if err != nil {
			return Err[any, any](ErrInvalidInput("compression.zstd: " + err.Error()))
		}
		return Ok[any, any](string(out))
	}
}

// asBytesString coerces any Sky value to a Go string. Bytes alias =
// String in Sky-side, so the input is always already string-shaped
// (or coercible via fmt.%v).
func asBytesString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}
