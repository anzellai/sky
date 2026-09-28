// crypto_sign_kx.go — Std.Crypto.Sign (Ed25519), Std.Crypto.Kx (X25519),
// Std.Crypto.Kdf (HKDF-SHA256) and the XChaCha20-Poly1305 AEAD of
// Sky.Core.Crypto (v0.27.0).
//
// Pure Go (crypto/ed25519, crypto/hkdf, golang.org/x/crypto): no cgo, so the
// same code builds for the server and for the wasm client.
//
// # Key types
//
// A secret key is never a String. Each secret key type below is an opaque
// struct whose one field is unexported, and which redacts itself in every path
// a value can be printed or serialised (fmt verbs, %#v, encoding/json, gob) —
// the same contract as rt.Secret (secret.go). The raw bytes leave only through
// the `secretKeyToBytes` / `secretKeyToBase64` kernels, and those return a
// Secret, so the one unwrap to a String is still the greppable `Secret.reveal`.
//
// Public keys are not secret. They print as base64 so a log line stays useful,
// and they gob-encode so a Sky.Live model can hold one.
//
// # Effects
//
// Key generation and the random-nonce seal draw from crypto/rand, so they are
// Tasks (`func() any`). Everything else is a pure function of its inputs.
package rt

import (
	"crypto/ed25519"
	"crypto/hkdf"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/curve25519"
)

const (
	keyBytes25519   = 32
	xchachaNonceLen = chacha20poly1305.NonceSizeX // 24
	hkdfMaxLen      = 255 * sha256.Size           // 8160
)

// errKeyNotStorable is returned by every attempt to gob-encode a secret key.
var errKeyNotStorable = errors.New("a secret key is never written to a session store or any gob stream " +
	"(it would be stored in clear); keep keys out of the model and load them where they are used")

// ─── Std.Crypto.Sign key types ─────────────────────────────────────

// SignSecretKey is an Ed25519 private key (seed || public key, 64 bytes).
type SignSecretKey struct{ k string }

func (SignSecretKey) String() string   { return "Ed25519SecretKey([REDACTED])" }
func (SignSecretKey) GoString() string { return "Ed25519SecretKey([REDACTED])" }
func (SignSecretKey) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, "Ed25519SecretKey([REDACTED])")
}
func (SignSecretKey) MarshalJSON() ([]byte, error)     { return []byte(`"[REDACTED]"`), nil }
func (SignSecretKey) GobEncode() ([]byte, error)       { return nil, errKeyNotStorable }
func (*SignSecretKey) GobDecode([]byte) error          { return errKeyNotStorable }
func (s SignSecretKey) privateKey() ed25519.PrivateKey { return ed25519.PrivateKey(s.k) }

// SignPublicKey is an Ed25519 public key (32 bytes).
type SignPublicKey struct{ k string }

func (p SignPublicKey) String() string {
	return "Ed25519PublicKey(" + base64.StdEncoding.EncodeToString([]byte(p.k)) + ")"
}
func (p SignPublicKey) MarshalJSON() ([]byte, error) {
	return []byte(`"` + base64.StdEncoding.EncodeToString([]byte(p.k)) + `"`), nil
}
func (p SignPublicKey) GobEncode() ([]byte, error) { return []byte(p.k), nil }
func (p *SignPublicKey) GobDecode(b []byte) error {
	if len(b) != ed25519.PublicKeySize {
		return errors.New("Ed25519 public key: wrong length")
	}
	p.k = string(b)
	return nil
}

// ─── Std.Crypto.Kx key types ───────────────────────────────────────

// KxSecretKey is an X25519 private scalar (32 bytes).
type KxSecretKey struct{ k string }

func (KxSecretKey) String() string   { return "X25519SecretKey([REDACTED])" }
func (KxSecretKey) GoString() string { return "X25519SecretKey([REDACTED])" }
func (KxSecretKey) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, "X25519SecretKey([REDACTED])")
}
func (KxSecretKey) MarshalJSON() ([]byte, error) { return []byte(`"[REDACTED]"`), nil }
func (KxSecretKey) GobEncode() ([]byte, error)   { return nil, errKeyNotStorable }
func (*KxSecretKey) GobDecode([]byte) error      { return errKeyNotStorable }

