//! `Std.Ai.Registry` end to end: a compiled program registers adapters in its
//! own database, promotes one with eval evidence, and serves it through a
//! slot; the `sky ai promote | rollback | lineage` verbs move and read the
//! alias; the slot follows. Then the artefact store on GCS and S3, offline
//! (test mode answers every request from `tests/mocks/` fixtures), and the
//! same registry code on a real PostgreSQL cluster.
//!
//! The engine is the FAKE llama-server (`runtime-go/rt/testdata/fakellama`),
//! which logs its launch arguments, so the test reads which adapter file the
//! slot loaded (`--lora <path>`).

use std::path::{Path, PathBuf};
use std::process::{Command, Output, Stdio};
use std::time::{Duration, Instant};

#[cfg(unix)]
#[path = "support/child_group.rs"]
mod child_group;
#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

/// A unique suffix for a scratch directory. The clock alone is not unique:
/// macOS gives microseconds, so two tests that start together got the SAME
/// directory and shared one fake engine (ai_cli_flow counted another test's
/// launches). The counter makes every name distinct within the process.
fn unique_suffix() -> String {
    use std::sync::atomic::{AtomicUsize, Ordering};
    static NEXT: AtomicUsize = AtomicUsize::new(0);
    format!(
        "{}-{}-{}",
        (u64::from(std::process::id()) << 32) | {
            static SEQ: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
            SEQ.fetch_add(1, std::sync::atomic::Ordering::Relaxed)
        },
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos(),
        NEXT.fetch_add(1, Ordering::Relaxed)
    )
}

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
    let d = std::env::temp_dir().join(format!("sky-ai-reg-{tag}-{}", unique_suffix()));
    std::fs::create_dir_all(&d).unwrap();
    d
}

fn copy_dir(from: &Path, to: &Path) {
    std::fs::create_dir_all(to).unwrap();
    for entry in std::fs::read_dir(from).unwrap() {
        let entry = entry.unwrap();
        let dest = to.join(entry.file_name());
        if entry.file_type().unwrap().is_dir() {
            copy_dir(&entry.path(), &dest);
        } else {
            std::fs::copy(entry.path(), dest).unwrap();
        }
    }
}

/// SHA-256 by the system tool, as `install_script_flow.rs` does (the
/// workspace carries no hashing crate for tests).
fn sha256_hex(data: &[u8]) -> String {
    let path = temp_dir("hash").join("data");
    std::fs::write(&path, data).unwrap();
    for (tool, args) in [("sha256sum", vec![]), ("shasum", vec!["-a", "256"])] {
        if let Ok(out) = Command::new(tool).args(&args).arg(&path).output() {
            if out.status.success() {
                let s = String::from_utf8_lossy(&out.stdout);
                let _ = std::fs::remove_dir_all(path.parent().unwrap());
                return s.split_whitespace().next().unwrap().to_string();
            }
        }
    }
    panic!("neither sha256sum nor shasum is installed");
}

/// A GGUF v3 file with string metadata only (no tensors): enough for the
/// registry's adapter check and the fake engine.
fn gguf(kvs: &[(&str, &str)]) -> Vec<u8> {
    let mut b = Vec::new();
    b.extend_from_slice(b"GGUF");
    b.extend_from_slice(&3u32.to_le_bytes());
    b.extend_from_slice(&0u64.to_le_bytes());
    b.extend_from_slice(&(kvs.len() as u64).to_le_bytes());
    for (k, v) in kvs {
        b.extend_from_slice(&(k.len() as u64).to_le_bytes());
        b.extend_from_slice(k.as_bytes());
        b.extend_from_slice(&8u32.to_le_bytes());
        b.extend_from_slice(&(v.len() as u64).to_le_bytes());
        b.extend_from_slice(v.as_bytes());
    }
    b
}

