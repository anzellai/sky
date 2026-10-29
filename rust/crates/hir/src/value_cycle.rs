//! `[E1018]`: a value defined in terms of itself ("bad recursion").
//!
//! A top-level value (a definition with no parameters whose body is not a
//! lambda) is computed once, the first time something reads it. A value that
//! reads itself while it is being computed, directly (`xs = 1 :: xs`) or through
//! other values (`a = b + 1` and `b = a * 2`), never finishes. Before v0.29.0
//! such a program passed `sky check`: a top-level cycle deadlocked on its first
//! read at run time, and a cycle inside a `let` failed `go build` (`undefined:
//! b_3`, or Go's own `initialization cycle`). Elm refuses the same programs
//! ("BAD RECURSION"), and so does Sky now.
//!
//! What counts as a read. Only references that are EVALUATED while the value is
//! computed are edges of the graph:
//!
//! * a reference to another value, anywhere except under a lambda or inside a
//!   local function (a `let` binding with parameters), whose body runs later;
//! * a CALL of a function (the callee of an application with at least as many
//!   arguments as the function has parameters, `x |> f` included): calling a
//!   function runs its body, so the values its body reads are read too. A
//!   function named without being called (`{ handler = f }`, a partial
//!   application `f 1` of a two-parameter `f`) runs nothing and adds no edge.
//!
//! Functions that call each other are ordinary recursion and are never
//! reported: a cycle is an error only when a value is on it. The rule is applied
//! to the top level of a module and to every `let`, each a graph of its own. A
//! cycle across modules would need an import cycle, which `[E1010]` refuses.

use crate::hir::{Body, Expr, ExprId, LocalDef};
use crate::ids::{LocalId, Res};
use base::DefId;
use base::Span;
use diagnostics::Diagnostic;
use indexmap::IndexMap;
use std::collections::{HashMap, HashSet};
use std::hash::Hash;

/// One node of a dependency graph: its name, whether it is a value, and where
/// it is written.
struct Node {
    name: String,
    is_value: bool,
    span: Option<Span>,
}

/// The `[E1018]` diagnostics of one module: the top-level graph, then every
/// `let` in every body.
pub(crate) fn check(
    bodies: &IndexMap<DefId, Body>,
    names: &HashMap<DefId, (String, Option<Span>)>,
) -> Vec<Diagnostic> {
    let mut out = Vec::new();
    // ---- the top level ---------------------------------------------------
    let arity: HashMap<DefId, usize> = bodies
        .iter()
        .filter(|(_, b)| b.root.is_some())
        .map(|(d, b)| (*d, def_arity(b)))
        .collect();
    let mut nodes: IndexMap<DefId, Node> = IndexMap::new();
    let mut edges: HashMap<DefId, Vec<DefId>> = HashMap::new();
    for (def, body) in bodies {
        let Some(root) = body.root else { continue };
        let Some((name, span)) = names.get(def) else {
            continue;
        };
        nodes.insert(
            *def,
            Node {
                name: name.clone(),
                is_value: is_value_body(body),
                span: *span,
            },
        );
        let mut acc = Vec::new();
        reads(body, run_root(body, root), &mut |r, called_with| {
            if let Res::Def(d) = r {
                match arity.get(d) {
                    Some(0) => acc.push(*d),
                    Some(n) if called_with.is_some_and(|k| k >= *n) => acc.push(*d),
                    _ => {}
                }
            }
        });
        edges.insert(*def, acc);
    }
    report(&nodes, &edges, &mut out);
    // ---- every `let` -----------------------------------------------------
    for body in bodies.values() {
        for (_, e) in body.exprs.iter() {
            if let Expr::Let { defs, .. } = e {
                check_let(body, defs, &mut out);
            }
        }
    }
    out
}

/// A local `let`'s own graph. A destructuring binding (`( a, b ) = …`) is one
/// value node that every name it binds stands for.
fn check_let(body: &Body, defs: &[LocalDef], out: &mut Vec<Diagnostic>) {
    // binder -> (index of its def)
    let mut owner: HashMap<LocalId, usize> = HashMap::new();
    for (i, d) in defs.iter().enumerate() {
        for (_, l) in &d.binders {
            owner.insert(*l, i);
        }
    }
    let arity_of = |i: usize| -> usize {
        let d = &defs[i];
        if d.pat.is_some() {
            0
        } else {
            d.params.len() + lambda_params(body, d.body)
        }
    };
    let mut nodes: IndexMap<usize, Node> = IndexMap::new();
    let mut edges: HashMap<usize, Vec<usize>> = HashMap::new();
    for (i, d) in defs.iter().enumerate() {
        let name = d
            .binders
            .iter()
            .map(|(n, _)| n.as_str().to_string())
            .collect::<Vec<_>>()
            .join(", ");
        let is_value = d.pat.is_some()
            || (d.params.is_empty() && !matches!(body.exprs[d.body], Expr::Lambda { .. }));
        nodes.insert(
            i,
            Node {
                name,
                is_value,
                span: body.expr_span(d.body),
            },
        );
        let mut acc = Vec::new();
        // A local function's body runs when it is called, so its reads are
        // the reads of its body; a value's are read at once. Either way the
        // edges are the body's eager reads.
        let run = if d.pat.is_some() {
            d.body
        } else {
            run_root(body, d.body)
        };
        reads(body, run, &mut |r, called_with| {
            if let Res::Local(l) = r {
                if let Some(&j) = owner.get(l) {
                    let n = arity_of(j);
                    if n == 0 || called_with.is_some_and(|k| k >= n) {
                        acc.push(j);
                    }
                }
            }
        });
        edges.insert(i, acc);
    }
    report(&nodes, &edges, out);
}