// KxPublicKey is an X25519 public u-coordinate (32 bytes).
type KxPublicKey struct{ k string }

func (p KxPublicKey) String() string {
	return "X25519PublicKey(" + base64.StdEncoding.EncodeToString([]byte(p.k)) + ")"
}
func (p KxPublicKey) MarshalJSON() ([]byte, error) {
	return []byte(`"` + base64.StdEncoding.EncodeToString([]byte(p.k)) + `"`), nil
}
func (p KxPublicKey) GobEncode() ([]byte, error) { return []byte(p.k), nil }
func (p *KxPublicKey) GobDecode(b []byte) error {
	if len(b) != keyBytes25519 {
		return errors.New("X25519 public key: wrong length")
	}
	p.k = string(b)
	return nil
}

// kxPublic computes the X25519 public key of a 32-byte scalar.
func kxPublic(sk string) string {
	pub, err := curve25519.X25519([]byte(sk), curve25519.Basepoint)
	if err != nil {
		// X25519 with the base point never produces the all-zero output
		// for a 32-byte scalar (the scalar is clamped to a multiple of the
		// cofactor times a non-zero value); the error path is unreachable.
		panic("rt: X25519 base-point multiplication failed: " + err.Error())
	}
	return string(pub)
}

// kxShared computes X25519(sk, pk) and refuses the all-zero output
// (RFC 7748 §6.1): a low-order peer key forces it, and a value an attacker
// can predict must never become key material.
func kxShared(sk, pk []byte) ([]byte, error) {
	out, err := curve25519.X25519(sk, pk)
	if err != nil {
		return nil, errors.New("the peer public key is a low-order point (the shared secret would be all zeros)")
	}
	var acc byte
	for _, b := range out {
		acc |= b
	}
	if subtle.ConstantTimeByteEq(acc, 0) == 1 {
		return nil, errors.New("the peer public key is a low-order point (the shared secret would be all zeros)")
	}
	return out, nil
}

// decodeSecretKeyB64 reveals a Secret holding standard base64 and decodes it.
func decodeSecretKeyB64(name string, v any) ([]byte, any) {
	raw, err := base64.StdEncoding.DecodeString(secretReveal(v))
	if err != nil {
		return nil, Err[any, any](ErrInvalidInput(name + ": the key is not valid standard base64"))
	}
	return raw, nil
}

func wrongLen(name, what string, n int) any {
	return Err[any, any](ErrInvalidInput(fmt.Sprintf("%s: %s must be 32 bytes, got %d", name, what, n)))
}

// ─── Std.Crypto.Sign kernels ───────────────────────────────────────

// Sign.generate : Task Error SecretKey (backed by `generateWith ()`).
func Sign_generate(_ any) any {
	return func() any {
		_, sk, err := ed25519.GenerateKey(cryptorand.Reader)
		if err != nil {
			return Err[any, any](ErrFfi("Sign.generate: " + err.Error()))
		}
		return Ok[any, any](SignSecretKey{k: string(sk)})
	}
}

func signFromSeed(name string, seed []byte) any {
	if len(seed) != ed25519.SeedSize {
		return wrongLen(name, "an Ed25519 seed", len(seed))
	}
	return Ok[any, any](SignSecretKey{k: string(ed25519.NewKeyFromSeed(seed))})
}

// Sign.secretKeyFromBytes : Secret -> Result Error SecretKey (32-byte seed).
func Sign_secretKeyFromBytes(seed any) any {
	return signFromSeed("Sign.secretKeyFromBytes", []byte(secretReveal(seed)))
}

// Sign.secretKeyFromBase64 : Secret -> Result Error SecretKey.
func Sign_secretKeyFromBase64(b64 any) any {
	raw, errv := decodeSecretKeyB64("Sign.secretKeyFromBase64", b64)
	if errv != nil {
		return errv
	}
	return signFromSeed("Sign.secretKeyFromBase64", raw)
}

