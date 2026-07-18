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

    fn shutdown(mut self) {
        // Best-effort graceful stop, then hard-kill so the harness never blocks.
        let _ = Command::new("kill")
            .arg("-9")
            .arg(self.child.id().to_string())
            .status();
        let _ = self.child.wait();
    }
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
