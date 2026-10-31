//! File-based migration gen: diff the type-derived target schema (from the
//! schema-dump) against the committed snapshot (`db/schema.json`) and emit a
//! migration's ops. Additive changes are active; destructive ones (drop/retype)
//! are quarantined in a `destructive` array (inert until a human moves them into
//! `ops`) — the "never silently lossy" rule from the architecture doc.

use serde::{Deserialize, Serialize};
use serde_json::{json, Value};

#[derive(Debug, Deserialize, Serialize, Clone, PartialEq)]
pub struct SchemaColumn {
    pub name: String,
    pub kind: String,
    #[serde(default)]
    pub nullable: bool,
    /// Serial AUTOINCREMENT / BIGSERIAL PK (Store.serial). Only ever set on the PK.
    #[serde(default, skip_serializing_if = "is_false")]
    pub autoinc: bool,
    /// UNIQUE constraint (Store.unique).
    #[serde(default, skip_serializing_if = "is_false")]
    pub unique: bool,
    /// Column DEFAULT (Store.defaultNow / defaultText / defaultInt / defaultBool /
    /// defaultFloat), carried as the `{int|text|bool|float|now}` shape the
    /// runtime renderer expects.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub default: Option<Value>,
    /// A required column's own field zero (`Codec.fieldZeros`), as a
    /// `{"text": …}` default, when it is not the zero of the column's kind: the
    /// JSON of a JSON (blob) field's zero, or an enum (TEXT) field's zero name
    /// (the first name of a `Codec.enum`, the witness's name under
    /// `Codec.auto`). Rule D1: it is the DEFAULT of the column when it is added
    /// to an existing table (never in CREATE TABLE).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub zero: Option<Value>,
}

fn is_false(b: &bool) -> bool {
    !*b
}

/// A declared index (Store.index / uniqueOn / uniqueWhere, Std.Db.Schema
/// withIndex). The dump writes the resolved name, so the diff compares by name.
#[derive(Debug, Deserialize, Serialize, Clone, PartialEq)]
pub struct SchemaIndex {
    pub name: String,
    #[serde(default)]
    pub columns: Vec<String>,
    #[serde(default, skip_serializing_if = "is_false")]
    pub unique: bool,
    /// A partial-index predicate, rendered from constants only.
    #[serde(default, rename = "where", skip_serializing_if = "String::is_empty")]
    pub predicate: String,
}

#[derive(Debug, Deserialize, Serialize, Clone)]
pub struct SchemaTable {
    pub name: String,
    #[serde(default)]
    pub pk: String,
    #[serde(default)]
    pub columns: Vec<SchemaColumn>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub indexes: Vec<SchemaIndex>,
}

#[derive(Debug, Deserialize, Serialize, Clone, Default)]
pub struct Schema {
    #[serde(default)]
    pub tables: Vec<SchemaTable>,
}

impl Schema {
    fn table(&self, name: &str) -> Option<&SchemaTable> {
        self.tables.iter().find(|t| t.name == name)
    }
}

impl SchemaTable {
    fn column(&self, name: &str) -> Option<&SchemaColumn> {
        self.columns.iter().find(|c| c.name == name)
    }
}

/// The result of diffing target vs snapshot.
pub struct Diff {
    /// Additive ops (safe to auto-apply).
    pub ops: Vec<Value>,
    /// Destructive ops, quarantined (inert until a human activates them).
    pub destructive: Vec<Value>,
    /// Human-readable warnings for destructive changes.
    pub warnings: Vec<String>,
}

impl Diff {
    pub fn is_empty(&self) -> bool {
        self.ops.is_empty() && self.destructive.is_empty()
    }
}

/// The zero DEFAULT of a required new column on an existing table, so the
/// migration backfills existing rows (non-interactive gen): the zero of its
/// kind. A blob has none, and an enum's text zero `''` is not one of its
/// names: their zero is the field's own (`SchemaColumn::zero`), read first.
fn zero_default(kind: &str) -> Option<Value> {
    match kind {
        "int" | "bigint" | "real" => Some(json!({ "int": 0 })),
        "text" => Some(json!({ "text": "" })),
        "bool" => Some(json!({ "bool": false })),
        _ => None,
    }
}

/// The name of a Store index: `ix_<table>_<h>` / `ux_<table>_<h>`, where `<h>`
/// is 8 hex digits of FNV-1a 32 over `table|cols joined by ,|1 or 0|predicate`.
/// The runtime's `codecIndexName` (runtime-go/rt/db_migrate_ops.go) computes the
/// same name; `runtime-go/rt/testdata/migrate_parity.json` pins both.
pub fn index_name(table: &str, cols: &[String], unique: bool, predicate: &str) -> String {
    let key = format!(
        "{table}|{}|{}|{predicate}",
        cols.join(","),
        if unique { "1" } else { "0" }
    );
    let mut h: u32 = 0x811c_9dc5;
    for b in key.as_bytes() {
        h ^= u32::from(*b);
        h = h.wrapping_mul(0x0100_0193);
    }
    let t = if table.len() > 51 {
        &table[..51]
    } else {
        table
    };
    let prefix = if unique { "ux_" } else { "ix_" };
    format!("{prefix}{t}_{h:08x}")
}

