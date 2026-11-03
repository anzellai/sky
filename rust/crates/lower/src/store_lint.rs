//! Compile-time Store builder lint (v0.29.2).
//!
//! A literal column name given to a `Std.Db.Store` builder (`Store.uniqueOn
//! [ "emial" ]`, `Store.primaryKey`, `Store.index`, …), to `Store.findBy`, or
//! to a query builder on a `Store.query` chain (`orderAsc`, `orderDesc`, and
//! the conditions inside `where_`) is checked against the columns of that
//! store's codec. At run time an unknown name makes every verb on the store
//! fail (`Store "t": Store.unique: unknown column …`), so `sky check` names it
//! first, with the column list and a did-you-mean.
//!
//! The lint only speaks when it is sure:
//! * The columns must be known from the source: a codec built with
//!   `Codec.auto` / `autoCamel` / `autoWith` over a record literal witness, or
//!   a `Codec.object … |> Codec.field "name" … |> Codec.buildObject` chain of
//!   literal names, reached through zero-parameter top-level definitions.
//!   Anything else (an `inline`, a non-literal name, a local, a codec from a
//!   function) gives no columns and no warning.
//! * A name the chain declares as an external column (one the codec does not
//!   carry: `defaultWith`, `defaultNow`, `touchOnUpdate`, the `default*`
//!   builders, `generated`) is a known column.
//! * Those external-capable builders accept any column name (it names a
//!   column the table has outside the codec), so they warn only for a name
//!   that differs from a codec column by case or `_` alone, which is a
//!   misspelling of it rather than a column of its own.
//!
//! A name resolves as the runtime resolves it (`Store.sky` `resolveCol`): the
//! name itself, or its snake_case form (`camelToSnake`, codec_auto.go), is a
//! column.

use base::DefId;
use hir::{Body, Expr, ExprId, Res};

/// The top-level definitions the lint can follow.
pub(crate) trait DefLookup {
    /// `(module, name, body)` of a top-level definition.
    fn lookup(&self, d: DefId) -> Option<(&str, &str, &Body)>;
}

/// One warning: the literal it is about, in the linted body, and the text.
pub(crate) struct Finding {
    pub at: ExprId,
    pub message: String,
}

/// How deep the lint follows definitions before it gives up.
const MAX_DEPTH: usize = 16;

/// Builders that take only codec columns, with their argument count before
/// the store.
const CODEC_ONLY: &[(&str, usize)] = &[
    ("primaryKey", 1),
    ("serial", 1),
    ("unique", 1),
    ("index", 1),
    ("uniqueOn", 1),
    ("uniqueWhere", 2),
];

/// Builders that also take an external column, with their argument count
/// before the store.
const EXTERNAL_CAPABLE: &[(&str, usize)] = &[
    ("defaultNow", 1),
    ("touchOnUpdate", 1),
    ("defaultText", 2),
    ("defaultInt", 2),
    ("defaultBool", 2),
    ("defaultFloat", 2),
    ("defaultDecimal", 2),
    ("defaultWith", 2),
    ("generated", 1),
];

/// Query builders that keep the query's columns, with their argument count
/// before the query.
const QUERY_KEEPS: &[(&str, usize)] = &[
    ("where_", 1),
    ("orderAsc", 1),
    ("orderDesc", 1),
    ("limit", 1),
    ("offset", 1),
];

/// Conditions whose first argument is a column name.
const COND_COLUMN: &[&str] = &[
    "eq",
    "neq",
    "gt",
    "gte",
    "lt",
    "lte",
    "like",
    "eqCi",
    "contains",
    "containsCi",
    "startsWith",
    "endsWith",
    "inList",
];

const STORE: &str = "Std.Db.Store";
const CODEC: &str = "Std.Codec";

/// The columns of a store, as far as the source shows them.
struct Cols {
    table: String,
    /// The codec's columns.
    codec: Vec<String>,
    /// Columns the chain declares outside the codec.
    external: Vec<String>,
}

impl Cols {
    fn knows(&self, name: &str) -> bool {
        let snake = camel_to_snake(name);
        self.codec.iter().any(|c| c == name || *c == snake)
            || self.external.iter().any(|c| c == name || *c == snake)
    }

    /// A codec column `name` misspells by case or `_` alone.
    fn respelled(&self, name: &str) -> Option<&str> {
        let n = fold(name);
        self.codec.iter().find(|c| fold(c) == n).map(|c| c.as_str())
    }

