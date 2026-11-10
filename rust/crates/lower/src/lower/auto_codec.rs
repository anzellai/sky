//! Generated typed codecs for `Codec.auto` / `Codec.autoCamel` (v0.29.9).
//!
//! Roots are the call sites of the canonical `Std.Codec.auto` and
//! `Std.Codec.autoCamel` whose witness has a closed nominal record Go type.
//! For each root this module emits three reflection-free Go functions per
//! reachable type and one `init()` that registers them with
//! `rt.RegisterAutoCodec` (runtime-go/rt/codec_auto_gen.go):
//!
//! * tree: the phase-1 tree the reflective `Codec_autoEnc` builds,
//! * app: the JSON bytes of that tree, written with the same leaf writers as
//!   `json_tree_writer.go` (a NaN or infinite Float returns the runtime's
//!   fallback error so the caller runs the reflective path),
//! * dec: the value the reflective `Codec_autoDecoder` builds from the parsed
//!   document, with every check and error string taken from the shared
//!   `rt.AutoDec*` helpers.
//!
//! Registration compares the emitted keys and declared tags with the
//! runtime's own field plan, so a disagreement keeps the reflective path. No
//! emitted line carries a coerce-floor tracked token: every value is read at
//! its static Go type.
//!
//! doc 14 §10: origin §4.2 wire decode (no R-number; R5 `decodeWith` is
//! unchanged); lever §5.3 typed kernel entry points (precedent
//! `rt.RegisterAdtVariant`, `kernel_typed_return_twin`); floor test §1: the
//! value's Go shape (the record) and the slot's Go shape (each field's
//! declared Go type) are both known at emit time; gate: `xtask coerce-floor`
//! and the differential tests in `rust/crates/sky/tests/auto_codec_flow.rs`.

use super::*;
use std::collections::BTreeMap;
use std::fmt::Write as _;

/// The closed set a generated codec handles.
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) enum Shape {
    Str,
    Bool,
    Int,
    Float,
    /// A registered iota enum, by Go type name.
    Enum(String),
    /// A non-generic record struct, by Go type name.
    Record(String),
    Maybe(Box<Shape>),
    List(Box<Shape>),
    /// `Dict String v` or `Dict Int v` (both `rt.SkyDict[V]`).
    Dict(Box<Shape>),
}

/// A record type's fields: Go field name and Go type, in declaration order.
pub(crate) type Records = BTreeMap<String, Vec<(String, GoTy)>>;

/// The eligibility proof: the shape of `t`, or why it has none.
pub(crate) fn shape_of(
    t: &GoTy,
    records: &Records,
    enums: &BTreeSet<String>,
) -> Result<Shape, String> {
    let mut visiting = Vec::new();
    shape_in(t, records, enums, &mut visiting)
}

fn shape_in(
    t: &GoTy,
    records: &Records,
    enums: &BTreeSet<String>,
    visiting: &mut Vec<String>,
) -> Result<Shape, String> {
    base::ensure_stack(|| match t {
        GoTy::Bare(Prim::Str) => Ok(Shape::Str),
        GoTy::Bare(Prim::Bool) => Ok(Shape::Bool),
        GoTy::Bare(Prim::Int) => Ok(Shape::Int),
        GoTy::Bare(Prim::Float) => Ok(Shape::Float),
        GoTy::Named(n, args) if n == "rt.SkyMaybe" && args.len() == 1 => Ok(Shape::Maybe(
            Box::new(shape_in(&args[0], records, enums, visiting)?),
        )),
        GoTy::Slice(e) => Ok(Shape::List(Box::new(shape_in(
            e, records, enums, visiting,
        )?))),
        GoTy::Map(k, v) => match k.as_ref() {
            GoTy::Bare(Prim::Str) | GoTy::Bare(Prim::Int) => Ok(Shape::Dict(Box::new(shape_in(
                v, records, enums, visiting,
            )?))),
            _ => Err(format!("Dict key {}", render_goty(k))),
        },
        GoTy::Named(n, args) if args.is_empty() && enums.contains(n) => Ok(Shape::Enum(n.clone())),
        GoTy::Named(n, args) if args.is_empty() => {
            let Some(fields) = records.get(n) else {
                return Err(format!("not a closed record: {n}"));
            };
            if visiting.contains(n) {
                return Err(format!("recursive type {n}"));
            }
            if fields.is_empty() {
                return Err(format!("empty record {n}"));
            }
            visiting.push(n.clone());
            for (field, ft) in fields {
                if !go_field_ok(field) {
                    visiting.pop();
                    return Err(format!("field {field} of {n}"));
                }
                if let Err(e) = shape_in(ft, records, enums, visiting) {
                    visiting.pop();
                    return Err(e);
                }
            }
            visiting.pop();
            Ok(Shape::Record(n.clone()))
        }
        other => Err(format!(
            "type outside the closed set: {}",
            render_goty(other)
        )),
    })
}

