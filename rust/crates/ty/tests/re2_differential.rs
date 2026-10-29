//! `ty::re2_syntax` against Go's own `regexp.Compile`.
//!
//! The compile-time check of a literal `Regex.fromString` pattern (`[E2014]`)
//! is a port of Go's `regexp/syntax` validation. It must never refuse a pattern
//! Go accepts (that would reject a working program), and when it refuses one
//! its message must be Go's. This test builds a tiny Go program, feeds it a
//! table of hand-picked patterns plus a few thousand generated ones, and
//! compares every verdict. Needs a `go` toolchain.

use std::io::Write;
use std::process::{Command, Stdio};

#[path = "../../sky/src/live_gate.rs"]
mod live_gate;
use live_gate::{required, Need};

fn have_go() -> bool {
    Command::new("go")
        .arg("version")
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .status()
        .is_ok_and(|s| s.success())
}

const GO_MAIN: &str = r#"package main

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strconv"
)

func main() {
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	w := bufio.NewWriter(os.Stdout)
	defer w.Flush()
	for sc.Scan() {
		p, err := strconv.Unquote(sc.Text())
		if err != nil {
			fmt.Fprintln(w, "BADLINE")
			continue
		}
		if _, err := regexp.Compile(p); err != nil {
			fmt.Fprintln(w, strconv.Quote(err.Error()))
		} else {
			fmt.Fprintln(w, "OK")
		}
	}
}
"#;

/// A Go-syntax quoted string (what `strconv.Unquote` reads).
fn go_quote(s: &str) -> String {
    let mut out = String::from("\"");
    for c in s.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\n' => out.push_str("\\n"),
            c if (c as u32) < 0x20 => out.push_str(&format!("\\x{:02x}", c as u32)),
            c => out.push(c),
        }
    }
    out.push('"');
    out
}