func asSignSecret(v any) SignSecretKey {
	if k, ok := v.(SignSecretKey); ok {
		return k
	}
	panic(fmt.Sprintf("rt: expected an Ed25519 SecretKey, got %T", v))
}

func asSignPublic(v any) SignPublicKey {
	if k, ok := v.(SignPublicKey); ok {
		return k
	}
	panic(fmt.Sprintf("rt: expected an Ed25519 PublicKey, got %T", v))
}

// Sign.secretKeyToBytes : SecretKey -> Secret (the 32-byte seed).
func Sign_secretKeyToBytes(sk any) any {
	return Secret{v: string(asSignSecret(sk).privateKey().Seed())}
}

// Sign.secretKeyToBase64 : SecretKey -> Secret (base64 of the seed).
func Sign_secretKeyToBase64(sk any) any {
	return Secret{v: base64.StdEncoding.EncodeToString(asSignSecret(sk).privateKey().Seed())}
}

// Sign.publicKey : SecretKey -> PublicKey.
func Sign_publicKey(sk any) any {
	pub := asSignSecret(sk).privateKey().Public().(ed25519.PublicKey)
	return SignPublicKey{k: string(pub)}
}

// Sign.publicKeyFromBytes : Bytes -> Result Error PublicKey. The bytes must
// be 32 long and decode to a point on the curve.
func Sign_publicKeyFromBytes(b any) any {
	raw := []byte(AsString(b))
	if len(raw) != ed25519.PublicKeySize {
		return wrongLen("Sign.publicKeyFromBytes", "an Ed25519 public key", len(raw))
	}
	if !ed25519PointValid(raw) {
		return Err[any, any](ErrInvalidInput("Sign.publicKeyFromBytes: the bytes are not a valid Ed25519 public key"))
	}
	return Ok[any, any](SignPublicKey{k: string(raw)})
}

// ed25519PointValid reports whether raw decodes to a curve point. Verifying
// any signature against it runs the same decode, and ed25519.Verify returns
// false (never panics) on a point that does not decode, so a probe verify of
// a fixed message tells the two cases apart: a non-point fails the decode on
// every call, and a valid key rejects the zero signature by the equation
// check. The decode path is what distinguishes them, so it is read directly
// from the error of VerifyWithOptions.
func ed25519PointValid(raw []byte) bool {
	err := ed25519.VerifyWithOptions(ed25519.PublicKey(raw), nil, make([]byte, ed25519.SignatureSize), &ed25519.Options{})
	return err == nil || !strings.Contains(err.Error(), "bad public key")
}

// Sign.publicKeyToBytes : PublicKey -> Bytes.
func Sign_publicKeyToBytes(pk any) any { return asSignPublic(pk).k }

// Sign.sign : SecretKey -> Bytes -> Bytes (a 64-byte signature). Ed25519 is
// deterministic, so this is pure.
func Sign_sign(sk any, msg any) any {
	return string(ed25519.Sign(asSignSecret(sk).privateKey(), []byte(AsString(msg))))
}

// Sign.verify : PublicKey -> Bytes -> Bytes -> Bool. False on any failure,
// including a signature of the wrong length.
func Sign_verify(pk any, msg any, sig any) any {
	s := []byte(AsString(sig))
	if len(s) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(asSignPublic(pk).k), []byte(AsString(msg)), s)
}

// ─── Std.Crypto.Kx kernels ─────────────────────────────────────────

// Kx.generate : Task Error SecretKey (backed by `generateWith ()`).
func Kx_generate(_ any) any {
	return func() any {
		sk := make([]byte, keyBytes25519)
		if _, err := cryptorand.Read(sk); err != nil {
			return Err[any, any](ErrFfi("Kx.generate: " + err.Error()))
		}
		return Ok[any, any](KxSecretKey{k: string(sk)})
	}
}

func kxFromBytes(name string, raw []byte) any {
	if len(raw) != keyBytes25519 {
		return wrongLen(name, "an X25519 secret key", len(raw))
	}
	return Ok[any, any](KxSecretKey{k: string(raw)})
}

