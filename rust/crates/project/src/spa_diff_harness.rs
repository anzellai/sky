//! `spa_diff_harness` — the emitter for the Sky.Spa **differential split fuzzer**
//! (design: `docs/design/auto-testing.md`, mode A; goal:
//! `.claude/AUTONOMOUS_GOAL.md` phase 2).
//!
//! ## What it does
//! For each **phase-2-checkable** server branch of an auto-split app it emits a
//! `checkOne` arm that runs the branch's `update` **two ways** over the SAME
//! generated `(Model, Msg)` and asserts they agree:
//!
//! * **direct** — `update msg model` (the whole model in scope, no projection):
//!   the reference semantics a non-split app would run.
//! * **split** — the real Sky.Spa RPC round-trip, emitted from the SAME shared
//!   wire emitters the generator ships (`emit_build_req` → `emit_reconstruct` +
//!   `emit_ctor_app` → `update` → `emit_write_set_encode` → `emit_apply_delta`).
//!
//! A dropped read-set field, a dropped write-set field, or an un-renamed Msg-arg
//! / Model-field collision makes the split leg diverge from the direct leg — the
//! two production bugs this cycle, caught with **no hand-written oracle**. Because
//! both legs share the emitters, any bug in them is present on the split leg only
//! by *effect* (the direct leg never projects), so the diff localises it.
//!
//! ## Why it is sound (no false positives, nothing to seed)
//! The harness deliberately emits **only the read/write-set + msg-arg plumbing** —
//! NOT the guard, `withRequest`, or signed-session override the backend layers on.
//! Those are security overrides orthogonal to the plumbing; omitting them on BOTH
//! legs keeps the legs identical with respect to them, so identity is just another
//! Model field that round-trips through the wire equally on each leg. There is
//! therefore nothing to "seed" and nothing to "overlay away" (the two failure
//! modes the phase-2 design grill flagged): both legs are pure, deterministic
//! functions of `(Model, Msg)`, and the ONLY divergence source is the plumbing.
//!
//! ## The fence (which branches are checkable)
//! [`select_checkable`]. A branch is checkable iff it is a SERVER branch with a
//! derived RPC I/O (`io: Some`), it FORCES no run-position effect
//! ([`BranchVerdict::forces_effect`] — a DB read / fresh Uuid / clock is not
//! deterministic under identical stubs and is deferred to the phase-3 effect-mock
//! harness), and it is not a server-internal / chaining / client-result-root
//! branch (grill fix 4 — field-excluding those makes the check vacuous). A
//! pure-typed kernel like `System.getenvOr` is deterministic and does NOT force,
//! so a branch reaching it stays checkable (the `spa-derived-read` fixture).

use crate::spa_diff_gen::{GenModule, TypeResolver};
use crate::spa_partition::{BranchIo, ModelFieldTy, SpaPartitionReport};

/// A resolver that resolves NO nominal type — sufficient for an app whose Model
/// and Msg args are all scalars / `List` / `Maybe` / `Result` of scalars (the
/// `spa-derived-read` fixture). An app with a nominal record/union field needs
/// the HIR-backed resolver ([`HirTypeResolver`]); with this one such a field is
/// not generatable and `genModel` is withheld (loudly).
pub struct NoTypeResolver;
impl TypeResolver for NoTypeResolver {
    fn resolve(&self, _name: &str) -> Option<crate::spa_diff_gen::TypeDef> {
        None
    }
}

/// The tail ctor NAME of a branch label (`GotTodos (Ok _)` → `GotTodos`,
/// `SetRegion` → `SetRegion`).
fn ctor_of(label: &str) -> &str {
    label.split_whitespace().next().unwrap_or(label)
}

/// One server branch the fuzzer will diff.
#[derive(Clone, Debug)]
pub struct CheckableBranch {
    /// The Msg ctor name (`SetRegion`).
    pub ctor: String,
    /// The branch's derived RPC read-set / write-set.
    pub io: BranchIo,
    /// The Msg args this branch binds, with types — for `genMsg_<Ctor>`.
    pub msg_arg_tys: Vec<ModelFieldTy>,
}

