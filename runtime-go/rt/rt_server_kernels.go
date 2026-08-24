//go:build !js

package rt

// rt_server_kernels.go — kernels split out of rt.go so the Sky.Spa CLIENT
// target (GOOS=js GOARCH=wasm, TinyGo) does not import server-only stdlib
// packages that TinyGo cannot compile: os/exec (subprocess), crypto/rsa +
// crypto/x509 + encoding/pem (RSA/JWT). These are genuine server effects — a
// browser client runs no subprocess and holds no RSA signing key — so their
// ABSENCE from the client build is correct, not a stub. The SERVER build
// (//go:build !js) compiles them here byte-for-byte as before; moving a
// function between files in the same package changes nothing the server emits.

import (
	"crypto"
	cryptorand "crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"os/exec"
)

// ═══════════════════════════════════════════════════════════
// Process (subprocess execution — server only)
// ═══════════════════════════════════════════════════════════

func Process_run(cmd any, args any) any {
	return func() any {
		cmdStr := fmt.Sprintf("%v", cmd)
		argList := AsList(args)
		strArgs := make([]string, len(argList))
		for i, a := range argList {
			strArgs[i] = fmt.Sprintf("%v", a)
		}
		c := exec.Command(cmdStr, strArgs...)
		out, err := c.CombinedOutput()
		if err != nil {
			return Err[any, any](ErrIo(fmt.Sprintf("%s: %v", string(out), err)))
		}
		return Ok[any, any](string(out))
	}
}

// ═══════════════════════════════════════════════════════════
// Crypto — RSA/PKCS (PEM key parsing + RSASSA-PKCS1-v1_5, server only)
// ═══════════════════════════════════════════════════════════

// Crypto.rsaSha256Sign : String -> String -> Result Error String
// (PEM private key, message) → standard-base64 RSASSA-PKCS1-v1_5
// signature over the SHA-256 digest. Accepts PKCS#1 and PKCS#8 PEM
// keys. The signing key never leaves this process.
func Crypto_rsaSha256Sign(pemKey any, msg any) any {
	block, _ := pem.Decode([]byte(fmt.Sprintf("%v", pemKey)))
	if block == nil {
		return Err[any, any](ErrFfi("Crypto.rsaSha256Sign: not a PEM-encoded key"))
	}
	var priv *rsa.PrivateKey
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		priv = k
	} else if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		rk, ok := k.(*rsa.PrivateKey)
		if !ok {
			return Err[any, any](ErrFfi("Crypto.rsaSha256Sign: PEM key is not RSA"))
		}
		priv = rk
	} else {
		return Err[any, any](ErrFfi("Crypto.rsaSha256Sign: could not parse the private key"))
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("%v", msg)))
	sig, err := rsa.SignPKCS1v15(cryptorand.Reader, priv, crypto.SHA256, digest[:])
	if err != nil {
		return Err[any, any](ErrFfi("Crypto.rsaSha256Sign: " + err.Error()))
	}
	return Ok[any, any](base64.StdEncoding.EncodeToString(sig))
}

// Crypto.rsaSha256Verify : String -> String -> String -> Bool
// (PEM public key, message, standard-base64 signature) → valid?
// False on any parse or verification failure.
func Crypto_rsaSha256Verify(pemKey any, msg any, sigB64 any) any {
	block, _ := pem.Decode([]byte(fmt.Sprintf("%v", pemKey)))
	if block == nil {
		return false
	}
	var pub *rsa.PublicKey
	if k, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
		rk, ok := k.(*rsa.PublicKey)
		if !ok {
			return false
		}
		pub = rk
	} else if k, err := x509.ParsePKCS1PublicKey(block.Bytes); err == nil {
		pub = k
	} else {
		return false
	}
	sig, err := base64.StdEncoding.DecodeString(fmt.Sprintf("%v", sigB64))
	if err != nil {
		return false
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("%v", msg)))
	return rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig) == nil
}
