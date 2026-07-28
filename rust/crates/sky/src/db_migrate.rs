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
}

#[derive(Debug, Deserialize, Serialize, Clone)]
pub struct SchemaTable {
    pub name: String,
    #[serde(default)]
    pub pk: String,
    #[serde(default)]
    pub columns: Vec<SchemaColumn>,
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

/// A safe zero DEFAULT for a required new column on an existing table, so the
/// migration backfills existing rows (non-interactive gen). `None` → add the
/// column nullable instead (blob columns can't have a simple default).
fn zero_default(kind: &str) -> Option<Value> {
    match kind {
        "int" | "bigint" | "real" => Some(json!({ "int": 0 })),
        "text" => Some(json!({ "text": "" })),
        "bool" => Some(json!({ "bool": false })),
        _ => None, // blob → nullable
    }
}

/// Diff the type-derived `target` against the committed `snapshot`.
pub fn diff(target: &Schema, snapshot: &Schema) -> Diff {
    let mut ops = Vec::new();
    let mut destructive = Vec::new();
    let mut warnings = Vec::new();

    // New + changed tables.
    for t in &target.tables {
        match snapshot.table(&t.name) {
            None => {
                // New table.
                let columns: Vec<Value> = t
                    .columns
                    .iter()
                    .map(|c| {
                        json!({
                            "name": c.name, "type": c.kind,
                            "nullable": c.nullable, "pk": c.name == t.pk,
                        })
                    })
                    .collect();
                ops.push(json!({ "kind": "createTable", "table": t.name, "columns": columns }));
            }
            Some(prev) => {
                // New columns.
                for c in &t.columns {
                    match prev.column(&c.name) {
                        None => {
                            let mut op = json!({
                                "kind": "addColumn", "table": t.name,
                                "column": c.name, "type": c.kind,
                            });
                            let m = op.as_object_mut().unwrap();
                            if c.nullable {
                                m.insert("nullable".into(), json!(true));
                            } else if let Some(d) = zero_default(&c.kind) {
                                m.insert("nullable".into(), json!(false));
                                m.insert("default".into(), d);
                            } else {
                                // required blob → can't default → add nullable + warn
                                m.insert("nullable".into(), json!(true));
                                warnings.push(format!(
                                    "column {}.{} is a required non-scalar — added NULLABLE (no default possible)",
                                    t.name, c.name
                                ));
                            }
                            ops.push(op);
                        }
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
            warnings.push(format!("table {} removed — DROP TABLE is DESTRUCTIVE (quarantined)", pt.name));
        }
    }

    Diff { ops, destructive, warnings }
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
    use super::*;

    fn col(name: &str, kind: &str, nullable: bool) -> SchemaColumn {
        SchemaColumn { name: name.into(), kind: kind.into(), nullable }
    }

    #[test]
    fn new_table_and_new_columns() {
        let snapshot = Schema {
            tables: vec![SchemaTable {
                name: "users".into(), pk: "id".into(),
                columns: vec![col("id", "text", false)],
            }],
        };
        let target = Schema {
            tables: vec![
                SchemaTable {
                    name: "users".into(), pk: "id".into(),
                    columns: vec![col("id", "text", false), col("age", "int", false), col("nick", "text", true)],
                },
                SchemaTable {
                    name: "orders".into(), pk: "id".into(),
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
                name: "users".into(), pk: "id".into(),
                columns: vec![col("id", "text", false), col("slug", "text", false)],
            }],
        };
        let target = Schema {
            tables: vec![SchemaTable {
                name: "users".into(), pk: "id".into(),
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
    fn no_change_is_empty() {
        let s = Schema {
            tables: vec![SchemaTable {
                name: "users".into(), pk: "id".into(),
                columns: vec![col("id", "text", false)],
            }],
        };
        let d = diff(&s, &s.clone());
        assert!(d.is_empty());
    }
}
