//! What the stdlib exported in the previous release (E-5).
//!
//! A name a stdlib module starts to export can make an OLD program's
//! unqualified reference ambiguous (`[E1012]`): `import Std.Html.Attributes
//! exposing (..)` plus `import Sky.Core.Json.Decode exposing (..)` compiled with
//! v0.26.1, and `value` became ambiguous in v0.27.0 when `Json.Decode.value`
//! was added. (v0.28.0 compares with v0.27.8: `Std.Html` plus
//! `Std.Ai.Provider`, both exposed, make a bare `embed` ambiguous.) The error is loud and the fix is one line, but the user did not
//! change anything, so the message must say why it appeared.
//!
//! The snapshot is a committed, generated file:
//! `rust/crates/hir/data/stdlib-exports-v0.27.8.tsv`. Regenerate it with
//! `rust/crates/hir/tests/regen-stdlib-exports-snapshot.sh <tag>`. Its lines are
//! `<kind>\t<module>\t<name>`, where `kind` is `module` (the module existed,
//! `name` is empty), `value`, `type` or `ctor`. A kernel pseudo-module (the
//! target of a path with no `.sky` file, such as `import Auth`) is recorded as
//! the module `kernel:<Pseudo>` with its `KERNEL_FUNCTIONS` members as values.

use std::collections::HashSet;
use std::sync::OnceLock;

/// The release the snapshot was taken from, as the message prints it.
pub const PREVIOUS_RELEASE: &str = "v0.27.8";

/// The committed snapshot text.
pub const PREVIOUS_EXPORTS_TSV: &str = include_str!("../data/stdlib-exports-v0.27.8.tsv");

/// One parsed snapshot line.
#[derive(Clone, Debug, PartialEq, Eq, Hash, PartialOrd, Ord)]
pub struct ExportRow {
    pub kind: String,
    pub module: String,
    pub name: String,
}

/// Parse a snapshot text. Blank lines and `#` comments are skipped.
pub fn parse_export_rows(text: &str) -> Vec<ExportRow> {
    text.lines()
        .filter(|l| !l.trim().is_empty() && !l.starts_with('#'))
        .filter_map(|l| {
            let mut it = l.split('\t');
            let kind = it.next()?.to_string();
            let module = it.next()?.to_string();
            let name = it.next().unwrap_or("").to_string();
            Some(ExportRow { kind, module, name })
        })
        .collect()
}

struct Previous {
    modules: HashSet<String>,
    names: HashSet<(String, String)>,
}

fn previous() -> &'static Previous {
    static P: OnceLock<Previous> = OnceLock::new();
    P.get_or_init(|| {
        let rows = parse_export_rows(PREVIOUS_EXPORTS_TSV);
        let mut modules = HashSet::new();
        let mut names = HashSet::new();
        for r in rows {
            if r.kind == "module" {
                modules.insert(r.module);
            } else {
                names.insert((r.module, r.name));
            }
        }
        Previous { modules, names }
    })
}

/// True when `module` (a stdlib module path, or a kernel pseudo-module name)
/// existed in the previous release but did not export `name` there. A module
/// the previous release did not have at all is not "new names in an old
/// module": no program written for that release could import it.
pub fn is_new_since_previous(module: &str, name: &str) -> bool {
    let p = previous();
    p.modules.contains(module) && !p.names.contains(&(module.to_string(), name.to_string()))
}

/// [`is_new_since_previous`] for an import path as written. A path with no
/// `.sky` module of its own (`import Auth`, `import Std.System`) reaches a
/// kernel pseudo-module, which the snapshot tracks under the pseudo-module's
/// name (`KERNEL_MODULES`).
pub fn is_new_import_member(path: &str, name: &str) -> bool {
    if previous().modules.contains(path) {
        return is_new_since_previous(path, name);
    }
    crate::kernel::KERNEL_MODULES
        .iter()
        .find(|(p, _)| *p == path)
        .is_some_and(|(_, k)| is_new_since_previous(&format!("kernel:{k}"), name))
}