    fn closest(&self, name: &str) -> Option<&str> {
        self.codec
            .iter()
            .map(|c| (levenshtein(&name.to_lowercase(), &c.to_lowercase()), c))
            .filter(|(d, _)| *d <= 2)
            .min_by_key(|(d, _)| *d)
            .map(|(_, c)| c.as_str())
    }
}

/// Every warning for the literals of `body`.
pub(crate) fn lint(body: &Body, defs: &dyn DefLookup) -> Vec<Finding> {
    let mut out: Vec<Finding> = Vec::new();
    let mut seen: Vec<ExprId> = Vec::new();
    for (id, _) in body.exprs.iter() {
        let (callee, args) = app(body, id);
        let Some((module, name)) = stdlib_name(body, callee, defs) else {
            continue;
        };
        if module != STORE {
            continue;
        }
        let mut check = |lits: Vec<(ExprId, String)>,
                         cols: &Cols,
                         builder: &str,
                         external: bool| {
            for (at, col) in lits {
                if seen.contains(&at) || col.contains('.') || cols.knows(&col) {
                    continue;
                }
                let message = if external {
                    let Some(meant) = cols.respelled(&col) else {
                        continue;
                    };
                    format!(
                        "`Store.{builder} \"{col}\"`: the store \"{table}\" has the codec column \
                         \"{meant}\", and \"{col}\" differs from it only by case or `_`. As written it \
                         names a column outside the codec, which the table must have, so `migrate` \
                         and `create` fail. Write \"{meant}\".",
                        table = cols.table
                    )
                } else {
                    let hint = cols
                        .closest(&col)
                        .map(|c| format!(" (did you mean \"{c}\"?)"))
                        .unwrap_or_default();
                    format!(
                        "`Store.{builder} \"{col}\"`: the store \"{table}\" has no column \"{col}\"{hint}. \
                         Its columns are {list}. Every verb on the store returns an Err until the \
                         name is fixed.",
                        table = cols.table,
                        list = column_list(cols)
                    )
                };
                seen.push(at);
                out.push(Finding { at, message });
            }
        };
        if let Some(n) = arity(CODEC_ONLY, name) {
            if args.len() == n + 1 {
                if let Some(cols) = store_cols(body, args[n], defs, 0) {
                    check(literal_names(body, args[0]), &cols, name, false);
                }
            }
        } else if let Some(n) = arity(EXTERNAL_CAPABLE, name) {
            if args.len() == n + 1 {
                if let Some(cols) = store_cols(body, args[n], defs, 0) {
                    check(literal_names(body, args[0]), &cols, name, true);
                }
            }
        } else if name == "findBy" && args.len() == 4 {
            if let Some(cols) = store_cols(body, args[1], defs, 0) {
                check(literal_names(body, args[2]), &cols, name, false);
            }
        } else if (name == "orderAsc" || name == "orderDesc") && args.len() == 2 {
            if let Some(cols) = query_cols(body, args[1], defs, 0) {
                check(literal_names(body, args[0]), &cols, name, false);
            }
        } else if name == "where_" && args.len() == 2 {
            if let Some(cols) = query_cols(body, args[1], defs, 0) {
                let mut lits = Vec::new();
                cond_names(body, args[0], defs, &mut lits, 0);
                for (at, col, cond) in lits {
                    check(vec![(at, col)], &cols, cond, false);
                }
            }
        }
    }
    out
}

fn arity(table: &[(&str, usize)], name: &str) -> Option<usize> {
    table.iter().find(|(n, _)| *n == name).map(|(_, a)| *a)
}

fn column_list(cols: &Cols) -> String {
    let mut all: Vec<&str> = cols.codec.iter().map(|s| s.as_str()).collect();
    all.extend(cols.external.iter().map(|s| s.as_str()));
    all.join(", ")
}

/// The callee of an application and all its arguments in order, through
/// curried calls and `|>` / `<|`. A bare expression is its own callee.
fn app(body: &Body, e: ExprId) -> (ExprId, Vec<ExprId>) {
    match &body.exprs[e] {
        Expr::Call(f, args) => {
            let (callee, mut first) = app(body, *f);
            first.extend(args.iter().copied());
            (callee, first)
        }
        Expr::Binop { op, lhs, rhs, .. } if op.as_str() == "|>" => {
            let (callee, mut first) = app(body, *rhs);
            first.push(*lhs);
            (callee, first)
        }
        Expr::Binop { op, lhs, rhs, .. } if op.as_str() == "<|" => {
            let (callee, mut first) = app(body, *lhs);
            first.push(*rhs);
            (callee, first)
        }
        _ => (e, Vec::new()),
    }
}

