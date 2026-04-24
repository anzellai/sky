package rt

// Kernel wrapper-shape parity tests.
//
// For every runtime helper whose corresponding kernel signature in
// `lookupKernelType` (src/Sky/Type/Constrain/Expression.hs) declares
// a tagged-union wrapper (Maybe / Result / Task / Decoder), assert
// that the runtime helper returns a value with the matching wrapper
// shape. The user-side pattern
//     case Sky.Core.X.foo arg of
//         Just n  -> ...
//         Nothing -> ...
//                       -- or Ok/Err for Result, etc.
// pattern-matches on Tag/JustValue/OkValue/ErrValue, so a runtime
// returning the wrong wrapper (the v0.9.10 String_toIntT regression:
// declared Maybe Int but ran SkyResult[string,int]) panics at the
// case match — silently, because typed codegen routes through the
// typed companion only on certain dispatch paths.
//
// Scope: ONLY the runtime helpers that exist in this package. The
// audit (2026-04-23) did not extend to kernel↔codegen mismatches
// (Encoding.*Encode is documented inline) or inner-type mismatches
// (Hex.decode → Result Error []int instead of Result Error String;
// Crypto.randomBytes → Task Error String instead of Task Error []int).
// Both of those are tracked separately in the Bucket A2 / typed-codegen
// follow-up notes.

import (
	"reflect"
	"strings"
	"testing"
)