// Kx.secretKeyFromBytes : Secret -> Result Error SecretKey.
func Kx_secretKeyFromBytes(b any) any {
	return kxFromBytes("Kx.secretKeyFromBytes", []byte(secretReveal(b)))
}

// Kx.secretKeyFromBase64 : Secret -> Result Error SecretKey.
func Kx_secretKeyFromBase64(b64 any) any {
	raw, errv := decodeSecretKeyB64("Kx.secretKeyFromBase64", b64)
	if errv != nil {
		return errv
	}
	return kxFromBytes("Kx.secretKeyFromBase64", raw)
}

func asKxSecret(v any) KxSecretKey {
	if k, ok := v.(KxSecretKey); ok {
		return k
	}
	panic(fmt.Sprintf("rt: expected an X25519 SecretKey, got %T", v))
}

func asKxPublic(v any) KxPublicKey {
	if k, ok := v.(KxPublicKey); ok {
		return k
	}
	panic(fmt.Sprintf("rt: expected an X25519 PublicKey, got %T", v))
}

// Kx.secretKeyToBytes : SecretKey -> Secret.
func Kx_secretKeyToBytes(sk any) any { return Secret{v: asKxSecret(sk).k} }

// Kx.secretKeyToBase64 : SecretKey -> Secret.
func Kx_secretKeyToBase64(sk any) any {
	return Secret{v: base64.StdEncoding.EncodeToString([]byte(asKxSecret(sk).k))}
}

// Kx.publicKey : SecretKey -> PublicKey.
func Kx_publicKey(sk any) any { return KxPublicKey{k: kxPublic(asKxSecret(sk).k)} }

// Kx.publicKeyFromBytes : Bytes -> Result Error PublicKey. Any 32 bytes are a
// u-coordinate X25519 accepts (RFC 7748 §5); a low-order point is refused
// later, by sharedSecret, where it would do harm.
func Kx_publicKeyFromBytes(b any) any {
	raw := AsString(b)
	if len(raw) != keyBytes25519 {
		return wrongLen("Kx.publicKeyFromBytes", "an X25519 public key", len(raw))
	}
	return Ok[any, any](KxPublicKey{k: raw})
}

// Kx.publicKeyToBytes : PublicKey -> Bytes.
func Kx_publicKeyToBytes(pk any) any { return asKxPublic(pk).k }

// Kx.sharedSecret : SecretKey -> PublicKey -> Result Error Secret.
func Kx_sharedSecret(sk any, pk any) any {
	out, err := kxShared([]byte(asKxSecret(sk).k), []byte(asKxPublic(pk).k))
	if err != nil {
		return Err[any, any](ErrInvalidInput("Kx.sharedSecret: " + err.Error()))
	}
	return Ok[any, any](Secret{v: string(out)})
}

// ─── Std.Crypto.Kdf kernels (HKDF-SHA256, RFC 5869) ────────────────

// Kdf.extract : Bytes -> Secret -> Secret — (salt, input keying material) →
// a 32-byte pseudorandom key.
func Kdf_extract(salt any, ikm any) any {
	prk, err := hkdf.Extract(sha256.New, []byte(secretReveal(ikm)), []byte(AsString(salt)))
	if err != nil {
		panic("rt: HKDF-Extract failed: " + err.Error())
	}
	return Secret{v: string(prk)}
}

// Kdf.expand : Secret -> Bytes -> Int -> Result Error Secret — (pseudorandom
// key, info, length) → output keying material. The length must be 1..8160
// (255 × 32, RFC 5869 §2.3).
func Kdf_expand(prk any, info any, length any) any {
	n := AsInt(length)
	if n < 1 || n > hkdfMaxLen {
		return Err[any, any](ErrInvalidInput(fmt.Sprintf("Kdf.expand: length must be 1..%d bytes (255 × 32), got %d", hkdfMaxLen, n)))
	}
	p := []byte(secretReveal(prk))
	if len(p) < sha256.Size {
		return Err[any, any](ErrInvalidInput(fmt.Sprintf("Kdf.expand: the pseudorandom key must be at least 32 bytes (the output of Kdf.extract), got %d", len(p))))
	}
	okm, err := hkdf.Expand(sha256.New, p, AsString(info), n)
	if err != nil {
		return Err[any, any](ErrInvalidInput("Kdf.expand: " + err.Error()))
	}
	return Ok[any, any](Secret{v: string(okm)})
}

