//! `sky doc --api openapi` — a valid OpenAPI 3.1 document derived STATICALLY from
//! the app's typed source. It reuses the `wire` analysis ([`crate::diagram::WireReport`])
//! — the SAME endpoints + request/response Model-field shapes the auto-split ships,
//! so the spec cannot drift from what the app actually serves.
//!
//! The unfair advantage: a Sky app's HTTP surface is statically and totally known
//! (every `/_rpc/<Msg>` from a server `update` branch, every declared `App.api` /
//! `Sky.Http.Server` route), so the whole spec is generated with no annotations —
//! unlike FastAPI decorators or springdoc annotations.
//!
//! Scope of the schemas: an endpoint's request/response fields are the Model
//! fields it reads/writes, whose types come from the typed HIR
//! ([`crate::spa_partition::ModelFieldTy`]). Primitives, `List`, `Maybe`, and
//! `Dict` map to JSON Schema exactly; a user record/union field is emitted as a
//! described object placeholder (its nested shape is not expanded in v1 — noted in
//! the description). Raw `App.api` handlers carry no Model-field shape, so their
//! operations describe the route + method + CSRF posture only.

use crate::diagram::{EndpointKind, WireEndpoint, WireReport};
use crate::spa_partition::ModelFieldTy;
use serde_json::{json, Map, Value};

/// The output serialisation format.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum ApiFormat {
    Yaml,
    Json,
}

/// Build the OpenAPI 3.1 document as a `serde_json::Value`, then serialise it to
/// `format`. `app_name` / `version` title the `info` block; `include_rpc` keeps
/// the `/_rpc/<Msg>` operations (default true; `--no-rpc` sets it false).
pub fn render(
    report: &WireReport,
    app_name: &str,
    version: &str,
    include_rpc: bool,
    format: ApiFormat,
) -> Result<String, String> {
    let doc = build(report, app_name, version, include_rpc);
    match format {
        ApiFormat::Json => {
            serde_json::to_string_pretty(&doc).map_err(|e| format!("openapi json: {e}"))
        }
        ApiFormat::Yaml => serde_yaml::to_string(&doc).map_err(|e| format!("openapi yaml: {e}")),
    }
}

