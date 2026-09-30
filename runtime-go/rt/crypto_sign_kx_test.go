package rt

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// Vectors: RFC 8032 §7.1 (Ed25519), RFC 7748 §5.2 + §6.1 (X25519),
// RFC 5869 Appendix A.1–A.3 (HKDF-SHA256), draft-irtf-cfrg-xchacha-03
// Appendix A.3.1 (AEAD_XChaCha20_Poly1305).

func unhex(t *testing.T, s string) string {
	t.Helper()
	b, err := hex.DecodeString(strings.Join(strings.Fields(s), ""))
	if err != nil {
		t.Fatalf("bad hex in test vector: %v", err)
	}
	return string(b)
}

func cryOk(t *testing.T, v any) any {
	t.Helper()
	r, ok := v.(SkyResult[any, any])
	if !ok {
		t.Fatalf("expected a Result, got %T", v)
	}
	if r.Tag != 0 {
		t.Fatalf("expected Ok, got Err %v", r.ErrValue)
	}
	return r.OkValue
}

func cryErr(t *testing.T, v any, want string) {
	t.Helper()
	r, ok := v.(SkyResult[any, any])
	if !ok {
		t.Fatalf("expected a Result, got %T", v)
	}
	if r.Tag != 1 {
		t.Fatalf("expected Err containing %q, got Ok", want)
	}
	if msg := fmt.Sprintf("%v", r.ErrValue); !strings.Contains(msg, want) {
		t.Fatalf("Err %q does not mention %q", msg, want)
	}
}

// ─── Ed25519 ───────────────────────────────────────────────────────

func TestSignRfc8032Vectors(t *testing.T) {
	cases := []struct{ seed, pub, msg, sig string }{
		{ // TEST 1
			"9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60",
			"d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a",
			"",
			"e5564300c360ac729086e2cc806e828a84877f1eb8e5d974d873e065224901555fb8821590a33bacc61e39701cf9b46bd25bf5f0595bbe24655141438e7a100b",
		},
		{ // TEST 2
			"4ccd089b28ff96da9db6c346ec114e0f5b8a319f35aba624da8cf6ed4fb8a6fb",
			"3d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c",
			"72",
			"92a009a9f0d4cab8720e820b5f642540a2b27b5416503f8fb3762223ebdb69da085ac1e43e15996e458f3613d0f11d8c387b2eaeb4302aeeb00d291612bb0c00",
		},
		{ // TEST 3
			"c5aa8df43f9f837bedb7442f31dcb7b166d38535076f094b85ce3a2e0b4458f7",
			"fc51cd8e6218a1a38da47ed00230f0580816ed13ba3303ac5deb911548908025",
			"af82",
			"6291d657deec24024827e69c3abe01a30ce548a284743a445e3680d7db5ac3ac18ff9b538d16f290ae67f760984dc6594a7c15e9716ed28dc027beceea1ec40a",
		},
	}
	for i, c := range cases {
		sk := cryOk(t, Sign_secretKeyFromBytes(Secret{v: unhex(t, c.seed)}))
		pk := Sign_publicKey(sk)
		if got := Sign_publicKeyToBytes(pk).(string); got != unhex(t, c.pub) {
			t.Fatalf("case %d: public key %x, want %s", i+1, got, c.pub)
		}
		msg := unhex(t, c.msg)
		sig := Sign_sign(sk, msg).(string)
		if sig != unhex(t, c.sig) {
			t.Fatalf("case %d: signature %x, want %s", i+1, sig, c.sig)
		}
		if !Sign_verify(pk, msg, sig).(bool) {
			t.Fatalf("case %d: verify rejected the RFC signature", i+1)
		}
		// Negative: a flipped signature bit, a changed message, a wrong key.
		bad := []byte(sig)
		bad[0] ^= 1
		if Sign_verify(pk, msg, string(bad)).(bool) {
			t.Fatalf("case %d: verify accepted a tampered signature", i+1)
		}
		if Sign_verify(pk, msg+"x", sig).(bool) {
			t.Fatalf("case %d: verify accepted a changed message", i+1)
		}
		other := cryOk(t, Sign_publicKeyFromBytes(unhex(t, cases[(i+1)%len(cases)].pub)))
		if Sign_verify(other, msg, sig).(bool) {
			t.Fatalf("case %d: verify accepted the wrong public key", i+1)
		}
		// A signature of the wrong length is False, never a panic.
		if Sign_verify(pk, msg, sig[:10]).(bool) {
			t.Fatalf("case %d: verify accepted a short signature", i+1)
		}
		// The seed round-trips through the export functions.
		if secretReveal(Sign_secretKeyToBytes(sk)) != unhex(t, c.seed) {
			t.Fatalf("case %d: secretKeyToBytes did not return the seed", i+1)
		}
		b64 := Sign_secretKeyToBase64(sk)
		back := cryOk(t, Sign_secretKeyFromBase64(b64))
		if Sign_sign(back, msg).(string) != sig {
			t.Fatalf("case %d: base64 round trip changed the key", i+1)
		}
	}
}