/// A body is a value when it takes no parameters and is not a lambda.
fn is_value_body(body: &Body) -> bool {
    body.params.is_empty()
        && !body
            .root
            .is_some_and(|r| matches!(body.exprs[r], Expr::Lambda { .. }))
}

/// How many arguments a top-level definition takes before its body runs.
fn def_arity(body: &Body) -> usize {
    body.params.len() + body.root.map_or(0, |r| lambda_params(body, r))
}

/// What runs when a definition is called: the body of a lambda root
/// (`f = \x -> …`), else the root itself.
fn run_root(body: &Body, e: ExprId) -> ExprId {
    match &body.exprs[e] {
        Expr::Lambda { body: inner, .. } => *inner,
        _ => e,
    }
}

/// The parameters of a lambda body (`f = \x y -> …` takes two).
fn lambda_params(body: &Body, e: ExprId) -> usize {
    match &body.exprs[e] {
        Expr::Lambda { params, .. } => params.len(),
        _ => 0,
    }
}

/// Every reference `e` evaluates while it is computed, with the number of
/// arguments it is called with (`None` when it is read as a value). Lambdas
/// and local functions are not entered: their bodies run later.
fn reads(body: &Body, e: ExprId, f: &mut dyn FnMut(&Res, Option<usize>)) {
    walk(body, e, None, f);
}

fn walk(
    body: &Body,
    e: ExprId,
    called_with: Option<usize>,
    f: &mut dyn FnMut(&Res, Option<usize>),
) {
    match &body.exprs[e] {
        Expr::Var(r) => f(r, called_with),
        Expr::Lambda { .. } => {}
        Expr::Call(callee, args) => {
            walk(
                body,
                *callee,
                Some(called_with.unwrap_or(0) + args.len()),
                f,
            );
            for a in args {
                walk(body, *a, None, f);
            }
        }
        Expr::Binop { res, lhs, rhs, .. } => {
            let (func, arg) = match res {
                Res::Kernel { func, .. } if func.as_str() == "apR" => (Some(*rhs), *lhs),
                Res::Kernel { func, .. } if func.as_str() == "apL" => (Some(*lhs), *rhs),
                _ => (None, *lhs),
            };
            match func {
                Some(callee) => {
                    walk(body, callee, Some(1), f);
                    walk(body, arg, None, f);
                }
                None => {
                    walk(body, *lhs, None, f);
                    walk(body, *rhs, None, f);
                }
            }
        }
        Expr::Let { defs, body: inner } => {
            for d in defs {
                let deferred = d.pat.is_none()
                    && (!d.params.is_empty() || matches!(body.exprs[d.body], Expr::Lambda { .. }));
                if !deferred {
                    walk(body, d.body, None, f);
                }
            }
            walk(body, *inner, None, f);
        }
        Expr::List(xs) | Expr::Tuple(xs) => {
            for x in xs {
                walk(body, *x, None, f);
            }
        }
        Expr::Record(fs) => {
            for (_, x) in fs {
                walk(body, *x, None, f);
            }
        }
        Expr::Update { base, fields } => {
            walk(body, *base, None, f);
            for (_, x) in fields {
                walk(body, *x, None, f);
            }
        }
        Expr::Negate(x) | Expr::Access(x, _) => walk(body, *x, None, f),
        Expr::If { arms, els } => {
            for (c, t) in arms {
                walk(body, *c, None, f);
                walk(body, *t, None, f);
            }
            walk(body, *els, None, f);
        }
        Expr::Case { subject, branches } => {
            walk(body, *subject, None, f);
            for b in branches {
                walk(body, b.body, None, f);
            }
        }
        Expr::Int(_)
        | Expr::Float(_)
        | Expr::Str(_)
        | Expr::Chr(_)
        | Expr::Bool(_)
        | Expr::Unit
        | Expr::Accessor(_)
        | Expr::Error => {}
    }
}