/// An exported ASCII Go field whose JSON key needs no escaping.
fn go_field_ok(name: &str) -> bool {
    let mut cs = name.chars();
    matches!(cs.next(), Some(c) if c.is_ascii_uppercase())
        && name.chars().all(|c| c.is_ascii_alphanumeric() || c == '_')
}

/// The `Codec.auto` key of a Go field: the Sky field name (codegen's
/// `sky_field_name`, first character lower-cased), snake-cased under
/// `Codec.auto` exactly as `rt.camelToSnake` does.
pub(crate) fn field_key(go_field: &str, snake: bool) -> String {
    let mut chars = go_field.chars();
    let sky: String = match chars.next() {
        Some(first) => first.to_lowercase().chain(chars).collect(),
        None => String::new(),
    };
    if !snake {
        return sky;
    }
    let mut out = String::new();
    for (i, c) in sky.chars().enumerate() {
        if c.is_ascii_uppercase() {
            if i > 0 {
                out.push('_');
            }
            out.push(c.to_ascii_lowercase());
        } else {
            out.push(c);
        }
    }
    out
}

fn go_ty(s: &Shape, records: &Records) -> String {
    match s {
        Shape::Str => "string".into(),
        Shape::Bool => "bool".into(),
        Shape::Int => "int".into(),
        Shape::Float => "float64".into(),
        Shape::Enum(n) | Shape::Record(n) => n.clone(),
        Shape::Maybe(e) => format!("rt.SkyMaybe[{}]", go_ty(e, records)),
        Shape::List(e) => format!("[]{}", go_ty(e, records)),
        Shape::Dict(e) => format!("rt.SkyDict[{}]", go_ty(e, records)),
    }
}

/// The emitter for one program: functions are shared across roots of one key
/// style and named by index (`skyAutoTreeS3`), never by a `Module_member`.
struct Gen<'a> {
    records: &'a Records,
    snake: bool,
    ids: BTreeMap<String, usize>,
    pending: Vec<(usize, Shape)>,
    out: String,
}