/// Apply the phase-2 fence to a partition report, returning the checkable branches
/// plus loud notes for every server branch that was skipped and why.
pub fn select_checkable(report: &SpaPartitionReport) -> (Vec<CheckableBranch>, Vec<String>) {
    let mut out = Vec::new();
    let mut notes = Vec::new();

    // Branches excluded by the chaining / client-result analysis (grill fix 4).
    let excluded: std::collections::BTreeSet<String> = report
        .server_internal
        .iter()
        .cloned()
        .chain(report.chaining_branches.iter().cloned())
        .chain(report.client_result.iter().map(|(root, _)| root.clone()))
        .collect();

    let mut seen: std::collections::BTreeSet<String> = std::collections::BTreeSet::new();
    for b in &report.branches {
        if !b.server {
            continue; // client branch: no round-trip, nothing to diff
        }
        let ctor = ctor_of(&b.msg).to_string();
        // A ctor that appears in more than one arm (`GotTodos (Ok _)` /
        // `GotTodos (Err _)`) would emit a duplicate `genMsg_<Ctor>` — diff it once.
        if !seen.insert(ctor.clone()) {
            continue;
        }
        let Some(io) = &b.io else {
            notes.push(format!("skip `{ctor}`: server branch with no derived I/O"));
            continue;
        };
        // The harness rebinds the arm as `case msg of <Ctor> <arg…>`, so the
        // pattern must be a SIMPLE top-level ctor application (`SetRegion region`).
        // A nested / literal pattern (`GotTodos (Ok _)`, `Key "Enter"`) binds args
        // the emitters cannot reconstruct from the wire — defer to phase 3.
        let simple = if io.msg_args.is_empty() {
            b.msg == ctor
        } else {
            b.msg == format!("{ctor} {}", io.msg_args.join(" "))
        };
        if !simple {
            notes.push(format!(
                "skip `{}`: non-simple arm pattern (nested / literal binders the wire cannot reconstruct) — deferred to phase 3",
                b.msg
            ));
            continue;
        }
        if b.forces_effect {
            notes.push(format!(
                "skip `{ctor}`: forces a run-position effect (DB / clock / fresh Uuid) — deferred to the phase-3 effect-mock harness"
            ));
            continue;
        }
        if excluded.contains(&ctor) {
            notes.push(format!(
                "skip `{ctor}`: server-internal / chaining / client-result root (field-excluding its writes would make the check vacuous)"
            ));
            continue;
        }
        out.push(CheckableBranch {
            ctor,
            io: io.clone(),
            msg_arg_tys: b.msg_arg_tys.clone(),
        });
    }
    (out, notes)
}

/// The emitted harness.
#[derive(Debug, Default)]
pub struct HarnessOutput {
    /// The Sky source SNIPPET to append to a copy of the entry module (whose
    /// `main` has been stripped): the generator prelude + `genModel` + `genMsg` +
    /// per-branch `checkOne` arms + the fuzz driver + a new `main`.
    pub snippet: String,
    /// Loud skips (a branch whose Msg / Model could not be generated, a fence
    /// skip carried in from [`select_checkable`], …).
    pub notes: Vec<String>,
    /// The ctors the harness actually diffs (a `checkOne` arm was emitted AND a
    /// `genMsg_<Ctor>` was generatable). Empty ⇒ the harness proves nothing.
    pub checked: Vec<String>,
    /// Extra imports the snippet needs (the caller merges these into the entry
    /// module's import list, deduped).
    pub required_imports: Vec<String>,
}

