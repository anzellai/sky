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
                assert!(
                    allowed.contains(&n.as_str()),
                    "{pkg}.{name} ({sky}): nominal `{n}` leaked — Go residue and opaque \
                     types must normalise or become the wildcard"
                );
            }
        }
    }
    // The three bindings the inspector emits WITHOUT a skyType (net/http's
    // `closeNotifierCloseNotify`, `requestCancel`, `requestSetCancel`).
    assert_eq!(
        missing,
        vec![
            "net_http.closeNotifierCloseNotify",
            "net_http.requestCancel",
            "net_http.requestSetCancel"
        ]
    );
    assert!(
        unparseable.is_empty(),
        "every present fixture skyType must parse (arity + wrapper): {unparseable:#?}"
    );
    assert_eq!(parsed, 82 + 77 + 510 - 3);
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
    let any = Ty::var("any");
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
    // `Bytes -> Result Error UUID@…` — both wildcards.
    assert_eq!(
        get("uuid", "fromBytes"),
        Ty::Fun(Box::new(any.clone()), Box::new(res(any.clone())))
    );
    // `RouteMatch@… -> Result Error (Dict String String)` — Dict is a wildcard.
    assert_eq!(
        get("mux", "routeMatchVars"),
        Ty::Fun(Box::new(any.clone()), Box::new(res(any.clone())))
    );
    // A function-typed payload is a wildcard.
    assert_eq!(
        get("net_http", "clientCheckRedirect"),
        Ty::Fun(Box::new(any.clone()), Box::new(res(any.clone())))
    );
    // A callback parameter keeps its arrows; its result is the wildcard.
    let Ty::Fun(_, rest) = get("net_http", "clientConnSetStateHook") else {
        panic!()
    };
    let Ty::Fun(cb, _) = *rest else { panic!() };
    assert_eq!(*cb, Ty::Fun(Box::new(any.clone()), Box::new(any.clone())));
}
