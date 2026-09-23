//! **`[E2011]` — one payload type per literal pub/sub topic.**
//!
//! The pub/sub surface keeps its `any` payload:
//!
//! * `Std.Cmd.publish` / `publishNoEcho : String -> any -> Cmd msg`
//! * `Std.PubSub.publish` / `publishNoEcho : String -> any -> Task Error Int`
//! * `Std.Sub.subscribeTopic : String -> (any -> msg) -> Sub msg`
//!
//! A topic is a string, so HM cannot link the value one module publishes to the
//! decoder another module subscribes with. `Cmd.publish "nums" "x"` next to
//! `Sub.subscribeTopic "nums" GotInt` built clean and failed on every delivery.
//!
//! When the topic is KNOWN at compile time (a string literal, or a top-level
//! `String` constant whose body is a literal) the checker collects every
//! publisher's payload type and every subscriber decoder's argument type for
//! that topic across the WHOLE program and requires them to agree. A mismatch
//! is `[E2011]`, naming both sites. A topic computed at run time is not seen
//! here; the runtime reports a payload its decoder cannot take as a classified
//! decode error (`runtime-go/rt/live.go`, `runSubscriberDispatch`).
//!
//! Type variables match anything (fail-open, like `[E2010]`).

use crate::Ty;
use base::Span;
use hir::{Body, Expr, ExprId, Res, SkyDb};
use std::collections::HashMap;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Role {
    Publish,
    Subscribe,
}

/// `(role)` when `(module, func)` is a publish / subscribe entry point. The
/// payload (publish) or decoder (subscribe) is always the SECOND argument.
fn role(module: &str, func: &str) -> Option<Role> {
    let m = crate::nominal::base(module);
    let std_or_bare = |want: &str| module == format!("Std.{want}") || module == want;
    match func {
        "publish" | "publishNoEcho" if (m == "Cmd" || m == "PubSub") => {
            (std_or_bare("Cmd") || std_or_bare("PubSub")).then_some(Role::Publish)
        }
        "subscribeTopic" if m == "Sub" => std_or_bare("Sub").then_some(Role::Subscribe),
        _ => None,
    }
}

/// One publish or subscribe site on a literal topic.
#[derive(Clone, Debug)]
pub struct TopicSite {
    pub topic: String,
    pub role: Role,
    /// Payload type (publish) or the decoder's argument type (subscribe).
    pub ty: Ty,
    pub module: String,
    pub def_name: String,
    pub span: Option<Span>,
    /// 1-based line of `span` in its module, for the "other site" text.
    pub line: Option<usize>,
}

/// The compile-time value of a topic argument: a string literal, or a
/// reference to a zero-argument top-level def whose body is a string literal.
fn literal_topic(body: &Body, e: ExprId, sky: &dyn SkyDb) -> Option<String> {
    match &body.exprs[e] {
        Expr::Str(s) => Some(s.to_string()),
        Expr::Var(Res::Def(def)) => {
            let loc = sky.def_loc(*def)?;
            let resolved = sky.resolve(loc.module);
            let b = resolved.bodies.get(def)?;
            if !b.params.is_empty() {
                return None;
            }
            match &b.exprs[b.root?] {
                Expr::Str(s) => Some(s.to_string()),
                _ => None,
            }
        }
        _ => None,
    }
}

/// Collect the literal-topic sites of one body.
pub fn scan_body(
    body: &Body,
    expr_ty: &HashMap<ExprId, Ty>,
    sky: &dyn SkyDb,
    module: &str,
    module_src: &str,
    def_name: &str,
    out: &mut Vec<TopicSite>,
) {
    for (eid, expr) in body.exprs.iter() {
        let Expr::Call(callee, args) = expr else {
            continue;
        };
        let Some((m, f)) = crate::form_submit::callee_name(&body.exprs[*callee], sky) else {
            continue;
        };
        let Some(role) = role(&m, &f) else { continue };
        if args.len() < 2 {
            continue;
        }
        let Some(topic) = literal_topic(body, args[0], sky) else {
            continue;
        };
        let Some(t) = expr_ty.get(&args[1]) else {
            continue;
        };
        let ty = match role {
            Role::Publish => t.clone(),
            Role::Subscribe => match t {
                Ty::Fun(a, _) => (**a).clone(),
                _ => continue,
            },
        };
        let span = body.expr_span(args[1]).or_else(|| body.expr_span(eid));
        let line = span.map(|s| {
            let at = (s.range.0 as usize).min(module_src.len());
            module_src[..at].matches('\n').count() + 1
        });
        out.push(TopicSite {
            topic,
            role,
            ty,
            module: module.to_string(),
            def_name: def_name.to_string(),
            span,
            line,
        });
    }
}

/// For every topic, the first site whose type disagrees with an earlier site,
/// paired with that earlier site. Sites are compared in the order collected
/// (module order, then source order), so the first site is the reference.
pub fn mismatches(sites: &[TopicSite]) -> Vec<(usize, usize)> {
    let mut out = Vec::new();
    let mut by_topic: HashMap<&str, Vec<usize>> = HashMap::new();
    for (i, s) in sites.iter().enumerate() {
        by_topic.entry(s.topic.as_str()).or_default().push(i);
    }
    let mut topics: Vec<&&str> = by_topic.keys().collect();
    topics.sort();
    for t in topics {
        let idx = &by_topic[*t];
        for (k, &i) in idx.iter().enumerate() {
            if let Some(&j) = idx[..k]
                .iter()
                .find(|&&j| !crate::form_submit::compatible(&sites[j].ty, &sites[i].ty))
            {
                out.push((i, j));
            }
        }
    }
    out
}

fn describe(s: &TopicSite) -> String {
    let what = match s.role {
        Role::Publish => "publishes",
        Role::Subscribe => "subscribes with a decoder that takes",
    };
    let at = match s.line {
        Some(l) => format!("{} line {l} (`{}`)", s.module, s.def_name),
        None => format!("{} (`{}`)", s.module, s.def_name),
    };
    format!("{at} {what} `{}`", s.ty.render_pretty())
}

pub fn message(this: &TopicSite, other: &TopicSite) -> String {
    format!(
        "the pub/sub topic \"{}\" carries two different payload types: {}, but {}. \
         Every publisher and every subscriber of one topic must agree on the payload \
         type, or each delivery fails to decode.",
        this.topic,
        describe(this),
        describe(other)
    )
}

pub fn suggestion() -> String {
    "give each payload type its own topic, or convert the payload to one shared type \
     (for example a record, or a String you decode in the subscriber)."
        .to_string()
}
