package sky_sky_core_crypto

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
)

func Sha256(input any) any {
	h := sha256.Sum256([]byte(asString(input)))
	return hex.EncodeToString(h[:])
}

func Sha512(input any) any {
	h := sha512.Sum512([]byte(asString(input)))
	return hex.EncodeToString(h[:])
}

func Md5(input any) any {
	h := md5.Sum([]byte(asString(input)))
	return hex.EncodeToString(h[:])
}

func HmacSha256(key, message any) any {
	mac := hmac.New(sha256.New, []byte(asString(key)))
	mac.Write([]byte(asString(message)))
	return hex.EncodeToString(mac.Sum(nil))
}

func asString(v any) string {
	if s, ok := v.(string); ok { return s }
	return fmt.Sprintf("%v", v)
}