func TestSignKeyValidation(t *testing.T) {
	cryErr(t, Sign_secretKeyFromBytes(Secret{v: "short"}), "32 bytes")
	cryErr(t, Sign_secretKeyFromBase64(Secret{v: "not base64!"}), "base64")
	cryErr(t, Sign_secretKeyFromBase64(Secret{v: base64.StdEncoding.EncodeToString([]byte("short"))}), "32 bytes")
	cryErr(t, Sign_publicKeyFromBytes("short"), "32 bytes")
	// A 32-byte string that is not a curve point is rejected at import.
	cryErr(t, Sign_publicKeyFromBytes(unhex(t, "0200000000000000000000000000000000000000000000000000000000000000")), "not a valid")
}

func TestSignGenerateIsATaskAndFresh(t *testing.T) {
	task := Sign_generate(struct{}{})
	a := cryOk(t, runCryptoTaskAny(t, task))
	b := cryOk(t, runCryptoTaskAny(t, task))
	if secretReveal(Sign_secretKeyToBytes(a)) == secretReveal(Sign_secretKeyToBytes(b)) {
		t.Fatalf("Sign.generate returned the same key twice")
	}
	pk := Sign_publicKey(a)
	sig := Sign_sign(a, "hello")
	if !Sign_verify(pk, "hello", sig).(bool) {
		t.Fatalf("a generated key does not verify its own signature")
	}
}

// ─── X25519 ────────────────────────────────────────────────────────