/// Stdlib values a release removed, with what to write instead:
/// `(module, name, migration sentence)`. A use of one is an `[E1001]` /
/// `[E1011]` that carries the sentence, so the error names the new API.
pub const REMOVED_STDLIB_NAMES: &[(&str, &str, &str)] = &[
    (
        "Std.Money",
        "addChecked",
        "`Money.add` returns `Result Error Money` since v0.29.2; use it in place of `addChecked`",
    ),
    (
        "Std.Money",
        "subChecked",
        "`Money.sub` returns `Result Error Money` since v0.29.2; use it in place of `subChecked`",
    ),
    (
        "Std.Money",
        "sumOf",
        "`sumOf` is `Money.sum : Currency -> List Money -> Result Error Money` since v0.29.2",
    ),
    (
        "Std.Money",
        "zeroOf",
        "`zeroOf m` is `Money.zero (Money.currency m)` since v0.29.2",
    ),
    (
        "Std.Money",
        "setRate",
        "exchange rates are an explicit value since v0.29.2: build one with \
         `Money.rates [ ( from, to, rate ) ]` and pass it to `Money.convert rates to m`",
    ),
    (
        "Std.Money",
        "getRate",
        "`getRate from to` is `Money.rate from to rates` (a `Maybe Decimal`) since v0.29.2, \
         reading an explicit `Money.rates` value",
    ),
    (
        "Std.Money",
        "hasRate",
        "use `Money.rate from to rates /= Nothing` since v0.29.2 (rates are an explicit \
         `Money.rates` value)",
    ),
    (
        "Std.Money",
        "clearRates",
        "there is no global rate table since v0.29.2: build a new `Money.rates` value",
    ),
    (
        "Std.Money",
        "parseCurrency",
        "`parseCurrency` is `Money.currencyFromCode : String -> Maybe Currency` since v0.29.2",
    ),
    (
        "Std.Money",
        "isKnownCode",
        "use `Money.currencyFromCode code` (a `Maybe Currency`; `CurrencyRaw` for a code Sky \
         does not name) since v0.29.2",
    ),
    (
        "Std.Money",
        "knownCurrency",
        "match on `CurrencyRaw _` (a code Sky does not name) since v0.29.2",
    ),
];

/// The migration sentence for a stdlib value a release removed, or `None`.
pub fn removed_name_hint(module: &str, name: &str) -> Option<String> {
    REMOVED_STDLIB_NAMES
        .iter()
        .find(|(m, n, _)| *m == module && *n == name)
        .map(|(_, _, h)| format!("{h} (see docs/migration/v0.29.md#money-results)"))
}

/// The stdlib handle types whose constructors v0.27.0 stopped exporting
/// (audit A-1b): `(module, constructor)`. Each id is now random and owned by
/// the session that opened it, so a program may keep and pass the value but
/// never build or match it.
pub const OPAQUE_HANDLE_CONSTRUCTORS: &[(&str, &str)] = &[
    ("Sky.Core.WebSocket", "WebSocket"),
    ("Sky.Http.Server.WebSocket", "WebSocketServer"),
    ("Sky.Core.Http.Stream", "StreamId"),
    ("Sky.Http.Server.Stream", "StreamWriter"),
    ("Std.Cache", "Cache"),
];

/// The migration sentence for a hidden constructor that v0.27.0 made opaque,
/// or `None` for any other constructor.
pub fn opaque_handle_hint(module: &str, ctor: &str) -> Option<String> {
    OPAQUE_HANDLE_CONSTRUCTORS
        .iter()
        .any(|(m, c)| *m == module && *c == ctor)
        .then(|| {
            format!(
                "In v0.27.0 the constructor `{ctor}` of `{module}` is no longer exported: a \
                 handle is opaque and its id is random. Use the module's functions instead of \
                 building or matching the value (for example keep the `{ctor}` value itself in \
                 your model). See docs/migration/v0.27.md#opaque-handle-constructors"
            )
        })
}