impl Gen<'_> {
    fn mode(&self) -> &'static str {
        if self.snake {
            "S"
        } else {
            "C"
        }
    }

    /// The index of the functions for a non-leaf shape (queued on first use).
    fn id(&mut self, s: &Shape) -> usize {
        let key = go_ty(s, self.records);
        if let Some(i) = self.ids.get(&key) {
            return *i;
        }
        let i = self.ids.len();
        self.ids.insert(key, i);
        self.pending.push((i, s.clone()));
        i
    }

    /// `(expr, fallible)` for the tree of value `x`.
    fn tree_expr(&mut self, s: &Shape, x: &str) -> (String, bool) {
        match s {
            Shape::Str | Shape::Bool | Shape::Float => (x.to_string(), false),
            Shape::Int => (format!("int64({x})"), false),
            Shape::Enum(n) => (format!("c.Enum({}, int64({x}))", lit(n)), false),
            _ => {
                let i = self.id(s);
                (format!("skyAutoTree{}{i}({x}, c)", self.mode()), true)
            }
        }
    }

    /// Statements that store the tree of `x` into `dst` (returning on error).
    fn tree_store(&mut self, s: &Shape, x: &str, dst: &str, fail: &str) -> String {
        let (e, fallible) = self.tree_expr(s, x);
        if fallible {
            format!("\tif {dst}, err = {e}; err != nil {{ return {fail}, err }}\n")
        } else {
            format!("\t{dst} = {e}\n")
        }
    }

    /// Statements that append the bytes of `x` to `b`.
    fn app_stmt(&mut self, s: &Shape, x: &str) -> String {
        match s {
            Shape::Str => format!("\tb = rt.AutoAppendString(b, {x})\n"),
            Shape::Bool => format!("\tb = rt.AutoAppendBool(b, {x})\n"),
            Shape::Int => format!("\tb = rt.AutoAppendInt(b, int64({x}))\n"),
            Shape::Float => {
                format!(
                    "\tif b, err = rt.AutoAppendFloat(b, {x}); err != nil {{ return nil, err }}\n"
                )
            }
            Shape::Enum(n) => format!("\tb = c.AppendEnum(b, {}, int64({x}))\n", lit(n)),
            _ => {
                let i = self.id(s);
                format!(
                    "\tif b, err = skyAutoApp{}{i}(b, {x}, c); err != nil {{ return nil, err }}\n",
                    self.mode()
                )
            }
        }
    }

    /// The call that decodes raw `x` into the Go type of `s`.
    fn dec_call(&mut self, s: &Shape, x: &str) -> String {
        match s {
            Shape::Str => format!("rt.AutoDecString({x})"),
            Shape::Bool => format!("rt.AutoDecBool({x})"),
            Shape::Int => format!("rt.AutoDecIntT({x})"),
            Shape::Float => format!("rt.AutoDecFloat({x})"),
            Shape::Enum(n) => format!("rt.AutoDecEnumT[{n}]({}, {x})", lit(n)),
            _ => {
                let i = self.id(s);
                format!("skyAutoDec{}{i}({x}, c)", self.mode())
            }
        }
    }

    fn emit(&mut self, i: usize, s: &Shape) {
        let m = self.mode();
        let t = go_ty(s, self.records);
        let mut tree = String::new();
        let mut app = String::new();
        let mut dec = String::new();
        let _ = writeln!(
            tree,
            "func skyAutoTree{m}{i}(v {t}, c *rt.AutoCodecCtx) (any, error) {{"
        );
        let _ = writeln!(
            app,
            "func skyAutoApp{m}{i}(b []byte, v {t}, c *rt.AutoCodecCtx) ([]byte, error) {{"
        );
        let _ = writeln!(
            dec,
            "func skyAutoDec{m}{i}(raw any, c *rt.AutoCodecCtx) (out {t}, err error) {{"
        );
        match s {
            Shape::List(e) => {
                let _ = writeln!(tree, "\tvar err error\n\t_ = err\n\tout := make([]any, len(v))\n\tfor i, e := range v {{");
                tree.push_str(&indent(&self.tree_store(e, "e", "out[i]", "nil")));
                let _ = writeln!(tree, "\t}}\n\treturn out, nil");
                let _ = writeln!(app, "\tvar err error\n\t_ = err\n\tb = append(b, '[')\n\tfor i, e := range v {{\n\t\tif i > 0 {{ b = append(b, ',') }}");
                app.push_str(&indent(&self.app_stmt(e, "e")));
                let _ = writeln!(app, "\t}}\n\treturn append(b, ']'), nil");
                let call = self.dec_call(e, "it");
                let _ = writeln!(dec, "\titems, err := rt.AutoDecList(raw)\n\tif err != nil {{ return out, err }}\n\tout = make({t}, len(items))\n\tfor i, it := range items {{\n\t\tif out[i], err = {call}; err != nil {{ return nil, err }}\n\t}}\n\treturn out, nil");
            }
            Shape::Maybe(e) => {
                let (ex, fallible) = self.tree_expr(e, "v.JustValue");
                if fallible {
                    let _ = writeln!(
                        tree,
                        "\tif v.Tag != 0 {{ return nil, nil }}\n\treturn rt.AutoJust({ex})"
                    );
                } else {
                    let _ = writeln!(
                        tree,
                        "\tif v.Tag != 0 {{ return nil, nil }}\n\treturn rt.AutoJust({ex}, nil)"
                    );
                }
                let _ = writeln!(app, "\tvar err error\n\t_ = err\n\tif v.Tag != 0 {{ return append(b, \"null\"...), nil }}");
                app.push_str(&self.app_stmt(e, "v.JustValue"));
                let _ = writeln!(app, "\treturn b, nil");
                let inner = go_ty(e, self.records);
                let call = self.dec_call(e, "raw");
                let _ = writeln!(dec, "\tjust, err := rt.AutoDecMaybe(raw)\n\tif err != nil {{ return out, err }}\n\tif !just {{ return rt.Nothing[{inner}](), nil }}\n\tx, err := {call}\n\tif err != nil {{ return out, err }}\n\treturn rt.Just(x), nil");
            }
            Shape::Dict(e) => {
                let _ = writeln!(tree, "\tvar err error\n\t_ = err\n\tks, vs := rt.AutoDictSorted(v)\n\tout := make([]any, len(vs))\n\tfor i, e := range vs {{");
                tree.push_str(&indent(&self.tree_store(e, "e", "out[i]", "nil")));
                let _ = writeln!(tree, "\t}}\n\treturn rt.AutoObj(ks, out), nil");
                let _ = writeln!(app, "\tvar err error\n\t_ = err\n\tks, vs := rt.AutoDictSorted(v)\n\tb = append(b, '{{')\n\tfor i, e := range vs {{\n\t\tif i > 0 {{ b = append(b, ',') }}\n\t\tb = rt.AutoAppendString(b, ks[i])\n\t\tb = append(b, ':')");
                app.push_str(&indent(&self.app_stmt(e, "e")));
                let _ = writeln!(app, "\t}}\n\treturn append(b, '}}'), nil");
                let inner = go_ty(e, self.records);
                let call = self.dec_call(e, "r");
                let _ = writeln!(dec, "\tks, rs, err := rt.AutoDecDict(raw)\n\tif err != nil {{ return out, err }}\n\tvs := make([]{inner}, len(rs))\n\tfor i, r := range rs {{\n\t\tif vs[i], err = {call}; err != nil {{ return out, err }}\n\t}}\n\treturn rt.AutoDictBuild(ks, vs), nil");
            }
            _ => unreachable!("records go through emit_record, leaves are inline"),
        }
        let _ = writeln!(tree, "}}");
        let _ = writeln!(app, "}}");
        let _ = writeln!(dec, "}}");
        self.out.push_str(&tree);
        self.out.push_str(&app);
        self.out.push_str(&dec);
    }
}