fn add_index_op(table: &str, ix: &SchemaIndex) -> Value {
    let mut op = json!({
        "kind": "addIndex", "table": table,
        "name": ix.name, "indexColumns": ix.columns,
    });
    let m = op.as_object_mut().unwrap();
    if ix.unique {
        m.insert("unique".into(), json!(true));
    }
    if !ix.predicate.is_empty() {
        m.insert("where".into(), json!(ix.predicate));
    }
    op
}

/// The ops that add column `c` to the existing table `table` (user decision
/// D1, the same rule as the runtime's `renderAddColumn`): a nullable column
/// keeps its declared default; a required column is NOT NULL with its declared
/// default, else its zero value (the field's own `zero` for a blob or an
/// enum column, else the zero of its kind); a required column with no zero value is
/// NOT NULL with no default, which only an empty table takes, so `--gen`
/// warns; a `defaultNow` column carries `{"nowAtApply": true}` (SQLite stamps
/// the apply time, PostgreSQL renders `now()`); a unique column gets its own
/// `addIndex` (SQLite refuses UNIQUE in ADD COLUMN).
fn add_column_ops(table: &str, c: &SchemaColumn, warnings: &mut Vec<String>) -> Vec<Value> {
    let mut op = json!({
        "kind": "addColumn", "table": table,
        "column": c.name, "type": c.kind,
    });
    let m = op.as_object_mut().unwrap();
    let declared = c.default.as_ref().map(|d| {
        if d.get("now").and_then(Value::as_bool) == Some(true) {
            json!({ "nowAtApply": true })
        } else {
            d.clone()
        }
    });
    if c.nullable {
        m.insert("nullable".into(), json!(true));
        if let Some(d) = declared {
            m.insert("default".into(), d);
        }
    } else if let Some(d) = declared
        .or_else(|| c.zero.clone())
        .or_else(|| zero_default(&c.kind))
    {
        m.insert("nullable".into(), json!(false));
        m.insert("default".into(), d);
    } else {
        m.insert("nullable".into(), json!(false));
        warnings.push(format!(
            "column {}.{} is required and its field's codec has no zero value (a taggedUnion, \
             a result, Codec.error, or an object holding one): it is added NOT NULL with no \
             default, which a table with rows refuses. Make the field a Maybe, derive the record \
             codec with Codec.auto or Codec.autoWith (the blank record's value for the field \
             gives the default), or add a data step",
            table, c.name
        ));
    }
    let mut ops = vec![op];
    if c.unique {
        let cols = vec![c.name.clone()];
        ops.push(add_index_op(
            table,
            &SchemaIndex {
                name: index_name(table, &cols, true, ""),
                columns: cols,
                unique: true,
                predicate: String::new(),
            },
        ));
    }
    ops
}

/// The prefix of the tables the Sky runtime keeps in the app's own database
/// (`sky_session_aliases`, `sky_session_bindings`, the analytics and telemetry
/// tables). They are the runtime's, never the app's schema.
pub const RUNTIME_TABLE_PREFIX: &str = "sky_";