/// The OpenAPI 3.1 document value.
fn build(report: &WireReport, app_name: &str, version: &str, include_rpc: bool) -> Value {
    let mut paths = Map::new();

    // Declared HTTP routes (App.api / Sky.Http.Server) — the intentional public
    // API, primary and untagged. Their handler bodies carry no Model-field shape,
    // so the operation describes method + path + CSRF posture.
    for e in &report.http_endpoints {
        let method = e.method.to_ascii_lowercase();
        let csrf_exempt = e.kind.is_csrf_exempt();
        let mut op = Map::new();
        op.insert("summary".into(), json!(format!("{} {}", e.method, e.path)));
        op.insert(
            "operationId".into(),
            json!(operation_id(&e.method, &e.path)),
        );
        let kind = match e.kind {
            EndpointKind::PageRoute => {
                "A Std.App page route (server-rendered GET). Its auth is enforced in the app, not statically recovered here."
            }
            EndpointKind::RawApi => {
                "A raw App.api endpoint — reached by a third party (webhook / API client), OUTSIDE the session/CSRF contract. ⚠ It must authenticate its caller itself."
            }
            EndpointKind::HttpRoute => {
                "A Sky.Http.Server route. Its auth is enforced in the handler, not statically recovered here."
            }
        };
        op.insert(
            "description".into(),
            json!(format!("Handler: {}. {kind}", e.handler)),
        );
        op.insert("responses".into(), default_responses());
        // A declared HTTP route is outside the framework CSRF contract; its auth
        // (if any) lives in the handler and is not statically recoverable, so we
        // assert no security scheme rather than guess one. `[]` = we make no auth
        // claim (distinct from the /_rpc operations, which we KNOW use CSRF).
        let _ = csrf_exempt;
        op.insert("security".into(), json!([]));
        path_entry(&mut paths, &e.path).insert(method, Value::Object(op));
    }

    // /_rpc/<Msg> operations — the framework's internal client↔server transport.
    // Included by default, tagged `rpc`, each flagged as internal transport.
    if include_rpc {
        for e in &report.endpoints {
            let path = format!("/_rpc/{}", e.msg);
            let mut op = Map::new();
            op.insert("summary".into(), json!(format!("RPC {}", e.msg)));
            op.insert("operationId".into(), json!(format!("rpc_{}", e.msg)));
            op.insert("tags".into(), json!(["rpc"]));
            let mut desc = format!(
                "Sky.Spa internal RPC transport for the `{}` update branch — NOT a hand-designed public REST endpoint. Generated from the server branch's read/write set.",
                e.msg
            );
            if !e.effect_families.is_empty() {
                desc.push_str(&format!(" Call-path: {}.", e.effect_families.join(", ")));
            }
            op.insert("description".into(), json!(desc));
            op.insert(
                "requestBody".into(),
                json!({
                    "required": true,
                    "content": { "application/json": { "schema": request_schema(e, &report.model_fields) } },
                }),
            );
            op.insert(
                "responses".into(),
                json!({
                    "200": {
                        "description": "The Model fields the branch writes.",
                        "content": { "application/json": { "schema": response_schema(e, &report.model_fields) } },
                    },
                    "4XX": { "description": "Rejected — CSRF failure, a guard/auth denial, or a bad request." },
                }),
            );
            op.insert("security".into(), json!([{ "csrfToken": [] }]));
            path_entry(&mut paths, &path).insert("post".into(), Value::Object(op));
        }
    }

    let description = format!(
        "Generated by `sky doc --api openapi` from the typed Sky source of `{app_name}`. \
         Every path is statically derived from the app's server surface. \
         `/_rpc/*` operations are tagged `rpc` (the Sky.Spa client↔server transport); \
         other paths are declared HTTP routes."
    );

    json!({
        "openapi": "3.1.0",
        "info": {
            "title": app_name,
            "version": version,
            "description": description,
        },
        "servers": [
            { "url": "/", "description": "Same-origin — the app serves its own API." }
        ],
        "tags": [
            { "name": "rpc", "description": "Sky.Spa internal client↔server RPC transport (not a public REST API)." }
        ],
        "paths": Value::Object(paths),
        "components": {
            "securitySchemes": {
                "csrfToken": {
                    "type": "apiKey",
                    "in": "header",
                    "name": "X-Sky-Csrf",
                    "description": "Double-submit CSRF token required on session-scoped endpoints.",
                }
            }
        },
    })
}

/// The request schema for an RPC endpoint: an object of the Model fields the wire
/// request carries — `read ∪ write` (a preserved-and-returned field rides the
/// request so the server does not default it) — plus the Msg args, or the whole
/// Model when it reads/writes it all.
fn request_schema(e: &WireEndpoint, model: &[ModelFieldTy]) -> Value {
    let mut props = Map::new();
    for a in &e.msg_arg_tys {
        props.insert(a.name.clone(), ty_to_schema(&a.ty_name));
    }
    // `read ∪ (write − always_written)`, matching the split's actual request
    // (BranchIo::request_fields): a preserved-and-returned field rides the request,
    // but a field the server assigns fresh on every leaf (always_written) does not.
    let mut req_fields = e.read_fields.clone();
    for f in &e.write_fields {
        if !e.always_written.contains(f) && !req_fields.contains(f) {
            req_fields.push(f.clone());
        }
    }
    req_fields.sort();
    if e.reads_whole_model || e.writes_whole_model {
        // The branch reads/writes the whole Model; the request still carries the
        // Msg args on top of it.
        let mut obj = Map::new();
        obj.insert("type".into(), json!("object"));
        obj.insert(
            "description".into(),
            json!("The whole Model (the branch reads every field), plus the Msg args below."),
        );
        if !props.is_empty() {
            obj.insert("properties".into(), Value::Object(props));
        }
        return Value::Object(obj);
    }
    for f in &req_fields {
        props.insert(f.clone(), field_schema(f, model));
    }
    if props.is_empty() {
        return json!({ "type": "object", "description": "No request body (no reads, no args)." });
    }
    json!({ "type": "object", "properties": Value::Object(props) })
}

