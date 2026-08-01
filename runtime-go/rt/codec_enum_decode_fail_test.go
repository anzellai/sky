package rt

import (
	"encoding/json"
	"testing"
)

// Conformance finding C1 regression.
//
// `Codec.fromJson` on an `enum`/`taggedUnion` codec used to PANIC
// (CoerceFailure in coerceInner) on a decode FAILURE instead of
// returning Err. Root cause: `JsonDec_fail` returned an Err carrying a
// bare string, which then could not narrow to the Error SkyADT when
// `fromJson`'s `Result Error a` result flowed through
// `ResultCoerce[Error, a]` → `coerceInner[Error](rawString)`.

// isErrOfErrorAdt reports whether r is an Err whose payload is a proper
// Sky Error ADT (SkyADT), not a bare string.
func isErrOfErrorAdt(t *testing.T, r any) {
	t.Helper()
	sr, ok := r.(SkyResult[any, any])
	if !ok {
		t.Fatalf("expected SkyResult, got %T", r)
	}
	if sr.Tag == 0 {
		t.Fatalf("expected Err, got Ok(%v)", sr.OkValue)
	}
	if _, isStr := sr.ErrValue.(string); isStr {
		t.Fatalf("Err payload is a bare string %q — must be an Error ADT (ErrDecode)", sr.ErrValue)
	}
	if _, isAdt := sr.ErrValue.(SkyADT); !isAdt {
		t.Fatalf("Err payload is not an Error SkyADT: %T", sr.ErrValue)
	}
}

// JsonDec_fail must produce an Err carrying an Error ADT, not a raw string.
func TestJsonDecFailReturnsErrorAdt(t *testing.T) {
	d := JsonDec_fail("Codec.enum: unknown name: nope").(JsonDecoder)
	isErrOfErrorAdt(t, d.run("nope"))
}

// The enum decoder shape — `D.andThen (\name -> <fail on unknown>) D.string`
// — on an unknown name must return Err (an Error ADT), never Ok<rawstring>.
func TestEnumDecoderUnknownNameReturnsErr(t *testing.T) {
	// enumValueFor "nope" [] == D.fail (no pair matched)
	enumDec := JsonDec_andThen(
		func(name any) any {
			return JsonDec_fail("Codec.enum: unknown name: " + name.(string))
		},
		JsonDec_string(),
	).(JsonDecoder)
	isErrOfErrorAdt(t, enumDec.run("nope"))
}

// End-to-end: the failing enum decode result, wrapped by the same
// ResultCoerce[Error, ADT] codegen emits for `fromJson`, must NOT panic
// and must stay an Err. `SkyADT` stands in for both the Error E-slot and
// a user ADT A-slot (both lower to rt.SkyADT).
func TestEnumDecodeFailureResultCoerceDoesNotPanic(t *testing.T) {
	enumDec := JsonDec_andThen(
		func(name any) any {
			return JsonDec_fail("Codec.enum: unknown name: " + name.(string))
		},
		JsonDec_string(),
	).(JsonDecoder)
	res := enumDec.run("nope")

	// This is the wrap codegen inserts around fromJson's result.
	coerced := ResultCoerce[SkyADT, SkyADT](res)
	if coerced.Tag == 0 {
		t.Fatalf("expected Err after ResultCoerce, got Ok(%v)", coerced.OkValue)
	}
}

// Belt-and-suspenders: coercing a bare string to the Error ADT must not
// panic — it wraps as an Unexpected Error instead.
func TestCoerceInnerStringToErrorAdtDoesNotPanic(t *testing.T) {
	got := coerceInner[SkyADT]("some decode message")
	if got.SkyName != "Error" {
		t.Fatalf("expected an Error SkyADT, got SkyName=%q", got.SkyName)
	}
}

// ── Conformance finding C2 — reflective record decoder is now STRICT ────────

// c2Rec mirrors what `Codec.auto { name = "", count = 0 }` lowers to.
type c2Rec struct {
	Name  string `sky:"name,string"`
	Count int    `sky:"count,int"`
}

// c2Opt mirrors a record with a Maybe field — the absent/null case must
// still decode to Nothing, NOT error.
type c2Opt struct {
	Label string           `sky:"label,string"`
	Note  SkyMaybe[string] `sky:"note,rt.SkyMaybe[string]"`
}

func autoDecode(t *testing.T, witness any, jsonInput string) SkyResult[any, any] {
	t.Helper()
	dec := Codec_autoDecoder(true, witness).(JsonDecoder)
	var raw any
	if err := json.Unmarshal([]byte(jsonInput), &raw); err != nil {
		t.Fatalf("bad test JSON %q: %v", jsonInput, err)
	}
	return dec.run(raw).(SkyResult[any, any])
}

// A missing required (non-Maybe) field must decode to Err, not a
// zero-value fill (C2 repro #1).
func TestAutoDecoderMissingRequiredFieldErrs(t *testing.T) {
	r := autoDecode(t, c2Rec{}, `{"name":"x"}`)
	if r.Tag == 0 {
		t.Fatalf("expected Err for missing required field, got Ok(%v)", r.OkValue)
	}
}

// A wrong-typed field (string where Int) must decode to Err, not be
// coerced to zero (C2 repro #2).
func TestAutoDecoderWrongTypedFieldErrs(t *testing.T) {
	r := autoDecode(t, c2Rec{}, `{"name":"x","count":"z"}`)
	if r.Tag == 0 {
		t.Fatalf("expected Err for wrong-typed field, got Ok(%v)", r.OkValue)
	}
}

// All fields present + well-typed still decodes Ok.
func TestAutoDecoderCompleteRecordOk(t *testing.T) {
	r := autoDecode(t, c2Rec{}, `{"name":"x","count":7}`)
	if r.Tag != 0 {
		t.Fatalf("expected Ok, got Err(%v)", r.ErrValue)
	}
	got := r.OkValue.(c2Rec)
	if got.Name != "x" || got.Count != 7 {
		t.Fatalf("wrong decode: %+v", got)
	}
}

// A Maybe field absent from the object is NOT an error — it decodes to
// Nothing (the nuance the strictness must preserve).
func TestAutoDecoderAbsentMaybeFieldIsNothing(t *testing.T) {
	r := autoDecode(t, c2Opt{}, `{"label":"a"}`)
	if r.Tag != 0 {
		t.Fatalf("absent Maybe field must decode Ok(Nothing), got Err(%v)", r.ErrValue)
	}
	got := r.OkValue.(c2Opt)
	if got.Label != "a" || got.Note.Tag != 1 {
		t.Fatalf("expected Nothing note, got %+v", got)
	}
}

// A null Maybe field also decodes to Nothing.
func TestAutoDecoderNullMaybeFieldIsNothing(t *testing.T) {
	r := autoDecode(t, c2Opt{}, `{"label":"a","note":null}`)
	if r.Tag != 0 {
		t.Fatalf("null Maybe field must decode Ok(Nothing), got Err(%v)", r.ErrValue)
	}
	if r.OkValue.(c2Opt).Note.Tag != 1 {
		t.Fatalf("expected Nothing note, got %+v", r.OkValue)
	}
}