/// Emit the differential-fuzzer harness snippet.
///
/// * `model_type` / `msg_type` — the app's `Model` / `Msg` type names.
/// * `model_fields` — the Model's fields (`report.model_fields`).
/// * `checkable` — the fenced branch set ([`select_checkable`]).
/// * `resolver` — resolves nominal user types for the value generators.
/// * `iters` / `seed0` — the fuzz loop's iteration count + starting seed.
pub fn emit_harness(
    model_type: &str,
    msg_type: &str,
    model_fields: &[ModelFieldTy],
    checkable: &[CheckableBranch],
    resolver: &dyn TypeResolver,
    iters: usize,
    seed0: i64,
) -> HarnessOutput {
    let mut out = HarnessOutput::default();

    // --- generators -------------------------------------------------------
    let mut gen = GenModule::new(resolver);
    let model_ok = gen.emit_model(model_type, model_fields);
    if !model_ok {
        // A withheld genModel means a Model field was not generatable — the whole
        // harness cannot run (it needs a random Model). Surface it and emit
        // nothing runnable; the caller treats an empty `checked` as "not proven".
        let gout = gen.finish();
        out.notes.extend(gout.notes);
        out.notes.push(format!(
            "genModel for `{model_type}` was withheld — no runnable harness (a Model field's type is not generatable)"
        ));
        return out;
    }

    // A genMsg_<Ctor> per checkable branch (skips a branch whose args are not
    // generatable, with a note; that branch simply is not diffed).
    let mut checked: Vec<CheckableBranch> = Vec::new();
    for b in checkable {
        if gen.emit_msg(msg_type, &b.ctor, &b.msg_arg_tys) {
            checked.push(b.clone());
        }
    }
    gen.emit_msg_dispatch();
    let gout = gen.finish();
    out.notes.extend(gout.notes);

    if checked.is_empty() {
        out.notes
            .push("no checkable branch had a generatable Msg — harness proves nothing".to_string());
        out.snippet = gout.source;
        return out;
    }

    // --- checkOne + driver + main ----------------------------------------
    let model_field_names: Vec<String> = model_fields.iter().map(|f| f.name.clone()).collect();
    let mut s = String::new();
    s.push_str(&gout.source);
    s.push('\n');
    s.push_str(&emit_check_one(model_type, msg_type, model_fields, &model_field_names, &checked));
    s.push('\n');
    s.push_str(&emit_driver(iters, seed0));

    out.snippet = s;
    out.checked = checked.iter().map(|b| b.ctor.clone()).collect();
    // Imports the snippet needs. The caller injects these, deduped: the two
    // shared-alias imports (`Cmd` for apply-delta's `Cmd.none`, `String` for the
    // prelude's `String.fromInt`) are skipped when the app already imports that
    // module; the two UNIQUE-alias imports (`SpaDiffLog`, `SpaDiffError`, used by
    // `main`) are always added — a second alias of a module the app may already
    // import under its own name, so they never collide with app bindings.
    out.required_imports = vec![
        "import Std.Cmd as Cmd".to_string(),
        "import Sky.Core.String as String".to_string(),
        "import Std.Log as SpaDiffLog".to_string(),
        "import Sky.Core.Error as SpaDiffError".to_string(),
    ];
    out
}