func TestKxRfc7748Vectors(t *testing.T) {
	// §5.2: scalar × u-coordinate.
	for i, c := range []struct{ k, u, out string }{
		{"a546e36bf0527c9d3b16154b82465edd62144c0ac1fc5a18506a2244ba449ac4",
			"e6db6867583030db3594c1a424b15f7c726624ec26b3353b10a903a6d0ab1c4c",
			"c3da55379de9c6908e94ea4df28d084f32eccf03491c71f754b4075577a28552"},
		{"4b66e9d4d1b4673c5ad22691957d6af5c11b6421e0ea01d42ca4169e7918ba0d",
			"e5210f12786811d3f4b7959d0538ae2c31dbe7106fc03c3efc4cd549c715a493",
			"95cbde9476e8907d7aade45cb4b873f88b595a68799fa152e6f8f7647aac7957"},
	} {
		sk := cryOk(t, Kx_secretKeyFromBytes(Secret{v: unhex(t, c.k)}))
		pk := cryOk(t, Kx_publicKeyFromBytes(unhex(t, c.u)))
		shared := cryOk(t, Kx_sharedSecret(sk, pk))
		if secretReveal(shared) != unhex(t, c.out) {
			t.Fatalf("§5.2 case %d: got %x", i+1, secretReveal(shared))
		}
	}
	// §6.1: Alice and Bob.
	a := cryOk(t, Kx_secretKeyFromBytes(Secret{v: unhex(t, "77076d0a7318a57d3c16c17251b26645df4c2f87ebc0992ab177fba51db92c2a")}))
	b := cryOk(t, Kx_secretKeyFromBytes(Secret{v: unhex(t, "5dab087e624a8a4b79e17f8b83800ee66f3bb1292618b6fd1c2f8b27ff88e0eb")}))
	if got := Kx_publicKeyToBytes(Kx_publicKey(a)).(string); got != unhex(t, "8520f0098930a754748b7ddcb43ef75a0dbf3a0d26381af4eba4a98eaa9b4e6a") {
		t.Fatalf("Alice public key %x", got)
	}
	if got := Kx_publicKeyToBytes(Kx_publicKey(b)).(string); got != unhex(t, "de9edb7d7b7dc1b4d35b61c2ece435373f8343c85b78674dadfc7e146f882b4f") {
		t.Fatalf("Bob public key %x", got)
	}
	k1 := secretReveal(cryOk(t, Kx_sharedSecret(a, Kx_publicKey(b))))
	k2 := secretReveal(cryOk(t, Kx_sharedSecret(b, Kx_publicKey(a))))
	want := unhex(t, "4a5d9d5ba4ce2de1728e3bf480350f25e07e21c947d19e3376f09b3c1e161742")
	if k1 != want || k2 != want {
		t.Fatalf("shared secret mismatch: %x %x", k1, k2)
	}
}

// A low-order public key makes X25519 output all zeros. That value is
// predictable by an attacker, so sharedSecret refuses it with an Err.
func TestKxRejectsLowOrderPoint(t *testing.T) {
	sk := cryOk(t, Kx_secretKeyFromBytes(Secret{v: unhex(t, "77076d0a7318a57d3c16c17251b26645df4c2f87ebc0992ab177fba51db92c2a")}))
	for _, u := range []string{
		"0000000000000000000000000000000000000000000000000000000000000000",
		"0100000000000000000000000000000000000000000000000000000000000000",
		"e0eb7a7c3b41b8ae1656e3faf19fc46ada098deb9c32b1fd866205165f49b800",
		"5f9c95bca3508c24b1d0b1559c83ef5b04445cc4581c8e86d8224eddd09f1157",
		"ecffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f",
		"edffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f",
	} {
		pk := cryOk(t, Kx_publicKeyFromBytes(unhex(t, u)))
		cryErr(t, Kx_sharedSecret(sk, pk), "low-order")
	}
}

func TestKxKeyValidationAndGenerate(t *testing.T) {
	cryErr(t, Kx_secretKeyFromBytes(Secret{v: "short"}), "32 bytes")
	cryErr(t, Kx_publicKeyFromBytes("short"), "32 bytes")
	cryErr(t, Kx_secretKeyFromBase64(Secret{v: "%%%"}), "base64")
	task := Kx_generate(struct{}{})
	a := cryOk(t, runCryptoTaskAny(t, task))
	b := cryOk(t, runCryptoTaskAny(t, task))
	k1 := secretReveal(cryOk(t, Kx_sharedSecret(a, Kx_publicKey(b))))
	k2 := secretReveal(cryOk(t, Kx_sharedSecret(b, Kx_publicKey(a))))
	if k1 != k2 || len(k1) != 32 {
		t.Fatalf("generated keys do not agree")
	}
	back := cryOk(t, Kx_secretKeyFromBase64(Kx_secretKeyToBase64(a)))
	if secretReveal(Kx_secretKeyToBytes(back)) != secretReveal(Kx_secretKeyToBytes(a)) {
		t.Fatalf("base64 round trip changed the key")
	}
}

// ─── Redaction of the secret key types ─────────────────────────────

