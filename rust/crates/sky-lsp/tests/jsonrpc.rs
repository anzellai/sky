//! End-to-end: drive the actual `sky-lsp` **binary** over LSP JSON-RPC on
//! stdio (doc 10 §"The 17-test compat gate"). Reproduces the 17 nvim scenarios
//! (`scripts/lsp-test-nvim.lua`) as real `initialize` → `didOpen` →
//! hover/definition/completion round-trips against the spawned server, matching
//! responses by id and asserting the result — proving the server itself works,
//! not just the in-process engine (see `scenarios.rs`).

use serde_json::{json, Value};
use std::io::{BufRead, BufReader, Read, Write};
use std::path::{Path, PathBuf};
use std::process::{Child, ChildStdin, Command, Stdio};
use std::sync::mpsc::{self, Receiver};
use std::time::Duration;

const FIXTURE: &str = "module Main exposing (main)\n\nimport Sky.Core.Prelude exposing (..)\nimport Sky.Core.Task as Task\nimport Sky.Core.String as String\nimport Std.Log exposing (println)\nimport Std.Ui as Ui\n\ntype alias Model = { count : Int, label : String }\n\nstringify : Model -> String\nstringify model =\n    String.fromInt model.count\n\nletDemo : Int\nletDemo =\n    let abcLocal = 1\n    in abcLocal\n\ntype Msg = Increment | Decrement | SetCount Int\n\napplyMsg : Msg -> Int -> Int\napplyMsg msg current =\n    case msg of\n        Increment -> current + 1\n        Decrement -> current - 1\n        SetCount n -> n\n\ndoubleIt : Int -> Int\ndoubleIt = \\x -> x * 2\n\nmain =\n    Task.run (Task.succeed (applyMsg Increment 41))\n";

fn repo_root() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .ancestors()
        .find(|p| p.join("sky-stdlib").is_dir())
        .expect("sky-stdlib not found")
        .to_path_buf()
}

fn bin_path() -> PathBuf {
    // The integration test binary lives in target/<profile>/deps; the sky-lsp
    // binary is one level up (target/<profile>/sky-lsp).
    let mut dir = std::env::current_exe().unwrap();
    dir.pop(); // deps
    if dir.ends_with("deps") {
        dir.pop();
    }
    dir.join("sky-lsp")
}

fn main_uri() -> String {
    "file:///tmp/lsp-rust-jsonrpc/src/Main.sky".to_string()
}

struct Client {
    child: Child,
    stdin: ChildStdin,
    rx: Receiver<Value>,
    next_id: i64,
}