fn lora(tag: &str) -> Vec<u8> {
    gguf(&[
        ("general.architecture", "qwen2"),
        ("general.type", "adapter"),
        ("adapter.type", "lora"),
        ("general.name", tag),
    ])
}

struct World {
    dir: PathBuf,
    engine: PathBuf,
    home: PathBuf,
    project: PathBuf,
    model_ref: String,
    other_ref: String,
    db: PathBuf,
    store: PathBuf,
}

impl Drop for World {
    fn drop(&mut self) {
        for d in [&self.dir, &self.engine, &self.home, &self.project] {
            let _ = std::fs::remove_dir_all(d);
        }
    }
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
    // A second base model: same shape, other bytes.
    let mut other = std::fs::read(&model).unwrap();
    other.extend_from_slice(b"another base");
    let other_path = engine.join("other.gguf");
    std::fs::write(&other_path, &other).unwrap();

    let dir = temp_dir("data");
    for tag in ["v1", "v2", "v3"] {
        std::fs::write(dir.join(format!("{tag}.gguf")), lora(tag)).unwrap();
    }
    let project = temp_dir("project");
    copy_dir(
        &PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/ai-registry"),
        &project,
    );
    World {
        model_ref: format!("file:{}#sha256={sum}", model.display()),
        other_ref: format!(
            "file:{}#sha256={}",
            other_path.display(),
            sha256_hex(&other)
        ),
        db: dir.join("app.db"),
        store: dir.join("store"),
        home: temp_dir("home"),
        engine,
        dir,
        project,
    }
}

impl World {
    fn env(&self, c: &mut Command) {
        c.current_dir(&self.project)
            .env("SKY_AI_HOME", &self.home)
            .env("SKY_AI_LLAMA_BIN", &self.engine)
            .env("SKY_DB_PATH", &self.db)
            .env(
                "SKY_AI_REGISTRY",
                format!("file://{}", self.store.display()),
            )
            .env("SKY_AI_CHAT_MODEL", &self.model_ref)
            .env("SKY_AI_OTHER_MODEL", &self.other_ref)
            .env("AI_REGISTRY_FIXTURE_DIR", &self.dir)
            .env("AI_REGISTRY_FIXTURE_BASE", &self.model_ref)
            // A cold bundled-program cache: the first `sky ai` verb builds the
            // bundled program, and its progress line must go to stderr so that
            // `--format json` stays one JSON value on stdout.
            .env("XDG_CACHE_HOME", self.home.join("xdg-cache"))
            .env_remove("ENV")
            .env_remove("DATABASE_URL")
            .stdin(Stdio::null());
    }

    fn app(&self, verb: &str) -> String {
        let mut c = Command::new(self.project.join("sky-out/app"));
        c.arg(verb);
        self.env(&mut c);
        let out = c.output().unwrap();
        let text = format!(
            "{}{}",
            String::from_utf8_lossy(&out.stdout),
            String::from_utf8_lossy(&out.stderr)
        );
        assert!(out.status.success(), "app {verb}: {text}");
        text
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
        self.store
            .join("blobs/sha256")
            .join(sha256_hex(&lora(tag)))
            .display()
            .to_string()
    }
}

