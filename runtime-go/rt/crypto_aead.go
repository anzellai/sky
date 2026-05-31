// crypto_aead.go — v0.15.44 symmetric encryption + Bytes helpers.
//
// AES-256-GCM and ChaCha20-Poly1305 cover the two AEAD ciphers that
// every Sky app encrypting data at rest needs.  Both use a fresh
// random 12-byte nonce per call (drawn from crypto/rand) prepended
// to the ciphertext so the caller only needs to track the key.
//
// Output format: base64( nonce[12] || ciphertext || tag[16] ) — a
// single opaque string safe for Postgres TEXT / cookie / SQLite
// VARCHAR storage.  The decrypt helper splits the nonce back off.
//
// PBKDF2-HMAC-SHA256 (100 000 iterations) for password→key
// derivation matches OWASP's 2026 recommended floor.  The salt
// MUST be unique per record — pass 16 bytes from `randomBytes 16`.
package rt

import (
	"crypto/aes"
	"crypto/cipher"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"unicode/utf8"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/pbkdf2"
)

const (
	aeadKeyBytes    = 32 // AES-256 + ChaCha20-Poly1305 both want 32-byte keys
	aeadNonceBytes  = 12 // GCM standard nonce + ChaCha20-Poly1305 standard nonce
	pbkdf2Iterations = 100_000
)

// readKey extracts a 32-byte key from a Sky-side string.  Accepts the
// raw byte string (length must match aeadKeyBytes).  Returns a
// descriptive error for the wrong length so callers can detect the
// "forgot to call aesKeyFromPassword" failure mode.
func readKey(name string, key any) ([]byte, error) {
	k := []byte(fmt.Sprintf("%v", key))
	if len(k) != aeadKeyBytes {
		return nil, fmt.Errorf("%s: key must be %d bytes, got %d (derive via Crypto.aesKeyFromPassword if you have a password)", name, aeadKeyBytes, len(k))
	}
	return k, nil
}

// readBytes coerces a Sky string-typed value to []byte without an
// extra fmt.Sprintf when the underlying value is already a Go string.
func readBytes(v any) []byte {
	switch s := v.(type) {
	case string:
		return []byte(s)
	case []byte:
		return s
	default:
		return []byte(fmt.Sprintf("%v", v))
	}
}

// Crypto.aesGcmEncrypt : String -> String -> Result Error String
// (key, plaintext) → base64(nonce || ciphertext || tag).
func Crypto_aesGcmEncrypt(key any, plaintext any) any {
	k, err := readKey("Crypto.aesGcmEncrypt", key)
	if err != nil {
		return Err[any, any](ErrInvalidInput(err.Error()))
	}
	block, err := aes.NewCipher(k)
	if err != nil {
		return Err[any, any](ErrFfi("Crypto.aesGcmEncrypt: " + err.Error()))
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return Err[any, any](ErrFfi("Crypto.aesGcmEncrypt: " + err.Error()))
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := cryptorand.Read(nonce); err != nil {
		return Err[any, any](ErrFfi("Crypto.aesGcmEncrypt: nonce read: " + err.Error()))
	}
	ct := gcm.Seal(nil, nonce, readBytes(plaintext), nil)
	out := append(nonce, ct...)
	return Ok[any, any](base64.StdEncoding.EncodeToString(out))
}

// Crypto.aesGcmDecrypt : String -> String -> Result Error String
// (key, encoded) → plaintext.  Err on any tag-validation failure.
func Crypto_aesGcmDecrypt(key any, encoded any) any {
	k, err := readKey("Crypto.aesGcmDecrypt", key)
	if err != nil {
		return Err[any, any](ErrInvalidInput(err.Error()))
	}
	buf, err := base64.StdEncoding.DecodeString(fmt.Sprintf("%v", encoded))
	if err != nil {
		return Err[any, any](ErrInvalidInput("Crypto.aesGcmDecrypt: invalid base64: " + err.Error()))
	}
	block, err := aes.NewCipher(k)
	if err != nil {
		return Err[any, any](ErrFfi("Crypto.aesGcmDecrypt: " + err.Error()))
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return Err[any, any](ErrFfi("Crypto.aesGcmDecrypt: " + err.Error()))
	}
	ns := gcm.NonceSize()
	if len(buf) < ns {
		return Err[any, any](ErrInvalidInput("Crypto.aesGcmDecrypt: ciphertext too short"))
	}
	nonce, ct := buf[:ns], buf[ns:]
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return Err[any, any](ErrInvalidInput("Crypto.aesGcmDecrypt: " + err.Error()))
	}
	return Ok[any, any](string(pt))
}