func TestSecretKeyTypesRedact(t *testing.T) {
	signSk := cryOk(t, Sign_secretKeyFromBytes(Secret{v: strings.Repeat("k", 32)}))
	kxSk := cryOk(t, Kx_secretKeyFromBytes(Secret{v: strings.Repeat("k", 32)}))
	for _, v := range []any{signSk, kxSk} {
		for _, verb := range []string{"%v", "%s", "%+v", "%#v", "%x", "%q", "%d"} {
			if out := fmt.Sprintf(verb, v); strings.Contains(out, "kkkk") || strings.Contains(out, "6b6b") || !strings.Contains(out, "REDACTED") {
				t.Fatalf("%T %s leaked: %q", v, verb, out)
			}
		}
		j, err := json.Marshal(map[string]any{"k": v})
		if err != nil || !strings.Contains(string(j), "REDACTED") || strings.Contains(string(j), "kkkk") {
			t.Fatalf("%T JSON leaked: %s %v", v, j, err)
		}
		if out := AsString(v); strings.Contains(out, "kkkk") {
			t.Fatalf("%T AsString leaked: %q", v, out)
		}
	}
	// Public keys are not secret: they print as base64 so logs stay useful.
	pk := Sign_publicKey(signSk)
	if out := fmt.Sprintf("%v", pk); !strings.Contains(out, base64.StdEncoding.EncodeToString([]byte(Sign_publicKeyToBytes(pk).(string)))) {
		t.Fatalf("public key did not print its base64: %q", out)
	}
}

// ─── HKDF-SHA256 ───────────────────────────────────────────────────

func TestKdfRfc5869Vectors(t *testing.T) {
	for i, c := range []struct {
		ikm, salt, info string
		l               int
		prk, okm        string
	}{
		{"0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b", "000102030405060708090a0b0c", "f0f1f2f3f4f5f6f7f8f9", 42,
			"077709362c2e32df0ddc3f0dc47bba6390b6c73bb50f9c3122ec844ad7c2b3e5",
			"3cb25f25faacd57a90434f64d0362f2a2d2d0a90cf1a5a4c5db02d56ecc4c5bf34007208d5b887185865"},
		{"000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f404142434445464748494a4b4c4d4e4f",
			"606162636465666768696a6b6c6d6e6f707172737475767778797a7b7c7d7e7f808182838485868788898a8b8c8d8e8f909192939495969798999a9b9c9d9e9fa0a1a2a3a4a5a6a7a8a9aaabacadaeaf",
			"b0b1b2b3b4b5b6b7b8b9babbbcbdbebfc0c1c2c3c4c5c6c7c8c9cacbcccdcecfd0d1d2d3d4d5d6d7d8d9dadbdcdddedfe0e1e2e3e4e5e6e7e8e9eaebecedeeeff0f1f2f3f4f5f6f7f8f9fafbfcfdfeff", 82,
			"06a6b88c5853361a06104c9ceb35b45cef760014904671014a193f40c15fc244",
			"b11e398dc80327a1c8e7f78c596a49344f012eda2d4efad8a050cc4c19afa97c59045a99cac7827271cb41c65e590e09da3275600c2f09b8367793a9aca3db71cc30c58179ec3e87c14c01d5c1f3434f1d87"},
		{"0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b", "", "", 42,
			"19ef24a32c717b167f33a91d6f648bdf96596776afdb6377ac434c1c293ccb04",
			"8da4e775a563c18f715f802a063c5a31b8a11f5c5ee1879ec3454e5f3c738d2d9d201395faa4b61a96c8"},
	} {
		prk := Kdf_extract(unhex(t, c.salt), Secret{v: unhex(t, c.ikm)})
		if secretReveal(prk) != unhex(t, c.prk) {
			t.Fatalf("case %d: PRK %x", i+1, secretReveal(prk))
		}
		okm := cryOk(t, Kdf_expand(prk, unhex(t, c.info), c.l))
		if secretReveal(okm) != unhex(t, c.okm) {
			t.Fatalf("case %d: OKM %x", i+1, secretReveal(okm))
		}
		if _, ok := okm.(Secret); !ok {
			t.Fatalf("case %d: expand must return a Secret, got %T", i+1, okm)
		}
	}
	prk := Kdf_extract("", Secret{v: "ikm"})
	cryErr(t, Kdf_expand(prk, "", 255*32+1), "8160")
	cryErr(t, Kdf_expand(prk, "", 0), "1..8160")
	cryErr(t, Kdf_expand(Secret{v: "short prk"}, "", 32), "32 bytes")
	okm := cryOk(t, Kdf_expand(prk, "", 255*32))
	if len(secretReveal(okm)) != 8160 {
		t.Fatalf("the maximum length did not produce 8160 bytes")
	}
}

