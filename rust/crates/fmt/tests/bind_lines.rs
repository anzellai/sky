//! `sky fmt` on bind lines (`p <- e`, docs/design/let-bind-lines.md §7).
//! Each case states the exact opinionated output, so a silent fallback to
//! the lossless reprint (the safety net) fails the test.

use fmt::format_source;

fn assert_formats(src: &str, want: &str) {
    let once = format_source(src);
    assert_eq!(once, want, "format of {src:?}");
    assert_eq!(format_source(&once), once, "not idempotent for {src:?}");
}

const HEAD: &str = "module Main exposing (main)\n\n\n";

#[test]
fn simple_bind_lines_render_with_larrow() {
    let src = format!(
        "{HEAD}main =\n    let\n        state   <-   Sync.get ref\n        n = state + 1\n        _ <- Sync.set n ref\n    in\n        Task.succeed n\n"
    );
    let want = format!(
        "{HEAD}main =\n    let\n        state <- Sync.get ref\n        n = state + 1\n        _ <- Sync.set n ref\n    in\n        Task.succeed n\n"
    );
    assert_formats(&src, &want);
}

#[test]
fn tuple_and_record_patterns() {
    let src = format!(
        "{HEAD}main =\n    let\n        (a,b) <- pair\n        {{x,y}} <- point\n    in\n        Task.succeed (a + x)\n"
    );
    let want = format!(
        "{HEAD}main =\n    let\n        ( a, b ) <- pair\n        {{ x, y }} <- point\n    in\n        Task.succeed (a + x)\n"
    );
    assert_formats(&src, &want);
}

#[test]
fn comments_between_lines_stay() {
    let src = format!(
        "{HEAD}main =\n    let\n        -- read it\n        a <- get\n        -- then write\n        _ <- put   a\n    in\n        Task.succeed a\n"
    );
    let want = src.replace("put   a", "put a");
    assert_formats(&src, &want);
}

#[test]
fn multi_line_expression_breaks_after_larrow() {
    // A short pipeline joins one line, as after `=`. A long one keeps its
    // break after `<-`.
    let src = format!(
        "{HEAD}main =\n    let\n        user <-\n            fetch   id\n                |> Task.map .user\n    in\n    Task.succeed user\n"
    );
    let want = format!(
        "{HEAD}main =\n    let\n        user <- fetch id |> Task.map .user\n    in\n        Task.succeed user\n"
    );
    assert_formats(&src, &want);
    let src = format!(
        "{HEAD}main =\n    let\n        user <-\n            fetchTheUserRecordFromTheServer identifier\n                |> Task.map .userRecordField\n    in\n        Task.succeed user\n"
    );
    assert_formats(&src, &src);
}

#[test]
fn long_expression_moves_below_the_pattern() {
    let src = format!(
        "{HEAD}main =\n    let\n        result <- someVeryLongFunctionName argumentNumberOne argumentNumberTwo argumentThree\n    in\n        Task.succeed result\n"
    );
    let want = format!(
        "{HEAD}main =\n    let\n        result <-\n            someVeryLongFunctionName\n                argumentNumberOne\n                argumentNumberTwo\n                argumentThree\n    in\n        Task.succeed result\n"
    );
    assert_formats(&src, &want);
}

#[test]
fn comment_between_larrow_and_body_stays_above_body() {
    let src = format!(
        "{HEAD}main =\n    let\n        a <-\n            -- the fetch\n            get  x\n    in\n        Task.succeed a\n"
    );
    let want = src.replace("get  x", "get x");
    assert_formats(&src, &want);
}