/// `(module, name)` of a reference to a top-level definition.
fn stdlib_name<'a>(body: &Body, e: ExprId, defs: &'a dyn DefLookup) -> Option<(&'a str, &'a str)> {
    match &body.exprs[e] {
        Expr::Var(Res::Def(d)) => defs.lookup(*d).map(|(m, n, _)| (m, n)),
        _ => None,
    }
}

/// The root of a zero-parameter top-level definition `e` refers to.
fn follow<'a>(body: &Body, e: ExprId, defs: &'a dyn DefLookup) -> Option<(&'a Body, ExprId)> {
    let Expr::Var(Res::Def(d)) = &body.exprs[e] else {
        return None;
    };
    let (_, _, b) = defs.lookup(*d)?;
    if !b.params.is_empty() {
        return None;
    }
    b.root.map(|r| (b, r))
}

/// The string literals of a name argument: one `"x"`, or a list of them.
fn literal_names(body: &Body, e: ExprId) -> Vec<(ExprId, String)> {
    match &body.exprs[e] {
        Expr::Str(s) => vec![(e, s.to_string())],
        Expr::List(items) => items
            .iter()
            .filter_map(|i| match &body.exprs[*i] {
                Expr::Str(s) => Some((*i, s.to_string())),
                _ => None,
            })
            .collect(),
        _ => Vec::new(),
    }
}

/// The columns of the store `e` builds.
fn store_cols(body: &Body, e: ExprId, defs: &dyn DefLookup, depth: usize) -> Option<Cols> {
    if depth > MAX_DEPTH {
        return None;
    }
    let (callee, args) = app(body, e);
    if args.is_empty() {
        let (b, root) = follow(body, callee, defs)?;
        return store_cols(b, root, defs, depth + 1);
    }
    let (module, name) = stdlib_name(body, callee, defs)?;
    if module != STORE {
        return None;
    }
    if name == "fromCodec" && args.len() == 2 {
        let Expr::Str(table) = &body.exprs[args[0]] else {
            return None;
        };
        let codec = codec_cols(body, args[1], defs, depth + 1)?;
        return Some(Cols {
            table: table.to_string(),
            codec,
            external: Vec::new(),
        });
    }
    if let Some(n) = arity(CODEC_ONLY, name) {
        if args.len() == n + 1 {
            return store_cols(body, args[n], defs, depth + 1);
        }
    }
    if let Some(n) = arity(EXTERNAL_CAPABLE, name) {
        if args.len() == n + 1 {
            let mut cols = store_cols(body, args[n], defs, depth + 1)?;
            for (_, col) in literal_names(body, args[0]) {
                if !cols.knows(&col) {
                    cols.external.push(col);
                }
            }
            return Some(cols);
        }
    }
    None
}

/// The columns of the query `e` builds: a `Store.query store` under builders
/// that keep its columns.
fn query_cols(body: &Body, e: ExprId, defs: &dyn DefLookup, depth: usize) -> Option<Cols> {
    if depth > MAX_DEPTH {
        return None;
    }
    let (callee, args) = app(body, e);
    if args.is_empty() {
        let (b, root) = follow(body, callee, defs)?;
        return query_cols(b, root, defs, depth + 1);
    }
    let (module, name) = stdlib_name(body, callee, defs)?;
    if module != STORE {
        return None;
    }
    if name == "query" && args.len() == 1 {
        return store_cols(body, args[0], defs, depth + 1);
    }
    let n = arity(QUERY_KEEPS, name)?;
    if args.len() == n + 1 {
        return query_cols(body, args[n], defs, depth + 1);
    }
    None
}

/// The literal column names of a condition, through `and_` / `or_` / `not_`.
fn cond_names<'a>(
    body: &Body,
    e: ExprId,
    defs: &'a dyn DefLookup,
    out: &mut Vec<(ExprId, String, &'a str)>,
    depth: usize,
) {
    if depth > MAX_DEPTH {
        return;
    }
    let (callee, args) = app(body, e);
    let Some((module, name)) = stdlib_name(body, callee, defs) else {
        return;
    };
    if module != STORE {
        return;
    }
    if COND_COLUMN.contains(&name) && args.len() == 2 {
        if let Expr::Str(s) = &body.exprs[args[0]] {
            out.push((args[0], s.to_string(), name));
        }
    } else if (name == "and_" || name == "or_") && args.len() == 1 {
        if let Expr::List(items) = &body.exprs[args[0]] {
            for i in items {
                cond_names(body, *i, defs, out, depth + 1);
            }
        }
    } else if name == "not_" && args.len() == 1 {
        cond_names(body, args[0], defs, out, depth + 1);
    }
}