/// `checkOne : Model -> Msg -> Result String () ` — one arm per checkable branch,
/// each running the direct leg vs the split-plumbing leg and comparing.
fn emit_check_one(
    model_type: &str,
    msg_type: &str,
    model_fields: &[ModelFieldTy],
    model_field_names: &[String],
    checked: &[CheckableBranch],
) -> String {
    use crate::spa_split::{
        emit_apply_delta, emit_build_req, emit_ctor_app, emit_reconstruct, emit_write_set_encode,
    };

    let mut s = String::new();
    s.push_str("-- Differential split-fuzzer check: run each checkable branch two ways\n");
    s.push_str("-- (direct vs the Sky.Spa split plumbing) over the SAME (Model, Msg) and\n");
    s.push_str("-- assert they agree. A read/write-set drop or a Msg-arg collision diverges.\n");
    s.push_str(&format!("spaDiffCheckOne : {model_type} -> {msg_type} -> Result String ()\n"));
    s.push_str("spaDiffCheckOne model msg =\n");
    s.push_str("    case msg of\n");

    for b in checked {
        // The case pattern rebinds the args by their source names (io.msg_args),
        // so `emit_build_req`'s bare-arg references resolve.
        let pattern = if b.io.msg_args.is_empty() {
            b.ctor.clone()
        } else {
            format!("{} {}", b.ctor, b.io.msg_args.join(" "))
        };
        let build_req = emit_build_req(&b.io, "model", model_field_names);
        // reconstruct: `m = …` (+ `( base, _ ) = init ()` in the narrow shapes) at
        // 16-space indent, exactly the backend handler's `let` body indentation.
        let reconstruct = emit_reconstruct(&b.io, model_fields);
        let ctor_app = emit_ctor_app(&b.ctor, &b.io, model_fields);
        let resp_field_names: Vec<String> = if b.io.writes_whole_model {
            model_field_names.to_vec()
        } else {
            b.io.write_fields.clone()
        };
        let write_set = emit_write_set_encode(&b.io, &resp_field_names, "m2");
        // apply-delta returns the whole `( <model>, Cmd.none )` tuple; bind and
        // project the model. Trim its leading indent so it sits at 20 spaces.
        let apply = emit_apply_delta(&b.io, "model");
        let apply = apply.trim_start();

        s.push_str(&format!("        {pattern} ->\n"));
        s.push_str("            let\n");
        s.push_str(&format!("                p =\n                    {build_req}\n\n"));
        s.push_str(&reconstruct);
        s.push('\n');
        s.push_str(&format!(
            "                ( m2, _ ) =\n                    update {ctor_app} m\n\n"
        ));
        s.push_str(&format!("                resp =\n                    {write_set}\n\n"));
        s.push_str(&format!("                ( applied, _ ) =\n                    {apply}\n\n"));
        s.push_str("                ( direct, _ ) =\n                    update msg model\n");
        s.push_str("            in\n");
        s.push_str("            if direct == applied then\n");
        s.push_str("                Ok ()\n\n");
        s.push_str("            else\n");
        s.push_str(&format!(
            "                Err \"{}: split-leg model diverged from the direct update (a read/write-set drop or a Msg-arg collision)\"\n\n",
            b.ctor
        ));
    }

    // A catch-all: any ctor the generator did not produce is not diffed.
    s.push_str("        _ ->\n");
    s.push_str("            Ok ()\n\n\n");
    s
}

