package rt

import (
	"fmt"
	"strings"
	"testing"
)

// AES-256-GCM round-trip + key length validation + tamper detection.
func TestCryptoAesGcmRoundTrip(t *testing.T) {
	// Wrong-length key → Err with helpful message.
	res := Crypto_aesGcmEncrypt("short-key", "msg").(SkyResult[any, any])
	if res.Tag != 1 {
		t.Fatalf("aesGcmEncrypt accepted a wrong-length key")
	}

	// Derive a real 32-byte key.
	key := Crypto_aesKeyFromPassword("hunter2", "regression-salt").(string)
	if len(key) != 32 {
		t.Fatalf("aesKeyFromPassword should return 32 bytes, got %d", len(key))
	}

	// Round-trip succeeds.
	enc := Crypto_aesGcmEncrypt(key, "a secret message").(SkyResult[any, any])
	if enc.Tag != 0 {
		t.Fatalf("aesGcmEncrypt returned Err: %v", enc.ErrValue)
	}
	encoded := enc.OkValue.(string)
	if encoded == "a secret message" {
		t.Fatalf("aesGcmEncrypt produced plaintext output (no encryption?)")
	}

	dec := Crypto_aesGcmDecrypt(key, encoded).(SkyResult[any, any])
	if dec.Tag != 0 {
		t.Fatalf("aesGcmDecrypt returned Err on valid ciphertext: %v", dec.ErrValue)
	}
	if dec.OkValue.(string) != "a secret message" {
		t.Fatalf("aesGcmDecrypt returned wrong plaintext: %q", dec.OkValue)
	}

	// Two encryptions of the same message yield distinct ciphertexts.
	enc2 := Crypto_aesGcmEncrypt(key, "a secret message").(SkyResult[any, any])
	if enc2.OkValue.(string) == encoded {
		t.Fatalf("aesGcmEncrypt is deterministic — nonce reuse is a serious bug")
	}

	// Tampered ciphertext fails.
	tampered := "AAAAA" + encoded[5:]
	bad := Crypto_aesGcmDecrypt(key, tampered).(SkyResult[any, any])
	if bad.Tag != 1 {
		t.Errorf("aesGcmDecrypt accepted a tampered ciphertext")
	}

	// Wrong key fails.
	badKey := Crypto_aesKeyFromPassword("hunter3", "regression-salt").(string)
	bad2 := Crypto_aesGcmDecrypt(badKey, encoded).(SkyResult[any, any])
	if bad2.Tag != 1 {
		t.Errorf("aesGcmDecrypt accepted a wrong key")
	}
}

// ChaCha20-Poly1305 round-trip mirrors AES-GCM (same shape, same tests).
func TestCryptoChaCha20RoundTrip(t *testing.T) {
	key := Crypto_chachaKeyFromPassword("hunter2", "salt").(string)
	if len(key) != 32 {
		t.Fatalf("chachaKeyFromPassword should return 32 bytes, got %d", len(key))
	}

	enc := Crypto_chacha20Encrypt(key, "another secret").(SkyResult[any, any])
	if enc.Tag != 0 {
		t.Fatalf("chacha20Encrypt returned Err: %v", enc.ErrValue)
	}
	encoded := enc.OkValue.(string)
	dec := Crypto_chacha20Decrypt(key, encoded).(SkyResult[any, any])
	if dec.Tag != 0 {
		t.Fatalf("chacha20Decrypt failed: %v", dec.ErrValue)
	}
	if dec.OkValue.(string) != "another secret" {
		t.Fatalf("chacha20Decrypt returned wrong plaintext")
	}
}

// PBKDF2 key derivation is deterministic for the same input.
func TestCryptoKeyDerivationDeterministic(t *testing.T) {
	k1 := Crypto_aesKeyFromPassword("p", "s").(string)
	k2 := Crypto_aesKeyFromPassword("p", "s").(string)
	if k1 != k2 {
		t.Errorf("aesKeyFromPassword is non-deterministic — key would never match on decrypt")
	}
	// Different salt → different key.
	k3 := Crypto_aesKeyFromPassword("p", "s2").(string)
	if k1 == k3 {
		t.Errorf("aesKeyFromPassword ignores salt")
	}
}

// Wrong-length key surfaces a helpful error mentioning aesKeyFromPassword.
func TestCryptoAesGcmKeyLengthError(t *testing.T) {
	res := Crypto_aesGcmEncrypt("abc", "x").(SkyResult[any, any])
	if res.Tag != 1 {
		t.Fatalf("expected Err on short key")
	}
	msg := fmt.Sprintf("%v", res.ErrValue)
	if !strings.Contains(msg, "aesKeyFromPassword") {
		t.Errorf("error message %q doesn't mention the derivation helper", msg)
	}
}

// Bytes round-trips.
func TestBytesEncodingRoundTrip(t *testing.T) {
	raw := "\x00\x01\x02hello\xff"

	hexEnc := Bytes_toHex(raw).(string)
	dec, ok := Bytes_fromHex(hexEnc).(SkyMaybe[any])
	if !ok || dec.Tag != 0 || dec.JustValue.(string) != raw {
		t.Errorf("hex round-trip failed: enc=%q dec=%v", hexEnc, dec)
	}

	b64 := Bytes_toBase64(raw).(string)
	dec2, ok := Bytes_fromBase64(b64).(SkyMaybe[any])
	if !ok || dec2.Tag != 0 || dec2.JustValue.(string) != raw {
		t.Errorf("base64 round-trip failed: enc=%q dec=%v", b64, dec2)
	}

	// Invalid hex / base64 → Nothing.
	if m, _ := Bytes_fromHex("zz").(SkyMaybe[any]); m.Tag == 0 {
		t.Errorf("fromHex accepted invalid hex")
	}
	if m, _ := Bytes_fromHex("abc").(SkyMaybe[any]); m.Tag == 0 {
		t.Errorf("fromHex accepted odd-length input")
	}
	if m, _ := Bytes_fromBase64("###").(SkyMaybe[any]); m.Tag == 0 {
		t.Errorf("fromBase64 accepted invalid input")
	}
}

// Bytes.toString returns Nothing on invalid UTF-8.
func TestBytesToStringUTF8(t *testing.T) {
	ok, _ := Bytes_toString("hello").(SkyMaybe[any])
	if ok.Tag != 0 || ok.JustValue.(string) != "hello" {
		t.Errorf("toString failed on valid UTF-8")
	}
	bad, _ := Bytes_toString("\xff\xfe\xfd").(SkyMaybe[any])
	if bad.Tag != 1 {
		t.Errorf("toString accepted invalid UTF-8")
	}
}