// ─── XChaCha20-Poly1305 ────────────────────────────────────────────

func TestXChaChaDraftVector(t *testing.T) {
	pt := unhex(t, `4c616469657320616e642047656e746c656d656e206f662074686520636c6173
		73206f66202739393a204966204920636f756c64206f6666657220796f75206f
		6e6c79206f6e652074697020666f7220746865206675747572652c2073756e73
		637265656e20776f756c642062652069742e`)
	aad := unhex(t, "50515253c0c1c2c3c4c5c6c7")
	key := unhex(t, "808182838485868788898a8b8c8d8e8f909192939495969798999a9b9c9d9e9f")
	nonce := unhex(t, "404142434445464748494a4b4c4d4e4f5051525354555657")
	want := unhex(t, `bd6d179d3e83d43b9576579493c0e939572a1700252bfaccbed2902c21396cbb
		731c7f1b0b4aa6440bf3a82f4eda7e39ae64c6708c54c216cb96b72e1213b452
		2f8c9ba40db5d945b11b69b982c1bb9e3f3fac2bc369488f76b2383565d3fff9
		21f9664c97637da9768812f615c68b13b52e`) + unhex(t, "c0875924c1c7987947deafd8780acf49")
	sealed, err := xchachaSealRaw([]byte(key), []byte(nonce), []byte(aad), []byte(pt))
	if err != nil {
		t.Fatal(err)
	}
	if string(sealed) != want {
		t.Fatalf("XChaCha20-Poly1305 ciphertext||tag mismatch:\n got %x", sealed)
	}
	// The kernel's wire format is base64(nonce || ciphertext || tag): the
	// draft vector opens through xchachaOpenWith.
	wire := base64.StdEncoding.EncodeToString([]byte(nonce + want))
	if got := cryOk(t, Crypto_xchachaOpenWith(Secret{v: key}, aad, wire)).(string); got != pt {
		t.Fatalf("xchachaOpenWith did not recover the draft plaintext")
	}
	// Wrong AD → Err.
	cryErr(t, Crypto_xchachaOpenWith(Secret{v: key}, "other", wire), "authentication")
}

