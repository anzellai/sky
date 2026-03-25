package sky_wrappers

import (
	"encoding/hex"
	"io"
)

func Sky_encoding_hex_AppendDecode(arg0 any, arg1 any) SkyResult {
	_arg0 := arg0.([]byte)
	_arg1 := arg1.([]byte)
	res, err := hex.AppendDecode(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_encoding_hex_AppendEncode(arg0 any, arg1 any) []byte {
	_arg0 := arg0.([]byte)
	_arg1 := arg1.([]byte)
	return hex.AppendEncode(_arg0, _arg1)
}

func Sky_encoding_hex_Decode(arg0 any, arg1 any) SkyResult {
	_arg0 := arg0.([]byte)
	_arg1 := arg1.([]byte)
	res, err := hex.Decode(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_encoding_hex_DecodeString(arg0 any) SkyResult {
	_arg0 := arg0.(string)
	res, err := hex.DecodeString(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_encoding_hex_DecodedLen(arg0 any) int {
	_arg0 := arg0.(int)
	return hex.DecodedLen(_arg0)
}

func Sky_encoding_hex_Dump(arg0 any) string {
	_arg0 := arg0.([]byte)
	return hex.Dump(_arg0)
}

func Sky_encoding_hex_Dumper(arg0 any) io.WriteCloser {
	_arg0 := arg0.(io.Writer)
	return hex.Dumper(_arg0)
}

func Sky_encoding_hex_Encode(arg0 any, arg1 any) int {
	_arg0 := arg0.([]byte)
	_arg1 := arg1.([]byte)
	return hex.Encode(_arg0, _arg1)
}

func Sky_encoding_hex_EncodeToString(arg0 any) string {
	_arg0 := arg0.([]byte)
	return hex.EncodeToString(_arg0)
}

func Sky_encoding_hex_EncodedLen(arg0 any) int {
	_arg0 := arg0.(int)
	return hex.EncodedLen(_arg0)
}

func Sky_encoding_hex_NewDecoder(arg0 any) io.Reader {
	_arg0 := arg0.(io.Reader)
	return hex.NewDecoder(_arg0)
}

func Sky_encoding_hex_NewEncoder(arg0 any) io.Writer {
	_arg0 := arg0.(io.Writer)
	return hex.NewEncoder(_arg0)
}

func Sky_encoding_hex_ErrLength() any {
	return hex.ErrLength
}

func Sky_encoding_hex_InvalidByteErrorError(this any) string {
	_this := this.(hex.InvalidByteError)

	return _this.Error()
}