/// The response schema: the Model fields the branch writes, or the whole Model.
fn response_schema(e: &WireEndpoint, model: &[ModelFieldTy]) -> Value {
    if e.writes_whole_model {
        return json!({
            "type": "object",
            "description": "The whole Model (the branch writes every field).",
        });
    }
    let mut props = Map::new();
    for f in &e.write_fields {
        props.insert(f.clone(), field_schema(f, model));
    }
    if props.is_empty() {
        return json!({ "type": "object", "description": "No response body." });
    }
    json!({ "type": "object", "properties": Value::Object(props) })
}

/// Resolve a Model field name to its JSON Schema via the typed Model fields; an
/// unknown field (not in the Model type) falls back to a described any.
fn field_schema(name: &str, model: &[ModelFieldTy]) -> Value {
    match model.iter().find(|m| m.name == name) {
        Some(m) => ty_to_schema(&m.ty_name),
        None => json!({ "description": format!("Sky field `{name}` (type not recovered)") }),
    }
}

/// Map a rendered Sky type name to a JSON Schema. Primitives, `List a`,
/// `Maybe a`, and `Dict …` map exactly; a user record/union is a described object
/// placeholder (its nested shape is not expanded in v1).
pub fn ty_to_schema(ty_name: &str) -> Value {
    let t = ty_name.trim();
    // Strip one layer of surrounding parens: `(Maybe Int)`.
    if let Some(inner) = t.strip_prefix('(').and_then(|s| s.strip_suffix(')')) {
        return ty_to_schema(inner);
    }
    if let Some(rest) = t.strip_prefix("Maybe ") {
        // JSON Schema (OpenAPI 3.1): a nullable value is `anyOf [T, null]`.
        return json!({ "anyOf": [ty_to_schema(rest), { "type": "null" }] });
    }
    if let Some(rest) = t.strip_prefix("List ") {
        return json!({ "type": "array", "items": ty_to_schema(rest) });
    }
    if let Some(rest) = t.strip_prefix("Dict ") {
        // `Dict K V` — key + value; JSON object keys are strings, value = last word.
        let val = rest.split_whitespace().last().unwrap_or("String");
        return json!({ "type": "object", "additionalProperties": ty_to_schema(val) });
    }
    match t {
        "String" => json!({ "type": "string" }),
        "Int" => json!({ "type": "integer", "format": "int64" }),
        "Float" => json!({ "type": "number", "format": "double" }),
        "Bool" => json!({ "type": "boolean" }),
        "Decimal" | "Money" => {
            json!({ "type": "string", "description": "Sky Decimal/Money — an exact decimal, never a float." })
        }
        "Time" | "Posix" | "Instant" => json!({ "type": "string", "format": "date-time" }),
        "Uuid" => json!({ "type": "string", "format": "uuid" }),
        "Secret" => {
            json!({ "type": "string", "format": "password", "description": "Sky Secret — redacted at every boundary." })
        }
        "" => json!({ "description": "unknown type" }),
        // A user record / union: honest placeholder (nested shape not expanded).
        other => {
            json!({ "type": "object", "description": format!("Sky type: {other} (nested shape not expanded)") })
        }
    }
}

/// A conservative operationId from a method + path (`get_admin_login`).
fn operation_id(method: &str, path: &str) -> String {
    let mut s = method.to_ascii_lowercase();
    for seg in path.split('/').filter(|p| !p.is_empty()) {
        s.push('_');
        s.push_str(&seg.replace([':', '{', '}', '-'], "_"));
    }
    s
}

/// A generic 200/4xx/5xx response set for a raw handler whose body shape is not
/// recoverable from the Model-field analysis.
fn default_responses() -> Value {
    json!({
        "200": { "description": "OK" },
        "4XX": { "description": "A client error (bad request, unauthorised, not found)." },
        "default": { "description": "The handler's response (shape not statically recovered)." }
    })
}