/// Diff the type-derived `target` against the committed `snapshot`.
///
/// A `sky_*` table is the runtime's (E-15): a migration never creates, alters
/// or drops one, whichever side lists it. A snapshot taken from a database
/// the runtime has used, or a hand-edited one, would otherwise quarantine a
/// `DROP TABLE sky_session_aliases`.
pub fn diff(target: &Schema, snapshot: &Schema) -> Diff {
    let app_only = |s: &Schema| Schema {
        tables: s
            .tables
            .iter()
            .filter(|t| !t.name.starts_with(RUNTIME_TABLE_PREFIX))
            .cloned()
            .collect(),
    };
    let reserved: Vec<&str> = target
        .tables
        .iter()
        .filter(|t| t.name.starts_with(RUNTIME_TABLE_PREFIX))
        .map(|t| t.name.as_str())
        .collect();
    let (target, snapshot) = (&app_only(target), &app_only(snapshot));
    let mut ops = Vec::new();
    let mut destructive = Vec::new();
    let mut warnings: Vec<String> = reserved
        .iter()
        .map(|n| {
            format!(
                "table {n} is skipped: names starting with `{RUNTIME_TABLE_PREFIX}` are \
                 reserved for the Sky runtime's own tables; rename the Store table"
            )
        })
        .collect();

    // New + changed tables.
    for t in &target.tables {
        match snapshot.table(&t.name) {
            None => {
                // New table.
                let columns: Vec<Value> = t
                    .columns
                    .iter()
                    .map(|c| {
                        // Carry the column-level constraints through to the
                        // createTable op so the applier renders serial
                        // AUTOINCREMENT/BIGSERIAL, UNIQUE, and DEFAULT — exactly
                        // what the push path emits. Dropping these here was the
                        // data-integrity bug (UNIQUE lost, serial PK → null-PK
                        // violation on Postgres).
                        let mut jc = json!({
                            "name": c.name, "type": c.kind,
                            "nullable": c.nullable, "pk": c.name == t.pk,
                            "autoinc": c.autoinc, "unique": c.unique,
                        });
                        if let Some(d) = &c.default {
                            jc.as_object_mut()
                                .unwrap()
                                .insert("default".into(), d.clone());
                        }
                        jc
                    })
                    .collect();
                ops.push(json!({
                    "kind": "createTable",
                    "table": t.name, "columns": columns,
                }));
                for ix in &t.indexes {
                    ops.push(add_index_op(&t.name, ix));
                }
            }
            Some(prev) => {
                // New columns.
                for c in &t.columns {
                    match prev.column(&c.name) {
                        None => ops.extend(add_column_ops(&t.name, c, &mut warnings)),
                        Some(pc) if pc.kind != c.kind => {
                            // Type change → destructive.
                            destructive.push(json!({
                                "kind": "alterColumnType", "table": t.name,
                                "column": c.name, "type": c.kind,
                            }));
                            warnings.push(format!(
                                "column {}.{} type change {} → {} is DESTRUCTIVE (quarantined)",
                                t.name, c.name, pc.kind, c.kind
                            ));
                        }
                        Some(_) => {}
                    }
                }
                // Indexes, by name. A removed or changed index is dropped (no
                // data is lost), then the new definition is added.
                for pi in &prev.indexes {
                    if !t.indexes.contains(pi) {
                        ops.push(json!({
                            "kind": "dropIndex",
                            "table": t.name, "name": pi.name,
                        }));
                    }
                }
                for ix in &t.indexes {
                    if !prev.indexes.contains(ix) {
                        ops.push(add_index_op(&t.name, ix));
                    }
                }
                // Dropped columns (in snapshot, not in target) → destructive.
                for pc in &prev.columns {
                    if t.column(&pc.name).is_none() {
                        destructive.push(json!({
                            "kind": "dropColumn", "table": t.name, "column": pc.name,
                        }));
                        warnings.push(format!(
                            "column {}.{} removed — DROP is DESTRUCTIVE (quarantined; edit to a renameColumn if it was renamed)",
                            t.name, pc.name
                        ));
                    }
                }
            }
        }
    }

    // Dropped tables.
    for pt in &snapshot.tables {
        if target.table(&pt.name).is_none() {
            destructive.push(json!({ "kind": "dropTable", "table": pt.name }));
            warnings.push(format!(
                "table {} removed — DROP TABLE is DESTRUCTIVE (quarantined)",
                pt.name
            ));
        }
    }

    Diff {
        ops,
        destructive,
        warnings,
    }
}

/// A quarantined column drop the interactive gen can reclassify. `rename_candidates`
/// are added columns on the same table with the same type — the plausible targets
/// if the "drop" was actually a rename.
pub struct DropDecision {
    pub table: String,
    pub column: String,
    pub rename_candidates: Vec<String>,
}

fn op_str(op: &Value, key: &str) -> String {
    op.get(key)
        .and_then(|v| v.as_str())
        .unwrap_or("")
        .to_string()
}

impl Diff {
    /// The quarantined drops the interactive layer should ask the human about,
    /// each paired with the added columns that could be its rename target.
    pub fn drop_decisions(&self) -> Vec<DropDecision> {
        self.destructive
            .iter()
            .filter(|op| op_str(op, "kind") == "dropColumn")
            .map(|op| {
                let table = op_str(op, "table");
                let drop_col = op_str(op, "column");
                // A rename target: an addColumn on the same table (any type — a
                // rename can coincide with a type change, but keep it simple and
                // offer same-table adds).
                let candidates = self
                    .ops
                    .iter()
                    .filter(|a| op_str(a, "kind") == "addColumn" && op_str(a, "table") == table)
                    .map(|a| op_str(a, "column"))
                    .filter(|c| *c != drop_col)
                    .collect();
                DropDecision {
                    table,
                    column: drop_col,
                    rename_candidates: candidates,
                }
            })
            .collect()
    }

