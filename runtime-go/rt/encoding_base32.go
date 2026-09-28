package rt

// Base32 (RFC 4648 §6) and base32hex (§7) for Sky.Core.Encoding.
//
// The decoders are strict: the input must be the CANONICAL encoding of its
// bytes. encoding/base32 on its own accepts two kinds of text that are not
// canonical: it skips '\r' and '\n' anywhere in the input, and it ignores the
// unused low bits of the last symbol, so "MY======" and "MZ======" both
// decode to "f". A decode here re-encodes its result and compares, so each
// byte string has exactly one accepted text. That matters where base32
// carries an identifier or a key (a TOTP secret, a content address): two
// different strings must never name the same bytes.

import (
	"encoding/base32"
	"fmt"
)

var (
	base32Std      = base32.StdEncoding
	base32StdNoPad = base32.StdEncoding.WithPadding(base32.NoPadding)
	base32Hex      = base32.HexEncoding
)

func base32DecodeStrict(enc *base32.Encoding, name, s string) any {
	data, err := enc.DecodeString(s)
	if err != nil {
		return Err[any, any](ErrInvalidInput(fmt.Sprintf("Encoding.%s: not valid base32: %v", name, err)))
	}
	if enc.EncodeToString(data) != s {
		return Err[any, any](ErrInvalidInput(fmt.Sprintf("Encoding.%s: not the canonical base32 form "+
			"(a line break, the wrong padding, or non-zero unused bits in the last symbol)", name)))
	}
	return Ok[any, any](string(data))
}

// Encoding_base32Encode : String -> String — RFC 4648 base32, standard
// alphabet (A-Z 2-7), padded with '=' to a multiple of 8.
func Encoding_base32Encode(s any) any { return base32Std.EncodeToString([]byte(AsString(s))) }

// Encoding_base32Decode : String -> Result Error String — the inverse of
// base32Encode; the input must be padded.
func Encoding_base32Decode(s any) any {
	return base32DecodeStrict(base32Std, "base32Decode", AsString(s))
}

// Encoding_base32EncodeNoPad : String -> String — base32 without '='.
func Encoding_base32EncodeNoPad(s any) any { return base32StdNoPad.EncodeToString([]byte(AsString(s))) }

// Encoding_base32DecodeNoPad : String -> Result Error String — the inverse of
// base32EncodeNoPad; padded input is an Err.
func Encoding_base32DecodeNoPad(s any) any {
	return base32DecodeStrict(base32StdNoPad, "base32DecodeNoPad", AsString(s))
}

// Encoding_base32HexEncode : String -> String — RFC 4648 §7 base32hex
// (0-9 A-V, sorts like the bytes), padded.
func Encoding_base32HexEncode(s any) any { return base32Hex.EncodeToString([]byte(AsString(s))) }

// Encoding_base32HexDecode : String -> Result Error String — the inverse of
// base32HexEncode.
func Encoding_base32HexDecode(s any) any {
	return base32DecodeStrict(base32Hex, "base32HexDecode", AsString(s))
}
