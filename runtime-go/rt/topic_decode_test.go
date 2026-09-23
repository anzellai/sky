package rt

import (
	"strings"
	"testing"
)

// The decoder shape the lowerer emits at a `Sub.subscribeTopic` slot: a
// `func(any) any` whose body narrows with the lenient rt.AsString.
func gotStrDecoder(p any) any { return "GotStr:" + AsString(p) }

// And a strict one (rt.AsInt panics on a non-number).
func gotIntDecoder(p any) any { return AsInt(p) + 1 }

// L11: before the tag, AsString(5) delivered "5" — a wrong value from a
// mismatched topic, silently. A tagged String decoder now refuses an Int.
func TestTopicDecode_TaggedStringRefusesAnInt(t *testing.T) {
	sub := Sub_subscribeTopic("chat", TypedTopicDecoder("String", gotStrDecoder))
	if sub.payloadKind != "String" {
		t.Fatalf("Sub_subscribeTopic must keep the payload kind, got %q", sub.payloadKind)
	}
	if _, ok := sub.toMsg.(func(any) any); !ok {
		t.Fatalf("Sub_subscribeTopic must unwrap the decoder function, got %T", sub.toMsg)
	}
	dec := TypedTopicDecoder(sub.payloadKind, sub.toMsg)
	if msg, err := decodeTopicPayload("chat", dec, "hi"); err != nil || msg != "GotStr:hi" {
		t.Fatalf("a matching payload must decode, got %v / %v", msg, err)
	}
	msg, err := decodeTopicPayload("chat", dec, 5)
	if err == nil {
		t.Fatalf("an Int payload into a String decoder must be a TopicDecodeError, got Msg %v", msg)
	}
	if !strings.Contains(err.Error(), "TopicDecode") || !strings.Contains(err.Error(), "String") {
		t.Errorf("the error names the class and the expected kind: %v", err)
	}
}

// A strict decoder's narrowing panic is classified, not re-raised.
func TestTopicDecode_NarrowingPanicIsClassified(t *testing.T) {
	msg, err := decodeTopicPayload("nums", gotIntDecoder, "not-a-number")
	if err == nil {
		t.Fatalf("expected a TopicDecodeError, got Msg %v", msg)
	}
	if msg, err := decodeTopicPayload("nums", gotIntDecoder, 41); err != nil || msg != 42 {
		t.Fatalf("a matching payload must decode, got %v / %v", msg, err)
	}
}

// A JSON wire (the Sky.Spa push endpoint) carries every number as float64: an
// Int subscriber accepts an integral float64 and refuses a fractional one.
func TestTopicDecode_IntAcceptsAnIntegralWireFloat(t *testing.T) {
	dec := TypedTopicDecoder("Int", gotIntDecoder)
	if _, err := decodeTopicPayload("n", dec, float64(7)); err != nil {
		t.Fatalf("integral float64 must decode as Int: %v", err)
	}
	if _, err := decodeTopicPayload("n", dec, 7.5); err == nil {
		t.Fatal("7.5 is not an Int")
	}
	if _, err := decodeTopicPayload("b", TypedTopicDecoder("Bool", func(p any) any { return p }), "true"); err == nil {
		t.Fatal(`"true" (a String) is not a Bool`)
	}
}

// A decoder panic that is not a narrowing failure is not a decode error: it is
// re-raised for the dispatcher's own recovery.
func TestTopicDecode_OtherPanicsAreReRaised(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected the decoder's own panic to propagate")
		}
	}()
	_, _ = decodeTopicPayload("x", func(p any) any { panic("boom") }, 1)
}