func TestXChaChaSealOpenNegative(t *testing.T) {
	key := Secret{v: strings.Repeat("\x07", 32)}
	enc := runCryptoTask(t, Crypto_xchachaSeal(key, "attack at dawn"))
	if enc.Tag != 0 {
		t.Fatalf("seal failed: %v", enc.ErrValue)
	}
	wire := enc.OkValue.(string)
	raw, _ := base64.StdEncoding.DecodeString(wire)
	if len(raw) != 24+len("attack at dawn")+16 {
		t.Fatalf("wire length %d: want nonce(24) + ciphertext + tag(16)", len(raw))
	}
	if got := cryOk(t, Crypto_xchachaOpen(key, wire)).(string); got != "attack at dawn" {
		t.Fatalf("round trip gave %q", got)
	}
	// Tampered ciphertext byte, tampered nonce, tampered tag.
	for _, i := range []int{0, 24, len(raw) - 1} {
		bad := append([]byte(nil), raw...)
		bad[i] ^= 0x80
		cryErr(t, Crypto_xchachaOpen(key, base64.StdEncoding.EncodeToString(bad)), "authentication")
	}
	cryErr(t, Crypto_xchachaOpen(Secret{v: strings.Repeat("\x08", 32)}, wire), "authentication")
	cryErr(t, Crypto_xchachaOpen(key, "!!"), "base64")
	cryErr(t, Crypto_xchachaOpen(key, base64.StdEncoding.EncodeToString(raw[:30])), "too short")
	short := runCryptoTask(t, Crypto_xchachaSeal(Secret{v: "short"}, "x"))
	if short.Tag != 1 {
		t.Fatalf("seal accepted a short key")
	}
	// AD binds: sealed with AD, opening without it fails.
	withAd := runCryptoTask(t, Crypto_xchachaSealWith(key, "header", "body"))
	cryErr(t, Crypto_xchachaOpen(key, withAd.OkValue.(string)), "authentication")
	if got := cryOk(t, Crypto_xchachaOpenWith(key, "header", withAd.OkValue.(string))).(string); got != "body" {
		t.Fatalf("open with AD gave %q", got)
	}
}

// runCryptoTaskAny forces a Task kernel and returns the Result as `any`,
// for use with mustOk / mustErr.
func runCryptoTaskAny(t *testing.T, v any) any {
	t.Helper()
	return runCryptoTask(t, v)
}

// ─── Explicit-nonce ChaCha20-Poly1305 / XChaCha20-Poly1305 ─────────

// rfc8439PT is the "sunscreen" plaintext of RFC 8439 §2.8.2 and
// draft-irtf-cfrg-xchacha-03 §A.3.1.
const rfc8439PT = "Ladies and Gentlemen of the class of '99: If I could offer you only one tip for the future, sunscreen would be it."

func TestChaCha20Poly1305Rfc8439Vector(t *testing.T) {
	key := Secret{v: unhex(t, "808182838485868788898a8b8c8d8e8f909192939495969798999a9b9c9d9e9f")}
	// §2.8.2: the 32-bit constant 07000000 followed by the 64-bit IV.
	nonce := unhex(t, "070000004041424344454647")
	aad := unhex(t, "50515253c0c1c2c3c4c5c6c7")
	want := unhex(t, `d31a8d34648e60db7b86afbc53ef7ec2a4aded51296e08fea9e2b5a736ee62d6
		3dbea45e8ca9671282fafb69da92728b1a71de0a9e060b2905d6a5b67ecd3b36
		92ddbd7f2d778b8c9803aee328091b58fab324e4fad675945585808b4831d7bc
		3ff4def08e4b7a9de576d26586cec64b6116`) + unhex(t, "1ae10b594f09e26a7e902ecbd0600691")
	got := cryOk(t, Crypto_chacha20Poly1305Seal(key, nonce, aad, rfc8439PT)).(string)
	if got != want {
		t.Fatalf("ChaCha20-Poly1305 ciphertext||tag mismatch:\n got %x", got)
	}
	// Deterministic: the same inputs seal to the same bytes.
	if again := cryOk(t, Crypto_chacha20Poly1305Seal(key, nonce, aad, rfc8439PT)).(string); again != want {
		t.Fatalf("seal is not deterministic")
	}
	if pt := cryOk(t, Crypto_chacha20Poly1305Open(key, nonce, aad, want)).(string); pt != rfc8439PT {
		t.Fatalf("open did not recover the RFC plaintext: %q", pt)
	}
}