fn text(out: &Output) -> String {
    format!(
        "{}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    )
}

#[test]
fn registry_promotes_serves_and_rolls_back_through_the_cli() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let w = world();
    let out = w.sky(&["build", "src/Main.sky"]);
    assert!(out.status.success(), "sky build: {}", text(&out));

    // The program registers three versions and promotes v1 with evidence.
    let seeded = w.app("seed");
    assert!(seeded.contains("seed prod=v1"), "{seeded}");

    // The slot serves v1: its engine loaded v1's file from the store, and the
    // engine's base model is the one the adapter is pinned to.
    let served = w.app("serve");
    assert!(
        served.contains("serve answer=fake[support-lora-v1]: hello"),
        "{served}"
    );
    assert!(
        served.contains("serve adapter=registry:support-lora@prod -> support-lora@v1 (sha256:"),
        "{served}"
    );
    assert!(
        w.launches().contains(&format!("--lora {}", w.blob("v1"))),
        "the engine did not load v1: {}",
        w.launches()
    );

    // An engine on another base refuses the adapter, typed.
    let mismatch = w.app("mismatch");
    assert!(
        mismatch.contains("mismatch AdapterMismatch adapter=support-lora@v1"),
        "{mismatch}"
    );

    // lineage, as JSON: the version, its aliases, its evidence.
    let out = w.sky(&["ai", "lineage", "support-lora@prod", "--format", "json"]);
    let lineage = text(&out);
    assert!(out.status.success(), "lineage: {lineage}");
    let json: serde_json::Value =
        serde_json::from_str(String::from_utf8_lossy(&out.stdout).trim()).expect("lineage JSON");
    assert_eq!(json["version"]["number"], 1, "{lineage}");
    assert_eq!(json["version"]["dataset"], "support@3", "{lineage}");
    assert_eq!(json["aliases"], serde_json::json!(["prod"]), "{lineage}");
    assert_eq!(json["reports"][0]["verdict"], "passed", "{lineage}");
    assert_eq!(json["version"]["metadata"]["seed"], "7", "{lineage}");

    // promote v2 on its attached evidence; the slot follows on its next start.
    let out = w.sky(&["ai", "promote", "support-lora", "2", "--to", "prod"]);
    assert!(out.status.success(), "promote: {}", text(&out));
    assert!(
        text(&out).contains("support-lora@prod: promote v1 -> v2"),
        "{}",
        text(&out)
    );
    let served = w.app("serve");
    assert!(served.contains("-> support-lora@v2 (sha256:"), "{served}");
    assert!(
        w.launches().contains(&format!("--lora {}", w.blob("v2"))),
        "the engine did not load v2: {}",
        w.launches()
    );

    // v3 has no evidence: promotion is refused, and the alias does not move.
    let out = w.sky(&["ai", "promote", "support-lora", "v3", "--to", "prod"]);
    assert!(
        !out.status.success(),
        "a version without evidence was promoted"
    );
    assert!(text(&out).contains("NotPromotable"), "{}", text(&out));

    // --expect guards against a stale view of the alias.
    let out = w.sky(&[
        "ai",
        "promote",
        "support-lora",
        "1",
        "--to",
        "prod",
        "--expect",
        "none",
    ]);
    assert!(!out.status.success(), "a stale --expect promoted");
    assert!(text(&out).contains("PromotionConflict"), "{}", text(&out));

    // A forced promotion needs a reason and records it.
    let out = w.sky(&[
        "ai",
        "promote",
        "support-lora",
        "3",
        "--to",
        "qa",
        "--force",
        "--reason",
        "manual check",
        "--format",
        "json",
    ]);
    assert!(out.status.success(), "force: {}", text(&out));
    let ev: serde_json::Value =
        serde_json::from_str(String::from_utf8_lossy(&out.stdout).trim()).unwrap();
    assert_eq!(ev["action"], "force");
    assert_eq!(ev["reason"], "manual check");

    // rollback returns prod to v1, and the slot follows.
    let out = w.sky(&["ai", "rollback", "support-lora@prod"]);
    assert!(out.status.success(), "rollback: {}", text(&out));
    assert!(text(&out).contains("rollback v2 -> v1"), "{}", text(&out));
    let served = w.app("serve");
    assert!(served.contains("-> support-lora@v1 (sha256:"), "{served}");

    // The erasure query lists every version trained on the dataset.
    let digest = sha256_hex(b"support dataset v3");
    let out = w.sky(&["ai", "lineage", "--dataset", &digest]);
    assert!(out.status.success(), "{}", text(&out));
    for v in ["support-lora@v1", "support-lora@v2", "support-lora@v3"] {
        assert!(text(&out).contains(v), "{}", text(&out));
    }

    // The registry's tables are in the app's migration ledger.
    let out = Command::new("sqlite3")
        .arg(&w.db)
        .arg("SELECT name FROM _sky_migrations ORDER BY name")
        .output();
    if let Ok(out) = out {
        if out.status.success() {
            let names = String::from_utf8_lossy(&out.stdout);
            assert!(names.contains("_sky_ai_registry_0001"), "{names}");
            assert!(names.contains("_sky_ai_eval_0001"), "{names}");
        }
    }
}

