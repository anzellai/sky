package rt

import "testing"

// RFC 4648 §10 test vectors, BASE32 and BASE32-HEX.
var rfc4648Base32 = []struct{ in, b32, b32hex string }{
	{"", "", ""},
	{"f", "MY======", "CO======"},
	{"fo", "MZXQ====", "CPNG===="},
	{"foo", "MZXW6===", "CPNMU==="},
	{"foob", "MZXW6YQ=", "CPNMUOG="},
	{"fooba", "MZXW6YTB", "CPNMUOJ1"},
	{"foobar", "MZXW6YTBOI======", "CPNMUOJ1E8======"},
}

func b32ok(t *testing.T, r any) string {
	t.Helper()
	tag, v, e := anyResultView(r)
	if tag != 0 {
		t.Fatalf("got Err %v", e)
	}
	return AsString(v)
}

func b32err(t *testing.T, r any, in string) {
	t.Helper()
	if tag, v, _ := anyResultView(r); tag == 0 {
		t.Fatalf("decode %q: got Ok %q, want Err", in, AsString(v))
	}
}

func TestBase32_RFC4648Vectors(t *testing.T) {
	for _, v := range rfc4648Base32 {
		if got := AsString(Encoding_base32Encode(v.in)); got != v.b32 {
			t.Errorf("base32Encode(%q) = %q, want %q", v.in, got, v.b32)
		}
		if got := b32ok(t, Encoding_base32Decode(v.b32)); got != v.in {
			t.Errorf("base32Decode(%q) = %q, want %q", v.b32, got, v.in)
		}
		if got := AsString(Encoding_base32HexEncode(v.in)); got != v.b32hex {
			t.Errorf("base32HexEncode(%q) = %q, want %q", v.in, got, v.b32hex)
		}
		if got := b32ok(t, Encoding_base32HexDecode(v.b32hex)); got != v.in {
			t.Errorf("base32HexDecode(%q) = %q, want %q", v.b32hex, got, v.in)
		}
		// The unpadded form is the padded form without '='.
		nopad := trimPad(v.b32)
		if got := AsString(Encoding_base32EncodeNoPad(v.in)); got != nopad {
			t.Errorf("base32EncodeNoPad(%q) = %q, want %q", v.in, got, nopad)
		}
		if got := b32ok(t, Encoding_base32DecodeNoPad(nopad)); got != v.in {
			t.Errorf("base32DecodeNoPad(%q) = %q, want %q", nopad, got, v.in)
		}
	}
}

func trimPad(s string) string {
	for len(s) > 0 && s[len(s)-1] == '=' {
		s = s[:len(s)-1]
	}
	return s
}

func TestBase32_BinaryRoundTrip(t *testing.T) {
	b := make([]byte, 256)
	for i := range b {
		b[i] = byte(i)
	}
	s := string(b)
	for n := 0; n <= len(s); n += 37 {
		in := s[:n]
		if got := b32ok(t, Encoding_base32Decode(Encoding_base32Encode(in))); got != in {
			t.Fatalf("padded round trip failed at %d bytes", n)
		}
		if got := b32ok(t, Encoding_base32DecodeNoPad(Encoding_base32EncodeNoPad(in))); got != in {
			t.Fatalf("unpadded round trip failed at %d bytes", n)
		}
		if got := b32ok(t, Encoding_base32HexDecode(Encoding_base32HexEncode(in))); got != in {
			t.Fatalf("base32hex round trip failed at %d bytes", n)
		}
	}
}

// Every text that is not the canonical encoding of some bytes is an Err.
func TestBase32_DecodeRejectsInvalidInput(t *testing.T) {
	for _, in := range []string{
		"mzxw6===",   // lower case
		"MZXW6==",    // padding short of a multiple of 8
		"MZXW6",      // unpadded input to the padded decoder
		"MZXW6===X",  // text after the padding
		"MZ1W6===",   // '1' is not in the alphabet
		"MZ======",   // non-zero unused bits ("f" is MY======)
		"MZXW\n6===", // line break (encoding/base32 alone skips it)
		"M=======",   // one symbol cannot hold a byte
		"=",
	} {
		b32err(t, Encoding_base32Decode(in), in)
	}
	for _, in := range []string{"MZXW6===", "MY======", "mzxw6", "MZ", "M"} {
		b32err(t, Encoding_base32DecodeNoPad(in), in)
	}
	for _, in := range []string{"MZXW6===", "CPNMW===", "cpnmu==="} {
		b32err(t, Encoding_base32HexDecode(in), in)
	}
}