impl Client {
    fn start() -> Client {
        let mut child = Command::new(bin_path())
            .env("SKY_STDLIB_DIR", repo_root().join("sky-stdlib"))
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::null())
            .spawn()
            .expect("spawn sky-lsp");
        let stdin = child.stdin.take().unwrap();
        let stdout = child.stdout.take().unwrap();
        // Watchdog: no matter what, kill the child after 30s so a transport
        // stall can never hang the test harness (bounds the run per CLAUDE.md §3).
        let pid = child.id();
        std::thread::spawn(move || {
            std::thread::sleep(Duration::from_secs(30));
            // SIGKILL by pid — safe even if the child already exited.
            let _ = Command::new("kill").arg("-9").arg(pid.to_string()).status();
        });
        let (tx, rx) = mpsc::channel();
        std::thread::spawn(move || {
            let mut reader = BufReader::new(stdout);
            loop {
                // read headers
                let mut len = 0usize;
                loop {
                    let mut line = String::new();
                    if reader.read_line(&mut line).unwrap_or(0) == 0 {
                        return; // EOF
                    }
                    let t = line.trim_end();
                    if t.is_empty() {
                        break;
                    }
                    if let Some(v) = t.strip_prefix("Content-Length:") {
                        len = v.trim().parse().unwrap_or(0);
                    }
                }
                if len == 0 {
                    continue;
                }
                let mut buf = vec![0u8; len];
                if reader.read_exact(&mut buf).is_err() {
                    return;
                }
                if let Ok(v) = serde_json::from_slice::<Value>(&buf) {
                    if tx.send(v).is_err() {
                        return;
                    }
                }
            }
        });
        Client {
            child,
            stdin,
            rx,
            next_id: 0,
        }
    }

    fn send(&mut self, msg: &Value) {
        let body = serde_json::to_string(msg).unwrap();
        write!(self.stdin, "Content-Length: {}\r\n\r\n{}", body.len(), body).unwrap();
        self.stdin.flush().unwrap();
    }

    fn request(&mut self, method: &str, params: Value) -> i64 {
        self.next_id += 1;
        let id = self.next_id;
        self.send(&json!({"jsonrpc":"2.0","id":id,"method":method,"params":params}));
        id
    }

    fn notify(&mut self, method: &str, params: Value) {
        self.send(&json!({"jsonrpc":"2.0","method":method,"params":params}));
    }

    /// Await the response whose `id` matches, skipping notifications.
    fn await_response(&self, id: i64) -> Value {
        let deadline = Duration::from_secs(10);
        loop {
            let msg = self
                .rx
                .recv_timeout(deadline)
                .expect("timed out awaiting response");
            if msg.get("id").and_then(Value::as_i64) == Some(id) {
                return msg.get("result").cloned().unwrap_or(Value::Null);
            }
        }
    }

    fn initialize(&mut self) {
        let id = self.request(
            "initialize",
            json!({
                "processId": null,
                "rootUri": "file:///tmp/lsp-rust-jsonrpc",
                "capabilities": {}
            }),
        );
        let _ = self.await_response(id);
        self.notify("initialized", json!({}));
    }

    fn open(&mut self, text: &str) {
        self.notify(
            "textDocument/didOpen",
            json!({"textDocument": {
                "uri": main_uri(), "languageId": "sky", "version": 1, "text": text
            }}),
        );
    }

    fn change(&mut self, text: &str) {
        self.notify(
            "textDocument/didChange",
            json!({
                "textDocument": {"uri": main_uri(), "version": 2},
                "contentChanges": [{"text": text}]
            }),
        );
    }

    fn hover(&mut self, line: u32, ch: u32) -> String {
        let id = self.request(
            "textDocument/hover",
            json!({
                "textDocument": {"uri": main_uri()},
                "position": {"line": line, "character": ch}
            }),
        );
        let r = self.await_response(id);
        r.get("contents")
            .and_then(|c| c.get("value"))
            .and_then(Value::as_str)
            .unwrap_or("")
            .to_string()
    }

    fn definition_line(&mut self, line: u32, ch: u32) -> Option<u64> {
        let id = self.request(
            "textDocument/definition",
            json!({
                "textDocument": {"uri": main_uri()},
                "position": {"line": line, "character": ch}
            }),
        );
        let r = self.await_response(id);
        // Scalar Location: { uri, range: { start: { line } } }
        r.get("range")
            .and_then(|rg| rg.get("start"))
            .and_then(|s| s.get("line"))
            .and_then(Value::as_u64)
    }

    fn completion_items(&mut self, line: u32, ch: u32) -> Vec<(String, Option<String>)> {
        let id = self.request(
            "textDocument/completion",
            json!({
                "textDocument": {"uri": main_uri()},
                "position": {"line": line, "character": ch}
            }),
        );
        let r = self.await_response(id);
        let arr = match r.as_array() {
            Some(a) => a.clone(),
            None => r
                .get("items")
                .and_then(Value::as_array)
                .cloned()
                .unwrap_or_default(),
        };
        arr.iter()
            .map(|i| {
                (
                    i.get("label").and_then(Value::as_str).unwrap_or("").to_string(),
                    i.get("insertText").and_then(Value::as_str).map(String::from),
                )
            })
            .collect()
    }

    fn reference_lines(&mut self, line: u32, ch: u32, include_decl: bool) -> Vec<u64> {
        let id = self.request(
            "textDocument/references",
            json!({
                "textDocument": {"uri": main_uri()},
                "position": {"line": line, "character": ch},
                "context": {"includeDeclaration": include_decl}
            }),
        );
        let r = self.await_response(id);
        let mut out: Vec<u64> = r
            .as_array()
            .map(|a| {
                a.iter()
                    .filter_map(|loc| {
                        loc.get("range")
                            .and_then(|rg| rg.get("start"))
                            .and_then(|s| s.get("line"))
                            .and_then(Value::as_u64)
                    })
                    .collect()
            })
            .unwrap_or_default();
        out.sort_unstable();
        out
    }

    /// Total edit count in a rename WorkspaceEdit (or None if the server
    /// declined the rename).
    fn rename_edit_count(&mut self, line: u32, ch: u32, new: &str) -> Option<usize> {
        let id = self.request(
            "textDocument/rename",
            json!({
                "textDocument": {"uri": main_uri()},
                "position": {"line": line, "character": ch},
                "newName": new
            }),
        );
        let r = self.await_response(id);
        let changes = r.get("changes")?.as_object()?;
        Some(changes.values().filter_map(Value::as_array).map(|v| v.len()).sum())
    }

    fn document_symbol_names(&mut self) -> Vec<String> {
        let id = self.request(
            "textDocument/documentSymbol",
            json!({"textDocument": {"uri": main_uri()}}),
        );
        let r = self.await_response(id);
        r.as_array()
            .map(|a| {
                a.iter()
                    .filter_map(|s| s.get("name").and_then(Value::as_str).map(String::from))
                    .collect()
            })
            .unwrap_or_default()
    }

    /// The decoded semantic tokens as absolute (line, char, token_type).
    fn semantic_tokens(&mut self) -> Vec<(u64, u64, u64)> {
        let id = self.request(
            "textDocument/semanticTokens/full",
            json!({"textDocument": {"uri": main_uri()}}),
        );
        let r = self.await_response(id);
        let data: Vec<u64> = r
            .get("data")
            .and_then(Value::as_array)
            .map(|a| a.iter().filter_map(Value::as_u64).collect())
            .unwrap_or_default();
        let mut out = Vec::new();
        let (mut line, mut ch) = (0u64, 0u64);
        for chunk in data.chunks(5) {
            if chunk.len() < 5 {
                break;
            }
            if chunk[0] == 0 {
                ch += chunk[1];
            } else {
                line += chunk[0];
                ch = chunk[1];
            }
            out.push((line, ch, chunk[3]));
        }
        out
    }

    fn shutdown(mut self) {
        // Best-effort graceful stop, then hard-kill so the harness never blocks.
        let _ = Command::new("kill")
            .arg("-9")
            .arg(self.child.id().to_string())
            .status();
        let _ = self.child.wait();
    }
}