    /// Reclassify a quarantined drop as a rename: remove the `dropColumn` from
    /// `destructive` and the matching `addColumn` from `ops`, and add one active
    /// `renameColumn` op. The new column keeps its data (no backfill needed).
    pub fn rename(&mut self, table: &str, from: &str, to: &str) {
        self.destructive.retain(|op| {
            !(op_str(op, "kind") == "dropColumn"
                && op_str(op, "table") == table
                && op_str(op, "column") == from)
        });
        self.ops.retain(|op| {
            !(op_str(op, "kind") == "addColumn"
                && op_str(op, "table") == table
                && op_str(op, "column") == to)
        });
        self.ops.push(json!({
            "kind": "renameColumn", "table": table, "from": from, "to": to,
        }));
        self.warnings
            .retain(|w| !(w.contains(&format!("{table}.{from}")) && w.contains("removed")));
    }

    /// Reclassify a quarantined drop as an active drop: move it from `destructive`
    /// into `ops` (the human confirmed the data loss).
    pub fn confirm_drop(&mut self, table: &str, column: &str) {
        let mut moved = None;
        self.destructive.retain(|op| {
            let hit = op_str(op, "kind") == "dropColumn"
                && op_str(op, "table") == table
                && op_str(op, "column") == column;
            if hit {
                moved = Some(op.clone());
            }
            !hit
        });
        if let Some(op) = moved {
            self.ops.push(op);
            self.warnings
                .retain(|w| !(w.contains(&format!("{table}.{column}")) && w.contains("removed")));
        }
    }

    /// Required new columns that carry a backfill DEFAULT, as
    /// `(table, column, type, current-default-display)` — the interactive layer
    /// offers to override each.
    pub fn defaulted_adds(&self) -> Vec<(String, String, String, String)> {
        self.ops
            .iter()
            .filter(|op| op_str(op, "kind") == "addColumn" && op.get("default").is_some())
            .map(|op| {
                let disp = op
                    .get("default")
                    .map(render_default_display)
                    .unwrap_or_default();
                (
                    op_str(op, "table"),
                    op_str(op, "column"),
                    op_str(op, "type"),
                    disp,
                )
            })
            .collect()
    }

    /// Override the backfill DEFAULT of a required `addColumn` op.
    pub fn set_default(&mut self, table: &str, column: &str, default: Value) {
        for op in &mut self.ops {
            if op_str(op, "kind") == "addColumn"
                && op_str(op, "table") == table
                && op_str(op, "column") == column
            {
                if let Some(m) = op.as_object_mut() {
                    m.insert("default".into(), default.clone());
                }
            }
        }
    }
}

/// Human-readable form of a `{int|text|bool: v}` default, for the prompt.
fn render_default_display(d: &Value) -> String {
    if let Some(v) = d.get("int") {
        v.to_string()
    } else if let Some(v) = d.get("text").and_then(|v| v.as_str()) {
        format!("\"{v}\"")
    } else if let Some(v) = d.get("bool") {
        v.to_string()
    } else if let Some(v) = d.get("float") {
        v.to_string()
    } else if d.get("nowAtApply").is_some() || d.get("now").is_some() {
        "now".to_string()
    } else {
        String::new()
    }
}

/// Parse a user-typed default for a column of `kind` into the `{int|text|bool: v}`
/// shape the runtime renders. `None` → keep the existing (zero) default.
pub fn parse_default(kind: &str, input: &str) -> Option<Value> {
    match kind {
        "int" | "bigint" => input
            .trim()
            .parse::<i64>()
            .ok()
            .map(|n| json!({ "int": n })),
        "bool" => match input.trim().to_lowercase().as_str() {
            "true" | "1" | "yes" | "y" => Some(json!({ "bool": true })),
            "false" | "0" | "no" | "n" => Some(json!({ "bool": false })),
            _ => None,
        },
        "text" => Some(json!({ "text": input })),
        "real" => input
            .trim()
            .parse::<f64>()
            .ok()
            .filter(|f| f.is_finite())
            .map(|f| json!({ "float": f })),
        _ => None,
    }
}

/// Render a generated migration file's JSON body.
pub fn migration_file_json(id: &str, diff: &Diff) -> String {
    let mut obj = serde_json::Map::new();
    obj.insert("id".into(), json!(id));
    obj.insert("ops".into(), json!(diff.ops));
    if !diff.destructive.is_empty() {
        obj.insert("destructive".into(), json!(diff.destructive));
    }
    serde_json::to_string_pretty(&Value::Object(obj)).unwrap_or_default()
}