fn indent(s: &str) -> String {
    s.lines().map(|l| format!("\t{l}\n")).collect()
}

fn lit(s: &str) -> String {
    base::go_string_literal(s)
}

/// Every record type a shape reaches, sorted.
fn records_of(s: &Shape, records: &Records, out: &mut BTreeSet<String>, enums: &BTreeSet<String>) {
    match s {
        Shape::Record(n) => {
            if out.insert(n.clone()) {
                for (_, ft) in &records[n] {
                    if let Ok(fs) = shape_of(ft, records, enums) {
                        records_of(&fs, records, out, enums);
                    }
                }
            }
        }
        Shape::Maybe(e) | Shape::List(e) | Shape::Dict(e) => records_of(e, records, out, enums),
        _ => {}
    }
}

/// The emitted Go for every eligible root `(witness Go type, snake)`, or None
/// when no root is eligible. `records` and `enums` come from the program's own
/// type items, so a generated codec names only types the program emits.
pub(crate) fn generate(
    roots: &BTreeSet<(String, bool)>,
    witness: &BTreeMap<(String, bool), GoTy>,
    records: &Records,
    enums: &BTreeSet<String>,
) -> Option<String> {
    let mut text = String::new();
    let mut inits = String::new();
    for snake in [true, false] {
        let mut g = Gen {
            records,
            snake,
            ids: BTreeMap::new(),
            pending: Vec::new(),
            out: String::new(),
        };
        let mut roots_here = Vec::new();
        for (name, s) in roots
            .iter()
            .filter(|(_, s)| *s == snake)
            .map(|(n, s)| (n, *s))
        {
            let Some(t) = witness.get(&(name.clone(), s)) else {
                continue;
            };
            let Ok(shape @ Shape::Record(_)) = shape_of(t, records, enums) else {
                continue;
            };
            roots_here.push((name.clone(), shape));
        }
        if roots_here.is_empty() {
            continue;
        }
        let gen_enums = enums.clone();
        for (name, shape) in &roots_here {
            let root = g.id(shape);
            let mut reach = BTreeSet::new();
            records_of(shape, records, &mut reach, &gen_enums);
            let shapes: Vec<String> = reach
                .iter()
                .map(|r| {
                    let fields = &records[r];
                    let keys: Vec<String> = fields
                        .iter()
                        .map(|(f, _)| lit(&field_key(f, snake)))
                        .collect();
                    let decl: Vec<String> =
                        fields.iter().map(|(_, t)| lit(&render_goty(t))).collect();
                    format!(
                        "rt.AutoShape[{r}]([]string{{{}}}, []string{{{}}})",
                        keys.join(", "),
                        decl.join(", ")
                    )
                })
                .collect();
            let m = g.mode();
            let _ = writeln!(
                inits,
                "\trt.RegisterAutoCodec[{name}]({snake}, []rt.AutoCodecShape{{{}}}, skyAutoTree{m}{root}, skyAutoApp{m}{root}, skyAutoDec{m}{root})",
                shapes.join(", ")
            );
        }
        while let Some((i, s)) = g.pending.pop() {
            g.emit_with_enums(i, &s, &gen_enums);
        }
        text.push_str(&g.out);
    }
    if inits.is_empty() {
        return None;
    }
    Some(format!(
        "// Generated typed Codec.auto codecs (rust/crates/lower/src/lower/auto_codec.rs).\n{text}func init() {{\n{inits}}}"
    ))
}

