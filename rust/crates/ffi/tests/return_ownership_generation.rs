//! Both generated return bridges and opaque struct field getters reach the
//! runtime ownership boundary; named containers retain opaque surface types.
use ffi::{generate, PackageInfo};

#[test]
fn convertible_returns_use_the_shared_ownership_boundary() {
    let info: PackageInfo = serde_json::from_str(include_str!(
        "fixtures/return-ownership/package.inspector.json"
    ))
    .unwrap();
    let generated = generate(&info);
    assert_eq!(generated.module_name, "Example.Test.Reusedbuffers");
    assert!(generated
        .kernel_json
        .contains("Int -> Result Error (List Int)"));
    assert!(
        generated
            .kernel_json
            .contains("go@Example.Test.Reusedbuffers.NamedList"),
        "a named Go slice must remain opaque: {}",
        generated.kernel_json
    );
    assert!(
        generated.bindings_go.contains("FfiRet(pkg.Read(arg0))"),
        "typed wrapper bypassed return ownership: {}",
        generated.bindings_go
    );
    let kernel: serde_json::Value = serde_json::from_str(&generated.kernel_json).unwrap();
    let array = kernel["functions"]
        .as_array()
        .unwrap()
        .iter()
        .find(|function| function["name"] == "readArray")
        .expect("real array producer must have a callable Sky surface");
    assert_eq!(
        array["skyType"], "Int -> Result Error (List (List Int))",
        "array fallback must expose nested Sky lists, never an opaque coercion"
    );
    // Fixed-size arrays deliberately do not qualify for the T wrapper. This
    // real producer forces the any/any fallback, rather than assuming one of
    // the fully typed signatures above happens to emit FfiRet(r0).
    let untyped = generated
        .bindings_go
        .split("func Go_Reusedbuffers_readArray(arg0 any) (out any) {")
        .nth(1)
        .expect("array producer must exercise the untyped wrapper");
    let untyped = untyped.split("\n}").next().unwrap();
    assert!(
        untyped.contains("r0 := pkg.ReadArray(FfiArg[int](arg0))"),
        "untyped fixture must call the real array producer: {untyped}"
    );
    assert!(
        untyped.contains("out = Ok[any, any](FfiRet(r0))"),
        "untyped wrapper bypassed return ownership: {untyped}"
    );
    assert!(
        !generated
            .bindings_go
            .contains("func Go_Reusedbuffers_readArrayT("),
        "array fixture unexpectedly stopped exercising the untyped fallback"
    );
    assert!(
        generated
            .bindings_go
            .contains("SkyFfiFieldGet3(arg0, \"Values\")"),
        "field getter bypassed data conversion"
    );
    assert!(
        generated.bindings_go.contains("Go_Reusedbuffers_readT"),
        "fixture must include actual typed bridge"
    );
    assert!(
        generated
            .kernel_json
            .contains("(List Int -> Int) -> Result Error Int"),
        "callback fixture must expose actual Sky arrow"
    );
    assert!(
        generated
            .bindings_go
            .contains("FfiArg[func([]int) int](arg0)"),
        "typed exact callback must exercise shared FfiArg boundary"
    );
    assert!(
        generated.kernel_json.contains("Result Error go@Go.GoFunc"),
        "function result must remain an opaque output handle"
    );
    assert!(
        generated
            .bindings_go
            .contains("FfiArg[func(int) []int](arg0)"),
        "callback result fixture must exercise an exact typed List output slot"
    );
    assert!(
        generated
            .kernel_json
            .contains("(Int -> List Int) -> Result Error Int"),
        "container-result callback must be one function parameter, not two ordinary parameters"
    );
    assert!(
        generated.kernel_json.contains("Result Error go@Go.GoAny"),
        "an interface OUTPUT is an opaque Go handle, not an unconstrained Sky variable"
    );
    assert!(
        !generated.bindings_go.contains("go:build !js"),
        "the fixture must remain usable in raw wasm builds"
    );
}
