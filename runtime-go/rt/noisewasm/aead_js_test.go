//go:build js && wasm

package noisewasm

import (
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"sky-app/rt"
)

// The keyed Sky.Core.Crypto primitives a `withClientCrypto` client runs under
// a key it derived itself (v0.27.0): the random-nonce seal (a Task that draws
// the nonce from crypto/rand, which Go's wasm port reads from
// crypto.getRandomValues), the explicit-nonce AEAD and the keyed MAC. The
// native side derives the same key, opens both seals, checks the MAC and
// answers with its own seal, which this side opens.
func TestWasmAeadAndMacInteropWithNative(t *testing.T) {
	base := os.Getenv("SKY_NOISE_SERVER")
	if base == "" {
		t.Fatal("SKY_NOISE_SERVER is not set: run this through rt/noise_wasm_interop_test.go")
	}
	key := ok(t, rt.Kdf_expand(rt.Kdf_extract("salt", rt.Secret_fromString("input key material")), "info", 32))
	a := run(t, rt.Crypto_xchachaSeal(key, "random nonce")).(string)
	b := run(t, rt.Crypto_xchachaSeal(key, "random nonce")).(string)
	if a == b {
		t.Fatal("two random-nonce seals in wasm are equal: crypto/rand is not drawing entropy")
	}
	c := ok(t, rt.Crypto_chacha20Poly1305Seal(key, "000000000000", "ad", "explicit nonce")).(string)
	mac := rt.Crypto_hmacSha256("key", "hello").(string)
	reply := post(t, base+"/aead", strings.Join([]string{a, hex.EncodeToString([]byte(c)), mac}, "\n"))
	pt := ok(t, rt.Crypto_chacha20Poly1305Open(key, "111111111111", "ad", reply))
	if pt != "pong" {
		t.Fatalf("opened %q, want pong", pt)
	}
}