/// Drive the M7 breadth capabilities (references / rename / documentSymbol /
/// semanticTokens) through the real server binary end-to-end.
#[test]
fn breadth_capabilities_over_jsonrpc() {
    // Legend indices mirror `sky_lsp::semantic_legend`.
    const T_TYPE: u64 = 1;
    const T_FUNCTION: u64 = 2;

    let mut c = Client::start();
    c.initialize();
    c.open(FIXTURE);

    let mut pass = 0u32;
    let mut fails: Vec<&str> = Vec::new();
    let check = |name: &'static str, ok: bool, pass: &mut u32, fails: &mut Vec<&'static str>| {
        if ok {
            *pass += 1;
        } else {
            fails.push(name);
        }
    };

    // references on the `abcLocal` use (line 17) → binder (16) + use (17).
    check(
        "references-let-binding",
        c.reference_lines(17, 8, true) == vec![16, 17],
        &mut pass,
        &mut fails,
    );
    // references on the `applyMsg` use (line 32): incl-decl = anno+decl+use.
    check(
        "references-function-incl-decl",
        c.reference_lines(32, 30, true) == vec![21, 22, 32],
        &mut pass,
        &mut fails,
    );
    check(
        "references-function-excl-decl",
        c.reference_lines(32, 30, false) == vec![32],
        &mut pass,
        &mut fails,
    );

    // rename the local `abcLocal` (2 sites) and the function `applyMsg` (3).
    check(
        "rename-local",
        c.rename_edit_count(17, 8, "renamed") == Some(2),
        &mut pass,
        &mut fails,
    );
    check(
        "rename-function",
        c.rename_edit_count(32, 30, "applyMsgV2") == Some(3),
        &mut pass,
        &mut fails,
    );
    // rename on the builtin `Int` (line 14) must be declined.
    check(
        "rename-builtin-rejected",
        c.rename_edit_count(14, 11, "Foo").is_none(),
        &mut pass,
        &mut fails,
    );

    // documentSymbol surfaces the top-level defs + types.
    let syms = c.document_symbol_names();
    check(
        "document-symbols",
        ["stringify", "letDemo", "Model", "Msg", "applyMsg", "doubleIt"]
            .iter()
            .all(|n| syms.iter().any(|s| s == n)),
        &mut pass,
        &mut fails,
    );

    // semantic tokens classify `fromInt` (line 12,11) as function and `Model`
    // (line 10,12) as type.
    let toks = c.semantic_tokens();
    check(
        "semantic-tokens-kernel-function",
        toks.iter().any(|&(l, ch, t)| l == 12 && ch == 11 && t == T_FUNCTION),
        &mut pass,
        &mut fails,
    );
    check(
        "semantic-tokens-type-name",
        toks.iter().any(|&(l, ch, t)| l == 10 && ch == 12 && t == T_TYPE),
        &mut pass,
        &mut fails,
    );

    c.shutdown();

    eprintln!("JSON-RPC breadth gate: {pass}/9 passed");
    if !fails.is_empty() {
        eprintln!("  failed: {fails:?}");
    }
    assert_eq!(pass, 9, "expected 9/9 breadth scenarios; failed: {fails:?}");
}