/// Get (or create) the path-item object for `path` in the paths map.
fn path_entry<'a>(paths: &'a mut Map<String, Value>, path: &str) -> &'a mut Map<String, Value> {
    paths
        .entry(path.to_string())
        .or_insert_with(|| Value::Object(Map::new()))
        .as_object_mut()
        .expect("path item is an object")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn primitive_types_map() {
        assert_eq!(ty_to_schema("String"), json!({ "type": "string" }));
        assert_eq!(
            ty_to_schema("Int"),
            json!({ "type": "integer", "format": "int64" })
        );
        assert_eq!(ty_to_schema("Bool"), json!({ "type": "boolean" }));
    }

    #[test]
    fn maybe_is_nullable_anyof() {
        assert_eq!(
            ty_to_schema("Maybe String"),
            json!({ "anyOf": [{ "type": "string" }, { "type": "null" }] })
        );
    }

    #[test]
    fn list_and_dict_map() {
        assert_eq!(
            ty_to_schema("List Int"),
            json!({ "type": "array", "items": { "type": "integer", "format": "int64" } })
        );
        assert_eq!(
            ty_to_schema("Dict String Int")["additionalProperties"],
            json!({ "type": "integer", "format": "int64" })
        );
    }

    #[test]
    fn secret_and_money_are_marked() {
        assert_eq!(ty_to_schema("Secret")["format"], json!("password"));
        assert_eq!(ty_to_schema("Money")["type"], json!("string"));
    }

    #[test]
    fn user_type_is_a_described_placeholder() {
        let s = ty_to_schema("Todo");
        assert_eq!(s["type"], json!("object"));
        assert!(s["description"].as_str().unwrap().contains("Todo"));
    }

    #[test]
    fn operation_id_is_sane() {
        assert_eq!(operation_id("GET", "/admin/login"), "get_admin_login");
        assert_eq!(
            operation_id("POST", "/webhooks/stripe"),
            "post_webhooks_stripe"
        );
    }

    /// Soundness (bug #1): a field an internal branch WRITES but does not READ
    /// still rides the request, so the server does not rebuild it as the
    /// empty-Model default. The request schema is `read ∪ write`, matching the
    /// split's own `BranchIo::request_fields`. A read-only request schema is
    /// exactly what let a preserved field get clobbered on the wire.
    #[test]
    fn request_schema_carries_a_write_only_preserved_field() {
        let model = vec![
            ModelFieldTy {
                name: "counter".into(),
                ty_name: "Int".into(),
                codec: Some("Codec.int".into()),
                ty: None,
            },
            ModelFieldTy {
                name: "note".into(),
                ty_name: "String".into(),
                codec: Some("Codec.string".into()),
                ty: None,
            },
        ];
        // A branch that reads `counter` and writes `note` (the else-arm bumps
        // `counter`, the then-arm sets `note`; the response is the union).
        let e = WireEndpoint {
            msg: "Act".into(),
            request: "{counter}".into(),
            response: "{counter, note}".into(),
            effects: Some("Log".into()),
            effect_families: vec![],
            read_fields: vec!["counter".into()],
            write_fields: vec!["counter".into(), "note".into()],
            // `counter` is assigned fresh on the counter++ leaf but `note` is
            // preserved there, and vice-versa on the note leaf, so neither is in
            // `always_written` — both preserved fields ride the request.
            always_written: vec![],
            reads_whole_model: false,
            writes_whole_model: false,
            msg_arg_tys: vec![],
        };
        let schema = request_schema(&e, &model);
        let props = schema["properties"]
            .as_object()
            .expect("request has properties");
        assert!(props.contains_key("counter"), "read field carried");
        assert!(
            props.contains_key("note"),
            "write-only preserved field MUST ride the request: {schema}"
        );
        assert_eq!(props["note"]["type"], json!("string"));
    }
}
