package rt

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"testing"
)

// A throwaway RSA key pair as PKCS#1 private / PKIX public PEM.
func testRSAPEMPair(t *testing.T) (string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	privPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	pubBytes, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey: %v", err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubBytes})
	return string(privPEM), string(pubPEM)
}

// Crypto.rsaSha256Sign → Crypto.rsaSha256Verify must round-trip, and
// verification must reject a tampered message or a malformed signature.
func TestCryptoRSASha256RoundTrip(t *testing.T) {
	priv, pub := testRSAPEMPair(t)
	msg := "a message that must sign and verify"

	res := Crypto_rsaSha256Sign(priv, msg).(SkyResult[any, any])
	if res.Tag != 0 {
		t.Fatalf("rsaSha256Sign returned Err: %v", res.ErrValue)
	}
	sig := res.OkValue.(string)

	if Crypto_rsaSha256Verify(pub, msg, sig) != true {
		t.Error("rsaSha256Verify rejected a valid signature")
	}
	if Crypto_rsaSha256Verify(pub, "tampered message", sig) != false {
		t.Error("rsaSha256Verify accepted a signature over a different message")
	}
	if Crypto_rsaSha256Verify(pub, msg, "@@not-base64@@") != false {
		t.Error("rsaSha256Verify accepted a malformed signature")
	}
}

// An unparseable key must yield Err, not a panic.
func TestCryptoRSASha256BadKey(t *testing.T) {
	res := Crypto_rsaSha256Sign("definitely not a PEM key", "msg").(SkyResult[any, any])
	if res.Tag != 1 {
		t.Error("rsaSha256Sign should return Err on an unparseable key")
	}
}

func TestCryptoSha1KnownVector(t *testing.T) {
	// sha1("abc") = a9993e364706816aba3e25717850c26c9cd0d89d
	if got := Crypto_sha1("abc"); got != "a9993e364706816aba3e25717850c26c9cd0d89d" {
		t.Errorf("sha1(abc) = %v", got)
	}
}

func TestCryptoHmacSha512Length(t *testing.T) {
	if got := Crypto_hmacSha512("secret", "msg").(string); len(got) != 128 {
		t.Errorf("hmacSha512 hex length = %d, want 128", len(got))
	}
}
