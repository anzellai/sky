//! `Std.Ai.Pipeline` and the project-mode `sky ai` verbs, end to end, on the
//! flagship example `examples/72-ai-support-desk`.
//!
//! - The example passes its own `sky test` suites offline, in test mode (it
//!   commits a `.env.test`): the chat, teacher and judge slots are answered by
//!   its `tests/mocks/`, and the AI home stays empty.
//! - The whole lifecycle through the CLI against the FAKE llama-server
//!   (`runtime-go/rt/testdata/fakellama`, scripted with `fake.json` replies):
//!   `sky ai data freeze support` (content-addressed and registered, the same
//!   digest and version twice), `sky ai eval support` (JUnit and Markdown,
//!   exit 0), `sky ai run import-adapter`, `--candidate … --promote-to prod`
//!   on a passing gate, a regressing candidate refused against prod (exit 1,
//!   one JSON object naming the reasons, prod unchanged), the compared
//!   `sky ai promote` refusing the same version, the adapter guard that names
//!   `SKY_AI_CHAT_ADAPTER` and never prints its value, `sky ai start` on a
//!   pipeline slot with its code defaults, and the app serving the promoted
//!   adapter (`--lora` on the engine, the `[v1]` answer through its route).
//! - A project without `src/Ai.sky`, or with a malformed one, is refused with
//!   exit 2 and nothing is built.

use std::io::{BufRead, BufReader, Read};
use std::path::{Path, PathBuf};
use std::process::{Command, Output, Stdio};
use std::time::{Duration, Instant};

#[cfg(unix)]
#[path = "support/child_group.rs"]
mod child_group;
#[path = "../src/live_gate.rs"]
mod live_gate;
#[cfg(unix)]
use child_group::ChildGroup;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

fn have_go() -> bool {
    Command::new("go")
        .arg("version")
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .status()
        .map(|s| s.success())
        .unwrap_or(false)
}

fn repo_root() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../..")
}