#[cfg(test)]
mod tests {
    /// E-15: the runtime's own `sky_*` tables never enter a migration, on
    /// either side of the diff.
    #[test]
    fn runtime_tables_never_enter_a_migration() {
        let table = |name: &str| SchemaTable {
            name: name.to_string(),
            pk: "id".to_string(),
            indexes: vec![],
            columns: vec![SchemaColumn {
                name: "id".to_string(),
                kind: "text".to_string(),
                nullable: false,
                autoinc: false,
                unique: false,
                default: None,
                zero: None,
            }],
        };
        let target = Schema {
            tables: vec![table("todos"), table("sky_session_aliases")],
        };
        let snapshot = Schema {
            tables: vec![table("todos"), table("sky_session_bindings")],
        };
        let d = super::diff(&target, &snapshot);
        assert!(d.ops.is_empty(), "{:?}", d.ops);
        assert!(d.destructive.is_empty(), "{:?}", d.destructive);
        assert!(
            d.warnings
                .iter()
                .any(|w| w.contains("sky_session_aliases") && w.contains("reserved")),
            "{:?}",
            d.warnings
        );
    }

    use super::*;

    fn col(name: &str, kind: &str, nullable: bool) -> SchemaColumn {
        SchemaColumn {
            name: name.into(),
            kind: kind.into(),
            nullable,
            autoinc: false,
            unique: false,
            default: None,
            zero: None,
        }
    }

    fn col_full(
        name: &str,
        kind: &str,
        nullable: bool,
        autoinc: bool,
        unique: bool,
        default: Option<Value>,
    ) -> SchemaColumn {
        SchemaColumn {
            name: name.into(),
            kind: kind.into(),
            nullable,
            autoinc,
            unique,
            default,
            zero: None,
        }
    }