impl Gen<'_> {
    /// `emit` with field shapes proved against the program's enum set.
    fn emit_with_enums(&mut self, i: usize, s: &Shape, enums: &BTreeSet<String>) {
        if let Shape::Record(n) = s {
            // Replace each field type by its proved shape before emitting.
            let fields = self.records[n].clone();
            let proved: Vec<Shape> = fields
                .iter()
                .map(|(_, ft)| shape_of(ft, self.records, enums).expect("proved by the root"))
                .collect();
            self.emit_record(i, n, &fields, &proved);
        } else {
            self.emit(i, s);
        }
    }

    fn emit_record(&mut self, i: usize, n: &str, fields: &[(String, GoTy)], proved: &[Shape]) {
        let m = self.mode();
        let keys: Vec<String> = fields
            .iter()
            .map(|(f, _)| field_key(f, self.snake))
            .collect();
        let mut tree = format!(
            "func skyAutoTree{m}{i}(v {n}, c *rt.AutoCodecCtx) (any, error) {{\n\tvar err error\n\t_ = err\n\tvals := make([]any, {})\n",
            fields.len()
        );
        let mut app = format!(
            "func skyAutoApp{m}{i}(b []byte, v {n}, c *rt.AutoCodecCtx) ([]byte, error) {{\n\tvar err error\n\t_ = err\n"
        );
        let mut dec = format!(
            "func skyAutoDec{m}{i}(raw any, c *rt.AutoCodecCtx) (out {n}, err error) {{\n\tm, err := rt.AutoDecObject(raw, {})\n\tif err != nil {{ return out, err }}\n\tvar x any\n",
            lit(n)
        );
        for (k, (((f, _), key), fs)) in fields.iter().zip(&keys).zip(proved).enumerate() {
            tree.push_str(&self.tree_store(fs, &format!("v.{f}"), &format!("vals[{k}]"), "nil"));
            let sep = if k == 0 { "{" } else { "," };
            let _ = writeln!(app, "\tb = append(b, `{sep}\"{key}\":`...)");
            app.push_str(&self.app_stmt(fs, &format!("v.{f}")));
            let optional = matches!(fs, Shape::Maybe(_));
            let _ = writeln!(
                dec,
                "\tif x, err = rt.AutoDecField(m, {}, {optional}); err != nil {{ return out, err }}",
                lit(key)
            );
            let call = self.dec_call(fs, "x");
            let _ = writeln!(
                dec,
                "\tif out.{f}, err = {call}; err != nil {{ return out, err }}"
            );
        }
        let ks: Vec<String> = keys.iter().map(|k| lit(k)).collect();
        let _ = writeln!(
            tree,
            "\treturn rt.AutoObj([]string{{{}}}, vals), nil\n}}",
            ks.join(", ")
        );
        let _ = writeln!(app, "\treturn append(b, '}}'), nil\n}}");
        let _ = writeln!(dec, "\treturn out, nil\n}}");
        self.out.push_str(&tree);
        self.out.push_str(&app);
        self.out.push_str(&dec);
    }
}

