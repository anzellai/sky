// Classified decode of a pub/sub payload into a subscriber's Msg.
//
// `Cmd.publish : String -> any -> Cmd msg` and `Sub.subscribeTopic : String ->
// (any -> msg) -> Sub msg` carry the payload as `any`: a topic is a string, so
// the type checker cannot link a publisher to a subscriber. For a literal
// topic the checker does it anyway ([E2011]); for a topic computed at run time
// the mismatch can only be seen here.
//
// Two failure shapes existed, both silent or raw:
//
//   - The decoder's narrowing is lenient for String: `rt.AsString(5)` is "5",
//     so an Int published to a `String -> Msg` subscriber arrived as a wrong
//     value. The lowerer now tags a primitive-parameter decoder with the kind
//     it expects (`rt.TypedTopicDecoder`, lower.rs `topic_payload_kind`) and
//     the payload's kind is checked BEFORE the decoder runs.
//   - A strict narrowing (`rt.AsInt`, `rt.Coerce`) panicked; the panic was
//     logged with a stack as an unexpected crash.
//
// Both are now a TopicDecodeError: the event is dropped and one classified
// line is logged (Sky.Live stderr / the Sky.Spa console), and the model is not
// touched.

package rt

import (
	"fmt"
	"math"
	"strings"
)

// typedTopicDecoder is a subscriber decoder tagged with the payload kind its
// parameter expects ("String" / "Int" / "Float" / "Bool").
type typedTopicDecoder struct {
	kind string
	fn   any
}

// TypedTopicDecoder tags a `Sub.subscribeTopic` decoder with the primitive
// payload kind it takes. Emitted by the lowerer; Sub_subscribeTopic unwraps it.
func TypedTopicDecoder(kind string, fn any) any {
	return typedTopicDecoder{kind: kind, fn: fn}
}

// TopicDecodeError is a payload its subscriber's decoder cannot take.
type TopicDecodeError struct {
	Topic  string
	Reason string
}

func (e *TopicDecodeError) Error() string {
	return fmt.Sprintf("rt.TopicDecode: topic %q: %s", e.Topic, e.Reason)
}

// topicPayloadProblem says why `payload` is not a value of `kind`, or "" when
// it is. A payload that crossed a JSON wire (the Sky.Spa push endpoint)
// carries every number as float64, so an Int accepts an integral float64.
func topicPayloadProblem(kind string, payload any) string {
	ok := false
	switch kind {
	case "", "any":
		return ""
	case "String":
		_, ok = payload.(string)
	case "Bool":
		_, ok = payload.(bool)
	case "Int":
		switch n := payload.(type) {
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			ok = true
		case float64:
			ok = n == math.Trunc(n) && !math.IsInf(n, 0)
		}
	case "Float":
		switch payload.(type) {
		case float64, float32:
			ok = true
		}
	default:
		return ""
	}
	if ok {
		return ""
	}
	return fmt.Sprintf("the subscriber's decoder takes %s, but the payload is %T (%v)", kind, payload, payload)
}

// topicDecoderParts splits a stored decoder into (function, expected kind).
func topicDecoderParts(dec any) (any, string) {
	if t, ok := dec.(typedTopicDecoder); ok {
		return t.fn, t.kind
	}
	return dec, ""
}

// decodeTopicPayload applies a subscriber's decoder to a payload. A payload of
// the wrong kind, or a decoder narrowing that fails on it (a type-mismatch /
// coerce panic), is a *TopicDecodeError. Any other panic is not a decode
// failure and is re-raised for the caller's own recovery.
func decodeTopicPayload(topic string, dec any, payload any) (msg any, derr *TopicDecodeError) {
	fn, kind := topicDecoderParts(dec)
	if p := topicPayloadProblem(kind, payload); p != "" {
		return nil, &TopicDecodeError{Topic: topic, Reason: p}
	}
	defer func() {
		if r := recover(); r != nil {
			if isNarrowingPanic(fmt.Sprintf("%v", r)) {
				msg = nil
				derr = &TopicDecodeError{
					Topic:  topic,
					Reason: fmt.Sprintf("the subscriber's decoder cannot take the payload %T (%v): %v", payload, payload, r),
				}
				return
			}
			panic(r)
		}
	}()
	return sky_call(fn, payload), nil
}

// isNarrowingPanic reports whether a recovered panic message is a failed
// narrowing of a value to a type (the TypeMismatch / CoerceFailure classes of
// panic_recover.go, which is not built for the wasm client).
func isNarrowingPanic(msg string) bool {
	for _, m := range []string{
		"rt.AsInt: expected numeric", "rt.AsFloat: expected numeric",
		"rt.AsBool: expected bool", "rt.skyCallDirect: argument",
		"rt.Coerce: expected", "rt.coerceInner: type mismatch",
		"reflect: Call using",
	} {
		if strings.Contains(msg, m) {
			return true
		}
	}
	return false
}