func TestXChaCha20Poly1305DraftVector(t *testing.T) {
	key := Secret{v: unhex(t, "808182838485868788898a8b8c8d8e8f909192939495969798999a9b9c9d9e9f")}
	nonce := unhex(t, "404142434445464748494a4b4c4d4e4f5051525354555657")
	aad := unhex(t, "50515253c0c1c2c3c4c5c6c7")
	want := unhex(t, `bd6d179d3e83d43b9576579493c0e939572a1700252bfaccbed2902c21396cbb
		731c7f1b0b4aa6440bf3a82f4eda7e39ae64c6708c54c216cb96b72e1213b452
		2f8c9ba40db5d945b11b69b982c1bb9e3f3fac2bc369488f76b2383565d3fff9
		21f9664c97637da9768812f615c68b13b52e`) + unhex(t, "c0875924c1c7987947deafd8780acf49")
	got := cryOk(t, Crypto_xchacha20Poly1305Seal(key, nonce, aad, rfc8439PT)).(string)
	if got != want {
		t.Fatalf("XChaCha20-Poly1305 ciphertext||tag mismatch:\n got %x", got)
	}
	if pt := cryOk(t, Crypto_xchacha20Poly1305Open(key, nonce, aad, want)).(string); pt != rfc8439PT {
		t.Fatalf("open did not recover the draft plaintext: %q", pt)
	}
	// The random-nonce wire format carries the same bytes behind the nonce.
	wire := base64.StdEncoding.EncodeToString([]byte(nonce + want))
	if pt := cryOk(t, Crypto_xchachaOpenWith(key, aad, wire)).(string); pt != rfc8439PT {
		t.Fatalf("xchachaOpenWith disagrees with xchacha20Poly1305Seal")
	}
}

func TestExplicitNonceAeadNegative(t *testing.T) {
	key := Secret{v: strings.Repeat("\x07", 32)}
	type pair struct {
		name       string
		nonceLen   int
		seal, open func(k, n, ad, x any) any
	}
	for _, c := range []pair{
		{"Crypto.chacha20Poly1305", 12, Crypto_chacha20Poly1305Seal, Crypto_chacha20Poly1305Open},
		{"Crypto.xchacha20Poly1305", 24, Crypto_xchacha20Poly1305Seal, Crypto_xchacha20Poly1305Open},
	} {
		nonce := strings.Repeat("\x01", c.nonceLen)
		sealed := cryOk(t, c.seal(key, nonce, "hdr", "attack at dawn")).(string)
		if len(sealed) != len("attack at dawn")+16 {
			t.Fatalf("%s: sealed length %d: want ciphertext + 16-byte tag, no nonce", c.name, len(sealed))
		}
		// Tampered ciphertext, tampered tag.
		for _, i := range []int{0, len(sealed) - 1} {
			bad := []byte(sealed)
			bad[i] ^= 0x80
			cryErr(t, c.open(key, nonce, "hdr", string(bad)), "authentication failed")
		}
		// Wrong nonce, wrong associated data, wrong key.
		cryErr(t, c.open(key, strings.Repeat("\x02", c.nonceLen), "hdr", sealed), "authentication failed")
		cryErr(t, c.open(key, nonce, "other", sealed), "authentication failed")
		cryErr(t, c.open(Secret{v: strings.Repeat("\x08", 32)}, nonce, "hdr", sealed), "authentication failed")
		// Bad lengths: nonce (short and long), key, sealed input shorter than the tag.
		for _, n := range []int{0, c.nonceLen - 1, c.nonceLen + 1} {
			cryErr(t, c.seal(key, strings.Repeat("\x01", n), "", "x"), fmt.Sprintf("nonce must be %d bytes, got %d", c.nonceLen, n))
			cryErr(t, c.open(key, strings.Repeat("\x01", n), "", sealed), fmt.Sprintf("nonce must be %d bytes", c.nonceLen))
		}
		cryErr(t, c.seal(Secret{v: "short"}, nonce, "", "x"), "key must be 32 bytes")
		cryErr(t, c.open(Secret{v: "short"}, nonce, "", sealed), "key must be 32 bytes")
		cryErr(t, c.open(key, nonce, "", sealed[:15]), "at least 16 bytes")
		// A length error is InvalidInput (kind 7), not an FFI failure.
		r := c.seal(key, "", "", "x").(SkyResult[any, any])
		wantErr := ErrInvalidInput(fmt.Sprintf("%sSeal: nonce must be %d bytes, got 0", c.name, c.nonceLen))
		if fmt.Sprintf("%v", r.ErrValue) != fmt.Sprintf("%v", wantErr) {
			t.Fatalf("%s: a bad nonce length must be InvalidInput, got %v", c.name, r.ErrValue)
		}
		// Empty plaintext seals to the tag alone and opens back to "".
		empty := cryOk(t, c.seal(key, nonce, "", "")).(string)
		if len(empty) != 16 {
			t.Fatalf("%s: empty plaintext sealed to %d bytes, want 16", c.name, len(empty))
		}
		if pt := cryOk(t, c.open(key, nonce, "", empty)).(string); pt != "" {
			t.Fatalf("%s: empty round trip gave %q", c.name, pt)
		}
	}
}

