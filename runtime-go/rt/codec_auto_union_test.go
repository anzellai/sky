package rt

import (
	"encoding/json"
	"strings"
	"testing"
)

// C-3: `Codec.auto` with a union blank used the blank's VARIANT struct as the
// decode target, so every JSON object decoded as the blank's variant: the
// round trip of `Rgb 4 5 6` through `Codec.auto Red` gave `Ok Red`. The
// decoder must read the tag and build the variant it names.

type codecTestColor interface {
	SkyVariant
	isCodecTestColor()
}

type codecTestColor_Red_V struct{}

func (codecTestColor_Red_V) SkyVariantTag() int     { return 0 }
func (codecTestColor_Red_V) SkyVariantName() string { return "Red" }
func (codecTestColor_Red_V) isCodecTestColor()      {}

type codecTestColor_Rgb_V struct {
	V0 int
	V1 int
	V2 int
}

func (codecTestColor_Rgb_V) SkyVariantTag() int     { return 1 }
func (codecTestColor_Rgb_V) SkyVariantName() string { return "Rgb" }
func (codecTestColor_Rgb_V) isCodecTestColor()      {}

func init() {
	RegisterAdtVariant("rt.codecTestColor", "Red", func(raw []JsonRawMessage) any {
		return codecTestColor_Red_V{}
	})
	RegisterAdtVariant("rt.codecTestColor", "Rgb", func(raw []JsonRawMessage) any {
		var v0, v1, v2 int
		if len(raw) >= 1 {
			_ = JsonUnmarshal(raw[0], &v0)
		}
		if len(raw) >= 2 {
			_ = JsonUnmarshal(raw[1], &v1)
		}
		if len(raw) >= 3 {
			_ = JsonUnmarshal(raw[2], &v2)
		}
		return codecTestColor_Rgb_V{V0: v0, V1: v1, V2: v2}
	})
}

func codecRunText(t *testing.T, blank any, text string) SkyResult[any, any] {
	t.Helper()
	var raw any
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		t.Fatalf("unmarshal %s: %v", text, err)
	}
	return Codec_autoDecoder(false, blank).(JsonDecoder).run(raw).(SkyResult[any, any])
}

func codecAutoRoundTrip(t *testing.T, blank, v any) SkyResult[any, any] {
	t.Helper()
	b, err := json.Marshal(Codec_autoEnc(false, v).(JsonValue).raw)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return codecRunText(t, blank, string(b))
}

func TestCodecAutoUnionBlankDecodesTheTaggedVariant(t *testing.T) {
	var blank codecTestColor = codecTestColor_Red_V{}
	res := codecAutoRoundTrip(t, blank, codecTestColor_Rgb_V{V0: 4, V1: 5, V2: 6})
	if res.Tag != 0 {
		t.Fatalf("Rgb 4 5 6 through Codec.auto Red: Err %v", res.ErrValue)
	}
	if got, ok := res.OkValue.(codecTestColor_Rgb_V); !ok || got.V0 != 4 || got.V2 != 6 {
		t.Fatalf("Rgb 4 5 6 through Codec.auto Red decoded as %#v", res.OkValue)
	}
	// The other way round: the blank Rgb 0 0 0 decodes Red.
	res = codecAutoRoundTrip(t, codecTestColor_Rgb_V{}, codecTestColor_Red_V{})
	if _, ok := res.OkValue.(codecTestColor_Red_V); res.Tag != 0 || !ok {
		t.Fatalf("Red through Codec.auto (Rgb 0 0 0): %#v", res)
	}
}

func TestCodecAutoUnionRefusesAWrongShape(t *testing.T) {
	blank := codecTestColor_Red_V{}
	for _, in := range []string{
		`{"tag":"Blue"}`,                       // no such variant
		`{"v0":1}`,                             // no tag
		`["Rgb",1,2,3]`,                        // not an object
		`{"tag":"Rgb","v0":"x","v1":1,"v2":2}`, // wrong payload type
		`{"tag":"Rgb","v0":1}`,                 // missing payload
	} {
		res := codecRunText(t, blank, in)
		if res.Tag == 0 {
			t.Fatalf("%s decoded to %#v, want Err", in, res.OkValue)
		}
		if strings.Contains(errorMessage(res.ErrValue), "Red_V") {
			t.Fatalf("%s: the error names the blank's variant struct: %s", in, errorMessage(res.ErrValue))
		}
	}
}