#[test]
fn registry_verbs_refuse_a_bad_shape() {
    for args in [
        vec!["ai", "promote", "support-lora", "2"],
        vec!["ai", "rollback", "support-lora"],
        vec!["ai", "lineage"],
        vec!["ai", "lineage", "no-alias"],
    ] {
        let out = Command::new(SKY).args(&args).output().unwrap();
        assert_eq!(out.status.code(), Some(2), "{args:?}");
        assert!(
            String::from_utf8_lossy(&out.stderr).contains("usage: sky ai"),
            "{args:?}"
        );
    }
}

// ── GCS and S3, offline ─────────────────────────────────────────────────

const CLOUD_TEST: &str = r#"module StoreCloudTest exposing (tests)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Task as Task
import Sky.Core.Error as Error exposing (Error)
import Sky.Core.String as String
import Sky.Core.File as File
import Sky.Core.Crypto as Crypto
import Sky.Core.System as System
import Sky.Core.Secret as Secret
import Sky.Test as Test exposing (Test)
import Std.Ai.Store as Store


body : String
body =
    "adapter bytes for the cloud"


roundTrip : Store.Store -> Result Error String
roundTrip store =
    File.tempDir "sky-cloud"
        |> Task.andThen
               (\dir ->
                   File.writeFile (dir ++ "/a.safetensors") body
                       |> Task.andThen (\_ -> Store.put store (dir ++ "/a.safetensors"))
                       |> Task.andThen (\blob -> Store.fetch store blob.sha256)
                       |> Task.andThen File.readFile)
        |> Task.run


s3Keys : Store.Store
s3Keys =
    Store.s3 "eu-west-1" "models" "reg"
        |> Store.withEndpoint "https://s3.example.test"
        |> Store.withAwsKeys "AKIDEXAMPLE" (Secret.fromEnv "CLOUD_TEST_SECRET")


s3Role : Store.Store
s3Role =
    Store.s3 "eu-west-1" "rolebucket" "reg"
        |> Store.withEndpoint "https://s3.example.test"


gcsMetadata : Store.Store
gcsMetadata =
    Store.gcs "models" "reg" |> Store.withEndpoint "https://gcs.example.test"


tests : List Test
tests =
    [ Test.test "S3 with keys: put then fetch, verified by digest"
        (\_ -> Test.equal (Ok body) (roundTrip s3Keys))
    , Test.test "S3 with an instance role (IMDSv2)"
        (\_ -> Test.equal (Ok body) (roundTrip s3Role))
    , Test.test "GCS with the metadata server's token"
        (\_ -> Test.equal (Ok body) (roundTrip gcsMetadata))
    ]
"#;

fn mock(method: &str, url_contains: &str, status: u16, body: &str) -> String {
    serde_json::json!({
        "match": { "method": method, "urlContains": url_contains },
        "status": status,
        "body": body,
    })
    .to_string()
}