// B-3: the eight small-order Ed25519 points (and non-canonical spellings of
// them) are refused at import. The identity key accepted one fixed
// signature (R = identity, S = 0) for EVERY message, so its holder could
// later claim to have signed anything.
func TestSignRejectsSmallOrderPublicKeys(t *testing.T) {
	for _, h := range []string{
		"0100000000000000000000000000000000000000000000000000000000000000", // identity
		"ecffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f", // order 2
		"0000000000000000000000000000000000000000000000000000000000000000", // order 4
		"0000000000000000000000000000000000000000000000000000000000000080", // order 4
		"26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc05", // order 8
		"26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc85", // order 8
		"c7176a703d4dd84fba3c0b760d10670f2a2053fa2c39ccc64ec7fd7792ac037a", // order 8
		"c7176a703d4dd84fba3c0b760d10670f2a2053fa2c39ccc64ec7fd7792ac03fa", // order 8
		"eeffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f", // y = p + 1: identity, non-canonical
		"0100000000000000000000000000000000000000000000000000000000000080", // identity with the sign bit set
	} {
		r, ok := Sign_publicKeyFromBytes(unhex(t, h)).(SkyResult[any, any])
		if !ok || r.Tag == 0 {
			t.Fatalf("small-order or non-canonical key %s was accepted", h)
		}
		if msg := errorMessage(r.ErrValue); strings.Contains(msg, "small-order") &&
			!strings.Contains(msg, "see docs/migration/v0.27.md#ed25519-small-order-keys") {
			t.Fatalf("small-order refusal has no migration link: %s", msg)
		}
	}
	// Ordinary keys still import (RFC 8032 test 1 and a generated key).
	cryOk(t, Sign_publicKeyFromBytes(unhex(t, "d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a")))
	sk := cryOk(t, runCryptoTaskAny(t, Sign_generate(struct{}{})))
	cryOk(t, Sign_publicKeyFromBytes(Sign_publicKeyToBytes(Sign_publicKey(sk))))
}

// B-6: the point check decodes the point itself; it no longer depends on the
// text of an error inside the Go standard library (Go before 1.24 said
// "invalid signature" there, and every 32-byte string then counted as a point).
func TestSignPointCheckDoesNotReadGoErrorText(t *testing.T) {
	src, err := os.ReadFile("crypto_sign_kx.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), `"bad public key"`) {
		t.Fatal("crypto_sign_kx.go still matches a Go error string to decide point validity")
	}
	if !ed25519PointValid([]byte(unhex(t, "d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a"))) {
		t.Fatal("an RFC 8032 public key is not a valid point")
	}
	if ed25519PointValid([]byte(unhex(t, "0200000000000000000000000000000000000000000000000000000000000000"))) {
		t.Fatal("y = 2 has no x on the curve, yet it was accepted")
	}
}
