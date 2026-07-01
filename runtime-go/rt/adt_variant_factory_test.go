package rt

import (
	"encoding/json"
	"testing"
)

// Variant struct that codegen will emit in P3+. SkyVariant
// methods + typed payload field — the wire-dispatch path
// constructs this via the registered factory.

type liveTest_Msg_UpdateEmail_V struct {
	V0 string
}

func (liveTest_Msg_UpdateEmail_V) SkyVariantTag() int     { return 0 }
func (liveTest_Msg_UpdateEmail_V) SkyVariantName() string { return "UpdateEmail" }

type liveTest_Msg_Increment_V struct{}

func (liveTest_Msg_Increment_V) SkyVariantTag() int     { return 1 }
func (liveTest_Msg_Increment_V) SkyVariantName() string { return "Increment" }

func TestBuildAdtFromWire_VariantFactoryPath(t *testing.T) {
	// Register factory for UpdateEmail. Mirrors codegen's emitted init().
	RegisterAdtVariant("liveTest_UpdateEmail", func(raw []json.RawMessage) any {
		var v0 string
		if len(raw) >= 1 {
			_ = json.Unmarshal(raw[0], &v0)
		}
		return liveTest_Msg_UpdateEmail_V{V0: v0}
	})
	RegisterAdtVariant("liveTest_Increment", func(raw []json.RawMessage) any {
		return liveTest_Msg_Increment_V{}
	})

	t.Run("typed-payload-decoded-from-json", func(t *testing.T) {
		emailJSON, _ := json.Marshal("anzel@example.com")
		got, ok := BuildAdtFromWire("liveTest_UpdateEmail", []json.RawMessage{emailJSON}, -1)
		if !ok {
			t.Fatal("expected factory hit")
		}
		v, isV := got.(liveTest_Msg_UpdateEmail_V)
		if !isV {
			t.Fatalf("expected variant struct, got %T", got)
		}
		if v.V0 != "anzel@example.com" {
			t.Fatalf("expected typed string field, got %q", v.V0)
		}
	})

	t.Run("nullary-variant", func(t *testing.T) {
		got, ok := BuildAdtFromWire("liveTest_Increment", nil, -1)
		if !ok {
			t.Fatal("expected factory hit")
		}
		_, isV := got.(liveTest_Msg_Increment_V)
		if !isV {
			t.Fatalf("expected nullary variant struct, got %T", got)
		}
	})

	t.Run("legacy-skyadt-fallback", func(t *testing.T) {
		// No variant factory registered for "LegacyMsg" → legacy path.
		RegisterAdtTag("liveTest_LegacyMsg", 7)
		argJSON, _ := json.Marshal(42)
		got, ok := BuildAdtFromWire("liveTest_LegacyMsg", []json.RawMessage{argJSON}, -1)
		if !ok {
			t.Fatal("expected legacy hit via LookupAdtTag")
		}
		adt, isAdt := got.(SkyADT)
		if !isAdt {
			t.Fatalf("expected legacy SkyADT, got %T", got)
		}
		if adt.Tag != 7 || adt.SkyName != "liveTest_LegacyMsg" || len(adt.Fields) != 1 {
			t.Fatalf("unexpected SkyADT shape: %+v", adt)
		}
	})

	t.Run("unknown-msg-name", func(t *testing.T) {
		_, ok := BuildAdtFromWire("liveTest_NonExistent_NeverRegistered", nil, -1)
		if ok {
			t.Fatal("expected miss for unknown Msg name")
		}
	})

	t.Run("local-tag-fallback", func(t *testing.T) {
		// No global registration AND no variant factory — but a per-app
		// localTag passes through, mirroring the renderer's lazy-built
		// msgTags cache.
		argJSON, _ := json.Marshal("xyz")
		got, ok := BuildAdtFromWire("liveTest_OnlyKnownLocally_NoGlobal", []json.RawMessage{argJSON}, 3)
		if !ok {
			t.Fatal("expected localTag fallback to succeed")
		}
		adt, isAdt := got.(SkyADT)
		if !isAdt || adt.Tag != 3 || adt.SkyName != "liveTest_OnlyKnownLocally_NoGlobal" {
			t.Fatalf("unexpected local-tag fallback: %+v", got)
		}
	})
}

func TestIsFinalisedAdt(t *testing.T) {
	if !IsFinalisedAdt(SkyADT{Tag: 0, SkyName: "X"}) {
		t.Fatal("expected SkyADT to be finalised")
	}
	if !IsFinalisedAdt(liveTest_Msg_Increment_V{}) {
		t.Fatal("expected SkyVariant to be finalised")
	}
	if !IsFinalisedAdt(liveTest_Msg_UpdateEmail_V{V0: "x"}) {
		t.Fatal("expected non-nullary SkyVariant to be finalised")
	}
	// Functions / closures are NOT finalised — applyMsgArgs should run on them.
	if IsFinalisedAdt(func(s string) any { return nil }) {
		t.Fatal("expected closure to NOT be finalised")
	}
	// Plain values are NOT finalised either.
	if IsFinalisedAdt("hello") {
		t.Fatal("expected string to NOT be finalised")
	}
}
