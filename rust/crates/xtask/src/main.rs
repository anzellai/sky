//! `xtask` — dev automation: run corpus, differential-test vs the Haskell
//! oracle, reproducibility gate (doc 02, docs 11, 12). Standalone; orchestrates
//! the workspace + the stage-0 Haskell compiler from the outside.
//!
//! M0 stub: subcommand skeleton only. `xtask diff` (shell both compilers over
//! the shared corpus, compare verdict + emitted-Go) and `xtask repro` (byte-diff
//! across seeds/platforms) are wired in M0-exit / M7 respectively (doc 12).

const VERSION: &str = "xtask (rust bring-up) v0.0.0-m0";

fn main() {
    let args: Vec<String> = std::env::args().skip(1).collect();
    match args.first().map(String::as_str) {
        Some("--version") | Some("version") => println!("{VERSION}"),
        Some("diff") => {
            println!("xtask diff: (M0 stub) will shell stage-0 + rust over the corpus");
        }
        Some("repro") => {
            println!("xtask repro: (M0 stub) will byte-diff the corpus across seeds");
        }
        _ => {
            println!("{VERSION}");
            println!("usage: xtask <diff|repro> [args]");
        }
    }
}
