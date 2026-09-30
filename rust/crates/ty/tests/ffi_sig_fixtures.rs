//! Parse every pinned `skyType` in the committed FFI fixtures
//! (`rust/crates/ffi/tests/fixtures/{uuid,mux,net_http}.expected.kernel.json`)
//! through `ty::ffi_sig` — the parser the checker uses to type a Go-FFI
//! reference. Pins: every scheme ends in the QUALIFIED `Result Error`; the
//! inspector arity is honoured; Go residue never survives as a nominal type;
//! and the arity-only fallback count is exact, so a parser regression that
//! silently drops typing (more fallbacks) or a fixture change goes red.

use std::path::PathBuf;
use ty::ffi_sig::{scheme_for, FfiSchemeKind, FFI_ERROR_TYPE};
use ty::Ty;

fn fixture(name: &str) -> Vec<(String, usize, String)> {
    let p = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("../ffi/tests/fixtures")
        .join(format!("{name}.expected.kernel.json"));
    let text = std::fs::read_to_string(&p).unwrap_or_else(|e| panic!("{}: {e}", p.display()));
    let v: serde_json::Value = serde_json::from_str(&text).unwrap();
    v["functions"]
        .as_array()
        .unwrap()
        .iter()
        .map(|f| {
            (
                f["name"].as_str().unwrap().to_string(),
                f["arity"].as_u64().unwrap() as usize,
                f["skyType"].as_str().unwrap_or("").to_string(),
            )
        })
        .collect()
}

/// Peel `arity` arrows; return (params, result).
fn peel(t: &Ty, arity: usize) -> (Vec<Ty>, Ty) {
    let mut ps = Vec::new();
    let mut cur = t.clone();
    for _ in 0..arity {
        match cur {
            Ty::Fun(a, b) => {
                ps.push(*a);
                cur = *b;
            }
            other => panic!("expected {arity} params, ran out at {other:?}"),
        }
    }
    (ps, cur)
}

/// Every nominal name in `t`.
fn names(t: &Ty, out: &mut Vec<String>) {
    match t {
        Ty::App(n, args) => {
            out.push(n.as_str().to_string());
            for a in args {
                names(a, out);
            }
        }
        Ty::Fun(a, b) => {
            names(a, out);
            names(b, out);
        }
        Ty::Tuple(xs) => xs.iter().for_each(|x| names(x, out)),
        Ty::Record(fs, _) => fs.iter().for_each(|(_, x)| names(x, out)),
        _ => {}
    }
}

#[test]
fn every_fixture_signature_parses_to_a_result_scheme() {
    let allowed = [
        "Result",
        FFI_ERROR_TYPE,
        "String",
        "Int",
        "Float",
        "Bool",
        "List",
        "Maybe",
        "Dict",
    ];
    let mut parsed = 0;
    let mut missing = Vec::new();
    let mut unparseable = Vec::new();
    for pkg in ["uuid", "mux", "net_http"] {
        for (name, arity, sky) in fixture(pkg) {
            let s = scheme_for(&sky, arity);
            match s.kind {
                FfiSchemeKind::Parsed => parsed += 1,
                FfiSchemeKind::FallbackMissing => missing.push(format!("{pkg}.{name}")),
                FfiSchemeKind::FallbackUnparseable => {
                    unparseable.push(format!("{pkg}.{name}: {sky}"))
                }
            }
            let (_, result) = peel(&s.scheme.ty, arity);
            match &result {
                Ty::App(n, args) if n.as_str() == "Result" && args.len() == 2 => {
                    assert_eq!(
                        args[0],
                        Ty::app(FFI_ERROR_TYPE, vec![]),
                        "{pkg}.{name}: the error type must be qualified"
                    );
                }
                other => panic!("{pkg}.{name} ({sky}): result is not `Result Error _`: {other:?}"),
            }
            let mut ns = Vec::new();
            names(&s.scheme.ty, &mut ns);
            for n in ns {
                // Since surface format 3 (v0.27.0) an opaque Go type is its own
                // nominal type, `go@<Pkg>.<Type>`, never the wildcard.
                assert!(
                    allowed.contains(&n.as_str()) || n.starts_with("go@"),
                    "{pkg}.{name} ({sky}): nominal `{n}` leaked — Go residue must \
                     normalise, and an opaque Go type must be a `go@` type"
                );
            }
        }
    }
    // Surface format 3 gives every emitted binding a skyType (the three
    // net/http bindings that had none now carry `go@Go.GoChan`), and skips the
    // bindings that need an unexported Go type.
    assert!(missing.is_empty(), "bindings with no skyType: {missing:?}");
    assert!(
        unparseable.is_empty(),
        "every present fixture skyType must parse (arity + wrapper): {unparseable:#?}"
    );
    assert_eq!(parsed, 82 + 77 + 504);
}

#[test]
fn normalisations_and_wildcards_on_real_entries() {
    let get = |pkg: &str, fname: &str| -> Ty {
        let (_, a, s) = fixture(pkg)
            .into_iter()
            .find(|(n, _, _)| n == fname)
            .unwrap_or_else(|| panic!("{pkg}.{fname} not in fixture"));
        scheme_for(&s, a).scheme.ty
    };
    let int = Ty::app("Int", vec![]);
    let res = |p: Ty| Ty::app("Result", vec![Ty::app(FFI_ERROR_TYPE, vec![]), p]);
    // `byte -> Result Error String` — byte normalises to Int.
    assert_eq!(
        get("uuid", "domainString"),
        Ty::Fun(
            Box::new(int.clone()),
            Box::new(res(Ty::app("String", vec![])))
        )
    );
    // `() -> Result Error (Int, uint16)` — uint16 normalises inside a tuple.
    assert_eq!(
        get("uuid", "getTime"),
        Ty::Fun(
            Box::new(Ty::Unit),
            Box::new(res(Ty::Tuple(vec![int.clone(), int.clone()])))
        )
    );
    // Surface format 3 (v0.27.0): `Bytes` is a `String`, and an opaque Go type
    // is its own nominal `go@` type (it was the wildcard).
    let go = |n: &str| Ty::app(&format!("go@{n}"), vec![]);
    assert_eq!(
        get("uuid", "fromBytes"),
        Ty::Fun(
            Box::new(Ty::app("String", vec![])),
            Box::new(res(go("Github.Com.Google.Uuid.UUID")))
        )
    );
    // A Go map keeps its key type: `map[string]string` is `Dict String String`.
    let string = || Ty::app("String", vec![]);
    assert_eq!(
        get("mux", "routeMatchVars"),
        Ty::Fun(
            Box::new(go("Github.Com.Gorilla.Mux.RouteMatch")),
            Box::new(res(Ty::app("Dict", vec![string(), string()])))
        )
    );
    // A function-typed Go payload is the opaque `go@Go.GoFunc`.
    assert_eq!(
        get("net_http", "clientCheckRedirect"),
        Ty::Fun(
            Box::new(go("Net.Http.Client")),
            Box::new(res(go("Go.GoFunc")))
        )
    );
    // A callback parameter keeps its arrows and its typed result.
    let Ty::Fun(_, rest) = get("net_http", "clientConnSetStateHook") else {
        panic!()
    };
    let Ty::Fun(cb, _) = *rest else { panic!() };
    assert_eq!(
        *cb,
        Ty::Fun(Box::new(go("Net.Http.ClientConn")), Box::new(Ty::Unit))
    );
}