/// The columns of a codec, when the source shows them.
fn codec_cols(body: &Body, e: ExprId, defs: &dyn DefLookup, depth: usize) -> Option<Vec<String>> {
    if depth > MAX_DEPTH {
        return None;
    }
    let (callee, args) = app(body, e);
    if args.is_empty() {
        let (b, root) = follow(body, callee, defs)?;
        return codec_cols(b, root, defs, depth + 1);
    }
    let (module, name) = stdlib_name(body, callee, defs)?;
    if module != CODEC {
        return None;
    }
    match (name, args.len()) {
        ("auto", 1) | ("autoWith", 2) => {
            let fields = record_fields(body, *args.last()?, defs, depth + 1)?;
            Some(fields.iter().map(|f| camel_to_snake(f)).collect())
        }
        ("autoCamel", 1) => record_fields(body, args[0], defs, depth + 1),
        ("buildObject", 1) => object_fields(body, args[0], defs, depth + 1),
        _ => None,
    }
}

/// The literal field names of a `Codec.object … |> Codec.field "n" …` chain.
fn object_fields(
    body: &Body,
    e: ExprId,
    defs: &dyn DefLookup,
    depth: usize,
) -> Option<Vec<String>> {
    if depth > MAX_DEPTH {
        return None;
    }
    let (callee, args) = app(body, e);
    if args.is_empty() {
        let (b, root) = follow(body, callee, defs)?;
        return object_fields(b, root, defs, depth + 1);
    }
    let (module, name) = stdlib_name(body, callee, defs)?;
    if module != CODEC {
        return None;
    }
    match (name, args.len()) {
        ("object", 1) => Some(Vec::new()),
        ("field", 4) => {
            let Expr::Str(n) = &body.exprs[args[0]] else {
                return None;
            };
            let mut fields = object_fields(body, args[3], defs, depth + 1)?;
            fields.push(n.to_string());
            Some(fields)
        }
        _ => None,
    }
}

/// The field names of a record literal witness.
fn record_fields(
    body: &Body,
    e: ExprId,
    defs: &dyn DefLookup,
    depth: usize,
) -> Option<Vec<String>> {
    if depth > MAX_DEPTH {
        return None;
    }
    match &body.exprs[e] {
        Expr::Record(fields) => Some(fields.iter().map(|(n, _)| n.as_str().to_string()).collect()),
        Expr::Var(Res::Def(_)) => {
            let (b, root) = follow(body, e, defs)?;
            record_fields(b, root, defs, depth + 1)
        }
        _ => None,
    }
}

/// `camelToSnake` of runtime-go/rt/codec_auto.go: an upper-case ASCII letter
/// becomes `_` and its lower case (no `_` at the start).
pub(crate) fn camel_to_snake(s: &str) -> String {
    let mut out = String::with_capacity(s.len() + 4);
    for (i, c) in s.chars().enumerate() {
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

fn fold(s: &str) -> String {
    s.chars()
        .filter(|c| *c != '_')
        .map(|c| c.to_ascii_lowercase())
        .collect()
}

fn levenshtein(a: &str, b: &str) -> usize {
    let a: Vec<char> = a.chars().collect();
    let b: Vec<char> = b.chars().collect();
    let mut prev: Vec<usize> = (0..=b.len()).collect();
    for (i, ca) in a.iter().enumerate() {
        let mut cur = vec![i + 1; b.len() + 1];
        for (j, cb) in b.iter().enumerate() {
            let cost = usize::from(ca != cb);
            cur[j + 1] = (prev[j] + cost).min(prev[j + 1] + 1).min(cur[j] + 1);
        }
        prev = cur;
    }
    prev[b.len()]
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn snake_matches_the_runtime() {
        assert_eq!(camel_to_snake("priceMinor"), "price_minor");
        assert_eq!(camel_to_snake("id"), "id");
        assert_eq!(camel_to_snake("createdAt"), "created_at");
    }
}
