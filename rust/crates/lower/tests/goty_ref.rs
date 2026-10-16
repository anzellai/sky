//! `sky_ty_ref_to_go_in` (the lowerer's table path) must return exactly what
//! `sky_ty_to_go_in` returns for the rebuilt owned type, for every node of a
//! table — the D1/D2 shapes of `goty_erasure.rs` (nominal records, the Model
//! subset, a field-set collision, a parametric alias, Dict keys, open rows),
//! nested under functions, tuples and containers, and sharing sub-trees.

use base::Name;
use lower::goty::{sky_ty_ref_to_go_in, sky_ty_to_go_in, Nominal, NominalKind, TypeEnv};
use std::collections::HashMap;
use ty::tytable::TyTableBuilder;
use ty::Ty;

fn app(n: &str, xs: Vec<Ty>) -> Ty {
    Ty::App(Name::new(n), xs)
}
fn app0(n: &str) -> Ty {
    app(n, vec![])
}
fn rec(fs: &[(&str, Ty)], ext: Option<&str>) -> Ty {
    Ty::Record(
        fs.iter().map(|(n, t)| (Name::new(n), t.clone())).collect(),
        ext.map(Name::new),
    )
}
fn fun(a: Ty, b: Ty) -> Ty {
    Ty::Fun(Box::new(a), Box::new(b))
}
fn reg(env: &mut TypeEnv, module: &str, name: &str, go: &str, kind: NominalKind, arity: usize) {
    let n = Nominal {
        go_name: go.into(),
        kind,
        type_arity: arity,
        opaque: false,
    };
    env.nominal.insert(name.into(), n.clone());
    env.nominal_by_module
        .insert((module.into(), name.into()), n);
}

fn env() -> TypeEnv {
    let mut env = TypeEnv::default();
    reg(&mut env, "Main", "Msg", "Main_Msg", NominalKind::Adt, 0);
    reg(
        &mut env,
        "Main",
        "Model",
        "Main_Model_R",
        NominalKind::Record,
        0,
    );
    reg(
        &mut env,
        "Main",
        "Cfg",
        "Main_Cfg_R",
        NominalKind::Record,
        1,
    );
    reg(
        &mut env,
        "Std.Analytics",
        "PropValue",
        "Analytics_PropValue",
        NominalKind::Adt,
        0,
    );
    env.record_fieldsets.insert(
        vec!["count".into(), "name".into()],
        vec!["Main_Model_R".into()],
    );
    env.record_fieldsets.insert(
        vec!["key".into(), "value".into()],
        vec!["User_EnvForm_R".into(), "Analytics_EventProp_R".into()],
    );
    env.record_templates.insert(
        "User_EnvForm_R".into(),
        vec![
            ("key".into(), app0("String")),
            ("value".into(), app0("String")),
        ],
    );
    env.record_templates.insert(
        "Analytics_EventProp_R".into(),
        vec![
            ("key".into(), app0("String")),
            ("value".into(), app0("PropValue")),
        ],
    );
    env.record_fieldsets
        .insert(vec!["on".into()], vec!["Main_Cfg_R".into()]);
    env.record_params
        .insert("Main_Cfg_R".into(), vec![Name::new("msg")]);
    env.record_templates.insert(
        "Main_Cfg_R".into(),
        vec![("on".into(), Ty::Var(Name::new("msg")))],
    );
    env.model = Some((vec!["count".into(), "name".into()], "Main_Model_R".into()));
    env
}

fn sample_types() -> Vec<Ty> {
    let model = rec(&[("count", app0("Int")), ("name", app0("String"))], None);
    let subset = rec(&[("name", app0("String"))], Some("r1"));
    let kv_user = rec(&[("key", app0("String")), ("value", app0("String"))], None);
    let kv_ev = rec(
        &[("key", app0("String")), ("value", app0("PropValue"))],
        None,
    );
    let cfg = rec(&[("on", app0("Msg"))], None);
    let anon = rec(
        &[("z", app0("Bool")), ("a", app("List", vec![app0("Int")]))],
        None,
    );
    let open = rec(&[("zzz", app0("Int"))], Some("r9"));
    let mut big = Vec::new();
    for i in 0..40 {
        big.push((format!("f{i}"), app0("String")));
    }
    let big = Ty::Record(
        big.iter().map(|(n, t)| (Name::new(n), t.clone())).collect(),
        None,
    );
    let mut chain = big.clone();
    let mut out = vec![
        model.clone(),
        subset.clone(),
        kv_user.clone(),
        kv_ev.clone(),
        cfg.clone(),
        anon.clone(),
        open.clone(),
        app("Cfg", vec![app0("Msg")]),
        app("Main.Cfg", vec![app0("Msg")]),
        app("Dict", vec![app0("Int"), model.clone()]),
        app("Maybe", vec![subset.clone()]),
        app(
            "Result",
            vec![app0("Error"), app("List", vec![kv_user.clone()])],
        ),
        app("Task", vec![app0("Error"), Ty::Unit]),
        app("Decoder", vec![model.clone()]),
        app("Cmd", vec![app0("Msg")]),
        app("go@Net.Http.Client", vec![]),
        app("Unknown.Thing", vec![model.clone()]),
        app("number", vec![]),
        Ty::Var(Name::new("a")),
        Ty::Error,
        Ty::Unit,
        Ty::Tuple(vec![model.clone(), app0("Msg"), open.clone()]),
        fun(
            app0("Msg"),
            fun(model.clone(), Ty::Tuple(vec![model.clone(), app0("Cmd")])),
        ),
        fun(subset.clone(), app0("String")),
        fun(
            fun(app0("Int"), app0("Int")),
            app("List", vec![kv_ev.clone()]),
        ),
        rec(&[("inner", model.clone()), ("k", kv_user.clone())], None),
        rec(&[("count", app0("Int")), ("name", app0("Int"))], None),
    ];
    for i in 0..40 {
        chain = fun(app0("String"), chain);
        out.push(app("ObjectCodec", vec![big.clone(), chain.clone()]));
        if i % 7 == 0 {
            out.push(chain.clone());
        }
    }
    out
}

#[test]
fn table_goty_matches_owned_goty() {
    let env = env();
    for cur in [Some("Main"), Some("Other"), None] {
        let mut b = TyTableBuilder::new();
        let tys = sample_types();
        let refs: Vec<_> = tys.iter().map(|t| b.intern(t)).collect();
        let table = b.finish();
        let mut memo = HashMap::new();
        // Every node of the table, not only the roots: walk them in a fixed
        // order, roots first so children are reached both directly and through
        // the memo.
        for (t, r) in tys.iter().zip(refs.iter()) {
            let got = sky_ty_ref_to_go_in(&table, *r, &env, cur, &mut memo);
            let want = sky_ty_to_go_in(t, &env, cur);
            assert_eq!(got, want, "cur={cur:?} type={}", t.render());
        }
        for i in 0..table.node_count() {
            let r = table.nth_ref(i);
            let got = sky_ty_ref_to_go_in(&table, r, &env, cur, &mut memo);
            let want = sky_ty_to_go_in(&table.get(r), &env, cur);
            assert_eq!(got, want, "cur={cur:?} node {i}: {}", table.get(r).render());
        }
    }
}