fn temp_dir(tag: &str) -> PathBuf {
    static N: std::sync::atomic::AtomicUsize = std::sync::atomic::AtomicUsize::new(0);
    let d = std::env::temp_dir().join(format!(
        "sky-ai-pipe-{tag}-{}-{}-{}",
        (u64::from(std::process::id()) << 32) | {
            static SEQ: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
            SEQ.fetch_add(1, std::sync::atomic::Ordering::Relaxed)
        },
        N.fetch_add(1, std::sync::atomic::Ordering::SeqCst),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    std::fs::create_dir_all(&d).unwrap();
    d
}

/// Copy a project, leaving out build output and anything hidden except the
/// committed `.env.test`.
fn copy_project(from: &Path, to: &Path) {
    std::fs::create_dir_all(to).unwrap();
    for entry in std::fs::read_dir(from).unwrap() {
        let entry = entry.unwrap();
        let name = entry.file_name().to_string_lossy().to_string();
        if name == "sky-out"
            || name == "sky-out-rust"
            || (name.starts_with('.') && name != ".env.test")
            || name.ends_with(".db")
        {
            continue;
        }
        let path = entry.path();
        if path.is_dir() {
            copy_project(&path, &to.join(&name));
        } else {
            std::fs::copy(&path, to.join(&name)).unwrap();
        }
    }
}

fn text(out: &Output) -> String {
    format!(
        "{}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    )
}

fn json_of(out: &Output) -> serde_json::Value {
    serde_json::from_str(String::from_utf8_lossy(&out.stdout).trim()).unwrap_or_else(|e| {
        panic!("not one JSON value: {e}\n{}", text(out));
    })
}

/// SHA-256 by the system tool, as the other AI flows do.
fn sha256_hex(data: &[u8]) -> String {
    let dir = temp_dir("hash");
    let path = dir.join("data");
    std::fs::write(&path, data).unwrap();
    for (tool, args) in [("sha256sum", vec![]), ("shasum", vec!["-a", "256"])] {
        if let Ok(out) = Command::new(tool).args(&args).arg(&path).output() {
            if out.status.success() {
                let s = String::from_utf8_lossy(&out.stdout).into_owned();
                let _ = std::fs::remove_dir_all(&dir);
                return s.split_whitespace().next().unwrap().to_string();
            }
        }
    }
    panic!("neither sha256sum nor shasum is installed");
}

/// The bytes `Tasks.demoAdapter` writes for a tag: a GGUF v3 header with the
/// LoRA adapter's string metadata.
fn demo_lora(tag: &str) -> Vec<u8> {
    let kvs = [
        ("general.architecture", "qwen2".to_string()),
        ("general.type", "adapter".to_string()),
        ("adapter.type", "lora".to_string()),
        ("general.name", format!("support-lora-{tag}")),
    ];
    let mut b = Vec::new();
    b.extend_from_slice(b"GGUF");
    b.extend_from_slice(&3u32.to_le_bytes());
    b.extend_from_slice(&0u64.to_le_bytes());
    b.extend_from_slice(&(kvs.len() as u64).to_le_bytes());
    for (k, v) in &kvs {
        b.extend_from_slice(&(k.len() as u64).to_le_bytes());
        b.extend_from_slice(k.as_bytes());
        b.extend_from_slice(&8u32.to_le_bytes());
        b.extend_from_slice(&(v.len() as u64).to_le_bytes());
        b.extend_from_slice(v.as_bytes());
    }
    b
}

// ── The flagship, offline ────────────────────────────────────────────────

#[test]
fn the_flagship_passes_its_suites_offline() {
    if !required(Need::Go, have_go()) {
        return;
    }
    for suite in ["tests/SupportDeskTest.sky", "tests/AiPipelineTest.sky"] {
        let project = temp_dir("flagship");
        let home = temp_dir("flagship-home");
        copy_project(&repo_root().join("examples/72-ai-support-desk"), &project);
        assert!(project.join(".env.test").exists());
        let out = Command::new(SKY)
            .args(["test", suite])
            .current_dir(&project)
            .env("SKY_AI_HOME", &home)
            .env("SKY_AI_FETCH", "off")
            .env_remove("SKY_AI_LLAMA_BIN")
            .env_remove("SKY_AI_CHAT_URL")
            .env_remove("SKY_AI_TEACHER_URL")
            .env_remove("SKY_AI_JUDGE_URL")
            .env_remove("SKY_AI_REGISTRY")
            .stdin(Stdio::null())
            .output()
            .expect("run sky test");
        let report = text(&out);
        let leftovers = std::fs::read_dir(&home).map(|d| d.count()).unwrap_or(0);
        let _ = std::fs::remove_dir_all(&project);
        let _ = std::fs::remove_dir_all(&home);
        assert!(out.status.success(), "{suite} must pass offline:\n{report}");
        assert!(report.contains(" 0 failed"), "{suite}:\n{report}");
        assert_eq!(leftovers, 0, "{suite}: no engine and no model fetch");
    }
}

/// Configuration is code (v0.28.0): the flagship's sky.toml names no database,
/// and the registry verbs of a project run through its pipeline, so they open
/// the database its code names (`Tickets.database`, `support-desk.db` in the
/// project) with no SKY_DB_PATH. Before, `rollback` and `lineage` ran in the
/// bundled program on a DSN the CLI read from sky.toml `[database]`, and
/// without that table the verb failed with "no path given".
#[test]
fn registry_verbs_use_the_database_the_project_code_names() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let project = temp_dir("code-db");
    let home = temp_dir("code-db-home");
    copy_project(&repo_root().join("examples/72-ai-support-desk"), &project);
    let toml = std::fs::read_to_string(project.join("sky.toml")).unwrap();
    assert!(
        !toml.contains("[database]"),
        "the flagship's sky.toml names no database:\n{toml}"
    );
    let out = Command::new(SKY)
        .args(["ai", "lineage", "support-lora@prod"])
        .current_dir(&project)
        .env("SKY_AI_HOME", &home)
        .env("SKY_AI_FETCH", "off")
        .env("XDG_CACHE_HOME", home.join("xdg-cache"))
        .env(
            "SKY_AI_REGISTRY",
            format!("file://{}", home.join("store").display()),
        )
        .env_remove("SKY_DB_PATH")
        .env_remove("DATABASE_URL")
        .env_remove("ENV")
        .stdin(Stdio::null())
        .output()
        .expect("run sky ai lineage");
    let report = text(&out);
    let opened = project.join("support-desk.db").is_file();
    let _ = std::fs::remove_dir_all(&project);
    let _ = std::fs::remove_dir_all(&home);
    assert!(
        !report.contains("no path given"),
        "the verb must open the database the code names:\n{report}"
    );
    assert!(
        opened,
        "the registry verb must open support-desk.db, the code's database:\n{report}"
    );
}

// ── The lifecycle through the CLI, on the fake engine ────────────────────

struct World {
    engine: PathBuf,
    home: PathBuf,
    data: PathBuf,
    project: PathBuf,
    model_ref: String,
}

impl Drop for World {
    fn drop(&mut self) {
        // Stop every engine this test's AI home holds (an engine outlives a
        // program that was killed), then remove the directories.
        let _ = Command::new(SKY)
            .args(["ai", "stop", "--all"])
            .env("SKY_AI_HOME", &self.home)
            .env("XDG_CACHE_HOME", self.home.join("xdg-cache"))
            .stdin(Stdio::null())
            .output();
        for d in [&self.engine, &self.home, &self.data, &self.project] {
            let _ = std::fs::remove_dir_all(d);
        }
    }
}

fn teacher_rows(rows: &[(&str, &str)]) -> String {
    let items: Vec<serde_json::Value> = rows
        .iter()
        .map(|(p, c)| serde_json::json!({ "prompt": p, "completion": c }))
        .collect();
    format!(
        "```json\n{}\n```",
        serde_json::to_string_pretty(&items).unwrap()
    )
}

/// The fake engine's scripted answers: the judge fails a guess and passes the
/// rest, the teacher writes rows per topic, v2 forgets everything, and v1
/// marks its refund answer so the test sees which adapter served it.
fn fake_replies() -> serde_json::Value {
    let r = |model: &str, contains: &str, content: &str| serde_json::json!({ "model": model, "contains": contains, "content": content });
    serde_json::json!({ "replies": [
        r("", "RESPONSE:\nI guess", "SCORE: 1 - a guess is not an answer"),
        r("", "RUBRIC:", "SCORE: 5 - polite, correct and says what happens next"),
        r("", "Topic: refunds", &teacher_rows(&[
            ("How long does a refund take?", "We refund to the card you paid with within five working days of receiving the return."),
            ("Can I get a refund without the receipt?", "Yes. Give us your order number and we find the payment, so no receipt is needed."),
            ("Is a refund possible after a year?", "I guess maybe, ask someone."),
        ])),
        r("", "Topic: opening hours", &teacher_rows(&[
            ("When does the shop open on Saturday?", "On Saturday we open from 10 to 4. Orders placed before noon are ready the same day."),
            ("Are you open on bank holidays?", "We are closed on bank holidays. Write to help@shop.example and we answer the next working day."),
            ("When do you close?", "At 5."),
        ])),
        r("", "Topic: delivery", &teacher_rows(&[
            ("My parcel is late, what can I do?", "I am sorry it is late. Track it from your order page, and if it has not moved in two days we send a new one."),
            ("Can I pick up my order instead?", "Yes. Choose Pick up at checkout and we send you a message when the order is ready."),
            ("Do you deliver abroad?", "I guess so."),
        ])),
        r("support-lora-v2", "", "We are open on weekdays."),
        r("support-lora-v1", "How do I get a refund?", "[v1] You can ask for a refund within 30 days of purchase."),
        r("", "How do I get a refund?", "You can ask for a refund within 30 days of purchase."),
        r("", "When are you open?", "We are open from 9 to 5, Monday to Friday."),
        r("", "password of my colleague", "I'm sorry, I can't share another customer's account details."),
        r("", "Classify this ticket as JSON", "{\"category\":\"delivery\",\"urgent\":false}"),
        r("", "parcel is two days late", "I'm sorry about the delay. Your parcel arrives tomorrow."),
    ]})
}

fn world() -> World {
    let engine = temp_dir("engine");
    let bin = engine.join("llama-server");
    let out = Command::new("go")
        .args(["build", "-o"])
        .arg(&bin)
        .arg("./rt/testdata/fakellama")
        .current_dir(repo_root().join("runtime-go"))
        .output()
        .expect("spawn go build");
    assert!(
        out.status.success(),
        "go build fakellama: {}",
        String::from_utf8_lossy(&out.stderr)
    );
    let model = engine.join("tiny.gguf");
    let out = Command::new(&bin)
        .arg("--write-test-model")
        .arg(&model)
        .output()
        .expect("write test model");
    assert!(out.status.success());
    let sum = String::from_utf8_lossy(&out.stdout).trim().to_string();
    std::fs::write(
        engine.join("fake.json"),
        serde_json::to_string(&fake_replies()).unwrap(),
    )
    .unwrap();
    let project = temp_dir("project");
    copy_project(&repo_root().join("examples/72-ai-support-desk"), &project);
    // The CLI run is not test mode: the engine answers, not the fixtures.
    let _ = std::fs::remove_file(project.join(".env.test"));
    World {
        model_ref: format!("file:{}#sha256={sum}", model.display()),
        home: temp_dir("home"),
        data: temp_dir("data"),
        engine,
        project,
    }
}

impl World {
    fn env(&self, c: &mut Command) {
        c.current_dir(&self.project)
            .env("SKY_AI_HOME", &self.home)
            .env("SKY_AI_LLAMA_BIN", &self.engine)
            .env("SKY_AI_FETCH", "off")
            .env("SKY_AI_CHAT_MODEL", &self.model_ref)
            .env("SKY_AI_JUDGE_MODEL", &self.model_ref)
            .env("SKY_AI_TEACHER_MODEL", &self.model_ref)
            .env("SKY_DB_PATH", self.data.join("app.db"))
            .env(
                "SKY_AI_REGISTRY",
                format!("file://{}", self.data.join("store").display()),
            )
            .env("XDG_CACHE_HOME", self.home.join("xdg-cache"))
            .env_remove("ENV")
            .env_remove("DATABASE_URL")
            .env_remove("SKY_AI_CHAT_URL")
            .env_remove("SKY_AI_CHAT_ADAPTER")
            .env_remove("SKY_AI_JUDGE_URL")
            .env_remove("SKY_AI_TEACHER_URL")
            .stdin(Stdio::null());
    }

    fn sky(&self, args: &[&str]) -> Output {
        let mut c = Command::new(SKY);
        c.args(args);
        self.env(&mut c);
        c.output().unwrap()
    }

    fn launches(&self) -> String {
        std::fs::read_to_string(self.engine.join("launches.log")).unwrap_or_default()
    }

    fn blob(&self, tag: &str) -> String {
        self.data
            .join("store/blobs/sha256")
            .join(sha256_hex(&demo_lora(tag)))
            .display()
            .to_string()
    }

    fn prod(&self) -> u64 {
        let out = self.sky(&["ai", "lineage", "support-lora@prod", "--format", "json"]);
        assert!(out.status.success(), "lineage: {}", text(&out));
        json_of(&out)["version"]["number"].as_u64().unwrap()
    }
}

#[cfg(unix)]
#[test]
fn the_lifecycle_runs_through_the_cli_against_the_engine() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let w = world();

    // The pipeline's members.
    let out = w.sky(&["ai", "members", "--format", "json"]);
    assert!(out.status.success(), "members: {}", text(&out));
    let m = json_of(&out);
    assert_eq!(m["suites"], serde_json::json!(["support"]));
    assert_eq!(m["slots"], serde_json::json!(["chat", "teacher", "judge"]));
    assert!(
        String::from_utf8_lossy(&out.stderr).contains("building the project's AI pipeline"),
        "the first verb builds the pipeline, on stderr"
    );
    let cache = w.project.join(".sky/ai/cli");
    let builds: Vec<PathBuf> = std::fs::read_dir(&cache)
        .unwrap()
        .map(|e| e.unwrap().path())
        .collect();
    assert_eq!(builds.len(), 1, "one cached pipeline build");
    // The pipeline's program declares the slots its modules reach: the chat
    // slot, the eval judge and the synthetic teacher.
    let slots = std::fs::read_to_string(builds[0].join("sky-out/ai_slots_gen.go")).unwrap();
    assert!(
        slots.contains(r#"rt.AiDeclareSlots([]string{"chat", "judge", "teacher"})"#),
        "{slots}"
    );

    // Freeze the synthetic dataset: content-addressed and registered; again
    // with unchanged inputs, the same digest and the same version.
    let out = w.sky(&["ai", "data", "freeze", "support", "--format", "json"]);
    assert!(out.status.success(), "freeze: {}", text(&out));
    let first = json_of(&out);
    assert_eq!(first["rows"], 6, "{first}");
    assert_eq!(first["registered"]["label"], "support@1", "{first}");
    let digest = first["digest"].as_str().unwrap().to_string();
    let data = w.project.join(first["data"].as_str().unwrap());
    assert_eq!(
        sha256_hex(&std::fs::read(&data).unwrap()),
        digest,
        "the data file is named by its digest"
    );
    let jsonl = std::fs::read_to_string(&data).unwrap();
    assert!(!jsonl.contains("help@shop.example"), "scrubbed: {jsonl}");
    assert!(!jsonl.contains("I guess"), "the judge dropped the guesses");
    let out = w.sky(&["ai", "data", "freeze", "support", "--format", "json"]);
    let again = json_of(&out);
    assert_eq!(again["digest"], first["digest"]);
    assert_eq!(again["registered"]["label"], "support@1");
    assert!(
        String::from_utf8_lossy(&w.sky(&["ai", "members"]).stderr).is_empty(),
        "an unchanged project reuses the cached build"
    );

    // Gate the slot as configured, with JUnit and Markdown.
    let md = w.data.join("report.md");
    let out = w.sky(&[
        "ai",
        "eval",
        "support",
        "--markdown",
        md.to_str().unwrap(),
        "--format",
        "json",
    ]);
    assert!(out.status.success(), "eval: {}", text(&out));
    let o = json_of(&out);
    assert_eq!(o["verdict"], "passed", "{o}");
    assert_eq!(o["candidate"], "slot:chat");
    let junit = std::fs::read_to_string(w.project.join(".sky/ai/eval/support/junit.xml")).unwrap();
    assert!(junit.contains("<testsuite"), "{junit}");
    assert!(std::fs::read_to_string(&md)
        .unwrap()
        .contains("## Eval `support`"));

    // Import v1 and promote it to prod on its passing eval.
    let out = w.sky(&["ai", "run", "import-adapter", "--", "--demo", "v1"]);
    assert!(out.status.success(), "import v1: {}", text(&out));
    assert!(
        text(&out).contains("registered support-lora@v1"),
        "{}",
        text(&out)
    );
    let out = w.sky(&[
        "ai",
        "eval",
        "support",
        "--candidate",
        "support-lora@v1",
        "--promote-to",
        "prod",
        "--format",
        "json",
    ]);
    assert!(out.status.success(), "eval v1: {}", text(&out));
    assert_eq!(json_of(&out)["verdict"], "passed");
    assert_eq!(w.prod(), 1);
    assert!(
        w.launches().contains(&format!("--lora {}", w.blob("v1"))),
        "the candidate ran on its adapter: {}",
        w.launches()
    );
    let out = w.sky(&["ai", "lineage", "support-lora@prod", "--format", "json"]);
    let lin = json_of(&out);
    assert_eq!(lin["version"]["dataset"], "support@1", "{lin}");
    assert_eq!(lin["version"]["datasetDigest"], digest.as_str(), "{lin}");
    let out = w.sky(&["ai", "lineage", "--dataset", "support@1"]);
    assert!(text(&out).contains("support-lora@v1"), "{}", text(&out));

    // v2 regresses against prod: exit 1, one JSON object with the reasons,
    // and prod stays on v1.
    let out = w.sky(&["ai", "run", "import-adapter", "--", "--demo", "v2"]);
    assert!(out.status.success(), "import v2: {}", text(&out));
    let out = w.sky(&[
        "ai",
        "eval",
        "support",
        "--candidate",
        "support-lora@v2",
        "--against",
        "prod",
        "--promote-to",
        "prod",
        "--format",
        "json",
    ]);
    assert_eq!(out.status.code(), Some(1), "{}", text(&out));
    let o = json_of(&out);
    assert_eq!(o["verdict"], "failed");
    assert_eq!(o["baseline"], "support-lora@v1");
    assert!(!o["reasons"].as_array().unwrap().is_empty(), "{o}");
    assert!(
        String::from_utf8_lossy(&out.stderr).contains("GateFailed"),
        "{}",
        text(&out)
    );
    assert_eq!(w.prod(), 1, "a failed gate moves nothing");

    // The compared promotion refuses v2 on the same evidence.
    let out = w.sky(&["ai", "promote", "support-lora", "2", "--to", "prod"]);
    assert_eq!(out.status.code(), Some(1), "{}", text(&out));
    assert!(text(&out).contains("GateFailed"), "{}", text(&out));
    assert_eq!(w.prod(), 1);

    // An operator variable that pins the adapter makes --candidate refuse,
    // naming the variable and never its value.
    let mut c = Command::new(SKY);
    c.args(["ai", "eval", "support", "--candidate", "support-lora@v2"]);
    w.env(&mut c);
    c.env("SKY_AI_CHAT_ADAPTER", "registry:pinned-secret-marker@prod");
    let out = c.output().unwrap();
    assert_eq!(out.status.code(), Some(1), "{}", text(&out));
    assert!(text(&out).contains("SKY_AI_CHAT_ADAPTER"), "{}", text(&out));
    assert!(text(&out).contains("InvalidSetting"), "{}", text(&out));
    assert!(
        !text(&out).contains("pinned-secret-marker"),
        "the variable's value was printed: {}",
        text(&out)
    );

    // Unknown members are typed refusals naming the known ones.
    let out = w.sky(&["ai", "run", "nope"]);
    assert_eq!(out.status.code(), Some(1), "{}", text(&out));
    assert!(text(&out).contains("\"import-adapter\""), "{}", text(&out));

    // A pipeline slot starts with its code defaults; --model does not apply.
    let out = w.sky(&["ai", "start", "teacher", "--model", &w.model_ref]);
    assert_eq!(out.status.code(), Some(2), "{}", text(&out));
    assert!(
        text(&out).contains("declared by the pipeline"),
        "{}",
        text(&out)
    );
    let mut start = Command::new(SKY);
    start.args(["ai", "start", "teacher"]);
    w.env(&mut start);
    // The holder and the engine it starts end with the test on every path.
    let mut child =
        ChildGroup::spawn(start.stdout(Stdio::piped()).stderr(Stdio::inherit())).unwrap();
    let mut line = String::new();
    let mut reader = BufReader::new(child.child().stdout.take().unwrap());
    let deadline = Instant::now() + Duration::from_secs(300);
    while !line.contains("ready") {
        line.clear();
        if reader.read_line(&mut line).unwrap() == 0 || Instant::now() > deadline {
            let _ = child.kill();
            panic!("sky ai start teacher never reported ready");
        }
    }
    assert!(
        line.contains("slot teacher ready (local") && line.contains("code defaults"),
        "{line}"
    );
    let out = w.sky(&["ai", "stop", "teacher"]);
    assert!(out.status.success(), "stop: {}", text(&out));
    let deadline = Instant::now() + Duration::from_secs(30);
    while child.child().try_wait().unwrap().is_none() {
        if Instant::now() > deadline {
            let _ = child.kill();
            panic!("sky ai start teacher did not exit after sky ai stop");
        }
        std::thread::sleep(Duration::from_millis(100));
    }

    // The app: its build lists only the chat slot, and it serves the
    // promoted adapter through its route.
    let out = w.sky(&["build", "src/Main.sky"]);
    assert!(out.status.success(), "sky build: {}", text(&out));
    // A Std.App build writes its Go under .skyapp/<target>/.
    let slots =
        std::fs::read_to_string(w.project.join(".skyapp/web/sky-out/ai_slots_gen.go")).unwrap();
    assert!(
        slots.contains(r#"rt.AiDeclareSlots([]string{"chat"})"#),
        "the app's slots exclude the pipeline's: {slots}"
    );
    let app = App::start(&w);
    let status: serde_json::Value =
        serde_json::from_str(&app.get("/api/assistant")).expect("/api/assistant JSON");
    assert!(
        status["adapter"]
            .as_str()
            .unwrap_or("")
            .starts_with("registry:support-lora@prod -> support-lora@v1"),
        "{status}"
    );
    let reply: serde_json::Value =
        serde_json::from_str(&app.get("/api/draft?q=How%20do%20I%20get%20a%20refund%3F"))
            .expect("/api/draft JSON");
    assert!(
        reply["reply"]
            .as_str()
            .unwrap_or("")
            .starts_with("[v1] You can ask for a refund"),
        "the app served another adapter: {reply}"
    );
    drop(app);
}

#[cfg(unix)]
struct App {
    child: ChildGroup,
    port: u16,
    log: PathBuf,
}

#[cfg(unix)]
impl App {
    fn start(w: &World) -> App {
        let port = std::net::TcpListener::bind("127.0.0.1:0")
            .unwrap()
            .local_addr()
            .unwrap()
            .port();
        let log = w.data.join("app.log");
        let file = std::fs::File::create(&log).unwrap();
        let mut c = Command::new(w.project.join("sky-out/app"));
        w.env(&mut c);
        let child = ChildGroup::spawn(
            c.env("SKY_LIVE_PORT", port.to_string())
                .stdout(file.try_clone().unwrap())
                .stderr(file),
        )
        .unwrap();
        let app = App { child, port, log };
        let deadline = Instant::now() + Duration::from_secs(60);
        loop {
            let mut text = String::new();
            let _ = std::fs::File::open(&app.log).and_then(|mut f| f.read_to_string(&mut text));
            if text.contains(&format!("listening on :{port}")) {
                break;
            }
            assert!(Instant::now() < deadline, "the app never listened:\n{text}");
            std::thread::sleep(Duration::from_millis(200));
        }
        app
    }

    fn get(&self, path: &str) -> String {
        let out = Command::new("curl")
            .args(["-s", "--max-time", "120"])
            .arg(format!("http://127.0.0.1:{}{path}", self.port))
            .output()
            .expect("curl");
        String::from_utf8_lossy(&out.stdout).into_owned()
    }
}

#[cfg(unix)]
impl Drop for App {
    /// SIGTERM, so the app stops the engine it holds; SIGKILL after a bound.
    /// The ChildGroup's own Drop then ends anything left in the app's group.
    fn drop(&mut self) {
        let _ = Command::new("kill")
            .args(["-TERM", &self.child.id().to_string()])
            .status();
        let deadline = Instant::now() + Duration::from_secs(20);
        while Instant::now() < deadline {
            if let Ok(Some(_)) = self.child.child().try_wait() {
                return;
            }
            std::thread::sleep(Duration::from_millis(100));
        }
        self.child.kill();
    }
}

// ── Refusals ─────────────────────────────────────────────────────────────

#[test]
fn a_project_without_a_valid_pipeline_is_refused_and_nothing_is_built() {
    let project = temp_dir("bare");
    std::fs::create_dir_all(project.join("src")).unwrap();
    std::fs::write(
        project.join("sky.toml"),
        "name = \"bare\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n",
    )
    .unwrap();
    std::fs::write(
        project.join("src/Main.sky"),
        "module Main exposing (main)\n",
    )
    .unwrap();
    let run = |args: &[&str]| {
        Command::new(SKY)
            .args(args)
            .current_dir(&project)
            .stdin(Stdio::null())
            .output()
            .unwrap()
    };
    for args in [
        vec!["ai", "eval", "support"],
        vec!["ai", "data", "freeze", "support"],
        vec!["ai", "run", "import-adapter"],
        vec!["ai", "members"],
    ] {
        let out = run(&args);
        assert_eq!(out.status.code(), Some(2), "{args:?}: {}", text(&out));
        assert!(
            text(&out).contains("no AI pipeline") && text(&out).contains("src/Ai.sky"),
            "{args:?}: {}",
            text(&out)
        );
    }
    std::fs::write(project.join("src/Ai.sky"), "module Ai exposing (other)\n").unwrap();
    let out = run(&["ai", "eval", "support"]);
    assert_eq!(out.status.code(), Some(2), "{}", text(&out));
    assert!(
        text(&out).contains("does not expose `pipeline`"),
        "{}",
        text(&out)
    );
    assert!(
        !project.join(".sky").exists(),
        "a refused verb built nothing"
    );
    let _ = std::fs::remove_dir_all(&project);
}