/// Drive all 17 nvim scenarios through the real server; report the pass count.
#[test]
fn seventeen_scenarios_over_jsonrpc() {
    let mut c = Client::start();
    eprintln!("[jsonrpc] initializing…");
    c.initialize();
    eprintln!("[jsonrpc] initialized; didOpen…");
    c.open(FIXTURE);
    eprintln!("[jsonrpc] running scenarios…");

    let mut pass = 0u32;
    let mut fails: Vec<&str> = Vec::new();
    let check = |name: &'static str, ok: bool, pass: &mut u32, fails: &mut Vec<&'static str>| {
        if ok {
            *pass += 1;
        } else {
            fails.push(name);
        }
    };

    // ---- hover (7) ----
    check("hover-task-run", c.hover(32, 9).contains("Task"), &mut pass, &mut fails);
    check("hover-field", c.hover(12, 25).contains("Int"), &mut pass, &mut fails);
    check("hover-type-name", c.hover(10, 13).contains("Model"), &mut pass, &mut fails);
    check("hover-function-use", c.hover(32, 30).contains("Int"), &mut pass, &mut fails);
    check("hover-ctor-use", c.hover(32, 37).contains("Msg"), &mut pass, &mut fails);
    check("hover-lambda-param", c.hover(29, 12).contains("Int"), &mut pass, &mut fails);
    check("hover-case-pattern", c.hover(26, 17).contains("Int"), &mut pass, &mut fails);
    check("hover-kernel-call", c.hover(12, 14).contains("Int"), &mut pass, &mut fails);

    // ---- goto-def (7) ----
    check("goto-def-type-name", c.definition_line(10, 13) == Some(8), &mut pass, &mut fails);
    let f = c.definition_line(32, 30);
    check("goto-def-function", f == Some(21) || f == Some(22), &mut pass, &mut fails);
    check("goto-def-ctor", c.definition_line(32, 37) == Some(19), &mut pass, &mut fails);
    check("goto-def-let-binding", c.definition_line(17, 8) == Some(16), &mut pass, &mut fails);
    check("goto-def-lambda-param", c.definition_line(29, 17) == Some(29), &mut pass, &mut fails);
    check("goto-def-field", c.definition_line(12, 25) == Some(8), &mut pass, &mut fails);

    // ---- completion (3) — the two field/qualified cases need an edited buffer ----
    c.change(&format!("{FIXTURE}x = Ui.\n"));
    let items = c.completion_items(33, 7);
    let ui_layout = items.iter().find(|(l, _)| l == "Ui.layout");
    check(
        "completion-qualified-insert-text",
        ui_layout.map(|(_, it)| it.as_deref() == Some("layout")).unwrap_or(false),
        &mut pass,
        &mut fails,
    );

    c.change(&format!(
        "{FIXTURE}\ndescribe : Model -> String\ndescribe m =\n    String.fromInt m.\n"
    ));
    let fields = c.completion_items(36, 21);
    let labels: Vec<&str> = fields.iter().map(|(l, _)| l.as_str()).collect();
    check(
        "completion-field",
        labels.contains(&"count") && labels.contains(&"label"),
        &mut pass,
        &mut fails,
    );

    c.change(FIXTURE);
    let lets = c.completion_items(17, 9);
    check(
        "completion-let-binding",
        lets.iter().any(|(l, _)| l == "abcLocal"),
        &mut pass,
        &mut fails,
    );

    c.shutdown();

    eprintln!("JSON-RPC 17-scenario gate: {pass}/17 passed");
    if !fails.is_empty() {
        eprintln!("  failed: {fails:?}");
    }
    assert_eq!(pass, 17, "expected 17/17 over JSON-RPC; failed: {fails:?}");
}
