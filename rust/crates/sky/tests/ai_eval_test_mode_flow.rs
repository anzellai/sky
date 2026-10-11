//! `Std.Ai.Eval` is mock-by-default under `sky test` in test mode (a project
//! with a committed `.env.test`), like every `Std.Ai.Endpoint` slot: the
//! default judge is the local `"judge"` slot, and its calls are answered by a
//! `tests/mocks/` fixture. No engine starts and nothing is downloaded: the AI
//! home is an empty directory with fetching off, so a call that reached for an
//! engine would fail with `NotProvisioned` and the suite would go red.

use std::path::PathBuf;
use std::process::Command;

#[path = "../src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

const SKY: &str = env!("CARGO_BIN_EXE_sky");

fn have_go() -> bool {
    Command::new("go")
        .arg("version")
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .status()
        .map(|s| s.success())
        .unwrap_or(false)
}

fn scratch(tag: &str) -> PathBuf {
    let dir = std::env::temp_dir().join(format!(
        "sky-ai-eval-{tag}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    std::fs::create_dir_all(&dir).unwrap();
    dir
}

const SUITE: &str = r#"module EvalMockTest exposing (tests)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Task as Task
import Sky.Core.Error as Error exposing (Error)
import Sky.Core.String as String
import Sky.Core.List as List
import Sky.Core.Maybe as Maybe
import Sky.Test as Test exposing (Test)
import Std.Db as Db
import Std.Ai.Provider as Provider
import Std.Ai.Eval as Eval exposing (Expect(..))


candidate : Provider.Provider
candidate =
    Provider.custom (\_ -> Task.succeed { content = "Happy to help.", usage = { promptTokens = 1, completionTokens = 1, totalTokens = 2 } })


tone : Eval.Suite String
tone =
    Eval.suite "tone" (\q -> [ Provider.user q ]) [ { id = "polite", input = "You are useless.", expect = Rubric "Stays polite.", tags = [] } ]


reason : Result Error String
reason =
    Db.open "sqlite" ":memory:"
        |> Task.andThen (\db -> Eval.run db candidate tone)
        |> Task.map (\r -> List.map (\c -> (if c.passed then "pass: " else "fail: ") ++ c.reason) r.cases |> String.join ";")
        |> Task.run


tests : List Test
tests =
    [ Test.test "the default judge is answered by the fixture"
        (\_ ->
            case reason of
                Ok s ->
                    if String.contains "pass: judge 5/5: SCORE: 5 - the fixture judge" s then
                        Test.pass

                    else
                        Test.fail s

                Err e ->
                    Test.fail (Error.toString e)
        )
    ]
"#;

const FIXTURE: &str = r#"{
  "match": { "method": "POST", "urlContains": "sky-ai-local.invalid/judge/" },
  "status": 200,
  "body": "{\"choices\":[{\"message\":{\"content\":\"SCORE: 5 - the fixture judge\"}}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":8,\"total_tokens\":18}}"
}
"#;

#[test]
fn the_default_judge_is_a_fixture_under_sky_test() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let dir = scratch("project");
    let home = scratch("home");
    std::fs::create_dir_all(dir.join("src")).unwrap();
    std::fs::create_dir_all(dir.join("tests/mocks")).unwrap();
    std::fs::write(
        dir.join("sky.toml"),
        "name = \"evalmock\"\nversion = \"0.1.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n",
    )
    .unwrap();
    std::fs::write(
        dir.join("src/Main.sky"),
        "module Main exposing (main)\n\nimport Sky.Core.Prelude exposing (..)\nimport Std.Log exposing (println)\n\n\nmain =\n    println \"ok\"\n",
    )
    .unwrap();
    std::fs::write(dir.join(".env.test"), "# test mode\n").unwrap();
    std::fs::write(dir.join("tests/EvalMockTest.sky"), SUITE).unwrap();
    std::fs::write(dir.join("tests/mocks/eval-judge.json"), FIXTURE).unwrap();

    let out = Command::new(SKY)
        .args(["test", "tests/EvalMockTest.sky"])
        .current_dir(&dir)
        .env("SKY_AI_HOME", &home)
        .env("SKY_AI_FETCH", "off")
        .env_remove("SKY_AI_LLAMA_BIN")
        .stdin(std::process::Stdio::null())
        .output()
        .expect("run sky test");
    let text = format!(
        "{}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
    let engines = std::fs::read_dir(&home).map(|d| d.count()).unwrap_or(0);
    let _ = std::fs::remove_dir_all(&dir);
    let _ = std::fs::remove_dir_all(&home);
    assert!(
        out.status.success(),
        "the suite must pass on the fixture judge:\n{text}"
    );
    assert_eq!(
        engines, 0,
        "the AI home must stay empty: no engine, no model fetch"
    );
}