/// The fuzz driver + `main`. Pure loop (`genModel` + `genMsg` + `spaDiffCheckOne`
/// are pure); `main` prints and `Task.fail`s on the first divergence so the
/// process exits non-zero — the gate goes red.
fn emit_driver(iters: usize, seed0: i64) -> String {
    format!(
        "spaDiffOne : Seed -> Result String Seed\n\
         spaDiffOne s0 =\n\
         \x20   let\n\
         \x20       ( model, s1 ) =\n\
         \x20           genModel s0\n\n\
         \x20       ( msg, s2 ) =\n\
         \x20           genMsg s1\n\
         \x20   in\n\
         \x20   case spaDiffCheckOne model msg of\n\
         \x20       Ok _ ->\n\
         \x20           Ok s2\n\n\
         \x20       Err e ->\n\
         \x20           Err e\n\n\n\
         spaDiffLoop : Int -> Seed -> Int -> Result String Int\n\
         spaDiffLoop remaining s count =\n\
         \x20   if remaining <= 0 then\n\
         \x20       Ok count\n\n\
         \x20   else\n\
         \x20       case spaDiffOne s of\n\
         \x20           Ok s2 ->\n\
         \x20               spaDiffLoop (remaining - 1) s2 (count + 1)\n\n\
         \x20           Err e ->\n\
         \x20               Err e\n\n\n\
         spaDiffIters : Int\n\
         spaDiffIters =\n\
         \x20   {iters}\n\n\n\
         spaDiffSeed0 : Seed\n\
         spaDiffSeed0 =\n\
         \x20   {seed0}\n\n\n\
         main : Task Error ()\n\
         main =\n\
         \x20   case spaDiffLoop spaDiffIters spaDiffSeed0 0 of\n\
         \x20       Ok c ->\n\
         \x20           SpaDiffLog.println (\"spa-diff-fuzz ok: \" ++ String.fromInt c ++ \" checks passed\")\n\n\
         \x20       Err e ->\n\
         \x20           SpaDiffLog.println (\"spa-diff-fuzz FAIL: \" ++ e)\n\
         \x20               |> Task.andThen (\\_ -> Task.fail (SpaDiffError.unexpected (\"spa-diff-fuzz divergence: \" ++ e)))\n"
    )
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::spa_diff_gen::{TypeDef, TypeResolver};
    use std::collections::HashMap;

    struct Env(HashMap<String, TypeDef>);
    impl TypeResolver for Env {
        fn resolve(&self, name: &str) -> Option<TypeDef> {
            self.0.get(name).cloned()
        }
    }
    fn env() -> Env {
        Env(HashMap::new())
    }

    fn f(name: &str, t: &str) -> ModelFieldTy {
        ModelFieldTy {
            name: name.to_string(),
            ty_name: t.to_string(),
            codec: None,
            ty: Some(ty::Ty::app(t, vec![])),
        }
    }

    // A `reads_whole_model` + `writes_whole_model` branch whose arg name COLLIDES
    // with a Model field — the region-switch shape. The emitted plumbing must read
    // the arg back under `spaMsgArg_scale`, not `p.scale`.
    #[test]
    fn collision_branch_reconstructs_arg_under_renamed_wire_field() {
        let model_fields = vec![f("n", "Int"), f("scale", "Int"), f("log", "String")];
        let io = BranchIo {
            reads_whole_model: true,
            read_fields: vec![],
            msg_args: vec!["scale".to_string()],
            writes_whole_model: true,
            write_fields: vec![],
        };
        let checkable = vec![CheckableBranch {
            ctor: "SetScaleArg".to_string(),
            io,
            msg_arg_tys: vec![f("scale", "Int")],
        }];
        let e = env();
        let out = emit_harness("Model", "Msg", &model_fields, &checkable, &e, 50, 7);
        assert_eq!(out.checked, vec!["SetScaleArg".to_string()]);
        // The arg rides the request AND is read back under the renamed field.
        assert!(
            out.snippet.contains("spaMsgArg_scale = scale"),
            "build_req must send the arg under the renamed wire field:\n{}",
            out.snippet
        );
        assert!(
            out.snippet.contains("update (SetScaleArg p.spaMsgArg_scale) m"),
            "ctor_app must read the arg back under the renamed field:\n{}",
            out.snippet
        );
        // Both legs present.
        assert!(out.snippet.contains("update msg model"), "direct leg missing");
        assert!(out.snippet.contains("if direct == applied then"), "diff missing");
        well_formed(&out.snippet);
    }

    // A narrow read-set / write-set branch: the request carries only the read
    // field(s) + args, the response only the write field(s).
    #[test]
    fn narrow_branch_carries_only_read_and_write_fields() {
        let model_fields = vec![f("count", "Int"), f("label", "String"), f("dirty", "Bool")];
        let io = BranchIo {
            reads_whole_model: false,
            read_fields: vec!["count".to_string()],
            msg_args: vec![],
            writes_whole_model: false,
            write_fields: vec!["count".to_string()],
        };
        let checkable = vec![CheckableBranch {
            ctor: "Inc".to_string(),
            io,
            msg_arg_tys: vec![],
        }];
        let e = env();
        let out = emit_harness("Model", "Msg", &model_fields, &checkable, &e, 50, 1);
        assert_eq!(out.checked, vec!["Inc".to_string()]);
        // Request built from the read field only; response from the write field.
        assert!(out.snippet.contains("count = model.count"), "req read field:\n{}", out.snippet);
        assert!(out.snippet.contains("( base, _ ) =\n                    init ()"), "narrow reconstruct seeds base from init:\n{}", out.snippet);
        assert!(out.snippet.contains("count = m2.count"), "resp write field:\n{}", out.snippet);
        well_formed(&out.snippet);
    }

    use crate::spa_partition::{BranchVerdict, SpaPartitionReport};

    fn bv(msg: &str, server: bool, io: Option<BranchIo>, forces: bool) -> BranchVerdict {
        BranchVerdict {
            msg: msg.to_string(),
            server,
            reason: String::new(),
            io,
            msg_arg_tys: vec![],
            forces_effect: forces,
        }
    }
    fn report_with(branches: Vec<BranchVerdict>) -> SpaPartitionReport {
        SpaPartitionReport {
            project: "t".into(),
            entry_module: "Main".into(),
            update_name: Some("update".into()),
            update_module_name: Some("Main".into()),
            branches,
            whole_update: None,
            tainted: vec![],
            model_fields: vec![],
            subscribes_topics: false,
            publishes: false,
            notes: vec![],
            init_model_server_reads: vec![],
            server_internal: vec!["Internal".into()],
            chaining_branches: vec!["Chained".into()],
            client_result: vec![("ClientRoot".into(), "GotIt".into())],
            server_chain_warnings: vec![],
        }
    }
    fn io_args(args: &[&str]) -> BranchIo {
        BranchIo {
            reads_whole_model: false,
            read_fields: vec!["a".into()],
            msg_args: args.iter().map(|s| s.to_string()).collect(),
            writes_whole_model: false,
            write_fields: vec!["a".into()],
        }
    }

    #[test]
    fn fence_selects_effect_free_server_branches_and_skips_the_rest() {
        let branches = vec![
            bv("SetRegion region", true, Some(io_args(&["region"])), false), // checkable
            bv("Pure", false, None, false),                          // client → skip
            bv("Loads", true, Some(io_args(&[])), true),             // forces effect → skip
            bv("Internal", true, Some(io_args(&[])), false),         // server-internal → skip
            bv("Chained", true, Some(io_args(&[])), false),          // chaining → skip
            bv("ClientRoot", true, Some(io_args(&[])), false),       // client-result root → skip
            bv("GotTodos (Ok _)", true, Some(io_args(&[])), false),  // nested pattern → skip
        ];
        let (checkable, _notes) = select_checkable(&report_with(branches));
        let ctors: Vec<&str> = checkable.iter().map(|c| c.ctor.as_str()).collect();
        assert_eq!(ctors, vec!["SetRegion"], "only the effect-free simple server branch is checkable");
    }

    #[test]
    fn fence_dedups_a_ctor_appearing_in_multiple_arms() {
        // Same ctor in two arms → diffed once (no duplicate genMsg_<Ctor>).
        let branches = vec![
            bv("Toggle id", true, Some(io_args(&["id"])), false),
            bv("Toggle id", true, Some(io_args(&["id"])), false),
        ];
        let (checkable, _) = select_checkable(&report_with(branches));
        assert_eq!(checkable.len(), 1, "a repeated ctor is selected once");
    }

    #[test]
    fn withheld_model_yields_no_runnable_harness() {
        // A Model field whose type does not resolve → genModel withheld.
        let model_fields = vec![ModelFieldTy {
            name: "opaque".to_string(),
            ty_name: "Widget".to_string(),
            codec: None,
            ty: Some(ty::Ty::app("Widget", vec![])),
        }];
        let e = env();
        let out = emit_harness("Model", "Msg", &model_fields, &[], &e, 10, 1);
        assert!(out.checked.is_empty());
        assert!(out.notes.iter().any(|n| n.contains("withheld")), "notes: {:?}", out.notes);
    }

    // Bracket / paren balance, reused from spa_diff_gen's shape check.
    fn well_formed(src: &str) {
        let (mut paren, mut brack, mut brace) = (0i32, 0i32, 0i32);
        let mut in_str = false;
        let mut prev = ' ';
        for c in src.chars() {
            if in_str {
                if c == '"' && prev != '\\' {
                    in_str = false;
                }
                prev = c;
                continue;
            }
            match c {
                '"' => in_str = true,
                '(' => paren += 1,
                ')' => paren -= 1,
                '[' => brack += 1,
                ']' => brack -= 1,
                '{' => brace += 1,
                '}' => brace -= 1,
                _ => {}
            }
            assert!(paren >= 0 && brack >= 0 && brace >= 0, "unbalanced close in:\n{src}");
            prev = c;
        }
        assert_eq!(paren, 0, "unbalanced parens:\n{src}");
        assert_eq!(brack, 0, "unbalanced brackets:\n{src}");
        assert_eq!(brace, 0, "unbalanced braces:\n{src}");
    }
}