// ─── XChaCha20-Poly1305 (Sky.Core.Crypto) ──────────────────────────

// xchachaSealRaw is the deterministic core: key, 24-byte nonce, AD and
// plaintext → ciphertext || tag. Only the Go test vectors call it with a
// fixed nonce; the kernels draw the nonce from crypto/rand.
func xchachaSealRaw(key, nonce, ad, pt []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	if len(nonce) != xchachaNonceLen {
		return nil, fmt.Errorf("nonce must be %d bytes", xchachaNonceLen)
	}
	return aead.Seal(nil, nonce, pt, ad), nil
}

func xchachaSealTask(name string, key, ad, pt any) any {
	return func() any {
		k, err := readKey(name, key)
		if err != nil {
			return Err[any, any](ErrInvalidInput(err.Error()))
		}
		nonce := make([]byte, xchachaNonceLen)
		if _, err := cryptorand.Read(nonce); err != nil {
			return Err[any, any](ErrFfi(name + ": nonce read: " + err.Error()))
		}
		ct, err := xchachaSealRaw(k, nonce, []byte(AsString(ad)), readBytes(pt))
		if err != nil {
			return Err[any, any](ErrFfi(name + ": " + err.Error()))
		}
		return Ok[any, any](base64.StdEncoding.EncodeToString(append(nonce, ct...)))
	}
}

func xchachaOpen(name string, key, ad, encoded any) any {
	k, err := readKey(name, key)
	if err != nil {
		return Err[any, any](ErrInvalidInput(err.Error()))
	}
	buf, err := base64.StdEncoding.DecodeString(AsString(encoded))
	if err != nil {
		return Err[any, any](ErrInvalidInput(name + ": invalid base64: " + err.Error()))
	}
	if len(buf) < xchachaNonceLen+chacha20poly1305.Overhead {
		return Err[any, any](ErrInvalidInput(name + ": ciphertext too short"))
	}
	aead, err := chacha20poly1305.NewX(k)
	if err != nil {
		return Err[any, any](ErrFfi(name + ": " + err.Error()))
	}
	pt, err := aead.Open(nil, buf[:xchachaNonceLen], buf[xchachaNonceLen:], []byte(AsString(ad)))
	if err != nil {
		return Err[any, any](ErrInvalidInput(name + ": authentication failed (wrong key, wrong associated data, or a tampered ciphertext)"))
	}
	return Ok[any, any](string(pt))
}

// Crypto.xchachaSeal : Secret -> Bytes -> Task Error String.
func Crypto_xchachaSeal(key any, plaintext any) any {
	return xchachaSealTask("Crypto.xchachaSeal", key, "", plaintext)
}

// Crypto.xchachaSealWith : Secret -> Bytes -> Bytes -> Task Error String —
// (key, associated data, plaintext).
func Crypto_xchachaSealWith(key any, ad any, plaintext any) any {
	return xchachaSealTask("Crypto.xchachaSealWith", key, ad, plaintext)
}

// Crypto.xchachaOpen : Secret -> String -> Result Error Bytes.
func Crypto_xchachaOpen(key any, encoded any) any {
	return xchachaOpen("Crypto.xchachaOpen", key, "", encoded)
}

// Crypto.xchachaOpenWith : Secret -> Bytes -> String -> Result Error Bytes.
func Crypto_xchachaOpenWith(key any, ad any, encoded any) any {
	return xchachaOpen("Crypto.xchachaOpenWith", key, ad, encoded)
}