/// Report every strongly connected component that holds a value and a cycle,
/// once, at the first value of it in source order.
fn report<K: Copy + Eq + Hash>(
    nodes: &IndexMap<K, Node>,
    edges: &HashMap<K, Vec<K>>,
    out: &mut Vec<Diagnostic>,
) {
    for comp in sccs(nodes, edges) {
        let set: HashSet<K> = comp.iter().copied().collect();
        let cyclic = comp.len() > 1 || edges.get(&comp[0]).is_some_and(|es| es.contains(&comp[0]));
        if !cyclic {
            continue;
        }
        // The first value of the component in source order.
        let Some(start) = nodes
            .keys()
            .copied()
            .find(|k| set.contains(k) && nodes[k].is_value)
        else {
            continue;
        };
        let path = cycle_path(start, &set, edges);
        let names: Vec<&str> = path.iter().map(|k| nodes[k].name.as_str()).collect();
        let first = names[0];
        let shown = names.join(" -> ");
        let message = if names.len() == 2 {
            format!(
                "The value `{first}` is defined in terms of itself. A value is computed \
                 once, the first time it is read, so a value that reads itself while it is \
                 computed never finishes. Give it a parameter (`{first} _ = …`, called as \
                 `{first} ()`) when it really is a loop, or compute it from other values. \
                 See docs/errors/E1018-value-cycle.md"
            )
        } else {
            format!(
                "The value `{first}` is defined in terms of itself through {shown}. A value \
                 is computed once, the first time it is read, so a cycle of values never \
                 finishes. Break the cycle: compute one of them from other values, or give \
                 one a parameter (`{first} _ = …`, called as `{first} ()`) when it really \
                 is a loop. See docs/errors/E1018-value-cycle.md"
            )
        };
        let mut d = Diagnostic::error("E1018", message);
        if let Some(span) = nodes[&start].span {
            d = d.with_label(span, format!("`{first}` reads itself: {shown}"));
        }
        out.push(d);
    }
}

/// A shortest cycle from `start` back to `start` inside one component.
fn cycle_path<K: Copy + Eq + Hash>(
    start: K,
    set: &HashSet<K>,
    edges: &HashMap<K, Vec<K>>,
) -> Vec<K> {
    let mut prev: HashMap<K, K> = HashMap::new();
    let mut queue = std::collections::VecDeque::new();
    queue.push_back(start);
    let mut seen: HashSet<K> = HashSet::new();
    while let Some(k) = queue.pop_front() {
        for &n in edges.get(&k).map(Vec::as_slice).unwrap_or(&[]) {
            if !set.contains(&n) {
                continue;
            }
            if n == start {
                let mut path = vec![start];
                let mut cur = k;
                while cur != start {
                    path.push(cur);
                    cur = prev[&cur];
                }
                path.push(start);
                // path is start, k, …, start reversed in the middle.
                let mut mid: Vec<K> = path[1..path.len() - 1].to_vec();
                mid.reverse();
                let mut p = vec![start];
                p.extend(mid);
                p.push(start);
                return p;
            }
            if seen.insert(n) {
                prev.insert(n, k);
                queue.push_back(n);
            }
        }
    }
    vec![start, start]
}

/// Tarjan's strongly connected components, in a deterministic order (nodes in
/// insertion order, edges in recorded order).
fn sccs<K: Copy + Eq + Hash>(nodes: &IndexMap<K, Node>, edges: &HashMap<K, Vec<K>>) -> Vec<Vec<K>> {
    struct St<K> {
        index: usize,
        idx: HashMap<K, usize>,
        low: HashMap<K, usize>,
        on: HashSet<K>,
        stack: Vec<K>,
        out: Vec<Vec<K>>,
    }
    fn visit<K: Copy + Eq + Hash>(
        v: K,
        nodes: &IndexMap<K, Node>,
        edges: &HashMap<K, Vec<K>>,
        st: &mut St<K>,
    ) {
        st.idx.insert(v, st.index);
        st.low.insert(v, st.index);
        st.index += 1;
        st.stack.push(v);
        st.on.insert(v);
        for &w in edges.get(&v).map(Vec::as_slice).unwrap_or(&[]) {
            if !nodes.contains_key(&w) {
                continue;
            }
            if !st.idx.contains_key(&w) {
                visit(w, nodes, edges, st);
                let lw = st.low[&w];
                let lv = st.low.get_mut(&v).unwrap();
                *lv = (*lv).min(lw);
            } else if st.on.contains(&w) {
                let iw = st.idx[&w];
                let lv = st.low.get_mut(&v).unwrap();
                *lv = (*lv).min(iw);
            }
        }
        if st.low[&v] == st.idx[&v] {
            let mut comp = Vec::new();
            while let Some(w) = st.stack.pop() {
                st.on.remove(&w);
                comp.push(w);
                if w == v {
                    break;
                }
            }
            st.out.push(comp);
        }
    }
    let mut st = St {
        index: 0,
        idx: HashMap::new(),
        low: HashMap::new(),
        on: HashSet::new(),
        stack: Vec::new(),
        out: Vec::new(),
    };
    for &k in nodes.keys() {
        if !st.idx.contains_key(&k) {
            visit(k, nodes, edges, &mut st);
        }
    }
    st.out
}