// Crypto.chacha20Encrypt : String -> String -> Result Error String
// ChaCha20-Poly1305 AEAD.  Same key length + output shape as
// aesGcmEncrypt — preferred when the host CPU lacks AES-NI.
func Crypto_chacha20Encrypt(key any, plaintext any) any {
	k, err := readKey("Crypto.chacha20Encrypt", key)
	if err != nil {
		return Err[any, any](ErrInvalidInput(err.Error()))
	}
	aead, err := chacha20poly1305.New(k)
	if err != nil {
		return Err[any, any](ErrFfi("Crypto.chacha20Encrypt: " + err.Error()))
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := cryptorand.Read(nonce); err != nil {
		return Err[any, any](ErrFfi("Crypto.chacha20Encrypt: nonce read: " + err.Error()))
	}
	ct := aead.Seal(nil, nonce, readBytes(plaintext), nil)
	out := append(nonce, ct...)
	return Ok[any, any](base64.StdEncoding.EncodeToString(out))
}

// Crypto.chacha20Decrypt : String -> String -> Result Error String
func Crypto_chacha20Decrypt(key any, encoded any) any {
	k, err := readKey("Crypto.chacha20Decrypt", key)
	if err != nil {
		return Err[any, any](ErrInvalidInput(err.Error()))
	}
	buf, err := base64.StdEncoding.DecodeString(fmt.Sprintf("%v", encoded))
	if err != nil {
		return Err[any, any](ErrInvalidInput("Crypto.chacha20Decrypt: invalid base64: " + err.Error()))
	}
	aead, err := chacha20poly1305.New(k)
	if err != nil {
		return Err[any, any](ErrFfi("Crypto.chacha20Decrypt: " + err.Error()))
	}
	ns := aead.NonceSize()
	if len(buf) < ns {
		return Err[any, any](ErrInvalidInput("Crypto.chacha20Decrypt: ciphertext too short"))
	}
	nonce, ct := buf[:ns], buf[ns:]
	pt, err := aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return Err[any, any](ErrInvalidInput("Crypto.chacha20Decrypt: " + err.Error()))
	}
	return Ok[any, any](string(pt))
}

// Crypto.aesKeyFromPassword : String -> String -> String
// PBKDF2-HMAC-SHA256, 100 000 iterations, 32-byte output.  Same
// derivation function for both AES and ChaCha; the alias exists so
// docs / IDE hovers are unambiguous.
func Crypto_aesKeyFromPassword(password any, salt any) any {
	return string(pbkdf2.Key(readBytes(password), readBytes(salt), pbkdf2Iterations, aeadKeyBytes, sha256.New))
}

// Crypto.chachaKeyFromPassword : String -> String -> String
func Crypto_chachaKeyFromPassword(password any, salt any) any {
	return string(pbkdf2.Key(readBytes(password), readBytes(salt), pbkdf2Iterations, aeadKeyBytes, sha256.New))
}

// ═══════════════════════════════════════════════════════════
// Sky.Core.Bytes helpers
// ═══════════════════════════════════════════════════════════

// Bytes.toString : Bytes -> Maybe String
// Returns Nothing if the bytes are not valid UTF-8.
func Bytes_toString(b any) any {
	s := fmt.Sprintf("%v", b)
	if !utf8.ValidString(s) {
		return Nothing[any]()
	}
	return Just[any](s)
}

// Bytes.fromHex : String -> Maybe Bytes
// Case-insensitive; odd-length input returns Nothing.
func Bytes_fromHex(s any) any {
	raw := fmt.Sprintf("%v", s)
	if len(raw)%2 != 0 {
		return Nothing[any]()
	}
	dec, err := hex.DecodeString(raw)
	if err != nil {
		return Nothing[any]()
	}
	return Just[any](string(dec))
}

// Bytes.toHex : Bytes -> String
// Lowercase hex encoding.
func Bytes_toHex(b any) any {
	return hex.EncodeToString(readBytes(b))
}

// Bytes.fromBase64 : String -> Maybe Bytes
func Bytes_fromBase64(s any) any {
	dec, err := base64.StdEncoding.DecodeString(fmt.Sprintf("%v", s))
	if err != nil {
		return Nothing[any]()
	}
	return Just[any](string(dec))
}

// Bytes.toBase64 : Bytes -> String
func Bytes_toBase64(b any) any {
	return base64.StdEncoding.EncodeToString(readBytes(b))
}

// errAEADKeyLen is the user-facing wrong-key-length message
// constant kept module-local so tests can assert on it without a
// magic string.
var errAEADKeyLen = errors.New("key must be 32 bytes")

// _ silences errAEADKeyLen unused warnings if AEAD ever loses the
// guarded path; kept for documentation parity.
var _ = errAEADKeyLen
