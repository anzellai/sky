package sky_sky_core_encoding

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
)

type SkyResult struct {
	Tag      int
	SkyName  string
	OkValue  any
	ErrValue any
}

func ok(v any) SkyResult  { return SkyResult{Tag: 0, SkyName: "Ok", OkValue: v} }
func err(v any) SkyResult { return SkyResult{Tag: 1, SkyName: "Err", ErrValue: v} }

func Base64Encode(s any) any { return base64.StdEncoding.EncodeToString([]byte(asString(s))) }
func Base64Decode(s any) any {
	b, e := base64.StdEncoding.DecodeString(asString(s))
	if e != nil { return err(e.Error()) }
	return ok(string(b))
}

func UrlEncode(s any) any { return url.QueryEscape(asString(s)) }
func UrlDecode(s any) any {
	r, e := url.QueryUnescape(asString(s))
	if e != nil { return err(e.Error()) }
	return ok(r)
}

func HexEncode(s any) any { return hex.EncodeToString([]byte(asString(s))) }
func HexDecode(s any) any {
	b, e := hex.DecodeString(asString(s))
	if e != nil { return err(e.Error()) }
	return ok(string(b))
}

func asString(v any) string {
	if s, ok := v.(string); ok { return s }
	return fmt.Sprintf("%v", v)
}
