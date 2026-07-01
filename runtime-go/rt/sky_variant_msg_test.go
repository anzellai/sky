package rt

import "testing"

// P3.1 regression: msgDisplayName + msgTags cache + ExtractMsgName
// must prefer the SkyVariant interface over the legacy SkyADT field
// path. Codegen-emitted user-Msg variant structs implement SkyVariant;
// rt-side builders (Sky.Core.Error, etc.) keep emitting SkyADT.

type fakeUserMsg_Increment_V struct{}

func (fakeUserMsg_Increment_V) SkyVariantTag() int     { return 0 }
func (fakeUserMsg_Increment_V) SkyVariantName() string { return "Increment" }

type fakeUserMsg_UpdateEmail_V struct {
	V0 string
}

func (fakeUserMsg_UpdateEmail_V) SkyVariantTag() int     { return 1 }
func (fakeUserMsg_UpdateEmail_V) SkyVariantName() string { return "UpdateEmail" }

func TestMsgDisplayName_VariantPath(t *testing.T) {
	if got := msgDisplayName(fakeUserMsg_Increment_V{}); got != "Increment" {
		t.Fatalf("expected variant Increment, got %q", got)
	}
	if got := msgDisplayName(fakeUserMsg_UpdateEmail_V{V0: "x"}); got != "UpdateEmail" {
		t.Fatalf("expected variant UpdateEmail, got %q", got)
	}
	// Legacy SkyADT still resolves.
	if got := msgDisplayName(SkyADT{Tag: 0, SkyName: "Legacy"}); got != "Legacy" {
		t.Fatalf("expected legacy SkyADT Legacy, got %q", got)
	}
	// Nil + non-ADT fall through unchanged.
	if got := msgDisplayName(nil); got != "" {
		t.Fatalf("expected nil → empty, got %q", got)
	}
}

func TestExtractMsgName_VariantPath(t *testing.T) {
	if got := ExtractMsgName(fakeUserMsg_Increment_V{}); got != "Increment" {
		t.Fatalf("expected variant Increment, got %q", got)
	}
	if got := ExtractMsgName(fakeUserMsg_UpdateEmail_V{V0: "x"}); got != "UpdateEmail" {
		t.Fatalf("expected variant UpdateEmail, got %q", got)
	}
	if got := ExtractMsgName(SkyADT{Tag: 0, SkyName: "Legacy"}); got != "Legacy" {
		t.Fatalf("expected legacy SkyADT, got %q", got)
	}
}