    #[test]
    fn new_table_and_new_columns() {
        let snapshot = Schema {
            tables: vec![SchemaTable {
                name: "users".into(),
                pk: "id".into(),
                indexes: vec![],
                columns: vec![col("id", "text", false)],
            }],
        };
        let target = Schema {
            tables: vec![
                SchemaTable {
                    name: "users".into(),
                    pk: "id".into(),
                    indexes: vec![],
                    columns: vec![
                        col("id", "text", false),
                        col("age", "int", false),
                        col("nick", "text", true),
                    ],
                },
                SchemaTable {
                    name: "orders".into(),
                    pk: "id".into(),
                    indexes: vec![],
                    columns: vec![col("id", "text", false)],
                },
            ],
        };
        let d = diff(&target, &snapshot);
        // createTable orders + addColumn age (NOT NULL DEFAULT 0) + addColumn nick (nullable)
        assert_eq!(d.ops.len(), 3);
        assert!(d.destructive.is_empty());
        let s = serde_json::to_string(&d.ops).unwrap();
        assert!(s.contains("createTable") && s.contains("orders"));
        assert!(s.contains(r#""column":"age""#) && s.contains(r#""default":{"int":0}"#));
        assert!(s.contains(r#""column":"nick""#));
    }

    #[test]
    fn dropped_column_is_quarantined() {
        let snapshot = Schema {
            tables: vec![SchemaTable {
                name: "users".into(),
                pk: "id".into(),
                indexes: vec![],
                columns: vec![col("id", "text", false), col("slug", "text", false)],
            }],
        };
        let target = Schema {
            tables: vec![SchemaTable {
                name: "users".into(),
                pk: "id".into(),
                indexes: vec![],
                columns: vec![col("id", "text", false)],
            }],
        };
        let d = diff(&target, &snapshot);
        assert!(d.ops.is_empty());
        assert_eq!(d.destructive.len(), 1);
        assert_eq!(d.warnings.len(), 1);
        assert!(d.warnings[0].contains("DESTRUCTIVE"));
    }

    #[test]
    fn rename_reclassifies_drop_plus_add() {
        // snapshot has `slug`; target renamed it to `handle` (drop slug + add handle).
        let snapshot = Schema {
            tables: vec![SchemaTable {
                name: "users".into(),
                pk: "id".into(),
                indexes: vec![],
                columns: vec![col("id", "text", false), col("slug", "text", false)],
            }],
        };
        let target = Schema {
            tables: vec![SchemaTable {
                name: "users".into(),
                pk: "id".into(),
                indexes: vec![],
                columns: vec![col("id", "text", false), col("handle", "text", false)],
            }],
        };
        let mut d = diff(&target, &snapshot);
        // Before: addColumn handle (active) + dropColumn slug (quarantined).
        let decisions = d.drop_decisions();
        assert_eq!(decisions.len(), 1);
        assert_eq!(decisions[0].column, "slug");
        assert!(decisions[0]
            .rename_candidates
            .contains(&"handle".to_string()));
        // Resolve as a rename.
        d.rename("users", "slug", "handle");
        assert!(d.destructive.is_empty(), "drop removed");
        assert_eq!(d.ops.len(), 1, "add replaced by rename");
        let s = serde_json::to_string(&d.ops).unwrap();
        assert!(
            s.contains("renameColumn")
                && s.contains(r#""from":"slug""#)
                && s.contains(r#""to":"handle""#)
        );
        assert!(d.warnings.is_empty(), "rename clears the drop warning");
    }

    #[test]
    fn confirm_drop_activates_it() {
        let snapshot = Schema {
            tables: vec![SchemaTable {
                name: "users".into(),
                pk: "id".into(),
                indexes: vec![],
                columns: vec![col("id", "text", false), col("legacy", "text", true)],
            }],
        };
        let target = Schema {
            tables: vec![SchemaTable {
                name: "users".into(),
                pk: "id".into(),
                indexes: vec![],
                columns: vec![col("id", "text", false)],
            }],
        };
        let mut d = diff(&target, &snapshot);
        assert_eq!(d.destructive.len(), 1);
        d.confirm_drop("users", "legacy");
        assert!(d.destructive.is_empty());
        assert_eq!(d.ops.len(), 1);
        assert!(serde_json::to_string(&d.ops)
            .unwrap()
            .contains("dropColumn"));
    }

    #[test]
    fn set_default_overrides_backfill() {
        let snapshot = Schema {
            tables: vec![SchemaTable {
                name: "users".into(),
                pk: "id".into(),
                indexes: vec![],
                columns: vec![col("id", "text", false)],
            }],
        };
        let target = Schema {
            tables: vec![SchemaTable {
                name: "users".into(),
                pk: "id".into(),
                indexes: vec![],
                columns: vec![col("id", "text", false), col("role", "text", false)],
            }],
        };
        let mut d = diff(&target, &snapshot);
        d.set_default("users", "role", json!({ "text": "member" }));
        let s = serde_json::to_string(&d.ops).unwrap();
        assert!(s.contains(r#""default":{"text":"member"}"#));
    }

    #[test]
    fn create_table_carries_column_constraints() {
        // Bug #9 regression: a serial PK + UNIQUE + defaultNow must survive the
        // diff into the createTable op (previously dropped → UNIQUE lost + null-PK
        // violation on Postgres).
        let snapshot = Schema::default();
        let target = Schema {
            tables: vec![SchemaTable {
                name: "users".into(),
                pk: "id".into(),
                indexes: vec![],
                columns: vec![
                    col_full("id", "int", false, true, false, None),
                    col_full("email", "text", false, false, true, None),
                    col_full(
                        "created_at",
                        "int",
                        false,
                        false,
                        false,
                        Some(json!({ "now": true })),
                    ),
                ],
            }],
        };
        let d = diff(&target, &snapshot);
        assert_eq!(d.ops.len(), 1);
        let cols = d.ops[0].get("columns").and_then(|c| c.as_array()).unwrap();
        let id = &cols[0];
        assert_eq!(
            id.get("autoinc"),
            Some(&json!(true)),
            "serial PK autoinc carried"
        );
        let email = &cols[1];
        assert_eq!(email.get("unique"), Some(&json!(true)), "UNIQUE carried");
        let created = &cols[2];
        assert_eq!(
            created.get("default"),
            Some(&json!({ "now": true })),
            "DEFAULT now carried"
        );
    }

    #[test]
    fn schema_column_roundtrips_constraints() {
        // dump JSON → SchemaColumn → snapshot JSON must preserve the flags so the
        // committed snapshot (db/schema.json) is not lossy.
        let dump = r#"{"tables":[{"name":"users","pk":"id","columns":[
            {"name":"id","kind":"int","nullable":false,"autoinc":true},
            {"name":"email","kind":"text","nullable":false,"unique":true},
            {"name":"created_at","kind":"int","nullable":false,"default":{"now":true}}
        ]}]}"#;
        let s: Schema = serde_json::from_str(dump).unwrap();
        let c = &s.tables[0].columns;
        assert!(c[0].autoinc);
        assert!(c[1].unique);
        assert_eq!(c[2].default, Some(json!({ "now": true })));
        // Re-serialize (as the snapshot writer does) and re-read: still intact.
        let round: Schema = serde_json::from_str(&serde_json::to_string(&s).unwrap()).unwrap();
        assert!(round.tables[0].columns[0].autoinc);
        assert!(round.tables[0].columns[1].unique);
    }

    #[test]
    fn no_change_is_empty() {
        let s = Schema {
            tables: vec![SchemaTable {
                name: "users".into(),
                pk: "id".into(),
                indexes: vec![],
                columns: vec![col("id", "text", false)],
            }],
        };
        let d = diff(&s, &s.clone());
        assert!(d.is_empty());
    }

    /// Gap A (D1): a required JSON (blob) column is NOT NULL. With the codec's
    /// blank value (`zero`, from the schema dump) it takes that value as its
    /// DEFAULT, so the rows that exist read it. With no zero value it is NOT
    /// NULL with no default (only an empty table takes it), and `--gen` warns.
    /// Before, both were added NULLABLE.
    #[test]
    fn required_blob_is_not_null() {
        let before = || SchemaTable {
            name: "orders".into(),
            pk: "id".into(),
            indexes: vec![],
            columns: vec![col("id", "text", false)],
        };
        let parse = |v: Value| -> SchemaColumn { serde_json::from_value(v).unwrap() };
        let mut target = before();
        target.columns.push(parse(json!({
            "name": "tags", "kind": "blob", "nullable": false, "zero": { "text": "[]" }
        })));
        target.columns.push(parse(
            json!({ "name": "doc", "kind": "blob", "nullable": false }),
        ));
        let d = diff(
            &Schema {
                tables: vec![target],
            },
            &Schema {
                tables: vec![before()],
            },
        );
        assert_eq!(
            d.ops,
            vec![
                json!({ "kind": "addColumn", "table": "orders", "column": "tags",
                        "type": "blob", "nullable": false, "default": { "text": "[]" } }),
                json!({ "kind": "addColumn", "table": "orders", "column": "doc",
                        "type": "blob", "nullable": false }),
            ]
        );
        assert!(
            d.warnings
                .iter()
                .any(|w| w.contains("orders.doc") && w.contains("no zero value")),
            "{:?}",
            d.warnings
        );
        assert!(
            !d.warnings.iter().any(|w| w.contains("orders.tags")),
            "{:?}",
            d.warnings
        );
    }

    /// D1, each column's own zero. The schema dump carries a `zero` for a
    /// list column whose sibling taggedUnion column has none, and for an enum
    /// (TEXT) column: the enum's first name, because the text zero `''` is not
    /// one of its names. `--gen` makes each the DEFAULT. A plain String column
    /// has no `zero` and keeps the text zero `''`. The no-zero sibling is NOT
    /// NULL with no default and warns.
    #[test]
    fn each_column_takes_its_own_zero() {
        let before = || SchemaTable {
            name: "orders".into(),
            pk: "id".into(),
            indexes: vec![],
            columns: vec![col("id", "text", false)],
        };
        let parse = |v: Value| -> SchemaColumn { serde_json::from_value(v).unwrap() };
        let mut target = before();
        target.columns.push(parse(
            json!({ "name": "shade", "kind": "blob", "nullable": false }),
        ));
        target.columns.push(parse(json!({
            "name": "tags", "kind": "blob", "nullable": false, "zero": { "text": "[]" }
        })));
        target.columns.push(parse(json!({
            "name": "stage", "kind": "text", "nullable": false, "zero": { "text": "draft" }
        })));
        target.columns.push(parse(
            json!({ "name": "label", "kind": "text", "nullable": false }),
        ));
        let d = diff(
            &Schema {
                tables: vec![target],
            },
            &Schema {
                tables: vec![before()],
            },
        );
        assert_eq!(
            d.ops,
            vec![
                json!({ "kind": "addColumn", "table": "orders", "column": "shade",
                        "type": "blob", "nullable": false }),
                json!({ "kind": "addColumn", "table": "orders", "column": "tags",
                        "type": "blob", "nullable": false, "default": { "text": "[]" } }),
                json!({ "kind": "addColumn", "table": "orders", "column": "stage",
                        "type": "text", "nullable": false, "default": { "text": "draft" } }),
                json!({ "kind": "addColumn", "table": "orders", "column": "label",
                        "type": "text", "nullable": false, "default": { "text": "" } }),
            ]
        );
        assert_eq!(
            d.warnings
                .iter()
                .filter(|w| w.contains("no zero value"))
                .map(|w| w.contains("orders.shade"))
                .collect::<Vec<_>>(),
            vec![true],
            "{:?}",
            d.warnings
        );
    }

    /// B9 / D1: a declared default is kept when a column is added. Before
    /// v0.29.0 `--gen` wrote the zero value (`{"int":0}`) for a required
    /// column and dropped the default of a nullable one.
    #[test]
    fn added_column_keeps_its_declared_default() {
        let before = || SchemaTable {
            name: "orders".into(),
            pk: "id".into(),
            indexes: vec![],
            columns: vec![col("id", "text", false)],
        };
        let mut target = before();
        target.columns.push(col_full(
            "rank",
            "int",
            false,
            false,
            false,
            Some(json!({ "int": 7 })),
        ));
        target.columns.push(col_full(
            "flagged",
            "bool",
            true,
            false,
            false,
            Some(json!({ "bool": true })),
        ));
        let d = diff(
            &Schema {
                tables: vec![target],
            },
            &Schema {
                tables: vec![before()],
            },
        );
        assert_eq!(d.ops.len(), 2, "{:?}", d.ops);
        assert_eq!(d.ops[0]["default"], json!({ "int": 7 }), "{:?}", d.ops[0]);
        assert_eq!(d.ops[0]["nullable"], json!(false));
        assert_eq!(
            d.ops[1]["default"],
            json!({ "bool": true }),
            "{:?}",
            d.ops[1]
        );
        assert_eq!(d.ops[1]["nullable"], json!(true));
    }

    /// The parity fixture (D1): the diff of each fixture column gives exactly
    /// the fixture's ops. The Go side checks that the runtime's
    /// `Store.migrate` runs the same SQL as `renderMigOp` of these ops.
    #[test]
    fn migrate_parity_fixture_ops() {
        let path = std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("../../../runtime-go/rt/testdata/migrate_parity.json");
        let fixture: Value =
            serde_json::from_str(&std::fs::read_to_string(&path).expect("read fixture"))
                .expect("fixture is JSON");
        let table = fixture["table"].as_str().unwrap().to_string();
        let pk = fixture["pk"].as_str().unwrap().to_string();
        let existing: Vec<SchemaColumn> = fixture["existing"]
            .as_array()
            .unwrap()
            .iter()
            .map(|p| col(p[0].as_str().unwrap(), p[1].as_str().unwrap(), false))
            .collect();
        let snapshot = Schema {
            tables: vec![SchemaTable {
                name: table.clone(),
                pk: pk.clone(),
                columns: existing.clone(),
                indexes: vec![],
            }],
        };
        for c in fixture["columns"].as_array().unwrap() {
            let added: SchemaColumn = serde_json::from_value(c["schema"].clone()).unwrap();
            let mut columns = existing.clone();
            columns.push(added);
            let target = Schema {
                tables: vec![SchemaTable {
                    name: table.clone(),
                    pk: pk.clone(),
                    columns,
                    indexes: vec![],
                }],
            };
            let d = diff(&target, &snapshot);
            assert_eq!(
                Value::Array(d.ops.clone()),
                c["ops"],
                "column {}",
                c["colspec"]
            );
        }
    }

    /// Store indexes: a new table gets its indexes as addIndex ops, an
    /// existing table gets the missing ones, and a removed or changed index is
    /// dropped (no data is lost). The names never collide across tables.
    #[test]
    fn indexes_are_diffed_by_name() {
        let ix = |t: &str, cols: &[&str], unique: bool, pred: &str| {
            let cols: Vec<String> = cols.iter().map(|c| c.to_string()).collect();
            SchemaIndex {
                name: index_name(t, &cols, unique, pred),
                columns: cols,
                unique,
                predicate: pred.into(),
            }
        };
        assert_ne!(
            index_name("a_b", &["c".into()], false, ""),
            index_name("a", &["b".into(), "c".into()], false, "")
        );
        assert_eq!(
            index_name("orders", &["email".into()], true, "").len(),
            "ux_orders_".len() + 8
        );
        let long = "t".repeat(63);
        assert!(index_name(&long, &["c".into()], false, "").len() <= 63);
        let table = |indexes: Vec<SchemaIndex>| SchemaTable {
            name: "orders".into(),
            pk: "id".into(),
            columns: vec![col("id", "text", false), col("slug", "text", false)],
            indexes,
        };
        let fresh = diff(
            &Schema {
                tables: vec![table(vec![ix("orders", &["slug"], false, "")])],
            },
            &Schema::default(),
        );
        assert_eq!(fresh.ops.len(), 2, "{:?}", fresh.ops);
        assert!(
            fresh.ops.iter().all(|op| op.get("v").is_none()),
            "{:?}",
            fresh.ops
        );
        assert_eq!(fresh.ops[1]["kind"], json!("addIndex"));
        let partial = ix("orders", &["slug"], true, "slug <> ''");
        let changed = diff(
            &Schema {
                tables: vec![table(vec![partial.clone()])],
            },
            &Schema {
                tables: vec![table(vec![ix("orders", &["slug"], false, "")])],
            },
        );
        let kinds: Vec<&str> = changed
            .ops
            .iter()
            .map(|o| o["kind"].as_str().unwrap())
            .collect();
        assert_eq!(kinds, vec!["dropIndex", "addIndex"], "{:?}", changed.ops);
        assert_eq!(changed.ops[1]["where"], json!("slug <> ''"));
        let same = diff(
            &Schema {
                tables: vec![table(vec![partial.clone()])],
            },
            &Schema {
                tables: vec![table(vec![partial])],
            },
        );
        assert!(same.is_empty());
    }
}