/// A sample program's generated codec text, for gates outside this crate
/// (xtask's coerce-floor test). Covers every shape of the closed set.
pub fn sample_generated_text() -> String {
    let (records, enums) = sample_types();
    let mut witness = BTreeMap::new();
    for snake in [true, false] {
        witness.insert(("Main_Model_R".to_string(), snake), named("Main_Model_R"));
    }
    let roots = witness.keys().cloned().collect();
    generate(&roots, &witness, &records, &enums).expect("the sample is eligible")
}

fn named(n: &str) -> GoTy {
    GoTy::Named(n.into(), vec![])
}

fn sample_types() -> (Records, BTreeSet<String>) {
    let s = GoTy::Bare(Prim::Str);
    let i = GoTy::Bare(Prim::Int);
    let maybe = |t: GoTy| GoTy::Named("rt.SkyMaybe".into(), vec![t]);
    let mut records = Records::new();
    records.insert(
        "Main_Item_R".into(),
        vec![
            ("Title".into(), s.clone()),
            ("Done".into(), GoTy::Bare(Prim::Bool)),
            ("Count".into(), i.clone()),
            ("Score".into(), GoTy::Bare(Prim::Float)),
            ("Color".into(), named("Main_Color")),
            ("Note".into(), maybe(s.clone())),
            ("Tags".into(), GoTy::Slice(Box::new(s.clone()))),
        ],
    );
    records.insert(
        "Main_Model_R".into(),
        vec![
            ("Items".into(), GoTy::Slice(Box::new(named("Main_Item_R")))),
            (
                "ById".into(),
                GoTy::Map(Box::new(s.clone()), Box::new(named("Main_Item_R"))),
            ),
            (
                "ByNum".into(),
                GoTy::Map(Box::new(i.clone()), Box::new(s.clone())),
            ),
            ("UserName".into(), s),
            ("Nested".into(), maybe(maybe(i))),
            ("Colors".into(), GoTy::Slice(Box::new(named("Main_Color")))),
        ],
    );
    (records, BTreeSet::from(["Main_Color".to_string()]))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn eligibility_table() {
        let (mut records, enums) = sample_types();
        let s = GoTy::Bare(Prim::Str);
        let bad: Vec<(&str, GoTy)> = vec![
            ("any", GoTy::Any),
            ("function", GoTy::Func(vec![s.clone()], Box::new(s.clone()))),
            ("tuple", GoTy::Tuple(vec![s.clone(), s.clone()])),
            ("unit", GoTy::Unit),
            ("rune", GoTy::Bare(Prim::Rune)),
            ("bytes", GoTy::Bare(Prim::Bytes)),
            ("adt", named("Main_Shape")),
            ("set", named("rt.SkySet")),
            ("array", GoTy::Named("rt.SkyArray".into(), vec![s.clone()])),
            ("decimal", named("Std_Decimal_Decimal")),
            ("money", named("Std_Money_Money")),
            (
                "handle",
                GoTy::Named("rt.SkyTask".into(), vec![GoTy::Any, s.clone()]),
            ),
            (
                "generic record",
                GoTy::Named("Main_Box_R".into(), vec![s.clone()]),
            ),
            ("type variable", GoTy::TyVar("T1".into())),
            (
                "anonymous record",
                GoTy::Struct(vec![(base::Name::new("x"), s.clone())]),
            ),
            (
                "record-keyed dict",
                GoTy::Map(Box::new(named("Main_Item_R")), Box::new(s.clone())),
            ),
            (
                "enum-keyed dict",
                GoTy::Map(Box::new(named("Main_Color")), Box::new(s.clone())),
            ),
            (
                "result",
                GoTy::Named("rt.SkyResult".into(), vec![s.clone(), s.clone()]),
            ),
        ];
        for (what, t) in &bad {
            assert!(shape_of(t, &records, &enums).is_err(), "{what} accepted");
            records.insert("Main_Wrap_R".into(), vec![("Inner".into(), t.clone())]);
            assert!(
                shape_of(&named("Main_Wrap_R"), &records, &enums).is_err(),
                "record of {what} accepted"
            );
        }
        for t in [
            GoTy::Bare(Prim::Str),
            GoTy::Bare(Prim::Int),
            GoTy::Bare(Prim::Float),
            GoTy::Bare(Prim::Bool),
            named("Main_Color"),
            named("Main_Item_R"),
            named("Main_Model_R"),
            GoTy::Slice(Box::new(named("Main_Item_R"))),
        ] {
            assert!(shape_of(&t, &records, &enums).is_ok(), "{t:?} refused");
        }
        // A recursive back edge, an empty record and an unexported field.
        records.insert(
            "Main_Tree_R".into(),
            vec![("Kids".into(), GoTy::Slice(Box::new(named("Main_Tree_R"))))],
        );
        assert!(shape_of(&named("Main_Tree_R"), &records, &enums).is_err());
        records.insert("Main_Empty_R".into(), vec![]);
        assert!(shape_of(&named("Main_Empty_R"), &records, &enums).is_err());
        records.insert("Main_Low_R".into(), vec![("low".into(), s.clone())]);
        assert!(shape_of(&named("Main_Low_R"), &records, &enums).is_err());
    }

    #[test]
    fn keys_follow_camel_to_snake() {
        assert_eq!(field_key("PriceMinor", true), "price_minor");
        assert_eq!(field_key("PriceMinor", false), "priceMinor");
        assert_eq!(field_key("ById", true), "by_id");
        assert_eq!(field_key("Id", true), "id");
        assert_eq!(field_key("URLPath", true), "u_r_l_path");
    }

    #[test]
    fn generated_names_avoid_the_module_member_pattern() {
        let text = sample_generated_text();
        for line in text.lines().filter(|l| l.starts_with("func ")) {
            let name = line["func ".len()..].split('(').next().unwrap();
            assert!(
                name == "init" || (name.starts_with("skyAuto") && !name.contains('_')),
                "{name}"
            );
        }
        assert!(text.contains("rt.RegisterAutoCodec[Main_Model_R](true"));
        assert!(text.contains("rt.RegisterAutoCodec[Main_Model_R](false"));
        assert!(text.contains(r#""user_name""#) && text.contains(r#""userName""#));
    }

    #[test]
    fn no_eligible_root_emits_nothing() {
        let (records, enums) = sample_types();
        let mut witness = BTreeMap::new();
        witness.insert(("any".to_string(), true), GoTy::Any);
        let roots = witness.keys().cloned().collect();
        assert!(generate(&roots, &witness, &records, &enums).is_none());
    }
}