#[test]
fn gcs_and_s3_stores_work_offline_in_test_mode() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = temp_dir("cloud");
    let home = temp_dir("cloud-home");
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::create_dir_all(dir.join("tests/mocks")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"cloudstore\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n",
    )
    .unwrap();
    std::fs::write(
        dir.join("src/Main.sky"),
        "module Main exposing (main)\n\nimport Sky.Core.Prelude exposing (..)\nimport Std.Log exposing (println)\n\n\nmain =\n    println \"ok\"\n",
    )
    .unwrap();
    std::fs::write(dir.join(".env.test"), "# test mode\n").unwrap();
    std::fs::write(dir.join("tests/StoreCloudTest.sky"), CLOUD_TEST).unwrap();
    let body = "adapter bytes for the cloud";
    let digest = sha256_hex(body.as_bytes());
    let mocks = [
        // S3, path style on the endpoint: a PUT and a GET of the blob.
        mock("PUT", &format!("s3.example.test/models/reg/blobs/sha256/{digest}"), 200, ""),
        mock("GET", &format!("s3.example.test/models/reg/blobs/sha256/{digest}"), 200, body),
        mock("PUT", &format!("s3.example.test/rolebucket/reg/blobs/sha256/{digest}"), 200, ""),
        mock("GET", &format!("s3.example.test/rolebucket/reg/blobs/sha256/{digest}"), 200, body),
        // IMDSv2: a session token, the role's name, its credentials.
        mock("PUT", "169.254.169.254/latest/api/token", 200, "imds-token"),
        mock("GET", "/latest/meta-data/iam/security-credentials/registry-role", 200,
            r#"{"AccessKeyId":"ASIAROLE","SecretAccessKey":"role-secret","Token":"role-session"}"#),
        mock("GET", "/latest/meta-data/iam/security-credentials/", 200, "registry-role\n"),
        // GCS: the metadata server's token, a media upload, a media download.
        mock("GET", "computeMetadata/v1/instance/service-accounts/default/token", 200,
            r#"{"access_token":"gcs-token","expires_in":3599,"token_type":"Bearer"}"#),
        mock("POST", "gcs.example.test/upload/storage/v1/b/models/o?uploadType=media&ifGenerationMatch=0&name=reg%2Fblobs%2Fsha256%2F", 200, "{}"),
        mock("GET", &format!("gcs.example.test/storage/v1/b/models/o/reg%2Fblobs%2Fsha256%2F{digest}?alt=media"), 200, body),
    ];
    for (i, m) in mocks.iter().enumerate() {
        std::fs::write(dir.join(format!("tests/mocks/{i:02}.json")), m).unwrap();
    }
    let out = Command::new(SKY)
        .args(["test", "tests/StoreCloudTest.sky"])
        .current_dir(&dir)
        .env("SKY_AI_HOME", &home)
        .env("CLOUD_TEST_SECRET", "not-a-real-secret")
        .env_remove("AWS_ACCESS_KEY_ID")
        .env_remove("AWS_SECRET_ACCESS_KEY")
        .env_remove("AWS_SESSION_TOKEN")
        .env_remove("AWS_EC2_METADATA_SERVICE_ENDPOINT")
        .env_remove("GOOGLE_APPLICATION_CREDENTIALS")
        .env_remove("GCE_METADATA_HOST")
        .stdin(Stdio::null())
        .output()
        .expect("run sky test");
    let log = text(&out);
    let _ = std::fs::remove_dir_all(&dir);
    let _ = std::fs::remove_dir_all(&home);
    assert!(
        out.status.success(),
        "the offline cloud stores failed:\n{log}"
    );
}

// ── The same registry on PostgreSQL ─────────────────────────────────────

const PG_APP: &str = r#"module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Task as Task
import Sky.Core.Error as Error exposing (Error)
import Sky.Core.String as String
import Sky.Core.List as List
import Sky.Core.Crypto as Crypto
import Sky.Core.Encoding as Encoding
import Sky.Core.Result as Result
import Sky.Core.File as File
import Std.Log exposing (println)
import Std.Db as Db exposing (Db)
import Std.Ai.Provider as Provider exposing (Provider)
import Std.Ai.Local as Local
import Std.Ai.Eval as Eval exposing (Expect(..))
import Std.Ai.Store as Store
import Std.Ai.Failure as Failure exposing (Failure(..))
import Std.Ai.Registry as Registry exposing (Format(..), Method(..))


safetensors : String -> String
safetensors tag =
    let
        header = "{\"t\":\"" ++ tag ++ "\"}"
    in
        (Encoding.hexDecode (String.padLeft 2 '0' (String.fromInt (String.length header)) ++ "00000000000000") |> Result.withDefault "") ++ header


suite : Eval.Suite String
suite =
    Eval.suite "support" (\q -> [ Provider.user q ]) [ { id = "r", input = "refund?", expect = Contains "refund", tags = [] } ]


candidate : Provider
candidate =
    Provider.custom (\_ -> Task.succeed { content = "a refund", usage = { promptTokens = 1, completionTokens = 1, totalTokens = 2 } })


check : String -> Bool -> Task Error ()
check name ok =
    let
        verdict =
            if ok then
                "PASS"

            else
                "FAIL"
    in
        println ("CHECK " ++ name ++ "=" ++ verdict)


register : Registry.Registry -> String -> String -> Task Error Registry.Version
register reg dir tag =
    File.writeFile (dir ++ "/" ++ tag) (safetensors tag)
        |> Task.andThen
               (\_ ->
                   Registry.register reg "pg-lora"
                       { path = dir ++ "/" ++ tag, format = Safetensors, method = Qlora }
                       { base = Local.devModel, dataset = "d@1", datasetDigest = Crypto.sha256 "d1", metadata = [] })


evidence : Db -> Registry.Registry -> Registry.Version -> Task Error Eval.Passed
evidence db reg v =
    Eval.runAs (Registry.label v) db candidate suite
        |> Task.andThen (\r -> Registry.attachReport reg v r |> Task.andThen (\_ -> Task.fromResult (Eval.check Eval.thresholds r)))


isOk : Result Error a -> Bool
isOk r =
    case r of

        Ok _ ->
            True

        Err _ ->
            False


isNotRegistered : Result Error a -> Bool
isNotRegistered r =
    case r of

        Err e ->
            case Failure.fromError e of

                Just (NotRegistered _) ->
                    True

                _ ->
                    False

        Ok _ ->
            False


attempt : Task Error a -> Task Error (Result Error a)
attempt t =
    t |> Task.map Ok |> Task.onError (\e -> Task.succeed (Err e))


scenario : Db -> Registry.Registry -> String -> Task Error ()
scenario db reg dir =
    Task.sequence [ register reg dir "a", register reg dir "b", register reg dir "c" ]
        |> Task.andThen
               (\vs ->
                   case vs of

                       [v1, v2, v3] ->
                           check "numbered" (List.map .number vs == [ 1, 2, 3 ])
                               |> Task.andThen (\_ -> evidence db reg v1)
                               |> Task.andThen (\p -> Registry.promote reg "prod" v1 p)
                               |> Task.andThen (\_ -> Task.map2 (\a b -> ( a, b )) (evidence db reg v2) (evidence db reg v3))
                               |> Task.andThen
                                      (\( p2, p3 ) ->
                                          Task.parallel
                                              [ attempt (Registry.promoteIf reg "prod" (Just v1) v2 p2)
                                              , attempt (Registry.promoteIf reg "prod" (Just v1) v3 p3)
                                              ])
                               |> Task.andThen
                                      (\outcomes ->
                                          check "race_one_winner" (List.length (List.filter isOk outcomes) == 1))
                               |> Task.andThen (\_ -> Registry.rollback reg "pg-lora@prod")
                               |> Task.andThen (\ev -> check "rollback_to_v1" (ev.to == 1))
                               |> Task.andThen (\_ -> Registry.lineage reg "pg-lora@prod")
                               |> Task.andThen (\l -> check "lineage" (l.version.number == 1 && l.aliases == [ "prod" ] && List.length l.reports == 1))
                               |> Task.andThen (\_ -> Registry.trainedOn reg (Crypto.sha256 "d1"))
                               |> Task.andThen (\t -> check "trained_on" (List.length t == 3))
                               |> Task.andThen (\_ -> attempt (Registry.resolve reg "pg-lora@nope"))
                               |> Task.andThen
                                      (\r -> check "not_registered" (isNotRegistered r))

                       _ ->
                           Task.fail (Error.unexpected "three versions"))


main : Task Error ()
main =
    Db.connect ()
        |> Task.andThen
               (\db ->
                   File.tempDir "pg-registry"
                       |> Task.andThen
                              (\dir ->
                                  Registry.open db (Store.local (dir ++ "/store"))
                                      |> Task.andThen (\reg -> Registry.open db (Store.local (dir ++ "/store")))
                                      |> Task.andThen (\reg -> scenario db reg dir)))
        |> Task.andThen (\_ -> println "ALL-REGISTRY-CHECKS-DONE")
"#;

/// A PostgreSQL `bin` directory holding `initdb` + `pg_ctl` + `postgres`.
fn find_pg_bin() -> Option<PathBuf> {
    let complete = |d: &Path| {
        ["initdb", "pg_ctl", "postgres"]
            .iter()
            .all(|b| d.join(b).is_file())
    };
    if let Ok(v) = std::env::var("SKY_POSTGRES_BIN") {
        let d = PathBuf::from(v);
        if complete(&d) {
            return Some(d);
        }
    }
    if let Ok(path) = std::env::var("PATH") {
        for d in std::env::split_paths(&path) {
            if complete(&d) {
                return Some(d);
            }
        }
    }
    let mut roots: Vec<PathBuf> = Vec::new();
    for prefix in ["/opt/homebrew/opt", "/usr/local/opt"] {
        if let Ok(rd) = std::fs::read_dir(prefix) {
            for e in rd.filter_map(Result::ok) {
                if e.file_name().to_string_lossy().starts_with("postgresql") {
                    roots.push(e.path().join("bin"));
                }
            }
        }
    }
    for v in (9..=20).rev() {
        roots.push(PathBuf::from(format!("/usr/lib/postgresql/{v}/bin")));
    }
    roots.sort();
    roots.reverse();
    roots.into_iter().find(|d| complete(d))
}

const RUN_LIMIT: Duration = Duration::from_secs(600);

// Heavy real-DB e2e leg (a real `go build` + a PostgreSQL cluster driven by
// `sky run`), run nightly and by the release gate via `--ignored`, as the other
// PostgreSQL legs are. The SQLite leg above and tests/Std/Ai/RegistryTest.sky
// run per commit.
#[cfg(unix)]
#[test]
#[ignore = "heavy real-DB e2e leg; runs nightly and in the release gate (--ignored)"]
fn the_registry_runs_unchanged_on_postgres() {
    let Some(pg_bin) = find_pg_bin().filter(|_| have_go()) else {
        required(Need::Go, have_go());
        required(Need::Postgres, false);
        return;
    };
    let project = temp_dir("pg");
    let sky_home = temp_dir("pg-home");
    std::fs::create_dir_all(project.join("src")).unwrap();
    std::fs::write(
        project.join("sky.toml"),
        "name = \"pgregistry\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[database]\nembedded = true\n",
    )
    .unwrap();
    std::fs::write(project.join("src/Main.sky"), PG_APP).unwrap();
    let out_path = project.join("run.out");
    // `sky run` and the cluster it supervises end with the test on every
    // path, a panic included.
    let mut child = child_group::ChildGroup::spawn(
        Command::new(SKY)
            .args(["run", "src/Main.sky"])
            .current_dir(&project)
            .env("SKY_HOME", &sky_home)
            .env("SKY_POSTGRES_BIN", &pg_bin)
            .env_remove("SKY_DB_PATH")
            .env_remove("DATABASE_URL")
            .env_remove("XDG_RUNTIME_DIR")
            .stdin(Stdio::null())
            .stdout(Stdio::from(std::fs::File::create(&out_path).unwrap()))
            .stderr(Stdio::from(
                std::fs::File::create(project.join("run.err")).unwrap(),
            )),
    )
    .expect("spawn sky run");
    let deadline = Instant::now() + RUN_LIMIT;
    let status = loop {
        match child.child().try_wait().unwrap() {
            Some(s) => break s,
            None if Instant::now() >= deadline => {
                child.kill();
                panic!("sky run did not finish within {RUN_LIMIT:?}");
            }
            None => std::thread::sleep(Duration::from_millis(200)),
        }
    };
    let log = format!(
        "{}{}",
        std::fs::read_to_string(&out_path).unwrap_or_default(),
        std::fs::read_to_string(project.join("run.err")).unwrap_or_default()
    );
    let _ = Command::new(SKY)
        .args(["db", "stop"])
        .current_dir(&project)
        .env("SKY_HOME", &sky_home)
        .env("SKY_POSTGRES_BIN", &pg_bin)
        .output();
    let _ = std::fs::remove_dir_all(&project);
    let _ = std::fs::remove_dir_all(&sky_home);
    assert!(status.success(), "sky run on PostgreSQL failed:\n{log}");
    assert!(
        !log.contains("=FAIL"),
        "a registry check failed on PostgreSQL:\n{log}"
    );
    for check in [
        "numbered",
        "race_one_winner",
        "rollback_to_v1",
        "lineage",
        "trained_on",
        "not_registered",
    ] {
        assert!(
            log.contains(&format!("CHECK {check}=PASS")),
            "{check} missing:\n{log}"
        );
    }
    assert!(log.contains("ALL-REGISTRY-CHECKS-DONE"), "{log}");
}

/// The registry example (`examples/71-ai-registry`): its program runs the
/// lifecycle with no model and no network, and its `sky test` (test mode)
/// serves the promoted adapter through a model slot, answered by its fixture.
#[test]
fn the_registry_example_runs_and_serves_offline() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let project = temp_dir("example");
    copy_dir(&repo_root().join("examples/71-ai-registry"), &project);
    let home = temp_dir("example-home");
    let run = |args: &[&str]| {
        Command::new(SKY)
            .args(args)
            .current_dir(&project)
            .env("SKY_AI_HOME", &home)
            .env("SKY_AI_FETCH", "off")
            .env_remove("SKY_AI_REGISTRY")
            .env_remove("SKY_AI_CHAT_URL")
            .env_remove("SKY_AI_CHAT_MODEL")
            .env_remove("SKY_AI_CHAT_ADAPTER")
            .env_remove("ENV")
            .stdin(Stdio::null())
            .output()
            .unwrap()
    };
    let out = run(&["run", "src/Main.sky"]);
    let log = text(&out);
    assert!(out.status.success(), "the example program: {log}");
    for want in [
        "registered support-lora@v1",
        "registered support-lora@v2",
        "evaluated support-lora@v1  2 of 2 passed",
        "evaluated support-lora@v2  1 of 2 passed",
        "promoted support-lora@v1 to prod (promote)",
        "support-lora@v2 to prod: refused by the gate: pass rate 50% is below 100%",
        "qa rolled back: v2 -> v1",
        "lineage of support-lora@prod: support-lora@v1",
    ] {
        assert!(log.contains(want), "missing {want:?} in:\n{log}");
    }
    let out = run(&["test", "tests/RegistryExampleTest.sky"]);
    let log = text(&out);
    let engines = std::fs::read_dir(home.join("run"))
        .map(|d| d.count())
        .unwrap_or(0);
    let _ = std::fs::remove_dir_all(&project);
    let _ = std::fs::remove_dir_all(&home);
    assert!(out.status.success(), "the example's test: {log}");
    assert_eq!(engines, 0, "test mode started an engine");
}