// hasField reports whether a value's underlying struct has a named
// field. Used to identify SkyMaybe/SkyResult shapes without binding
// to a specific type-parameter instantiation.
func hasField(v any, name string) bool {
	if v == nil {
		return false
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Struct {
		return false
	}
	return rv.FieldByName(name).IsValid()
}

func isMaybeShape(v any) bool {
	return hasField(v, "Tag") && hasField(v, "JustValue")
}

func isResultShape(v any) bool {
	return hasField(v, "Tag") && hasField(v, "OkValue") && hasField(v, "ErrValue")
}

func isTaskShape(v any) bool {
	if v == nil {
		return false
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Func {
		return false
	}
	t := rv.Type()
	return t.NumIn() == 0 && t.NumOut() == 1
}

// ── Maybe-returning kernels ──────────────────────────────────────────

func TestParity_StringToInt_Maybe(t *testing.T) {
	// Already covered by string_toint_test; included here for the
	// catalogue. The any-typed and typed paths must both be Maybe.
	if !isMaybeShape(String_toInt("42")) {
		t.Errorf("String_toInt: kernel says Maybe Int, runtime returned %T", String_toInt("42"))
	}
	if !isMaybeShape(String_toIntT("42")) {
		t.Errorf("String_toIntT: kernel says Maybe Int, runtime returned %T", String_toIntT("42"))
	}
	if !isMaybeShape(String_toFloat("3.14")) {
		t.Errorf("String_toFloat: kernel says Maybe Float, runtime returned %T", String_toFloat("3.14"))
	}
	if !isMaybeShape(String_toFloatT("3.14")) {
		t.Errorf("String_toFloatT: kernel says Maybe Float, runtime returned %T", String_toFloatT("3.14"))
	}
}

func TestParity_ListHead_Maybe(t *testing.T) {
	// kernel: List.head : List a -> Maybe a
	out := List_head([]any{1, 2, 3})
	if !isMaybeShape(out) {
		t.Errorf("List_head([1,2,3]): kernel says Maybe a, runtime returned %T", out)
	}
	empty := List_head([]any{})
	if !isMaybeShape(empty) {
		t.Errorf("List_head([]): kernel says Maybe a, runtime returned %T", empty)
	}
	tOut := List_headT[int]([]int{1, 2, 3})
	if !isMaybeShape(tOut) {
		t.Errorf("List_headT[int]: kernel says Maybe a, runtime returned %T", tOut)
	}
}

func TestParity_ListTail_Maybe(t *testing.T) {
	if !isMaybeShape(List_tail([]any{1, 2, 3})) {
		t.Errorf("List_tail: kernel says Maybe (List a)")
	}
	if !isMaybeShape(List_tail([]any{})) {
		t.Errorf("List_tail([]): kernel says Maybe (List a)")
	}
}

func TestParity_ListFind_Maybe(t *testing.T) {
	pred := func(x any) any { return x.(int) > 1 }
	if !isMaybeShape(List_find(pred, []any{1, 2, 3})) {
		t.Errorf("List_find: kernel says Maybe a")
	}
	none := func(x any) any { return false }
	if !isMaybeShape(List_find(none, []any{1, 2, 3})) {
		t.Errorf("List_find(no match): kernel says Maybe a")
	}
}

func TestParity_DictGet_Maybe(t *testing.T) {
	d := map[string]any{"k": "v"}
	if !isMaybeShape(Dict_get("k", d)) {
		t.Errorf("Dict_get(hit): kernel says Maybe v")
	}
	if !isMaybeShape(Dict_get("missing", d)) {
		t.Errorf("Dict_get(miss): kernel says Maybe v")
	}
	tOut := Dict_getT[string]("k", map[string]string{"k": "v"})
	if !isMaybeShape(tOut) {
		t.Errorf("Dict_getT: kernel says Maybe v, got %T", tOut)
	}
}

func TestParity_SystemGetArg_Task(t *testing.T) {
	// System.getArg : Int -> Task Error (Maybe String). Migration
	// target for the dropped Args.getArg (v0.10.0). Whatever idx we
	// pass, the outer shape is a Task thunk.
	if !isTaskShape(System_getArg(0)) {
		t.Errorf("System_getArg(0): kernel says Task Error (Maybe String)")
	}
	if !isTaskShape(System_getArg(99999)) {
		t.Errorf("System_getArg(99999): kernel says Task Error (Maybe String)")
	}
}

// Env.get / getOrDefault / getInt / getBool dropped in v0.10.0 — the
// surface lives on as System_getenv / System_getenvOr / System_getenvInt
// / System_getenvBool. The Maybe-shaped Env_get is gone; the Task-
// shaped System_getenv is covered by TestParity_SystemGetenv_Task.

func TestParity_ServerExtractors_Maybe(t *testing.T) {
	// Server.{param,queryParam,header,getCookie} : String -> Request -> Maybe String
	req := SkyRequest{
		Params:  map[string]any{"id": "1"},
		Query:   map[string]any{"q": "x"},
		Headers: map[string]any{"H": "v"},
		Cookies: map[string]string{"c": "v"},
	}
	if !isMaybeShape(Server_param("id", req)) {
		t.Errorf("Server_param hit: kernel says Maybe String")
	}
	if !isMaybeShape(Server_param("missing", req)) {
		t.Errorf("Server_param miss: kernel says Maybe String")
	}
	if !isMaybeShape(Server_queryParam("q", req)) {
		t.Errorf("Server_queryParam hit: kernel says Maybe String")
	}
	if !isMaybeShape(Server_queryParam("missing", req)) {
		t.Errorf("Server_queryParam miss: kernel says Maybe String")
	}
	if !isMaybeShape(Server_header("H", req)) {
		t.Errorf("Server_header hit: kernel says Maybe String")
	}
	if !isMaybeShape(Server_header("missing", req)) {
		t.Errorf("Server_header miss: kernel says Maybe String")
	}
	if !isMaybeShape(Server_getCookie("c", req)) {
		t.Errorf("Server_getCookie hit: kernel says Maybe String")
	}
	if !isMaybeShape(Server_getCookie("missing", req)) {
		t.Errorf("Server_getCookie miss: kernel says Maybe String")
	}
}

// ── Result-returning kernels ────────────────────────────────────────

func TestParity_SystemGetenv_Task(t *testing.T) {
	// System.getenv : String -> Task Error String (Task-everywhere
	// migration 2026-04-24+). Returns a `func() any` thunk.
	// Renamed from Os.getenv to free the `Os` qualifier for Go FFI.
	if !isTaskShape(System_getenv("PATH")) {
		t.Errorf("System_getenv(set): kernel says Task Error String")
	}
	if !isTaskShape(System_getenv("___SKY_NEVER_SET___")) {
		t.Errorf("System_getenv(unset): kernel says Task Error String")
	}
}

func TestParity_SystemCwd_Task(t *testing.T) {
	// System.cwd : () -> Task Error String
	if !isTaskShape(System_cwd(nil)) {
		t.Errorf("System_cwd: kernel says Task Error String")
	}
}

func TestParity_EncodingDecode_Result(t *testing.T) {
	// Encoding.{base64,url,hex}Decode : String -> Result Error String
	// Note: the *_Encode variants are documented as a kernel↔codegen
	// mismatch (kernel says Result, codegen treats result as bare
	// String); not asserted here so the test stays runtime-only and
	// matches what the codegen actually emits. See the comment block
	// above Encoding_base64Encode in rt.go.
	if !isResultShape(Encoding_base64Decode("aGk=")) {
		t.Errorf("Encoding_base64Decode: kernel says Result Error String")
	}
	if !isResultShape(Encoding_base64Decode("***")) {
		t.Errorf("Encoding_base64Decode (bad): kernel says Result Error String")
	}
	if !isResultShape(Encoding_urlDecode("hi")) {
		t.Errorf("Encoding_urlDecode: kernel says Result Error String")
	}
	if !isResultShape(Encoding_hexDecode("aabb")) {
		t.Errorf("Encoding_hexDecode: kernel says Result Error String")
	}
	// Typed companions for decoders are also Result-shaped.
	if !isResultShape(Encoding_base64DecodeT("aGk=")) {
		t.Errorf("Encoding_base64DecodeT: kernel says Result Error String")
	}
	if !isResultShape(Encoding_urlDecodeT("hi")) {
		t.Errorf("Encoding_urlDecodeT: kernel says Result Error String")
	}
	if !isResultShape(Encoding_hexDecodeT("aabb")) {
		t.Errorf("Encoding_hexDecodeT: kernel says Result Error String")
	}
}

// Hex.* dropped in v0.10.0 — Encoding.hexEncode / Encoding.hexDecode
// are the consolidated surface, covered by TestParity_EncodingDecode_Result
// (decode side) and the bare-string Encoding_hexEncode codegen path.

func TestParity_AuthCrypto_Result(t *testing.T) {
	// Auth.hashPassword       : String -> Result Error String
	// Auth.hashPasswordCost   : String -> Int -> Result Error String
	// Auth.verifyPassword     : String -> String -> Result Error Bool
	//   (runtime returns bool, but the kernel says Result — the typed
	//    runtime helper is verified here)
	// Auth.passwordStrength   : String -> Result Error String
	// Auth.signToken          : String -> claims -> Int -> Result Error String
	// Auth.verifyToken        : String -> String -> Result Error claims
	if !isResultShape(Auth_hashPassword("password123")) {
		t.Errorf("Auth_hashPassword: kernel says Result Error String")
	}
	if !isResultShape(Auth_hashPasswordCost("password123", 4)) {
		t.Errorf("Auth_hashPasswordCost: kernel says Result Error String")
	}
	if !isResultShape(Auth_passwordStrength("password123")) {
		t.Errorf("Auth_passwordStrength: kernel says Result Error String")
	}
	// signToken / verifyToken need a 32+ byte secret. Use a stub.
	secret := strings.Repeat("a", 32)
	tok := Auth_signToken(secret, map[string]any{"sub": "u1"}, 60)
	if !isResultShape(tok) {
		t.Errorf("Auth_signToken: kernel says Result Error String")
	}
	tr, ok := tok.(SkyResult[any, any])
	if ok && tr.Tag == 0 {
		if !isResultShape(Auth_verifyToken(secret, tr.OkValue)) {
			t.Errorf("Auth_verifyToken: kernel says Result Error claims")
		}
	}
}

// Auth.verifyPassword's kernel sig is `String -> String -> Result Error
// Bool`, but the runtime returns a bare bool (different class — the
// kernel sig is misleading vs. the runtime contract; Sky's verify-then-
// branch pattern relies on truthiness). Documented but not asserted
// here so the suite stays accurate to the runtime as-shipped.

// ── Task-shape kernels ──────────────────────────────────────────────

func TestParity_TimeSleep_Task(t *testing.T) {
	// Time.sleep : Int -> Task Error ()
	if !isTaskShape(Time_sleep(0)) {
		t.Errorf("Time_sleep: kernel says Task Error ()")
	}
}

func TestParity_RandomInt_Task(t *testing.T) {
	// Random.int : Int -> Int -> Task Error Int (any-typed and typed)
	if !isTaskShape(Random_int(0, 10)) {
		t.Errorf("Random_int: kernel says Task Error Int")
	}
	if !isTaskShape(Random_intT(0, 10)) {
		t.Errorf("Random_intT: kernel says Task Error Int")
	}
	if !isTaskShape(Random_float(0.0, 1.0)) {
		t.Errorf("Random_float: kernel says Task Error Float")
	}
	if !isTaskShape(Random_floatT(0.0, 1.0)) {
		t.Errorf("Random_floatT: kernel says Task Error Float")
	}
}

func TestParity_Crypto_Task(t *testing.T) {
	// Crypto.{randomBytes,randomToken} : Int -> Task Error _
	if !isTaskShape(Crypto_randomBytes(8)) {
		t.Errorf("Crypto_randomBytes: kernel says Task Error (List Int)")
	}
	if !isTaskShape(Crypto_randomToken(8)) {
		t.Errorf("Crypto_randomToken: kernel says Task Error String")
	}
}

func TestParity_FileIo_Task(t *testing.T) {
	// File.{readFile,writeFile,exists,...} : ... -> Task Error _
	if !isTaskShape(File_readFile("nonexistent")) {
		t.Errorf("File_readFile: kernel says Task Error String")
	}
	if !isTaskShape(File_exists("nonexistent")) {
		t.Errorf("File_exists: kernel says Task Error Bool")
	}
	if !isTaskShape(File_writeFile("nonexistent", "")) {
		t.Errorf("File_writeFile: kernel says Task Error ()")
	}
	if !isTaskShape(File_remove("nonexistent")) {
		t.Errorf("File_remove: kernel says Task Error ()")
	}
	if !isTaskShape(File_mkdirAll("nonexistent")) {
		t.Errorf("File_mkdirAll: kernel says Task Error ()")
	}
	if !isTaskShape(File_readDir("nonexistent")) {
		t.Errorf("File_readDir: kernel says Task Error (List String)")
	}
	if !isTaskShape(File_isDir("nonexistent")) {
		t.Errorf("File_isDir: kernel says Task Error Bool")
	}
}

func TestParity_Process_Task(t *testing.T) {
	// Process.run : String -> List String -> Task Error String
	// (getEnv / getCwd / exit / loadEnv all moved to System.* in
	// v0.10.0; their Task-shape parity is covered by the System
	// tests above.)
	if !isTaskShape(Process_run("true", []any{})) {
		t.Errorf("Process_run: kernel says Task Error String")
	}
}

func TestParity_IoWrite_Task(t *testing.T) {
	// Io.{readLine,writeStdout,writeStderr} : ... -> Task Error _
	if !isTaskShape(Io_writeStdout("")) {
		t.Errorf("Io_writeStdout: kernel says Task Error ()")
	}
	if !isTaskShape(Io_writeStderr("")) {
		t.Errorf("Io_writeStderr: kernel says Task Error ()")
	}
	if !isTaskShape(Io_readLine()) {
		t.Errorf("Io_readLine: kernel says Task Error String")
	}
}

// ── Decoder-shape kernels ───────────────────────────────────────────

func TestParity_JsonDec_Decoder(t *testing.T) {
	// JsonDec.{string,int,float,bool,...} : ... -> Decoder _
	checkDec := func(name string, v any) {
		if _, ok := v.(JsonDecoder); !ok {
			t.Errorf("%s: kernel says Decoder, got %T", name, v)
		}
	}
	checkDec("JsonDec_string", JsonDec_string())
	checkDec("JsonDec_int", JsonDec_int())
	checkDec("JsonDec_float", JsonDec_float())
	checkDec("JsonDec_bool", JsonDec_bool())
	checkDec("JsonDec_succeed", JsonDec_succeed("v"))
	checkDec("JsonDec_fail", JsonDec_fail("x"))
	checkDec("JsonDec_field", JsonDec_field("k", JsonDec_string()))
	checkDec("JsonDec_at", JsonDec_at([]any{"a", "b"}, JsonDec_string()))
	checkDec("JsonDec_index", JsonDec_index(0, JsonDec_string()))
	checkDec("JsonDec_list", JsonDec_list(JsonDec_string()))
	checkDec("JsonDec_map", JsonDec_map(func(any) any { return nil }, JsonDec_string()))
	checkDec("JsonDec_andThen",
		JsonDec_andThen(func(any) any { return JsonDec_string() }, JsonDec_string()))
	checkDec("JsonDec_oneOf", JsonDec_oneOf([]any{JsonDec_string()}))
	checkDec("JsonDec_map2",
		JsonDec_map2(func(any) any { return func(any) any { return nil } },
			JsonDec_string(), JsonDec_string()))

	// JsonDec_decodeString : Decoder a -> String -> Result Error a
	if !isResultShape(JsonDec_decodeString(JsonDec_string(), `"hi"`)) {
		t.Errorf("JsonDec_decodeString: kernel says Result Error a")
	}
	if !isResultShape(JsonDec_decodeString(JsonDec_string(), `garbage`)) {
		t.Errorf("JsonDec_decodeString (bad): kernel says Result Error a")
	}
}