// ─── ChaCha20-Poly1305 / XChaCha20-Poly1305 with a caller nonce ────
//
// The explicit-nonce AEAD (RFC 8439 §2.8; draft-irtf-cfrg-xchacha §2): the
// caller supplies the nonce and associated data, and the result is the raw
// `ciphertext || tag` bytes (no nonce prefix, no base64 — the caller already
// holds the nonce). Pure and deterministic: the same inputs always give the
// same output, which is what a protocol with its own nonce schedule (a
// counter, a transcript-derived nonce) or a published test vector needs.
//
// Reusing a nonce with the same key breaks both confidentiality (the two
// plaintexts XOR out of the two ciphertexts) and authenticity (the Poly1305
// key repeats, so tags can be forged). The random-nonce `xchachaSeal` is the
// recommended default; these are for interoperability with a fixed protocol.

// aeadExplicit seals or opens with a caller nonce. `x` selects XChaCha20
// (24-byte nonce) over ChaCha20 (12-byte nonce).
func aeadExplicit(name string, x bool, key, nonce, ad, data any, seal bool) any {
	k, err := readKey(name, key)
	if err != nil {
		return Err[any, any](ErrInvalidInput(err.Error()))
	}
	n := readBytes(nonce)
	want := chacha20poly1305.NonceSize
	if x {
		want = chacha20poly1305.NonceSizeX
	}
	if len(n) != want {
		return Err[any, any](ErrInvalidInput(fmt.Sprintf("%s: nonce must be %d bytes, got %d", name, want, len(n))))
	}
	var aead interface {
		Seal(dst, nonce, plaintext, additionalData []byte) []byte
		Open(dst, nonce, ciphertext, additionalData []byte) ([]byte, error)
	}
	if x {
		aead, err = chacha20poly1305.NewX(k)
	} else {
		aead, err = chacha20poly1305.New(k)
	}
	if err != nil {
		return Err[any, any](ErrFfi(name + ": " + err.Error()))
	}
	buf := readBytes(data)
	if seal {
		return Ok[any, any](string(aead.Seal(nil, n, buf, readBytes(ad))))
	}
	if len(buf) < chacha20poly1305.Overhead {
		return Err[any, any](ErrInvalidInput(fmt.Sprintf("%s: sealed input must be at least %d bytes (the tag), got %d", name, chacha20poly1305.Overhead, len(buf))))
	}
	pt, err := aead.Open(nil, n, buf, readBytes(ad))
	if err != nil {
		return Err[any, any](ErrInvalidInput(name + ": authentication failed (wrong key, wrong nonce, wrong associated data, or a tampered ciphertext)"))
	}
	return Ok[any, any](string(pt))
}

// Crypto.chacha20Poly1305Seal : Secret -> Bytes -> Bytes -> Bytes -> Result Error Bytes
// — (key, 12-byte nonce, associated data, plaintext) → ciphertext || tag.
func Crypto_chacha20Poly1305Seal(key any, nonce any, ad any, plaintext any) any {
	return aeadExplicit("Crypto.chacha20Poly1305Seal", false, key, nonce, ad, plaintext, true)
}

// Crypto.chacha20Poly1305Open : Secret -> Bytes -> Bytes -> Bytes -> Result Error Bytes
// — (key, 12-byte nonce, associated data, ciphertext || tag) → plaintext.
func Crypto_chacha20Poly1305Open(key any, nonce any, ad any, sealed any) any {
	return aeadExplicit("Crypto.chacha20Poly1305Open", false, key, nonce, ad, sealed, false)
}

// Crypto.xchacha20Poly1305Seal : Secret -> Bytes -> Bytes -> Bytes -> Result Error Bytes
// — (key, 24-byte nonce, associated data, plaintext) → ciphertext || tag.
func Crypto_xchacha20Poly1305Seal(key any, nonce any, ad any, plaintext any) any {
	return aeadExplicit("Crypto.xchacha20Poly1305Seal", true, key, nonce, ad, plaintext, true)
}

// Crypto.xchacha20Poly1305Open : Secret -> Bytes -> Bytes -> Bytes -> Result Error Bytes
// — (key, 24-byte nonce, associated data, ciphertext || tag) → plaintext.
func Crypto_xchacha20Poly1305Open(key any, nonce any, ad any, sealed any) any {
	return aeadExplicit("Crypto.xchacha20Poly1305Open", true, key, nonce, ad, sealed, false)
}