/// Go's verdict for each pattern: `None` for OK, else its error text.
fn go_verdicts(patterns: &[String]) -> Vec<Option<String>> {
    let dir = std::env::temp_dir().join(format!(
        "sky-re2-differential-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    std::fs::create_dir_all(&dir).unwrap();
    std::fs::write(dir.join("go.mod"), "module re2go\n\ngo 1.22\n").unwrap();
    std::fs::write(dir.join("main.go"), GO_MAIN).unwrap();
    let bin = dir.join("re2go");
    let built = Command::new("go")
        .args(["build", "-o"])
        .arg(&bin)
        .arg(".")
        .current_dir(&dir)
        .output()
        .expect("spawn go build");
    assert!(
        built.status.success(),
        "go build: {}",
        String::from_utf8_lossy(&built.stderr)
    );
    let mut child = Command::new(&bin)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .spawn()
        .expect("run the Go helper");
    {
        let mut stdin = child.stdin.take().unwrap();
        for p in patterns {
            writeln!(stdin, "{}", go_quote(p)).unwrap();
        }
    }
    let out = child.wait_with_output().unwrap();
    let _ = std::fs::remove_dir_all(&dir);
    let text = String::from_utf8(out.stdout).unwrap();
    let lines: Vec<&str> = text.lines().collect();
    assert_eq!(lines.len(), patterns.len(), "one verdict per pattern");
    lines
        .iter()
        .map(|l| {
            assert_ne!(*l, "BADLINE", "the Go helper could not read a pattern");
            if *l == "OK" {
                None
            } else {
                Some(unquote(l))
            }
        })
        .collect()
}

/// Undo `strconv.Quote` for the texts Go prints (ASCII escapes only).
fn unquote(l: &str) -> String {
    let inner = &l[1..l.len() - 1];
    let mut out = String::new();
    let mut it = inner.chars();
    while let Some(c) = it.next() {
        if c != '\\' {
            out.push(c);
            continue;
        }
        match it.next() {
            Some('\\') => out.push('\\'),
            Some('"') => out.push('"'),
            Some('n') => out.push('\n'),
            Some('t') => out.push('\t'),
            Some('x') => {
                let h: String = it.by_ref().take(2).collect();
                out.push(u8::from_str_radix(&h, 16).unwrap() as char);
            }
            Some(o) => {
                out.push('\\');
                out.push(o);
            }
            None => out.push('\\'),
        }
    }
    out
}

/// Hand-picked patterns: every branch of the parser, valid and invalid.
const TABLE: &[&str] = &[
    r"\pX",
    r"\pZ",
    r"\p^L",
    "",
    "a",
    r"\s+",
    r"\b\w+\b",
    r"\d{2,4}",
    r"\d{2}",
    r"\d{2,}",
    "a{",
    "a{,3}",
    "{",
    "}",
    "x{01}",
    "a{1,",
    "a{1,2",
    "a*?",
    "a{2}?",
    "(?i)abc",
    "(?i:a)b",
    "(?-s:.)",
    "(?i-s)",
    "(?P<year>\\d+)",
    "(?<year>\\d+)",
    "(?P<1a>x)",
    "(?P<a b>x)",
    "(?P<a",
    "(?P=a)",
    "[a-z0-9_]",
    "[]a]",
    "[^]a]",
    "[^]",
    "[]",
    "[a-]",
    "[-a]",
    "[[:alpha:]]",
    "[[:^space:]]",
    "[[:foo:]]",
    "[[:alpha:]",
    r"\pL\p{Greek}\PN\p{^Han}",
    r"\p{",
    r"\p",
    r"\P{^}",
    r"[\d\s\pL]",
    r"\x41\x{1F600}\101\0",
    r"\x{}",
    r"\x{zz}",
    r"\x{110000}",
    r"\xg1",
    r"\Qa.b*\E+",
    r"\Qunclosed.",
    r"\A^$\z",
    r"\Z",
    r"\G",
    r"\e",
    r"\cA",
    "^*",
    "$+",
    "()*",
    "a|b|",
    "|",
    "|*",
    r"\.\-\_\/\#\ \:",
    r"[\-\]]",
    r"[\b]",
    r"[\B]",
    r"[\Q]",
    "(a{10}){10}",
    "(a{100}){11}",
    "((a{10})*){10}",
    "((a{100})*){11}",
    "(?)",
    "(?-)",
    "(?--i)",
    "(?i",
    "(?",
    "(",
    ")",
    "a)",
    "(a",
    "((a)",
    "(a))",
    "*",
    "+",
    "?",
    "a**",
    "a+*",
    "a*??",
    "a{2}{3}",
    "a{2}*",
    "{2}",
    "a{3,2}",
    "a{1001}",
    "a{1000}",
    "a{0,1000}",
    "a{99999999999}",
    "a{2,99999999999}",
    "\\",
    "a\\",
    "[a\\",
    r"\1",
    r"\12",
    r"\8",
    r"\9",
    r"\C",
    "[z-a]",
    "[a-\\d]",
    "[\\d-z]",
    "(?=a)",
    "(?!a)",
    "(?<=a)",
    "(?<!a)",
    "(?P<>a)",
    r"\p{}",
    "a|*",
    "(*)",
    "(?:*)",
    "(?i)*",
    "a(?i)*",
    "x{2}x{3}",
    ".*",
    "[.]",
    r"\ü",
    "ü+",
    "[ü-a]",
    "[a-ü]",
    "(?s).",
    "(?U)a*",
    "(?m)^a$",
];

/// Tokens the generated patterns are built from: every construct that
/// changes the parse, with a bias towards the error edges.
const TOKENS: &[&str] = &[
    "a",
    "b",
    "ü",
    ".",
    "^",
    "$",
    "|",
    "(",
    ")",
    "(?:",
    "(?i)",
    "(?P<n>",
    "(?<m>",
    "(?=",
    "*",
    "+",
    "?",
    "*?",
    "{2}",
    "{2,}",
    "{2,5}",
    "{5,2}",
    "{",
    "}",
    "{,1}",
    "{01}",
    "[",
    "]",
    "[^",
    "-",
    "[:alpha:]",
    "[:bad:]",
    r"\d",
    r"\w",
    r"\s",
    r"\b",
    r"\B",
    r"\A",
    r"\z",
    r"\pL",
    r"\p{Greek}",
    r"\x41",
    r"\x{263a}",
    r"\0",
    r"\7",
    r"\8",
    r"\Q",
    r"\E",
    r"\.",
    r"\-",
    r"\q",
    r"\C",
    "\\",
];

/// A deterministic stream of generated patterns (xorshift, fixed seed).
fn generated(n: usize) -> Vec<String> {
    let mut state: u64 = 0x5eed_1234_9abc_def0;
    let mut next = || {
        state ^= state << 13;
        state ^= state >> 7;
        state ^= state << 17;
        state
    };
    (0..n)
        .map(|_| {
            let len = 1 + (next() % 7) as usize;
            (0..len)
                .map(|_| TOKENS[(next() % TOKENS.len() as u64) as usize])
                .collect::<String>()
        })
        .collect()
}

#[test]
fn the_rust_re2_check_agrees_with_go() {
    if !required(Need::Go, have_go()) {
        return;
    }
    let mut patterns: Vec<String> = TABLE.iter().map(|s| s.to_string()).collect();
    patterns.extend(generated(4000));
    let go = go_verdicts(&patterns);
    let mut disagreements = Vec::new();
    let mut go_only = Vec::new();
    for (p, g) in patterns.iter().zip(&go) {
        let ours = ty::re2_syntax::validate(p).err().map(|e| e.message());
        match (&ours, g) {
            (None, None) => {}
            (Some(a), Some(b)) if a == b => {}
            // Go refuses a pattern the Rust check accepts: safe (the run-time
            // Result still refuses it), but only for the documented cases.
            (None, Some(b)) => go_only.push(format!("{p:?}: Go {b:?}")),
            _ => disagreements.push(format!("{p:?}: Rust {ours:?}, Go {g:?}")),
        }
    }
    assert!(
        disagreements.is_empty(),
        "{} pattern(s) where the Rust check and Go disagree:\n{}",
        disagreements.len(),
        disagreements.join("\n")
    );
    // The documented gaps (an unknown Unicode class name in braces, the size
    // limits) are not in these tables, so nothing may land here.
    assert!(
        go_only.is_empty(),
        "{} pattern(s) Go refuses and the Rust check accepts:\n{}",
        go_only.len(),
        go_only.join("\n")
    );
}
