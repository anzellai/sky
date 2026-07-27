package rt

import "testing"

// TestCastConfigDecoderTypedReturn — a typed Sky decoder closure lowers with a
// concrete return type (here func(any) SkyResult, NOT func(any) any), so the
// raw `.(func(any) any)` assertion misses it. castConfigDecoder must still
// apply it (via sky_call), not degrade to an error decoder. Same root cause as
// the Db.withTransaction "body is not a function" defect.
func TestCastConfigDecoderTypedReturn(t *testing.T) {
	// A decoder shaped exactly like typed Rust codegen emits: concrete
	// SkyResult return, not `any`.
	typed := func(v any) SkyResult[any, any] {
		return Ok[any, any](AsString(v) + "!")
	}
	dec := castConfigDecoder(any(typed))
	got := dec("hi")
	sr, ok := got.(SkyResult[any, any])
	if !ok {
		t.Fatalf("decoder result not a SkyResult: %T", got)
	}
	if sr.Tag != 0 {
		t.Fatalf("typed decoder degraded to an error decoder: %+v", sr)
	}
	if s, _ := sr.OkValue.(string); s != "hi!" {
		t.Errorf("decoder not applied: got %v, want hi!", sr.OkValue)
	}
}
